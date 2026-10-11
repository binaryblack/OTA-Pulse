// GC-AG-12 / TASK-S108-037: additive pin-current-behaviour tests for the
// boot-adjacent paths that previously had no direct coverage:
// verifyWrittenData, the StoreUpdate short-write guard, switchBootSlot and
// extractPartitionNumber. These tests document what the code does TODAY;
// they intentionally change no production code. Tightening any behaviour
// pinned here (e.g. the tolerated-missing-script branch, GC-AG-16/H-11) is a
// separate, attended-hardware change.
package installer

import (
	"bytes"
	"crypto/sha256"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTempFile creates a regular file with the given content in a per-test
// temp dir and returns its path.
func writeTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, ioutil.WriteFile(p, content, 0600))
	return p
}

func sha(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

// ---------------------------------------------------------------- verifyWrittenData

func Test_verifyWrittenData_matchingContent_passes(t *testing.T) {
	content := []byte("the quick brown fox jumps over the lazy dog")
	p := writeTempFile(t, "part", content)

	assert.NoError(t, verifyWrittenData(p, int64(len(content)), sha(content)))
}

func Test_verifyWrittenData_mismatchedContent_failsWithBothHashes(t *testing.T) {
	written := []byte("what we intended to write")
	atRest := []byte("what the media actually kept!") // different bytes
	p := writeTempFile(t, "part", atRest)

	// Verify exactly len(written) bytes of a file holding different data.
	err := verifyWrittenData(p, int64(len(written)), sha(written))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match what was written")
	assert.Contains(t, err.Error(), p)
}

func Test_verifyWrittenData_singleBitFlip_isDetected(t *testing.T) {
	content := bytes.Repeat([]byte{0xAB}, 4096)
	p := writeTempFile(t, "part", content)
	want := sha(content)

	corrupted := append([]byte(nil), content...)
	corrupted[2048] ^= 0x01
	require.NoError(t, ioutil.WriteFile(p, corrupted, 0600))

	assert.Error(t, verifyWrittenData(p, int64(len(content)), want))
}

func Test_verifyWrittenData_onlyFirstSizeBytesAreHashed(t *testing.T) {
	// Pins current behaviour: the partition is typically LARGER than the
	// image. Only the first `size` bytes are compared; trailing bytes
	// (the rest of the partition) are ignored.
	image := []byte("0123456789")
	p := writeTempFile(t, "part", append(append([]byte(nil), image...), []byte("-trailing-old-data")...))

	assert.NoError(t, verifyWrittenData(p, int64(len(image)), sha(image)))
}

func Test_verifyWrittenData_fileShorterThanSize_failsReadBack(t *testing.T) {
	content := []byte("short")
	p := writeTempFile(t, "part", content)

	err := verifyWrittenData(p, int64(len(content))+10, sha(content))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read back")
}

func Test_verifyWrittenData_missingPath_failsReopen(t *testing.T) {
	err := verifyWrittenData(filepath.Join(t.TempDir(), "does-not-exist"), 4, sha([]byte("abcd")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to reopen")
}

func Test_verifyWrittenData_zeroSize_isVacuousPassOnlyForEmptyHash(t *testing.T) {
	// Pins the reason StoreUpdate has an explicit short-write guard:
	// verifyWrittenData(size=0) on ANY file passes iff expectedSum is the
	// SHA-256 of empty input. It cannot detect "nothing was written".
	p := writeTempFile(t, "part", []byte("old image still here"))

	assert.NoError(t, verifyWrittenData(p, 0, sha(nil)))
	assert.Error(t, verifyWrittenData(p, 0, sha([]byte("x"))))
}

func Test_verifyWrittenData_wrongLengthExpectedSum_fails(t *testing.T) {
	content := []byte("abc")
	p := writeTempFile(t, "part", content)

	assert.Error(t, verifyWrittenData(p, int64(len(content)), []byte("not-a-sha256")))
	assert.Error(t, verifyWrittenData(p, int64(len(content)), nil))
}

// ---------------------------------------------------------------- StoreUpdate short-write guard

// stubBlockDeviceSizes makes blockdevice.Open accept a regular file whose size
// is `size` (as the existing StoreUpdate test does) and restores the hooks.
func stubBlockDeviceSizes(t *testing.T, size int) {
	t.Helper()
	oldSize, oldSector := BlockDeviceGetSizeOf, BlockDeviceGetSectorSizeOf
	BlockDeviceGetSizeOf = func(*os.File) (uint64, error) { return uint64(size), nil }
	BlockDeviceGetSectorSizeOf = func(*os.File) (int, error) { return size, nil }
	t.Cleanup(func() {
		BlockDeviceGetSizeOf = oldSize
		BlockDeviceGetSectorSizeOf = oldSector
	})
}

func Test_StoreUpdate_shortWrite_isRefusedAndOldDataUntouched(t *testing.T) {
	declared := 64
	payload := []byte("only-twenty-bytes-ok") // 20 bytes < declared 64
	oldImage := bytes.Repeat([]byte{'O'}, declared)
	part := writeTempFile(t, "inactivePart", oldImage)
	stubBlockDeviceSizes(t, declared)

	dev := dualRootfsDeviceImpl{partitions: &partitions{inactive: part}}
	err := dev.StoreUpdate(bytes.NewReader(payload), &sizeOnlyFileInfo{int64(declared)})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to install a short/incomplete write")
	assert.Contains(t, err.Error(), "wrote 20 of 64")

	// The tail of the partition past the short write still holds old data:
	// exactly the half-new/half-old state the guard exists to reject.
	got, rerr := ioutil.ReadFile(part)
	require.NoError(t, rerr)
	require.Len(t, got, declared)
	assert.Equal(t, oldImage[len(payload):], got[len(payload):])
}

func Test_StoreUpdate_emptyPayloadWithNonZeroSize_isRefused(t *testing.T) {
	declared := 32
	part := writeTempFile(t, "inactivePart", bytes.Repeat([]byte{'O'}, declared))
	stubBlockDeviceSizes(t, declared)

	dev := dualRootfsDeviceImpl{partitions: &partitions{inactive: part}}
	err := dev.StoreUpdate(bytes.NewReader(nil), &sizeOnlyFileInfo{int64(declared)})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "short/incomplete write")
}

func Test_StoreUpdate_exactPayload_passesAndLeavesVerifiedBytesAtRest(t *testing.T) {
	payload := []byte("exactly-the-declared-image-bytes")
	part := writeTempFile(t, "inactivePart", make([]byte, len(payload)))
	stubBlockDeviceSizes(t, len(payload))

	dev := dualRootfsDeviceImpl{partitions: &partitions{inactive: part}}
	require.NoError(t, dev.StoreUpdate(bytes.NewReader(payload), &sizeOnlyFileInfo{int64(len(payload))}))

	got, err := ioutil.ReadFile(part)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

// ---------------------------------------------------------------- extractPartitionNumber

func Test_extractPartitionNumber(t *testing.T) {
	cases := map[string]string{
		"/dev/mmcblk0p3":               "3",
		"/dev/mmcblk1p12":              "12",
		"/dev/sda2":                    "2",
		"  /dev/nvme0n1p5\n":           "5", // whitespace trimmed
		"/dev/disk/by-partlabel/rootB": "",  // no trailing digits
		"":                             "",
		"123":                          "", // digits only: no non-digit separator found
		"p7":                           "7",
	}
	for in, want := range cases {
		assert.Equalf(t, want, extractPartitionNumber(in), "input %q", in)
	}
}

// ---------------------------------------------------------------- switchBootSlot (script absent)

// switchBootSlot hardcodes /usr/sbin/switch-boot-slot.sh, so with no
// production seam the only branch reachable without touching the host's
// /usr/sbin is "script absent". The present-script branches (slot a/b
// mapping, default-to-b, non-zero exit, env passthrough) can NOT be driven
// from a fake script dir without a production change and are reported as
// not covered (see task report).
func skipIfHostHasSwitchScript(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/usr/sbin/switch-boot-slot.sh"); err == nil {
		t.Skip("host has /usr/sbin/switch-boot-slot.sh; missing-script branch not reachable")
	}
}

func Test_switchBootSlot_scriptMissing_isToleratedAsNoOp(t *testing.T) {
	skipIfHostHasSwitchScript(t)

	dev := dualRootfsDeviceImpl{partitions: &partitions{
		rootfsPartA: "/dev/mmcblk0p2",
		rootfsPartB: "/dev/mmcblk0p3",
	}}
	// Tolerated-missing-script branch: nil for matching, non-matching and
	// empty partition numbers alike (no slot is even computed).
	for _, part := range []string{"2", "3", "99", ""} {
		assert.NoErrorf(t, dev.switchBootSlot(part), "partition %q", part)
	}
}

func Test_InstallUpdate_scriptMissing_setsBootVarsAndSucceeds(t *testing.T) {
	skipIfHostHasSwitchScript(t)

	env := &fakeBootEnv{}
	dev := dualRootfsDeviceImpl{
		BootEnvReadWriter: env,
		partitions: &partitions{
			rootfsPartA: "/dev/mmcblk0p2",
			rootfsPartB: "/dev/mmcblk0p3",
			inactive:    "/dev/mmcblk0p3",
		},
	}

	require.NoError(t, dev.InstallUpdate())
	assert.Equal(t, BootVars{
		"upgrade_available":    "1",
		"mender_boot_part":     "3",
		"mender_boot_part_hex": "3",
		"bootcount":            "0",
	}, env.writeVars)
}

func Test_InstallUpdate_hexPartitionNumber(t *testing.T) {
	skipIfHostHasSwitchScript(t)

	env := &fakeBootEnv{}
	dev := dualRootfsDeviceImpl{
		BootEnvReadWriter: env,
		partitions:        &partitions{inactive: "/dev/mmcblk0p11"},
	}

	require.NoError(t, dev.InstallUpdate())
	assert.Equal(t, "11", env.writeVars["mender_boot_part"])
	assert.Equal(t, "B", env.writeVars["mender_boot_part_hex"])
}

// bootEnvMap is a stateful BootEnvReadWriter used where ReadEnv must honour
// the requested keys (Rollback reads upgrade_available, then mender_boot_part).
type bootEnvMap struct {
	vars   BootVars
	writes []BootVars
}

func (b *bootEnvMap) ReadEnv(keys ...string) (BootVars, error) {
	out := BootVars{}
	for _, k := range keys {
		if v, ok := b.vars[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (b *bootEnvMap) WriteEnv(w BootVars) error {
	b.writes = append(b.writes, w)
	for k, v := range w {
		b.vars[k] = v
	}
	return nil
}

func Test_Rollback_scriptMissing_stillRevertsBootVars(t *testing.T) {
	skipIfHostHasSwitchScript(t)

	env := &bootEnvMap{vars: BootVars{"upgrade_available": "1", "mender_boot_part": "3"}}
	dev := dualRootfsDeviceImpl{
		BootEnvReadWriter: env,
		partitions: &partitions{
			rootfsPartA: "/dev/mmcblk0p2",
			rootfsPartB: "/dev/mmcblk0p3",
			active:      "/dev/mmcblk0p2",
			inactive:    "/dev/mmcblk0p3",
		},
	}

	// boot_part (3) != active (2): update not yet rebooted into, so rollback
	// switches back to the active partition.
	require.NoError(t, dev.Rollback())
	require.Len(t, env.writes, 1)
	assert.Equal(t, BootVars{
		"mender_boot_part":     "2",
		"mender_boot_part_hex": "2",
		"upgrade_available":    "0",
	}, env.writes[0])
}

func Test_Rollback_noUpdatePending_isNoOp(t *testing.T) {
	env := &bootEnvMap{vars: BootVars{"upgrade_available": "0", "mender_boot_part": "2"}}
	dev := dualRootfsDeviceImpl{BootEnvReadWriter: env, partitions: &partitions{}}

	assert.NoError(t, dev.Rollback())
	assert.Empty(t, env.writes)
}

func Test_Rollback_missingBootPart_isCriticalError(t *testing.T) {
	env := &bootEnvMap{vars: BootVars{"upgrade_available": "1"}}
	dev := dualRootfsDeviceImpl{BootEnvReadWriter: env, partitions: &partitions{active: "/dev/mmcblk0p2"}}

	err := dev.Rollback()
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "mender_boot_part"))
	assert.Empty(t, env.writes)
}
