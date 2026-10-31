package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/boxws"
	"github.com/JRpersonal/streborn/internal/oled"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/webui"
)

// Tap a preset key, then hold it (#1110): the hold saves what played before
// the tap onto the key's second row, never onto the key itself.

// tapHoldRig is a holdRig with the tap+hold wiring recorded.
type tapHoldRig struct {
	*holdRig
	screen  *screenLog
	notices *noticeLog

	mu       sync.Mutex
	rowTwo   []int // slots the tap+hold save wrote
	snaps    []webui.PlayingSnapshot
	plays    []int // slots played after a tap+hold
	saveRes  webui.HoldSaved
	saveErr  error
	grouped  bool
	known    bool
	presses  int // what the key trace reports
	traceAsk int
}

func newTapHoldRig(t *testing.T) *tapHoldRig {
	r, sl, nl := newScreenRig(t)
	tr := &tapHoldRig{holdRig: r, screen: sl, notices: nl, known: true}
	r.hs.tapHold = tapHoldWiring{
		save: func(_ context.Context, snap webui.PlayingSnapshot, slot int) (webui.HoldSaved, error) {
			tr.mu.Lock()
			defer tr.mu.Unlock()
			tr.rowTwo = append(tr.rowTwo, slot)
			tr.snaps = append(tr.snaps, snap)
			res := tr.saveRes
			res.Slot = slot
			if res.Name == "" {
				res.Name = snap.Name()
			}
			return res, tr.saveErr
		},
		play: func(_ context.Context, _, slot int) {
			tr.mu.Lock()
			tr.plays = append(tr.plays, slot)
			tr.mu.Unlock()
		},
		grouped: func() (bool, bool) { return tr.grouped, tr.known },
		tracePresses: func(int, time.Time) int {
			tr.mu.Lock()
			defer tr.mu.Unlock()
			tr.traceAsk++
			return tr.presses
		},
	}
	return tr
}

var spotifyBefore = webui.PlayingSnapshot{Kind: "spotify", Source: "UPNP",
	Preset: presets.Preset{Name: "Jens Chill", Type: "spotify", URI: "spotify:playlist:37i9dQZF1DX0AMssoUKCz7"}}

// tap is the tap's selection frame at ms, with what played before it. As in
// OnPresetSelected, the frame is also an activation for the hold saver.
func (r *tapHoldRig) tap(key, ms int, before webui.PlayingSnapshot) {
	at := r.at(ms)
	saved := r.now
	r.now = at
	r.hs.NoteSelection()
	r.now = saved
	r.hs.NoteTap(key, at, false, func() webui.PlayingSnapshot { return before })
}

func (r *tapHoldRig) got() (rowTwo, plays []int, snaps []webui.PlayingSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.rowTwo...), append([]int(nil), r.plays...), append([]webui.PlayingSnapshot(nil), r.snaps...)
}

func (r *tapHoldRig) wantNoRowOneWrite(t *testing.T) {
	t.Helper()
	if s := r.savedSlots(); len(s) != 0 {
		t.Fatalf("row 1 was written: hold-to-save saved %v", s)
	}
}

func (r *tapHoldRig) wantLog(t *testing.T, s string) {
	t.Helper()
	if !strings.Contains(r.log.String(), s) {
		t.Fatalf("log lacks %q:\n%s", s, r.log.String())
	}
}

// The BCO/taigan case: the key trace names the hold's press, the firmware's
// hold verdict pairs with it, and after the tap window slot 7 gets what
// played before the tap and plays.
func TestTapHoldSavesOnRowTwo(t *testing.T) {
	r := newTapHoldRig(t)
	start := r.now
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginTrace, -250)
	r.release("PRESET_1", -130)
	r.gesture(false, -130) // the tap's own verdict, at its release
	r.tap(1, 0, spotifyBefore)
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginTrace, 250)
	// the held screen is armed from the hold's press: 1.8 s after 250 ms
	if r.fireDelay(heldScreenAfter+250*time.Millisecond) != 1 {
		t.Fatalf("held-screen timer not armed (delays %v)", r.checkDelays)
	}
	wantEvents(t, r.screen.get(), "start 2x 1")
	r.gesture(true, 1850)
	rowTwo, plays, snaps := r.got()
	if len(rowTwo) != 1 || rowTwo[0] != 7 || snaps[0].Kind != "spotify" {
		t.Fatalf("row-2 saves %v %+v, want slot 7 with the Spotify playlist", rowTwo, snaps)
	}
	if len(plays) != 1 || plays[0] != 7 {
		t.Fatalf("played %v, want slot 7", plays)
	}
	if waited := r.now.Sub(start); waited < 250*time.Millisecond+holdConfirmAfter {
		t.Fatalf("saved %v after the tap, before the hold's tap window closed", waited)
	}
	r.wantNoRowOneWrite(t)
	n := r.notices.only(t, oled.NoticeSavedByKey, 1)
	if n.Line1 != "SAVED 2X 1" {
		t.Fatalf("notice %+v, want SAVED 2X 1", n)
	}
	r.wantLog(t, "tap+hold on key 1 -> saved 'Jens Chill' on row 2 slot 7")
}

// The held-key screen of the hold names the second row.
func TestTapHoldScreenNamesRowTwo(t *testing.T) {
	r := newTapHoldRig(t)
	r.tap(3, 0, spotifyBefore)
	r.press("PRESET_3", boxlog.ProducerConsole, boxlog.OriginTrace, 300)
	r.fireDelay(heldScreenAfter + 300*time.Millisecond)
	wantEvents(t, r.screen.get(), "start 2x 3")

	// A plain hold keeps the plain screen.
	p := newTapHoldRig(t)
	p.press("PRESET_3", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	p.fireDelay(heldScreenAfter)
	wantEvents(t, p.screen.get(), "start 3")
}

// The sm2 ST10: the analytics line reports both presses, a tap has its tap
// verdict and activation, the hold has neither. statsHold reads it as the
// hold, and the tap before it makes it a row-2 save.
func TestTapHoldStatsOnlyHold(t *testing.T) {
	r := newTapHoldRig(t)
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, -250)
	r.gesture(false, -130)
	r.tap(2, 0, spotifyBefore)
	r.press("PRESET_2", boxlog.ProducerConsole, boxlog.OriginStats, 280)
	// the tap's own statsHold finds a newer press and stands down; the
	// hold's fires holdConfirmAfter after its press
	r.fireDelay(0)
	r.fireDelay(280*time.Millisecond + holdConfirmAfter)
	rowTwo, plays, _ := r.got()
	if len(rowTwo) != 1 || rowTwo[0] != 8 || len(plays) != 1 || plays[0] != 8 {
		t.Fatalf("row-2 saves %v plays %v, want slot 8", rowTwo, plays)
	}
	r.wantNoRowOneWrite(t)
	r.wantLog(t, "reading it as a hold")
}

// A hold well after the tap is a plain hold of the key, as before.
func TestTapHoldOutsideTheWindow(t *testing.T) {
	r := newTapHoldRig(t)
	r.result = webui.HoldSaved{Kind: "spotify", Name: "X"}
	r.tap(1, 0, spotifyBefore)
	ms := int((tapHoldWindow + 300*time.Millisecond) / time.Millisecond)
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginTrace, ms)
	r.gesture(true, ms+1600)
	if rowTwo, _, _ := r.got(); len(rowTwo) != 0 {
		t.Fatalf("row-2 saves %v, want none", rowTwo)
	}
	if s := r.savedSlots(); len(s) != 1 || s[0] != 1 {
		t.Fatalf("hold-to-save %v, want today's row-1 save of key 1", s)
	}
}

// No tap before the hold: exactly today's hold-to-save.
func TestTapHoldPlainHoldUnchanged(t *testing.T) {
	r := newTapHoldRig(t)
	r.result = webui.HoldSaved{Kind: "spotify", Name: "X"}
	r.press("PRESET_4", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	r.gesture(true, 1600)
	if rowTwo, _, _ := r.got(); len(rowTwo) != 0 {
		t.Fatalf("row-2 saves %v, want none", rowTwo)
	}
	if s := r.savedSlots(); len(s) != 1 || s[0] != 4 {
		t.Fatalf("hold-to-save %v, want [4]", s)
	}
	// A tap of ANOTHER key before the hold changes nothing either.
	r = newTapHoldRig(t)
	r.result = webui.HoldSaved{Kind: "spotify", Name: "X"}
	r.tap(2, -1500, spotifyBefore)
	r.press("PRESET_4", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	r.gesture(true, 1600)
	if rowTwo, _, _ := r.got(); len(rowTwo) != 0 {
		t.Fatalf("row-2 saves %v after another key's tap", rowTwo)
	}
}

// A double press (two taps, two frames) saves nothing anywhere: the second
// press gets a tap verdict, and two frames standalone are a double press even
// if a hold verdict should follow.
func TestTapHoldDoublePressUnaffected(t *testing.T) {
	r := newTapHoldRig(t)
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginTrace, -250)
	r.gesture(false, -130)
	r.tap(1, 0, spotifyBefore)
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginTrace, 120)
	r.gesture(false, 230)
	r.tap(1, 360, spotifyBefore)
	r.fireChecks()
	if rowTwo, _, _ := r.got(); len(rowTwo) != 0 {
		t.Fatalf("row-2 saves %v on a double press", rowTwo)
	}
	r.wantNoRowOneWrite(t)

	r = newTapHoldRig(t)
	r.tap(1, 0, spotifyBefore)
	r.tap(1, 300, spotifyBefore)
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginTrace, 450)
	r.gesture(true, 2050)
	if rowTwo, _, _ := r.got(); len(rowTwo) != 0 {
		t.Fatalf("row-2 saves %v after a double press", rowTwo)
	}
	r.wantNoRowOneWrite(t)
	r.wantLog(t, "tap+hold on key 1 ignored: the key was pressed twice before the hold")
}

// 1 + volume down (the factory reset) started with a tap of 1 is never the
// gesture, whether the trace names the volume key or the firmware's
// countdown does.
func TestTapHoldCombinationIgnored(t *testing.T) {
	for name, other := range map[string]func(r *tapHoldRig){
		"trace": func(r *tapHoldRig) {
			r.press("VOLUME_DOWN", boxlog.ProducerConsole, boxlog.OriginTrace, 400)
		},
		"countdown": func(r *tapHoldRig) { r.hs.NoteCombination(r.at(600)) },
	} {
		t.Run(name, func(t *testing.T) {
			r := newTapHoldRig(t)
			r.tap(1, 0, spotifyBefore)
			r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginTrace, 300)
			other(r)
			r.gesture(true, 1900)
			r.fireChecks()
			if rowTwo, _, _ := r.got(); len(rowTwo) != 0 {
				t.Fatalf("row-2 saves %v inside a key combination", rowTwo)
			}
			r.wantNoRowOneWrite(t)
			r.wantLog(t, "tap+hold on key 1 ignored: a key combination")
			if r.hs.tapHoldClaims(1) {
				t.Fatal("a combination must leave the firmware's store alone")
			}
		})
	}
}

// Inside a zone or pair the key trace must show both presses.
func TestTapHoldGrouped(t *testing.T) {
	r := newTapHoldRig(t)
	r.grouped, r.presses = true, 2
	r.tap(5, 0, spotifyBefore)
	r.tap(5, 350, spotifyBefore) // the group's echo of the tap
	r.press("PRESET_5", boxlog.ProducerConsole, boxlog.OriginTrace, 300)
	r.gesture(true, 1900)
	if rowTwo, _, _ := r.got(); len(rowTwo) != 1 || rowTwo[0] != 11 {
		t.Fatalf("row-2 saves %v, want slot 11 with trace evidence", rowTwo)
	}

	for presses, why := range map[int]string{0: "grouped without trace", 1: "grouped, the key trace shows one press"} {
		r := newTapHoldRig(t)
		r.grouped, r.presses = true, presses
		r.tap(5, 0, spotifyBefore)
		r.press("PRESET_5", boxlog.ProducerConsole, boxlog.OriginTrace, 300)
		r.gesture(true, 1900)
		if rowTwo, _, _ := r.got(); len(rowTwo) != 0 {
			t.Fatalf("%d trace presses: row-2 saves %v, want none", presses, rowTwo)
		}
		r.wantNoRowOneWrite(t)
		r.wantLog(t, "tap+hold on key 5 ignored: "+why)
	}
}

// Nothing that can be kept played before the tap (standby, AUX, Bluetooth):
// nothing is written, and the display says NOT SAVED.
func TestTapHoldNothingBeforeTheTap(t *testing.T) {
	r := newTapHoldRig(t)
	r.tap(1, 0, webui.PlayingSnapshot{Source: "STANDBY", Code: "nothing-playing"})
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginTrace, 300)
	r.gesture(true, 1900)
	if rowTwo, plays, _ := r.got(); len(rowTwo) != 0 || len(plays) != 0 {
		t.Fatalf("saves %v plays %v, want none", rowTwo, plays)
	}
	r.wantNoRowOneWrite(t)
	r.notices.only(t, oled.NoticeNotSaved, 1)
	r.wantLog(t, "tap+hold on key 1 -> nothing to save")
}

// What played already sits on another key: refused like the app's save,
// the display names that key, and that key plays so the station comes back.
func TestTapHoldDuplicateOnAnotherSlot(t *testing.T) {
	r := newTapHoldRig(t)
	r.saveErr = &webui.HoldSaveError{Code: "already-on-slot", OtherSlot: 9, OtherName: "Jens Chill"}
	r.tap(1, 0, spotifyBefore)
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginTrace, 300)
	r.gesture(true, 1900)
	_, plays, _ := r.got()
	if len(plays) != 1 || plays[0] != 9 {
		t.Fatalf("played %v, want slot 9 where it sits", plays)
	}
	n := r.notices.only(t, oled.NoticeAlreadyOnKey, 3)
	if n.Line1 != "ON 2X 3" {
		t.Fatalf("notice %+v, want ON 2X 3", n)
	}
	r.wantNoRowOneWrite(t)
	r.wantLog(t, "is already on slot 9, not saved on row 2")
}

// The firmware's own store of the key (it would put the key's own station,
// which the tap started, back on the key) is refused during a tap+hold, and
// row 1 is not written. A plain hold still reaches the keeper.
func TestTapHoldRefusesTheFirmwaresStore(t *testing.T) {
	l := swapShowNotice(t)
	r := newTapHoldRig(t)
	heldGestureSaver.Store(r.hs)
	t.Cleanup(func() { heldGestureSaver.Store(nil) })
	store := holdTestStore(t)
	keep := newHeldPresetKeeper(store, heldLive{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	other := webui.OrionStationLocation(boxurl.RawStream("https://stream.example/other.mp3"), "Other", "")

	r.tap(1, 0, spotifyBefore)
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginTrace, 300)
	r.gesture(true, 1900)
	if err := keep(heldRadio(1, other, "Other")); !errors.Is(err, errTapHoldRowTwo) {
		t.Fatalf("keeper err = %v, want the tap+hold refusal", err)
	}
	if p, _ := store.Get(1); p.Name != "1LIVE" {
		t.Fatalf("key 1 = %+v, want it untouched", p)
	}
	l.none(t)

	// A plain hold of key 3 is the firmware's own store, as before.
	q := newTapHoldRig(t)
	heldGestureSaver.Store(q.hs)
	q.press("PRESET_3", boxlog.ProducerConsole, boxlog.OriginTrace, 0)
	q.now = q.now.Add(2 * time.Second)
	if err := keep(heldRadio(3, other, "Other")); err != nil {
		t.Fatalf("plain hold: %v", err)
	}
	if p, _ := store.Get(3); p.StreamURL != "https://stream.example/other.mp3" {
		t.Fatalf("key 3 = %+v, want the station", p)
	}
}

// Off without the wiring: a tap and a hold are today's hold-to-save.
func TestTapHoldOffWithoutWiring(t *testing.T) {
	r := newHoldRig(t)
	r.hs.NoteTap(1, r.at(0), false, func() webui.PlayingSnapshot { return spotifyBefore })
	r.press("PRESET_1", boxlog.ProducerConsole, boxlog.OriginTrace, 300)
	if r.hs.tapHoldClaims(1) {
		t.Fatal("no wiring: the firmware's store must not be refused")
	}
}

// What played before the tap is the newest now-playing frame from before the
// press, not the firmware's switch that may already have arrived.
func TestNoteTapTakesTheFrameBeforeThePress(t *testing.T) {
	r := newTapHoldRig(t)
	var docs []string
	h := &presetWsHandler{
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		holdSaver:      r.hs,
		ownSelectionFn: func(int) bool { return false },
		snapshotFn: func(doc []byte) webui.PlayingSnapshot {
			docs = append(docs, string(doc))
			return spotifyBefore
		},
	}
	tap := time.Now()
	h.OnNowPlayingFrame(boxws.NowPlayingFrame{At: tap.Add(-5 * time.Second), Raw: []byte("before")})
	h.OnNowPlayingFrame(boxws.NowPlayingFrame{At: tap.Add(-200 * time.Millisecond), Raw: []byte("switch")})
	h.noteTap(1, tap)
	// the group's echo of the same tap takes no second snapshot
	h.noteTap(1, tap.Add(350*time.Millisecond))
	if len(docs) != 1 || docs[0] != "before" {
		t.Fatalf("snapshots of %q, want one of the frame before the press", docs)
	}
	r.hs.mu.Lock()
	mark := r.hs.tap
	r.hs.mu.Unlock()
	if mark.key != 1 || mark.frames != 2 || mark.snap.Kind != "spotify" {
		t.Fatalf("tap mark %+v", mark)
	}
	// STR's own selection is no tap.
	h.ownSelectionFn = func(int) bool { return true }
	h.noteTap(2, tap.Add(2*time.Second))
	r.hs.mu.Lock()
	mark = r.hs.tap
	r.hs.mu.Unlock()
	if mark.key != 0 {
		t.Fatalf("own selection left a tap mark %+v", mark)
	}
}

func TestNowPlayingHistorySeed(t *testing.T) {
	var n nowPlayingHistory
	t0 := time.Now()
	n.seed([]byte("read"), t0)
	if string(n.before(t0.Add(time.Second))) != "read" {
		t.Fatal("seed not kept")
	}
	n.add(boxws.NowPlayingFrame{At: t0.Add(2 * time.Second), Raw: []byte("bus")})
	n.seed([]byte("stale read"), t0.Add(time.Second))
	if string(n.before(t0.Add(3*time.Second))) != "bus" {
		t.Fatal("a read older than the bus's newest frame must not win")
	}
	for i := 0; i < 2*nowPlayingHistoryLen; i++ {
		n.add(boxws.NowPlayingFrame{At: t0.Add(time.Duration(3+i) * time.Second), Raw: []byte("x")})
	}
	if len(n.frames) != nowPlayingHistoryLen {
		t.Fatalf("history holds %d, want %d", len(n.frames), nowPlayingHistoryLen)
	}
}

// Every now-playing frame also reaches the webui's woken-stop mark (#1629),
// so a group member's switch is decided on the speaker without an app poll.
func TestNowPlayingFrameReachesWokenStop(t *testing.T) {
	var got []string
	h := &presetWsHandler{onNowPlaying: func(raw []byte) { got = append(got, string(raw)) }}
	h.OnNowPlayingFrame(boxws.NowPlayingFrame{At: time.Now(), Raw: []byte("doc")})
	if len(got) != 1 || got[0] != "doc" {
		t.Fatalf("woken-stop hook got %q, want the frame once", got)
	}
	// Unwired: the frame is still kept and nothing panics.
	(&presetWsHandler{}).OnNowPlayingFrame(boxws.NowPlayingFrame{At: time.Now(), Raw: []byte("doc")})
}
