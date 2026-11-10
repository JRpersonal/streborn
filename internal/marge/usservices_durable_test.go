package marge

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A registered account is stored at 0600 (it holds the credential), without a
// temp file left behind, and a save that fails keeps the account that was
// stored before.
func TestSaveNativeContentModeAndFailedWriteKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "pandora-source.json")
	s := New(testStoredMusicLogger(), WithPandora(filepath.Join(t.TempDir(), "optin"), path))

	first := nativeRecord{Username: "listener@example.com", CredentialType: "token", Credential: "secret"}
	if err := s.saveNative("PANDORA", first); err != nil {
		t.Fatal(err)
	}
	got, ok := s.loadNative("PANDORA")
	if !ok || got.Username != first.Username || got.Credential != first.Credential {
		t.Fatalf("loadNative = %+v, %v", got, ok)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Occupy the temp sibling with a directory so the write cannot start.
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.saveNative("PANDORA", nativeRecord{Username: "other@example.com"}); err == nil {
		t.Fatal("blocked save reported success")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("failed save changed the file: %q, want %q", after, before)
	}
}
