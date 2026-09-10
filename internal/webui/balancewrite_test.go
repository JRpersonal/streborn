// POST /api/box/balance, the write half of the balance endpoint.
//
// Background, because the shape of these tests follows from it: the balance was
// read-only from v0.9.35 until 2026-09-10, and the reason given was that the
// firmware accepts no write. That was measured, and it was measured only over
// HTTP. gesellix found that Bose's own client on the speaker sends it on the
// gabbo WebSocket, so the write now goes there and this endpoint is the front
// door for it.
//
// The box host is unroutable here, as everywhere else in this package, so the
// firmware reads fail and what is pinned is what the handler decides on its
// own. The one thing the handler must never do is report a write it could not
// make: a slider that snaps back is a nuisance, a slider that lies about having
// moved sends the user looking for a fault in their speakers.

package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// postBalance drives POST /api/box/balance with the given body and returns the
// decoded answer.
func postBalance(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/box/balance", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleBoxBalance(w, req)
	var out map[string]any
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad JSON: %v (%s)", err, w.Body.String())
		}
	}
	return w.Code, out
}

// An agent with no socket to write on must say so, not fail and not pretend.
// Every build without the box WebSocket lands here.
func TestBalanceWriteWithoutASocketReportsUnsupported(t *testing.T) {
	s := quietServer("192.0.2.10")
	code, out := postBalance(t, s, `{"target":-3}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if out["ok"] == true {
		t.Fatalf("a write with no socket reported success: %v", out)
	}
	if out["reason"] != "unsupported" {
		t.Fatalf("reason = %v, want unsupported", out["reason"])
	}
}

// The read answer carries settable so the app can render a slider or a read-out
// without having to know which agent version answered. It must be false when
// there is no socket, and it must never be true on a speaker that reports no
// balance at all.
func TestBalanceReadSaysWhetherItCanBeSet(t *testing.T) {
	if got := balanceWithSettable(boxapi.Balance{Available: true}, false)["settable"]; got != false {
		t.Fatalf("settable = %v with no write function, want false", got)
	}
	if got := balanceWithSettable(boxapi.Balance{Available: false}, true)["settable"]; got != false {
		t.Fatalf("settable = %v on a speaker with no pair, want false", got)
	}
	if got := balanceWithSettable(boxapi.Balance{Available: true}, true)["settable"]; got != true {
		t.Fatalf("settable = %v on a pair with a socket, want true", got)
	}
	// The read answer keeps the field names it had before the write existed.
	b := balanceWithSettable(boxapi.Balance{Available: true, Min: -7, Max: 7, Actual: -3, Target: -3}, true)
	for _, k := range []string{"available", "min", "max", "default", "target", "actual"} {
		if _, ok := b[k]; !ok {
			t.Fatalf("the read answer lost its %q field", k)
		}
	}
}

// A body without a target is a bug in the caller, not a balance of zero. Zero is
// the centre position and the most likely value anybody sends, so the two must
// not collapse into each other.
func TestBalanceWriteNeedsAnExplicitTarget(t *testing.T) {
	s := quietServer("192.0.2.10")
	s.SetBalanceWriteFn(func(string, int) error { return nil })

	if code, _ := postBalance(t, s, `{}`); code != http.StatusBadRequest {
		t.Fatalf("a body with no target got %d, want 400", code)
	}
	if code, _ := postBalance(t, s, `not json`); code != http.StatusBadRequest {
		t.Fatalf("an unparseable body got %d, want 400", code)
	}
}

// A speaker that does not answer must not be written to. The read comes first
// precisely so that a write is never sent into the dark, and its failure has to
// surface as "unreachable" rather than as a write that happened.
func TestBalanceWriteRefusesWhenTheSpeakerDoesNotAnswer(t *testing.T) {
	s := quietServer("192.0.2.10") // unroutable, so the firmware read fails
	sent := 0
	s.SetBalanceWriteFn(func(string, int) error { sent++; return nil })

	code, out := postBalance(t, s, `{"target":-3}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if out["ok"] == true {
		t.Fatalf("a write to an unreachable speaker reported success: %v", out)
	}
	if out["reason"] != "unreachable" {
		t.Fatalf("reason = %v, want unreachable", out["reason"])
	}
	if sent != 0 {
		t.Fatalf("the frame was sent %d times to a speaker that never answered a read", sent)
	}
}

// GET is unchanged, and the endpoint now takes exactly these two methods.
func TestBalanceEndpointTakesGetAndPostOnly(t *testing.T) {
	s := quietServer("192.0.2.10")
	req := httptest.NewRequest(http.MethodDelete, "/api/box/balance", nil)
	w := httptest.NewRecorder()
	s.handleBoxBalance(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE got %d, want 405", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/box/balance", nil)
	w = httptest.NewRecorder()
	s.handleBoxBalance(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET got %d, want 200", w.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["available"] != false {
		t.Fatalf("an unreachable speaker must report no balance, got %v", out)
	}
}

// The bounds come from the speaker, never from a constant. A widely-copied
// community implementation assumes -50..+50 and the firmware answers -7..+7, so
// a client built against that range must land inside this one instead of being
// refused or written straight through.
func TestBalanceIsClampedToWhatTheSpeakerReports(t *testing.T) {
	cases := []struct{ in, lo, hi, want int }{
		{-50, -7, 7, -7}, // the community range, folded into the real one
		{50, -7, 7, 7},
		{-3, -7, 7, -3}, // in range, untouched
		{0, -7, 7, 0},
		{-7, -7, 7, -7}, // the ends themselves are legal
		{7, -7, 7, 7},
	}
	for _, c := range cases {
		if got := clampInt(c.in, c.lo, c.hi); got != c.want {
			t.Fatalf("clamp(%d, %d..%d) = %d, want %d", c.in, c.lo, c.hi, got, c.want)
		}
	}
	// A speaker that reports a nonsense range (min above max, which is what a
	// zeroed or half-parsed read looks like) must not have its value forced to
	// one of those two numbers.
	if got := clampInt(-3, 7, -7); got != -3 {
		t.Fatalf("an inverted range rewrote the value to %d", got)
	}
}

// A failed send is a failed write. It is the state the client sits in whenever
// the speaker is asleep or reconnecting, so it is common, and it must not be
// dressed up as a success with verified:false.
func TestBalanceWriteReportsASendFailureAsAFailure(t *testing.T) {
	s := quietServer("192.0.2.10")
	s.SetBalanceWriteFn(func(string, int) error { return errors.New("not connected") })

	// The read fails first on an unroutable host, so this asserts the ordering
	// rather than the send: nothing may claim ok:true on this path either.
	_, out := postBalance(t, s, `{"target":-3}`)
	if out["ok"] == true {
		t.Fatalf("a write that could not be sent reported success: %v", out)
	}
}
