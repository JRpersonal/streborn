package oled

import (
	"strconv"
	"testing"
	"time"
)

func TestSleepMinutesRoundsUp(t *testing.T) {
	now := time.Date(2026, 10, 20, 22, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		left time.Duration
		want int
	}{
		{0, 0},
		{-time.Second, 0},
		{40 * time.Second, 1},
		{time.Minute, 1},
		{time.Minute + time.Second, 2},
		{23*time.Minute + 10*time.Second, 24},
		{60 * time.Minute, 60},
	} {
		if got := sleepMinutes(now.Add(c.left), now); got != c.want {
			t.Errorf("%v left: shows %d, want %d", c.left, got, c.want)
		}
	}
	if got := sleepMinutes(time.Time{}, now); got != 0 {
		t.Errorf("no timer shows %d", got)
	}
}

func TestToSleepStep(t *testing.T) {
	now := time.Date(2026, 10, 20, 22, 0, 0, 0, time.UTC)
	if d := toSleepStep(now.Add(23*time.Minute+10*time.Second), now); d != 10*time.Second+trackClockLate {
		t.Errorf("23:10 left: next change in %v", d)
	}
	if d := toSleepStep(now.Add(5*time.Minute), now); d != time.Minute+trackClockLate {
		t.Errorf("5:00 left: next change in %v", d)
	}
	if d := toSleepStep(time.Time{}, now); d != 0 {
		t.Errorf("no timer: %v", d)
	}
	// the value shown after the wait is one lower
	until := now.Add(23*time.Minute + 10*time.Second)
	before := sleepMinutes(until, now)
	after := sleepMinutes(until, now.Add(toSleepStep(until, now)))
	if after != before-1 {
		t.Errorf("after the step the countdown shows %d, want %d", after, before-1)
	}
}

// The countdown redraws at its minute boundary even with the clock off and
// the speaker paused, when nothing else would ask for a frame for minutes.
func TestTrackNextDrawSleepBoundary(t *testing.T) {
	now := time.Date(2026, 10, 13, 15, 42, 20, 0, time.UTC)
	s := newTrackScreen()
	s.now = func() time.Time { return now }
	s.shiftEvery = 3 * time.Minute
	s.paused = true
	s.want = &TrackText{Title: "X"}
	s.sleepUntil = now.Add(23*time.Minute + 10*time.Second)
	if d := s.nextDraw(); d != 10*time.Second+trackClockLate {
		t.Errorf("paused, no clock, countdown: %v", d)
	}
	// with the clock, whichever comes first (the countdown here)
	s.want = &TrackText{Title: "X", Clock: true}
	if d := s.nextDraw(); d != 10*time.Second+trackClockLate {
		t.Errorf("clock and countdown: %v", d)
	}
	// a run-out timer asks for nothing
	s.want = &TrackText{Title: "X"}
	s.sleepUntil = now.Add(-time.Second)
	if d := s.nextDraw(); d != 2*time.Minute+40*time.Second {
		t.Errorf("expired timer: %v", d)
	}
}

// boxOf returns the horizontal extent of the line whose text is s.
func boxOf(t *testing.T, f trackFrame, s string) (int, int) {
	t.Helper()
	for _, l := range f.lines {
		if l.s == s {
			return l.x0, l.x0 + l.w
		}
	}
	t.Fatalf("no line %q in %+v", s, f.lines)
	return 0, 0
}

// The countdown never overlaps the station or the time: the station's box
// ends before the moon, the minutes end before the time, also with a long
// station name and a 12-hour clock.
func TestTrackHeaderSleepLayout(t *testing.T) {
	at := time.Date(2026, 10, 20, 15, 42, 0, 0, time.Local) // 3:42 PM
	station := "Deutschlandfunk Kultur Nova Extra"
	for _, h12 := range []bool{false, true} {
		for _, mins := range []int{1, 9, 23, 60} {
			tt := TrackText{Station: station, Artist: "A", Title: "T", Clock: true, Hour12: h12}.sanitized()
			tt.sleepMin = mins
			f := trackLayout(tt, at)
			if !f.moon {
				t.Fatalf("no moon with %d minutes", mins)
			}
			_, stEnd := boxOf(t, f, tt.Station)
			if stEnd > f.moonX-2 {
				t.Errorf("h12=%v %d min: station box ends at %d, moon starts at %d", h12, mins, stEnd, f.moonX)
			}
			_, dEnd := boxOf(t, f, strconv.Itoa(mins))
			hm, _ := clockText(at, h12)
			cStart, _ := boxOf(t, f, hm)
			if dEnd+sleepClockGap > cStart {
				t.Errorf("h12=%v %d min: minutes end at %d, time starts at %d", h12, mins, dEnd, cStart)
			}
			if h12 {
				_, apEnd := boxOf(t, f, "PM")
				if apEnd > Width {
					t.Errorf("PM runs off the panel: %d", apEnd)
				}
			}
		}
	}
	// without the time: flush right, the station centred clear of it
	tt := TrackText{Station: station, Title: "T"}.sanitized()
	tt.sleepMin = 30
	f := trackLayout(tt, at)
	x0, x1 := boxOf(t, f, tt.Station)
	if x1 > f.moonX-2 {
		t.Errorf("plain header: station box ends at %d, moon at %d", x1, f.moonX)
	}
	if x0 != Width-x1 {
		t.Errorf("plain header: station box %d..%d is not centred", x0, x1)
	}
	_, dEnd := boxOf(t, f, "30")
	if dEnd > Width-1 {
		t.Errorf("minutes end at %d, past the panel's last column", dEnd)
	}
	// no timer: the header is exactly what it was
	tt.sleepMin = 0
	if g := trackLayout(tt, at); g.moon {
		t.Error("moon drawn without a timer")
	}
}

// The countdown is drawn, inside the top line, and the cache rebuilds when the
// minute shown changes.
func TestTrackSleepRenderAndCache(t *testing.T) {
	at := time.Date(2026, 10, 20, 15, 42, 0, 0, time.Local)
	tt := TrackText{Station: "WDR 2", Title: "Männer"}.sanitized()
	tt.sleepMin = 23
	buf := make([]byte, FrameSize)
	renderTrack(tt, at, trackAnim{}, 0, 0, 0, buf)
	f := trackLayout(tt, at)
	if litIn(buf, f.moonX, f.moonY, moonW, moonW) == 0 {
		t.Error("moon not drawn")
	}
	var tc trackCache
	tc.prepare(tt, at, 0, 0)
	first := append([]byte(nil), tc.base...)
	tt.sleepMin = 22
	tc.prepare(tt, at, 0, 0)
	if string(first) == string(tc.base) {
		t.Error("cache kept the old countdown")
	}
}

func TestSleepNoticeTexts(t *testing.T) {
	n := SleepSet(30)
	if n.Kind != NoticeSleep || n.Line1 != "SLEEP 30" || n.Line2 != "MINUTES" {
		t.Errorf("set notice %+v", n)
	}
	o := SleepOff()
	if o.Kind != NoticeSleep || o.Line1 != "SLEEP OFF" {
		t.Errorf("off notice %+v", o)
	}
	for _, s := range []string{n.Line1, n.Line2, o.Line1, o.Line2} {
		if SanitizeText(s, 30) != s {
			t.Errorf("%q is not drawable as is", s)
		}
	}
	r := newNoticeRenderer(n)
	if litIn(r.full, iconX, iconY, 32, 32) == 0 {
		t.Error("moon symbol not drawn")
	}
}

// Tapping through: a newer sleep notice takes over the showing one, and the
// one after it does not wait for the start gap.
func TestSleepNoticeSupersedesItsOwnKind(t *testing.T) {
	f := newFakeQueue()
	posted := false
	f.during = func(n Notice) {
		if n.Line1 == "SLEEP 15" && !posted {
			posted = true
			f.post(SleepSet(30))
			f.noticeQueue.mu.Lock()
			cur := f.cur
			f.noticeQueue.mu.Unlock()
			if cur.Line1 != "SLEEP 30" {
				t.Errorf("the showing notice was not taken over: %q", cur.Line1)
			}
		}
	}
	f.post(SleepSet(15))
	f.drain()
	if len(f.shown) != 2 || f.shown[1].Line1 != "SLEEP 30" {
		t.Fatalf("want SLEEP 15 then SLEEP 30, got %+v", f.shown)
	}
	if gap := f.shownAt[1].Sub(f.shownAt[0]); gap >= noticeGap {
		t.Errorf("the newer sleep notice waited for the gap: %v", gap)
	}
	// one second after the last one ended: still no gap
	f.during = nil
	f.post(SleepOff())
	f.drain()
	if gap := f.shownAt[2].Sub(f.shownAt[1]); gap >= noticeGap {
		t.Errorf("SLEEP OFF waited for the gap: %v", gap)
	}
}

// Other kinds keep the gap, before and after a sleep notice.
func TestSleepNoticeLeavesOtherKindsAlone(t *testing.T) {
	f := newFakeQueue()
	f.post(EmptyKey(1))
	f.drain()
	f.post(SleepSet(15))
	f.drain()
	f.post(EmptyKey(2))
	f.drain()
	if len(f.shown) != 3 {
		t.Fatalf("shown %v", f.kinds())
	}
	for i := 1; i < 3; i++ {
		if gap := f.shownAt[i].Sub(f.shownAt[i-1]); gap < noticeGap {
			t.Errorf("notice %d came %v after the one before, want the gap", i, gap)
		}
	}
	// a sleep notice never takes over another kind in place
	f.during = func(n Notice) {
		if n.Kind == NoticeEmptyKey {
			f.post(SleepSet(30))
			f.noticeQueue.mu.Lock()
			cur := f.cur
			f.noticeQueue.mu.Unlock()
			if cur.Kind != NoticeEmptyKey {
				t.Errorf("a sleep notice took over an empty-key notice")
			}
		}
	}
	f.post(EmptyKey(3))
	f.drain()
	if k := f.kinds(); k[len(k)-1] != NoticeSleep {
		t.Errorf("the sleep notice after it was lost: %v", k)
	}
}

func TestNoticeSwap(t *testing.T) {
	var s noticeSwap
	if _, ok := s.take(); ok {
		t.Fatal("empty swap gave a notice")
	}
	if !s.offer(SleepSet(30)) {
		t.Fatal("open swap refused")
	}
	if n, ok := s.take(); !ok || n.Line1 != "SLEEP 30" {
		t.Fatalf("take %+v %v", n, ok)
	}
	s.offer(SleepSet(60))
	left := s.close()
	if left == nil || left.Line1 != "SLEEP 60" {
		t.Fatalf("close did not hand back the untaken notice: %+v", left)
	}
	if s.offer(SleepOff()) {
		t.Error("a closed swap took an offer")
	}
}
