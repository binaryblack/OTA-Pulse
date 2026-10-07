package conf

import (
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBootstrapArtifactPathPrefersOTAPulseName(t *testing.T) {
	dir := t.TempDir()
	otapulse := path.Join(dir, "bootstrap.otapulse")
	mender := path.Join(dir, "bootstrap.mender")

	// Neither present: the preferred (new) name.
	assert.Equal(t, otapulse, BootstrapArtifactPath(dir))

	// Only the legacy file: fall back to it.
	require.NoError(t, os.WriteFile(mender, []byte("x"), 0600))
	assert.Equal(t, mender, BootstrapArtifactPath(dir))

	// Both: the new name wins.
	require.NoError(t, os.WriteFile(otapulse, []byte("x"), 0600))
	assert.Equal(t, otapulse, BootstrapArtifactPath(dir))

	// Only the new file.
	require.NoError(t, os.Remove(mender))
	assert.Equal(t, otapulse, BootstrapArtifactPath(dir))
}
