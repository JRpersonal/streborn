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

// #500 experiment: the station answer offers a now-playing address, and that
// address answers with the live title split into artist and song.
func TestTuneInNowPlaying(t *testing.T) {
	resetBMXLimiters(t)
	fakeRadiotime(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/Tune.ashx":
			_, _ = w.Write([]byte(`{"head":{"status":"200"},"body":[{"element":"audio","url":"https://radio.example/s.mp3","media_type":"mp3"}]}`))
		case "/Describe.ashx":
			_, _ = w.Write([]byte(`{"head":{"status":"200"},"body":[{"element":"station","name":"1LIVE","logo":"https://cdn.example/l.jpg"}]}`))
		}
	})
	var logs bytes.Buffer
	s := bmxTestServer(&logs)
	// a title left over from whatever played before
	s.lastICYTitle = "Old Artist - Old Song"

	rr := httptest.NewRecorder()
	s.handleBMX(rr, httptest.NewRequest(http.MethodGet, "/bmx/tunein/v1/playback/station/s25260", nil))
	var st tuneInStation
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Links["bmx_nowplaying"].Href != "/v1/now-playing/station/s25260" || st.NowPlayingURI == "" || st.Name != "1LIVE" {
		t.Fatalf("station answer = %s", rr.Body.String())
	}

	ask := func() tuneInNowPlaying {
		rr := httptest.NewRecorder()
		s.handleBMX(rr, httptest.NewRequest(http.MethodGet, "/bmx/tunein"+st.Links["bmx_nowplaying"].Href, nil))
		assertJSON(t, rr, http.StatusOK)
		var np tuneInNowPlaying
		if err := json.Unmarshal(rr.Body.Bytes(), &np); err != nil {
			t.Fatal(err)
		}
		return np
	}
	if np := ask(); np.Name != "1LIVE" || np.Artist.Name != "" {
		t.Fatalf("before a title the station name, never the old song: %+v", np)
	}
	s.HandleStreamTitle("Teddy Swims - Mr. Know It All")
	if np := ask(); np.Name != "Mr. Know It All" || np.Track.Name != "Mr. Know It All" || np.Artist.Name != "Teddy Swims" {
		t.Fatalf("now playing = %+v", np)
	}
	// the firmware's own spelling of the route lands on the same answer
	rr = httptest.NewRecorder()
	s.handleBMX(rr, httptest.NewRequest(http.MethodGet, "/bmx/tunein/nowPlaying?partnerId=x", nil))
	assertJSON(t, rr, http.StatusOK)
	if !strings.Contains(logs.String(), "bmx tunein: now-playing asked") {
		t.Fatalf("every now-playing request must be logged: %s", logs.String())
	}
}

func TestSplitICYTitle(t *testing.T) {
	for in, want := range map[string][2]string{
		"Tyla - CHANEL": {"Tyla", "CHANEL"},
		"1LIVE":         {"", "1LIVE"},
		" - ":           {"", "-"},
		"A - B - C":     {"A", "B - C"},
	} {
		if a, s := splitICYTitle(in); a != want[0] || s != want[1] {
			t.Errorf("splitICYTitle(%q) = %q, %q; want %q, %q", in, a, s, want[0], want[1])
		}
	}
}
