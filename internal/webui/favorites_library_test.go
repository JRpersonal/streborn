package webui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A music-library favourite (#1605) keeps what marks it as one through the
// round trip over the speaker: the source, the MIME, the length and, for a
// folder, the "queue:" key it is replayed from. The speaker used to drop the
// first three, so every device read the album back as a plain radio station.
func TestFavoritesKeepTheLibraryIdentity(t *testing.T) {
	s := favServer(t)
	body := `[
	  {"stationuuid":"queue:uuid:server-id/0$1$2","name":"Album","url":"http://192.0.2.10:9790/1.flac","url_resolved":"http://192.0.2.10:9790/1.flac","favicon":"http://192.0.2.10:9790/cover.jpg","source":"upnp","mime":"audio/flac","starredAt":5},
	  {"stationuuid":"http://192.0.2.10:9790/2.mp3","name":"Track","url":"http://192.0.2.10:9790/2.mp3","source":"upnp","mime":"audio/mpeg","durationSec":187},
	  {"stationuuid":"a","name":"1LIVE","url_resolved":"http://example/1live"}
	]`
	if code, _ := putFavs(t, s, body); code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	got := getFavs(t, s)
	if len(got) != 3 {
		t.Fatalf("stored list = %+v", got)
	}
	if f := got[0]; f.Source != "upnp" || f.Mime != "audio/flac" || f.UUID != "queue:uuid:server-id/0$1$2" || f.Favicon == "" {
		t.Errorf("folder favourite lost its library identity: %+v", f)
	}
	if f := got[1]; f.Source != "upnp" || f.Mime != "audio/mpeg" || f.DurationSec != 187 {
		t.Errorf("track favourite lost its library identity: %+v", f)
	}
	if f := got[2]; f.Source != "" || f.Mime != "" || f.DurationSec != 0 {
		t.Errorf("a radio favourite grew library fields: %+v", f)
	}
	// The key, and so the phone page's removal, is the same as before.
	if got[0].key() != "queue:uuid:server-id/0$1$2" {
		t.Errorf("folder key = %q", got[0].key())
	}
}

// A radio favourite stored before the fields existed is written back
// byte-identical: no empty source/mime keys, so an unchanged list stays
// unchanged on the flash.
func TestFavoritesRadioRowHasNoLibraryKeys(t *testing.T) {
	b, err := json.Marshal(Favorite{UUID: "a", Name: "1LIVE", Res: "http://example/1live"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"source", "mime", "durationSec"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("radio favourite marshals %q: %s", k, b)
		}
	}
}

// The phone page must play a library favourite the library way: a folder
// through the speaker's folder replay, a track with its MIME and length, so
// the speaker files it under Library with its cover instead of as radio.
func TestPhoneRemotePlaysLibraryFavouriteAsLibrary(t *testing.T) {
	start := strings.Index(indexHTML, "async function playFavorite(")
	if start < 0 {
		t.Fatal("playFavorite is gone; if it moved, move this test with it")
	}
	fn := indexHTML[start:]
	fn = fn[:strings.Index(fn, "\n}\n")]
	if !strings.Contains(fn, "isLibraryFav(st)") || !strings.Contains(fn, "playLibraryFav(st)") {
		t.Error("playFavorite no longer routes a library favourite to playLibraryFav")
	}
	start = strings.Index(indexHTML, "async function playLibraryFav(")
	if start < 0 {
		t.Fatal("playLibraryFav is gone")
	}
	lib := indexHTML[start:]
	lib = lib[:strings.Index(lib, "\n}\n")]
	for _, want := range []string{"isFolderFavKey(key)", "/api/queue/replay-card", "mime: st.mime", "duration_sec: st.durationSec"} {
		if !strings.Contains(lib, want) {
			t.Errorf("playLibraryFav lost %q", want)
		}
	}
	isLib := indexHTML[strings.Index(indexHTML, "function isLibraryFav("):]
	isLib = isLib[:strings.Index(isLib, "\n}\n")]
	if !strings.Contains(isLib, "st.source === 'upnp'") {
		t.Error("isLibraryFav no longer recognises the desktop app's upnp mark")
	}
}
