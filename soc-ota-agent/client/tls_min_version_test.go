// Copyright 2026 SoC Monitoring
package client

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// startTestHTTPSWithMaxVersion is like startTestHTTPS but caps the server's
// negotiable TLS version, so tests can prove the agent's client actually
// refuses to complete a handshake below its pinned floor (GAP-SEC-F4) rather
// than merely asserting the happy path still works.
func startTestHTTPSWithMaxVersion(maxVersion uint16) *httptest.Server {
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	cert, err := tls.X509KeyPair(localhostCert, localhostKey)
	if err != nil {
		panic(err)
	}
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
		MaxVersion:   maxVersion,
		NextProtos:   []string{"http/1.1"},
	}
	ts.StartTLS()
	return ts
}

func TestOpenSSLCtxRejectsTLS11Server(t *testing.T) {
	ts := startTestHTTPSWithMaxVersion(tls.VersionTLS11)
	defer ts.Close()

	cl, err := NewApiClient(Config{ServerCert: "testdata/server.crt"})
	require.NoError(t, err)
	require.NotNil(t, cl)

	hreq, err := http.NewRequest(http.MethodGet, ts.URL, nil)
	require.NoError(t, err)

	_, err = cl.Do(hreq)
	require.Error(t, err,
		"a server that only negotiates up to TLS 1.1 must be rejected by the TLS-1.2-pinned client")
}

func TestOpenSSLCtxAcceptsTLS12Server(t *testing.T) {
	ts := startTestHTTPSWithMaxVersion(tls.VersionTLS12)
	defer ts.Close()

	cl, err := NewApiClient(Config{ServerCert: "testdata/server.crt"})
	require.NoError(t, err)
	require.NotNil(t, cl)

	hreq, err := http.NewRequest(http.MethodGet, ts.URL, nil)
	require.NoError(t, err)

	resp, err := cl.Do(hreq)
	require.NoError(t, err, "a TLS 1.2 server must still be accepted by the pinned client")
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
