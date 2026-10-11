package installer

// GC-AG-07 / NEW-AG-010: end-to-end policy test for the artifact
// authentication decision. It drives the SAME two steps the daemon and the
// standalone installer run (conf.MenderConfig.GetVerificationKeys, then
// ReadHeaders with whatever keys came back) over every combination of
//
//	(keys configured, RequireArtifactVerification, AllowUnsignedArtifacts)
//
// against a real signed and a real unsigned golden artifact, and asserts
// install vs refuse. The decision table lives on GetVerificationKeys.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/binaryblack/OTA-Pulse/conf"
)

// installAttempt mirrors device.ReadArtifactHeaders / app.DoStandaloneInstall:
// resolve keys from config first, and only read the artifact when that
// succeeds.
func installAttempt(t *testing.T, cfg *conf.MenderConfig, artifactFile string) error {
	t.Helper()
	keys, err := cfg.GetVerificationKeys()
	if err != nil {
		return err
	}
	art, err := os.Open(filepath.Join(goldenDir, artifactFile))
	require.NoError(t, err)
	defer art.Close()
	_, _, err = ReadHeaders(art, "golden-device", keys, t.TempDir(), goldenInst(t))
	return err
}

func TestUnsignedArtifactPolicyMatrix(t *testing.T) {
	t.Cleanup(ResetSignatureLockdown)
	right := filepath.Join(goldenDir, "test-public.pem")
	wrong := filepath.Join(goldenDir, "wrong-public.pem")

	const signed = "rootfs-otapulse-signed.otapulse"
	const unsigned = "rootfs-otapulse-unsigned.otapulse"

	type result struct{ signed, unsigned string } // "ok" | "refused" | "rejected"
	cases := []struct {
		name           string
		keyPath        string // "" = no key configured
		require, allow bool
		want           result
	}{
		// Right key configured: verify. Signed installs, unsigned is rejected,
		// whatever Require/Allow say (Allow never downgrades a configured key).
		{"rightkey/req0/allow0", right, false, false, result{"ok", "rejected"}},
		{"rightkey/req1/allow0", right, true, false, result{"ok", "rejected"}},
		{"rightkey/req0/allow1", right, false, true, result{"ok", "rejected"}},
		{"rightkey/req1/allow1", right, true, true, result{"ok", "rejected"}},
		// Wrong key configured: a signed artifact fails verification even when
		// the operator allowed unsigned installs.
		{"wrongkey/req0/allow1", wrong, false, true, result{"rejected", "rejected"}},
		{"wrongkey/req0/allow0", wrong, false, false, result{"rejected", "rejected"}},
		// No key: refused outright unless explicitly allowed; Require beats Allow.
		{"nokey/req0/allow0", "", false, false, result{"refused", "refused"}},
		{"nokey/req1/allow0", "", true, false, result{"refused", "refused"}},
		{"nokey/req1/allow1", "", true, true, result{"refused", "refused"}},
		{"nokey/req0/allow1", "", false, true, result{"ok", "ok"}},
	}

	classify := func(err error) string {
		switch {
		case err == nil:
			return "ok"
		case isConfigRefusal(err):
			return "refused"
		default:
			return "rejected"
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(ResetSignatureLockdown)
			mk := func() *conf.MenderConfig {
				c := &conf.MenderConfig{}
				c.RequireArtifactVerification = tc.require
				c.AllowUnsignedArtifacts = tc.allow
				if tc.keyPath != "" {
					c.ArtifactVerifyKeys = []string{tc.keyPath}
				}
				return c
			}
			assert.Equal(t, tc.want.signed, classify(installAttempt(t, mk(), signed)), "signed artifact")
			ResetSignatureLockdown()
			assert.Equal(t, tc.want.unsigned, classify(installAttempt(t, mk(), unsigned)), "unsigned artifact")
		})
	}
}

// The refusal for the new default must be the clear, actionable config error,
// not a downstream artifact error.
func TestUnsignedArtifactPolicyRefusalMessage(t *testing.T) {
	err := installAttempt(t, &conf.MenderConfig{}, "rootfs-otapulse-signed.otapulse")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AllowUnsignedArtifacts")
	assert.Contains(t, err.Error(), "refusing to install")
}

func isConfigRefusal(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "AllowUnsignedArtifacts is not set") ||
		strings.Contains(err.Error(), "RequireArtifactVerification is set"))
}
