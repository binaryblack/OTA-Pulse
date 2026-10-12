// Copyright 2026 OTA-Pulse
//
//	Licensed under the Apache License, Version 2.0 (the "License");
//	you may not use this file except in compliance with the License.
//	You may obtain a copy of the License at
//
//	    http://www.apache.org/licenses/LICENSE-2.0
//
//	Unless required by applicable law or agreed to in writing, software
//	distributed under the License is distributed on an "AS IS" BASIS,
//	WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//	See the License for the specific language governing permissions and
//	limitations under the License.

// Tests for BootEnvMode (file | bootloader) boot environment selection.
//
// None of these tests runs the host's real fw_printenv / grub tools: the
// command runner and the tool lookup are injected.
package cli

import (
	"flag"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"github.com/binaryblack/OTA-Pulse/conf"
	"github.com/binaryblack/OTA-Pulse/system"
)

// fakeTool describes how a named command behaves when UBootEnv runs it.
type fakeTool struct {
	exit   int
	output string
}

// fakeBootCommands is a system.StatCommander that records every command name
// it is asked to run. A name not in tools behaves like a missing executable.
type fakeBootCommands struct {
	mu    sync.Mutex
	tools map[string]fakeTool
	calls []string
}

func (f *fakeBootCommands) Command(name string, arg ...string) *system.Cmd {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
	tool, ok := f.tools[name]
	if !ok {
		// A bare name that does not exist: Start() fails with exec.ErrNotFound,
		// exactly like a missing fw_printenv.
		return system.Command("otapulse-test-missing-tool-" + name)
	}
	return system.Command("sh", "-c", `printf '%s\n' "$1"; exit "$2"`, "sh",
		tool.output, strconv.Itoa(tool.exit))
}

func (f *fakeBootCommands) Stat(name string) (os.FileInfo, error) {
	return os.Stat(name)
}

func (f *fakeBootCommands) called(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == name {
			return true
		}
	}
	return false
}

// lookPathFrom returns a lookPath that finds only the named tools.
func lookPathFrom(present ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, p := range present {
			if p == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
}

func writeFwEnvConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fw_env.config")
	require.NoError(t, ioutil.WriteFile(p, []byte(content), 0600))
	return p
}

const activeFwEnvConfig = "# comment\n/dev/mmcblk0 0x400000 0x4000\n"

func bootloaderConfig() *conf.MenderConfig {
	c := conf.NewMenderConfig()
	c.BootEnvMode = conf.BootEnvModeBootloader
	c.RootfsPartA = "/dev/mmcblk0p2"
	c.RootfsPartB = "/dev/mmcblk0p3"
	return c
}

// captureLogs collects log entries for the duration of the test.
type logCapture struct {
	mu      sync.Mutex
	entries []string
}

func (l *logCapture) Levels() []log.Level { return log.AllLevels }
func (l *logCapture) Fire(e *log.Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e.Message)
	return nil
}
func (l *logCapture) contains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.entries {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	lc := &logCapture{}
	old := log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	log.AddHook(lc)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(old) })
	return lc
}

// --- bootloader mode: selectBootloaderEnv ---------------------------------

func TestSelectBootloaderEnv_UBootWorks(t *testing.T) {
	cmds := &fakeBootCommands{tools: map[string]fakeTool{
		"fw_printenv": {0, "mender_boot_part=2"},
	}}
	fwEnv := writeFwEnvConfig(t, activeFwEnvConfig)

	env, err := selectBootloaderEnv(bootloaderConfig(), cmds, fwEnv,
		lookPathFrom("fw_printenv"))
	require.NoError(t, err)
	require.NotNil(t, env)
	assert.True(t, cmds.called("fw_printenv"))
}

// (a) the read tool exits non-zero: hard error, not a file-mode fallback.
func TestSelectBootloaderEnv_ReadFails_IsError(t *testing.T) {
	cmds := &fakeBootCommands{tools: map[string]fakeTool{
		"fw_printenv": {243, ""},
	}}
	fwEnv := writeFwEnvConfig(t, activeFwEnvConfig)

	env, err := selectBootloaderEnv(bootloaderConfig(), cmds, fwEnv,
		lookPathFrom("fw_printenv"))
	require.Error(t, err)
	assert.Nil(t, env)
	assert.Contains(t, err.Error(), "cannot read mender_boot_part")
	assert.Contains(t, err.Error(), "not falling back")
}

// (b) fw_env.config missing, no GRUB tool: error before fw_printenv is run
// (it would crash on an empty config, BUG-178).
func TestSelectBootloaderEnv_NoFwEnvConfigNoGrub_IsError(t *testing.T) {
	for name, fwEnv := range map[string]string{
		"missing":      filepath.Join(t.TempDir(), "does-not-exist"),
		"empty":        writeFwEnvConfig(t, ""),
		"only comment": writeFwEnvConfig(t, "# nothing\n\n   \n"),
	} {
		t.Run(name, func(t *testing.T) {
			cmds := &fakeBootCommands{tools: map[string]fakeTool{
				"fw_printenv": {0, "mender_boot_part=2"},
			}}
			env, err := selectBootloaderEnv(bootloaderConfig(), cmds, fwEnv,
				lookPathFrom("fw_printenv"))
			require.Error(t, err)
			assert.Nil(t, env)
			assert.Contains(t, err.Error(), "no active entries")
			assert.False(t, cmds.called("fw_printenv"),
				"fw_printenv must not be executed with an empty fw_env.config")
		})
	}
}

func TestSelectBootloaderEnv_NoToolAtAll_IsError(t *testing.T) {
	cmds := &fakeBootCommands{}
	env, err := selectBootloaderEnv(bootloaderConfig(), cmds,
		writeFwEnvConfig(t, activeFwEnvConfig), lookPathFrom())
	require.Error(t, err)
	assert.Nil(t, env)
	assert.Contains(t, err.Error(), "no bootloader environment read tool found")
	assert.Empty(t, cmds.calls)
}

// GRUB board: grub-mender-grubenv-print present, no /etc/fw_env.config at all.
func TestSelectBootloaderEnv_GrubToolNoFwEnvConfig_Works(t *testing.T) {
	cmds := &fakeBootCommands{tools: map[string]fakeTool{
		"grub-mender-grubenv-print": {0, "mender_boot_part=2"},
		// If the chain ever reached fw_printenv it would "work" too; the
		// assertion below proves it was never used.
		"fw_printenv": {0, "mender_boot_part=2"},
	}}
	fwEnv := filepath.Join(t.TempDir(), "does-not-exist")

	env, err := selectBootloaderEnv(bootloaderConfig(), cmds, fwEnv,
		lookPathFrom("grub-mender-grubenv-print"))
	require.NoError(t, err)
	require.NotNil(t, env)
	assert.True(t, cmds.called("grub-mender-grubenv-print"))
	assert.False(t, cmds.called("fw_printenv"))
}

// GRUB tool present but unreadable: still a hard error (fail closed for GRUB).
func TestSelectBootloaderEnv_GrubToolReadFails_IsError(t *testing.T) {
	cmds := &fakeBootCommands{tools: map[string]fakeTool{
		"grub-mender-grubenv-print": {1, ""},
	}}
	env, err := selectBootloaderEnv(bootloaderConfig(), cmds,
		filepath.Join(t.TempDir(), "does-not-exist"),
		lookPathFrom("grub-mender-grubenv-print"))
	require.Error(t, err)
	assert.Nil(t, env)
}

// An unset mender_boot_part is not a tool error (grub-mender-grubenv-print
// prints "mender_boot_part=" and exits 0) but must still be a hard error:
// an empty value matches every partition suffix.
func TestSelectBootloaderEnv_EmptyBootPart_IsError(t *testing.T) {
	cases := map[string]struct {
		tool, output string
		present      []string
		fwEnv        string
	}{
		"grub empty value":      {"grub-mender-grubenv-print", "mender_boot_part=", []string{"grub-mender-grubenv-print"}, ""},
		"grub whitespace value": {"grub-mender-grubenv-print", "mender_boot_part=  ", []string{"grub-mender-grubenv-print"}, ""},
		"grub variable absent":  {"grub-mender-grubenv-print", "", []string{"grub-mender-grubenv-print"}, ""},
		"fw_printenv empty":     {"fw_printenv", "mender_boot_part=", []string{"fw_printenv"}, activeFwEnvConfig},
		"fw_printenv blank":     {"fw_printenv", "mender_boot_part= ", []string{"fw_printenv"}, activeFwEnvConfig},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cmds := &fakeBootCommands{tools: map[string]fakeTool{c.tool: {0, c.output}}}
			fwEnv := filepath.Join(t.TempDir(), "does-not-exist")
			if c.fwEnv != "" {
				fwEnv = writeFwEnvConfig(t, c.fwEnv)
			}
			env, err := selectBootloaderEnv(bootloaderConfig(), cmds, fwEnv,
				lookPathFrom(c.present...))
			require.Error(t, err)
			assert.Nil(t, env)
			assert.Contains(t, err.Error(), "mender_boot_part is empty or not set")
			assert.Contains(t, err.Error(), "not falling back")
		})
	}
}

// Both fw_printenv and the GRUB tool are installed and there is no
// fw_env.config: UBootEnv's runtime chain runs the GRUB tool first, so that
// is the tool the guard must reason about (no fw_env.config needed), and
// fw_printenv must never run.
func TestSelectBootloaderEnv_BothToolsPresent_GrubWinsAndFwEnvNotRequired(t *testing.T) {
	cmds := &fakeBootCommands{tools: map[string]fakeTool{
		"grub-mender-grubenv-print": {0, "mender_boot_part=2"},
		"fw_printenv":               {0, "mender_boot_part=2"},
	}}
	env, err := selectBootloaderEnv(bootloaderConfig(), cmds,
		filepath.Join(t.TempDir(), "does-not-exist"),
		lookPathFrom("fw_printenv", "grub-mender-grubenv-print"))
	require.NoError(t, err)
	require.NotNil(t, env)
	assert.True(t, cmds.called("grub-mender-grubenv-print"))
	assert.False(t, cmds.called("fw_printenv"))
}

// The runtime chain never falls through to fw_printenv once the GRUB tool
// starts, even if that tool then fails; neither may the mode selection.
func TestSelectBootloaderEnv_BothToolsPresent_GrubFails_NoFallThrough(t *testing.T) {
	cmds := &fakeBootCommands{tools: map[string]fakeTool{
		"grub-mender-grubenv-print": {1, ""},
		"fw_printenv":               {0, "mender_boot_part=2"},
	}}
	env, err := selectBootloaderEnv(bootloaderConfig(), cmds,
		writeFwEnvConfig(t, activeFwEnvConfig),
		lookPathFrom("fw_printenv", "grub-mender-grubenv-print"))
	require.Error(t, err)
	assert.Nil(t, env)
	assert.False(t, cmds.called("fw_printenv"))
}

// systemd-boot-printenv sits between GRUB and fw_printenv in the chain, so it
// also makes fw_env.config unnecessary.
func TestSelectBootloaderEnv_SystemdBootBeforeFwPrintenv(t *testing.T) {
	cmds := &fakeBootCommands{tools: map[string]fakeTool{
		"systemd-boot-printenv": {0, "mender_boot_part=2"},
		"fw_printenv":           {0, "mender_boot_part=2"},
	}}
	env, err := selectBootloaderEnv(bootloaderConfig(), cmds,
		filepath.Join(t.TempDir(), "does-not-exist"),
		lookPathFrom("fw_printenv", "systemd-boot-printenv"))
	require.NoError(t, err)
	require.NotNil(t, env)
	assert.False(t, cmds.called("fw_printenv"))
}

// A custom BootUtilitiesGetNextActivePart replaces the whole chain, and its
// own tool decides whether fw_env.config matters.
func TestSelectBootloaderEnv_CustomGetCommand(t *testing.T) {
	cfg := bootloaderConfig()
	cfg.BootUtilitiesGetNextActivePart = "my-env-reader"
	cmds := &fakeBootCommands{tools: map[string]fakeTool{
		"my-env-reader": {0, "mender_boot_part=3"},
	}}
	env, err := selectBootloaderEnv(cfg, cmds,
		filepath.Join(t.TempDir(), "does-not-exist"), lookPathFrom("my-env-reader"))
	require.NoError(t, err)
	require.NotNil(t, env)

	// Not installed: error.
	_, err = selectBootloaderEnv(cfg, &fakeBootCommands{},
		filepath.Join(t.TempDir(), "does-not-exist"), lookPathFrom("fw_printenv"))
	require.Error(t, err)
}

// --- bootloader mode: whole device construction ---------------------------

func TestInitBootloaderDualRootfsDevice_Works(t *testing.T) {
	cmds := &fakeBootCommands{tools: map[string]fakeTool{
		"fw_printenv": {0, "mender_boot_part=2"},
	}}
	dev, err := initBootloaderDualRootfsDevice(bootloaderConfig(), cmds,
		writeFwEnvConfig(t, activeFwEnvConfig), lookPathFrom("fw_printenv"))
	require.NoError(t, err)
	assert.NotNil(t, dev)
}

func TestInitBootloaderDualRootfsDevice_NoRootfsParts_IsError(t *testing.T) {
	cfg := bootloaderConfig()
	cfg.RootfsPartB = ""
	cmds := &fakeBootCommands{}
	dev, err := initBootloaderDualRootfsDevice(cfg, cmds,
		writeFwEnvConfig(t, activeFwEnvConfig), lookPathFrom("fw_printenv"))
	require.Error(t, err)
	assert.Nil(t, dev)
	assert.Empty(t, cmds.calls)
}

func TestInitBootloaderDualRootfsDevice_UnreadableEnv_IsErrorAndNeverFileMode(t *testing.T) {
	lc := captureLogs(t)
	cmds := &fakeBootCommands{tools: map[string]fakeTool{
		"fw_printenv": {243, ""},
	}}
	dev, err := initBootloaderDualRootfsDevice(bootloaderConfig(), cmds,
		writeFwEnvConfig(t, activeFwEnvConfig), lookPathFrom("fw_printenv"))
	require.Error(t, err)
	assert.Nil(t, dev)
	// Bootloader mode must never log (or take) the file-based fallback.
	assert.False(t, lc.contains("falling back to file-based"))
	assert.False(t, lc.contains("Using file-based boot environment"))
	assert.False(t, lc.contains("and using file-based boot environment"))
}

// --- initDualRootfsDeviceChecked: mode dispatch and config errors ----------

func TestInitDualRootfsDeviceChecked_InvalidMode_IsError(t *testing.T) {
	for _, mode := range []string{"Bootloader", "grub", "BOOTLOADER", "uboot", " file"} {
		cfg := conf.NewMenderConfig()
		cfg.BootEnvMode = mode
		cfg.RootfsPartA = "/dev/mmcblk0p2"
		cfg.RootfsPartB = "/dev/mmcblk0p3"
		dev, err := initDualRootfsDeviceChecked(cfg)
		require.Error(t, err, mode)
		assert.Nil(t, dev, mode)
		assert.Contains(t, err.Error(), "invalid BootEnvMode", mode)
	}
}

func TestInitDualRootfsDeviceChecked_BootloaderWithUseFileBased_IsError(t *testing.T) {
	cfg := bootloaderConfig()
	cfg.UseFileBasedBootEnv = true
	dev, err := initDualRootfsDeviceChecked(cfg)
	require.Error(t, err)
	assert.Nil(t, dev)
	assert.Contains(t, err.Error(), "contradicts UseFileBasedBootEnv")
}

func TestInitDualRootfsDeviceChecked_BootloaderNoRootfsParts_IsError(t *testing.T) {
	cfg := conf.NewMenderConfig()
	cfg.BootEnvMode = conf.BootEnvModeBootloader
	dev, err := initDualRootfsDeviceChecked(cfg)
	require.Error(t, err)
	assert.Nil(t, dev)
}

// --- file mode: behaviour unchanged ----------------------------------------

// UseFileBasedBootEnv=true selects the file-based environment, for an absent
// BootEnvMode and for an explicit "file".
func TestInitDualRootfsDeviceChecked_FileMode_UseFileBasedBootEnv(t *testing.T) {
	for _, mode := range []string{"", conf.BootEnvModeFile} {
		lc := captureLogs(t)
		cfg := conf.NewMenderConfig()
		cfg.BootEnvMode = mode
		cfg.UseFileBasedBootEnv = true
		cfg.RootfsPartA = "/dev/mmcblk0p2"
		cfg.RootfsPartB = "/dev/mmcblk0p3"

		dev, err := initDualRootfsDeviceChecked(cfg)
		require.NoError(t, err, mode)
		assert.NotNil(t, dev, mode)
		assert.True(t, lc.contains(
			"Using file-based boot environment (configured via UseFileBasedBootEnv)"), mode)
	}
}

// No rootfs partitions configured: nil device and NO error in file mode, as
// before (this is the "No dual rootfs configuration present" case).
func TestInitDualRootfsDeviceChecked_FileMode_NoRootfsParts_NilNoError(t *testing.T) {
	cfg := conf.NewMenderConfig()
	cfg.UseFileBasedBootEnv = true
	dev, err := initDualRootfsDeviceChecked(cfg)
	require.NoError(t, err)
	assert.Nil(t, dev)
}

// UseFileBasedBootEnv=false and no active fw_env.config entries: file-based
// environment plus the BUG-178 warning. Only meaningful on a host without a
// real /etc/fw_env.config (it would otherwise run the real fw_printenv).
func TestInitDualRootfsDeviceChecked_FileMode_NoFwEnvEntries_FallsBackToFile(t *testing.T) {
	if fwEnvConfigHasActiveEntries(fwEnvConfigPath) {
		t.Skipf("%s has active entries on this host; would run the real fw_printenv",
			fwEnvConfigPath)
	}
	lc := captureLogs(t)
	cfg := conf.NewMenderConfig()
	cfg.RootfsPartA = "/dev/mmcblk0p2"
	cfg.RootfsPartB = "/dev/mmcblk0p3"

	dev, err := initDualRootfsDeviceChecked(cfg)
	require.NoError(t, err)
	assert.NotNil(t, dev)
	assert.True(t, lc.contains("No active entries in /etc/fw_env.config, "+
		"skipping U-Boot environment probe and using file-based boot environment"))
}

// UseFileBasedBootEnv=false, fw_env.config has entries, U-Boot unreadable:
// the legacy silent fallback to the file-based environment. This needs a real
// /etc/fw_env.config and a fake fw_printenv first in PATH, so it only runs
// where explicitly allowed (the documented docker run sets
// OTAPULSE_TEST_ALLOW_ETC_WRITE=1) and /etc/fw_env.config does not exist.
func TestInitDualRootfsDeviceChecked_FileMode_UBootUnreadable_FallsBackToFile(t *testing.T) {
	if os.Getenv("OTAPULSE_TEST_ALLOW_ETC_WRITE") != "1" {
		t.Skip("set OTAPULSE_TEST_ALLOW_ETC_WRITE=1 (disposable container only) to run")
	}
	if _, err := os.Stat(fwEnvConfigPath); err == nil {
		t.Skipf("%s already exists", fwEnvConfigPath)
	}
	require.NoError(t, ioutil.WriteFile(fwEnvConfigPath, []byte(activeFwEnvConfig), 0600))
	t.Cleanup(func() { os.Remove(fwEnvConfigPath) })

	binDir := t.TempDir()
	require.NoError(t, ioutil.WriteFile(filepath.Join(binDir, "fw_printenv"),
		[]byte("#!/bin/sh\nexit 243\n"), 0755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	lc := captureLogs(t)
	cfg := conf.NewMenderConfig()
	cfg.RootfsPartA = "/dev/mmcblk0p2"
	cfg.RootfsPartB = "/dev/mmcblk0p3"

	dev, err := initDualRootfsDeviceChecked(cfg)
	require.NoError(t, err)
	assert.NotNil(t, dev)
	assert.True(t, lc.contains("falling back to file-based boot environment"))

	// The same unreadable environment in bootloader mode is a hard error.
	cfg.BootEnvMode = conf.BootEnvModeBootloader
	dev, err = initDualRootfsDeviceChecked(cfg)
	require.Error(t, err)
	assert.Nil(t, dev)
}

// --- callers ---------------------------------------------------------------

func artifactOpsFixture(t *testing.T, cmdName string, cfg *conf.MenderConfig) error {
	t.Helper()
	runOptions := runOptionsType{dataStore: t.TempDir()}
	ctx := cli.NewContext(&cli.App{}, flag.NewFlagSet("Flags", flag.ContinueOnError), nil)
	ctx.Command = &cli.Command{Name: cmdName}
	return handleArtifactOperations(ctx, runOptions, cfg)
}

// install / commit / rollback refuse to run when the bootloader-mode device
// cannot be created (here: invalid mode; no process is started).
func TestHandleArtifactOperations_BootloaderModeFailure_RefusesMutatingCommands(t *testing.T) {
	cfg := conf.NewMenderConfig()
	cfg.BootEnvMode = "Bootloader" // invalid
	cfg.RootfsPartA = "/dev/mmcblk0p2"
	cfg.RootfsPartB = "/dev/mmcblk0p3"
	for _, name := range []string{"install", "commit", "rollback"} {
		err := artifactOpsFixture(t, name, cfg)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "invalid BootEnvMode", name)
	}
}

// show-artifact / show-provides only read the datastore, so they must not be
// blocked by the boot environment failure.
func TestHandleArtifactOperations_BootloaderModeFailure_ReadOnlyCommandsContinue(t *testing.T) {
	cfg := conf.NewMenderConfig()
	cfg.BootEnvMode = conf.BootEnvModeBootloader
	cfg.UseFileBasedBootEnv = true // contradiction: device init fails
	for _, name := range []string{"show-artifact", "show-provides"} {
		lc := captureLogs(t)
		err := artifactOpsFixture(t, name, cfg)
		// It got past the device init failure (whatever the datastore says
		// about the artifact name afterwards is not this test's business).
		assert.True(t, lc.contains("Continuing without dual rootfs device"), name)
		if err != nil {
			assert.NotContains(t, err.Error(), "contradicts", name)
		}
	}
}

// commonInit (used by the daemon and the bootstrap commands) must fail, not
// continue with a nil dual rootfs device.
func TestCommonInit_BootloaderModeFailure_ReturnsError(t *testing.T) {
	cfg := conf.NewMenderConfig()
	cfg.BootEnvMode = conf.BootEnvModeBootloader // no RootfsPartA/B
	cfg.ServerURL = "http://localhost"
	opts := &runOptionsType{dataStore: t.TempDir()}

	m, mp, err := commonInit(cfg, opts, true)
	require.Error(t, err)
	assert.Nil(t, m)
	assert.Nil(t, mp)
	assert.Contains(t, err.Error(), "dual rootfs device")
}
