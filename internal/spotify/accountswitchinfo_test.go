package spotify

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

type refusalReply struct {
	PremiumRequired    bool   `json:"premiumRequired"`
	AudioKeyRefused    bool   `json:"audioKeyRefused"`
	AccountRefused     bool   `json:"accountRefused"`
	AccountRefusedName string `json:"accountRefusedName"`
	Account            string `json:"account"`
}

func (r refusalReply) any() bool {
	return r.PremiumRequired || r.AudioKeyRefused || r.AccountRefused || r.AccountRefusedName != ""
}

func refusalInfo(t *testing.T, m *Manager) refusalReply {
	t.Helper()
	noBackgroundRead(m)
	rr := httptest.NewRecorder()
	m.ServeInfo(rr, httptest.NewRequest("GET", "/spotify/info", nil))
	var got refusalReply
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	return got
}

// freeRefusedManager is a speaker whose engine is logged in as "u", a free
// account Spotify has refused: every refusal field of /spotify/info is set.
func freeRefusedManager(t *testing.T) *Manager {
	t.Helper()
	m, _, cleanup := mockLibrespot(t)
	t.Cleanup(cleanup)
	m.rememberAccountName("u", "Alex")
	m.markAccountRefused("u")
	m.mu.Lock()
	m.sawFreeAccountLog = true
	m.engineKeyDeniedAt = time.Now()
	m.mu.Unlock()
	got := refusalInfo(t, m)
	if !got.PremiumRequired || !got.AudioKeyRefused || !got.AccountRefused || got.AccountRefusedName != "Alex" {
		t.Fatalf("setup: the refused free account is not reported as such: %+v", got)
	}
	return m
}

// Discussion #1947: a Premium account's key was recalled while the engine sat
// on a lapsed free account. For the eight seconds of the account switch the
// reply still carried the free account's refusal, and the app showed its
// "free account" notice over a key that was about to play.
func TestSpotifyInfoDropsTheDepartingAccountsRefusalDuringASwitch(t *testing.T) {
	m := freeRefusedManager(t)

	m.mu.Lock()
	m.recallRestartAt = time.Now()
	m.mu.Unlock()
	if got := refusalInfo(t, m); got.any() {
		t.Fatalf("the account being switched away from is still reported as refused: %+v", got)
	}

	// The switch is over and the engine is still on the refused account (the
	// switch was overridden, or timed out): the notice is true again.
	m.mu.Lock()
	m.recallRestartAt = time.Now().Add(-11 * time.Second)
	m.mu.Unlock()
	if got := refusalInfo(t, m); !got.PremiumRequired || !got.AccountRefused || !got.AudioKeyRefused {
		t.Fatalf("the refusal did not come back after the switch window: %+v", got)
	}
}

// Between the press and the restart nothing marks the switch yet. A recall in
// flight over an account that gates as free can only be another account's key
// (the gate refuses the free account's own keys before a recall is marked), so
// the reply already stops describing the free account there.
func TestSpotifyInfoDropsTheFreeAccountsRefusalWhileARecallIsInFlight(t *testing.T) {
	t.Run("recall window", func(t *testing.T) {
		m := freeRefusedManager(t)
		m.mu.Lock()
		m.recallUntil = time.Now().Add(5 * time.Second)
		m.mu.Unlock()
		if got := refusalInfo(t, m); got.any() {
			t.Fatalf("refusal reported during the recall: %+v", got)
		}
		m.mu.Lock()
		m.recallUntil = time.Now().Add(-time.Second)
		m.mu.Unlock()
		if got := refusalInfo(t, m); !got.PremiumRequired || !got.AccountRefused {
			t.Fatalf("the refusal did not come back after the recall: %+v", got)
		}
	})
	t.Run("display gate", func(t *testing.T) {
		m := freeRefusedManager(t)
		m.mu.Lock()
		m.armRecallDisplayLocked("spotify:playlist:NEW", time.Now())
		m.mu.Unlock()
		if got := refusalInfo(t, m); got.any() {
			t.Fatalf("refusal reported while the recall's display gate is pending: %+v", got)
		}
	})
}

// A recall on the account that stays logged in is not a switch. A Premium
// account Spotify refuses on this engine keeps its notice through its own
// recalls; hiding it there would only make it flicker.
func TestSpotifyInfoKeepsTheRefusalOfTheAccountThatStays(t *testing.T) {
	m, _, cleanup := mockLibrespot(t)
	defer cleanup()
	m.rememberAccountName("u", "Alex")
	m.markAccountRefused("u")
	m.mu.Lock()
	m.recallUntil = time.Now().Add(5 * time.Second)
	m.armRecallDisplayLocked("spotify:playlist:NEW", time.Now())
	m.mu.Unlock()

	got := refusalInfo(t, m)
	if got.PremiumRequired {
		t.Fatalf("setup: the account gates as free: %+v", got)
	}
	if !got.AccountRefused || got.AccountRefusedName != "Alex" {
		t.Fatalf("a same-account recall hid the account's own refusal: %+v", got)
	}
}
