// TASK-S109-006 (S109 / TODO-157, bootloader compat mode): tests for the
// BootEnvMode "bootloader" switch inside dualRootfsDeviceImpl.
//
// In bootloader mode the bootloader (U-Boot altbootcmd / GRUB grubenv) owns the
// slot flip and the rollback. The agent must still write the standard boot
// variables through its BootEnvReadWriter (that is the whole slot switch), but
// it must NEVER run the native /usr/sbin/switch-boot-slot.sh and
// HandleBootCountFallback must NEVER clear upgrade_available / bootcount.
// File mode (the zero value) must keep behaving exactly as before.
package installer

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/binaryblack/OTA-Pulse/conf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installRecordingSwitchScript points switchBootSlotScriptPath at a fake
// script that appends "<args> A=<ROOTFS_A_PARTITION> B=<ROOTFS_B_PARTITION>" to a
// record file, and exits with exitCode. It returns the record-file path (the
// file only exists once the script has run). The real path is restored on
// cleanup, so the host's /usr/sbin is never touched.
func installRecordingSwitchScript(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "executed.log")
	script := filepath.Join(dir, "switch-boot-slot.sh")
	body := "#!/bin/sh\n" +
		"echo \"$1 A=$ROOTFS_A_PARTITION B=$ROOTFS_B_PARTITION\" >> '" + record + "'\n" +
		"exit " + string(rune('0'+exitCode)) + "\n"
	require.NoError(t, ioutil.WriteFile(script, []byte(body), 0700))

	old := switchBootSlotScriptPath
	switchBootSlotScriptPath = script
	t.Cleanup(func() { switchBootSlotScriptPath = old })
	return record
}

func assertScriptNotExecuted(t *testing.T, record string) {
	t.Helper()
	_, err := os.Stat(record)
	assert.True(t, os.IsNotExist(err), "switch-boot-slot.sh must not have been executed")
}

func readRecord(t *testing.T, record string) string {
	t.Helper()
	b, err := ioutil.ReadFile(record)
	require.NoError(t, err, "switch-boot-slot.sh should have been executed")
	return string(b)
}

// newModeDevice returns a device on A=/dev/mmcblk0p2 (active) and
// B=/dev/mmcblk0p3 (inactive).
func newModeDevice(env BootEnvReadWriter, bootloader bool) *dualRootfsDeviceImpl {
	return &dualRootfsDeviceImpl{
		BootEnvReadWriter: env,
		bootloaderMode:    bootloader,
		partitions: &partitions{
			rootfsPartA: "/dev/mmcblk0p2",
			rootfsPartB: "/dev/mmcblk0p3",
			active:      "/dev/mmcblk0p2",
			inactive:    "/dev/mmcblk0p3",
		},
	}
}

var installBootVars = BootVars{
	"upgrade_available":    "1",
	"mender_boot_part":     "3",
	"mender_boot_part_hex": "3",
	"bootcount":            "0",
}

// ------------------------------------------------------------ InstallUpdate

func Test_BootMode_InstallUpdate_fileMode_executesSwitchScript(t *testing.T) {
	record := installRecordingSwitchScript(t, 0)
	env := &fakeBootEnv{}
	dev := newModeDevice(env, false)

	require.NoError(t, dev.InstallUpdate())

	assert.Equal(t, installBootVars, env.writeVars)
	// Slot b, with the board-detected partitions passed through.
	assert.Equal(t, "b A=/dev/mmcblk0p2 B=/dev/mmcblk0p3\n", readRecord(t, record))
}

func Test_BootMode_InstallUpdate_bootloaderMode_neverExecutesSwitchScript(t *testing.T) {
	record := installRecordingSwitchScript(t, 0)
	env := &fakeBootEnv{}
	dev := newModeDevice(env, true)

	require.NoError(t, dev.InstallUpdate())

	// The upstream variables are still written: that is the slot switch.
	assert.Equal(t, installBootVars, env.writeVars)
	assertScriptNotExecuted(t, record)
}

// A failing script is fatal in file mode (and clears upgrade_available) but is
// irrelevant in bootloader mode, where it is never run.
func Test_BootMode_InstallUpdate_failingScript_fileModeFails_bootloaderModeSucceeds(t *testing.T) {
	record := installRecordingSwitchScript(t, 1)

	fileEnv := &fakeBootEnv{}
	err := newModeDevice(fileEnv, false).InstallUpdate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "switch-boot-slot.sh")
	// Last write is the clear of upgrade_available.
	assert.Equal(t, BootVars{"upgrade_available": "0"}, fileEnv.writeVars)
	readRecord(t, record) // it did run
	require.NoError(t, os.Remove(record))

	blEnv := &fakeBootEnv{}
	require.NoError(t, newModeDevice(blEnv, true).InstallUpdate())
	assert.Equal(t, installBootVars, blEnv.writeVars)
	assertScriptNotExecuted(t, record)
}

// ---------------------------------------------------------------- Rollback

// Pre-reboot rollback: mender_boot_part (3) != running slot (2), so it points
// the bootloader back at the running slot. File mode additionally runs the
// script for the slot it rolled back to; bootloader mode only writes the env.
func Test_BootMode_Rollback_preReboot(t *testing.T) {
	wantWrite := BootVars{
		"mender_boot_part":     "2",
		"mender_boot_part_hex": "2",
		"upgrade_available":    "0",
	}

	record := installRecordingSwitchScript(t, 0)
	fileEnv := &bootEnvMap{vars: BootVars{"upgrade_available": "1", "mender_boot_part": "3"}}
	require.NoError(t, newModeDevice(fileEnv, false).Rollback())
	require.Len(t, fileEnv.writes, 1)
	assert.Equal(t, wantWrite, fileEnv.writes[0])
	assert.Equal(t, "a A=/dev/mmcblk0p2 B=/dev/mmcblk0p3\n", readRecord(t, record))
	require.NoError(t, os.Remove(record))

	blEnv := &bootEnvMap{vars: BootVars{"upgrade_available": "1", "mender_boot_part": "3"}}
	require.NoError(t, newModeDevice(blEnv, true).Rollback())
	require.Len(t, blEnv.writes, 1)
	assert.Equal(t, wantWrite, blEnv.writes[0])
	assertScriptNotExecuted(t, record)
}

// Post-reboot rollback: the new slot (3) is the running one, mender_boot_part
// already names it, so rollback points the bootloader at the other slot (2).
func Test_BootMode_Rollback_postReboot(t *testing.T) {
	wantWrite := BootVars{
		"mender_boot_part":     "2",
		"mender_boot_part_hex": "2",
		"upgrade_available":    "0",
	}
	newPartitions := func() *partitions {
		return &partitions{
			rootfsPartA: "/dev/mmcblk0p2",
			rootfsPartB: "/dev/mmcblk0p3",
			active:      "/dev/mmcblk0p3", // running the new slot
			inactive:    "/dev/mmcblk0p2",
		}
	}

	record := installRecordingSwitchScript(t, 0)
	fileEnv := &bootEnvMap{vars: BootVars{"upgrade_available": "1", "mender_boot_part": "3"}}
	fileDev := newModeDevice(fileEnv, false)
	fileDev.partitions = newPartitions()
	require.NoError(t, fileDev.Rollback())
	assert.Equal(t, wantWrite, fileEnv.writes[0])
	assert.Equal(t, "a A=/dev/mmcblk0p2 B=/dev/mmcblk0p3\n", readRecord(t, record))
	require.NoError(t, os.Remove(record))

	blEnv := &bootEnvMap{vars: BootVars{"upgrade_available": "1", "mender_boot_part": "3"}}
	blDev := newModeDevice(blEnv, true)
	blDev.partitions = newPartitions()
	require.NoError(t, blDev.Rollback())
	require.Len(t, blEnv.writes, 1)
	assert.Equal(t, wantWrite, blEnv.writes[0])
	assertScriptNotExecuted(t, record)
}

func Test_BootMode_Rollback_failingScript_fileModeFails_bootloaderModeSucceeds(t *testing.T) {
	record := installRecordingSwitchScript(t, 1)

	fileEnv := &bootEnvMap{vars: BootVars{"upgrade_available": "1", "mender_boot_part": "3"}}
	err := newModeDevice(fileEnv, false).Rollback()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "switch-boot-slot.sh")
	require.NoError(t, os.Remove(record))

	blEnv := &bootEnvMap{vars: BootVars{"upgrade_available": "1", "mender_boot_part": "3"}}
	require.NoError(t, newModeDevice(blEnv, true).Rollback())
	assertScriptNotExecuted(t, record)
}

// No pending update: Rollback is a no-op in both modes and runs nothing.
func Test_BootMode_Rollback_noUpdatePending_runsNothing(t *testing.T) {
	for _, bl := range []bool{false, true} {
		record := installRecordingSwitchScript(t, 0)
		env := &bootEnvMap{vars: BootVars{"upgrade_available": "0", "mender_boot_part": "2"}}
		require.NoError(t, newModeDevice(env, bl).Rollback())
		assert.Empty(t, env.writes, "bootloader=%v", bl)
		assertScriptNotExecuted(t, record)
	}
}

// ----------------------------------------------------- HandleBootCountFallback

func Test_BootMode_HandleBootCountFallback_spikeEnv_neverClearsInBootloaderMode(t *testing.T) {
	// The env the S109 spike really saw (docs/mender-compat/SPIKE.md s7):
	// bootlimit=1; bootcount 1 in steady state, 2 after a rollback (altbootcmd
	// already flipped the slot and cleared upgrade_available); 0 right after
	// the agent armed an install. The extra rows push bootcount past
	// DefaultMaxBootRetries with the flag still armed: the case that clears in
	// file mode and must not in bootloader mode.
	cases := []struct {
		name string
		vars BootVars
	}{
		{"steady state", BootVars{"bootlimit": "1", "bootcount": "1", "upgrade_available": "0"}},
		{"after rollback", BootVars{"bootlimit": "1", "bootcount": "2", "upgrade_available": "0"}},
		{"install armed", BootVars{"bootlimit": "1", "bootcount": "0", "upgrade_available": "1"}},
		{"first boot of new slot", BootVars{"bootlimit": "1", "bootcount": "1", "upgrade_available": "1"}},
		{"second boot, armed", BootVars{"bootlimit": "1", "bootcount": "2", "upgrade_available": "1"}},
		{"at max retries, armed", BootVars{"bootlimit": "1", "bootcount": "3", "upgrade_available": "1"}},
		{"above max retries, armed", BootVars{"bootlimit": "1", "bootcount": "7", "upgrade_available": "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := &bootEnvMap{vars: tc.vars}
			before := BootVars{}
			for k, v := range tc.vars {
				before[k] = v
			}
			newModeDevice(env, true).HandleBootCountFallback()

			assert.Empty(t, env.writes, "bootloader mode must never write the env here")
			assert.Equal(t, before, env.vars, "flags must be untouched")
		})
	}
	assert.GreaterOrEqual(t, 7, DefaultMaxBootRetries, "test rows must cross the in-agent threshold")
}

// File mode is unchanged: it clears both flags at bootcount >= DefaultMaxBootRetries
// with upgrade_available=1 and does nothing below it or when not armed.
func Test_BootMode_HandleBootCountFallback_fileMode_unchanged(t *testing.T) {
	for _, bc := range []string{"3", "7"} {
		env := &bootEnvMap{vars: BootVars{"bootlimit": "1", "bootcount": bc, "upgrade_available": "1"}}
		newModeDevice(env, false).HandleBootCountFallback()
		require.Len(t, env.writes, 1, "bootcount=%s", bc)
		assert.Equal(t, BootVars{"upgrade_available": "0", "bootcount": "0"}, env.writes[0])
	}
	for _, vars := range []BootVars{
		{"bootlimit": "1", "bootcount": "1", "upgrade_available": "1"},
		{"bootlimit": "1", "bootcount": "2", "upgrade_available": "1"},
		{"bootlimit": "1", "bootcount": "7", "upgrade_available": "0"},
	} {
		env := &bootEnvMap{vars: vars}
		newModeDevice(env, false).HandleBootCountFallback()
		assert.Empty(t, env.writes, "%v", vars)
	}
}

// Bootloader mode does not even read the env (nothing for it to act on).
func Test_BootMode_HandleBootCountFallback_bootloaderMode_doesNotTouchEnv(t *testing.T) {
	env := &countingBootEnv{}
	newModeDevice(env, true).HandleBootCountFallback()
	assert.Zero(t, env.reads)
	assert.Zero(t, env.writes)
}

type countingBootEnv struct{ reads, writes int }

func (c *countingBootEnv) ReadEnv(...string) (BootVars, error) { c.reads++; return BootVars{}, nil }
func (c *countingBootEnv) WriteEnv(BootVars) error             { c.writes++; return nil }

// ------------------------------------------------------- constructor / plumbing

func Test_BootMode_NewDualRootfsDevice_defaultIsFileMode_optionSetsBootloaderMode(t *testing.T) {
	cfg := conf.DualRootfsDeviceConfig{RootfsPartA: "/dev/mmcblk0p2", RootfsPartB: "/dev/mmcblk0p3"}
	env := &fakeBootEnv{}

	def := NewDualRootfsDevice(env, nil, cfg)
	require.NotNil(t, def)
	assert.False(t, def.(interface{ IsBootloaderMode() bool }).IsBootloaderMode())

	bl := NewDualRootfsDevice(env, nil, cfg, WithBootloaderMode())
	require.NotNil(t, bl)
	assert.True(t, bl.(interface{ IsBootloaderMode() bool }).IsBootloaderMode())

	// The nil contract is unchanged by the option.
	assert.Nil(t, NewDualRootfsDevice(env, nil, conf.DualRootfsDeviceConfig{}, WithBootloaderMode()))
}

// DeltaRootfs embeds the DualRootfsDevice interface and the factory keeps the
// same pointer (no struct copy), so the mode flag reaches the inherited
// InstallUpdate / Rollback / HandleBootCountFallback.
func Test_BootMode_DeltaRootfsInstaller_inheritsMode(t *testing.T) {
	for _, bl := range []bool{false, true} {
		record := installRecordingSwitchScript(t, 0)
		env := &bootEnvMap{vars: BootVars{"upgrade_available": "0", "mender_boot_part": "2",
			"bootcount": "5"}}
		dev := newModeDevice(env, bl)
		inst := NewDeltaRootfsFactory(dev, &fakeProvides{p: map[string]string{}}, 60).newInstaller()

		require.NoError(t, inst.InstallUpdate())
		if bl {
			assertScriptNotExecuted(t, record)
		} else {
			assert.Equal(t, "b A=/dev/mmcblk0p2 B=/dev/mmcblk0p3\n", readRecord(t, record))
			require.NoError(t, os.Remove(record))
		}
		assert.Equal(t, "1", env.vars["upgrade_available"])
		assert.Equal(t, "3", env.vars["mender_boot_part"])

		// bootcount is 0 now (InstallUpdate wrote it); force the file-mode
		// clear threshold and check the inherited HandleBootCountFallback.
		env.vars["bootcount"] = "9"
		env.writes = nil
		inst.HandleBootCountFallback()
		if bl {
			assert.Empty(t, env.writes)
			assert.Equal(t, "1", env.vars["upgrade_available"])
		} else {
			require.Len(t, env.writes, 1)
			assert.Equal(t, "0", env.vars["upgrade_available"])
		}

		// Rollback inherited too.
		env.vars["upgrade_available"] = "1"
		env.vars["mender_boot_part"] = "3"
		env.writes = nil
		require.NoError(t, inst.Rollback())
		require.Len(t, env.writes, 1)
		if bl {
			assertScriptNotExecuted(t, record)
		} else {
			assert.Equal(t, "a A=/dev/mmcblk0p2 B=/dev/mmcblk0p3\n", readRecord(t, record))
		}
	}
}
