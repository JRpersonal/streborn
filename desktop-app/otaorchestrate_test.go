package main

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// The update sequence against a fake speaker, at the PRODUCTION policy on a
// fake clock. Every number the field taught (360 s verify, 50 s stability,
// 600 s engine window, 30 s settle, the 2 s -> 30 s backoff, one retry) is
// exercised as it ships, and a sleep costs nothing.

const (
	testAppSha   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testOtherSha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testAppVer   = "v1.0.20"
	testAppBuild = "2026-10-23-1200"
	testOldVer   = "v1.0.19"
	testOldBuild = "2026-10-20-0900"
)

var errUnreachable = errors.New("dial tcp 192.0.2.1:8888: connect: no route to host")

type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	c.sleeps = append(c.sleeps, d)
}

// otaFakeBox is one scripted speaker. versionAt answers the version probe as a
// function of the fake time, so a reboot is "unreachable for 60 s after the
// push" and a late confirmation is a number, not a sequence of canned replies.
type otaFakeBox struct {
	clk *fakeClock
	mu  sync.Mutex

	versionAt func(t time.Time) (map[string]string, error)
	reads     int

	pushErr        error
	pushes         []time.Time
	pushRunning    []bool // otaRunning as seen inside the push
	onPush         func()
	engineReplies  []engineReply
	engineCalls    []time.Time
	verdicts       []string
	classifyCalls  int
	outcomes       []string
	intents        []time.Time
	clears         []time.Time
	restores       int
	phases         []updatePhase
	taps           progressTaps
	engineTapsSeen int
}

type engineReply struct {
	res string
	err error
}

func newOtaFakeBox() *otaFakeBox {
	fb := &otaFakeBox{clk: newFakeClock()}
	fb.versionAt = func(time.Time) (map[string]string, error) { return oldVersion(), nil }
	return fb
}

func oldVersion() map[string]string {
	return map[string]string{"version": testOldVer, "build": testOldBuild, "agentRunningSha256": testOtherSha, "goLibrespot": "present", "uptimeSec": "9000"}
}

func newVersion(engine string) map[string]string {
	v := map[string]string{"version": testAppVer, "build": testAppBuild, "agentRunningSha256": testAppSha, "uptimeSec": "9000"}
	if engine != "" {
		v["goLibrespot"] = engine
	}
	return v
}

func (fb *otaFakeBox) pushTime() time.Time {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if len(fb.pushes) == 0 {
		return time.Time{}
	}
	return fb.pushes[len(fb.pushes)-1]
}

// rebootsTo scripts the usual update: the box answers the old version until the
// push, is unreachable for `down` after it, then answers the new version.
func (fb *otaFakeBox) rebootsTo(down time.Duration, after func(t time.Time) map[string]string) {
	fb.versionAt = func(t time.Time) (map[string]string, error) {
		pt := fb.pushTime()
		if pt.IsZero() {
			return oldVersion(), nil
		}
		if t.Sub(pt) < down {
			return nil, errUnreachable
		}
		return after(t), nil
	}
}

func (fb *otaFakeBox) seams() *otaSeams {
	return &otaSeams{
		push: func(host string, port int) error {
			fb.mu.Lock()
			fb.pushes = append(fb.pushes, fb.clk.now())
			fb.pushRunning = append(fb.pushRunning, otaRunning.Load())
			onPush := fb.onPush
			err := fb.pushErr
			fb.mu.Unlock()
			if onPush != nil {
				onPush()
			}
			return err
		},
		ensureEngine: func(host string, port int) (string, error) {
			fb.mu.Lock()
			defer fb.mu.Unlock()
			fb.engineCalls = append(fb.engineCalls, fb.clk.now())
			if len(fb.engineReplies) == 0 {
				return "current", nil
			}
			r := fb.engineReplies[0]
			if len(fb.engineReplies) > 1 {
				fb.engineReplies = fb.engineReplies[1:]
			}
			return r.res, r.err
		},
		readVersion: func(host string, port int) (map[string]string, error) {
			fb.mu.Lock()
			fb.reads++
			fn := fb.versionAt
			fb.mu.Unlock()
			return fn(fb.clk.now())
		},
		classify: func(host string, port int) string {
			fb.mu.Lock()
			defer fb.mu.Unlock()
			fb.classifyCalls++
			if len(fb.verdicts) == 0 {
				return "unreachable"
			}
			v := fb.verdicts[0]
			if len(fb.verdicts) > 1 {
				fb.verdicts = fb.verdicts[1:]
			}
			return v
		},
		recordOutcome: func(host, verdict string) {
			fb.mu.Lock()
			defer fb.mu.Unlock()
			fb.outcomes = append(fb.outcomes, verdict)
		},
		restoreGroup: func(host string, port int) {
			fb.mu.Lock()
			defer fb.mu.Unlock()
			fb.restores++
		},
		recordIntent: func(host string, port int, deviceID, name string) {
			fb.mu.Lock()
			defer fb.mu.Unlock()
			fb.intents = append(fb.intents, fb.clk.now())
		},
		clearIntent: func(host string, port int) {
			fb.mu.Lock()
			defer fb.mu.Unlock()
			fb.clears = append(fb.clears, fb.clk.now())
		},
		spawn: func(fn func()) { fn() },
		sleep: fb.clk.sleep,
		now:   fb.clk.now,
		emit: func(p updatePhase) {
			fb.mu.Lock()
			defer fb.mu.Unlock()
			fb.phases = append(fb.phases, p)
		},
		taps:       &fb.taps,
		appVersion: testAppVer,
		appBuild:   testAppBuild,
		appSha:     testAppSha,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func (fb *otaFakeBox) phaseIDs() []string {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	var out []string
	for _, p := range fb.phases {
		if len(out) == 0 || out[len(out)-1] != p.Phase {
			out = append(out, p.Phase)
		}
	}
	return out
}

func (fb *otaFakeBox) countPhase(id string) int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	n := 0
	for _, p := range fb.phases {
		if p.Phase == id {
			n++
		}
	}
	return n
}

func (fb *otaFakeBox) hasOutcome(prefix string) bool {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	for _, o := range fb.outcomes {
		if strings.HasPrefix(o, prefix) {
			return true
		}
	}
	return false
}

func runSingle(t *testing.T, fb *otaFakeBox) (UpdateOutcome, error) {
	t.Helper()
	return orchestrateSpeaker(fb.seams(), otaProductionPolicy, "192.0.2.1", 8888, "device-id-here", "Kitchen", nil)
}

// ---- the predicates, ported from otaconfirm / engineonlyupdate / enginerepush ----

func TestUpdateConfirms(t *testing.T) {
	cases := []struct {
		name                                   string
		appSha, appBuild, preBuild, preVersion string
		v                                      map[string]string
		want                                   bool
	}{
		// v0.9.88 exactly: same version, stamps a minute apart, nothing else to
		// go on. This is the case that cost three users an afternoon.
		{"confirms on the binary the speaker runs, whatever the stamps say",
			testAppSha, "2026-09-26-2023", "2026-09-26-2022", "v0.9.88",
			map[string]string{"version": "v0.9.88", "build": "2026-09-26-2022", "agentRunningSha256": testAppSha}, true},
		// #381: the disk carries this build, the agent boots the old one. The
		// app must keep waiting and then classify, not declare victory.
		{"does NOT confirm when the binary only reached the disk",
			testAppSha, "2026-09-26-2023", "2026-09-25-1715", "v0.9.87",
			map[string]string{"version": "v0.9.87", "build": "2026-09-25-1715", "agentBinarySha256": testAppSha, "agentRunningSha256": testOtherSha}, false},
		{"does not confirm a speaker that came back on the old software",
			testAppSha, "2026-09-26-2023", "2026-09-25-1715", "v0.9.87",
			map[string]string{"version": "v0.9.87", "build": "2026-09-25-1715", "agentRunningSha256": testOtherSha}, false},
		{"still confirms an older agent by its build, which is all it reports",
			testAppSha, "2026-09-26-2023", "2026-09-25-1715", "v0.9.87",
			map[string]string{"version": "v0.9.88", "build": "2026-09-26-2023"}, true},
		{"confirms when the version moved at all, for an agent reporting no build",
			testAppSha, "", "", "v0.9.87",
			map[string]string{"version": "v0.9.88"}, true},
		{"says no to nothing at all rather than panicking",
			testAppSha, "x", "y", "z", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := updateConfirms(c.v, c.appSha, c.appBuild, c.preBuild, c.preVersion); got != c.want {
				t.Fatalf("updateConfirms = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAgentCurrentEngineMissing(t *testing.T) {
	is := func(v map[string]string) bool {
		return agentCurrentEngineMissing(v, testAppVer, testAppBuild, testAppSha)
	}
	if !is(map[string]string{"version": testAppVer, "build": testAppBuild, "agentRunningSha256": testAppSha, "goLibrespot": "missing"}) {
		t.Error("the running agent is this one and the engine is missing: skip the agent")
	}
	if is(map[string]string{"version": testOldVer, "build": testOldBuild, "agentRunningSha256": testOtherSha, "goLibrespot": "missing"}) {
		t.Error("a different agent needs the push")
	}
	// Same version and build stamp, different running binary: a push that
	// landed on disk but did not boot, or a dev build. The agent goes again.
	if is(map[string]string{"version": testAppVer, "build": testAppBuild, "agentRunningSha256": testOtherSha, "goLibrespot": "missing"}) {
		t.Error("the running checksum must win over a matching version string")
	}
	if !is(map[string]string{"version": testAppVer, "build": testAppBuild, "goLibrespot": "missing"}) {
		t.Error("without a checksum, version plus build decide")
	}
	if is(map[string]string{"version": testAppVer, "build": "other", "goLibrespot": "missing"}) {
		t.Error("a different build is not current")
	}
	if is(map[string]string{"version": testAppVer, "build": testAppBuild, "agentRunningSha256": testAppSha, "goLibrespot": "present"}) {
		t.Error("a speaker with its engine takes the normal update")
	}
	if is(nil) {
		t.Error("nil is not a speaker")
	}
	if agentCurrentEngineMissing(map[string]string{"agentRunningSha256": testAppSha, "goLibrespot": "missing"}, "", "", "") {
		t.Error("an app that knows nothing about itself cannot call a speaker current")
	}
}

func TestSpeakerReachedTarget(t *testing.T) {
	check := func(live map[string]string, pre string, wantEngine bool, wantDone bool, wantMissing string) {
		t.Helper()
		done, missing := speakerReachedTarget(live, pre, wantEngine, testAppSha)
		if done != wantDone || missing != wantMissing {
			t.Errorf("speakerReachedTarget(%v, %q, %v) = (%v, %q), want (%v, %q)", live, pre, wantEngine, done, missing, wantDone, wantMissing)
		}
	}
	// A push of the SAME version is confirmed by the binary that is running:
	// the case that used to be unconfirmable, and therefore the case where the
	// engine was dropped and never restored.
	check(map[string]string{"version": "v0.9.88", "agentRunningSha256": testAppSha, "goLibrespot": "present"}, "v0.9.88", false, true, "")
	check(map[string]string{"version": "v0.9.88", "agentRunningSha256": testAppSha, "goLibrespot": "missing"}, "v0.9.88", true, false, "engine")
	check(map[string]string{"version": "v0.9.88", "agentRunningSha256": testOtherSha}, "v0.9.88", false, false, "agent")
	check(map[string]string{"version": "v0.9.88"}, "v0.9.87", false, true, "")
	check(map[string]string{"version": "v0.9.87"}, "v0.9.87", false, false, "agent")
	check(nil, "v0.9.87", false, false, "unreachable")
	if done, _ := speakerReachedTarget(map[string]string{"version": "v0.9.88"}, "v0.9.87", false, ""); !done {
		t.Error("a dev build with no hash still judges by the version string")
	}
}

func TestEngineBackoff(t *testing.T) {
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := otaProductionPolicy.engineBackoff(i + 1); got != w {
			t.Errorf("attempt %d: backoff %s, want %s", i+1, got, w)
		}
	}
}

func TestProgressTaps(t *testing.T) {
	var taps progressTaps
	var got []int
	remove := taps.add("h", func(pct int) { got = append(got, pct) })
	taps.notify("h", 50)
	taps.notify("other", 100)
	taps.notify("", 100)
	remove()
	taps.notify("h", 100)
	if len(got) != 1 || got[0] != 50 {
		t.Fatalf("tap saw %v, want [50]", got)
	}
}

// ---- the sequence ----

func TestSpeakerUpdateHappyPath(t *testing.T) {
	fb := newOtaFakeBox()
	fb.rebootsTo(60*time.Second, func(time.Time) map[string]string { return newVersion("present") })
	start := fb.clk.now()
	out, err := runSingle(t, fb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Outcome != outcomeDone || !out.Confirmed || out.EngineDelivered || out.Attempts != 1 {
		t.Fatalf("outcome %+v, want done, confirmed, not delivered, 1 attempt", out)
	}
	if out.Version["version"] != testAppVer {
		t.Errorf("the outcome carries the confirmed reading, got %v", out.Version)
	}
	// The intent is written down before a single byte moves.
	if len(fb.intents) != 1 || len(fb.pushes) != 1 || fb.intents[0].After(fb.pushes[0]) {
		t.Errorf("intent at %v, push at %v: the intent must come first", fb.intents, fb.pushes)
	}
	if len(fb.pushRunning) != 1 || !fb.pushRunning[0] {
		t.Error("the app must report itself busy while the push runs")
	}
	if otaRunning.Load() {
		t.Error("busy must be cleared when the run ends")
	}
	// Confirmed only after the 50 s stability window on the new version.
	confirmedAt := fb.clears[0]
	if minimum := start.Add(60*time.Second + 50*time.Second); confirmedAt.Before(minimum) {
		t.Errorf("confirmed at +%s, before the 60 s reboot plus the 50 s stability window", confirmedAt.Sub(start))
	}
	if !fb.hasOutcome("confirmed: box is on build " + testAppBuild) {
		t.Errorf("the journal must carry the confirmation, got %v", fb.outcomes)
	}
	if fb.restores != 1 {
		t.Errorf("the group restore runs once on every confirmed update, ran %d times", fb.restores)
	}
	if len(fb.clears) != 1 {
		t.Errorf("the intent is cleared exactly once on done, cleared %d times", len(fb.clears))
	}
	if len(fb.engineCalls) != 0 {
		t.Errorf("the engine was present on the new build, nothing to deliver, but EnsureSpotifyEngine was called %d times", len(fb.engineCalls))
	}
	want := []string{phaseUploading, phaseRebooting, phaseVerifying, phaseSettling, phaseConfirmed, phaseSpotify, outcomeDone}
	if got := fb.phaseIDs(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("phases %v, want %v", got, want)
	}
	if fb.classifyCalls != 0 {
		t.Error("an in-window confirmation never classifies")
	}
}

func TestSpeakerUpdatePhaseCadence(t *testing.T) {
	fb := newOtaFakeBox()
	fb.rebootsTo(60*time.Second, func(time.Time) map[string]string { return newVersion("present") })
	if _, err := runSingle(t, fb); err != nil {
		t.Fatal(err)
	}
	// The countdown is re-emitted at least every second while the sequence
	// sleeps: 60 s of restarting is at least 60 verifying phases, each with a
	// smaller remaining time than the one before.
	if n := fb.countPhase(phaseVerifying); n < 60 {
		t.Errorf("only %d verifying phases over a 60 s wait; the frontend needs one per second", n)
	}
	var last int64 = -1
	for _, p := range fb.phases {
		if p.Phase != phaseVerifying {
			continue
		}
		if last >= 0 && p.RemainingMs > last {
			t.Fatalf("remaining time went up from %d to %d", last, p.RemainingMs)
		}
		last = p.RemainingMs
	}
	if n := fb.countPhase(phaseSettling); n < 50 {
		t.Errorf("only %d settling phases over the 50 s stability window", n)
	}
}

func TestSpeakerUpdateLostReplyIsNotAFailure(t *testing.T) {
	fb := newOtaFakeBox()
	fb.pushErr = errors.New(`Post "http://192.0.2.1:8888/api/agent/update": context deadline exceeded (Client.Timeout or context cancellation while reading body)`)
	fb.rebootsTo(60*time.Second, func(time.Time) map[string]string { return newVersion("present") })
	out, err := runSingle(t, fb)
	if err != nil || out.Outcome != outcomeDone {
		t.Fatalf("a lost reply must fall through to the version poll, got %v / %+v", err, out)
	}
}

func TestSpeakerUpdateCleanRejectFails(t *testing.T) {
	fb := newOtaFakeBox()
	fb.pushErr = errors.New("status 500: rename /mnt/nv/streborn/bin/streborn-armv7l.new: no such file or directory")
	out, err := runSingle(t, fb)
	if err == nil || out.Outcome != outcomeFailed || !strings.Contains(out.Err, "status 500") {
		t.Fatalf("a clean reject is a failure, got %v / %+v", err, out)
	}
	if len(fb.clears) != 0 {
		t.Error("a failed push keeps the intent so the next run can finish the job")
	}
	// One read before the push, one to ask whether the engine was dropped for
	// it (restoreEngineAfterIncompleteUpdate); no verify poll.
	if fb.reads != 2 {
		t.Errorf("after a clean reject nothing is polled, read %d times", fb.reads)
	}
	if len(fb.engineCalls) != 0 {
		t.Error("a speaker that still has its engine gets no engine delivery")
	}
	if got := fb.phaseIDs(); got[len(got)-1] != outcomeFailed {
		t.Errorf("the terminal phase must be failed, phases %v", got)
	}
}

func TestSpeakerUpdateSecondDropConfirmsOnNextSighting(t *testing.T) {
	// The bootstrap reboot: the box comes back on the new version at +60 s,
	// drops again at +80 s, is back at +90 s. The next sighting confirms at
	// once instead of starting another 50 s window.
	fb := newOtaFakeBox()
	fb.versionAt = func(t time.Time) (map[string]string, error) {
		pt := fb.pushTime()
		if pt.IsZero() {
			return oldVersion(), nil
		}
		since := t.Sub(pt)
		switch {
		case since < 60*time.Second, since >= 80*time.Second && since < 90*time.Second:
			return nil, errUnreachable
		default:
			return newVersion("present"), nil
		}
	}
	out, err := runSingle(t, fb)
	if err != nil || out.Outcome != outcomeDone {
		t.Fatalf("got %v / %+v", err, out)
	}
	at := fb.clears[0].Sub(fb.pushes[0])
	if at < 90*time.Second || at > 95*time.Second {
		t.Errorf("confirmed at +%s; after a second drop the next sighting (+90 s) confirms, not +90+50", at)
	}
}

func TestSpeakerUpdateRetriesOnceOnNotLanded(t *testing.T) {
	fb := newOtaFakeBox()
	// The box reboots onto its OLD version both times: the write stalled.
	fb.rebootsTo(60*time.Second, func(time.Time) map[string]string { return oldVersion() })
	fb.verdicts = []string{"not-landed"}
	out, err := runSingle(t, fb)
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.pushes) != 2 {
		t.Fatalf("not-landed earns exactly one retry, pushed %d times", len(fb.pushes))
	}
	if out.Outcome != outcomeTimeout || out.Verdict != "not-landed" || out.Attempts != 2 {
		t.Fatalf("after the retry the verdict stands: %+v", out)
	}
	if !fb.hasOutcome("retrying once") {
		t.Errorf("the retry is journaled, got %v", fb.outcomes)
	}
	found := false
	for _, p := range fb.phases {
		if p.Phase == phaseRetrying && p.Attempt == 2 {
			found = true
		}
	}
	if !found {
		t.Error("the retrying phase names attempt 2")
	}
	if len(fb.clears) != 0 {
		t.Error("a timeout keeps the intent")
	}
	// The second attempt waited its own full verify window.
	if gap := fb.pushes[1].Sub(fb.pushes[0]); gap < 360*time.Second {
		t.Errorf("the retry came %s after the first push, before the 360 s verify window ran out", gap)
	}
}

func TestSpeakerUpdateNoRetryOnOtherVerdicts(t *testing.T) {
	for _, c := range []struct{ verdict, outcome string }{
		{"landed-not-running", outcomeTimeout},
		{"swap-failed", outcomeTimeout},
		{"unreachable", outcomeTimeout},
		{"agent-gone", outcomeAgentGone},
		{"box-in-setup", outcomeBoxInSetup},
	} {
		t.Run(c.verdict, func(t *testing.T) {
			fb := newOtaFakeBox()
			fb.rebootsTo(10*time.Minute, func(time.Time) map[string]string { return oldVersion() })
			fb.verdicts = []string{c.verdict}
			out, err := runSingle(t, fb)
			if err != nil {
				t.Fatal(err)
			}
			if len(fb.pushes) != 1 {
				t.Errorf("%s must not be retried, pushed %d times", c.verdict, len(fb.pushes))
			}
			if out.Outcome != c.outcome || out.Verdict != c.verdict {
				t.Errorf("outcome %+v, want %s with verdict %s", out, c.outcome, c.verdict)
			}
			if len(fb.clears) != 0 {
				t.Error("an unconfirmed update keeps the intent")
			}
			if got := fb.phaseIDs(); got[len(got)-1] != c.outcome {
				t.Errorf("terminal phase %v, want %s", got, c.outcome)
			}
		})
	}
}

func TestSpeakerUpdateLateConfirmation(t *testing.T) {
	// The box crosses the verify window late: the poll never sees it, the
	// classifier does. Counted as updated and the engine step follows.
	fb := newOtaFakeBox()
	// Back at +359.5 s: the last poll of the window sees it, but the 50 s
	// stability window cannot pass inside the 360 s, so the poll gives up.
	fb.rebootsTo(359500*time.Millisecond, func(time.Time) map[string]string { return newVersion("present") })
	fb.verdicts = []string{"confirmed"}
	out, err := runSingle(t, fb)
	if err != nil || out.Outcome != outcomeDone || !out.Confirmed {
		t.Fatalf("a late confirmation is a confirmation: %v / %+v", err, out)
	}
	if fb.restores != 1 {
		t.Error("the group restore runs for a late confirmation too")
	}
	if len(fb.pushes) != 1 || fb.classifyCalls != 1 {
		t.Errorf("one push, one classification; got %d / %d", len(fb.pushes), fb.classifyCalls)
	}
}

func TestSpeakerUpdateEngineStep(t *testing.T) {
	// Every path the engine step can take after a confirmed agent whose
	// engine is reported missing.
	type want struct {
		outcome        string
		delivered      bool
		tooFull, quiet bool
		unmet          string
		clears         int
		engineCalls    int
	}
	cases := []struct {
		name    string
		engine  string // what the new build reports for goLibrespot
		replies []engineReply
		after   func(fb *otaFakeBox) // what the box reports once the engine was pushed
		want    want
	}{
		{"no goLibrespot field at all: nothing to deliver", "", nil, nil,
			want{outcomeDone, false, false, false, "", 1, 0}},
		{"no embedded engine in this build", "missing", []engineReply{{res: "no embedded engine in this build"}}, nil,
			want{outcomeDone, false, false, false, "", 1, 1}},
		{"skipped because STR is being removed", "missing", []engineReply{{res: EngineSkippedUninstalling}}, nil,
			want{outcomeDone, false, false, true, "", 1, 1}},
		{"delivered and present", "missing", []engineReply{{res: "ok"}}, presentAfterEngine,
			want{outcomeDone, true, false, false, "", 1, 1}},
		{"already current and present", "missing", []engineReply{{res: "current"}}, presentAfterEngine,
			want{outcomeDone, false, false, false, "", 1, 1}},
		{"too full: stop, clear the intent", "missing", []engineReply{{err: errors.New("status 507: insufficient NAND space: need 16 MB, free 11 MB")}}, nil,
			want{outcomePartial, false, true, false, unmetSpotifyEngine, 1, 1}},
		{"stream drop then delivered", "missing", []engineReply{{err: errors.New("write tcp: wsarecv: connection reset by peer")}, {res: "ok"}}, presentAfterSecondEngine,
			want{outcomeDone, true, false, false, "", 1, 2}},
		{"window over: partial, intent kept", "missing", []engineReply{{err: errors.New("status 500: agent busy")}}, nil,
			want{outcomePartial, false, false, false, unmetSpotifyEngine, 0, -1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fb := newOtaFakeBox()
			fb.rebootsTo(60*time.Second, func(time.Time) map[string]string { return newVersion(c.engine) })
			fb.engineReplies = c.replies
			if c.after != nil {
				c.after(fb)
			}
			out, err := runSingle(t, fb)
			if err != nil {
				t.Fatal(err)
			}
			w := c.want
			if out.Outcome != w.outcome || out.EngineDelivered != w.delivered || out.EngineTooFull != w.tooFull || out.Quiet != w.quiet || out.Unmet != w.unmet {
				t.Errorf("outcome %+v, want %+v", out, w)
			}
			if len(fb.clears) != w.clears {
				t.Errorf("intent cleared %d times, want %d", len(fb.clears), w.clears)
			}
			if w.engineCalls >= 0 && len(fb.engineCalls) != w.engineCalls {
				t.Errorf("EnsureSpotifyEngine called %d times, want %d", len(fb.engineCalls), w.engineCalls)
			}
			if got := fb.phaseIDs(); got[len(got)-1] != w.outcome {
				t.Errorf("terminal phase %v, want %s", got, w.outcome)
			}
		})
	}
}

// presentAfterEngine makes the box report the engine present once the first
// engine call happened.
func presentAfterEngine(fb *otaFakeBox) { presentAfterEngineCalls(fb, 1) }

func presentAfterSecondEngine(fb *otaFakeBox) { presentAfterEngineCalls(fb, 2) }

func presentAfterEngineCalls(fb *otaFakeBox, n int) {
	fb.rebootsTo(60*time.Second, func(time.Time) map[string]string {
		fb.mu.Lock()
		calls := len(fb.engineCalls)
		fb.mu.Unlock()
		if calls >= n {
			return newVersion("present")
		}
		return newVersion("missing")
	})
}

func TestSpeakerUpdateEngineWindowAndBackoff(t *testing.T) {
	fb := newOtaFakeBox()
	fb.rebootsTo(60*time.Second, func(time.Time) map[string]string { return newVersion("missing") })
	fb.engineReplies = []engineReply{{err: errors.New("status 500: agent busy")}}
	out, err := runSingle(t, fb)
	if err != nil || out.Outcome != outcomePartial || out.Unmet != unmetSpotifyEngine {
		t.Fatalf("got %v / %+v", err, out)
	}
	if out.Version["version"] != testAppVer {
		t.Error("a partial outcome carries the confirmed reading")
	}
	// The engine step ends 600 s after it began, never earlier.
	confirmedAt := fb.pushes[0].Add(60*time.Second + 50*time.Second)
	if end := fb.clk.now(); end.Sub(confirmedAt) < 600*time.Second {
		t.Errorf("the engine window closed after %s, want the full 600 s", end.Sub(confirmedAt))
	}
	// Between passes: the settle wait, then the backoff 2, 4, 8, 16, 30, 30.
	// A backoff sleep is a single uninterrupted sleep (no countdown runs during
	// it), so it shows up as its own entry in the sleep log.
	var backoffs []time.Duration
	for _, d := range fb.clk.sleeps {
		if d >= 2*time.Second {
			backoffs = append(backoffs, d)
		}
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	if len(backoffs) < len(want) {
		t.Fatalf("saw backoffs %v, want at least %v", backoffs, want)
	}
	for i, w := range want {
		if backoffs[i] != w {
			t.Errorf("backoff %d = %s, want %s (all: %v)", i+1, backoffs[i], w, backoffs)
		}
	}
}

func TestSpeakerUpdateStreamDropRetriesWithoutBackoff(t *testing.T) {
	fb := newOtaFakeBox()
	fb.engineReplies = []engineReply{{err: errors.New("write: broken pipe")}, {res: "ok"}}
	presentAfterSecondEngine(fb)
	if _, err := runSingle(t, fb); err != nil {
		t.Fatal(err)
	}
	for _, d := range fb.clk.sleeps {
		if d >= 2*time.Second {
			t.Fatalf("a stream drop loops straight back to the settle gate, but a %s backoff was slept", d)
		}
	}
	if len(fb.engineCalls) != 2 {
		t.Fatalf("engine called %d times, want 2", len(fb.engineCalls))
	}
	// The second push waits for a settled agent: 30 s of answers (the fake
	// reports a high uptime, so only the stability gate applies).
	if gap := fb.engineCalls[1].Sub(fb.engineCalls[0]); gap < 30*time.Second {
		t.Errorf("second engine push %s after the first; the settle gate is 30 s", gap)
	}
}

func TestSpeakerUpdateSkipsPushWhenOnlyEngineMissing(t *testing.T) {
	fb := newOtaFakeBox()
	fb.versionAt = func(time.Time) (map[string]string, error) {
		fb.mu.Lock()
		calls := len(fb.engineCalls)
		fb.mu.Unlock()
		if calls > 0 {
			return newVersion("present"), nil
		}
		return newVersion("missing"), nil
	}
	fb.engineReplies = []engineReply{{res: "ok"}}
	out, err := runSingle(t, fb)
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.pushes) != 0 {
		t.Fatal("a speaker that already runs this agent must not be pushed again")
	}
	if !fb.hasOutcome("agent upload skipped") {
		t.Errorf("the skip is journaled, got %v", fb.outcomes)
	}
	if out.Outcome != outcomeDone || !out.EngineDelivered || out.Confirmed {
		t.Errorf("outcome %+v, want done, delivered, not confirmed by this run", out)
	}
	if fb.restores != 0 {
		t.Error("no reboot, no group to restore")
	}
	if got := fb.phaseIDs(); got[0] != phaseConfirmed {
		t.Errorf("the engine-only path starts at confirmed, phases %v", got)
	}
}

func TestWaitForStableAgentGates(t *testing.T) {
	type probe struct {
		uptime string
		err    error
	}
	run := func(script func(n int) probe) (bool, time.Duration) {
		fb := newOtaFakeBox()
		n := 0
		fb.versionAt = func(time.Time) (map[string]string, error) {
			n++
			p := script(n)
			if p.err != nil {
				return nil, p.err
			}
			return map[string]string{"version": testAppVer, "uptimeSec": p.uptime}, nil
		}
		u := &speakerUpdater{host: "h", port: 1, seams: fb.seams(), policy: otaProductionPolicy}
		start := fb.clk.now()
		ok := u.waitForStableAgent(start.Add(600 * time.Second))
		return ok, fb.clk.now().Sub(start)
	}
	// Plain reachability for an agent without uptime: 30 s of answers after
	// the first one, which arrives at the first 3 s probe.
	if ok, took := run(func(int) probe { return probe{} }); !ok || took != 33*time.Second {
		t.Errorf("no uptime: ok=%v after %s, want true after 33 s", ok, took)
	}
	// Uptime below 150 s keeps the clock from starting.
	if ok, took := run(func(n int) probe {
		if n <= 10 {
			return probe{uptime: "100"}
		}
		return probe{uptime: "200"}
	}); !ok || took != 63*time.Second {
		t.Errorf("settling window: ok=%v after %s, want true after 63 s (10 probes gated, then 30 s from the 11th)", ok, took)
	}
	// An uptime DROP between two probes is a reboot: it resets the clock.
	if ok, took := run(func(n int) probe {
		switch {
		case n <= 5:
			return probe{uptime: "500"}
		case n == 6:
			return probe{uptime: "160"}
		default:
			return probe{uptime: "200"}
		}
	}); !ok || took != 48*time.Second {
		t.Errorf("uptime drop: ok=%v after %s, want true after 48 s (reset at probe 6, 30 s from there)", ok, took)
	}
	// A failed probe resets too; the window ending reports false.
	if ok, _ := run(func(int) probe { return probe{err: errUnreachable} }); ok {
		t.Error("a speaker that never answers is never stable")
	}
}

func TestOrchestrationRefusesASecondRun(t *testing.T) {
	release, err := otaOrch.claim("an update for Kitchen")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fb := newOtaFakeBox()
	out, err := runSingle(t, fb)
	if err == nil || !strings.Contains(err.Error(), errBoxBusyToken) {
		t.Fatalf("a second run must be refused with the busy token, got %v", err)
	}
	if out.Outcome != outcomeFailed || len(fb.pushes) != 0 || len(fb.intents) != 0 {
		t.Errorf("a refused run touches nothing: %+v, pushes %d, intents %d", out, len(fb.pushes), len(fb.intents))
	}
	if _, err := orchestrateFleet(fb.seams(), otaProductionPolicy, []FleetTarget{{Host: "h", Port: 1}}, nil); err == nil || !strings.Contains(err.Error(), errBoxBusyToken) {
		t.Fatalf("the fleet run is refused the same way, got %v", err)
	}
	// The bound methods refuse before anything reaches the network.
	a := NewApp()
	if _, err := a.RunSpeakerUpdate("192.0.2.1", 8888, "device-id-here", "Kitchen"); err == nil || !strings.Contains(err.Error(), errBoxBusyToken) {
		t.Fatalf("RunSpeakerUpdate must refuse while a run is going, got %v", err)
	}
	if _, err := a.RunFleetUpdate([]FleetTarget{{Host: "192.0.2.1", Port: 8888}}); err == nil || !strings.Contains(err.Error(), errBoxBusyToken) {
		t.Fatalf("RunFleetUpdate must refuse while a run is going, got %v", err)
	}
}

func TestOrchestrationReleasesTheLatch(t *testing.T) {
	fb := newOtaFakeBox()
	fb.pushErr = errors.New("status 500: refused")
	if _, err := runSingle(t, fb); err == nil {
		t.Fatal("expected the clean reject to fail")
	}
	release, err := otaOrch.claim("next")
	if err != nil {
		t.Fatalf("the latch must be free after a failed run: %v", err)
	}
	release()
}

// ---- the fleet ----

// fleetFake is a house of scripted speakers on the real clock with a policy
// in milliseconds. The gate is the subject here, and a gate is about which
// goroutine waits for which, so real time is the honest clock for it.
type fleetFake struct {
	mu        sync.Mutex
	taps      progressTaps
	boxes     map[string]*fleetBox
	pushLog   []pushSpan
	intents   []string
	clears    []string
	outcomes  map[string][]string
	phases    []updatePhase
	restores  int
	pushDelay time.Duration // how long a push takes to return after its bytes are on the wire
}

type fleetBox struct {
	version   map[string]string
	pushed    bool
	pushErr   error
	engine    []engineReply
	engineCnt int
	verdict   string
}

type pushSpan struct {
	host       string
	start, end time.Time
	wireAt     time.Time // when pct 100 was reported
}

var fleetPolicy = otaPolicy{
	verifyWindow:    400 * time.Millisecond,
	verifyPoll:      5 * time.Millisecond,
	stability:       20 * time.Millisecond,
	engineWindow:    300 * time.Millisecond,
	settleProbe:     5 * time.Millisecond,
	settleStable:    10 * time.Millisecond,
	settleMinUptime: 0,
	backoffBase:     2 * time.Millisecond,
	backoffCap:      10 * time.Millisecond,
	maxAttempts:     2,
	phaseTick:       2 * time.Millisecond,
}

func newFleetFake() *fleetFake {
	return &fleetFake{boxes: map[string]*fleetBox{}, outcomes: map[string][]string{}, pushDelay: 40 * time.Millisecond}
}

func (ff *fleetFake) add(host string, engine string) *fleetBox {
	b := &fleetBox{version: oldVersion()}
	b.version["goLibrespot"] = engine
	ff.boxes[host] = b
	return b
}

func (ff *fleetFake) seams() *otaSeams {
	return &otaSeams{
		push: func(host string, port int) error {
			ff.mu.Lock()
			b := ff.boxes[host]
			span := pushSpan{host: host, start: time.Now()}
			idx := len(ff.pushLog)
			ff.pushLog = append(ff.pushLog, span)
			ff.mu.Unlock()
			// The bytes take a moment to stream, then the reply is lost to the
			// reboot for a while longer.
			time.Sleep(10 * time.Millisecond)
			ff.mu.Lock()
			ff.pushLog[idx].wireAt = time.Now()
			ff.mu.Unlock()
			ff.taps.notify(host, 100)
			time.Sleep(ff.pushDelay)
			ff.mu.Lock()
			ff.pushLog[idx].end = time.Now()
			err := b.pushErr
			if err == nil {
				b.pushed = true
				b.version = newVersion(b.version["goLibrespot"])
			}
			ff.mu.Unlock()
			return err
		},
		ensureEngine: func(host string, port int) (string, error) {
			ff.mu.Lock()
			defer ff.mu.Unlock()
			b := ff.boxes[host]
			b.engineCnt++
			if len(b.engine) == 0 {
				b.version["goLibrespot"] = "present"
				return "ok", nil
			}
			r := b.engine[0]
			if len(b.engine) > 1 {
				b.engine = b.engine[1:]
			}
			if r.err == nil && r.res != "current" && !noEmbeddedEngine.MatchString(r.res) {
				b.version["goLibrespot"] = "present"
			}
			return r.res, r.err
		},
		readVersion: func(host string, port int) (map[string]string, error) {
			ff.mu.Lock()
			defer ff.mu.Unlock()
			b := ff.boxes[host]
			out := map[string]string{}
			for k, v := range b.version {
				out[k] = v
			}
			return out, nil
		},
		classify: func(host string, port int) string {
			ff.mu.Lock()
			defer ff.mu.Unlock()
			if v := ff.boxes[host].verdict; v != "" {
				return v
			}
			return "unreachable"
		},
		recordOutcome: func(host, verdict string) {
			ff.mu.Lock()
			defer ff.mu.Unlock()
			ff.outcomes[host] = append(ff.outcomes[host], verdict)
		},
		restoreGroup: func(host string, port int) {
			ff.mu.Lock()
			defer ff.mu.Unlock()
			ff.restores++
		},
		recordIntent: func(host string, port int, deviceID, name string) {
			ff.mu.Lock()
			defer ff.mu.Unlock()
			ff.intents = append(ff.intents, host)
		},
		clearIntent: func(host string, port int) {
			ff.mu.Lock()
			defer ff.mu.Unlock()
			ff.clears = append(ff.clears, host)
		},
		spawn: func(fn func()) { fn() },
		sleep: time.Sleep,
		now:   time.Now,
		emit: func(p updatePhase) {
			ff.mu.Lock()
			defer ff.mu.Unlock()
			ff.phases = append(ff.phases, p)
		},
		taps:       &ff.taps,
		appVersion: testAppVer,
		appBuild:   testAppBuild,
		appSha:     testAppSha,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func (ff *fleetFake) count(hosts []string, host string) int {
	n := 0
	for _, h := range hosts {
		if h == host {
			n++
		}
	}
	return n
}

func TestFleetGateSerialisesPushesOnly(t *testing.T) {
	ff := newFleetFake()
	ff.add("a", "present")
	ff.add("b", "present")
	ff.add("c", "present")
	targets := []FleetTarget{{Host: "a", Port: 1, Name: "A"}, {Host: "b", Port: 1, Name: "B"}, {Host: "c", Port: 1, Name: "C"}}
	out, err := orchestrateFleet(ff.seams(), fleetPolicy, targets, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Rows {
		if r.Outcome != outcomeDone {
			t.Errorf("%s: %+v", r.Host, r)
		}
	}
	if len(ff.pushLog) != 3 {
		t.Fatalf("pushed %d times, want 3", len(ff.pushLog))
	}
	// Sort the spans by start.
	spans := append([]pushSpan(nil), ff.pushLog...)
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j].start.Before(spans[j-1].start); j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
	for i := 1; i < len(spans); i++ {
		prev, cur := spans[i-1], spans[i]
		// Serialised: the next push starts only after the previous one's
		// bytes are on the wire.
		if cur.start.Before(prev.wireAt) {
			t.Errorf("push %s started %s before push %s reached the wire", cur.host, prev.wireAt.Sub(cur.start), prev.host)
		}
		// But NOT after the previous call settled: the gate is released on
		// pct 100, while the reply is still being waited for.
		if !cur.start.Before(prev.end) {
			t.Errorf("push %s started only after push %s had settled; the gate must release on the wire signal", cur.host, prev.host)
		}
	}
	// Every speaker's target was written down, and cleared once done.
	for _, h := range []string{"a", "b", "c"} {
		if ff.count(ff.intents, h) != 1 || ff.count(ff.clears, h) != 1 {
			t.Errorf("%s: intents %d, clears %d, want 1 and 1", h, ff.count(ff.intents, h), ff.count(ff.clears, h))
		}
	}
	if ff.restores != 3 {
		t.Errorf("group restore ran %d times, want once per confirmed speaker", ff.restores)
	}
}

func TestFleetGateFallsBackToSettleWithoutProgress(t *testing.T) {
	ff := newFleetFake()
	ff.add("a", "present")
	ff.add("b", "present")
	s := ff.seams()
	// A chassis that emits no progress: the push returns without ever
	// reporting pct 100, and the gate releases when the call settles.
	s.push = func(host string, port int) error {
		ff.mu.Lock()
		idx := len(ff.pushLog)
		ff.pushLog = append(ff.pushLog, pushSpan{host: host, start: time.Now()})
		ff.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		ff.mu.Lock()
		ff.pushLog[idx].end = time.Now()
		b := ff.boxes[host]
		b.version = newVersion("present")
		ff.mu.Unlock()
		return nil
	}
	if _, err := orchestrateFleet(s, fleetPolicy, []FleetTarget{{Host: "a", Port: 1}, {Host: "b", Port: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	a, b := ff.pushLog[0], ff.pushLog[1]
	if a.start.After(b.start) {
		a, b = b, a
	}
	if b.start.Before(a.end) {
		t.Errorf("without a wire signal the second push must wait for the first call to settle (%s early)", a.end.Sub(b.start))
	}
}

func TestFleetEngineOnlyRows(t *testing.T) {
	cases := []struct {
		name          string
		engine        []engineReply
		staysMissing  bool
		wantOutcome   string
		wantUnmet     string
		wantDelivered bool
		wantTooFull   bool
		wantErr       string
		wantCleared   bool
	}{
		{"delivered and confirmed", []engineReply{{res: "ok"}}, false, outcomeDone, "", true, false, "", true},
		{"no engine in this build", []engineReply{{res: "no embedded engine in this build"}}, true, outcomePartial, unmetNoEmbeddedEngine, false, false, "", false},
		{"delivered but gone again", []engineReply{{res: "ok"}}, true, outcomePartial, unmetSpotifyEngine, true, false, "", false},
		{"too full", []engineReply{{err: errors.New("status 507: insufficient NAND space")}}, true, outcomeFailed, "", false, true, "507", false},
		{"failed", []engineReply{{err: errors.New("dial tcp: connection refused")}}, true, outcomeFailed, "", false, false, "connection refused", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ff := newFleetFake()
			b := ff.add("e", "missing")
			b.version = newVersion("missing")
			b.engine = c.engine
			s := ff.seams()
			if c.staysMissing {
				inner := s.ensureEngine
				s.ensureEngine = func(host string, port int) (string, error) {
					res, err := inner(host, port)
					ff.mu.Lock()
					b.version["goLibrespot"] = "missing"
					ff.mu.Unlock()
					return res, err
				}
			}
			out, err := orchestrateFleet(s, fleetPolicy, []FleetTarget{{Host: "e", Port: 1, Name: "E", EngineOnly: true}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			r := out.Rows[0]
			if !r.EngineOnly || r.Outcome != c.wantOutcome || r.Unmet != c.wantUnmet || r.EngineDelivered != c.wantDelivered || r.EngineTooFull != c.wantTooFull || !strings.Contains(r.Err, c.wantErr) {
				t.Errorf("row %+v", r)
			}
			if ff.count(ff.intents, "e") != 1 {
				t.Error("an engine-only row records its target first, like every other speaker")
			}
			if cleared := ff.count(ff.clears, "e") == 1; cleared != c.wantCleared {
				t.Errorf("intent cleared=%v, want %v", cleared, c.wantCleared)
			}
			if b.engineCnt != 1 {
				t.Errorf("EnsureSpotifyEngine called %d times, want exactly 1", b.engineCnt)
			}
			var ids []string
			for _, p := range ff.phases {
				ids = append(ids, p.Phase)
			}
			if want := []string{phaseEngineQueued, phaseEngineUploading, c.wantOutcome}; strings.Join(ids, ",") != strings.Join(want, ",") {
				t.Errorf("phases %v, want %v", ids, want)
			}
		})
	}
}

func TestFleetHonestyRecheck(t *testing.T) {
	// The engine was there when the update finished and gone by the time the
	// row is judged: the row says partial, the intent stays.
	ff := newFleetFake()
	b := ff.add("h", "present")
	s := ff.seams()
	inner := s.readVersion
	reads := 0
	s.readVersion = func(host string, port int) (map[string]string, error) {
		v, err := inner(host, port)
		ff.mu.Lock()
		if b.pushed {
			reads++
		}
		ff.mu.Unlock()
		return v, err
	}
	// Once the update is done, the next read (the honesty check) sees the
	// engine missing. Done by swapping the box state from the done phase.
	s.emit = func(p updatePhase) {
		if p.Phase == outcomeDone && p.Outcome == outcomeDone {
			ff.mu.Lock()
			b.version["goLibrespot"] = "missing"
			ff.mu.Unlock()
		}
	}
	out, err := orchestrateFleet(s, fleetPolicy, []FleetTarget{{Host: "h", Port: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := out.Rows[0]; r.Outcome != outcomePartial || r.Unmet != unmetSpotifyEngine {
		t.Errorf("row %+v, want partial with the engine unmet", r)
	}
}

func TestFleetNothingStopsEarly(t *testing.T) {
	ff := newFleetFake()
	ff.add("ok", "present")
	bad := ff.add("bad", "present")
	bad.pushErr = errors.New("status 500: refused")
	gone := ff.add("gone", "present")
	gone.verdict = "agent-gone"
	s := ff.seams()
	inner := s.push
	s.push = func(host string, port int) error {
		if host == "gone" {
			// Takes the bytes, never comes back on the new build.
			ff.mu.Lock()
			ff.pushLog = append(ff.pushLog, pushSpan{host: host, start: time.Now(), end: time.Now()})
			ff.mu.Unlock()
			ff.taps.notify(host, 100)
			return nil
		}
		return inner(host, port)
	}
	out, err := orchestrateFleet(s, fleetPolicy, []FleetTarget{{Host: "ok", Port: 1}, {Host: "bad", Port: 1}, {Host: "gone", Port: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]UpdateOutcome{}
	for _, r := range out.Rows {
		got[r.Host] = r
	}
	if got["ok"].Outcome != outcomeDone {
		t.Errorf("ok: %+v", got["ok"])
	}
	if got["bad"].Outcome != outcomeFailed || !strings.Contains(got["bad"].Err, "status 500") {
		t.Errorf("bad: %+v", got["bad"])
	}
	if got["gone"].Outcome != outcomeAgentGone || got["gone"].Verdict != "agent-gone" {
		t.Errorf("gone: %+v", got["gone"])
	}
	if len(out.Rows) != 3 || out.Rows[0].Host != "ok" || out.Rows[2].Host != "gone" {
		t.Errorf("rows come back in target order: %v", out.Rows)
	}
	if otaRunning.Load() {
		t.Error("busy must be cleared when the batch ends")
	}
}
