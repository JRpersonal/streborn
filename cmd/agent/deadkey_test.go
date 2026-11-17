package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/webui"
)

// deadRig is a holdRig whose speaker lost the keys in dead (the store holds
// them, the speaker's own list does not) while it plays source.
type deadRig struct {
	*holdRig
	dmu    sync.Mutex
	played []int
	reads  int
}

func newDeadRig(t *testing.T, source string, dead ...int) *deadRig {
	r := &deadRig{holdRig: newHoldRig(t)}
	lost := map[int]bool{}
	for _, s := range dead {
		lost[s] = true
	}
	r.hs.deadKey = func(slot int) deadKeyVerdict {
		r.dmu.Lock()
		r.reads++
		r.dmu.Unlock()
		if !lost[slot] {
			return deadKeyVerdict{Why: "the store holds nothing on this key"}
		}
		return deadKeyVerdict{Dead: true, Name: "Station " + string(rune('0'+slot)), Type: "radio",
			Source: source, Why: "the store holds a preset on this key, the speaker's own list lacks it"}
	}
	r.hs.playDeadKey = func(slot int, _ deadKeyVerdict) {
		r.dmu.Lock()
		r.played = append(r.played, slot)
		r.dmu.Unlock()
	}
	return r
}

func (r *deadRig) playedSlots() []int {
	r.dmu.Lock()
	defer r.dmu.Unlock()
	return append([]int(nil), r.played...)
}

// TestDeadKeyPressDuringPandoraPlaysTheKey replays #1931: a ST10 (sm2, the
// analytics line only) plays Pandora, its own key list is empty while STR's
// store holds keys 1 to 5, and key 1 is pressed on the remote. Nothing at all
// comes of it on the speaker. STR plays key 1 itself instead of reading the
// press as a hold, and the press is never taken for a store of the user's.
func TestDeadKeyPressDuringPandoraPlaysTheKey(t *testing.T) {
	r := newDeadRig(t, "PANDORA", 1, 3)
	r.err = webui.ErrHoldNotUPnP
	start := r.now
	r.press("PRESET_1", boxlog.ProducerIRRemote, boxlog.OriginStats, 0)
	r.fireAfter(holdConfirmAfter)
	if got := r.playedSlots(); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("played %v, want [1]\n%s", got, r.log.String())
	}
	if got := r.savedSlots(); len(got) != 0 {
		t.Fatalf("a dead key was read as a hold and saved onto: %v", got)
	}
	if waited := r.now.Sub(start); waited < deadKeyDecideAfter {
		t.Fatalf("played %v after the press, before a firmware store could show itself", waited)
	}
	log := r.log.String()
	for _, want := range []string{"dead key: the speaker lost key 1", "slot=1", "source=PANDORA", "why=", "origin=stats"} {
		if !strings.Contains(log, want) {
			t.Fatalf("the dead-key line lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "reading it as a hold") {
		t.Fatalf("the dead key must not be read as a hold:\n%s", log)
	}
	// A re-sync PUT for the key right after is no store of the user's.
	if r.hs.recentPhysicalHold(1, heldNoticeWindow) {
		t.Fatal("a dead-key press counted as a hold for the marge keeper's notice")
	}
}

// TestDeadKeyEmptyInStoreAndBoxKeepsTheHoldSave: a key empty in BOTH the
// store and the speaker's list is no dead key. The analytics-line press with
// nothing after it is read as a hold and saves what STR plays, as before.
func TestDeadKeyEmptyInStoreAndBoxKeepsTheHoldSave(t *testing.T) {
	r := newDeadRig(t, "UPNP", 1) // key 6 is empty everywhere
	r.result = webui.HoldSaved{Kind: "spotify", Name: "Gute-Laune-Mix"}
	r.press("PRESET_6", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.fireAfter(holdConfirmAfter)
	if got := r.savedSlots(); !reflect.DeepEqual(got, []int{6}) {
		t.Fatalf("saves = %v, want [6]\n%s", got, r.log.String())
	}
	if got := r.playedSlots(); len(got) != 0 {
		t.Fatalf("an empty key was played: %v", got)
	}
	if !strings.Contains(r.log.String(), "reading it as a hold") {
		t.Fatalf("missing the hold line:\n%s", r.log.String())
	}
}

// TestDeadKeyNotForWorkingKeys: a tap whose key activated (a working key) or
// a hold paired with its verdict never reaches the dead-key decision, and the
// speaker's list is not even read for them.
func TestDeadKeyNotForWorkingKeys(t *testing.T) {
	r := newDeadRig(t, "PANDORA", 1)
	r.hs.NoteSelection()
	r.now = r.now.Add(400 * time.Millisecond)
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(false, 3)
	r.fireAfter(holdConfirmAfter)
	if len(r.playedSlots()) != 0 || r.reads != 0 {
		t.Fatalf("a tap that activated was taken for a dead key (played %v, list reads %d)", r.playedSlots(), r.reads)
	}

	r = newDeadRig(t, "UPNP", 2)
	r.result = webui.HoldSaved{Kind: "spotify", Name: "Mix"}
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.gesture(true, 1600)
	r.fireAfter(holdConfirmAfter)
	if got := r.savedSlots(); !reflect.DeepEqual(got, []int{2}) || len(r.playedSlots()) != 0 {
		t.Fatalf("a hold with its verdict: saves %v played %v", got, r.playedSlots())
	}
}

// TestDeadKeyHoldStoredByTheFirmware: holding a dead key while the speaker
// plays a source it stores itself makes the firmware put that source to
// marge. That store says the press was a hold: the key is not played.
func TestDeadKeyHoldStoredByTheFirmware(t *testing.T) {
	r := newDeadRig(t, "PANDORA", 1)
	r.err = webui.ErrHoldNotUPnP
	r.during = func() { r.hs.NoteFirmwareStore(1) }
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.fireAfter(holdConfirmAfter)
	if got := r.playedSlots(); len(got) != 0 {
		t.Fatalf("a hold the firmware stored was played: %v\n%s", got, r.log.String())
	}
	if !strings.Contains(r.log.String(), "the speaker stored what plays on the key itself") {
		t.Fatalf("missing the reason:\n%s", r.log.String())
	}
	// A store on ANOTHER key says nothing about this press.
	r = newDeadRig(t, "PANDORA", 1)
	r.during = func() { r.hs.NoteFirmwareStore(4) }
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.fireAfter(holdConfirmAfter)
	if got := r.playedSlots(); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("played %v, want [1]", got)
	}
}

// TestDeadKeyLateActivationAndNewerPress: an activation or a newer press
// inside the decision window stands the dead-key play down.
func TestDeadKeyLateActivationAndNewerPress(t *testing.T) {
	r := newDeadRig(t, "PANDORA", 1)
	r.during = func() { r.hs.NoteSelection() }
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.fireAfter(holdConfirmAfter)
	if got := r.playedSlots(); len(got) != 0 {
		t.Fatalf("played %v after the speaker activated a key", got)
	}

	r = newDeadRig(t, "PANDORA", 1, 3)
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginStats, 0)
	r.press("PRESET_3", boxlog.ProducerConsole, boxlog.OriginStats, 500)
	r.fireAfter(holdConfirmAfter)
	if got := r.playedSlots(); !reflect.DeepEqual(got, []int{3}) {
		t.Fatalf("played %v, want only the newest press [3]", got)
	}
}

// TestDeadKeyTracePress: on a chassis whose key trace reports the release, a
// short press with only a tap verdict on a dead key is played; a key held
// past the tap limit is not.
func TestDeadKeyTracePress(t *testing.T) {
	r := newDeadRig(t, "AUX", 2)
	r.key("PRESET_2", boxlog.StatePressed, 0)
	r.key("PRESET_2", boxlog.StateReleased, 200)
	r.gesture(false, 210)
	r.fireAfter(holdConfirmAfter)
	if got := r.playedSlots(); !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("played %v, want [2]\n%s", got, r.log.String())
	}

	r = newDeadRig(t, "AUX", 2)
	r.key("PRESET_2", boxlog.StatePressed, 0)
	r.key("PRESET_2", boxlog.StateReleased, 2000)
	r.fireAfter(holdConfirmAfter)
	if got := r.playedSlots(); len(got) != 0 {
		t.Fatalf("a held key was played: %v", got)
	}
}

// TestDeadKeyNotForNetworkKeys: a key the app or STR sent is nobody at the
// speaker; it never plays a dead key.
func TestDeadKeyNotForNetworkKeys(t *testing.T) {
	r := newDeadRig(t, "PANDORA", 5)
	r.press("PRESET_5", boxlog.ProducerGabbo, boxlog.OriginStats, 0)
	r.press("PRESET_5", boxlog.ProducerGabbo, boxlog.OriginWeb, 100)
	r.fireAfter(holdConfirmAfter)
	if len(r.playedSlots()) != 0 || r.reads != 0 {
		t.Fatalf("a network key played a dead key (played %v, reads %d)", r.playedSlots(), r.reads)
	}
}

// TestDeadKeyFromList pins the decision on the store and the speaker's list.
func TestDeadKeyFromList(t *testing.T) {
	radio := presets.Preset{Slot: 1, Name: "Radio One", Type: "radio"}
	listed := []boxPresetEntry{{Slot: 1, Location: "x"}, {Slot: 2}}
	cases := []struct {
		name    string
		slot    int
		have    bool
		entries []boxPresetEntry
		err     error
		dead    bool
	}{
		{"empty speaker list", 1, true, nil, nil, true},
		{"speaker lacks the key", 1, true, []boxPresetEntry{{Slot: 2}}, nil, true},
		{"speaker lists the key", 1, true, listed, nil, false},
		{"store holds nothing", 1, false, nil, nil, false},
		{"list unreadable", 1, true, nil, errors.New("timeout"), false},
		{"second row is no hardware key", 7, true, nil, nil, false},
	}
	for _, c := range cases {
		v := deadKeyFromList(c.slot, radio, c.have, c.entries, c.err)
		if v.Dead != c.dead || v.Why == "" {
			t.Errorf("%s: %+v, want dead=%v with a reason", c.name, v, c.dead)
		}
	}
}

// TestPlayDeadKeyRecallsAndAsksForTheResync: the handler plays the key
// through the app's recall path and arms the dead-key ask with its consent.
func TestPlayDeadKeyRecallsAndAsksForTheResync(t *testing.T) {
	var buf bytes.Buffer
	asks := newResyncAsks()
	var recalled []int
	h := &presetWsHandler{
		logger: slog.New(slog.NewTextHandler(&buf, nil)),
		resync: asks,
		playSlotFn: func(_ context.Context, slot int) (string, error) {
			recalled = append(recalled, slot)
			return "Radio One", nil
		},
	}
	h.playDeadKey(context.Background(), 1, deadKeyVerdict{Dead: true, Name: "Radio One", Source: "PANDORA"})
	if !reflect.DeepEqual(recalled, []int{1}) {
		t.Fatalf("recalled %v, want [1]", recalled)
	}
	if !asks.ask.Load() || !asks.keysDead.Load() {
		t.Fatal("the dead-key press must arm the re-sync ask with its dead-key evidence")
	}
	if ok, _ := asks.deadKeyConsent(time.Now()); !ok {
		t.Fatal("the dead-key press must carry the consent to leave the source")
	}
	if asker, _ := asks.asker.Load().(string); asker != "dead-key" {
		t.Fatalf("asker = %q", asker)
	}
	// A second press within the budget does not arm a second ask, but it
	// renews the consent.
	asks.ask.Store(false)
	asks.deadKeyAt.Store(0)
	h.playDeadKey(context.Background(), 3, deadKeyVerdict{Dead: true, Name: "Radio Three", Source: "UPNP"})
	if asks.ask.Load() {
		t.Fatal("a second dead-key press inside the budget armed another ask")
	}
	if ok, _ := asks.deadKeyConsent(time.Now()); !ok {
		t.Fatal("the second press must renew the consent")
	}
	if !strings.Contains(buf.String(), "dead key: playing slot 1") {
		t.Fatalf("missing the play line:\n%s", buf.String())
	}
}

// TestStepDeadKeyPressReleasesThePassHeldOnPandora: #1931. The forced pass
// was held on Pandora, a source the listener chose, with no time limit. A
// press on a dead key is consent to leave it: the held pass runs at its next
// retry, and the consent is used up with it.
func TestStepDeadKeyPressReleasesThePassHeldOnPandora(t *testing.T) {
	rig := newLoopRig(t).steady()
	r := rig.r
	rig.src, rig.playing = "PANDORA", true
	r.forceHeld = true

	r.step()
	if len(rig.passes) != 0 || r.userHeldSrc != "PANDORA" {
		t.Fatalf("without a dead-key press the pass stays held on PANDORA (passes %v, held on %q)", rig.passes, r.userHeldSrc)
	}

	r.asks.requestDeadKey(r.logger, 1, "PANDORA")
	r.step()
	if !reflect.DeepEqual(rig.passes, []bool{true}) {
		t.Fatalf("the dead-key press must run the held pass on PANDORA (passes %v)", rig.passes)
	}
	if r.asks.deadKeyAt.Load() != 0 || r.forceHeld || r.userHeldSrc != "" {
		t.Fatal("the pass that ran must use up the consent and clear the hold")
	}

	// The listener goes back to Pandora: a later forced pass waits on it
	// again.
	rig.passes = nil
	r.asks.ask.Store(false)
	r.forceHeld = true
	r.step()
	if len(rig.passes) != 0 {
		t.Fatalf("a used-up consent let a pass run on PANDORA (passes %v)", rig.passes)
	}
}

// TestStepNoOtherAskerLeavesAListenersSource: the consent is the dead-key
// press's alone. Every other asker, and a dead-key press older than the
// window, still waits out the listener's source, and a speaker in setup is
// never written into.
func TestStepNoOtherAskerLeavesAListenersSource(t *testing.T) {
	for _, asker := range []string{"thumb", "power-wake", "standby-exit", "1036", "paired"} {
		rig := newLoopRig(t).steady()
		rig.src, rig.playing = "PANDORA", true
		rig.r.asks.request(nil, asker)
		rig.r.step()
		if len(rig.passes) != 0 {
			t.Fatalf("%s: a forced pass ran on PANDORA (passes %v)", asker, rig.passes)
		}
	}
	rig := newLoopRig(t).steady()
	rig.src, rig.playing = "PANDORA", true
	rig.r.forceHeld = true
	rig.r.asks.deadKeyAt.Store(time.Now().Add(-deadKeyConsentWindow - time.Second).UnixNano())
	rig.r.step()
	if len(rig.passes) != 0 {
		t.Fatalf("a stale dead-key press let a pass run on PANDORA (passes %v)", rig.passes)
	}
	rig = newLoopRig(t).steady()
	rig.src = "SETUP_WIFI"
	rig.r.forceHeld = true
	rig.r.defaultGateway = func() string { return "" }
	rig.r.asks.requestDeadKey(nil, 1, "SETUP_WIFI")
	rig.r.step()
	if len(rig.passes) != 0 {
		t.Fatalf("a dead-key press let a pass run into setup (passes %v)", rig.passes)
	}
}
