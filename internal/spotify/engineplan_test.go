package spotify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// productEngine is an engine whose /status reports the account AND, on a build
// that has the field, the plan the access point told it at login. tokenCalls
// counts the /token requests, which only the /v1/me fallback makes.
type productEngine struct {
	mu         sync.Mutex
	user       string
	product    string // "" = an older engine without the field
	tokenCalls atomic.Int32
}

func (e *productEngine) set(user, product string) {
	e.mu.Lock()
	e.user, e.product = user, product
	e.mu.Unlock()
}

func (e *productEngine) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			e.tokenCalls.Add(1)
			_, _ = w.Write([]byte(`{"token":"BQ-test-token"}`))
		case "/status":
			e.mu.Lock()
			st := map[string]string{"username": e.user}
			if e.product != "" {
				st["product_type"] = e.product
			}
			e.mu.Unlock()
			_ = json.NewEncoder(w).Encode(st)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The field case behind #1077: the Web API answers 429/503 every time, but the
// engine already knows the plan from login. The plan must come from the engine
// with no Web API call at all, be fresh enough to gate, and be remembered.
func TestThePlanComesFromTheEngineWithoutAskingSpotify(t *testing.T) {
	eng := &productEngine{user: "accountalpha0001", product: "free"}
	var product atomic.Value
	product.Store("premium")
	var limited atomic.Bool
	limited.Store(true)
	var meCalls atomic.Int32
	spot := spotifyAnswering(t, &product, &limited, &meCalls)
	m := productTestManager(t, eng.serve(t).URL)
	// The name is already known, so the Web API has nothing left to tell.
	m.rememberAccountName("accountalpha0001", "Alpha")

	if got := m.accountProductAt(context.Background(), spot.URL); got != "free" {
		t.Fatalf("accountProduct = %q, want the engine's free", got)
	}
	if meCalls.Load() != 0 || eng.tokenCalls.Load() != 0 {
		t.Fatalf("Web API asked although the engine knew: me=%d token=%d", meCalls.Load(), eng.tokenCalls.Load())
	}
	// Fresh, so it gates exactly like a fresh /v1/me answer.
	noBackgroundRead(m)
	if !m.PremiumRequired() {
		t.Error("an engine-reported free plan did not gate")
	}
	// And remembered for the next engine process.
	m.mu.Lock()
	rp := m.accountPlans["accountalpha0001"]
	m.mu.Unlock()
	if rp.Product != "free" {
		t.Fatalf("remembered plan = %q, want free", rp.Product)
	}
	// Normalised the way the engine may not be.
	engineRestart(m)
	eng.set("accountalpha0001", " Premium ")
	if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
		t.Fatalf("accountProduct = %q, want premium", got)
	}
	if meCalls.Load() != 0 {
		t.Fatalf("Web API asked on the second process: me=%d", meCalls.Load())
	}
}

// An engine build without the field changes nothing: /v1/me is asked exactly
// as before.
func TestAnOlderEngineFallsBackToTheWebAPI(t *testing.T) {
	eng := &productEngine{user: "accountalpha0001"}
	var product atomic.Value
	product.Store("premium")
	var limited atomic.Bool
	var meCalls atomic.Int32
	spot := spotifyAnswering(t, &product, &limited, &meCalls)
	m := productTestManager(t, eng.serve(t).URL)

	if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
		t.Fatalf("accountProduct = %q, want premium from /v1/me", got)
	}
	if meCalls.Load() != 1 || eng.tokenCalls.Load() != 1 {
		t.Fatalf("calls: me=%d token=%d, want one each", meCalls.Load(), eng.tokenCalls.Load())
	}
}

// A plan reported with no account logged in has nobody to belong to.
func TestAnEnginePlanWithoutAnAccountIsNotUsed(t *testing.T) {
	eng := &productEngine{user: "", product: "free"}
	var product atomic.Value
	product.Store("premium")
	var limited atomic.Bool
	limited.Store(true)
	var meCalls atomic.Int32
	spot := spotifyAnswering(t, &product, &limited, &meCalls)
	m := productTestManager(t, eng.serve(t).URL)

	if got := m.accountProductAt(context.Background(), spot.URL); got != "" {
		t.Fatalf("accountProduct = %q with nobody logged in, want unknown", got)
	}
	noBackgroundRead(m)
	if m.PremiumRequired() {
		t.Error("gated on a plan that belongs to no account")
	}
}

// Switching accounts never carries the previous account's plan over, whether
// the switch restarts the engine or is only seen in /status.
func TestAnAccountSwitchDoesNotCarryTheEnginePlan(t *testing.T) {
	eng := &productEngine{user: "accountalpha0001", product: "free"}
	var product atomic.Value
	product.Store("premium")
	var limited atomic.Bool
	limited.Store(true)
	var meCalls atomic.Int32
	spot := spotifyAnswering(t, &product, &limited, &meCalls)
	m := productTestManager(t, eng.serve(t).URL)
	m.rememberAccountName("accountalpha0001", "Alpha")
	m.rememberAccountName("accountbravo0002", "Bravo")

	if got := m.accountProductAt(context.Background(), spot.URL); got != "free" {
		t.Fatalf("first account = %q, want free", got)
	}

	// The switch restarts the engine; the new login has not had its
	// ProductInfo packet yet and the Web API is throttled.
	engineRestart(m)
	eng.set("accountbravo0002", "")
	if got := m.accountProductAt(context.Background(), spot.URL); got != "" {
		t.Fatalf("second account = %q, want unknown, not the first account's plan", got)
	}
	noBackgroundRead(m)
	if m.PremiumRequired() {
		t.Error("the second account was gated by the first account's free plan")
	}

	// That miss fell back to /v1/me (throttled). Lift the throttle so any
	// further Web API call would be seen answering, and count from here.
	limited.Store(false)
	m.mu.Lock()
	m.productQuietUntil, m.productQuietFor = time.Time{}, 0
	m.mu.Unlock()
	before := meCalls.Load()

	// The packet arrives: the second account's own plan is used.
	eng.set("accountbravo0002", "premium")
	if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
		t.Fatalf("second account after its packet = %q, want premium", got)
	}

	// A switch seen only in /status (no restart in between) drops the cached
	// fresh plan once the cache is re-checked, and the new account's report
	// wins over it.
	eng.set("accountalpha0001", "free")
	m.mu.Lock()
	m.productCheckedAt = m.productCheckedAt.Add(-10 * time.Minute)
	m.mu.Unlock()
	if got := m.accountProductAt(context.Background(), spot.URL); got != "free" {
		t.Fatalf("switch back = %q, want the first account's own free", got)
	}
	if n := meCalls.Load() - before; n != 0 {
		t.Fatalf("Web API asked %d times although the engine reported the plan", n)
	}
}

// spotifyWithName answers /v1/me like Spotify does for a token with the
// user-read-private scope: a plan AND a display name.
func spotifyWithName(t *testing.T, product, name string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"product": product, "display_name": name})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An engine that reports the plan must not cost the account its name: /v1/me
// is the only place the display name comes from, so an account whose name is
// not known yet gets exactly one call, the name is remembered, and the plan in
// that answer does not override the engine's.
func TestTheEnginePlanStillLearnsTheAccountName(t *testing.T) {
	eng := &productEngine{user: "accountalpha0001", product: "free"}
	var meCalls atomic.Int32
	// Spotify says premium here on purpose: the engine's report from login
	// is the one that gates.
	spot := spotifyWithName(t, "premium", "Alpha Household", &meCalls)
	m := productTestManager(t, eng.serve(t).URL)

	if got := m.accountProductAt(context.Background(), spot.URL); got != "free" {
		t.Fatalf("accountProduct = %q, want the engine's free", got)
	}
	if n := meCalls.Load(); n != 1 {
		t.Fatalf("/v1/me calls = %d, want exactly one for the name", n)
	}
	if got := m.AccountNames()["accountalpha0001"]; got != "Alpha Household" {
		t.Fatalf("remembered name = %q, want it learned from /v1/me", got)
	}
	noBackgroundRead(m)
	if !m.PremiumRequired() {
		t.Error("the /v1/me product overrode the engine's free plan")
	}

	// The next checks (cache expired) have the name: no more Web API calls.
	for i := 0; i < 3; i++ {
		m.mu.Lock()
		m.productCheckedAt = m.productCheckedAt.Add(-10 * time.Minute)
		m.mu.Unlock()
		if got := m.accountProductAt(context.Background(), spot.URL); got != "free" {
			t.Fatalf("check %d = %q, want free", i, got)
		}
	}
	if n := meCalls.Load(); n != 1 {
		t.Fatalf("/v1/me calls = %d after the name was known, want still one", n)
	}
}

// A name that is already known means the engine's plan needs no Web API call
// at all.
func TestAKnownNameMeansNoWebAPICall(t *testing.T) {
	eng := &productEngine{user: "accountalpha0001", product: "premium"}
	var meCalls atomic.Int32
	spot := spotifyWithName(t, "premium", "Alpha Household", &meCalls)
	m := productTestManager(t, eng.serve(t).URL)
	m.rememberAccountName("accountalpha0001", "Alpha Household")

	if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
		t.Fatalf("accountProduct = %q, want premium", got)
	}
	if meCalls.Load() != 0 || eng.tokenCalls.Load() != 0 {
		t.Fatalf("Web API asked although plan and name were known: me=%d token=%d", meCalls.Load(), eng.tokenCalls.Load())
	}
}

// Spotify not sending a name (a token without the scope) is asked about once
// per engine process, not every five minutes, and a throttled Spotify is left
// alone for the name exactly as for the plan.
func TestTheNameIsAskedForOncePerProcess(t *testing.T) {
	eng := &productEngine{user: "accountalpha0001", product: "premium"}
	var product atomic.Value
	product.Store("premium")
	var limited atomic.Bool
	limited.Store(true)
	var meCalls atomic.Int32
	spot := spotifyAnswering(t, &product, &limited, &meCalls)
	m := productTestManager(t, eng.serve(t).URL)

	expire := func() {
		m.mu.Lock()
		m.productCheckedAt = m.productCheckedAt.Add(-10 * time.Minute)
		m.mu.Unlock()
	}
	// Throttled: one refused call, and the engine's plan still answers.
	if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
		t.Fatalf("throttled = %q, want the engine's premium", got)
	}
	expire()
	if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
		t.Fatalf("in the quiet window = %q, want premium", got)
	}
	if n := meCalls.Load(); n != 1 {
		t.Fatalf("/v1/me calls = %d, want one: the quiet window was not honoured", n)
	}

	// The throttle lifts; the answer carries no name. Asked once, then not again.
	limited.Store(false)
	m.mu.Lock()
	m.productQuietUntil, m.productQuietFor = time.Time{}, 0
	m.mu.Unlock()
	for i := 0; i < 3; i++ {
		expire()
		if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
			t.Fatalf("check %d = %q, want premium", i, got)
		}
	}
	if n := meCalls.Load(); n != 2 {
		t.Fatalf("/v1/me calls = %d, want two (one refused, one answered without a name)", n)
	}
}
