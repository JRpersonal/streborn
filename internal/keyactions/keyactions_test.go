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

// Snooze is known and may go on the thumbs keys and on AUX (discussion #1415).
func TestValidateAcceptsSnoozeOnThumbsAndAux(t *testing.T) {
	d := Document{Keys: map[string]string{"thumbsUp": "Snooze", "aux": " snooze "}}
	if err := d.Validate(nil); err != nil {
		t.Fatalf("snooze on thumbs and AUX refused: %v", err)
	}
	if d.Keys["thumbsUp"] != ActionSnooze || d.Keys["aux"] != ActionSnooze {
		t.Errorf("snooze not normalised: %v", d.Keys)
	}
	if !BindableKey(KeyAux) || !AllowedOn(ActionSnooze, KeyAux) {
		t.Error("AUX has to be bindable to snooze")
	}
}

// AUX always switches the input, so the sleep timer cannot go there: stepping
// it would also switch away from whatever plays.
func TestValidateRejectsSleepOnAux(t *testing.T) {
	d := Document{Keys: map[string]string{"aux": ActionSleep}}
	if err := d.Validate(nil); err == nil {
		t.Fatal("sleep timer on AUX accepted")
	}
	if AllowedOn(ActionSleep, KeyAux) {
		t.Error("AllowedOn lets the sleep timer onto AUX")
	}
}

// The transport keys keep their firmware reaction, so snooze cannot go there
// either.
func TestValidateRejectsSnoozeOnTransportKeys(t *testing.T) {
	for _, key := range []string{"prev", "next", "playPause", "preset1", "power"} {
		d := Document{Keys: map[string]string{key: ActionSnooze}}
		if err := d.Validate(nil); err == nil {
			t.Errorf("snooze on %q accepted", key)
		}
	}
}

// One thing per key: snooze and a group exclude each other like the sleep
// timer and a group.
func TestValidateRejectsSnoozeOnAGroupKey(t *testing.T) {
	bound := func(k string) bool { return k == "thumbsDown" }
	d := Document{Keys: map[string]string{"thumbsDown": ActionSnooze}}
	if err := d.Validate(bound); err == nil || !strings.Contains(err.Error(), "group") {
		t.Fatalf("snooze on a group key not refused with a group reason: %v", err)
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

// The bass steps (discussion #1494) go on the thumbs keys, in any spelling
// the app sends, and normalise to their canonical names.
func TestValidateAcceptsBassOnThumbs(t *testing.T) {
	d := Document{Keys: map[string]string{"thumbsUp": " BassUp ", "thumbsDown": "bassdown"}}
	if err := d.Validate(nil); err != nil {
		t.Fatalf("bass steps on the thumbs keys refused: %v", err)
	}
	if d.Keys["thumbsUp"] != ActionBassUp || d.Keys["thumbsDown"] != ActionBassDown {
		t.Errorf("bass steps not normalised: %v", d.Keys)
	}
	for _, a := range []string{ActionBassUp, ActionBassDown} {
		if !AllowedOn(a, KeyThumbsUp) || !AllowedOn(a, KeyThumbsDown) {
			t.Errorf("%s not allowed on a thumbs key", a)
		}
		if !IsBass(a) {
			t.Errorf("IsBass(%q) = false", a)
		}
	}
	if IsBass(ActionSleep) || IsBass(ActionSnooze) {
		t.Error("IsBass true for a non-bass action")
	}
}

// AUX always switches the input, so a bass step there would switch away from
// whatever plays; the transport keys keep their firmware reaction.
func TestValidateRejectsBassOffThumbs(t *testing.T) {
	for _, a := range []string{ActionBassUp, ActionBassDown} {
		if AllowedOn(a, KeyAux) {
			t.Errorf("AllowedOn lets %s onto AUX", a)
		}
		d := Document{Keys: map[string]string{"aux": a}}
		err := d.Validate(nil)
		if err == nil || !strings.Contains(err.Error(), "bass") {
			t.Errorf("%s on AUX: err %v, want a refusal naming the bass step", a, err)
		}
		for _, key := range []string{"prev", "next", "playPause", "preset1"} {
			d := Document{Keys: map[string]string{key: a}}
			if err := d.Validate(nil); err == nil {
				t.Errorf("%s on %q accepted", a, key)
			}
		}
	}
}

// A bass step and a group exclude each other on one key, like the others.
func TestValidateRejectsBassOnAGroupKey(t *testing.T) {
	bound := func(k string) bool { return k == "thumbsUp" }
	d := Document{Keys: map[string]string{"thumbsUp": ActionBassUp}}
	if err := d.Validate(bound); err == nil || !strings.Contains(err.Error(), "group") {
		t.Errorf("bass step on a group key: err %v, want a group conflict", err)
	}
}
