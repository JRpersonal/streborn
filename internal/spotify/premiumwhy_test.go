package spotify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/anonymise"
)

// Discussion #1947, second round: the gate refused a Premium account's key and
// the log could not say why it had judged it by the logged-in account. The
// verdict now comes with the case that decided, the logged-in account in the
// masked form the other log lines use, and the plan values it read.
func TestPremiumGateSaysWhosePlanDecided(t *testing.T) {
	const lapsed, premium, noCopy, emptyCopy = "lapsedaccount01", "premiumaccount2", "nocopyaccount03", "emptycopyacct04"
	eng := &productEngine{user: lapsed, product: "free"}
	m := productTestManager(t, eng.serve(t).URL)
	m.rememberAccountName(lapsed, "Lapsed")
	if got := m.accountProductAt(context.Background(), "http://127.0.0.1:1/unused"); got != "free" {
		t.Fatalf("plan = %q, want the engine's free", got)
	}
	noBackgroundRead(m)
	writeAccountCopy(t, m, lapsed, []byte("lapsed-login"))
	writeAccountCopy(t, m, premium, []byte("premium-login"))
	writeAccountCopy(t, m, emptyCopy, nil)
	m.mu.Lock()
	m.loadAccountPlansLocked()
	m.accountPlans[noCopy] = rememberedPlan{Product: "premium", CheckedAt: time.Now()}
	m.mu.Unlock()

	cases := []struct {
		account, wantCase string
		want              bool
	}{
		{"", premiumWhyNoAccount, true},
		{lapsed, premiumWhySameAcct, true},
		{premium, premiumWhySwitches, false},
		{noCopy, premiumWhyNoCred, true},
		{emptyCopy, premiumWhyBadCred, true},
	}
	for _, c := range cases {
		required, why := m.PremiumRequiredForWhy(c.account)
		if required != c.want || required != m.PremiumRequiredFor(c.account) {
			t.Errorf("%q: required = %v, want %v and the same as PremiumRequiredFor", c.account, required, c.want)
		}
		if !strings.HasPrefix(why, c.wantCase+" (") {
			t.Errorf("%q: why = %q, want the case %q", c.account, why, c.wantCase)
		}
		if !strings.Contains(why, "logged in: "+anonymise.MaskAccount(lapsed)) ||
			!strings.Contains(why, "its plan read in this engine run: free") {
			t.Errorf("%q: why does not carry the logged-in account and its plan: %q", c.account, why)
		}
		for _, raw := range []string{lapsed, premium, noCopy, emptyCopy} {
			if strings.Contains(why, raw) {
				t.Errorf("%q: why carries the raw account %q: %q", c.account, raw, why)
			}
		}
		// The bundle scrubber must leave the value as it is: a part of it taken
		// for an account would be hashed and the evidence would be gone.
		if got := anonymise.ScrubPII(why); got != why {
			t.Errorf("%q: the scrubber rewrote why:\n got %q\nwant %q", c.account, got, why)
		}
	}
	// The case this was built for: the key's account is Premium as far as the
	// speaker remembers, and it still falls to the free account logged in.
	if _, why := m.PremiumRequiredForWhy(noCopy); !strings.Contains(why, "the key account's remembered plan: premium") {
		t.Errorf("why does not carry the plan remembered for the key's account: %q", why)
	}
}
