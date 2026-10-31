package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
)

// trackRig records what the track screen and the wake hooks were told.
type trackRig struct {
	mu        sync.Mutex
	sources   []string // "UPNP:true", "STANDBY:false", ...
	refreshed chan struct{}
	exits     chan struct{}
	logs      bytes.Buffer
}

func (r *trackRig) lastSource() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sources) == 0 {
		return ""
	}
	return r.sources[len(r.sources)-1]
}

func (r *trackRig) sourceLog() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.sources, ",")
}

func (r *trackRig) logText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logs.String()
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// newTrackRig wires the seams and returns a handler whose speaker answers
// now_playing from answers, one per read (the last one repeats; "" is no
// answer).
func newTrackRig(t *testing.T, answers ...string) (*trackRig, *presetWsHandler) {
	t.Helper()
	resetResyncGlobals()
	r := &trackRig{refreshed: make(chan struct{}, 8), exits: make(chan struct{}, 8)}
	oldSet, oldRefresh, oldNote, oldDelay := setTrackSource, refreshTrackPanel, notePanelSource, wakeConfirmDelay
	t.Cleanup(func() {
		setTrackSource, refreshTrackPanel, notePanelSource, wakeConfirmDelay = oldSet, oldRefresh, oldNote, oldDelay
		resetResyncGlobals()
	})
	setTrackSource = func(name string, ours bool) {
		r.mu.Lock()
		r.sources = append(r.sources, fmt.Sprintf("%s:%v", name, ours))
		r.mu.Unlock()
	}
	refreshTrackPanel = func() { r.refreshed <- struct{}{} }
	notePanelSource = func(string) {}
	wakeConfirmDelay = 150 * time.Millisecond
	reads := 0
	h := &presetWsHandler{
		boxHost:       "192.0.2.1",
		onStandbyExit: func() { r.exits <- struct{}{} },
		boxSummaryFn: func() (string, string, string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			i := reads
			if i >= len(answers) {
				i = len(answers) - 1
			}
			reads++
			return answers[i], "", ""
		},
	}
	h.logger = slog.New(slog.NewTextHandler(&lockedWriter{w: &r.logs, mu: &r.mu}, nil))
	return r, h
}

// powerOff plays the start of the field sequence: STR's radio on UPNP, the
// power key, the bus's UPNP -> STANDBY and its standby delivery.
func powerOff(h *presetWsHandler) {
	ctx := context.Background()
	h.OnSourceChanged(ctx, "UPNP", "STANDBY")
	h.OnEnterStandby(ctx)
}

// phantomFrame is the speaker's STANDBY -> UPNP frame as the dispatcher
// hands it on: the source hook, then the standby-exit hook.
func phantomFrame(h *presetWsHandler) {
	ctx := context.Background()
	h.OnSourceChanged(ctx, "STANDBY", "UPNP")
	h.OnStandbyExit(ctx)
}

func waitNone(t *testing.T, ch chan struct{}, d time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal(what)
	case <-time.After(d):
	}
}

func waitOne(t *testing.T, ch chan struct{}, d time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatal(what)
	}
}

func assertStaysOff(t *testing.T, r *trackRig, h *presetWsHandler) {
	t.Helper()
	waitNone(t, r.refreshed, 4*wakeConfirmDelay, "the phantom wake put the track on the panel")
	waitNone(t, r.exits, 10*time.Millisecond, "the phantom wake ran the standby-exit hook")
	for _, s := range strings.Split(r.sourceLog(), ",") {
		if s == "UPNP:true" {
			t.Fatalf("titles were let onto the panel of a speaker in standby: %s", r.sourceLog())
		}
	}
	if got := r.lastSource(); got != "STANDBY:false" {
		t.Fatalf("track source ends %q, want STANDBY:false (%s)", got, r.sourceLog())
	}
	if presetResyncAsk.Load() {
		t.Fatal("the phantom wake asked for a key re-sync of a sleeping speaker")
	}
	h.powerGate.mu.Lock()
	wake := h.powerGate.entries[powerSignalWake].deliveredBy
	h.powerGate.mu.Unlock()
	if wake != "" {
		t.Fatalf("the phantom was recorded as a wake delivered by %q", wake)
	}
}

// The Portable sequence of 2026-10-27: power key, UPNP -> STANDBY, STR's
// standby bounce stops and clears the transport, and the speaker answers
// with STANDBY -> UPNP without ever leaving standby (now_playing keeps
// saying STANDBY). The track must stay off the panel and the wake hooks
// must not act.
func TestPhantomWakeAfterStandbyBounceKeepsTheTrackOff(t *testing.T) {
	r, h := newTrackRig(t, "STANDBY")
	powerOff(h)
	phantomFrame(h)
	assertStaysOff(t, r, h)
	if !strings.Contains(r.logText(), "never left standby") {
		t.Fatalf("the phantom is not in the log:\n%s", r.logText())
	}
}

// Live on the same Portable the sleeping speaker kept sending such frames
// while STR talked to its renderer, tens of seconds after the standby. Each
// of them is checked the same way.
func TestPhantomWakeLongAfterTheStandbyIsCheckedToo(t *testing.T) {
	r, h := newTrackRig(t, "STANDBY")
	powerOff(h)
	h.powerGate.mu.Lock()
	h.powerGate.entries[powerSignalStandby].deliveredAt = time.Now().Add(-time.Minute)
	h.powerGate.mu.Unlock()
	h.OnSourceChanged(context.Background(), "UPNP", "STANDBY")
	phantomFrame(h)
	assertStaysOff(t, r, h)
}

// A real power-on onto STR's radio still brings the screen back at once:
// the speaker answers the first read with UPNP, long before the confirming
// read.
func TestRealWakeRedrawsAtOnce(t *testing.T) {
	r, h := newTrackRig(t, "UPNP")
	wakeConfirmDelay = 2 * time.Second
	powerOff(h)
	phantomFrame(h)
	waitOne(t, r.refreshed, 500*time.Millisecond, "a real wake did not redraw at once")
	waitOne(t, r.exits, 500*time.Millisecond, "a real wake did not run the standby-exit hook")
	if got := r.lastSource(); got != "UPNP:true" {
		t.Fatalf("track source %q, want UPNP:true", got)
	}
}

// A speaker that does not answer keeps the panel handed back, but the wake
// hooks are taken at the frame's word, as before the check existed.
func TestUnansweredWakeKeepsThePanelBackButActs(t *testing.T) {
	r, h := newTrackRig(t, "")
	powerOff(h)
	phantomFrame(h)
	waitOne(t, r.exits, 10*wakeConfirmDelay, "a wake the speaker did not answer about never ran the hooks")
	waitNone(t, r.refreshed, 2*wakeConfirmDelay, "a wake the speaker did not confirm put the track up")
}

// A speaker that is slow to report its new source still gets the screen, at
// the confirming read.
func TestSlowWakeRedrawsAtTheConfirmingRead(t *testing.T) {
	r, h := newTrackRig(t, "STANDBY", "UPNP")
	powerOff(h)
	phantomFrame(h)
	waitOne(t, r.refreshed, 10*wakeConfirmDelay, "a slow wake never redrew")
	waitOne(t, r.exits, 10*wakeConfirmDelay, "a slow wake never ran the standby-exit hook")
	if strings.Contains(r.logText(), "never left standby") {
		t.Fatal("a real wake was logged as a phantom")
	}
}

// A switch that arrives while the check runs is newer than its verdict.
func TestNewerSwitchOutranksAWakeCheck(t *testing.T) {
	r, h := newTrackRig(t, "UPNP")
	release := make(chan struct{})
	inner := h.boxSummaryFn
	h.boxSummaryFn = func() (string, string, string) {
		<-release
		return inner()
	}
	powerOff(h)
	ctx := context.Background()
	h.OnSourceChanged(ctx, "STANDBY", "UPNP")
	h.OnSourceChanged(ctx, "UPNP", "AUX")
	close(release)
	waitNone(t, r.refreshed, 4*wakeConfirmDelay, "a stale wake check redrew over a newer switch")
	if got := r.lastSource(); got != "AUX:false" {
		t.Fatalf("track source %q, want AUX:false", got)
	}
}

// The speaker's syslog standby takes the track off whatever the bus said,
// and a later syslog wake onto STR's source brings it back even when the bus
// reports no switch out of STANDBY (it still believes the phantom UPNP).
func TestSyslogStandbyAndWakeFollowTheSpeaker(t *testing.T) {
	r, h := newTrackRig(t, "UPNP")
	h.strSourceRecently = func() bool { return false }
	h.OnBoxPowerEvent(boxlog.PowerEvent{Kind: boxlog.PowerStandby, Source: boxlog.PowerSourceHSM})
	if got := r.lastSource(); got != "STANDBY:false" {
		t.Fatalf("syslog standby left the track source at %q", got)
	}
	h.OnBoxPowerEvent(boxlog.PowerEvent{Kind: boxlog.PowerWake, Source: boxlog.PowerSourceHSM})
	waitOne(t, r.refreshed, 500*time.Millisecond, "the syslog wake did not bring the track back")
	if got := r.lastSource(); got != "UPNP:true" {
		t.Fatalf("track source %q, want UPNP:true", got)
	}
}

// After a phantom the bus believes UPNP, so the real wake is no switch out
// of STANDBY on the bus; the speaker's syslog wake brings the screen back.
func TestRealWakeAfterAPhantomComesThroughTheSyslog(t *testing.T) {
	r, h := newTrackRig(t, "STANDBY", "STANDBY", "UPNP")
	powerOff(h)
	phantomFrame(h)
	waitNone(t, r.refreshed, 4*wakeConfirmDelay, "the phantom wake put the track on the panel")
	h.OnBoxPowerEvent(boxlog.PowerEvent{Kind: boxlog.PowerWake, Source: boxlog.PowerSourceHSM})
	waitOne(t, r.refreshed, 500*time.Millisecond, "the real wake after a phantom did not bring the track back")
	if got := r.lastSource(); got != "UPNP:true" {
		t.Fatalf("track source %q, want UPNP:true", got)
	}
}
