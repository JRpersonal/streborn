package spotify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// planEngine is an engine that hands out a token and says who it is logged in
// as, with the account switchable mid-test the way a Connect tap switches it.
type planEngine struct {
	mu   sync.Mutex
	user string
}

func (e *planEngine) setUser(u string) {
	e.mu.Lock()
	e.user = u
	e.mu.Unlock()
}

func (e *planEngine) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"token":"BQ-test-token"}`))
		case "/status":
			e.mu.Lock()
			u := e.user
			e.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]string{"username": u})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// spotifyAnswering answers /v1/me with the given plan, or with a 429 while
// limited is set, which is what the field bundle showed for hours on end.
func spotifyAnswering(t *testing.T, product *atomic.Value, limited *atomic.Bool, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if limited.Load() {
			w.Header().Set("Retry-After", "47")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"product": product.Load().(string)})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// engineRestart is what runOnce does to the plan state on every engine start:
// the same helper, so the two cannot drift apart. It leaves a running cooldown
// alone, exactly like the real start does.
func engineRestart(m *Manager) {
	m.mu.Lock()
	m.resetProductStateLocked()
	m.mu.Unlock()
}

// noBackgroundRead keeps PremiumRequired from starting its background plan read
// against the real Spotify endpoint: these tests drive the read themselves.
func noBackgroundRead(m *Manager) {
	m.mu.Lock()
	m.productTriedAt = time.Now()
	m.mu.Unlock()
}

func infoProduct(t *testing.T, m *Manager) string {
	t.Helper()
	noBackgroundRead(m)
	rr := httptest.NewRecorder()
	m.ServeInfo(rr, httptest.NewRequest("GET", "/spotify/info", nil))
	var got struct {
		Product string `json:"product"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Product
}

// The field case (2026-10-26): the plan was read once, the engine restarted,
// and Spotify then answered 429 for hours. The plan the speaker already knew
// has to survive that, for the apps and for the Premium gate alike.
func TestRememberedPlanIsUsedAfterAnEngineRestart(t *testing.T) {
	eng := &planEngine{user: "accountalpha0001"}
	var product atomic.Value
	product.Store("premium")
	var limited atomic.Bool
	var calls atomic.Int32
	spot := spotifyAnswering(t, &product, &limited, &calls)
	m := productTestManager(t, eng.serve(t).URL)

	if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
		t.Fatalf("first read = %q, want premium", got)
	}

	engineRestart(m)
	limited.Store(true)
	if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
		t.Fatalf("rate-limited read after a restart = %q, want the remembered premium", got)
	}
	if got := infoProduct(t, m); got != "premium" {
		t.Fatalf("/spotify/info product = %q, want the remembered premium", got)
	}

	// The same holds across a reboot: a fresh manager over the same store.
	m2 := productTestManager(t, eng.serve(t).URL)
	m2.credStore = m.credStore
	if got := infoProduct(t, m2); got != "premium" {
		t.Fatalf("after a reboot product = %q, want the remembered premium", got)
	}

	// A remembered "free" is shown, but it never blocks recall: only a fresh
	// signal does (accountplans.go says why). The account may have upgraded.
	m3 := productTestManager(t, eng.serve(t).URL)
	m3.credStore = t.TempDir()
	m3.rememberAccountPlan("accountalpha0001", "free")
	m3.noteEngineAccount("accountalpha0001")
	if got := infoProduct(t, m3); got != "free" {
		t.Fatalf("/spotify/info product = %q, want the remembered free", got)
	}
	noBackgroundRead(m3)
	if m3.PremiumRequired() {
		t.Error("a remembered free plan blocked recall; only a fresh signal may")
	}
}

// The real restart path: the cooldown from an earlier 429 is still running when
// the engine comes back, so the plan comes from the store without Spotify being
// asked at all.
func TestARestartDuringTheCooldownUsesTheRememberedPlan(t *testing.T) {
	eng := &planEngine{user: "accountalpha0001"}
	var product atomic.Value
	product.Store("premium")
	var limited atomic.Bool
	var calls atomic.Int32
	spot := spotifyAnswering(t, &product, &limited, &calls)
	m := productTestManager(t, eng.serve(t).URL)

	if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
		t.Fatalf("first read = %q, want premium", got)
	}
	// Spotify throttles the next read: the cooldown starts.
	m.mu.Lock()
	m.productType = ""
	m.mu.Unlock()
	limited.Store(true)
	_ = m.accountProductAt(context.Background(), spot.URL)

	engineRestart(m)
	before := calls.Load()
	if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
		t.Fatalf("read during the cooldown after a restart = %q, want the remembered premium", got)
	}
	if calls.Load() != before {
		t.Error("Spotify was asked although the cooldown was still running")
	}
	m.mu.Lock()
	quiet := m.productQuietUntil
	m.mu.Unlock()
	if !time.Now().Before(quiet) {
		t.Error("the engine restart cleared the cooldown")
	}
}

// A remembered "premium" for an account that has since been downgraded, while
// Spotify answers 429: recall is not blocked on the stale value, and not on
// "unknown" either.
func TestARememberedPremiumDoesNotGateWhileThrottled(t *testing.T) {
	eng := &planEngine{user: "accountalpha0001"}
	var product atomic.Value
	product.Store("premium")
	var limited atomic.Bool
	var calls atomic.Int32
	spot := spotifyAnswering(t, &product, &limited, &calls)
	m := productTestManager(t, eng.serve(t).URL)

	_ = m.accountProductAt(context.Background(), spot.URL)
	engineRestart(m)
	product.Store("free") // downgraded, but Spotify will not say so yet
	limited.Store(true)
	if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
		t.Fatalf("throttled read = %q, want the remembered premium", got)
	}
	noBackgroundRead(m)
	if m.PremiumRequired() {
		t.Error("PremiumRequired true without a fresh non-Premium signal")
	}
}

// The account switches between the two /status reads around the /v1/me call.
// Whose token answered is not known for sure, so the answer is used for
// neither account: not in the file and not in memory.
func TestAnAccountSwitchDuringTheReadIsNotFiledUnderEitherAccount(t *testing.T) {
	eng := &planEngine{user: "accountalpha0001"}
	spot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eng.setUser("accountbravo0002") // a Connect tap while the call is out
		_ = json.NewEncoder(w).Encode(map[string]string{"product": "free"})
	}))
	t.Cleanup(spot.Close)
	m := productTestManager(t, eng.serve(t).URL)

	if got := m.accountProductAt(context.Background(), spot.URL); got != "" {
		t.Fatalf("read across a switch = %q, want unknown", got)
	}
	m.mu.Lock()
	fresh, user := m.productType, m.productUser
	m.mu.Unlock()
	if fresh != "" {
		t.Errorf("the other account's answer became the fresh plan: %q", fresh)
	}
	if user != "accountbravo0002" {
		t.Errorf("productUser = %q, want the account the engine reports now", user)
	}
	m.noteEngineAccount("accountalpha0001")
	if got := m.productPlan(); got != "" {
		t.Errorf("the answer was remembered for the first account: %q", got)
	}
	m.noteEngineAccount("accountbravo0002")
	if got := m.productPlan(); got != "" {
		t.Errorf("the answer was remembered for the second account: %q", got)
	}
	noBackgroundRead(m)
	if m.PremiumRequired() {
		t.Error("the second account was gated by an answer about the first")
	}
}

// A read that started before the engine restarted finishes afterwards. Same
// account on both sides, so the store may learn from it, but the new process's
// fresh plan is not written by a read the old process started.
func TestAReadFromBeforeAnEngineRestartIsNotTheFreshPlan(t *testing.T) {
	eng := &planEngine{user: "accountalpha0001"}
	var m *Manager
	spot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		engineRestart(m)
		_ = json.NewEncoder(w).Encode(map[string]string{"product": "free"})
	}))
	t.Cleanup(spot.Close)
	m = productTestManager(t, eng.serve(t).URL)

	_ = m.accountProductAt(context.Background(), spot.URL)
	m.mu.Lock()
	fresh := m.productType
	m.mu.Unlock()
	if fresh != "" {
		t.Errorf("a read from the previous engine process set the fresh plan: %q", fresh)
	}
	if got := m.productPlan(); got != "free" {
		t.Errorf("remembered plan = %q, want free (same account on both sides)", got)
	}
	noBackgroundRead(m)
	if m.PremiumRequired() {
		t.Error("a stale read gated recall")
	}
}

// An engine that stays up but reports nobody logged in: the previous account's
// plan, remembered or fresh, no longer applies.
func TestALoggedOutEngineDropsThePreviousAccountsPlan(t *testing.T) {
	eng := &planEngine{user: "accountalpha0001"}
	var product atomic.Value
	product.Store("free")
	var limited atomic.Bool
	var calls atomic.Int32
	spot := spotifyAnswering(t, &product, &limited, &calls)
	m := productTestManager(t, eng.serve(t).URL)

	if got := m.accountProductAt(context.Background(), spot.URL); got != "free" {
		t.Fatalf("first read = %q, want free", got)
	}
	eng.setUser("")
	if got := infoProduct(t, m); got != "" {
		t.Fatalf("/spotify/info product = %q with nobody logged in, want absent", got)
	}
	if m.PremiumRequired() {
		t.Error("a speaker nobody is logged into was gated by the previous account")
	}

	// An engine that cannot be asked at all says nothing about the login, and
	// does not clear it.
	m2 := productTestManager(t, "http://127.0.0.1:1")
	m2.credStore = m.credStore
	m2.noteEngineAccount("accountalpha0001")
	if got := infoProduct(t, m2); got != "free" {
		t.Fatalf("unreachable engine: product = %q, want the remembered free", got)
	}
}

// Overlapping writers (two background reads) leave every account in the file,
// the newest value last, and no temp file behind.
func TestConcurrentPlanWritesKeepEveryAccount(t *testing.T) {
	m := namesManager(t)
	ids := []string{"accountalpha0001", "accountbravo0002", "accountcharl0003", "accountdelta0004"}
	var wg sync.WaitGroup
	for round := 0; round < 10; round++ {
		plan := "premium"
		if round%2 == 1 {
			plan = "free"
		}
		for _, id := range ids {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				m.rememberAccountPlan(id, plan)
			}(id)
		}
		wg.Wait()
	}
	m2 := namesManager(t)
	m2.credStore = m.credStore
	for _, id := range ids {
		m2.noteEngineAccount(id)
		// The last round wrote "free" for everyone.
		if got := m2.productPlan(); got != "free" {
			t.Errorf("%s: plan on disk = %q, want free", id, got)
		}
	}
	if _, err := os.Stat(filepath.Join(m.credStore, accountPlansFile+".tmp")); !os.IsNotExist(err) {
		t.Errorf("the temporary file was left behind: %v", err)
	}
}

// The household's other account must never be judged by this one's plan.
func TestADifferentAccountDoesNotInheritThePlan(t *testing.T) {
	eng := &planEngine{user: "accountalpha0001"}
	var product atomic.Value
	product.Store("free")
	var limited atomic.Bool
	var calls atomic.Int32
	spot := spotifyAnswering(t, &product, &limited, &calls)
	m := productTestManager(t, eng.serve(t).URL)

	if got := m.accountProductAt(context.Background(), spot.URL); got != "free" {
		t.Fatalf("first read = %q, want free", got)
	}

	engineRestart(m)
	eng.setUser("accountbravo0002")
	limited.Store(true)
	if got := m.accountProductAt(context.Background(), spot.URL); got != "" {
		t.Fatalf("the other account got plan %q, want unknown", got)
	}
	if got := infoProduct(t, m); got != "" {
		t.Fatalf("/spotify/info product = %q for the other account, want absent", got)
	}
	noBackgroundRead(m)
	if m.PremiumRequired() {
		t.Error("the other account was gated by the first account's free plan")
	}

	// Before the new process has said who it is, nothing applies either.
	engineRestart(m)
	if got := m.productPlan(); got != "" {
		t.Fatalf("plan before the account is known = %q, want unknown", got)
	}
}

// A fresh read always wins over what was remembered, and replaces it.
func TestAFreshReadOverwritesTheRememberedPlan(t *testing.T) {
	eng := &planEngine{user: "accountalpha0001"}
	var product atomic.Value
	product.Store("free")
	var limited atomic.Bool
	var calls atomic.Int32
	spot := spotifyAnswering(t, &product, &limited, &calls)
	m := productTestManager(t, eng.serve(t).URL)

	_ = m.accountProductAt(context.Background(), spot.URL)
	engineRestart(m)
	product.Store("premium") // the account was upgraded
	if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
		t.Fatalf("fresh read = %q, want premium", got)
	}

	// And the store now says so, for the next restart.
	engineRestart(m)
	limited.Store(true)
	if got := m.accountProductAt(context.Background(), spot.URL); got != "premium" {
		t.Fatalf("after the upgrade the remembered plan = %q, want premium", got)
	}
	noBackgroundRead(m)
	if m.PremiumRequired() {
		t.Error("an upgraded account is still gated by its old plan")
	}
}

// The store is on NAND: the same answer must not rewrite it.
func TestThePlanFileIsOnlyRewrittenOnChange(t *testing.T) {
	m := namesManager(t)
	path := filepath.Join(m.credStore, accountPlansFile)

	m.rememberAccountPlan("accountalpha0001", "premium")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("nothing was written: %v", err)
	}
	// Mark the file so a rewrite is visible without relying on timestamps.
	const marker = `{"accountalpha0001":{"product":"premium","checkedAt":"2026-10-01T00:00:00Z"}}` + "\n"
	if err := os.WriteFile(path, []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		m.rememberAccountPlan("accountalpha0001", "premium")
		m.rememberAccountPlan("accountalpha0001", " Premium ")
	}
	if b, _ := os.ReadFile(path); string(b) != marker {
		t.Fatalf("an unchanged plan rewrote the file: %s", b)
	}

	m.rememberAccountPlan("accountalpha0001", "free")
	if b, _ := os.ReadFile(path); string(b) == marker {
		t.Fatal("a changed plan was not written")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("the temporary file was left behind: %v", err)
	}
}

// The store the plans live in is also where LoggedIn and CanRecall look for a
// credential copy. A labels-only store (a name and a plan, no credential left)
// must not read as a speaker that can still play a Spotify key.
func TestNamesAndPlansAreNotCredentials(t *testing.T) {
	m := productTestManager(t, "http://127.0.0.1:1")
	m.credStore = filepath.Join(t.TempDir(), "sp-accounts")
	m.rememberAccountName("accountalpha0001", "Alex")
	m.rememberAccountPlan("accountalpha0001", "premium")
	if m.LoggedIn() {
		t.Error("a store holding only names and plans counted as logged in")
	}
	if m.hasUsableCredential() {
		t.Error("a store holding only names and plans counted as a usable credential")
	}
}

// A torn or missing store is the state before this existed, never an error.
func TestACorruptOrMissingPlanFileIsHarmless(t *testing.T) {
	for name, content := range map[string]string{
		"missing": "",
		"corrupt": `{"accountalpha0001":{"product":"prem`,
		"wrong":   `["premium"]`,
		"junk":    `{"accountalpha0001":{"product":"not a plan at all"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			m := namesManager(t)
			if content != "" {
				if err := os.WriteFile(filepath.Join(m.credStore, accountPlansFile), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			m.noteEngineAccount("accountalpha0001")
			if got := m.productPlan(); got != "" {
				t.Fatalf("plan = %q from a %s store, want unknown", got, name)
			}
			// And it can still learn.
			m.rememberAccountPlan("accountalpha0001", "premium")
			if got := m.productPlan(); got != "premium" {
				t.Fatalf("plan after learning = %q, want premium", got)
			}
		})
	}
}
