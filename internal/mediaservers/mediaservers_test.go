package mediaservers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileIsEmptyNotAnError(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatalf("want empty store, got %v", s.List())
	}
}

func TestAddListRemoveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mediaservers.json")
	s, _ := Load(path)
	if err := s.Add(Server{ID: "fa095ecc-uuid", Name: "AVM FRITZ!Mediaserver"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.Add(Server{ID: "00113251-uuid", Name: "Backupserver"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if !s.Has("fa095ecc-uuid") {
		t.Error("Has must find an added server")
	}

	// Reload from disk: the whole point of the store.
	again, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := again.List()
	if len(got) != 2 {
		t.Fatalf("want 2 servers after reload, got %d: %v", len(got), got)
	}
	// Ordered by name, so "AVM ..." sorts before "Backupserver".
	if got[0].Name != "AVM FRITZ!Mediaserver" || got[1].Name != "Backupserver" {
		t.Errorf("List must be ordered by name, got %v", got)
	}

	if err := again.Remove("fa095ecc-uuid"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	final, _ := Load(path)
	if len(final.List()) != 1 || final.Has("fa095ecc-uuid") {
		t.Errorf("removal did not persist: %v", final.List())
	}
}

// A repeat enable/disable must not rewrite the file. NAND writes on these boxes
// are the scarce resource, and the UI can re-send the same state freely.
func TestIdempotentOpsDoNotRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mediaservers.json")
	s, _ := Load(path)
	if err := s.Add(Server{ID: "a", Name: "A"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Remove the file: if a no-op write happened, it would come back.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if err := s.Add(Server{ID: "a", Name: "A"}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if err := s.Remove("does-not-exist"); err != nil {
		t.Fatalf("remove absent: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("an unchanged Add/Remove must not write the file again")
	}
	_ = st

	// A changed name IS a change and must persist.
	if err := s.Add(Server{ID: "a", Name: "A renamed"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	back, _ := Load(path)
	if got := back.List(); len(got) != 1 || got[0].Name != "A renamed" {
		t.Errorf("rename must persist, got %v", got)
	}
}

func TestAddRejectsEmptyID(t *testing.T) {
	s := New()
	if err := s.Add(Server{Name: "no id"}); err == nil {
		t.Error("an entry without an id must be rejected")
	}
}
