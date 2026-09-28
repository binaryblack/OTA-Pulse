// Copyright 2026 OTA-Pulse
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

package installer

import (
	"os"
	"path/filepath"
	"testing"
)

// BUG-436: uBootEnvWorks() executed fw_printenv unconditionally. On boards
// whose /etc/fw_env.config is the shipped all-commented placeholder (no real
// U-Boot env: Dragon Q6A systemd-boot, RPi4 VideoCore, ...) libubootenv's
// fw_printenv SIGSEGVs, so every install (WriteEnv writes mender_boot_part
// AND mender_boot_part_hex -> two syncBootSlotToBootPartition calls) and every
// commit (ClearDirectBootBackup) crashed it — the dmesg/audit sig=11 pairs
// seen on dev-d07bbb07. The probe result was already "false" in that case, so
// skipping the exec must not change the classification.

// installFakeFwPrintenv puts a recording fw_printenv first on PATH. It exits
// with exitCode, and appends one line to the returned log file per call.
func installFakeFwPrintenv(t *testing.T, exitCode string) string {
	t.Helper()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho called >> '" + logFile + "'\nexit " + exitCode + "\n"
	if err := os.WriteFile(filepath.Join(dir, "fw_printenv"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logFile
}

func withFwEnvConfig(t *testing.T, content *string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fw_env.config")
	if content != nil {
		if err := os.WriteFile(path, []byte(*content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	old := uBootEnvConfigPath
	uBootEnvConfigPath = path
	t.Cleanup(func() { uBootEnvConfigPath = old })
}

func fakeCalls(t *testing.T, logFile string) int {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	return n
}

// The shipped u-boot-env-config placeholder: every line commented out.
const placeholderFwEnvConfig = `# U-Boot Environment Configuration
# Placeholder - will be configured by setup script
# /dev/mmcblk0  0x3F8000  0x8000
`

func TestBUG436_PlaceholderConfig_DoesNotExecFwPrintenv(t *testing.T) {
	// Fake exits 0: if the probe still execs it, it would even report a
	// working env — the test must see neither the exec nor a true result.
	logFile := installFakeFwPrintenv(t, "0")
	c := placeholderFwEnvConfig
	withFwEnvConfig(t, &c)

	if uBootEnvWorks() {
		t.Error("uBootEnvWorks() = true for a config with no active entries; want false")
	}
	if n := fakeCalls(t, logFile); n != 0 {
		t.Errorf("fw_printenv executed %d time(s) against a device-less fw_env.config; want 0 (it SIGSEGVs on real boards)", n)
	}
}

func TestBUG436_EmptyConfig_DoesNotExecFwPrintenv(t *testing.T) {
	logFile := installFakeFwPrintenv(t, "0")
	c := "\n   \n"
	withFwEnvConfig(t, &c)

	if uBootEnvWorks() {
		t.Error("uBootEnvWorks() = true for an empty fw_env.config; want false")
	}
	if n := fakeCalls(t, logFile); n != 0 {
		t.Errorf("fw_printenv executed %d time(s) against an empty fw_env.config; want 0", n)
	}
}

// Unchanged behaviour: a config with a real device line is still probed by
// actually running fw_printenv, and its exit status decides.
func TestBUG436_ActiveConfig_StillProbesByExec(t *testing.T) {
	c := "/dev/mmcblk0  0x88000  0x8000\n"

	logFile := installFakeFwPrintenv(t, "0")
	withFwEnvConfig(t, &c)
	if !uBootEnvWorks() {
		t.Error("uBootEnvWorks() = false with an active config and fw_printenv exit 0; want true")
	}
	if n := fakeCalls(t, logFile); n != 1 {
		t.Errorf("fw_printenv executed %d time(s); want 1", n)
	}

	logFile = installFakeFwPrintenv(t, "1")
	if uBootEnvWorks() {
		t.Error("uBootEnvWorks() = true with fw_printenv exit 1; want false")
	}
	if n := fakeCalls(t, logFile); n != 1 {
		t.Errorf("fw_printenv executed %d time(s); want 1", n)
	}
}

// Unchanged behaviour: a MISSING fw_env.config is still probed by exec (an
// implementation may locate the env without the file), not short-circuited.
func TestBUG436_MissingConfig_StillProbesByExec(t *testing.T) {
	logFile := installFakeFwPrintenv(t, "1")
	withFwEnvConfig(t, nil)

	if uBootEnvWorks() {
		t.Error("uBootEnvWorks() = true with fw_printenv exit 1; want false")
	}
	if n := fakeCalls(t, logFile); n != 1 {
		t.Errorf("fw_printenv executed %d time(s) with no fw_env.config; want 1 (unchanged behaviour)", n)
	}
}
