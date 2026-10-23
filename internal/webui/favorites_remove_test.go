package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func deleteFav(t *testing.T, s *Server, query, remote string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodDelete, "/api/favorites?"+query, nil)
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	s.handleFavorites(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func favRemovals(t *testing.T, s *Server) []favoriteRemoval {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/favorites?removed=1", nil)
	w := httptest.NewRecorder()
	s.handleFavorites(w, r)
	var out struct {
		Removed []favoriteRemoval `json:"removed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET ?removed=1 did not answer the notes: %s", w.Body.String())
	}
	return out.Removed
}

const twoFavs = `[
  {"stationuuid":"u-1","name":"1LIVE","url_resolved":"http://example/1live"},
  {"name":"NAS folder","url":"http://192.0.2.5:8200/folder"}
]`

// #1389: a favourite can be removed on the phone page. The key is the desktop
// app's own (UUID, else name|url), so a row without a UUID, like the DLNA
// folder the reporter starred, is just as removable.
func TestFavoriteDeleteRemovesTheRowAndNotesIt(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, twoFavs)
	code, out := deleteFav(t, s, "id="+url.QueryEscape("NAS folder|http://192.0.2.5:8200/folder"), "192.168.178.20:5555")
	if code != http.StatusOK || out["removed"] != float64(1) {
		t.Fatalf("delete answered %d %v", code, out)
	}
	got := getFavs(t, s)
	if len(got) != 1 || got[0].Name != "1LIVE" {
		t.Fatalf("after delete = %+v", got)
	}
	notes := favRemovals(t, s)
	if len(notes) != 1 || notes[0].ID != "NAS folder|http://192.0.2.5:8200/folder" || notes[0].At == 0 {
		t.Errorf("removal notes = %+v", notes)
	}
}

// The heart of it: the desktop app pushes its whole list whenever any star
// changes. A list that still carries the removed favourite (starred before the
// removal, or from an app that predates starredAt) must not bring it back.
func TestFavoriteRemovedOnThePhoneStaysRemovedAfterADesktopPush(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, twoFavs)
	deleteFav(t, s, "id=u-1", "192.168.178.20:5555")
	// The PC's old list, plus a station starred there in the meantime.
	putFavs(t, s, `[
	  {"stationuuid":"u-1","name":"1LIVE","url_resolved":"http://example/1live","starredAt":1000},
	  {"name":"NAS folder","url":"http://192.0.2.5:8200/folder"},
	  {"stationuuid":"u-2","name":"WDR 2","url":"http://example/wdr2","starredAt":1000}
	]`)
	for _, f := range getFavs(t, s) {
		if f.UUID == "u-1" {
			t.Fatal("the favourite removed on the phone came back with the next desktop push")
		}
	}
	if got := getFavs(t, s); len(got) != 2 {
		t.Errorf("the other favourites must stay, got %+v", got)
	}
	if notes := favRemovals(t, s); len(notes) != 1 {
		t.Errorf("the note must stay in force until the favourite is starred again, got %+v", notes)
	}
}

// Starring it again on the PC after the removal is a newer decision and wins;
// the note is then dropped so it cannot catch the next push either.
func TestFavoriteStarredAgainAfterRemovalComesBack(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, twoFavs)
	deleteFav(t, s, "id=u-1", "192.168.178.20:5555")
	later := time.Now().Add(time.Minute).UnixMilli()
	body := `[{"stationuuid":"u-1","name":"1LIVE","url_resolved":"http://example/1live","starredAt":` +
		jsonInt(later) + `}]`
	putFavs(t, s, body)
	got := getFavs(t, s)
	if len(got) != 1 || got[0].UUID != "u-1" || got[0].StarredAt != later {
		t.Fatalf("a re-starred favourite must come back with its star time, got %+v", got)
	}
	if notes := favRemovals(t, s); len(notes) != 0 {
		t.Errorf("the note must be dropped once the favourite is starred again, got %+v", notes)
	}
}

// Removing is a write to the speaker and stays on the LAN like every other.
func TestFavoriteDeleteIsLANOnlyAndNeedsAnID(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, twoFavs)
	if code, _ := deleteFav(t, s, "id=u-1", "203.0.113.9:5555"); code != http.StatusForbidden {
		t.Errorf("off-LAN delete: want 403, got %d", code)
	}
	if code, _ := deleteFav(t, s, "", "192.168.178.20:5555"); code != http.StatusBadRequest {
		t.Errorf("delete without id: want 400, got %d", code)
	}
	if got := getFavs(t, s); len(got) != 2 {
		t.Errorf("a refused delete must remove nothing, got %+v", got)
	}
}

// The notes are bounded: they live on the same flash as everything else.
func TestFavoriteRemovalNotesAreCapped(t *testing.T) {
	s := favServer(t)
	for i := 0; i < maxFavoriteRemovals+20; i++ {
		deleteFav(t, s, "id=gone-"+jsonInt(int64(i))+"&local=1", "192.168.178.20:5555")
	}
	if got := favRemovals(t, s); len(got) != maxFavoriteRemovals {
		t.Errorf("want %d notes, got %d", maxFavoriteRemovals, len(got))
	}
}

// A removal on one speaker's page reaches the other STR speakers, marked
// local=1 so they do not hand it on again; a peer is only sent the DELETE once
// it answered as an STR agent.
func TestFavoriteRemovalIsHandedOnToTheOtherSpeakers(t *testing.T) {
	var mu sync.Mutex
	var got []string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/agent/version":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"v1.0.20"}`))
		case r.URL.Path == "/api/favorites" && r.Method == http.MethodDelete:
			mu.Lock()
			got = append(got, r.URL.RawQuery)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"removed":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer peer.Close()
	notAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			t.Errorf("a speaker without an STR agent was sent %s", r.URL)
		}
		http.NotFound(w, r)
	}))
	defer notAgent.Close()

	origBase, origClient := fleetPeerBase, fleetClient
	t.Cleanup(func() { fleetPeerBase, fleetClient = origBase, origClient })
	fleetPeerBase = func(ip string, _ int) string {
		switch ip {
		case "10.0.0.2":
			return peer.URL
		case "10.0.0.3":
			return notAgent.URL
		}
		return "http://127.0.0.1:1"
	}
	fleetClient = func(timeout time.Duration) *http.Client { return &http.Client{Timeout: timeout} }

	s := favServer(t)
	s.peersFn = func(context.Context) []PeerLink {
		return []PeerLink{
			{Name: "Kitchen", IP: "10.0.0.2", URL: "http://10.0.0.2:8888/", Reachable: true},
			{Name: "Stock box", IP: "10.0.0.3", URL: "http://10.0.0.3:8888/", Reachable: true},
		}
	}
	s.forwardFavoriteRemoval(context.Background(), "NAS folder|http://192.0.2.5:8200/folder")
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("want one DELETE at the STR peer, got %v", got)
	}
	if !strings.Contains(got[0], "local=1") || !strings.Contains(got[0], "id=NAS+folder%7Chttp") {
		t.Errorf("forwarded query = %q", got[0])
	}
}

// A removal that arrived from another speaker is not handed on again.
func TestFavoriteRemovalFromAPeerIsNotForwarded(t *testing.T) {
	s := favServer(t)
	called := make(chan struct{}, 1)
	s.peersFn = func(context.Context) []PeerLink {
		called <- struct{}{}
		return nil
	}
	putFavs(t, s, twoFavs)
	if code, _ := deleteFav(t, s, "id=u-1&local=1", "192.168.178.20:5555"); code != http.StatusOK {
		t.Fatalf("delete answered %d", code)
	}
	select {
	case <-called:
		t.Error("a removal handed on by a peer was forwarded again")
	case <-time.After(100 * time.Millisecond):
	}
}

// The phone page's favourites rows carry a remove control with an inline
// confirmation, through DELETE /api/favorites with the desktop's key.
func TestPhoneRemoteFavoritesCanBeRemoved(t *testing.T) {
	at := strings.Index(indexHTML, "async function loadFavorites(")
	end := strings.Index(indexHTML, "async function playFavorite(")
	if at < 0 {
		t.Fatal("could not find the phone page's favourites code")
	}
	if end < at {
		end = len(indexHTML)
	}
	code := indexHTML[at:end]
	for _, want := range []string{
		"function favKey(st)",
		"'/api/favorites?id=' + encodeURIComponent(favKey(st)), 'DELETE'",
		"fav-del", "T.favDelQ", "T.favDelYes", "T.recClearNo",
	} {
		if !strings.Contains(indexHTML[at:], want) {
			t.Errorf("phone favourites code is missing %q", want)
		}
	}
	for _, banned := range []string{"confirm(", "alert("} {
		if strings.Contains(code, banned) {
			t.Errorf("phone favourites code uses %q; the confirmation must be inline", banned)
		}
	}
}

// The phone page re-reads the favourites instead of keeping what it loaded on
// open: a star taken off in the desktop app reached every speaker, and an open
// page still listed it minutes later (#1389). Re-read on foreground and
// pull-to-refresh (refreshAll), on opening the Find tab, and on a slow poll
// while that tab shows; a quiet re-read never paints over a confirmation.
func TestPhoneRemoteFavoritesFollowTheSpeaker(t *testing.T) {
	section := func(from, to string) string {
		at := strings.Index(indexHTML, from)
		if at < 0 {
			t.Fatalf("could not find %q in the phone page", from)
		}
		rest := indexHTML[at:]
		if end := strings.Index(rest, to); end > 0 {
			return rest[:end]
		}
		return rest
	}
	if all := section("function refreshAll(){", "\n}"); !strings.Contains(all, "loadFavorites(true)") {
		t.Error("refreshAll (foreground, pull-to-refresh) does not re-read the favourites")
	}
	if tab := section("function showTab(name) {", "\n}"); !strings.Contains(tab, "name === 'Browse') loadFavorites(true)") {
		t.Error("opening the Find tab does not re-read the favourites")
	}
	if !strings.Contains(indexHTML, "if (p && !p.hidden && document.visibilityState === 'visible') loadFavorites(true)") {
		t.Error("no slow favourites re-read while the Find tab shows")
	}
	load := section("async function loadFavorites(", "function favKey(")
	for _, want := range []string{
		"if (quiet && !Array.isArray(list)) return;",
		"box.querySelector('.fav-confirm')",
		"sig === favSig",
	} {
		if !strings.Contains(load, want) {
			t.Errorf("loadFavorites is missing %q", want)
		}
	}
}

// Every new favourites string exists in every locale bundle (favKeepRec is the
// "it stays in Recently played" line of the unstar question, #1339).
func TestPhoneRemoteLocalesCarryFavoriteDelete(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	for _, key := range []string{"favDel", "favDelQ", "favDelYes", "favKeepRec"} {
		if got := strings.Count(indexHTML, key+":\""); got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
