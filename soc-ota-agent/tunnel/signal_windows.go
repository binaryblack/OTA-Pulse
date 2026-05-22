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

package tunnel

import (
	"os"
	"os/exec"
)

// setPgid is a no-op on Windows.
func setPgid(cmd *exec.Cmd) {}

// sendSIGTERM falls back to Kill on Windows (no SIGTERM concept).
func sendSIGTERM(p *os.Process) error {
	return p.Kill()
}

// killProcessGroup kills the single process on Windows.
func killProcessGroup(p *os.Process) error {
	return p.Kill()
}
