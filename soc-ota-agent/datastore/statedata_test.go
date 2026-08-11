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
package datastore

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMenderState(t *testing.T) {
	d, err := json.Marshal(MenderStateInit)

	assert.Equal(t, []byte(`"init"`), d)
	assert.NoError(t, err)

	d, err = json.Marshal(MenderState(333))
	assert.Error(t, err)
	assert.Empty(t, d)

	var s MenderState
	err = json.Unmarshal([]byte(`"init"`), &s)

	assert.NoError(t, err)
	assert.Equal(t, MenderStateInit, s)
}

// TestUpdateInfoStatusReportRetryAttemptsRoundTrip covers BUG-283's
// persisted sending-attempt budget: the field must round-trip through the
// same JSON marshal/unmarshal path used by StoreStateData/LoadStateData.
func TestUpdateInfoStatusReportRetryAttemptsRoundTrip(t *testing.T) {
	orig := UpdateInfo{
		ID:                        "deployment-1",
		StatusReportRetryAttempts: 7,
	}

	data, err := json.Marshal(StateData{
		Version:    StateDataVersion,
		Name:       MenderStateStatusReportRetry,
		UpdateInfo: orig,
	})
	assert.NoError(t, err)

	var restored StateData
	err = json.Unmarshal(data, &restored)
	assert.NoError(t, err)
	assert.Equal(t, 7, restored.UpdateInfo.StatusReportRetryAttempts)
}

// TestUpdateInfoStatusReportRetryAttemptsBackwardCompat covers BUG-283's
// backward-compatibility requirement: state data persisted by an agent
// build that predates this field (and therefore never wrote it) must still
// load, with the new field defaulting to zero rather than erroring or
// leaving garbage.
func TestUpdateInfoStatusReportRetryAttemptsBackwardCompat(t *testing.T) {
	// A pre-fix StateData blob for an update-retry-report state, built by
	// hand to guarantee it has no "StatusReportRetryAttempts" key at all
	// (not just a zero value) -- this is what an old agent's datastore
	// actually contains.
	preFixBlob := []byte(`{
		"Version": 2,
		"Name": "update-retry-report",
		"UpdateInfo": {
			"Artifact": {"PayloadTypes": ["rootfs-image"]},
			"ID": "deployment-1",
			"RebootRequested": ["reboot-type-custom"],
			"SupportsRollback": "rollback-supported",
			"StateDataStoreCount": 3,
			"HasDBSchemaUpdate": false
		}
	}`)

	var restored StateData
	err := json.Unmarshal(preFixBlob, &restored)
	assert.NoError(t, err)
	assert.Equal(t, "deployment-1", restored.UpdateInfo.ID)
	assert.Equal(t, 0, restored.UpdateInfo.StatusReportRetryAttempts)
}
