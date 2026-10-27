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

func TestSanitizeText(t *testing.T) {
	cases := []struct {
		in    string
		limit int
		want  string
	}{
		{"Über Größe", 21, "UEBER GROESSE"},
		{"Café Noël – Señor", 21, "CAFE NOEL - SENOR"},
		{"  Radio   Bob! ", 21, "RADIO BOB"},
		{"1LIVE diggi", 10, "1LIVE DIGG"},
		{"Rock & Pop 80s/90s", 21, "ROCK + POP 80S/90S"},
		{"Ζ日本", 21, ""},
		{"A B", 2, "A"},
	}
	for _, c := range cases {
		if got := SanitizeText(c.in, c.limit); got != c.want {
			t.Errorf("SanitizeText(%q, %d) = %q, want %q", c.in, c.limit, got, c.want)
		}
	}
}

func TestFontCoversNoticeAlphabet(t *testing.T) {
	for _, ch := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+/.:-,= " {
		if _, ok := font[ch]; !ok {
			t.Errorf("font misses %q", ch)
		}
	}
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
		// The preset notice's lines are the station name in the smooth
		// font, laid out by TestPresetPlayingSmoothFont.
		dotMatrix := n.Kind != NoticePreset
		for _, l := range []struct {
			s     string
			limit int
		}{{n.Line1, line1Max}, {n.Line2, line2Max}, {n.Line3, line2Max}} {
			if !dotMatrix {
				break
			}
			if len(l.s) > l.limit {
				t.Errorf("%s: %q longer than %d", n.Kind, l.s, l.limit)
			}
			for _, ch := range l.s {
				if _, ok := font[ch]; !ok {
					t.Errorf("%s: %q not in the font", n.Kind, ch)
				}
			}
		}
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
		if dotMatrix && litIn(buf, 0, line1Y, Width, 14) < 40 {
			t.Errorf("%s: line 1 not drawn", n.Kind)
		}
		if dotMatrix && n.Line2 != "" && litIn(buf, 0, line2Y, Width, 7) < 15 {
			t.Errorf("%s: line 2 not drawn", n.Kind)
		}
		if dotMatrix && litIn(buf, 0, line3Y, Width, Height-line3Y) > 0 && n.Line3 == "" && n.Bar == 0 && n.Kind != NoticeIdentify {
			t.Errorf("%s: pixels below line 2 without a third line", n.Kind)
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
