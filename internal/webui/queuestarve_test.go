package webui

import (
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// The shapes a real speaker produces when a library file starves. Measured in a
// field bundle 2026-09-28: the firmware answers a stream that delivers nothing
// by tearing the source down, and such a document carries no <playStatus> at
// all. That is why the queue watcher saw nothing and sat out the rest of the
// track's nominal length in silence, then logged the dead track as a natural
// end. Eleven minutes of it inside one 33-minute queue, and with repeat=all
// there was no exit.
func TestATornDownSourceIsRecognised(t *testing.T) {
	tornDown := []string{
		`<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="DEV#1" source="INVALID_SOURCE"><ContentItem source="INVALID_SOURCE" isPresetable="false" /></nowPlaying>`,
		`<nowPlaying source="INVALID_SOURCE" />`,
	}
	playing := []string{
		`<nowPlaying deviceID="DEV#1" source="UPNP"><playStatus>PLAY_STATE</playStatus></nowPlaying>`,
		`<nowPlaying source="LOCAL_INTERNET_RADIO"><playStatus>BUFFERING_STATE</playStatus></nowPlaying>`,
		// Standby is its own case with its own handling; it must NOT read as a
		// teardown, or a powered-off box would be treated as a dead track and
		// the queue would skip through it instead of stopping.
		`<nowPlaying deviceID="DEV#1" source="STANDBY"><ContentItem source="STANDBY" isPresetable="false" /></nowPlaying>`,
		``,
	}
	for _, x := range tornDown {
		if !boxapi.ParseNowPlaying([]byte(x)).SourceTornDown() {
			t.Errorf("a torn-down source was not recognised, so the queue would wait out the whole track: %s", x)
		}
	}
	for _, x := range playing {
		if boxapi.ParseNowPlaying([]byte(x)).SourceTornDown() {
			t.Errorf("read as torn down, so a live track would be skipped: %s", x)
		}
	}
}

// Standby and a teardown must stay distinguishable: they look similar in the
// document and demand opposite handling. Standby stops the queue, because
// advancing would wake the box back up; a teardown skips, because the next
// track may well play.
func TestStandbyAndTeardownDoNotOverlap(t *testing.T) {
	standby := `<nowPlaying deviceID="DEV#1" source="STANDBY"><ContentItem source="STANDBY" /></nowPlaying>`
	torn := `<nowPlaying deviceID="DEV#1" source="INVALID_SOURCE"><ContentItem source="INVALID_SOURCE" /></nowPlaying>`

	if !boxapi.ParseNowPlaying([]byte(standby)).IsStandby() || boxapi.ParseNowPlaying([]byte(standby)).SourceTornDown() {
		t.Error("a standby document must read as standby and NOT as a teardown")
	}
	if boxapi.ParseNowPlaying([]byte(torn)).IsStandby() || !boxapi.ParseNowPlaying([]byte(torn)).SourceTornDown() {
		t.Error("a teardown document must read as a teardown and NOT as standby")
	}
}

// One dead track is a bad file worth skipping. Several in a row is the server
// having gone away, and racing through the remaining fifty tracks to prove it
// helps nobody.
func TestTheDeadTrackLimitIsSaneAndBoundsTheRun(t *testing.T) {
	if queueDeadTrackLimit < 2 {
		t.Errorf("limit %d stops the queue on a single bad file", queueDeadTrackLimit)
	}
	if queueDeadTrackLimit > 6 {
		t.Errorf("limit %d races through most of a queue before giving up", queueDeadTrackLimit)
	}
}

// The stop reason has to say what happened in words the owner can act on: the
// server stopped delivering, not an internal state name.
func TestTheStopReasonNamesTheServerNotTheState(t *testing.T) {
	const reason = "the music server stopped delivering; several tracks in a row did not play"
	for _, jargon := range []string{"INVALID_SOURCE", "tornDown", "sawPlay", "queueGen"} {
		if strings.Contains(reason, jargon) {
			t.Errorf("the stop reason leaks the internal name %q", jargon)
		}
	}
	if !strings.Contains(reason, "server") {
		t.Error("the stop reason does not name the server, which is the thing the owner can check")
	}
}

// A track that never starts counts towards the same limit as a dropped one.
// The #1322 Wave never took the stream at all, and the queue walked the whole
// folder in silence, one never-started track after the other.
func TestNeverStartedTracksStopTheQueueAfterTheLimit(t *testing.T) {
	dead := 0
	for i := 1; i < queueDeadTrackLimit; i++ {
		if queueDeadTrackStops(&dead) {
			t.Fatalf("the queue stopped after %d dead tracks, the limit is %d", i, queueDeadTrackLimit)
		}
	}
	if !queueDeadTrackStops(&dead) {
		t.Fatalf("the queue kept going after %d dead tracks in a row", queueDeadTrackLimit)
	}
	src := readRepoFile(t, "queueplay.go")
	i := strings.Index(src, `"the track never started playing"`)
	if i < 0 {
		t.Fatal("the never-started advance is gone; if it moved, move this test with it")
	}
	if !strings.Contains(src[max(0, i-1200):i], "queueDeadTrackStops(&deadInARow)") {
		t.Error("a never-started track no longer counts towards the dead-track limit")
	}
}

// #1322: before a never-started track is skipped, the first silence of a run
// gets one more wake and the same track again, and audible progress gives the
// next run its own retry.
func TestANeverStartedQueueTrackIsRetriedOnceBeforeItIsSkipped(t *testing.T) {
	src := readRepoFile(t, "queueplay.go")
	r := strings.Index(src, "!startRetried && s.retryQueueTrackStart(curGen, start)")
	d := strings.Index(src, `"the track never started playing"`)
	if r < 0 || d < 0 || r > d {
		t.Fatal("the never-started branch no longer retries the track before skipping it")
	}
	p := strings.Index(src, "deadInARow = 0\n\t\t\t\tstartRetried = false")
	if p < 0 {
		t.Error("audible progress no longer re-arms the start retry")
	}
}
