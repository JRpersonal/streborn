package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
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
