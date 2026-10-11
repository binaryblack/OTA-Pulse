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

// --- GC-AG-10: auto-expiry of an active lockdown.

// fakeLockdownClock is an injectable clock for expiry tests.
type fakeLockdownClock struct{ t time.Time }

func (c *fakeLockdownClock) now() time.Time          { return c.t }
func (c *fakeLockdownClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newExpiringLockdown(cooldown time.Duration) (*signatureLockdown, *fakeLockdownClock) {
	clk := &fakeLockdownClock{t: time.Unix(1_700_000_000, 0)}
	return &signatureLockdown{
		maxFailures: 3,
		window:      24 * time.Hour,
		cooldown:    cooldown,
		now:         clk.now,
	}, clk
}

func TestSignatureLockdownStruct_ExpiresAfterCooldown(t *testing.T) {
	l, clk := newExpiringLockdown(time.Hour)
	for i := 0; i < 3; i++ {
		l.recordFailure()
	}
	require.True(t, l.active)

	clk.advance(59 * time.Minute)
	err := l.checkNotLockedDown()
	require.Error(t, err, "must still be locked down before the cool-down elapses")
	assert.Contains(t, err.Error(), "lockdown active")
	assert.Contains(t, err.Error(), "expires automatically")

	clk.advance(2 * time.Minute)
	assert.NoError(t, l.checkNotLockedDown(), "lockdown must expire after the cool-down")
	assert.False(t, l.active)
}

func TestSignatureLockdownStruct_ExpiryIsHalfOpenAndRelocksOnNextFailure(t *testing.T) {
	// Expiry must never be a free pass: one more verification failure after
	// expiry re-activates lockdown for a full new cool-down.
	l, clk := newExpiringLockdown(time.Hour)
	for i := 0; i < 3; i++ {
		l.recordFailure()
	}
	clk.advance(time.Hour)
	require.NoError(t, l.checkNotLockedDown())
	assert.Equal(t, 2, l.failureCount, "expiry must leave the counter one short of the threshold")

	l.recordFailure()
	assert.True(t, l.active, "a single failure after expiry must re-lock")
	clk.advance(30 * time.Minute)
	assert.Error(t, l.checkNotLockedDown(), "the new cool-down must start from re-activation")
	clk.advance(31 * time.Minute)
	assert.NoError(t, l.checkNotLockedDown())
}

func TestSignatureLockdownStruct_SuccessAfterExpiryClearsCounter(t *testing.T) {
	l, clk := newExpiringLockdown(time.Hour)
	for i := 0; i < 3; i++ {
		l.recordFailure()
	}
	clk.advance(time.Hour)
	require.NoError(t, l.checkNotLockedDown())
	l.recordSuccess()
	assert.Equal(t, 0, l.failureCount)
}

func TestSignatureLockdownStruct_ZeroCooldownNeverExpires(t *testing.T) {
	l, clk := newExpiringLockdown(0)
	for i := 0; i < 3; i++ {
		l.recordFailure()
	}
	clk.advance(1000 * time.Hour)
	assert.Error(t, l.checkNotLockedDown(), "cool-down 0 keeps the legacy restart-only reset")
	assert.True(t, l.active)
}

func TestSignatureLockdown_ExpiredLockdownStillVerifiesSignatures(t *testing.T) {
	// End to end through Install(): after expiry a BAD signature is still
	// rejected (and re-locks); only a GOOD signature installs.
	t.Cleanup(ResetSignatureLockdown)
	t.Cleanup(func() { SetSignatureLockdownCooldown(DefaultLockdownCooldown) })
	ResetSignatureLockdown()
	updateProducers := AllModules{DualRootfs: new(fDevice)}

	clk := &fakeLockdownClock{t: time.Now()}
	defaultLockdown.mu.Lock()
	defaultLockdown.now = clk.now
	defaultLockdown.mu.Unlock()
	t.Cleanup(func() {
		defaultLockdown.mu.Lock()
		defaultLockdown.now = nil
		defaultLockdown.mu.Unlock()
	})
	SetSignatureLockdownCooldown(time.Hour)

	for i := 0; i < DefaultMaxVerificationFailures; i++ {
		art, err := MakeRootfsImageArtifact(2, true, false)
		require.NoError(t, err)
		_, _ = Install(art, "vexpress-qemu", wrongVerificationKey, "", &updateProducers)
	}
	require.True(t, SignatureLockdownActive())

	clk.advance(time.Hour + time.Second)
	require.False(t, SignatureLockdownActive(), "lockdown must have expired")

	// A bad signature is still refused, and re-locks at once.
	art, err := MakeRootfsImageArtifact(2, true, false)
	require.NoError(t, err)
	_, err = Install(art, "vexpress-qemu", wrongVerificationKey, "", &updateProducers)
	require.Error(t, err)
	assert.True(t, SignatureLockdownActive(), "one failure after expiry must re-lock")

	// Expire again; now a correctly signed artifact installs.
	clk.advance(time.Hour + time.Second)
	art, err = MakeRootfsImageArtifact(2, true, false)
	require.NoError(t, err)
	_, err = Install(art, "vexpress-qemu", testVerificationKeys, "", &updateProducers)
	assert.NoError(t, err)
	assert.Equal(t, 0, SignatureFailureCount())
}

func TestLockdownCooldownFromEnv(t *testing.T) {
	t.Setenv(LockdownCooldownEnv, "")
	assert.Equal(t, DefaultLockdownCooldown, lockdownCooldownFromEnv())

	t.Setenv(LockdownCooldownEnv, "30m")
	assert.Equal(t, 30*time.Minute, lockdownCooldownFromEnv())

	t.Setenv(LockdownCooldownEnv, "0")
	assert.Equal(t, time.Duration(0), lockdownCooldownFromEnv(), "0 disables auto-expiry")

	for _, bad := range []string{"banana", "-5m", "10"} {
		t.Setenv(LockdownCooldownEnv, bad)
		assert.Equal(t, DefaultLockdownCooldown, lockdownCooldownFromEnv(),
			"invalid value %q must fall back to the default", bad)
	}
}
