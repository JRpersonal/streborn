package keyactions

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestValidateAcceptsSleepOnThumbs(t *testing.T) {
	d := Document{Keys: map[string]string{"thumbsUp": " Sleep ", "thumbsDown": ""}}
	if err := d.Validate(nil); err != nil {
		t.Fatalf("valid document refused: %v", err)
	}
	if d.Keys["thumbsUp"] != ActionSleep {
		t.Errorf("action not normalised: %q", d.Keys["thumbsUp"])
	}
	if _, ok := d.Keys["thumbsDown"]; ok {
		t.Error("an empty entry should be dropped")
	}
}

// Back, forward and play/pause keep the firmware's own reaction, so a sleep
// action there would fire on top of a skip or a pause.
func TestValidateRejectsNonThumbsKeys(t *testing.T) {
	for _, key := range []string{"prev", "next", "playPause", "preset1", "power", "volUp", ""} {
		d := Document{Keys: map[string]string{key: ActionSleep}}
		if err := d.Validate(nil); err == nil {
			t.Errorf("key %q accepted, want refused", key)
		}
	}
}

func TestValidateRejectsUnknownAction(t *testing.T) {
	d := Document{Keys: map[string]string{"thumbsUp": "reboot"}}
	if err := d.Validate(nil); err == nil {
		t.Fatal("unknown action accepted")
	}
}

// A key carries a group OR the sleep timer, never both.
func TestValidateRejectsKeyThatCarriesAGroup(t *testing.T) {
	bound := func(k string) bool { return k == "thumbsUp" }
	d := Document{Keys: map[string]string{"thumbsUp": ActionSleep}}
	err := d.Validate(bound)
	if err == nil || !strings.Contains(err.Error(), "group") {
		t.Fatalf("conflict not refused with a group reason: %v", err)
	}
	d = Document{Keys: map[string]string{"thumbsDown": ActionSleep}}
	if err := d.Validate(bound); err != nil {
		t.Errorf("the other thumbs key is free, refused: %v", err)
	}
}

func TestStorePersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key-actions.json")
	s, err := Load(path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if s.Bound("thumbsUp") {
		t.Fatal("fresh store reports a binding")
	}
	if err := s.Set(Document{Keys: map[string]string{"thumbsUp": "sleep"}}, nil); err != nil {
		t.Fatal(err)
	}
	s2, err := Load(path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if s2.Action("thumbsUp") != ActionSleep {
		t.Errorf("reloaded action = %q", s2.Action("thumbsUp"))
	}
}

func TestStoreRefusedSaveWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key-actions.json")
	s, _ := Load(path, quiet())
	err := s.Set(Document{Keys: map[string]string{"prev": "sleep"}}, nil)
	if err == nil {
		t.Fatal("refused document saved")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a refused save wrote the file")
	}
}

func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	if s.Bound("thumbsUp") || s.Action("thumbsUp") != "" || s.Snapshot() != nil {
		t.Error("nil store should report nothing")
	}
}
