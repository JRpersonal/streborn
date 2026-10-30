package webui

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

const pandoraLoc = "126740707481236361"

func pandoraBody(status string) string {
	return `<nowPlaying deviceID="device-id-here" source="PANDORA" sourceAccount="user@example.com">` +
		`<ContentItem source="PANDORA" type="tracklisturl" location="` + pandoraLoc + `" sourceAccount="user@example.com" isPresetable="true">` +
		`<itemName>Station</itemName></ContentItem><playStatus>` + status + `</playStatus></nowPlaying>`
}

// servedStatus serves body as the cached now_playing and returns what
// /api/status hands the apps.
func servedStatus(s *Server, body string) string {
	s.statusMu.Lock()
	s.statusBody, s.statusCode, s.statusAt = []byte(body), http.StatusOK, time.Now()
	s.statusMu.Unlock()
	return pollStatus(s).Body.String()
}

// #1629: the quiet wake of a group operation stops what the firmware resumed.
// A Pandora station turns that STOP into PAUSE_STATE, and both apps then lit
// its key as paused for the 18 minutes until the speaker's idle auto-off. The
// watch now marks that pause, and /api/status reports the speaker stopped
// until it plays again.
func TestQuietWakePandoraPauseIsReportedStopped(t *testing.T) {
	var logBuf bytes.Buffer
	s := &Server{logger: slog.New(slog.NewTextHandler(&logBuf, nil)), boxHost: "192.0.2.10"}
	stops := 0
	s.quietWakeNowPlayingFn = func(context.Context, string) nowPlayingSnapshot {
		st := "PLAY_STATE"
		if stops > 0 {
			st = "PAUSE_STATE"
		}
		return nowPlayingSnapshot{Source: "PANDORA", Location: pandoraLoc, PlayStatus: st}
	}
	s.quietWakeStopFn = func(context.Context, string) error { stops++; return nil }
	s.stopQuietWakeResume()

	if !s.wokenStop.active() {
		t.Fatal("a quiet wake that ended on PAUSE_STATE set no woken-stop mark")
	}
	if !strings.Contains(logBuf.String(), "quiet wake resume watch ended") || !strings.Contains(logBuf.String(), "playStatus=PAUSE_STATE") {
		t.Fatalf("the watch's final state was not logged:\n%s", logBuf.String())
	}
	got := servedStatus(s, pandoraBody("PAUSE_STATE"))
	if !strings.Contains(got, "<playStatus>STOP_STATE</playStatus>") || strings.Contains(got, "PAUSE_STATE") {
		t.Fatalf("woken pause served as %q, want STOP_STATE", got)
	}
	// Served again on the next poll: the mark is not used up by one read.
	if got := servedStatus(s, pandoraBody("PAUSE_STATE")); !strings.Contains(got, "STOP_STATE") {
		t.Fatalf("second poll served %q, want STOP_STATE", got)
	}

	// The user plays the station: the mark is gone, and a pause the user
	// makes from there keeps its paused label.
	if got := servedStatus(s, pandoraBody("PLAY_STATE")); !strings.Contains(got, "PLAY_STATE") {
		t.Fatalf("playing body served as %q", got)
	}
	if s.wokenStop.active() {
		t.Fatal("the mark survived the speaker playing again")
	}
	if got := servedStatus(s, pandoraBody("PAUSE_STATE")); !strings.Contains(got, "PAUSE_STATE") {
		t.Fatalf("a pause the user made was served as %q, want PAUSE_STATE", got)
	}
}

func TestWokenStopClearsOnAnotherSourceOrStation(t *testing.T) {
	for name, body := range map[string]string{
		"standby":       `<nowPlaying deviceID="device-id-here" source="STANDBY"><ContentItem source="STANDBY" isPresetable="false" /></nowPlaying>`,
		"other station": strings.Replace(pandoraBody("PAUSE_STATE"), pandoraLoc, "999", 1),
		"buffering":     pandoraBody("BUFFERING_STATE"),
	} {
		var w wokenStop
		w.mark("PANDORA", pandoraLoc)
		out := w.apply([]byte(body))
		if string(out) != body {
			t.Errorf("%s: body rewritten to %q", name, out)
		}
		if w.active() {
			t.Errorf("%s: mark survived", name)
		}
	}
	// An error reply says nothing about the speaker: the mark stays.
	var w wokenStop
	w.mark("PANDORA", pandoraLoc)
	w.apply([]byte(`<errors><error name="HTTP_STATUS_NOT_FOUND" /></errors>`))
	if !w.active() {
		t.Error("an error reply cleared the mark")
	}
}

func TestQuietWakeStoppedSpeakerSetsNoMark(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10"}
	stops := 0
	s.quietWakeNowPlayingFn = func(context.Context, string) nowPlayingSnapshot {
		st := "PLAY_STATE"
		if stops > 0 {
			st = "STOP_STATE"
		}
		return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", Location: "http://192.0.2.1/s", PlayStatus: st}
	}
	s.quietWakeStopFn = func(context.Context, string) error { stops++; return nil }
	s.stopQuietWakeResume()
	if s.wokenStop.active() {
		t.Fatal("a speaker the quiet wake stopped got a woken-stop mark")
	}
	// Without a mark, a paused speaker is served as paused.
	if got := servedStatus(s, pandoraBody("PAUSE_STATE")); !strings.Contains(got, "PAUSE_STATE") {
		t.Fatalf("unmarked pause served as %q", got)
	}
}
