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

// Package tunnel provides a supervisor that manages a persistent frpc
// (frp client) child process for NAT-traversal reverse SSH tunnels.
// The supervisor launches frpc as an external binary, restarts it on
// unexpected exits using exponential back-off with jitter, and shuts it
// down cleanly when the context is cancelled.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Logger is the minimal logging interface accepted by Supervisor.
// Any *log.Logger or compatible type satisfies it.
type Logger interface {
	Printf(format string, args ...interface{})
}

// noopLogger discards all log output.
type noopLogger struct{}

func (noopLogger) Printf(format string, args ...interface{}) {}

// stdLogger wraps the standard library logger.
type stdLogger struct{}

func (stdLogger) Printf(format string, args ...interface{}) {
	log.Printf(format, args...)
}

// Config holds all configuration for a Supervisor.
type Config struct {
	// FrpcPath is the absolute path to the frpc binary.  Required.
	FrpcPath string

	// ConfigPath is the absolute path to the frpc TOML config file.  Required.
	ConfigPath string

	// Logger receives diagnostic messages from the supervisor.
	// If nil, log.Printf is used.
	Logger Logger

	// BackoffBase is the initial back-off interval after the first crash.
	// Defaults to 1 second.
	BackoffBase time.Duration

	// BackoffCap is the maximum back-off interval.
	// Defaults to 5 minutes.
	BackoffCap time.Duration

	// BackoffJitter is the fractional jitter applied to each back-off
	// interval (0 ≤ BackoffJitter ≤ 1).  A value of 0.2 adds up to ±20%
	// random noise.  The zero value causes NewSupervisor to apply the
	// default of 0.2.  To request truly zero jitter, set DisableJitter=true.
	BackoffJitter float64

	// DisableJitter suppresses all jitter when true.  Use this to obtain
	// deterministic back-off intervals in tests or controlled environments.
	DisableJitter bool

	// StabilityThreshold is the minimum time a frpc process must stay
	// alive before its back-off counter is reset to the base.
	// Defaults to 30 seconds.
	StabilityThreshold time.Duration

	// ShutdownGrace is how long the supervisor waits for frpc to exit
	// after sending SIGTERM before sending SIGKILL.
	// Defaults to 5 seconds.
	ShutdownGrace time.Duration
}

func (c *Config) applyDefaults() {
	if c.Logger == nil {
		c.Logger = stdLogger{}
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = time.Second
	}
	if c.BackoffCap <= 0 {
		c.BackoffCap = 5 * time.Minute
	}
	// BackoffJitter: the zero value means "apply the default 0.2".
	// To request truly zero jitter, set DisableJitter = true.
	if c.BackoffJitter <= 0 {
		if c.DisableJitter {
			c.BackoffJitter = 0
		} else {
			c.BackoffJitter = 0.2
		}
	}
	if c.BackoffJitter > 1 {
		c.BackoffJitter = 1
	}
	if c.StabilityThreshold <= 0 {
		c.StabilityThreshold = 30 * time.Second
	}
	if c.ShutdownGrace <= 0 {
		c.ShutdownGrace = 5 * time.Second
	}
}

// Supervisor supervises a frpc child process, restarting it on failure
// with exponential back-off + jitter.
type Supervisor struct {
	cfg Config

	// mu guards cmd during an active process lifetime.
	mu  sync.Mutex
	cmd *exec.Cmd
}

// NewSupervisor creates a new Supervisor from cfg.
// It returns an error if required fields are missing or if the frpc
// binary does not exist on disk.
func NewSupervisor(cfg Config) (*Supervisor, error) {
	if cfg.FrpcPath == "" {
		return nil, errors.New("tunnel: FrpcPath must not be empty")
	}
	if cfg.ConfigPath == "" {
		return nil, errors.New("tunnel: ConfigPath must not be empty")
	}
	// Validate that the binary is present.  The check is advisory — the
	// binary might be replaced by a package update between now and first
	// use, so the supervisor will also handle exec errors at run time.
	if _, err := os.Stat(cfg.FrpcPath); err != nil {
		return nil, fmt.Errorf("tunnel: frpc binary not accessible at %q: %w", cfg.FrpcPath, err)
	}
	cfg.applyDefaults()
	return &Supervisor{cfg: cfg}, nil
}

// Run starts frpc and supervises it until ctx is cancelled.
// It blocks the caller until supervision ends.
//
// On clean context cancellation Run returns nil.
// On an unrecoverable setup error (e.g. exec.LookPath failure) Run
// returns a non-nil error.
//
// Run is safe to call from a goroutine.  Do not call Run concurrently
// on the same Supervisor.
func (s *Supervisor) Run(ctx context.Context) error {
	s.cfg.Logger.Printf("tunnel/supervisor: starting (frpc=%s config=%s)",
		s.cfg.FrpcPath, s.cfg.ConfigPath)

	attempt := 0
	for {
		if ctx.Err() != nil {
			s.cfg.Logger.Printf("tunnel/supervisor: context done before launch, exiting")
			return nil
		}

		startedAt := time.Now()
		err := s.runOnce(ctx)
		elapsed := time.Since(startedAt)

		// Context was cancelled — clean exit.
		if ctx.Err() != nil {
			s.cfg.Logger.Printf("tunnel/supervisor: shutdown complete")
			return nil
		}

		// frpc exited unexpectedly.
		if err != nil {
			s.cfg.Logger.Printf("tunnel/supervisor: frpc exited with error: %v", err)
		} else {
			s.cfg.Logger.Printf("tunnel/supervisor: frpc exited cleanly (unexpected)")
		}

		// Reset back-off counter if the process was stable.
		if elapsed >= s.cfg.StabilityThreshold {
			s.cfg.Logger.Printf("tunnel/supervisor: process was stable for %v, resetting back-off", elapsed)
			attempt = 0
		}

		delay := s.nextBackoff(attempt)
		s.cfg.Logger.Printf("tunnel/supervisor: restarting in %v (attempt %d)", delay, attempt+1)
		attempt++

		select {
		case <-ctx.Done():
			s.cfg.Logger.Printf("tunnel/supervisor: shutdown complete")
			return nil
		case <-time.After(delay):
		}
	}
}

// runOnce launches one frpc process and waits for it to exit.
// It returns the process error (nil means clean exit from frpc's point
// of view).  Context cancellation triggers a graceful SIGTERM+SIGKILL
// shutdown and then returns.
func (s *Supervisor) runOnce(ctx context.Context) error {
	// Build the command but do NOT use exec.CommandContext directly —
	// that sends SIGKILL immediately on cancel; we want graceful SIGTERM
	// first.
	cmd := exec.Command(s.cfg.FrpcPath, "-c", s.cfg.ConfigPath)

	// Put frpc in its own process group so that shell-spawned children
	// (if any) are also killed when we terminate the group.
	setPgid(cmd)

	// Wire stdout/stderr to a logger-backed writer.
	lw := &logWriter{prefix: "frpc: ", logger: s.cfg.Logger}
	cmd.Stdout = lw
	cmd.Stderr = lw

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("tunnel: failed to start frpc: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()

	// waitCh receives the process exit error.
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	var procErr error
	select {
	case procErr = <-waitCh:
		// frpc exited on its own.
	case <-ctx.Done():
		// Context cancelled — gracefully terminate frpc.
		procErr = s.terminate(cmd, waitCh)
	}

	s.mu.Lock()
	s.cmd = nil
	s.mu.Unlock()

	return procErr
}

// terminate sends SIGTERM to the frpc process group, waits up to
// ShutdownGrace, then sends SIGKILL to the group if still alive.
// It always waits for cmd.Wait() to return before returning itself,
// ensuring no goroutine leak.
func (s *Supervisor) terminate(cmd *exec.Cmd, waitCh <-chan error) error {
	// Kill the process group so that any shell-spawned children
	// (which hold the stdout/stderr pipes open) also receive the signal.
	// This is critical to avoid cmd.Wait() blocking indefinitely on pipe
	// reads after the shell exits but a child (e.g. `sleep`) is still alive.
	_ = sendSIGTERM(cmd.Process)

	select {
	case err := <-waitCh:
		return err
	case <-time.After(s.cfg.ShutdownGrace):
		s.cfg.Logger.Printf("tunnel/supervisor: grace period expired, sending SIGKILL to process group")
		_ = killProcessGroup(cmd.Process)
		return <-waitCh
	}
}

// nextBackoff returns the back-off duration for the given attempt number
// (0-based).  Duration = min(BackoffBase * 2^attempt, BackoffCap) ±
// BackoffJitter fraction.
func (s *Supervisor) nextBackoff(attempt int) time.Duration {
	return nextBackoffDuration(attempt, s.cfg.BackoffBase, s.cfg.BackoffCap, s.cfg.BackoffJitter)
}

// nextBackoffDuration is the pure-function core of the back-off
// calculation, exported via the supervisor method and tested directly.
func nextBackoffDuration(attempt int, base, cap_ time.Duration, jitter float64) time.Duration {
	// Clamp attempt to avoid overflow in Pow.
	if attempt > 62 {
		attempt = 62
	}
	mult := math.Pow(2, float64(attempt))
	d := float64(base) * mult
	if d > float64(cap_) {
		d = float64(cap_)
	}
	// Apply symmetric jitter: d ± (d * jitter * rand[0,1))
	if jitter > 0 {
		noise := d * jitter * (rand.Float64()*2 - 1)
		d += noise
	}
	// Clamp to [base, cap] after jitter.
	if d < float64(base) {
		d = float64(base)
	}
	if d > float64(cap_) {
		d = float64(cap_)
	}
	return time.Duration(d)
}

// logWriter bridges an io.Writer to a Logger.
type logWriter struct {
	prefix string
	logger Logger
	buf    []byte
	mu     sync.Mutex
}

func (lw *logWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	lw.buf = append(lw.buf, p...)
	// Flush complete lines.
	for {
		idx := -1
		for i, b := range lw.buf {
			if b == '\n' {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		line := string(lw.buf[:idx])
		lw.buf = lw.buf[idx+1:]
		lw.logger.Printf("%s%s", lw.prefix, line)
	}
	return len(p), nil
}

// Ensure logWriter implements io.Writer.
var _ io.Writer = (*logWriter)(nil)
