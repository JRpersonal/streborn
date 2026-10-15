package anonymise

import (
	"encoding/json"
	"strings"
	"testing"
)

// The Spotify engine names the other device in its own message text, and the
// agent forwards that line verbatim. An anonymised bundle on a public issue
// (#1314) carried "playback was transferred to <the owner's laptop name>" in
// clear. The cases are the engine's real format strings, wrapped the way the
// agent's slog forwarder wraps them.
func TestScrubPII_SpotifyPeerDeviceNames(t *testing.T) {
	const name = "Kim’s MacBook Pro"
	wrap := func(msg string) string {
		return `time=2026-10-15T08:26:37.269-05:00 level=INFO msg=go-librespot comp=spotify line="time=\"2026-10-15T08:26:37-05:00\" level=info msg=\"` + msg + `\""`
	}
	cases := []struct {
		what string
		line string
	}{
		{"transfer, forwarded", wrap("playback was transferred to " + name)},
		{"zeroconf accepted, with a trailing field", wrap("accepted zeroconf from "+name) + ` username=ACCT#12345678`},
		{"zeroconf refused", wrap("refused zeroconf from " + name)},
		{"session failure", wrap("failed creating new session from " + name)},
		{"bare engine line, plain quotes", `level=info msg="playback was transferred to ` + name + `"`},
		{"line end, no quote", `playback was transferred to ` + name},
	}
	for _, c := range cases {
		got := ScrubPII(c.line)
		for _, leak := range []string{"Kim", "MacBook"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s: %q survived\n in:  %s\n out: %s", c.what, leak, c.line, got)
			}
		}
		if !strings.Contains(got, "DEVICE#") {
			t.Errorf("%s: no pseudonym in %s", c.what, got)
		}
	}
}

// The same name always gets the same pseudonym, so a bundle still shows that
// two transfers went to the same device.
func TestScrubPII_SpotifyPeerDeviceIsStable(t *testing.T) {
	a := ScrubPII(`msg="playback was transferred to Living Room Phone"`)
	b := ScrubPII(`msg="accepted zeroconf from Living Room Phone"`)
	pa := a[strings.Index(a, "DEVICE#"):]
	pb := b[strings.Index(b, "DEVICE#"):]
	if pa != pb {
		t.Errorf("same device, different pseudonyms: %s vs %s", pa, pb)
	}
}

// The engine's placeholder for a nameless device names nobody and says
// something useful (the transfer came from an unidentified client).
func TestScrubPII_SpotifyUnknownPeerStaysReadable(t *testing.T) {
	in := `msg="playback was transferred to <unknown>"`
	if got := ScrubPII(in); got != in {
		t.Errorf("the placeholder was changed: %s", got)
	}
}

// A bundle file carries these lines inside JSON strings. Scrubbing the
// encoded text must leave valid JSON whose decoded value lost the name,
// including a name with an ampersand that the encoder escapes.
func TestScrubPII_SpotifyPeerDeviceInsideEncodedJSON(t *testing.T) {
	for _, name := range []string{"Kim’s MacBook Pro", "Kim & Sam iPad"} {
		log := "line one\n" + `level=INFO msg=go-librespot line="level=info msg=\"playback was transferred to ` + name + `\""` + "\nline three"
		raw, err := json.Marshal(map[string]string{"agent_log": log})
		if err != nil {
			t.Fatal(err)
		}
		out := ScrubPII(string(raw))
		var back map[string]string
		if err := json.Unmarshal([]byte(out), &back); err != nil {
			t.Fatalf("scrubbed JSON no longer parses (%v):\n%s", err, out)
		}
		got := back["agent_log"]
		if strings.Contains(got, "Kim") || strings.Contains(got, "Sam") {
			t.Errorf("the name survived inside encoded JSON: %s", got)
		}
		if !strings.Contains(got, "line three") || !strings.Contains(got, "DEVICE#") {
			t.Errorf("the scrub cut more than the name: %s", got)
		}
	}
}
