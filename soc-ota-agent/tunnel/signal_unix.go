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
	"os"
	"os/exec"
	"syscall"
)

// setPgid configures cmd to start in its own process group.
// This lets us kill the whole group (shell + any child processes it spawned)
// when terminating.
func setPgid(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// sendSIGTERM sends SIGTERM to the process group led by p.
// Killing the group ensures shell-spawned children (e.g. `sleep`) are also
// terminated, which unblocks cmd.Wait() when the I/O pipes close.
func sendSIGTERM(p *os.Process) error {
	// Negative PID targets the process group.
	return syscall.Kill(-p.Pid, syscall.SIGTERM)
}

// killProcessGroup sends SIGKILL to the process group led by p.
func killProcessGroup(p *os.Process) error {
	return syscall.Kill(-p.Pid, syscall.SIGKILL)
}
