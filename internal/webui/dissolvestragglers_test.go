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
	prevLoc := playingStateFn
	prevStop := stopKeyFn
	playingStateFn = func(_ context.Context, host string) playingState {
		f := byHost[host]
		if f == nil {
			return playingState{}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.status != "PLAY_STATE" && f.status != "BUFFERING_STATE" {
			return playingState{}
		}
		return playingState{Source: f.source, Location: f.location}
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
	t.Cleanup(func() { playingStateFn, stopKeyFn = prevLoc, prevStop })
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
	s.stopStragglers(context.Background(), groupURL, "",
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
	s.stopStragglers(context.Background(), "http://192.0.2.10:8888/stream/2", "",
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
	s.stopStragglers(context.Background(), "http://192.0.2.10:8888/stream/2", "",
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
	s.stopStragglers(context.Background(), "", "", []boxapi.ZoneMember{{DeviceID: "DEV-A", IP: "192.0.2.54"}})

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
	s.stopStragglers(context.Background(), "http://192.0.2.10:8888/stream/2", "",
		[]boxapi.ZoneMember{{DeviceID: "SELF", IP: "192.0.2.10"}})

	if got := self.stops(); got != 0 {
		t.Errorf("the master was stopped %d time(s)", got)
	}
}

// A MIRROR follower never reports the master's own loopback location: the master
// plays 127.0.0.1:8888/stream/N while the slave was pointed at
// masterIP:17008/stream/N. The old exact-string compare left it playing after a
// "dissolve" (the group kept going). It must be stopped when its host:port
// matches the master's mirror proxy, even though the full URL differs.
func TestMirrorFollowerOnMasterProxyIsStopped(t *testing.T) {
	const masterLoc = "http://127.0.0.1:8888/stream/2"     // what the master reports
	const mirrorHostPort = "192.0.2.10:17008"              // master's mirror proxy
	const followerLoc = "http://192.0.2.10:17008/stream/2" // what the slave pulls
	follower := newFakeSpeaker("UPNP", "PLAY_STATE", followerLoc)
	defer follower.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.54": follower})

	s := newDissolveServer()
	s.stopStragglers(context.Background(), masterLoc, mirrorHostPort,
		[]boxapi.ZoneMember{{DeviceID: "DEV-A", IP: "192.0.2.54"}})

	if got := follower.stops(); got != 1 {
		t.Errorf("mirror follower stop keys = %d, want 1 (it kept playing the group stream)", got)
	}
}

// A speaker on a DIFFERENT proxy host:port than the master's mirror proxy moved
// on to its own thing and must be left alone even with a mirror reference set.
func TestMirrorSweepLeavesAForeignProxyAlone(t *testing.T) {
	const masterLoc = "http://127.0.0.1:8888/stream/2"
	const mirrorHostPort = "192.0.2.10:17008"
	other := newFakeSpeaker("UPNP", "PLAY_STATE", "http://192.0.2.99:17008/stream/7")
	defer other.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.54": other})

	s := newDissolveServer()
	s.stopStragglers(context.Background(), masterLoc, mirrorHostPort,
		[]boxapi.ZoneMember{{DeviceID: "DEV-A", IP: "192.0.2.54"}})

	if got := other.stops(); got != 0 {
		t.Errorf("a speaker on a foreign proxy was stopped %d time(s)", got)
	}
}

// The 2026-09-07 bundle: the master plays a music library and has moved on to
// the next track since the follower dropped out. The follower still plays the
// earlier track from the SAME server, so it is on the group's programme and
// must be stopped although the locations differ.
func TestAFollowerOnTheSameMediaServerIsStopped(t *testing.T) {
	const server = "http://192.0.2.17:10243/WMPNSSv4/3763749585/"
	follower := newFakeSpeaker("STORED_MUSIC", "PLAY_STATE", server+"1_track-before.mp3")
	defer follower.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.54": follower})

	s := newDissolveServer()
	s.stopStragglers(context.Background(), server+"1_track-now.mp3", "",
		[]boxapi.ZoneMember{{DeviceID: "DEV-A", IP: "192.0.2.54"}})

	if got := follower.stops(); got != 1 {
		t.Errorf("stop keys sent = %d, want 1", got)
	}
}

// A member on its OWN local proxy is not on the master's programme just
// because the master's location is a loopback proxy URL as well: the
// same-server rule never applies to loopback hosts.
func TestAMemberOnItsOwnLoopbackProxyIsLeftAlone(t *testing.T) {
	own := newFakeSpeaker("UPNP", "PLAY_STATE", "http://127.0.0.1:8888/stream/5")
	defer own.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.54": own})

	s := newDissolveServer()
	s.stopStragglers(context.Background(), "http://127.0.0.1:8888/stream/2", "",
		[]boxapi.ZoneMember{{DeviceID: "DEV-A", IP: "192.0.2.54"}})

	if got := own.stops(); got != 0 {
		t.Errorf("a member on its own proxy was stopped %d time(s)", got)
	}
}

// A former member that still reports the GROUP_SLAVE source carries the
// group's audio by its own account, whatever its location says.
func TestAMemberStillReportingGroupSlaveIsStopped(t *testing.T) {
	slave := newFakeSpeaker("GROUP_SLAVE", "PLAY_STATE", "")
	defer slave.srv.Close()
	withFakeFleet(t, map[string]*fakeSpeaker{"192.0.2.54": slave})

	s := newDissolveServer()
	s.stopStragglers(context.Background(), "http://192.0.2.17:10243/track.mp3", "",
		[]boxapi.ZoneMember{{DeviceID: "DEV-A", IP: "192.0.2.54"}})

	if got := slave.stops(); got != 1 {
		t.Errorf("stop keys sent = %d, want 1", got)
	}
}

func TestGroupServerHostPort(t *testing.T) {
	cases := map[string]string{
		"":                                 "",
		"http://127.0.0.1:8888/stream/2":   "",
		"http://localhost:8888/stream/2":   "",
		"http://192.0.2.17:10243/a/b.mp3":  "192.0.2.17:10243",
		"http://[::1]:8888/stream/2":       "",
		"http://192.0.2.10:17008/stream/1": "192.0.2.10:17008",
		"bt://phone":                       "phone",
	}
	for in, want := range cases {
		if got := groupServerHostPort(in); got != want {
			t.Errorf("groupServerHostPort(%q) = %q, want %q", in, got, want)
		}
	}
}
