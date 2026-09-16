// Copyright 2026 OTA-Pulse
//
// BUG-447: when the server answers a `Range: bytes=N-` resume request with
// 200 + the full body (no Range support), UpdateResumer must log the REAL
// rejection reason once per attempt and must log the original break only
// once — not re-log the stale break error on every retry, which made the
// staging run e00007ee journal read as three fresh TLS failures.

package client

import (
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bug447NoRangeHandler serves the whole body with 200 on EVERY request
// (ignoring Range entirely) and truncates the first response at 1/3.
type bug447NoRangeHandler struct {
	data        []byte
	mu          sync.Mutex
	firstServed bool
	rangeReqs   int
}

func (h *bug447NoRangeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	size := len(h.data)
	h.mu.Lock()
	if r.Header.Get("Range") != "" {
		h.rangeReqs++
	}
	breakThisOne := !h.firstServed
	h.firstServed = true
	h.mu.Unlock()

	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.WriteHeader(http.StatusOK)
	if breakThisOne {
		_, _ = w.Write(h.data[:size/3])
		return
	}
	_, _ = w.Write(h.data)
}

type bug447LogHook struct {
	mu      sync.Mutex
	entries []string
}

func (h *bug447LogHook) Levels() []log.Level { return log.AllLevels }
func (h *bug447LogHook) Fire(e *log.Entry) error {
	h.mu.Lock()
	h.entries = append(h.entries, e.Message)
	h.mu.Unlock()
	return nil
}
func (h *bug447LogHook) count(prefix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.entries {
		if strings.HasPrefix(m, prefix) {
			n++
		}
	}
	return n
}

func TestUpdateResumerLogsRealRejectionWhenServerIgnoresRange(t *testing.T) {
	oldSmallestUnit := ExponentialBackoffSmallestUnit
	ExponentialBackoffSmallestUnit = 10 * time.Millisecond
	defer func() { ExponentialBackoffSmallestUnit = oldSmallestUnit }()

	hook := &bug447LogHook{}
	oldHooks := log.StandardLogger().Hooks
	log.AddHook(hook)
	defer log.StandardLogger().ReplaceHooks(oldHooks)

	data := make([]byte, 3*1024*1024)
	_, err := rand.Read(data)
	require.NoError(t, err)

	handler := &bug447NoRangeHandler{data: data}
	ts := httptest.NewTLSServer(handler)
	defer ts.Close()

	certFile := filepath.Join(t.TempDir(), "test-server.crt")
	require.NoError(t, ioutil.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: ts.Certificate().Raw,
	}), 0o600))
	apiClient, err := NewApiClient(Config{ServerCert: certFile})
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodGet, ts.URL, nil)
	require.NoError(t, err)
	res, err := apiClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)

	resumer := NewUpdateResumer(res.Body, res.ContentLength, 200*time.Millisecond, apiClient, req)
	_, err = io.Copy(ioutil.Discard, resumer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Cannot resume download")

	handler.mu.Lock()
	rangeReqs := handler.rangeReqs
	handler.mu.Unlock()
	require.GreaterOrEqual(t, rangeReqs, 1, "at least one Range resume request must have been sent")

	assert.Equal(t, 1, hook.count("Download connection broken"),
		"the original break must be logged exactly once, not once per retry: %v", hook.entries)
	assert.Equal(t, rangeReqs, hook.count("Download resume rejected: Could not resume download"),
		"every rejected resume attempt must log its real reason (HTTP 200): %v", hook.entries)
}
