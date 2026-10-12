package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/binaryblack/OTA-Pulse/conf"
)

// TASK-S109-006: BootEnvMode "bootloader" must reach the installer, and file
// mode must not set it.
type bootloaderModer interface{ IsBootloaderMode() bool }

func TestInitBootloaderDualRootfsDevice_SetsBootloaderMode(t *testing.T) {
	cmds := &fakeBootCommands{tools: map[string]fakeTool{
		"fw_printenv": {0, "mender_boot_part=2"},
	}}
	dev, err := initBootloaderDualRootfsDevice(bootloaderConfig(), cmds,
		writeFwEnvConfig(t, activeFwEnvConfig), lookPathFrom("fw_printenv"))
	require.NoError(t, err)
	bm, ok := dev.(bootloaderModer)
	require.True(t, ok)
	assert.True(t, bm.IsBootloaderMode())
}

func TestInitDualRootfsDeviceChecked_FileMode_IsNotBootloaderMode(t *testing.T) {
	for _, mode := range []string{"", conf.BootEnvModeFile} {
		cfg := conf.NewMenderConfig()
		cfg.BootEnvMode = mode
		cfg.UseFileBasedBootEnv = true
		cfg.RootfsPartA = "/dev/mmcblk0p2"
		cfg.RootfsPartB = "/dev/mmcblk0p3"
		dev, err := initDualRootfsDeviceChecked(cfg)
		require.NoError(t, err, mode)
		bm, ok := dev.(bootloaderModer)
		require.True(t, ok)
		assert.False(t, bm.IsBootloaderMode(), mode)
	}
}
