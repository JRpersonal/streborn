package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The phone remote is the self-contained indexHTML page the agent serves on "/".
// These tests guard the client-side behaviour reported in #294 and #295, which
// lives only as embedded JS and so has no other automated coverage.

// TestPhoneRemoteDecodesNowPlayingEntities guards #295: a now_playing title the
// box serves entity-encoded (e.g. New York&apos;s) must be decoded before it is
// re-escaped for display, otherwise the leading & is doubled and the remote
// shows a literal "&apos;". The fix adds decodeEntities and runs the captured
// itemName/track through it.
func TestPhoneRemoteDecodesNowPlayingEntities(t *testing.T) {
	if !strings.Contains(indexHTML, "function decodeEntities(") {
		t.Fatal("indexHTML is missing the decodeEntities helper (#295)")
	}
	// The captured now-playing name must be decoded, not used raw.
	if !strings.Contains(indexHTML, "const name = m ? decodeEntities(m[1]) : '';") {
		t.Fatal("indexHTML must decode entities on the now_playing name before display (#295)")
	}
	if strings.Contains(indexHTML, "const name = m ? m[1] : '';") {
		t.Fatal("indexHTML still uses the raw, un-decoded now_playing name (#295 regression)")
	}
}

// TestPhoneRemotePlayPauseToggle guards #294: the single Pause button must double
// as Play/Pause so a stream paused from the remote can be resumed from the remote
// (via the existing /api/resume endpoint) instead of only from the app or the
// physical Bose remote.
func TestPhoneRemotePlayPauseToggle(t *testing.T) {
	if !strings.Contains(indexHTML, "onclick=\"togglePlayPause(this)\"") {
		t.Fatal("the Pause button must call togglePlayPause (#294)")
	}
	if !strings.Contains(indexHTML, "async function togglePlayPause(") {
		t.Fatal("indexHTML is missing the togglePlayPause function (#294)")
	}
	if !strings.Contains(indexHTML, "'/api/resume'") {
		t.Fatal("togglePlayPause must resume via /api/resume when paused (#294)")
	}
	if !strings.Contains(indexHTML, "function applyTransportUI(") {
		t.Fatal("indexHTML is missing applyTransportUI to reflect the paused state (#294)")
	}
	// The old, resume-less wiring must be gone.
	if strings.Contains(indexHTML, "pp(this,'/api/pause')") {
		t.Fatal("the Pause button still hard-wires /api/pause with no resume path (#294 regression)")
	}
}

// TestPhoneRemoteLocalesHavePlayLabel guards that the new Play/Resume button
// label is translated for every locale bundle, not left to fall through to the
// English "Play". Each bundle carries exactly one now:"..." and must carry one
// play:"..." beside it.
func TestPhoneRemoteLocalesHavePlayLabel(t *testing.T) {
	nowCount := strings.Count(indexHTML, "now:\"")
	// play appears once per bundle, and once as the applyTransportUI reference
	// (T.play). Count only the bundle keys via the play:" object-key form.
	playKeys := regexp.MustCompile(`play:"`).FindAllString(indexHTML, -1)
	if nowCount == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	if len(playKeys) != nowCount {
		t.Fatalf("expected one play label per locale bundle: %d bundles but %d play keys", nowCount, len(playKeys))
	}
}
