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
//
// Tests for BUG-433: soc-ota-agent's HTTPS/websocket TLS transport moves
// from the cgo github.com/mendersoftware/openssl binding to Go's native
// crypto/tls by default (the binding's Go<->BIO glue was root-caused to
// corrupt the TLS record stream on the Orange Pi Zero 2W under concurrent
// download + eMMC writes); the binding is retained ONLY for pkcs11:/
// SSLEngine-configured keys (HSM support), selected via selectTLSStack.
package client

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMain relaxes Go crypto/tls's hard 8192-bit RSA key ceiling
// (crypto/tls/conn.go's documented DoS guard, overridable only via the
// GODEBUG=tlsmaxrsasize setting) to accommodate this package's existing
// test-only fixture certs (client/https_server_test.go's localhostCert and
// friends, which predate this suite and carry a 16378-bit RSA key — no real
// production server should ever present a key anywhere near this size).
// This env var is scoped to THIS TEST BINARY ONLY via os.Setenv before
// m.Run(): it is never compiled into the production soc-ota-agent binary
// (TestMain only exists in _test.go files) and does not touch go.mod's
// module-wide godebug defaults, so it changes nothing about the real
// agent's TLS security posture. Before BUG-433, every one of these tests
// dialed through the cgo OpenSSL binding, which has no equivalent key-size
// ceiling, so this limit was never hit; moving the default transport to Go's
// crypto/tls (BUG-433) surfaces it here for the first time as a pure
// test-fixture compatibility issue, confirmed by feeding a >8192-bit RSA key
// through both a bare TestMain-less and TestMain-equipped experiment.
func TestMain(m *testing.M) {
	_ = os.Setenv("GODEBUG", "tlsmaxrsasize=20000")
	os.Exit(m.Run())
}

// --- Selection rule: plain config -> Go stack, pkcs11/SSLEngine -> OpenSSL ---

func TestUseOpenSSLTLSSelectionRule(t *testing.T) {
	tests := map[string]struct {
		conf        Config
		wantOpenSSL bool
	}{
		"no HttpsClient at all": {
			conf:        Config{},
			wantOpenSSL: false,
		},
		"HttpsClient with plain PEM cert/key": {
			conf: Config{
				HttpsClient: &HttpsClient{Certificate: "a.crt", Key: "a.key"},
			},
			wantOpenSSL: false,
		},
		"pkcs11-prefixed key": {
			conf: Config{
				HttpsClient: &HttpsClient{Key: "pkcs11:token=foo;object=bar"},
			},
			wantOpenSSL: true,
		},
		"SSLEngine set with a plain key path": {
			conf: Config{
				HttpsClient: &HttpsClient{Key: "a.key", SSLEngine: "pkcs11"},
			},
			wantOpenSSL: true,
		},
		"both pkcs11 key and SSLEngine": {
			conf: Config{
				HttpsClient: &HttpsClient{Key: "pkcs11:token=foo", SSLEngine: "pkcs11"},
			},
			wantOpenSSL: true,
		},
	}

	for name, tc := range tests {
		tc := tc
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.wantOpenSSL, useOpenSSLTLS(tc.conf))
			// selectTLSStack must agree with useOpenSSLTLS (it wraps it with
			// logging only) and must not panic/log-crash on any of these.
			require.Equal(t, tc.wantOpenSSL, selectTLSStack(tc.conf))
		})
	}
}

func TestNewHttpsClientSelectsGoStackByDefault(t *testing.T) {
	client, err := newHttpsClient(Config{})
	require.NoError(t, err)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok, "expected *http.Transport")
	require.NotNil(t, transport.TLSClientConfig)
	require.Nil(t, transport.DialTLS, "Go stack must not use the openssl DialTLS hook")
	require.False(t, transport.ForceAttemptHTTP2, "must stay HTTP/1.1 for streaming/Range semantics")
	require.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
}

func TestNewHttpsClientSelectsOpenSSLForPKCS11(t *testing.T) {
	client, err := newHttpsClient(Config{
		HttpsClient: &HttpsClient{Key: "pkcs11:token=foo", SSLEngine: "pkcs11"},
	})
	require.NoError(t, err)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok, "expected *http.Transport")
	require.NotNil(t, transport.DialTLS, "pkcs11/SSLEngine configs must keep the openssl dialer")
}

func TestNewWebsocketDialerTLSSelectsGoStackByDefault(t *testing.T) {
	dialer, err := newWebsocketDialerTLS(Config{ServerCert: "testdata/server.crt"})
	require.NoError(t, err)
	require.NotNil(t, dialer.TLSClientConfig)
	require.Nil(t, dialer.NetDialTLSContext, "Go stack must not use the openssl custom dial hook")
}

func TestNewWebsocketDialerTLSSelectsOpenSSLForSSLEngine(t *testing.T) {
	dialer, err := newWebsocketDialerTLS(Config{
		HttpsClient: &HttpsClient{Key: "pkcs11:token=foo", SSLEngine: "pkcs11"},
	})
	require.NoError(t, err)
	require.NotNil(t, dialer.NetDialTLSContext, "pkcs11/SSLEngine configs must keep the openssl dialer")
}

// --- buildGoTLSConfig ---

func TestBuildGoTLSConfigDefaults(t *testing.T) {
	tlsConfig, err := buildGoTLSConfig(Config{})
	require.NoError(t, err)
	require.Equal(t, uint16(tls.VersionTLS12), tlsConfig.MinVersion)
	require.False(t, tlsConfig.InsecureSkipVerify)
	require.NotNil(t, tlsConfig.RootCAs)
}

func TestBuildGoTLSConfigSkipVerify(t *testing.T) {
	tlsConfig, err := buildGoTLSConfig(Config{NoVerify: true})
	require.NoError(t, err)
	require.True(t, tlsConfig.InsecureSkipVerify)
}

func TestBuildGoTLSConfigRootCAsFromServerCert(t *testing.T) {
	// testdata/server.crt is an existing fixture already used by the
	// openssl-path tests (client_test.go's TestLoadingTrust, tls_min_version_test.go).
	tlsConfig, err := buildGoTLSConfig(Config{ServerCert: "testdata/server.crt"})
	require.NoError(t, err)
	require.NotNil(t, tlsConfig.RootCAs)
}

func TestBuildGoTLSConfigServerCertMissingFile(t *testing.T) {
	// Parity with the OpenSSL path's loadServerTrust: a missing ServerCert
	// file is logged, not a hard error (TestHttpClient/TestClientAuthNoCert
	// in client_test.go/client_auth_test.go rely on this exact behavior —
	// NewApiClient must still return a usable client).
	tlsConfig, err := buildGoTLSConfig(Config{ServerCert: "does-not-exist.crt"})
	require.NoError(t, err)
	require.NotNil(t, tlsConfig.RootCAs)
}

func TestBuildGoTLSConfigServerCertUnparseable(t *testing.T) {
	// client.go is a real file but contains no PEM certificate. Same parity
	// as the missing-file case above: logged, not a hard error.
	tlsConfig, err := buildGoTLSConfig(Config{ServerCert: "client.go"})
	require.NoError(t, err)
	require.NotNil(t, tlsConfig.RootCAs)
}

func TestBuildGoTLSConfigClientCertificate(t *testing.T) {
	certFile, keyFile := writeEphemeralCertKeyPair(t)

	tlsConfig, err := buildGoTLSConfig(Config{
		HttpsClient: &HttpsClient{Certificate: certFile, Key: keyFile},
	})
	require.NoError(t, err)
	require.Len(t, tlsConfig.Certificates, 1)
}

func TestBuildGoTLSConfigClientCertificateBadKey(t *testing.T) {
	certFile, _ := writeEphemeralCertKeyPair(t)

	_, err := buildGoTLSConfig(Config{
		HttpsClient: &HttpsClient{Certificate: certFile, Key: "does-not-exist.key"},
	})
	require.Error(t, err)
}

// --- GAP-SEC-F4 key-strength hook (VerifyPeerCertificate) ---

func TestBuildGoTLSConfigInstallsVerifyPeerCertificateByDefault(t *testing.T) {
	tlsConfig, err := buildGoTLSConfig(Config{})
	require.NoError(t, err)
	require.NotNil(t, tlsConfig.VerifyPeerCertificate,
		"the GAP-SEC-F4 key-strength hook must be installed by default")
}

func TestBuildGoTLSConfigOmitsVerifyPeerCertificateUnderNoVerify(t *testing.T) {
	// Mirrors the OpenSSL path exactly: dialOpenSSL returns immediately on
	// conf.NoVerify without ever calling conn.VerifyResult(), performing NO
	// certificate checks of any kind once NoVerify is set — not just
	// hostname. The Go path's hook must be skipped the same way.
	tlsConfig, err := buildGoTLSConfig(Config{NoVerify: true})
	require.NoError(t, err)
	require.Nil(t, tlsConfig.VerifyPeerCertificate)
}

func TestCheckGapSecF4KeyStrength(t *testing.T) {
	weakRSAKey, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	okRSAKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	weakECDSAKey, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	require.NoError(t, err)
	okECDSAKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ed25519Pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	tests := map[string]struct {
		pub     interface{}
		wantErr bool
	}{
		"RSA 1024 bits rejected":  {pub: &weakRSAKey.PublicKey, wantErr: true},
		"RSA 2048 bits accepted":  {pub: &okRSAKey.PublicKey, wantErr: false},
		"ECDSA P-224 rejected":    {pub: &weakECDSAKey.PublicKey, wantErr: true},
		"ECDSA P-256 accepted":    {pub: &okECDSAKey.PublicKey, wantErr: false},
		"Ed25519 always accepted": {pub: ed25519Pub, wantErr: false},
	}

	for name, tc := range tests {
		tc := tc
		t.Run(name, func(t *testing.T) {
			cert := &x509.Certificate{
				Subject:   pkix.Name{CommonName: name},
				PublicKey: tc.pub,
			}
			err := checkGapSecF4KeyStrength(cert)
			if tc.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "GAP-SEC-F4")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCheckGapSecF4KeyStrengthUnsupportedAlgorithm(t *testing.T) {
	cert := &x509.Certificate{
		Subject:   pkix.Name{CommonName: "unsupported-key-algo"},
		PublicKey: "not-a-real-key", // no x509 cert ever has this, but proves the default case
	}
	err := checkGapSecF4KeyStrength(cert)
	require.Error(t, err)
	require.Contains(t, err.Error(), "GAP-SEC-F4")
	require.Contains(t, err.Error(), "unsupported public key algorithm")
}

// --- Real streaming download + mid-stream Range resume over the Go stack ---

const bug433BigDownloadSize = 32 * 1024 * 1024 // 32 MiB, per fix_design item 2

// bug433BigDownloadHandler serves deterministic content and, on the FIRST
// non-Range request only, writes a short body against a full Content-Length
// header so the net/http server forcibly closes the connection mid-stream
// (net/http's own "wrote fewer bytes than declared Content-Length" abort) —
// reproducing, at the protocol level, the mid-download connection break this
// suite regression-tests the resume path against (BUG-433's real-world
// symptom was a broken artifact download). The retry (which carries a Range
// header) is always served in full.
type bug433BigDownloadHandler struct {
	data []byte

	mu          sync.Mutex
	firstServed bool
}

func (h *bug433BigDownloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	size := int64(len(h.data))
	rangeHeader := r.Header.Get("Range")

	if rangeHeader != "" {
		var pos int64
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-", &pos); err != nil {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", pos, size-1, size))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size-pos))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(h.data[pos:])
		return
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.WriteHeader(http.StatusOK)

	h.mu.Lock()
	breakThisOne := !h.firstServed
	h.firstServed = true
	h.mu.Unlock()

	if breakThisOne {
		_, _ = w.Write(h.data[:size/3])
		return
	}
	_, _ = w.Write(h.data)
}

// writeEphemeralCertKeyPair generates a throw-away self-signed ECDSA
// certificate/key pair and writes both as PEM files under t.TempDir(), so
// client-certificate tests don't depend on the repo's testdata fixtures
// (client/testdata/client-cert.key and wrong.key referenced by
// client_test.go's TestLoadingTrust do not exist in this checkout — a
// pre-existing gap unrelated to BUG-433, confirmed still failing on this
// branch's base commit before any BUG-433 change).
func writeEphemeralCertKeyPair(t *testing.T) (certFile, keyFile string) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "bug433-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	require.NoError(t, err)

	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")

	certOut, err := os.Create(certFile)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}))
	require.NoError(t, certOut.Close())

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	keyOut, err := os.Create(keyFile)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}))
	require.NoError(t, keyOut.Close())

	return certFile, keyFile
}

// newEphemeralRSAServerCert generates a throw-away SELF-SIGNED RSA server
// certificate of the given key size, valid for 127.0.0.1/::1, and returns
// its PEM cert/key bytes (for feeding directly into startTestHTTPS) plus the
// cert written out as a file (for use as Config.ServerCert — this cert is
// its own trust anchor). Used by the GAP-SEC-F4 key-strength hook tests:
// unlike client/https_server_test.go's localhostCertShortEEKey (whose
// Subject DN matches testdata/server.crt but whose KEY does not — Go's own
// default chain verification rejects that pairing outright, with "signed by
// unknown authority", before ever reaching the VerifyPeerCertificate hook;
// confirmed empirically), a cert that IS its own configured root always
// clears Go's default verification and reaches the hook, so weak-vs-strong
// key strength is the only thing left for the hook to reject or accept on.
func newEphemeralRSAServerCert(t *testing.T, bits int) (certPEM, keyPEM []byte, certFile string) {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, bits)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "bug433-weak-key-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	require.NoError(t, err)

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})

	certFile = filepath.Join(t.TempDir(), "weak-server-cert.pem")
	require.NoError(t, ioutil.WriteFile(certFile, certPEM, 0o600))

	return certPEM, keyPEM, certFile
}

// TestGoTLSBigDownloadWithMidStreamRangeResume exercises fix_design item 2
// end to end: a 32 MiB download from an httptest.NewTLSServer, whose
// certificate is trusted via the RootCAs path buildGoTLSConfig wires up
// (NOT InsecureSkipVerify — that's the whole point, it proves the RootCAs
// plumbing works), broken mid-stream and completed through a real HTTP
// Range resume driven by the actual client/update_resumer.go UpdateResumer.
func TestGoTLSBigDownloadWithMidStreamRangeResume(t *testing.T) {
	oldSmallestUnit := ExponentialBackoffSmallestUnit
	ExponentialBackoffSmallestUnit = 10 * time.Millisecond
	defer func() { ExponentialBackoffSmallestUnit = oldSmallestUnit }()

	data := make([]byte, bug433BigDownloadSize)
	_, err := rand.Read(data)
	require.NoError(t, err)

	handler := &bug433BigDownloadHandler{data: data}
	ts := httptest.NewTLSServer(handler)
	defer ts.Close()

	// Write the test server's own certificate out as a PEM file and feed it
	// in as Config.ServerCert, exactly like a device's real ServerCertificate
	// setting — this is what proves RootCAs wiring, as opposed to just
	// setting NoVerify/InsecureSkipVerify.
	serverCertPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: ts.Certificate().Raw,
	})
	certFile := filepath.Join(t.TempDir(), "test-server.crt")
	require.NoError(t, ioutil.WriteFile(certFile, serverCertPEM, 0o600))

	apiClient, err := NewApiClient(Config{ServerCert: certFile})
	require.NoError(t, err)

	// Confirm this test is actually exercising the Go stack, not openssl.
	transport, ok := apiClient.Transport.(*http.Transport)
	require.True(t, ok)
	require.Nil(t, transport.DialTLS)

	req, err := http.NewRequest(http.MethodGet, ts.URL, nil)
	require.NoError(t, err)

	res, err := apiClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, int64(len(data)), res.ContentLength)

	resumer := NewUpdateResumer(res.Body, res.ContentLength, 2*time.Second, apiClient, req)
	defer resumer.Close()

	got, err := ioutil.ReadAll(resumer)
	require.NoError(t, err)
	require.Equal(t, data, got, "resumed download must be byte-identical to the source")

	// Sanity: the break actually happened and a resume actually occurred,
	// i.e. this test is exercising the resume path and not accidentally
	// getting the whole file in one shot.
	require.True(t, handler.firstServed)
}

var _ io.Closer = (*UpdateResumer)(nil)
