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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --------------------------------------------------------------------------
// Table-driven round-trip tests
// --------------------------------------------------------------------------

func TestSaveAndLoadCredential_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		token string
	}{
		{"simple sotc token", "sotc_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{"token with underscores", "sotc_abcdef_1234"},
		{"short token", "sotc_x"},
		{"token no newline", "sotc_no_trailing_newline"},
	}

	for _, tc := range tests {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "credential")

			if err := SaveCredential(path, tc.token); err != nil {
				t.Fatalf("SaveCredential: %v", err)
			}
			got, err := LoadCredential(path)
			if err != nil {
				t.Fatalf("LoadCredential: %v", err)
			}
			if got != tc.token {
				t.Errorf("round-trip: got %q, want %q", got, tc.token)
			}
		})
	}
}

// --------------------------------------------------------------------------
// File mode
// --------------------------------------------------------------------------

func TestSaveCredential_FileMode0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "credential")

	if err := SaveCredential(path, "sotc_test_token"); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	got := info.Mode().Perm()
	if got != 0600 {
		t.Errorf("file mode: got %04o, want 0600", got)
	}
}

// --------------------------------------------------------------------------
// Atomic overwrite (rotate)
// --------------------------------------------------------------------------

func TestSaveCredential_AtomicOverwrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "credential")

	tokenA := "sotc_aaaaaaaaaaaaaaaa"
	tokenB := "sotc_bbbbbbbbbbbbbbbb"

	if err := SaveCredential(path, tokenA); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := SaveCredential(path, tokenB); err != nil {
		t.Fatalf("second save: %v", err)
	}

	got, err := LoadCredential(path)
	if err != nil {
		t.Fatalf("LoadCredential after rotate: %v", err)
	}
	if got != tokenB {
		t.Errorf("after rotate: got %q, want %q", got, tokenB)
	}
}

// --------------------------------------------------------------------------
// Error: missing file
// --------------------------------------------------------------------------

func TestLoadCredential_MissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "does_not_exist")

	_, err := LoadCredential(path)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist in error chain, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// Error: empty file
// --------------------------------------------------------------------------

func TestLoadCredential_EmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "empty")

	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := LoadCredential(path)
	if err == nil {
		t.Fatal("expected error for empty file, got nil")
	}
}

// --------------------------------------------------------------------------
// Trailing newline trimming
// --------------------------------------------------------------------------

func TestLoadCredential_TrimsTrailingNewline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "credential")

	token := "sotc_trimtest"
	// Write with various trailing whitespace.
	for _, suffix := range []string{"\n", "\r\n", "  \n", "\n\n"} {
		if err := os.WriteFile(path, []byte(token+suffix), 0600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got, err := LoadCredential(path)
		if err != nil {
			t.Fatalf("LoadCredential with suffix %q: %v", suffix, err)
		}
		if got != token {
			t.Errorf("suffix %q: got %q, want %q", suffix, got, token)
		}
	}
}

// --------------------------------------------------------------------------
// Parent directory creation
// --------------------------------------------------------------------------

func TestSaveCredential_CreatesParentDir(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	// Nested path that does not exist yet.
	path := filepath.Join(base, "sub", "dir", "credential")

	if err := SaveCredential(path, "sotc_mkdir_test"); err != nil {
		t.Fatalf("SaveCredential with nested path: %v", err)
	}

	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("parent directory not created: %v", err)
	}
	got, err := LoadCredential(path)
	if err != nil {
		t.Fatalf("LoadCredential after mkdir: %v", err)
	}
	if got != "sotc_mkdir_test" {
		t.Errorf("got %q, want %q", got, "sotc_mkdir_test")
	}
}

// --------------------------------------------------------------------------
// Parent directory mode
// --------------------------------------------------------------------------

func TestSaveCredential_ParentDirMode0700(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	dir := filepath.Join(base, "frp")
	path := filepath.Join(dir, "credential")

	if err := SaveCredential(path, "sotc_dirmode"); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	got := info.Mode().Perm()
	if got != 0700 {
		t.Errorf("parent dir mode: got %04o, want 0700", got)
	}
}

// --------------------------------------------------------------------------
// CredentialAuthLines helper
// --------------------------------------------------------------------------

func TestCredentialAuthLines(t *testing.T) {
	t.Parallel()

	token := "sotc_abc123"
	lines := CredentialAuthLines(token)

	if !strings.Contains(lines, `method = "token"`) {
		t.Errorf("expected 'method = \"token\"' in output, got:\n%s", lines)
	}
	if !strings.Contains(lines, token) {
		t.Errorf("expected token %q in output, got:\n%s", token, lines)
	}
	if !strings.Contains(lines, "[auth]") {
		t.Errorf("expected '[auth]' section header, got:\n%s", lines)
	}
}

// --------------------------------------------------------------------------
// DefaultCredentialPath constant sanity check
// --------------------------------------------------------------------------

func TestDefaultCredentialPath(t *testing.T) {
	t.Parallel()
	// Must be rooted under /data.
	if !strings.HasPrefix(DefaultCredentialPath, "/data/") {
		t.Errorf("DefaultCredentialPath must be under /data/, got %q", DefaultCredentialPath)
	}
}
