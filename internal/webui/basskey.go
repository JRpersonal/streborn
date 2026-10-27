package webui

// The bass on a remote key (discussion #1494).
//
// A thumbs key bound to "bassUp" or "bassDown" (internal/keyactions, saved
// from the desktop app at /api/keyactions) moves the speaker's bass one step
// on every press. Each press reads the level fresh from the speaker (the
// app's slider, the Bose app or another key may have moved it since), takes
// one step of the speaker's own granularity (boxapi.Bass.Step, 1 when the
// speaker names none), clamps it to the range the speaker reports and writes
// it through the same boxapi.SetBass the app's slider uses. A press at either
// end writes nothing and shows the end on the panel.
//
// A speaker without adjustable bass (bassAvailable false and no tone
// controls) does nothing on the press and says so in the log once per agent
// run. The app does not offer the actions for such a speaker; this covers a
// binding saved before that was known.
//
// Presses run one at a time (bassKeyState.mu): two quick presses each read
// what the one before wrote. Nothing is persisted here: the level lives on
// the speaker, and a press writes nothing to NAND.

import (
	"context"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/keyactions"
	"github.com/JRpersonal/streborn/internal/oled"
)

// bassKeyMinGap drops a second press event inside this window, as for the
// sleep key: it only guards against a stray repeat of the same event, and
// stays short so tapping the key about once a second steps every time.
const bassKeyMinGap = 250 * time.Millisecond

// bassKeyTimeout bounds one press: the read and the write on the speaker.
const bassKeyTimeout = 6 * time.Second

// bassKeyState is what the bass key keeps between presses.
type bassKeyState struct {
	// mu runs presses one at a time, from the read to the write.
	mu sync.Mutex
	// last is when the key last stepped, for the debounce.
	last time.Time
	// noBassLogged: the "no adjustable bass" line went out once already.
	noBassLogged bool
}

// bassRead reads the speaker's bass state. A seam (bassReadFn): the real one
// reaches the firmware on its fixed port.
func (s *Server) bassRead(ctx context.Context) (boxapi.Bass, error) {
	if s.bassReadFn != nil {
		return s.bassReadFn(ctx)
	}
	if s.boxHost == "" {
		return boxapi.Bass{}, errNoBoxHost
	}
	return boxapi.New(s.boxHost).GetBass(ctx)
}

// bassWrite sets the speaker's bass, the app slider's path. A seam
// (bassWriteFn).
func (s *Server) bassWrite(ctx context.Context, v int) error {
	if s.bassWriteFn != nil {
		return s.bassWriteFn(ctx, v)
	}
	if s.boxHost == "" {
		return errNoBoxHost
	}
	return boxapi.New(s.boxHost).SetBass(ctx, v)
}

// nextBassLevel is where one press in direction dir (+1 or -1) takes the bass
// from b.Target: one step of the speaker's granularity, kept inside the range
// and on the step grid that starts at b.Min. Equal to b.Target at an end.
func nextBassLevel(b boxapi.Bass, dir int) int {
	step := max(1, b.Step)
	// the highest level on the grid, for a range that is not a whole number
	// of steps
	top := b.Min + (b.Max-b.Min)/step*step
	next := b.Target + dir*step
	return max(b.Min, min(top, next))
}

// BassKeyPress is the press handler of a key bound to a bass step: action is
// keyactions.ActionBassUp or ActionBassDown. Wired into the key handler in
// cmd/agent, which calls it off the key reader's goroutine.
func (s *Server) BassKeyPress(action string) {
	dir := 1
	if action == keyactions.ActionBassDown {
		dir = -1
	} else if action != keyactions.ActionBassUp {
		return
	}
	k := &s.bassKey
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	if !k.last.IsZero() && now.Sub(k.last) < bassKeyMinGap {
		s.logger.Debug("bass key: press ignored, same press reported twice")
		return
	}
	k.last = now

	ctx, cancel := context.WithTimeout(context.Background(), bassKeyTimeout)
	defer cancel()
	b, err := s.bassRead(ctx)
	if err != nil {
		s.logger.Warn("bass key pressed, but the speaker's bass could not be read", "action", action, "err", err)
		return
	}
	if !b.Avail || b.Max <= b.Min {
		if !k.noBassLogged {
			k.noBassLogged = true
			s.logger.Info("bass key pressed, but this speaker has no adjustable bass; the key does nothing here",
				"action", action, "available", b.Avail, "min", b.Min, "max", b.Max)
		}
		return
	}
	next := nextBassLevel(b, dir)
	if next == b.Target {
		s.logger.Info("bass key pressed", "action", action, "level", b.Target, "min", b.Min, "max", b.Max, "atEnd", true)
		s.showBassNotice(b.Target, b.Min, b.Max)
		return
	}
	if err := s.bassWrite(ctx, next); err != nil {
		s.logger.Warn("bass key pressed, but the speaker refused the new level", "action", action, "from", b.Target, "to", next, "err", err)
		return
	}
	s.logger.Info("bass key pressed", "action", action, "from", b.Target, "to", next, "min", b.Min, "max", b.Max, "step", max(1, b.Step))
	s.showBassNotice(next, b.Min, b.Max)
}

// showBassNotice puts the level on this speaker's panel. Like the sleep
// step it shows even with the notices switched off: it is the only answer to
// a key the user just pressed. A seam (showBassNoticeFn).
func (s *Server) showBassNotice(level, lo, hi int) {
	if s.showBassNoticeFn != nil {
		s.showBassNoticeFn(level, lo, hi)
		return
	}
	if !oled.Supported() {
		return
	}
	n := oled.Bass(level, lo, hi)
	n.Bypass = true
	oled.Show(n)
}
