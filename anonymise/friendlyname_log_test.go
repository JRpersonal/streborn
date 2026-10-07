package anonymise

import (
	"encoding/json"
	"strings"
	"testing"
)

// The agent's network-name log lines carried the room name under generic keys
// (name=, old=, new=) and an anonymised bundle shipped them in clear.
func TestScrubPII_FriendlyNameChangeLines(t *testing.T) {
	cases := []string{
		`time=2026-10-07T10:00:00Z level=INFO msg="mDNS FriendlyName updated" name=Kitchen`,
		`time=2026-10-07T10:00:00Z level=INFO msg="mDNS FriendlyName updated" name="Living Room"`,
		`time=2026-10-07T10:00:00Z level=WARN msg="mDNS phase: re-announce trigger" reason="friendlyName change" old=Kitchen new="Living Room"`,
		`time=2026-10-07T10:00:00Z level=WARN msg="mDNS phase: re-announce trigger" reason="box info change" old="model=SoundTouch 10 friendlyName=Kitchen" new="model=SoundTouch 10 friendlyName=Living Room"`,
	}
	for _, in := range cases {
		out := ScrubPII(in)
		for _, leak := range []string{"Kitchen", "Living Room"} {
			if strings.Contains(out, leak) {
				t.Errorf("speaker name %q survived:\n in: %s\nout: %s", leak, in, out)
			}
		}
		if !strings.Contains(out, "NAME#") {
			t.Errorf("no pseudonym in %s", out)
		}
	}
}

// The same name must map to the same pseudonym as in a friendlyName= line, so a
// reader can still follow one speaker across the bundle.
func TestScrubPII_FriendlyNameChangeSamePseudonym(t *testing.T) {
	want := "NAME#" + hashShort("Kitchen")
	out := ScrubPII(`msg="mDNS FriendlyName updated" name=Kitchen`)
	if !strings.Contains(out, "name="+want) {
		t.Fatalf("want name=%s, got %s", want, out)
	}
	out = ScrubPII(`msg="x" friendlyName=Kitchen`)
	if !strings.Contains(out, want) {
		t.Fatalf("friendlyName= line hashed differently: %s", out)
	}
}

// old=/new= outside a friendly-name line carry models, states and ids that a
// bundle is read for; they must survive.
func TestScrubPII_FriendlyNameChangeLeavesOtherOldNewAlone(t *testing.T) {
	in := `level=WARN msg="mDNS phase: re-announce trigger" reason="model change" old=SoundTouch new=Portable`
	if out := ScrubPII(in); out != in {
		t.Fatalf("model change line altered:\n in: %s\nout: %s", in, out)
	}
	in = `level=INFO msg="state change" old=STANDBY new=PLAYING name=preset`
	if out := ScrubPII(in); out != in {
		t.Fatalf("unrelated line altered:\n in: %s\nout: %s", in, out)
	}
	// An empty old= (first announce) stays empty.
	out := ScrubPII(`msg="mDNS phase: re-announce trigger" reason="friendlyName change" old="" new=Kitchen`)
	if !strings.Contains(out, `old=""`) || strings.Contains(out, "Kitchen") {
		t.Fatalf("empty old= or name mishandled: %s", out)
	}
}

// A log tail embedded in a JSON string keeps its quotes escaped; the scrub must
// hash the name there too and leave the document valid JSON.
func TestScrubPII_FriendlyNameChangeInsideJSON(t *testing.T) {
	logText := "level=INFO msg=\"mDNS FriendlyName updated\" name=\"Living Room\"\n" +
		"level=WARN msg=\"mDNS phase: re-announce trigger\" reason=\"friendlyName change\" old=Kitchen new=\"Living Room\"\n"
	b, err := json.Marshal(map[string]string{"agentLog": logText})
	if err != nil {
		t.Fatal(err)
	}
	out := ScrubPII(string(b))
	var back map[string]string
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("scrub broke the JSON: %v\n%s", err, out)
	}
	for _, leak := range []string{"Kitchen", "Living Room"} {
		if strings.Contains(back["agentLog"], leak) {
			t.Errorf("speaker name %q survived inside JSON: %s", leak, back["agentLog"])
		}
	}
}
