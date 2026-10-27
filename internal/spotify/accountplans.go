package spotify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/anonymise"
)

// Remembering what plan a Spotify account is ON, so a speaker that already knew
// does not forget it every time the engine restarts.
//
// The plan comes from the engine's /status (product_type, see engineAccount)
// or, on an engine without that field, from Spotify's /v1/me with a token the
// engine hands out. It used to live only in memory: every engine start (an account switch, a
// device-name change, a crash restart) wiped it, and the speaker had to ask
// again. Spotify rate-limits that shared client hard. A field bundle from
// 2026-10-26 (three ST10s, a Premium account) shows "spotify answered 429 ...
// retry after 47s" and the escalating quiet window reaching two hours, so the
// plan stayed unknown for hours although the speaker had read it successfully
// the evening before. Saving a Spotify key then said "STR could not check the
// Spotify plan on this speaker just now", which was true and useless.
//
// A plan changes about as often as somebody changes their subscription, so the
// last answer Spotify gave for an account is a far better guess than "unknown".
// It is kept per account, beside the display names (accountnames.go) and for
// the same reason: the household's other account must never be judged by this
// one's plan. A fresh read always wins and refreshes the stored value.
//
// Semantics, chosen deliberately: a remembered plan feeds the "product" the
// apps are told, and nothing else. PremiumRequired, which blocks preset recall,
// keeps its conservative contract: true only on a FRESH positive non-Premium
// signal (a /v1/me answer or an engine-reported product_type from this engine
// process, or the engine's own free-account log line), never on a value carried over from an earlier
// process. A remembered "free" can be months old; an account upgraded since
// would otherwise have its presets blocked for as long as Spotify throttles the
// re-read, which is up to productRateLimitMax per window and survives reboots.
// Showing a stale plan in the UI costs a wrong label; blocking a Premium user's
// presets costs the feature. The background re-read keeps running while only
// the remembered plan is known, so a fresh answer replaces it as soon as
// Spotify gives one.

// accountPlansFile sits beside names.json in the per-account store, not in the
// engine's config dir: it is a fact about the same accounts, and the store is
// the part of the Spotify state that outlives the engine being removed and
// reinstalled to make room for an agent update.
const accountPlansFile = "plans.json"

// maxPlanLen bounds a value that arrives from the network. Spotify's plans are
// one short word ("premium", "free", "open").
const maxPlanLen = 32

// rememberedPlan is one account's last confirmed plan. CheckedAt is when that
// VALUE was first confirmed, not when it was last re-read: the file is only
// rewritten when the plan changes, so a later identical answer leaves it alone.
type rememberedPlan struct {
	Product   string    `json:"product"`
	CheckedAt time.Time `json:"checkedAt"`
}

func (m *Manager) accountPlansPath() string {
	return filepath.Join(m.credStore, accountPlansFile)
}

// loadAccountPlansLocked reads the store once. A missing, unreadable or corrupt
// file means "nothing remembered", never an error: the speaker then behaves
// exactly as it did before this file existed and asks Spotify. Caller holds m.mu.
func (m *Manager) loadAccountPlansLocked() {
	if m.accountPlans != nil {
		return
	}
	m.accountPlans = map[string]rememberedPlan{}
	data, err := os.ReadFile(m.accountPlansPath())
	if err != nil {
		return
	}
	var got map[string]rememberedPlan
	if json.Unmarshal(data, &got) != nil {
		return
	}
	for id, p := range got {
		id = strings.TrimSpace(id)
		if p.Product = cleanPlan(p.Product); id == "" || p.Product == "" {
			continue
		}
		m.accountPlans[id] = p
	}
}

// cleanPlan normalises a plan to the one-word form PremiumRequired compares
// against. Returns "" for anything that is not a plan.
func cleanPlan(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" || len(v) > maxPlanLen || strings.ContainsAny(v, " \t\r\n") {
		return ""
	}
	return v
}

// rememberAccountPlan records the plan Spotify just confirmed for an account,
// and writes to the NAND only when that is news. Same rule as the names: on
// hardware whose flash is the part that wears out, the guard is the reason the
// file is allowed to exist, not an optimisation. A re-read every few minutes
// with the same answer costs nothing.
func (m *Manager) rememberAccountPlan(id, product string) {
	id = strings.TrimSpace(id)
	product = cleanPlan(product)
	if id == "" || product == "" {
		return
	}
	// Held across the snapshot AND the write, so two overlapping background
	// reads can neither rename each other's temp file away nor let an older
	// snapshot be the last one renamed into place.
	m.storeWriteMu.Lock()
	defer m.storeWriteMu.Unlock()
	m.mu.Lock()
	m.loadAccountPlansLocked()
	if m.accountPlans[id].Product == product {
		m.mu.Unlock()
		return
	}
	m.accountPlans[id] = rememberedPlan{Product: product, CheckedAt: time.Now().UTC()}
	snapshot := make(map[string]rememberedPlan, len(m.accountPlans))
	for k, v := range m.accountPlans {
		snapshot[k] = v
	}
	m.mu.Unlock()

	if err := os.MkdirAll(m.credStore, 0o755); err != nil {
		return
	}
	blob, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	// Through a synced temporary file, so a power cut mid-write leaves the
	// previous store rather than a torn one that loadAccountPlansLocked would
	// discard whole.
	if writeFileAtomic(m.accountPlansPath(), blob, 0o644) != nil {
		return
	}
	m.logger.Info("spotify: remembered the account plan", "plan", product, "user", anonymise.MaskAccount(id))
}

// noteEngineAccount records which account the engine is logged in as, so the
// non-blocking readers (PremiumRequired, ServeInfo) can pick that account's
// remembered plan without asking the engine themselves. Callers already have
// the answer in hand from a /status read they make anyway, and call this only
// when the engine actually ANSWERED (engineUsername's ok): an unreachable engine
// says nothing about who is logged in. An empty user is a real answer (logged
// out, credential refused) and clears the account, so no remembered plan
// applies to a speaker nobody is logged into. Leaving an account also drops the
// fresh plan read for it: that answer was about the previous login.
func (m *Manager) noteEngineAccount(user string) {
	user = strings.TrimSpace(user)
	m.mu.Lock()
	if m.productUser != user {
		if m.productUser != "" {
			m.productType, m.productCheckedAt = "", time.Time{}
		}
		m.productUser = user
		// A different account is a different question: the line saying a
		// remembered plan was used has to be able to appear for it too.
		m.productRememberedLogged = false
	}
	m.mu.Unlock()
}

// resetProductStateLocked is what every engine start does to the plan state:
// a fresh process re-detects the account plan (an account switch relaunches
// go-librespot), and forgets the account until the new process says who it is
// logged in as, so a switch can never hand one account's plan to the next.
// productQuietUntil is deliberately kept: Spotify's throttle is on the shared
// client, not on the engine process. Bumping productGen makes a read that
// started under the old process drop its answer. Caller holds m.mu.
func (m *Manager) resetProductStateLocked() {
	m.productType, m.sawFreeAccountLog, m.productTriedAt = "", false, time.Time{}
	m.productUser, m.productFreshLogged, m.productRememberedLogged = "", false, false
	m.productNameAsked = ""
	m.productGen++
}

// productPlan returns the plan to act on: the fresh read when there is one,
// otherwise the plan remembered for the account the engine is logged in as.
// "" when neither is known. The remembered plan is looked up by the CURRENT
// account only, so another account's plan is never inherited.
func (m *Manager) productPlan() string {
	m.mu.Lock()
	p := m.productType
	if p != "" || m.productUser == "" {
		m.mu.Unlock()
		return p
	}
	m.loadAccountPlansLocked()
	rp, ok := m.accountPlans[m.productUser]
	user := m.productUser
	first := ok && !m.productRememberedLogged
	if first {
		m.productRememberedLogged = true
	}
	m.mu.Unlock()
	if !ok {
		return ""
	}
	if first {
		// Once per account per engine process, so a bundle can tell a plan
		// Spotify just confirmed from one carried over from earlier.
		m.logger.Info("spotify: using the account plan remembered from an earlier check",
			"plan", rp.Product, "checkedAt", rp.CheckedAt.Format(time.RFC3339), "user", anonymise.MaskAccount(user))
	}
	return rp.Product
}
