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
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

// SoCMonitoringClient is an API client that uses X-API-Key and X-Device-ID
// headers for authentication with the SoC Monitoring backend.
// This replaces the standard Mender JWT-based authentication.
type SoCMonitoringClient struct {
	ApiClient
	// Guards the mutable credential fields (apiKey/deviceID) and the reload
	// bookkeeping below, since a single client instance is shared as both the
	// API and download requester and the API key can be swapped in mid-flight
	// by an on-disk reload (see reloadAPIKeyIfChanged).
	mu sync.Mutex
	// API key for authentication
	apiKey string
	// Device ID for identification
	deviceID string
	// Server URL
	serverURL ServerURL

	// keyReloadFn re-reads the current API key from its on-disk source. It is
	// an injectable field (like installer.fileBasedBootEnv.detectFn) so it is
	// unit-testable and so the client package need not import conf — conf
	// already imports client, so the reverse would be an import cycle. The
	// production reader is wired in by app.NewMender. A nil keyReloadFn
	// disables reload entirely: the client then behaves exactly as before with
	// no extra I/O on the happy path.
	keyReloadFn func() (string, error)
	// nowFn is the clock used for rate-limiting reloads; overridable in tests.
	nowFn func() time.Time
	// lastReloadAttempt is when keyReloadFn was last consulted. Used to
	// rate-limit disk reads so a persistently-rejected key cannot trigger a
	// filesystem read on every request.
	lastReloadAttempt time.Time
	// reloadMinInterval is the minimum gap between two on-disk key reads.
	reloadMinInterval time.Duration
}

// NewSoCMonitoringClient creates a new client configured for SoC Monitoring
// authentication using API key and device ID headers.
//
// keyReloadFn, when non-nil, is consulted after an HTTP 401 to re-read the
// current API key from its on-disk source; if it differs from the cached key
// the request is retried once with the reloaded key (see Do). Pass nil to
// disable that behavior (e.g. in tests that don't exercise it).
func NewSoCMonitoringClient(
	conf Config,
	apiKey string,
	deviceID string,
	serverURL string,
	keyReloadFn func() (string, error),
) (*SoCMonitoringClient, error) {
	client, err := NewApiClient(conf)
	if err != nil {
		return nil, err
	}

	return &SoCMonitoringClient{
		ApiClient:         *client,
		apiKey:            apiKey,
		deviceID:          deviceID,
		serverURL:         ServerURL(serverURL),
		keyReloadFn:       keyReloadFn,
		nowFn:             time.Now,
		reloadMinInterval: 10 * time.Second,
	}, nil
}

// reconstructRequest builds the request with SoC Monitoring auth headers
func (c *SoCMonitoringClient) reconstructRequest(req *http.Request) (*http.Request, error) {
	if c.serverURL == "" {
		return nil, errors.New("Server URL not configured")
	}

	serverURL, err := url.Parse(string(c.serverURL))
	if err != nil {
		return nil, errors.Wrap(err, "Could not parse ServerURL")
	}

	// Build the full URL
	newURL, _ := url.Parse(req.URL.String())
	newURL.Scheme = serverURL.Scheme
	newURL.Host = serverURL.Host
	newURL.Path = strings.TrimRight(serverURL.Path, "/") +
		"/" +
		strings.TrimLeft(req.URL.Path, "/")

	log.Debugf("SoCMonitoringClient: Connecting to %s", newURL.String())

	var body io.ReadCloser
	if req.GetBody != nil {
		body, err = req.GetBody()
		if err != nil {
			return nil, errors.Wrap(err, "Unable to reconstruct HTTP request body")
		}
	} else {
		body = nil
	}

	// Create new request
	newReq, err := http.NewRequest(req.Method, newURL.String(), body)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to create new request")
	}

	// Copy original headers
	for key, values := range req.Header {
		for _, value := range values {
			newReq.Header.Add(key, value)
		}
	}
	newReq.GetBody = req.GetBody

	// Set SoC Monitoring authentication headers. Read the credentials under
	// the lock since reloadAPIKeyIfChanged may swap apiKey concurrently.
	c.mu.Lock()
	apiKey := c.apiKey
	deviceID := c.deviceID
	c.mu.Unlock()
	newReq.Header.Set("X-API-Key", apiKey)
	newReq.Header.Set("X-Device-ID", deviceID)

	// Remove any Bearer token header (not used with SoC Monitoring)
	newReq.Header.Del("Authorization")

	return newReq, nil
}

// doRequest performs one authenticated round-trip. It logs the request and
// response but, unlike Do, does NOT translate auth-failure statuses into
// errors — that (and any one-shot API-key reload retry) is Do's job, so it can
// decide based on the response before deciding to retry.
func (c *SoCMonitoringClient) doRequest(req *http.Request) (*http.Response, error) {
	newReq, err := c.reconstructRequest(req)
	if err != nil {
		return nil, err
	}

	log.Infof("SoCMonitoringClient: Making request to %s %s", newReq.Method, newReq.URL.String())

	resp, err := c.ApiClient.Do(newReq)
	if err != nil {
		log.Errorf("SoCMonitoringClient: Request failed: %s", err.Error())
		return nil, err
	}

	// Log response details for debugging
	log.Infof("SoCMonitoringClient: Response status: %d, ContentLength: %d", resp.StatusCode, resp.ContentLength)
	log.Infof("SoCMonitoringClient: Response headers: %v", resp.Header)

	return resp, nil
}

// Do executes the HTTP request with SoC Monitoring authentication.
//
// This client caches the API key it was constructed with. If that key is
// rotated on disk while the agent keeps running (e.g. a re-provision rewrote
// otapulse.conf), every request would otherwise 401 until the process is
// restarted. To recover without a restart, on a 401 Do re-reads the key from
// its on-disk source once (rate-limited); if the key changed it adopts the new
// key and retries the request a single time. The happy path (2xx) does no
// extra work.
func (c *SoCMonitoringClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.doRequest(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized && c.reloadAPIKeyIfChanged() {
		log.Warn("SoCMonitoringClient: API key rotated on disk — reloaded and retrying request once")
		drainAndClose(resp)
		resp, err = c.doRequest(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			log.Warn("SoCMonitoringClient: still 401 after API key reload+retry — reloaded key was also rejected")
		}
	}

	// Surface auth failures as errors (unchanged behavior), now reflecting the
	// outcome after any reload+retry.
	if resp.StatusCode == http.StatusUnauthorized {
		log.Error("SoCMonitoringClient: Authentication failed - check API key and device ID")
		return resp, errors.New("Authentication failed: invalid API key or device ID")
	}

	if resp.StatusCode == http.StatusForbidden {
		log.Error("SoCMonitoringClient: Authorization failed - API key may lack required scopes")
		return resp, errors.New("Authorization failed: API key lacks required permissions")
	}

	return resp, nil
}

// reloadAPIKeyIfChanged re-reads the API key from its on-disk source, subject
// to reloadMinInterval between reads, and adopts it if it differs from the
// cached key. It returns true only when a new, non-empty key was adopted (so
// the caller should retry). It is safe to call with a nil keyReloadFn, in
// which case it always returns false and reads nothing.
func (c *SoCMonitoringClient) reloadAPIKeyIfChanged() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.keyReloadFn == nil {
		return false
	}

	now := c.nowFn()
	if !c.lastReloadAttempt.IsZero() && now.Sub(c.lastReloadAttempt) < c.reloadMinInterval {
		log.Debug("SoCMonitoringClient: skipping API key reload (rate-limited)")
		return false
	}
	// Count this as an attempt even if it fails or the key is unchanged, so a
	// persistently-invalid key can't force a disk read on every heartbeat.
	c.lastReloadAttempt = now

	newKey, err := c.keyReloadFn()
	if err != nil {
		log.Warnf("SoCMonitoringClient: failed to reload API key from disk: %s", err)
		return false
	}
	if newKey == "" || newKey == c.apiKey {
		return false
	}

	c.apiKey = newKey
	log.Info("SoCMonitoringClient: adopted rotated API key from disk")
	return true
}

// drainAndClose releases an HTTP response body so its connection can be reused
// before the request is retried.
func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// ClearAuthorization is a no-op for SoCMonitoringClient since we use
// static API key authentication instead of refreshable JWT tokens.
func (c *SoCMonitoringClient) ClearAuthorization() {
	// No-op: API key authentication doesn't need token refresh
	log.Debug("SoCMonitoringClient: ClearAuthorization called (no-op)")
}

// UpdateCredentials allows updating the API key and device ID
func (c *SoCMonitoringClient) UpdateCredentials(apiKey, deviceID string) {
	c.mu.Lock()
	c.apiKey = apiKey
	c.deviceID = deviceID
	c.mu.Unlock()
	log.Debugf("SoCMonitoringClient: Credentials updated for device %s", deviceID)
}

// GetServerURL returns the current server URL
func (c *SoCMonitoringClient) GetServerURL() ServerURL {
	return c.serverURL
}

// SetServerURL updates the server URL
func (c *SoCMonitoringClient) SetServerURL(serverURL string) {
	c.serverURL = ServerURL(serverURL)
}
