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
package conf

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/binaryblack/OTA-Pulse/client"
	"github.com/binaryblack/OTA-Pulse/dbus"
)

const (
	DefaultUpdateControlMapBootExpirationTimeSeconds = 600
)

type MenderConfigFromFile struct {
	// Path to the public key used to verify signed updates.
	// Only one of ArtifactVerifyKey/ArtifactVerifyKeys can be specified.
	ArtifactVerifyKey string `json:",omitempty"`
	// List of verification keys for verifying signed updates.
	// Starting in order from the first key in the list,
	// each key will try to verify the artifact until one succeeds.
	// Only one of ArtifactVerifyKey/ArtifactVerifyKeys can be specified.
	ArtifactVerifyKeys []string `json:",omitempty"`
	// RequireArtifactVerification, when true, makes it a hard install
	// failure for GetVerificationKeys to resolve zero usable keys (whether
	// because none are configured or because every configured key failed
	// to load). Prevents a device from silently falling back to unsigned
	// artifact installation (GAP-OTA-003).
	RequireArtifactVerification bool `json:",omitempty"`
	// AllowUnsignedArtifacts is the explicit, dev/QEMU-only opt-in to install
	// artifacts WITHOUT authenticating them. It only matters when NO
	// verification key is configured; it never weakens verification when a key
	// is configured. Default false: with no key configured and this unset, the
	// agent refuses every install (GC-AG-07 / NEW-AG-010) so a mis-templated
	// config cannot silently turn artifact authentication off. See the
	// decision table on GetVerificationKeys.
	AllowUnsignedArtifacts bool `json:",omitempty"`

	// HTTPS client parameters
	HttpsClient client.HttpsClient `json:",omitempty"`
	// Security parameters
	Security client.Security `json:",omitempty"`
	// Connectivity connection handling and transfer parameters
	Connectivity client.Connectivity `json:",omitempty"`

	// Rootfs device path
	RootfsPartA string `json:",omitempty"`
	RootfsPartB string `json:",omitempty"`

	// Command to set active partition.
	BootUtilitiesSetActivePart string `json:",omitempty"`
	// Command to get the partition which will boot next.
	BootUtilitiesGetNextActivePart string `json:",omitempty"`

	// Path to the device type file
	DeviceTypeFile string `json:",omitempty"`
	// DBus configuration
	DBus DBusConfig `json:",omitempty"`
	// Expiration timeout for the control map
	UpdateControlMapExpirationTimeSeconds int `json:",omitempty"`
	// Expiration timeout for the control map when just booted
	UpdateControlMapBootExpirationTimeSeconds int `json:",omitempty"`

	// Poll interval for checking for new updates
	UpdatePollIntervalSeconds int `json:",omitempty"`
	// Poll interval for periodically sending inventory data
	InventoryPollIntervalSeconds int `json:",omitempty"`

	// Skip CA certificate validation
	SkipVerify bool `json:",omitempty"`

	// Global retry polling max interval for fetching update, authorize wait and update status
	RetryPollIntervalSeconds int `json:",omitempty"`
	// Global max retry poll count
	RetryPollCount int `json:",omitempty"`

	// State script parameters
	StateScriptTimeoutSeconds      int `json:",omitempty"`
	StateScriptRetryTimeoutSeconds int `json:",omitempty"`
	// Poll interval for checking for update (check-update)
	StateScriptRetryIntervalSeconds int `json:",omitempty"`

	// Update module parameters:

	// The timeout for the execution of the update module, after which it
	// will be killed.
	ModuleTimeoutSeconds int `json:",omitempty"`

	// Path to server SSL certificate
	ServerCertificate string `json:",omitempty"`
	// Server URL (For single server conf)
	ServerURL string `json:",omitempty"`
	// Path to deployment log file
	UpdateLogPath string `json:",omitempty"`
	// Server JWT TenantToken
	TenantToken string `json:",omitempty"`
	// List of available servers, to which client can fall over
	Servers []client.MenderServer `json:",omitempty"`
	// Log level which takes effect right before daemon startup
	DaemonLogLevel string `json:",omitempty"`

	// SoC Monitoring specific configuration
	// When UseSoCMonitoring is true, the client uses X-API-Key and X-Device-ID
	// headers for authentication instead of the standard Mender JWT flow.
	UseSoCMonitoring bool `json:",omitempty"`
	// API key for SoC Monitoring authentication
	SoCAPIKey string `json:",omitempty"`
	// Device ID for SoC Monitoring authentication (can be hardware ID or UUID)
	SoCDeviceID string `json:",omitempty"`

	// Boot environment configuration
	// UseFileBasedBootEnv enables file-based boot environment instead of U-Boot env.
	// This is useful when fw_printenv/fw_setenv are not available or not configured.
	// When true, boot state is stored in /data/ota/ directory.
	// When false (default), tries U-Boot env first, falls back to file-based if it fails.
	UseFileBasedBootEnv bool `json:",omitempty"`

	// BootEnvMode selects who owns the boot-slot flag. See the BootEnvMode*
	// constants. Empty (the default) and "file" behave exactly as before this
	// field existed (UseFileBasedBootEnv, then bootloader probing with a silent
	// fall back to the file-based environment). "bootloader" is for boards
	// that use the bootloader's own A/B integration: the agent must use the
	// bootloader environment and must refuse to run without it (it never
	// falls back to the file-based environment). Any other value is rejected
	// by ValidateBootEnvMode.
	BootEnvMode string `json:",omitempty"`
}

const (
	// BootEnvModeFile is the default mode. It is the value the agent assumes
	// when BootEnvMode is absent or empty.
	BootEnvModeFile = "file"
	// BootEnvModeBootloader makes the bootloader environment mandatory.
	BootEnvModeBootloader = "bootloader"
)

// ValidateBootEnvMode checks BootEnvMode, failing closed. It rejects:
//   - any value other than "", "file" or "bootloader" (a typo such as
//     "Bootloader" must not silently select file mode on a board whose
//     bootloader owns the boot flag), and
//   - BootEnvMode=bootloader combined with UseFileBasedBootEnv=true, which
//     contradict each other (file-based state vs. bootloader-owned state).
//     BootEnvMode=bootloader is only valid when UseFileBasedBootEnv is false
//     or absent. The agent does not pick a winner, because the wrong guess
//     would leave the boot flag in the wrong place.
func (c *MenderConfigFromFile) ValidateBootEnvMode() error {
	switch c.BootEnvMode {
	case "", BootEnvModeFile:
		return nil
	case BootEnvModeBootloader:
		if c.UseFileBasedBootEnv {
			return errors.New("invalid configuration: BootEnvMode \"bootloader\" " +
				"contradicts UseFileBasedBootEnv=true; set UseFileBasedBootEnv to false " +
				"or remove it")
		}
		return nil
	default:
		return errors.Errorf("invalid BootEnvMode %q: must be %q or %q",
			c.BootEnvMode, BootEnvModeFile, BootEnvModeBootloader)
	}
}

type MenderConfig struct {
	MenderConfigFromFile

	// Additional fields that are in our config struct for convenience, but
	// not actually configurable via the config file.
	ModulesPath     string
	ModulesWorkPath string

	ArtifactScriptsPath string
	RootfsScriptsPath   string

	BootstrapArtifactFile string
}

type DBusConfig struct {
	Enabled bool
}

type DualRootfsDeviceConfig struct {
	RootfsPartA string
	RootfsPartB string
}

func NewMenderConfig() *MenderConfig {
	return &MenderConfig{
		MenderConfigFromFile:  MenderConfigFromFile{},
		ModulesPath:           DefaultModulesPath,
		ModulesWorkPath:       DefaultModulesWorkPath,
		ArtifactScriptsPath:   DefaultArtScriptsPath,
		RootfsScriptsPath:     DefaultRootfsScriptsPath,
		BootstrapArtifactFile: DefaultBootstrapArtifactFile,
	}
}

// LoadConfig parses the OTAPulse configuration json-files
// (/etc/otapulse/otapulse.conf and /var/lib/otapulse/otapulse.conf with
// fallback to /etc/mender/mender.conf for backward compatibility) and loads the
// values into the MenderConfig structure defining high level client
// configurations.
func LoadConfig(mainConfigFile string, fallbackConfigFile string) (*MenderConfig, error) {
	// Load fallback configuration first, then main configuration.
	// It is OK if either file does not exist, so long as the other one does exist.
	// It is also OK if both files exist.
	// Because the main configuration is loaded last, its option values
	// override those from the fallback file, for options present in both files.

	var filesLoadedCount int
	config := NewMenderConfig()

	// If DBus is compiled in, enable it by default
	_, err := dbus.GetDBusAPI()
	if err == nil {
		config.DBus.Enabled = true
	}

	if loadErr := loadConfigFile(fallbackConfigFile, config, &filesLoadedCount); loadErr != nil {
		return nil, loadErr
	}

	if loadErr := loadConfigFile(mainConfigFile, config, &filesLoadedCount); loadErr != nil {
		return nil, loadErr
	}

	log.Debugf("Loaded %d configuration file(s)", filesLoadedCount)

	checkConfigDefaults(config)

	if filesLoadedCount == 0 {
		log.Info("No configuration files present. Using defaults")
		return config, nil
	}

	log.Debugf("Loaded configuration = %#v", redactedForLog(config))

	return config, nil
}

// redactedConfigValue replaces non-empty secrets in log output.
const redactedConfigValue = "[REDACTED]"

// redactedForLog returns a shallow copy of c with the secret fields (device API
// key, tenant token) masked, for use in log lines only (GC-AG-06): debug dumps
// end up in the journal and in uploaded deployment logs. The original c is
// never modified. Empty secrets stay empty so "not configured" remains visible.
func redactedForLog(c *MenderConfig) MenderConfig {
	cp := *c
	if cp.SoCAPIKey != "" {
		cp.SoCAPIKey = redactedConfigValue
	}
	if cp.TenantToken != "" {
		cp.TenantToken = redactedConfigValue
	}
	return cp
}

// Validate verifies the Servers fields in the configuration
func (c *MenderConfig) Validate() error {
	if err := c.ValidateBootEnvMode(); err != nil {
		return err
	}

	if c.Servers == nil {
		if c.ServerURL == "" {
			log.Warn("No server URL(s) specified in OTAPulse configuration.")
		}
		c.Servers = make([]client.MenderServer, 1)
		c.Servers[0].ServerURL = c.ServerURL
	} else if c.ServerURL != "" {
		log.Error("In otapulse.conf: don't specify both Servers field " +
			"AND the corresponding fields in base structure (i.e. " +
			"ServerURL). The first server on the list overwrites" +
			"these fields.")
		return errors.New("Both Servers AND ServerURL given in " +
			"otapulse.conf")
	}
	for i := 0; i < len(c.Servers); i++ {
		// Trim possible '/' suffix, which is added back in URL path
		c.Servers[i].ServerURL = strings.TrimSuffix(c.Servers[i].ServerURL, "/")
		if c.Servers[i].ServerURL == "" {
			log.Warnf("Server entry %d has no associated server URL.", i+1)
		}
	}

	c.HttpsClient.Validate()

	if c.HttpsClient.Key != "" && c.Security.AuthPrivateKey != "" {
		log.Warn("both config.HttpsClient.Key and config.Security.AuthPrivateKey" +
			" specified; config.Security.AuthPrivateKey will take precedence over" +
			" the former for the signing of auth requests.")
	}

	log.Debugf("Verified configuration = %#v", redactedForLog(c))

	return nil
}

func (c *MenderConfigFromFile) GetUpdateControlMapExpirationTimeSeconds() int {
	if c.UpdateControlMapExpirationTimeSeconds == 0 {
		return 2 * c.UpdatePollIntervalSeconds
	}
	return c.UpdateControlMapExpirationTimeSeconds
}

func (c *MenderConfigFromFile) GetUpdateControlMapBootExpirationTimeSeconds() int {
	if c.UpdateControlMapBootExpirationTimeSeconds == 0 {
		return DefaultUpdateControlMapBootExpirationTimeSeconds
	}
	return c.UpdateControlMapBootExpirationTimeSeconds
}

func checkConfigDefaults(config *MenderConfig) {
	if config.MenderConfigFromFile.UpdateControlMapExpirationTimeSeconds == 0 {
		log.Info(
			"'UpdateControlMapExpirationTimeSeconds' is not set " +
				"in the otapulse.conf configuration file." +
				" Falling back to the default of 2*UpdatePollIntervalSeconds")
	}

	if config.MenderConfigFromFile.UpdateControlMapBootExpirationTimeSeconds == 0 {
		log.Infof(
			"'UpdateControlMapBootExpirationTimeSeconds' is not set "+
				"in the otapulse.conf configuration file."+
				" Falling back to the default of %d seconds",
			DefaultUpdateControlMapBootExpirationTimeSeconds,
		)
	}
}

func loadConfigFile(configFile string, config *MenderConfig, filesLoadedCount *int) error {
	// Do not treat a single config file not existing as an error here.
	// It is up to the caller to fail when both config files don't exist.
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		log.Debug("Configuration file does not exist: ", configFile)
		return nil
	}

	if err := readConfigFile(&config.MenderConfigFromFile, configFile); err != nil {
		log.Errorf("Error loading configuration from file: %s (%s)", configFile, err.Error())
		return err
	}

	if config.ArtifactVerifyKey != "" {
		if len(config.ArtifactVerifyKeys) > 0 {
			return errors.New("both ArtifactVerifyKey and ArtifactVerifyKeys are set")
		}
		// Unify the logic for verification key processing by moving
		// the single ArtifactVerifyKey to the list version.
		config.ArtifactVerifyKeys = append(config.ArtifactVerifyKeys, config.ArtifactVerifyKey)
		config.ArtifactVerifyKey = ""
	}

	(*filesLoadedCount)++
	log.Info("Loaded configuration file: ", configFile)
	return nil
}

func readConfigFile(config interface{}, fileName string) error {
	// Reads mender configuration (JSON) file.

	log.Debug("Reading OTAPulse configuration from file " + fileName)
	conf, err := ioutil.ReadFile(fileName)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(conf, &config); err != nil {
		switch err.(type) {
		case *json.SyntaxError:
			return errors.New("Error parsing otapulse.conf configuration file: " + err.Error())
		}
		return errors.New("Error parsing config file: " + err.Error())
	}

	return nil
}

// SaveConfigFile writes config to filename atomically: the JSON is written to
// a temporary file in the same directory, fsynced, and renamed over the target,
// so a power cut can never leave a truncated or empty configuration (GC-AG-14).
// An existing file keeps its permission bits; a new file is created 0600 (see
// MEN-3762). A symlink at filename is followed so the link itself survives.
func SaveConfigFile(config *MenderConfigFromFile, filename string) error {
	configJson, err := json.MarshalIndent(config, "", "    ")
	if err != nil {
		return errors.Wrap(err, "Error encoding configuration to JSON")
	}

	// Open (creating if absent, never truncating) the target first: this keeps
	// the historical behaviour that an unwritable target or missing parent
	// directory is reported as an open error, and gives us the mode to carry
	// over to the replacement.
	f, err := os.OpenFile(
		filename,
		os.O_WRONLY|os.O_CREATE,
		0600,
	) // for mode see MEN-3762
	if err != nil {
		return errors.Wrap(err, "Error opening configuration file")
	}
	fi, statErr := f.Stat()
	f.Close()

	target := filename
	if resolved, rerr := filepath.EvalSymlinks(filename); rerr == nil {
		target = resolved
	}

	tmp, err := ioutil.TempFile(filepath.Dir(target), "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return errors.Wrap(err, "Error creating temporary configuration file")
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if _, err = tmp.Write(configJson); err != nil {
		cleanup()
		return errors.Wrap(err, "Error writing to configuration file")
	}
	perm := os.FileMode(0600)
	if statErr == nil {
		perm = fi.Mode().Perm()
	}
	if err = tmp.Chmod(perm); err != nil {
		cleanup()
		return errors.Wrap(err, "Error setting configuration file mode")
	}
	if err = tmp.Sync(); err != nil {
		cleanup()
		return errors.Wrap(err, "Error syncing configuration file")
	}
	if err = tmp.Close(); err != nil {
		os.Remove(tmpName)
		return errors.Wrap(err, "Error closing configuration file")
	}
	if err = os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return errors.Wrap(err, "Error replacing configuration file")
	}
	// Persist the directory entry too; best effort (not all filesystems allow
	// fsync on a directory).
	if d, derr := os.Open(filepath.Dir(target)); derr == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

func maybeHTTPSClient(c *MenderConfig) *client.HttpsClient {
	if c.HttpsClient.Certificate != "" && c.HttpsClient.Key != "" {
		return &c.HttpsClient
	}
	c.HttpsClient.Validate()
	return nil
}

func (c *MenderConfig) GetHttpConfig() client.Config {
	return client.Config{
		ServerCert: c.ServerCertificate,
		// The HttpsClient config is only loaded when both a cert and
		// key is given
		HttpsClient:  maybeHTTPSClient(c),
		NoVerify:     c.SkipVerify,
		Connectivity: &c.Connectivity,
	}
}

func (c *MenderConfig) GetDeviceConfig() DualRootfsDeviceConfig {
	return DualRootfsDeviceConfig{
		RootfsPartA: c.RootfsPartA,
		RootfsPartB: c.RootfsPartB,
	}
}

func (c *MenderConfig) GetDeploymentLogLocation() string {
	return c.UpdateLogPath
}

// GetTenantToken returns a default tenant-token if
// no custom token is set in local.conf
func (c *MenderConfig) GetTenantToken() []byte {
	return []byte(c.TenantToken)
}

type VerificationKey struct {
	Path string
	Data []byte
}

// GetVerificationKeys reads all verification keys. A ReadFile error on any
// CONFIGURED key is treated as fatal (returned, not logged-and-skipped): a
// mis-set key path must never silently downgrade the device to accepting
// unsigned artifacts (GAP-OTA-003). When RequireArtifactVerification is set,
// resolving zero keys (none configured, or all configured keys unreadable)
// is also a hard error.
//
// Decision table (GC-AG-07). "keys" = at least one ArtifactVerifyKey(s)
// entry is configured (ArtifactVerifyKey is folded into the list on load):
//
//	keys  RequireArtifactVerification  AllowUnsignedArtifacts  result
//	yes   any                          any                     VERIFY every artifact with the keys
//	                                                           (an unreadable key is fatal; Allow
//	                                                           never downgrades a configured key)
//	no    true                         any                     REFUSE (Require wins over Allow)
//	no    false                        false                   REFUSE (new default, fail closed)
//	no    false                        true                    INSTALL WITHOUT VERIFICATION (WARN);
//	                                                           dev/QEMU images only
//
// A nil, nil return therefore always means "operator explicitly allowed
// unauthenticated installs"; the installer's no-key path is only reachable
// through that opt-in.
func (c *MenderConfig) GetVerificationKeys() ([]*VerificationKey, error) {
	if len(c.ArtifactVerifyKeys) == 0 {
		if c.RequireArtifactVerification {
			return nil, errors.New(
				"config: RequireArtifactVerification is set but no " +
					"ArtifactVerifyKey(s) are configured")
		}
		if !c.AllowUnsignedArtifacts {
			return nil, errors.New(
				"config: no ArtifactVerifyKey(s) are configured and " +
					"AllowUnsignedArtifacts is not set; refusing to install an " +
					"artifact that cannot be authenticated (configure " +
					"ArtifactVerifyKeys, or set AllowUnsignedArtifacts=true on " +
					"dev/test devices only)")
		}
		log.Warn("config: AllowUnsignedArtifacts is set and no ArtifactVerifyKey(s) " +
			"are configured: artifacts will be installed WITHOUT authentication " +
			"(dev/test only)")
		return nil, nil
	}

	var out []*VerificationKey
	for _, keyPath := range c.ArtifactVerifyKeys {
		key, err := ioutil.ReadFile(keyPath)
		if err != nil {
			log.Errorf("config: error reading configured artifact verify key from %v: %v", keyPath, err)
			return nil, errors.Wrapf(err, "config: error reading configured artifact verify key from %v", keyPath)
		}
		out = append(out, &VerificationKey{
			Path: keyPath,
			Data: key,
		})
	}

	if len(out) == 0 && c.RequireArtifactVerification {
		return nil, errors.New(
			"config: RequireArtifactVerification is set but no usable " +
				"artifact verify keys were loaded")
	}

	return out, nil
}
