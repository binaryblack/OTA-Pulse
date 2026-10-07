package installer

// Golden artifact vectors (TODO-011 / TASK-S102-003). Every file under
// testdata/golden must be accepted by ReadHeaders regardless of the `version`
// member's format identifier ("mender" or "otapulse"): the agent never looks
// at the identifier on the install path. See testdata/golden/README.md.

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/binaryblack/OTA-Pulse/conf"
)

const goldenDir = "../testdata/golden"

var goldenKinds = map[string]struct {
	name        string
	payloadType string
}{
	// The built-in rootfs handler is the fDevice stub here; its GetType is
	// whatever the stub reports.
	"rootfs":       {"golden-rootfs", new(fDevice).GetType()},
	"module":       {"golden-module", "single-file"},
	"rootfs-delta": {"golden-delta", DeltaRootfsPayloadType},
}

func goldenKey(t *testing.T, name string) []*conf.VerificationKey {
	data, err := os.ReadFile(filepath.Join(goldenDir, name))
	require.NoError(t, err)
	return []*conf.VerificationKey{{Path: name, Data: data}}
}

type goldenInfo struct{}

func (goldenInfo) GetCurrentArtifactName() (string, error)  { return "golden-current", nil }
func (goldenInfo) GetCurrentArtifactGroup() (string, error) { return "", nil }
func (goldenInfo) GetDeviceType() (string, error)           { return "golden-device", nil }

// goldenInst serves rootfs-image via the built-in handler and the other two
// payload types via stub Update Modules (ReadHeaders only parses and verifies
// here; the delta/module install semantics are covered by their own tests).
func goldenInst(t *testing.T) *AllModules {
	modDir := t.TempDir()
	for _, typ := range []string{"single-file", DeltaRootfsPayloadType} {
		require.NoError(t, os.WriteFile(filepath.Join(modDir, typ),
			[]byte("#!/bin/sh\nexit 0\n"), 0755))
	}
	return &AllModules{
		DualRootfs: new(fDevice),
		Modules:    NewModuleInstallerFactory(modDir, t.TempDir(), goldenInfo{}, goldenInfo{}, 0),
	}
}

func goldenVersionMember(t *testing.T, path string) string {
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	tr := tar.NewReader(f)
	hdr, err := tr.Next()
	require.NoError(t, err)
	require.Equal(t, "version", hdr.Name, "version must stay the first tar member")
	b, err := io.ReadAll(tr)
	require.NoError(t, err)
	return string(b)
}

func goldenVectors(t *testing.T) []string {
	files, err := filepath.Glob(filepath.Join(goldenDir, "*.otapulse"))
	require.NoError(t, err)
	require.Len(t, files, 12, "expected 3 kinds x 2 format ids x signed/unsigned")
	return files
}

func TestGoldenVectorsAcceptedByReadHeaders(t *testing.T) {
	t.Cleanup(ResetSignatureLockdown)
	pub := goldenKey(t, "test-public.pem")

	for _, path := range goldenVectors(t) {
		base := filepath.Base(path)
		t.Run(base, func(t *testing.T) {
			var kind, format string
			for k := range goldenKinds {
				if strings.HasPrefix(base, k+"-") && len(k) > len(kind) {
					kind = k
				}
			}
			require.NotEmpty(t, kind)
			switch {
			case strings.Contains(base, "-otapulse-"):
				format = "otapulse"
			case strings.Contains(base, "-mender-"):
				format = "mender"
			default:
				t.Fatalf("unrecognised vector name %s", base)
			}
			signed := strings.HasSuffix(base, "-signed.otapulse")

			// The vector itself must carry the identifier its name claims.
			assert.JSONEq(t, `{"format":"`+format+`","version":3}`,
				goldenVersionMember(t, path))

			var keys []*conf.VerificationKey
			if signed {
				keys = pub
			}
			art, err := os.Open(path)
			require.NoError(t, err)
			defer art.Close()

			inst, payloads, err := ReadHeaders(art, "golden-device", keys,
				t.TempDir(), goldenInst(t))
			require.NoError(t, err)
			assert.Equal(t, goldenKinds[kind].name, inst.GetArtifactName())
			assert.Equal(t, []string{"golden-device"}, inst.GetCompatibleDevices())
			require.Len(t, payloads, 1)
			assert.Equal(t, goldenKinds[kind].payloadType, payloads[0].GetType())
		})
	}
}

func TestGoldenVectorsSignatureEnforcement(t *testing.T) {
	t.Cleanup(ResetSignatureLockdown)
	right := goldenKey(t, "test-public.pem")
	wrong := goldenKey(t, "wrong-public.pem")

	for _, path := range goldenVectors(t) {
		base := filepath.Base(path)
		signed := strings.HasSuffix(base, "-signed.otapulse")
		t.Run(base, func(t *testing.T) {
			t.Cleanup(ResetSignatureLockdown)
			read := func(keys []*conf.VerificationKey) error {
				art, err := os.Open(path)
				require.NoError(t, err)
				defer art.Close()
				_, _, err = ReadHeaders(art, "golden-device", keys, t.TempDir(), goldenInst(t))
				return err
			}

			if signed {
				// Wrong key: rejected, whatever the format identifier.
				err := read(wrong)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "failed to verify")
				// No key configured: documented MEN-1196 behaviour, accepted.
				assert.NoError(t, read(nil))
			} else {
				// A key is configured but the artifact carries no signature:
				// must be refused (fail closed).
				assert.Error(t, read(right))
			}
		})
	}
}

// A device-type mismatch is still refused for the otapulse identifier
// (the identifier must not weaken any other check).
func TestGoldenVectorsDeviceTypeStillEnforced(t *testing.T) {
	t.Cleanup(ResetSignatureLockdown)
	art, err := os.Open(filepath.Join(goldenDir, "rootfs-otapulse-unsigned.otapulse"))
	require.NoError(t, err)
	defer art.Close()
	_, _, err = ReadHeaders(art, "some-other-board", nil, t.TempDir(), goldenInst(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not compatible")
}
