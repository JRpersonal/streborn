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

	"github.com/JRpersonal/streborn/internal/boxapi"
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

// read is the box's answer on the Server's now_playing seam: its current
// source, nothing playing.
func (b *handoverBox) read(context.Context) (*boxapi.NowPlaying, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return scriptSnapshot(nowPlayingSnapshot{Source: b.source})
}

func (b *handoverBox) stopKey(context.Context) bool {
	b.mu.Lock()
	b.events = append(b.events, "stop")
	b.mu.Unlock()
	return true
}

func (b *handoverBox) playKey(context.Context) bool {
	b.mu.Lock()
	b.events = append(b.events, "play")
	b.mu.Unlock()
	return true
}

func (b *handoverBox) seen() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.events...)
}

// fastHandoverTiming shrinks the verify window so a test does not wait real
// seconds. Installed on the server under test, never on a package variable,
// so no cleanup is needed.
func fastHandoverTiming() *nativeHandoverTiming {
	return &nativeHandoverTiming{window: 60 * time.Millisecond, poll: 5 * time.Millisecond}
}

func newHandoverServer(t *testing.T, b *handoverBox) *Server {
	t.Helper()
	srv := httptest.NewServer(b.handler())
	t.Cleanup(srv.Close)
	return &Server{
		nativeHandoverTiming: fastHandoverTiming(),
		logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		queue:                newPlayQueue(),
		renderer:             &upnp.Renderer{ControlURL: srv.URL, Client: srv.Client()},
		boxNowReadFn:         b.read,
		nativeStopFn:         b.stopKey,
		nativeResumeFn:       b.playKey,
	}
}

func joined(ev []string) string { return strings.Join(ev, ",") }

// The common case must not change: a speaker already on STR's own stream gets
// the push and nothing else, no stop key and no second push.
func TestHandoverLeavesOtherSourcesAlone(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	// #1314: the message used to say "press Stop and try again" after STR had
	// pressed Stop twice itself. It now names the refusal and the remedy.
	for _, want := range []string{"refusing", "radio station was put back on", "restart"} {
		if !strings.Contains(resp["detail"], want) {
			t.Errorf("detail = %q, want it to mention %q", resp["detail"], want)
		}
	}
	if strings.Contains(resp["detail"], "Press Stop") {
		t.Errorf("detail = %q still tells the user to press Stop", resp["detail"])
	}
	// Exactly one retry, then the station the user was listening to comes back.
	if got := joined(b.seen()); got != "stop,push,stop,push,play" {
		t.Errorf("speaker saw %q, want stop,push,stop,push,play", got)
	}
}

// A refused handover is the same refusal the recall verify counts, so two of
// them latch the speaker banner, and one alone stays quiet.
func TestRefusedHandoverCountsTowardTheRefusalHint(t *testing.T) {
	t.Parallel()
	b := &handoverBox{source: nativeRadioSource}
	s := newHandoverServer(t, b)
	push := func() error { return s.renderer.PlayURL(t.Context(), "http://stream.example/a", "Station", "") }
	if err := s.pushOverNativeStation(t.Context(), "Station", push); !errors.Is(err, errNativeStationKept) {
		t.Fatalf("err = %v, want errNativeStationKept", err)
	}
	if active, _ := s.RecallRefusal(); active {
		t.Fatal("one refused handover must stay quiet")
	}
	if err := s.pushOverNativeStation(t.Context(), "Station", push); !errors.Is(err, errNativeStationKept) {
		t.Fatalf("err = %v, want errNativeStationKept", err)
	}
	if active, _ := s.RecallRefusal(); !active {
		t.Fatal("two refused handovers must latch the refusal hint")
	}
}

// When no stop key reached the speaker, nothing was interrupted, so there is
// nothing to resume: a PLAY key then could start a station the user had
// stopped themselves.
func TestRefusedHandoverResumesOnlyWhatItStopped(t *testing.T) {
	t.Parallel()
	b := &handoverBox{source: nativeRadioSource}
	s := newHandoverServer(t, b)
	s.nativeStopFn = func(context.Context) bool { return false }
	err := s.pushOverNativeStation(t.Context(), "Station", func() error {
		return s.renderer.PlayURL(t.Context(), "http://stream.example/a", "Station", "")
	})
	if !errors.Is(err, errNativeStationKept) {
		t.Fatalf("err = %v, want errNativeStationKept", err)
	}
	if got := joined(b.seen()); got != "push,push" {
		t.Errorf("speaker saw %q, want push,push and no play key", got)
	}
}

// The queue (a folder started from the app) shares the push, so it shares the
// handover and its failure too.
func TestHandoverCoversTheQueuePush(t *testing.T) {
	t.Parallel()
	b := &handoverBox{source: nativeRadioSource}
	s := newHandoverServer(t, b)
	err := s.pushStream(t.Context(), "http://media.example/a.mp3", "Track", "", "audio/mpeg", "", 0)
	if !errors.Is(err, errNativeStationKept) {
		t.Fatalf("err = %v, want errNativeStationKept", err)
	}
	if s.play.last != nil {
		t.Error("a push the speaker ignored was recorded as the last play")
	}
}

// A push the transport itself refuses keeps its own error, and no retry runs
// on top of it: the handover only adds the source check to a push that
// answered success.
func TestHandoverKeepsThePushError(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	cases := []struct {
		file, fn, want string
	}{
		{"playback.go", "func (s *Server) playSlot", "s.pushOverNativeStation(playCtx, p.Name"},
		{"boxsettings_http.go", "func (s *Server) playTrackWithWrongStateRepair", "s.pushOverNativeStation("},
		{"librarykey.go", "func (s *Server) playLibraryFilePresetLocked", "s.pushOverNativeStation("},
		{"queueplay.go", "func (s *Server) pushStream", "s.pushOverNativeStation("},
		{"../../cmd/agent/agent.go", "spotifyMgr.SetOnActivate(", "webuiSrv.PushOverNativeStation("},
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
