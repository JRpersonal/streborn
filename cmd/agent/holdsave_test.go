package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/webui"
)

// holdRig drives a holdSaver on a fake clock: sleep advances the clock and
// runs whatever the test scheduled inside the confirmation window, and the
// spawned confirmation runs inline.
type holdRig struct {
	t     *testing.T
	now   time.Time
	hs    *holdSaver
	log   *bytes.Buffer
	mu    sync.Mutex
	saves []int
	// during runs inside the confirmation wait (an activation arriving late).
	during func()
	result webui.HoldSaved
	err    error
}

func newHoldRig(t *testing.T) *holdRig {
	r := &holdRig{t: t, now: time.Date(2026, 10, 10, 6, 7, 4, 0, time.UTC), log: &bytes.Buffer{}}
	logger := slog.New(slog.NewTextHandler(r.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r.hs = newHoldSaver(logger, func(_ context.Context, slot int) (webui.HoldSaved, error) {
		r.mu.Lock()
		r.saves = append(r.saves, slot)
		r.mu.Unlock()
		res := r.result
		res.Slot = slot
		return res, r.err
	})
	r.hs.now = func() time.Time { return r.now }
	r.hs.sleep = func(d time.Duration) {
		if r.during != nil {
			r.during()
		}
		r.now = r.now.Add(d)
	}
	r.hs.spawn = func(f func()) { f() }
	return r
}

func (r *holdRig) at(ms int) time.Time { return r.now.Add(time.Duration(ms) * time.Millisecond) }

func (r *holdRig) press(name string, prod boxlog.Producer, origin boxlog.Origin, ms int) {
	r.hs.NotePress(boxlog.KeyEvent{Key: boxlog.KeyNumber(name), Name: name, Producer: prod,
		State: boxlog.StatePressed, Origin: origin, At: r.at(ms)})
}

func (r *holdRig) gesture(hold bool, ms int) {
	r.hs.NoteGesture(boxlog.PresetGesture{Hold: hold, At: r.at(ms)})
}

func (r *holdRig) savedSlots() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.saves...)
}

// TestHoldSaveSpotifyHold: the field case of #1217. A console press of key 1
// on an ST10 (analytics line, press only) and the firmware's hold verdict
// 1.6 s later save what plays on key 1, no earlier than the tap window.
func TestHoldSaveSpotifyHold(t *testing.T) {
	r := newHoldRig(t)
	r.result = webui.HoldSaved{Kind: "spotify", Name: "Jens Chill"}
	start := r.now
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(true, 1600)
	if got := r.savedSlots(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("saves = %v, want [1]", got)
	}
	if waited := r.now.Sub(start); waited < holdConfirmAfter {
		t.Fatalf("saved %v after the press, before the tap window closed", waited)
	}
	if !strings.Contains(r.log.String(), "hold-to-save: stored what STR plays on the held key") ||
		!strings.Contains(r.log.String(), "kind=spotify") {
		t.Fatalf("log does not record the save:\n%s", r.log.String())
	}
}

// TestHoldSaveVerdictBeforeKeyLine: the analytics line may trail the hold
// verdict; the pair still forms.
func TestHoldSaveVerdictBeforeKeyLine(t *testing.T) {
	r := newHoldRig(t)
	r.result = webui.HoldSaved{Kind: "queue", Name: "Album One"}
	r.gesture(true, 1600)
	r.press("PRESET_4", boxlog.ProducerIRRemote, boxlog.OriginStats, 1900)
	if got := r.savedSlots(); len(got) != 1 || got[0] != 4 {
		t.Fatalf("saves = %v, want [4]", got)
	}
}

// TestHoldSaveQueueAndLibrarySong: the saver hands the slot over whatever
// plays; what is stored is the webui's decision (holdsave_test.go there),
// and the log names it.
func TestHoldSaveQueueAndLibrarySong(t *testing.T) {
	for _, kind := range []string{"queue", "library"} {
		r := newHoldRig(t)
		r.result = webui.HoldSaved{Kind: kind, Name: "One Alpha"}
		r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
		r.gesture(true, 1700)
		if got := r.savedSlots(); len(got) != 1 || got[0] != 2 {
			t.Fatalf("%s: saves = %v, want [2]", kind, got)
		}
		if !strings.Contains(r.log.String(), "kind="+kind) {
			t.Fatalf("%s: log does not name the kind:\n%s", kind, r.log.String())
		}
	}
}

// TestHoldSaveNativeSourceNoAction: on a source the speaker plays itself the
// webui answers ErrHoldNotUPnP; the saver writes nothing and says why.
func TestHoldSaveNativeSourceNoAction(t *testing.T) {
	r := newHoldRig(t)
	r.err = webui.ErrHoldNotUPnP
	r.press("PRESET_3", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(true, 1600)
	if strings.Contains(r.log.String(), "stored what STR plays") {
		t.Fatalf("a native source must not be reported as saved:\n%s", r.log.String())
	}
	if !strings.Contains(r.log.String(), "plays a source of its own and stores it itself") {
		t.Fatalf("missing the native-source line:\n%s", r.log.String())
	}
}

// TestHoldSaveTapNeverSaves: a tap has no hold verdict, only the tap verdict
// and the activation; nothing is saved, not even when a stray hold verdict
// from an earlier press is still pending.
func TestHoldSaveTapNeverSaves(t *testing.T) {
	r := newHoldRig(t)
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(false, 150)
	r.hs.NoteSelection()
	if got := r.savedSlots(); len(got) != 0 {
		t.Fatalf("a tap saved %v", got)
	}

	// A hold verdict nobody claimed, followed by a tap on another key: the
	// tap verdict clears it and vetoes the pairing.
	r = newHoldRig(t)
	r.gesture(true, 0)
	r.gesture(false, 300)
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, 400)
	if got := r.savedSlots(); len(got) != 0 {
		t.Fatalf("a tap after a stray hold verdict saved %v", got)
	}
}

// TestHoldSaveLateActivationVetoes: should the speaker announce an
// activation inside the confirmation window after all (up to 4 s late is the
// bar), nothing is saved.
func TestHoldSaveLateActivationVetoes(t *testing.T) {
	r := newHoldRig(t)
	r.during = func() { r.hs.NoteSelection() }
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(true, 1600)
	if got := r.savedSlots(); len(got) != 0 {
		t.Fatalf("an activation inside the window must veto, saved %v", got)
	}
	if !strings.Contains(r.log.String(), "read the press as a tap") {
		t.Fatalf("missing the veto line:\n%s", r.log.String())
	}
}

// TestHoldSaveOutOfWindowNoPair: a hold verdict long after the press belongs
// to another press.
func TestHoldSaveOutOfWindowNoPair(t *testing.T) {
	r := newHoldRig(t)
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(true, 5000)
	if got := r.savedSlots(); len(got) != 0 {
		t.Fatalf("saves = %v, want none", got)
	}
}

// TestHoldSaveDuplicateRefusal: the one-station-one-key refusal (#836) comes
// back as a HoldSaveError and is logged, nothing else happens.
func TestHoldSaveDuplicateRefusal(t *testing.T) {
	r := newHoldRig(t)
	r.err = &webui.HoldSaveError{Status: 409, Code: "already-on-slot", OtherSlot: 3, OtherName: "Jens Chill"}
	r.press("PRESET_5", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(true, 1600)
	if got := r.savedSlots(); len(got) != 1 {
		t.Fatalf("the save must be attempted once, got %v", got)
	}
	out := r.log.String()
	if !strings.Contains(out, "the preset rules refused it") || !strings.Contains(out, "code=already-on-slot") ||
		strings.Contains(out, "stored what STR plays") {
		t.Fatalf("refusal not logged as such:\n%s", out)
	}
}

// TestHoldSaveIgnoresNetworkKeys: a key STR or the app sent over :8090/key
// never saves, unless the test switch is on, and the switch expires.
func TestHoldSaveIgnoresNetworkKeys(t *testing.T) {
	r := newHoldRig(t)
	r.press("PRESET_2", boxlog.ProducerGabbo, boxlog.OriginWeb, 0)
	r.gesture(true, 1700)
	if got := r.savedSlots(); len(got) != 0 {
		t.Fatalf("a network key saved %v", got)
	}
	if !strings.Contains(r.log.String(), "came over the network") {
		t.Fatalf("missing the network-key line:\n%s", r.log.String())
	}

	r = newHoldRig(t)
	r.hs.SetAcceptAppKeys(true, time.Minute)
	r.press("PRESET_2", boxlog.ProducerGabbo, boxlog.OriginWeb, 0)
	r.gesture(true, 1700)
	if got := r.savedSlots(); len(got) != 1 || got[0] != 2 {
		t.Fatalf("with the test switch on: saves = %v, want [2]", got)
	}

	r = newHoldRig(t)
	r.hs.SetAcceptAppKeys(true, time.Second)
	r.now = r.now.Add(2 * time.Second)
	r.press("PRESET_2", boxlog.ProducerGabbo, boxlog.OriginWeb, 0)
	r.gesture(true, 1700)
	if got := r.savedSlots(); len(got) != 0 {
		t.Fatalf("an expired test switch still accepted a network key: %v", got)
	}

	// A gabbo producer on the analytics line is not covered by the switch.
	r = newHoldRig(t)
	r.hs.SetAcceptAppKeys(true, time.Minute)
	r.press("PRESET_2", boxlog.ProducerGabbo, boxlog.OriginStats, 0)
	r.gesture(true, 1700)
	if got := r.savedSlots(); len(got) != 0 {
		t.Fatalf("the switch must cover only the API key line, saved %v", got)
	}
}

// TestOnKeyEventFeedsHoldSaver: the key trace's preset press reaches the
// saver through OnKeyEvent, and a preset activation reaches it through
// OnPresetSelected.
func TestOnKeyEventFeedsHoldSaver(t *testing.T) {
	r := newHoldRig(t)
	h := &presetWsHandler{logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), holdSaver: r.hs}
	h.OnKeyEvent(boxlog.KeyEvent{Key: 13, Name: "PRESET_2", Producer: boxlog.ProducerConsole,
		State: boxlog.StatePressed, Origin: boxlog.OriginStats, At: r.now})
	r.gesture(true, 1600)
	if got := r.savedSlots(); len(got) != 1 || got[0] != 2 {
		t.Fatalf("saves = %v, want [2]", got)
	}
	if presetKeySlot("PRESET_7") != 0 || presetKeySlot("PLAY") != 0 || presetKeySlot("PRESET_6") != 6 {
		t.Fatal("presetKeySlot maps the wrong keys")
	}
}
