// Copyright 2026 SoC Monitoring
package installer

import (
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/binaryblack/OTA-Pulse/conf"
)

// wrongVerificationKey never matches PrivateRSAKey (the key MakeRootfsImageArtifact
// signs with) — PublicRSAKey2 is the same "known mismatched key" installer_test.go
// itself uses to exercise the fallback-to-next-key path in TestInstallSigned.
var wrongVerificationKey = []*conf.VerificationKey{
	{Data: []byte(PublicRSAKey2), Path: "/path/to/wrong_key"},
}

func TestSignatureLockdown_TripsAfterMaxConsecutiveFailures(t *testing.T) {
	t.Cleanup(ResetSignatureLockdown)
	ResetSignatureLockdown()
	updateProducers := AllModules{DualRootfs: new(fDevice)}

	for i := 1; i <= DefaultMaxVerificationFailures; i++ {
		art, err := MakeRootfsImageArtifact(2, true, false)
		require.NoError(t, err)
		_, err = Install(art, "vexpress-qemu", wrongVerificationKey, "", &updateProducers)
		require.Error(t, err)

		if i < DefaultMaxVerificationFailures {
			assert.Falsef(t, SignatureLockdownActive(),
				"must not be locked down yet (%d/%d failures)", i, DefaultMaxVerificationFailures)
		}
	}

	assert.True(t, SignatureLockdownActive(), "must be locked down after the threshold is reached")
	assert.Equal(t, DefaultMaxVerificationFailures, SignatureFailureCount())
}

func TestSignatureLockdown_ActiveLockdownRefusesFurtherInstalls(t *testing.T) {
	t.Cleanup(ResetSignatureLockdown)
	ResetSignatureLockdown()
	updateProducers := AllModules{DualRootfs: new(fDevice)}

	for i := 0; i < DefaultMaxVerificationFailures; i++ {
		art, err := MakeRootfsImageArtifact(2, true, false)
		require.NoError(t, err)
		_, _ = Install(art, "vexpress-qemu", wrongVerificationKey, "", &updateProducers)
	}
	require.True(t, SignatureLockdownActive())

	// Even a correctly-signed artifact with valid keys must now be refused
	// outright — lockdown gates ReadHeaders before it ever opens the
	// artifact.
	art, err := MakeRootfsImageArtifact(2, true, false)
	require.NoError(t, err)
	_, err = Install(art, "vexpress-qemu", testVerificationKeys, "", &updateProducers)
	require.Error(t, err)
	assert.Contains(t, errors.Cause(err).Error(), "lockdown active")
}

func TestSignatureLockdown_ResetClearsLockdownAndAllowsInstall(t *testing.T) {
	t.Cleanup(ResetSignatureLockdown)
	updateProducers := AllModules{DualRootfs: new(fDevice)}

	for i := 0; i < DefaultMaxVerificationFailures; i++ {
		art, err := MakeRootfsImageArtifact(2, true, false)
		require.NoError(t, err)
		_, _ = Install(art, "vexpress-qemu", wrongVerificationKey, "", &updateProducers)
	}
	require.True(t, SignatureLockdownActive())

	ResetSignatureLockdown()
	assert.False(t, SignatureLockdownActive())
	assert.Equal(t, 0, SignatureFailureCount())

	art, err := MakeRootfsImageArtifact(2, true, false)
	require.NoError(t, err)
	_, err = Install(art, "vexpress-qemu", testVerificationKeys, "", &updateProducers)
	assert.NoError(t, err)
}

func TestSignatureLockdown_SuccessResetsFailureCount(t *testing.T) {
	t.Cleanup(ResetSignatureLockdown)
	ResetSignatureLockdown()
	updateProducers := AllModules{DualRootfs: new(fDevice)}

	for i := 0; i < DefaultMaxVerificationFailures-1; i++ {
		art, err := MakeRootfsImageArtifact(2, true, false)
		require.NoError(t, err)
		_, err = Install(art, "vexpress-qemu", wrongVerificationKey, "", &updateProducers)
		require.Error(t, err)
	}
	require.Equal(t, DefaultMaxVerificationFailures-1, SignatureFailureCount())
	require.False(t, SignatureLockdownActive())

	art, err := MakeRootfsImageArtifact(2, true, false)
	require.NoError(t, err)
	_, err = Install(art, "vexpress-qemu", testVerificationKeys, "", &updateProducers)
	require.NoError(t, err)

	assert.Equal(t, 0, SignatureFailureCount(),
		"a successful verification must reset the counter, not just leave it below threshold")
}

func TestSignatureLockdown_DeviceIncompatibilityDoesNotCountAsFailure(t *testing.T) {
	t.Cleanup(ResetSignatureLockdown)
	ResetSignatureLockdown()
	updateProducers := AllModules{DualRootfs: new(fDevice)}

	art, err := MakeRootfsImageArtifact(2, true, false)
	require.NoError(t, err)
	_, err = Install(art, "fake-device", testVerificationKeys, "", &updateProducers)
	require.Error(t, err)
	assert.Contains(t, errors.Cause(err).Error(), "not compatible with device")

	assert.Equal(t, 0, SignatureFailureCount(),
		"a device-type mismatch is not a signature failure and must not count toward lockdown")
	assert.False(t, SignatureLockdownActive())
}

func TestSignatureLockdown_MissingSignatureCountsAsFailure(t *testing.T) {
	t.Cleanup(ResetSignatureLockdown)
	ResetSignatureLockdown()
	updateProducers := AllModules{DualRootfs: new(fDevice)}

	art, err := MakeRootfsImageArtifact(2, false, false)
	require.NoError(t, err)
	_, err = Install(art, "vexpress-qemu", testVerificationKeys, "", &updateProducers)
	require.Error(t, err)
	assert.Contains(t, errors.Cause(err).Error(), "expecting signed artifact")

	assert.Equal(t, 1, SignatureFailureCount(),
		"an artifact with no signature at all is a verification failure and must count")
}

// --- Direct unit tests of the lockdown struct, isolated from the Install()
// path — DefaultFailureWindow is 24h, far too long to exercise through a
// real Install() loop, so these construct their own short-window instance.

func TestSignatureLockdownStruct_FailureOutsideWindowResetsCounter(t *testing.T) {
	l := &signatureLockdown{maxFailures: 3, window: 15 * time.Millisecond}

	l.recordFailure()
	l.recordFailure()
	assert.Equal(t, 2, l.failureCount)
	assert.False(t, l.active)

	time.Sleep(30 * time.Millisecond)

	l.recordFailure() // outside the window: restarts the count instead of reaching 3
	assert.Equal(t, 1, l.failureCount)
	assert.False(t, l.active)
}

func TestSignatureLockdownStruct_CheckNotLockedDown(t *testing.T) {
	l := &signatureLockdown{maxFailures: 1, window: time.Hour}
	assert.NoError(t, l.checkNotLockedDown())

	l.recordFailure()
	assert.True(t, l.active)

	err := l.checkNotLockedDown()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lockdown active")
}

func TestIsSignatureFailure(t *testing.T) {
	assert.False(t, isSignatureFailure(nil))
	assert.True(t, isSignatureFailure(errors.New(
		"reader: expecting signed artifact, but no signature file found")))
	assert.True(t, isSignatureFailure(errors.Wrap(errors.New(
		"failed to verify message with any of the provided verification keys"), "readHeaderV3")))
	assert.False(t, isSignatureFailure(errors.New(
		"installer: image (device types [vexpress-qemu]) not compatible with device fake-device")))
	assert.False(t, isSignatureFailure(errors.New("some unrelated I/O error")))
}
