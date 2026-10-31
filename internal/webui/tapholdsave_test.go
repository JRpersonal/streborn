package webui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/presets"
)

// Tap a key, then hold it (#1110): the snapshot taken at the tap is what is
// stored on the key's second row once the hold is confirmed.

func TestSnapshotPlayingNothingToKeep(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  []byte
		code string
	}{
		"unknown":        {nil, "nowplaying-unknown"},
		"standby":        {[]byte(`<nowPlaying source="STANDBY"><ContentItem source="STANDBY" isPresetable="false"/></nowPlaying>`), "nothing-playing"},
		"aux":            {nowPlayingDoc("AUX", "AUX", "AUX IN", ""), "nothing-playing"},
		"bluetooth":      {nowPlayingDoc("BLUETOOTH", "", "Phone", ""), "nothing-playing"},
		"spotify native": {nowPlayingDoc("SPOTIFY", "/playback/container/c3BvdGlmeTpwbGF5bGlzdDp4", "Song", ""), "spotify-native"},
		"own raw proxy":  {nowPlayingDoc("UPNP", "http://127.0.0.1:8888/stream/raw?u=%%%", "x", ""), "nothing-saveable"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSavePlayingServer(t, nil)
			snap := s.SnapshotPlaying(tc.doc)
			if snap.Savable() || snap.Code != tc.code {
				t.Fatalf("snapshot = %+v, want code %s", snap, tc.code)
			}
			if _, err := s.SaveSnapshot(context.Background(), snap, 7); err == nil {
				t.Fatal("an empty snapshot must not save")
			}
			if len(s.presets.All()) != 0 {
				t.Fatal("nothing may be stored")
			}
		})
	}
}

// Spotify before the tap: the engine's context is taken at the tap, and the
// save is a live save (the key belongs to the account that played it).
func TestSnapshotPlayingSpotifySavesOnRowTwo(t *testing.T) {
	s := newHoldTestServer(t, "UPNP", holdTestSpotifyLoc)
	ctxURI := holdTestSpotifyURI
	s.spotifyContext = func() string { return ctxURI }
	s.spotifyUser = func(context.Context) string { return "second-user" }
	s.spotifyMeta = func(context.Context, string) (string, string) { return "https://i.scdn.co/image/x", "Jens Chill" }
	row1 := presets.Preset{Slot: 1, Name: "Radio One", Type: "radio", StreamURL: savePlayingStation}
	if err := s.presets.SetSlot(row1); err != nil {
		t.Fatal(err)
	}
	snap := s.SnapshotPlaying(nowPlayingDoc("UPNP", holdTestSpotifyLoc, "Song", ""))
	if snap.Kind != "spotify" || snap.Preset.URI != holdTestSpotifyURI {
		t.Fatalf("snapshot = %+v", snap)
	}
	// The tap switched the speaker over; the engine moved on. The snapshot
	// keeps what played before.
	ctxURI = ""
	saved, err := s.SaveSnapshot(context.Background(), snap, presets.Row2Slot(1))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Slot != 7 || saved.Name != "Jens Chill" {
		t.Fatalf("saved = %+v", saved)
	}
	if p, ok := s.presets.Get(7); !ok || p.Type != "spotify" || p.URI != holdTestSpotifyURI {
		t.Fatalf("slot 7 = %+v", p)
	}
	if p, _ := s.presets.Get(1); p.StreamURL != row1.StreamURL || p.Name != row1.Name || p.Type != row1.Type {
		t.Fatalf("row 1 changed: %+v", p)
	}
}

// A station started from the app (not on any key) is kept as the station.
func TestSnapshotPlayingStationSavesOnRowTwo(t *testing.T) {
	loc := boxurl.RawStream(savePlayingStation)
	s := newSavePlayingServer(t, nil)
	s.play.last = &lastPlayInfo{boxURL: loc, title: "Radio One", art: "https://img.example/r1.png", ts: time.Now()}
	snap := s.SnapshotPlaying(nowPlayingDoc("UPNP", loc, "radio-one.mp3", ""))
	if snap.Kind != "stream" || snap.Name() != "Radio One" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if _, err := s.SaveSnapshot(context.Background(), snap, 9); err != nil {
		t.Fatal(err)
	}
	if p, ok := s.presets.Get(9); !ok || p.StreamURL != savePlayingStation || p.Name != "Radio One" {
		t.Fatalf("slot 9 = %+v", p)
	}
}

// The item already sits on another key: refused like the app's save, with
// that key named, and nothing moved.
func TestSnapshotPlayingDuplicateIsRefused(t *testing.T) {
	s := newSavePlayingServer(t, nil)
	keeper := presets.Preset{Slot: 3, Name: "Keeper", Type: "radio", StreamURL: savePlayingStation}
	if err := s.presets.SetSlot(keeper); err != nil {
		t.Fatal(err)
	}
	snap := s.SnapshotPlaying(nowPlayingDoc("UPNP", boxurl.StreamSlot(3), "stale", ""))
	if snap.Kind != "copy" || snap.FromSlot != 3 {
		t.Fatalf("snapshot = %+v", snap)
	}
	_, err := s.SaveSnapshot(context.Background(), snap, 7)
	var refusal *HoldSaveError
	if !errors.As(err, &refusal) || refusal.Code != "already-on-slot" || refusal.OtherSlot != 3 {
		t.Fatalf("err = %v, want already-on-slot 3", err)
	}
	if _, ok := s.presets.Get(7); ok {
		t.Fatal("nothing may be stored on a refusal")
	}
	if p, _ := s.presets.Get(3); p.StreamURL != keeper.StreamURL || p.Name != keeper.Name {
		t.Fatalf("key 3 changed: %+v", p)
	}
}

// A Pandora station the speaker plays itself is kept as a native preset.
func TestSnapshotPlayingNativeService(t *testing.T) {
	s := newSavePlayingServer(t, nil)
	snap := s.SnapshotPlaying([]byte(`<nowPlaying deviceID="device-id-here" source="PANDORA" sourceAccount="listener@example.com">` +
		`<ContentItem source="PANDORA" type="stationurl" location="4071226281950183516" sourceAccount="listener@example.com" isPresetable="true">` +
		`<itemName>Country Radio</itemName></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>`))
	if snap.Kind != "native" || !snap.Preset.IsNative() || snap.Preset.Native.Location != "4071226281950183516" {
		t.Fatalf("snapshot = %+v", snap)
	}
}

// A folder playing as the queue is kept as the folder.
func TestSnapshotPlayingFolder(t *testing.T) {
	s := newSavePlayingServer(t, nil)
	s.queue.load(queueSaveTracks, 0, false, repeatOff)
	s.queueFolder = recentCardCtx{key: "queue:uuid:abc:64$1", name: "Abbey Road", source: "Living Room NAS"}
	snap := s.SnapshotPlaying(nowPlayingDoc("UPNP", queueSaveTracks[0].URL, "Come Together", ""))
	if snap.Kind != "queue" || snap.Name() != "Abbey Road" {
		t.Fatalf("snapshot = %+v", snap)
	}
	saved, err := s.SaveSnapshot(context.Background(), snap, 8)
	if err != nil || saved.Name != "Abbey Road" {
		t.Fatalf("save: %+v %v", saved, err)
	}
	if p, ok := s.presets.Get(8); !ok || p.Type != "queue" || len(p.Items) != 2 {
		t.Fatalf("slot 8 = %+v", p)
	}
	again, err := s.SaveSnapshot(context.Background(), snap, 8)
	if err != nil || !again.Unchanged {
		t.Fatalf("again: %+v %v", again, err)
	}
	var refusal *HoldSaveError
	if _, err := s.SaveSnapshot(context.Background(), snap, 9); !errors.As(err, &refusal) || refusal.OtherSlot != 8 {
		t.Fatalf("second key: err = %v, want already-on-slot 8", err)
	}
}
