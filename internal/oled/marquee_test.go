package oled

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMarqueeAtCycle(t *testing.T) {
	// nothing scrolls: the start, and no redraw asked for
	if st, nx := marqueeAt(5*time.Second, 0); st != 0 || nx != 0 {
		t.Fatalf("fits: step %d next %v", st, nx)
	}
	const n = 10
	period := marqueeHoldStart + n*marqueeStep + marqueeHoldEnd
	for _, c := range []struct {
		e    time.Duration
		step int
		next time.Duration
	}{
		{0, 0, marqueeHoldStart}, // the start stands
		{marqueeHoldStart - 100*time.Millisecond, 0, 100 * time.Millisecond},                     // still standing
		{marqueeHoldStart, 1, marqueeStep},                                                       // the first step
		{marqueeHoldStart + marqueeStep/2, 1, marqueeStep / 2},                                   // between steps
		{marqueeHoldStart + 4*marqueeStep, 5, marqueeStep},                                       // half way
		{marqueeHoldStart + (n-1)*marqueeStep, n, period - marqueeHoldStart - (n-1)*marqueeStep}, // the end, then the pause
		{period - time.Millisecond, n, time.Millisecond},                                         // the end still stands
		{period, 0, marqueeHoldStart},                                                            // jumped back to the start
		{period + marqueeHoldStart, 1, marqueeStep},                                              // and scrolls again
	} {
		st, nx := marqueeAt(c.e, n)
		if st != c.step || nx != c.next {
			t.Errorf("at %v: step %d next %v, want %d %v", c.e, st, nx, c.step, c.next)
		}
	}
	// a single step: straight from the start pause to the end pause
	if st, nx := marqueeAt(marqueeHoldStart, 1); st != 1 || nx != marqueeStep+marqueeHoldEnd {
		t.Errorf("one step: %d %v", st, nx)
	}
}

func TestMarqueeLine(t *testing.T) {
	fit := marqueeLine{s: "UNDER", sc: 2, lvl: 15, x0: blockBoxX, w: blockBoxW}
	if fit.steps() != 0 {
		t.Fatalf("a fitting line scrolls: %d steps", fit.steps())
	}
	a, b := make([]byte, FrameSize), make([]byte, FrameSize)
	fit.draw(canvas(a), 0, 0, 0)
	fit.draw(canvas(b), 7, 0, 0)
	if string(a) != string(b) {
		t.Error("a fitting line moved with the marquee")
	}
	long := marqueeLine{s: "THUNDERSTRUCK", sc: 2, lvl: 15, x0: blockBoxX, w: blockBoxW}
	// 13 characters at 12 px = 154 px in a 126 px box: 28 px, 14 steps of 2
	if long.overflow() != 154-blockBoxW || long.steps() != (154-blockBoxW+1)/2 {
		t.Fatalf("overflow %d steps %d", long.overflow(), long.steps())
	}
	for _, step := range []int{0, 5, long.steps(), long.steps() + 20} {
		clear(a)
		long.draw(canvas(a), step, 0, 0)
		left, right := Width, -1
		for i, v := range a {
			if v != 0 {
				left, right = min(left, i%Width), max(right, i%Width)
			}
		}
		if left < blockBoxX || right >= blockBoxX+blockBoxW {
			t.Errorf("step %d: drawn %d..%d outside the box", step, left, right)
		}
		switch {
		case step == 0 && left != blockBoxX:
			t.Errorf("start: first column %d", left)
		case step >= long.steps() && right < blockBoxX+blockBoxW-2:
			// the last "K" ends flush with the box
			t.Errorf("end: last column %d", right)
		}
	}
}

func TestTrackLayoutScrollsOnlyWhatDoesNotFit(t *testing.T) {
	at := time.Date(2026, 10, 14, 21, 5, 0, 0, time.Local)
	short := TrackText{Station: "1LIVE", Artist: "QUEEN", Title: "UNDER PRESSURE", Clock: true}.sanitized()
	if n := trackLayout(short, at).steps(); n != 0 {
		t.Errorf("everything fits but %d steps", n)
	}
	for _, tt := range []TrackText{
		{Station: "A STATION NAME FAR TOO LONG", Title: "X"},
		{Station: "A STATION NAME", Title: "X", Clock: true},
		{Artist: strings.Repeat("ARTIST ", 8), Title: "X"},
		{Title: "INTO MY ARMS (LIVE AT THE ROYAL ALBERT HALL)"},
		{Station: strings.Repeat("S", 30), Clock: true},
	} {
		f := trackLayout(tt, at)
		if f.steps() == 0 {
			t.Errorf("%+v does not scroll", tt)
		}
		// every frame of the cycle stays on the panel and inside 0..15
		buf := make([]byte, FrameSize)
		for step := 0; step <= f.steps(); step += 3 {
			f.render(trackAnim{playing: true}, step, 1, 1, buf)
			if _, l, bad := litRows(buf); bad || l >= Height-1 {
				t.Fatalf("%+v step %d: last row %d bad=%v", tt, step, l, bad)
			}
		}
	}
	// the station never runs into the playing symbol or the time
	f := trackLayout(TrackText{Station: strings.Repeat("W", 40), Title: "X", Clock: true}.sanitized(), at)
	buf := make([]byte, FrameSize)
	for step := 0; step <= f.steps(); step++ {
		f.render(trackAnim{}, step, 0, 0, buf)
		for y := 0; y < trackTopY+14; y++ {
			if buf[y*Width+stationBoxX-1] != 0 {
				t.Fatalf("step %d: the station touches the bars at row %d", step, y)
			}
		}
	}
}

// The screen asks for marquee frames only while a line moves and the speaker
// plays, and stops asking when the text fits, playback pauses or the screen
// goes.
func TestTrackScreenMarqueeTicker(t *testing.T) {
	now := time.Date(2026, 10, 14, 21, 5, 10, 0, time.UTC)
	s := newTrackScreen()
	s.now = func() time.Time { return now }
	s.shiftEvery = 3 * time.Minute
	s.want = &TrackText{Station: "RADIO", Title: "INTO MY ARMS (LIVE AT THE ROYAL ALBERT HALL)"}
	buf := make([]byte, FrameSize)
	s.drawn = true

	// the start stands: the next marquee change is the end of the pause
	s.draw(buf)
	if got := s.marqueeNext.Sub(now); got != marqueeHoldStart {
		t.Fatalf("start pause: next %v", got)
	}
	start := append([]byte(nil), buf...)
	// in the scroll: a frame every step, the bars are slower
	now = now.Add(marqueeHoldStart + 3*marqueeStep)
	s.draw(buf)
	if d := s.nextDraw(); d != marqueeStep {
		t.Errorf("scrolling: next draw in %v", d)
	}
	if string(buf) == string(start) {
		t.Error("the title did not move")
	}
	if s.scrollDraws != 1 {
		t.Errorf("scroll draws %d", s.scrollDraws)
	}
	// paused: the start again, and no marquee frame asked for
	s.setPlaying(false)
	s.draw(buf)
	if !s.marqueeNext.IsZero() {
		t.Errorf("paused: marquee still wants %v", s.marqueeNext)
	}
	if d := s.nextDraw(); d < 10*time.Second {
		t.Errorf("paused: next draw in %v", d)
	}
	if string(buf[trackAreaY*Width:]) != string(start[trackAreaY*Width:]) {
		t.Error("paused: the title is not at its start")
	}
	// playing again: the cycle starts over with the start pause
	s.setPlaying(true)
	s.draw(buf)
	if got := s.marqueeNext.Sub(now); got != marqueeHoldStart {
		t.Errorf("resumed: next %v", got)
	}
	// a title that fits: nothing scrolls, only the playing symbol moves
	s.want = &TrackText{Station: "RADIO", Title: "UNDER PRESSURE"}
	s.draw(buf)
	if !s.marqueeNext.IsZero() {
		t.Errorf("fitting text: marquee wants %v", s.marqueeNext)
	}
	if d := s.nextDraw(); d > barsFrame {
		t.Errorf("fitting text: next draw in %v", d)
	}
	// the screen gone: nothing moves any more
	s.want = &TrackText{Title: "INTO MY ARMS (LIVE AT THE ROYAL ALBERT HALL)"}
	now = now.Add(marqueeHoldStart + marqueeStep)
	s.draw(buf)
	s.clear("test")
	if d := s.nextDraw(); d < 10*time.Second {
		t.Errorf("cleared: next draw in %v", d)
	}
}

// Renders the start, a middle and the end of a scrolling title to PNG so the
// result can be looked at: in a temp dir, or in $STR_TRACK_PNG_DIR when set.
func TestRenderMarqueeFramesPNG(t *testing.T) {
	dir := os.Getenv("STR_TRACK_PNG_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	at := time.Date(2026, 10, 14, 21, 5, 0, 0, time.Local)
	tt := TrackText{Station: "TROPICAL HOUSE", Artist: "Nick Cave & The Bad Seeds and Friends", Title: "Into My Arms (Live at the Royal Albert Hall)", Clock: true}.sanitized()
	f := trackLayout(tt, at)
	n := f.steps()
	buf := make([]byte, FrameSize)
	for name, step := range map[string]int{"start": 0, "middle": n / 2, "end": n} {
		f.render(trackAnim{playing: true, frame: step}, step, 0, 0, buf)
		png := ScreenPNG(buf)
		if png == nil {
			t.Fatal("no PNG")
		}
		if err := os.WriteFile(filepath.Join(dir, "marquee-"+name+".png"), png, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
