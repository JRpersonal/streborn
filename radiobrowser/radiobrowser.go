// Package radiobrowser is a thin client for https://radio-browser.info,
// a community internet radio station database without an API key.
//
// API docs: https://api.radio-browser.info/
//
// We use several mirror servers with automatic failover. The list is
// hardcoded because the official server discovery itself needs a mirror
// — chicken-and-egg. Mirrors are sorted by latency from a DE/EU
// perspective.
package radiobrowser

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Mirrors are the servers we try in order. First success wins; on
// error or timeout we move to the next. Order is kept across calls
// and the most recent successful mirror moves to the front, so a
// stable mirror stays primary as long as it answers.
//
// As of 2026-05-17 radio-browser.info has consolidated their
// infrastructure: their own discovery endpoint
// (https://all.api.radio-browser.info/json/servers) only returns
// de1.api.radio-browser.info as an alive server — de2/at1/nl1/fi1
// no longer resolve. The hardcoded list mirrors that reality:
//   - `de1.api...`     — the named primary
//   - `all.api...`     — DNS round-robin fallback that resolves to
//     whichever server is currently alive (one
//     request away from being self-healing if
//     radio-browser adds new servers).
//   - `91.98.4.78`     — de1's current IPv4 hard-coded as last-
//     resort if DNS itself is down on the
//     speaker's network.
var Mirrors = []string{
	"https://de1.api.radio-browser.info/json",
	"https://all.api.radio-browser.info/json",
	"https://91.98.4.78/json",
}

// Station describes a single station as it comes from the API.
//
// LastCheckOK is 1 if radio-browser reached the station on the last
// check. ClickTrend is the change trend over 24h. Favicon is a URL to a
// logo, may be missing.
type Station struct {
	StationUUID string `json:"stationuuid"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	URLResolved string `json:"url_resolved"`
	Favicon     string `json:"favicon"`
	Homepage    string `json:"homepage"`
	Tags        string `json:"tags"`
	Country     string `json:"country"`
	CountryCode string `json:"countrycode"`
	Language    string `json:"language"`
	State       string `json:"state"`
	Codec       string `json:"codec"`
	Bitrate     int    `json:"bitrate"`
	// Hls is radio-browser's flag for HLS (.m3u8) stations (1 = HLS). STR
	// converts HLS playlists on the fly (v0.7.21), so the desktop's
	// Bose-compatible filter treats these as playable; without the field the
	// frontend could not tell HLS stations apart and hid them (#124).
	Hls         int `json:"hls"`
	Votes       int `json:"votes"`
	ClickCount  int `json:"clickcount"`
	ClickTrend  int `json:"clicktrend"`
	LastCheckOK int `json:"lastcheckok"`
	// LastCheckTime is when radio-browser last checked the stream. A station a
	// user just added has NOT been checked yet, so this is empty / all-zeros AND
	// LastCheckOK is 0. That combination must be told apart from a station that
	// WAS checked and failed (broken): the former is a fresh, likely-good entry
	// the user is trying to find, the latter is genuinely dead. See neverChecked
	// and IncludeUnchecked (#252: a self-added station was invisible in search).
	LastCheckTime string `json:"lastchecktime"`
}

// neverChecked reports whether radio-browser has not yet run its stream check on
// this station (a just-added entry). radio-browser reports the never-checked time
// as an empty string or an all-zero timestamp depending on the mirror.
func neverChecked(s Station) bool {
	t := strings.TrimSpace(s.LastCheckTime)
	return t == "" || strings.HasPrefix(t, "0000-00-00")
}

// Tag is a genre tag with the number of stations.
type Tag struct {
	Name         string `json:"name"`
	StationCount int    `json:"stationcount"`
}

// Language lists a language plus station count.
type Language struct {
	Name         string `json:"name"`
	Iso639       string `json:"iso_639"`
	StationCount int    `json:"stationcount"`
}

// ipMirrorServerName is the hostname the hard-coded IP fallback mirror really
// serves a TLS certificate for. Connecting to the bare IP with the default
// client fails verification (the cert is for de1.api..., not the IP), so that
// last-resort mirror used to ALWAYS error — leaving effectively two working
// mirrors and a 502 to the user (radio search HTTP 502, #121) whenever the
// primary was momentarily slow. Pinning ServerName lets the real cert validate
// while still dialling the IP, so the fallback actually works.
const ipMirrorServerName = "de1.api.radio-browser.info"

// Client wraps http.Client plus mirror rotation.
type Client struct {
	HTTP    *http.Client // standard client for hostname mirrors
	ipHTTP  *http.Client // client for IP-literal mirrors (TLS ServerName pinned)
	UA      string
	mu      sync.Mutex
	mirrors []string
}

// New creates a Client with defaults and all mirrors.
func New() *Client {
	mirrors := make([]string, len(Mirrors))
	copy(mirrors, Mirrors)
	return &Client{
		HTTP: &http.Client{Timeout: 8 * time.Second},
		ipHTTP: &http.Client{
			Timeout: 8 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{ServerName: ipMirrorServerName},
			},
		},
		// radio-browser asks clients to identify themselves via a
		// descriptive User-Agent (it has no referral mechanism). Naming
		// the project URL lets the radio-browser maintainers see where
		// the traffic and the station clicks come from.
		UA:      "ST-Reborn/1.0 (+https://st-reborn.de)",
		mirrors: mirrors,
	}
}

// SearchOpts bundles all search parameters.
//
// TagList is a list of substrings, each of which must match SOME tag
// of a station (AND across the list, substring per element). Set this
// for multi-word queries — see SearchSmart.
type SearchOpts struct {
	Name     string
	Tag      string
	TagList  []string
	Country  string
	Language string
	Order    string
	Limit    int
	Offset   int
	OnlyOK   bool
	// IncludeUnchecked keeps stations radio-browser has not checked yet in the
	// results. By default every search sends hidebroken=true, which hides any
	// station whose last check failed OR that has never been checked — so a
	// station the user just added does not appear until radio-browser's checker
	// gets to it (hours later). With this set, the search drops hidebroken and
	// instead filters client-side, keeping reachable AND never-checked stations
	// while still dropping the genuinely checked-and-broken ones. Ignored when
	// OnlyOK is set (the user explicitly asked for confirmed-working only).
	// Set for name searches so a freshly self-added station is findable (#252).
	IncludeUnchecked bool
}

// Search searches stations according to the given options.
func (c *Client) Search(ctx context.Context, opts SearchOpts) ([]Station, error) {
	if opts.Limit <= 0 {
		opts.Limit = 30
	}
	q := url.Values{}
	if opts.Name != "" {
		q.Set("name", opts.Name)
	}
	if opts.Tag != "" {
		q.Set("tag", opts.Tag)
	}
	if len(opts.TagList) > 0 {
		q.Set("tagList", strings.Join(opts.TagList, ","))
	}
	if opts.Country != "" {
		q.Set("countrycode", strings.ToUpper(opts.Country))
	}
	if opts.Language != "" {
		q.Set("language", strings.ToLower(opts.Language))
	}
	order := opts.Order
	if order == "" {
		order = "votes"
	}
	q.Set("order", order)
	// Keep unchecked stations in play by filtering client-side (below) instead of
	// letting hidebroken drop a just-added station server-side. Fetch a wider page
	// then so the checked-and-broken rows we drop do not thin the visible window.
	relaxUnchecked := opts.IncludeUnchecked && !opts.OnlyOK
	fetchLimit := opts.Limit
	if relaxUnchecked {
		fetchLimit = opts.Limit * 3
		if fetchLimit > 200 {
			fetchLimit = 200
		}
	}
	q.Set("limit", fmt.Sprintf("%d", fetchLimit))
	if opts.Offset > 0 {
		q.Set("offset", fmt.Sprintf("%d", opts.Offset))
	}
	if !relaxUnchecked {
		q.Set("hidebroken", "true")
	}
	if opts.OnlyOK {
		q.Set("lastcheckok", "true")
	}
	// reverse=true means descending: for votes/clickcount/clicktrend you
	// want the highest first. For "name" reverse would turn A->Z into
	// Z->A, so leave it out there.
	if order != "name" {
		q.Set("reverse", "true")
	}
	var out []Station
	if err := c.fetchJSON(ctx, "/stations/search?"+q.Encode(), &out); err != nil {
		return out, err
	}
	if relaxUnchecked {
		out = keepReachableOrUnchecked(out)
		if len(out) > opts.Limit {
			out = out[:opts.Limit]
		}
	}
	return out, nil
}

// keepReachableOrUnchecked drops only the stations radio-browser has checked and
// found broken (LastCheckOK==0 and it HAS been checked), keeping reachable ones
// and never-checked ones. This is the client-side equivalent of hidebroken=true
// minus the "hide the just-added station" side effect. Order is preserved.
func keepReachableOrUnchecked(in []Station) []Station {
	out := in[:0]
	for _, s := range in {
		if s.LastCheckOK == 1 || neverChecked(s) {
			out = append(out, s)
		}
	}
	return out
}

// SearchSmart runs Search with the literal Name plus a tag-based
// fallback for multi-word queries and merges the results. It exists
// because radio-browser's `name` parameter is a plain substring match
// against the station name field — multi-word queries like
// "rap old school" almost never appear there literally, so users see
// an empty list even though plenty of stations are tagged that way.
//
// Strategy:
//
//  1. Run the original Search (matches station names).
//  2. If Name has two or more whitespace-separated tokens, run a
//     second Search with TagList set to those tokens. radio-browser
//     AND-matches the list, substring per element — so a station
//     tagged "hip hop, rap, old school" satisfies all three of
//     {rap, old, school}.
//
// Both queries respect the other filters (Country, Language, OnlyOK,
// Order). Results are merged, deduplicated by station UUID, capped
// at opts.Limit, and returned in vote order. Callers that have
// already constrained the search to a tag chip (Tag != "") get the
// plain Search behaviour — the user already narrowed the scope and
// we should not widen it back.
func (c *Client) SearchSmart(ctx context.Context, opts SearchOpts) ([]Station, error) {
	if opts.Limit <= 0 {
		opts.Limit = 30
	}
	if opts.Name == "" || opts.Tag != "" {
		return c.Search(ctx, opts)
	}
	tokens := tokenize(opts.Name)

	type result struct {
		stations []Station
		err      error
	}
	nameCh := make(chan result, 1)
	tagCh := make(chan result, 1)

	go func() {
		st, err := c.Search(ctx, opts)
		nameCh <- result{st, err}
	}()
	if len(tokens) >= 2 {
		go func() {
			tagOpts := opts
			tagOpts.Name = ""
			tagOpts.TagList = tokens
			// Tag-based hits are often plentiful; fetch a wider
			// page so the dedup-and-cap step has material to
			// promote into the visible window.
			tagOpts.Limit = opts.Limit * 2
			st, err := c.Search(ctx, tagOpts)
			tagCh <- result{st, err}
		}()
	} else {
		close(tagCh)
	}

	nameRes := <-nameCh
	var tagStations []Station
	if r, ok := <-tagCh; ok {
		tagStations = r.stations
	}

	if nameRes.err != nil && len(tagStations) == 0 {
		return nil, nameRes.err
	}

	seen := make(map[string]bool, len(nameRes.stations)+len(tagStations))
	merged := make([]Station, 0, len(nameRes.stations)+len(tagStations))
	add := func(stations []Station) {
		for _, s := range stations {
			key := s.StationUUID
			if key == "" {
				key = s.Name + "|" + s.URL
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, s)
		}
	}
	add(nameRes.stations)
	add(tagStations)

	if len(merged) > opts.Limit {
		merged = merged[:opts.Limit]
	}
	return merged, nil
}

// EnrichSiblingLogos fills an entry's empty Favicon/Homepage from a SIBLING
// result for the same station. radio-browser routinely returns several entries
// for one station, and the vote-leading one (the row STR renders) often has an
// empty favicon while a lower-voted sibling carries a working logo, e.g.
// Couleur 3: the top entry has favicon="" and homepage couleur3.ch (which no icon
// service knows), while a sibling has favicon https://www.rts.ch/favicon.ico and
// homepage www.rts.ch (which resolves). The logo lookup is scoped to a single
// entry, so without this the strong sibling is never consulted and the tile falls
// back to a monogram.
//
// Conservative to avoid cross-contaminating different stations: siblings are
// grouped by their EXACT stream URL (url_resolved, else url). The same stream URL
// is the same audio, i.e. the same station, so this cannot pull a logo across two
// genuinely different stations the way a fuzzy name match could. Only EMPTY fields
// are filled, never overwritten; an https favicon is preferred as the donor since
// the resolver only trusts https favicons.
func EnrichSiblingLogos(stations []Station) []Station {
	keyOf := func(s Station) string {
		u := s.URLResolved
		if u == "" {
			u = s.URL
		}
		return strings.ToLower(strings.TrimSpace(u))
	}
	type donor struct{ favicon, homepage string }
	best := make(map[string]*donor, len(stations))
	isHTTPS := func(s string) bool { return strings.HasPrefix(strings.ToLower(s), "https://") }
	for _, s := range stations {
		k := keyOf(s)
		if k == "" {
			continue
		}
		d := best[k]
		if d == nil {
			d = &donor{}
			best[k] = d
		}
		if s.Favicon != "" && (d.favicon == "" || (isHTTPS(s.Favicon) && !isHTTPS(d.favicon))) {
			d.favicon = s.Favicon
		}
		if s.Homepage != "" && d.homepage == "" {
			d.homepage = s.Homepage
		}
	}
	for i := range stations {
		d := best[keyOf(stations[i])]
		if d == nil {
			continue
		}
		if stations[i].Favicon == "" && d.favicon != "" {
			stations[i].Favicon = d.favicon
		}
		if stations[i].Homepage == "" && d.homepage != "" {
			stations[i].Homepage = d.homepage
		}
	}
	return stations
}

// tokenize splits a free-text query into trimmed, non-empty,
// lowercase substrings on whitespace. Used by SearchSmart.
func tokenize(s string) []string {
	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// TopVote returns the most-voted stations, filtered by country.
func (c *Client) TopVote(ctx context.Context, cc string, limit int, onlyOK bool) ([]Station, error) {
	return c.Search(ctx, SearchOpts{
		Country: cc,
		Order:   "votes",
		Limit:   limit,
		OnlyOK:  onlyOK,
	})
}

// ByCountry returns all stations of a country (good for browsing).
func (c *Client) ByCountry(ctx context.Context, cc string, limit int) ([]Station, error) {
	return c.Search(ctx, SearchOpts{Country: cc, Limit: limit, Order: "votes"})
}

// TopTags returns the most popular tags for genre chips. Limit here is
// not an API parameter but a client-side cap.
func (c *Client) TopTags(ctx context.Context, limit int) ([]Tag, error) {
	q := url.Values{}
	q.Set("order", "stationcount")
	q.Set("reverse", "true")
	q.Set("hidebroken", "true")
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	var out []Tag
	err := c.fetchJSON(ctx, "/tags?"+q.Encode(), &out)
	return out, err
}

// Languages returns the language list (with the number of stations).
func (c *Client) Languages(ctx context.Context, limit int) ([]Language, error) {
	q := url.Values{}
	q.Set("order", "stationcount")
	q.Set("reverse", "true")
	q.Set("hidebroken", "true")
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	var out []Language
	err := c.fetchJSON(ctx, "/languages?"+q.Encode(), &out)
	return out, err
}

// LanguagesByCountry fetches all stations for a country code and
// aggregates by the language field. radio-browser.info has no direct
// "languages by country" endpoint, hence the workaround. Per country
// that is about 1k to 5k stations — a JSON payload of 1 MB max,
// aggregatable locally in the server in under 100 ms.
func (c *Client) LanguagesByCountry(ctx context.Context, country string) ([]Language, error) {
	stations, err := c.Search(ctx, SearchOpts{
		Country: country,
		OnlyOK:  true,
		Limit:   10000,
		Order:   "name",
	})
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, 64)
	for _, st := range stations {
		// the language field may contain several comma-separated languages
		// ("german,english"). We count each one individually so stations
		// carrying multiple languages are counted in each language.
		for _, raw := range strings.Split(st.Language, ",") {
			lang := strings.TrimSpace(strings.ToLower(raw))
			if lang == "" {
				continue
			}
			counts[lang]++
		}
	}
	out := make([]Language, 0, len(counts))
	for name, n := range counts {
		out = append(out, Language{Name: name, StationCount: n})
	}
	// Sort by stationcount desc so the frontend does not have to sort itself
	sort.Slice(out, func(i, j int) bool {
		if out[i].StationCount != out[j].StationCount {
			return out[i].StationCount > out[j].StationCount
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Vote sends a thumbs up for the station. Best effort: the error is
// returned but can be ignored.
func (c *Client) Vote(ctx context.Context, uuid string) error {
	var out map[string]any
	return c.fetchJSON(ctx, "/vote/"+url.PathEscape(uuid), &out)
}

// Click records a click for the station. This way our app counts toward
// the popularity statistics.
func (c *Client) Click(ctx context.Context, uuid string) error {
	var out map[string]any
	return c.fetchJSON(ctx, "/url/"+url.PathEscape(uuid), &out)
}

// perMirrorTimeout caps every individual attempt against a single
// mirror. Smaller than the outer handler ctx so several mirrors can
// be tried within the user-perceived wait — without this the first
// slow mirror used to consume the entire 8 s handler budget and
// failover was effectively dead.
const perMirrorTimeout = 6 * time.Second

// fetchJSON tries all mirrors in order. First success wins. On success
// we remember the mirror as "primary" so the next call prefers the same
// server.
//
// Each attempt runs under its own derived context with a 3 s timeout
// instead of sharing the outer handler ctx. That way a single slow
// or hung mirror cannot eat the whole budget — five mirrors at 3 s
// each fit inside the webui handler's 15 s outer timeout with margin.
func (c *Client) fetchJSON(ctx context.Context, path string, out any) error {
	c.mu.Lock()
	mirrors := append([]string(nil), c.mirrors...)
	c.mu.Unlock()

	var lastErr error
	for i, base := range mirrors {
		// Outer ctx cancelled (handler timeout fired, client went
		// away): stop trying further mirrors.
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		err := c.tryMirror(ctx, base, path, out)
		if err == nil {
			// Success: move the mirror to the front if not already first
			if i > 0 {
				c.mu.Lock()
				c.mirrors[0], c.mirrors[i] = c.mirrors[i], c.mirrors[0]
				c.mu.Unlock()
			}
			return nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no mirror reachable")
	}
	return lastErr
}

// tryMirror executes a single mirror attempt under its own short ctx.
// Returns nil on success, error otherwise (timeout, non-200, decode
// failure). Caller is responsible for falling through to the next
// mirror on error.
func (c *Client) tryMirror(parent context.Context, base, path string, out any) error {
	ctx, cancel := context.WithTimeout(parent, perMirrorTimeout)
	defer cancel()

	full := base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return err
	}
	if c.UA != "" {
		req.Header.Set("User-Agent", c.UA)
	}
	// IP-literal mirrors need the ServerName-pinned client so TLS validates
	// against the real hostname's cert instead of failing on the bare IP.
	client := c.HTTP
	if u, perr := url.Parse(base); perr == nil && net.ParseIP(u.Hostname()) != nil {
		client = c.ipHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		n := len(body)
		if n > 256 {
			n = 256
		}
		return fmt.Errorf("mirror %s: %d: %s", base, resp.StatusCode, string(body[:n]))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", base, err)
	}
	return nil
}
