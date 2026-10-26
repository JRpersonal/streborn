package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone page used api() for every play, and api() throws the body of a
// failed answer away, so even the agent's 409 for a stereo follower reached the
// user as "Could not start, tap again". These pin the page side of the fix.

// No play path may go back to api(): its null cannot carry the refusal.
func TestPhonePlayPathsReadTheRefusal(t *testing.T) {
	page := readIndexHTML(t)
	if m := regexp.MustCompile(`api\('/api/play'\s*,\s*'POST'`).FindString(page); m != "" {
		t.Errorf("a play still goes through api(), which drops the refusal body: %s", m)
	}
	if m := regexp.MustCompile(`api\('/api/queue'\s*,\s*'POST'`).FindString(page); m != "" {
		t.Errorf("a folder play still goes through api(), which drops the refusal body: %s", m)
	}
	if !strings.Contains(page, "postPlay('/api/queue/replay-card'") {
		t.Error("the folder-card replay does not go through postPlay")
	}
	if !strings.Contains(page, "isPairFollowerRefusal(res.data)") {
		t.Error("a preset key on a stereo follower does not show the pair notice")
	}
	if m := regexp.MustCompile(`api\('/api/pandora/play'`).FindString(page); m != "" {
		t.Errorf("a Pandora play still goes through api(), which drops the refusal body: %s", m)
	}
	if !strings.Contains(page, "postPlay('/api/resume')") {
		t.Error("the resume button does not go through postPlay")
	}
	// The folder-card replay showed the bare "stereo-incomplete" code.
	if strings.Contains(page, "showPlayNotice(r.data.error ||") {
		t.Error("the folder-card replay shows the agent's raw error field")
	}
}

// Half of a broken pair gets a sentence, not the bare refusal code.
func TestPairIncompleteHasItsOwnMessageEverywhere(t *testing.T) {
	page := readIndexHTML(t)
	start := strings.Index(page, "var I18N2 = {")
	if start < 0 {
		t.Fatal("I18N2 dictionary not found")
	}
	end := strings.Index(page[start:], "\n};")
	for _, line := range strings.Split(page[start:start+end], "\n") {
		m := regexp.MustCompile(`^  ([a-z]{2}):\{`).FindStringSubmatch(line)
		if m != nil && !strings.Contains(line, "pairBroken:") {
			t.Errorf("language %q has no pairBroken", m[1])
		}
	}
	if !strings.Contains(page, "if (isPairIncompleteRefusal(detail)) return T.pairBroken;") {
		t.Error("playRefusal does not map stereo-incomplete to its message")
	}
}

// Every language the page speaks gets the message and the link text.
func TestPairFollowerNoticeIsTranslatedEverywhere(t *testing.T) {
	page := readIndexHTML(t)
	start := strings.Index(page, "var I18N2 = {")
	if start < 0 {
		t.Fatal("I18N2 dictionary not found")
	}
	end := strings.Index(page[start:], "\n};")
	block := page[start : start+end]
	langs := regexp.MustCompile(`(?m)^  ([a-z]{2}):\{`).FindAllStringSubmatch(block, -1)
	if len(langs) < 10 {
		t.Fatalf("found %d language blocks, expected the full set", len(langs))
	}
	for _, line := range strings.Split(block, "\n") {
		m := regexp.MustCompile(`^  ([a-z]{2}):\{`).FindStringSubmatch(line)
		if m == nil {
			continue
		}
		for _, key := range []string{"pairFollowPlay:", "pairOpenMaster:"} {
			if !strings.Contains(line, key) {
				t.Errorf("language %q has no %s", m[1], strings.TrimSuffix(key, ":"))
			}
		}
		if !strings.Contains(line, "{name}") {
			t.Errorf("language %q does not name the leading speaker", m[1])
		}
	}
}
