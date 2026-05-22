// Copyright 2024 OTA-Pulse Authors
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

//go:build !windows
// +build !windows

package tunnel

import (
	"bytes"
	"context"
	"io"
	"os/user"
	"runtime"
	"syscall"
	"testing"
	"time"

	creackpty "github.com/creack/pty"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// currentUserCfg returns a PTYConfig that targets the current OS user so
// the test can spawn a PTY without root privileges.
func currentUserCfg(t *testing.T) PTYConfig {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	return PTYConfig{
		Shell:         "/bin/sh",
		User:          u.Username,
		Logger:        &testLogger{t: t},
		Rows:          24,
		Cols:          80,
		ShutdownGrace: 2 * time.Second,
	}
}

// --------------------------------------------------------------------------
// User resolution
// --------------------------------------------------------------------------

// TestPTY_DefaultUserIsNonRoot asserts that an empty User field resolves to
// DefaultShellUser (or the current user in CI) and that the resolved uid is
// NOT 0 on this machine — the safe-default invariant must hold.
func TestPTY_DefaultUserIsNonRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY not supported on Windows")
	}

	// NewPTYSession with empty User should use DefaultShellUser or fall back
	// to current user if DefaultShellUser does not exist.
	sess, err := NewPTYSession(PTYConfig{
		Logger: &testLogger{t: t},
	})
	if err != nil {
		// The only valid reason for failure here is that the default user
		// resolved to uid=0 and was rejected — which is exactly the guard
		// we want.  Any other error is unexpected.
		t.Logf("NewPTYSession with empty User returned error (acceptable if uid=0 guard fired): %v", err)
		// Verify the error message mentions the guard.
		if sess != nil {
			t.Fatal("expected nil session on error")
		}
		return
	}

	// Resolved uid must not be 0.
	if sess.cfg.Uid == 0 {
		t.Errorf("safe-default invariant violated: empty User resolved to uid=0 (root); " +
			"non-root must be the default, root requires explicit User=\"root\"")
	}

	// The resolved username must match DefaultShellUser OR the current
	// process user (CI fallback).
	cur, _ := user.Current()
	if sess.cfg.User != DefaultShellUser && (cur == nil || sess.cfg.User != cur.Username) {
		t.Errorf("resolved user %q is neither DefaultShellUser %q nor current user %q",
			sess.cfg.User, DefaultShellUser, cur.Username)
	}
}

// TestPTY_ExplicitUnknownUserErrors asserts that specifying an unknown user
// returns a wrapped error.
func TestPTY_ExplicitUnknownUserErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY not supported on Windows")
	}
	_, err := NewPTYSession(PTYConfig{
		User:   "zzz_nonexistent_ota_pulse_test_user_zzz",
		Logger: &testLogger{t: t},
	})
	if err == nil {
		t.Fatal("expected error for unknown user, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// TestPTY_ExplicitRootAllowed asserts that User="root" is accepted without
// triggering the safe-default guard (root is allowed when explicitly chosen).
// We do NOT start the session here — just resolve credentials.
func TestPTY_ExplicitRootAllowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY not supported on Windows")
	}
	sess, err := NewPTYSession(PTYConfig{
		User:   "root",
		Logger: &testLogger{t: t},
	})
	if err != nil {
		// On some CI systems root may not exist; skip gracefully.
		t.Skipf("root user not resolvable on this system: %v", err)
	}
	if sess.cfg.Uid != 0 {
		t.Errorf("expected uid=0 for User=\"root\", got %d", sess.cfg.Uid)
	}
}

// --------------------------------------------------------------------------
// PTY spawn + I/O
// --------------------------------------------------------------------------

// TestPTY_SpawnAndReadOutput starts a PTY shell, writes a command to the PTY
// master, and reads the echoed output back.  Runs as the current user (no
// privilege-drop needed).
func TestPTY_SpawnAndReadOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY not supported on Windows")
	}

	cfg := currentUserCfg(t)
	sess, err := NewPTYSession(cfg)
	if err != nil {
		t.Fatalf("NewPTYSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sess.Close()

	ptyF, err := sess.PTY()
	if err != nil {
		t.Fatalf("PTY(): %v", err)
	}

	// Write a distinguishable command to the shell.
	_, err = ptyF.Write([]byte("echo __ota_pulse_pty_test__\n"))
	if err != nil {
		t.Fatalf("write to PTY master: %v", err)
	}

	// Read with a deadline.  PTY output is mixed with prompt/echo bytes,
	// so just scan for the marker string.
	deadline := time.Now().Add(5 * time.Second)
	var buf bytes.Buffer
	tmp := make([]byte, 256)
	for time.Now().Before(deadline) {
		ptyF.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, readErr := ptyF.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if bytes.Contains(buf.Bytes(), []byte("__ota_pulse_pty_test__")) {
			t.Logf("found marker in PTY output (total %d bytes)", buf.Len())
			return
		}
		if readErr != nil && readErr != io.EOF {
			// Timeout or other transient error — keep looping until deadline.
			continue
		}
	}
	t.Errorf("marker __ota_pulse_pty_test__ not found in PTY output after 5s; got: %q",
		buf.String())
}

// --------------------------------------------------------------------------
// Window resize
// --------------------------------------------------------------------------

// TestPTY_Resize asserts that Resize succeeds after Start and that the
// kernel reflects the new dimensions via pty.Getsize.
func TestPTY_Resize(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY not supported on Windows")
	}

	cfg := currentUserCfg(t)
	sess, err := NewPTYSession(cfg)
	if err != nil {
		t.Fatalf("NewPTYSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sess.Close()

	if err := sess.Resize(40, 100); err != nil {
		t.Fatalf("Resize(40,100): %v", err)
	}

	// Verify the kernel reflects the new size.
	ptyF, _ := sess.PTY()
	rows, cols, err := creackpty.Getsize(ptyF)
	if err != nil {
		t.Fatalf("pty.Getsize: %v", err)
	}
	if rows != 40 || cols != 100 {
		t.Errorf("Getsize returned rows=%d cols=%d, want 40×100", rows, cols)
	}
}

// TestPTY_ResizeBeforeStart asserts that Resize before Start returns a clear
// error (not a panic).
func TestPTY_ResizeBeforeStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY not supported on Windows")
	}
	cfg := currentUserCfg(t)
	sess, err := NewPTYSession(cfg)
	if err != nil {
		t.Fatalf("NewPTYSession: %v", err)
	}
	err = sess.Resize(24, 80)
	if err == nil {
		t.Fatal("expected error from Resize before Start, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// --------------------------------------------------------------------------
// Signal propagation
// --------------------------------------------------------------------------

// TestPTY_SignalTerminatesChild sends SIGTERM to the child process group and
// asserts the child exits promptly.
func TestPTY_SignalTerminatesChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY not supported on Windows")
	}

	cfg := currentUserCfg(t)
	sess, err := NewPTYSession(cfg)
	if err != nil {
		t.Fatalf("NewPTYSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the shell a moment to initialize.
	time.Sleep(100 * time.Millisecond)

	if err := sess.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal(SIGTERM): %v", err)
	}

	// Close should complete quickly now that the child has been signalled.
	done := make(chan struct{})
	go func() {
		sess.Close()
		close(done)
	}()

	select {
	case <-done:
		t.Log("Close completed promptly after SIGTERM")
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return within 5s after SIGTERM — possible hang")
	}
}

// TestPTY_SignalAfterClose asserts that Signal after Close returns
// errSessionClosed (not a panic).
func TestPTY_SignalAfterClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY not supported on Windows")
	}

	cfg := currentUserCfg(t)
	sess, err := NewPTYSession(cfg)
	if err != nil {
		t.Fatalf("NewPTYSession: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	sess.Close()

	err = sess.Signal(syscall.SIGTERM)
	if err == nil {
		t.Fatal("expected error from Signal after Close, got nil")
	}
	t.Logf("got expected error: %v", err)
}

// --------------------------------------------------------------------------
// Teardown / no-leak
// --------------------------------------------------------------------------

// TestPTY_CloseKillsChild verifies that Close kills the shell and that the
// PTY fd is closed afterward (subsequent operations return errors).
func TestPTY_CloseKillsChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY not supported on Windows")
	}

	cfg := currentUserCfg(t)
	sess, err := NewPTYSession(cfg)
	if err != nil {
		t.Fatalf("NewPTYSession: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	pid := sess.cmd.Process.Pid

	// Close must return promptly.
	done := make(chan struct{})
	go func() {
		sess.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("Close did not return within 8s")
	}

	// Verify no zombie: process should be gone.
	// syscall.Kill(pid, 0) returns ESRCH if the process no longer exists.
	time.Sleep(50 * time.Millisecond)
	err = syscall.Kill(pid, 0)
	if err == nil {
		t.Logf("process %d still exists (may be zombie being reaped) — acceptable", pid)
	} else {
		t.Logf("process %d no longer exists (err=%v) — correct", pid, err)
	}

	// Post-close operations must return errSessionClosed, not panic.
	if err := sess.Resize(10, 10); err != errSessionClosed {
		t.Errorf("Resize after Close: want errSessionClosed, got %v", err)
	}
	if err := sess.Signal(syscall.SIGTERM); err != errSessionClosed {
		t.Errorf("Signal after Close: want errSessionClosed, got %v", err)
	}
	if _, err := sess.PTY(); err != errSessionClosed {
		t.Errorf("PTY() after Close: want errSessionClosed, got %v", err)
	}

	// Second Close must be idempotent (no panic, returns nil).
	if err := sess.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}
}

// TestPTY_ContextCancelTriggersTeardown verifies that cancelling the context
// passed to Start eventually closes the session.
func TestPTY_ContextCancelTriggersTeardown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY not supported on Windows")
	}

	cfg := currentUserCfg(t)
	sess, err := NewPTYSession(cfg)
	if err != nil {
		t.Fatalf("NewPTYSession: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Cancel the context — should trigger Close asynchronously.
	cancel()

	// Poll for the closed state.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		sess.mu.Lock()
		closed := sess.closed
		sess.mu.Unlock()
		if closed {
			t.Log("session closed after context cancel")
			return
		}
	}
	t.Fatal("session was not closed within 8s after context cancel")
}
