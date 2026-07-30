package spotify

import (
	"strings"
	"testing"
	"time"
)

// A key request that failed because the connection to Spotify was gone must not
// be reported as a missing Premium subscription. The engine authenticates and
// its session comes up a moment later, so the first press right after it starts
// can hit a closed connection while the same press works seconds later (#512).
func TestPlayDenialHintSeparatesAClosedConnectionFromADenial(t *testing.T) {
	cases := []struct {
		name        string
		line        string
		wantPremium bool
	}{
		{
			name:        "session not up yet",
			line:        `failed creating stream for spotify:track:x: failed retrieving audio key: accesspoint closed`,
			wantPremium: false,
		},
		{
			name:        "connection reset while fetching the key",
			line:        `failed retrieving audio key: connection reset by peer`,
			wantPremium: false,
		},
		{
			name:        "a real denial still blames the subscription",
			line:        `failed retrieving audio key: denied`,
			wantPremium: true,
		},
		{
			name:        "the aes wording is still a denial",
			line:        `error handling aes key request: rejected`,
			wantPremium: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &Manager{}
			m.lastPlayFailLine = c.line
			m.lastPlayFailAt = time.Now()

			got := m.playDenialHint()
			if got == "" {
				t.Fatal("an audio-key failure must always produce a hint")
			}
			isPremium := strings.Contains(got, "Premium")
			if isPremium != c.wantPremium {
				t.Fatalf("premium blame = %v, want %v\nhint: %s", isPremium, c.wantPremium, got)
			}
			if !c.wantPremium && !strings.Contains(got, "again") {
				t.Fatalf("a not-ready connection must tell the user to try again, got: %s", got)
			}
		})
	}
}
