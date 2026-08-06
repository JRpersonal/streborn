package webui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// fakeSpeaker answers /now_playing with a chosen state and records stop keys.
type fakeSpeaker struct {
	mu       sync.Mutex
	source   string
	status   string
	location string
	keys     []string
	srv      *httptest.Server
}

func newFakeSpeaker(source, status, location string) *fakeSpeaker {
	f := &fakeSpeaker{source: source, status: status, location: location}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/key") {
			b, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.keys = append(f.keys, string(b))
			f.mu.Unlock()
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		_, _ = w.Write([]byte(`<nowPlaying source="` + f.source + `">` +
			`<ContentItem location="` + f.location + `"/>` +
			`<playStatus>` + f.status + `</playStatus></nowPlaying>`))
	}))
	return f
}

func (f *fakeSpeaker) stops() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, k := range f.keys {
		if strings.Contains(k, "STOP") && strings.Contains(k, "press") {
			n++
		}
	}
	return n
}

// The box calls in this file reach the firmware on a fixed port, so the tests
// swap the two helpers the sweep uses. Without that every case would fall
// through the "unreadable, leave it alone" branch and assert nothing.
func withFakeFleet(t *testing.T, byHost map[string]*fakeSpeaker) {
	t.Helper()
	prevLoc := playingLocationFn
	prevStop := stopKeyFn
	playingLocationFn = func(_ context.Context, host string) string {
		f := byHost[host]
		if f == nil {
			return ""
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.status != "PLAY_STATE" && f.status != "BUFFERING_STATE" {
			return ""
		}
		return f.location
	}
	stopKeyFn = func(_ context.Context, host string) error {
		f := byHost[host]
		if f == nil {
			return http.ErrServerClosed
		}
		f.mu.Lock()
		f.keys = append(f.keys, `<key state="press" sender="Gabbo">STOP</key>`)
		f.mu.Unlock()
		return nil
	}
	t.Cleanup(func() { playingLocationFn, stopKeyFn = prevLoc, prevStop })
}

func newDissolveServer() *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10"}
}

// The reported case: a member the master never registered is still carrying the
// group's stream and must be stopped.
func TestStragglerOnTheGroupStreamIsStopped(t *testing.T) {
	const groupURL = "http://192.0.2.10:8888/stream/2"
	straggler := newFakeSpeaker("UPNP", "PLAY_STATE", groupURL)
	defer straggler.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.54": straggler})

	s := newDissolveServer()
	s.stopStragglers(context.Background(), groupURL,
		[]boxapi.ZoneMember{{DeviceID: "DEV-A", IP: "192.0.2.54"}})

	if got := straggler.stops(); got != 1 {
		t.Errorf("stop keys sent = %d, want 1", got)
	}
}

// A speaker that moved on to something of its own must be left alone. Silencing
// it would be worse than the bug being fixed.
func TestAMemberPlayingSomethingElseIsLeftAlone(t *testing.T) {
	other := newFakeSpeaker("BLUETOOTH", "PLAY_STATE", "bt://phone")
	defer other.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.54": other})

	s := newDissolveServer()
	s.stopStragglers(context.Background(), "http://192.0.2.10:8888/stream/2",
		[]boxapi.ZoneMember{{DeviceID: "DEV-A", IP: "192.0.2.54"}})

	if got := other.stops(); got != 0 {
		t.Errorf("a speaker playing its own source was stopped %d time(s)", got)
	}
}

// A member the firmware already tore down is silent and needs nothing.
func TestAnAlreadySilentMemberIsNotTouched(t *testing.T) {
	quiet := newFakeSpeaker("STANDBY", "STOP_STATE", "")
	defer quiet.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.54": quiet})

	s := newDissolveServer()
	s.stopStragglers(context.Background(), "http://192.0.2.10:8888/stream/2",
		[]boxapi.ZoneMember{{DeviceID: "DEV-A", IP: "192.0.2.54"}})

	if got := quiet.stops(); got != 0 {
		t.Errorf("a silent member was sent %d stop key(s)", got)
	}
}

// Without knowing what the master was playing there is nothing to compare
// against, and stopping speakers on a guess is not worth the risk.
func TestNoMasterLocationDisablesTheSweep(t *testing.T) {
	playing := newFakeSpeaker("UPNP", "PLAY_STATE", "http://192.0.2.10:8888/stream/2")
	defer playing.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.54": playing})

	s := newDissolveServer()
	s.stopStragglers(context.Background(), "", []boxapi.ZoneMember{{DeviceID: "DEV-A", IP: "192.0.2.54"}})

	if got := playing.stops(); got != 0 {
		t.Errorf("the sweep ran without a master location and stopped %d speaker(s)", got)
	}
}

// The speaker running the dissolve is the master. It keeps playing.
func TestTheMasterItselfIsNeverStopped(t *testing.T) {
	self := newFakeSpeaker("UPNP", "PLAY_STATE", "http://192.0.2.10:8888/stream/2")
	defer self.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.10": self})

	s := newDissolveServer()
	s.stopStragglers(context.Background(), "http://192.0.2.10:8888/stream/2",
		[]boxapi.ZoneMember{{DeviceID: "SELF", IP: "192.0.2.10"}})

	if got := self.stops(); got != 0 {
		t.Errorf("the master was stopped %d time(s)", got)
	}
}
