package webui

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

// An SSID with an empty password must be refused unless the request declares
// the network open: handed through, the box switches to an open-network
// profile and drops off a protected home Wi-Fi right after an otherwise
// successful OTA install (field report 2026-08-29). Older app versions still
// send the empty password, so the agent is the guard that covers them all.
func TestBoxWLANRefusesEmptyPasswordWithoutOpenFlag(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	put := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/api/box/wlan", strings.NewReader(body))
		req.RemoteAddr = "192.168.178.20:40000"
		s.handleBoxWLAN(rr, req)
		return rr
	}

	rr := put(`{"ssid":"HomeNet","password":"","force":true}`)
	if rr.Code != 400 || !strings.Contains(rr.Body.String(), "open") {
		t.Fatalf("empty password without open flag must 400 with guidance, got %d %q", rr.Code, rr.Body.String())
	}

	if rr := put(`{"ssid":"HomeNet","password":"short"}`); rr.Code != 400 {
		t.Fatalf("short password must 400, got %d", rr.Code)
	}
}
