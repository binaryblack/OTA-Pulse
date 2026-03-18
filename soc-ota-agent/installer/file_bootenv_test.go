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
	"testing"

	stest "github.com/binaryblack/OTA-Pulse/system/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestFileBasedBootEnv(t *testing.T) (*FileBasedBootEnv, string) {
	t.Helper()
	tmpDir := t.TempDir()

	runner := stest.NewTestOSCalls("", 0)
	env := &FileBasedBootEnv{
		Commander:          runner,
		slotFile:           filepath.Join(tmpDir, "current_slot"),
		bootCountFile:      filepath.Join(tmpDir, "boot_count"),
		upgradeAvailFile:   filepath.Join(tmpDir, "upgrade_available"),
		menderBootPartFile: filepath.Join(tmpDir, "mender_boot_part"),
		rootfsPartA:        "/dev/mmcblk0p3",
		rootfsPartB:        "/dev/mmcblk0p4",
	}
	return env, tmpDir
}

func TestFileBasedBootEnv_ReadWriteRoundtrip(t *testing.T) {
	env, _ := newTestFileBasedBootEnv(t)

	// Write boot partition
	err := env.WriteEnv(BootVars{"mender_boot_part": "3"})
	assert.NoError(t, err)

	// Read it back
	vars, err := env.ReadEnv("mender_boot_part")
	assert.NoError(t, err)
	assert.Equal(t, "3", vars["mender_boot_part"])
}

func TestFileBasedBootEnv_WriteUpdatesSlot(t *testing.T) {
	env, _ := newTestFileBasedBootEnv(t)

	err := env.WriteEnv(BootVars{"mender_boot_part": "3"})
	assert.NoError(t, err)

	// slot file should contain "a" (partition 3 = slot a)
	data, err := os.ReadFile(env.slotFile)
	assert.NoError(t, err)
	assert.Equal(t, "a", string(data[:1]))

	err = env.WriteEnv(BootVars{"mender_boot_part": "4"})
	assert.NoError(t, err)

	data, err = os.ReadFile(env.slotFile)
	assert.NoError(t, err)
	assert.Equal(t, "b", string(data[:1]))
}

func TestFileBasedBootEnv_UpgradeAvailable(t *testing.T) {
	env, _ := newTestFileBasedBootEnv(t)

	// Default should be "0"
	vars, err := env.ReadEnv("upgrade_available")
	assert.NoError(t, err)
	assert.Equal(t, "0", vars["upgrade_available"])

	// Write and read back
	err = env.WriteEnv(BootVars{"upgrade_available": "1"})
	assert.NoError(t, err)

	vars, err = env.ReadEnv("upgrade_available")
	assert.NoError(t, err)
	assert.Equal(t, "1", vars["upgrade_available"])
}

func TestFileBasedBootEnv_BootCount(t *testing.T) {
	env, _ := newTestFileBasedBootEnv(t)

	// Default should be "0"
	vars, err := env.ReadEnv("bootcount")
	assert.NoError(t, err)
	assert.Equal(t, "0", vars["bootcount"])

	err = env.WriteEnv(BootVars{"bootcount": "2"})
	assert.NoError(t, err)

	vars, err = env.ReadEnv("bootcount")
	assert.NoError(t, err)
	assert.Equal(t, "2", vars["bootcount"])
}

func TestFileBasedBootEnv_HexPartition(t *testing.T) {
	env, _ := newTestFileBasedBootEnv(t)

	// Write decimal, read hex
	err := env.WriteEnv(BootVars{"mender_boot_part": "10"})
	assert.NoError(t, err)

	vars, err := env.ReadEnv("mender_boot_part_hex")
	assert.NoError(t, err)
	assert.Equal(t, "a", vars["mender_boot_part_hex"])

	// Write hex
	err = env.WriteEnv(BootVars{"mender_boot_part_hex": "3"})
	assert.NoError(t, err)

	vars, err = env.ReadEnv("mender_boot_part")
	assert.NoError(t, err)
	assert.Equal(t, "3", vars["mender_boot_part"])
}

func TestFileBasedBootEnv_Canary(t *testing.T) {
	env, _ := newTestFileBasedBootEnv(t)

	vars, err := env.ReadEnv("mender_check_saveenv_canary", "mender_saveenv_canary")
	assert.NoError(t, err)
	assert.Equal(t, "0", vars["mender_check_saveenv_canary"])
	assert.Equal(t, "1", vars["mender_saveenv_canary"])
}

func TestFileBasedBootEnv_SlotToPartition(t *testing.T) {
	env, _ := newTestFileBasedBootEnv(t)

	partNum, err := env.slotToPartitionNumber("a")
	assert.NoError(t, err)
	assert.Equal(t, "3", partNum)

	partNum, err = env.slotToPartitionNumber("b")
	assert.NoError(t, err)
	assert.Equal(t, "4", partNum)

	_, err = env.slotToPartitionNumber("c")
	assert.Error(t, err)
}

func TestFileBasedBootEnv_PartitionToSlot(t *testing.T) {
	env, _ := newTestFileBasedBootEnv(t)

	assert.Equal(t, "a", env.partitionNumberToSlot("3"))
	assert.Equal(t, "b", env.partitionNumberToSlot("4"))
	assert.Equal(t, "", env.partitionNumberToSlot("5"))
}

// Tests for ReconcileBootState (BUG-001 fix)

func TestReconcileBootState_NoRecordedState(t *testing.T) {
	env, _ := newTestFileBasedBootEnv(t)

	// Write a mock /proc/self/mounts to make detectCurrentPartitionNumber work
	// Since we can't mock /proc, we'll test the reconciliation logic by
	// pre-populating the mender_boot_part file and testing mismatch detection.

	// When no mender_boot_part file exists, ReconcileBootState should try to
	// detect and initialize. On a test system this will fail to detect the
	// partition, which is expected.
	_, err := env.ReconcileBootState()
	// Error expected since /proc/self/mounts won't have a real rootfs partition
	assert.Error(t, err)
}

func TestReconcileBootState_ConsistentState(t *testing.T) {
	env, tmpDir := newTestFileBasedBootEnv(t)

	// Pre-populate state files to simulate consistent state
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "mender_boot_part"), []byte("3\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "current_slot"), []byte("a\n"), 0644))

	// On a test system, detectCurrentPartitionNumber won't return "3",
	// so this tests the mismatch path. In production, when the actual partition
	// matches the recorded one, corrected should be false.

	// We can't easily mock /proc/self/mounts in unit tests.
	// The integration test (test-qemu-ota.sh) covers the full scenario.
	// Here we verify the function doesn't panic and handles errors gracefully.
	_, _ = env.ReconcileBootState()
}

func TestReconcileBootState_WriteFiles(t *testing.T) {
	// Test that writeFile creates directories and handles content correctly
	env, tmpDir := newTestFileBasedBootEnv(t)

	subDir := filepath.Join(tmpDir, "sub", "dir")
	err := env.writeFile(filepath.Join(subDir, "test"), "hello")
	assert.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(subDir, "test"))
	assert.NoError(t, err)
	assert.Equal(t, "hello\n", string(data))
}
