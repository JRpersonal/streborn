package anonymise

import (
	"encoding/json"
	"strings"
	"testing"
)

// #1314: the Spotify engine logs its own, partly obfuscated account id
// (first and last two letters kept) as username="31****ui", and the agent
// forwards the whole engine line as a quoted slog value, so the attribute
// arrives as username=\"31****ui\". The mask used to stop at the backslash:
// it hashed the backslash alone and left the id's ends standing, with the
// escape broken (username=ACCT#xxxxxxxx"31****ui\").
func TestScrubPII_EscapedQuotedAccountIsMaskedWhole(t *testing.T) {
	const id = "31abcdefghijklmnopqrstuvui"
	const obf = "31************************ui"
	cases := []struct {
		name, in, wantTail string
	}{
		{"forwarded engine line",
			`level=INFO msg=go-librespot comp=spotify line="time=\"t\" level=info msg=\"authenticated AP\" username=\"` + obf + `\""`,
			`\""`},
		{"plain quoted", `msg="x" username="` + id + `" next=1`, `" next=1`},
		{"bare", `msg="x" user=` + id + ` next=1`, ` next=1`},
		{"double-escaped", `line=\"msg=\\\"x\\\" username=\\\"` + obf + `\\\"\"`, `\\\"\"`},
		{"sessionUser escaped", `line="msg=\"x\" sessionUser=\"` + id + `\" wantAccount=\"` + id + `\""`, `\""`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ScrubPII(c.in)
			for _, leak := range []string{id, obf, "31*", "*ui", `"31`} {
				if strings.Contains(got, leak) {
					t.Errorf("%q survived\n in:  %s\n out: %s", leak, c.in, got)
				}
			}
			if !strings.Contains(got, "ACCT#") {
				t.Errorf("nothing masked: %s", got)
			}
			if !strings.HasSuffix(got, c.wantTail) {
				t.Errorf("the closing quote or what follows it changed\n in:  %s\n out: %s", c.in, got)
			}
			// Quote structure is unchanged: the mask replaces a value, never a delimiter.
			if strings.Count(got, `"`) != strings.Count(c.in, `"`) || strings.Count(got, `\`) != strings.Count(c.in, `\`) {
				t.Errorf("quotes or backslashes changed\n in:  %s\n out: %s", c.in, got)
			}
		})
	}
}

// The same line inside a bundle's encoded JSON: scrubbing the raw JSON text
// must keep it valid and leave the decoded line with an intact escape.
func TestScrubPII_EscapedAccountInsideEncodedJSON(t *testing.T) {
	line := `level=INFO msg=go-librespot line="level=info msg=\"authenticated AP\" username=\"31************************ui\""`
	raw, err := json.Marshal(map[string]string{"agent_log": line + "\nnext line"})
	if err != nil {
		t.Fatal(err)
	}
	out := ScrubPII(string(raw))
	var back map[string]string
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("scrubbed JSON no longer parses (%v):\n%s", err, out)
	}
	got := back["agent_log"]
	if strings.Contains(got, "31*") || strings.Contains(got, "*ui") {
		t.Errorf("the id's ends survived: %s", got)
	}
	if !strings.Contains(got, `username=\"ACCT#`) || !strings.Contains(got, "\\\"\"\nnext line") {
		t.Errorf("the escape around the masked value is broken: %s", got)
	}
}
