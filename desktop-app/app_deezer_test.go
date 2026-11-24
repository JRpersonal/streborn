package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestDeezerBindings(t *testing.T) {
	var mu sync.Mutex
	bodies := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path
		mu.Lock()
		bodies[key] = string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch key {
		case "GET /api/deezer/account":
			_, _ = w.Write([]byte(`{"state":"connected","account":"us***@example.com","source":{"status":"READY","hasAccount":true}}`))
		case "POST /api/deezer/account":
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"error":"a new Deezer sign-in is not possible without the Bose cloud","code":"new-login-unsupported"}`))
		case "POST /api/deezer/recheck":
			_, _ = w.Write([]byte(`{"state":"connecting","recreate":{"ran":true}}`))
		case "DELETE /api/deezer/account":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "POST /api/deezer/play":
			_, _ = w.Write([]byte(`{"ok":true,"kind":"album","id":"376222197","name":"Paschtaschutta"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a := newTestApp()
	var logBuf bytes.Buffer
	a.logger = slog.New(slog.NewTextHandler(&logBuf, nil))
	port := listenPort(t, srv)

	st, err := a.DeezerStatus("127.0.0.1", port)
	if err != nil || st["state"] != "connected" {
		t.Fatalf("status = %v, %v", st, err)
	}
	// An older frontend's sign-in gets the agent's refusal back, code and all.
	if err := a.LinkMusicService("127.0.0.1", port, "deezer", "user@example.com", "deezer-pw"); err == nil || !strings.Contains(err.Error(), "new-login-unsupported") {
		t.Fatalf("sign-in = %v", err)
	}
	re, err := a.DeezerRecheck("127.0.0.1", port)
	if err != nil || re["state"] != "connecting" || bodies["POST /api/deezer/recheck"] != "{}" {
		t.Fatalf("recheck = %v, %v, body %q", re, err, bodies["POST /api/deezer/recheck"])
	}
	if err := a.UnlinkMusicService("127.0.0.1", port, "deezer"); err != nil {
		t.Fatal(err)
	}
	got, err := a.PlayDeezerLink("127.0.0.1", port, " https://www.deezer.com/en/album/376222197 ", "")
	if err != nil || got["name"] != "Paschtaschutta" {
		t.Fatalf("play = %v, %v", got, err)
	}
	if !strings.Contains(bodies["POST /api/deezer/play"], `"url":"https://www.deezer.com/en/album/376222197"`) {
		t.Fatalf("play body %q", bodies["POST /api/deezer/play"])
	}
	if _, err := a.PlayDeezerLink("127.0.0.1", port, " ", ""); err == nil {
		t.Fatal("an empty link was sent")
	}
	if strings.Contains(logBuf.String(), "deezer-pw") {
		t.Fatal("password logged")
	}
}
