package oled

import (
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWrapText(t *testing.T) {
	cases := []struct {
		in       string
		cols, n  int
		want     []string
		wantsCut bool
	}{
		{"", 10, 2, nil, false},
		{"HELLO", 10, 2, []string{"HELLO"}, false},
		{"HELLO WORLD", 10, 2, []string{"HELLO", "WORLD"}, false},
		{"A B C", 3, 2, []string{"A B", "C"}, false},
		// a word longer than a line is split, not dropped
		{"ABCDEFGHIJKL", 10, 2, []string{"ABCDEFGHIJ", "KL"}, false},
		// too long: the last line ends in "..." within the width
		{"ONE TWO THREE FOUR FIVE", 9, 2, []string{"ONE TWO", "THREE..."}, true},
		{"ABCDEFGHIJ KLMNOPQRST UVW", 10, 2, []string{"ABCDEFGHIJ", "KLMNOPQ..."}, true},
	}
	for _, c := range cases {
		got, cut := wrapText(c.in, c.cols, c.n)
		if !reflect.DeepEqual(got, c.want) || cut != c.wantsCut {
			t.Errorf("wrapText(%q, %d, %d) = %q cut=%v, want %q cut=%v", c.in, c.cols, c.n, got, cut, c.want, c.wantsCut)
		}
		for _, l := range got {
			if len(l) > c.cols {
				t.Errorf("wrapText(%q): line %q wider than %d", c.in, l, c.cols)
			}
		}
	}
}

func TestLayoutTrack(t *testing.T) {
	// a short title is drawn big
	l := layoutTrack(TrackText{Station: "RADIO BOB", Artist: "AC/DC", Title: "THUNDERSTRUCK"}.sanitized())
	if l.big {
		t.Errorf("a 13-letter word must not be split across big lines: %+v", l)
	}
	l = layoutTrack(TrackText{Artist: "QUEEN", Title: "Under Pressure"}.sanitized())
	if !l.big || !reflect.DeepEqual(l.title, []string{"UNDER", "PRESSURE"}) {
		t.Errorf("short title: %+v", l)
	}
	// a long one is small, wrapped and cut, with the artist above it
	long := TrackText{
		Station: "A station name that is far too long for one line",
		Artist:  "Nick Cave & The Bad Seeds",
		Title:   "Into My Arms (Live at the Royal Albert Hall, remastered edition 2024)",
	}.sanitized()
	l = layoutTrack(long)
	if l.big || len(l.title) != 3 || len(l.artist) != 2 {
		t.Fatalf("long text: %+v", l)
	}
	if !strings.HasSuffix(l.title[2], "...") || !strings.HasSuffix(l.station, "...") {
		t.Errorf("cut text must end in ...: %+v", l)
	}
	if len(l.station) > smallCols {
		t.Errorf("station %q wider than a line", l.station)
	}
	if l.artist[0] != "NICK CAVE + THE BAD" || l.artist[1] != "SEEDS" {
		t.Errorf("artist %q", l.artist)
	}
	// title only (the "artist" and "title" modes): up to four small lines
	l = layoutTrack(TrackText{Title: strings.Repeat("WORD ", 30)})
	if len(l.title) != 4 || len(l.artist) != 0 {
		t.Errorf("title only: %+v", l)
	}
}

// litRows reports the first and last row with a lit pixel, and whether any
// pixel is outside 0..15.
func litRows(buf []byte) (first, last int, bad bool) {
	first, last = -1, -1
	for i, v := range buf {
		if v > 15 {
			bad = true
		}
		if v != 0 {
			y := i / Width
			if first < 0 {
				first = y
			}
			last = y
		}
	}
	return first, last, bad
}

func TestRenderTrack(t *testing.T) {
	buf := make([]byte, FrameSize)
	tt := TrackText{Station: "Radio", Artist: "Queen", Title: "Under Pressure"}.sanitized()
	renderTrack(tt, 0, 0, buf)
	first, last, bad := litRows(buf)
	if bad || first != trackTopY || last >= Height {
		t.Fatalf("rows %d..%d bad=%v", first, last, bad)
	}
	// the title is the brightest text, the station the dimmest
	if v := buf[trackTopY*Width+Width/2-20 : (trackTopY+7)*Width]; maxByte(v) != lvlTrackStation {
		t.Errorf("station level %d", maxByte(v))
	}
	if maxByte(buf) != lvlTrackTitle {
		t.Errorf("title level %d", maxByte(buf))
	}
	// the shifted layout is the same picture one pixel to the right and down
	moved := make([]byte, FrameSize)
	renderTrack(tt, 1, 1, moved)
	for y := 0; y < Height-1; y++ {
		for x := 0; x < Width-1; x++ {
			if buf[y*Width+x] != moved[(y+1)*Width+x+1] {
				t.Fatalf("shifted frame differs at %d,%d", x, y)
			}
		}
	}
	// nothing to show draws nothing
	renderTrack(TrackText{}, 0, 0, buf)
	if f, _, _ := litRows(buf); f != -1 {
		t.Errorf("empty text lit row %d", f)
	}
	// the longest layout stays on the panel
	renderTrack(TrackText{Station: strings.Repeat("S", 40), Artist: strings.Repeat("A ", 40), Title: strings.Repeat("T ", 80)}, 1, 1, buf)
	if _, l, _ := litRows(buf); l >= Height-1 {
		t.Errorf("longest layout reaches row %d", l)
	}
}

func maxByte(b []byte) byte {
	var m byte
	for _, v := range b {
		m = max(m, v)
	}
	return m
}

func TestTrackShiftWalks(t *testing.T) {
	seen := map[[2]int]bool{}
	for n := 0; n < 4; n++ {
		dx, dy := trackShift(n)
		if dx < 0 || dx > 1 || dy < 0 || dy > 1 {
			t.Fatalf("shift %d = %d,%d", n, dx, dy)
		}
		seen[[2]int{dx, dy}] = true
	}
	if len(seen) != 4 {
		t.Errorf("shift visits %d corners, want 4", len(seen))
	}
}

// fakePanel stands in for the panel lock and the held flip thread.
type fakePanel struct {
	mu      sync.Mutex
	locked  bool
	holds   atomic.Int32 // holds started
	held    atomic.Bool
	draws   atomic.Int32
	notice  atomic.Bool
	game    atomic.Bool
	fail    atomic.Bool
	lastTxt atomic.Value
}

func newFakeTrack() (*trackScreen, *fakePanel) {
	p := &fakePanel{}
	s := newTrackScreen()
	s.supported = func() bool { return true }
	s.ready = func() bool { return true }
	s.gameActive = p.game.Load
	s.noticeBusy = p.notice.Load
	s.tryLock = func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.locked {
			return false
		}
		p.locked = true
		return true
	}
	s.unlock = func() { p.mu.Lock(); p.locked = false; p.mu.Unlock() }
	s.retry = 5 * time.Millisecond
	s.log = func() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
	s.hold = func(draw func([]byte), redraw, quit <-chan struct{}, _ time.Duration) error {
		p.holds.Add(1)
		if p.fail.Load() {
			return errors.New("seize: operation not permitted")
		}
		p.held.Store(true)
		defer p.held.Store(false)
		buf := make([]byte, FrameSize)
		for {
			draw(buf)
			p.draws.Add(1)
			s.mu.Lock()
			if s.want != nil {
				p.lastTxt.Store(s.want.Title)
			}
			s.mu.Unlock()
			select {
			case <-quit:
				return nil
			case <-redraw:
			}
		}
	}
	return s, p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestTrackScreenShowUpdateClear(t *testing.T) {
	s, p := newFakeTrack()
	s.show(TrackText{Station: "Radio", Artist: "Queen", Title: "Under Pressure"})
	waitFor(t, "the panel to be held", p.held.Load)
	// a new song redraws without letting go of the panel
	s.show(TrackText{Station: "Radio", Artist: "Queen", Title: "Bohemian Rhapsody"})
	waitFor(t, "the redraw", func() bool { v, _ := p.lastTxt.Load().(string); return v == "BOHEMIAN RHAPSODY" })
	if p.holds.Load() != 1 {
		t.Errorf("a text change took the panel again: %d holds", p.holds.Load())
	}
	// the same text again draws nothing
	before := p.draws.Load()
	s.show(TrackText{Station: "Radio", Artist: "Queen", Title: "Bohemian Rhapsody"})
	time.Sleep(20 * time.Millisecond)
	if p.draws.Load() != before {
		t.Errorf("identical text was drawn again")
	}
	s.clear("standby")
	if p.held.Load() || TrackShowingOf(s) {
		t.Fatal("clear returned with the panel still held")
	}
	p.mu.Lock()
	locked := p.locked
	p.mu.Unlock()
	if locked {
		t.Error("clear left the panel lock taken")
	}
	// and it stays off
	time.Sleep(20 * time.Millisecond)
	if p.holds.Load() != 1 {
		t.Errorf("panel taken again after clear: %d holds", p.holds.Load())
	}
}

// TrackShowingOf is TrackShowing for a test instance.
func TrackShowingOf(s *trackScreen) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holding
}

func TestTrackScreenEmptyTextClears(t *testing.T) {
	s, p := newFakeTrack()
	s.show(TrackText{Title: "Song"})
	waitFor(t, "the panel to be held", p.held.Load)
	// a title the font cannot draw at all is nothing to show
	s.show(TrackText{Title: "♪♪"})
	waitFor(t, "the release", func() bool { return !p.held.Load() })
}

func TestTrackScreenYieldsToNoticesAndReturns(t *testing.T) {
	s, p := newFakeTrack()
	s.show(TrackText{Title: "Song"})
	waitFor(t, "the panel to be held", p.held.Load)
	// a notice asks: the track screen lets go at once and stays off while the
	// notice is showing or waiting
	p.notice.Store(true)
	s.yield("notice")
	if p.held.Load() {
		t.Fatal("yield returned with the panel held")
	}
	time.Sleep(trackYieldPause + 50*time.Millisecond)
	if p.held.Load() {
		t.Fatal("track screen took the panel while a notice was showing")
	}
	// the notice ends: the track screen comes back by itself
	p.notice.Store(false)
	waitFor(t, "the track screen to return", p.held.Load)
	if p.holds.Load() != 2 {
		t.Errorf("holds %d, want 2", p.holds.Load())
	}
	s.clear("test")
}

func TestTrackScreenWaitsForPanelLock(t *testing.T) {
	s, p := newFakeTrack()
	p.mu.Lock()
	p.locked = true // the splash holds the panel
	p.mu.Unlock()
	s.show(TrackText{Title: "Song"})
	time.Sleep(30 * time.Millisecond)
	if p.held.Load() {
		t.Fatal("took a panel that was in use")
	}
	s.unlock()
	waitFor(t, "the panel once free", p.held.Load)
	s.clear("test")
}

func TestTrackScreenGameKeepsItOff(t *testing.T) {
	s, p := newFakeTrack()
	p.game.Store(true)
	s.show(TrackText{Title: "Song"})
	time.Sleep(30 * time.Millisecond)
	if p.holds.Load() != 0 {
		t.Fatal("took the panel during a game round")
	}
	p.game.Store(false)
	waitFor(t, "the panel after the round", p.held.Load)
	s.clear("test")
}

func TestTrackScreenPause(t *testing.T) {
	s, p := newFakeTrack()
	s.show(TrackText{Title: "Song"})
	waitFor(t, "the panel to be held", p.held.Load)
	start := time.Now()
	s.pause(60*time.Millisecond, "volume")
	if p.held.Load() {
		t.Fatal("pause returned with the panel held")
	}
	waitFor(t, "the return after the pause", p.held.Load)
	if d := time.Since(start); d < 60*time.Millisecond {
		t.Errorf("back after %v, before the pause ended", d)
	}
	s.clear("test")
}

func TestTrackScreenBacksOffWhenThePanelCannotBeTaken(t *testing.T) {
	s, p := newFakeTrack()
	p.fail.Store(true)
	s.show(TrackText{Title: "Song"})
	waitFor(t, "the first attempt", func() bool { return p.holds.Load() >= 1 })
	time.Sleep(30 * time.Millisecond)
	// retry is 5 ms; the back-off after a failure is ten times that and more
	if n := p.holds.Load(); n > 2 {
		t.Errorf("%d attempts in 30 ms, no back-off", n)
	}
	s.mu.Lock()
	fails, holding := s.failures, s.holding
	s.mu.Unlock()
	if fails < 1 || holding {
		t.Errorf("failures %d holding %v", fails, holding)
	}
	p.mu.Lock()
	locked := p.locked
	p.mu.Unlock()
	if locked {
		t.Error("a failed attempt kept the panel lock")
	}
	s.clear("test")
}

func TestTrackScreenFollowsTheSource(t *testing.T) {
	s, p := newFakeTrack()
	s.show(TrackText{Title: "Song"})
	waitFor(t, "the panel to be held", p.held.Load)
	// the speaker switches to AUX: the track goes, and does not return
	s.setSource("AUX", false)
	waitFor(t, "the release", func() bool { return !p.held.Load() })
	s.show(TrackText{Title: "Late title from the old stream"})
	time.Sleep(30 * time.Millisecond)
	if p.holds.Load() != 1 {
		t.Fatalf("titles reached the panel on a foreign source: %d holds", p.holds.Load())
	}
	// back on STR's source: titles show again
	s.setSource("UPNP", true)
	s.show(TrackText{Title: "Next Song"})
	waitFor(t, "the panel again", p.held.Load)
	s.clear("test")
}

func TestTrackClearWithoutHoldIsQuiet(t *testing.T) {
	s, _ := newFakeTrack()
	s.clear("standby") // nothing wanted, nothing held: no block, no panic
	s.pause(time.Second, "volume")
	s.yield("notice")
}
