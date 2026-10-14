package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newStreamSourceTestApp() *App {
	return &App{httpClient: &http.Client{}}
}

func TestStreamSourceStationsAndPlayback(t *testing.T) {
	directRequests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/stations":
			fmt.Fprint(w, `[{"id":"octane","name":"Octane","stream_type":"audio"},{"id":"","name":"ignored"}]`)
		case "/api/stations/octane/stream":
			directRequests <- r.Method
			w.Header().Set("Content-Type", "audio/mpeg")
			fmt.Fprint(w, "audio")
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
	if len(stations) != 1 || stations[0].ID != "octane" || stations[0].Name != "Octane" || stations[0].StreamType != "audio" {
		t.Fatalf("unexpected stations: %#v", stations)
	}
	playback, err := a.StreamSourcePlayback(server.URL, "octane", stations[0].StreamType)
	if err != nil {
		t.Fatal(err)
	}
	if playback.StreamURL != server.URL+"/api/stations/octane/stream" || playback.Title != "" {
		t.Fatalf("unexpected direct playback: %#v", playback)
	}
	select {
	case method := <-directRequests:
		t.Fatalf("direct playback opened a server session with %s", method)
	default:
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

	playback, err := newStreamSourceTestApp().StreamSourcePlayback(server.URL, "show", "json")
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
		_, err := newStreamSourceTestApp().StreamSourcePlayback(server.URL, "show", "json")
		server.Close()
		if err == nil {
			t.Errorf("unsafe playback URL %q unexpectedly succeeded", streamURL)
		}
	}
}

func TestStreamSourceRejectsUnknownStreamType(t *testing.T) {
	_, err := newStreamSourceTestApp().StreamSourcePlayback("http://127.0.0.1", "station", "video")
	if err == nil || !strings.Contains(err.Error(), "unsupported stream_type") {
		t.Fatalf("unexpected stream type result: %v", err)
	}
}
