// Copyright 2024 SoC Monitoring
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package client

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keyGatedServer returns 200 to any request whose X-API-Key is in validKeys,
// and 401 otherwise. It records every X-API-Key value it saw, in order.
type keyGatedServer struct {
	mu        sync.Mutex
	validKeys map[string]bool
	seenKeys  []string
	seenBody  []string
}

func (s *keyGatedServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		body, _ := io.ReadAll(r.Body)

		s.mu.Lock()
		s.seenKeys = append(s.seenKeys, key)
		s.seenBody = append(s.seenBody, string(body))
		ok := s.validKeys[key]
		s.mu.Unlock()

		if ok {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}
}

func (s *keyGatedServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.seenKeys))
	copy(out, s.seenKeys)
	return out
}

func (s *keyGatedServer) bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.seenBody))
	copy(out, s.seenBody)
	return out
}

func newSoCTestClient(t *testing.T, serverURL, apiKey string, reload func() (string, error)) *SoCMonitoringClient {
	t.Helper()
	c, err := NewSoCMonitoringClient(Config{}, apiKey, "dev-test", serverURL, reload)
	require.NoError(t, err)
	return c
}

func newSoCGetRequest(t *testing.T) *http.Request {
	t.Helper()
	// Only the path is used by reconstructRequest; scheme/host come from the
	// configured serverURL. The host below is a placeholder.
	req, err := http.NewRequest(http.MethodGet, "http://placeholder/api/devices/v1/foo", nil)
	require.NoError(t, err)
	return req
}

// (a) 401 with a rotated on-disk key: the client should reload the new key and
// retry exactly once, succeeding on the retry.
func TestSoCClientDo_ReloadRotatedKeyRetriesOnceAndSucceeds(t *testing.T) {
	srv := &keyGatedServer{validKeys: map[string]bool{"newkey": true}}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	reloadCalls := 0
	c := newSoCTestClient(t, ts.URL, "oldkey", func() (string, error) {
		reloadCalls++
		return "newkey", nil
	})

	resp, err := c.Do(newSoCGetRequest(t))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, reloadCalls, "reload should be consulted exactly once")
	// First attempt with the stale key, second (retry) with the reloaded key.
	assert.Equal(t, []string{"oldkey", "newkey"}, srv.requests())
	// The cached key should now be the reloaded one.
	assert.Equal(t, "newkey", c.apiKey)
}

// (b) Persistent 401 even after the reload returns a *different* key: the
// client must retry at most once and then surface the auth error.
func TestSoCClientDo_PersistentUnauthorizedRetriesAtMostOnce(t *testing.T) {
	// No valid keys — server always 401s.
	srv := &keyGatedServer{validKeys: map[string]bool{}}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	reloadCalls := 0
	c := newSoCTestClient(t, ts.URL, "oldkey", func() (string, error) {
		reloadCalls++
		return "newbadkey", nil
	})

	resp, err := c.Do(newSoCGetRequest(t))
	require.Error(t, err)
	require.NotNil(t, resp)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, 1, reloadCalls, "reload consulted once")
	// Exactly one retry: original attempt + one retry == 2 requests, no more.
	assert.Equal(t, []string{"oldkey", "newbadkey"}, srv.requests())
}

// (b') If the reloaded key is unchanged, there is no point retrying — the
// client should not issue a second request.
func TestSoCClientDo_UnchangedKeyDoesNotRetry(t *testing.T) {
	srv := &keyGatedServer{validKeys: map[string]bool{}}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	reloadCalls := 0
	c := newSoCTestClient(t, ts.URL, "samekey", func() (string, error) {
		reloadCalls++
		return "samekey", nil
	})

	resp, err := c.Do(newSoCGetRequest(t))
	require.Error(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, 1, reloadCalls, "reload consulted once")
	assert.Equal(t, []string{"samekey"}, srv.requests(), "no retry when key is unchanged")
}

// (c) Rate-limit: two immediate 401-producing calls must trigger only one disk
// read, because the second falls inside reloadMinInterval.
func TestSoCClientDo_ReloadRateLimited(t *testing.T) {
	srv := &keyGatedServer{validKeys: map[string]bool{}}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	reloadCalls := 0
	c := newSoCTestClient(t, ts.URL, "oldkey", func() (string, error) {
		reloadCalls++
		return "rotatingkey", nil
	})
	// Make the window large so the two back-to-back calls are unambiguously
	// within it regardless of wall-clock jitter.
	c.reloadMinInterval = time.Hour

	resp1, err := c.Do(newSoCGetRequest(t))
	require.Error(t, err)
	resp1.Body.Close()

	resp2, err := c.Do(newSoCGetRequest(t))
	require.Error(t, err)
	resp2.Body.Close()

	assert.Equal(t, 1, reloadCalls, "second 401 within reloadMinInterval must not re-read the key from disk")
}

// A nil keyReloadFn disables the whole mechanism: a 401 is surfaced immediately
// with no reload and no retry (identical to the pre-fix behavior).
func TestSoCClientDo_NilReloadFnNoRetry(t *testing.T) {
	srv := &keyGatedServer{validKeys: map[string]bool{}}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	c := newSoCTestClient(t, ts.URL, "oldkey", nil)

	resp, err := c.Do(newSoCGetRequest(t))
	require.Error(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, []string{"oldkey"}, srv.requests(), "no retry without a reload func")
}

// The happy path must not consult the reload func at all (zero extra I/O).
func TestSoCClientDo_HappyPathNoReload(t *testing.T) {
	srv := &keyGatedServer{validKeys: map[string]bool{"goodkey": true}}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	reloadCalls := 0
	c := newSoCTestClient(t, ts.URL, "goodkey", func() (string, error) {
		reloadCalls++
		return "goodkey", nil
	})

	resp, err := c.Do(newSoCGetRequest(t))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 0, reloadCalls, "reload must not be consulted on a 2xx response")
	assert.Equal(t, []string{"goodkey"}, srv.requests())
}

// A 403 must NOT trigger a reload/retry — the fix is scoped to 401. A stale
// key producing 403 is out of scope (403 means authenticated-but-unscoped).
func TestSoCClientDo_ForbiddenDoesNotReload(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()

	reloadCalls := 0
	c := newSoCTestClient(t, ts.URL, "oldkey", func() (string, error) {
		reloadCalls++
		return "newkey", nil
	})

	resp, err := c.Do(newSoCGetRequest(t))
	require.Error(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, 0, reloadCalls, "403 must not trigger an API key reload")
}

// The retry must resend the request body (status/inventory updates are bodied
// PUT/POSTs), which relies on http.Request.GetBody being replayable.
func TestSoCClientDo_RetryReplaysRequestBody(t *testing.T) {
	srv := &keyGatedServer{validKeys: map[string]bool{"newkey": true}}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	c := newSoCTestClient(t, ts.URL, "oldkey", func() (string, error) {
		return "newkey", nil
	})

	// bytes.Reader gives http.NewRequest a non-nil GetBody for replay.
	req, err := http.NewRequest(http.MethodPut, "http://placeholder/api/status",
		bytes.NewReader([]byte(`{"status":"installing"}`)))
	require.NoError(t, err)

	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	// Both the original attempt and the retry must carry the same body.
	assert.Equal(t,
		[]string{`{"status":"installing"}`, `{"status":"installing"}`},
		srv.bodies())
}
