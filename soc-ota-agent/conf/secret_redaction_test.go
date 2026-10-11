// GC-AG-06 / TASK-S108-039: the device API key and tenant token must never
// reach a log line, even with the log level raised to debug (the logs are
// uploaded to the server as deployment logs).
package conf

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	secretAPIKey      = "spt_SUPERSECRET_API_KEY_0123456789"
	secretTenantToken = "TENANT_TOKEN_SUPERSECRET_abcdef"
)

func captureAllLogs(t *testing.T) *logtest.Hook {
	t.Helper()
	oldLevel := log.GetLevel()
	log.SetLevel(log.TraceLevel)
	hook := logtest.NewGlobal()
	t.Cleanup(func() {
		log.SetLevel(oldLevel)
		hook.Reset()
		log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	})
	return hook
}

func assertNoSecretInLogs(t *testing.T, hook *logtest.Hook) {
	t.Helper()
	require.NotEmpty(t, hook.AllEntries(), "expected the code under test to log something")
	for _, e := range hook.AllEntries() {
		line, err := e.String()
		require.NoError(t, err)
		assert.False(t, strings.Contains(line, secretAPIKey), "API key leaked in log line: %q", line)
		assert.False(t, strings.Contains(line, secretTenantToken), "tenant token leaked in log line: %q", line)
	}
}

func Test_LoadConfigAndValidate_doNotLogSecrets(t *testing.T) {
	hook := captureAllLogs(t)

	path := filepath.Join(t.TempDir(), "otapulse.conf")
	body := `{"ServerURL":"https://ota.example.test","UseSoCMonitoring":true,` +
		`"SoCAPIKey":"` + secretAPIKey + `","SoCDeviceID":"dev-1",` +
		`"TenantToken":"` + secretTenantToken + `"}`
	require.NoError(t, ioutil.WriteFile(path, []byte(body), 0600))

	config, err := LoadConfig(path, filepath.Join(t.TempDir(), "missing.conf"))
	require.NoError(t, err)
	require.NoError(t, config.Validate())

	// The real values must still be usable by the rest of the agent.
	assert.Equal(t, secretAPIKey, config.SoCAPIKey)
	assert.Equal(t, secretTenantToken, config.TenantToken)

	assertNoSecretInLogs(t, hook)

	// The debug dump is still useful: non-secret fields stay visible.
	var sawURL bool
	for _, e := range hook.AllEntries() {
		if strings.Contains(e.Message, "ota.example.test") {
			sawURL = true
		}
	}
	assert.True(t, sawURL, "redaction must keep non-secret fields in the config dump")
}

func Test_redactedForLog_doesNotMutateOriginal(t *testing.T) {
	c := NewMenderConfig()
	c.SoCAPIKey = secretAPIKey
	c.TenantToken = secretTenantToken

	r := redactedForLog(c)

	assert.NotContains(t, r.SoCAPIKey, secretAPIKey)
	assert.NotContains(t, r.TenantToken, secretTenantToken)
	assert.Equal(t, secretAPIKey, c.SoCAPIKey)
	assert.Equal(t, secretTenantToken, c.TenantToken)
}

func Test_redactedForLog_emptySecretsStayEmpty(t *testing.T) {
	r := redactedForLog(NewMenderConfig())
	assert.Empty(t, r.SoCAPIKey)
	assert.Empty(t, r.TenantToken)
}

// GC-AG-14: SaveConfigFile must replace the file atomically (temp file in the
// same directory + fsync + rename), so a power cut can never leave a
// truncated/empty config.
func Test_SaveConfigFile_leavesNoTempFilesBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "otapulse.conf")
	require.NoError(t, ioutil.WriteFile(path, []byte(`{"ServerURL":"old"}`), 0600))

	require.NoError(t, SaveConfigFile(&MenderConfigFromFile{ServerURL: "new"}, path))

	entries, err := ioutil.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the config file should remain")
	assert.Equal(t, "otapulse.conf", entries[0].Name())
}

func Test_SaveConfigFile_replacesByRename(t *testing.T) {
	// A hard link to the old inode keeps the OLD content only if the save
	// replaced the directory entry (rename) instead of truncating in place.
	dir := t.TempDir()
	path := filepath.Join(dir, "otapulse.conf")
	keep := filepath.Join(dir, "old-inode-link")
	require.NoError(t, ioutil.WriteFile(path, []byte(`{"ServerURL":"old"}`), 0600))
	require.NoError(t, os.Link(path, keep))

	require.NoError(t, SaveConfigFile(&MenderConfigFromFile{ServerURL: "new"}, path))

	oldContent, err := ioutil.ReadFile(keep)
	require.NoError(t, err)
	assert.Equal(t, `{"ServerURL":"old"}`, string(oldContent))

	var out MenderConfigFromFile
	require.NoError(t, readConfigFile(&out, path))
	assert.Equal(t, "new", out.ServerURL)
}

func Test_SaveConfigFile_followsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.conf")
	link := filepath.Join(dir, "link.conf")
	require.NoError(t, ioutil.WriteFile(real, []byte(`{}`), 0600))
	require.NoError(t, os.Symlink(real, link))

	require.NoError(t, SaveConfigFile(&MenderConfigFromFile{ServerURL: "sym"}, link))

	fi, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, fi.Mode()&os.ModeSymlink, "the symlink itself must survive")
	var out MenderConfigFromFile
	require.NoError(t, readConfigFile(&out, real))
	assert.Equal(t, "sym", out.ServerURL)
}
