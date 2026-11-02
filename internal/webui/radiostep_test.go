package webui

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/presets"
)

func radio(slot int) presets.Preset {
	return presets.Preset{Slot: slot, Name: "Station", Type: "radio", StreamURL: "http://radio.example/s"}
}

func TestNextRadioSlotStepsOverEverythingButRadio(t *testing.T) {
	all := []presets.Preset{
		radio(1),
		{Slot: 2, Type: "spotify", URI: "spotify:playlist:x"},
		radio(3),
		{Slot: 4, Type: "queue", Items: []presets.PresetItem{{}}},
		{Slot: 5, Type: "radio", Source: "NAS", StreamURL: "http://nas.example/song.flac"},
		// 6 empty
		radio(8), // 2x2
		{Slot: 9, Type: presets.TypeNative, Native: &presets.NativeItem{Source: "PANDORA"}},
		radio(12), // 2x6
	}
	for _, c := range []struct {
		from    int
		forward bool
		want    int
	}{
		{1, true, 3},
		{3, true, 8},
		{8, true, 12},
		{12, true, 1}, // wraps
		{1, false, 12},
		{12, false, 8},
		{8, false, 3},
	} {
		if got := nextRadioSlot(all, c.from, c.forward); got != c.want {
			t.Errorf("from %d forward=%v: %d, want %d", c.from, c.forward, got, c.want)
		}
	}
	if got := nextRadioSlot([]presets.Preset{radio(4)}, 4, true); got != 0 {
		t.Errorf("a single radio key has nothing to step to, got %d", got)
	}
}

// One press reported by both the gabbo skip frame and the key trace steps
// once; two presses step twice whatever order their reports arrive in.
func TestRadioStepPairsTheTwoReportsOfOnePress(t *testing.T) {
	var st radioStepState
	t0 := time.Now()
	steps := 0
	for _, a := range []struct {
		via radioStepVia
		at  time.Duration
	}{
		{radioStepViaSkipFrame, 0},                     // press 1
		{radioStepViaKeyTrace, 200 * time.Millisecond}, // press 1, the other report
		{radioStepViaKeyTrace, 900 * time.Millisecond}, // press 2, trace first this time
		{radioStepViaSkipFrame, 1000 * time.Millisecond},
	} {
		if !st.samePress(a.via, t0.Add(a.at)) {
			steps++
		}
	}
	if steps != 2 {
		t.Fatalf("two presses stepped %d times, want 2", steps)
	}
	// A speaker that sends only one of the two signals steps on every press.
	var one radioStepState
	steps = 0
	for i := 0; i < 3; i++ {
		if !one.samePress(radioStepViaKeyTrace, t0.Add(time.Duration(i)*300*time.Millisecond)) {
			steps++
		}
	}
	if steps != 3 {
		t.Fatalf("three presses on one signal stepped %d times, want 3", steps)
	}
}

type radioStepFake struct {
	mu     sync.Mutex
	played []int
}

func newRadioStepServer(t *testing.T, playing int, source, location string) (*Server, *radioStepFake) {
	t.Helper()
	store := presets.New()
	for _, p := range []presets.Preset{radio(1), {Slot: 2, Type: "spotify", URI: "spotify:playlist:x"}, radio(3), radio(7)} {
		_ = store.SetSlot(p) // no path: kept in memory
	}
	f := &radioStepFake{}
	s := &Server{
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		presets:          store,
		queue:            newPlayQueue(),
		boxNowReadFn:     scriptNowPlaying(source, "PLAY_STATE", location),
		spotifySkip:      func(context.Context, bool) error { return nil },
		spotifyStreaming: func() bool { return false },
	}
	s.radioStepPlayFn = func(_ context.Context, slot int) error {
		f.mu.Lock()
		f.played = append(f.played, slot)
		f.mu.Unlock()
		// what playSlot records for a radio key
		s.play.record(&lastPlayInfo{boxURL: boxurl.StreamSlot(slot)})
		return nil
	}
	if playing > 0 {
		s.play.record(&lastPlayInfo{boxURL: boxurl.StreamSlot(playing)})
	}
	return s, f
}

func TestHardwareSkipOnARadioKeyStepsToTheNextRadioKey(t *testing.T) {
	s, f := newRadioStepServer(t, 1, "UPNP", boxurl.StreamSlot(1))
	src, err := s.HardwareSkip(context.Background(), true)
	if err != nil || src != "radio-step" {
		t.Fatalf("src=%q err=%v, want a radio step", src, err)
	}
	// The speaker follows each step, so its location is the key just played.
	s.boxNowReadFn = func(ctx context.Context) (*boxapi.NowPlaying, error) {
		return scriptNowPlaying("UPNP", "PLAY_STATE", s.play.currentURL())(ctx)
	}
	// Next from 1 skips the Spotify key 2 and lands on 3; Previous from 3
	// goes back to 1; Previous from 1 wraps to the second row. The presses
	// are seconds apart, so none of them is taken for the other signal's
	// report of the one before (the skip frame of the first press is still
	// unpaired).
	later := time.Now().Add(5 * time.Second)
	s.radioStep.step(context.Background(), s, false, radioStepViaKeyTrace, later)
	s.radioStep.step(context.Background(), s, false, radioStepViaKeyTrace, later.Add(5*time.Second))
	f.mu.Lock()
	defer f.mu.Unlock()
	if want := []int{3, 1, 7}; len(f.played) != 3 || f.played[0] != want[0] || f.played[1] != want[1] || f.played[2] != want[2] {
		t.Fatalf("played %v, want %v", f.played, want)
	}
}

// Off the radio keys the remote keeps skipping songs: a station from the
// search, or a speaker that moved on to AUX, is not stepped.
func TestRadioStepStaysOutOfOtherSources(t *testing.T) {
	for name, c := range map[string]struct {
		playing          int
		source, location string
	}{
		"nothing played":    {0, "UPNP", ""},
		"spotify key":       {2, "UPNP", boxurl.StreamSlot(2)},
		"speaker on AUX":    {1, "AUX", ""},
		"another stream":    {1, "UPNP", "http://other.example/x.mp3"},
		"speaker not there": {1, "", ""},
	} {
		s, f := newRadioStepServer(t, c.playing, c.source, c.location)
		if _, handled := s.stepRadioPreset(context.Background(), true); handled {
			t.Errorf("%s: the radio step answered", name)
		}
		if len(f.played) != 0 {
			t.Errorf("%s: played %v", name, f.played)
		}
	}
}

// A station the speaker plays natively, and a source the speaker tore down on
// its own failed skip, are still the radio key STR played.
func TestRadioStepAcceptsNativeAndTornDownSources(t *testing.T) {
	for _, src := range []string{"LOCAL_INTERNET_RADIO", "INVALID_SOURCE"} {
		s, f := newRadioStepServer(t, 3, src, "")
		if _, handled := s.stepRadioPreset(context.Background(), true); !handled {
			t.Errorf("%s: not stepped", src)
		}
		if len(f.played) != 1 || f.played[0] != 7 {
			t.Errorf("%s: played %v, want [7]", src, f.played)
		}
	}
}
