// Package oled draws STR's own splash animation on the SoundTouch Portable's
// OLED panel: after a fresh install, after an update, on a box boot, and while
// an agent update is being received.
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
// Only the Portable's panel geometry is recognised; on every other model, and
// when the user switched it off, all entry points return without touching
// anything.
package oled

import (
	"log/slog"
	"os"
	"strings"
	"sync"
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
func Enabled() bool {
	b, err := os.ReadFile(FlagPath)
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

// busy keeps two animations from fighting over the panel.
var busy sync.Mutex

// ShowAtStart waits for BoseApp to be up and plays the splash for kind once.
// Blocking; run it in its own goroutine.
func ShowAtStart(kind Kind, logger *slog.Logger) {
	if kind == KindNone || !Enabled() || !panelSupported() {
		return
	}
	// At a box boot the agent is usually up before BoseApp has its display
	// thread; wait for it (bounded), then give BoseApp a moment to settle.
	deadline := time.Now().Add(3 * time.Minute)
	for !boseAppDisplayReady() {
		if time.Now().After(deadline) {
			logger.Info("oled splash skipped: BoseApp display thread never appeared")
			return
		}
		time.Sleep(2 * time.Second)
	}
	time.Sleep(3 * time.Second)
	if !busy.TryLock() {
		return
	}
	defer busy.Unlock()
	start := time.Now()
	logo := NewLogo(kind.subtitle(), 6.5, uint64(start.UnixNano()))
	err := play(func(t float64, buf []byte) bool {
		logo.Frame(t, buf)
		return !logo.Done(t)
	}, nil, 15*time.Second, nil)
	logger.Info("oled splash", "kind", kind.String(), "ms", time.Since(start).Milliseconds(), "err", err)
}

// StartUpdating shows the logo with a running bar while an agent update is
// received and written. The returned stop zooms it out and hands the panel
// back; it is only needed on the paths that do not end in a reboot (the
// reboot takes the animation down with the process). Safe to call stop more
// than once. A hard cap releases the panel even if stop is never called.
func StartUpdating(logger *slog.Logger) (stop func()) {
	if !Enabled() || !panelSupported() || !boseAppDisplayReady() || !busy.TryLock() {
		return func() {}
	}
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
// how to draw on (the Portable's).
func Supported() bool { return panelSupported() }
