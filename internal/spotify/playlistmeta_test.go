package spotify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A key holding a playlist Spotify's public oEmbed lookup does not answer for
// (a private one) was stored as "Spotify" with no cover, however often it was
// saved (discussion #1870). The name and cover then come from the Web API with
// the logged-in account's own token.

// metaTestEngine is an engine that hands out a token, counting the requests.
func metaTestEngine(t *testing.T, tokenCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" && r.Method == http.MethodPost {
			tokenCalls.Add(1)
			_, _ = w.Write([]byte(`{"token":"BQ-test-token"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestPlaylistMetaPublicPlaylistNeverAsksTheWebAPI(t *testing.T) {
	var tokenCalls, apiCalls atomic.Int32
	engine := metaTestEngine(t, &tokenCalls)
	defer engine.Close()
	oembed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("url"); got != "https://open.spotify.com/playlist/pub" {
			t.Errorf("oEmbed asked for %q", got)
		}
		_, _ = w.Write([]byte(`{"title":"Jens Chill","thumbnail_url":"https://i.scdn.co/image/pub"}`))
	}))
	defer oembed.Close()
	api := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { apiCalls.Add(1) }))
	defer api.Close()

	m := productTestManager(t, engine.URL)
	cover, title := m.playlistMetaAt(context.Background(), "spotify:playlist:pub", oembed.URL, api.URL)
	if cover != "https://i.scdn.co/image/pub" || title != "Jens Chill" {
		t.Fatalf("got cover=%q title=%q, want the oEmbed answer", cover, title)
	}
	if tokenCalls.Load() != 0 || apiCalls.Load() != 0 {
		t.Fatalf("a public playlist cost token=%d api=%d Web API requests, want none", tokenCalls.Load(), apiCalls.Load())
	}
}

func TestPlaylistMetaPrivatePlaylistIsNamedThroughTheAccount(t *testing.T) {
	var tokenCalls atomic.Int32
	engine := metaTestEngine(t, &tokenCalls)
	defer engine.Close()
	oembed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Not found", http.StatusNotFound)
	}))
	defer oembed.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer BQ-test-token" {
			t.Errorf("Authorization header = %q, want the engine's token", got)
		}
		switch r.URL.Path {
		case "/playlists/priv":
			if r.URL.Query().Get("fields") != "name,images" {
				t.Errorf("playlist asked without the fields filter: %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"name":" Worship Hits ","images":[
				{"url":"https://mosaic.scdn.co/640/x","width":640},
				{"url":"https://mosaic.scdn.co/300/x","width":300},
				{"url":"https://mosaic.scdn.co/60/x","width":60}]}`))
		case "/playlists/ownpic":
			_, _ = w.Write([]byte(`{"name":"Own picture","images":[{"url":"https://image-cdn.example/one","width":null}]}`))
		case "/tracks/song":
			_, _ = w.Write([]byte(`{"name":"A Song","album":{"images":[{"url":"https://i.scdn.co/image/album","width":640}]}}`))
		case "/albums/plain":
			_, _ = w.Write([]byte(`{"name":"No pictures","images":[{"url":"http://insecure.example/x","width":300}]}`))
		default:
			http.Error(w, "nope", http.StatusNotFound)
		}
	}))
	defer api.Close()

	m := productTestManager(t, engine.URL)
	for _, tc := range []struct{ uri, cover, title string }{
		{"spotify:playlist:priv", "https://mosaic.scdn.co/300/x", "Worship Hits"},
		{"spotify:playlist:ownpic", "https://image-cdn.example/one", "Own picture"},
		{"spotify:track:song", "https://i.scdn.co/image/album", "A Song"},
		// Only an https picture is kept: the apps draw it from the user's machine.
		{"spotify:album:plain", "", "No pictures"},
		// Spotify does not know it either: nothing, and no error.
		{"spotify:playlist:gone", "", ""},
		// Not a context the Web API has a page for.
		{"spotify:show:podcast", "", ""},
	} {
		cover, title := m.playlistMetaAt(context.Background(), tc.uri, oembed.URL, api.URL)
		if cover != tc.cover || title != tc.title {
			t.Errorf("%s: got cover=%q title=%q, want cover=%q title=%q", tc.uri, cover, title, tc.cover, tc.title)
		}
	}
}

// oEmbed sometimes answers with a title and no picture; only the missing half
// is taken from the Web API, the oEmbed title stays.
func TestPlaylistMetaFillsOnlyWhatOEmbedLeftEmpty(t *testing.T) {
	var tokenCalls atomic.Int32
	engine := metaTestEngine(t, &tokenCalls)
	defer engine.Close()
	oembed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"title":"From oEmbed"}`))
	}))
	defer oembed.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"From the API","images":[{"url":"https://i.scdn.co/image/api","width":300}]}`))
	}))
	defer api.Close()

	m := productTestManager(t, engine.URL)
	cover, title := m.playlistMetaAt(context.Background(), "spotify:playlist:half", oembed.URL, api.URL)
	if cover != "https://i.scdn.co/image/api" || title != "From oEmbed" {
		t.Fatalf("got cover=%q title=%q", cover, title)
	}
}

// The lookup is cosmetic. While Spotify has asked the speaker to leave its Web
// API alone it does not ask, and a 429 of its own starts that quiet window so
// the plan read does not run into the same throttle.
func TestPlaylistMetaRespectsTheWebAPIThrottle(t *testing.T) {
	var tokenCalls, apiCalls atomic.Int32
	engine := metaTestEngine(t, &tokenCalls)
	defer engine.Close()
	oembed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Not found", http.StatusNotFound)
	}))
	defer oembed.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apiCalls.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer api.Close()

	m := productTestManager(t, engine.URL)
	if cover, title := m.playlistMetaAt(context.Background(), "spotify:playlist:priv", oembed.URL, api.URL); cover != "" || title != "" {
		t.Fatalf("a throttled lookup answered cover=%q title=%q", cover, title)
	}
	m.mu.Lock()
	quiet := time.Until(m.productQuietUntil)
	m.mu.Unlock()
	if quiet < 100*time.Second || quiet > 121*time.Second {
		t.Fatalf("quiet window after a 429 with Retry-After 120 = %s", quiet)
	}
	_, _ = m.playlistMetaAt(context.Background(), "spotify:playlist:priv", oembed.URL, api.URL)
	if apiCalls.Load() != 1 || tokenCalls.Load() != 1 {
		t.Fatalf("asked again inside the quiet window: api=%d token=%d, want 1 each", apiCalls.Load(), tokenCalls.Load())
	}
}

// Without an engine there is no token to ask with, and nothing is tried.
func TestPlaylistMetaWithoutEngineStaysOnOEmbed(t *testing.T) {
	var tokenCalls atomic.Int32
	engine := metaTestEngine(t, &tokenCalls)
	defer engine.Close()
	oembed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Not found", http.StatusNotFound)
	}))
	defer oembed.Close()

	m := productTestManager(t, engine.URL)
	m.binPath = ""
	if cover, title := m.playlistMetaAt(context.Background(), "spotify:playlist:priv", oembed.URL, "http://127.0.0.1:1"); cover != "" || title != "" {
		t.Fatalf("got cover=%q title=%q without an engine", cover, title)
	}
	if tokenCalls.Load() != 0 {
		t.Fatalf("asked the engine for a token %d times without an engine binary", tokenCalls.Load())
	}
}

// The reporter's save (#1940): a private playlist, oEmbed does not know it and
// the Web API answers 429. The engine plays that very playlist and names it in
// its own /status, so the key gets the name without the Web API being asked.
// The cover still comes from the Web API, here throttled.
func TestPlaylistMetaTakesThePlayingPlaylistsNameFromTheEngine(t *testing.T) {
	for _, tc := range []struct {
		name, playing, want string
	}{
		{"same playlist", "spotify:playlist:priv", "Mj Lenderman universe"},
		{"engine plays another", "spotify:playlist:other", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var apiCalls atomic.Int32
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/status":
					_, _ = w.Write([]byte(`{"context_uri":"` + tc.playing + `","context_name":"Mj Lenderman universe"}`))
				case "/token":
					_, _ = w.Write([]byte(`{"token":"BQ-test-token"}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer engine.Close()
			oembed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "Not found", http.StatusNotFound)
			}))
			defer oembed.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				apiCalls.Add(1)
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer api.Close()

			m := productTestManager(t, engine.URL)
			cover, title := m.playlistMetaAt(context.Background(), "spotify:playlist:priv", oembed.URL, api.URL)
			if title != tc.want || cover != "" {
				t.Fatalf("got cover=%q title=%q, want title %q", cover, title, tc.want)
			}
			if apiCalls.Load() != 1 {
				t.Fatalf("Web API asked %d times, want once for the cover", apiCalls.Load())
			}
		})
	}
}

// An artist page in the Spotify app plays the artist's popular tracks as a
// list that wraps the artist. Neither oEmbed nor the Web API has a page for
// that list, so nobody was asked and a key saved from an artist page stayed
// "Spotify" (#1940). The name the engine has for the list comes first (it is
// what the speaker's own Spotify shows); without one, the artist's page names
// the key and gives it the artist's picture.
func TestPlaylistMetaNamesAnArtistsPopularTracks(t *testing.T) {
	const list = "spotify:list:popular-release-segments-main-roles:artist_0aBcDeFgHiJkLmNoPqRsTu"
	for _, tc := range []struct {
		name, engineName, want string
	}{
		{"engine names the list", "Some Artist Popular", "Some Artist Popular"},
		{"engine has no name", "", "Some Artist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var apiCalls atomic.Int32
			engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/status":
					_, _ = w.Write([]byte(`{"context_uri":"` + list + `","context_name":"` + tc.engineName + `"}`))
				case "/token":
					_, _ = w.Write([]byte(`{"token":"BQ-test-token"}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer engine.Close()
			oembed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("url"); got != "https://open.spotify.com/artist/0aBcDeFgHiJkLmNoPqRsTu" {
					t.Errorf("oEmbed asked for %q, want the artist's page", got)
				}
				_, _ = w.Write([]byte(`{"title":"Some Artist","thumbnail_url":"https://i.scdn.co/image/artist"}`))
			}))
			defer oembed.Close()
			api := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { apiCalls.Add(1) }))
			defer api.Close()

			m := productTestManager(t, engine.URL)
			cover, title := m.playlistMetaAt(context.Background(), list, oembed.URL, api.URL)
			if cover != "https://i.scdn.co/image/artist" || title != tc.want {
				t.Fatalf("got cover=%q title=%q, want the artist's picture and %q", cover, title, tc.want)
			}
			if apiCalls.Load() != 0 {
				t.Fatalf("Web API asked %d times although oEmbed answered", apiCalls.Load())
			}
		})
	}
}

// oEmbed does not answer for the artist either: the Web API is asked for the
// artist, not for the list it has no page for.
func TestPlaylistMetaAsksTheWebAPIForTheWrappedArtist(t *testing.T) {
	var tokenCalls atomic.Int32
	engine := metaTestEngine(t, &tokenCalls)
	defer engine.Close()
	oembed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer oembed.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artists/0aBcDeFgHiJkLmNoPqRsTu" {
			t.Errorf("Web API asked for %q", r.URL.Path)
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"name":"Some Artist","images":[{"url":"https://i.scdn.co/image/a300","width":300}]}`))
	}))
	defer api.Close()

	m := productTestManager(t, engine.URL)
	cover, title := m.playlistMetaAt(context.Background(),
		"spotify:list:popular-release-segments-main-roles:artist_0aBcDeFgHiJkLmNoPqRsTu", oembed.URL, api.URL)
	if cover != "https://i.scdn.co/image/a300" || title != "Some Artist" {
		t.Fatalf("got cover=%q title=%q", cover, title)
	}
}

func TestMetaPageURI(t *testing.T) {
	for _, tc := range []struct{ uri, want string }{
		{"spotify:list:popular-release-segments-main-roles:artist_0aBcDeFgHiJkLmNoPqRsTu", "spotify:artist:0aBcDeFgHiJkLmNoPqRsTu"},
		// A list that wraps nothing it could be looked up by stays as it is.
		{"spotify:list:jam-list:163dc0978f246346e7f1d14a4dd2bb9d", "spotify:list:jam-list:163dc0978f246346e7f1d14a4dd2bb9d"},
		{"spotify:list:some-list:artist_", "spotify:list:some-list:artist_"},
		{"spotify:list:some-list:artist_x/../y", "spotify:list:some-list:artist_x/../y"},
		// Contexts that are their own page.
		{"spotify:playlist:abc", "spotify:playlist:abc"},
		{"spotify:artist:abc", "spotify:artist:abc"},
		{"", ""},
	} {
		if got := metaPageURI(tc.uri); got != tc.want {
			t.Errorf("metaPageURI(%q) = %q, want %q", tc.uri, got, tc.want)
		}
	}
}
