// Copyright 2023 Northern.tech AS
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
package cli

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/signal"
	"path"
	"sort"
	"strings"
	"syscall"

	log "github.com/sirupsen/logrus"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"

	"github.com/binaryblack/OTA-Pulse/app"
	"github.com/binaryblack/OTA-Pulse/client"
	"github.com/binaryblack/OTA-Pulse/conf"
	"github.com/binaryblack/OTA-Pulse/dbus"
	dev "github.com/binaryblack/OTA-Pulse/device"
	"github.com/binaryblack/OTA-Pulse/installer"
	"github.com/binaryblack/OTA-Pulse/store"
	"github.com/binaryblack/OTA-Pulse/system"
)

type logOptionsType struct {
	logLevel string
	logFile  string
	noSyslog bool
}

type runOptionsType struct {
	config         string
	fallbackConfig string
	dataStore      string
	imageFile      string
	keyPassphrase  string
	bootstrapForce bool
	client.Config
	logOptions     logOptionsType
	setupOptions   setupOptionsType // Options for setup subcommand
	rebootExitCode bool
}

var out io.Writer = os.Stdout

var (
	errArtifactNameEmpty = errors.New(
		"The Artifact name is empty. Please set a valid name for the Artifact!",
	)
)

func initDualRootfsDevice(config *conf.MenderConfig) installer.DualRootfsDevice {
	deviceConfig := config.GetDeviceConfig()
	var env installer.BootEnvReadWriter

	// Decide which boot environment to use
	if config.UseFileBasedBootEnv {
		// Explicitly configured to use file-based boot environment
		log.Info("Using file-based boot environment (configured via UseFileBasedBootEnv)")
		env = installer.NewFileBasedBootEnv(
			new(system.OsCalls),
			deviceConfig.RootfsPartA,
			deviceConfig.RootfsPartB,
		)
	} else if !fwEnvConfigHasActiveEntries(fwEnvConfigPath) {
		// BUG-178 defense-in-depth: on a board where /etc/fw_env.config has
		// no active (non-comment, non-blank) entries, fw_printenv itself
		// SIGSEGVs under libubootenv, and ReadEnv below falls through its
		// getCommand chain to fw_printenv (see installer.UBootEnv.getCommand
		// in bootenv.go) — so merely PROBING the U-Boot environment produces
		// a crash-loop of junk coredumps on every agent start. This mirrors
		// the intent of installer.uBootEnvWorks (file_bootenv.go), which
		// probes by actually running fw_printenv; that helper is unexported
		// and, more importantly, running the binary at all is the crash we
		// are trying to avoid here, so this checks the static config instead
		// of executing it. Skip the U-Boot probe entirely and go straight to
		// file-based boot env. Note this also bypasses the grub-mender-
		// grubenv-print / systemd-boot-printenv probes in the getCommand
		// chain (bootenv.go) — acceptable because this branch is unreachable
		// when otapulse.conf carries the baked-in UseFileBasedBootEnv=true
		// (enforced by validate-image.sh CFG-005), and in the corrupted-conf
		// state it guards against (BUG-178), file-based IS the designed path.
		log.Warnf("No active entries in %s, skipping U-Boot environment probe and using file-based boot environment", fwEnvConfigPath)
		env = installer.NewFileBasedBootEnv(
			new(system.OsCalls),
			deviceConfig.RootfsPartA,
			deviceConfig.RootfsPartB,
		)
	} else {
		// Try U-Boot environment first
		ubootEnv := installer.NewEnvironment(new(system.OsCalls), config.BootUtilitiesSetActivePart,
			config.BootUtilitiesGetNextActivePart)

		// Test if U-Boot environment works by trying to read mender_boot_part
		_, err := ubootEnv.ReadEnv("mender_boot_part")
		if err != nil {
			// U-Boot environment failed, fall back to file-based
			log.Warnf("U-Boot environment not available (%s), falling back to file-based boot environment", err.Error())
			env = installer.NewFileBasedBootEnv(
				new(system.OsCalls),
				deviceConfig.RootfsPartA,
				deviceConfig.RootfsPartB,
			)
		} else {
			// U-Boot environment works
			log.Info("Using U-Boot environment for boot state")
			env = ubootEnv
		}
	}

	dualRootfsDevice := installer.NewDualRootfsDevice(
		env, new(system.OsCalls), deviceConfig)
	if dualRootfsDevice == nil {
		log.Info("No dual rootfs configuration present")
	} else {
		ap, err := dualRootfsDevice.GetActive()
		if err != nil {
			log.Errorf("Failed to read the current active partition: %s", err.Error())
		} else {
			log.Infof("OTAPulse running on partition: %s", ap)
		}
	}

	return dualRootfsDevice
}

// fwEnvConfigPath is the standard libubootenv/fw_printenv config location
// (see the mender_saveenv_canary error message in installer/bootenv.go for
// the same reference path).
const fwEnvConfigPath = "/etc/fw_env.config"

// fwEnvConfigHasActiveEntries reports whether filePath has at least one
// non-comment, non-blank line. An empty or all-commented fw_env.config means
// libubootenv has no environment location configured, which is exactly the
// condition under which fw_printenv SIGSEGVs instead of exiting cleanly
// (BUG-178). A missing file is treated the same as no active entries.
func fwEnvConfigHasActiveEntries(filePath string) bool {
	data, err := ioutil.ReadFile(filePath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return true
	}
	return false
}

var SignalHandlerChan = make(chan os.Signal, 2)

func commonInit(
	config *conf.MenderConfig,
	opts *runOptionsType,
	initDbOnly bool,
) (*app.Mender, *app.MenderPieces, error) {

	tentok := config.GetTenantToken()

	stat, err := os.Stat(opts.dataStore)
	if os.IsNotExist(err) {
		// Create data directory if it does not exist.
		err = os.MkdirAll(opts.dataStore, 0700)
		if err != nil {
			return nil, nil, err
		}
	} else if err != nil {
		return nil, nil, errors.Wrapf(err, "Could not stat data directory: %s", opts.dataStore)
	} else if !stat.IsDir() {
		return nil, nil, errors.Errorf("%s is not a directory", opts.dataStore)
	}

	var (
		ks       *store.Keystore
		dirstore *store.DirStore
		authmgr  *app.MenderAuthManager
	)
	dirstore = store.NewDirStore(opts.dataStore)
	dbstore := store.NewDBStore(opts.dataStore)
	if dbstore == nil {
		return nil, nil, errors.New("failed to initialize DB store")
	}

	var privateKey string
	var sslEngine string
	var static bool

	if !initDbOnly {
		if config.HttpsClient.Key != "" {
			privateKey = config.HttpsClient.Key
			sslEngine = config.HttpsClient.SSLEngine
			static = true
		}
		if config.Security.AuthPrivateKey != "" {
			privateKey = config.Security.AuthPrivateKey
			sslEngine = config.Security.SSLEngine
			static = true
		}
		if config.HttpsClient.Key == "" && config.Security.AuthPrivateKey == "" {
			privateKey = conf.DefaultKeyFile
			sslEngine = config.HttpsClient.SSLEngine
			static = false
		}

		ks = store.NewKeystore(dirstore, privateKey, sslEngine, static, opts.keyPassphrase)
		if ks == nil {
			return nil, nil, errors.New("failed to setup key storage")
		}

		authmgr = app.NewAuthManager(app.AuthManagerConfig{
			AuthDataStore:  dbstore,
			KeyStore:       ks,
			IdentitySource: dev.NewIdentityDataGetter(),
			TenantToken:    tentok,
			Config:         config,
		})
		if authmgr == nil {
			// close DB store explicitly
			dbstore.Close()
			return nil, nil, errors.New("error initializing authentication manager")
		}

		if config.DBus.Enabled {
			api, err := dbus.GetDBusAPI()
			if err != nil {
				// close DB store explicitly
				dbstore.Close()
				return nil, nil, errors.Wrap(
					err,
					"DBus API support not available, but DBus is enabled",
				)
			}
			authmgr.EnableDBus(api)
		}
	}

	mp := app.MenderPieces{
		Store:       dbstore,
		AuthManager: authmgr,
	}

	mp.DualRootfsDevice = initDualRootfsDevice(config)

	m, err := app.NewMender(config, mp)
	if err != nil {
		// close DB store explicitly
		dbstore.Close()
		return nil, nil, errors.Wrap(err, "error initializing mender controller")
	}

	return m, &mp, nil
}

func doHandleBootstrapArtifact(config *conf.MenderConfig, opts *runOptionsType) error {
	controller, mp, err := commonInit(config, opts, true)
	if err != nil {
		return err
	}

	// need to close DB store manually, since we're not running under a
	// daemonized version
	defer mp.Store.Close()

	return controller.HandleBootstrapArtifact(mp.Store)
}

func doBootstrapAuthorize(config *conf.MenderConfig, opts *runOptionsType) error {
	controller, mp, err := commonInit(config, opts, false)
	if err != nil {
		return err
	}

	// need to close DB store manually, since we're not running under a
	// daemonized version
	defer mp.Store.Close()

	authManager := mp.AuthManager
	if opts.bootstrapForce {
		authManager.ForceBootstrap()
	}

	if merr := authManager.Bootstrap(); merr != nil {
		return merr.Cause()
	}

	authManager.Start()
	defer authManager.Stop()

	_, _, err = controller.Authorize()

	return err
}

func getMenderDaemonPID(cmd *system.Cmd) (string, error) {
	buf := bytes.NewBuffer(nil)
	cmd.Stdout = buf
	err := cmd.Run()
	if err != nil {
		return "", errors.New("getMenderDaemonPID: Failed to run systemctl")
	}
	pid := strings.Trim(buf.String(), "MainPID=\n")
	if pid == "" || pid == "0" {
		return "", errors.New("could not find the PID of the mender daemon")
	}
	return pid, nil
}

func handleArtifactOperations(ctx *cli.Context, runOptions runOptionsType,
	config *conf.MenderConfig) error {

	dbstore := store.NewDBStore(runOptions.dataStore)
	if dbstore == nil {
		return errors.New("failed to initialize DB store")
	}

	dualRootfsDevice := initDualRootfsDevice(config)

	stateExec := dev.NewStateScriptExecutor(config)
	deviceManager := dev.NewDeviceManager(dualRootfsDevice, config, dbstore)

	switch ctx.Command.Name {
	case "show-artifact":
		return PrintArtifactName(deviceManager)

	case "show-provides":
		return PrintProvides(deviceManager)

	case "install":
		return app.DoStandaloneInstall(deviceManager, runOptions.imageFile,
			runOptions.Config, stateExec, runOptions.rebootExitCode)

	case "commit":
		return app.DoStandaloneCommit(deviceManager, stateExec)

	case "rollback":
		return app.DoStandaloneRollback(deviceManager, stateExec)

	default:
		return errors.New("handleArtifactOperations: Should never get here")
	}
}

func initDaemon(config *conf.MenderConfig,
	opts *runOptionsType) (*app.MenderDaemon, error) {

	controller, mp, err := commonInit(config, opts, false)
	if err != nil {
		return nil, err
	}

	checkDemoCert()

	if opts.bootstrapForce {
		authManager := mp.AuthManager
		authManager.ForceBootstrap()
	}

	daemon, err := app.NewDaemon(config, controller, mp.Store, mp.AuthManager)
	if err != nil {
		return nil, err
	}

	// add logging hook; only daemon needs this
	log.AddHook(app.NewDeploymentLogHook(app.DeploymentLogger))

	// At the moment we don't do anything with this, just force linking to it.
	_, _ = dbus.GetDBusAPI()

	return daemon, nil
}

func checkDemoCert() {
	entries, err := ioutil.ReadDir(DefaultLocalTrustMenderDir)
	if err != nil {
		log.Debugf("Could not open local OTAPulse trust store directory: %s", err.Error())
		return
	}

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), DefaultLocalTrustMenderPrefix) {
			log.Warnf("Running with demo certificate installed in trust store. This is INSECURE! "+
				"Please remove %s if you plan to use this device in production.",
				path.Join(DefaultLocalTrustMenderDir, entry.Name()))
		}
	}
}

func PrintArtifactName(device *dev.DeviceManager) error {
	name, err := device.GetCurrentArtifactName()
	if err != nil {
		return err
	} else if name == "" {
		return errArtifactNameEmpty
	}
	fmt.Fprintln(out, name)
	return nil
}

func PrintProvides(device *dev.DeviceManager) error {
	provides, err := device.GetProvides()
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(provides))
	for k := range provides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintln(out, key+"="+provides[key])
	}
	return nil
}

func runDaemon(d *app.MenderDaemon) error {
	// Handle user forcing update check.
	go func() {
		defer signal.Stop(SignalHandlerChan)

		for {
			s := <-SignalHandlerChan // Block until a signal is received.
			if s == syscall.SIGUSR1 {
				log.Debug("SIGUSR1 signal received.")
				d.ForceToState <- app.States.UpdateCheck
			} else if s == syscall.SIGUSR2 {
				log.Debug("SIGUSR2 signal received.")
				d.ForceToState <- app.States.InventoryUpdate
			}
			d.Sctx.WakeupChan <- true
			log.Debug("Sent wake up!")
		}
	}()
	return d.Run()
}

// sendSignalToProcess sends a SIGUSR{1,2} signal to the running mender daemon.
func sendSignalToProcess(cmdKill, cmdGetPID *system.Cmd) error {
	pid, err := getMenderDaemonPID(cmdGetPID)
	if err != nil {
		return errors.Wrap(err, "failed to force updateCheck")
	}
	cmdKill.Args = append(cmdKill.Args, pid)
	err = cmdKill.Run()
	if err != nil {
		return fmt.Errorf(
			"updateCheck: Failed to send %s the mender process, pid: %s",
			cmdKill.Args[len(cmdKill.Args)-1],
			pid,
		)
	}
	return nil
}
