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
	"time"

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

// --- BUG-110: ReconcileToBootedSlot /data/ota realignment ------------------
//
// On systemd-boot / loader.conf boards the boot slot is owned by /boot, so the
// agent's own /data/ota bookkeeping can lag the actually-booted slot forever
// after a switch. ReconcileToBootedSlot realigns it once per boot, but MUST NOT
// run while an OTA is pending (upgrade_available=1), because mender_boot_part
// then points at the pending target slot, not the running one.
//
// The temp env is built with rootfsPartA=/dev/mmcblk0p2 (slot "a", part "2")
// and rootfsPartB=/dev/mmcblk0p3 (slot "b", part "3").

// (d) Stale /data/ota + upgrade_available=0 + a valid, different detected
// partition ⇒ both files are rewritten to the detected slot.
func TestReconcileToBootedSlot_RewritesStaleBookkeeping(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	env.detectFn = func() (string, error) { return "2", nil } // running slot-a

	require.NoError(t, os.WriteFile(env.upgradeAvailFile, []byte("0\n"), 0644))
	require.NoError(t, os.WriteFile(env.menderBootPartFile, []byte("3\n"), 0644)) // stale slot-b
	require.NoError(t, os.WriteFile(env.slotFile, []byte("b\n"), 0644))           // stale slot-b

	env.ReconcileToBootedSlot()

	part, err := os.ReadFile(env.menderBootPartFile)
	require.NoError(t, err)
	assert.Equal(t, "2", strings.TrimSpace(string(part)), "mender_boot_part must be realigned to the booted partition")

	slot, err := os.ReadFile(env.slotFile)
	require.NoError(t, err)
	assert.Equal(t, "a", strings.TrimSpace(string(slot)), "current_slot must be realigned to the booted slot")
}

// (e) SAFETY GATE — the most important test: with upgrade_available=1 the
// reconcile must touch NOTHING, even though the detected slot differs from the
// stale /data/ota values (which, mid-OTA, intentionally point at the pending
// target slot).
func TestReconcileToBootedSlot_UpgradePendingGateBlocksAllWrites(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	env.detectFn = func() (string, error) { return "2", nil } // running slot-a

	require.NoError(t, os.WriteFile(env.upgradeAvailFile, []byte("1\n"), 0644))   // OTA pending
	require.NoError(t, os.WriteFile(env.menderBootPartFile, []byte("3\n"), 0644)) // target slot-b
	require.NoError(t, os.WriteFile(env.slotFile, []byte("b\n"), 0644))           // target slot-b

	env.ReconcileToBootedSlot()

	part, err := os.ReadFile(env.menderBootPartFile)
	require.NoError(t, err)
	assert.Equal(t, "3", strings.TrimSpace(string(part)), "mender_boot_part must be untouched while an OTA is pending")

	slot, err := os.ReadFile(env.slotFile)
	require.NoError(t, err)
	assert.Equal(t, "b", strings.TrimSpace(string(slot)), "current_slot must be untouched while an OTA is pending")
}

// (f) An unrecognized/unmapped detected partition (not part of the configured
// A/B pair) ⇒ no files are written; a slot outside the pair must never be
// persisted.
func TestReconcileToBootedSlot_UnmappedPartitionWritesNothing(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	env.detectFn = func() (string, error) { return "9", nil } // not part A(2) or B(3)

	require.NoError(t, os.WriteFile(env.upgradeAvailFile, []byte("0\n"), 0644))

	env.ReconcileToBootedSlot()

	_, statErr := os.Stat(env.menderBootPartFile)
	assert.True(t, os.IsNotExist(statErr), "mender_boot_part must not be created for an unmapped partition")
	_, statErr = os.Stat(env.slotFile)
	assert.True(t, os.IsNotExist(statErr), "current_slot must not be created for an unmapped partition")
}

// (g) When /data/ota already matches the booted slot (the common every-boot
// case) the reconcile is a pure no-op: it must not rewrite the files. Proven by
// stamping an old mtime on the files and asserting it is unchanged after the
// call (writeFile would refresh mtime).
func TestReconcileToBootedSlot_AlreadyInSyncIsNoOp(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	env.detectFn = func() (string, error) { return "2", nil } // running slot-a

	require.NoError(t, os.WriteFile(env.upgradeAvailFile, []byte("0\n"), 0644))
	require.NoError(t, os.WriteFile(env.menderBootPartFile, []byte("2\n"), 0644)) // already slot-a
	require.NoError(t, os.WriteFile(env.slotFile, []byte("a\n"), 0644))           // already slot-a

	old := time.Unix(1000000000, 0)
	require.NoError(t, os.Chtimes(env.menderBootPartFile, old, old))
	require.NoError(t, os.Chtimes(env.slotFile, old, old))

	env.ReconcileToBootedSlot()

	fi, err := os.Stat(env.menderBootPartFile)
	require.NoError(t, err)
	assert.True(t, fi.ModTime().Equal(old), "mender_boot_part must not be rewritten when already in sync")

	fi, err = os.Stat(env.slotFile)
	require.NoError(t, err)
	assert.True(t, fi.ModTime().Equal(old), "current_slot must not be rewritten when already in sync")
}

// (h) REGRESSION for the Jetson by-partlabel boot-slot bug: NewFileBasedBootEnv
// must resolve rootfsPartA/rootfsPartB via maybeResolveLink, exactly like
// NewDualRootfsDevice already does (dual_rootfs_device.go). Before the fix,
// a board configured with unresolved /dev/disk/by-partlabel/... paths stored
// them verbatim; extractPartitionNumber found no trailing digit and returned
// "", so partitionNumberToSlot could never map the running partition to slot
// a or b and ReconcileToBootedSlot silently no-op'd on every single boot
// (the exact, previously zero-coverage case that broke dev-aedbdbbe).
//
// resolvePathsInDirs is swapped for the duration of this test so the
// by-partlabel resolution branch of maybeResolveLink can be exercised
// against a temp directory instead of requiring real /dev/disk/by-partlabel
// entries (not present, and not writable as non-root, in a CI/dev sandbox).
func TestNewFileBasedBootEnv_ResolvesByPartlabelPaths(t *testing.T) {
	tmp := t.TempDir()

	// Simulate real partition device nodes, e.g. /dev/mmcblk0p1, /dev/mmcblk0p2.
	partA := filepath.Join(tmp, "mmcblk0p1")
	partB := filepath.Join(tmp, "mmcblk0p2")
	require.NoError(t, os.WriteFile(partA, nil, 0644))
	require.NoError(t, os.WriteFile(partB, nil, 0644))

	// Simulate /dev/disk/by-partlabel/rootfs_a|_b symlinks to those nodes.
	labelDir := filepath.Join(tmp, "by-partlabel")
	require.NoError(t, os.Mkdir(labelDir, 0755))
	linkA := filepath.Join(labelDir, "rootfs_a")
	linkB := filepath.Join(labelDir, "rootfs_b")
	require.NoError(t, os.Symlink(partA, linkA))
	require.NoError(t, os.Symlink(partB, linkB))

	origDirs := resolvePathsInDirs
	resolvePathsInDirs = map[string]bool{labelDir: true}
	defer func() { resolvePathsInDirs = origDirs }()

	env := NewFileBasedBootEnv(&recordingCommander{}, linkA, linkB)
	stateDir := t.TempDir()
	env.slotFile = filepath.Join(stateDir, "current_slot")
	env.bootCountFile = filepath.Join(stateDir, "boot_count")
	env.upgradeAvailFile = filepath.Join(stateDir, "upgrade_available")
	env.menderBootPartFile = filepath.Join(stateDir, "mender_boot_part")

	assert.Equal(t, partA, env.rootfsPartA,
		"rootfsPartA must be resolved to the real device node, not left as an unresolvable by-partlabel symlink")
	assert.Equal(t, partB, env.rootfsPartB,
		"rootfsPartB must be resolved to the real device node, not left as an unresolvable by-partlabel symlink")

	// With resolution in place, partition numbers are extractable and
	// partitionNumberToSlot can map the running partition to its slot —
	// this is exactly what was permanently broken before the fix.
	numA := extractPartitionNumber(partA)
	numB := extractPartitionNumber(partB)
	require.NotEmpty(t, numA, "resolved rootfsPartA must have an extractable trailing partition number")
	require.NotEmpty(t, numB, "resolved rootfsPartB must have an extractable trailing partition number")

	assert.Equal(t, "a", env.partitionNumberToSlot(numA))
	assert.Equal(t, "b", env.partitionNumberToSlot(numB))

	// And ReconcileToBootedSlot — dead on every boot before the fix — now
	// actually realigns stale bookkeeping to the booted slot.
	env.detectFn = func() (string, error) { return numA, nil }
	require.NoError(t, os.WriteFile(env.upgradeAvailFile, []byte("0\n"), 0644))
	require.NoError(t, os.WriteFile(env.menderBootPartFile, []byte(numB+"\n"), 0644)) // stale
	require.NoError(t, os.WriteFile(env.slotFile, []byte("b\n"), 0644))               // stale

	env.ReconcileToBootedSlot()

	part, err := os.ReadFile(env.menderBootPartFile)
	require.NoError(t, err)
	assert.Equal(t, numA, strings.TrimSpace(string(part)))

	slot, err := os.ReadFile(env.slotFile)
	require.NoError(t, err)
	assert.Equal(t, "a", strings.TrimSpace(string(slot)))
}
