// GC-AG-12 / TASK-S108-037: additive pin-current-behaviour tests for
// SaveConfigFile. They document what the function does TODAY (including that
// the write is NOT atomic -- GC-AG-14 tracks that separately); no production
// code is changed by this file.
package conf

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_SaveConfigFile_roundTripsThroughReadConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "otapulse.conf")
	in := &MenderConfigFromFile{
		RootfsPartA:               "/dev/mmcblk0p2",
		RootfsPartB:               "/dev/mmcblk0p3",
		ServerURL:                 "https://ota.example.test",
		UpdatePollIntervalSeconds: 42,
		DeviceTypeFile:            "/var/lib/mender/device_type",
	}

	require.NoError(t, SaveConfigFile(in, path))

	var out MenderConfigFromFile
	require.NoError(t, readConfigFile(&out, path))
	assert.Equal(t, in.RootfsPartA, out.RootfsPartA)
	assert.Equal(t, in.RootfsPartB, out.RootfsPartB)
	assert.Equal(t, in.ServerURL, out.ServerURL)
	assert.Equal(t, in.UpdatePollIntervalSeconds, out.UpdatePollIntervalSeconds)
	assert.Equal(t, in.DeviceTypeFile, out.DeviceTypeFile)
}

func Test_SaveConfigFile_newFileIsMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "otapulse.conf")
	require.NoError(t, SaveConfigFile(&MenderConfigFromFile{ServerURL: "x"}, path))

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), fi.Mode().Perm()) // MEN-3762
}

func Test_SaveConfigFile_existingFileKeepsItsMode(t *testing.T) {
	// Pins current behaviour: O_CREATE's perm only applies on creation, so a
	// pre-existing file keeps whatever mode it already had (no chmod).
	path := filepath.Join(t.TempDir(), "otapulse.conf")
	require.NoError(t, ioutil.WriteFile(path, []byte("{}"), 0644))
	require.NoError(t, os.Chmod(path, 0644)) // defeat umask

	require.NoError(t, SaveConfigFile(&MenderConfigFromFile{ServerURL: "x"}, path))

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), fi.Mode().Perm())
}

func Test_SaveConfigFile_truncatesLongerExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "otapulse.conf")
	long := []byte(`{"Junk":"` + strings.Repeat("a", 512) + `"}`)
	require.NoError(t, ioutil.WriteFile(path, long, 0600))

	require.NoError(t, SaveConfigFile(&MenderConfigFromFile{ServerURL: "y"}, path))

	var out MenderConfigFromFile
	require.NoError(t, readConfigFile(&out, path)) // no trailing garbage => still valid JSON
	assert.Equal(t, "y", out.ServerURL)
}

func Test_SaveConfigFile_isIndentedJSONWithoutTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "otapulse.conf")
	require.NoError(t, SaveConfigFile(&MenderConfigFromFile{ServerURL: "z"}, path))

	b, err := ioutil.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(b), "\n    \"ServerURL\": \"z\"")
	assert.NotEqual(t, byte('\n'), b[len(b)-1])
}

func Test_SaveConfigFile_missingParentDir_returnsOpenError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "otapulse.conf")

	err := SaveConfigFile(&MenderConfigFromFile{}, path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Error opening configuration file")
}

func Test_SaveConfigFile_readOnlyExistingFile_returnsOpenError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	path := filepath.Join(t.TempDir(), "otapulse.conf")
	require.NoError(t, ioutil.WriteFile(path, []byte("{}"), 0400))

	err := SaveConfigFile(&MenderConfigFromFile{ServerURL: "x"}, path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Error opening configuration file")

	b, rerr := ioutil.ReadFile(path)
	require.NoError(t, rerr)
	assert.Equal(t, "{}", string(b)) // untouched
}
