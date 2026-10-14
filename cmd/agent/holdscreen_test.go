package main

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/webui"
)

// screenLog records what the hold saver told the held-key screen.
type screenLog struct {
	mu     sync.Mutex
	events []string
}

func (s *screenLog) add(e string) { s.mu.Lock(); s.events = append(s.events, e); s.mu.Unlock() }

func (s *screenLog) Start(slot int, _ time.Time)    { s.add(fmt.Sprintf("start %d", slot)) }
func (s *screenLog) Released(slot int, _ time.Time) { s.add(fmt.Sprintf("release %d", slot)) }
func (s *screenLog) Cancel(slot int, reason string) {
	s.add(fmt.Sprintf("cancel %d: %s", slot, reason))
}

func (s *screenLog) get() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

func newScreenRig(t *testing.T) (*holdRig, *screenLog, *noticeLog) {
	r := newHoldRig(t)
	sl, nl := &screenLog{}, &noticeLog{}
	r.hs.screen = sl
	r.hs.notify = nl.add
	return r, sl, nl
}

// fireDelay runs the armed timers of exactly delay d, as if they fired now.
func (r *holdRig) fireDelay(d time.Duration) int {
	r.mu.Lock()
	var run []func()
	var keepF []func()
	var keepD []time.Duration
	for i, f := range r.checks {
		if r.checkDelays[i] == d {
			run = append(run, f)
		} else {
			keepF, keepD = append(keepF, f), append(keepD, r.checkDelays[i])
		}
	}
	r.checks, r.checkDelays = keepF, keepD
	r.mu.Unlock()
	for _, f := range run {
		f()
	}
	return len(run)
}

func (r *holdRig) release(name string, ms int) {
	r.hs.NotePress(boxlog.KeyEvent{Key: boxlog.KeyNumber(name), Name: name, Producer: boxlog.ProducerConsole,
		State: boxlog.StateReleased, Origin: boxlog.OriginTrace, At: r.at(ms)})
}

func wantEvents(t *testing.T, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("screen events %q, want %q", got, want)
	}
}

// TestHeldScreenStartsBeforeTheFirmware: the Portable field case. The key is
// still down heldScreenAfter after the press, so the held screen starts
// before the firmware's 2.0 s hold verdict; the save's outcome goes out as
// an ordinary notice (the display routes it into the session) and the
// release is passed on.
func TestHeldScreenStartsBeforeTheFirmware(t *testing.T) {
	// above the longest tap (under 1.75 s), below the Portable's 2.0 s verdict
	if heldScreenAfter >= 2*time.Second || heldScreenAfter < 1750*time.Millisecond {
		t.Fatalf("heldScreenAfter %v must sit between the longest tap and the Portable's 2.0 s hold verdict", heldScreenAfter)
	}
	r, sl, nl := newScreenRig(t)
	r.result = webui.HoldSaved{Kind: "spotify", Name: "Jens Chill", Unchanged: true}
	r.press("PRESET_6", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	if n := r.fireDelay(heldScreenAfter); n != 1 {
		t.Fatalf("held-screen timers armed: %d (delays %v)", n, r.checkDelays)
	}
	r.gesture(true, 2003)
	r.release("PRESET_6", 4015)
	wantEvents(t, sl.get(), "start 6", "release 6")
	nl.only(t, oled.NoticeSavedByKey, 6)
}

// TestHeldScreenLateLineArmsFromThePress: a press line read late arms the
// timer for what is left of heldScreenAfter, not the whole of it.
func TestHeldScreenLateLineArmsFromThePress(t *testing.T) {
	r, _, _ := newScreenRig(t)
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginTrace, -500)
	if r.fireDelay(heldScreenAfter-500*time.Millisecond) != 1 {
		t.Fatalf("delays %v, want one at %v", r.checkDelays, heldScreenAfter-500*time.Millisecond)
	}
}

// TestHeldScreenTapLeavesThePanel: a key released before heldScreenAfter, or
// a tap verdict, never starts the screen.
func TestHeldScreenTapLeavesThePanel(t *testing.T) {
	r, sl, _ := newScreenRig(t)
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	r.release("PRESET_2", 300)
	r.fireDelay(heldScreenAfter)
	wantEvents(t, sl.get())

	// sm2: no release line, but the firmware's tap verdict
	r, sl, _ = newScreenRig(t)
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(false, 400)
	r.fireDelay(heldScreenAfter)
	wantEvents(t, sl.get())

	// a newer press superseded it
	r, sl, _ = newScreenRig(t)
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	r.press("PRESET_3", boxlog.ProducerConsole, boxlog.OriginTrace, 600)
	r.mu.Lock()
	first := r.checks[1] // PRESET_2's held-screen timer (after its unpaired check)
	if r.checkDelays[1] != heldScreenAfter {
		r.mu.Unlock()
		t.Fatalf("delays %v", r.checkDelays)
	}
	r.mu.Unlock()
	first()
	wantEvents(t, sl.get())

	// a key sent over the network: no timer at all
	r, sl, _ = newScreenRig(t)
	r.press("PRESET_2", boxlog.ProducerGabbo, boxlog.OriginWeb, 0)
	if r.fireDelay(heldScreenAfter) != 0 {
		t.Fatal("held-screen timer for a network key")
	}
	wantEvents(t, sl.get())
}

// TestHeldScreenEnds: every path without an outcome ends the screen.
func TestHeldScreenEnds(t *testing.T) {
	// Released between 1.8 and 2.0 s on the Portable: a tap after all.
	r, sl, _ := newScreenRig(t)
	r.press("PRESET_4", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	r.fireDelay(heldScreenAfter)
	r.release("PRESET_4", 1900)
	r.gesture(false, 1950)
	wantEvents(t, sl.get(), "start 4", "release 4", "cancel 4: a tap after all")

	// The save failed.
	r, sl, _ = newScreenRig(t)
	r.err = errors.New("box unreachable")
	r.press("PRESET_4", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	r.fireDelay(heldScreenAfter)
	r.gesture(true, 2000)
	wantEvents(t, sl.get(), "start 4", "cancel 4: save failed")

	// Held, but the firmware never said "hold".
	r, sl, _ = newScreenRig(t)
	r.press("PRESET_4", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	r.fireDelay(heldScreenAfter)
	r.fireDelay(holdUnpairedCheckAfter)
	wantEvents(t, sl.get(), "start 4", "cancel 4: no hold verdict")

	// A native source: holdsave leaves it to the keeper, the screen waits
	// for the keeper's notice (or its own timeout) and is not cancelled.
	r, sl, nl := newScreenRig(t)
	r.err = fmt.Errorf("%w (source TUNEIN)", webui.ErrHoldNotUPnP)
	r.press("PRESET_4", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	r.fireDelay(heldScreenAfter)
	r.gesture(true, 2000)
	wantEvents(t, sl.get(), "start 4")
	nl.none(t)
}

// TestHeldScreenEmptyKeyWaitsForTheVerdict: with the held screen up, the
// empty-key look waits until a hold has had time to show itself, so a held
// empty key ends with the save's outcome, not with EMPTY first.
func TestHeldScreenEmptyKeyWaitsForTheVerdict(t *testing.T) {
	r, _, nl := newScreenRig(t)
	r.hs.emptyKey = func(int) (bool, bool) { return true, true }
	r.result = webui.HoldSaved{Kind: "spotify", Name: "Jens Chill"}
	r.press("PRESET_5", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	r.fireDelay(heldScreenAfter)
	r.fireDelay(emptyKeyCheckAfter) // fires before the 2.0 s verdict
	r.gesture(true, 2003)
	r.fireDelay(holdConfirmAfter - emptyKeyCheckAfter)
	nl.only(t, oled.NoticeSavedByKey, 5)

	// Held on an empty key with no hold verdict at all: EMPTY on the second look.
	r, _, nl = newScreenRig(t)
	r.hs.emptyKey = func(int) (bool, bool) { return true, true }
	r.press("PRESET_5", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	r.fireDelay(heldScreenAfter)
	r.fireDelay(emptyKeyCheckAfter)
	nl.none(t)
	r.fireDelay(holdConfirmAfter - emptyKeyCheckAfter)
	nl.only(t, oled.NoticeEmptyKey, 5)
}
