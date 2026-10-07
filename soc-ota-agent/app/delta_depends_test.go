// Copyright 2026 OTA-Pulse
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

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TODO-054: a rootfs-image-delta artifact depends on the BASE image's
// rootfs-image.checksum. updateStoreState.maybeVerifyArtifactDependsAndProvides
// runs this check before StorePayloads, i.e. before any byte is written.
func TestVerifyArtifactDependencies_DeltaBaseChecksum(t *testing.T) {
	base := "1111111111111111111111111111111111111111111111111111111111111111"
	other := "2222222222222222222222222222222222222222222222222222222222222222"
	depends := map[string]interface{}{"rootfs-image.checksum": base}

	assert.NoError(t, verifyArtifactDependencies(depends,
		map[string]string{"rootfs-image.checksum": base, "artifact_name": "r1"}))

	err := verifyArtifactDependencies(depends,
		map[string]string{"rootfs-image.checksum": other, "artifact_name": "r1"})
	assert.Error(t, err, "delta built for another base must be rejected")
	assert.Contains(t, err.Error(), "rootfs-image.checksum")

	// A device that never recorded its rootfs checksum cannot take a delta.
	err = verifyArtifactDependencies(depends, map[string]string{"artifact_name": "r1"})
	assert.Error(t, err)
}
