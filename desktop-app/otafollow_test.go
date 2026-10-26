package main

import (
	"io"
	"log/slog"
	"strings"
	"sync"
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
// keeps the old behaviour: no guard, no following, the address decides.
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
	called := false
	mf.locate = func(speakerIdentity, string, bool) (string, int, bool) {
		called = true
		return "", 0, false
	}
	out, _ := orchestrateSpeaker(mf.seams(), otaProductionPolicy, addrOld, 8888, idA, "Kitchen", nil)
	if !out.Confirmed || out.Outcome != outcomeDone {
		t.Fatalf("outcome = %+v, want done", out)
	}
	if called {
		t.Error("locate ran for a speaker whose identity is unknown")
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
	// A record carrying the id is accepted on an answer that cannot be
	// compared, but never on one that names another speaker.
	a = mk(BoxInfo{Host: "192.0.2.32", Kind: "str", DeviceID: idA})
	if h, _, ok := a.locateSpeaker(ident, addrOld, false, probe); !ok || h != "192.0.2.32" {
		t.Errorf("by id = %q, %v; want 192.0.2.32", h, ok)
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
		t.Error("an identity the speaker never reported cannot confirm any answer")
	}
}
