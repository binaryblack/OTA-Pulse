// GC-AG-06 / TASK-S108-039: the SoC Monitoring client must not log secrets
// (API key request header, response Set-Cookie) at any level, and must not
// dump response headers at INFO.
package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSoCClientDo_doesNotLogSecretsOrHeadersAtInfo(t *testing.T) {
	const apiKey = "spt_SUPERSECRET_API_KEY_0123456789"
	const cookie = "session=SUPERSECRET_COOKIE_VALUE"

	oldLevel := log.GetLevel()
	log.SetLevel(log.TraceLevel)
	hook := logtest.NewGlobal()
	defer func() {
		log.SetLevel(oldLevel)
		hook.Reset()
		log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	}()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", cookie)
		w.Header().Set("X-Echo-Api-Key", r.Header.Get("X-API-Key"))
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newSoCTestClient(t, ts.URL, apiKey, nil)
	resp, err := c.Do(newSoCGetRequest(t))
	require.NoError(t, err)
	resp.Body.Close()

	require.NotEmpty(t, hook.AllEntries())
	for _, e := range hook.AllEntries() {
		line, lerr := e.String()
		require.NoError(t, lerr)
		assert.False(t, strings.Contains(line, apiKey), "API key leaked in log line: %q", line)
		assert.False(t, strings.Contains(line, "SUPERSECRET_COOKIE_VALUE"), "cookie leaked in log line: %q", line)
		if e.Level <= log.InfoLevel {
			assert.NotContains(t, e.Message, "headers", "response headers must not be dumped at INFO or above")
		}
	}
}
