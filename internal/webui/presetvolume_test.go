package webui

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

// fakeLevel is the speaker's volume as the per-key volume sees it.
type fakeLevel struct {
	mu     sync.Mutex
	level  int
	writes []int
}

func (f *fakeLevel) set(_ context.Context, v int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.level = v
	f.writes = append(f.writes, v)
	return nil
}

func (f *fakeLevel) get(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.level, nil
}

func (f *fakeLevel) snapshot() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.writes...)
}

func presetVolServer(t *testing.T, playing *atomic.Bool) (*Server, *fakeLevel) {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSlot(presets.Preset{Slot: 1, Name: "Quiet news", StreamURL: "http://example.com/news", Type: "radio", Volume: 5}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSlot(presets.Preset{Slot: 2, Name: "No level", StreamURL: "http://example.com/music", Type: "radio"}); err != nil {
		t.Fatal(err)
	}
	lv := &fakeLevel{level: 3}
	s := &Server{
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		presets:     store,
		setVolumeFn: lv.set,
		getVolumeFn: lv.get,
		playStateFn: func() (bool, bool) { return false, playing.Load() },
	}
	shortenPresetVolumeTiming(t)
	return s, lv
}

func shortenPresetVolumeTiming(t *testing.T) {
	t.Helper()
	oldFor, oldPoll, oldSettle, oldRe := presetVolumeWatchFor, presetVolumeWatchPoll, presetVolumeSettle, presetVolumeRecheck
	presetVolumeWatchFor, presetVolumeWatchPoll, presetVolumeSettle, presetVolumeRecheck =
		300*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() {
		presetVolumeWatchFor, presetVolumeWatchPoll, presetVolumeSettle, presetVolumeRecheck = oldFor, oldPoll, oldSettle, oldRe
	})
}

func waitWrites(t *testing.T, lv *fakeLevel, n int) []int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w := lv.snapshot(); len(w) >= n {
			return w
		}
		time.Sleep(5 * time.Millisecond)
	}
	return lv.snapshot()
}

// The key's level is written once the key plays, not before.
func TestPresetVolumeIsSetOnceTheKeyPlays(t *testing.T) {
	var playing atomic.Bool
	s, lv := presetVolServer(t, &playing)
	s.ApplyPresetVolume(1, "test")
	time.Sleep(60 * time.Millisecond)
	if w := lv.snapshot(); len(w) != 0 {
		t.Fatalf("level written before the key played: %v", w)
	}
	playing.Store(true)
	if w := waitWrites(t, lv, 1); len(w) != 1 || w[0] != 5 {
		t.Fatalf("writes %v, want [5]", w)
	}
	time.Sleep(100 * time.Millisecond)
	if w := lv.snapshot(); len(w) != 1 {
		t.Fatalf("level written again although it held: %v", w)
	}
}

// A key without a level leaves the volume alone: off by default.
func TestPresetVolumeOffLeavesTheVolumeAlone(t *testing.T) {
	var playing atomic.Bool
	playing.Store(true)
	s, lv := presetVolServer(t, &playing)
	s.ApplyPresetVolume(2, "test")
	time.Sleep(150 * time.Millisecond)
	if w := lv.snapshot(); len(w) != 0 {
		t.Fatalf("a key without a level changed the volume: %v", w)
	}
}

// A speaker that settles its source late puts its own level back; the key's
// level is written a second time. A level somebody set at the knob is not.
func TestPresetVolumeUndoesOnlyTheFirmwareRevert(t *testing.T) {
	var playing atomic.Bool
	playing.Store(true)
	s, lv := presetVolServer(t, &playing)
	s.ApplyPresetVolume(1, "test")
	waitWrites(t, lv, 1)
	lv.mu.Lock()
	lv.level = 3 // back at the level from before the write
	lv.mu.Unlock()
	if w := waitWrites(t, lv, 2); len(w) != 2 || w[1] != 5 {
		t.Fatalf("writes %v, want the level written again after the revert", w)
	}

	s2, lv2 := presetVolServer(t, &playing)
	s2.ApplyPresetVolume(1, "test")
	waitWrites(t, lv2, 1)
	lv2.mu.Lock()
	lv2.level = 9 // the listener turned it up
	lv2.mu.Unlock()
	time.Sleep(120 * time.Millisecond)
	if w := lv2.snapshot(); len(w) != 1 {
		t.Fatalf("the listener's own level was overwritten: %v", w)
	}
}

// A newer press retires the watch of the one before it.
func TestPresetVolumeNewerPressWins(t *testing.T) {
	var playing atomic.Bool
	s, lv := presetVolServer(t, &playing)
	s.ApplyPresetVolume(1, "test")
	s.presetVolumeSeq.Add(1) // a newer press, of a key without a level
	playing.Store(true)
	time.Sleep(150 * time.Millisecond)
	if w := lv.snapshot(); len(w) != 0 {
		t.Fatalf("a superseded press still set its level: %v", w)
	}
}

// PATCH {"volume":N} stores the level without touching the station, and 0
// switches it off.
func TestPresetPatchStoresTheVolume(t *testing.T) {
	var playing atomic.Bool
	s, _ := presetVolServer(t, &playing)
	patch := func(body string) (int, presets.Preset) {
		r := httptest.NewRequest(http.MethodPatch, "/api/presets/2", strings.NewReader(body))
		r.RemoteAddr = "192.0.2.20:5555"
		w := httptest.NewRecorder()
		s.handlePresetSlot(w, r)
		var p presets.Preset
		_ = json.Unmarshal(w.Body.Bytes(), &p)
		return w.Code, p
	}
	if code, p := patch(`{"volume":4}`); code != http.StatusOK || p.Volume != 4 || p.Name != "No level" {
		t.Fatalf("PATCH volume = %d %+v", code, p)
	}
	if code, p := patch(`{"volume":0}`); code != http.StatusOK || p.Volume != 0 {
		t.Fatalf("PATCH volume 0 = %d %+v", code, p)
	}
	// Name and volume in one call.
	if code, p := patch(`{"name":"Music","volume":5}`); code != http.StatusOK || p.Name != "Music" || p.Volume != 5 {
		t.Fatalf("PATCH name+volume = %d %+v", code, p)
	}
	// A rename alone keeps the level.
	if code, p := patch(`{"name":"Music 2"}`); code != http.StatusOK || p.Volume != 5 {
		t.Fatalf("rename dropped the level: %d %+v", code, p)
	}
	r := httptest.NewRequest(http.MethodPatch, "/api/presets/5", strings.NewReader(`{"volume":4}`))
	w := httptest.NewRecorder()
	s.handlePresetSlot(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("volume on an empty key = %d, want 404", w.Code)
	}
}

// Wiring: the app's and the phone's recall start the watch, an in-process
// recall (the alarm sets its own level) does not.
func TestPlaySlotAppliesTheKeyVolumeOnlyForARealRequest(t *testing.T) {
	src := readRepoFile(t, "playback.go")
	i := strings.Index(src, "func (s *Server) handlePlaySlot")
	if i < 0 {
		t.Fatal("handlePlaySlot moved; move this test with it")
	}
	body := src[i:]
	j := strings.Index(body, `s.ApplyPresetVolume(slot, "app recall")`)
	if j < 0 {
		t.Fatal("the slot recall no longer applies the key's volume")
	}
	if !strings.Contains(body[max(0, j-200):j], `r.RemoteAddr != ""`) {
		t.Error("the key's volume is applied to in-process recalls too, so it would fight the alarm's level")
	}
}
