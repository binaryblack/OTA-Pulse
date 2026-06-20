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

// Package tunnel provides PTY session management for remote-shell access
// on embedded Linux devices.  This file (pty.go) implements the device-side
// PTY mechanics: spawning an interactive shell as a non-privileged OS user,
// window-resize, signal propagation, and clean teardown.
//
// Security model
// ==============
// The tier→user mapping is:
//
//	Standard   → "support"  (non-root, the safe default)
//	Elevated   → "root"
//	Break-Glass → "root"
//
// The agent process runs as root and drops privileges to the requested user
// just before exec.  Root must be an EXPLICIT, DELIBERATE choice by the
// caller — an empty/zero User field NEVER silently produces root.  The
// server enforces who may request which tier (role+group AND-gate) in a
// later sprint; here we only enforce the safe-default invariant.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"sync"
	"syscall"
	"time"

	creackpty "github.com/creack/pty"
)

// DefaultShellUser is the non-root OS user that PTY sessions run as when
// the caller does not specify a user.  Embedding teams should override this
// via PTYConfig.User; this constant is the safe fallback.
const DefaultShellUser = "support"

// defaultShutdownGrace is how long PTYSession.Close waits for SIGTERM to
// cleanly kill the child before escalating to SIGKILL.
const defaultShutdownGrace = 5 * time.Second

// errSessionClosed is returned by Resize/Signal/PTY after Close.
var errSessionClosed = errors.New("tunnel/pty: session is closed")

// errSessionNotStarted is returned by Resize/Signal/PTY before Start.
var errSessionNotStarted = errors.New("tunnel/pty: session has not been started")

// PTYConfig holds all configuration needed to start a PTY session.
//
// Caller checklist:
//   - Leave User empty to use DefaultShellUser (non-root).
//   - Set User to "root" only when the tier explicitly requires root.
//   - Never set Uid/Gid directly — call NewPTYSession which resolves them
//     from the username.
type PTYConfig struct {
	// Shell is the path to the shell binary.  Defaults to /bin/sh.
	Shell string

	// User is the OS username to run the shell as.  Empty means
	// DefaultShellUser (currently "support"), NOT root.  To run as root
	// the caller must pass "root" explicitly.
	User string

	// Uid and Gid are resolved by NewPTYSession from User.  Callers must
	// not set these directly; they are exported only so tests can inspect
	// the resolved values.
	Uid uint32
	Gid uint32

	// Home is the resolved home directory for the user (used for $HOME).
	Home string

	// Env is appended to the minimal base environment (HOME, USER, SHELL,
	// TERM).  If nil the base set is used unchanged.
	Env []string

	// Logger receives diagnostic messages.  If nil, a no-op logger is used.
	Logger Logger

	// Rows and Cols are the initial PTY window dimensions.
	// Zero values default to 24 rows × 80 cols.
	Rows uint16
	Cols uint16

	// ShutdownGrace is how long Close waits after SIGTERM before SIGKILL.
	// Zero defaults to defaultShutdownGrace (5 s).
	ShutdownGrace time.Duration

	// skipCredential is set internally when the resolved uid/gid matches
	// the current process uid/gid, allowing CI tests to spawn a PTY
	// without needing root.  It is never set by external callers.
	skipCredential bool
}

// NewPTYSession resolves username to uid/gid/home and returns a PTYSession
// ready to Start.
//
// username handling:
//   - "" → resolved as DefaultShellUser ("support").  If that account does
//     not exist the function falls back to the current process user and
//     sets skipCredential so the session can start without privileges.
//   - non-empty → looked up exactly; returns an error if not found.
//
// Guard: if the resolved uid is 0 AND the caller passed "" or
// DefaultShellUser, NewPTYSession returns an error — root must be opted in
// explicitly by passing "root".
func NewPTYSession(cfg PTYConfig) (*PTYSession, error) {
	if cfg.Shell == "" {
		cfg.Shell = "/bin/sh"
	}
	if cfg.Logger == nil {
		cfg.Logger = noopLogger{}
	}
	if cfg.Rows == 0 {
		cfg.Rows = 24
	}
	if cfg.Cols == 0 {
		cfg.Cols = 80
	}
	if cfg.ShutdownGrace <= 0 {
		cfg.ShutdownGrace = defaultShutdownGrace
	}

	// Determine which username to resolve.
	requestedUser := cfg.User
	callerExplicitRoot := (cfg.User == "root")
	if cfg.User == "" {
		cfg.User = DefaultShellUser
	}

	u, err := user.Lookup(cfg.User)
	if err != nil {
		if requestedUser != "" {
			// Caller asked for a specific user that does not exist.
			return nil, fmt.Errorf("tunnel/pty: resolve user %q: %w", cfg.User, err)
		}
		// DefaultShellUser ("support") not found — fall back to current user
		// for CI / test environments.
		cfg.Logger.Printf("tunnel/pty: user %q not found, falling back to current user for PTY (CI mode)", cfg.User)
		u, err = user.Current()
		if err != nil {
			return nil, fmt.Errorf("tunnel/pty: resolve current user: %w", err)
		}
		cfg.User = u.Username
	}

	uid64, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("tunnel/pty: parse uid %q for user %q: %w", u.Uid, cfg.User, err)
	}
	gid64, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("tunnel/pty: parse gid %q for user %q: %w", u.Gid, cfg.User, err)
	}

	cfg.Uid = uint32(uid64)
	cfg.Gid = uint32(gid64)
	cfg.Home = u.HomeDir

	// Safety guard: if uid resolved to 0 but the caller did NOT explicitly
	// ask for "root", refuse.  This prevents a misconfigured "support"
	// account (uid=0) or a coding mistake from silently granting root.
	if cfg.Uid == 0 && !callerExplicitRoot {
		return nil, fmt.Errorf(
			"tunnel/pty: resolved user %q to uid 0 but caller did not explicitly request root; "+
				"to run as root pass User=\"root\" explicitly", cfg.User)
	}

	// skipCredential: if the resolved uid/gid match the current process,
	// we can start the PTY without calling setuid (which needs root).
	// This lets the unit tests run without privilege.
	curUID := uint32(syscall.Getuid())
	curGID := uint32(syscall.Getgid())
	if cfg.Uid == curUID && cfg.Gid == curGID {
		cfg.skipCredential = true
	}

	return &PTYSession{cfg: cfg}, nil
}

// PTYSession owns a single PTY-attached shell subprocess.
//
// Lifecycle:
//  1. NewPTYSession(cfg) — resolves credentials, returns session
//  2. session.Start(ctx) — spawns the shell, wires PTY
//  3. io.Copy to/from session.PTY() — the gateway's job (future sprint)
//  4. session.Resize(rows, cols) — on window-change events
//  5. session.Signal(os.Interrupt) — forward Ctrl-C etc.
//  6. session.Close() / ctx cancel — graceful SIGTERM → SIGKILL teardown
type PTYSession struct {
	cfg PTYConfig

	mu      sync.Mutex
	cmd     *exec.Cmd
	ptyFile *os.File
	started bool
	closed  bool
}

// Start spawns the shell inside a PTY, running as the configured user.
//
// SysProcAttr sets:
//   - Setsid=true  — new session; child becomes session leader with PGID=PID
//   - Setctty=true, Ctty=0 — fd 0 (stdin/tty) becomes the controlling terminal
//   - Credential — drop to Uid/Gid unless skipCredential is set
//
// NOTE: Setpgid is intentionally NOT set alongside Setsid.  On Linux, after
// setsid() the child is already in its own process group (PGID=PID); calling
// setpgid(0,0) on a session leader returns EPERM.  Signal delivery to the
// process group is done via -PID (negative PID) which works correctly for a
// session-leader shell.
//
// We call pty.Open() directly rather than pty.Start/StartWithSize because
// modern Go (>= 1.17) changed the meaning of SysProcAttr.Ctty from a
// parent-side fd number to a child-side fd index (0=stdin). creack/pty
// v1.1.9 still passes tty.Fd() (parent-side), which fails the runtime
// validation "Setctty set but Ctty not valid in child" on Go ≥ 1.17.
// By opening the pty pair ourselves and setting Ctty=0 we avoid this.
//
// The initial window size is applied immediately after the pty is opened.
// ctx cancellation triggers an asynchronous Close().
func (s *PTYSession) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return errors.New("tunnel/pty: Start called more than once")
	}
	if s.closed {
		return errSessionClosed
	}

	// Open the pty/tty pair before building the command.
	ptyF, ttyF, err := creackpty.Open()
	if err != nil {
		return fmt.Errorf("tunnel/pty: open PTY pair: %w", err)
	}
	defer ttyF.Close() // tty is only needed in the parent to pass to the child

	// Apply initial window size now, before the child starts.
	ws := &creackpty.Winsize{
		Rows: s.cfg.Rows,
		Cols: s.cfg.Cols,
	}
	if err := creackpty.Setsize(ptyF, ws); err != nil {
		ptyF.Close()
		return fmt.Errorf("tunnel/pty: set initial window size: %w", err)
	}

	env := buildEnv(s.cfg)

	cmd := exec.Command(s.cfg.Shell)
	cmd.Env = env
	// Wire the tty as stdin/stdout/stderr of the child so that it becomes
	// the child's fd 0/1/2.  Ctty=0 then correctly refers to fd 0 (stdin)
	// in the child's fd space, which is the portable way to set a
	// controlling terminal in modern Go.
	cmd.Stdin = ttyF
	cmd.Stdout = ttyF
	cmd.Stderr = ttyF

	attr := &syscall.SysProcAttr{
		Setsid:  true, // new session — child becomes session leader, PGID=PID
		Setctty: true, // make fd Ctty the controlling terminal
		Ctty:    0,    // child fd 0 (stdin) = the tty
		// Setpgid is intentionally absent: setsid() already places the
		// child in its own group; adding Setpgid would trigger EPERM on Linux.
	}
	if !s.cfg.skipCredential {
		attr.Credential = &syscall.Credential{
			Uid: s.cfg.Uid,
			Gid: s.cfg.Gid,
		}
	}
	cmd.SysProcAttr = attr

	if err := cmd.Start(); err != nil {
		ptyF.Close()
		return fmt.Errorf("tunnel/pty: start PTY shell: %w", err)
	}

	s.cmd = cmd
	s.ptyFile = ptyF
	s.started = true

	s.cfg.Logger.Printf(
		"tunnel/pty: started shell %q as user %q (uid=%d gid=%d) pid=%d rows=%d cols=%d",
		s.cfg.Shell, s.cfg.User, s.cfg.Uid, s.cfg.Gid, cmd.Process.Pid,
		s.cfg.Rows, s.cfg.Cols,
	)

	// Watch for ctx cancellation and trigger Close.
	go func() {
		<-ctx.Done()
		s.cfg.Logger.Printf("tunnel/pty: context cancelled, closing session")
		s.Close()
	}()

	return nil
}

// PTY returns the master side of the pseudo-terminal.  The gateway reads
// from and writes to this file to communicate with the shell.
//
// Returns nil (and errSessionNotStarted / errSessionClosed) if the session
// is not in a running state.
func (s *PTYSession) PTY() (*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errSessionClosed
	}
	if !s.started {
		return nil, errSessionNotStarted
	}
	return s.ptyFile, nil
}

// Resize updates the PTY window size.  Must be called after Start and before
// (or after) Close — returns errors in those boundary cases instead of
// panicking.
func (s *PTYSession) Resize(rows, cols uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errSessionClosed
	}
	if !s.started {
		return errSessionNotStarted
	}
	ws := &creackpty.Winsize{Rows: rows, Cols: cols}
	if err := creackpty.Setsize(s.ptyFile, ws); err != nil {
		return fmt.Errorf("tunnel/pty: resize to %dx%d: %w", rows, cols, err)
	}
	s.cfg.Logger.Printf("tunnel/pty: resized PTY to rows=%d cols=%d", rows, cols)
	return nil
}

// Signal delivers sig to the child's process GROUP so that foreground jobs
// inside the shell also receive it.  Uses the same negative-PID syscall as
// supervisor.go's group-signal helpers.
func (s *PTYSession) Signal(sig os.Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errSessionClosed
	}
	if !s.started {
		return errSessionNotStarted
	}
	sysSig, ok := sig.(syscall.Signal)
	if !ok {
		return fmt.Errorf("tunnel/pty: signal %v is not a syscall.Signal", sig)
	}
	// Negative PID targets the process group (mirrors signal_unix.go).
	if err := syscall.Kill(-s.cmd.Process.Pid, sysSig); err != nil {
		return fmt.Errorf("tunnel/pty: send signal %v to process group %d: %w",
			sig, s.cmd.Process.Pid, err)
	}
	return nil
}

// Close shuts down the PTY session:
//  1. Send SIGTERM to the process group.
//  2. Wait up to ShutdownGrace for the child to exit.
//  3. Send SIGKILL to the group if it is still alive.
//  4. Call cmd.Wait() to reap the child (no zombie / goroutine leak).
//  5. Close the PTY master fd.
//
// Calling Close more than once is safe; subsequent calls return nil.
// Resize/Signal after Close return errSessionClosed.
func (s *PTYSession) Close() error {
	s.mu.Lock()

	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if !s.started {
		s.closed = true
		s.mu.Unlock()
		return nil
	}

	cmd := s.cmd
	ptyF := s.ptyFile
	grace := s.cfg.ShutdownGrace
	s.closed = true
	s.mu.Unlock()

	// Reuse the group-signal helpers from signal_unix.go.
	_ = sendSIGTERM(cmd.Process)

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	select {
	case <-waitCh:
		// Child exited cleanly within grace period.
	case <-time.After(grace):
		s.cfg.Logger.Printf("tunnel/pty: grace period expired, sending SIGKILL to process group")
		_ = killProcessGroup(cmd.Process)
		<-waitCh // reap after kill
	}

	_ = ptyF.Close()

	s.cfg.Logger.Printf("tunnel/pty: session closed (pid=%d)", cmd.Process.Pid)
	return nil
}

// buildEnv constructs the minimal environment for the shell process:
// HOME, USER, SHELL, TERM, plus any caller-supplied extras.
func buildEnv(cfg PTYConfig) []string {
	base := []string{
		"HOME=" + cfg.Home,
		"USER=" + cfg.User,
		"SHELL=" + cfg.Shell,
		"TERM=xterm-256color",
	}
	return append(base, cfg.Env...)
}
