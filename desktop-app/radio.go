// App-side internet-radio search. Per the app-first direction, radio-browser
// queries run in the desktop app (reliable internet, real CPU) instead of on
// the constrained box. The box only ever receives the final stream URL to play.
// The agent no longer serves /api/radio at all and no longer compiles the
// radiobrowser package (see internal/webui Run): routing search through the
// box made its flaky internet/DNS gate search, the HTTP 502s in #121.
package main

import (
	"context"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/radiobrowser"
)

// radioClient is shared across calls so the mirror-rotation (sticky primary)
// carries over between searches.
var radioClient = radiobrowser.New()

// RadioSearchOpts mirrors the search filters the frontend builds. JSON tags
// match the keys the old /api/radio query string used so the frontend change
// is mechanical.
type RadioSearchOpts struct {
	Q        string `json:"q"`
	Country  string `json:"cc"`
	Language string `json:"lang"`
	Tag      string `json:"tag"`
	Order    string `json:"order"`
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
	OnlyOK   bool   `json:"onlyok"`
	Top      bool   `json:"top"` // true = top list (no name query)
}

func (a *App) radioCtx() (context.Context, context.CancelFunc) {
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, 20*time.Second)
}

// searchOptsFrom maps the frontend's search filters onto the radiobrowser
// package's options. Shared by RadioSearch and RadioSearchDetailed so the two
// bindings cannot drift on filter semantics.
func searchOptsFrom(o RadioSearchOpts) radiobrowser.SearchOpts {
	return radiobrowser.SearchOpts{
		Name:     o.Q,
		Tag:      o.Tag,
		Country:  o.Country,
		Language: o.Language,
		Order:    o.Order,
		Limit:    o.Limit,
		Offset:   o.Offset,
		OnlyOK:   o.OnlyOK,
		// A free-text/name search is how a user looks for a station they just
		// added to radio-browser. Keep not-yet-checked stations in the results so
		// their own entry is findable and assignable right away instead of being
		// hidden for hours by hidebroken (#252). Browse/top lists stay strict.
		// The radiobrowser package applies this on name/tag searches regardless
		// of OnlyOK, which then only drops stations checked AND found broken -
		// never-checked ones stay visible (#267).
		IncludeUnchecked: !o.Top && o.Q != "",
	}
}

// RadioSearch runs a station search/top-list directly against radio-browser.
// For a free-text query it uses SearchSmart (name + tag fallback); for the top
// list (no query) it uses a plain vote-ordered Search. Returns the raw station
// list the frontend already knows how to render.
func (a *App) RadioSearch(o RadioSearchOpts) ([]radiobrowser.Station, error) {
	ctx, cancel := a.radioCtx()
	defer cancel()
	opts := searchOptsFrom(o)
	if !o.Top && o.Q != "" {
		st, err := radioClient.SearchSmart(ctx, opts)
		// Borrow a logo from a sibling result for the same station so a
		// vote-leading entry with an empty favicon (e.g. Couleur 3) still shows
		// one. Runs before the frontend renders AND saves from the list, so a
		// preset saved from search also keeps the borrowed logo.
		return radiobrowser.EnrichSiblingLogos(nonNilStations(st)), err
	}
	st, err := radioClient.Search(ctx, opts)
	return radiobrowser.EnrichSiblingLogos(nonNilStations(st)), err
}

// RadioSearchResult is the detailed search reply for the frontend: the station
// list plus whether the broken-station filters had to be relaxed to produce it
// (so the UI can badge entries radio-browser's checker flagged broken, e.g.
// geo-fenced streams that play fine locally).
type RadioSearchResult struct {
	Stations []radiobrowser.Station `json:"stations"`
	Relaxed  bool                   `json:"relaxed"`
}

// RadioSearchDetailed is RadioSearch plus the relaxed-filters flag: same opts
// mapping, but through the radiobrowser package's SearchDetailed so the
// frontend learns when the list only exists because the reachability filters
// were dropped.
func (a *App) RadioSearchDetailed(o RadioSearchOpts) (RadioSearchResult, error) {
	ctx, cancel := a.radioCtx()
	defer cancel()
	res, err := radioClient.SearchDetailed(ctx, searchOptsFrom(o))
	return RadioSearchResult{
		Stations: radiobrowser.EnrichSiblingLogos(nonNilStations(res.Stations)),
		Relaxed:  res.Relaxed,
	}, err
}

// RadioStationsByURL resolves a pasted stream URL to its radio-browser station
// entries (name, logo, UUID) via the stations/byurl lookup. An empty input
// short-circuits to an empty list without a network round trip.
func (a *App) RadioStationsByURL(streamURL string) ([]radiobrowser.Station, error) {
	if strings.TrimSpace(streamURL) == "" {
		return []radiobrowser.Station{}, nil
	}
	ctx, cancel := a.radioCtx()
	defer cancel()
	st, err := radioClient.ByURL(ctx, streamURL)
	return nonNilStations(st), err
}

// RadioTags returns the most popular genre tags for the chips.
func (a *App) RadioTags(limit int) ([]radiobrowser.Tag, error) {
	ctx, cancel := a.radioCtx()
	defer cancel()
	t, err := radioClient.TopTags(ctx, limit)
	if t == nil {
		t = []radiobrowser.Tag{}
	}
	return t, err
}

// RadioLanguages returns languages, scoped to a country when one is given
// (counts then reflect stations in that country, like the old endpoint).
func (a *App) RadioLanguages(country string, limit int) ([]radiobrowser.Language, error) {
	ctx, cancel := a.radioCtx()
	defer cancel()
	var (
		l   []radiobrowser.Language
		err error
	)
	if country != "" {
		l, err = radioClient.LanguagesByCountry(ctx, country)
	} else {
		l, err = radioClient.Languages(ctx, limit)
	}
	if l == nil {
		l = []radiobrowser.Language{}
	}
	return l, err
}

// RadioVote and RadioClick feed radio-browser's popularity stats. Best-effort;
// the frontend ignores failures.
func (a *App) RadioVote(uuid string) error {
	ctx, cancel := a.radioCtx()
	defer cancel()
	return radioClient.Vote(ctx, uuid)
}

func (a *App) RadioClick(uuid string) error {
	ctx, cancel := a.radioCtx()
	defer cancel()
	return radioClient.Click(ctx, uuid)
}

// nonNilStations makes Wails marshal an empty list as [] rather than null so
// the frontend can always treat the result as an array.
func nonNilStations(s []radiobrowser.Station) []radiobrowser.Station {
	if s == nil {
		return []radiobrowser.Station{}
	}
	return s
}
