package webui

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/presets"
)

// #1467: a station saved from the phone page carries the one radio-browser
// favicon the page has, often http or none at all, while the desktop app saves
// the whole logo chain (logos.js stationLogoCandidates). The agent completes
// the chain so a key holds the same art whichever client saved it.

func TestCompleteStationArt(t *testing.T) {
	const stream = "https://stream.jazzlounge.example/live.mp3"
	const home = "https://www.jazzlounge.example/"
	ddg := func(h string) string { return ddgIconPrefix + h + ".ico" }
	cases := []struct {
		name, art, home, stream, want string
	}{
		{"no favicon: the icon chain alone, homepage hosts first", "", home, stream,
			ddg("www.jazzlounge.example") + "|" + ddg("jazzlounge.example") + "|" + ddg("stream.jazzlounge.example")},
		{"https favicon stays first", "https://cdn.example/jl.png", home, "",
			"https://cdn.example/jl.png|" + ddg("www.jazzlounge.example") + "|" + ddg("jazzlounge.example")},
		{"http favicon goes last", "http://jazzlounge.example/fav.ico", home, "",
			ddg("www.jazzlounge.example") + "|" + ddg("jazzlounge.example") + "|http://jazzlounge.example/fav.ico"},
		{"a chain the app built is left alone", "https://cdn.example/jl.png|" + ddg("x.example"), home, stream,
			"https://cdn.example/jl.png|" + ddg("x.example")},
		{"two-label host has no separate root", "", "https://radio.example", "",
			ddg("radio.example")},
		{"LAN hosts are never looked up", "http://192.0.2.60:8200/a.jpg", "", "http://192.0.2.60:8200/s.mp3",
			"http://192.0.2.60:8200/a.jpg"},
		{"local names are never looked up", "", "http://nas.local/", "http://nas:8000/live",
			""},
		// #1773: "Radio Eure du Blues" streams from a *.ddns.net host and got
		// ddns.net's favicon. Neither the host nor its root domain is looked up.
		{"a dynamic-DNS stream host is never looked up", "", "https://www.radio-eure.example/", "http://radioeure.ddns.net:8000/stream",
			ddg("www.radio-eure.example") + "|" + ddg("radio-eure.example")},
		{"only dynamic-DNS hosts: nothing to look up", "", "", "http://blues.duckdns.org/live",
			""},
		{"the dyndns family under any top-level domain", "", "http://radio.dyndns.info/", "http://x.dnsalias.net/live",
			""},
		{"a chain an older app saved loses the provider's icon", "https://cdn.example/jl.png|" + ddg("radioeure.ddns.net") + "|" + ddg("ddns.net"), "", "http://radioeure.ddns.net:8000/stream",
			"https://cdn.example/jl.png"},
		{"a tunnel host is never looked up", "", "", "https://abc123.trycloudflare.com/live",
			""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := completeStationArt(c.art, c.home, c.stream); got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

// #1773: the art proxy refuses the icon service's picture of a shared or
// dynamic-DNS host without fetching it, so a key saved before the rule and the
// phone page's own candidates fall through to the letter tile.
func TestArtProxyRefusesTheIconOfASharedHost(t *testing.T) {
	fetched := false
	saved := artFetchClient
	artFetchClient = &http.Client{Transport: sharedHostArtTrip(func(*http.Request) (*http.Response, error) {
		fetched = true
		return nil, http.ErrHandlerTimeout
	})}
	defer func() { artFetchClient = saved }()
	s := &Server{logger: slog.New(slog.DiscardHandler)}
	for _, host := range []string{"ddns.net", "radioeure.ddns.net", "x.dyndns.org", "192.0.2.7"} {
		enc := base64.RawURLEncoding.EncodeToString([]byte(ddgIconPrefix + host + ".ico"))
		rec := httptest.NewRecorder()
		s.handleArt(rec, httptest.NewRequest(http.MethodGet, "/art?u="+enc+"&to=browser", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", host, rec.Code)
		}
	}
	if fetched {
		t.Error("the icon of a shared host was fetched")
	}
	if ddgSharedHostIcon(ddgIconPrefix + "www.radio-eure.example.ico") {
		t.Error("a station's own host must still be looked up")
	}
}

type sharedHostArtTrip func(*http.Request) (*http.Response, error)

func (f sharedHostArtTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The phone page's own save (Find tab Save, hold on a station it started):
// the same station must come out with the desktop app's chain.
func TestPresetSaveCompletesAPhoneStationsLogoChain(t *testing.T) {
	s := newSavePlayingServer(t, nil)
	saved, ref := s.savePresetSlot(context.Background(), presetSaveRequest{Slot: 1, Preset: presets.Preset{
		Type: "radio", Name: "Jazz Lounge", StreamURL: savePlayingStation,
		Art: "http://www.jazzlounge.example/fav.png", Homepage: "https://www.jazzlounge.example",
	}})
	if ref != nil {
		t.Fatalf("refused: %+v", ref)
	}
	want := ddgIconPrefix + "www.jazzlounge.example.ico|" + ddgIconPrefix + "jazzlounge.example.ico|http://www.jazzlounge.example/fav.png"
	if saved.Art != want {
		t.Fatalf("art %q, want %q", saved.Art, want)
	}
	if p, _ := s.presets.Get(1); p.Art != want {
		t.Fatalf("stored art %q", p.Art)
	}
}

// A media-server song keeps exactly the cover it came with.
func TestPresetSaveLeavesALibraryCoverAlone(t *testing.T) {
	s := newSavePlayingServer(t, nil)
	saved, ref := s.savePresetSlot(context.Background(), presetSaveRequest{Slot: 2, Preset: presets.Preset{
		Type: "radio", Name: "Song", StreamURL: savePlayingStation, Source: "Demo Server",
		Art: "http://192.0.2.60:8200/cover.jpg", Homepage: "https://www.example.org",
	}})
	if ref != nil {
		t.Fatalf("refused: %+v", ref)
	}
	if saved.Art != "http://192.0.2.60:8200/cover.jpg" {
		t.Fatalf("art %q", saved.Art)
	}
}

// A hold on the phone for a station the phone started: the agent's record of
// the play has the favicon and the homepage, and the key gets the full chain,
// the same one the desktop app's long-press stores.
func TestSavePlayingCompletesTheStationLogoChain(t *testing.T) {
	loc := boxurl.RawStream(savePlayingStation)
	s := newSavePlayingServer(t, nowPlayingDoc("UPNP", loc, "live.mp3", "http://127.0.0.1:8888/icon.png"))
	s.play.last = &lastPlayInfo{boxURL: loc, title: "Jazz Lounge", art: "",
		homepage: "https://www.jazzlounge.example", ts: time.Now()}
	if rec, _ := postSavePlaying(t, s, 4); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	want := ddgIconPrefix + "www.jazzlounge.example.ico|" + ddgIconPrefix + "jazzlounge.example.ico"
	if p, _ := s.presets.Get(4); p.Art != want || p.Name != "Jazz Lounge" {
		t.Fatalf("stored %+v, want art %q", p, want)
	}
}
