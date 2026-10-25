package oled

import "testing"

func TestBassNoticeTexts(t *testing.T) {
	for _, tc := range []struct {
		level, lo, hi int
		l1, l2        string
		bar           float64
	}{
		{-3, -9, 0, "BASS -3", "", 6.0 / 9},
		{0, -9, 0, "BASS 0", "MAXIMUM", 1},
		{-9, -9, 0, "BASS -9", "MINIMUM", 0.001},
		{2, -5, 5, "BASS +2", "", 0.7},
		// a collapsed range claims no end and keeps the empty bar
		{0, 0, 0, "BASS 0", "", 0.001},
	} {
		n := Bass(tc.level, tc.lo, tc.hi)
		if n.Kind != NoticeBass || n.Line1 != tc.l1 || n.Line2 != tc.l2 {
			t.Errorf("Bass(%d,%d,%d) = %q / %q, want %q / %q", tc.level, tc.lo, tc.hi, n.Line1, n.Line2, tc.l1, tc.l2)
		}
		if d := n.Bar - tc.bar; d > 1e-9 || d < -1e-9 {
			t.Errorf("Bass(%d,%d,%d) bar %v, want %v", tc.level, tc.lo, tc.hi, n.Bar, tc.bar)
		}
		for _, s := range []string{n.Line1, n.Line2} {
			if SanitizeText(s, 30) != s {
				t.Errorf("%q is not drawable as is", s)
			}
		}
	}
}

// The notice draws its icon, its text and the bar.
func TestBassNoticeDraws(t *testing.T) {
	buf := make([]byte, FrameSize)
	newNoticeRenderer(Bass(-3, -9, 0)).frame(1, buf)
	if litIn(buf, iconX, iconY, 34, 32) < 40 {
		t.Error("no loudspeaker icon")
	}
	if litIn(buf, 0, line1Y, Width, 14) < 40 {
		t.Error("no level line")
	}
	if levelAt(buf, 20, barY+2) == 0 {
		t.Error("bar not drawn")
	}
}

// A bass step takes over a showing bass step in place, like the sleep steps;
// it never takes over another kind.
func TestBassNoticeSupersedes(t *testing.T) {
	if !Bass(-2, -9, 0).supersedes(NoticeBass) {
		t.Error("a bass step does not take over the showing one")
	}
	if Bass(-2, -9, 0).supersedes(NoticeSleep) || SleepSet(30).supersedes(NoticeBass) {
		t.Error("bass and sleep notices take over each other")
	}
	if !SleepSet(30).supersedes(NoticeSleep) {
		t.Error("sleep steps lost their takeover")
	}
}
