package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func boolp(b bool) *bool { return &b }

// The speaker's MUTE key is a TOGGLE. Pressing it on a speaker that is already
// where the caller wants it moves it the other way, which is the one thing a
// "set" must never do, so the decision is tested exhaustively.
func TestMuteDecision(t *testing.T) {
	for _, c := range []struct {
		name       string
		current    bool
		want       *bool
		wantTarget bool
		wantPress  bool
	}{
		{"no body on an unmuted speaker toggles it quiet", false, nil, true, true},
		{"no body on a muted speaker brings it back", true, nil, false, true},
		{"mute an unmuted speaker", false, boolp(true), true, true},
		{"mute a speaker already muted presses nothing", true, boolp(true), true, false},
		{"unmute a muted speaker", true, boolp(false), false, true},
		{"unmute a speaker already playing presses nothing", false, boolp(false), false, false},
	} {
		target, press := muteDecision(c.current, c.want)
		if target != c.wantTarget || press != c.wantPress {
			t.Errorf("%s: got target=%v press=%v, want target=%v press=%v",
				c.name, target, press, c.wantTarget, c.wantPress)
		}
	}
}

// false has to be distinguishable from "not given", or an unmute request reads
// as a toggle and silences a speaker that was already playing.
func TestExplicitFalseIsNotTheSameAsNoBody(t *testing.T) {
	_, pressNoBody := muteDecision(false, nil)
	_, pressFalse := muteDecision(false, boolp(false))
	if !pressNoBody {
		t.Error("an empty body did not toggle")
	}
	if pressFalse {
		t.Error("an explicit unmute on an unmuted speaker pressed the key, muting it")
	}
}

func TestMuteRefusesTheWrongMethod(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10"}
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rr := httptest.NewRecorder()
		s.handleBoxMute(rr, httptest.NewRequest(m, "/api/box/mute", nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s answered %d, want 405", m, rr.Code)
		}
	}
}

// #1748: /api/status carries the mute flag the speaker's frames reported, so
// the phone remote's 5 s poll can follow a mute set in the app or with the
// remote, without a /volume read per poll. Unknown means no header at all.
func TestStatusCarriesTheMuteFlag(t *testing.T) {
	body := `<nowPlaying source="LOCAL_INTERNET_RADIO"><ContentItem location="http://radio.example/stream"></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>`
	s := &Server{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		boxHost:    "127.0.0.1:1",
		statusBody: []byte(body),
		statusCode: http.StatusOK,
		statusAt:   time.Now(),
	}
	if got := pollStatus(s).Header().Values(mutedHeader); len(got) != 0 {
		t.Fatalf("no frame yet: want no %s, got %v", mutedHeader, got)
	}
	s.NoteBoxMute(true)
	w := pollStatus(s)
	if got := w.Header().Get(mutedHeader); got != "1" {
		t.Errorf("muted: %s = %q, want 1", mutedHeader, got)
	}
	if w.Body.String() != body {
		t.Errorf("the body must stay the speaker's own XML, got %q", w.Body.String())
	}
	s.NoteBoxMute(false)
	if got := pollStatus(s).Header().Get(mutedHeader); got != "0" {
		t.Errorf("unmuted: %s = %q, want 0", mutedHeader, got)
	}
	// A bus reconnect may have missed a change: say nothing rather than the
	// old state.
	s.ForgetBoxMute()
	if got := pollStatus(s).Header().Values(mutedHeader); len(got) != 0 {
		t.Errorf("after a reconnect: want no %s, got %v", mutedHeader, got)
	}
}

// The flag is the bus's, not the body's: a stale cached body (the speaker's
// now_playing unreachable) still carries the newest flag.
func TestStatusMuteFlagOnAStaleBody(t *testing.T) {
	s := &Server{
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		boxHost:    "127.0.0.1:1",
		statusBody: []byte(`<nowPlaying source="STANDBY"></nowPlaying>`),
		statusCode: http.StatusOK,
		statusAt:   time.Now().Add(-statusCacheTTL - time.Second),
	}
	s.NoteBoxMute(true)
	if got := pollStatus(s).Header().Get(mutedHeader); got != "1" {
		t.Errorf("%s = %q on the fallback body, want 1", mutedHeader, got)
	}
}
