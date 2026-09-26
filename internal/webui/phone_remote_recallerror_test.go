package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone remote used to swallow the one refusal that has a cure (#973).
//
// A Spotify recall the agent knows cannot work is answered here in
// recallgate.go with 422, a stable code and a sentence that says what to do.
// The desktop app has branched on that code since #45. The phone remote called
// it through api(), which collapses every non-2xx to null, so playSlot saw
// nothing but failure and showed "Could not start / tap again" - in the
// now-playing line, which the 5 s status poll then overwrote. The reporter
// described exactly that: the key highlights, un-highlights, and no error
// message appears, while the speaker's own log carried the reason four times.
func TestPhoneRemoteShowsWhyARecallWasRefused(t *testing.T) {
	if !strings.Contains(indexHTML, "async function apiDetail(") {
		t.Fatal("apiDetail is gone; playSlot cannot read the body of a failed answer")
	}
	if !strings.Contains(indexHTML, "await apiDetail('/api/play/' + n, 'POST')") {
		t.Fatal("playSlot must use apiDetail, or the refusal body is thrown away again")
	}
	// api() itself must keep collapsing failures: every other caller here
	// relies on that shape.
	if !strings.Contains(indexHTML, "if (!r.ok) { console.error(path, r.status); return null; }") {
		t.Fatal("api() changed shape; the other callers expect null on failure")
	}
	for _, want := range []string{
		"function playRefusal(",
		"spotify-not-logged-in",
		"T.spNeedPick",
		"T.spNeedPremium",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("the refusal branch lost %q", want)
		}
	}
	// The message must land somewhere the status poll cannot paint over.
	if !strings.Contains(indexHTML, "function showPlayNotice(") {
		t.Fatal("no notice card; the message would go back into the now-playing line")
	}
	if !strings.Contains(indexHTML, "showPlayNotice(why)") {
		t.Fatal("playSlot does not route the reason to the notice card")
	}
}

// Every phone locale carries the three new strings.
func TestPhoneRemoteLocalesCarryTheRecallRefusal(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	for _, key := range []string{"spNeedPick", "spNeedPremium", "closeW"} {
		got := len(regexp.MustCompile(key+`:"`).FindAllString(indexHTML, -1))
		if got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
}
