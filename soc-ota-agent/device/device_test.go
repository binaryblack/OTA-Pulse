package device

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/binaryblack/OTA-Pulse/conf"
	"github.com/binaryblack/OTA-Pulse/installer"
)

// goldenRootfsArtifact is a real, unsigned artifact whose compatible device
// list is {"golden-device"} (see testdata/golden/README.md).
const goldenRootfsArtifact = "../testdata/golden/rootfs-otapulse-unsigned.otapulse"

func openGolden(t *testing.T) *os.File {
	f, err := os.Open(goldenRootfsArtifact)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	return f
}

func newTestDeviceManager(deviceTypeFile string) *DeviceManager {
	return &DeviceManager{
		Config:             conf.MenderConfig{},
		DeviceTypeFile:     deviceTypeFile,
		InstallerFactories: installer.AllModules{},
		StateScriptPath:    "",
	}
}

// GC-AG-01 / BUG-504: when the device's own device_type cannot be read, the
// daemon path must refuse the artifact (fail closed), exactly like the
// standalone path does, instead of logging "Update will continue anyway" and
// skipping the compatibility gate.
func TestReadArtifactHeadersRefusesUnreadableDeviceType(t *testing.T) {
	dir := t.TempDir()

	malformed := filepath.Join(dir, "malformed")
	require.NoError(t, os.WriteFile(malformed, []byte("this is not key=value\nnor this\n"), 0644))
	duplicated := filepath.Join(dir, "duplicated")
	require.NoError(t, os.WriteFile(duplicated,
		[]byte("device_type=a\ndevice_type=b\n"), 0644))

	cases := map[string]string{
		"missing file":     filepath.Join(dir, "does-not-exist"),
		"malformed file":   malformed,
		"multiple entries": duplicated,
	}
	for name, file := range cases {
		t.Run(name, func(t *testing.T) {
			d := newTestDeviceManager(file)
			inst, err := d.ReadArtifactHeaders(openGolden(t))
			require.Error(t, err)
			assert.Nil(t, inst)
			// Must be the device_type refusal, not some later artifact error.
			assert.Contains(t, err.Error(), "device_type")
			assert.Contains(t, err.Error(), file)
		})
	}
}

// A device whose device_type file is readable but carries no device_type key
// (GetManifestData returns "", nil) must also be refused rather than treated
// as compatible with everything.
func TestReadArtifactHeadersRefusesEmptyDeviceType(t *testing.T) {
	file := filepath.Join(t.TempDir(), "device_type")
	require.NoError(t, os.WriteFile(file, []byte("# only a comment\n"), 0644))

	d := newTestDeviceManager(file)
	_, err := d.ReadArtifactHeaders(openGolden(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "device_type")
}
