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

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

// SoCMonitoringClient is an API client that uses X-API-Key and X-Device-ID
// headers for authentication with the SoC Monitoring backend.
// This replaces the standard Mender JWT-based authentication.
type SoCMonitoringClient struct {
	ApiClient
	// API key for authentication
	apiKey string
	// Device ID for identification
	deviceID string
	// Server URL
	serverURL ServerURL
}

// NewSoCMonitoringClient creates a new client configured for SoC Monitoring
// authentication using API key and device ID headers.
func NewSoCMonitoringClient(
	conf Config,
	apiKey string,
	deviceID string,
	serverURL string,
) (*SoCMonitoringClient, error) {
	client, err := NewApiClient(conf)
	if err != nil {
		return nil, err
	}

	return &SoCMonitoringClient{
		ApiClient: *client,
		apiKey:    apiKey,
		deviceID:  deviceID,
		serverURL: ServerURL(serverURL),
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

	// Set SoC Monitoring authentication headers
	newReq.Header.Set("X-API-Key", c.apiKey)
	newReq.Header.Set("X-Device-ID", c.deviceID)

	// Remove any Bearer token header (not used with SoC Monitoring)
	newReq.Header.Del("Authorization")

	return newReq, nil
}

// Do executes the HTTP request with SoC Monitoring authentication
func (c *SoCMonitoringClient) Do(req *http.Request) (*http.Response, error) {
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

	// Handle authentication errors
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

// ClearAuthorization is a no-op for SoCMonitoringClient since we use
// static API key authentication instead of refreshable JWT tokens.
func (c *SoCMonitoringClient) ClearAuthorization() {
	// No-op: API key authentication doesn't need token refresh
	log.Debug("SoCMonitoringClient: ClearAuthorization called (no-op)")
}

// UpdateCredentials allows updating the API key and device ID
func (c *SoCMonitoringClient) UpdateCredentials(apiKey, deviceID string) {
	c.apiKey = apiKey
	c.deviceID = deviceID
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
