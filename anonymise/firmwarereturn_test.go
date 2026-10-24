package anonymise

import (
	"strings"
	"testing"
)

// The speaker firmware's native Spotify library traces every API call with its
// result. One of those results is the owner's canonical Spotify user id, and
// that line reached box_syslog_tail in a bundle exported with anonymize on,
// because no pass recognised the "[returned value: ...]" shape.
func TestAFirmwareGetterThatReturnsAnAccountIsMasked(t *testing.T) {
	const id = "user-name-here"
	cases := []string{
		`Oct 24 10:00:00 spotty local0.info BoseApp[812]: SpotifyLibrary 1234 INFO SpGetCanonicalUsername [returned value: ` + id + `]`,
		`SpotifyLibrary SpGetCanonicalUsername() [returned value: "` + id + `"]`,
		`SpotifyLibrary SpGetUsername [returned value: ` + id + `]`,
		`SpotifyLibrary SpGetDisplayUserName [returned value: ` + id + `]`,
		`PandoraLibrary GetAccountEmail [returned value: ` + id + `]`,
		`DeezerLibrary getUserId [returned value: ` + id + `]`,
		`SpotifyLibrary SpGetCanonicalUsername [returned value: ` + id,
	}
	for _, line := range cases {
		got := ScrubPII(line)
		if strings.Contains(got, id) {
			t.Errorf("the account survived\n  in:  %s\n  out: %s", line, got)
		}
		if !strings.Contains(got, "ACCT#") {
			t.Errorf("nothing was masked\n  out: %s", got)
		}
		// The call name is what the tail is read for and has to stay.
		if !strings.Contains(got, "returned value") {
			t.Errorf("the trace shape was lost: %s", got)
		}
		if again := ScrubPII(got); again != got {
			t.Errorf("not idempotent\n  1st: %s\n  2nd: %s", got, again)
		}
	}
}

// The same id under the attribute names used for a canonical user name.
func TestACanonicalUsernameAttributeIsMasked(t *testing.T) {
	const id = "user-name-here"
	for _, line := range []string{
		`spotify session canonical_username=` + id + ` country=DE`,
		`{"canonicalUsername":"` + id + `"}`,
		`canonicalUsername: ` + id,
	} {
		got := ScrubPII(line)
		if strings.Contains(got, id) {
			t.Errorf("the account survived\n  in:  %s\n  out: %s", line, got)
		}
	}
}

// Every other traced call keeps its result: volumes, states and bitrates are
// the reason the tail is captured. And a getter that found no account says so
// in clear, because "nothing linked" is the answer a reader is looking for.
func TestOtherFirmwareReturnValuesStayReadable(t *testing.T) {
	for _, line := range []string{
		`SpotifyLibrary SpGetVolume [returned value: 42]`,
		`SpotifyLibrary SpPlaybackIsActiveDevice [returned value: 1]`,
		`SpotifyLibrary SpGetCanonicalUsername [returned value: (null)]`,
		`SpotifyLibrary SpGetCanonicalUsername [returned value: ]`,
	} {
		if got := ScrubPII(line); got != line {
			t.Errorf("a non-identity result was changed\n  in:  %s\n  out: %s", line, got)
		}
	}
}

// The path a bundle takes: box_syslog_tail is a []string section, so it reaches
// DebugState as []any after the JSON round-trip.
func TestTheSyslogTailSectionMasksTheFirmwareAccount(t *testing.T) {
	const id = "user-name-here"
	state := map[string]any{
		"box_syslog_tail": []any{
			`BoseApp: SpotifyLibrary SpGetVolume [returned value: 42]`,
			`BoseApp: SpotifyLibrary SpGetCanonicalUsername [returned value: ` + id + `]`,
		},
	}
	out := DebugState(state)
	tail := out["box_syslog_tail"].([]any)
	if strings.Contains(tail[1].(string), id) {
		t.Errorf("the account survived in box_syslog_tail: %s", tail[1])
	}
	if tail[0] != `BoseApp: SpotifyLibrary SpGetVolume [returned value: 42]` {
		t.Errorf("an unrelated line changed: %s", tail[0])
	}
}
