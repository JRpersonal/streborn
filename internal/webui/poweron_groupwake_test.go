package webui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// A group key pressed on a sleeping speaker forms the group (a quiet wake that
// opens the group-wake episode) and then asks for the last station through the
// power-on endpoint. The record that endpoint re-pushes is the firmware's own
// power-on resume, marked fromBox. Once STR has pushed it, it is STR's play:
// the late group-wake check must leave it playing (2026-10-27, the station
// played for four seconds and stopped).
func TestPowerOnRepushIsNotTakenForTheMastersOwnResume(t *testing.T) {
	t.Parallel()
	playing := nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE", Location: "http://127.0.0.1:8888/stream/1"}
	s, stops := selfResumeTestServer(t, playing)
	s.noteQuietWakeEpisode()
	time.Sleep(5 * time.Millisecond)
	// The firmware's own power-on resume lands inside the episode.
	gen := s.setLastPlay("http://127.0.0.1:8888/stream/1", "Station", "", "")
	s.play.markFromBoxIfGen(gen)

	// Before the re-push it is the self-resume the guard exists for.
	if s.userPlayedSince(time.Time{}) {
		t.Fatal("a fromBox record must not count as the user's play")
	}

	s.play.noteRepushed("http://127.0.0.1:8888/stream/1")
	if s.stopGroupWakeSelfResume(context.Background(), "after forming") || *stops != 0 {
		t.Fatalf("stops=%d, the power-on re-push was stopped as a self-resume", *stops)
	}
}

// The re-push mark only touches the record that was pushed: a newer play that
// replaced it in between keeps its own state.
func TestNoteRepushedLeavesANewerPlayAlone(t *testing.T) {
	t.Parallel()
	s, _ := selfResumeTestServer(t, nowPlayingSnapshot{})
	gen := s.setLastPlay("http://192.0.2.5/newer", "Newer", "", "")
	s.play.markFromBoxIfGen(gen)
	before, _ := s.play.snapshot()
	s.play.noteRepushed("http://192.0.2.5/older")
	after, _ := s.play.snapshot()
	if !after.fromBox || !after.ts.Equal(before.ts) {
		t.Fatal("noteRepushed changed a record it did not push")
	}
}

// The firmware's own resume inside the episode is still stopped when nothing
// re-pushed it: the fleet-run guard keeps working.
func TestNativeSelfResumeInsideTheEpisodeIsStillStopped(t *testing.T) {
	t.Parallel()
	s, stops := selfResumeTestServer(t, nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "PLAY_STATE"})
	s.noteQuietWakeEpisode()
	time.Sleep(5 * time.Millisecond)
	gen := s.setLastPlay("http://127.0.0.1:8888/stream/1", "Station", "", "")
	s.play.markFromBoxIfGen(gen)
	if !s.stopGroupWakeSelfResume(context.Background(), "after forming") || *stops != 1 {
		t.Fatalf("stops=%d, want the firmware's own resume stopped", *stops)
	}
}

// A push that failed is not STR's play: undo puts the record back, so the
// guard still treats a firmware self-resume of that stream as one.
func TestNoteRepushedUndoRestoresTheRecord(t *testing.T) {
	t.Parallel()
	s, _ := selfResumeTestServer(t, nowPlayingSnapshot{})
	gen := s.setLastPlay("http://192.0.2.5/s", "S", "", "")
	s.play.markFromBoxIfGen(gen)
	before, _ := s.play.snapshot()
	undo := s.play.noteRepushed("http://192.0.2.5/s")
	if mid, _ := s.play.snapshot(); mid.fromBox {
		t.Fatal("the mark did not take")
	}
	undo()
	after, _ := s.play.snapshot()
	if !after.fromBox || !after.ts.Equal(before.ts) {
		t.Fatal("undo did not restore the record")
	}
}

// handleBoxPower must mark its re-push BEFORE it wakes the box and pushes:
// the late group-wake check runs four seconds after the form, in parallel with
// this call, and a wake plus a push on a stalling speaker takes longer than
// that. The handler wakes a real box, so the order is checked in its source.
func TestHandleBoxPowerMarksItsRepushBeforePushing(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("boxsettings_http.go")
	if err != nil {
		t.Fatal(err)
	}
	body := funcBody(t, string(src), "handleBoxPower")
	mark := strings.Index(body, "s.play.noteRepushed(boxURL)")
	wake := strings.Index(body, "s.ensureBoxReady(")
	push := strings.Index(body, "s.renderer.PlayURLMime(")
	if mark < 0 || wake < 0 || push < 0 || mark > wake || mark > push {
		t.Fatal("handleBoxPower must mark its re-push as STR's play before it wakes the box and pushes")
	}
	if !strings.Contains(body, "undo()") {
		t.Fatal("handleBoxPower must undo the mark when the push fails")
	}
}
