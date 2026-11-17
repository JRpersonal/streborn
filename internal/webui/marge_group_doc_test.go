package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/marge"
)

// newMargeGroupServer builds a webui Server whose marge-group bridge is wired
// to a real in-memory marge server, the way cmd/agent/main.go wires it.
func newMargeGroupServer() (*Server, *marge.Server) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ms := marge.New(logger)
	s := &Server{logger: logger}
	WithMargeGroups(ms.GroupSnapshot, ms.SetCanonicalGroup, ms.ClearGroup, ms.RenameGroup, ms.GroupName)(s)
	return s, ms
}

func margeGroupDo(s *Server, method, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/api/marge/group", strings.NewReader(body))
	s.handleMargeGroupDoc(rec, req)
	return rec
}

// The relay endpoint the desktop app uses to install the canonical pair
// document on the partner's marge (series-I speakers cannot reach each other's
// agents, so the app is the messenger).
func TestMargeGroupDocRelayRoundTrip(t *testing.T) {
	s, ms := newMargeGroupServer()

	// Empty: GET answers 404 JSON (NOT the catch-all HTML index).
	rec := margeGroupDo(s, http.MethodGet, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("empty GET status=%d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("empty GET content-type=%q, want JSON", ct)
	}

	// POST installs the canonical document.
	doc := marge.CanonicalGroupXML("Stereo pair", "AAAABBBBCCCC", "192.0.2.10", "DDDDEEEEFFFF", "192.0.2.11")
	rec = margeGroupDo(s, http.MethodPost, doc)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status=%d (body %s)", rec.Code, rec.Body.String())
	}
	if xmlDoc, canonical, ok := ms.GroupSnapshot(); !ok || !canonical || !strings.Contains(xmlDoc, "AAAABBBBCCCC") {
		t.Fatalf("marge record after relay: ok=%v canonical=%v xml=%s", ok, canonical, xmlDoc)
	}

	// GET returns it.
	rec = margeGroupDo(s, http.MethodGet, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "AAAABBBBCCCC") {
		t.Fatalf("GET status=%d body=%s", rec.Code, rec.Body.String())
	}

	// A garbage document is rejected, record untouched.
	rec = margeGroupDo(s, http.MethodPost, "<group></group>")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage POST status=%d, want 400", rec.Code)
	}
	if _, _, ok := ms.GroupSnapshot(); !ok {
		t.Fatal("valid record was dropped by a rejected POST")
	}

	// DELETE clears (dissolve relay).
	rec = margeGroupDo(s, http.MethodDelete, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status=%d", rec.Code)
	}
	if _, _, ok := ms.GroupSnapshot(); ok {
		t.Fatal("record still present after DELETE")
	}
}

// A server without the bridge (older wiring) must answer 501, never pretend.
func TestMargeGroupDocUnwired(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := margeGroupDo(s, http.MethodGet, "")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("unwired GET status=%d, want 501", rec.Code)
	}
}

// #1964: a pair document relayed to a SoundTouch 30 made it refuse every play
// as the following half of a pair it can never be part of. Only a SoundTouch
// 10 (or a speaker whose model is not known yet) takes one.
func TestMargeGroupDocRelayRefusedOnAModelThatCannotPair(t *testing.T) {
	doc := marge.CanonicalGroupXML("Stereo pair", "AAAABBBBCCCC", "192.0.2.10", "DDDDEEEEFFFF", "192.0.2.11")

	s, ms := newMargeGroupServer()
	s.boxNameFn = func() (string, string) { return "Kitchen", "SoundTouch 30" }
	rec := margeGroupDo(s, http.MethodPost, doc)
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST on a SoundTouch 30 status=%d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("refusal content-type=%q, want JSON", ct)
	}
	if _, _, ok := ms.GroupSnapshot(); ok {
		t.Fatal("a SoundTouch 30 stored a stereo pair document")
	}
	// The clear stays open, so a record left by an older agent can be removed.
	if rec = margeGroupDo(s, http.MethodDelete, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE on a SoundTouch 30 status=%d", rec.Code)
	}

	s, ms = newMargeGroupServer()
	s.boxNameFn = func() (string, string) { return "Office", "SoundTouch 10" }
	if rec = margeGroupDo(s, http.MethodPost, doc); rec.Code != http.StatusOK {
		t.Fatalf("POST on a SoundTouch 10 status=%d (body %s)", rec.Code, rec.Body.String())
	}
	if _, _, ok := ms.GroupSnapshot(); !ok {
		t.Fatal("a SoundTouch 10 did not store the pair document")
	}
}
