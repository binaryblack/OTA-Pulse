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
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --------------------------------------------------------------------------
// DefaultDaemonConfig
// --------------------------------------------------------------------------

func TestDefaultDaemonConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg := DefaultDaemonConfig()

	if cfg.ServerAddr != "" {
		t.Errorf("ServerAddr default: want empty, got %q", cfg.ServerAddr)
	}
	if cfg.ServerPort != 7000 {
		t.Errorf("ServerPort default: want 7000, got %d", cfg.ServerPort)
	}
	if cfg.LocalSSHPort != 22 {
		t.Errorf("LocalSSHPort default: want 22, got %d", cfg.LocalSSHPort)
	}
	if cfg.FrpcPath != "/usr/bin/frpc" {
		t.Errorf("FrpcPath default: want /usr/bin/frpc, got %q", cfg.FrpcPath)
	}
	if cfg.CredentialPath != DefaultCredentialPath {
		t.Errorf("CredentialPath default: want %q, got %q", DefaultCredentialPath, cfg.CredentialPath)
	}
	if cfg.RenderedConfigPath != DefaultRenderedConfigPath {
		t.Errorf("RenderedConfigPath default: want %q, got %q", DefaultRenderedConfigPath, cfg.RenderedConfigPath)
	}
	if cfg.RemotePortHint != 0 {
		t.Errorf("RemotePortHint default: want 0, got %d", cfg.RemotePortHint)
	}
}

// --------------------------------------------------------------------------
// LoadDaemonConfig
// --------------------------------------------------------------------------

func TestLoadDaemonConfig_MissingFile_ReturnsDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg, err := LoadDaemonConfig(filepath.Join(dir, "does_not_exist.conf"))
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	def := DefaultDaemonConfig()
	if cfg.ServerPort != def.ServerPort {
		t.Errorf("ServerPort: got %d, want %d", cfg.ServerPort, def.ServerPort)
	}
	if cfg.FrpcPath != def.FrpcPath {
		t.Errorf("FrpcPath: got %q, want %q", cfg.FrpcPath, def.FrpcPath)
	}
}

func TestLoadDaemonConfig_JSONOverlay(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "tunnel.conf")

	// Write a partial JSON config — only override some fields.
	content := `{
		"serverAddr": "tunnel.example.com",
		"serverPort": 7001,
		"proxyName":  "rpi4-device-abc"
	}`
	if err := ioutil.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadDaemonConfig(path)
	if err != nil {
		t.Fatalf("LoadDaemonConfig: %v", err)
	}
	if cfg.ServerAddr != "tunnel.example.com" {
		t.Errorf("ServerAddr: got %q, want tunnel.example.com", cfg.ServerAddr)
	}
	if cfg.ServerPort != 7001 {
		t.Errorf("ServerPort: got %d, want 7001", cfg.ServerPort)
	}
	if cfg.ProxyName != "rpi4-device-abc" {
		t.Errorf("ProxyName: got %q, want rpi4-device-abc", cfg.ProxyName)
	}
	// Fields not in JSON keep the default.
	if cfg.LocalSSHPort != 22 {
		t.Errorf("LocalSSHPort (default): got %d, want 22", cfg.LocalSSHPort)
	}
	if cfg.FrpcPath != "/usr/bin/frpc" {
		t.Errorf("FrpcPath (default): got %q, want /usr/bin/frpc", cfg.FrpcPath)
	}
}

func TestLoadDaemonConfig_InvalidJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.conf")

	if err := ioutil.WriteFile(path, []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := LoadDaemonConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// --------------------------------------------------------------------------
// ApplyEnvOverrides
// --------------------------------------------------------------------------

func setEnvForTest(t *testing.T, key, val string) {
	t.Helper()
	old, hadOld := os.LookupEnv(key)
	if err := os.Setenv(key, val); err != nil {
		t.Fatalf("Setenv %s: %v", key, err)
	}
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	old, hadOld := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv(key, old)
		}
	})
}

func TestApplyEnvOverrides_AllVars(t *testing.T) {
	// Not parallel — mutates env.
	setEnvForTest(t, "TUNNEL_SERVER_ADDR", "env-server.example.com")
	setEnvForTest(t, "TUNNEL_SERVER_PORT", "8001")
	setEnvForTest(t, "TUNNEL_LOCAL_SSH_PORT", "2222")
	setEnvForTest(t, "TUNNEL_PROXY_NAME", "env-proxy")
	setEnvForTest(t, "TUNNEL_FRPC_PATH", "/opt/frpc")
	setEnvForTest(t, "TUNNEL_CREDENTIAL_PATH", "/data/frp/custom-cred")
	setEnvForTest(t, "TUNNEL_RENDERED_CONFIG_PATH", "/data/frp/custom.toml")

	cfg := DefaultDaemonConfig()
	if err := cfg.ApplyEnvOverrides(); err != nil {
		t.Fatalf("ApplyEnvOverrides: %v", err)
	}

	if cfg.ServerAddr != "env-server.example.com" {
		t.Errorf("ServerAddr: got %q", cfg.ServerAddr)
	}
	if cfg.ServerPort != 8001 {
		t.Errorf("ServerPort: got %d, want 8001", cfg.ServerPort)
	}
	if cfg.LocalSSHPort != 2222 {
		t.Errorf("LocalSSHPort: got %d, want 2222", cfg.LocalSSHPort)
	}
	if cfg.ProxyName != "env-proxy" {
		t.Errorf("ProxyName: got %q", cfg.ProxyName)
	}
	if cfg.FrpcPath != "/opt/frpc" {
		t.Errorf("FrpcPath: got %q", cfg.FrpcPath)
	}
	if cfg.CredentialPath != "/data/frp/custom-cred" {
		t.Errorf("CredentialPath: got %q", cfg.CredentialPath)
	}
	if cfg.RenderedConfigPath != "/data/frp/custom.toml" {
		t.Errorf("RenderedConfigPath: got %q", cfg.RenderedConfigPath)
	}
}

func TestApplyEnvOverrides_BadNumericPort_Error(t *testing.T) {
	tests := []struct {
		envKey string
		val    string
	}{
		{"TUNNEL_SERVER_PORT", "not-a-number"},
		{"TUNNEL_SERVER_PORT", "0"},     // below range
		{"TUNNEL_SERVER_PORT", "65536"}, // above range
		{"TUNNEL_LOCAL_SSH_PORT", "abc"},
		{"TUNNEL_LOCAL_SSH_PORT", "-1"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.envKey+"="+tc.val, func(t *testing.T) {
			// Not parallel — mutates env.
			// Unset the other port key so only the target key matters.
			unsetEnvForTest(t, "TUNNEL_SERVER_PORT")
			unsetEnvForTest(t, "TUNNEL_LOCAL_SSH_PORT")
			setEnvForTest(t, tc.envKey, tc.val)

			cfg := DefaultDaemonConfig()
			err := cfg.ApplyEnvOverrides()
			if err == nil {
				t.Errorf("expected error for %s=%q, got nil", tc.envKey, tc.val)
			}
		})
	}
}

func TestApplyEnvOverrides_EmptyVars_Ignored(t *testing.T) {
	// Not parallel — mutates env.
	setEnvForTest(t, "TUNNEL_SERVER_ADDR", "")
	setEnvForTest(t, "TUNNEL_SERVER_PORT", "")
	setEnvForTest(t, "TUNNEL_LOCAL_SSH_PORT", "")

	cfg := DefaultDaemonConfig()
	if err := cfg.ApplyEnvOverrides(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Defaults must remain untouched.
	if cfg.ServerPort != 7000 {
		t.Errorf("ServerPort after empty env: got %d, want 7000", cfg.ServerPort)
	}
	if cfg.LocalSSHPort != 22 {
		t.Errorf("LocalSSHPort after empty env: got %d, want 22", cfg.LocalSSHPort)
	}
}

// --------------------------------------------------------------------------
// Validate
// --------------------------------------------------------------------------

func TestValidate_EmptyServerAddr_Error(t *testing.T) {
	t.Parallel()
	cfg := DefaultDaemonConfig()
	// Leave ServerAddr empty.
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty ServerAddr")
	}
}

func TestValidate_Valid(t *testing.T) {
	t.Parallel()
	cfg := DefaultDaemonConfig()
	cfg.ServerAddr = "tunnel.example.com"
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected valid config to pass Validate, got: %v", err)
	}
}

func TestValidate_OutOfRangePorts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		fn   func(*DaemonConfig)
	}{
		{"ServerPort=0", func(c *DaemonConfig) { c.ServerPort = 0 }},
		{"ServerPort=65536", func(c *DaemonConfig) { c.ServerPort = 65536 }},
		{"LocalSSHPort=0", func(c *DaemonConfig) { c.LocalSSHPort = 0 }},
		{"LocalSSHPort=70000", func(c *DaemonConfig) { c.LocalSSHPort = 70000 }},
		{"RemotePortHint=-1", func(c *DaemonConfig) { c.RemotePortHint = -1 }},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultDaemonConfig()
			cfg.ServerAddr = "tunnel.example.com"
			tc.fn(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Errorf("%s: expected error, got nil", tc.name)
			}
		})
	}
}

func TestValidate_EmptyPaths_Error(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		fn   func(*DaemonConfig)
	}{
		{"FrpcPath empty", func(c *DaemonConfig) { c.FrpcPath = "" }},
		{"CredentialPath empty", func(c *DaemonConfig) { c.CredentialPath = "" }},
		{"RenderedConfigPath empty", func(c *DaemonConfig) { c.RenderedConfigPath = "" }},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultDaemonConfig()
			cfg.ServerAddr = "tunnel.example.com"
			tc.fn(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Errorf("%s: expected error, got nil", tc.name)
			}
		})
	}
}

// --------------------------------------------------------------------------
// RenderFrpcTOML
// --------------------------------------------------------------------------

type frpcTomlCase struct {
	name           string
	cfg            DaemonConfig
	token          string
	wantServer     string // expected serverAddr value
	wantPort       string // expected serverPort value
	wantProxyName  string
	wantLocalPort  string
	wantRemote     bool   // whether remotePort line should be present
	wantRemotePort string // if wantRemote
	wantToken      string // must appear in output
}

func buildFrpcTomlCases() []frpcTomlCase {
	base := DefaultDaemonConfig()
	base.ServerAddr = "staging.ota-pulse.com"
	base.ProxyName = "rpi4-abc123"

	withRemote := base
	withRemote.RemotePortHint = 54321

	return []frpcTomlCase{
		{
			name:          "no remotePort when hint=0",
			cfg:           base,
			token:         "sotc_testtoken",
			wantServer:    `"staging.ota-pulse.com"`,
			wantPort:      "7000",
			wantProxyName: `"rpi4-abc123"`,
			wantLocalPort: "22",
			wantRemote:    false,
			wantToken:     "sotc_testtoken",
		},
		{
			name:           "remotePort present when hint!=0",
			cfg:            withRemote,
			token:          "sotc_anothertoken",
			wantServer:     `"staging.ota-pulse.com"`,
			wantPort:       "7000",
			wantProxyName:  `"rpi4-abc123"`,
			wantLocalPort:  "22",
			wantRemote:     true,
			wantRemotePort: "54321",
			wantToken:      "sotc_anothertoken",
		},
	}
}

func TestRenderFrpcTOML(t *testing.T) {
	t.Parallel()
	for _, tc := range buildFrpcTomlCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := RenderFrpcTOML(tc.cfg, tc.token)

			assertContains := func(label, needle string) {
				t.Helper()
				if !strings.Contains(out, needle) {
					t.Errorf("%s: expected %q in TOML output\nGot:\n%s", label, needle, out)
				}
			}
			assertAbsent := func(label, needle string) {
				t.Helper()
				if strings.Contains(out, needle) {
					t.Errorf("%s: expected %q absent from TOML output\nGot:\n%s", label, needle, out)
				}
			}

			assertContains("serverAddr", "serverAddr = "+tc.wantServer)
			assertContains("serverPort", "serverPort = "+tc.wantPort)
			assertContains("auth method", `method = "token"`)
			// [auth].token must be empty — sotc moves to [proxies.metadatas].
			assertContains("auth token empty", `token = ""`)
			// The bare "token" key (not sotc_token) must not hold the credential.
			// Use newline prefix to avoid matching the "sotc_token" key name.
			assertAbsent("sotc not in auth token", "\ntoken = \""+tc.wantToken)
			assertContains("[auth]", "[auth]")
			assertContains("[[proxies]]", "[[proxies]]")
			assertContains("proxyName", "name = "+tc.wantProxyName)
			assertContains("type=tcp", `type = "tcp"`)
			assertContains("localIP", `localIP = "127.0.0.1"`)
			assertContains("localPort", "localPort = "+tc.wantLocalPort)
			// sotc credential must appear in [proxies.metadatas].
			assertContains("[proxies.metadatas]", "[proxies.metadatas]")
			assertContains("sotc_token in metadatas", `sotc_token = "`+tc.wantToken+`"`)

			if tc.wantRemote {
				assertContains("remotePort", "remotePort = "+tc.wantRemotePort)
				// remotePort must appear before [proxies.metadatas] (sub-table
				// terminates the parent table's key/value section).
				rpIdx := strings.Index(out, "remotePort")
				metaIdx := strings.Index(out, "[proxies.metadatas]")
				if rpIdx > metaIdx {
					t.Errorf("remotePort must appear before [proxies.metadatas] in TOML\nGot:\n%s", out)
				}
			} else {
				assertAbsent("remotePort absent", "remotePort")
			}

			// Validate the output is valid JSON-serialisable TOML-like structure
			// (we don't have a TOML lib in this module, so string assertions
			// are the right approach).
			_ = out // already tested above
		})
	}
}

func TestRenderFrpcTOML_SotcInMetadatas(t *testing.T) {
	t.Parallel()
	c := DefaultDaemonConfig()
	c.ServerAddr = "10.0.0.5"
	c.ProxyName = "dev-abc123"
	out := RenderFrpcTOML(c, "sotc_deadbeef")

	// [auth].token must be empty
	if !strings.Contains(out, `token = ""`) {
		t.Fatalf("expected [auth].token = \"\" in rendered TOML; got:\n%s", out)
	}
	// "token = " (the bare auth key, not sotc_token) must not carry the credential.
	// We check for the bare key preceded by a newline to avoid matching "sotc_token".
	if strings.Contains(out, "\ntoken = \"sotc_deadbeef\"") {
		t.Fatalf("sotc must NOT appear in [auth].token; got:\n%s", out)
	}
	// [proxies.metadatas].sotc_token must carry the credential
	if !strings.Contains(out, "[proxies.metadatas]") {
		t.Fatalf("missing [proxies.metadatas] sub-table; got:\n%s", out)
	}
	if !strings.Contains(out, `sotc_token = "sotc_deadbeef"`) {
		t.Fatalf("missing sotc_token in [proxies.metadatas]; got:\n%s", out)
	}
}

func TestRenderFrpcTOML_TokenInOutput(t *testing.T) {
	t.Parallel()
	cfg := DefaultDaemonConfig()
	cfg.ServerAddr = "h"
	cfg.ProxyName = "p"
	token := "sotc_secrettoken_xyz"
	out := RenderFrpcTOML(cfg, token)
	if !strings.Contains(out, token) {
		t.Errorf("token must be embedded in rendered TOML (caller writes it 0600)")
	}
}

// --------------------------------------------------------------------------
// WriteRenderedConfig
// --------------------------------------------------------------------------

func TestWriteRenderedConfig_Mode0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := DefaultDaemonConfig()
	cfg.ServerAddr = "staging.ota-pulse.com"
	cfg.ProxyName = "rpi4-test"
	cfg.RenderedConfigPath = filepath.Join(dir, "frpc.toml")

	content := RenderFrpcTOML(cfg, "sotc_filemode_test")
	if err := WriteRenderedConfig(cfg, content); err != nil {
		t.Fatalf("WriteRenderedConfig: %v", err)
	}

	info, err := os.Stat(cfg.RenderedConfigPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("file mode: got %04o, want 0600", got)
	}
}

func TestWriteRenderedConfig_CreatesParentDir(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	cfg := DefaultDaemonConfig()
	cfg.ServerAddr = "staging.ota-pulse.com"
	cfg.RenderedConfigPath = filepath.Join(base, "nested", "dir", "frpc.toml")

	if err := WriteRenderedConfig(cfg, "content"); err != nil {
		t.Fatalf("WriteRenderedConfig: %v", err)
	}

	if _, err := os.Stat(cfg.RenderedConfigPath); err != nil {
		t.Fatalf("rendered file not created: %v", err)
	}
}

func TestWriteRenderedConfig_Atomic_OverwriteExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := DefaultDaemonConfig()
	cfg.ServerAddr = "staging.ota-pulse.com"
	cfg.ProxyName = "x"
	cfg.RenderedConfigPath = filepath.Join(dir, "frpc.toml")

	first := RenderFrpcTOML(cfg, "sotc_first")
	if err := WriteRenderedConfig(cfg, first); err != nil {
		t.Fatalf("first write: %v", err)
	}

	second := RenderFrpcTOML(cfg, "sotc_second")
	if err := WriteRenderedConfig(cfg, second); err != nil {
		t.Fatalf("second write: %v", err)
	}

	data, err := ioutil.ReadFile(cfg.RenderedConfigPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "sotc_second") {
		t.Error("expected second token in overwritten file")
	}
	if strings.Contains(string(data), "sotc_first") {
		t.Error("first token should not appear after overwrite")
	}
}

func TestWriteRenderedConfig_ContentRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := DefaultDaemonConfig()
	cfg.ServerAddr = "staging.ota-pulse.com"
	cfg.ProxyName = "rpi4-rt"
	cfg.RemotePortHint = 9022
	cfg.RenderedConfigPath = filepath.Join(dir, "frpc.toml")

	token := "sotc_roundtrip123"
	content := RenderFrpcTOML(cfg, token)
	if err := WriteRenderedConfig(cfg, content); err != nil {
		t.Fatalf("WriteRenderedConfig: %v", err)
	}

	data, err := ioutil.ReadFile(cfg.RenderedConfigPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != content {
		t.Errorf("content mismatch\nwant:\n%s\ngot:\n%s", content, string(data))
	}
}

// --------------------------------------------------------------------------
// DefaultRenderedConfigPath constant
// --------------------------------------------------------------------------

func TestDefaultRenderedConfigPath_UnderData(t *testing.T) {
	t.Parallel()
	if !strings.HasPrefix(DefaultRenderedConfigPath, "/data/") {
		t.Errorf("DefaultRenderedConfigPath must be under /data/, got %q", DefaultRenderedConfigPath)
	}
}

// --------------------------------------------------------------------------
// JSON serialisation round-trip (DaemonConfig is json-decodable)
// --------------------------------------------------------------------------

func TestDaemonConfig_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	orig := DaemonConfig{
		ServerAddr:         "srv.example.com",
		ServerPort:         7002,
		LocalSSHPort:       2222,
		ProxyName:          "dev-001",
		FrpcPath:           "/usr/bin/frpc",
		CredentialPath:     "/data/frp/credential",
		RenderedConfigPath: "/data/frp/frpc.toml",
		RemotePortHint:     9022,
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got DaemonConfig
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != orig {
		t.Errorf("JSON round-trip mismatch\norig: %+v\ngot:  %+v", orig, got)
	}
}
