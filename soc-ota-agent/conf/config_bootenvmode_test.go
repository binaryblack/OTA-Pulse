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
package conf

import (
	"encoding/json"
	"io/ioutil"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConf(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, ioutil.WriteFile(p, []byte(content), 0600))
	return p
}

func TestBootEnvMode_DefaultIsEmptyAndValid(t *testing.T) {
	cfg, err := LoadConfig(writeConf(t, "main.conf", `{"RootfsPartA":"/a","RootfsPartB":"/b"}`),
		"does-not-exist")
	require.NoError(t, err)
	assert.Equal(t, "", cfg.BootEnvMode)
	assert.NoError(t, cfg.ValidateBootEnvMode())
	assert.NoError(t, cfg.Validate())
}

func TestBootEnvMode_ValidateTable(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		useFile bool
		wantErr string
	}{
		{"empty", "", false, ""},
		{"empty with UseFileBasedBootEnv", "", true, ""},
		{"file", "file", false, ""},
		{"file with UseFileBasedBootEnv", "file", true, ""},
		{"bootloader", "bootloader", false, ""},
		{"bootloader contradicts UseFileBasedBootEnv", "bootloader", true, "contradicts"},
		{"capitalised", "Bootloader", false, "invalid BootEnvMode"},
		{"upper", "BOOTLOADER", false, "invalid BootEnvMode"},
		{"grub", "grub", false, "invalid BootEnvMode"},
		{"padded", " bootloader", false, "invalid BootEnvMode"},
		{"file typo", "File", false, "invalid BootEnvMode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := NewMenderConfig()
			cfg.BootEnvMode = c.mode
			cfg.UseFileBasedBootEnv = c.useFile
			err := cfg.ValidateBootEnvMode()
			verr := cfg.Validate()
			if c.wantErr == "" {
				assert.NoError(t, err)
				assert.NoError(t, verr)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.wantErr)
			// Validate() (run for every CLI command except setup) must
			// enforce it too.
			require.Error(t, verr)
			assert.Contains(t, verr.Error(), c.wantErr)
		})
	}
}

// The field is read from the config file and follows the same fallback/main
// merge rule as every other field.
func TestBootEnvMode_LoadAndMerge(t *testing.T) {
	main := writeConf(t, "main.conf", `{"BootEnvMode":"bootloader"}`)
	fallback := writeConf(t, "fallback.conf", `{"BootEnvMode":"file","RootfsPartA":"/a"}`)

	// main overrides fallback
	cfg, err := LoadConfig(main, fallback)
	require.NoError(t, err)
	assert.Equal(t, "bootloader", cfg.BootEnvMode)
	assert.Equal(t, "/a", cfg.RootfsPartA)

	// only in fallback: used
	empty := writeConf(t, "empty.conf", `{}`)
	cfg, err = LoadConfig(empty, fallback)
	require.NoError(t, err)
	assert.Equal(t, "file", cfg.BootEnvMode)

	// only in main, no fallback file
	cfg, err = LoadConfig(main, "does-not-exist")
	require.NoError(t, err)
	assert.Equal(t, "bootloader", cfg.BootEnvMode)
}

// An unrecognised value is rejected when the config is validated, whichever
// file it came from, and a later file can correct it.
func TestBootEnvMode_InvalidFromFileIsRejected(t *testing.T) {
	bad := writeConf(t, "bad.conf", `{"BootEnvMode":"Bootloader","ServerURL":"http://x"}`)
	cfg, err := LoadConfig(bad, "does-not-exist")
	require.NoError(t, err) // loading is not the gate: setup must still be able to fix it
	require.Error(t, cfg.Validate())

	good := writeConf(t, "good.conf", `{"BootEnvMode":"bootloader"}`)
	cfg, err = LoadConfig(good, bad) // main wins
	require.NoError(t, err)
	assert.NoError(t, cfg.Validate())
}

// Key spellings: the exact key, and Go's case-insensitive JSON key matching
// (the same behaviour every other field of this struct has, e.g. a lower-case
// "usefilebasedbootenv").
func TestBootEnvMode_JSONKeySpellings(t *testing.T) {
	for _, key := range []string{"BootEnvMode", "bootenvmode", "BOOTENVMODE"} {
		cfg, err := LoadConfig(writeConf(t, "main.conf", `{"`+key+`":"bootloader"}`), "does-not-exist")
		require.NoError(t, err, key)
		assert.Equal(t, "bootloader", cfg.BootEnvMode, key)
	}
}

func TestBootEnvMode_JSONRoundTripAndOmitEmpty(t *testing.T) {
	in := MenderConfigFromFile{BootEnvMode: "bootloader", RootfsPartA: "/a"}
	data, err := json.Marshal(&in)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"BootEnvMode":"bootloader"`)

	var out MenderConfigFromFile
	require.NoError(t, json.Unmarshal(data, &out))
	assert.Equal(t, "bootloader", out.BootEnvMode)

	// Absent stays absent: configs written by an older agent / by setup
	// do not gain a BootEnvMode key.
	data, err = json.Marshal(&MenderConfigFromFile{RootfsPartA: "/a"})
	require.NoError(t, err)
	assert.NotContains(t, string(data), "BootEnvMode")

	// SaveConfigFile -> LoadConfig round trip
	p := filepath.Join(t.TempDir(), "saved.conf")
	require.NoError(t, SaveConfigFile(&in, p))
	cfg, err := LoadConfig(p, "does-not-exist")
	require.NoError(t, err)
	assert.Equal(t, "bootloader", cfg.BootEnvMode)
}
