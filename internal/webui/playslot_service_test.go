package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// The playSlot service behind POST /api/play/<slot> and recallSlotClean.
//
// recallSlotClean used to drive the HTTP handler with a throwaway
// ResponseWriter and read the status back out. Now it calls the service; these
// tests pin that an in-process recall sees the same refusals the wire does.

// newFailingBoxServer is newPlayTestServer with a box that answers every SOAP
// action with 500 and the given fault text, so a recall's 502 and the
// box-grouped 409 can be reached without a speaker.
func newFailingBoxServer(t *testing.T, fault string) *Server {
	t.Helper()
	box := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, fault, http.StatusInternalServerError)
	}))
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
	t.Cleanup(func() { s.stopQueue("test cleanup") })
	return s
}

func TestRecallSlotCleanReportsTheRefusalStatus(t *testing.T) {
	t.Parallel()
	s, rec := newPlayTestServer(t)
	seedRadioPreset(t, s, 1)
	if err := s.presets.SetSlot(presets.Preset{Slot: 2, Name: "Mix", Type: "spotify", URI: "spotify:playlist:x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.presets.SetSlot(presets.Preset{Slot: 4, Name: "Gone folder", Type: "queue"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		slot int
		want int
		why  string
	}{
		{3, http.StatusNotFound, "no preset on the slot"},
		{4, http.StatusUnprocessableEntity, "a folder key with no tracks"},
		{2, http.StatusServiceUnavailable, "a Spotify key on a box without the engine"},
		{1, http.StatusOK, "a radio key the box took"},
	}
	for _, c := range cases {
		if got := s.recallSlotClean(context.Background(), c.slot); got != c.want {
			t.Errorf("slot %d (%s): status %d, want %d", c.slot, c.why, got, c.want)
		}
	}
	if !rec.has("Play") {
		t.Errorf("the delivered recall never reached the box: %v", rec.list())
	}
}

// What an in-process caller can read off a refusal without parsing JSON.
func TestPlaySlotRefusalCarriesCodeAndError(t *testing.T) {
	t.Parallel()
	s, _ := newPlayTestServer(t)
	if err := s.presets.SetSlot(presets.Preset{Slot: 1, Name: "Mix", Type: "spotify", URI: "spotify:playlist:x"}); err != nil {
		t.Fatal(err)
	}
	s.spotifyPlay = func(context.Context, string, string, bool, bool) error { return nil }
	s.spotifyCanRecall = func(context.Context) bool { return false }

	_, ref := s.playSlot(context.Background(), 1)
	if ref == nil {
		t.Fatal("a speaker never picked in Spotify was not refused")
	}
	if ref.Status != http.StatusUnprocessableEntity || ref.Code != "spotify-not-logged-in" {
		t.Errorf("refusal = %d %q, want 422 spotify-not-logged-in", ref.Status, ref.Code)
	}
	if !strings.Contains(ref.Error, "Spotify Premium") || ref.JSON == nil {
		t.Errorf("refusal lost its message or body: %+v", ref)
	}

	_, ref = s.playSlot(context.Background(), 5)
	if ref == nil || ref.Status != http.StatusNotFound || ref.JSON != nil || ref.Error != "preset not configured" {
		t.Errorf("text refusal = %+v, want 404 plain text", ref)
	}

	g := newFailingBoxServer(t, "Can't control member of group")
	seedRadioPreset(t, g, 1)
	_, ref = g.playSlot(context.Background(), 1)
	if ref == nil || ref.Status != http.StatusConflict || ref.Error != "box-grouped" {
		t.Errorf("grouped follower refusal = %+v, want 409 box-grouped", ref)
	}
}

// The delivered result renders the same 200 body the handler always wrote.
func TestPlayResultBodyKeepsTheTypeKeyOptional(t *testing.T) {
	t.Parallel()
	radio := playResult{Slot: 1, Name: "A"}
	if got := radio.body(); len(got) != 3 || got["status"] != "playing" {
		t.Errorf("radio body = %v", got)
	}
	queue := playResult{Slot: 1, Name: "A", Type: "queue"}
	if got := queue.body(); got["type"] != "queue" {
		t.Errorf("queue body = %v", got)
	}
}

// The wire contract of POST /api/play/<slot>, one golden per exit. The phone
// remote reads code and error, the desktop app passes the box-grouped and
// stereo-incomplete bodies through raw, groupkeys and the alarms read the
// status: a change in any of these lines is a change in what they see.
func TestHandlePlaySlotWire(t *testing.T) {
	t.Parallel()
	spotifyOn := func(s *Server) {
		s.spotifyPlay = func(context.Context, string, string, bool, bool) error { return nil }
		s.spotifyCanRecall = func(context.Context) bool { return true }
	}
	cases := []struct {
		name   string
		method string
		path   string
		setup  func(t *testing.T) *Server
		status int
		body   string
	}{
		{"method", http.MethodGet, "/api/play/1", func(t *testing.T) *Server { s, _ := newPlayTestServer(t); return s },
			http.StatusMethodNotAllowed, "method not allowed\n"},
		{"no renderer", http.MethodPost, "/api/play/1", func(t *testing.T) *Server {
			s, _ := newPlayTestServer(t)
			s.renderer = nil
			return s
		}, http.StatusServiceUnavailable, "renderer not configured\n"},
		{"no renderer beats a bad slot", http.MethodPost, "/api/play/x", func(t *testing.T) *Server {
			s, _ := newPlayTestServer(t)
			s.renderer = nil
			return s
		}, http.StatusServiceUnavailable, "renderer not configured\n"},
		{"no store", http.MethodPost, "/api/play/1", func(t *testing.T) *Server {
			s, _ := newPlayTestServer(t)
			s.presets = nil
			return s
		}, http.StatusServiceUnavailable, "presets store not initialized\n"},
		{"bad slot", http.MethodPost, "/api/play/x", func(t *testing.T) *Server { s, _ := newPlayTestServer(t); return s },
			http.StatusBadRequest, "invalid slot\n"},
		{"empty slot", http.MethodPost, "/api/play/3", func(t *testing.T) *Server { s, _ := newPlayTestServer(t); return s },
			http.StatusNotFound, "preset not configured\n"},
		{"empty folder", http.MethodPost, "/api/play/4", func(t *testing.T) *Server {
			s, _ := newPlayTestServer(t)
			_ = s.presets.SetSlot(presets.Preset{Slot: 4, Name: "Gone folder", Type: "queue"})
			return s
		}, http.StatusUnprocessableEntity, "preset has no playable tracks\n"},
		{"legacy spotify", http.MethodPost, "/api/play/5", func(t *testing.T) *Server {
			s, _ := newPlayTestServer(t)
			_ = s.presets.SetSlot(presets.Preset{Slot: 5, Name: "Old mix", Type: "radio", StreamURL: "/playback/container/not-a-url"})
			return s
		}, http.StatusUnprocessableEntity,
			`{"code":"spotify-preset-unreplayable","error":"This Spotify preset was saved in an older version and can't be replayed. Please open the playlist and save it to the preset again.","name":"Old mix","slot":5}` + "\n"},
		{"spotify not configured", http.MethodPost, "/api/play/1", func(t *testing.T) *Server {
			s, _ := newPlayTestServer(t)
			_ = s.presets.SetSlot(presets.Preset{Slot: 1, Name: "Mix", Type: "spotify", URI: "spotify:playlist:x"})
			return s
		}, http.StatusServiceUnavailable, `{"error":"Spotify not configured","name":"Mix","slot":1}` + "\n"},
		{"spotify not logged in", http.MethodPost, "/api/play/1", func(t *testing.T) *Server {
			s, _ := newPlayTestServer(t)
			_ = s.presets.SetSlot(presets.Preset{Slot: 1, Name: "Mix", Type: "spotify", URI: "spotify:playlist:x"})
			spotifyOn(s)
			s.spotifyCanRecall = func(context.Context) bool { return false }
			return s
		}, http.StatusUnprocessableEntity,
			`{"code":"spotify-not-logged-in","error":"A Spotify preset needs two things: a Spotify Premium account, and this speaker picked once in Spotify. In the Spotify app on a device on the same Wi-Fi, tap the Connect/devices icon, choose the entry for this speaker ending in (STR), and play any track. A free Spotify account cannot pick that entry.","name":"Mix","slot":1}` + "\n"},
		{"spotify premium required", http.MethodPost, "/api/play/1", func(t *testing.T) *Server {
			s, _ := newPlayTestServer(t)
			_ = s.presets.SetSlot(presets.Preset{Slot: 1, Name: "Mix", Type: "spotify", URI: "spotify:playlist:x"})
			spotifyOn(s)
			s.spotifyPremiumRequired = func() bool { return true }
			return s
		}, http.StatusUnprocessableEntity,
			`{"code":"spotify-premium-required","error":"This speaker's Spotify account is free. Spotify preset recall needs Spotify Premium.","name":"Mix","slot":1}` + "\n"},
		{"stereo incomplete", http.MethodPost, "/api/play/1", func(t *testing.T) *Server {
			s, _ := newPlayTestServer(t)
			seedRadioPreset(t, s, 1)
			s.noteStereoSeen(brokenPair())
			return s
		}, http.StatusConflict,
			`{"error":"stereo-incomplete","master":"DEV#MASTER","members":[{"deviceID":"DEV#SELF","ip":"192.0.2.26","role":"LEFT"},{"deviceID":"DEV#MASTER","ip":"192.0.2.25","role":"RIGHT"}],"pairName":"","partnerID":"","partnerIP":"","reason":"group-error"}` + "\n"},
		{"grouped follower", http.MethodPost, "/api/play/1", func(t *testing.T) *Server {
			s := newFailingBoxServer(t, "Can't control member of group")
			seedRadioPreset(t, s, 1)
			return s
		}, http.StatusConflict, `{"error":"box-grouped"}` + "\n"},
		{"radio refused by the box", http.MethodPost, "/api/play/1", func(t *testing.T) *Server {
			s := newFailingBoxServer(t, "boom")
			seedRadioPreset(t, s, 1)
			return s
		}, http.StatusBadGateway,
			`{"detail":"SetURI: soap SetAVTransportURI status 500: boom\n","error":"Station could not be played","name":"Test Station","slot":1}` + "\n"},
		{"radio playing", http.MethodPost, "/api/play/1", func(t *testing.T) *Server {
			s, _ := newPlayTestServer(t)
			seedRadioPreset(t, s, 1)
			return s
		}, http.StatusOK, `{"name":"Test Station","slot":1,"status":"playing"}` + "\n"},
		{"spotify playing", http.MethodPost, "/api/play/1", func(t *testing.T) *Server {
			s, _ := newPlayTestServer(t)
			_ = s.presets.SetSlot(presets.Preset{Slot: 1, Name: "Mix", Type: "spotify", URI: "spotify:playlist:x"})
			spotifyOn(s)
			return s
		}, http.StatusOK, `{"name":"Mix","slot":1,"status":"playing","type":"spotify"}` + "\n"},
		{"folder playing", http.MethodPost, "/api/play/4", func(t *testing.T) *Server {
			s, _ := newPlayTestServer(t)
			_ = s.presets.SetSlot(presets.Preset{Slot: 4, Name: "Folder", Type: "queue", Items: []presets.PresetItem{
				{URL: "http://192.0.2.10:50002/a.mp3", Title: "A", Mime: "audio/mpeg"},
			}})
			return s
		}, http.StatusOK, `{"name":"Folder","slot":4,"status":"playing","type":"queue"}` + "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := c.setup(t)
			w := httptest.NewRecorder()
			s.handlePlaySlot(w, httptest.NewRequest(c.method, c.path, nil))
			if w.Code != c.status {
				t.Errorf("status = %d, want %d", w.Code, c.status)
			}
			if got := w.Body.String(); got != c.body {
				t.Errorf("body =\n%s\nwant\n%s", got, c.body)
			}
		})
	}
}
