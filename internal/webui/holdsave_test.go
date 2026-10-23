package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
)

// A hardware key held while STR's own stream plays (#1217) stores what plays
// through the app's save paths. These cover what HoldSaveLive picks and that
// the per-slot rules apply to it.

const (
	holdTestSpotifyURI = "spotify:playlist:37i9dQZF1DX0AMssoUKCz7"
	holdTestSpotifyLoc = "http://127.0.0.1:8888/spotify/stream.ogg"
	// Port 1 refuses at once, so the save's web-page probe finds no evidence
	// either way and lets the file through, as for an unreachable stream.
	holdTestFileURL = "http://127.0.0.1:1/m/NDLNA/15.mp3"
)

func newHoldTestServer(t *testing.T, source, location string) *Server {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		presets: store,
		queue:   newPlayQueue(),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		boxNowFn: func(context.Context) (string, string, bool) {
			return source, location, true
		},
	}
}

func TestHoldSaveLiveSpotify(t *testing.T) {
	s := newHoldTestServer(t, "UPNP", holdTestSpotifyLoc)
	s.spotifyContext = func() string { return holdTestSpotifyURI }
	s.spotifyUser = func(context.Context) string { return "second-user" }
	s.spotifyMeta = func(context.Context, string) (string, string) { return "https://i.scdn.co/image/x", "Jens Chill" }
	saved, err := s.HoldSaveLive(context.Background(), 2)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Kind != "spotify" || saved.Name != "Jens Chill" || saved.Unchanged {
		t.Fatalf("saved %+v", saved)
	}
	p, ok := s.presets.Get(2)
	if !ok || p.Type != "spotify" || p.URI != holdTestSpotifyURI || p.Name != "Jens Chill" {
		t.Fatalf("stored %+v", p)
	}
	// A live save: the key belongs to the account playing right now.
	if p.Account != "second-user" {
		t.Fatalf("account = %q, want the live account", p.Account)
	}
	// Holding the same key again writes nothing.
	again, err := s.HoldSaveLive(context.Background(), 2)
	if err != nil || !again.Unchanged {
		t.Fatalf("re-hold: %+v %v", again, err)
	}
}

func TestHoldSaveLiveQueue(t *testing.T) {
	s := newHoldTestServer(t, "UPNP", queueSaveTracks[0].URL)
	s.queue.load(queueSaveTracks, 0, true, repeatOff)
	s.queueFolder = recentCardCtx{key: "queue:uuid:abc:64$1", name: "Abbey Road", source: "Living Room NAS"}
	saved, err := s.HoldSaveLive(context.Background(), 4)
	if err != nil || saved.Kind != "queue" || saved.Name != "Abbey Road" {
		t.Fatalf("save: %+v %v", saved, err)
	}
	p, ok := s.presets.Get(4)
	if !ok || p.Type != "queue" || p.Source != "Living Room NAS" || !p.Shuffle || len(p.Items) != 2 {
		t.Fatalf("stored %+v", p)
	}
	if again, err := s.HoldSaveLive(context.Background(), 4); err != nil || !again.Unchanged {
		t.Fatalf("re-hold: %+v %v", again, err)
	}
}

func TestHoldSaveLiveLibrarySong(t *testing.T) {
	s := newHoldTestServer(t, "UPNP", holdTestFileURL)
	reg, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(mediaservers.Server{ID: "uuid-nas", Name: "Backupserver", Location: "http://127.0.0.1:50001/desc/device.xml"}); err != nil {
		t.Fatal(err)
	}
	s.mediaServers = reg
	s.lastPlay = &lastPlayInfo{boxURL: holdTestFileURL, title: "One Alpha", art: "http://192.0.2.10:50002/art/15.jpg", mime: "audio/mpeg", ts: time.Now()}
	s.trackLens.put(holdTestFileURL, 18*time.Second)
	saved, err := s.HoldSaveLive(context.Background(), 2)
	if err != nil || saved.Kind != "library" || saved.Name != "One Alpha" {
		t.Fatalf("save: %+v %v", saved, err)
	}
	p, ok := s.presets.Get(2)
	if !ok || p.Type != "radio" || p.StreamURL != holdTestFileURL || p.Source != "Backupserver" ||
		p.DurationSec != 18 || p.Art != "http://192.0.2.10:50002/art/15.jpg" {
		t.Fatalf("stored %+v", p)
	}

	// No registered server on that host: the key still gets a server name,
	// or a press would send the file through the radio proxy (#139).
	s.mediaServers = nil
	if err := s.presets.RemoveSlot(2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HoldSaveLive(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.presets.Get(2); p.Source != holdLibraryFallbackSource || libraryFileMime(p) == "" {
		t.Fatalf("stored %+v, want a library key", p)
	}
}

// A single file STR does not know as its own play (another app pushed it, or
// the play is older than what plays now) is not saved.
func TestHoldSaveLiveUnknownStream(t *testing.T) {
	s := newHoldTestServer(t, "UPNP", "http://192.0.2.50:8200/MediaItems/22.flac")
	s.lastPlay = &lastPlayInfo{boxURL: holdTestFileURL, title: "One Alpha", mime: "audio/mpeg"}
	if _, err := s.HoldSaveLive(context.Background(), 2); !errors.Is(err, ErrHoldUnknownStream) {
		t.Fatalf("err = %v, want ErrHoldUnknownStream", err)
	}
	if _, ok := s.presets.Get(2); ok {
		t.Fatal("nothing may be stored")
	}
}

// On a source the speaker plays itself nothing happens here: its own
// hold-to-store reaches holdstore.go.
func TestHoldSaveLiveNativeSourceNoAction(t *testing.T) {
	s := newHoldTestServer(t, "LOCAL_INTERNET_RADIO", "/station?data=abc")
	s.spotifyContext = func() string { return holdTestSpotifyURI }
	if _, err := s.HoldSaveLive(context.Background(), 1); !errors.Is(err, ErrHoldNotUPnP) {
		t.Fatalf("err = %v, want ErrHoldNotUPnP", err)
	}
	if len(s.presets.All()) != 0 {
		t.Fatal("nothing may be stored")
	}
}

// The one-station-one-key rule (#836) holds for a held key too, but a hold is
// an explicit "put it here": the playlist on key 3 comes over to the held key
// 2, and key 3 gets what key 2 had. Nothing is copied, nothing is lost.
func TestHoldSaveLiveDuplicateSwapsKeys(t *testing.T) {
	s := newHoldTestServer(t, "UPNP", holdTestSpotifyLoc)
	s.spotifyContext = func() string { return holdTestSpotifyURI }
	if err := s.presets.SetSlot(presets.Preset{Slot: 3, Name: "Jens Chill", Type: "spotify", URI: holdTestSpotifyURI, Shuffle: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.presets.SetSlot(presets.Preset{Slot: 2, Name: "Rock FM", Type: "radio", StreamURL: "http://example.com/rock"}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.HoldSaveLive(context.Background(), 2)
	if err != nil {
		t.Fatalf("err = %v, want a swap", err)
	}
	if saved.Slot != 2 || saved.Name != "Jens Chill" || saved.SwappedWith != 3 || saved.SwappedName != "Rock FM" {
		t.Fatalf("saved = %+v, want Jens Chill on 2 swapped with Rock FM on 3", saved)
	}
	if p, ok := s.presets.Get(2); !ok || p.URI != holdTestSpotifyURI || !p.Shuffle {
		t.Fatalf("key 2 = %+v, want the playlist with its shuffle", p)
	}
	if p, ok := s.presets.Get(3); !ok || p.StreamURL != "http://example.com/rock" {
		t.Fatalf("key 3 = %+v, want the station key 2 had", p)
	}
	if n := len(s.presets.All()); n != 2 {
		t.Fatalf("store holds %d presets, want 2", n)
	}
}

// A held empty key takes the item over and the key it sat on ends up empty.
func TestHoldSaveLiveDuplicateOnEmptyKeyMoves(t *testing.T) {
	s := newHoldTestServer(t, "UPNP", holdTestSpotifyLoc)
	s.spotifyContext = func() string { return holdTestSpotifyURI }
	if err := s.presets.SetSlot(presets.Preset{Slot: 3, Name: "Jens Chill", Type: "spotify", URI: holdTestSpotifyURI}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.HoldSaveLive(context.Background(), 2)
	if err != nil {
		t.Fatalf("err = %v, want a move", err)
	}
	if saved.SwappedWith != 3 || saved.SwappedName != "" {
		t.Fatalf("saved = %+v, want a move from key 3", saved)
	}
	if p, ok := s.presets.Get(2); !ok || p.URI != holdTestSpotifyURI {
		t.Fatal("key 2 must hold the playlist")
	}
	if _, ok := s.presets.Get(3); ok {
		t.Fatal("key 3 must be empty")
	}
}

// The hold-to-save reads the source and the decoded location through the
// boxapi reader; the seam-free path is pinned here on the two shapes it
// decides on.
func TestHoldSaveReadsSourceAndDecodedLocation(t *testing.T) {
	s := &Server{boxNowReadFn: scriptNowPlayingDoc(`<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="AABBCCDDEEFF" source="UPNP" sourceAccount="UPnPUserName"><ContentItem source="UPNP" location="http://192.0.2.28:50002/m/a.mp3?x=1&amp;y=2" sourceAccount="UPnPUserName" isPresetable="false"><itemName>One Alpha</itemName></ContentItem></nowPlaying>`)}
	src, loc, ok := s.boxNowPlaying(context.Background())
	if !ok || src != "UPNP" || loc != "http://192.0.2.28:50002/m/a.mp3?x=1&y=2" {
		t.Fatalf("got %q %q %v", src, loc, ok)
	}
	s.boxNowReadFn = scriptNowPlayingDoc(`<nowPlaying deviceID="AABBCCDDEEFF" source="STANDBY"><ContentItem source="STANDBY" isPresetable="false" /></nowPlaying>`)
	src, loc, ok = s.boxNowPlaying(context.Background())
	if !ok || src != "STANDBY" || loc != "" {
		t.Fatalf("standby: got %q %q %v", src, loc, ok)
	}
	s.boxNowReadFn = scriptNowPlayingUnreadable()
	if _, _, ok := s.boxNowPlaying(context.Background()); ok {
		t.Fatal("an unreadable speaker reported ok")
	}
}
