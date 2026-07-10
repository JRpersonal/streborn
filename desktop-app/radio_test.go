package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JRpersonal/streborn/radiobrowser"
)

// withTestRadioMirror points the package-level radioClient at a local test
// server for the duration of the test, restoring the real mirrors afterwards.
func withTestRadioMirror(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	oldMirrors := radiobrowser.Mirrors
	oldClient := radioClient
	radiobrowser.Mirrors = []string{srv.URL + "/json"}
	radioClient = radiobrowser.New()
	t.Cleanup(func() {
		radiobrowser.Mirrors = oldMirrors
		radioClient = oldClient
		srv.Close()
	})
}

func TestSearchOptsFromMapping(t *testing.T) {
	o := RadioSearchOpts{
		Q: "swiss jazz", Country: "CH", Language: "german", Tag: "jazz",
		Order: "clickcount", Limit: 12, Offset: 24, OnlyOK: true,
	}
	got := searchOptsFrom(o)
	if got.Name != o.Q || got.Tag != o.Tag || got.Country != o.Country ||
		got.Language != o.Language || got.Order != o.Order ||
		got.Limit != o.Limit || got.Offset != o.Offset || got.OnlyOK != o.OnlyOK {
		t.Errorf("searchOptsFrom dropped a filter: %+v from %+v", got, o)
	}
	// Free-text searches keep never-checked stations findable (#252/#267)...
	if !got.IncludeUnchecked {
		t.Errorf("free-text search must set IncludeUnchecked")
	}
	// ...while top/browse lists stay strict.
	if searchOptsFrom(RadioSearchOpts{Top: true}).IncludeUnchecked {
		t.Errorf("top list must not set IncludeUnchecked")
	}
	if searchOptsFrom(RadioSearchOpts{Tag: "jazz"}).IncludeUnchecked {
		t.Errorf("tag browse without a free-text query must not set IncludeUnchecked")
	}
}

func TestRadioSearchDetailedReportsRelaxed(t *testing.T) {
	// One station that radio-browser checked AND found broken: the client-side
	// filter drops it, the name-search fallback re-surfaces it marked Relaxed
	// (geo-fenced streams play fine locally, #252).
	withTestRadioMirror(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"stationuuid":"u1","name":"Geo Station","url":"http://radio.example/geo","lastcheckok":0,"lastchecktime":"2026-01-01 10:00:00"}]`))
	})
	a := &App{}
	res, err := a.RadioSearchDetailed(RadioSearchOpts{Q: "geo station", Limit: 5})
	if err != nil {
		t.Fatalf("RadioSearchDetailed: %v", err)
	}
	if !res.Relaxed {
		t.Errorf("all-broken name search must come back Relaxed")
	}
	if len(res.Stations) != 1 || res.Stations[0].StationUUID != "u1" {
		t.Errorf("stations = %+v, want the one relaxed hit", res.Stations)
	}
}

func TestRadioSearchDetailedStrictHitNotRelaxed(t *testing.T) {
	withTestRadioMirror(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"stationuuid":"u2","name":"Good Station","url":"http://radio.example/good","lastcheckok":1,"lastchecktime":"2026-01-01 10:00:00"}]`))
	})
	a := &App{}
	res, err := a.RadioSearchDetailed(RadioSearchOpts{Q: "good station", Limit: 5})
	if err != nil {
		t.Fatalf("RadioSearchDetailed: %v", err)
	}
	if res.Relaxed {
		t.Errorf("a reachable hit must not be marked Relaxed")
	}
	if res.Stations == nil {
		t.Errorf("stations must marshal as [] rather than null")
	}
	if len(res.Stations) != 1 {
		t.Errorf("stations = %+v, want one hit", res.Stations)
	}
}

func TestRadioStationsByURL(t *testing.T) {
	var gotURL string
	withTestRadioMirror(t, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Query().Get("url")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"stationuuid":"u3","name":"Pasted Station","url":"http://radio.example/stream"}]`))
	})
	a := &App{}
	st, err := a.RadioStationsByURL("http://radio.example/stream")
	if err != nil {
		t.Fatalf("RadioStationsByURL: %v", err)
	}
	if gotURL != "http://radio.example/stream" {
		t.Errorf("byurl query url = %q, want the pasted stream URL", gotURL)
	}
	if len(st) != 1 || st[0].StationUUID != "u3" {
		t.Errorf("stations = %+v, want the resolved entry", st)
	}
}

func TestRadioStationsByURLEmptyInputGuard(t *testing.T) {
	// No mirror swap on purpose: the guard must answer without any network.
	a := &App{}
	st, err := a.RadioStationsByURL("   ")
	if err != nil {
		t.Fatalf("empty input must not error, got %v", err)
	}
	if st == nil || len(st) != 0 {
		t.Errorf("empty input must yield an empty (non-nil) list, got %+v", st)
	}
}
