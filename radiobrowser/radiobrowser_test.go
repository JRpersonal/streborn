package radiobrowser

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

func TestNeverChecked(t *testing.T) {
	cases := map[string]bool{
		"":                    true,  // radio-browser omits the field for a fresh station
		"   ":                 true,  // whitespace only
		"0000-00-00 00:00:00": true,  // some mirrors report an all-zero timestamp
		"2026-07-01 09:44:00": false, // genuinely checked
	}
	for in, want := range cases {
		if got := neverChecked(Station{LastCheckTime: in}); got != want {
			t.Errorf("neverChecked(%q) = %v, want %v", in, got, want)
		}
	}
}

// keepReachableOrUnchecked is the client-side stand-in for hidebroken=true that
// does NOT hide a just-added (never-checked) station: it drops only the stations
// radio-browser checked and found broken, keeping reachable and never-checked
// ones, in order (#252).
func TestKeepReachableOrUnchecked(t *testing.T) {
	in := []Station{
		{Name: "Reachable", LastCheckOK: 1, LastCheckTime: "2026-07-01 09:44:00"},
		{Name: "JustAddedByUser", LastCheckOK: 0, LastCheckTime: ""},                     // never checked -> keep
		{Name: "CheckedButBroken", LastCheckOK: 0, LastCheckTime: "2026-06-30 12:00:00"}, // dead -> drop
		{Name: "ZeroTime", LastCheckOK: 0, LastCheckTime: "0000-00-00 00:00:00"},         // never checked -> keep
	}
	out := keepReachableOrUnchecked(in)
	got := make([]string, 0, len(out))
	for _, s := range out {
		got = append(got, s.Name)
	}
	want := []string{"Reachable", "JustAddedByUser", "ZeroTime"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order/content mismatch: got %v, want %v", got, want)
		}
	}
}

// fakeMirror is an httptest stand-in for a radio-browser mirror. It records
// every request URL and answers the i-th request with respond[i] (missing
// indices get an empty JSON array).
type fakeMirror struct {
	mu       sync.Mutex
	requests []*url.URL
	respond  map[int]string
	srv      *httptest.Server
}

func newFakeMirror(t *testing.T, respond map[int]string) *fakeMirror {
	t.Helper()
	f := &fakeMirror{respond: respond}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		idx := len(f.requests)
		u := *r.URL
		f.requests = append(f.requests, &u)
		f.mu.Unlock()
		body, ok := f.respond[idx]
		if !ok {
			body = "[]"
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// client returns a Client whose only mirror is the fake.
func (f *fakeMirror) client() *Client {
	return &Client{
		HTTP:    f.srv.Client(),
		ipHTTP:  f.srv.Client(),
		mirrors: []string{f.srv.URL + "/json"},
	}
}

func (f *fakeMirror) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeMirror) request(t *testing.T, i int) *url.URL {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.requests) {
		t.Fatalf("request %d not made (have %d)", i, len(f.requests))
	}
	return f.requests[i]
}

// Canned station rows: one confirmed-reachable, one never-checked
// (just added by a user), one checked-and-broken.
const (
	stationsMixedJSON = `[
		{"name":"Reachable","url":"http://radio.example/ok","lastcheckok":1,"lastchecktime":"2026-07-01 09:44:00"},
		{"name":"JustAdded","url":"http://radio.example/new","lastcheckok":0,"lastchecktime":""},
		{"name":"CheckedBroken","url":"http://radio.example/dead","lastcheckok":0,"lastchecktime":"2026-06-30 12:00:00"}
	]`
	stationsAllBrokenJSON = `[
		{"name":"GeoFenced","url":"http://radio.example/geo","lastcheckok":0,"lastchecktime":"2026-06-30 12:00:00"},
		{"name":"AlsoFlagged","url":"http://radio.example/geo2","lastcheckok":0,"lastchecktime":"2026-06-29 12:00:00"}
	]`
)

func names(st []Station) []string {
	out := make([]string, 0, len(st))
	for _, s := range st {
		out = append(out, s.Name)
	}
	return out
}

func equalNames(got []Station, want ...string) bool {
	g := names(got)
	if len(g) != len(want) {
		return false
	}
	for i := range want {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

// A name search with the app's default OnlyOK=true must still keep a
// never-checked station: OnlyOK there means "drop checked-and-broken", not
// "hide everything unverified" (#252, #267 — the old
// IncludeUnchecked && !OnlyOK guard disabled the fix on default installs).
func TestSearchNameKeepsUncheckedDespiteOnlyOK(t *testing.T) {
	f := newFakeMirror(t, map[int]string{0: stationsMixedJSON})
	res, err := f.client().SearchDetailed(context.Background(), SearchOpts{
		Name: "radio", OnlyOK: true, IncludeUnchecked: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Relaxed {
		t.Error("Relaxed set although the filtered pass had results")
	}
	if !equalNames(res.Stations, "Reachable", "JustAdded") {
		t.Errorf("stations = %v, want [Reachable JustAdded]", names(res.Stations))
	}
	q := f.request(t, 0).Query()
	if q.Has("hidebroken") || q.Has("lastcheckok") {
		t.Errorf("name search sent server-side filters: %v", q)
	}
	// Client-side filtering fetches a widened page (3x the limit).
	if got := q.Get("limit"); got != "30" {
		t.Errorf("limit = %q, want widened 30", got)
	}
	if f.requestCount() != 1 {
		t.Errorf("requests = %d, want 1", f.requestCount())
	}
}

// Tag-scoped searches get the same relaxed treatment as name searches.
func TestSearchTagKeepsUncheckedDespiteOnlyOK(t *testing.T) {
	f := newFakeMirror(t, map[int]string{0: stationsMixedJSON})
	res, err := f.client().SearchDetailed(context.Background(), SearchOpts{
		Tag: "jazz", OnlyOK: true, IncludeUnchecked: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(res.Stations, "Reachable", "JustAdded") {
		t.Errorf("stations = %v, want [Reachable JustAdded]", names(res.Stations))
	}
	q := f.request(t, 0).Query()
	if q.Has("hidebroken") || q.Has("lastcheckok") {
		t.Errorf("tag search sent server-side filters: %v", q)
	}
}

// Browse/top lists (no name/tag) keep the strict server-side filters when
// OnlyOK is set, even with IncludeUnchecked — a ranking should not surface
// unverified entries.
func TestSearchBrowseStaysStrict(t *testing.T) {
	f := newFakeMirror(t, map[int]string{
		0: `[{"name":"Reachable","lastcheckok":1,"lastchecktime":"2026-07-01 09:44:00"}]`,
	})
	res, err := f.client().SearchDetailed(context.Background(), SearchOpts{
		Country: "de", OnlyOK: true, IncludeUnchecked: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(res.Stations, "Reachable") || res.Relaxed {
		t.Errorf("got %v (relaxed=%v), want [Reachable] strict", names(res.Stations), res.Relaxed)
	}
	q := f.request(t, 0).Query()
	if q.Get("hidebroken") != "true" || q.Get("lastcheckok") != "true" {
		t.Errorf("browse dropped server-side filters: %v", q)
	}
	if got := q.Get("limit"); got != "10" {
		t.Errorf("limit = %q, want unwidened 10", got)
	}
}

// A strict name search (no IncludeUnchecked) that finds nothing is retried
// once without the server-side filters and the result is marked Relaxed, so
// a station wrongly flagged broken (geo-fenced stream) stays findable.
func TestSearchNameFallbackAfterStrictZero(t *testing.T) {
	f := newFakeMirror(t, map[int]string{0: `[]`, 1: stationsAllBrokenJSON})
	res, err := f.client().SearchDetailed(context.Background(), SearchOpts{
		Name: "geo", OnlyOK: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Relaxed {
		t.Error("Relaxed not set on fallback results")
	}
	if !equalNames(res.Stations, "GeoFenced", "AlsoFlagged") {
		t.Errorf("stations = %v, want the unfiltered hits", names(res.Stations))
	}
	if f.requestCount() != 2 {
		t.Fatalf("requests = %d, want 2 (strict + fallback)", f.requestCount())
	}
	q0 := f.request(t, 0).Query()
	if q0.Get("hidebroken") != "true" || q0.Get("lastcheckok") != "true" {
		t.Errorf("first pass missing strict filters: %v", q0)
	}
	q1 := f.request(t, 1).Query()
	if q1.Has("hidebroken") || q1.Has("lastcheckok") {
		t.Errorf("fallback pass still sent filters: %v", q1)
	}
}

// In the client-side-filter path the fallback needs no second request: when
// every raw hit was checked-and-broken, the raw page is returned as Relaxed.
func TestSearchNameFallbackWhenAllHitsBroken(t *testing.T) {
	f := newFakeMirror(t, map[int]string{0: stationsAllBrokenJSON})
	res, err := f.client().SearchDetailed(context.Background(), SearchOpts{
		Name: "geo", OnlyOK: true, IncludeUnchecked: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Relaxed {
		t.Error("Relaxed not set although only checked-and-broken hits exist")
	}
	if !equalNames(res.Stations, "GeoFenced", "AlsoFlagged") {
		t.Errorf("stations = %v, want raw hits", names(res.Stations))
	}
	if f.requestCount() != 1 {
		t.Errorf("requests = %d, want 1 (no refetch needed)", f.requestCount())
	}
}

// A name search that is empty even without filters stays empty and unmarked.
func TestSearchNameTrulyEmptyStaysEmpty(t *testing.T) {
	f := newFakeMirror(t, nil) // every request answers []
	res, err := f.client().SearchDetailed(context.Background(), SearchOpts{
		Name: "nosuchstation", OnlyOK: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stations) != 0 || res.Relaxed {
		t.Errorf("got %v (relaxed=%v), want empty unrelaxed", names(res.Stations), res.Relaxed)
	}
	if f.requestCount() != 2 {
		t.Errorf("requests = %d, want 2 (strict + one fallback try)", f.requestCount())
	}
}

// Browse lists never trigger the fallback: zero results mean zero results.
func TestSearchBrowseZeroResultsNoFallback(t *testing.T) {
	f := newFakeMirror(t, nil)
	res, err := f.client().SearchDetailed(context.Background(), SearchOpts{
		Country: "de", OnlyOK: true, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stations) != 0 || res.Relaxed {
		t.Errorf("got %v (relaxed=%v), want empty unrelaxed", names(res.Stations), res.Relaxed)
	}
	if f.requestCount() != 1 {
		t.Errorf("requests = %d, want 1 (no fallback for browse)", f.requestCount())
	}
}
