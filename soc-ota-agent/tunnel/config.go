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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultRenderedConfigPath is the path where soc-ota-tunneld writes the
// rendered frpc TOML before launching frpc.  /data is preserved across OTA
// updates, so the rendered config (which embeds a session token) survives
// firmware upgrades.
const DefaultRenderedConfigPath = "/data/frp/frpc.toml"

// DaemonConfig holds all runtime configuration for soc-ota-tunneld.
//
// Sources (lowest → highest precedence):
//  1. Built-in defaults (DefaultDaemonConfig).
//  2. JSON file loaded by LoadDaemonConfig.
//  3. Environment variables applied by ApplyEnvOverrides.
//
// JSON tags follow camelCase to match the project's existing JSON convention
// (see otapulse.conf).
type DaemonConfig struct {
	// ServerAddr is the hostname or IP of the frps (frp server) host.
	// Required; must be non-empty after all overrides are applied.
	ServerAddr string `json:"serverAddr"`

	// ServerPort is the TCP port the frps listens on for frpc connections.
	// Default: 7000.
	ServerPort int `json:"serverPort"`

	// LocalSSHPort is the port that sshd listens on locally.
	// Default: 22.
	LocalSSHPort int `json:"localSshPort"`

	// ProxyName is the frp proxy/tunnel identifier for this device.
	// It must be unique across all devices connecting to the same frps.
	// Default: empty string, which causes the daemon to derive a name from
	// the system hostname at start-up time.
	ProxyName string `json:"proxyName"`

	// FrpcPath is the absolute path to the frpc binary on this device.
	// Default: /usr/bin/frpc.
	FrpcPath string `json:"frpcPath"`

	// CredentialPath is the path to the per-device tunnel credential file.
	// Default: DefaultCredentialPath (/data/frp/credential).
	CredentialPath string `json:"credentialPath"`

	// RenderedConfigPath is where the daemon writes the rendered frpc TOML
	// before launching frpc.  Must be under /data for update-safety.
	// Default: DefaultRenderedConfigPath (/data/frp/frpc.toml).
	RenderedConfigPath string `json:"renderedConfigPath"`

	// RemotePortHint is the port on the frps host that frpc requests for the
	// reverse-TCP proxy.  0 means "let frps pick a free port" (remotePort is
	// omitted from the generated TOML when this is 0).
	RemotePortHint int `json:"remotePortHint"`
}

// DefaultDaemonConfig returns a DaemonConfig populated with sensible
// production defaults.  ServerAddr is intentionally left empty — it MUST be
// set via the config file or TUNNEL_SERVER_ADDR before the daemon starts.
func DefaultDaemonConfig() DaemonConfig {
	return DaemonConfig{
		ServerAddr:         "",
		ServerPort:         7000,
		LocalSSHPort:       22,
		ProxyName:          "",
		FrpcPath:           "/usr/bin/frpc",
		CredentialPath:     DefaultCredentialPath,
		RenderedConfigPath: DefaultRenderedConfigPath,
		RemotePortHint:     0,
	}
}

// LoadDaemonConfig reads a JSON config file at path and overlays its values
// on top of DefaultDaemonConfig.
//
// Design choice — missing file returns defaults:
// If path does not exist the function returns DefaultDaemonConfig() and a nil
// error, allowing env-only operation (e.g. containerised or test environments
// that inject all settings via TUNNEL_* vars).  Any other I/O error — or a
// file that exists but contains invalid JSON — is returned as a non-nil error.
//
// Only fields present in the JSON file override the defaults; missing JSON
// fields keep the default value.
func LoadDaemonConfig(path string) (DaemonConfig, error) {
	cfg := DefaultDaemonConfig()

	data, err := ioutil.ReadFile(path) // ioutil for go 1.14 compat
	if err != nil {
		if os.IsNotExist(err) {
			// No config file — env-only operation; caller may still apply
			// TUNNEL_* overrides and then validate.
			return cfg, nil
		}
		return cfg, fmt.Errorf("tunnel/config: read config at %q: %w", path, err)
	}

	// Unmarshal directly into cfg so only explicitly-set JSON keys override
	// the defaults.
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("tunnel/config: parse JSON config at %q: %w", path, err)
	}
	return cfg, nil
}

// ApplyEnvOverrides overlays environment variables onto c.
//
// Recognised variables (all optional):
//
//	TUNNEL_SERVER_ADDR        → ServerAddr   (string)
//	TUNNEL_SERVER_PORT        → ServerPort   (int, 1–65535)
//	TUNNEL_LOCAL_SSH_PORT     → LocalSSHPort (int, 1–65535)
//	TUNNEL_PROXY_NAME         → ProxyName    (string)
//	TUNNEL_FRPC_PATH          → FrpcPath     (string)
//	TUNNEL_CREDENTIAL_PATH    → CredentialPath (string)
//	TUNNEL_RENDERED_CONFIG_PATH → RenderedConfigPath (string)
//
// An environment variable that is present but empty is ignored (treated as
// unset) for string fields; for numeric fields an empty value is also ignored.
// A numeric variable that is present but not a valid integer in range
// [1, 65535] returns a non-nil error — bad config must not be silently
// swallowed.
func (c *DaemonConfig) ApplyEnvOverrides() error {
	if v := os.Getenv("TUNNEL_SERVER_ADDR"); v != "" {
		c.ServerAddr = v
	}
	if v := os.Getenv("TUNNEL_PROXY_NAME"); v != "" {
		c.ProxyName = v
	}
	if v := os.Getenv("TUNNEL_FRPC_PATH"); v != "" {
		c.FrpcPath = v
	}
	if v := os.Getenv("TUNNEL_CREDENTIAL_PATH"); v != "" {
		c.CredentialPath = v
	}
	if v := os.Getenv("TUNNEL_RENDERED_CONFIG_PATH"); v != "" {
		c.RenderedConfigPath = v
	}

	var err error
	if v := os.Getenv("TUNNEL_SERVER_PORT"); v != "" {
		c.ServerPort, err = parsePort("TUNNEL_SERVER_PORT", v)
		if err != nil {
			return err
		}
	}
	if v := os.Getenv("TUNNEL_LOCAL_SSH_PORT"); v != "" {
		c.LocalSSHPort, err = parsePort("TUNNEL_LOCAL_SSH_PORT", v)
		if err != nil {
			return err
		}
	}

	return nil
}

// parsePort parses a port string for the named env var and validates range.
func parsePort(envName, val string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(val))
	if err != nil {
		return 0, fmt.Errorf("tunnel/config: env %s: not a valid integer: %q", envName, val)
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("tunnel/config: env %s: port %d out of range [1, 65535]", envName, n)
	}
	return n, nil
}

// Validate checks that c is self-consistent and ready for use.
//
// Errors are returned (not panicked) so callers can report them gracefully.
func (c DaemonConfig) Validate() error {
	if strings.TrimSpace(c.ServerAddr) == "" {
		return fmt.Errorf("tunnel/config: ServerAddr must not be empty (set it in the config file or TUNNEL_SERVER_ADDR)")
	}
	if c.ServerPort < 1 || c.ServerPort > 65535 {
		return fmt.Errorf("tunnel/config: ServerPort %d out of range [1, 65535]", c.ServerPort)
	}
	if c.LocalSSHPort < 1 || c.LocalSSHPort > 65535 {
		return fmt.Errorf("tunnel/config: LocalSSHPort %d out of range [1, 65535]", c.LocalSSHPort)
	}
	if c.RemotePortHint < 0 || c.RemotePortHint > 65535 {
		return fmt.Errorf("tunnel/config: RemotePortHint %d out of range [0, 65535]", c.RemotePortHint)
	}
	if strings.TrimSpace(c.FrpcPath) == "" {
		return fmt.Errorf("tunnel/config: FrpcPath must not be empty")
	}
	if strings.TrimSpace(c.CredentialPath) == "" {
		return fmt.Errorf("tunnel/config: CredentialPath must not be empty")
	}
	if strings.TrimSpace(c.RenderedConfigPath) == "" {
		return fmt.Errorf("tunnel/config: RenderedConfigPath must not be empty")
	}
	return nil
}

// RenderFrpcTOML produces a complete frp v0.69 TOML config string for frpc,
// embedding the given token and the reverse-TCP proxy settings from c.
//
// The rendered string is suitable for writing directly to RenderedConfigPath
// and then passing to frpc via "-c <path>".
//
// SECURITY: token is embedded in the returned string (inside [proxies.metadatas])
// — callers MUST write it to a 0600 file.  Do NOT log the returned string.
//
// frp v0.69 TOML structure:
//
//	serverAddr = "<host>"
//	serverPort = <port>
//
//	[auth]
//	method = "token"
//	token  = ""
//
//	[[proxies]]
//	name       = "<proxyName>"
//	type       = "tcp"
//	localIP    = "127.0.0.1"
//	localPort  = <LocalSSHPort>
//	remotePort = <RemotePortHint>   # omitted when RemotePortHint == 0
//	[proxies.metadatas]
//	sotc_token = "<sotc_...>"
//
// The sotc credential moves from [auth].token to [proxies.metadatas].sotc_token
// so the LAN frps httpPlugin device-auth handler can read it from
// content.metas.sotc_token on the Login op, while frp itself authenticates
// with an empty token (matching the frps auth.method = "none" configuration).
func RenderFrpcTOML(c DaemonConfig, token string) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "serverAddr = %q\n", c.ServerAddr)
	fmt.Fprintf(&sb, "serverPort = %d\n", c.ServerPort)
	sb.WriteString("\n")

	// Auth block — empty token so frps auth.method = "none" is satisfied.
	// The sotc credential is carried in [proxies.metadatas].sotc_token instead,
	// where the frps httpPlugin device-auth handler reads it from the Login op.
	sb.WriteString("[auth]\n")
	sb.WriteString("method = \"token\"\n")
	sb.WriteString("token = \"\"\n")
	sb.WriteString("\n")

	// Proxy block.
	sb.WriteString("[[proxies]]\n")
	fmt.Fprintf(&sb, "name = %q\n", c.ProxyName)
	sb.WriteString("type = \"tcp\"\n")
	sb.WriteString("localIP = \"127.0.0.1\"\n")
	fmt.Fprintf(&sb, "localPort = %d\n", c.LocalSSHPort)
	if c.RemotePortHint != 0 {
		fmt.Fprintf(&sb, "remotePort = %d\n", c.RemotePortHint)
	}

	// [proxies.metadatas] is a TOML sub-table of the current [[proxies]] entry.
	// It MUST come after all key/value pairs of the [[proxies]] table because a
	// sub-table header terminates the parent table's key/value section.
	sb.WriteString("[proxies.metadatas]\n")
	fmt.Fprintf(&sb, "sotc_token = %q\n", token)

	return sb.String()
}

// WriteRenderedConfig atomically writes the rendered frpc TOML config to
// c.RenderedConfigPath with mode 0600 (it embeds the tunnel token).
//
// Atomicity: write to a sibling temp file then os.Rename (POSIX atomic).
// Parent directory is created with mode 0700 if it does not exist.
//
// SECURITY: content MUST come from RenderFrpcTOML; do NOT log it.
func WriteRenderedConfig(c DaemonConfig, content string) error {
	dir := filepath.Dir(c.RenderedConfigPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("tunnel/config: create parent dir %q: %w", dir, err)
	}

	// ioutil.TempFile for go 1.14 compat (os.CreateTemp is Go 1.16+).
	tmp, err := ioutil.TempFile(dir, ".frpc-*.toml.tmp")
	if err != nil {
		return fmt.Errorf("tunnel/config: create temp file in %q: %w", dir, err)
	}
	tmpPath := tmp.Name()

	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tunnel/config: write rendered config to temp file %q: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("tunnel/config: close temp file %q: %w", tmpPath, err)
	}

	// Restrict to 0600 before making visible at the final path.
	if err := os.Chmod(tmpPath, 0600); err != nil {
		return fmt.Errorf("tunnel/config: chmod 0600 temp file %q: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, c.RenderedConfigPath); err != nil {
		return fmt.Errorf("tunnel/config: rename %q → %q: %w", tmpPath, c.RenderedConfigPath, err)
	}
	committed = true
	return nil
}
