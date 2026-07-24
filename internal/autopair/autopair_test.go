package autopair

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const pairedInfo = `<info><margeAccountUUID>stick@local</margeAccountUUID></info>`
const unpairedInfo = `<info><margeAccountUUID></margeAccountUUID></info>`

// fakeBox simulates the BoseApp :8090 endpoints the manager talks to.
type fakeBox struct {
	mu        sync.Mutex
	infoBody  string
	infoDate  string // optional Date header (the RTC clock gate reads it)
	infoDelay time.Duration
	pairPosts atomic.Int64
}

func (f *fakeBox) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		body, date, delay := f.infoBody, f.infoDate, f.infoDelay
		f.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		if date != "" {
			w.Header().Set("Date", date)
		}
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/setMargeAccount", func(w http.ResponseWriter, r *http.Request) {
		f.pairPosts.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func newTestManager(t *testing.T, box *fakeBox) *Manager {
	t.Helper()
	srv := httptest.NewServer(box.handler())
	t.Cleanup(srv.Close)
	m := New(slog.Default(), Config{BoxHost: "127.0.0.1"})
	m.base = srv.URL
	return m
}

func TestEnsurePairedSkipsWhenPaired(t *testing.T) {
	box := &fakeBox{infoBody: pairedInfo}
	m := newTestManager(t, box)
	if err := m.EnsurePaired(context.Background()); err != nil {
		t.Fatalf("EnsurePaired: %v", err)
	}
	if got := box.pairPosts.Load(); got != 0 {
		t.Fatalf("paired box must not be re-paired, got %d setMargeAccount POSTs", got)
	}
}

func TestEnsurePairedPairsWhenUnpaired(t *testing.T) {
	box := &fakeBox{infoBody: unpairedInfo}
	m := newTestManager(t, box)
	if err := m.EnsurePaired(context.Background()); err != nil {
		t.Fatalf("EnsurePaired: %v", err)
	}
	if got := box.pairPosts.Load(); got != 1 {
		t.Fatalf("unpaired box must be paired exactly once, got %d POSTs", got)
	}
}

func TestForceReassertsDespitePaired(t *testing.T) {
	// First cycle after agent start: a stale "paired" must not skip the
	// re-assert (dead-cloud amber icon, ST300 silent login loss).
	box := &fakeBox{infoBody: pairedInfo}
	m := newTestManager(t, box)
	if err := m.ensure(context.Background(), true); err != nil {
		t.Fatalf("ensure(force): %v", err)
	}
	if got := box.pairPosts.Load(); got != 1 {
		t.Fatalf("forced cycle must POST setMargeAccount once, got %d", got)
	}
}

// --- Fake-login maintenance (the 1036 NOT_LOGGED_IN class) ---
//
// A margeAccountUUID on the box does not mean the box considers itself logged
// in: fresh installs / factory-reset boxes keep the UUID while the MargeHSM
// drops to not-logged-in, and every UPnP source activation is refused with
// 1036 (field bundles 2026-07-22, all models). Boxes that still carry a cached
// pre-shutdown Bose account never show this. These tests pin the maintenance
// contract: a rejection marks the box login-suspect, and while suspect every
// pair cycle re-asserts the account instead of trusting the UUID-present skip.

func TestPairedBoxNoOpEvenAfterLoginError(t *testing.T) {
	// v0.9.0 behaviour, restored: a paired box (margeAccountUUID present) is a
	// no-op on the regular pair cycle even right after the box refused a source
	// as not-logged-in (1036). The old proactive per-heartbeat re-assert was
	// removed because re-onboarding a playing box powered it off mid-song (the
	// 0.9.17 self-off) and never prevented the box's own post-standby 1036. Only
	// the reactive ForcePair re-logs in, and only after a real failed press.
	box := &fakeBox{infoBody: pairedInfo}
	m := newTestManager(t, box)

	for i := 0; i < 5; i++ {
		if err := m.EnsurePaired(context.Background()); err != nil {
			t.Fatalf("EnsurePaired #%d: %v", i, err)
		}
	}
	if got := box.pairPosts.Load(); got != 0 {
		t.Fatalf("a paired box must never be re-asserted on the regular cycle, got %d POSTs", got)
	}
}

func TestForcePairReLogsInReactively(t *testing.T) {
	// The reactive re-login (boxws 1036 routing -> webui -> ForcePair) must POST
	// unconditionally to un-wedge the box (#342, ST300), independent of the
	// UUID-present skip. A second ForcePair right behind it coalesces so a burst
	// of failed presses does not storm setMargeAccount (#375).
	box := &fakeBox{infoBody: pairedInfo}
	m := newTestManager(t, box)

	m.ForcePair(context.Background())
	if got := box.pairPosts.Load(); got != 1 {
		t.Fatalf("ForcePair must POST unconditionally, got %d", got)
	}
	// Immediately again: coalesced into the just-completed re-login.
	m.ForcePair(context.Background())
	if got := box.pairPosts.Load(); got != 1 {
		t.Fatalf("a ForcePair right behind another must coalesce, got %d POSTs", got)
	}
}

func TestForcePairPostsEvenWhenInfoFails(t *testing.T) {
	// ForcePair is the reactive last resort: it must not depend on /info
	// being readable (a mid-flap box can refuse it) - it POSTs directly.
	box := &fakeBox{infoBody: pairedInfo}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/setMargeAccount" {
			box.pairPosts.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	m := New(slog.Default(), Config{BoxHost: "127.0.0.1"})
	m.base = srv.URL

	m.ForcePair(context.Background())
	if got := box.pairPosts.Load(); got != 1 {
		t.Fatalf("ForcePair must POST even when /info fails, got %d", got)
	}
}

func TestClockGateDefersPairing(t *testing.T) {
	// The 2015-RTC gate holds pairing: pairing against a not-yet-synced clock
	// fails with a TLS error anyway, so the cycle defers and the next tick
	// retries. Uses an unpaired box, the only path that reaches the clock gate
	// now that a paired box short-circuits to a no-op.
	box := &fakeBox{infoBody: unpairedInfo, infoDate: "Mon, 02 Feb 2015 10:00:00 GMT"}
	m := newTestManager(t, box)
	if err := m.EnsurePaired(context.Background()); err != nil {
		t.Fatalf("EnsurePaired: %v", err)
	}
	if got := box.pairPosts.Load(); got != 0 {
		t.Fatalf("a box on the 2015 RTC must not be paired yet, got %d POSTs", got)
	}
}

func TestPairXMLEscapesCredentials(t *testing.T) {
	xml := buildPairXML(`a&b<c>"d`, "tok&", "e<f")
	for _, want := range []string{"a&amp;b&lt;c&gt;&quot;d", "tok&amp;", "e&lt;f"} {
		if !strings.Contains(xml, want) {
			t.Fatalf("pair XML must escape credentials, missing %q in %s", want, xml)
		}
	}
}

func TestEnsurePairedSingleFlight(t *testing.T) {
	// Concurrent triggers (ticker + hardware press + app recall) must
	// coalesce into ONE cycle instead of stacking POSTs on a slow box (#375).
	box := &fakeBox{infoBody: unpairedInfo, infoDelay: 150 * time.Millisecond}
	m := newTestManager(t, box)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.EnsurePaired(context.Background())
		}()
	}
	wg.Wait()
	if got := box.pairPosts.Load(); got != 1 {
		t.Fatalf("8 concurrent triggers must coalesce into 1 pair POST, got %d", got)
	}
}

func TestOnPairedFiresOnlyOnUnpairedToPairedTransition(t *testing.T) {
	box := &fakeBox{infoBody: unpairedInfo}
	m := newTestManager(t, box)
	fired := 0
	m.SetOnPaired(func() { fired++ })

	// Initial observation (unpaired) is not a transition.
	if err := m.EnsurePaired(context.Background()); err != nil {
		t.Fatalf("EnsurePaired: %v", err)
	}
	if fired != 0 {
		t.Fatalf("initial unpaired observation must not fire, got %d", fired)
	}

	// The box completed its onboarding: unpaired -> paired must fire once
	// (the firmware wipes its key registrations during the onboarding, and
	// this hook is what schedules the immediate re-registration).
	box.mu.Lock()
	box.infoBody = pairedInfo
	box.mu.Unlock()
	if err := m.EnsurePaired(context.Background()); err != nil {
		t.Fatalf("EnsurePaired: %v", err)
	}
	if fired != 1 {
		t.Fatalf("unpaired->paired must fire the hook once, got %d", fired)
	}

	// Steady paired state: no more fires.
	if err := m.EnsurePaired(context.Background()); err != nil {
		t.Fatalf("EnsurePaired: %v", err)
	}
	if fired != 1 {
		t.Fatalf("steady paired state must not re-fire, got %d", fired)
	}
}

func TestOnPairedDoesNotFireOnInitialPairedObservation(t *testing.T) {
	// A box that is already paired at agent start went through no onboarding:
	// no wipe happened, so no forced re-sync is needed.
	box := &fakeBox{infoBody: pairedInfo}
	m := newTestManager(t, box)
	fired := 0
	m.SetOnPaired(func() { fired++ })
	if err := m.EnsurePaired(context.Background()); err != nil {
		t.Fatalf("EnsurePaired: %v", err)
	}
	if fired != 0 {
		t.Fatalf("initial paired observation must not fire, got %d", fired)
	}
}

func TestShouldSettleToSteady(t *testing.T) {
	const fastFor = 2 * time.Minute
	const unpairedMax = 10 * time.Minute
	cases := []struct {
		name    string
		elapsed time.Duration
		paired  bool
		want    bool
	}{
		{"paired, inside fast window", time.Minute, true, false},
		{"paired, fast window elapsed", 3 * time.Minute, true, true},
		{"unpaired, would have settled before", 3 * time.Minute, false, false},
		{"unpaired, holds the fast cadence long", 9 * time.Minute, false, false},
		{"unpaired, capped eventually", 11 * time.Minute, false, true},
	}
	for _, c := range cases {
		if got := shouldSettleToSteady(c.elapsed, c.paired, fastFor, unpairedMax); got != c.want {
			t.Errorf("%s: shouldSettleToSteady = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestForcePairDoesNotOverlapEnsure: the press-time suspect re-assert and the
// same press's 1036-driven ForcePair used to POST setMargeAccount concurrently
// - exactly the stacked-POST pattern the single-flight guard exists for
// (#375). ForcePair now runs inside the same guard: with a slow POST handler
// one of the two waits, and no two POSTs are ever in flight at once.
func TestForcePairDoesNotOverlapEnsure(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxInFlight, total := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/setMargeAccount" {
			mu.Lock()
			inFlight++
			total++
			if inFlight > maxInFlight {
				maxInFlight = inFlight
			}
			mu.Unlock()
			time.Sleep(400 * time.Millisecond) // the slow box the guard exists for
			mu.Lock()
			inFlight--
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(unpairedInfo))
	}))
	defer srv.Close()
	m := New(slog.Default(), Config{BoxHost: "127.0.0.1"})
	m.base = srv.URL
	// Unpaired box: the EnsurePaired cycle POSTs to pair, and the concurrent
	// ForcePair POSTs too - the two must not overlap (single-flight guard, #375).

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = m.EnsurePaired(context.Background()) }()
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m.ForcePair(ctx)
	}()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxInFlight > 1 {
		t.Fatalf("setMargeAccount POSTs overlapped (max %d in flight); the single-flight guard must cover ForcePair too (#375)", maxInFlight)
	}
	if total < 1 {
		t.Fatal("at least one re-assert must have been sent")
	}
}

// TestForcePairStampsSuspectAssert: the forced re-login IS a suspect
// re-assert, so the next ensure trigger inside the min-gap must not POST a
// second re-onboarding right behind it.
func TestForcePairStampsSuspectAssert(t *testing.T) {
	box := &fakeBox{infoBody: pairedInfo}
	m := newTestManager(t, box)

	m.ForcePair(context.Background())
	if got := box.pairPosts.Load(); got != 1 {
		t.Fatalf("ForcePair must POST, got %d", got)
	}

	if err := m.EnsurePaired(context.Background()); err != nil {
		t.Fatalf("EnsurePaired: %v", err)
	}
	if got := box.pairPosts.Load(); got != 1 {
		t.Fatalf("an ensure right after a forced re-login must coalesce into its min-gap, got %d POSTs", got)
	}
}
