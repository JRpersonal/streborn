package oled

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeQueue is a notice queue on a fake clock whose panel only records what
// it was asked to show.
type fakeQueue struct {
	*noticeQueue
	mu      sync.Mutex
	clock   time.Time
	shown   []Notice
	shownAt []time.Time
	locked  bool
	during  func(n Notice) // runs while n is "on the panel"
}

func newFakeQueue() *fakeQueue {
	f := &fakeQueue{clock: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
	q := newNoticeQueue()
	q.startOnce.Do(func() {}) // drained by hand
	q.now = func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.clock }
	q.sleep = func(d time.Duration) { f.mu.Lock(); f.clock = f.clock.Add(d); f.mu.Unlock() }
	q.enabled = func() bool { return true }
	q.supported = func() bool { return true }
	q.gameActive = func() bool { return false }
	q.ready = func() bool { return true }
	q.acquire = func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.locked {
			return false
		}
		f.locked = true
		return true
	}
	q.release = func() { f.mu.Lock(); f.locked = false; f.mu.Unlock() }
	q.show = func(n Notice, _ <-chan struct{}) error {
		f.mu.Lock()
		f.shown = append(f.shown, n)
		f.shownAt = append(f.shownAt, f.clock)
		during := f.during
		f.mu.Unlock()
		if during != nil {
			during(n)
		}
		q.sleep(n.Hold)
		return nil
	}
	f.noticeQueue = q
	return f
}

func (f *fakeQueue) kinds() []NoticeKind {
	var out []NoticeKind
	for _, n := range f.shown {
		out = append(out, n.Kind)
	}
	return out
}

func TestNoticeRateLimit(t *testing.T) {
	f := newFakeQueue()
	f.post(EmptyKey(1))
	f.drain()
	f.post(EmptyKey(2))
	f.drain()
	if len(f.shown) != 2 {
		t.Fatalf("shown %v", f.kinds())
	}
	if gap := f.shownAt[1].Sub(f.shownAt[0]); gap < noticeGap {
		t.Fatalf("second notice %v after the first, want at least %v", gap, noticeGap)
	}
	// A notice longer than the gap: the next one waits for its end, not
	// just for the gap.
	f.post(Identify("KITCHEN"))
	f.drain()
	f.post(EmptyKey(3))
	f.drain()
	if gap := f.shownAt[3].Sub(f.shownAt[2]); gap < 5*time.Second {
		t.Fatalf("notice overlapped the identify notice: %v", gap)
	}
}

func TestNoticeNewestPendingWins(t *testing.T) {
	f := newFakeQueue()
	f.during = func(n Notice) {
		if n.Kind == NoticeSavedFromApp {
			f.post(EmptyKey(2))
			f.post(EmptyKey(3))
		}
	}
	f.post(SavedFromApp(1))
	f.drain()
	if len(f.shown) != 2 || f.shown[1].Key != 3 {
		t.Fatalf("want the app save and then key 3 only, got %+v", f.shown)
	}
}

func TestNoticePriority(t *testing.T) {
	f := newFakeQueue()
	f.post(Identify("X"))
	f.post(EmptyKey(4)) // less important, must not push identify out
	f.drain()
	if k := f.kinds(); len(k) != 1 || k[0] != NoticeIdentify {
		t.Fatalf("got %v", k)
	}
	f.post(EmptyKey(4))
	f.post(SavedByKey(4, "Radio")) // more important, replaces
	f.drain()
	if k := f.kinds(); len(k) != 2 || k[1] != NoticeSavedByKey {
		t.Fatalf("got %v", k)
	}
}

func TestNoticeCoalesce(t *testing.T) {
	f := newFakeQueue()
	f.during = func(n Notice) { f.post(n) } // the same notice again while it shows
	f.post(SavedByKey(2, "Jazz"))
	f.post(SavedByKey(2, "Jazz"))
	f.drain()
	if len(f.shown) != 1 {
		t.Fatalf("identical notices were not coalesced: %v", f.kinds())
	}
	// Once it is over, the same notice may show again.
	f.during = nil
	f.post(SavedByKey(2, "Jazz"))
	f.drain()
	if len(f.shown) != 2 {
		t.Fatalf("a later identical notice was dropped: %v", f.kinds())
	}
}

func TestNoticeOffUnsupportedBusy(t *testing.T) {
	f := newFakeQueue()
	f.enabled = func() bool { return false }
	f.post(SavedFromApp(1))
	f.drain()
	if len(f.shown) != 0 {
		t.Fatal("shown while switched off")
	}
	f.post(Identify("LIVING ROOM"))
	f.drain()
	if len(f.shown) != 1 {
		t.Fatal("identify must show even with notices off")
	}

	f = newFakeQueue()
	f.supported = func() bool { return false }
	f.post(Identify("X"))
	f.drain()
	if len(f.shown) != 0 {
		t.Fatal("shown on an unsupported panel")
	}

	f = newFakeQueue()
	f.gameActive = func() bool { return true }
	f.post(EmptyKey(1))
	f.drain()
	if len(f.shown) != 0 {
		t.Fatal("shown during a game")
	}

	// The splash holds the panel for a while: the notice waits for it.
	f = newFakeQueue()
	f.locked = true
	start := f.clock
	f.sleep = func(d time.Duration) {
		f.mu.Lock()
		f.clock = f.clock.Add(d)
		if f.clock.Sub(start) > 7*time.Second && f.locked && len(f.shown) == 0 {
			f.locked = false
		}
		f.mu.Unlock()
	}
	f.post(KeysRestored(3))
	f.drain()
	if len(f.shown) != 1 {
		t.Fatal("notice not shown after the splash let go")
	}
	// ...but not forever.
	f = newFakeQueue()
	f.locked = true
	f.post(KeysRestored(3))
	f.drain()
	if len(f.shown) != 0 {
		t.Fatal("notice shown although the panel never came free")
	}
}

func TestNoticeStopRequeues(t *testing.T) {
	f := newFakeQueue()
	stopped := make(chan struct{})
	f.show = func(n Notice, quit <-chan struct{}) error {
		f.mu.Lock()
		f.shown = append(f.shown, n)
		first := len(f.shown) == 1
		f.mu.Unlock()
		if first {
			<-quit
			close(stopped)
		}
		return nil
	}
	f.post(KeysRestored(2))
	go func() {
		for {
			f.noticeQueue.mu.Lock()
			showing := f.showing
			f.noticeQueue.mu.Unlock()
			if showing {
				f.stop(true)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	f.drain()
	<-stopped
	if len(f.shown) != 2 {
		t.Fatalf("interrupted notice not shown again: %v", f.kinds())
	}
}

// Every notice line is in a smooth face (the big line in the large one, the
// small lines in the small one), keeps its case and accents, and is cut with
// an ellipsis instead of running off the panel.
func TestNoticeLinesAreSmoothText(t *testing.T) {
	n := Identify("Küche & Bad")
	if n.Line1 != "HELLO" || n.Line2 != "Küche & Bad" {
		t.Errorf("identify lines %q / %q: the name lost its case or accents", n.Line1, n.Line2)
	}
	n = SavedByKey(3, "Absolut relax - Entspannungsmusik und mehr für den ganzen Tag")
	if !strings.HasSuffix(n.Line2, "…") || smoothSmall.width(n.Line2) > noticeTextW {
		t.Errorf("long name %q (%d px) not cut to %d px with an ellipsis", n.Line2, smoothSmall.width(n.Line2), noticeTextW)
	}
	if n := SavedByKey(1, "  Radio \t Bob!  "); n.Line2 != "Radio Bob!" {
		t.Errorf("whitespace not folded: %q", n.Line2)
	}
}

// assertNoticeLinesFit checks that n's lines are drawable as they are in
// their faces and fit the panel without being cut: a fixed text that needs
// the ellipsis is a text that has to be shortened.
func assertNoticeLinesFit(t *testing.T, n Notice) {
	t.Helper()
	for i, l := range []struct {
		s string
		f *smoothFont
	}{{n.Line1, smoothLarge}, {n.Line2, smoothSmall}, {n.Line3, smoothSmall}} {
		if n.Kind == NoticePreset {
			l.f = n.face
		}
		if sanitizeSmooth(l.s, trackTextMax) != l.s {
			t.Errorf("%s line %d %q is not drawable as is", n.Kind, i+1, l.s)
		}
		if strings.HasSuffix(l.s, "…") && !strings.HasSuffix(l.s, "Saving…") {
			t.Errorf("%s line %d %q had to be cut: shorten the text", n.Kind, i+1, l.s)
		}
		if w := l.f.width(l.s); w > noticeTextW {
			t.Errorf("%s line %d %q is %d px wide, the panel holds %d", n.Kind, i+1, l.s, w, noticeTextW)
		}
	}
}

// Every notice with a fixed text fits the panel in its faces as it is.
func TestFixedNoticeTextsFit(t *testing.T) {
	for _, n := range []Notice{
		SavedFromApp(6), AlreadyOnKey(2), AlreadyOnKey(12), Swapped(5, 6, "x"), Swapped(2, 3, ""),
		NotSaved(5), EmptyKey(1), KeysRestored(6), KeysRestored(1), KeyHeld(6), KeyHeldRowTwo(6),
		PresetFailed(4, "x"), FolderMoved(1), SleepSet(60), SleepOff(), Snooze("06:39"),
		Bass(-10, -10, 10), Bass(10, -10, 10), SavedOnRowTwo(6, "x"), SavedByKey(6, "x"), Identify("x"),
	} {
		assertNoticeLinesFit(t, n)
	}
}

// greyIn counts the pixels in the box that are lit below level lvl: the
// edge pixels of anti-aliased text. Text in the dot matrix lights every
// pixel it touches at its full level, so a text box without any grey is
// text drawn in the dot matrix.
func greyIn(buf []byte, x0, y0, w, h int, lvl byte) int {
	n := 0
	for y := max(y0, 0); y < min(y0+h, Height); y++ {
		for x := max(x0, 0); x < min(x0+w, Width); x++ {
			if v := buf[y*Width+x]; v > 0 && v < lvl {
				n++
			}
		}
	}
	return n
}
func levelAt(buf []byte, x, y int) byte { return buf[y*Width+x] }

func litIn(buf []byte, x0, y0, w, h int) int {
	n := 0
	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			if buf[y*Width+x] > 0 {
				n++
			}
		}
	}
	return n
}

func TestNoticesRender(t *testing.T) {
	all := []Notice{
		SavedByKey(3, "Deutschlandfunk Kultur und mehr"),
		SavedFromApp(6),
		AlreadyOnKey(2),
		Swapped(5, 6, "Best Of Rock.FM"),
		Swapped(2, 3, ""),
		NotSaved(5),
		EmptyKey(1),
		KeysRestored(4),
		Identify("Küche"),
		KeyHeld(6),
		PresetPlaying(3, "Radio Arabella Oberösterreich"),
		PresetPlaying(1, ""),
		PresetFailed(4, "Radio Arabella Oberösterreich"),
		FolderMoved(1),
	}
	buf := make([]byte, FrameSize)
	for _, n := range all {
		// The preset notice's lines are the station name in one face, laid
		// out by TestPresetPlayingSmoothFont.
		lined := n.Kind != NoticePreset
		r := newNoticeRenderer(n)
		r.frame(0, buf)
		if m := meanLevel(buf); m != 0 {
			t.Errorf("%s: first frame not dark (fade-in), mean %.2f", n.Kind, m)
		}
		r.frame(1, buf)
		for _, v := range buf {
			if v > 15 {
				t.Fatalf("%s: level %d out of range", n.Kind, v)
			}
		}
		if litIn(buf, iconX, iconY, 40, 40) < 30 {
			t.Errorf("%s: icon area nearly empty", n.Kind)
		}
		l1Top, l2Top := line1Y-smoothLarge.capH, line2Y-smoothSmall.capH
		if lined && litIn(buf, 0, l1Top, Width, smoothLarge.capH) < 40 {
			t.Errorf("%s: line 1 not drawn", n.Kind)
		}
		if lined && greyIn(buf, 0, l1Top, Width, smoothLarge.capH, lvlLine1) == 0 {
			t.Errorf("%s: line 1 has no anti-aliased pixels, drawn in the dot matrix?", n.Kind)
		}
		if lined && n.Line2 != "" && litIn(buf, 0, l2Top, Width, smoothSmall.capH) < 15 {
			t.Errorf("%s: line 2 not drawn", n.Kind)
		}
		if lined && n.Line2 != "" && greyIn(buf, 0, l2Top, Width, smoothSmall.capH, lvlLine2) == 0 {
			t.Errorf("%s: line 2 has no anti-aliased pixels, drawn in the dot matrix?", n.Kind)
		}
		below := line2Y + smoothSmall.bottom
		if lined && litIn(buf, 0, below, Width, Height-below) > 0 && n.Line3 == "" && n.Bar == 0 && n.Kind != NoticeIdentify {
			t.Errorf("%s: pixels below line 2 without a third line", n.Kind)
		}
		if lined && n.Line3 != "" {
			if y := line3Y + smoothSmall.bottom; y > barY || y >= Height {
				t.Errorf("%s: line 3 ends at row %d, into the bar or off the panel", n.Kind, y)
			}
		}
		// Burn-in budget like the splash: a mostly dark screen.
		if m := meanLevel(buf); m > 2 {
			t.Errorf("%s: too bright, mean %.2f", n.Kind, m)
		}
		half := make([]byte, FrameSize)
		r.frame(noticeFadeIn/2, half)
		if a, b := meanLevel(half), meanLevel(buf); n.Kind != NoticeIdentify && (a < b*0.3 || a > b*0.7) {
			t.Errorf("%s: fade-in midway at %.2f of full %.2f", n.Kind, a, b)
		}
	}
	// The badge cuts its symbol out of a lit disc.
	r := newNoticeRenderer(SavedByKey(1, "X"))
	r.frame(1, buf)
	if levelAt(buf, badgeCX+6, badgeCY) != 15 {
		t.Error("badge disc not lit")
	}
	if levelAt(buf, badgeCX-1, badgeCY+3) != 0 {
		t.Error("check mark not cut out of the badge")
	}
	// The empty key is dotted: its top edge has gaps.
	r = newNoticeRenderer(EmptyKey(2))
	r.frame(1, buf)
	if lit := litIn(buf, iconX+8, iconY+3, 16, 1); lit == 0 || lit == 16 {
		t.Errorf("empty key edge not dotted (%d of 16 lit)", lit)
	}
	// Identify: the border blinks and the rings pulse.
	r = newNoticeRenderer(Identify("Bad"))
	on, off := make([]byte, FrameSize), make([]byte, FrameSize)
	r.frame(0.25, on)
	r.frame(0.75, off)
	if levelAt(on, 0, 50) != 9 || levelAt(off, 0, 50) != 0 {
		t.Errorf("border blink: on %d, off %d", levelAt(on, 0, 50), levelAt(off, 0, 50))
	}
	if meanLevel(on[:Width*40]) == meanLevel(off[:Width*40]) {
		t.Error("rings do not pulse")
	}
}

// The number inside the preset key is smooth text, not the doubled dot
// matrix: anti-aliased, on the rows the old number used (13..26, the
// round digits' overshoot one row above), and its ink centred in the key.
// A hardware preset press used to show the speaker's own smooth number and
// then STR's blocky one in the frame (discussion #1305).
func TestPresetKeyNumberIsSmooth(t *testing.T) {
	// inside the key's outline (columns 51..76, rows 7..32), clear of its
	// rounded corners
	const x0, y0, w, h = iconX + 7, iconY + 4, 18, 24
	for key := 1; key <= 6; key++ {
		for _, n := range []Notice{PresetPlaying(key, "WDR 2"), SavedByKey(key, "WDR 2"), EmptyKey(key), KeyHeldRowTwo(key)} {
			buf := make([]byte, FrameSize)
			newNoticeRenderer(n).frame(1, buf)
			lvl := byte(15)
			if n.Kind == NoticeEmptyKey {
				lvl = 11
			}
			if greyIn(buf, x0, y0, w, h, lvl) == 0 {
				t.Errorf("%s key %d: number has no anti-aliased pixels, drawn in the dot matrix?", n.Kind, key)
			}
			lo, hi, top, bottom := Width, -1, Height, -1
			for y := y0; y < y0+h; y++ {
				for x := x0; x < x0+w; x++ {
					// the badge reaches into the key's bottom right corner
					if dx, dy := x-badgeCX, y-badgeCY; dx*dx+dy*dy <= 9*9 {
						continue
					}
					if buf[y*Width+x] > 0 {
						lo, hi, top, bottom = min(lo, x), max(hi, x), min(top, y), max(bottom, y)
					}
				}
			}
			if hi < 0 {
				t.Errorf("%s key %d: no number in the key", n.Kind, key)
				continue
			}
			if top < keyNumY-smoothKey.capH-1 || bottom > keyNumY {
				t.Errorf("%s key %d: number on rows %d..%d, want within %d..%d", n.Kind, key, top, bottom, keyNumY-smoothKey.capH-1, keyNumY)
			}
			if off := lo + hi + 1 - 2*keyCX; off < -2 || off > 2 {
				t.Errorf("%s key %d: number spans %d..%d, not centred on %d", n.Kind, key, lo, hi, keyCX)
			}
		}
	}
}

// The sleep notice's crescent is shaded at its edge like the text under it,
// not stepped: well beyond the two stars' few dim pixels, its curve is drawn
// in between levels.
func TestBigMoonIsShaded(t *testing.T) {
	buf := make([]byte, FrameSize)
	newNoticeRenderer(SleepSet(30)).frame(1, buf)
	if g := greyIn(buf, iconX, iconY, 32, 32, 15); g < 30 {
		t.Errorf("only %d shaded pixels in the moon symbol", g)
	}
	if litIn(buf, iconX, iconY, 32, 32) < 150 {
		t.Error("moon symbol not drawn")
	}
}

func TestNoticeBar(t *testing.T) {
	n := EmptyKey(1)
	n.Bar = 0.5
	buf := make([]byte, FrameSize)
	newNoticeRenderer(n).frame(1, buf)
	if levelAt(buf, 20, barY+2) == 0 || levelAt(buf, 100, barY+2) != 0 {
		t.Error("bar not half filled")
	}
}

func TestNoticesFlag(t *testing.T) {
	NoticesFlagPath = filepath.Join(t.TempDir(), "display-notices")
	defer func() { NoticesFlagPath = "/mnt/nv/streborn/display-notices" }()
	if !NoticesEnabled() {
		t.Fatal("absent flag must mean on")
	}
	_ = os.WriteFile(NoticesFlagPath, []byte("off\n"), 0o644)
	if NoticesEnabled() {
		t.Fatal("off must mean off")
	}
	_ = os.WriteFile(NoticesFlagPath, []byte("1"), 0o644)
	if !NoticesEnabled() {
		t.Fatal("1 must mean on")
	}
}

// A long station name wraps over the large lines rather than being cut at
// the first one, by pixel width in the track screen's large face, and keeps
// its case and umlauts: the notice shows right before the track screen, so
// the lettering must not change in between (discussion #1305).
func TestPresetPlayingWrapsTheName(t *testing.T) {
	n := PresetPlaying(3, "Radio Arabella Oberösterreich")
	if n.Line1 != "Radio Arabella" || n.Line2 != "Oberösterreich" || n.Line3 != "" {
		t.Fatalf("got %q / %q / %q", n.Line1, n.Line2, n.Line3)
	}
	if n.face != smoothLarge {
		t.Error("a name whose words fit the large face is not in it")
	}
	for _, l := range []string{n.Line1, n.Line2, n.Line3} {
		if w := smoothLarge.width(l); w > presetTextW {
			t.Errorf("%q is %d px wide, the line holds %d", l, w, presetTextW)
		}
	}
	// A word too wide for a large line is not split: the whole name steps
	// down to the small face.
	n = PresetPlaying(2, "Deutschlandfunk Kultur")
	if n.face != smoothSmall || n.Line1 != "Deutschlandfunk" || n.Line2 != "Kultur" || n.Line3 != "" {
		t.Errorf("wide word: got %q / %q / %q, small face %v", n.Line1, n.Line2, n.Line3, n.face == smoothSmall)
	}
	// A name that does not fit three lines ends in an ellipsis; one that is
	// empty says so.
	n = PresetPlaying(4, "Absolut relax - Entspannungsmusik und mehr für den ganzen Tag")
	if n.Line3 == "" || !strings.HasSuffix(n.Line3, "…") {
		t.Errorf("long name not cut with an ellipsis: %q / %q / %q", n.Line1, n.Line2, n.Line3)
	}
	if n := PresetPlaying(1, ""); n.Line1 != "Preset" || n.Line2 != "" {
		t.Errorf("empty name: got %q / %q", n.Line1, n.Line2)
	}
}

// The preset notice draws its lines in the smooth font (anti-aliased, so
// grey levels between dark and the text's level appear), centred, under the
// key, and with the baselines a track screen line step apart.
func TestPresetPlayingSmoothFont(t *testing.T) {
	for _, name := range []string{"WDR 2", "Deutschlandfunk Kultur", "Radio Arabella Oberösterreich", "Absolut relax - Entspannungsmusik und mehr für den ganzen Tag"} {
		n := PresetPlaying(2, name)
		buf := make([]byte, FrameSize)
		newNoticeRenderer(n).frame(1, buf)
		lines := 0
		for _, l := range []string{n.Line1, n.Line2, n.Line3} {
			if l != "" {
				lines++
			}
		}
		f, step := n.face, presetLineStep(n.face)
		keyBottom := iconY + 3 + 26
		firstTop := presetLineY - f.top // accents and overshoot included
		if lit := litIn(buf, 0, keyBottom, Width, firstTop-keyBottom); lit != 0 {
			t.Errorf("%q: %d pixels between the key and the first line", name, lit)
		}
		lastBottom := presetLineY + (lines-1)*step + f.bottom
		if lastBottom >= Height {
			t.Fatalf("%q: last line ends at %d, off the panel", name, lastBottom)
		}
		if lit := litIn(buf, 0, lastBottom, Width, Height-lastBottom); lit != 0 {
			t.Errorf("%q: %d pixels below the last line", name, lit)
		}
		grey := 0
		for i := 0; i < lines; i++ {
			y0 := presetLineY + i*step - f.capH
			// the row band of this line's capitals: lit, and centred
			lo, hi := Width, -1
			for y := y0; y < y0+f.capH; y++ {
				for x := 0; x < Width; x++ {
					if v := buf[y*Width+x]; v > 0 {
						lo, hi = min(lo, x), max(hi, x)
						if v < lvlLine1 {
							grey++
						}
					}
				}
			}
			if hi < 0 {
				t.Errorf("%q: line %d not drawn", name, i+1)
				continue
			}
			if off := (lo + hi + 1) - Width; off < -2 || off > 2 {
				t.Errorf("%q: line %d spans %d..%d, not centred", name, i+1, lo, hi)
			}
		}
		if grey == 0 {
			t.Errorf("%q: no anti-aliased pixels, drawn in the dot matrix?", name)
		}
	}
}
