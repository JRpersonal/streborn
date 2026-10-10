package webui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/upnp"
)

// handoverBox is a fake speaker for the native-station handover (#1065). Its
// UPnP transport accepts every SOAP call, exactly like the ST10 in the report,
// and its source only turns to UPNP once takeAfter pushes have arrived, each of
// them behind a STOP key when needsStop is set. takeAfter 0 means the speaker
// never lets go of its native station.
type handoverBox struct {
	mu        sync.Mutex
	events    []string // "stop" and "push", in order
	pushes    int
	takeAfter int
	source    string
}

func (b *handoverBox) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("SOAPACTION"), "#SetAVTransportURI") {
			b.mu.Lock()
			b.pushes++
			b.events = append(b.events, "push")
			if b.takeAfter > 0 && b.pushes >= b.takeAfter {
				b.source = "UPNP"
			}
			b.mu.Unlock()
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope><s:Body/></s:Envelope>`))
	}
}

func (b *handoverBox) currentSource() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.source
}

func (b *handoverBox) stopKey(context.Context) bool {
	b.mu.Lock()
	b.events = append(b.events, "stop")
	b.mu.Unlock()
	return true
}

func (b *handoverBox) seen() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.events...)
}

// fastHandover shrinks the verify window so a test does not wait real seconds.
func fastHandover(t *testing.T) {
	t.Helper()
	window, poll := nativeHandoverWindow, nativeHandoverPoll
	nativeHandoverWindow, nativeHandoverPoll = 60*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { nativeHandoverWindow, nativeHandoverPoll = window, poll })
}

func newHandoverServer(t *testing.T, b *handoverBox) *Server {
	t.Helper()
	fastHandover(t)
	srv := httptest.NewServer(b.handler())
	t.Cleanup(srv.Close)
	return &Server{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		queue:        newPlayQueue(),
		renderer:     &upnp.Renderer{ControlURL: srv.URL, Client: srv.Client()},
		boxSourceFn:  b.currentSource,
		nativeStopFn: b.stopKey,
	}
}

func joined(ev []string) string { return strings.Join(ev, ",") }

// The common case must not change: a speaker already on STR's own stream gets
// the push and nothing else, no stop key and no second push.
func TestHandoverLeavesOtherSourcesAlone(t *testing.T) {
	for _, src := range []string{"UPNP", "STANDBY", "INVALID_SOURCE", "BLUETOOTH", ""} {
		b := &handoverBox{source: src} // never turns to UPNP on its own
		s := newHandoverServer(t, b)
		err := s.pushOverNativeStation(t.Context(), "Station", func() error {
			return s.renderer.PlayURL(t.Context(), "http://stream.example/a", "Station", "")
		})
		if err != nil {
			t.Errorf("source %q: err = %v, want nil", src, err)
		}
		if got := joined(b.seen()); got != "push" {
			t.Errorf("source %q: speaker saw %q, want a single push", src, got)
		}
	}
}

// The #1065 case where one stop is enough: the native station is stopped
// BEFORE the push, and the push then takes.
func TestHandoverStopsTheNativeStationBeforeThePush(t *testing.T) {
	b := &handoverBox{source: nativeRadioSource, takeAfter: 1}
	s := newHandoverServer(t, b)
	w := httptest.NewRecorder()
	s.handlePlay(w, httptest.NewRequest(http.MethodPost, "/api/play",
		strings.NewReader(`{"url":"http://stream.example/nova","title":"Nova"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if got := joined(b.seen()); got != "stop,push" {
		t.Errorf("speaker saw %q, want stop,push", got)
	}
}

// The speaker swallows the first push: STR stops the station again and pushes
// once more, and the second push counts as the play.
func TestHandoverRetriesOnceWhenThePushIsSwallowed(t *testing.T) {
	b := &handoverBox{source: nativeRadioSource, takeAfter: 2}
	s := newHandoverServer(t, b)
	err := s.pushOverNativeStation(t.Context(), "Station", func() error {
		return s.renderer.PlayURL(t.Context(), "http://stream.example/a", "Station", "")
	})
	if err != nil {
		t.Fatalf("err = %v, want nil after the second push took", err)
	}
	if got := joined(b.seen()); got != "stop,push,stop,push" {
		t.Errorf("speaker saw %q, want stop,push,stop,push", got)
	}
}

// The field symptom itself: every SOAP call succeeds, the speaker keeps its
// native station. The app must get an error, not "playing".
func TestHandoverReportsAPlayTheSpeakerIgnored(t *testing.T) {
	b := &handoverBox{source: nativeRadioSource}
	s := newHandoverServer(t, b)
	w := httptest.NewRecorder()
	s.handlePlay(w, httptest.NewRequest(http.MethodPost, "/api/play",
		strings.NewReader(`{"url":"http://stream.example/nova","title":"Nova"}`)))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if !strings.Contains(resp["detail"], "own radio station") {
		t.Errorf("detail = %q, want the native-station explanation", resp["detail"])
	}
	if got := joined(b.seen()); got != "stop,push,stop,push" {
		t.Errorf("speaker saw %q, want exactly one retry (stop,push,stop,push)", got)
	}
}

// The queue (a folder started from the app) shares the push, so it shares the
// handover and its failure too.
func TestHandoverCoversTheQueuePush(t *testing.T) {
	b := &handoverBox{source: nativeRadioSource}
	s := newHandoverServer(t, b)
	err := s.pushStream(t.Context(), "http://media.example/a.mp3", "Track", "", "audio/mpeg", "", 0)
	if !errors.Is(err, errNativeStationKept) {
		t.Fatalf("err = %v, want errNativeStationKept", err)
	}
	if s.lastPlay != nil {
		t.Error("a push the speaker ignored was recorded as the last play")
	}
}

// A push the transport itself refuses keeps its own error, and no retry runs
// on top of it: the handover only adds the source check to a push that
// answered success.
func TestHandoverKeepsThePushError(t *testing.T) {
	b := &handoverBox{source: nativeRadioSource}
	s := newHandoverServer(t, b)
	boom := errors.New("connection refused")
	calls := 0
	err := s.pushOverNativeStation(t.Context(), "Station", func() error {
		calls++
		return boom
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the push's own error", err)
	}
	if calls != 1 {
		t.Errorf("push ran %d times, want 1", calls)
	}
}

// Every app path that pushes a UPnP stream must go through the handover. The
// Spotify key and the auto-attach need a live engine to drive end to end, so
// they are pinned against the source like undoWakeAutoResume's stop order.
func TestEveryUPnPPushPathUsesTheHandover(t *testing.T) {
	cases := []struct {
		file, fn, want string
	}{
		{"playback.go", "func (s *Server) handlePlaySlot", "s.pushOverNativeStation(playCtx, p.Name"},
		{"boxsettings_http.go", "func (s *Server) playTrackWithWrongStateRepair", "s.pushOverNativeStation("},
		{"librarykey.go", "func (s *Server) playLibraryFilePresetLocked", "s.pushOverNativeStation("},
		{"queueplay.go", "func (s *Server) pushStream", "s.pushOverNativeStation("},
		{"../../cmd/agent/main.go", "spotifyMgr.SetOnActivate(", "webuiSrv.PushOverNativeStation("},
	}
	for _, tc := range cases {
		src, err := readSourceFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		i := strings.Index(src, tc.fn)
		if i < 0 {
			t.Fatalf("%s: %q not found", tc.file, tc.fn)
		}
		body := src[i:]
		if j := strings.Index(body, "\n}\n"); j > 0 && !strings.HasPrefix(tc.fn, "spotifyMgr") {
			body = body[:j]
		} else if j := strings.Index(body, "\n\t})\n"); j > 0 {
			body = body[:j]
		}
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s: %s pushes to the speaker without the native-station handover", tc.file, tc.fn)
		}
	}
}
