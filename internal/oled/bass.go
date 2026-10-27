package oled

// The bass level on the panel: a notice when a remote key steps the bass
// (internal/webui basskey.go), BASS -3 with a bar of where that sits in the
// speaker's range. Like the sleep steps, a newer one takes over the showing
// one in place, so tapping the key several times reads as one moving bar.

import (
	"strconv"
	"time"
)

// NoticeBass is the bass step's notice.
const NoticeBass NoticeKind = "bass"

// bassNoticeHold is how long a bass notice stays.
const bassNoticeHold = 2 * time.Second

// Bass: the speaker's bass is now level, in the range lo..hi it reports.
// At either end the second line says so, which is the answer to a press
// that could not move it any further.
func Bass(level, lo, hi int) Notice {
	l1 := "BASS " + strconv.Itoa(level)
	if level > 0 {
		l1 = "BASS +" + strconv.Itoa(level)
	}
	l2 := ""
	switch {
	case hi > lo && level >= hi:
		l2 = "MAXIMUM"
	case hi > lo && level <= lo:
		l2 = "MINIMUM"
	}
	n := keyNotice(NoticeBass, iconBass, 0, l1, l2, bassNoticeHold, 2)
	// The bar draws only above 0, so the bottom of the range keeps a sliver
	// of a value: the empty outline still shows where the level sits.
	n.Bar = 0.001
	if hi > lo {
		n.Bar = max(0.001, min(1, float64(level-lo)/float64(hi-lo)))
	}
	return n
}

// swapsInPlace reports whether a notice of kind k may be taken over by a
// newer one of the same kind inside one panel hold: the steps of a key the
// user taps through (sleep timer, bass).
func swapsInPlace(k NoticeKind) bool { return k == NoticeSleep || k == NoticeBass }

// bassIcon is a loudspeaker: the magnet, the cone and two sound waves.
func (c canvas) bassIcon() {
	const x, cy = iconX + 2, iconY + 17
	c.fill(x, cy-5, 7, 11, 15)
	for i := 0; i < 9; i++ {
		c.fill(x+7+i, cy-5-i, 1, 11+2*i, 15)
	}
	// arc takes angles in 0..360, so each wave is its upper and lower half
	for _, w := range []struct{ rad, lvl int }{{7, 15}, {8, 15}, {12, 10}, {13, 10}} {
		c.arc(x+16, cy, w.rad, 0, 50, w.lvl)
		c.arc(x+16, cy, w.rad, 310, 360, w.lvl)
	}
}
