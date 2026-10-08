package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
)

func queueSaveTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		queue:   newPlayQueue(),
		presets: store,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

var queueSaveTracks = []queueItem{
	{URL: "http://192.0.2.10:9000/disk/01.mp3", Title: "Come Together", Art: "http://192.0.2.10:9000/art/1.jpg", Mime: "audio/mpeg", Duration: 259 * time.Second, Artist: "The Beatles"},
	{URL: "", Title: "no url, dropped"},
	{URL: "http://192.0.2.10:9000/disk/02.mp3", Title: "Something", Mime: "audio/mpeg", Duration: 182 * time.Second},
}

// The folder preset built from the live queue has the exact shape the Library
// star button stores: name, type queue, shuffle, source, tracks in folder order.
func TestQueuePresetFromLiveMatchesTheStarButton(t *testing.T) {
	p, ok := queuePresetFromLive(2, queueSaveTracks, true, recentCardCtx{key: "queue:uuid:abc:64$1", name: "Abbey Road", source: "Living Room NAS"})
	if !ok {
		t.Fatal("no preset")
	}
	if p.Slot != 2 || p.Type != "queue" || p.Name != "Abbey Road" || p.Source != "Living Room NAS" || !p.Shuffle || p.Art != "" {
		t.Fatalf("preset %+v", p)
	}
	if len(p.Items) != 2 {
		t.Fatalf("items = %d, want 2 (the URL-less one dropped)", len(p.Items))
	}
	want := presets.PresetItem{URL: "http://192.0.2.10:9000/disk/01.mp3", Title: "Come Together", Art: "http://192.0.2.10:9000/art/1.jpg", Mime: "audio/mpeg", DurationSec: 259, Artist: "The Beatles"}
	if p.Items[0] != want {
		t.Fatalf("item 0 = %+v, want %+v", p.Items[0], want)
	}

	// No folder name: the server name, then a fixed fallback.
	if p, _ := queuePresetFromLive(1, queueSaveTracks, false, recentCardCtx{source: "NAS"}); p.Name != "NAS" {
		t.Fatalf("name = %q, want the server name", p.Name)
	}
	if p, _ := queuePresetFromLive(1, queueSaveTracks, false, recentCardCtx{}); p.Name != queueFolderFallbackName {
		t.Fatalf("name = %q, want the fallback", p.Name)
	}
	if _, ok := queuePresetFromLive(1, []queueItem{{Title: "x"}}, false, recentCardCtx{}); ok {
		t.Fatal("a queue without playable tracks must give no preset")
	}
}

func TestLiveQueuePreset(t *testing.T) {
	s := queueSaveTestServer(t)
	if _, ok := s.LiveQueuePreset(1); ok {
		t.Fatal("no queue plays: want no preset")
	}

	// A folder started by an app too old to send the server name: the
	// registered server list names it.
	reg, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(mediaservers.Server{ID: "abc-123", Name: "Living Room NAS"}); err != nil {
		t.Fatal(err)
	}
	s.mediaServers = reg
	s.queue.load(queueSaveTracks, 0, false, repeatOff)
	s.queueFolder = recentCardCtx{key: "queue:uuid:abc-123:64$1", name: "Abbey Road"}
	p, ok := s.LiveQueuePreset(4)
	if !ok || p.Source != "Living Room NAS" || p.Name != "Abbey Road" || p.Slot != 4 {
		t.Fatalf("preset %+v ok=%v", p, ok)
	}

	// A queue recalled from folder key 5 is that key's preset, copied whole.
	saved := presets.Preset{Slot: 5, Name: "Saved", Type: "queue", Source: "Other NAS", Art: "http://192.0.2.10/c.jpg",
		Items: []presets.PresetItem{{URL: "http://192.0.2.10:9000/disk/01.mp3"}}}
	if err := s.presets.SetSlot(saved); err != nil {
		t.Fatal(err)
	}
	s.queueFolder = recentCardCtx{key: "queue:slot:5", name: "Saved"}
	p, ok = s.LiveQueuePreset(2)
	if !ok || p.Slot != 2 || p.Name != "Saved" || p.Source != "Other NAS" || p.Art != saved.Art || len(p.Items) != 1 {
		t.Fatalf("copied preset %+v ok=%v", p, ok)
	}
}

func TestHandleQueueSaveSlot(t *testing.T) {
	s := queueSaveTestServer(t)
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.handleQueueSaveSlot(rec, httptest.NewRequest(http.MethodPost, "/api/queue/save-slot", strings.NewReader(body)))
		return rec
	}
	if rec := post(`{"slot":3}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no-queue") {
		t.Fatalf("no queue: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(`{"slot":9}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad slot: %d", rec.Code)
	}
	s.queue.load(queueSaveTracks, 0, true, repeatOff)
	s.queueFolder = recentCardCtx{key: "queue:uuid:abc:64$1", name: "Abbey Road", source: "Living Room NAS"}
	rec := post(`{"slot":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	var resp presets.Preset
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	got, ok := s.presets.Get(3)
	if !ok || got.Type != "queue" || got.Name != "Abbey Road" || got.Source != "Living Room NAS" || !got.Shuffle || len(got.Items) != 2 {
		t.Fatalf("stored %+v", got)
	}
	if resp.Name != got.Name || len(resp.Items) != 2 {
		t.Fatalf("answer %+v", resp)
	}
}
