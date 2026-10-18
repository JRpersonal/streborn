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
	// checks are the armed one-shot unpaired checks (holdSaver.after).
	checks      []func()
	checkDelays []time.Duration
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
	r.hs.after = func(d time.Duration, f func()) {
		r.mu.Lock()
		r.checks = append(r.checks, f)
		r.checkDelays = append(r.checkDelays, d)
		r.mu.Unlock()
	}
	return r
}

// fireChecks runs the armed unpaired checks as if their timers fired at
// press + holdUnpairedCheckAfter.
func (r *holdRig) fireChecks() {
	r.mu.Lock()
	fs := r.checks
	r.checks = nil
	r.mu.Unlock()
	r.now = r.now.Add(holdUnpairedCheckAfter)
	for _, f := range fs {
		f()
	}
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

// TestHoldSaveUnpairedPressLeavesTrace: the #1217 field case. A physical
// press of key 3 for which no hold verdict ever arrives must leave one INFO
// line naming the slot once the pair window has passed. A trace press: a
// stats-only press takes the fallback (TestHoldSaveStatsHoldWithoutVerdict).
func TestHoldSaveUnpairedPressLeavesTrace(t *testing.T) {
	r := newHoldRig(t)
	r.press("PRESET_3", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	if len(r.checks) != 1 || r.checkDelays[0] != holdUnpairedCheckAfter {
		t.Fatalf("armed checks = %d (%v), want one at %v", len(r.checks), r.checkDelays, holdUnpairedCheckAfter)
	}
	r.fireChecks()
	out := r.log.String()
	if !strings.Contains(out, "level=INFO msg=\"hold-to-save: no hold verdict paired\"") ||
		!strings.Contains(out, "slot=3") || !strings.Contains(out, "lastVerdict=none") ||
		!strings.Contains(out, "lastVerdictAgeMs=-1") {
		t.Fatalf("missing the unpaired line:\n%s", out)
	}
}

// TestHoldSaveUnpairedAfterTapNamesTheTap: a tap reaches the check too, and
// the line says the newest verdict was a tap and how far after the press.
func TestHoldSaveUnpairedAfterTapNamesTheTap(t *testing.T) {
	r := newHoldRig(t)
	r.press("PRESET_1", boxlog.ProducerIRRemote, boxlog.OriginStats, 0)
	r.gesture(false, 200)
	r.fireChecks()
	out := r.log.String()
	if !strings.Contains(out, "the speaker read a preset key as a tap") || !strings.Contains(out, "pressAgeMs=200") {
		t.Fatalf("the tap verdict is not logged at INFO with the press age:\n%s", out)
	}
	if !strings.Contains(out, "no hold verdict paired") || !strings.Contains(out, "lastVerdict=tap") ||
		!strings.Contains(out, "lastVerdictAfterPressMs=200") {
		t.Fatalf("the unpaired line does not name the tap:\n%s", out)
	}
}

// TestHoldSavePairedPressStaysQuiet: a press that paired logs the hold
// verdict and no unpaired line.
func TestHoldSavePairedPressStaysQuiet(t *testing.T) {
	r := newHoldRig(t)
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(true, 1600)
	r.fireChecks()
	out := r.log.String()
	if !strings.Contains(out, "the speaker read a preset key as a hold") || !strings.Contains(out, "paired=true") {
		t.Fatalf("the paired hold verdict is not logged:\n%s", out)
	}
	if strings.Contains(out, "no hold verdict paired") {
		t.Fatalf("a paired press logged the unpaired line:\n%s", out)
	}
}

// TestHoldSaveVerdictWithoutPressIsInfo: the hold verdict that finds no
// press is logged at INFO now (it was Debug and invisible in #1217), with
// the newest press and its age when there is a stale one.
func TestHoldSaveVerdictWithoutPressIsInfo(t *testing.T) {
	r := newHoldRig(t)
	r.gesture(true, 0)
	out := r.log.String()
	if !strings.Contains(out, "level=INFO msg=\"hold-to-save: the speaker reported a held preset key, no press to pair it with\"") ||
		!strings.Contains(out, "pressSlot=0") || !strings.Contains(out, "pressAgeMs=-1") {
		t.Fatalf("missing the unpaired verdict line:\n%s", out)
	}

	r = newHoldRig(t)
	r.press("PRESET_6", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(true, 6000)
	if out := r.log.String(); !strings.Contains(out, "pressSlot=6") || !strings.Contains(out, "pressAgeMs=6000") {
		t.Fatalf("the stale press is not named:\n%s", out)
	}
}

// TestHoldSaveNoCheckForNetworkKeys: keys sent over :8090/key and releases
// arm no timer; the check is for human presses only.
func TestHoldSaveNoCheckForNetworkKeys(t *testing.T) {
	r := newHoldRig(t)
	r.press("PRESET_2", boxlog.ProducerGabbo, boxlog.OriginWeb, 0)
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.hs.NotePress(boxlog.KeyEvent{Name: "PRESET_2", Producer: boxlog.ProducerConsole,
		State: boxlog.StateReleased, Origin: boxlog.OriginStats, At: r.now})
	// The physical stats press arms the unpaired check and the stats-hold
	// look; the network key and the release arm nothing.
	if len(r.checks) != 2 {
		t.Fatalf("armed checks = %d, want 2 (the physical press only)", len(r.checks))
	}
}

// fireAfter advances the clock by d and runs every armed one-shot.
func (r *holdRig) fireAfter(d time.Duration) {
	r.mu.Lock()
	fs := r.checks
	r.checks = nil
	r.mu.Unlock()
	r.now = r.now.Add(d)
	for _, f := range fs {
		f()
	}
}

// TestHoldSaveStatsHoldWithoutVerdict replays the live ST10 run of
// 2026-10-16: a held key 6 left one stats press line and no verdict at all.
// Read as a hold once the tap window has passed quietly, and saved.
func TestHoldSaveStatsHoldWithoutVerdict(t *testing.T) {
	r := newHoldRig(t)
	r.result = webui.HoldSaved{Kind: "spotify", Name: "Gute-Laune-Mix"}
	r.press("PRESET_6", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.fireAfter(holdConfirmAfter)
	if got := r.savedSlots(); len(got) != 1 || got[0] != 6 {
		t.Fatalf("saves = %v, want [6]\n%s", got, r.log.String())
	}
	if !strings.Contains(r.log.String(), "reading it as a hold") {
		t.Fatalf("missing the fallback line:\n%s", r.log.String())
	}
}

// TestHoldSaveStatsTapNeverSaves replays the taps of the same run: the
// activation came about 0.4 s before the stats line, the tap verdict 3 ms
// after it. Neither may be read as a hold.
func TestHoldSaveStatsTapNeverSaves(t *testing.T) {
	r := newHoldRig(t)
	r.hs.NoteSelection()
	r.now = r.now.Add(400 * time.Millisecond)
	r.press("PRESET_6", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(false, 3)
	r.fireAfter(holdConfirmAfter)
	if got := r.savedSlots(); len(got) != 0 {
		t.Fatalf("a tap saved %v\n%s", got, r.log.String())
	}

	// A tap verdict alone (an empty key activates nothing) vetoes too.
	r = newHoldRig(t)
	r.press("PRESET_6", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(false, 3)
	r.fireAfter(holdConfirmAfter)
	if got := r.savedSlots(); len(got) != 0 {
		t.Fatalf("a tap on an empty key saved %v", got)
	}
}

// TestHoldSaveStatsFallbackNotForNetworkOrTrace: the fallback is for the
// stats-only physical press; a key sent over the network or one the key
// trace reports (with its release) never takes it.
func TestHoldSaveStatsFallbackNotForNetworkOrTrace(t *testing.T) {
	r := newHoldRig(t)
	r.press("PRESET_6", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	r.press("PRESET_5", boxlog.ProducerGabbo, boxlog.OriginStats, 100)
	r.fireAfter(holdConfirmAfter)
	if got := r.savedSlots(); len(got) != 0 {
		t.Fatalf("saved %v without a stats press", got)
	}
}

// key sends one state of a key at ms on the trace (press and release).
func (r *holdRig) key(name string, state boxlog.State, ms int) {
	r.hs.NotePress(boxlog.KeyEvent{Key: boxlog.KeyNumber(name), Name: name, Producer: boxlog.ProducerConsole,
		State: state, Origin: boxlog.OriginTrace, At: r.at(ms)})
}

// TestHoldSaveLeavesKeyCombinationsAlone: #1340. Holding 2 and volume down
// opens the speaker's setup network; the firmware still reads 2 as held on
// the way, and STR swapped the keys and broke the combination. Whichever key
// goes first, nothing is saved.
func TestHoldSaveLeavesKeyCombinationsAlone(t *testing.T) {
	for _, tc := range []struct {
		name          string
		presetMs, vMs int
	}{
		{"volume first", 400, 0},
		{"preset first", 0, 700},
		{"same instant", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newHoldRig(t)
			r.result = webui.HoldSaved{Kind: "spotify", Name: "Mix", SwappedWith: 5}
			if tc.vMs <= tc.presetMs {
				r.key("VOLUME_DOWN", boxlog.StatePressed, tc.vMs)
				r.key("PRESET_2", boxlog.StatePressed, tc.presetMs)
			} else {
				r.key("PRESET_2", boxlog.StatePressed, tc.presetMs)
				r.key("VOLUME_DOWN", boxlog.StatePressed, tc.vMs)
			}
			r.gesture(true, tc.presetMs+2000)
			r.fireAfter(holdConfirmAfter)
			if got := r.savedSlots(); len(got) != 0 {
				t.Fatalf("saves = %v, want none\n%s", got, r.log.String())
			}
			if !strings.Contains(r.log.String(), "a key combination, left to the speaker") {
				t.Fatalf("missing the combination line:\n%s", r.log.String())
			}
		})
	}
}

// TestHoldSaveComboVolumeHeldLong: volume down pressed well before the preset
// and still held (no release) is a combination too; once released, a later
// hold saves again.
func TestHoldSaveComboVolumeHeldLong(t *testing.T) {
	r := newHoldRig(t)
	r.result = webui.HoldSaved{Kind: "spotify", Name: "Mix"}
	r.key("VOLUME_DOWN", boxlog.StatePressed, 0)
	r.key("PRESET_1", boxlog.StatePressed, 5000)
	r.gesture(true, 6600)
	r.fireAfter(holdConfirmAfter)
	if got := r.savedSlots(); len(got) != 0 {
		t.Fatalf("saves = %v during a held volume key, want none", got)
	}
	r.key("PRESET_1", boxlog.StateReleased, 0)
	r.key("VOLUME_DOWN", boxlog.StateReleased, 0)
	r.now = r.now.Add(5 * time.Second)
	r.key("PRESET_1", boxlog.StatePressed, 0)
	r.gesture(true, 1600)
	if got := r.savedSlots(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("saves = %v after the combination ended, want [1]\n%s", got, r.log.String())
	}
}

// TestHoldSaveComboFromStatsLine: on sm2 the analytics line is all there is;
// a volume press there marks the combination the same way.
func TestHoldSaveComboFromStatsLine(t *testing.T) {
	r := newHoldRig(t)
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.press("VOLUME_DOWN", boxlog.ProducerConsole, boxlog.OriginStats, 300)
	r.fireAfter(holdConfirmAfter)
	if got := r.savedSlots(); len(got) != 0 {
		t.Fatalf("saves = %v, want none\n%s", got, r.log.String())
	}
	if r.hs.recentPhysicalPress(1, heldNoticeWindow) {
		t.Fatal("a combination must not count as the user's hold for the keeper's notice")
	}
}

// TestHoldSaveVolumeFromAppIsNoCombo: a volume key a network client sent is
// not a finger on the speaker.
func TestHoldSaveVolumeFromAppIsNoCombo(t *testing.T) {
	r := newHoldRig(t)
	r.result = webui.HoldSaved{Kind: "spotify", Name: "Mix"}
	r.press("VOLUME_DOWN", boxlog.ProducerGabbo, boxlog.OriginWeb, 0)
	r.press("PRESET_3", boxlog.ProducerConsole, boxlog.OriginStats, 100)
	r.gesture(true, 1700)
	if got := r.savedSlots(); len(got) != 1 || got[0] != 3 {
		t.Fatalf("saves = %v, want [3]\n%s", got, r.log.String())
	}
}

// On the sm2 ST10 the key trace sees only the preset key of a combination; the
// firmware's countdown line marks it instead, before or after the lagging
// analytics line.
func TestHoldSaveComboFromFirmwareCountdown(t *testing.T) {
	for _, countdownFirst := range []bool{false, true} {
		r := newHoldRig(t)
		r.result = webui.HoldSaved{Kind: "spotify", Name: "Mix"}
		if countdownFirst {
			r.hs.NoteCombination(r.at(0))
			r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, 1400)
		} else {
			r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, 0)
			r.hs.NoteCombination(r.at(600))
		}
		r.fireAfter(holdConfirmAfter)
		if got := r.savedSlots(); len(got) != 0 {
			t.Fatalf("countdownFirst=%v: saves = %v, want none\n%s", countdownFirst, got, r.log.String())
		}
	}
}
