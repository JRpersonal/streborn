package streamproxy

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// #1394: switching from a radio station to a music library track showed the
// station's last song under the track for a few seconds on the phone remote.
// The end-of-stream wipe waits titleEndGrace for a takeover, and in that window
// /api/stream/title still answered with the radio song. ForgetTitle is what the
// agent calls the moment a library file play is accepted.

func TestForgetTitleEmptiesTheServedTitleAtOnce(t *testing.T) {
	s := newTitleServer()
	gen := s.noteStreamStart("http://radio/a")
	s.setTitle("http://radio/a", "Chairmen Of The Board - Give Me Just A Little More Time")

	s.ForgetTitle()

	if got := s.CurrentTitle(); got != "" {
		t.Fatalf("CurrentTitle after ForgetTitle = %q, want empty", got)
	}
	rec := httptest.NewRecorder()
	s.handleTitle(rec, httptest.NewRequest("GET", "/api/stream/title", nil))
	if body := rec.Body.String(); !strings.Contains(body, `"title":""`) {
		t.Fatalf("/api/stream/title after ForgetTitle = %s, want an empty title", body)
	}

	// The delayed end-of-stream wipe still recognises its stream, so the panel
	// is still told the radio stream ended.
	ended := false
	s.SetOnTitleEnd(func() { ended = true })
	s.wipeTitleIfUnclaimed("http://radio/a", gen)
	if !ended {
		t.Fatal("end-of-stream callback did not fire after ForgetTitle")
	}
}

func TestForgetTitleThenStationReturnFillsTheTitleAgain(t *testing.T) {
	s := newTitleServer()
	s.noteStreamStart("http://radio/a")
	s.setTitle("http://radio/a", "Old Song")
	s.ForgetTitle()
	// The box goes back to the same station: its next metadata block counts.
	var pushed []string
	s.SetOnTitle(func(t string) { pushed = append(pushed, t) })
	s.setTitle("http://radio/a", "Old Song")
	if got := s.CurrentTitle(); got != "Old Song" {
		t.Fatalf("title after the station came back = %q, want %q", got, "Old Song")
	}
	if len(pushed) != 1 {
		t.Fatalf("title pushes after the station came back = %v, want one", pushed)
	}
}
