package oled

// The sleep timer on the panel: a notice when a remote key steps the timer
// (SLEEP 30 / MINUTES, SLEEP OFF), and while it runs a small crescent moon
// with the minutes left in the track screen's top line.
//
// The countdown costs no timer of its own. The deadline is handed in once
// (SetSleepDeadline), the track screen's draw turns it into whole minutes,
// and nextDraw asks the panel hold for one redraw at each minute boundary,
// the same way the clock does. The cache key carries the minutes shown, so a
// redraw between two boundaries reuses the standing frame.

import (
	"strconv"
	"sync"
	"time"
)

// NoticeSleep is the sleep timer's notice. A newer one replaces an older one
// on the panel at once instead of waiting out noticeGap: the user taps
// through 15, 30, 60, off and each step must show without delay.
const NoticeSleep NoticeKind = "sleep"

// sleepNoticeHold is how long a sleep notice stays.
const sleepNoticeHold = 2500 * time.Millisecond

// sleepNoticeMax bounds one panel hold of sleep notices replacing each other
// in place: somebody tapping the key for half a minute still gets the panel
// handed back.
const sleepNoticeMax = 30 * time.Second

// SleepSet: the sleep timer now runs for minutes.
func SleepSet(minutes int) Notice {
	return keyNotice(NoticeSleep, iconMoon, 0, "SLEEP "+strconv.Itoa(minutes), "MINUTES", sleepNoticeHold, 2)
}

// SleepOff: the sleep timer was switched off.
func SleepOff() Notice {
	return keyNotice(NoticeSleep, iconMoon, 0, "SLEEP OFF", "TIMER CANCELLED", sleepNoticeHold, 2)
}

// NoticeSnooze is a snoozed alarm's notice (internal/webui alarmsnooze.go).
const NoticeSnooze NoticeKind = "snooze"

// snoozeNoticeHold is how long the snooze notice stays: a little longer than
// a sleep step, because somebody half asleep reads the wake time off it.
const snoozeNoticeHold = 4 * time.Second

// Snooze: a ringing alarm was snoozed and rings again at until, already
// formatted as the alarm's local "15:04".
func Snooze(until string) Notice {
	return keyNotice(NoticeSnooze, iconMoon, 0, "SNOOZE", "UNTIL "+until, snoozeNoticeHold, 2)
}

// supersedes reports whether n may take over from a notice of kind last
// without the start gap: only a sleep notice following a sleep notice.
func (n Notice) supersedes(last NoticeKind) bool {
	return n.Kind == NoticeSleep && last == NoticeSleep
}

// noticeSwap lets a newer notice take over a showing one inside the same
// panel hold (no hand-back to BoseApp, no fade, no gap). The queue offers;
// playNotice takes. Once closed, an offer fails and the queue shows the
// notice the normal way; an offer that came in but was never taken is handed
// back by close.
type noticeSwap struct {
	mu     sync.Mutex
	next   *Notice
	closed bool
}

func (s *noticeSwap) offer(n Notice) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.next = &n
	return true
}

func (s *noticeSwap) take() (Notice, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next == nil {
		return Notice{}, false
	}
	n := *s.next
	s.next = nil
	return n, true
}

// close ends offers and returns one that was never taken.
func (s *noticeSwap) close() *Notice {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	n := s.next
	s.next = nil
	return n
}

// SetSleepDeadline tells the track screen when the sleep timer switches the
// speaker off; the zero time clears the countdown. Returns at once.
func SetSleepDeadline(at time.Time) { tracks.setSleep(at) }

func (s *trackScreen) setSleep(at time.Time) {
	s.mu.Lock()
	if s.sleepUntil.Equal(at) {
		s.mu.Unlock()
		return
	}
	s.sleepUntil = at
	holding := s.holding
	s.mu.Unlock()
	if holding {
		select {
		case s.redraw <- struct{}{}:
		default:
		}
	}
}

// sleepMinutes is the countdown shown at now for a timer due at until: the
// minutes left, rounded up (0:40 left shows 1), 0 when nothing runs.
func sleepMinutes(until, now time.Time) int {
	if until.IsZero() || !until.After(now) {
		return 0
	}
	left := until.Sub(now)
	return int((left + time.Minute - 1) / time.Minute)
}

// toSleepStep is how long until the countdown shown changes, a hair late so
// the redraw reads the new value; 0 when nothing runs.
func toSleepStep(until, now time.Time) time.Duration {
	if until.IsZero() || !until.After(now) {
		return 0
	}
	left := until.Sub(now)
	d := left % time.Minute
	if d == 0 {
		d = time.Minute
	}
	return d + trackClockLate
}

// The countdown in the top line: the moon, a gap, the minutes in the small
// face, all on the top line's baseline.
const (
	moonW        = 8
	moonGap      = 2
	lvlTrackMoon = 11
	// sleepClockGap keeps the countdown apart from the time on its right.
	sleepClockGap = 5
)

// sleepWidth is the width of the countdown for minutes.
func sleepWidth(minutes int) int {
	return moonW + moonGap + textWidth(strconv.Itoa(minutes), smoothSmall)
}

// sleepIndicator lays out the countdown with its left edge at x on baseline:
// the moon is placed in the frame, the minutes become a line.
func (f *trackFrame) sleepIndicator(minutes, x, baseline int) {
	f.moon = true
	f.moonX, f.moonY = x, baseline-smoothSmall.capH
	digits := strconv.Itoa(minutes)
	dx := x + moonW + moonGap
	f.lines = append(f.lines, marqueeLine{s: digits, f: smoothSmall, lvl: lvlTrackStation, x0: dx, w: textWidth(digits, smoothSmall), y: baseline, left: true})
}

// moonIcon paints a crescent moon in the moonW x moonW box at x, y (top
// left): a disc with a smaller disc cut out of its upper right. It is drawn
// with 4x4 samples a pixel, so its edge is shaded like the text beside it
// rather than stepped; only ever brightening, as the font does.
func (c canvas) moonIcon(x, y, lvl int) {
	const (
		cx, cy, r  = 4.0, 4.0, 4.0 // the moon
		ox, oy, or = 5.7, 2.5, 3.6 // the bite out of it
		ss         = 4
	)
	for py := 0; py < moonW; py++ {
		for px := 0; px < moonW; px++ {
			n := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					fx := float64(px) + (float64(sx)+0.5)/ss
					fy := float64(py) + (float64(sy)+0.5)/ss
					if (fx-cx)*(fx-cx)+(fy-cy)*(fy-cy) > r*r {
						continue
					}
					if (fx-ox)*(fx-ox)+(fy-oy)*(fy-oy) <= or*or {
						continue
					}
					n++
				}
			}
			if n == 0 {
				continue
			}
			v := (lvl*n + ss*ss/2) / (ss * ss)
			if X, Y := x+px, y+py; X >= 0 && Y >= 0 && X < Width && Y < Height && int(c[Y*Width+X]) < v {
				c[Y*Width+X] = byte(v)
			}
		}
	}
}

// bigMoon is the sleep notice's symbol: a crescent filling the 32x32 icon
// box, with two small stars beside it.
func (c canvas) bigMoon() {
	const cx, cy, r = iconX + 15, iconY + 17, 13
	const ox, oy, or = iconX + 23, iconY + 10, 11
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy > r*r+r {
				continue
			}
			if ex, ey := x-ox, y-oy; ex*ex+ey*ey <= or*or+or {
				continue
			}
			c.px(x, y, 15)
		}
	}
	// two stars: small plus signs in the cut-out's sky
	for _, s := range [][3]int{{iconX + 27, iconY + 6, 11}, {iconX + 22, iconY + 16, 8}} {
		c.px(s[0], s[1], s[2])
		c.px(s[0]-1, s[1], s[2]-4)
		c.px(s[0]+1, s[1], s[2]-4)
		c.px(s[0], s[1]-1, s[2]-4)
		c.px(s[0], s[1]+1, s[2]-4)
	}
}
