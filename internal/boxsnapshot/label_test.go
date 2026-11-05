package boxsnapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLabelPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box-label.json")

	s := OpenLabel(path)
	if got := s.Get(); got != (Label{}) {
		t.Fatalf("a missing file must read empty, got %+v", got)
	}
	changed, err := s.Note("Kitchen", "SoundTouch Wave")
	if err != nil || !changed {
		t.Fatalf("first Note = (%v, %v), want a write", changed, err)
	}

	again := OpenLabel(path)
	if got := again.Get(); got != (Label{Name: "Kitchen", Model: "SoundTouch Wave"}) {
		t.Fatalf("after restart label = %+v", got)
	}
}

// The poll feeds this every 30 seconds; only a real change may touch NAND.
func TestLabelWritesOnlyOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box-label.json")
	s := OpenLabel(path)
	if _, err := s.Note("Kitchen", "SoundTouch 10"); err != nil {
		t.Fatal(err)
	}
	// Remove the file: an unchanged Note must not recreate it.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.Note("Kitchen", "SoundTouch 10"); changed || err != nil {
		t.Fatalf("an unchanged Note = (%v, %v), want no write", changed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an unchanged Note wrote to disk")
	}
	// Empty fields mean "not reported" and the generic model is no knowledge.
	if changed, _ := s.Note("", "SoundTouch"); changed {
		t.Fatal("empty name and generic model must not count as a change")
	}
	if got := s.Get(); got != (Label{Name: "Kitchen", Model: "SoundTouch 10"}) {
		t.Fatalf("label = %+v, want the known values kept", got)
	}
	// A rename is a change.
	if changed, err := s.Note("Living room", ""); !changed || err != nil {
		t.Fatalf("a rename = (%v, %v), want a write", changed, err)
	}
	if got := OpenLabel(path).Get(); got != (Label{Name: "Living room", Model: "SoundTouch 10"}) {
		t.Fatalf("stored label = %+v", got)
	}
}

func TestLabelRejectsUnusableInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box-label.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := OpenLabel(path)
	if got := s.Get(); got != (Label{}) {
		t.Fatalf("a corrupt file must read empty, got %+v", got)
	}
	// A Latin-1 name would come back from JSON altered and be rewritten every
	// boot; it is not stored.
	if changed, _ := s.Note("K\xfcche", ""); changed {
		t.Fatal("a name that is not valid UTF-8 must not be stored")
	}

	var nilStore *LabelStore
	if got := nilStore.Get(); got != (Label{}) {
		t.Fatal("a nil store must read empty")
	}
	if changed, err := nilStore.Note("x", "y"); changed || err != nil {
		t.Fatal("a nil store must not write")
	}
}
