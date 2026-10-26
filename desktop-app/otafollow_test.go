package main

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A router that hands out new leases on every reboot moved three of four
// speakers in one fleet update (2026-10-26). These tests replay the shape of
// it: speaker A is updated at addrOld, speaker B takes addrOld while both
// restart, and A turns up at addrNew.

const (
	addrOld = "192.0.2.10"
	addrNew = "192.0.2.12"
	idA     = "AAAAAAAAAAAA"
	idB     = "BBBBBBBBBBBB"
	nameA   = "str-aaaaaa.local"
	nameB   = "str-bbbbbb.local"
)

// movingFake is a house in which addresses change hands over (fake) time.
type movingFake struct {
	clk *fakeClock
	mu  sync.Mutex

	// answer is what host says at time t, after the push at pushAt.
	answer func(host string, since time.Duration) (map[string]string, error)
	pushAt time.Time
	pushed bool

	engineDelivered map[string]bool
	pushes          []string
	engines         []string
	classifies      int
	outcomes        []string
	noted           []string
	restores        int
	phases          []updatePhase
	locate          func(id speakerIdentity, exclude string, sweep bool) (string, int, bool)
}

func newMovingFake() *movingFake {
	return &movingFake{clk: newFakeClock(), engineDelivered: map[string]bool{}}
}

func (mf *movingFake) read(host string, _ int) (map[string]string, error) {
	mf.mu.Lock()
	since := time.Duration(-1)
	if mf.pushed {
		since = mf.clk.now().Sub(mf.pushAt)
	}
	ans := mf.answer
	mf.mu.Unlock()
	return ans(host, since)
}

func (mf *movingFake) seams() *otaSeams {
	return &otaSeams{
		push: func(host string, port int) error {
			mf.mu.Lock()
			defer mf.mu.Unlock()
			mf.pushes = append(mf.pushes, host)
			mf.pushed = true
			mf.pushAt = mf.clk.now()
			return nil
		},
		ensureEngine: func(host string, port int) (string, error) {
			mf.mu.Lock()
			defer mf.mu.Unlock()
			mf.engines = append(mf.engines, host)
			mf.engineDelivered[host] = true
			return "ok", nil
		},
		readVersion: mf.read,
		classify: func(host string, port int) string {
			mf.mu.Lock()
			defer mf.mu.Unlock()
			mf.classifies++
			return "confirmed"
		},
		recordOutcome: func(host, verdict string) {
			mf.mu.Lock()
			defer mf.mu.Unlock()
			mf.outcomes = append(mf.outcomes, host+" "+verdict)
		},
		restoreGroup: func(host string, port int) {
			mf.mu.Lock()
			defer mf.mu.Unlock()
			mf.restores++
		},
		locate: func(id speakerIdentity, exclude string, sweep bool) (string, int, bool) {
			if mf.locate == nil {
				return "", 0, false
			}
			return mf.locate(id, exclude, sweep)
		},
		noteUnconfirmed: func(host, verdict string) {
			mf.mu.Lock()
			defer mf.mu.Unlock()
			mf.noted = append(mf.noted, host+" "+verdict)
		},
		spawn: func(fn func()) { fn() },
		sleep: mf.clk.sleep,
		now:   mf.clk.now,
		emit: func(p updatePhase) {
			mf.mu.Lock()
			defer mf.mu.Unlock()
			mf.phases = append(mf.phases, p)
		},
		appVersion: testAppVer,
		appBuild:   testAppBuild,
		appSha:     testAppSha,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func (mf *movingFake) journal() string {
	mf.mu.Lock()
	defer mf.mu.Unlock()
	return strings.Join(mf.outcomes, "\n")
}

// speakerA before the update: an agent that reports its name, not its id.
func speakerAOld() map[string]string {
	v := oldVersion()
	v["mdnsName"] = nameA
	return v
}

// speakerANew after the update: the new agent reports its id too, and the
// engine was dropped to make the update fit until it is delivered again.
func (mf *movingFake) speakerANew(host string) map[string]string {
	engine := "missing"
	mf.mu.Lock()
	if mf.engineDelivered[host] {
		engine = "present"
	}
	mf.mu.Unlock()
	v := newVersion(engine)
	v["mdnsName"] = nameA
	v["deviceID"] = idA
	if engine == "missing" {
		v["goLibrespotDroppedForUpdate"] = "true"
	}
	return v
}

// speakerB is already on the new build, with its engine.
func speakerB() map[string]string {
	v := newVersion("present")
	v["mdnsName"] = nameB
	v["deviceID"] = idB
	return v
}

// leaseSwap: A answers at addrOld until the push, nothing answers for 30 s,
// then B answers at addrOld. A answers at addrNew from aBackAt on (never, if
// aBackAt is negative).
func (mf *movingFake) leaseSwap(aBackAt time.Duration) {
	mf.answer = func(host string, since time.Duration) (map[string]string, error) {
		switch host {
		case addrOld:
			if since < 0 {
				return speakerAOld(), nil
			}
			if since < 30*time.Second {
				return nil, errUnreachable
			}
			return speakerB(), nil
		case addrNew:
			if aBackAt >= 0 && since >= aBackAt {
				return mf.speakerANew(host), nil
			}
		}
		return nil, errUnreachable
	}
}

func TestIdentityCompare(t *testing.T) {
	ref := identityFrom(map[string]string{"mdnsName": "STR-AAAAAA.local.", "deviceID": idA})
	cases := []struct {
		name             string
		v                map[string]string
		comparable, want bool
	}{
		{"same name", map[string]string{"mdnsName": nameA}, true, true},
		{"same id, other case", map[string]string{"deviceID": strings.ToLower(idA)}, true, true},
		{"id as the firmware id", map[string]string{"boxDeviceID": idA}, true, true},
		{"other name", map[string]string{"mdnsName": nameB}, true, false},
		{"other id and name", map[string]string{"mdnsName": nameB, "deviceID": idB}, true, false},
		// One signal that agrees is enough: a speaker that changed one of its
		// ids is still itself.
		{"same name, other id", map[string]string{"mdnsName": nameA, "deviceID": idB}, true, true},
		{"nothing to compare", map[string]string{"version": "v1"}, false, false},
	}
	for _, c := range cases {
		gotC, gotM := ref.compare(c.v)
		if gotC != c.comparable || gotM != c.want {
			t.Errorf("%s: compare = (%v, %v), want (%v, %v)", c.name, gotC, gotM, c.comparable, c.want)
		}
	}
	if (speakerIdentity{}).contradicts(speakerB()) {
		t.Error("an unknown identity must never call an answer foreign")
	}
	// A name-only reference learns the id from a matching answer.
	nameOnly := identityFrom(map[string]string{"mdnsName": nameA})
	nameOnly.learn(map[string]string{"mdnsName": nameA, "deviceID": idA})
	if !nameOnly.contradicts(map[string]string{"deviceID": idB}) {
		t.Error("the learned id did not take: an answer with only another id was not called foreign")
	}
}

// The field case end to end: B answers at A's old address on the new build.
// A's update must not confirm on it, the engine must not go to B, and A must
// be found at its new address, confirmed there and given its engine there.
func TestUpdateFollowsSpeakerToItsNewAddress(t *testing.T) {
	mf := newMovingFake()
	mf.leaseSwap(90 * time.Second)
	// What discovery knows: A's record at its new address (found by its id)
	// and B at the old one.
	a := &App{discCache: map[string]discEntry{
		addrNew: {box: BoxInfo{Host: addrNew, Port: 8888, Kind: "str", DeviceID: idA}},
		addrOld: {box: BoxInfo{Host: addrOld, Port: 8888, Kind: "str", DeviceID: idB}},
	}}
	mf.locate = func(id speakerIdentity, exclude string, sweep bool) (string, int, bool) {
		return a.locateSpeaker(id, exclude, sweep, mf.read)
	}

	out, err := orchestrateSpeaker(mf.seams(), otaProductionPolicy, addrOld, 8888, idA, "Kitchen", nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !out.Confirmed || out.Outcome != outcomeDone {
		t.Fatalf("outcome = %+v, want a confirmed, done update", out)
	}
	if out.Version["mdnsName"] != nameA {
		t.Errorf("confirmed on %q's answer, want speaker A's", out.Version["mdnsName"])
	}
	for _, h := range mf.engines {
		if h != addrNew {
			t.Fatalf("engine pushed to %s, want only %s (the speaker's new address): %v", h, addrNew, mf.engines)
		}
	}
	if len(mf.engines) == 0 {
		t.Fatal("the engine A dropped for the update was never delivered back")
	}
	if mf.classifies != 0 {
		t.Errorf("classify ran %d times; a followed speaker confirms on its own answer", mf.classifies)
	}
	j := mf.journal()
	if !strings.Contains(j, "moved from "+addrOld+" to "+addrNew) {
		t.Errorf("journal lacks the move line:\n%s", j)
	}
	if !strings.Contains(j, "another speaker ("+nameB+") now answers at "+addrOld) {
		t.Errorf("journal lacks the other-speaker line:\n%s", j)
	}
	// The group record names the old addresses, one of which is B's now.
	if mf.restores != 0 || !strings.Contains(j, "group not rebuilt automatically") {
		t.Errorf("restores = %d, journal:\n%s", mf.restores, j)
	}
	// The window's row stays keyed by the address the update started on.
	for _, p := range mf.phases {
		if p.Host != addrOld {
			t.Fatalf("phase %q went out for host %q, want %q", p.Phase, p.Host, addrOld)
		}
	}
	// Every journal line stays on the update's own thread.
	for _, line := range mf.outcomes {
		if !strings.HasPrefix(line, addrOld+" ") {
			t.Errorf("journal line under another host: %q", line)
		}
	}
}

// When the speaker is not found at a new address in time, the update is NOT
// confirmed on the other speaker's answer, nothing is pushed to the other
// speaker, and the verdict says the address changed instead of running the
// classifier on the other speaker (which would have read "confirmed").
func TestUpdateNeverConfirmsOnAnotherSpeakersAnswer(t *testing.T) {
	mf := newMovingFake()
	mf.leaseSwap(-1)

	out, err := orchestrateSpeaker(mf.seams(), otaProductionPolicy, addrOld, 8888, idA, "Kitchen", nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out.Confirmed || out.Outcome != outcomeTimeout {
		t.Fatalf("outcome = %+v, want an unconfirmed timeout", out)
	}
	if out.Verdict != verdictAddressChanged || out.Unmet != verdictAddressChanged {
		t.Errorf("verdict=%q unmet=%q, want %q for both", out.Verdict, out.Unmet, verdictAddressChanged)
	}
	if mf.classifies != 0 {
		t.Errorf("classify ran on the other speaker's address %d times", mf.classifies)
	}
	if len(mf.engines) != 0 {
		t.Errorf("engine pushed while another speaker held the address: %v", mf.engines)
	}
	if len(mf.noted) != 1 || mf.noted[0] != addrOld+" "+verdictAddressChanged {
		t.Errorf("late-confirmation memo = %v, want one %q note for %s", mf.noted, verdictAddressChanged, addrOld)
	}
	j := mf.journal()
	if !strings.Contains(j, "NOT CONFIRMED - another speaker now answers at "+addrOld) || strings.Contains(j, "firewall") {
		t.Errorf("journal:\n%s", j)
	}
	last := mf.phases[len(mf.phases)-1]
	if last.Phase != outcomeTimeout || last.Unmet != verdictAddressChanged {
		t.Errorf("final phase = %+v, want a timeout carrying unmet %q", last, verdictAddressChanged)
	}
}

// A speaker whose agent says nothing about who it is (older than mdnsName)
// keeps the old behaviour: no guard, the address decides. It may be looked
// for by its discovery id while it restarts, but an answer that does not
// carry that id never moves the update.
func TestUpdateWithoutIdentityKeepsTheAddressRule(t *testing.T) {
	mf := newMovingFake()
	mf.answer = func(host string, since time.Duration) (map[string]string, error) {
		if host != addrOld {
			return nil, errUnreachable
		}
		if since < 0 {
			return oldVersion(), nil
		}
		if since < 30*time.Second {
			return nil, errUnreachable
		}
		return newVersion("present"), nil
	}
	mf.locate = func(speakerIdentity, string, bool) (string, int, bool) {
		return "", 0, false
	}
	out, _ := orchestrateSpeaker(mf.seams(), otaProductionPolicy, addrOld, 8888, idA, "Kitchen", nil)
	if !out.Confirmed || out.Outcome != outcomeDone {
		t.Fatalf("outcome = %+v, want done", out)
	}
	for _, h := range append(append([]string(nil), mf.pushes...), mf.engines...) {
		if h != addrOld {
			t.Errorf("pushed to %s, want only %s", h, addrOld)
		}
	}
}

func TestIdentityCompareHints(t *testing.T) {
	// A speaker on an agent that reports only its name before the update,
	// plus the id discovery holds for it.
	ref := identityFrom(map[string]string{"mdnsName": nameA})
	ref.hints = []string{idA}
	const idA2, idB2 = "A2A2A2A2A2A2", "B2B2B2B2B2B2"
	cases := []struct {
		name             string
		v                map[string]string
		comparable, want bool
	}{
		// Another speaker whose network was not up when its agent started:
		// no name for that whole boot, but both ids.
		{"other speaker, both ids, no name", map[string]string{"deviceID": idB, "boxDeviceID": idB2}, true, false},
		{"hint is the agent id", map[string]string{"deviceID": idA}, true, true},
		{"hint is the firmware id", map[string]string{"deviceID": idA2, "boxDeviceID": idA}, true, true},
		// One id that differs from the hint proves nothing: discovery may hold
		// the firmware id while the agent reports its own.
		{"lone other id", map[string]string{"deviceID": idA2}, false, false},
		{"name agrees, ids do not", map[string]string{"mdnsName": nameA, "deviceID": idA2, "boxDeviceID": idB2}, true, true},
	}
	for _, c := range cases {
		gotC, gotM := ref.compare(c.v)
		if gotC != c.comparable || gotM != c.want {
			t.Errorf("%s: compare = (%v, %v), want (%v, %v)", c.name, gotC, gotM, c.comparable, c.want)
		}
	}
	// What matches by a hint is learned, so a later lone id is held to it.
	ref.learn(map[string]string{"deviceID": idA})
	if !ref.contradicts(map[string]string{"deviceID": idB}) {
		t.Error("an id learned through a hint did not take")
	}
}

// The lease swap with an answer that carries no name: speaker B, which took
// A's address, started before its network was up and so answers without a
// name for the whole boot. A's reference is name-only (its agent before the
// update reported no id), so only the discovery id can tell them apart. B's
// answer must not confirm A's update, and A's engine must not go to B.
func TestUpdateLeaseSwapWithoutNames(t *testing.T) {
	const idB2 = "B2B2B2B2B2B2"
	mf := newMovingFake()
	mf.answer = func(host string, since time.Duration) (map[string]string, error) {
		switch host {
		case addrOld:
			if since < 0 {
				return speakerAOld(), nil
			}
			if since < 30*time.Second {
				return nil, errUnreachable
			}
			v := newVersion("present")
			v["deviceID"] = idB
			v["boxDeviceID"] = idB2
			return v, nil
		case addrNew:
			if since >= 90*time.Second {
				v := mf.speakerANew(host)
				delete(v, "mdnsName")
				return v, nil
			}
		}
		return nil, errUnreachable
	}
	a := &App{discCache: map[string]discEntry{
		addrNew: {box: BoxInfo{Host: addrNew, Port: 8888, Kind: "str", DeviceID: idA}},
		addrOld: {box: BoxInfo{Host: addrOld, Port: 8888, Kind: "str", DeviceID: idB}},
	}}
	mf.locate = func(id speakerIdentity, exclude string, sweep bool) (string, int, bool) {
		return a.locateSpeaker(id, exclude, sweep, mf.read)
	}
	out, err := orchestrateSpeaker(mf.seams(), otaProductionPolicy, addrOld, 8888, idA, "Kitchen", nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !out.Confirmed || out.Outcome != outcomeDone || out.Version["deviceID"] != idA {
		t.Fatalf("outcome = %+v, want done and confirmed on speaker A's answer", out)
	}
	if len(mf.engines) == 0 {
		t.Fatal("the engine A dropped for the update was never delivered back")
	}
	for _, h := range mf.engines {
		if h != addrNew {
			t.Fatalf("engine pushed to %s, want only %s: %v", h, addrNew, mf.engines)
		}
	}
}

// A retry runs after the speaker restarted. If another speaker holds its
// address by then, at the top of the retry or after waiting at the upload
// gate behind other speakers, the agent must not be pushed there.
func TestRetryNeverPushesToAnotherSpeaker(t *testing.T) {
	for _, tc := range []struct {
		name string
		// flipOn is the phase at which B takes the address ("" = before).
		flipOn string
	}{
		{"taken before the retry", ""},
		{"taken while queued at the gate", phaseUploading},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mf := newMovingFake()
			var flipped atomic.Bool
			flipped.Store(tc.flipOn == "")
			mf.answer = func(host string, _ time.Duration) (map[string]string, error) {
				if host != addrOld {
					return nil, errUnreachable
				}
				if flipped.Load() {
					return speakerB(), nil
				}
				return mf.speakerANew(host), nil
			}
			s := mf.seams()
			emit := s.emit
			s.emit = func(p updatePhase) {
				if tc.flipOn != "" && p.Phase == tc.flipOn {
					flipped.Store(true)
				}
				emit(p)
			}
			ident := identityFrom(speakerAOld())
			ident.hints = []string{idA}
			// An older pre-update build, so the retry has an agent to push.
			s.appBuild, s.appSha = "other-build", "other-sha"
			u := &speakerUpdater{host: addrOld, port: 8888, deviceID: idA, name: "Kitchen",
				seams: s, policy: otaProductionPolicy, gate: newUploadGate(1), ident: ident}
			out := u.run(2)
			if len(mf.pushes) != 0 {
				t.Fatalf("agent pushed to %v while another speaker held the address", mf.pushes)
			}
			if len(mf.engines) != 0 {
				t.Errorf("engine pushed to %v", mf.engines)
			}
			if out.Unmet != verdictAddressChanged || out.Outcome != outcomeTimeout {
				t.Errorf("outcome = %+v, want a timeout with unmet %q", out, verdictAddressChanged)
			}
		})
	}
}

// The engine can wait at the upload gate for minutes behind other speakers'
// pushes. If another speaker takes the address meanwhile, the engine must
// not go to it: the speaker is asked again right before the push.
func TestEngineNotPushedWhenAddressTakenWhileQueued(t *testing.T) {
	mf := newMovingFake()
	var flipped atomic.Bool
	mf.answer = func(host string, _ time.Duration) (map[string]string, error) {
		if host != addrOld {
			return nil, errUnreachable
		}
		if flipped.Load() {
			return speakerB(), nil
		}
		return mf.speakerANew(host), nil
	}
	s := mf.seams()
	emit := s.emit
	s.emit = func(p updatePhase) {
		if p.Phase == phaseEngineQueued {
			flipped.Store(true)
		}
		emit(p)
	}
	ident := identityFrom(speakerAOld())
	ident.hints = []string{idA}
	u := &speakerUpdater{host: addrOld, port: 8888, deviceID: idA, name: "Kitchen",
		seams: s, policy: otaProductionPolicy, gate: newUploadGate(1), ident: ident}
	out := u.deliverEngine(UpdateOutcome{Host: addrOld, Attempts: 1}, mf.speakerANew(addrOld), testOldVer)
	if len(mf.engines) != 0 {
		t.Fatalf("engine pushed to %v after another speaker took the address in the queue", mf.engines)
	}
	if out.Outcome != outcomePartial {
		t.Errorf("outcome = %q, want partial (engine still missing)", out.Outcome)
	}
}

// The update record moves with the speaker, so the speaker that now holds the
// old address is never judged against it.
func TestUpdateIntentFollowsTheSpeaker(t *testing.T) {
	mf := newMovingFake()
	mf.leaseSwap(90 * time.Second)
	a := &App{discCache: map[string]discEntry{
		addrNew: {box: BoxInfo{Host: addrNew, Port: 8888, Kind: "str", DeviceID: idA}},
		addrOld: {box: BoxInfo{Host: addrOld, Port: 8888, Kind: "str", DeviceID: idB}},
	}}
	mf.locate = func(id speakerIdentity, exclude string, sweep bool) (string, int, bool) {
		return a.locateSpeaker(id, exclude, sweep, mf.read)
	}
	var mu sync.Mutex
	intents := map[string]string{}
	s := mf.seams()
	s.recordIntent = func(host string, _ int, deviceID, _ string) {
		mu.Lock()
		defer mu.Unlock()
		intents[host] = deviceID
	}
	s.clearIntent = func(host string, _ int) {
		mu.Lock()
		defer mu.Unlock()
		delete(intents, host)
	}
	// Keep the record alive past the move: the engine never lands.
	s.ensureEngine = func(string, int) (string, error) { return "", errors.New("connection reset by peer") }
	out, _ := orchestrateSpeaker(s, otaProductionPolicy, addrOld, 8888, idA, "Kitchen", nil)
	if out.Outcome != outcomePartial {
		t.Fatalf("outcome = %q, want partial", out.Outcome)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := intents[addrOld]; ok {
		t.Errorf("update record left at the old address, which another speaker holds now: %v", intents)
	}
	if intents[addrNew] != idA {
		t.Errorf("update record not moved to the speaker's new address: %v", intents)
	}
}

func TestIntentForOtherSpeaker(t *testing.T) {
	in := updateIntent{Host: addrOld, DeviceID: idA}
	if !intentForOtherSpeaker(in, map[string]string{"deviceID": idB, "boxDeviceID": "B2B2B2B2B2B2"}) {
		t.Error("a speaker reporting two other ids was judged against this record")
	}
	if intentForOtherSpeaker(in, map[string]string{"deviceID": idA}) {
		t.Error("the record's own speaker was called another one")
	}
	if intentForOtherSpeaker(in, map[string]string{"deviceID": idB}) {
		t.Error("a lone differing id proves nothing")
	}
	if intentForOtherSpeaker(updateIntent{Host: addrOld}, speakerB()) {
		t.Error("a record without an id cannot tell")
	}
}

func TestLocateSpeaker(t *testing.T) {
	ident := identityFrom(map[string]string{"mdnsName": nameA})
	ident.hints = []string{idA}
	answers := map[string]map[string]string{
		"192.0.2.30": speakerB(),
		"192.0.2.31": {"mdnsName": nameA, "version": "v1"},
		"192.0.2.32": {"version": "v1"},
	}
	probe := func(host string, _ int) (map[string]string, error) {
		if v, ok := answers[host]; ok {
			return v, nil
		}
		return nil, errUnreachable
	}
	mk := func(recs ...BoxInfo) *App {
		a := &App{discCache: map[string]discEntry{}}
		for _, b := range recs {
			a.discCache[b.Host] = discEntry{box: b}
		}
		return a
	}

	// Without sweep only records tied to the speaker's id are asked.
	a := mk(BoxInfo{Host: "192.0.2.31", Kind: "str", Port: 8888})
	if _, _, ok := a.locateSpeaker(ident, addrOld, false, probe); ok {
		t.Error("found the speaker by asking a record not tied to its id, without sweep")
	}
	// With sweep, a positive answer anywhere is accepted.
	if h, _, ok := a.locateSpeaker(ident, addrOld, true, probe); !ok || h != "192.0.2.31" {
		t.Errorf("sweep = %q, %v; want 192.0.2.31", h, ok)
	}
	// A record carrying the id is no proof on its own: the address may hold
	// another speaker by now, so an answer that cannot be compared is not
	// accepted, and one that names another speaker never is.
	a = mk(BoxInfo{Host: "192.0.2.32", Kind: "str", DeviceID: idA})
	if _, _, ok := a.locateSpeaker(ident, addrOld, false, probe); ok {
		t.Error("accepted an address on the discovery record's word alone")
	}
	// An answer that carries the id discovery holds for the speaker is a
	// positive match, even without a name.
	answers["192.0.2.33"] = map[string]string{"deviceID": idA, "version": "v1"}
	a = mk(BoxInfo{Host: "192.0.2.33", Kind: "str", DeviceID: idA})
	if h, _, ok := a.locateSpeaker(ident, addrOld, false, probe); !ok || h != "192.0.2.33" {
		t.Errorf("by id = %q, %v; want 192.0.2.33", h, ok)
	}
	a = mk(BoxInfo{Host: "192.0.2.30", Kind: "str", DeviceID: idA})
	if _, _, ok := a.locateSpeaker(ident, addrOld, true, probe); ok {
		t.Error("accepted an address whose speaker names itself as another one")
	}
	// The address the speaker left is never a candidate, and a stock record
	// is swept only when it carries the id.
	a = mk(BoxInfo{Host: addrOld, Kind: "str", DeviceID: idA}, BoxInfo{Host: "192.0.2.31", Kind: "stock"})
	if _, _, ok := a.locateSpeaker(ident, addrOld, true, probe); ok {
		t.Error("located the speaker at the excluded address or on a stock record without its id")
	}
	if _, _, ok := a.locateSpeaker(speakerIdentity{hints: []string{idA}}, "", true, probe); ok {
		t.Error("located the speaker on answers that do not carry its id")
	}
}
