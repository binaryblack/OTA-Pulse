// Copyright 2022 Northern.tech AS
//
//    Licensed under the Apache License, Version 2.0 (the "License");
//    you may not use this file except in compliance with the License.
//    You may obtain a copy of the License at
//
//        http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS,
//    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//    See the License for the specific language governing permissions and
//    limitations under the License.

//go:build !local
// +build !local

package conf

import (
	"os"
	"path"
)

const (
	BrokenArtifactSuffix = "_INCONSISTENT"
)

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if len(value) == 0 {
		return fallback
	}
	return value
}

// getenvWithFallback checks primary env var, then fallback env var, then default value
func getenvWithFallback(primaryKey, fallbackKey, defaultValue string) string {
	// First check the primary (OTAPulse) environment variable
	value := os.Getenv(primaryKey)
	if len(value) > 0 {
		return value
	}
	// Then check the fallback (Mender) environment variable for backward compatibility
	value = os.Getenv(fallbackKey)
	if len(value) > 0 {
		return value
	}
	// Finally use the default value
	return defaultValue
}

// pathExistsAndNotEmpty checks if a directory exists and is not empty
func pathExistsAndNotEmpty(dirPath string) bool {
	info, err := os.Stat(dirPath)
	if err != nil || !info.IsDir() {
		return false
	}
	// Check if directory has any files
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

var (
	// OTAPulse paths - primary paths with fallback to Mender paths for backward compatibility
	// Priority: OTAPULSE_* env var -> MENDER_* env var -> OTAPulse default -> Mender fallback (if OTAPulse doesn't exist)
	DefaultPathConfDir = getenvWithFallback("OTAPULSE_CONF_DIR", "MENDER_CONF_DIR", "/etc/otapulse")
	DefaultPathDataDir = getenvWithFallback("OTAPULSE_DATA_DIR", "MENDER_DATA_DIR", "/usr/share/otapulse")
	DefaultDataStore   = getenvWithFallback("OTAPULSE_DATASTORE_DIR", "MENDER_DATASTORE_DIR", "/var/lib/otapulse")

	// Key file names - new OTAPulse naming
	DefaultKeyFile     = "otapulse-agent.pem"
	LegacyKeyFile      = "mender-agent.pem" // For backward compatibility

	DefaultConfFile         = path.Join(GetConfDirPath(), "otapulse.conf")
	DefaultFallbackConfFile = path.Join(GetStateDirPath(), "otapulse.conf")

	// Legacy Mender paths for fallback support
	LegacyConfDir      = "/etc/mender"
	LegacyDataDir      = "/usr/share/mender"
	LegacyDataStore    = "/var/lib/mender"
	LegacyConfFile     = path.Join(LegacyConfDir, "mender.conf")
)

var (
	// device specific paths
	DefaultArtScriptsPath    = path.Join(GetStateDirPath(), "scripts")
	DefaultRootfsScriptsPath = path.Join(GetConfDirPath(), "scripts")
	DefaultModulesPath       = path.Join(GetDataDirPath(), "modules", "v3")
	DefaultModulesWorkPath   = path.Join(GetStateDirPath(), "modules", "v3")

	DefaultBootstrapArtifactFile = path.Join(GetStateDirPath(), "bootstrap.mender")

	// deprecated files
	DeprecatedArtifactInfoFile = path.Join(GetConfDirPath(), "artifact_info")
)

func GetDataDirPath() string {
	// Check if OTAPulse path exists and is populated
	if pathExistsAndNotEmpty(DefaultPathDataDir) {
		return DefaultPathDataDir
	}
	// Fallback to legacy Mender path if it exists
	if pathExistsAndNotEmpty(LegacyDataDir) {
		return LegacyDataDir
	}
	// Default to OTAPulse path (will be created if needed)
	return DefaultPathDataDir
}

func GetStateDirPath() string {
	// Check if OTAPulse path exists and is populated
	if pathExistsAndNotEmpty(DefaultDataStore) {
		return DefaultDataStore
	}
	// Fallback to legacy Mender path if it exists
	if pathExistsAndNotEmpty(LegacyDataStore) {
		return LegacyDataStore
	}
	// Default to OTAPulse path (will be created if needed)
	return DefaultDataStore
}

func GetConfDirPath() string {
	// Check if OTAPulse path exists and is populated
	if pathExistsAndNotEmpty(DefaultPathConfDir) {
		return DefaultPathConfDir
	}
	// Fallback to legacy Mender path if it exists
	if pathExistsAndNotEmpty(LegacyConfDir) {
		return LegacyConfDir
	}
	// Default to OTAPulse path (will be created if needed)
	return DefaultPathConfDir
}
