package spotify

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// infoPlayModes is the playModes part of a /spotify/info answer, nil when the
// answer has none.
func infoPlayModes(t *testing.T, m *Manager) *InfoPlayModes {
	t.Helper()
	noBackgroundRead(m)
	rr := httptest.NewRecorder()
	m.ServeInfo(rr, httptest.NewRequest("GET", "/spotify/info", nil))
	var got struct {
		PlayModes *InfoPlayModes `json:"playModes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	return got.PlayModes
}

// /spotify/info carries the engine's shuffle and repeat from the same /status
// read as the song, so the app's buttons show the real state (#2058).
func TestServeInfoReportsThePlayModes(t *testing.T) {
	for _, c := range []struct {
		modes   playModes
		shuffle bool
		repeat  string
	}{
		{playModes{}, false, RepeatOff},
		{playModes{Shuffle: true, RepeatContext: true}, true, RepeatContext},
		{playModes{RepeatContext: true, RepeatTrack: true}, false, RepeatTrack},
	} {
		f := &fakePlayModeEngine{modes: c.modes}
		m := newFakePlayModeManager(t, f)
		got := infoPlayModes(t, m)
		if got == nil {
			t.Fatalf("%+v: no playModes in /spotify/info", c.modes)
		}
		if got.Shuffle != c.shuffle || got.Repeat != c.repeat {
			t.Fatalf("%+v: playModes = %+v, want shuffle %v repeat %q", c.modes, *got, c.shuffle, c.repeat)
		}
	}
}

// An engine that does not answer leaves the field out: the app then shows no
// buttons rather than a guess.
func TestServeInfoLeavesThePlayModesOutWithoutAnEngine(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	if got := infoPlayModes(t, m); got != nil {
		t.Fatalf("playModes = %+v without an engine, want none", *got)
	}
}

func TestPlayModesReadsTheLiveState(t *testing.T) {
	f := &fakePlayModeEngine{modes: playModes{Shuffle: true, RepeatContext: true, RepeatTrack: true}}
	m := newFakePlayModeManager(t, f)
	shuffle, repeat, err := m.PlayModes(context.Background())
	if err != nil || !shuffle || repeat != RepeatTrack {
		t.Fatalf("shuffle=%v repeat=%q err=%v, want true, track", shuffle, repeat, err)
	}
	if got := f.calls(); len(got) != 0 {
		t.Fatalf("a read wrote to the engine: %q", got)
	}
}

func TestPlayModesFailsWithoutAnEngine(t *testing.T) {
	m := newTestManagerAt(t, "http://127.0.0.1:1")
	if _, _, err := m.PlayModes(context.Background()); err == nil {
		t.Fatal("no error from an engine that is not there")
	}
}
