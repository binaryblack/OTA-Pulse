// Copyright 2026 SoC Monitoring
package statescript

import (
	"archive/tar"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GAP-SEC-F7: statescript/executor.go runs scripts unpacked from the OTA
// artifact as root, checking only the exec bit — authenticity depends
// entirely on F1's signature enforcement, but a second, independent layer
// must also stop a script name from escaping the scripts directory via a
// crafted tar entry, regardless of signature status.
// statescript_test.go's TestStoreScriptRejectsUnsafeNames already covers
// StoreScript's direct name-rejection behavior; the tests below cover the
// two things it doesn't: the real tar/areader interaction that decides what
// name StoreScript ever actually receives, and StoreScript/Clear behaviors
// that name-rejection alone doesn't exercise.
//
// Traced every real call site: installer.ReadHeaders registers
// ar.ScriptsReadCallback = func(r, fi) { return scr.StoreScript(r, fi.Name()) }
// (installer/installer.go), and the vendored mender-artifact areader only
// invokes that callback when filepath.Dir(hdr.Name) == "scripts" exactly
// (areader/reader.go readStateScripts). hdr.FileInfo().Name() is Go's own
// archive/tar — always path.Base(hdr.Name), so StoreScript can never receive
// a name containing "/". That still leaves one non-obvious case (see
// TestTarFileInfoName_ScriptsDotDot_StripsToDotDot below): a crafted tar
// entry literally named "scripts/.." passes the outer Dir()=="scripts" gate
// AND yields a stripped name of exactly "..". Nothing outside StoreScript's
// own strings.Contains(name, "..") check stops that case — it is not
// defense-in-depth theater, it is load-bearing. artifact.Scripts.Add (the
// real artifact-writer API) can't be used to construct this: it enforces a
// strict ArtifactInstall_Enter_05-style name regex and derives the tar entry
// name from a real local file's basename, so this shape only reaches the
// agent via a hand-crafted artifact that bypasses the SDK — exactly the
// threat model that matters here.

// TestTarFileInfoName_ScriptsDotDot_StripsToDotDot pins the exact real-world
// behavior installer.go's ScriptsReadCallback depends on. If this test ever
// fails, StoreScript's strings.Contains(name, "..") check is the only thing
// standing between a crafted artifact and a path-traversal write, and that
// fact needs to be re-verified, not assumed.
func TestTarFileInfoName_ScriptsDotDot_StripsToDotDot(t *testing.T) {
	hdr := &tar.Header{Name: "scripts/..", Typeflag: tar.TypeReg, Mode: 0755}

	require.Equal(t, "scripts", filepath.Dir(hdr.Name),
		"the areader library's own gate treats this entry as belonging to scripts/")
	require.Equal(t, "..", hdr.FileInfo().Name(),
		"and hands StoreScript exactly the string \"..\" as the script name")

	// Confirm StoreScript really does reject exactly that string end to end.
	dir := t.TempDir()
	s := NewStore(dir)
	err := s.StoreScript(strings.NewReader("payload"), hdr.FileInfo().Name())
	require.Error(t, err)
}

func TestStoreScript_RejectsDuplicateWrite(t *testing.T) {
	// O_EXCL means a second write to the same name must fail rather than
	// silently overwrite — relevant if a crafted artifact repeats a script
	// name to probe for TOCTOU.
	dir := t.TempDir()
	s := NewStore(dir)

	require.NoError(t, s.StoreScript(strings.NewReader("first"), "ArtifactCommit_Enter_01"))
	err := s.StoreScript(strings.NewReader("second"), "ArtifactCommit_Enter_01")
	assert.Error(t, err)
}

func TestClear_RejectsRootAndRelativePaths(t *testing.T) {
	assert.Error(t, (&Store{location: "/"}).Clear())
	assert.Error(t, (&Store{location: "relative/path"}).Clear())

	dir := t.TempDir()
	assert.NoError(t, (&Store{location: filepath.Join(dir, "scripts")}).Clear())
}
