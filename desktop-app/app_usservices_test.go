package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// usAgent is a speaker agent with the US-services endpoints.
type usAgent struct {
	mu     sync.Mutex
	calls  []string
	bodies map[string]string
}

func newUSAgent(t *testing.T) (*usAgent, *httptest.Server) {
	t.Helper()
	ag := &usAgent{bodies: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path
		ag.calls = append(ag.calls, key)
		ag.bodies[key] = string(b)
		w.Header().Set("Content-Type", "application/json")
		switch key {
		case "GET /api/us-services":
			_, _ = w.Write([]byte(`{"region":{"country":"DE","source":"str"},"standIn":{"optIn":false,"services":[]}}`))
		case "POST /api/us-services", "POST /api/pandora/account", "DELETE /api/iheart/account", "POST /api/pandora/play":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "GET /api/pandora/stations":
			_, _ = w.Write([]byte(`{"status":"READY","stations":[{"name":"Jazz Radio","source":"PANDORA","location":"111","type":"stationurl","art":"http://img.example.com/1.jpg","presetable":true}]}`))
		case "POST /api/iheart/account":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"off","code":"service-off"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return ag, srv
}

func TestUSServicesBindings(t *testing.T) {
	ag, srv := newUSAgent(t)
	a := newTestApp()
	var logBuf bytes.Buffer
	a.logger = slog.New(slog.NewTextHandler(&logBuf, nil))
	port := listenPort(t, srv)

	st, err := a.USServicesStatus("127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	if region, _ := st["region"].(map[string]any); region["country"] != "DE" {
		t.Fatalf("status = %v", st)
	}
	if err := a.SetUSServicesOptIn("127.0.0.1", port, true); err != nil {
		t.Fatal(err)
	}
	if got := ag.bodies["POST /api/us-services"]; got != `{"enabled":true}` {
		t.Fatalf("opt-in body %q", got)
	}
	if err := a.LinkMusicService("127.0.0.1", port, "pandora", " user@example.com ", "s3cret-pw"); err != nil {
		t.Fatal(err)
	}
	var link map[string]string
	_ = json.Unmarshal([]byte(ag.bodies["POST /api/pandora/account"]), &link)
	if link["user"] != "user@example.com" || link["password"] != "s3cret-pw" {
		t.Fatalf("link body %v", link)
	}
	if strings.Contains(logBuf.String(), "s3cret-pw") {
		t.Fatal("password logged")
	}
	err = a.LinkMusicService("127.0.0.1", port, "iheart", "user@example.com", "pw")
	if err == nil || !strings.Contains(err.Error(), "service-off") {
		t.Fatalf("refusal must carry the agent's code: %v", err)
	}
	if err := a.LinkMusicService("127.0.0.1", port, "spotify", "u", "p"); err == nil {
		t.Fatal("unknown service accepted")
	}
	if err := a.LinkMusicService("127.0.0.1", port, "pandora", "u", ""); err == nil {
		t.Fatal("empty password accepted")
	}
	if err := a.UnlinkMusicService("127.0.0.1", port, "iheart"); err != nil {
		t.Fatal(err)
	}
	list, err := a.PandoraStations("127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	if list.Status != "READY" || len(list.Stations) != 1 || list.Stations[0].Location != "111" || list.Stations[0].Art == "" {
		t.Fatalf("stations = %+v", list)
	}
	if err := a.PlayPandoraStation("127.0.0.1", port, "111", "Jazz Radio", "stationurl"); err != nil {
		t.Fatal(err)
	}
	if got := ag.bodies["POST /api/pandora/play"]; !strings.Contains(got, `"location":"111"`) {
		t.Fatalf("play body %q", got)
	}
	if err := a.PlayPandoraStation("127.0.0.1", port, " ", "", ""); err == nil {
		t.Fatal("empty location accepted")
	}
}
