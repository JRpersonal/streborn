package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// soapRecorder is a fake AVTransport endpoint that records the SOAP actions
// the renderer sends, so a test can assert a play actually REACHED the box.
type soapRecorder struct {
	mu      sync.Mutex
	actions []string
}

func (rec *soapRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	action := r.Header.Get("SOAPACTION")
	// `"urn:schemas-upnp-org:service:AVTransport:1#SetAVTransportURI"`
	if i := strings.Index(action, "#"); i >= 0 {
		action = strings.Trim(action[i+1:], `"`)
	}
	rec.mu.Lock()
	rec.actions = append(rec.actions, action)
	rec.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (rec *soapRecorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.actions)
}

func (rec *soapRecorder) list() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.actions...)
}

func (rec *soapRecorder) has(action string) bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, a := range rec.actions {
		if a == action {
			return true
		}
	}
	return false
}

// newPlayTestServer builds a Server whose renderer points at a recording fake
// AVTransport endpoint. boxHost stays empty so ensureBoxReady and the verify
// loops never touch the network.
func newPlayTestServer(t *testing.T) (*Server, *soapRecorder) {
	t.Helper()
	rec := &soapRecorder{}
	box := httptest.NewServer(rec)
	t.Cleanup(box.Close)
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("presets.Load: %v", err)
	}
	s := &Server{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		presets:  store,
		queue:    newPlayQueue(),
		renderer: &upnp.Renderer{ControlURL: box.URL, Client: box.Client()},
	}
	t.Cleanup(s.stopQueue)
	return s, rec
}

// cancelledRequest builds a request whose context is ALREADY cancelled, the
// state a play handler sees when the desktop app's HTTP timeout expired during
// the standby wake (#252: the app gives up after 6s, a wake can take ~8s).
func cancelledRequest(method, target, body string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	return req.WithContext(ctx)
}

func seedRadioPreset(t *testing.T, s *Server, slot int) {
	t.Helper()
	if err := s.presets.SetSlot(presets.Preset{
		Slot: slot, Name: "Test Station", Type: "radio",
		StreamURL: "http://stream.example/relax", Codec: "MP3",
	}); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}
}

// The #252 regression guard: a radio preset recall whose request context was
// cancelled (app timeout during a slow wake) must still push the stream to the
// box and answer 200, instead of failing instantly with "context canceled".
// This pins the context.WithoutCancel detach in handlePlaySlot - it looks like
// a mistake to a lint-minded refactor, and reverting it kills every preset
// recall on speakers whose wake outlasts the app's HTTP timeout.
func TestRadioPresetRecallSurvivesCancelledRequest(t *testing.T) {
	s, rec := newPlayTestServer(t)
	seedRadioPreset(t, s, 1)

	w := httptest.NewRecorder()
	s.handlePlaySlot(w, cancelledRequest(http.MethodPost, "/api/play/1", ""))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if !rec.has("SetAVTransportURI") || !rec.has("Play") {
		t.Errorf("play did not reach the box: recorded actions %v", rec.list())
	}
}

// Same guard for the library-direct preset branch (a saved NAS file).
func TestLibraryPresetRecallSurvivesCancelledRequest(t *testing.T) {
	s, rec := newPlayTestServer(t)
	if err := s.presets.SetSlot(presets.Preset{
		Slot: 3, Name: "Track", Source: "nas-udn",
		StreamURL: "http://192.0.2.10:50002/song.mp3",
	}); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}

	w := httptest.NewRecorder()
	s.handlePlaySlot(w, cancelledRequest(http.MethodPost, "/api/play/3", ""))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if !rec.has("SetAVTransportURI") || !rec.has("Play") {
		t.Errorf("play did not reach the box: recorded actions %v", rec.list())
	}
}

// Same guard for the Spotify preset branch. The cancelled request context used
// to kill the recall twice: spotifyCanRecall probed on it (misreporting "not
// picked in Spotify yet", a 422) and the slot-stream push ran on it. Both must
// use the detached context.
func TestSpotifyPresetRecallSurvivesCancelledRequest(t *testing.T) {
	s, rec := newPlayTestServer(t)
	if err := s.presets.SetSlot(presets.Preset{
		Slot: 2, Name: "My List", Type: "spotify", URI: "spotify:playlist:37i9dQZF1DWX7rdRjO",
	}); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}
	s.spotifyPlay = func(ctx context.Context, uri, account string, shuffle bool) error { return nil }
	// Reports "can recall" only on a live context, like the real probe: on the
	// raw cancelled request context this returns false and the handler answers
	// a bogus 422 instead of playing.
	s.spotifyCanRecall = func(ctx context.Context) bool { return ctx.Err() == nil }

	w := httptest.NewRecorder()
	s.handlePlaySlot(w, cancelledRequest(http.MethodPost, "/api/play/2", ""))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if !rec.has("SetAVTransportURI") || !rec.has("Play") {
		t.Errorf("slot stream push did not reach the box: recorded actions %v", rec.list())
	}
}

// Same guard for the ad-hoc play path (a station tapped in radio search, a
// single library track).
func TestAdHocPlaySurvivesCancelledRequest(t *testing.T) {
	s, rec := newPlayTestServer(t)

	w := httptest.NewRecorder()
	req := cancelledRequest(http.MethodPost, "/api/play",
		`{"url":"http://stream.example/relax","title":"Test Station"}`)
	s.handlePlay(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if !rec.has("SetAVTransportURI") || !rec.has("Play") {
		t.Errorf("play did not reach the box: recorded actions %v", rec.list())
	}
}

// Same guard for starting a library folder queue.
func TestQueueStartSurvivesCancelledRequest(t *testing.T) {
	s, rec := newPlayTestServer(t)

	w := httptest.NewRecorder()
	req := cancelledRequest(http.MethodPost, "/api/queue",
		`{"items":[{"url":"http://192.0.2.10:50002/a.mp3","title":"A","mime":"audio/mpeg"}]}`)
	s.handleQueue(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if !rec.has("SetAVTransportURI") || !rec.has("Play") {
		t.Errorf("first track push did not reach the box: recorded actions %v", rec.list())
	}
}

// A preset recall must end an active library queue: the queue watcher keeps
// evaluating the OLD track's timing, and when its wall-clock net tripped
// minutes later it yanked playback from the station the user explicitly chose
// back to the next queue track, seemingly at random.
func TestNonQueueRecallStopsActiveQueue(t *testing.T) {
	s, _ := newPlayTestServer(t)
	seedRadioPreset(t, s, 1)
	s.queue.load([]queueItem{
		{URL: "http://192.0.2.10:50002/a.mp3", Title: "A", Mime: "audio/mpeg"},
		{URL: "http://192.0.2.10:50002/b.mp3", Title: "B", Mime: "audio/mpeg"},
	}, 0, false, repeatOff)
	if !s.queue.isActive() {
		t.Fatal("queue should be active before the recall")
	}

	w := httptest.NewRecorder()
	s.handlePlaySlot(w, httptest.NewRequest(http.MethodPost, "/api/play/1", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if s.queue.isActive() {
		t.Error("radio preset recall left the library queue active (the watcher would advance over the station later)")
	}
}

// The hardware preset path reports its play via NoteLastPlay (cmd/agent goes
// straight to the renderer); that must end an active queue too, since a queue
// preset never reaches NoteLastPlay (RecallSlot claims it first).
func TestNoteLastPlayStopsActiveQueue(t *testing.T) {
	s, _ := newPlayTestServer(t)
	s.queue.load([]queueItem{
		{URL: "http://192.0.2.10:50002/a.mp3", Title: "A", Mime: "audio/mpeg"},
	}, 0, false, repeatOff)

	s.NoteLastPlay("http://127.0.0.1:8888/stream/2", "Station", "", "")

	if s.queue.isActive() {
		t.Error("hardware recall (NoteLastPlay) left the library queue active")
	}
}
