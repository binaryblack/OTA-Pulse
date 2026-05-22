// Copyright 2024 OTA-Pulse Authors
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

package tunnel

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

// DefaultCredentialPath is the default location on the device where the
// tunnel credential (sotc_ token) is stored.  The /data partition is
// preserved across OTA updates, so a credential written here survives
// firmware upgrades without requiring re-provisioning.
//
// Use this constant as the default when no explicit path is provided by
// the caller.  Tests MUST pass a temp-dir-based path instead so they
// never touch the real /data path.
const DefaultCredentialPath = "/data/frp/credential"

// LoadCredential reads the tunnel credential from the given file path.
//
// The file is expected to contain a single sotc_ token, optionally
// followed by a trailing newline or whitespace.  The returned string is
// trimmed of all leading and trailing whitespace.
//
// Errors:
//   - file does not exist → wrapped os.ErrNotExist
//   - file exists but is empty after trimming → explicit error
//   - any other I/O error → wrapped error
func LoadCredential(path string) (string, error) {
	// ioutil.ReadFile (not os.ReadFile) keeps this compatible with the
	// module's declared go 1.14 floor; os.ReadFile is a Go 1.16+ alias.
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("tunnel/creds: read credential at %q: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("tunnel/creds: credential file at %q is empty", path)
	}
	return token, nil
}

// SaveCredential atomically writes token to path with mode 0600.
//
// Atomicity is achieved by writing to a sibling temp file and then
// calling os.Rename, which is atomic on POSIX filesystems.  A crash
// between the write and the rename leaves the old credential file intact
// (or no file at all if this is the first write).
//
// The parent directory is created with mode 0700 if it does not exist.
//
// Errors are wrapped with context so callers can identify the failing step.
func SaveCredential(path, token string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("tunnel/creds: create parent directory %q: %w", dir, err)
	}

	// Write to a temp file in the same directory so that os.Rename is
	// guaranteed to be on the same filesystem (cross-device rename fails).
	// ioutil.TempFile (not os.CreateTemp) keeps this compatible with the
	// module's declared go 1.14 floor; os.CreateTemp is a Go 1.16+ alias.
	tmp, err := ioutil.TempFile(dir, ".cred-*.tmp")
	if err != nil {
		return fmt.Errorf("tunnel/creds: create temp file in %q: %w", dir, err)
	}
	tmpPath := tmp.Name()

	// Ensure the temp file is removed on any error path.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.WriteString(token); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tunnel/creds: write credential to temp file %q: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("tunnel/creds: close temp file %q: %w", tmpPath, err)
	}

	// Restrict permissions before making the file visible at the final path.
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return fmt.Errorf("tunnel/creds: chmod 0600 temp file %q: %w", tmpPath, err)
	}

	// Atomic rename: on POSIX this replaces path in one syscall.
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("tunnel/creds: rename %q → %q: %w", tmpPath, path, err)
	}
	committed = true
	return nil
}

// CredentialAuthLines returns the two TOML lines required to configure
// frp token authentication for the given credential.
//
// Example output:
//
//	[auth]
//	method = "token"
//	token = "sotc_abcdef..."
//
// This helper is provided as a convenience for config templating.
// It does NOT write to disk — callers are responsible for embedding the
// returned string into the frpc TOML config before starting the supervisor.
func CredentialAuthLines(token string) string {
	return fmt.Sprintf("[auth]\nmethod = \"token\"\ntoken = %q\n", token)
}
