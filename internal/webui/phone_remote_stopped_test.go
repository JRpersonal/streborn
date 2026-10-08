package webui

import (
	"strings"
	"testing"
)

// A stopped speaker was "Idle" on the phone page and "ready" in the desktop
// app (#1034). Both now say "Stopped"; the phone page has no idle word left.
func TestPhoneRemoteCallsAStoppedSpeakerStopped(t *testing.T) {
	if strings.Contains(indexHTML, "T.idle") || strings.Contains(indexHTML, ",idle:\"") {
		t.Error("the phone page still has an idle word for a stopped speaker")
	}
	if !strings.Contains(indexHTML, "INVALID_PLAY_STATUS:T.stopped") {
		t.Error("a speaker with no transport must read as stopped")
	}
	if !strings.Contains(indexHTML, "(upSrc === 'STANDBY' ? T.standby : T.stopped)") {
		t.Error("with nothing named, the big line must say Standby or Stopped")
	}
	if !strings.Contains(indexHTML, `en:{langAuto:"Automatic"`) || !strings.Contains(indexHTML, `stopped:"Stopped"`) || !strings.Contains(indexHTML, `stopped:"Gestoppt"`) {
		t.Error("the English and German stopped words changed; keep them in step with status.stopped in the desktop bundles")
	}
}
