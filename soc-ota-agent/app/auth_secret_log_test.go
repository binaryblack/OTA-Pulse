// GC-AG-06 / TASK-S108-039: building an auth request at debug level must not
// log the tenant token.
package app

import (
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dev "github.com/binaryblack/OTA-Pulse/device"
	"github.com/binaryblack/OTA-Pulse/store"
	stest "github.com/binaryblack/OTA-Pulse/system/testing"
)

func TestMakeAuthRequest_doesNotLogTenantToken(t *testing.T) {
	const secret = "TENANT_TOKEN_SUPERSECRET_abcdef"

	oldLevel := log.GetLevel()
	log.SetLevel(log.TraceLevel)
	hook := logtest.NewGlobal()
	defer func() {
		log.SetLevel(oldLevel)
		hook.Reset()
		log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	}()

	ms := store.NewMemStore()
	am := NewAuthManager(AuthManagerConfig{
		AuthDataStore: ms,
		IdentitySource: dev.IdentityDataRunner{
			Cmdr: stest.NewTestOSCalls("mac=foobar", 0),
		},
		KeyStore:    store.NewKeystore(ms, "key", "", false, defaultKeyPassphrase),
		TenantToken: []byte(secret),
	})
	require.NotNil(t, am)
	require.NoError(t, am.GenerateKey())

	req, err := am.MakeAuthRequest()
	require.NoError(t, err)
	// The token must still be sent to the server...
	assert.Contains(t, string(req.Data), secret)
	assert.Equal(t, secret, string(req.Token))

	// ...but never written to a log line.
	require.NotEmpty(t, hook.AllEntries())
	for _, e := range hook.AllEntries() {
		line, lerr := e.String()
		require.NoError(t, lerr)
		assert.False(t, strings.Contains(line, secret), "tenant token leaked in log line: %q", line)
	}
}
