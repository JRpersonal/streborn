// Package radiobrowser ist ein duenner Client fuer https://radio-browser.info,
// eine community Internet Radio Station Datenbank ohne API Key.
//
// API Doku: https://api.radio-browser.info/
//
// Wir nutzen mehrere Mirror Server mit automatischem Failover. Die Liste
// ist hardcoded da die offizielle Server Discovery selbst einen Mirror
// braucht — Henne-Ei. Mirrors sind alphabetisch nach Latenz aus DE/EU
// Sicht sortiert.
package radiobrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
//                        whichever server is currently alive (one
//                        request away from being self-healing if
//                        radio-browser adds new servers).
//   - `91.98.4.78`     — de1's current IPv4 hard-coded as last-
//                        resort if DNS itself is down on the
//                        speaker's network.
var Mirrors = []string{
	"https://de1.api.radio-browser.info/json",
	"https://all.api.radio-browser.info/json",
	"https://91.98.4.78/json",
}

// Station beschreibt einen einzelnen Sender wie er von der API kommt.
//
// LastCheckOK ist 1 wenn radio-browser den Sender beim letzten Check
// erreicht hat. ClickTrend ist der Aenderungstrend ueber 24h.
// Favicon ist eine URL zu einem Logo, kann fehlen.
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
	Votes       int    `json:"votes"`
	ClickCount  int    `json:"clickcount"`
	ClickTrend  int    `json:"clicktrend"`
	LastCheckOK int    `json:"lastcheckok"`
}

// Tag ist ein Genre Tag mit Anzahl der Stations.
type Tag struct {
	Name           string `json:"name"`
	StationCount   int    `json:"stationcount"`
}

// Language listet eine Sprache plus Sender Anzahl.
type Language struct {
	Name         string `json:"name"`
	Iso639       string `json:"iso_639"`
	StationCount int    `json:"stationcount"`
}

// Client kapselt http.Client plus Mirror Rotation.
type Client struct {
	HTTP    *http.Client
	UA      string
	mu      sync.Mutex
	mirrors []string
}

// New erzeugt einen Client mit defaults und allen Mirrors.
func New() *Client {
	mirrors := make([]string, len(Mirrors))
	copy(mirrors, Mirrors)
	return &Client{
		HTTP:    &http.Client{Timeout: 8 * time.Second},
		UA:      "SoundTouchReborn/1.0",
		mirrors: mirrors,
	}
}

// SearchOpts buendelt alle Such Parameter.
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
}

// Search sucht Stations nach den uebergebenen Optionen.
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
	q.Set("limit", fmt.Sprintf("%d", opts.Limit))
	if opts.Offset > 0 {
		q.Set("offset", fmt.Sprintf("%d", opts.Offset))
	}
	q.Set("hidebroken", "true")
	if opts.OnlyOK {
		q.Set("lastcheckok", "true")
	}
	// reverse=true bedeutet descending: bei votes/clickcount/clicktrend
	// will man die hoechsten zuerst. Bei "name" wuerde reverse aus A->Z
	// ein Z->A machen, also dort weglassen.
	if order != "name" {
		q.Set("reverse", "true")
	}
	var out []Station
	err := c.fetchJSON(ctx, "/stations/search?"+q.Encode(), &out)
	return out, err
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

// TopVote liefert die meistgevoteten Sender, gefiltert nach country.
func (c *Client) TopVote(ctx context.Context, cc string, limit int, onlyOK bool) ([]Station, error) {
	return c.Search(ctx, SearchOpts{
		Country: cc,
		Order:   "votes",
		Limit:   limit,
		OnlyOK:  onlyOK,
	})
}

// ByCountry liefert alle Sender eines Landes (gut fuer Stoebern).
func (c *Client) ByCountry(ctx context.Context, cc string, limit int) ([]Station, error) {
	return c.Search(ctx, SearchOpts{Country: cc, Limit: limit, Order: "votes"})
}

// TopTags liefert die populaersten Tags fuer Genre Chips. Limit hier ist
// nicht ein API Parameter sondern eine clientseitige Begrenzung.
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

// Languages liefert die Sprach Liste (mit Anzahl der Stations).
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

// LanguagesByCountry holt alle Stations fuer einen Country Code und
// aggregiert by language Feld. radio-browser.info hat keinen direkten
// "languages by country" Endpoint, daher der Workaround. Pro Land sind
// das ca. 1k bis 5k Stations — ein JSON Payload von 1 MB max, im
// Server lokal aggregierbar in unter 100 ms.
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
		// language Feld kann mehrere kommagetrennte Sprachen enthalten
		// ("german,english"). Wir zaehlen jede einzeln damit Stationen
		// die mehrere Sprachen tragen in jeder Sprache mitgezaehlt werden.
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
	// Sort by stationcount desc damit das Frontend nicht selbst sortieren muss
	sort.Slice(out, func(i, j int) bool {
		if out[i].StationCount != out[j].StationCount {
			return out[i].StationCount > out[j].StationCount
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Vote schickt einen Daumen hoch fuer den Sender. Best Effort: Fehler
// wird zurueckgegeben aber kann ignoriert werden.
func (c *Client) Vote(ctx context.Context, uuid string) error {
	var out map[string]any
	return c.fetchJSON(ctx, "/vote/"+url.PathEscape(uuid), &out)
}

// Click traegt einen Click fuer den Sender ein. Damit zaehlt unsere App
// in die Beliebtheits Statistik mit ein.
func (c *Client) Click(ctx context.Context, uuid string) error {
	var out map[string]any
	return c.fetchJSON(ctx, "/url/"+url.PathEscape(uuid), &out)
}

// perMirrorTimeout caps every individual attempt against a single
// mirror. Smaller than the outer handler ctx so several mirrors can
// be tried within the user-perceived wait — without this the first
// slow mirror used to consume the entire 8 s handler budget and
// failover was effectively dead.
const perMirrorTimeout = 3 * time.Second

// fetchJSON probiert alle Mirrors der Reihe nach. Erster erfolgreicher
// gewinnt. Bei Erfolg merken wir uns den Mirror als "primary" damit der
// naechste Call denselben Server bevorzugt.
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
			// Erfolg: Mirror nach vorne sortieren wenn nicht schon erster
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
		lastErr = fmt.Errorf("kein mirror erreichbar")
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
	resp, err := c.HTTP.Do(req)
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
