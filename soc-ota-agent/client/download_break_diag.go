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

// BUG-433 diagnosability. On orange-pi-zero2w a Go process (the agent, or
// a plain net/http+crypto/tls probe) occasionally sees a corrupted TLS
// record mid-download ("tls: bad record MAC"); curl on the same board never
// does. Two open hypotheses are not yet separated by evidence:
//
//	H1  in-process corruption (Go async-preemption signals + kernel
//	    FPSIMD/NEON state handling) -> test with GODEBUG=asyncpreemptoff=1
//	H2  NIC/USB RX-path corruption after RX checksum offload accepted the
//	    segment (r8152) -> test with `ethtool -K <nic> rx off`, watch
//	    Tcp InCsumErrors
//
// No mitigation is applied in code until one of them is confirmed. Instead,
// every download break logs ONE structured line carrying exactly the facts
// needed to attribute a break after the fact: the error class, the TLS
// version/cipher/ALPN of the connection that broke, the byte offset, the Go
// runtime knobs in effect (GODEBUG, GOMAXPROCS), and the kernel TCP and
// per-interface RX error counters as a delta over the download window. The
// line goes through logrus, so it also lands in the deployment log the
// server stores — no SSH needed to read it.
package client

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// diagProcRoot / diagSysClassNet are variables only so tests can point
// them at fixture trees.
var (
	diagProcRoot    = "/proc"
	diagSysClassNet = "/sys/class/net"
)

// Download-break classes. Stable strings: they are grepped from journals
// and deployment logs when comparing break rates across experiments.
const (
	breakClassTLSBadRecordMAC = "tls-bad-record-mac"
	breakClassTLSRecordLayer  = "tls-record-layer"
	breakClassTLSRemoteAlert  = "tls-remote-alert"
	breakClassTLSRecordHeader = "tls-record-header"
	breakClassUnexpectedEOF   = "unexpected-eof"
	breakClassTimeout         = "timeout"
	breakClassConnReset       = "connection-reset"
	breakClassOther           = "other"
)

// classifyDownloadBreak maps a stream read error to a stable class. It
// covers both TLS stacks: Go crypto/tls ("local error: tls: bad record
// MAC") and the OpenSSL binding ("decryption failed or bad record mac",
// "record layer failure").
func classifyDownloadBreak(err error) string {
	if err == nil {
		return breakClassOther
	}
	msg := strings.ToLower(err.Error())
	var rhe tls.RecordHeaderError
	switch {
	case strings.Contains(msg, "bad record mac"):
		return breakClassTLSBadRecordMAC
	case strings.Contains(msg, "record layer failure") ||
		strings.Contains(msg, "decryption failed") ||
		strings.Contains(msg, "cipher operation failed"):
		return breakClassTLSRecordLayer
	case errors.As(err, &rhe):
		return breakClassTLSRecordHeader
	case strings.Contains(msg, "remote error: tls:"):
		return breakClassTLSRemoteAlert
	case errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF):
		return breakClassUnexpectedEOF
	case errors.Is(err, syscall.ECONNRESET) ||
		strings.Contains(msg, "connection reset"):
		return breakClassConnReset
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return breakClassTimeout
	}
	return breakClassOther
}

// ifaceCounters are the /proc/net/dev RX columns relevant to H2.
type ifaceCounters struct {
	rxBytes, rxPackets, rxErrs, rxDrop, rxFifo, rxFrame int64
}

// netCounters is a point-in-time snapshot of kernel counters.
type netCounters struct {
	tcp    map[string]int64 // /proc/net/snmp "Tcp:" row
	ifaces map[string]ifaceCounters
}

// diagTCPFields are the Tcp counters reported. InCsumErrors only rises
// when the KERNEL verifies the checksum, i.e. not for segments the NIC
// already marked good with RX checksum offload on — so a zero delta does
// NOT rule out H2 unless offload was off.
var diagTCPFields = []string{"InCsumErrors", "InErrs", "RetransSegs", "EstabResets"}

func readNetCounters() netCounters {
	return netCounters{
		tcp:    readSNMPTcp(filepath.Join(diagProcRoot, "net", "snmp")),
		ifaces: readProcNetDev(filepath.Join(diagProcRoot, "net", "dev")),
	}
}

// readSNMPTcp parses the header/value "Tcp:" line pair. Missing file or
// odd format -> empty map (diagnostics must never fail a download).
func readSNMPTcp(path string) map[string]int64 {
	out := map[string]int64{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	var header []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] != "Tcp:" {
			continue
		}
		if header == nil {
			header = fields[1:]
			continue
		}
		for i, v := range fields[1:] {
			if i >= len(header) {
				break
			}
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				out[header[i]] = n
			}
		}
		break
	}
	return out
}

// readProcNetDev parses /proc/net/dev, skipping the loopback interface.
func readProcNetDev(path string) map[string]ifaceCounters {
	out := map[string]ifaceCounters{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		if name == "lo" || strings.Contains(name, "|") {
			continue
		}
		cols := strings.Fields(line[colon+1:])
		if len(cols) < 6 {
			continue
		}
		var v [6]int64
		ok := true
		for i := 0; i < 6; i++ {
			n, err := strconv.ParseInt(cols[i], 10, 64)
			if err != nil {
				ok = false
				break
			}
			v[i] = n
		}
		if !ok {
			continue
		}
		out[name] = ifaceCounters{
			rxBytes: v[0], rxPackets: v[1], rxErrs: v[2],
			rxDrop: v[3], rxFifo: v[4], rxFrame: v[5],
		}
	}
	return out
}

// ifaceDriver returns the kernel driver bound to an interface (e.g.
// "r8152"), or "?" when it cannot be read.
func ifaceDriver(name string) string {
	target, err := os.Readlink(filepath.Join(diagSysClassNet, name, "device", "driver"))
	if err != nil {
		return "?"
	}
	return filepath.Base(target)
}

// describeTLS renders the negotiated parameters of the connection a body
// came from. A nil state means the OpenSSL-binding stack (which does not
// populate http.Response.TLS) or plain HTTP.
func describeTLS(cs *tls.ConnectionState, proto string) string {
	if cs == nil {
		return fmt.Sprintf("proto=%s tls=unavailable", orQ(proto))
	}
	return fmt.Sprintf("proto=%s tls=%s cipher=%s alpn=%s resumed=%t",
		orQ(proto), tlsVersionName(cs.Version), tls.CipherSuiteName(cs.CipherSuite),
		orQ(cs.NegotiatedProtocol), cs.DidResume)
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "1.0"
	case tls.VersionTLS11:
		return "1.1"
	case tls.VersionTLS12:
		return "1.2"
	case tls.VersionTLS13:
		return "1.3"
	}
	return fmt.Sprintf("0x%04x", v)
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// formatDownloadBreakDiag builds the single BUG-433 diagnostics line. All
// inputs are explicit so it is deterministic under test.
func formatDownloadBreakDiag(
	err error,
	offset, contentLength, connOffset int64,
	connAge time.Duration,
	tlsDesc string,
	godebug string,
	gomaxprocs int,
	before, after netCounters,
	driver func(string) string,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Download break diagnostics (BUG-433): class=%s offset=%d/%d"+
		" conn_bytes=%d conn_age=%s %s go=%s gomaxprocs=%d godebug=%q",
		classifyDownloadBreak(err), offset, contentLength, connOffset,
		connAge.Round(time.Millisecond), tlsDesc, runtime.Version(), gomaxprocs, godebug)

	b.WriteString(" tcp_delta={")
	for i, k := range diagTCPFields {
		if i > 0 {
			b.WriteString(" ")
		}
		a, okA := after.tcp[k]
		p, okP := before.tcp[k]
		if !okA || !okP {
			fmt.Fprintf(&b, "%s:?", k)
			continue
		}
		fmt.Fprintf(&b, "%s:%+d", k, a-p)
	}
	b.WriteString("}")

	// Report interfaces that carried RX traffic during the window (the
	// download path); if none can be told apart, report every non-lo one.
	names := make([]string, 0, len(after.ifaces))
	for name, a := range after.ifaces {
		if p, ok := before.ifaces[name]; ok && a.rxPackets > p.rxPackets {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		for name := range after.ifaces {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	b.WriteString(" ifaces={")
	for i, name := range names {
		if i > 0 {
			b.WriteString(" ")
		}
		a := after.ifaces[name]
		p, ok := before.ifaces[name]
		if !ok {
			p = a // appeared mid-window: no meaningful delta
		}
		fmt.Fprintf(&b, "%s(%s):rx_bytes%+d,rx_errs%+d,rx_drop%+d,rx_fifo%+d,rx_frame%+d",
			name, driver(name), a.rxBytes-p.rxBytes, a.rxErrs-p.rxErrs,
			a.rxDrop-p.rxDrop, a.rxFifo-p.rxFifo, a.rxFrame-p.rxFrame)
	}
	b.WriteString("}")
	return b.String()
}
