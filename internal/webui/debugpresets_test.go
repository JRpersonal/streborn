package webui

import (
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/anonymise"
	"github.com/JRpersonal/streborn/internal/presets"
)

// The presets debug section names the account a Spotify key was saved under
// (#1947), hashed at the source: the raw form of the section must not carry
// the name, and the token is the one the log lines print for that account.
func TestPresetDebugLineCarriesTheKeysAccountMasked(t *testing.T) {
	const account = "premiumaccount2"
	line := presetDebugLine(presets.Preset{Slot: 5, Name: "Mix", Type: "spotify", URI: "spotify:playlist:b", Account: account})
	if strings.Contains(line, account) {
		t.Fatalf("the line carries the raw account: %s", line)
	}
	if want := `account="` + anonymise.MaskAccount(account) + `"`; !strings.Contains(line, want) {
		t.Fatalf("line = %s, want it to carry %s", line, want)
	}
	// The masked debug state runs the scrubber over every string. It must keep
	// the token (hashing it again would cut the link to the log lines) and the
	// field after it.
	scrubbed := anonymise.ScrubPII(line)
	if !strings.Contains(scrubbed, anonymise.MaskAccount(account)) || !strings.HasSuffix(scrubbed, "items=0") {
		t.Fatalf("the scrubber damaged the line:\n got %s\nfrom %s", scrubbed, line)
	}

	// A key without an account, and a radio key, say so with an empty value.
	radio := presetDebugLine(presets.Preset{Slot: 2, Name: "Station", Type: "radio", StreamURL: "http://stream.example/x"})
	if !strings.Contains(radio, `account="" items=0`) {
		t.Fatalf("line = %s, want an empty account", radio)
	}
	if got := anonymise.ScrubPII(radio); !strings.Contains(got, `account="" items=0`) {
		t.Fatalf("the scrubber damaged the empty account:\n got %s\nfrom %s", got, radio)
	}
}
