// Copyright 2026 SoC Monitoring
//
//	Licensed under the Apache License, Version 2.0 (the "License");
//	you may not use this file except in compliance with the License.
//	You may obtain a copy of the License at
//
//	    http://www.apache.org/licenses/LICENSE-2.0
//
//	Unless required by applicable law or agreed to in writing, software
//	distributed under the License is distributed on an "AS IS" BASIS,
//	WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//	See the License for the specific language governing permissions and
//	limitations under the License.

// BUG-433 diagnosability: every download break logs one structured line
// (error class, TLS parameters, Go runtime knobs, kernel TCP/NIC counter
// deltas) so the H1/H2 hypotheses can be told apart from field logs.
package client

import (
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassifyDownloadBreak(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"go crypto/tls local alert",
			&net.OpError{Op: "local error", Err: errors.New("tls: bad record MAC")},
			breakClassTLSBadRecordMAC},
		{"openssl binding (field journal line)",
			errors.New("SSL errors: Provider routines::cipher operation failed / " +
				"SSL routines::decryption failed or bad record mac / record layer failure"),
			breakClassTLSBadRecordMAC},
		{"openssl record layer without mac wording",
			errors.New("SSL routines::record layer failure"), breakClassTLSRecordLayer},
		{"record header", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"},
			breakClassTLSRecordHeader},
		{"remote alert", errors.New("remote error: tls: internal error"), breakClassTLSRemoteAlert},
		{"unexpected eof", fmt.Errorf("read body: %w", io.ErrUnexpectedEOF), breakClassUnexpectedEOF},
		{"short eof", io.EOF, breakClassUnexpectedEOF},
		{"reset", &net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNRESET)},
			breakClassConnReset},
		{"timeout", &net.OpError{Op: "read", Err: timeoutErr{}}, breakClassTimeout},
		{"other", errors.New("something else"), breakClassOther},
		{"nil", nil, breakClassOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, classifyDownloadBreak(c.err))
		})
	}
}

const fixtureSNMP = `Ip: Forwarding DefaultTTL
Ip: 1 64
Tcp: RtoAlgorithm RtoMin RtoMax MaxConn ActiveOpens PassiveOpens AttemptFails EstabResets CurrEstab InSegs OutSegs RetransSegs InErrs OutRsts InCsumErrors
Tcp: 1 200 120000 -1 10 0 0 %d 1 1000 900 %d %d 0 %d
Udp: InDatagrams NoPorts
Udp: 0 0
`

const fixtureDev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 500 5 0 0 0 0 0 0 500 5 0 0 0 0 0 0
  end0: %d %d %d %d 0 0 0 0 100 1 0 0 0 0 0 0
 wlan0: 77 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0
`

func writeProcFixture(t *testing.T, root string, estabResets, retrans, inErrs, csum,
	rxBytes, rxPkts, rxErrs, rxDrop int64) {
	require.NoError(t, os.MkdirAll(filepath.Join(root, "net"), 0o755))
	require.NoError(t, ioutil.WriteFile(filepath.Join(root, "net", "snmp"),
		[]byte(fmt.Sprintf(fixtureSNMP, estabResets, retrans, inErrs, csum)), 0o644))
	require.NoError(t, ioutil.WriteFile(filepath.Join(root, "net", "dev"),
		[]byte(fmt.Sprintf(fixtureDev, rxBytes, rxPkts, rxErrs, rxDrop)), 0o644))
}

func withDiagRoots(t *testing.T, proc, sys string) {
	oldP, oldS := diagProcRoot, diagSysClassNet
	diagProcRoot, diagSysClassNet = proc, sys
	t.Cleanup(func() { diagProcRoot, diagSysClassNet = oldP, oldS })
}

func TestReadNetCountersParsesProcFiles(t *testing.T) {
	root := t.TempDir()
	writeProcFixture(t, root, 2, 15, 0, 3, 1000, 10, 1, 4)
	withDiagRoots(t, root, t.TempDir())

	c := readNetCounters()
	assert.Equal(t, int64(3), c.tcp["InCsumErrors"])
	assert.Equal(t, int64(15), c.tcp["RetransSegs"])
	assert.Equal(t, int64(2), c.tcp["EstabResets"])
	_, hasLo := c.ifaces["lo"]
	assert.False(t, hasLo, "loopback must be skipped")
	assert.Equal(t, ifaceCounters{rxBytes: 1000, rxPackets: 10, rxErrs: 1, rxDrop: 4}, c.ifaces["end0"])
	assert.Contains(t, c.ifaces, "wlan0")
}

func TestReadNetCountersMissingFilesIsHarmless(t *testing.T) {
	withDiagRoots(t, filepath.Join(t.TempDir(), "nope"), t.TempDir())
	c := readNetCounters()
	assert.Empty(t, c.tcp)
	assert.Empty(t, c.ifaces)
	line := formatDownloadBreakDiag(errors.New("x"), 1, 2, 1, time.Second, "proto=? tls=unavailable",
		"", 4, c, c, func(string) string { return "?" })
	assert.Contains(t, line, "InCsumErrors:?")
}

func TestFormatDownloadBreakDiagDeltasAndInterfaceSelection(t *testing.T) {
	root := t.TempDir()
	sys := t.TempDir()
	withDiagRoots(t, root, sys)
	// end0 bound to r8152 via the sysfs driver symlink.
	require.NoError(t, os.MkdirAll(filepath.Join(sys, "end0", "device"), 0o755))
	require.NoError(t, os.Symlink("../../../bus/usb/drivers/r8152",
		filepath.Join(sys, "end0", "device", "driver")))

	writeProcFixture(t, root, 2, 15, 0, 3, 1000, 10, 1, 4)
	before := readNetCounters()
	writeProcFixture(t, root, 2, 40, 1, 5, 9000, 90, 1, 6)
	after := readNetCounters()

	cs := &tls.ConnectionState{Version: tls.VersionTLS13,
		CipherSuite: tls.TLS_AES_128_GCM_SHA256, NegotiatedProtocol: "h2"}
	line := formatDownloadBreakDiag(
		&net.OpError{Op: "local error", Err: errors.New("tls: bad record MAC")},
		26_000_000, 118_000_000, 25_000_000, 7*time.Second,
		describeTLS(cs, "HTTP/2.0"), "asyncpreemptoff=1", 1, before, after, ifaceDriver)

	for _, want := range []string{
		"Download break diagnostics (BUG-433): class=tls-bad-record-mac",
		"offset=26000000/118000000", "conn_bytes=25000000", "conn_age=7s",
		"proto=HTTP/2.0 tls=1.3 cipher=TLS_AES_128_GCM_SHA256 alpn=h2 resumed=false",
		`gomaxprocs=1 godebug="asyncpreemptoff=1"`,
		"tcp_delta={InCsumErrors:+2 InErrs:+1 RetransSegs:+25 EstabResets:+0}",
		"ifaces={end0(r8152):rx_bytes+8000,rx_errs+0,rx_drop+2,rx_fifo+0,rx_frame+0}",
	} {
		assert.Contains(t, line, want)
	}
	// wlan0 carried no RX in the window, so it is left out.
	assert.NotContains(t, line, "wlan0")
}

func TestDescribeTLSWithoutState(t *testing.T) {
	assert.Equal(t, "proto=HTTP/1.1 tls=unavailable", describeTLS(nil, "HTTP/1.1"))
}

// TestUpdateResumerLogsBreakDiagnostics drives a real Go crypto/tls
// download that breaks mid-stream and resumes, and checks that exactly one
// diagnostics line is logged, carrying the TLS parameters of the
// connection that broke.
func TestUpdateResumerLogsBreakDiagnostics(t *testing.T) {
	oldSmallestUnit := ExponentialBackoffSmallestUnit
	ExponentialBackoffSmallestUnit = 10 * time.Millisecond
	defer func() { ExponentialBackoffSmallestUnit = oldSmallestUnit }()

	hook := logtest.NewGlobal()
	defer hook.Reset()

	data := make([]byte, 1<<20)
	_, err := rand.Read(data)
	require.NoError(t, err)
	handler := &bug433BigDownloadHandler{data: data}
	ts := httptest.NewTLSServer(handler)
	defer ts.Close()
	client := ts.Client()

	req, err := http.NewRequest(http.MethodGet, ts.URL, nil)
	require.NoError(t, err)
	res, err := client.Do(req)
	require.NoError(t, err)
	require.NotNil(t, res.TLS)

	resumer := NewUpdateResumer(res.Body, res.ContentLength, 2*time.Second, client, req)
	resumer.noteConnection(res)
	defer resumer.Close()

	got, err := ioutil.ReadAll(resumer)
	require.NoError(t, err)
	require.Equal(t, data, got)

	var diag []string
	for _, e := range hook.AllEntries() {
		if strings.HasPrefix(e.Message, "Download break diagnostics (BUG-433):") {
			assert.Equal(t, log.WarnLevel, e.Level)
			diag = append(diag, e.Message)
		}
	}
	require.Len(t, diag, 1, "one break -> exactly one diagnostics line")
	line := diag[0]
	assert.Contains(t, line, "class=unexpected-eof")
	assert.Contains(t, line, fmt.Sprintf("/%d", len(data)))
	assert.Contains(t, line, "tls="+tlsVersionName(res.TLS.Version))
	assert.Contains(t, line, "cipher="+tls.CipherSuiteName(res.TLS.CipherSuite))
	assert.Contains(t, line, "tcp_delta={")
}
