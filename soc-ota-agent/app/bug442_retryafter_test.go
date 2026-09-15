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
// Tests for BUG-442: fetchStoreRetryState.Handle honors a server-provided
// Retry-After hint (surfaced from client.FetchUpdate as a *client.RetryAfterError)
// instead of always falling through to the generic exponential poll/backoff
// schedule, and does not count an honored Retry-After wait against the
// retry budget (ctx.fetchInstallAttempts).
package app

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/binaryblack/OTA-Pulse/client"
	"github.com/binaryblack/OTA-Pulse/datastore"
	"github.com/binaryblack/OTA-Pulse/store"
)

// newTestRetryAfterError builds a *client.RetryAfterError the way
// client.FetchUpdate really does: wrapping a genuine *client.APIError built
// from an actual *http.Response, not a hand-rolled stand-in.
func newTestRetryAfterError(t *testing.T, status int, after time.Duration) *client.RetryAfterError {
	t.Helper()

	resp := &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"error":"download_busy"}`)),
	}
	apiErr := client.NewAPIError(errors.New("error receiving scheduled update information"), resp)

	return &client.RetryAfterError{
		APIError: apiErr,
		Status:   status,
		After:    after,
	}
}

func TestFetchStoreRetryStateHonorsRetryAfter(t *testing.T) {
	update := &datastore.UpdateInfo{ID: "foobar"}
	ctx := StateContext{Store: store.NewMemStore()}
	stc := &stateTestController{updatePollIntvl: 5 * time.Minute}

	retryErr := newTestRetryAfterError(t, http.StatusServiceUnavailable, 2*time.Second)
	s := NewFetchStoreRetryState(NewUpdateFetchState(update), update, retryErr)
	ws := &waitStateTest{}
	s.(*fetchStoreRetryState).WaitState = ws

	next, cancelled := s.Handle(&ctx, stc)
	assert.False(t, cancelled)
	assert.IsType(t, &updateFetchState{}, next)
	assert.Equal(t, 2*time.Second, ws.lastWait,
		"must wait exactly the server's Retry-After hint when it fits under the poll interval")
	assert.Equal(t, 0, ctx.fetchInstallAttempts,
		"an honored Retry-After must NOT count against the retry budget")
}

func TestFetchStoreRetryStateBoundsRetryAfterToPollInterval(t *testing.T) {
	update := &datastore.UpdateInfo{ID: "foobar"}
	ctx := StateContext{Store: store.NewMemStore()}
	stc := &stateTestController{updatePollIntvl: 5 * time.Second}

	// Server asks for far longer than our own poll interval; must be capped.
	retryErr := newTestRetryAfterError(t, http.StatusTooManyRequests, 10*time.Minute)
	s := NewFetchStoreRetryState(NewUpdateFetchState(update), update, retryErr)
	ws := &waitStateTest{}
	s.(*fetchStoreRetryState).WaitState = ws

	next, cancelled := s.Handle(&ctx, stc)
	assert.False(t, cancelled)
	assert.IsType(t, &updateFetchState{}, next)
	assert.Equal(t, 5*time.Second, ws.lastWait,
		"must bound the Retry-After wait above by GetUpdatePollInterval()")
	assert.Equal(t, 0, ctx.fetchInstallAttempts)
}

func TestFetchStoreRetryStateFloorsRetryAfterAtOneSecond(t *testing.T) {
	update := &datastore.UpdateInfo{ID: "foobar"}
	ctx := StateContext{Store: store.NewMemStore()}
	stc := &stateTestController{updatePollIntvl: 5 * time.Minute}

	retryErr := newTestRetryAfterError(t, http.StatusServiceUnavailable, 200*time.Millisecond)
	s := NewFetchStoreRetryState(NewUpdateFetchState(update), update, retryErr)
	ws := &waitStateTest{}
	s.(*fetchStoreRetryState).WaitState = ws

	next, cancelled := s.Handle(&ctx, stc)
	assert.False(t, cancelled)
	assert.IsType(t, &updateFetchState{}, next)
	assert.Equal(t, 1*time.Second, ws.lastWait,
		"must never wait less than 1s even if the server asked for less")
	assert.Equal(t, 0, ctx.fetchInstallAttempts)
}

func TestFetchStoreRetryStateFallsBackToBackoffWithoutRetryAfterHeader(t *testing.T) {
	// A 503 with NO Retry-After header parses to After == 0 (parseRetryAfter's
	// "no hint given" sentinel); fetchStoreRetryState must fall through to
	// the ordinary exponential backoff schedule exactly as before BUG-442,
	// and this DOES count against the retry budget.
	update := &datastore.UpdateInfo{ID: "foobar"}
	ctx := StateContext{Store: store.NewMemStore()}
	stc := &stateTestController{updatePollIntvl: 5 * time.Minute}

	retryErr := newTestRetryAfterError(t, http.StatusServiceUnavailable, 0)
	s := NewFetchStoreRetryState(NewUpdateFetchState(update), update, retryErr)
	ws := &waitStateTest{}
	s.(*fetchStoreRetryState).WaitState = ws

	next, cancelled := s.Handle(&ctx, stc)
	assert.False(t, cancelled)
	assert.IsType(t, &updateFetchState{}, next)
	assert.Equal(t, 1, ctx.fetchInstallAttempts,
		"without a usable Retry-After, the old backoff path must still count the attempt")
	assert.NotEqual(t, time.Duration(0), ws.lastWait)
}

func TestFetchStoreRetryStateFallsBackToBackoffForOrdinaryError(t *testing.T) {
	// A completely unrelated error (not even an APIError) must be
	// unaffected by BUG-442 and take the pre-existing exponential path.
	update := &datastore.UpdateInfo{ID: "foobar"}
	ctx := StateContext{Store: store.NewMemStore()}
	stc := &stateTestController{updatePollIntvl: 5 * time.Minute}

	s := NewFetchStoreRetryState(NewUpdateFetchState(update), update,
		errors.New("connection reset by peer"))
	ws := &waitStateTest{}
	s.(*fetchStoreRetryState).WaitState = ws

	next, cancelled := s.Handle(&ctx, stc)
	assert.False(t, cancelled)
	assert.IsType(t, &updateFetchState{}, next)
	assert.Equal(t, 1, ctx.fetchInstallAttempts)
}

func TestNewTestRetryAfterErrorUnwrapsToAPIError(t *testing.T) {
	// Sanity check on the fixture itself: errors.As must still be able to
	// reach the *client.APIError layer through *client.RetryAfterError,
	// exactly like real callers rely on (BUG-442's design goal).
	retryErr := newTestRetryAfterError(t, http.StatusServiceUnavailable, time.Second)

	var apiErr *client.APIError
	require.True(t, errors.As(error(retryErr), &apiErr))
	require.Same(t, retryErr.APIError, apiErr)
}
