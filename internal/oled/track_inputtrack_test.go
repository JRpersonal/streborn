package oled

import (
	"testing"
	"time"
)

// The track screen on an input (the phone's song on Bluetooth) follows the
// rules of the input clock: only while the speaker is on that input, ended by
// a source change and by the clear for that input, and it falls back to the
// clock, or to nothing, when there is no song to draw.
func TestInputTrackFollowsTheSource(t *testing.T) {
	s, p := newFakeTrack()
	want := func() TrackText {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.want == nil {
			return TrackText{}
		}
		return *s.want
	}
	song := TrackText{Station: "Bluetooth", Artist: "Queen", Title: "Under Pressure", Clock: true}

	// asked for while the speaker is on STR's source: nothing
	s.setSource("UPNP", true)
	s.showSourceTrack("BLUETOOTH", song)
	time.Sleep(30 * time.Millisecond)
	if p.holds.Load() != 0 {
		t.Fatal("input track on STR's own source")
	}

	s.setSource("BLUETOOTH", false)
	s.showSourceTrack("BLUETOOTH", song)
	waitFor(t, "the input track", func() bool { return heldStill(s) })
	if got := want(); got.Artist != "Queen" || got.Title != "Under Pressure" || got.Station != "Bluetooth" || !got.Clock {
		t.Fatalf("input track %+v", got)
	}
	// the next song redraws in the same hold
	s.showSourceTrack("BLUETOOTH", TrackText{Station: "Bluetooth", Title: "Heroes"})
	if got := want(); got.Title != "Heroes" || got.Artist != "" || got.Clock || !got.still {
		t.Fatalf("next song %+v", got)
	}
	if p.holds.Load() != 1 {
		t.Fatalf("the next song took the panel again: %d holds", p.holds.Load())
	}
	// no song any more, the clock on: the input clock
	s.showSourceTrack("BLUETOOTH", TrackText{Station: "Bluetooth", Clock: true})
	if got := want(); got.Title != "" || !got.Clock || !got.still {
		t.Fatalf("without a song %+v", got)
	}
	// nothing at all to draw: the input screen ends
	s.showSourceTrack("BLUETOOTH", TrackText{Station: "Bluetooth"})
	waitFor(t, "the release with nothing to draw", func() bool { return !p.held.Load() })

	// the clear for the input ends it, and so does a source change
	s.showSourceTrack("BLUETOOTH", song)
	waitFor(t, "the input track again", func() bool { return heldStill(s) })
	s.clearSourceClock("BLUETOOTH", "test")
	if p.held.Load() {
		t.Fatal("clear returned with the panel held")
	}
	s.showSourceTrack("BLUETOOTH", song)
	waitFor(t, "the input track a third time", func() bool { return heldStill(s) })
	s.setSource("STANDBY", false)
	waitFor(t, "the release on standby", func() bool { return !p.held.Load() })
	s.showSourceTrack("BLUETOOTH", song) // late
	time.Sleep(30 * time.Millisecond)
	if p.held.Load() {
		t.Fatal("input track in standby")
	}
}

// The input track is the track screen without the playing symbol, and it
// stands still: redrawn at the full minute for its clock, never for a symbol
// or a scrolling line.
func TestRenderInputTrack(t *testing.T) {
	at := time.Date(2026, 10, 13, 15, 42, 0, 0, time.Local)
	playing := trackAnim{playing: true, frame: 3}
	in := TrackText{Station: "Bluetooth", Artist: "Queen", Title: "Under Pressure", Clock: true, still: true}.sanitized()
	plain := TrackText{Station: "Bluetooth", Artist: "Queen", Title: "Under Pressure", Clock: true}.sanitized()
	f := trackLayout(plain, at)
	lit := func(buf []byte, x0, y0, x1, y1 int) int {
		n := 0
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if buf[y*Width+x] != 0 {
					n++
				}
			}
		}
		return n
	}
	buf, ref := make([]byte, FrameSize), make([]byte, FrameSize)
	renderTrack(plain, at, playing, 0, 0, 0, ref)
	if lit(ref, 1, f.barsBottom-barsMaxH, 1+barsW, f.barsBottom) == 0 {
		t.Fatal("control: the track screen draws the playing symbol")
	}
	renderTrack(in, at, playing, 0, 0, 0, buf)
	if lit(buf, 1, f.barsBottom-barsMaxH, 1+barsW, f.barsBottom) != 0 {
		t.Error("input track draws the playing symbol")
	}
	// everything right of the symbol's corner is the track screen as it is
	if n := lit(buf, stationBoxX, 0, Width, Height); n == 0 || n != lit(ref, stationBoxX, 0, Width, Height) {
		t.Error("input track differs from the track screen beyond the playing symbol")
	}

	s, _ := newFakeTrack()
	s.now = func() time.Time { return at.Add(20 * time.Second) }
	s.shiftEvery = 0
	s.want = &in
	if d := s.nextDraw(); d < 30*time.Second || d > time.Minute {
		t.Errorf("input track is redrawn after %v, want at the next full minute", d)
	}
}
