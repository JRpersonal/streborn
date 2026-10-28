package spotify

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The rule in one table: an explicit choice wins, Premium defaults to 320,
// every other known plan and an unknown plan to 160.
func TestEffectiveBitrate(t *testing.T) {
	cases := []struct {
		choice     int
		plan       string
		wantKbps   int
		wantReason string
	}{
		{bitrateAuto, "premium", 320, bitrateReasonPremium},
		{bitrateAuto, "free", 160, bitrateReasonFree},
		{bitrateAuto, "open", 160, bitrateReasonFree},
		{bitrateAuto, "", 160, bitrateReasonUnknown},
		{160, "premium", 160, bitrateReasonChosen},
		{96, "premium", 96, bitrateReasonChosen},
		{320, "free", 320, bitrateReasonChosen},
		{320, "", 320, bitrateReasonChosen},
	}
	for _, c := range cases {
		k, r := effectiveBitrate(c.choice, c.plan)
		if k != c.wantKbps || r != c.wantReason {
			t.Errorf("effectiveBitrate(%d, %q) = %d %q, want %d %q", c.choice, c.plan, k, r, c.wantKbps, c.wantReason)
		}
	}
}

// Files written by an agent before this change carry no marker. Their 160 can
// only be the "back to the default" step of the old two-option selector, so it
// reads as automatic; their 96 and 320 can only have been chosen.
func TestLoadBitrateChoiceLegacyFiles(t *testing.T) {
	cases := []struct {
		content string
		marker  bool
		want    int
	}{
		{"160\n", false, bitrateAuto},
		{"320\n", false, 320},
		{"96\n", false, 96},
		{"160\n", true, 160},
		{"320\n", true, 320},
	}
	for _, c := range cases {
		cfg := filepath.Join(t.TempDir(), "cfg")
		if err := os.WriteFile(bitratePath(cfg), []byte(c.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if c.marker {
			if err := os.WriteFile(bitrateChoiceMarkerPath(cfg), []byte("explicit\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := loadBitrateChoice(cfg); got != c.want {
			t.Errorf("file %q marker=%v: choice %d, want %d", strings.TrimSpace(c.content), c.marker, got, c.want)
		}
	}
}

// writeLogin makes state.json name user as the account the engine will load.
func writeLogin(t *testing.T, cfg, user string) {
	t.Helper()
	blob, _ := json.Marshal(map[string]any{"credentials": storedCredential{Username: user, Data: []byte("x")}})
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "state.json"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writePlan stores a remembered plan for user, as accountplans.go would.
func writePlan(t *testing.T, cfg, user, plan string) {
	t.Helper()
	store := filepath.Join(filepath.Dir(cfg), "sp-accounts")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(map[string]rememberedPlan{user: {Product: plan, CheckedAt: time.Now()}})
	if err := os.WriteFile(filepath.Join(store, accountPlansFile), blob, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newPlanManager(t *testing.T, cfg string) (*Manager, *syncBuf) {
	t.Helper()
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	buf := &syncBuf{}
	return New("", cfg, "", nil, slog.New(slog.NewTextHandler(buf, nil))), buf
}

func bitrState(m *Manager) (kbps, choice int, reason string, pending bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bitr, m.bitrChoice, m.bitrReason, m.bitrPending
}

// A speaker whose account is remembered as Premium starts at 320 without
// anybody choosing it, and the engine start says so in the log.
func TestPremiumRememberedPlanDefaultsTo320(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "cfg")
	writeLogin(t, cfg, "user-a")
	writePlan(t, cfg, "user-a", "premium")
	m, logs := newPlanManager(t, cfg)
	if kbps, choice, reason, _ := bitrState(m); kbps != 320 || choice != bitrateAuto || reason != bitrateReasonPremium {
		t.Fatalf("new manager = %d choice=%d %q, want 320 automatic premium-default", kbps, choice, reason)
	}
	m.prepareBitrateForStart()
	if !strings.Contains(logs.String(), "engine streaming quality") ||
		!strings.Contains(logs.String(), "kbps=320") || !strings.Contains(logs.String(), "reason=premium-default") {
		t.Errorf("engine-start line missing or wrong:\n%s", logs.String())
	}
}

// Free and unknown plans keep the old 160, and the plan of ANOTHER account is
// never borrowed.
func TestFreeOrUnknownPlanStaysAt160(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "cfg")
	writeLogin(t, cfg, "user-a")
	writePlan(t, cfg, "user-a", "free")
	m, _ := newPlanManager(t, cfg)
	if kbps, _, reason, _ := bitrState(m); kbps != 160 || reason != bitrateReasonFree {
		t.Errorf("free: %d %q, want 160 free-default", kbps, reason)
	}

	cfg2 := filepath.Join(t.TempDir(), "cfg")
	writeLogin(t, cfg2, "user-b")
	writePlan(t, cfg2, "user-a", "premium")
	m2, _ := newPlanManager(t, cfg2)
	if kbps, _, reason, _ := bitrState(m2); kbps != 160 || reason != bitrateReasonUnknown {
		t.Errorf("other account's plan: %d %q, want 160 plan-unknown", kbps, reason)
	}
}

// An explicit 160 survives a Premium plan, through a fresh Manager too.
func TestExplicitChoiceBeatsPremiumDefault(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "cfg")
	writeLogin(t, cfg, "user-a")
	writePlan(t, cfg, "user-a", "premium")
	m, _ := newPlanManager(t, cfg)
	if _, err := m.SetBitrate(160); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bitrateChoiceMarkerPath(cfg)); err != nil {
		t.Fatalf("explicit choice wrote no marker: %v", err)
	}
	m2, _ := newPlanManager(t, cfg)
	m2.prepareBitrateForStart()
	if kbps, choice, reason, _ := bitrState(m2); kbps != 160 || choice != 160 || reason != bitrateReasonChosen {
		t.Errorf("after restart: %d choice=%d %q, want explicit 160", kbps, choice, reason)
	}
	raw, _ := os.ReadFile(filepath.Join(cfg, "config.yml"))
	if strings.Contains(string(raw), "bitrate: 320") {
		t.Errorf("config.yml went to 320 despite the explicit 160:\n%s", raw)
	}

	// Back to automatic: both files go, and the default (320) applies.
	if _, err := m2.SetBitrate(bitrateAuto); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bitratePath(cfg)); !os.IsNotExist(err) {
		t.Error("automatic left sp-bitrate.txt behind")
	}
	if _, err := os.Stat(bitrateChoiceMarkerPath(cfg)); !os.IsNotExist(err) {
		t.Error("automatic left the marker behind")
	}
	if kbps, choice, reason, _ := bitrState(m2); kbps != 320 || choice != bitrateAuto || reason != bitrateReasonPremium {
		t.Errorf("automatic: %d choice=%d %q, want 320 premium-default", kbps, choice, reason)
	}
}

// A legacy 160 from an older agent is the default now: a Premium speaker that
// once went 320 -> 160 in the old two-option selector streams 320.
func TestLegacy160UpgradesToPremiumDefault(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "cfg")
	writeLogin(t, cfg, "user-a")
	writePlan(t, cfg, "user-a", "premium")
	if err := os.WriteFile(bitratePath(cfg), []byte("160\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := newPlanManager(t, cfg)
	if kbps, choice, _, _ := bitrState(m); kbps != 320 || choice != bitrateAuto {
		t.Errorf("legacy 160 on Premium: %d choice=%d, want 320 automatic", kbps, choice)
	}
}

// Choosing what already runs records the choice and does NOT restart.
func TestExplicitChoiceEqualToDefaultDoesNotRestart(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "cfg")
	writeLogin(t, cfg, "user-a")
	writePlan(t, cfg, "user-a", "premium")
	m, _ := newPlanManager(t, cfg)
	restarts := 0
	m.mu.Lock()
	m.runCancel = func() { restarts++ }
	m.mu.Unlock()
	applied, err := m.SetBitrate(320)
	if err != nil || !applied {
		t.Fatalf("SetBitrate(320) = %v, %v", applied, err)
	}
	if restarts != 0 {
		t.Errorf("engine restarted %d times for an unchanged bitrate", restarts)
	}
	if _, choice, reason, _ := bitrState(m); choice != 320 || reason != bitrateReasonChosen {
		t.Errorf("choice=%d %q, want explicit 320", choice, reason)
	}
}

// A plan that arrives after the start (fresh install, first check) moves the
// default, but only as an owed restart: never right away, even when idle, and
// never twice for the same account and target.
func TestPlanArrivalDefersRestartAndNeverLoops(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "cfg")
	writeLogin(t, cfg, "user-a")
	m, logs := newPlanManager(t, cfg)
	m.prepareBitrateForStart()
	if kbps, _, reason, _ := bitrState(m); kbps != 160 || reason != bitrateReasonUnknown {
		t.Fatalf("start without a plan: %d %q, want 160 plan-unknown", kbps, reason)
	}
	restarts := 0
	m.mu.Lock()
	m.runCancel = func() { restarts++ }
	gen := m.productGen
	m.mu.Unlock()

	m.noteEngineAccount("user-a")
	if !m.takeEnginePlan("user-a", "premium", gen) {
		t.Fatal("engine plan not taken")
	}
	kbps, _, reason, pending := bitrState(m)
	if kbps != 320 || reason != bitrateReasonPremium || !pending {
		t.Fatalf("after the plan: %d %q pending=%v, want 320 premium-default pending", kbps, reason, pending)
	}
	if restarts != 0 {
		t.Errorf("plan arrival restarted the engine immediately (%d)", restarts)
	}
	raw, _ := os.ReadFile(filepath.Join(cfg, "config.yml"))
	if !strings.Contains(string(raw), "bitrate: 320") {
		t.Errorf("config.yml not rewritten for the owed restart:\n%s", raw)
	}

	// The owed restart: the next start reads the now-remembered plan and
	// agrees, so nothing is owed afterwards and the fresh read is a no-op.
	m.prepareBitrateForStart()
	if kbps, _, _, pending := bitrState(m); kbps != 320 || pending {
		t.Fatalf("after the restart: %d pending=%v, want 320 settled", kbps, pending)
	}
	m.reconcilePlanBitrate()
	if _, _, _, pending := bitrState(m); pending {
		t.Error("a matching plan re-armed a restart")
	}

	// A start that cannot see the plan (forced back to 160 here) must not
	// get a second owed restart for the same account and target.
	m.mu.Lock()
	m.bitr = 160
	m.mu.Unlock()
	m.reconcilePlanBitrate()
	if _, _, _, pending := bitrState(m); pending {
		t.Error("plan-driven restart owed twice for the same account and target: loop risk")
	}
	if n := strings.Count(logs.String(), "the account plan changes the default streaming quality"); n != 1 {
		t.Errorf("plan-change line logged %d times, want 1", n)
	}
}

// An explicit choice is never touched by a plan answer.
func TestPlanArrivalLeavesExplicitChoiceAlone(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "cfg")
	writeLogin(t, cfg, "user-a")
	m, _ := newPlanManager(t, cfg)
	if _, err := m.SetBitrate(160); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	gen := m.productGen
	m.mu.Unlock()
	m.noteEngineAccount("user-a")
	m.takeEnginePlan("user-a", "premium", gen)
	if kbps, _, reason, pending := bitrState(m); kbps != 160 || reason != bitrateReasonChosen || pending {
		t.Errorf("explicit 160 after a Premium answer: %d %q pending=%v", kbps, reason, pending)
	}
}

// A start at a different bitrate than the previous process drops the header
// cache; a start at the same one keeps it.
func TestStartAtNewBitrateDropsHeaderCache(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "cfg")
	m, _ := newPlanManager(t, cfg)
	m.mu.Lock()
	m.headerPages = []byte("hdr")
	m.mu.Unlock()
	m.prepareBitrateForStart()
	m.mu.Lock()
	kept := len(m.headerPages) > 0
	m.mu.Unlock()
	if !kept {
		t.Error("same-bitrate start dropped the header cache")
	}
	writeLogin(t, cfg, "user-a")
	writePlan(t, cfg, "user-a", "premium")
	m.mu.Lock()
	m.accountPlans = nil // re-read the store written above
	m.mu.Unlock()
	m.prepareBitrateForStart()
	m.mu.Lock()
	dropped := len(m.headerPages) == 0
	started := m.bitrStarted
	m.mu.Unlock()
	if !dropped || started != 320 {
		t.Errorf("start at 320 after 160: headers dropped=%v started=%d", dropped, started)
	}
}

// The endpoint reports the choice and the reason, and takes 0 for automatic.
func TestServeQualityReportsChoiceAndReason(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "cfg")
	writeLogin(t, cfg, "user-a")
	writePlan(t, cfg, "user-a", "premium")
	m, _ := newPlanManager(t, cfg)

	w := httptest.NewRecorder()
	m.ServeQuality(w, httptest.NewRequest("GET", "/spotify/quality", nil))
	body := w.Body.String()
	for _, want := range []string{`"bitrate":320`, `"choice":0`, `"reason":"premium-default"`} {
		if !strings.Contains(body, want) {
			t.Errorf("GET lacks %s: %s", want, body)
		}
	}

	w = httptest.NewRecorder()
	m.ServeQuality(w, httptest.NewRequest("POST", "/spotify/quality", strings.NewReader(`{"bitrate":160}`)))
	if !strings.Contains(w.Body.String(), `"choice":160`) || !strings.Contains(w.Body.String(), `"reason":"chosen"`) {
		t.Errorf("POST 160 = %s", w.Body.String())
	}

	w = httptest.NewRecorder()
	m.ServeQuality(w, httptest.NewRequest("POST", "/spotify/quality", strings.NewReader(`{"bitrate":0}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"bitrate":320`) || !strings.Contains(w.Body.String(), `"choice":0`) {
		t.Errorf("POST 0 = %d %s", w.Code, w.Body.String())
	}
}

// A plan-driven change already written to config.yml waits for a detach. When
// the user then picks that same quality while nothing plays, the restart is
// paid at once instead of after a whole play-then-stop cycle.
func TestSetBitrateIdlePaysAWaitingPlanChange(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "cfg")
	m, logs := newPlanManager(t, cfg)
	restarted := 0
	m.mu.Lock()
	m.bitr, m.bitrReason, m.bitrPending = 320, bitrateReasonPremium, true
	m.runCancel = func() { restarted++ }
	m.mu.Unlock()
	applied, err := m.SetBitrate(320)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, pending := bitrState(m); !applied || pending || restarted != 1 {
		t.Fatalf("applied=%v pending=%v restarts=%d, want true false 1\n%s", applied, pending, restarted, logs.String())
	}
}
