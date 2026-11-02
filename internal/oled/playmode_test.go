package oled

import "testing"

func TestPlayModeNoticeTexts(t *testing.T) {
	for _, tc := range []struct {
		n      Notice
		l1, l2 string
	}{
		{Shuffle(true), "SHUFFLE ON", "Random order"},
		{Shuffle(false), "SHUFFLE OFF", "Playlist order"},
		{Repeat("context"), "REPEAT ALL", "Whole playlist"},
		{Repeat("track"), "REPEAT ONE", "This song"},
		{Repeat("off"), "REPEAT OFF", "Plays to the end"},
	} {
		if tc.n.Kind != NoticePlayMode || tc.n.Line1 != tc.l1 || tc.n.Line2 != tc.l2 {
			t.Errorf("notice = %q / %q, want %q / %q", tc.n.Line1, tc.n.Line2, tc.l1, tc.l2)
		}
		assertNoticeLinesFit(t, tc.n)
	}
}

// Both notices draw their icon and their text.
func TestPlayModeNoticesDraw(t *testing.T) {
	for _, n := range []Notice{Shuffle(true), Repeat("context")} {
		buf := make([]byte, FrameSize)
		newNoticeRenderer(n).frame(1, buf)
		if litIn(buf, iconX, iconY, 34, 32) < 40 {
			t.Errorf("%s: no icon", n.Line1)
		}
		if litIn(buf, 0, line1Y-smoothLarge.capH, Width, smoothLarge.capH) < 40 {
			t.Errorf("%s: no text", n.Line1)
		}
	}
}

// Tapping the repeat key through its modes swaps the notice in place; it
// never takes over a bass or sleep step.
func TestPlayModeNoticeSupersedes(t *testing.T) {
	if !Repeat("track").supersedes(NoticePlayMode) {
		t.Error("a repeat step does not take over the showing one")
	}
	if Shuffle(true).supersedes(NoticeBass) || Bass(1, -9, 9).supersedes(NoticePlayMode) {
		t.Error("play mode and bass notices take over each other")
	}
}
