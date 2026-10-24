// Package oled draws STR's own splash animation on the SoundTouch Portable's
// OLED panel: after a fresh install, after an update, on a box boot, and while
// an agent update is being received. The same panel carries STR's short
// display notices (notice.go) and the hidden games (game.go).
//
// The panel is a plain Linux framebuffer (/dev/fb0, ssdspi, 128x100, 8 bpp)
// that BoseApp fills about six times a second from one thread named
// DirectFBFlipTas. For the few seconds of the animation that one thread is
// held with PTRACE_SEIZE + PTRACE_INTERRUPT (no signal, every other BoseApp
// thread keeps running, playback included) and released afterwards; BoseApp
// then repaints its own screen within a frame. If the agent dies mid-splash
// the kernel releases the thread on its own. Nothing is written to NAND
// except the small splash-seen marker.
//
// Only the SoundTouch panel geometry (ST20, ST30, Portable) is recognised;
// on every other model (the Wave included: its firmware reports the panel,
// its front is a segment display, model.go), and
// when the user switched it off, all entry points return without touching
// anything.
package oled

import (
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Kind selects the splash text.
type Kind int

const (
	KindNone      Kind = iota
	KindBoot           // a real box boot
	KindInstalled      // first start of STR on this box
	KindUpdated        // first start of a new agent version
)

func (k Kind) subtitle() string {
	switch k {
	case KindInstalled:
		return "INSTALLED"
	case KindUpdated:
		return "UPDATED"
	case KindBoot:
		return "REBORN"
	}
	return ""
}

func (k Kind) String() string {
	if k == KindNone {
		return "none"
	}
	return strings.ToLower(k.subtitle())
}

// Paths on the speaker; vars so tests can redirect them.
var (
	// FlagPath is the per-speaker switch. Absent means on.
	FlagPath = "/mnt/nv/streborn/display-splash"
	// SeenPath remembers the agent version the last splash was decided for,
	// so an install and an update are told apart from a plain boot.
	SeenPath = "/mnt/nv/streborn/splash-seen"
)

// Enabled reads the per-speaker switch. On unless the flag file says otherwise.
func Enabled() bool { return flagOn(FlagPath) }

// flagOn reads a per-speaker switch file: absent or anything but 0, false,
// off, no means on.
func flagOn(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(string(b))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// DecideKind picks the splash for this agent start. otaMarker is whether the
// update handler's reboot marker was present (read before anything consumes
// it), stamp the running version, boxBoot whether the box itself just booted
// (as opposed to the agent being respawned on a running box, which gets no
// splash at all). It records stamp in SeenPath when that changes, one small
// NAND write per version.
func DecideKind(stamp string, otaMarker, boxBoot bool) Kind {
	prev, err := os.ReadFile(SeenPath)
	seen := strings.TrimSpace(string(prev))
	kind := KindNone
	switch {
	case otaMarker:
		kind = KindUpdated
	case err != nil:
		kind = KindInstalled
	case seen != stamp:
		kind = KindUpdated // updated another way (stick, SSH)
	case boxBoot:
		kind = KindBoot
	}
	if seen != stamp {
		tmp := SeenPath + ".str-new"
		if os.WriteFile(tmp, []byte(stamp+"\n"), 0o644) == nil {
			_ = os.Rename(tmp, SeenPath)
		}
	}
	return kind
}

// busy keeps two animations from fighting over the panel. The splash, the
// update screen and the games take it through acquirePanel (notice.go), which
// first ends a display notice that holds it.
var busy sync.Mutex

// boseAppSettledAge is how old BoseApp has to be for its display to count as
// booted when the syslog has not said so: an agent restarted under a BoseApp
// that has been up for minutes (a self-update, a crash respawn) starts
// following the log after the display controller's boot lines went by.
const boseAppSettledAge = 2 * time.Minute

// splashMayStart is the gate in front of the panel hold at start-up: the
// flip thread exists, and the display controller has either been seen past
// its boot state or BoseApp is old enough that it must be. Holding the flip
// thread while the controller was still in Bootup left an ST30's panel
// unpainted by the firmware until the next reboot (internal/boxlog,
// displayready.go, 2026-10-23); the flip thread alone appears seconds before
// the controller is done with it.
func splashMayStart(flipThreadUp, controllerUp bool, boseAppAge time.Duration) bool {
	return flipThreadUp && (controllerUp || boseAppAge >= boseAppSettledAge)
}

// displayUpFn is the agent's view of the firmware's display controller
// (internal/boxlog, Reader.DisplayControllerUp), wired once at start through
// SetDisplayUp. Unset means "not seen", so only a settled BoseApp passes.
var displayUpFn atomic.Pointer[func() bool]

// SetDisplayUp wires the check that the firmware's display controller has
// been seen past its boot state. Every panel hold (splash, update screen,
// notices, held-key screens, the track overlay, the games) waits for it, or
// for a settled BoseApp, before it pauses the flip thread. nil unwires it.
func SetDisplayUp(f func() bool) {
	if f == nil {
		displayUpFn.Store(nil)
		return
	}
	displayUpFn.Store(&f)
}

// displayUp reports the wired controller check; false when none is wired.
func displayUp() bool {
	if f := displayUpFn.Load(); f != nil {
		return (*f)()
	}
	return false
}

// panelMayHold is the gate every panel hold goes through: splashMayStart on
// the live state. A hold the gate refuses is skipped or retried by its
// caller, never waited for inside a caller's request.
func panelMayHold() bool { return panelGate(boseAppDisplayReady, displayUp, boseAppAge) }

// panelGate evaluates splashMayStart lazily, cheapest and most decisive
// first: no flip thread ends it before /proc is walked for BoseApp's age, a
// controller seen up ends it before the age is read.
func panelGate(flipThreadUp, controllerUp func() bool, age func() time.Duration) bool {
	if !flipThreadUp() {
		return false
	}
	if controllerUp() {
		return true
	}
	return splashMayStart(true, false, age())
}

// holdAttrs are the log fields every panel hold carries (one line per hold,
// never per frame), so a display that stays dark can be matched against the
// state the hold started in.
func holdAttrs(kind string) []any {
	return []any{"holdKind", kind, "controllerUp", displayUp(), "boseAppAge", boseAppAge().Round(time.Second)}
}

// ShowAtStart waits for BoseApp's display to be up and plays the splash for
// kind once. up reports whether the firmware's display controller has been
// seen past its boot state; when non-nil it is wired through SetDisplayUp
// for every other panel hold too (nil keeps what is wired). Blocking; run it
// in its own goroutine.
func ShowAtStart(kind Kind, logger *slog.Logger, up func() bool) {
	if up != nil {
		SetDisplayUp(up)
	}
	if kind == KindNone || !Enabled() || !panelSupported() {
		return
	}
	// At a box boot the agent is usually up before BoseApp has its display
	// thread, and the thread is there before the display controller is done
	// booting; wait for both (bounded), then give BoseApp a moment to settle.
	// Past the bound the splash is skipped: a splash is cosmetic, a panel the
	// firmware never paints again is not.
	deadline := time.Now().Add(3 * time.Minute)
	for !panelMayHold() {
		if time.Now().After(deadline) {
			logger.Info("oled splash skipped: BoseApp display not up in time",
				"flipThread", boseAppDisplayReady(), "controllerUp", displayUp(), "boseAppAge", boseAppAge().Round(time.Second))
			return
		}
		time.Sleep(2 * time.Second)
	}
	time.Sleep(3 * time.Second)
	// A notice that is up gives way and shows again after the splash.
	if !acquirePanel(true) {
		return
	}
	defer busy.Unlock()
	start := time.Now()
	logo := NewLogo(kind.subtitle(), 6.5, uint64(start.UnixNano()))
	err := play(func(t float64, buf []byte) bool {
		logo.Frame(t, buf)
		return !logo.Done(t)
	}, nil, 15*time.Second, nil)
	logger.Info("oled splash", append([]any{"kind", kind.String(), "ms", time.Since(start).Milliseconds(), "err", err}, holdAttrs("splash")...)...)
}

// StartUpdating shows the logo with a running bar while an agent update is
// received and written. The returned stop zooms it out and hands the panel
// back; it is only needed on the paths that do not end in a reboot (the
// reboot takes the animation down with the process). Safe to call stop more
// than once. A hard cap releases the panel even if stop is never called.
func StartUpdating(logger *slog.Logger) (stop func()) {
	if !Enabled() || !panelSupported() {
		return func() {}
	}
	// No wait: an update that arrives while the speaker's display is still
	// starting goes ahead without its screen rather than hold a booting panel.
	if !panelMayHold() {
		logger.Info("oled update splash skipped: speaker display still starting", holdAttrs("update")...)
		return func() {}
	}
	if !acquirePanel(false) {
		return func() {}
	}
	logger.Info("oled panel hold", holdAttrs("update")...)
	quit := make(chan struct{})
	var once sync.Once
	done := make(chan struct{})
	logo := NewLogo("UPDATING", 0, uint64(time.Now().UnixNano()))
	logo.Hold = true
	go func() {
		defer close(done)
		defer busy.Unlock()
		err := play(func(t float64, buf []byte) bool {
			logo.Frame(t, buf)
			return !logo.Done(t)
		}, func(t float64) { logo.Exit(t) }, 4*time.Minute, quit)
		if err != nil {
			logger.Info("oled update splash ended", "err", err)
		}
	}()
	return func() {
		once.Do(func() { close(quit) })
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
}

// Supported reports whether this speaker has the one panel the splash knows
// how to draw on (the Portable's, also in the ST20 and ST30; not the Wave).
func Supported() bool { return panelSupported() }
