// App-side internet-radio search. Per the app-first direction, radio-browser
// queries run in the desktop app (reliable internet, real CPU) instead of on
// the constrained box. The box only ever receives the final stream URL to play.
// The agent keeps its own /api/radio for the browser-direct-to-box case, but
// the desktop app no longer routes search through the box (that made the box's
// flaky internet/DNS gate search, the HTTP 502s in #121).
package main

import (
	"context"
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

// RadioSearch runs a station search/top-list directly against radio-browser.
// For a free-text query it uses SearchSmart (name + tag fallback); for the top
// list (no query) it uses a plain vote-ordered Search. Returns the raw station
// list the frontend already knows how to render.
func (a *App) RadioSearch(o RadioSearchOpts) ([]radiobrowser.Station, error) {
	ctx, cancel := a.radioCtx()
	defer cancel()
	opts := radiobrowser.SearchOpts{
		Name:     o.Q,
		Tag:      o.Tag,
		Country:  o.Country,
		Language: o.Language,
		Order:    o.Order,
		Limit:    o.Limit,
		Offset:   o.Offset,
		OnlyOK:   o.OnlyOK,
	}
	if !o.Top && o.Q != "" {
		st, err := radioClient.SearchSmart(ctx, opts)
		return nonNilStations(st), err
	}
	st, err := radioClient.Search(ctx, opts)
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
