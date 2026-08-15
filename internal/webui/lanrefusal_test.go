package webui

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A refused update used to say only "update only allowed from LAN", and said
// it nowhere else: no log line, so the diagnostic bundle from a user whose
// update was refused carried no trace of it. One reporter went through ten
// attempts and a mail exchange over this. The refusal has to name the address
// it turned away.
func TestALANRefusalNamesTheAddressItTurnedAway(t *testing.T) {
	var logged bytes.Buffer
	s := &Server{logger: slog.New(slog.NewTextHandler(&logged, nil))}

	req := httptest.NewRequest(http.MethodPost, "/api/agent/update", nil)
	req.RemoteAddr = "203.0.113.9:51234"
	rec := httptest.NewRecorder()

	s.refuseNonLAN(rec, req, "agent-update")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "203.0.113.9") {
		t.Errorf("the answer must name the refused address, got %q", body)
	}
	if !strings.Contains(logged.String(), "203.0.113.9") {
		t.Errorf("the refusal must be logged so a bundle carries it, got %q", logged.String())
	}
	if !strings.Contains(logged.String(), "agent-update") {
		t.Error("the log line must say which endpoint refused")
	}
}

// The port must not end up in the message: it is noise, and it changes on
// every attempt, which makes two identical refusals look different.
func TestTheRefusalReportsTheAddressWithoutThePort(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/sidecar", nil)
	req.RemoteAddr = "198.51.100.4:44321"
	rec := httptest.NewRecorder()

	s.refuseNonLAN(rec, req, "sidecar")

	if strings.Contains(rec.Body.String(), "44321") {
		t.Errorf("the source port has no business in the message: %q", rec.Body.String())
	}
}
