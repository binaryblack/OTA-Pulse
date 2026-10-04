// Copyright 2026 OTA-Pulse
//
//	Licensed under the Apache License, Version 2.0 (the "License");
//	you may not use this file except in compliance with the License.
//	You may obtain a copy of the License at
//
//	    http://www.apache.org/licenses/LICENSE-2.0
//
//	Unless required by applicable law or agreed to in writing, software
//	distributed under the License is distributed on an "AS IS" BASIS,
//	WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//	See the License for the specific language governing permissions and
//	limitations under the License.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDaemonHost puts stub `systemctl` and `kill` executables first on PATH.
// The stub systemctl answers `show -p MainPID <unit>` with the PID in pids
// (MainPID=0 for an unknown unit, which is what real systemd prints for a unit
// that does not exist). The stub kill records its arguments to a file.
func fakeDaemonHost(t *testing.T, pids map[string]string) (killArgsFile string) {
	t.Helper()
	dir := t.TempDir()
	killArgsFile = filepath.Join(dir, "kill.args")

	var cases strings.Builder
	for unit, pid := range pids {
		cases.WriteString("  " + unit + ") echo \"MainPID=" + pid + "\" ;;\n")
	}
	systemctl := "#!/bin/sh\n" +
		"unit=\"\"\nfor a in \"$@\"; do unit=\"$a\"; done\n" +
		"case \"$unit\" in\n" + cases.String() +
		"  *) echo \"MainPID=0\" ;;\nesac\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "systemctl"), []byte(systemctl), 0755))

	kill := "#!/bin/sh\necho \"$@\" > \"" + killArgsFile + "\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kill"), []byte(kill), 0755))

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return killArgsFile
}

func readKillArgs(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// BUG-478: Buildroot images install the daemon as otapulse.service
// (buildroot-otapulse/package/otapulse/otapulse.mk), not soc-ota-agent.service.
// `check-update` used to ask systemd only for soc-ota-agent's MainPID, got
// MainPID=0 and failed with "could not find the PID", so a forced update check
// never reached the daemon on Buildroot. The daemon then only polled on its own
// 300 s UpdatePollIntervalSeconds, and E2E-002's deployment sat in 'pending'.
func TestCheckUpdateSignalsDaemonRunningAsOtapulseUnit(t *testing.T) {
	killArgs := fakeDaemonHost(t, map[string]string{"otapulse": "4242"})

	err := SetupCLI([]string{"otapulse", "check-update"})
	assert.NoError(t, err)
	assert.Equal(t, "-USR1 4242", readKillArgs(t, killArgs))
}

func TestSendInventorySignalsDaemonRunningAsOtapulseUnit(t *testing.T) {
	killArgs := fakeDaemonHost(t, map[string]string{"otapulse": "4242"})

	err := SetupCLI([]string{"otapulse", "send-inventory"})
	assert.NoError(t, err)
	assert.Equal(t, "-USR2 4242", readKillArgs(t, killArgs))
}

// Yocto images run the daemon as soc-ota-agent.service; that unit keeps
// priority when it has a live MainPID.
func TestCheckUpdatePrefersSocOtaAgentUnit(t *testing.T) {
	killArgs := fakeDaemonHost(t, map[string]string{
		"soc-ota-agent": "1111",
		"otapulse":      "2222",
	})

	err := SetupCLI([]string{"otapulse", "check-update"})
	assert.NoError(t, err)
	assert.Equal(t, "-USR1 1111", readKillArgs(t, killArgs))
}

// No daemon under any known unit: still an error, and nothing is signalled
// (in particular never `kill -USR1 0`, which would hit the caller's process
// group).
func TestCheckUpdateFailsWhenNoDaemonUnitIsRunning(t *testing.T) {
	killArgs := fakeDaemonHost(t, map[string]string{})

	err := SetupCLI([]string{"otapulse", "check-update"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not find the PID")
	assert.Equal(t, "", readKillArgs(t, killArgs))
}
