package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSetBoxPowerPostsTheTarget pins the music tab's power toggle (#1644) to
// the agent endpoint behind the phone remote's power button: a POST to
// /api/box/power carrying the target, on for a wake and off for standby.
func TestSetBoxPowerPostsTheTarget(t *testing.T) {
	for _, on := range []bool{true, false} {
		var gotMethod, gotPath string
		var got map[string]bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath = r.Method, r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"on":true}`))
		}))
		a := newTestApp()
		err := a.SetBoxPower("127.0.0.1", listenPort(t, srv), on)
		srv.Close()
		if err != nil {
			t.Fatalf("on=%v: SetBoxPower: %v", on, err)
		}
		if gotMethod != http.MethodPost || gotPath != "/api/box/power" {
			t.Errorf("on=%v: request = %s %s, want POST /api/box/power", on, gotMethod, gotPath)
		}
		if v, ok := got["on"]; !ok || v != on {
			t.Errorf("on=%v: body = %v, want {\"on\":%v}", on, got, on)
		}
	}
}

// TestSetBoxPowerSurfacesTheAgentError keeps a failed standby or wake visible:
// the agent answers 502 with the box's reason, and the app must show it rather
// than leave the button in the clicked state.
func TestSetBoxPowerSurfacesTheAgentError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "standby failed", http.StatusBadGateway)
	}))
	defer srv.Close()
	a := newTestApp()
	err := a.SetBoxPower("127.0.0.1", listenPort(t, srv), false)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want the agent's 502", err)
	}
}
