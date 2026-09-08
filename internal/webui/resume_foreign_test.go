package webui

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The predicate the guard rests on. The false cases are the ones that must
// stand the resume down; every true case is a shape STR itself produces and
// would break if it were called foreign.
func TestLocationServedBySTR(t *testing.T) {
	const ownStream = "http://127.0.0.1:8888/stream/2"
	cases := []struct {
		name, loc, own string
		want           bool
	}{
		{"this agent's own proxy", "http://127.0.0.1:8888/stream/2", ownStream, true},
		{"the redirect port on the box's LAN address", "http://192.0.2.7:17008/stream/4", ownStream, true},
		{"a group follower on the master's mirror proxy", "http://192.0.2.10:17008/stream/1", ownStream, true},
		{"the Spotify Ogg stream", "http://127.0.0.1:8888/spotify/stream-3.ogg", ownStream, true},
		{"a native station descriptor", "/station?data=eyJuYW1lIjoiV0RSIn0=", ownStream, true},
		{"no content item at all", "", ownStream, true},
		{"something that is not a URL", "AUX", ownStream, true},
		{"a media library track STR pushed from the NAS",
			"http://192.0.2.28:9000/disk/track-42.flac", "http://192.0.2.28:9000/disk/track-7.flac", true},
		{"a stream another controller pushed", "http://192.0.2.50:8095/stream/flow.mp3", ownStream, false},
		{"a foreign server while STR's target is its own proxy",
			"http://192.0.2.99:1400/audio.mp3", ownStream, false},
	}
	for _, c := range cases {
		if got := locationServedBySTR(c.loc, c.own); got != c.want {
			t.Errorf("%s: locationServedBySTR(%q) = %v, want %v", c.name, c.loc, got, c.want)
		}
	}
}

// The reader must see every ContentItem in the body, not just the first: the
// teardown restore and the item that was playing can both be present.
func TestContentLocationsReadsEveryItem(t *testing.T) {
	body := `<nowPlaying source="INVALID_SOURCE">` +
		`<ContentItem source="UPNP" location="http://192.0.2.50:8095/stream/flow.mp3"/>` +
		`<previous location="http://127.0.0.1:8888/stream/2"/></nowPlaying>`
	got := contentLocations(body)
	if len(got) != 2 || got[0] != "http://192.0.2.50:8095/stream/flow.mp3" {
		t.Fatalf("contentLocations = %v, want both items in document order", got)
	}
}

// The freshness rule, per reading. The teardown is over in a second or two, so
// the reading taken at the wake INSTANT is the only one that can catch it: a
// live foreign stream there stands the resume down even though the box is idle
// by the time the settle is over.
func TestForeignContentStandDownReadsEachReadingOnItsOwn(t *testing.T) {
	const ownStream = "http://127.0.0.1:8888/stream/2"
	const foreign = `<ContentItem source="UPNP" location="http://192.0.2.50:8095/stream/flow.mp3"/>`
	cases := []struct {
		name, prior, now string
		want             bool
	}{
		{"live at the wake instant, gone by the settle",
			`<nowPlaying source="UPNP">` + foreign + `<playStatus>PLAY_STATE</playStatus></nowPlaying>`,
			`<nowPlaying source="INVALID_SOURCE"/>`, true},
		{"nothing at the wake instant, live by the settle",
			`<nowPlaying source="INVALID_SOURCE"/>`,
			`<nowPlaying source="UPNP">` + foreign + `<playStatus>BUFFERING_STATE</playStatus></nowPlaying>`, true},
		{"a restored selection in both, nothing playing",
			`<nowPlaying source="INVALID_SOURCE">` + foreign + `</nowPlaying>`,
			`<nowPlaying source="INVALID_SOURCE">` + foreign + `</nowPlaying>`, false},
		{"playing, but it is STR's own stream",
			`<nowPlaying source="UPNP"><ContentItem source="UPNP" location="` + ownStream + `"/>` +
				`<playStatus>PLAY_STATE</playStatus></nowPlaying>`,
			`<nowPlaying source="UPNP"/>`, false},
	}
	for _, c := range cases {
		s, _ := newPlayTestServer(t)
		s.nowPlayingBodyFn = func() string { return c.now }
		if got := s.foreignContentStandDown(parseBoxReading(c.prior), ownStream); got != c.want {
			t.Errorf("%s: stand down = %v, want %v", c.name, got, c.want)
		}
	}
}

// resumeTestServer builds a Server that can run ResumeLastPlay end to end
// without touching a box: no boxHost (so the wake and the zone probe are
// no-ops), a seamed play state and a seamed now_playing body.
func resumeTestServer(t *testing.T, nowPlaying string) (*Server, *soapRecorder) {
	t.Helper()
	s, rec := newPlayTestServer(t)
	s.resumeOnPowerOnPath = t.TempDir() + "/resume-on-power-on" // absent = default on
	s.playStateFn = func() (standby, busy bool) { return false, false }
	s.nowPlayingBodyFn = func() string { return nowPlaying }
	s.lastPlayMu.Lock()
	s.lastPlay = &lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/2", title: "Last Station", ts: time.Now()}
	s.lastPlayMu.Unlock()
	return s, rec
}

// waitForSOAP waits for the resume goroutine (2 s settle) to either push or
// give up.
func waitForSOAP(rec *soapRecorder, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if rec.count() > 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// #827: the speaker is carrying a Music Assistant stream when the teardown's
// DO_NOT_RESUME restore arrives. Pushing STR's last station over it is the
// audible "the speaker wants to switch presets".
func TestResumeLastPlayDeclinesOnForeignContent(t *testing.T) {
	s, rec := resumeTestServer(t, `<nowPlaying source="INVALID_SOURCE">`+
		`<ContentItem source="UPNP" location="http://192.0.2.50:8095/stream/flow.mp3"/>`+
		`<playStatus>PLAY_STATE</playStatus></nowPlaying>`)
	s.ResumeLastPlay()
	if waitForSOAP(rec, 4*time.Second) {
		t.Fatalf("STR pushed its own station onto a speaker streaming from elsewhere: %v", rec.list())
	}
}

// A paused foreign stream is still somebody's speaker: pushing a station over it
// is the same interruption, so PAUSE counts as live.
func TestResumeLastPlayDeclinesOnPausedForeignContent(t *testing.T) {
	s, rec := resumeTestServer(t, `<nowPlaying source="UPNP">`+
		`<ContentItem source="UPNP" location="http://192.0.2.50:8095/stream/flow.mp3"/>`+
		`<playStatus>PAUSE_STATE</playStatus></nowPlaying>`)
	s.ResumeLastPlay()
	if waitForSOAP(rec, 4*time.Second) {
		t.Fatalf("STR pushed its own station over a paused foreign stream: %v", rec.list())
	}
}

// The morning after. The firmware restores the last selection when it comes out
// of standby, so a user whose evening ended on Music Assistant is still carrying
// that foreign item when they switch the speaker on - with nothing playing.
// Standing down on it would turn the power-on resume off for that user for good.
func TestResumeLastPlayProceedsOnAStaleForeignSelection(t *testing.T) {
	s, rec := resumeTestServer(t, `<nowPlaying source="INVALID_SOURCE">`+
		`<ContentItem source="UPNP" location="http://192.0.2.50:8095/stream/flow.mp3"/>`+
		`<playStatus>STOP_STATE</playStatus></nowPlaying>`)
	s.ResumeLastPlay()
	if !waitForSOAP(rec, 6*time.Second) {
		t.Fatalf("a restored foreign selection with nothing playing killed the power-on resume: %v", rec.list())
	}
}

// The same shape as the box reports it after a night in standby: the restored
// selection, and no play status at all.
func TestResumeLastPlayProceedsOnAForeignSelectionWithNoPlayState(t *testing.T) {
	s, rec := resumeTestServer(t, `<nowPlaying source="INVALID_SOURCE">`+
		`<ContentItem source="UPNP" location="http://192.0.2.50:8095/stream/flow.mp3"/></nowPlaying>`)
	s.ResumeLastPlay()
	if !waitForSOAP(rec, 6*time.Second) {
		t.Fatalf("a foreign selection with no play state killed the power-on resume: %v", rec.list())
	}
}

// The other half: a genuine power-on, where the box carries STR's own selection
// (or none at all), still brings the last station back.
func TestResumeLastPlayProceedsOnItsOwnContent(t *testing.T) {
	s, rec := resumeTestServer(t, `<nowPlaying source="INVALID_SOURCE">`+
		`<ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/2"/></nowPlaying>`)
	s.ResumeLastPlay()
	if !waitForSOAP(rec, 6*time.Second) {
		t.Fatalf("the power-on resume did not push the last station: %v", rec.list())
	}
	if !rec.has("SetAVTransportURI") {
		t.Fatalf("want a SetAVTransportURI, got %v", rec.list())
	}
}

// The stand-down names the other controller by HOST, never by URL. A stream a
// foreign controller pushed routinely carries a session token in its path or
// query, and this line goes into a diagnostic bundle users mail around (the
// redaction rule in CLAUDE.md).
func TestForeignStandDownLogsTheHostAndNotTheURL(t *testing.T) {
	var buf bytes.Buffer
	s, _ := newPlayTestServer(t)
	s.logger = slog.New(slog.NewTextHandler(&buf, nil))
	s.nowPlayingBodyFn = func() string {
		return `<nowPlaying source="UPNP"><ContentItem source="UPNP" ` +
			`location="http://192.0.2.50:8095/stream/flow.mp3?token=s3cr3tsession"/>` +
			`<playStatus>PLAY_STATE</playStatus></nowPlaying>`
	}
	if !s.foreignContentStandDown(boxReading{}, "http://127.0.0.1:8888/stream/2") {
		t.Fatal("a live foreign stream must stand the resume down")
	}
	got := buf.String()
	if !strings.Contains(got, "192.0.2.50:8095") {
		t.Fatalf("the log must name the other controller so a bundle is diagnosable:\n%s", got)
	}
	if strings.Contains(got, "s3cr3tsession") || strings.Contains(got, "flow.mp3") {
		t.Fatalf("the log leaked the media URL, not just its host:\n%s", got)
	}
}

// The guard judges CONTENT, not playback. A speaker playing one of its own
// sources (AUX, Bluetooth, the Wave's tuner) reports a play status and no
// content location at all, and that is not evidence of a foreign controller:
// standing down here would make this a second, weaker copy of the busy check
// that already owns that case further down ResumeLastPlay.
func TestForeignGuardIgnoresALivePlayStatusWithNoContentAtAll(t *testing.T) {
	s, _ := newPlayTestServer(t)
	s.nowPlayingBodyFn = func() string {
		return `<nowPlaying source="AUX"><playStatus>PLAY_STATE</playStatus></nowPlaying>`
	}
	if s.foreignContentStandDown(boxReading{}, "http://127.0.0.1:8888/stream/2") {
		t.Fatal("a play status with no content location must not read as foreign content")
	}
}

// An unreadable box must not disable the resume: the guard only ever acts on
// evidence it actually has.
func TestResumeLastPlayProceedsWhenTheBoxSaysNothing(t *testing.T) {
	s, rec := resumeTestServer(t, "")
	s.ResumeLastPlay()
	if !waitForSOAP(rec, 6*time.Second) {
		t.Fatalf("an empty now_playing must not stand the resume down: %v", rec.list())
	}
}
