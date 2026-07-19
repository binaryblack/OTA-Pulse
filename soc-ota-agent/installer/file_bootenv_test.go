// Copyright 2026 SoC Monitoring
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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/binaryblack/OTA-Pulse/system"
)

// recordingCommander is a fake system.Commander that records every command
// invoked (name + args, space joined) and runs a harmless no-op ("false")
// instead of the real command, so tests never attempt a real `mount` of a
// non-existent device.
type recordingCommander struct {
	calls []string
}

func (r *recordingCommander) Command(name string, arg ...string) *system.Cmd {
	r.calls = append(r.calls, strings.TrimSpace(strings.Join(append([]string{name}, arg...), " ")))
	// Never actually run the requested command (e.g. "mount ...") — just
	// fail harmlessly so syncBootSlotToBootPartition takes its error path.
	return system.Command("false")
}

// newTempFileBasedBootEnv builds a FileBasedBootEnv whose state files all
// live under a fresh temp directory, backed by a recordingCommander. The boot
// partition mount points syncBootSlotToBootPartition probes ("/mnt/boot",
// "/boot/firmware", "/boot") and the boot devices it tries to mount
// ("/dev/mmcblk1p1", etc.) are hardcoded in file_bootenv.go and do not exist
// in the test sandbox, so syncBootSlotToBootPartition — and therefore
// writeMenderBootPart — reliably fails here. That is exactly the "failing FAT
// sync" scenario GAP-OTA-004 hardened WriteEnv against, and it lets us prove
// ordering without needing to fake the filesystem mount itself.
func newTempFileBasedBootEnv(t *testing.T) (*FileBasedBootEnv, *recordingCommander) {
	t.Helper()
	dir := t.TempDir()
	cmd := &recordingCommander{}
	env := NewFileBasedBootEnv(cmd, "/dev/mmcblk0p2", "/dev/mmcblk0p3")
	env.slotFile = filepath.Join(dir, "current_slot")
	env.bootCountFile = filepath.Join(dir, "boot_count")
	env.upgradeAvailFile = filepath.Join(dir, "upgrade_available")
	env.menderBootPartFile = filepath.Join(dir, "mender_boot_part")
	return env, cmd
}

// (a) WriteEnv must write mender_boot_part* (and sync it to the boot
// partition) before it ever writes upgrade_available — see the BUG-106
// deterministic write-order fix in WriteEnv's writeOrder slice. Because the
// boot-partition sync always fails in this sandbox (no real boot device to
// mount), WriteEnv aborts partway through; if the correct write order is in
// place, that abort always happens before upgrade_available is written, so
// the upgrade_available file must never appear on disk. Before the fix,
// WriteEnv iterated `vars` with Go's randomized map order, so roughly half
// of all runs would write upgrade_available first and leave it on disk even
// though the overall call still errored. Looping many iterations makes that
// a near-certain catch (probability of 60 consecutive "lucky" random
// orderings is astronomically small).
func TestWriteEnv_MenderBootPartWrittenBeforeUpgradeAvailable(t *testing.T) {
	const iterations = 60
	for i := 0; i < iterations; i++ {
		env, cmd := newTempFileBasedBootEnv(t)

		err := env.WriteEnv(BootVars{
			"upgrade_available": "1",
			"mender_boot_part":  "3",
			"bootcount":         "0",
		})

		require.Error(t, err, "iteration %d: WriteEnv must fail because the boot partition sync cannot succeed in this sandbox", i)
		assert.Contains(t, err.Error(), "mender_boot_part", "iteration %d: error should be attributable to the mender_boot_part write", i)

		_, statErr := os.Stat(env.upgradeAvailFile)
		assert.True(t, os.IsNotExist(statErr),
			"iteration %d: upgrade_available was written to disk even though the mender_boot_part sync failed — write order regressed (BUG-106)", i)

		// No mount(8) invocation should have succeeded or even been reachable
		// via a real device in this sandbox; recordingCommander only proves we
		// never silently short-circuited through a stray successful command.
		_ = cmd
	}
}

// (b) A failing FAT sync must abort WriteEnv before upgrade_available is
// written — the GAP-OTA-004 fix propagates syncBootSlotToBootPartition's
// error out of writeMenderBootPart instead of only Warnf-ing it.
func TestWriteEnv_FailingFATSyncAbortsBeforeUpgradeAvailable(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)

	err := env.WriteEnv(BootVars{
		"mender_boot_part":  "2",
		"upgrade_available": "1",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to sync boot slot to boot partition")

	_, statErr := os.Stat(env.upgradeAvailFile)
	assert.True(t, os.IsNotExist(statErr), "upgrade_available must not exist after an aborted WriteEnv")

	// mender_boot_part itself is written to disk before the sync is attempted
	// (writeMenderBootPart writes the file, then syncs) — only the later
	// upgrade_available write is guarded against.
	content, readErr := os.ReadFile(env.menderBootPartFile)
	require.NoError(t, readErr)
	assert.Equal(t, "2", strings.TrimSpace(string(content)))
}

// (c) updateBootCmdline must rewrite only the root= token (preserving every
// other cmdline parameter) and must write cmdline_prev.txt exactly once per
// OTA cycle, never clobbering an existing backup with an already-updated
// cmdline.
func TestUpdateBootCmdline_RewritesOnlyRootAndBacksUpOnce(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	bootDir := t.TempDir()

	original := "console=serial0,115200 console=tty1 root=/dev/mmcblk0p2 rootfstype=ext4 rootwait fsck.repair=yes"
	cmdlinePath := filepath.Join(bootDir, "cmdline.txt")
	require.NoError(t, os.WriteFile(cmdlinePath, []byte(original+"\n"), 0644))

	env.updateBootCmdline(bootDir, "/dev/mmcblk0p3")

	updated, err := os.ReadFile(cmdlinePath)
	require.NoError(t, err)
	updatedFields := strings.Fields(string(updated))
	assert.Contains(t, updatedFields, "root=/dev/mmcblk0p3", "root= must be rewritten to the new slot device")
	assert.NotContains(t, updatedFields, "root=/dev/mmcblk0p2")
	for _, tok := range []string{"console=serial0,115200", "console=tty1", "rootfstype=ext4", "rootwait", "fsck.repair=yes"} {
		assert.Contains(t, updatedFields, tok, "non-root= parameter %q must be preserved verbatim", tok)
	}

	prevPath := filepath.Join(bootDir, directBootPrevCmdline)
	prevContent, err := os.ReadFile(prevPath)
	require.NoError(t, err, "cmdline_prev.txt rollback backup must be written")
	assert.Equal(t, original+"\n", string(prevContent), "cmdline_prev.txt must hold the pre-switch (old-slot) cmdline verbatim")

	// A second call within the same OTA cycle (e.g. a re-run of
	// SetUpdatedPartition) must NOT clobber the existing backup — otherwise
	// the boot-health revert path would restore an already-current slot
	// instead of the true previous one.
	env.updateBootCmdline(bootDir, "/dev/mmcblk0p2")

	prevContentAfterSecondCall, err := os.ReadFile(prevPath)
	require.NoError(t, err)
	assert.Equal(t, original+"\n", string(prevContentAfterSecondCall), "cmdline_prev.txt must be written only once per OTA cycle")

	// But the second call must still update cmdline.txt's root= itself.
	updatedAgain, err := os.ReadFile(cmdlinePath)
	require.NoError(t, err)
	assert.Contains(t, strings.Fields(string(updatedAgain)), "root=/dev/mmcblk0p2")
}
