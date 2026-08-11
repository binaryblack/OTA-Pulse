// Copyright 2023 Northern.tech AS
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
package client

import (
	"io/ioutil"
	"net/http"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

func TestStatusClient(t *testing.T) {
	responder := &struct {
		httpStatus int
		recdata    []byte
		path       string
	}{
		http.StatusNoContent, // 204
		[]byte{},
		"",
	}

	// Test server that always responds with 200 code, and specific payload
	ts := startTestHTTPS(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(responder.httpStatus)

			responder.recdata, _ = ioutil.ReadAll(r.Body)
			responder.path = r.URL.Path
		}),
		localhostCert,
		localhostKey)
	defer ts.Close()

	ac, err := NewApiClient(
		Config{ServerCert: "testdata/server.crt"},
	)
	assert.NotNil(t, ac)
	assert.NoError(t, err)

	client := NewStatus()
	assert.NotNil(t, client)

	err = client.Report(NewMockApiClient(nil, errors.New("foo")),
		ts.URL,
		StatusReport{
			DeploymentID: "deployment1",
			Status:       StatusFailure,
		})
	assert.Error(t, err)
	assert.NotEqual(t, err, ErrDeploymentAborted)

	err = client.Report(ac, ts.URL, StatusReport{
		DeploymentID: "deployment1",
		Status:       StatusFailure,
	})
	assert.NoError(t, err)
	assert.NotNil(t, responder.recdata)
	assert.JSONEq(t, `{"status": "failure"}`, string(responder.recdata))
	assert.Equal(
		t,
		apiPrefix+"v1/deployments/device/deployments/deployment1/status",
		responder.path,
	)

	responder.httpStatus = http.StatusUnauthorized
	err = client.Report(ac, ts.URL, StatusReport{
		DeploymentID: "deployment1",
		Status:       StatusSuccess,
	})
	assert.Error(t, err)
	assert.NotEqual(t, err, ErrDeploymentAborted)

	responder.httpStatus = http.StatusConflict
	err = client.Report(ac, ts.URL, StatusReport{
		DeploymentID: "deployment1",
		Status:       StatusSuccess,
	})
	errCause := errors.Cause(err)
	assert.Equal(t, errCause, ErrDeploymentAborted)

	// BUG-283: a 404 for a deployment that no longer exists server-side
	// must be classified the same as an explicit 409 abort — permanent,
	// not transient — so the caller does not burn its retry budget on a
	// deployment that can never come back.
	responder.httpStatus = http.StatusNotFound
	err = client.Report(ac, ts.URL, StatusReport{
		DeploymentID: "deployment1",
		Status:       StatusSuccess,
	})
	errCause = errors.Cause(err)
	assert.Equal(t, errCause, ErrDeploymentAborted)

	// BUG-283: same for 410 Gone, which some backends use to explicitly
	// distinguish "deleted" from "never existed".
	responder.httpStatus = http.StatusGone
	err = client.Report(ac, ts.URL, StatusReport{
		DeploymentID: "deployment1",
		Status:       StatusSuccess,
	})
	errCause = errors.Cause(err)
	assert.Equal(t, errCause, ErrDeploymentAborted)

	// A 500 remains a plain transient error — not reclassified as
	// permanent/aborted, so the existing retry behavior is unchanged.
	responder.httpStatus = http.StatusInternalServerError
	err = client.Report(ac, ts.URL, StatusReport{
		DeploymentID: "deployment1",
		Status:       StatusSuccess,
	})
	assert.Error(t, err)
	errCause = errors.Cause(err)
	assert.NotEqual(t, errCause, ErrDeploymentAborted)
}
