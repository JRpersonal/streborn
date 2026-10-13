package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newStreamSourceTestApp() *App {
	return &App{httpClient: &http.Client{}}
}

func TestStreamSourceStationsAndPlayback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/stations":
			fmt.Fprint(w, `[{"id":"octane","name":"Octane"},{"id":"","name":"ignored"}]`)
		case "/api/stations/octane/stream":
			w.Header().Set("Content-Type", "audio/mpeg")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done() // model a live response that does not reach EOF
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	a := newStreamSourceTestApp()
	stations, err := a.StreamSourceStations(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(stations) != 1 || stations[0].ID != "octane" || stations[0].Name != "Octane" {
		t.Fatalf("unexpected stations: %#v", stations)
	}
	playback, err := a.StreamSourcePlayback(server.URL, "octane")
	if err != nil {
		t.Fatal(err)
	}
	if playback.StreamURL != server.URL+"/api/stations/octane/stream" || playback.Title != "octane" {
		t.Fatalf("unexpected direct playback: %#v", playback)
	}
}

func TestStreamSourcePlaybackFromJSONStreamEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/stations/show/stream" {
			fmt.Fprint(w, `{"url":"https://radio.example/live","now_playing":"Live show"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	playback, err := newStreamSourceTestApp().StreamSourcePlayback(server.URL, "show")
	if err != nil {
		t.Fatal(err)
	}
	if playback.StreamURL != "https://radio.example/live" || playback.Title != "Live show" {
		t.Fatalf("unexpected playback metadata: %#v", playback)
	}
}

func TestStreamSourceRejectsUnsafeBaseAndPlaybackURLs(t *testing.T) {
	for _, raw := range []string{
		"", "example.com", "file:///tmp/stream", "http://user:pass@example.com", "http://example.com/?token=x",
	} {
		if _, err := streamSourceBase(raw); err == nil {
			t.Errorf("streamSourceBase(%q) unexpectedly succeeded", raw)
		}
	}
	for _, streamURL := range []string{"javascript:alert(1)", "ftp://radio.example/live"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"url":%q}`, streamURL)
		}))
		_, err := newStreamSourceTestApp().StreamSourcePlayback(server.URL, "show")
		server.Close()
		if err == nil {
			t.Errorf("unsafe playback URL %q unexpectedly succeeded", streamURL)
		}
	}
}
