package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
)

// v1.5.6 named a single library track's artist and album on the phone page
// when it was started from the Library (lonefilemeta_test.go). The same track
// started from a preset key, from Favourites, from Recently played or by the
// power-on resume still showed its title alone, because those entries stored
// no artist (#1839). They store both now, and every start carries them into
// the lone file GET /api/queue reports.

const labelTrackURL = "http://192.0.2.10:8200/MediaItems/42.flac"

// labelledKey is a library key as it is saved now.
func labelledKey(slot int) presets.Preset {
	return presets.Preset{Slot: slot, Name: "Amanda", Type: "radio", StreamURL: labelTrackURL,
		Source: "MiniDLNA", DurationSec: 256, Artist: "Boston", Album: "Third Stage"}
}

// queueFile is the "file" object of GET /api/queue, nil when there is none.
func queueFile(t *testing.T, s *Server) map[string]any {
	t.Helper()
	file, _ := queueAnswer(t, s)["file"].(map[string]any)
	return file
}

func wantLabel(t *testing.T, file map[string]any, artist, album string) {
	t.Helper()
	if file == nil {
		t.Fatal("GET /api/queue names no file")
	}
	if file["url"] != labelTrackURL {
		t.Errorf("file.url = %v, want %q", file["url"], labelTrackURL)
	}
	if got, _ := file["artist"].(string); got != artist {
		t.Errorf("file.artist = %q, want %q", got, artist)
	}
	if got, _ := file["album"].(string); got != album {
		t.Errorf("file.album = %q, want %q", got, album)
	}
}

func TestAKeyOnTheSpeakerNamesItsTracksArtistAndAlbum(t *testing.T) {
	s, _ := newDIDLTestServer(t)
	t.Cleanup(s.bumpRecallGen)
	if err := s.presets.SetSlot(labelledKey(3)); err != nil {
		t.Fatal(err)
	}
	if !s.RecallSlot(context.Background(), 3) {
		t.Fatal("RecallSlot did not claim the library track key")
	}
	if tr := activeFileTrack(t, s); tr.artist != "Boston" || tr.album != "Third Stage" {
		t.Fatalf("the end watch follows the file without its label: %+v", tr)
	}
	wantLabel(t, queueFile(t, s), "Boston", "Third Stage")
}

func TestAKeyInTheAppNamesItsTracksArtistAndAlbum(t *testing.T) {
	s, _ := newDIDLTestServer(t)
	s.recent = recent.New()
	t.Cleanup(s.bumpRecallGen)
	if err := s.presets.SetSlot(labelledKey(3)); err != nil {
		t.Fatal(err)
	}
	if _, ref := s.playSlot(context.Background(), 3); ref != nil {
		t.Fatalf("playSlot refused the key: %+v", ref)
	}
	wantLabel(t, queueFile(t, s), "Boston", "Third Stage")
	// The Recently-played card keeps them, so its replay names them again.
	ring := s.recent.All()
	if len(ring) != 1 || ring[0].Artist != "Boston" || ring[0].Album != "Third Stage" || ring[0].Source != "upnp" {
		t.Fatalf("recently played = %+v, want the track with its label", ring)
	}
}

// A key saved before the fields existed: the title alone, as before, and
// nothing breaks on the way.
func TestAKeySavedBeforeTheLabelShowsItsTitleOnly(t *testing.T) {
	s, _ := newDIDLTestServer(t)
	s.recent = recent.New()
	t.Cleanup(s.bumpRecallGen)
	old := labelledKey(3)
	old.Artist, old.Album = "", ""
	if err := s.presets.SetSlot(old); err != nil {
		t.Fatal(err)
	}
	for name, recall := range map[string]func() bool{
		"the speaker's key": func() bool { return s.RecallSlot(context.Background(), 3) },
		"the app's key":     func() bool { _, ref := s.playSlot(context.Background(), 3); return ref == nil },
	} {
		if !recall() {
			t.Fatalf("%s: the recall failed", name)
		}
		file := queueFile(t, s)
		if file == nil || file["title"] != "Amanda" {
			t.Fatalf("%s: file = %v, want the title", name, file)
		}
		for _, k := range []string{"artist", "album"} {
			if _, has := file[k]; has {
				t.Errorf("%s: a key without a label reports %q: %v", name, k, file)
			}
		}
	}
	if got, _ := s.presets.Get(3); got.Artist != "" || got.Album != "" {
		t.Errorf("a label appeared on the key from nowhere: %+v", got)
	}
}

// An old key whose track this agent played from the Library since: the label
// of that play is still in Recently played, so the key is played under it and
// keeps it from then on. No question goes to the media server for it.
func TestAKeySavedBeforeTheLabelRecoversItFromRecentlyPlayed(t *testing.T) {
	s, _ := newDIDLTestServer(t)
	s.recent = recent.New()
	t.Cleanup(s.bumpRecallGen)
	old := labelledKey(3)
	old.Artist, old.Album = "", ""
	if err := s.presets.SetSlot(old); err != nil {
		t.Fatal(err)
	}
	playLoneFile(t, s, `{"url":"`+labelTrackURL+`","title":"Amanda","mime":"audio/flac","artist":"Boston","album":"Third Stage"}`)
	// Something else plays in between, so only the ring still knows the label.
	s.setLastPlay("http://192.0.2.1:8888/stream/1", "A Station", "", "audio/mpeg")
	if a, _, from := s.knownTrackLabel(labelTrackURL); a != "Boston" || from != "recently played" {
		t.Fatalf("knownTrackLabel = %q from %q, want the ring's", a, from)
	}

	if !s.RecallSlot(context.Background(), 3) {
		t.Fatal("RecallSlot did not claim the library track key")
	}
	wantLabel(t, queueFile(t, s), "Boston", "Third Stage")
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, _ := s.presets.Get(3)
		if got.Artist == "Boston" && got.Album == "Third Stage" {
			if got.DurationSec != 256 || got.Source != "MiniDLNA" || got.Name != "Amanda" {
				t.Fatalf("storing the label changed the key: %+v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the recovered label was not stored on the key: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A replay from Recently played or Favourites that names no artist (a card
// from before, an older app) is labelled from what the agent knows.
func TestAPlayThatNamesNoArtistGetsTheOneTheAgentKnows(t *testing.T) {
	s, _ := newPlayTestServer(t)
	if err := s.presets.SetSlot(labelledKey(5)); err != nil {
		t.Fatal(err)
	}
	playLoneFile(t, s, `{"url":"`+labelTrackURL+`","title":"Amanda","mime":"audio/flac"}`)
	wantLabel(t, queueFile(t, s), "Boston", "Third Stage")

	// What the request names always wins.
	playLoneFile(t, s, `{"url":"`+labelTrackURL+`","title":"Amanda","mime":"audio/flac","artist":"Tom Scholz"}`)
	wantLabel(t, queueFile(t, s), "Tom Scholz", "")
}

// The power-on resume pushes the record of the last play again, often after
// the agent restarted over a long standby: no end watch holds the label then,
// the persisted record does.
func TestTheLabelSurvivesAnAgentRestart(t *testing.T) {
	s, _ := newPlayTestServer(t)
	s.lastPlayPath = filepath.Join(t.TempDir(), "last-play.json")
	playLoneFile(t, s, `{"url":"`+labelTrackURL+`","title":"Amanda","mime":"audio/flac","artist":"Boston","album":"Third Stage"}`)

	s2, _ := newPlayTestServer(t)
	s2.lastPlayPath = s.lastPlayPath
	s2.loadLastPlay()
	if _, ok := s2.currentLoneFile(); ok {
		t.Fatal("a fresh agent cannot have an end watch on the file")
	}
	wantLabel(t, queueFile(t, s2), "Boston", "Third Stage")

	// A running queue hides it, and any newer play replaces it.
	s2.queue.mu.Lock()
	s2.queue.items, s2.queue.order, s2.queue.active = []queueItem{{Title: "a"}}, []int{0}, true
	s2.queue.mu.Unlock()
	if file := queueFile(t, s2); file != nil {
		t.Errorf("an active queue still names the file: %v", file)
	}
	s2.queue.mu.Lock()
	s2.queue.active = false
	s2.queue.mu.Unlock()
	s2.setLastPlay("http://192.0.2.1:8888/stream/1", "A Station", "", "audio/mpeg")
	if file := queueFile(t, s2); file != nil {
		t.Errorf("a station carries the old file: %v", file)
	}
}

// A record without a label (every station, every folder track, a file nobody
// named an artist for) never becomes a lone file through the record alone.
func TestThePlayRecordAloneNamesOnlyALabelledFile(t *testing.T) {
	s, _ := newPlayTestServer(t)
	s.setLastPlay(labelTrackURL, "Amanda", "", "audio/flac")
	if _, ok := s.lastPlayLoneFile(); ok {
		t.Error("an unlabelled record became a lone file")
	}
	s.recordLastPlay(&lastPlayInfo{boxURL: "http://192.0.2.10:8200/live", title: "Station", artist: "x"})
	if _, ok := s.lastPlayLoneFile(); ok {
		t.Error("a record without a MIME became a lone file")
	}
}

func TestAPresetSaveKeepsAndBoundsTheLabel(t *testing.T) {
	s := newSaveGateServer(t)
	path := filepath.Join(t.TempDir(), "presets.json")
	store, err := presets.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.presets = store
	p := labelledKey(2)
	p.Artist = "  Bos\x00ton\r\n"
	p.Album = strings.Repeat("ä", 5000)
	saved, ref := s.savePresetSlot(context.Background(), presetSaveRequest{Slot: 2, Preset: p})
	if ref != nil {
		t.Fatalf("save refused: %+v", ref)
	}
	got, _ := s.presets.Get(2)
	if got.Artist != "Bos ton" || saved.Artist != "Bos ton" {
		t.Errorf("artist = %q (answer %q), want it cleaned", got.Artist, saved.Artist)
	}
	if n := utf8.RuneCountInString(got.Album); n != trackTextMax {
		t.Errorf("album kept %d runes, want %d", n, trackTextMax)
	}
	// A reload reads them back.
	again, err := presets.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := again.Get(2); r.Artist != "Bos ton" || r.Album != got.Album {
		t.Errorf("the label did not survive the store: %+v", r)
	}
}

// A save that names no artist: the hold that saves what plays, an older app,
// or a rename written by an app that read the key without the fields.
func TestAPresetSaveWithoutALabelTakesWhatTheAgentKnows(t *testing.T) {
	s, _ := newPlayTestServer(t)
	ctx := context.Background()
	playLoneFile(t, s, `{"url":"`+labelTrackURL+`","title":"Amanda","mime":"audio/flac","artist":"Boston","album":"Third Stage"}`)

	bare := labelledKey(4)
	bare.Artist, bare.Album = "", ""
	if _, ref := s.savePresetSlot(ctx, presetSaveRequest{Slot: 4, Preset: bare}); ref != nil {
		t.Fatalf("save refused: %+v", ref)
	}
	if got, _ := s.presets.Get(4); got.Artist != "Boston" || got.Album != "Third Stage" {
		t.Fatalf("the playing track's label was not stored: %+v", got)
	}

	// Something else plays; the same key is written again without the fields.
	s.setLastPlay("http://192.0.2.1:8888/stream/1", "A Station", "", "audio/mpeg")
	bare.Name = "Amanda (live)"
	if _, ref := s.savePresetSlot(ctx, presetSaveRequest{Slot: 4, Preset: bare}); ref != nil {
		t.Fatalf("rename refused: %+v", ref)
	}
	if got, _ := s.presets.Get(4); got.Name != "Amanda (live)" || got.Artist != "Boston" || got.Album != "Third Stage" {
		t.Fatalf("a write without the fields stripped the label: %+v", got)
	}
}

func TestOnlyATrackKeyKeepsALabel(t *testing.T) {
	for _, typ := range []string{"spotify", "queue", presets.TypeNative} {
		p := presets.Preset{Type: typ, Artist: "x", Album: "y"}
		boundPresetTrackLabel(&p)
		if p.Artist != "" || p.Album != "" {
			t.Errorf("a %s key kept a label: %+v", typ, p)
		}
	}
	p := presets.Preset{Type: "radio", Artist: " Boston ", Album: strings.Repeat("x", 999)}
	boundPresetTrackLabel(&p)
	if p.Artist != "Boston" || utf8.RuneCountInString(p.Album) != trackTextMax {
		t.Errorf("a track key's label was not bounded: %q, %d runes", p.Artist, utf8.RuneCountInString(p.Album))
	}
}

// The copy to another speaker and the backup restore write the whole set
// through PUT /api/presets: the label goes across, bounded.
func TestACopiedSetCarriesTheLabel(t *testing.T) {
	s, store := newBulkServer(t)
	long := labelledKey(2)
	long.StreamURL = "http://192.0.2.10:8200/MediaItems/43.flac"
	long.Artist = strings.Repeat("a", 4000)
	rec := bulkPut(t, s, []presets.Preset{labelledKey(1), long,
		{Slot: 3, Name: "Mix", Type: "spotify", URI: "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M", Artist: "x"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk write: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := store.Get(1); got.Artist != "Boston" || got.Album != "Third Stage" || got.DurationSec != 256 {
		t.Errorf("the copied key lost its label: %+v", got)
	}
	if got, _ := store.Get(2); utf8.RuneCountInString(got.Artist) != trackTextMax {
		t.Errorf("a copied label was not bounded: %d runes", utf8.RuneCountInString(got.Artist))
	}
	if got, _ := store.Get(3); got.Artist != "" {
		t.Errorf("a Spotify key kept an artist: %+v", got)
	}
	// And GET /api/presets, which the copy reads the source with, hands it out.
	w := httptest.NewRecorder()
	s.handlePresets(w, httptest.NewRequest(http.MethodGet, "/api/presets", nil))
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range out {
		if p["slot"] == float64(1) {
			found = p["artist"] == "Boston" && p["album"] == "Third Stage"
		}
	}
	if !found {
		t.Errorf("GET /api/presets does not hand out the label: %s", w.Body.String())
	}
}

func TestALibraryFavouriteKeepsItsLabel(t *testing.T) {
	s := favServer(t)
	body := `[
	  {"stationuuid":"` + labelTrackURL + `","name":"Amanda","url":"` + labelTrackURL + `","source":"upnp","mime":"audio/flac","artist":"Boston","album":"` + strings.Repeat("ä", 3000) + `"},
	  {"stationuuid":"a","name":"1LIVE","url_resolved":"http://example/1live","artist":"not a track"}
	]`
	if code, _ := putFavs(t, s, body); code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	got := getFavs(t, s)
	if len(got) != 2 {
		t.Fatalf("stored list = %+v", got)
	}
	if got[0].Artist != "Boston" || utf8.RuneCountInString(got[0].Album) != trackTextMax {
		t.Errorf("track favourite: artist %q, album of %d runes", got[0].Artist, utf8.RuneCountInString(got[0].Album))
	}
	if got[1].Artist != "" {
		t.Errorf("a station kept an artist: %+v", got[1])
	}
	// A favourite without them is written as before: no empty keys.
	b, _ := json.Marshal(Favorite{UUID: labelTrackURL, Name: "Amanda", URL: labelTrackURL, Source: "upnp"})
	if strings.Contains(string(b), `"artist"`) || strings.Contains(string(b), `"album"`) {
		t.Errorf("an unlabelled favourite marshals the keys: %s", b)
	}
}

// The title is a client's text too: it leaves GET /api/queue bounded.
func TestTheLoneFilesTitleIsBounded(t *testing.T) {
	s, _ := newPlayTestServer(t)
	body, _ := json.Marshal(map[string]any{"url": labelTrackURL, "title": strings.Repeat("ä", 5000), "mime": "audio/flac", "artist": "Boston"})
	playLoneFile(t, s, string(body))
	title, _ := queueFile(t, s)["title"].(string)
	if n := utf8.RuneCountInString(title); n != trackTextMax {
		t.Errorf("title of %d runes, want %d", n, trackTextMax)
	}
}

// A folder saved from the live queue (the hold on a key) keeps each track's
// album next to its artist, as the app's own folder save always has.
func TestAFolderKeySavedFromTheQueueKeepsTheAlbum(t *testing.T) {
	p, ok := queuePresetFromLive(2, []queueItem{
		{URL: "http://192.0.2.10:8200/1.flac", Title: "Amanda", Artist: "Boston", Album: "Third Stage"},
	}, false, recentCardCtx{name: "Third Stage"})
	if !ok || len(p.Items) != 1 {
		t.Fatalf("no folder preset: %+v", p)
	}
	if it := p.Items[0]; it.Artist != "Boston" || it.Album != "Third Stage" {
		t.Errorf("the saved track lost its label: %+v", it)
	}
	if q := presetItemsToQueue(p.Items); q[0].Artist != "Boston" || q[0].Album != "Third Stage" {
		t.Errorf("the recalled queue lost it: %+v", q[0])
	}
}

// The phone page sends what a favourite and a Recently-played card keep.
func TestPhoneRemoteSendsTheLabelOfAFavouriteAndARecentCard(t *testing.T) {
	page := strings.ReplaceAll(indexHTML, "\r\n", "\n")
	for name, want := range map[string][]string{
		"async function playLibraryFav(": {"artist: st.artist || ''", "album: st.album || ''"},
		"async function playRecent(":     {"artist: e.artist || ''", "album: e.album || ''"},
	} {
		start := strings.Index(page, name)
		if start < 0 {
			t.Fatalf("%s is gone; if it moved, move this test with it", name)
		}
		fn := page[start:]
		if end := strings.Index(fn, "\n}\n"); end > 0 {
			fn = fn[:end]
		}
		for _, w := range want {
			if !strings.Contains(fn, w) {
				t.Errorf("%s does not send %q", name, w)
			}
		}
	}
}
