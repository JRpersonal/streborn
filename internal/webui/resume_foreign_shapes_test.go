// The two content shapes STR itself produces that do NOT carry an STR address,
// exercised through the whole ResumeLastPlay path rather than through the
// predicate alone: a native station descriptor and a media-library file on the
// user's own server. Both are what the box reports after an ordinary power-on
// for a user whose last play was a hardware preset or an album, so a guard that
// called either of them foreign would silently kill the power-on resume for
// them.

package webui

import (
	"testing"
	"time"
)

// A station STR registered natively is reported back by the speaker as a bare
// "/station?data=..." descriptor: no host, nothing to attribute. It is the
// normal shape after a hardware preset press, so it must resume.
func TestResumeLastPlayProceedsOnANativeStationDescriptor(t *testing.T) {
	s, rec := resumeTestServer(t, `<nowPlaying source="INVALID_SOURCE">`+
		`<ContentItem source="LOCAL_INTERNET_RADIO" type="DO_NOT_RESUME" `+
		`location="/station?data=eyJuYW1lIjoiV0RSIn0="/></nowPlaying>`)
	s.ResumeLastPlay()
	if !waitForSOAP(rec, 6*time.Second) {
		t.Fatalf("a native station selection must not stand the power-on resume down: %v", rec.list())
	}
}

// A library track: STR pushes the user's own media server URL straight at the
// box, so the box carries an address that is neither STR's nor a stranger's.
// The resume target names the same server, which is what tells it apart from a
// stream a foreign controller pushed.
func TestResumeLastPlayProceedsOnALibraryTrackFromTheSameServer(t *testing.T) {
	s, rec := resumeTestServer(t, `<nowPlaying source="INVALID_SOURCE">`+
		`<ContentItem source="UPNP" location="http://192.0.2.28:9000/disk/track-42.flac"/></nowPlaying>`)
	s.lastPlayMu.Lock()
	s.lastPlay = &lastPlayInfo{
		boxURL: "http://192.0.2.28:9000/disk/track-43.flac",
		title:  "Next Track", mime: "audio/flac", ts: time.Now(),
	}
	s.lastPlayMu.Unlock()

	s.ResumeLastPlay()
	if !waitForSOAP(rec, 6*time.Second) {
		t.Fatalf("a library track from the resume target's own server must still resume: %v", rec.list())
	}
}

// The mirror stream a group follower pulls from the master runs on the agent's
// externally reachable port, i.e. another speaker's LAN address. It is STR's
// own audio and must not read as a stranger's.
func TestResumeLastPlayProceedsOnAnotherSpeakersAgentPort(t *testing.T) {
	s, rec := resumeTestServer(t, `<nowPlaying source="INVALID_SOURCE">`+
		`<ContentItem source="UPNP" location="http://192.0.2.7:17008/stream/4"/></nowPlaying>`)
	s.ResumeLastPlay()
	if !waitForSOAP(rec, 6*time.Second) {
		t.Fatalf("an STR agent port on another speaker must not stand the resume down: %v", rec.list())
	}
}
