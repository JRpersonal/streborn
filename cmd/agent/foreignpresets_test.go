package main

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/webui"
)

func testForeignStore(t *testing.T) *foreignPresetStore {
	t.Helper()
	return newForeignPresetStore(filepath.Join(t.TempDir(), "foreign-presets.json"), slog.Default())
}

// The classifier must keep everything STR writes OUT of the preservation:
// the strict UPnP proxy form, the relative native form the box echoes back,
// and the absolute orion form - and keep real foreign presets IN.
func TestIsForeignBoxPreset(t *testing.T) {
	cases := []struct {
		loc     string
		foreign bool
	}{
		{"http://127.0.0.1:8888/stream/3", false},
		{"http://127.0.0.1:8888/spotify/stream-2.ogg", false},
		{"/station?data=eyJuYW1lIjoi", false},
		{"/core02/svc-bmx-adapter-orion/prod/orion/station?data=abc", false},
		{"https://content.api.bose.io/core02/svc-bmx-adapter-orion/prod/orion/station?data=abc", false},
		{"https://api.deezer.com/user/me/flow", true},
		{"/v1/playback/station/s10464", true},
		{"0$1$13$75$1429$1430", true},
		{"", false},
	}
	for _, c := range cases {
		if got := isForeignBoxPreset(c.loc); got != c.foreign {
			t.Errorf("isForeignBoxPreset(%q) = %v, want %v", c.loc, got, c.foreign)
		}
	}
}

// A full box report replaces the foreign set: STR-written slots are ignored,
// foreign ones are kept, and a later NON-empty report without a slot drops it
// (the user overwrote it), while an EMPTY report - the firmware wipe shape -
// must not erase anything.
func TestForeignStoreNoteBoxList(t *testing.T) {
	s := testForeignStore(t)
	s.NoteBoxList([]webui.BoxPreset{
		{Slot: 1, Source: "LOCAL_INTERNET_RADIO", Type: "stationurl", Location: "/station?data=x", Name: "France Inter"},
		{Slot: 3, Source: "DEEZER", Type: "tracklistRadio", Location: "https://api.deezer.com/user/me/flow", SourceAccount: "user@example.com", Name: "Flux"},
		{Slot: 4, Source: "UPNP", Type: "audio", Location: "http://127.0.0.1:8888/stream/4", Name: "Europe 2"},
	})
	if got := s.MargePresets(nil); len(got) != 1 || got[0].ID != 3 || got[0].Source != "DEEZER" {
		t.Fatalf("expected exactly the Deezer slot preserved, got %+v", got)
	}

	// Empty report (firmware wipe): nothing may be erased.
	s.NoteBoxList(nil)
	if got := s.MargePresets(nil); len(got) != 1 {
		t.Fatalf("empty report erased the preservation, got %+v", got)
	}

	// Non-empty report without slot 3: the user overwrote it, so it goes.
	s.NoteBoxList([]webui.BoxPreset{
		{Slot: 3, Source: "UPNP", Type: "audio", Location: "http://127.0.0.1:8888/stream/3", Name: "Now STR"},
	})
	if got := s.MargePresets(nil); len(got) != 0 {
		t.Fatalf("overwritten slot survived, got %+v", got)
	}
}

// The store persists across restarts (that is its whole point: the wipe this
// guards against happens at the first re-onboarding after an agent start).
func TestForeignStorePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign-presets.json")
	s := newForeignPresetStore(path, slog.Default())
	s.NoteBoxList([]webui.BoxPreset{
		{Slot: 5, Source: "STORED_MUSIC", Location: "0$1$13$75", SourceAccount: "aa/0", Name: "Random Access Memories"},
	})
	s2 := newForeignPresetStore(path, slog.Default())
	got := s2.MargePresets(nil)
	if len(got) != 1 || got[0].ID != 5 || got[0].Source != "STORED_MUSIC" {
		t.Fatalf("persisted store did not reload, got %+v", got)
	}
}

// Serve-time rules: STR-store slots win (taken map), and values are escaped
// for the verbatim text/template (a name with '&' must not break the box's
// preset parse).
func TestForeignStoreMargePresets(t *testing.T) {
	s := testForeignStore(t)
	s.NoteBoxList([]webui.BoxPreset{
		{Slot: 2, Source: "DEEZER", Location: "https://api.deezer.com/x?a=1&b=2", Name: "Pop & Rock"},
		{Slot: 6, Source: "TUNEIN", Location: "/v1/playback/station/s1", Name: "Radio"},
	})
	got := s.MargePresets(map[int]bool{6: true})
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("taken slot not skipped, got %+v", got)
	}
	if got[0].ItemName != "Pop &amp; Rock" || got[0].Location != "https://api.deezer.com/x?a=1&amp;b=2" {
		t.Fatalf("values not escaped for the marge template: %+v", got[0])
	}
}
