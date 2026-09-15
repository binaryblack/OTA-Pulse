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
// Tests for BUG-442: client/client_update.go's FetchUpdate parses a
// Retry-After header on a 503/429 response into a typed *RetryAfterError
// that wraps the existing *APIError, so app/state.go's fetchStoreRetryState
// can honor the server's own back-off hint.
package client

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- parseRetryAfter ---

func TestParseRetryAfter(t *testing.T) {
	tests := map[string]struct {
		header string
		want   time.Duration
	}{
		"absent header":                 {header: "", want: 0},
		"garbage value":                 {header: "banana", want: 0},
		"zero delta-seconds":            {header: "0", want: 0},
		"positive delta-seconds":        {header: "120", want: 120 * time.Second},
		"negative delta-seconds":        {header: "-5", want: 0},
		"delta-seconds with whitespace": {header: "  15  ", want: 15 * time.Second},
	}

	for name, tc := range tests {
		tc := tc
		t.Run(name, func(t *testing.T) {
			got := parseRetryAfter(tc.header)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("HTTP-date in the future", func(t *testing.T) {
		future := time.Now().Add(30 * time.Second).UTC()
		got := parseRetryAfter(future.Format(http.TimeFormat))
		// http.TimeFormat has 1-second resolution and we lose sub-second
		// precision round-tripping through it, so allow a small tolerance.
		assert.InDelta(t, 30*time.Second, got, float64(2*time.Second))
	})

	t.Run("HTTP-date in the past", func(t *testing.T) {
		past := time.Now().Add(-30 * time.Second).UTC()
		got := parseRetryAfter(past.Format(http.TimeFormat))
		assert.Equal(t, time.Duration(0), got, "an already-past HTTP-date must not produce a negative wait")
	})
}

// --- FetchUpdate wiring ---

func Test_FetchUpdate_503WithRetryAfterSeconds_ReturnsRetryAfterError(t *testing.T) {
	ts := startTestHTTPS(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":"download_busy"}`)
		}),
		localhostCert,
		localhostKey)
	defer ts.Close()

	ac, err := NewApiClient(Config{ServerCert: "testdata/server.crt"})
	require.NoError(t, err)

	uc := NewUpdate()
	_, _, err = uc.FetchUpdate(ac, ts.URL, 1*time.Minute)
	require.Error(t, err)

	var retryErr *RetryAfterError
	require.True(t, errors.As(err, &retryErr), "error must be (wrap) a *RetryAfterError")
	assert.Equal(t, http.StatusServiceUnavailable, retryErr.Status)
	assert.Equal(t, 2*time.Second, retryErr.After)

	// The existing *APIError layer must still be reachable, so callers that
	// predate BUG-442 and only know about *APIError keep working.
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr), "the wrapped *APIError must still be reachable via errors.As")
	assert.Same(t, retryErr.APIError, apiErr)
}

func Test_FetchUpdate_429WithRetryAfterDate_ReturnsRetryAfterError(t *testing.T) {
	future := time.Now().Add(5 * time.Second).UTC()

	ts := startTestHTTPS(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", future.Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":"rate_limited"}`)
		}),
		localhostCert,
		localhostKey)
	defer ts.Close()

	ac, err := NewApiClient(Config{ServerCert: "testdata/server.crt"})
	require.NoError(t, err)

	uc := NewUpdate()
	_, _, err = uc.FetchUpdate(ac, ts.URL, 1*time.Minute)
	require.Error(t, err)

	var retryErr *RetryAfterError
	require.True(t, errors.As(err, &retryErr))
	assert.Equal(t, http.StatusTooManyRequests, retryErr.Status)
	assert.InDelta(t, 5*time.Second, retryErr.After, float64(2*time.Second))
}

func Test_FetchUpdate_503WithoutRetryAfter_ReturnsPlainAPIError(t *testing.T) {
	ts := startTestHTTPS(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Deliberately no Retry-After header.
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":"download_busy"}`)
		}),
		localhostCert,
		localhostKey)
	defer ts.Close()

	ac, err := NewApiClient(Config{ServerCert: "testdata/server.crt"})
	require.NoError(t, err)

	uc := NewUpdate()
	_, _, err = uc.FetchUpdate(ac, ts.URL, 1*time.Minute)
	require.Error(t, err)

	// Still a *RetryAfterError (BUG-442's FetchUpdate always wraps 503/429),
	// but with After == 0, which is fetchStoreRetryState.Handle's signal to
	// fall through to the old exponential backoff schedule untouched
	// (asserted at the state-machine level in app/bug442_retryafter_test.go).
	var retryErr *RetryAfterError
	require.True(t, errors.As(err, &retryErr))
	assert.Equal(t, time.Duration(0), retryErr.After)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
}

func Test_FetchUpdate_404_ReturnsPlainAPIErrorNeverRetryAfter(t *testing.T) {
	// Sanity check: only 503/429 get the RetryAfterError treatment.
	ts := startTestHTTPS(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
		}),
		localhostCert,
		localhostKey)
	defer ts.Close()

	ac, err := NewApiClient(Config{ServerCert: "testdata/server.crt"})
	require.NoError(t, err)

	uc := NewUpdate()
	_, _, err = uc.FetchUpdate(ac, ts.URL, 1*time.Minute)
	require.Error(t, err)

	var retryErr *RetryAfterError
	assert.False(t, errors.As(err, &retryErr), "a 404 must never produce a *RetryAfterError")

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
}
