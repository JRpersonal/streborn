package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func postFav(t *testing.T, s *Server, query, body, remote string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/favorites"+query, strings.NewReader(body))
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	s.handleFavorites(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func favAdditions(t *testing.T, s *Server) []favoriteAddition {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/favorites?removed=1", nil)
	w := httptest.NewRecorder()
	s.handleFavorites(w, r)
	var out struct {
		Added []favoriteAddition `json:"added"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET ?removed=1 did not answer the notes: %s", w.Body.String())
	}
	return out.Added
}

const lan = "192.168.1.20:5555"
const swr3 = `{"stationuuid":"u-9","name":"SWR3","url_resolved":"http://example/swr3"}`

// #1621: a favourite starred on the phone or in a remote app lands on the
// speaker's list and is noted, with the row, for the desktop app.
func TestFavoriteAddStoresItAndNotesIt(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, twoFavs)
	code, out := postFav(t, s, "?local=1", swr3, lan)
	if code != http.StatusOK || out["added"] != true || out["id"] != "u-9" {
		t.Fatalf("POST = %d %v", code, out)
	}
	if got := getFavs(t, s); len(got) != 3 || got[2].Name != "SWR3" {
		t.Fatalf("list after the add = %+v", got)
	}
	adds := favAdditions(t, s)
	if len(adds) != 1 || adds[0].ID != "u-9" || adds[0].Fav.Name != "SWR3" || adds[0].At == 0 {
		t.Fatalf("addition notes = %+v", adds)
	}
	// A second star on the same station changes nothing on the list.
	if _, out := postFav(t, s, "?local=1", swr3, lan); out["added"] != false {
		t.Errorf("a second add of the same station reported added=%v", out["added"])
	}
	if got := getFavs(t, s); len(got) != 3 {
		t.Errorf("a second add grew the list to %d", len(got))
	}
}

// The desktop app's next push does not know the new favourite yet. It must not
// wipe it; the first push that carries it settles the note.
func TestFavoriteAddSurvivesTheDesktopPushUntilItIsTakenOver(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, twoFavs)
	postFav(t, s, "?local=1", swr3, lan)

	putFavs(t, s, twoFavs) // the desktop app, still on its old list
	if got := getFavs(t, s); len(got) != 3 {
		t.Fatalf("an older desktop push wiped the new favourite: %+v", got)
	}
	if len(favAdditions(t, s)) != 1 {
		t.Fatal("the note went before the desktop app took the favourite over")
	}

	withIt := strings.TrimSuffix(strings.TrimSpace(twoFavs), "]") + "," + swr3 + "]"
	putFavs(t, s, withIt) // the desktop app has read the note
	if n := len(favAdditions(t, s)); n != 0 {
		t.Fatalf("the note stayed after the desktop app took it over: %d", n)
	}
	putFavs(t, s, twoFavs) // and now unstars it on the PC
	if got := getFavs(t, s); len(got) != 2 {
		t.Errorf("a star taken off on the PC came back: %+v", got)
	}
}

// Starred again after a removal: the removal note goes, and the next push from
// a desktop app that still carries the removal does not drop it.
func TestFavoriteAddAfterARemovalWins(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, `[{"stationuuid":"u-9","name":"SWR3","url_resolved":"http://example/swr3"}]`)
	deleteFav(t, s, "local=1&id=u-9", lan)
	if len(favRemovals(t, s)) != 1 {
		t.Fatal("setup: no removal note")
	}
	postFav(t, s, "?local=1", swr3, lan)
	if n := len(favRemovals(t, s)); n != 0 {
		t.Errorf("the removal note survived the new star: %d", n)
	}
	if got := getFavs(t, s); len(got) != 1 {
		t.Errorf("list = %+v", got)
	}
}

// Unstarred on the phone before the desktop app took it over: the addition
// note goes too, or the next push would bring the favourite back.
func TestFavoriteRemovedBeforeTakeoverDropsTheAdditionNote(t *testing.T) {
	s := favServer(t)
	postFav(t, s, "?local=1", swr3, lan)
	deleteFav(t, s, "local=1&id=u-9", lan)
	if n := len(favAdditions(t, s)); n != 0 {
		t.Fatalf("the addition note survived the removal: %d", n)
	}
	putFavs(t, s, twoFavs)
	for _, f := range getFavs(t, s) {
		if f.key() == "u-9" {
			t.Fatal("a favourite removed on the phone came back with a desktop push")
		}
	}
}

func TestFavoriteAddRefusesWhatCannotPlay(t *testing.T) {
	s := favServer(t)
	for _, body := range []string{
		`{"name":"No stream"}`,
		`{"name":"","url":"http://example/x"}`,
		`{"name":"Spotify","url":"spotify:playlist:abc"}`,
		`not json`,
	} {
		if code, _ := postFav(t, s, "", body, lan); code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", body, code)
		}
	}
	if code, _ := postFav(t, s, "", swr3, "203.0.113.9:4000"); code != http.StatusForbidden {
		t.Errorf("a POST from outside the LAN got %d", code)
	}
	if len(getFavs(t, s)) != 0 {
		t.Error("a refused add left a row behind")
	}
}

// A star set on one speaker's page reaches the other STR speakers, with the
// time the first speaker gave it, and is not handed on again from there.
func TestFavoriteAddIsHandedOnToTheOtherSpeakers(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	var queries []string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/agent/version":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"v1.2.1"}`))
		case r.URL.Path == "/api/favorites" && r.Method == http.MethodPost:
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
	s.peersFn = func(context.Context) []PeerLink {
		return []PeerLink{{Name: "Kitchen", IP: "10.0.0.2", URL: "http://10.0.0.2:8888/", Reachable: true}}
	}
	f := Favorite{UUID: "u-9", Name: "SWR3", Res: "http://example/swr3", StarredAt: 1234}
	body, _ := json.Marshal(f)
	s.forwardFavoriteChange(context.Background(), "addition", http.MethodPost, "/api/favorites?local=1", body)
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 || queries[0] != "local=1" {
		t.Fatalf("forwarded %v %v", queries, bodies)
	}
	var got Favorite
	if err := json.Unmarshal([]byte(bodies[0]), &got); err != nil || got.UUID != "u-9" || got.StarredAt != 1234 {
		t.Errorf("forwarded row = %s", bodies[0])
	}

	// On the peer's side, local=1 keeps the first speaker's time.
	p := favServer(t)
	postFav(t, p, "?local=1", bodies[0], lan)
	if adds := favAdditions(t, p); len(adds) != 1 || adds[0].At != 1234 {
		t.Errorf("peer note = %+v", adds)
	}
}
