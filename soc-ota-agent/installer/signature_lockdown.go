// Copyright 2026 SoC Monitoring
//
// GAP-SEC-F2: consecutive-failure lockdown for artifact signature
// verification, as defense-in-depth on top of F1's fail-closed
// GetVerificationKeys (conf/config.go:385-421).
//
// This replaces an earlier design (client/signature_verifier.go.disabled +
// client/client_update_verified.go.disabled + conf/signature_config.go.disabled)
// that was never integrated: it imported a mender/signature package that does
// not exist anywhere in this module's dependency tree, and modeled firmware
// as a raw binary plus a detached ".sig" file fetched over bare HTTP — a
// download shape this system has never used. The real update flow (both
// daemon-managed and standalone) verifies a signature embedded inside the
// OTA artifact's own manifest via ReadHeaders' areader.Reader below, using
// the same artifact.NewPKIVerifier the agent has always used. This file adds
// lockdown tracking directly to that real, already-shipped verification
// path instead of building a second, parallel one.

package installer

import (
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

const (
	// DefaultMaxVerificationFailures is the number of consecutive signature
	// verification failures (within DefaultFailureWindow) that trip
	// lockdown.
	DefaultMaxVerificationFailures = 5

	// DefaultFailureWindow is the time window failures are counted within;
	// a failure older than this resets the counter instead of accumulating.
	DefaultFailureWindow = 24 * time.Hour

	// errNoSignatureFile is the vendored areader's error text when a
	// signed-mode reader (areader.NewReaderSigned) finds no signature
	// block at all (github.com/mendersoftware/mender-artifact/areader,
	// pinned by this module's go.mod — a future dependency bump could
	// change this wording, at which point isSignatureFailure below would
	// stop matching it and lockdown would simply not count that mode
	// until updated, not misfire).
	errNoSignatureFile = "expecting signed artifact, but no signature file found"

	// errSignatureNotVerified is installer.go's own VerifySignatureCallback
	// error text for a signature that doesn't verify against any
	// configured key — fully within this codebase's control.
	errSignatureNotVerified = "failed to verify message with any of the provided verification keys"
)

// isSignatureFailure reports whether err represents an actual signature
// verification failure (missing signature, or signature that doesn't
// verify) as opposed to any of the other errors ReadArtifactHeaders can
// return (device-type incompatibility, malformed artifact, unsupported
// update type, ...), which must not count toward lockdown.
func isSignatureFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, errNoSignatureFile) ||
		strings.Contains(msg, errSignatureNotVerified)
}

// signatureLockdown tracks consecutive artifact-signature verification
// failures across the process lifetime. State is in-memory only and resets
// on agent restart — persisting lockdown state across restarts is not
// implemented here (deliberately out of scope for this pass; the fail-closed
// GetVerificationKeys check in conf/config.go still applies on every start
// regardless of lockdown state).
type signatureLockdown struct {
	mu             sync.Mutex
	maxFailures    int
	window         time.Duration
	failureCount   int
	firstFailureAt time.Time
	active         bool
}

var defaultLockdown = &signatureLockdown{
	maxFailures: DefaultMaxVerificationFailures,
	window:      DefaultFailureWindow,
}

// checkNotLockedDown returns an error if lockdown is currently active.
// Called before attempting to read/install a new artifact.
func (l *signatureLockdown) checkNotLockedDown() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active {
		return errors.Errorf(
			"installer: signature verification lockdown active after %d consecutive failures — refusing further installs until ResetSignatureLockdown is called",
			l.failureCount)
	}
	return nil
}

// recordSuccess clears the failure count on a successful verification.
func (l *signatureLockdown) recordSuccess() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failureCount > 0 {
		log.Infof("installer: signature verification succeeded, resetting failure count (was %d)", l.failureCount)
	}
	l.failureCount = 0
}

// recordFailure increments the failure count (resetting it first if the
// previous failure fell outside the tracking window) and activates lockdown
// once the threshold is reached.
func (l *signatureLockdown) recordFailure() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if l.failureCount == 0 || now.Sub(l.firstFailureAt) > l.window {
		l.firstFailureAt = now
		l.failureCount = 0
	}
	l.failureCount++
	log.Warnf("installer: signature verification failure %d/%d within the tracking window", l.failureCount, l.maxFailures)

	if l.failureCount >= l.maxFailures {
		l.active = true
		log.Errorf(
			"installer: signature verification lockdown ACTIVATED after %d consecutive failures — further installs refused until manual reset",
			l.failureCount)
	}
}

// ResetSignatureLockdown clears the failure count and lockdown state. This
// is the manual-intervention / operator-recovery path referenced in the
// lockdown error message.
func ResetSignatureLockdown() {
	defaultLockdown.mu.Lock()
	defer defaultLockdown.mu.Unlock()
	defaultLockdown.failureCount = 0
	defaultLockdown.active = false
	log.Info("installer: signature verification lockdown reset")
}

// SignatureLockdownActive reports whether installs are currently refused
// due to repeated signature-verification failures.
func SignatureLockdownActive() bool {
	defaultLockdown.mu.Lock()
	defer defaultLockdown.mu.Unlock()
	return defaultLockdown.active
}

// SignatureFailureCount reports the current consecutive-failure count.
func SignatureFailureCount() int {
	defaultLockdown.mu.Lock()
	defer defaultLockdown.mu.Unlock()
	return defaultLockdown.failureCount
}
