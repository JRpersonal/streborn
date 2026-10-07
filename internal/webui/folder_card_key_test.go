package webui

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// The phone page keyed a folder card "folder:<udn>/<id>" while the desktop app
// and the agent's replay use "queue:uuid:<guid>:<container>". A folder started
// from the phone then lost its "playing" mark in the app from the second track
// on (#1065 matches on the queue: card), could not be replayed as a whole, and
// showed up as a second card next to the same folder played from the app.
func TestPhoneFolderCardUsesTheAppKeyScheme(t *testing.T) {
	start := strings.Index(indexHTML, "function folderCardKey(")
	if start < 0 {
		t.Fatal("folderCardKey is gone; if it moved, move this test with it")
	}
	fn := indexHTML[start:]
	fn = fn[:strings.Index(fn, "\n}\n")]
	for _, want := range []string{"'queue:' + udn + ':'", "'uuid:' + top.udn"} {
		if !strings.Contains(fn, want) {
			t.Errorf("folderCardKey lost %q", want)
		}
	}
	if !strings.Contains(indexHTML, "key: folderCardKey(top, items[0].url)") {
		t.Error("a folder play from the phone does not use folderCardKey")
	}
	if strings.Contains(indexHTML, "'folder:' + (top ?") {
		t.Error("the phone still writes the old folder: card key")
	}
}

// Old phone cards stay in the Recently-played ring. A replay maps them to the
// canonical key, so they replay as a whole folder and the queue they start
// carries the card the app knows.
func TestCanonicalFolderCardKey(t *testing.T) {
	cases := []struct {
		key, want string
		ok        bool
	}{
		{"queue:uuid:00113251-28ed-0011-ed28-ed2851321100:22$601", "queue:uuid:00113251-28ed-0011-ed28-ed2851321100:22$601", true},
		{"folder:00113251-28ed-0011-ed28-ed2851321100/22$601", "queue:uuid:00113251-28ed-0011-ed28-ed2851321100:22$601", true},
		{"folder:fa095ecc-e13e-40e7-8e6c-3C37129F8346/4:cont2:578:AVM GmbH0:3:Pop", "queue:uuid:fa095ecc-e13e-40e7-8e6c-3C37129F8346:4:cont2:578:AVM GmbH0:3:Pop", true},
		{"folder:uuid:fa095ecc-e13e-40e7-8e6c-3C37129F8346/0", "queue:uuid:fa095ecc-e13e-40e7-8e6c-3C37129F8346:0", true},
		// The no-server fallback: the first track's URL is not a folder.
		{"folder:http://192.0.2.1:9000/a.mp3", "", false},
		{"folder:", "", false},
		{"spotify:playlist:37i9dQ", "spotify:playlist:37i9dQ", true},
	}
	for _, c := range cases {
		got, ok := canonicalFolderCardKey(c.key)
		if got != c.want || ok != c.ok {
			t.Errorf("canonicalFolderCardKey(%q) = (%q, %v), want (%q, %v)", c.key, got, ok, c.want, c.ok)
		}
	}
}

func TestQueueReplayCardAcceptsOldPhoneCards(t *testing.T) {
	store, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), mediaServers: store, renderer: &upnp.Renderer{}}
	post := func(body string) int {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/queue/replay-card", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		s.handleQueueReplayCard(rr, r)
		return rr.Code
	}
	// Read as a folder card: it reaches the registry check (404 for a server
	// that is not registered here) instead of "not a folder card".
	if code := post(`{"key":"folder:11111111-2222-3333-4444-555555555555/64$0"}`); code != 404 {
		t.Errorf("an old phone folder card must be read as a folder card, got %d", code)
	}
	if code := post(`{"key":"folder:http://192.0.2.1/a.mp3"}`); code != 400 {
		t.Errorf("a folder: key without a server is not a folder card, got %d", code)
	}
}
