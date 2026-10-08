package webui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
)

// A folder played as a queue kept only the bare track titles in GET /api/queue,
// although the Library rows it came from named artist and album (#1033).
func TestQueueSnapshotCarriesArtistAndAlbum(t *testing.T) {
	q := newPlayQueue()
	q.load([]queueItem{
		{URL: "http://192.0.2.1/a.mp3", Title: "Amanda", Artist: "Boston", Album: "Third Stage"},
		{URL: "http://192.0.2.1/b.mp3", Title: "Untitled"},
	}, 0, false, repeatOff)
	snap := q.snapshot()
	if got := snap.Items[0]; got.Artist != "Boston" || got.Album != "Third Stage" || got.Title != "Amanda" {
		t.Fatalf("first item lost its metadata: %+v", got)
	}
	b, err := json.Marshal(snap.Items)
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	if !strings.Contains(js, `"artist":"Boston"`) || !strings.Contains(js, `"album":"Third Stage"`) {
		t.Fatalf("artist/album missing from the JSON: %s", js)
	}
	// A track the server named nothing for stays as small as before.
	if strings.Count(js, `"artist"`) != 1 || strings.Count(js, `"album"`) != 1 {
		t.Fatalf("empty artist/album must be omitted: %s", js)
	}
}

// The phone page sends artist and album with a folder play, and names them
// for the running song from the queue it already reads (#1033).
func TestPhoneRemoteQueueShowsArtistAndAlbum(t *testing.T) {
	start := strings.Index(indexHTML, "async function playBrowseFolder(")
	if start < 0 {
		t.Fatal("playBrowseFolder is gone; if it moved, move this test with it")
	}
	fn := indexHTML[start:]
	if end := strings.Index(fn, "async function playBrowseTrack("); end > 0 {
		fn = fn[:end]
	}
	for _, want := range []string{"artist: it.artist || ''", "album: it.album || ''"} {
		if !strings.Contains(fn, want) {
			t.Errorf("a folder play from the phone does not send %q", want)
		}
	}
	if !strings.Contains(indexHTML, "song = queueSongMeta(queue)") {
		t.Error("the now-playing line no longer names the queued song's artist and album")
	}
}

// The album travels from the app's queue start and from a saved folder preset
// into the queue, like the artist already did.
func TestQueueItemsKeepAlbum(t *testing.T) {
	var req queueStartRequest
	body := `{"items":[{"url":"http://192.0.2.1/a.mp3","title":"Amanda","artist":"Boston","album":"Third Stage"}]}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	items := toQueueItems(req.Items)
	if len(items) != 1 || items[0].Artist != "Boston" || items[0].Album != "Third Stage" {
		t.Fatalf("queue start dropped artist/album: %+v", items)
	}
	fromPreset := presetItemsToQueue([]presets.PresetItem{{URL: "http://192.0.2.1/a.mp3", Title: "Amanda", Artist: "Boston", Album: "Third Stage"}})
	if len(fromPreset) != 1 || fromPreset[0].Album != "Third Stage" || fromPreset[0].Artist != "Boston" {
		t.Fatalf("preset recall dropped artist/album: %+v", fromPreset)
	}
	// A preset saved before the album existed still loads.
	var old presets.PresetItem
	if err := json.Unmarshal([]byte(`{"url":"http://192.0.2.1/a.mp3","title":"Amanda"}`), &old); err != nil {
		t.Fatal(err)
	}
	if got := presetItemsToQueue([]presets.PresetItem{old}); len(got) != 1 || got[0].Album != "" {
		t.Fatalf("old preset item must load with an empty album: %+v", got)
	}
}
