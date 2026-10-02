package anonymise

import (
	"strings"
	"testing"
)

// A Spotify account identity must not survive anonymisation under ANY of the
// attribute names the code emits it under.
//
// This is written from the emitters, not from the regex: the regex is the thing
// under test, and a test derived from it would have passed on 2026-10-02 while
// a public bundle carried the owner's canonical Spotify user id in clear. The
// names below come from `grep '"user",'` and `grep '"wantAccount"'` over the
// tree, and the point of the test is to fail the day a new one is added without
// the mask being told about it.
func TestSpotifyAccountIdentityIsMaskedUnderEveryAttributeNameWeEmit(t *testing.T) {
	// Shaped like a real canonical Spotify user id: 25 lower-case characters,
	// no separators, which is what LooksLikeAccountIdentity has to recognise.
	const id = "zq4xm7p2vb9nkd6rt1ys8hgwj"

	// Each case is a log line this repository actually produces.
	cases := []struct {
		where string
		line  string
	}{
		{"internal/spotify/accounts.go captured account credential",
			`msg="spotify: captured account credential" comp=spotify user=` + id},
		{"internal/spotify/accounts.go capture credential failed",
			`msg="spotify: capture credential failed" comp=spotify user=` + id + ` err="x"`},
		{"internal/spotify/accounts.go switched account",
			`msg="spotify: switched account" comp=spotify user=` + id + ` tookMs=12`},
		{"internal/spotify/recall.go recall start",
			`msg="spotify: recall start" uri=spotify:album:x wantAccount=` + id + ` sessionUser=` + id + ` loggedIn=true`},
		{"the names that were already covered, so a widening cannot break them",
			`msg="x" username=` + id + ` account=` + id + ` userId=` + id},
	}

	for _, c := range cases {
		got := scrubIdentities(scrubPII(c.line))
		if strings.Contains(got, id) {
			t.Errorf("the account identity survived (%s)\n  in:  %s\n  out: %s", c.where, c.line, got)
		}
		if !strings.Contains(got, "ACCT#") {
			t.Errorf("nothing was masked at all (%s)\n  out: %s", c.where, got)
		}
	}
}

// A socket label is not an identity, and masking it would cost the bundle the
// thing it is read for. This is why `account` is judged rather than masked
// outright, and the widening above must not have changed that.
func TestASocketLabelOnAccountStaysReadable(t *testing.T) {
	for _, label := range []string{"AUX", "TV", "CBL-Sat"} {
		line := `msg="source changed" account=` + label
		got := scrubIdentities(scrubPII(line))
		if !strings.Contains(got, label) {
			t.Errorf("the socket label %q was masked away: %s", label, got)
		}
	}
}
