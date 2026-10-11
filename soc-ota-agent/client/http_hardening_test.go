// Copyright 2026 SoC Monitoring
package client

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GC-AG-03 / GC-AG-04 / GC-AG-05 (TASK-S108-038): redirect policy, transport
// timeouts and bounded server response bodies on the agent's HTTP client.

// --- GC-AG-03: CheckRedirect policy ----------------------------------------

func doGet(t *testing.T, cl *ApiClient, url string, hdr map[string]string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return cl.Do(req)
}

func TestRedirectCrossHostRefused(t *testing.T) {
	var leaked int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&leaked, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	// "localhost" and "127.0.0.1" reach the same listener but are different
	// host names, which is what the policy compares.
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loc := strings.Replace(target.URL, "127.0.0.1", "localhost", 1) + "/steal"
		http.Redirect(w, r, loc, http.StatusFound)
	}))
	defer redirector.Close()

	cl, err := NewApiClient(Config{})
	require.NoError(t, err)

	resp, err := doGet(t, cl, redirector.URL+"/x", map[string]string{
		"X-API-Key":   "secret-key",
		"X-Device-ID": "dev-1",
	})
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err, "a cross-host redirect must be refused")
	assert.Contains(t, err.Error(), "cross-host redirect")
	assert.EqualValues(t, 0, atomic.LoadInt32(&leaked),
		"the redirect target must never receive a request (and so never the X-API-Key header)")
}

func TestRedirectSameHostFollowed(t *testing.T) {
	var seenKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusFound)
			return
		}
		seenKey = r.Header.Get("X-API-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cl, err := NewApiClient(Config{})
	require.NoError(t, err)

	resp, err := doGet(t, cl, ts.URL+"/old", map[string]string{"X-API-Key": "k"})
	require.NoError(t, err, "a same-host, same-scheme redirect (e.g. trailing slash) must keep working")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "k", seenKey)
}

func TestRedirectHTTPSToHTTPRefused(t *testing.T) {
	var leaked int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&leaked, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer plain.Close()

	// Same host name (127.0.0.1), only the scheme downgrades.
	tlsSrv := startTestHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/downgraded", http.StatusFound)
	}), localhostCert, localhostKey)
	defer tlsSrv.Close()

	cl, err := NewApiClient(Config{ServerCert: "testdata/server.crt"})
	require.NoError(t, err)

	resp, err := doGet(t, cl, tlsSrv.URL+"/x", map[string]string{"X-API-Key": "k"})
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err, "an https->http redirect must be refused")
	assert.Contains(t, err.Error(), "https to http")
	assert.EqualValues(t, 0, atomic.LoadInt32(&leaked))
}

func TestRedirectHTTPToHTTPSSameHostAllowed(t *testing.T) {
	tlsSrv := startTestHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), localhostCert, localhostKey)
	defer tlsSrv.Close()

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tlsSrv.URL+"/upgraded", http.StatusFound)
	}))
	defer plain.Close()

	cl, err := NewApiClient(Config{ServerCert: "testdata/server.crt"})
	require.NoError(t, err)

	resp, err := doGet(t, cl, plain.URL+"/x", nil)
	require.NoError(t, err, "an http->https upgrade on the same host stays allowed")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestRedirectLoopStillBounded(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"x", http.StatusFound)
	}))
	defer ts.Close()

	cl, err := NewApiClient(Config{})
	require.NoError(t, err)
	resp, err := doGet(t, cl, ts.URL+"/", nil)
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped after 10 redirects")
}

// --- GC-AG-04: transport timeouts -------------------------------------------

func TestTransportTimeoutsConfigured(t *testing.T) {
	for name, conf := range map[string]Config{
		"plain": {},
		"tls":   {ServerCert: "testdata/server.crt"},
	} {
		t.Run(name, func(t *testing.T) {
			cl, err := NewApiClient(conf)
			require.NoError(t, err)
			tr, ok := cl.Client.Transport.(*http.Transport)
			require.True(t, ok)
			assert.Equal(t, 10*time.Second, tr.TLSHandshakeTimeout)
			assert.Equal(t, 60*time.Second, tr.ResponseHeaderTimeout)
			assert.NotNil(t, tr.DialContext)
			// Whole-exchange (body) cap is unchanged.
			assert.Equal(t, 4*time.Hour, cl.Client.Timeout)
		})
	}
}

func TestResponseHeaderTimeoutFiresOnStalledServer(t *testing.T) {
	old := responseHeaderTimeout
	responseHeaderTimeout = 200 * time.Millisecond
	defer func() { responseHeaderTimeout = old }()

	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // accept the request, never answer
	}))
	defer ts.Close()
	defer close(release)

	cl, err := NewApiClient(Config{})
	require.NoError(t, err)

	start := time.Now()
	resp, err := doGet(t, cl, ts.URL, nil)
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout awaiting response headers")
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestSlowBodyIsNotCutByHeaderTimeout(t *testing.T) {
	old := responseHeaderTimeout
	responseHeaderTimeout = 300 * time.Millisecond
	defer func() { responseHeaderTimeout = old }()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(700 * time.Millisecond) // body slower than the header timeout
		_, _ = w.Write([]byte("done"))
	}))
	defer ts.Close()

	cl, err := NewApiClient(Config{})
	require.NoError(t, err)
	resp, err := doGet(t, cl, ts.URL, nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := readServerBody(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "done", string(body))
}

// --- GC-AG-05: bounded server response bodies -------------------------------

func TestReadServerBodyLimit(t *testing.T) {
	atLimit := bytes.Repeat([]byte("a"), maxServerBodyBytes)
	got, err := readServerBody(bytes.NewReader(atLimit))
	require.NoError(t, err)
	assert.Len(t, got, maxServerBodyBytes)

	_, err = readServerBody(bytes.NewReader(append(atLimit, 'a')))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds the 4194304 byte limit")
}

func oversizedServer(t *testing.T) *httptest.Server {
	t.Helper()
	big := bytes.Repeat([]byte(" "), maxServerBodyBytes+1024)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(big)
	}))
}

func TestOversizedUpdateCheckBodyRejected(t *testing.T) {
	ts := oversizedServer(t)
	defer ts.Close()

	_, err := NewUpdate().GetScheduledUpdate(ts.Client(), ts.URL, &CurrentUpdate{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "byte limit")
}

func TestOversizedControlMapBodyRejected(t *testing.T) {
	ts := oversizedServer(t)
	defer ts.Close()

	_, err := GetUpdateControlMap(ts.Client(), ts.URL, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "byte limit")
}

func TestOversizedAuthBodyRejected(t *testing.T) {
	ts := oversizedServer(t)
	defer ts.Close()

	_, err := NewAuth().Request(ts.Client(), ts.URL, &testAuthDataMessenger{
		reqData: []byte("req"),
		code:    "tok",
		sigData: []byte("sig"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "byte limit")
}

func TestOversizedErrorBodyTruncatedNotFatal(t *testing.T) {
	big := strings.Repeat("x", maxServerBodyBytes+1024)
	msg := unmarshalErrorMessage(strings.NewReader(big))
	assert.Contains(t, msg, "byte limit")
	assert.Less(t, len(msg), 1024, "an oversized error body must not be echoed back in full")
}
