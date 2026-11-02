package webui

import (
	"net/http"
	"testing"
)

const pandoraFav = `{"source":"pandora","name":"Jazz Radio","location":"111","itemType":"stationurl","favicon":"https://img.example.com/111.jpg"}`

// #1663: a Pandora station can be starred. It has no stream address, so it is
// kept by its station reference, under the key its Recently-played card has.
func TestFavoritePandoraStationCanBeStarred(t *testing.T) {
	s := favServer(t)
	code, out := postFav(t, s, "?local=1", pandoraFav, lan)
	if code != http.StatusOK || out["id"] != "pandora:111" {
		t.Fatalf("POST = %d %v", code, out)
	}
	got := getFavs(t, s)
	if len(got) != 1 {
		t.Fatalf("list = %+v", got)
	}
	f := got[0]
	if f.Source != "pandora" || f.Location != "111" || f.ItemType != "stationurl" || f.UUID != "pandora:111" ||
		f.Favicon != "https://img.example.com/111.jpg" {
		t.Errorf("stored = %+v", f)
	}
	if adds := favAdditions(t, s); len(adds) != 1 || adds[0].ID != "pandora:111" || adds[0].Fav.Location != "111" {
		t.Errorf("addition notes = %+v", adds)
	}
}

// The desktop app's push keeps a Pandora favourite too, and one without a
// station reference is dropped like a station without an address.
func TestFavoritePandoraSurvivesThePush(t *testing.T) {
	s := favServer(t)
	putFavs(t, s, `[`+pandoraFav+`,{"source":"pandora","name":"No location"},`+
		`{"stationuuid":"a","name":"1LIVE","url_resolved":"http://example/1live"}]`)
	got := getFavs(t, s)
	if len(got) != 2 || got[0].key() != "pandora:111" || got[1].Name != "1LIVE" {
		t.Fatalf("list = %+v", got)
	}
	deleteFav(t, s, "local=1&id=pandora:111", lan)
	if got := getFavs(t, s); len(got) != 1 || got[0].Name != "1LIVE" {
		t.Errorf("after the removal = %+v", got)
	}
}

// Without the Pandora mark a row still needs an http(s) address.
func TestFavoritePandoraNeedsTheMark(t *testing.T) {
	s := favServer(t)
	if code, _ := postFav(t, s, "", `{"name":"Jazz Radio","location":"111"}`, lan); code != http.StatusBadRequest {
		t.Errorf("a row with a location but no Pandora mark got %d", code)
	}
	if code, _ := postFav(t, s, "", `{"source":"pandora","name":"Jazz Radio"}`, lan); code != http.StatusBadRequest {
		t.Errorf("a Pandora row without a location got %d", code)
	}
}
