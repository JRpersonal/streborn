package oled

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Only the speaker's plain inputs get the clock; firmware services that draw
// their own titles, the TV inputs and standby keep the firmware's screen.
func TestSourceClockLabel(t *testing.T) {
	for src, want := range map[string]string{"BLUETOOTH": "Bluetooth", "AUX": "AUX"} {
		if got, ok := SourceClockLabel(src); !ok || got != want {
			t.Errorf("%s: %q %v, want %q", src, got, ok, want)
		}
	}
	for _, src := range []string{
		"", "STANDBY", "UPNP", "LOCAL_INTERNET_RADIO", "TUNEIN", "SPOTIFY", "AMAZON",
		"PANDORA", "IHEART", "DEEZER", "AIRPLAY", "PRODUCT", "LOCAL", "INVALID_SOURCE", "aux",
	} {
		if _, ok := SourceClockLabel(src); ok {
			t.Errorf("%q must keep the firmware's screen", src)
		}
	}
}

func heldStill(s *trackScreen) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holding && s.want != nil && s.want.still
}

// The input clock is held only while the speaker is on the input it was put
// up for, and every source change ends it.
func TestInputClockFollowsTheSource(t *testing.T) {
	s, p := newFakeTrack()
	// asked for while the speaker is still on STR's source: nothing
	s.setSource("UPNP", true)
	s.showSourceClock("BLUETOOTH", TrackText{Station: "Bluetooth"})
	time.Sleep(30 * time.Millisecond)
	if p.holds.Load() != 0 {
		t.Fatal("input clock on STR's own source")
	}

	s.setSource("BLUETOOTH", false)
	s.showSourceClock("BLUETOOTH", TrackText{Station: "Bluetooth"})
	waitFor(t, "the input clock", func() bool { return heldStill(s) })
	s.mu.Lock()
	got := *s.want
	s.mu.Unlock()
	if !got.Clock || got.Station != "Bluetooth" || got.Title != "" || got.Artist != "" {
		t.Fatalf("input clock %+v", got)
	}
	// late radio titles and a stale call for another input change nothing
	s.show(TrackText{Title: "Late title"})
	s.showSourceClock("AUX", TrackText{Station: "AUX"})
	time.Sleep(30 * time.Millisecond)
	s.mu.Lock()
	still := s.want != nil && s.want.Station == "Bluetooth"
	s.mu.Unlock()
	if !still || p.holds.Load() != 1 {
		t.Fatalf("the input clock was replaced: %d holds", p.holds.Load())
	}

	// standby ends it
	s.setSource("STANDBY", false)
	waitFor(t, "the release on standby", func() bool { return !p.held.Load() })
	s.showSourceClock("BLUETOOTH", TrackText{Station: "Bluetooth"}) // late
	time.Sleep(30 * time.Millisecond)
	if p.held.Load() {
		t.Fatal("input clock in standby")
	}

	// a firmware service ends it
	s.setSource("AUX", false)
	s.showSourceClock("AUX", TrackText{Station: "AUX"})
	waitFor(t, "the input clock on AUX", func() bool { return heldStill(s) })
	s.setSource("TUNEIN", false)
	waitFor(t, "the release on TuneIn", func() bool { return !p.held.Load() })

	// back on STR's source: the input clock goes, the track screen comes
	s.setSource("AUX", false)
	s.showSourceClock("AUX", TrackText{Station: "AUX"})
	waitFor(t, "the input clock on AUX again", func() bool { return heldStill(s) })
	s.setSource("UPNP", true)
	waitFor(t, "the release on UPNP", func() bool { return !p.held.Load() })
	s.show(TrackText{Station: "WDR 2", Title: "Song"})
	waitFor(t, "the track screen", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.holding && s.want != nil && !s.want.still && s.want.Title == "Song"
	})
	s.clear("test")
}

// Notices, the volume bar and a cleared setting win over the input clock as
// they do over the track screen.
func TestInputClockYields(t *testing.T) {
	s, p := newFakeTrack()
	s.setSource("BLUETOOTH", false)
	s.showSourceClock("BLUETOOTH", TrackText{Station: "Bluetooth"})
	waitFor(t, "the input clock", p.held.Load)

	p.notice.Store(true)
	s.yield("notice")
	time.Sleep(trackYieldPause + 50*time.Millisecond)
	if p.held.Load() {
		t.Fatal("input clock over a notice")
	}
	p.notice.Store(false)
	waitFor(t, "the input clock after the notice", p.held.Load)

	s.pause(40*time.Millisecond, "volume")
	if p.held.Load() {
		t.Fatal("pause returned with the panel held")
	}
	waitFor(t, "the input clock after the volume bar", p.held.Load)

	s.clear("setting off")
	if p.held.Load() {
		t.Fatal("clear returned with the panel held")
	}
	time.Sleep(30 * time.Millisecond)
	if p.held.Load() {
		t.Fatal("input clock came back after the clear")
	}
}

// The input clock stands still: no playing symbol, redrawn only at the
// minute (and the layout shift), and the play status does not redraw it.
func TestInputClockStandsStill(t *testing.T) {
	now := time.Date(2026, 10, 13, 15, 42, 20, 0, time.UTC)
	s := newTrackScreen()
	s.now = func() time.Time { return now }
	s.shiftEvery = 3 * time.Minute
	s.want = &TrackText{Station: "AUX", Clock: true, still: true}
	if d := s.nextDraw(); d != 40*time.Second+trackClockLate {
		t.Errorf("playing input clock: next draw %v, want the minute", d)
	}

	s2, p := newFakeTrack()
	s2.setSource("AUX", false)
	s2.showSourceClock("AUX", TrackText{Station: "AUX"})
	waitFor(t, "the input clock", p.held.Load)
	time.Sleep(20 * time.Millisecond)
	before := p.draws.Load()
	s2.setPlaying(false)
	s2.setPlaying(true)
	time.Sleep(30 * time.Millisecond)
	if d := p.draws.Load(); d != before {
		t.Errorf("play status redrew the input clock: %d draws, want %d", d, before)
	}
	s2.clear("test")
}

// The input clock is the clock-alone screen without the playing symbol: the
// label on top, the time large in the middle, nothing in the symbol's corner,
// in the plain render and through the cache alike.
func TestRenderInputClock(t *testing.T) {
	at := time.Date(2026, 10, 13, 15, 42, 0, 0, time.Local)
	playing := trackAnim{playing: true, frame: 3}
	in := TrackText{Station: "Bluetooth", Clock: true, still: true}.sanitized()
	plain := TrackText{Station: "Bluetooth", Clock: true}.sanitized()
	corner := func(buf []byte) bool {
		f := trackLayout(plain, at)
		for y := f.barsBottom - barsMaxH; y < f.barsBottom; y++ {
			for x := 1; x < 1+barsW; x++ {
				if buf[y*Width+x] != 0 {
					return true
				}
			}
		}
		return false
	}
	buf := make([]byte, FrameSize)
	renderTrack(plain, at, playing, 0, 0, 0, buf)
	if !corner(buf) {
		t.Fatal("control: the clock-alone screen draws the playing symbol")
	}
	renderTrack(in, at, playing, 0, 0, 0, buf)
	if corner(buf) {
		t.Error("input clock draws the playing symbol")
	}
	first, last, bad := litRows(buf)
	if bad || first != trackTopY || last-plainAreaY() < smoothClock.capH || last >= Height {
		t.Errorf("input clock rows %d..%d bad=%v", first, last, bad)
	}
	var tc trackCache
	tc.prepare(in, at, 0, 0)
	cached := make([]byte, FrameSize)
	tc.render(playing, 0, cached)
	if string(cached) != string(buf) {
		t.Error("cached input clock differs from the plain render")
	}
}

// Renders the input clock to PNG, like TestRenderDisplayPNG:
//
//	STR_DISPLAY_PNG_DIR=$PWD/docs/images/display/input go test ./internal/oled -run InputClockPNG
func TestRenderInputClockPNG(t *testing.T) {
	dir := os.Getenv("STR_DISPLAY_PNG_DIR")
	if dir == "" {
		t.Skip("STR_DISPLAY_PNG_DIR not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 15, 42, 0, 0, time.Local)
	playing := trackAnim{playing: true, frame: 3}
	for _, c := range []struct {
		name   string
		label  string
		hour12 bool
		sleep  int
	}{
		{"bluetooth", "Bluetooth", false, 0},
		{"aux", "AUX", false, 0},
		{"bluetooth-12h", "Bluetooth", true, 0},
		{"aux-sleep-30", "AUX", false, 30},
	} {
		tt := TrackText{Station: c.label, Clock: true, Hour12: c.hour12, still: true}.sanitized()
		tt.sleepMin = c.sleep
		buf := make([]byte, FrameSize)
		renderTrack(tt, at, playing, 0, 0, 0, buf)
		writePNG(t, filepath.Join(dir, "input-clock-"+c.name+".png"), buf, 1)
		writePNG(t, filepath.Join(dir, "input-clock-"+c.name+"@3x.png"), buf, 3)
	}
}

// A clear of the input clock decided from a source read a moment ago must not
// take down the screen of the source the speaker moved on to: the radio screen
// after a quick Bluetooth and back stays, with the clock off (nothing was up
// on Bluetooth) and with the clock on (the input clock was up, then left).
func TestClearSourceClockOnlyEndsItsOwnInput(t *testing.T) {
	for _, clockOn := range []bool{false, true} {
		s, p := newFakeTrack()
		s.setSource("UPNP", true)
		s.show(TrackText{Station: "WDR 2", Title: "Song"})
		waitFor(t, "the track screen", p.held.Load)
		s.setSource("BLUETOOTH", false)
		if clockOn {
			s.showSourceClock("BLUETOOTH", TrackText{Station: "Bluetooth"})
			waitFor(t, "the input clock", func() bool { return heldStill(s) })
		}
		s.setSource("UPNP", true)
		s.show(TrackText{Station: "WDR 2", Title: "Song"})
		waitFor(t, "the track screen back", func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.holding && s.want != nil && !s.want.still
		})
		// the late clear from the Bluetooth refresh
		s.clearSourceClock("BLUETOOTH", "nothing to show")
		time.Sleep(30 * time.Millisecond)
		s.mu.Lock()
		kept := s.holding && s.want != nil && s.want.Title == "Song"
		s.mu.Unlock()
		if !kept {
			t.Fatalf("clock %v: a late input clear took the radio screen down", clockOn)
		}
		// on the input itself it does end the input clock
		s.setSource("AUX", false)
		s.showSourceClock("AUX", TrackText{Station: "AUX"})
		waitFor(t, "the input clock on AUX", func() bool { return heldStill(s) })
		s.clearSourceClock("BLUETOOTH", "stale")
		time.Sleep(30 * time.Millisecond)
		if !heldStill(s) {
			t.Fatalf("clock %v: a clear for another input ended the AUX clock", clockOn)
		}
		s.clearSourceClock("AUX", "setting off")
		waitFor(t, "the release", func() bool { return !p.held.Load() })
		s.mu.Lock()
		gone := s.want == nil
		s.mu.Unlock()
		if !gone {
			t.Fatalf("clock %v: input clock still wanted after its clear", clockOn)
		}
	}
}
