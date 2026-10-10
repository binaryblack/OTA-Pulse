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
package device

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/binaryblack/OTA-Pulse/conf"
	"github.com/binaryblack/OTA-Pulse/datastore"
	"github.com/binaryblack/OTA-Pulse/installer"
	"github.com/binaryblack/OTA-Pulse/statescript"
	"github.com/binaryblack/OTA-Pulse/store"
)

type DeviceManager struct {
	Config                conf.MenderConfig
	DeviceTypeFile        string
	BootstrapArtifactFile string
	Installers            []installer.PayloadUpdatePerformer
	InstallerFactories    installer.AllModules
	StateScriptExecutor   statescript.Executor
	StateScriptPath       string
	Store                 store.Store
}

func NewDeviceManager(
	dualRootfsDevice installer.DualRootfsDevice,
	config *conf.MenderConfig,
	store store.Store,
) *DeviceManager {
	d := &DeviceManager{
		DeviceTypeFile:        config.DeviceTypeFile,
		BootstrapArtifactFile: config.BootstrapArtifactFile,
		Config:                *config,
		StateScriptPath:       config.ArtifactScriptsPath,
		Store:                 store,
	}
	d.InstallerFactories = installer.AllModules{
		DualRootfs: dualRootfsDevice,
		// nil when dualRootfsDevice is nil. Dormant until the image mounts
		// / read-only and ships xdelta3 (TODO-054/TODO-115).
		DeltaRootfs: installer.NewDeltaRootfsFactory(dualRootfsDevice, d,
			config.ModuleTimeoutSeconds),
		Modules: installer.NewModuleInstallerFactory(config.ModulesPath,
			config.ModulesWorkPath, d, d, config.ModuleTimeoutSeconds),
	}

	return d
}

func NewStateScriptExecutor(config *conf.MenderConfig) statescript.Launcher {
	ret := statescript.Launcher{
		ArtScriptsPath:          config.ArtifactScriptsPath,
		RootfsScriptsPath:       config.RootfsScriptsPath,
		SupportedScriptVersions: []int{2, 3},
		Timeout:                 config.StateScriptTimeoutSeconds,
		RetryInterval:           config.StateScriptRetryIntervalSeconds,
		RetryTimeout:            config.StateScriptRetryTimeoutSeconds,
	}
	return ret
}

func GetManifestData(dataType, manifestFile string) (string, error) {
	// This is where Yocto stores build information
	manifest, err := os.Open(manifestFile)
	if err != nil {
		return "", err
	}
	defer manifest.Close()

	log.Debugf("Reading data from the device manifest file: %s", manifestFile)

	var found *string
	scanner := bufio.NewScanner(manifest)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			// Comments.
			continue
		}

		log.Debug(line)
		lineID := strings.SplitN(line, "=", 2)
		if len(lineID) != 2 {
			log.Errorf("Broken device manifest file: (%v)", lineID)
			return "", fmt.Errorf("Broken device manifest file: (%v)", lineID)
		}
		if lineID[0] == dataType {
			log.Debug("Current manifest data: ", strings.TrimSpace(lineID[1]))
			if found != nil {
				return "", errors.Errorf("More than one instance of %s found in manifest file %s.",
					dataType, manifestFile)
			}
			str := strings.TrimSpace(lineID[1])
			found = &str
		}
	}
	err = scanner.Err()
	if err != nil {
		log.Error(err)
		return "", err
	}
	if found == nil {
		return "", nil
	} else {
		return *found, nil
	}
}

func (d *DeviceManager) GetProvides() (map[string]string, error) {
	return datastore.LoadProvides(d.Store)
}

func (d *DeviceManager) GetCurrentArtifactName() (string, error) {
	return d.getValueFromDatabaseKey(datastore.ArtifactNameKey)
}

func (d *DeviceManager) GetCurrentArtifactGroup() (string, error) {
	return d.getValueFromDatabaseKey(datastore.ArtifactGroupKey)
}

func (d *DeviceManager) getValueFromDatabaseKey(key string) (string, error) {
	if d.Store != nil {
		dbname, err := d.Store.ReadAll(key)
		if err == nil {
			value := string(dbname)
			log.Debugf("Returning %s value %s from database.", key, value)
			return value, nil
		} else if err != os.ErrNotExist {
			log.Errorf("Could not read %s value from database: %s", key, err.Error())
			return "", err
		}
	}
	return "", nil
}

func (d *DeviceManager) GetDeviceType() (string, error) {
	return GetDeviceType(d.DeviceTypeFile)
}

func GetDeviceType(deviceTypeFile string) (string, error) {
	return GetManifestData("device_type", deviceTypeFile)
}

func (d *DeviceManager) ReadArtifactHeaders(from io.ReadCloser) (*installer.Installer, error) {

	deviceType, err := d.GetDeviceType()
	if err != nil {
		// GC-AG-01 / BUG-504: fail closed, like the standalone install path
		// (app/standalone.go). Without a readable device_type the
		// compatibility gate cannot run, so the artifact is refused.
		log.Errorf(
			"Unable to verify the existing hardware, refusing to install: %v : %v",
			d.DeviceTypeFile,
			err,
		)
		return nil, errors.Wrapf(err,
			"unable to read device_type from %s; refusing to install an Artifact "+
				"whose compatibility with this device cannot be verified",
			d.DeviceTypeFile)
	}
	if deviceType == "" {
		// A readable device_type file with no device_type entry is the same
		// failure as an unreadable one: "empty" must not mean "compatible
		// with everything". Applied here (daemon path) and not in
		// installer.ReadHeaders, whose empty-device_type pass-through is
		// pinned by the standalone-install tests (zeroLengthDeviceTypeFile).
		log.Errorf("Device type is empty in %s, refusing to install", d.DeviceTypeFile)
		return nil, errors.Errorf(
			"device_type in %s is empty or missing; refusing to install an Artifact "+
				"whose compatibility with this device cannot be verified",
			d.DeviceTypeFile)
	}

	verifyKeys, err := d.Config.GetVerificationKeys()
	if err != nil {
		log.Errorf("Loading artifact verification keys failed: %v", err)
		return nil, err
	}

	var i *installer.Installer
	i, d.Installers, err = installer.ReadHeaders(from,
		deviceType,
		verifyKeys,
		d.StateScriptPath,
		&d.InstallerFactories)
	return i, err
}

func (d *DeviceManager) GetInstallers() []installer.PayloadUpdatePerformer {
	return d.Installers
}

func (d *DeviceManager) RestoreInstallersFromTypeList(payloadTypes []string) error {
	var err error
	d.Installers, err = installer.CreateInstallersFromList(&d.InstallerFactories, payloadTypes)
	return err
}
