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

//go:build windows
// +build windows

// Package tunnel – Windows stub for PTY session management.
// PTY sessions are only supported on embedded Linux (the target device
// platform).  This file satisfies the Go build on Windows by exposing the
// same exported symbols with "unsupported" errors so that cross-compilation
// and IDE tooling work without modification.
package tunnel

import (
	"context"
	"errors"
	"os"
	"time"
)

// errUnsupported is returned by all PTY operations on Windows.
var errUnsupported = errors.New("tunnel/pty: PTY sessions are not supported on Windows")

// DefaultShellUser is the default non-root OS user for PTY sessions.
// Defined here so the constant is available on all platforms.
const DefaultShellUser = "support"

// PTYConfig holds PTY session configuration.  Defined on all platforms so
// callers can import the package without build-tag guards.
type PTYConfig struct {
	Shell          string
	User           string
	Uid            uint32
	Gid            uint32
	Home           string
	Env            []string
	Logger         Logger
	Rows           uint16
	Cols           uint16
	ShutdownGrace  time.Duration
	skipCredential bool
}

// PTYSession is a no-op stub on Windows.
type PTYSession struct{}

// NewPTYSession always returns errUnsupported on Windows.
func NewPTYSession(cfg PTYConfig) (*PTYSession, error) {
	return nil, errUnsupported
}

// Start always returns errUnsupported on Windows.
func (s *PTYSession) Start(ctx context.Context) error { return errUnsupported }

// PTY always returns errUnsupported on Windows.
func (s *PTYSession) PTY() (*os.File, error) { return nil, errUnsupported }

// Resize always returns errUnsupported on Windows.
func (s *PTYSession) Resize(rows, cols uint16) error { return errUnsupported }

// Signal always returns errUnsupported on Windows.
func (s *PTYSession) Signal(sig os.Signal) error { return errUnsupported }

// Close always returns nil on Windows (nothing to clean up).
func (s *PTYSession) Close() error { return nil }
