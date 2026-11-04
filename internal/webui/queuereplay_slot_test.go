package webui

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// #1592: a folder started from a preset key is recorded as "queue:slot:N". The
// replay used to split that as server "slot", answer 404, and the app played
// the folder's first file alone.
func TestSlotFolderCardSlot(t *testing.T) {
	for key, want := range map[string]int{
		"queue:slot:3":   3,
		" queue:slot:12": 12,
	} {
		got, _, ok := slotFolderCardSlot(key)
		if !ok || got != want {
			t.Errorf("slotFolderCardSlot(%q) = %d, %v; want %d, true", key, got, ok, want)
		}
	}
	for _, key := range []string{
		"queue:slot:",
		"queue:slot:x",
		"queue:slot:0",
		"queue:uuid:AAAA:64$0",
		"spotify:playlist:37i9dQ",
		"",
	} {
		if _, _, ok := slotFolderCardSlot(key); ok {
			t.Errorf("slotFolderCardSlot(%q) read a slot card", key)
		}
	}
}

func TestSlotStillHoldsFolder(t *testing.T) {
	folder := presets.Preset{Slot: 3, Name: "Power Windows", Type: "queue", Items: []presets.PresetItem{{}}}
	cases := []struct {
		name  string
		p     presets.Preset
		found bool
		card  string
		want  bool
	}{
		{"same folder", folder, true, "Power Windows", true},
		{"name in another case", folder, true, "power windows ", true},
		{"card without a name", folder, true, "", true},
		{"key now holds another folder", folder, true, "Moving Pictures", false},
		{"key now holds a station", presets.Preset{Slot: 3, Name: "Power Windows", Type: "radio"}, true, "Power Windows", false},
		{"folder without tracks", presets.Preset{Slot: 3, Name: "Power Windows", Type: "queue"}, true, "Power Windows", false},
		{"empty key", presets.Preset{}, false, "Power Windows", false},
	}
	for _, c := range cases {
		if got := slotStillHoldsFolder(c.p, c.found, c.card, ""); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// A slot card whose key now holds something else keeps the app's older
// fallback (404), and never reaches the speaker.
func TestQueueReplayCardSlotKeyReassigned(t *testing.T) {
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.SetSlot(presets.Preset{Slot: 3, Name: "1LIVE", Type: "radio", StreamURL: "http://radio.example/live.mp3"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), presets: store, renderer: &upnp.Renderer{}}
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/queue/replay-card", strings.NewReader(`{"key":"queue:slot:3","name":"Power Windows"}`))
	r.Header.Set("Content-Type", "application/json")
	s.handleQueueReplayCard(rr, r)
	if rr.Code != 404 {
		t.Fatalf("a key that no longer holds the folder must 404 (app fallback), got %d: %s", rr.Code, rr.Body.String())
	}
}

// #1730: a key-started folder's card shows the key's cover, never the first
// song a shuffle happened to start with.
func TestQueueKeyCover(t *testing.T) {
	items := []presets.PresetItem{{Title: "no cover"}, {Title: "first with cover", Art: "http://192.0.2.60/a.jpg"}, {Art: "http://192.0.2.60/b.jpg"}}
	if got := queueKeyCover(presets.Preset{Art: " http://192.0.2.60/key.jpg ", Items: items}); got != "http://192.0.2.60/key.jpg" {
		t.Errorf("the key's own cover must win, got %q", got)
	}
	if got := queueKeyCover(presets.Preset{Items: items}); got != "http://192.0.2.60/a.jpg" {
		t.Errorf("without its own cover the key shows the first song with one in list order, got %q", got)
	}
	if got := queueKeyCover(presets.Preset{Items: []presets.PresetItem{{Title: "x"}}}); got != "" {
		t.Errorf("no cover anywhere stays empty, got %q", got)
	}
}
