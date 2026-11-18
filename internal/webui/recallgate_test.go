package webui

import (
	"context"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// Discussion #1947: the Premium gate is asked for the key's OWN account. The
// engine decides whether that account's key would play under a free account
// (a key of another account the speaker can switch to is not judged by the
// logged-in one); the gate must hand it the account, not ask about the speaker.
func TestRecallPremiumGateAsksForTheKeysAccount(t *testing.T) {
	s, _ := newPlayTestServer(t)
	s.spotifyPlay = func(context.Context, string, string, bool, bool) error { return nil }
	s.spotifyCanRecall = func(context.Context) bool { return true }
	var asked []string
	s.spotifyPremiumRequired = func(account string) (bool, string) {
		asked = append(asked, account)
		return account == "lapsedaccount01", "key-account-is-logged-in (test)"
	}

	lapsed := presets.Preset{Slot: 1, Name: "Lapsed key", Type: "spotify", URI: "spotify:playlist:a", Account: "lapsedaccount01"}
	if ref := s.recallRefusal(1, &lapsed); ref == nil {
		t.Fatal("the lapsed account's key was not refused")
	}
	premium := presets.Preset{Slot: 5, Name: "Premium key", Type: "spotify", URI: "spotify:playlist:b", Account: "premiumaccount2"}
	if ref := s.recallRefusal(5, &premium); ref != nil {
		t.Fatalf("a Premium account's key was refused after the lapsed one: %+v", ref)
	}
	if len(asked) != 2 || asked[0] != "lapsedaccount01" || asked[1] != "premiumaccount2" {
		t.Fatalf("gate asked for %q, want each key's own account", asked)
	}
}
