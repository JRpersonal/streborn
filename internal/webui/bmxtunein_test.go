package webui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func fakeRadiotime(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	oldBase, oldClient := radiotimeBase, radiotimeClient
	radiotimeBase = srv.URL
	radiotimeClient = srv.Client()
	t.Cleanup(func() { radiotimeBase, radiotimeClient = oldBase, oldClient })
}

func TestTuneInStationResolves(t *testing.T) {
	resetBMXLimiters(t)
	var gotIDs []string
	fakeRadiotime(t, func(w http.ResponseWriter, r *http.Request) {
		gotIDs = append(gotIDs, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/Tune.ashx":
			_, _ = w.Write([]byte(`{"head":{"status":"200"},"body":[
				{"element":"audio","url":"ftp://bad.example/x","media_type":"mp3"},
				{"element":"audio","url":"https://hls.example/live.m3u8","media_type":"hls"},
				{"element":"audio","url":"https://radio.example/stream.mp3?aggregator=tunein","media_type":"mp3","is_direct":true}]}`))
		case "/Describe.ashx":
			_, _ = w.Write([]byte(`{"head":{"status":"200"},"body":[{"element":"station","guide_id":"s25260","name":"1LIVE","logo":"https://cdn.example/logoq.jpg"}]}`))
		default:
			http.NotFound(w, r)
		}
	})
	var logs bytes.Buffer
	s := bmxTestServer(&logs)
	rr := httptest.NewRecorder()
	s.handleBMX(rr, httptest.NewRequest(http.MethodGet, "/bmx/tunein/v1/playback/station/s25260", nil))
	assertJSON(t, rr, http.StatusOK)

	var d lirDescriptor
	if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Name != "1LIVE" || d.StreamType != "liveRadio" || !d.Audio.IsRealtime || d.Audio.HasPlaylist {
		t.Fatalf("descriptor = %+v", d)
	}
	u, err := url.Parse(d.Audio.StreamURL)
	if err != nil || u.Path != "/stream/raw" {
		t.Fatalf("streamUrl %q is not the raw stream proxy", d.Audio.StreamURL)
	}
	origin, _ := base64.RawURLEncoding.DecodeString(u.Query().Get("u"))
	if string(origin) != "https://radio.example/stream.mp3?aggregator=tunein" {
		t.Fatalf("proxied origin = %q, want the mp3 entry", origin)
	}
	if d.ImageURL == "" {
		t.Fatal("imageUrl empty")
	}
	if len(gotIDs) != 2 || !strings.Contains(gotIDs[0], "id=s25260") || !strings.Contains(gotIDs[0], "render=json") {
		t.Fatalf("upstream calls = %v", gotIDs)
	}
	if !strings.Contains(logs.String(), "bmx tunein: station resolved") || !strings.Contains(logs.String(), "streamHost=radio.example") {
		t.Fatalf("log: %s", logs.String())
	}
}

func TestTuneInStationDescribeFailureKeepsPlaying(t *testing.T) {
	resetBMXLimiters(t)
	fakeRadiotime(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Tune.ashx" {
			_, _ = w.Write([]byte(`{"head":{"status":"200"},"body":[{"element":"audio","url":"http://radio.example/a.aac","media_type":"aac"}]}`))
			return
		}
		http.Error(w, "down", http.StatusInternalServerError)
	})
	rr := httptest.NewRecorder()
	bmxTestServer(&bytes.Buffer{}).handleBMX(rr, httptest.NewRequest(http.MethodGet, "/bmx/tunein/v1/playback/station/s1", nil))
	assertJSON(t, rr, http.StatusOK)
	var d lirDescriptor
	_ = json.Unmarshal(rr.Body.Bytes(), &d)
	if d.Name != "s1" || d.Audio.StreamURL == "" {
		t.Fatalf("descriptor = %+v", d)
	}
}

func TestTuneInStationBadID(t *testing.T) {
	resetBMXLimiters(t)
	fakeRadiotime(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream must not be called for a bad id")
		w.WriteHeader(500)
	})
	for _, p := range []string{
		"/bmx/tunein/v1/playback/station/",
		"/bmx/tunein/v1/playback/station/x123",
		"/bmx/tunein/v1/playback/station/s12a",
		"/bmx/tunein/v1/playback/station/s1&id=s2",
		"/bmx/tunein/v1/playback/station/s1/extra",
	} {
		rr := httptest.NewRecorder()
		bmxTestServer(&bytes.Buffer{}).handleBMX(rr, httptest.NewRequest(http.MethodGet, (&url.URL{Path: p}).String(), nil))
		assertJSON(t, rr, http.StatusBadRequest)
	}
}

func TestTuneInStationUpstreamDown(t *testing.T) {
	resetBMXLimiters(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // connection refused from here on
	oldBase, oldClient := radiotimeBase, radiotimeClient
	radiotimeBase, radiotimeClient = base, &http.Client{Timeout: 2 * time.Second}
	t.Cleanup(func() { radiotimeBase, radiotimeClient = oldBase, oldClient })

	rr := httptest.NewRecorder()
	bmxTestServer(&bytes.Buffer{}).handleBMX(rr, httptest.NewRequest(http.MethodGet, "/bmx/tunein/v1/playback/station/s25260", nil))
	assertJSON(t, rr, http.StatusBadGateway)
}

func TestTuneInStationNoPlayableEntry(t *testing.T) {
	resetBMXLimiters(t)
	fakeRadiotime(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"head":{"status":"400","fault":"Invalid root"},"body":[]}`))
	})
	rr := httptest.NewRecorder()
	bmxTestServer(&bytes.Buffer{}).handleBMX(rr, httptest.NewRequest(http.MethodGet, "/bmx/tunein/v1/playback/station/s9", nil))
	assertJSON(t, rr, http.StatusBadGateway)
}

func TestTuneInToken(t *testing.T) {
	resetBMXLimiters(t)
	rr := httptest.NewRecorder()
	bmxTestServer(&bytes.Buffer{}).handleBMX(rr, httptest.NewRequest(http.MethodGet, "/bmx/tunein/v1/token", nil))
	m := assertJSON(t, rr, http.StatusOK)
	if _, ok := m["access_token"]; !ok {
		t.Fatalf("token body = %v", m)
	}
}
