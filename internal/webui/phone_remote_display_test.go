package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone page offers the display switches the desktop app has under Speaker
// settings, Sound & display (#1309, #1123), against the same agent endpoints.
func TestPhoneRemoteDisplayCardUsesTheAgentEndpoints(t *testing.T) {
	if !strings.Contains(indexHTML, `id="dispCard" hidden`) {
		t.Fatal("the display card is gone, or it no longer starts hidden (a speaker without a display must never show it)")
	}
	for _, path := range []string{"/api/box/display-track", "/api/box/display-clock",
		"/api/box/display-messages", "/api/box/display-notices", "/api/box/display-splash"} {
		if !strings.Contains(indexHTML, "'"+path+"'") {
			t.Errorf("the display card no longer talks to %s", path)
		}
	}
	for _, kind := range []string{"track", "clock", "messages", "notices", "splash"} {
		if !strings.Contains(indexHTML, `data-ds="`+kind+`"`) {
			t.Errorf("no switch for %s on the display card", kind)
		}
	}
}

// Each row shows only where the speaker says it can do it, and the track row
// only on a panel STR draws on itself: elsewhere every title change re-buffers
// the stream, and the desktop app warns before switching that on.
func TestPhoneRemoteDisplayRowsFollowWhatTheSpeakerCanDo(t *testing.T) {
	for _, want := range []string{
		"dt.onPanel === true",
		"dc.supported === true",
		"dm.hasDisplay === true",
		"dn.supported === true",
		"dsp.supported === true",
		"card.hidden = !any",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("display card: %q is gone", want)
		}
	}
	if !strings.Contains(indexHTML, "if (name === 'More') loadDisplayCard();") {
		t.Error("the display card is not re-read when the More tab opens")
	}
}

func TestPhoneRemoteLocalesCarryTheDisplayKeys(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	for _, key := range []string{"dsHead", "dsSum", "dsTrack", "dsArtist", "dsTitle", "dsBoth",
		"dsClock", "dsMsg", "dsNotices", "dsSplash", "dsSaved", "dsFail"} {
		got := len(regexp.MustCompile(`[{,]`+key+`:"`).FindAllString(indexHTML, -1))
		if got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
}
