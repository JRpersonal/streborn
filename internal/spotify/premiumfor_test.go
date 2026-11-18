package spotify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeAccountCopy stores a per-account credential copy, the file a
// cross-account recall swaps in (captureCredential writes the real one).
func writeAccountCopy(t *testing.T, m *Manager, user string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(m.credStore, 0o755); err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(storedCredential{Username: user, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.credStore, sanitizeUser(user)+".json"), blob, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Discussion #1947: the engine sat on an account that had lapsed to free, and
// every Spotify key on the speaker was refused with "the account on this
// speaker is a free account", including the keys a Premium account had saved.
// Those keys switch to their own account before they play, so the logged-in
// account's plan must not refuse them. The lapsed account's own keys still must.
func TestPremiumGateJudgesTheKeysOwnAccount(t *testing.T) {
	const lapsed, premium, noCopy, emptyCopy = "lapsedaccount01", "premiumaccount2", "nocopyaccount03", "emptycopyacct04"
	eng := &productEngine{user: lapsed, product: "free"}
	m := productTestManager(t, eng.serve(t).URL)
	// Name known, so the plan read is the engine's report alone.
	m.rememberAccountName(lapsed, "Lapsed")
	if got := m.accountProductAt(context.Background(), "http://127.0.0.1:1/unused"); got != "free" {
		t.Fatalf("plan = %q, want the engine's free", got)
	}
	noBackgroundRead(m)
	writeAccountCopy(t, m, lapsed, []byte("lapsed-login"))
	writeAccountCopy(t, m, premium, []byte("premium-login"))
	writeAccountCopy(t, m, emptyCopy, nil)

	if !m.PremiumRequired() {
		t.Fatal("the free account logged in now does not gate at all")
	}
	cases := []struct {
		name, account string
		want          bool
	}{
		{"a key of the free account logged in now", lapsed, true},
		{"a key with surrounding blanks is the same account", "  " + lapsed + " ", true},
		{"a key saved before keys carried an account", "", true},
		{"a key another account saved, with a login to switch to", premium, false},
		{"a key of an account with no stored login plays with the current one", noCopy, true},
		{"a key of an account whose stored login is empty cannot switch either", emptyCopy, true},
	}
	for _, c := range cases {
		if got := m.PremiumRequiredFor(c.account); got != c.want {
			t.Errorf("%s: PremiumRequiredFor = %v, want %v", c.name, got, c.want)
		}
	}
}

// Right after an engine start the plan state names nobody until /status has
// been read, while the free-account log line can already have latched. The
// gate then takes the account from the login the engine started from, so a
// Premium account's key still passes and the free account's own key does not.
func TestPremiumGateBeforeTheEngineHasSaidWhoIsLoggedIn(t *testing.T) {
	const lapsed, premium = "lapsedaccount01", "premiumaccount2"
	m := productTestManager(t, "http://127.0.0.1:1")
	if err := m.writeActiveCredential(storedCredential{Username: lapsed, Data: []byte("lapsed-login")}); err != nil {
		t.Fatal(err)
	}
	writeAccountCopy(t, m, premium, []byte("premium-login"))
	m.mu.Lock()
	m.sawFreeAccountLog = true
	m.mu.Unlock()
	noBackgroundRead(m)

	if !m.PremiumRequiredFor(lapsed) {
		t.Error("the free account's own key passed the gate")
	}
	if m.PremiumRequiredFor(premium) {
		t.Error("a Premium account's key was refused for the logged-in free account")
	}
}

// The gate lets a key past because the recall will switch; the switch must
// read the stored login the same way, or the key would play with the free
// account the gate just looked past.
func TestReadAccountCredentialMatchesWhatTheSwitchUses(t *testing.T) {
	m := productTestManager(t, "http://127.0.0.1:1")
	if _, found, _ := m.readAccountCredential("nobodyhere"); found {
		t.Error("a missing copy reported as found")
	}
	writeAccountCopy(t, m, "someaccount", []byte("login"))
	cred, found, err := m.readAccountCredential("someaccount")
	if !found || err != nil || cred.Username != "someaccount" || string(cred.Data) != "login" {
		t.Fatalf("read = %+v found=%v err=%v", cred, found, err)
	}
	if err := os.WriteFile(filepath.Join(m.credStore, "brokenaccount.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := m.readAccountCredential("brokenaccount"); !found || err == nil {
		t.Errorf("a corrupt copy: found=%v err=%v, want found with an error", found, err)
	}
}
