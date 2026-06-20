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

package tunnel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// --------------------------------------------------------------------------
// Test helpers
// --------------------------------------------------------------------------

// testLogger forwards log output to testing.T.
type testLogger struct {
	t *testing.T
}

func (l *testLogger) Printf(format string, args ...interface{}) {
	l.t.Helper()
	l.t.Logf(format, args...)
}

// writeFakeScript writes an executable shell script to dir/name and returns
// its path.  Skips the calling test on non-Unix platforms.
func writeFakeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake frpc scripts require a Unix shell")
	}
	path := filepath.Join(dir, name)
	content := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("writeFakeScript: %v", err)
	}
	return path
}

// writeFakeConfig writes a minimal placeholder TOML config and returns its path.
func writeFakeConfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "frpc.toml")
	if err := os.WriteFile(path, []byte("# placeholder\n"), 0644); err != nil {
		t.Fatalf("writeFakeConfig: %v", err)
	}
	return path
}

// shortTestConfig returns a Config with short back-offs suitable for tests.
func shortTestConfig(t *testing.T, frpcPath, cfgPath string) Config {
	return Config{
		FrpcPath:           frpcPath,
		ConfigPath:         cfgPath,
		Logger:             &testLogger{t: t},
		BackoffBase:        10 * time.Millisecond,
		BackoffCap:         50 * time.Millisecond,
		BackoffJitter:      0.2,
		StabilityThreshold: 5 * time.Second, // high — tests don't rely on reset
		ShutdownGrace:      500 * time.Millisecond,
	}
}

// --------------------------------------------------------------------------
// NewSupervisor validation
// --------------------------------------------------------------------------

func TestNewSupervisor_MissingFrpcPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeFakeConfig(t, dir)
	_, err := NewSupervisor(Config{FrpcPath: "", ConfigPath: cfgPath})
	if err == nil {
		t.Fatal("expected error for empty FrpcPath, got nil")
	}
}

func TestNewSupervisor_MissingConfigPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell script for frpc binary")
	}
	dir := t.TempDir()
	frpcPath := writeFakeScript(t, dir, "frpc", "exit 0")
	_, err := NewSupervisor(Config{FrpcPath: frpcPath, ConfigPath: ""})
	if err == nil {
		t.Fatal("expected error for empty ConfigPath, got nil")
	}
}

func TestNewSupervisor_BinaryNotFound(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeFakeConfig(t, dir)
	_, err := NewSupervisor(Config{
		FrpcPath:   filepath.Join(dir, "does_not_exist"),
		ConfigPath: cfgPath,
	})
	if err == nil {
		t.Fatal("expected error for missing binary, got nil")
	}
}

func TestNewSupervisor_DefaultsApplied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell script for frpc binary")
	}
	dir := t.TempDir()
	frpcPath := writeFakeScript(t, dir, "frpc", "exit 0")
	cfgPath := writeFakeConfig(t, dir)

	s, err := NewSupervisor(Config{FrpcPath: frpcPath, ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.cfg.BackoffBase != time.Second {
		t.Errorf("BackoffBase default: got %v, want 1s", s.cfg.BackoffBase)
	}
	if s.cfg.BackoffCap != 5*time.Minute {
		t.Errorf("BackoffCap default: got %v, want 5m", s.cfg.BackoffCap)
	}
	if s.cfg.BackoffJitter != 0.2 {
		t.Errorf("BackoffJitter default: got %v, want 0.2", s.cfg.BackoffJitter)
	}
	if s.cfg.ShutdownGrace != 5*time.Second {
		t.Errorf("ShutdownGrace default: got %v, want 5s", s.cfg.ShutdownGrace)
	}
	if s.cfg.StabilityThreshold != 30*time.Second {
		t.Errorf("StabilityThreshold default: got %v, want 30s", s.cfg.StabilityThreshold)
	}
	if s.cfg.Logger == nil {
		t.Error("Logger default must not be nil")
	}
}

// --------------------------------------------------------------------------
// Back-off math
// --------------------------------------------------------------------------

// pow2 returns 2^n as a float64 without importing math.
func pow2(n int) float64 {
	r := 1.0
	for i := 0; i < n; i++ {
		r *= 2
	}
	return r
}

func TestNextBackoffDuration_Cap(t *testing.T) {
	base := 10 * time.Millisecond
	cap_ := 50 * time.Millisecond

	for attempt := 0; attempt < 20; attempt++ {
		d := nextBackoffDuration(attempt, base, cap_, 0)
		if d < base {
			t.Errorf("attempt %d: got %v < base %v", attempt, d, base)
		}
		if d > cap_ {
			t.Errorf("attempt %d: got %v > cap %v", attempt, d, cap_)
		}
	}
}

func TestNextBackoffDuration_Doubles(t *testing.T) {
	base := 10 * time.Millisecond
	cap_ := 1 * time.Second

	var prev time.Duration
	for attempt := 0; attempt <= 5; attempt++ {
		curr := nextBackoffDuration(attempt, base, cap_, 0)

		// With zero jitter, each step must equal base * 2^attempt (capped).
		expected := time.Duration(float64(base) * pow2(attempt))
		if expected > cap_ {
			expected = cap_
		}
		if curr != expected {
			t.Errorf("attempt %d: got %v, want %v", attempt, curr, expected)
		}
		if attempt > 0 && curr < prev {
			t.Errorf("attempt %d: back-off decreased: %v < %v", attempt, curr, prev)
		}
		prev = curr
	}
}

func TestNextBackoffDuration_JitterBounds(t *testing.T) {
	base := 10 * time.Millisecond
	cap_ := 100 * time.Millisecond
	jitter := 0.3

	for i := 0; i < 1000; i++ {
		d := nextBackoffDuration(3, base, cap_, jitter)
		if d < base {
			t.Errorf("iteration %d: got %v < base %v", i, d, base)
		}
		if d > cap_ {
			t.Errorf("iteration %d: got %v > cap %v", i, d, cap_)
		}
	}
}

func TestNextBackoffDuration_NeverNegative(t *testing.T) {
	base := 1 * time.Millisecond
	cap_ := 5 * time.Millisecond

	for attempt := 0; attempt < 100; attempt++ {
		d := nextBackoffDuration(attempt, base, cap_, 1.0)
		if d < 0 {
			t.Errorf("attempt %d: negative backoff %v", attempt, d)
		}
	}
}

func TestNextBackoffDuration_LargeAttemptNoPanic(t *testing.T) {
	base := time.Second
	cap_ := 5 * time.Minute
	d := nextBackoffDuration(1000, base, cap_, 0.2)
	if d < base || d > cap_ {
		t.Errorf("large attempt: got %v, want in [%v, %v]", d, base, cap_)
	}
}

// --------------------------------------------------------------------------
// Process lifecycle (Unix only)
// --------------------------------------------------------------------------

// TestSupervisor_Restart verifies that a frpc that exits immediately is
// restarted several times before the context deadline.
func TestSupervisor_Restart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell")
	}
	dir := t.TempDir()
	cfgPath := writeFakeConfig(t, dir)
	counterFile := filepath.Join(dir, "count.txt")

	// Each run appends a byte to the counter file, then exits with error.
	script := fmt.Sprintf("echo x >> %s; exit 1", counterFile)
	frpcPath := writeFakeScript(t, dir, "frpc", script)

	cfg := shortTestConfig(t, frpcPath, cfgPath)
	s, err := NewSupervisor(cfg)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	runErr := s.Run(ctx)
	if runErr != nil {
		t.Fatalf("Run returned unexpected error: %v", runErr)
	}

	data, readErr := os.ReadFile(counterFile)
	if readErr != nil {
		t.Fatalf("could not read counter file: %v", readErr)
	}
	// Count newline characters = number of times frpc was launched.
	count := 0
	for _, b := range data {
		if b == '\n' {
			count++
		}
	}
	t.Logf("frpc was launched %d times", count)
	if count < 3 {
		t.Errorf("expected >= 3 restarts in 600ms with 10ms backoff, got %d", count)
	}
}

// TestSupervisor_CleanShutdown verifies that a sleeping frpc is terminated
// promptly when the context is cancelled.
func TestSupervisor_CleanShutdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell")
	}
	dir := t.TempDir()
	cfgPath := writeFakeConfig(t, dir)
	frpcPath := writeFakeScript(t, dir, "frpc", "sleep 9999")

	cfg := shortTestConfig(t, frpcPath, cfgPath)
	s, err := NewSupervisor(cfg)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Wait for frpc to start before cancelling.
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Errorf("Run returned non-nil error on clean shutdown: %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s after context cancel")
	}
}

// TestSupervisor_GracefulSIGTERM verifies that a process that handles SIGTERM
// and exits cleanly is not force-killed.
func TestSupervisor_GracefulSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell")
	}
	dir := t.TempDir()
	cfgPath := writeFakeConfig(t, dir)
	markerFile := filepath.Join(dir, "sigterm.txt")

	// Trap SIGTERM, write a marker, then exit 0.
	script := fmt.Sprintf(
		"trap 'echo caught >> %s; exit 0' TERM; sleep 9999",
		markerFile,
	)
	frpcPath := writeFakeScript(t, dir, "frpc", script)

	cfg := shortTestConfig(t, frpcPath, cfgPath)
	cfg.ShutdownGrace = 2 * time.Second
	s, err := NewSupervisor(cfg)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Errorf("Run returned error: %v", runErr)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Run did not return within 4s")
	}

	// Marker must exist — SIGTERM was delivered and caught, not SIGKILL.
	if _, statErr := os.Stat(markerFile); statErr != nil {
		t.Errorf("SIGTERM marker file missing — process was likely SIGKILLed: %v", statErr)
	}
}

// TestSupervisor_NoOrphan verifies that after Run returns, the cmd field
// is nil (no orphan reference left).
func TestSupervisor_NoOrphan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell")
	}
	dir := t.TempDir()
	cfgPath := writeFakeConfig(t, dir)
	frpcPath := writeFakeScript(t, dir, "frpc", "sleep 9999")

	cfg := shortTestConfig(t, frpcPath, cfgPath)
	s, err := NewSupervisor(cfg)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s")
	}

	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd != nil {
		t.Error("s.cmd is non-nil after Run returned — potential orphan process")
	}
}

// TestSupervisor_ContextAlreadyCancelled verifies Run exits immediately
// when ctx is already done before the call.
func TestSupervisor_ContextAlreadyCancelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell script for frpc binary")
	}
	dir := t.TempDir()
	cfgPath := writeFakeConfig(t, dir)
	frpcPath := writeFakeScript(t, dir, "frpc", "sleep 9999")

	cfg := shortTestConfig(t, frpcPath, cfgPath)
	s, err := NewSupervisor(cfg)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	start := time.Now()
	runErr := s.Run(ctx)
	elapsed := time.Since(start)

	if runErr != nil {
		t.Errorf("Run returned error on pre-cancelled ctx: %v", runErr)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Run took too long on pre-cancelled ctx: %v (want < 500ms)", elapsed)
	}
}

// TestSupervisor_LogOutput verifies that frpc stdout is forwarded to the
// logger without panics or deadlocks.
func TestSupervisor_LogOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell")
	}
	dir := t.TempDir()
	cfgPath := writeFakeConfig(t, dir)
	frpcPath := writeFakeScript(t, dir, "frpc", `echo "hello from frpc"; exit 0`)

	var count int64
	cfg := Config{
		FrpcPath:           frpcPath,
		ConfigPath:         cfgPath,
		Logger:             &countLogger{count: &count},
		BackoffBase:        10 * time.Millisecond,
		BackoffCap:         50 * time.Millisecond,
		DisableJitter:      true,
		StabilityThreshold: 5 * time.Second,
		ShutdownGrace:      200 * time.Millisecond,
	}
	s, err := NewSupervisor(cfg)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_ = s.Run(ctx)

	if atomic.LoadInt64(&count) == 0 {
		t.Error("expected log output from frpc stdout, got none")
	}
}

// countLogger counts Printf invocations.
type countLogger struct {
	count *int64
}

func (l *countLogger) Printf(format string, args ...interface{}) {
	atomic.AddInt64(l.count, 1)
}
