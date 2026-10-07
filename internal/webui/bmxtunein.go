package webui

// TuneIn BMX adapter.
//
// The BMX registry declares TuneIn with baseUrl {BMX_SERVER}/bmx/tunein, so
// once the box reflects TUNEIN as a source, a select for a TuneIn station makes
// the firmware fetch /bmx/tunein/v1/playback/station/<guide id> (plus the
// /v1/token link) from this port. Bose's adapter behind that path is gone, so
// this one resolves the station against TuneIn's public OPML API and answers
// the same station document the orion adapter serves (lirDescriptor), which is
// the shape this firmware is known to play.
//
// The stream goes through STR's raw stream proxy exactly like an orion station
// (https upstreams the speaker cannot fetch itself, ICY titles, reconnects on
// token expiry), and the logo through the art proxy.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/netutil"
)

const (
	tuneInStationPrefix    = "/bmx/tunein/v1/playback/station/"
	tuneInTokenPath        = "/bmx/tunein/v1/token"
	tuneInNowPlayingPrefix = "/bmx/tunein/v1/now-playing/station/"
)

// tuneInStation is the station document plus the now-playing address.
//
// Experiment (#500): Bose's BMX answers carried a _links.bmx_nowplaying entry
// (the field name is in Bose's schema as other cloud replacements recorded
// it), and the firmware's TuneIn client knows a nowPlayingUri. Offering both
// shows whether this firmware then asks for the current title, which would
// put the song on the display without restarting the stream. Unknown fields
// are ignored by the firmware's parser, so a speaker that does not use them
// plays exactly as before. Every request lands in the /bmx/ log.
type tuneInStation struct {
	lirDescriptor
	Links         map[string]bmxLink `json:"_links,omitempty"`
	NowPlayingURI string             `json:"nowPlayingUri,omitempty"`
}

type bmxLink struct {
	Href              string `json:"href"`
	UseInternalClient string `json:"useInternalClient,omitempty"`
}

// tuneInNowPlaying is the answer at the now-playing address: the shape of a
// BMX playback answer (name, artist.name, imageUrl), with the song as name
// and track.name as a second spelling.
type tuneInNowPlaying struct {
	Links  map[string]bmxLink `json:"_links,omitempty"`
	Name   string             `json:"name"`
	Artist struct {
		Name string `json:"name"`
	} `json:"artist"`
	Track struct {
		Name string `json:"name"`
	} `json:"track"`
	ImageURL   string `json:"imageUrl,omitempty"`
	StreamType string `json:"streamType"`
}

var (
	// radiotimeBase is TuneIn's public OPML API. A var so tests can point it
	// at a fake server.
	radiotimeBase = "http://opml.radiotime.com"
	// radiotimeClient fetches it. SSRF-guarded like every other outbound
	// fetch the agent makes; tests swap in a plain client for a loopback fake.
	radiotimeClient = netutil.GuardedClient(8 * time.Second)

	// tuneInIDRe accepts TuneIn guide ids for stations (s), programs (p) and
	// topics (t). Strict on purpose: the id goes into an upstream query.
	tuneInIDRe = regexp.MustCompile(`^[spt][0-9]+$`)
)

// opmlEntry is the part of an OPML body element this adapter reads.
type opmlEntry struct {
	Element   string `json:"element"`
	URL       string `json:"url"`
	MediaType string `json:"media_type"`
	Name      string `json:"name"`
	Logo      string `json:"logo"`
}

type opmlResponse struct {
	Head struct {
		Status string `json:"status"`
		Fault  string `json:"fault"`
	} `json:"head"`
	Body []opmlEntry `json:"body"`
}

// fetchOPML GETs one OPML endpoint (Tune.ashx, Describe.ashx) as JSON.
func fetchOPML(r *http.Request, endpoint, id string) (*opmlResponse, error) {
	u := radiotimeBase + "/" + endpoint + "?id=" + url.QueryEscape(id) + "&render=json"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := radiotimeClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", endpoint, resp.StatusCode)
	}
	var out opmlResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: %w", endpoint, err)
	}
	if out.Head.Status != "" && out.Head.Status != "200" {
		return nil, fmt.Errorf("%s status %s: %s", endpoint, out.Head.Status, out.Head.Fault)
	}
	return &out, nil
}

// pickTuneInStream chooses the stream to play from a Tune.ashx body: the first
// http(s) audio entry in mp3 or aac, else the first http(s) entry of any kind
// (the stream proxy also follows playlists and HLS).
func pickTuneInStream(body []opmlEntry) (string, error) {
	var fallback string
	for _, e := range body {
		if e.Element != "" && e.Element != "audio" {
			continue
		}
		if netutil.SafeHTTPURL(e.URL) != nil {
			continue
		}
		switch strings.ToLower(e.MediaType) {
		case "mp3", "aac":
			return e.URL, nil
		}
		if fallback == "" {
			fallback = e.URL
		}
	}
	if fallback == "" {
		return "", errors.New("no playable stream in the TuneIn answer")
	}
	return fallback, nil
}

// handleTuneInStation serves /bmx/tunein/v1/playback/station/<id>.
func (s *Server) handleTuneInStation(r *http.Request) (int, []byte) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, tuneInStationPrefix), "/")
	if !tuneInIDRe.MatchString(id) {
		s.logger.Info("bmx tunein: station request refused, not a TuneIn id", "path", r.URL.Path)
		return bmxError(http.StatusBadRequest, "invalid station id")
	}
	tune, err := fetchOPML(r, "Tune.ashx", id)
	var stream string
	if err == nil {
		stream, err = pickTuneInStream(tune.Body)
	}
	if err != nil {
		s.logger.Info("bmx tunein: station could not be resolved", "path", r.URL.Path, "id", id, "err", err)
		return bmxError(http.StatusBadGateway, "station could not be resolved")
	}
	// Name and logo are cosmetic: a failed Describe leaves the id as the name
	// and STR's own logo on the display, and the station still plays.
	name, logo := id, ""
	if desc, derr := fetchOPML(r, "Describe.ashx", id); derr == nil {
		for _, e := range desc.Body {
			if e.Name != "" {
				name, logo = e.Name, e.Logo
				break
			}
		}
	} else {
		s.logger.Info("bmx tunein: station name lookup failed, using the id", "id", id, "err", derr)
	}
	host := ""
	if u, perr := url.Parse(stream); perr == nil {
		host = u.Host
	}
	var d tuneInStation
	d.Audio.IsRealtime = true
	d.Audio.StreamURL = boxurl.RawStream(stream)
	d.ImageURL = stationImageURL(logo)
	d.Name = name
	d.StreamType = "liveRadio"
	np := "/v1/now-playing/station/" + id
	d.Links = map[string]bmxLink{"bmx_nowplaying": {Href: np, UseInternalClient: "ALWAYS"}}
	d.NowPlayingURI = np
	// a title from the previous station must not reach this one
	s.lastPlayMu.Lock()
	s.lastICYTitle = ""
	s.tuneInStation = tuneInNowStation{id: id, name: name, image: d.ImageURL}
	s.lastPlayMu.Unlock()
	s.logger.Info("bmx tunein: station resolved", "path", r.URL.Path, "id", id, "name", name, "streamHost", host, "nowPlaying", np)
	return bmxJSON(http.StatusOK, d)
}

// handleTuneInToken answers the registry's bmx_token link for TuneIn, the same
// empty token the orion adapter serves: the firmware fetches it before using
// the service, and TuneIn's public API needs no credentials.
func (s *Server) handleTuneInToken(r *http.Request) (int, []byte) {
	s.logger.Info("bmx tunein: token served", "path", r.URL.Path)
	return http.StatusOK, []byte(`{"access_token":"","refresh_token":""}`)
}

// tuneInNowStation is the TuneIn station the speaker was last given.
type tuneInNowStation struct {
	id, name, image string
}

// handleTuneInNowPlaying answers the now-playing address with the live ICY
// title of the station the speaker plays, split into artist and song where
// the title reads "Artist - Song". Without a title yet, the station name.
func (s *Server) handleTuneInNowPlaying(r *http.Request) (int, []byte) {
	s.lastPlayMu.Lock()
	title, st := s.lastICYTitle, s.tuneInStation
	s.lastPlayMu.Unlock()
	var np tuneInNowPlaying
	np.Links = map[string]bmxLink{"self": {Href: strings.TrimPrefix(r.URL.Path, "/bmx/tunein")}}
	np.StreamType = "liveRadio"
	np.ImageURL = st.image
	artist, song := splitICYTitle(title)
	switch {
	case song != "":
		np.Name, np.Track.Name, np.Artist.Name = song, song, artist
	default:
		np.Name, np.Track.Name = st.name, st.name
	}
	s.logger.Info("bmx tunein: now-playing asked", "path", r.URL.Path, "query", r.URL.RawQuery,
		"station", st.id, "artist", np.Artist.Name, "song", np.Name)
	return bmxJSON(http.StatusOK, np)
}

// splitICYTitle reads "Artist - Song" (the common StreamTitle form). A title
// without the separator is the song alone.
func splitICYTitle(t string) (artist, song string) {
	t = strings.TrimSpace(t)
	if a, b, ok := strings.Cut(t, " - "); ok && strings.TrimSpace(a) != "" && strings.TrimSpace(b) != "" {
		return strings.TrimSpace(a), strings.TrimSpace(b)
	}
	return "", t
}

// isNowPlayingPath recognises the now-playing address in the spellings the
// firmware might use (our own, the firmware's nowPlaying route).
func isNowPlayingPath(p string) bool {
	l := strings.ToLower(p)
	return strings.HasPrefix(p, tuneInNowPlayingPrefix) || strings.Contains(l, "/nowplaying") || strings.Contains(l, "/now-playing")
}
