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

	"github.com/pkg/errors"
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

// --- BUG-353: VideoCore/RPi tryboot platforms must never get the permanent
// cmdline.txt rewrite at install time, and the post-install reboot must carry
// the firmware's "0 tryboot" argument exactly when a try is armed -----------

func TestApplyDirectBootSlot_VideoCoreTrybootPlatformLeavesCmdlineUntouched(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	bootDir := t.TempDir()
	original := "console=serial0,115200 root=/dev/mmcblk0p2 rootfstype=ext4 rootwait"
	cmdlinePath := filepath.Join(bootDir, "cmdline.txt")
	require.NoError(t, os.WriteFile(cmdlinePath, []byte(original+"\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bootDir, "config.txt"), []byte("enable_uart=1\n"), 0644))

	env.applyDirectBootSlot(bootDir, "/dev/mmcblk0p3")

	after, err := os.ReadFile(cmdlinePath)
	require.NoError(t, err)
	assert.Equal(t, original+"\n", string(after),
		"permanent cmdline.txt must be left untouched on a VideoCore tryboot platform (BUG-353)")
	_, statErr := os.Stat(filepath.Join(bootDir, directBootPrevCmdline))
	assert.True(t, os.IsNotExist(statErr), "no cmdline_prev.txt breadcrumb may be written when nothing was rewritten")
}

func TestApplyDirectBootSlot_NonVideoCoreDirectBootStillRewrites(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	bootDir := t.TempDir()
	original := "console=ttyS0 root=/dev/mmcblk0p2 rootwait"
	cmdlinePath := filepath.Join(bootDir, "cmdline.txt")
	require.NoError(t, os.WriteFile(cmdlinePath, []byte(original+"\n"), 0644))
	// deliberately NO config.txt -> not a VideoCore platform

	env.applyDirectBootSlot(bootDir, "/dev/mmcblk0p3")

	after, err := os.ReadFile(cmdlinePath)
	require.NoError(t, err)
	assert.Contains(t, strings.Fields(string(after)), "root=/dev/mmcblk0p3",
		"non-VideoCore direct-boot boards keep the permanent rewrite")
	prev, err := os.ReadFile(filepath.Join(bootDir, directBootPrevCmdline))
	require.NoError(t, err)
	assert.Equal(t, original+"\n", string(prev))
}

func TestTrybootRebootArgumentFor(t *testing.T) {
	write := func(dir, name string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0644))
	}

	armed := t.TempDir() // VideoCore platform with an armed try -> "0 tryboot"
	write(armed, "config.txt")
	write(armed, "cmdline.txt")
	write(armed, "tryboot.txt")
	assert.Equal(t, "0 tryboot", trybootRebootArgumentFor(armed))

	idle := t.TempDir() // VideoCore platform, nothing armed -> plain reboot
	write(idle, "config.txt")
	write(idle, "cmdline.txt")
	assert.Equal(t, "", trybootRebootArgumentFor(idle))

	notVC := t.TempDir() // tryboot.txt but no config.txt (not VideoCore) -> plain reboot
	write(notVC, "cmdline.txt")
	write(notVC, "tryboot.txt")
	assert.Equal(t, "", trybootRebootArgumentFor(notVC))

	assert.Equal(t, "", trybootRebootArgumentFor(filepath.Join(t.TempDir(), "nope")))
}

// The RPi firmware driver matches " tryboot" (WITH the leading space) in the
// reboot(2) argument — pin the exact string the agent passes.
func TestTrybootRebootArgument_MatchesFirmwareContract(t *testing.T) {
	assert.Equal(t, "0 tryboot", trybootRebootArgument)
	assert.Contains(t, trybootRebootArgument, " tryboot")
}

// --- TASK-S55-004 (TODO-049/S55) -------------------------------------------
//
// Executed with a real `go test ./installer/...` (golang:1.21 docker image,
// matching go.mod) during authoring, not just hand-verified — see this
// task's tasks.yaml completion_note for the exact command and result.

func TestIsBootScrNumericBoard(t *testing.T) {
	write := func(dir string, rel ...string) {
		full := filepath.Join(dir, filepath.Join(rel...))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, []byte("x\n"), 0644))
	}

	numeric := t.TempDir() // imx8mp-frdm / orange-pi-zero2w / beagleplay-ti class
	write(numeric, "boot.scr")
	assert.True(t, isBootScrNumericBoard(numeric))

	noBootScr := t.TempDir() // e.g. Jetson: no boot.scr candidate at all
	assert.False(t, isBootScrNumericBoard(noBootScr))

	rpi4 := t.TempDir() // VideoCore direct-boot excludes it even with boot.scr present
	write(rpi4, "boot.scr")
	write(rpi4, "config.txt")
	write(rpi4, "cmdline.txt")
	assert.False(t, isBootScrNumericBoard(rpi4))

	cm5 := t.TempDir() // boot.scr built as a harmless fallback, real boot is extlinux
	write(cm5, "boot.scr")
	write(cm5, "extlinux", "extlinux.conf")
	assert.False(t, isBootScrNumericBoard(cm5))

	systemdBoot := t.TempDir() // defense-in-depth: a future systemd-boot-class board
	write(systemdBoot, "boot.scr")
	write(systemdBoot, "loader", "entries")
	assert.False(t, isBootScrNumericBoard(systemdBoot))
}

func TestResolveOldPartForRollback(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	env.detectFn = func() (string, error) { return "2", nil } // running slot-a

	nonNumeric := t.TempDir() // no boot.scr -> always "", regardless of running root
	assert.Equal(t, "", env.resolveOldPartForRollback(nonNumeric))

	numeric := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(numeric, "boot.scr"), []byte("x\n"), 0644))
	assert.Equal(t, "2", env.resolveOldPartForRollback(numeric),
		"a numeric board's first-ever OTA must derive the rollback target from the running root partition")
}

// Fable's TASK-S55-004 review, R1: when detection itself fails (not just
// when the board is non-numeric), resolveOldPartForRollback must return ""
// — and armPendingSwitchRollback must then fail closed for that board,
// exactly as if the FAT file and the running-root fallback had both come up
// empty. This is the "no rollback target available at all" case.
func TestResolveOldPartForRollback_DetectionFailureFailsClosedDownstream(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	env.detectFn = func() (string, error) { return "", errors.New("no root mount found") }

	numeric := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(numeric, "boot.scr"), []byte("x\n"), 0644))
	assert.Equal(t, "", env.resolveOldPartForRollback(numeric))

	err := env.armPendingSwitchRollback(numeric, env.resolveOldPartForRollback(numeric), "3")
	require.Error(t, err, "a numeric board with NO rollback target at all (FAT absent AND running-root detection failed) must fail closed")
	assert.Contains(t, err.Error(), "no rollback target available")
}

// R1's other half: an EXISTING but garbled/empty FAT mender_boot_part must
// be treated the same as an absent one, not silently accepted as a (wrong)
// literal value. This is exercised at the syncBootSlotToBootPartitionOnce
// call site (partitionNumberToSlot(oldPart) == "" triggers the same
// resolveOldPartForRollback fallback as statErr != nil) — proven here via
// partitionNumberToSlot directly, since the full mount-discovery path is not
// sandboxable (see newTempFileBasedBootEnv's own doc comment).
func TestPartitionNumberToSlot_RejectsGarbledValue(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	assert.Equal(t, "", env.partitionNumberToSlot(""), "an empty/garbled mender_boot_part must not resolve to a real slot")
	assert.Equal(t, "", env.partitionNumberToSlot("9"), "a value that is neither rootfsPartA nor rootfsPartB must not resolve to a real slot")
	assert.Equal(t, "a", env.partitionNumberToSlot("2"), "sanity: a genuinely valid partition number must still resolve")
}

// oldPart == partNum ("nothing is actually switching") is always a true
// no-op, on every board — there was never a real switch to arm a rollback
// for.
func TestArmPendingSwitchRollback_NoOpWhenUnchanged(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	bootDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bootDir, "boot.scr"), []byte("x\n"), 0644))

	require.NoError(t, env.armPendingSwitchRollback(bootDir, "2", "2"))

	_, err := os.Stat(filepath.Join(bootDir, "mender_boot_part_prev"))
	assert.True(t, os.IsNotExist(err), "an unchanged slot must not write a rollback backup")
}

// oldPart == "" ("no rollback target could be determined at all") behaves
// DIFFERENTLY per board class since Fable's TASK-S55-004 review (R1): a
// non-numeric board stays a silent no-op (there was never a rollback target
// to lose), but a numeric board now fails closed — see
// TestResolveOldPartForRollback_DetectionFailureFailsClosedDownstream for
// the fail-closed half of this.
func TestArmPendingSwitchRollback_EmptyOldPartIsNoOpOnlyOnNonNumericBoard(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	nonNumeric := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(nonNumeric, "config.txt"), []byte("x\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(nonNumeric, "cmdline.txt"), []byte("x\n"), 0644))

	require.NoError(t, env.armPendingSwitchRollback(nonNumeric, "", "3"))
	_, err := os.Stat(filepath.Join(nonNumeric, "mender_boot_part_prev"))
	assert.True(t, os.IsNotExist(err))
}

// The actual TODO-049 fix: on a boot.scr-numeric board, uboot_boot_count and
// boot_count must be CREATED (not merely reset-if-present) so U-Boot's
// fatwrite — which can only overwrite an existing file — has something to
// overwrite on a board's very first arm.
func TestArmPendingSwitchRollback_NumericBoardCreatesCounters(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	bootDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bootDir, "boot.scr"), []byte("x\n"), 0644))

	require.NoError(t, env.armPendingSwitchRollback(bootDir, "2", "3"))

	prev, err := os.ReadFile(filepath.Join(bootDir, "mender_boot_part_prev"))
	require.NoError(t, err)
	assert.Equal(t, "2\n", string(prev))

	for _, name := range []string{"uboot_boot_count", "boot_count"} {
		content, err := os.ReadFile(filepath.Join(bootDir, name))
		require.NoError(t, err, "%s must be created on a boot.scr-numeric board even though it did not exist before", name)
		assert.Equal(t, "0", string(content))
	}
}

// Non-numeric boards (RPi4/VideoCore, CM5) must keep the ORIGINAL
// conservative behavior: counters are reset only if already present, never
// created — nothing on those boards ever creates or reads these files, so
// creating them would be unread clutter.
func TestArmPendingSwitchRollback_NonNumericBoardDoesNotCreateCounters(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	bootDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bootDir, "config.txt"), []byte("x\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bootDir, "cmdline.txt"), []byte("x\n"), 0644))

	require.NoError(t, env.armPendingSwitchRollback(bootDir, "2", "3"))

	prev, err := os.ReadFile(filepath.Join(bootDir, "mender_boot_part_prev"))
	require.NoError(t, err, "the prev backup itself is best-effort but still attempted on every board")
	assert.Equal(t, "2\n", string(prev))

	for _, name := range []string{"uboot_boot_count", "boot_count"} {
		_, err := os.Stat(filepath.Join(bootDir, name))
		assert.True(t, os.IsNotExist(err), "%s must NOT be created on a non-numeric board", name)
	}
}

// The fail-closed hardening itself: on a boot.scr-numeric board, a write
// failure for the rollback backup must abort the slot switch (return an
// error) rather than silently proceeding best-effort as before.
func TestArmPendingSwitchRollback_NumericBoardFailsClosedOnWriteFailure(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	bootDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bootDir, "boot.scr"), []byte("x\n"), 0644))
	require.True(t, isBootScrNumericBoard(bootDir), "sanity: this dir must be detected as numeric, or the rest of this test proves nothing")

	// Force the mender_boot_part_prev write to fail without needing
	// permission tricks: put a DIRECTORY at the exact path
	// armPendingSwitchRollback will try to open as a FILE for writing.
	blocker := filepath.Join(bootDir, "mender_boot_part_prev")
	require.NoError(t, os.MkdirAll(blocker, 0755))

	err := env.armPendingSwitchRollback(bootDir, "2", "3")
	require.Error(t, err, "a numeric board that cannot write its rollback backup must fail closed")
	assert.Contains(t, err.Error(), "refusing to proceed")
}

// Fable's TASK-S55-004 review, R3: only the _prev write failure was covered
// above — a COUNTER file write failure (uboot_boot_count/boot_count) must
// fail closed too, independently of whether _prev itself succeeded.
func TestArmPendingSwitchRollback_NumericBoardFailsClosedOnCounterWriteFailure(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	bootDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bootDir, "boot.scr"), []byte("x\n"), 0644))
	require.True(t, isBootScrNumericBoard(bootDir), "sanity: this dir must be detected as numeric, or the rest of this test proves nothing")

	// mender_boot_part_prev itself is left writable (a real file path, no
	// blocker) — only uboot_boot_count is blocked, proving the counter
	// write failure alone is sufficient to fail closed.
	blocker := filepath.Join(bootDir, "uboot_boot_count")
	require.NoError(t, os.MkdirAll(blocker, 0755))

	err := env.armPendingSwitchRollback(bootDir, "2", "3")
	require.Error(t, err, "a numeric board that cannot write a bootcount-net counter must fail closed, even if the _prev backup itself succeeded")
	assert.Contains(t, err.Error(), "refusing to proceed")

	prev, readErr := os.ReadFile(filepath.Join(bootDir, "mender_boot_part_prev"))
	require.NoError(t, readErr, "the _prev backup write itself should have succeeded before the counter write failed")
	assert.Equal(t, "2\n", string(prev))
}

// The same failure mode on a non-numeric board must stay best-effort — no
// error, matching this project's original (pre-TODO-049) behavior for RPi4/
// CM5-class boards, whose own working mechanisms never depend on this file.
func TestArmPendingSwitchRollback_NonNumericBoardStaysBestEffortOnWriteFailure(t *testing.T) {
	env, _ := newTempFileBasedBootEnv(t)
	bootDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bootDir, "config.txt"), []byte("x\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bootDir, "cmdline.txt"), []byte("x\n"), 0644))

	blocker := filepath.Join(bootDir, "mender_boot_part_prev")
	require.NoError(t, os.MkdirAll(blocker, 0755))

	err := env.armPendingSwitchRollback(bootDir, "2", "3")
	assert.NoError(t, err, "a non-numeric board must never abort the slot switch over this file")
}
