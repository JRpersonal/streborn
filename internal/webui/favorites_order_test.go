package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const threeFavs = `[
	{"stationuuid":"a","name":"1LIVE","url_resolved":"http://example/1live"},
	{"stationuuid":"b","name":"WDR 2","url_resolved":"http://example/wdr2"},
	{"stationuuid":"c","name":"SWR3","url_resolved":"http://example/swr3"}
]`

func postOrder(t *testing.T, s *Server, query, body, remote string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/favorites/order"+query, strings.NewReader(body))
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	s.handleFavoriteOrder(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func putFavsAt(t *testing.T, s *Server, body string, orderAt int64) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, "/api/favorites?orderAt="+strconv.FormatInt(orderAt, 10), strings.NewReader(body))
	r.RemoteAddr = lan
	w := httptest.NewRecorder()
	s.handleFavorites(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", w.Code, w.Body.String())
	}
}

func favOrderNote(t *testing.T, s *Server) *favoriteOrder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/favorites?removed=1", nil)
	w := httptest.NewRecorder()
	s.handleFavorites(w, r)
	var out struct {
		Order *favoriteOrder `json:"order"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET ?removed=1 = %s", w.Body.String())
	}
	return out.Order
}

func favNames(list []Favorite) string {
	names := make([]string, len(list))
	for i, f := range list {
		names[i] = f.Name
	}
	return strings.Join(names, ",")
}

// #1519: a favourite moved on the phone page stays where it was put, and the
// order is noted for the desktop app.
func TestFavoriteOrderMovesAndNotes(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, threeFavs)
	code, out := postOrder(t, s, "?local=1", `{"ids":["c","a","b"]}`, lan)
	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("POST = %d %v", code, out)
	}
	if got := favNames(getFavs(t, s)); got != "SWR3,1LIVE,WDR 2" {
		t.Fatalf("order = %s", got)
	}
	note := favOrderNote(t, s)
	if note == nil || note.At == 0 || strings.Join(note.IDs, ",") != "c,a,b" {
		t.Fatalf("order note = %+v", note)
	}
}

// Keys the request leaves out keep their place after the named ones; keys the
// speaker does not know are skipped.
func TestFavoriteOrderPartialAndUnknownKeys(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, threeFavs)
	postOrder(t, s, "?local=1", `{"ids":["zzz","b"]}`, lan)
	if got := favNames(getFavs(t, s)); got != "WDR 2,1LIVE,SWR3" {
		t.Fatalf("order = %s", got)
	}
	if note := favOrderNote(t, s); strings.Join(note.IDs, ",") != "b,a,c" {
		t.Errorf("the note should carry the whole order: %+v", note)
	}
}

// The desktop app's next push still has the old order. It must not undo the
// move; a push that knows an order at least as new settles the note, and from
// then on the desktop app's order counts again.
func TestFavoriteOrderSurvivesAnOlderDesktopPush(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, threeFavs)
	_, out := postOrder(t, s, "?local=1", `{"ids":["c","a","b"]}`, lan)
	at := int64(out["at"].(float64))

	putFavs(t, s, threeFavs) // an app from before #1519: no orderAt
	if got := favNames(getFavs(t, s)); got != "SWR3,1LIVE,WDR 2" {
		t.Fatalf("an older push undid the phone's order: %s", got)
	}
	putFavsAt(t, s, threeFavs, at-1) // an app that has not read the note yet
	if got := favNames(getFavs(t, s)); got != "SWR3,1LIVE,WDR 2" {
		t.Fatalf("a push older than the note undid the order: %s", got)
	}
	if favOrderNote(t, s) == nil {
		t.Fatal("the note went before the desktop app took the order over")
	}

	moved := `[
		{"stationuuid":"c","name":"SWR3","url_resolved":"http://example/swr3"},
		{"stationuuid":"a","name":"1LIVE","url_resolved":"http://example/1live"},
		{"stationuuid":"b","name":"WDR 2","url_resolved":"http://example/wdr2"}
	]`
	putFavsAt(t, s, moved, at) // the app took the order over
	if n := favOrderNote(t, s); n != nil {
		t.Fatalf("the note stayed after the desktop app took it over: %+v", n)
	}
	putFavsAt(t, s, threeFavs, at+5000) // and moved them back on the PC
	if got := favNames(getFavs(t, s)); got != "1LIVE,WDR 2,SWR3" {
		t.Errorf("the desktop app's newer order did not count: %s", got)
	}
}

// A favourite starred after the order was set is not lost when a push gets the
// noted order laid over it.
func TestFavoriteOrderKeepsFavouritesItDoesNotName(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, threeFavs)
	postOrder(t, s, "?local=1", `{"ids":["c","b","a"]}`, lan)
	withNew := strings.TrimSuffix(strings.TrimSpace(threeFavs), "]") + `,{"stationuuid":"d","name":"Bayern 3","url_resolved":"http://example/b3"}]`
	putFavs(t, s, withNew)
	if got := favNames(getFavs(t, s)); got != "SWR3,WDR 2,1LIVE,Bayern 3" {
		t.Errorf("order = %s", got)
	}
}

func TestFavoriteOrderRefusals(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, threeFavs)
	for _, body := range []string{`{"ids":[]}`, `{"ids":[""]}`, `{}`, `not json`} {
		if code, _ := postOrder(t, s, "", body, lan); code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", body, code)
		}
	}
	if code, _ := postOrder(t, s, "", `{"ids":["c"]}`, "203.0.113.9:4000"); code != http.StatusForbidden {
		t.Errorf("a POST from outside the LAN got %d", code)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/favorites/order", nil)
	r.RemoteAddr = lan
	w := httptest.NewRecorder()
	s.handleFavoriteOrder(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET got %d", w.Code)
	}
	if got := favNames(getFavs(t, s)); got != "1LIVE,WDR 2,SWR3" {
		t.Errorf("a refused request moved something: %s", got)
	}
	if favOrderNote(t, s) != nil {
		t.Error("a refused request left a note")
	}
}

// An order set on one speaker's page reaches the other STR speakers with the
// time the first speaker gave it, and is not handed on again from there.
func TestFavoriteOrderIsHandedOnToTheOtherSpeakers(t *testing.T) {
	var mu sync.Mutex
	var bodies, queries []string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/agent/version":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"v1.3.3"}`))
		case r.URL.Path == "/api/favorites/order" && r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(b))
			queries = append(queries, r.URL.RawQuery)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer peer.Close()
	origBase, origClient := fleetPeerBase, fleetClient
	t.Cleanup(func() { fleetPeerBase, fleetClient = origBase, origClient })
	fleetPeerBase = func(string, int) string { return peer.URL }
	fleetClient = func(timeout time.Duration) *http.Client { return &http.Client{Timeout: timeout} }

	s := favServer(t)
	putFavs(t, s, threeFavs)
	s.peersFn = func(context.Context) []PeerLink {
		return []PeerLink{{Name: "Kitchen", IP: "10.0.0.2", URL: "http://10.0.0.2:8888/", Reachable: true}}
	}
	_, out := postOrder(t, s, "", `{"ids":["b","c","a"]}`, lan)
	at := int64(out["at"].(float64))
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(bodies)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 || queries[0] != "local=1" {
		t.Fatalf("forwarded %v %v", queries, bodies)
	}

	p := favServer(t)
	putFavs(t, p, threeFavs)
	postOrder(t, p, "?local=1", bodies[0], lan)
	if note := favOrderNote(t, p); note == nil || note.At != at || strings.Join(note.IDs, ",") != "b,c,a" {
		t.Errorf("peer note = %+v, want at %d", note, at)
	}
	if got := favNames(getFavs(t, p)); got != "WDR 2,SWR3,1LIVE" {
		t.Errorf("peer order = %s", got)
	}
}

func TestApplyFavoriteOrder(t *testing.T) {
	in := []Favorite{{UUID: "a"}, {UUID: "b"}, {UUID: "c"}, {UUID: "d"}}
	got := favoriteKeys(applyFavoriteOrder(in, []string{"d", "b", "d", "x"}))
	if strings.Join(got, ",") != "d,b,a,c" {
		t.Errorf("applyFavoriteOrder = %v", got)
	}
	if got := applyFavoriteOrder(in, nil); !sameFavoriteOrder(got, in) {
		t.Errorf("no ids changed the order: %v", favoriteKeys(got))
	}
}
