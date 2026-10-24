package webui

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/recent"
)

// #1339: taking a star off on the phone must not forget that the station was
// ever played. The favourite's control calls DELETE /api/favorites only, and
// that handler leaves the recently-played ring alone while it still notes the
// removal for the desktop app and the other speakers.
func TestPhoneUnstarLeavesRecentlyPlayedUntouched(t *testing.T) {
	s := favServer(t)
	s.recent = recent.New()
	s.recent.Add(recent.Entry{Source: "radio", CardKey: "http://example/1live", CardName: "1LIVE", CardURL: "http://example/1live", TS: "t1"})
	putFavs(t, s, twoFavs)

	code, out := deleteFav(t, s, "id=u-1", "192.168.1.20:5555")
	if code != http.StatusOK || out["removed"] != float64(1) {
		t.Fatalf("unstar answered %d %v", code, out)
	}
	if got := s.recent.All(); len(got) != 1 || got[0].CardKey != "http://example/1live" {
		t.Fatalf("unstarring dropped the entry from Recently played: %+v", got)
	}
	if got := getFavs(t, s); len(got) != 1 || got[0].Name != "NAS folder" {
		t.Errorf("the star must be gone and the other favourite kept, got %+v", got)
	}
	if notes := favRemovals(t, s); len(notes) != 1 || notes[0].ID != "u-1" {
		t.Errorf("the removal must still be noted for the desktop app, got %+v", notes)
	}
}

// The other way round: the x on a Recently played row forgets the entry and
// leaves the favourites, and their removal notes, alone.
func TestRecentDeleteLeavesFavoritesUntouched(t *testing.T) {
	s := favServer(t)
	s.recent = recent.New()
	s.recent.Add(recent.Entry{Source: "radio", CardKey: "http://example/1live", CardName: "1LIVE", CardURL: "http://example/1live", TS: "t1"})
	putFavs(t, s, twoFavs)

	w := recentRequest(t, s, http.MethodDelete, "?cardKey="+url.QueryEscape("http://example/1live")+"&ts=t1")
	if w.Code != http.StatusOK {
		t.Fatalf("recent delete answered %d: %s", w.Code, w.Body.String())
	}
	if got := s.recent.All(); len(got) != 0 {
		t.Fatalf("the entry must be gone from Recently played, got %+v", got)
	}
	if got := getFavs(t, s); len(got) != 2 {
		t.Errorf("forgetting a play must not touch the favourites, got %+v", got)
	}
	if notes := favRemovals(t, s); len(notes) != 0 {
		t.Errorf("forgetting a play must not read as an unstar to the desktop app, got %+v", notes)
	}
}

// The phone page: a favourite's control is a star, not an x; its confirmation
// says the entry stays and calls /api/favorites only; a Recently played row
// carries the same star when the card is a favourite; and the row's x remains
// the one control that calls DELETE /api/recent (#1339).
func TestPhoneRemoteUnstarIsAStarAndNeverTouchesRecent(t *testing.T) {
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
	fav := section("function renderFavRow(", "// The stations of the Pandora account")
	for _, want := range []string{
		"unstarButton(st,",
		"function unstarButton(",
		"'btn fav-del fav-star'",
		"'&#9733;'",
		"function renderUnstarConfirm(",
		"T.favKeepRec",
		"'/api/favorites?id=' + encodeURIComponent(favKey(st)), 'DELETE'",
	} {
		if !strings.Contains(fav, want) {
			t.Errorf("favourites code is missing %q", want)
		}
	}
	if strings.Contains(fav, "/api/recent") {
		t.Error("the favourite's star must never call /api/recent")
	}
	if strings.Contains(fav, "'&times;'") {
		t.Error("the favourite's control is an x again; it must be the star")
	}

	load := section("async function loadFavorites(", "function favKey(")
	if !strings.Contains(load, "syncRecentStars();") {
		t.Error("a re-read favourites list does not repaint the stars on Recently played")
	}

	rec := section("async function loadRecent()", "// renderRecentFoot paints")
	for _, want := range []string{
		"row.dataset.idx = String(idx)",
		"'rec-star-slot'",
		"renderRecentStar(slot, row, e)",
		"function favForRecent(",
		"f.stationuuid === e.cardKey",
		"u === e.cardURL",
		"function renderRecentStar(",
		"unstarButton(f,",
		"renderUnstarConfirm(conf, f, err,",
		"function syncRecentStars()",
		"deleteRecent(e, db)",
	} {
		if !strings.Contains(rec, want) {
			t.Errorf("Recently played code is missing %q", want)
		}
	}
	del := section("async function deleteRecent(", "\n}")
	if !strings.Contains(del, "'/api/recent?cardKey='") {
		t.Error("the row's x no longer removes the entry from Recently played")
	}
	if strings.Contains(del, "/api/favorites") {
		t.Error("the row's x must never touch the favourites")
	}
	if n := strings.Count(indexHTML, "'/api/recent?cardKey='"); n != 1 {
		t.Errorf("DELETE /api/recent?cardKey is called from %d places, want exactly the row's x", n)
	}
}
