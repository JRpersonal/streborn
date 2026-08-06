package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// withLocalProbe points the probe at a plain client for the duration of a test.
// The production client refuses loopback (SSRF guard) and a test server is
// loopback, so without this every assertion below would silently pass through
// the "could not reach it, allow the save" branch and test nothing at all.
func withLocalProbe(t *testing.T) {
	t.Helper()
	prev := presetProbeClient
	presetProbeClient = func(timeout time.Duration) *http.Client {
		return &http.Client{Timeout: timeout}
	}
	t.Cleanup(func() { presetProbeClient = prev })
}

// The probe must refuse ONLY on positive evidence of a web page. Every other
// answer, including every failure, has to allow the save: a station that is
// down, rate-limiting or speaking legacy ICY is still a station, and refusing
// it would be an intermittent bug that looks like STR losing presets.
func TestLooksLikeWebPage(t *testing.T) {
	withLocalProbe(t)
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    bool
	}{
		{
			name: "station home page is refused",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte("<!doctype html><html><body>Radio Hamburg</body></html>"))
			},
			want: true,
		},
		{
			name: "html with no content type is sniffed and refused",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header()["Content-Type"] = nil
				_, _ = w.Write([]byte("<!DOCTYPE html>\n<html lang=\"de\">"))
			},
			want: true,
		},
		{
			name: "mp3 stream is allowed",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "audio/mpeg")
				_, _ = w.Write([]byte("\xff\xfb\x90\x00"))
			},
			want: false,
		},
		{
			name: "icecast without a content type but with ICY headers is allowed",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("icy-name", "Studio D")
				w.Header().Set("icy-metaint", "16384")
				w.Header().Set("Content-Type", "text/html") // some servers really do this
				_, _ = w.Write([]byte("\xff\xfb"))
			},
			want: false,
		},
		{
			name: "a station that is down is allowed",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("<html>maintenance</html>"))
			},
			want: false,
		},
		{
			name: "a 404 is allowed",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			want: false,
		},
		{
			name: "aac stream is allowed",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "audio/aac")
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			if got := looksLikeWebPage(context.Background(), srv.URL+"/stream"); got != tc.want {
				t.Errorf("looksLikeWebPage = %v, want %v", got, tc.want)
			}
		})
	}
}

// A playlist is resolved elsewhere and is legitimately served as text by some
// servers, so it must never be probed into a refusal.
func TestPlaylistURLsSkipTheProbe(t *testing.T) {
	withLocalProbe(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html>"))
	}))
	defer srv.Close()
	for _, ext := range []string{".pls", ".m3u", ".m3u8", ".asx", ".xspf"} {
		if looksLikeWebPage(context.Background(), srv.URL+"/listen"+ext) {
			t.Errorf("%s was refused; playlists must skip the probe", ext)
		}
	}
}

// An unreachable host is inconclusive, never a refusal.
func TestUnreachableHostIsAllowed(t *testing.T) {
	withLocalProbe(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL + "/gone"
	srv.Close() // nothing is listening any more
	if looksLikeWebPage(context.Background(), url) {
		t.Error("an unreachable station was refused; it must be allowed")
	}
}

// The scheme gate belongs to the existing save path, and a non-http URL must
// not even be dialled here.
func TestNonHTTPSchemeIsNotProbed(t *testing.T) {
	if looksLikeWebPage(context.Background(), "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M") {
		t.Error("a spotify URI was treated as a web page")
	}
	if looksLikeWebPage(context.Background(), "") {
		t.Error("an empty URL was treated as a web page")
	}
}

// The production probe must keep its SSRF guard: a preset URL pointing at the
// speaker's own loopback services is never dialled, and the save is allowed
// rather than refused (the structural gates own that case, not this probe).
// This test deliberately does NOT install the local-probe seam.
func TestProbeKeepsTheSSRFGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html>"))
	}))
	defer srv.Close()
	// The server serves HTML on loopback. With the guard in place the dial never
	// happens, so the answer is "could not tell", not "web page".
	if looksLikeWebPage(context.Background(), srv.URL+"/stream") {
		t.Error("the guarded probe reached a loopback address; the SSRF guard is not in the path")
	}
}
