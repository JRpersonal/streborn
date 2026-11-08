package webui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// #1765: a folder on a key, shuffled and on repeat, played one song after the
// speaker was switched off and on again, then stopped. The standby ended the
// queue and the power-on resume pushed only the play on record.

func resumeTracks(n int) []queueItem {
	out := make([]queueItem, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, queueItem{URL: fmt.Sprintf("http://192.0.2.10:9000/music/%d.mp3", i),
			Title: fmt.Sprintf("Song %d", i), Mime: "audio/mpeg", Duration: 3 * time.Minute})
	}
	return out
}

// folderResumeServer is a play-test server whose speaker plays whatever it is
// handed, so a queue the resume starts keeps its watcher quiet.
func folderResumeServer(t *testing.T) (*Server, *soapRecorder) {
	t.Helper()
	s, rec := newPlayTestServer(t)
	s.queueInZoneFn = func() bool { return false }
	s.nowPlayingFn = func() (string, time.Duration, time.Duration, bool) {
		return "PLAY_STATE", time.Second, 3 * time.Minute, false
	}
	return s, rec
}

// playingFolder loads a shuffled, repeating folder, moves it a few tracks on
// and records its current track as the play on record, the way pushStream
// leaves it. card names the folder.
func playingFolder(s *Server, items []queueItem, card recentCardCtx) playQueueState {
	s.queue.load(items, -1, true, repeatAll)
	for i := 0; i < 3; i++ {
		s.queue.next()
	}
	s.queueMu.Lock()
	s.queueFolder = card
	s.queueMu.Unlock()
	cur, _ := s.queue.current()
	s.setLastPlay(queueBoxURL(cur.URL, cur.Mime), cur.Title, cur.Art, cur.Mime)
	return s.queue.save()
}

func resumeLocked(t *testing.T, s *Server) bool {
	t.Helper()
	lp, ok := s.play.snapshot()
	if !ok {
		t.Fatal("no play on record")
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	folder, err := s.resumePushLocked(context.Background(), "wake resume", true, lp.boxURL, lp.title, lp.art, lp.mime, lp.ts)
	if err != nil {
		t.Fatalf("resume push: %v", err)
	}
	return folder
}

func TestPowerOnResumeContinuesTheFolderWithItsOrderAndFlags(t *testing.T) {
	s, rec := folderResumeServer(t)
	before := playingFolder(s, resumeTracks(40), recentCardCtx{key: "queue:srv:1", name: "All music"})
	s.stopQueueForStandby("the box went into standby")
	if s.queue.isActive() {
		t.Fatal("the standby did not end the queue")
	}

	if !resumeLocked(t, s) {
		t.Fatal("the power-on resume played the single track, not the folder")
	}
	after := s.queue.save()
	if !after.active {
		t.Fatal("the folder is not running after the resume")
	}
	if !slices.Equal(after.order, before.order) || after.pos != before.pos {
		t.Fatalf("the folder came back at pos %d of another order, want pos %d of the same shuffled order", after.pos, before.pos)
	}
	if !after.shuffle || after.repeat != repeatAll {
		t.Fatalf("shuffle=%v repeat=%s, want shuffle on and repeat all", after.shuffle, after.repeat)
	}
	if !watcherSlotTaken(s) {
		t.Fatal("the folder runs without a watcher, it would stop after this track")
	}
	if !rec.has("SetAVTransportURI") {
		t.Fatal("the speaker was handed nothing")
	}
	// The folder is started again once: a second resume has nothing to take.
	if qr := s.takeQueueResume(before.items[before.order[before.pos]].URL, time.Now()); qr != nil {
		t.Fatal("the kept folder was handed out twice")
	}
}

func TestPowerOnResumePlaysTheTrackAloneWhenSomethingElseWasPlayedSince(t *testing.T) {
	s, rec := folderResumeServer(t)
	playingFolder(s, resumeTracks(10), recentCardCtx{key: "queue:srv:1"})
	s.stopQueueForStandby("the box went into standby")
	// A newer play on record, without going through any queue stop.
	s.setLastPlay("http://stream.example/radio", "Radio", "", "")

	if resumeLocked(t, s) {
		t.Fatal("the folder came back over a newer play")
	}
	if s.queue.isActive() {
		t.Fatal("a queue runs after a single-stream resume")
	}
	if !rec.has("SetAVTransportURI") {
		t.Fatal("the single stream was not pushed")
	}
}

func TestPowerOnResumePlaysTheTrackAloneWhenTheFolderIsTooOld(t *testing.T) {
	s, _ := folderResumeServer(t)
	playingFolder(s, resumeTracks(10), recentCardCtx{key: "queue:srv:1"})
	s.stopQueueForStandby("the box went into standby")
	s.queueResumeMu.Lock()
	s.queueResume.at = time.Now().Add(-resumeMaxAge - time.Minute)
	s.queueResumeMu.Unlock()

	if resumeLocked(t, s) {
		t.Fatal("a folder older than the resume age came back")
	}
	if s.queue.isActive() {
		t.Fatal("a queue runs after a single-stream resume")
	}
}

func TestAnyOtherWayOffTheFolderForgetsIt(t *testing.T) {
	for _, stop := range []func(s *Server){
		func(s *Server) { s.stopQueue("a single station or track was played instead") },
		func(s *Server) { s.stopQueueIfActive("the speaker was switched to AUX") },
		func(s *Server) { s.StopQueueForSpotify() },
	} {
		s, _ := folderResumeServer(t)
		playingFolder(s, resumeTracks(5), recentCardCtx{key: "queue:srv:1"})
		s.stopQueueForStandby("the box went into standby")
		stop(s)
		if resumeLocked(t, s) {
			t.Fatal("the folder came back after the user moved on")
		}
	}
}

// A second standby report for a queue that already ended keeps the folder.
func TestASecondStandbyReportKeepsTheFolder(t *testing.T) {
	s, _ := folderResumeServer(t)
	playingFolder(s, resumeTracks(5), recentCardCtx{key: "queue:srv:1"})
	s.stopQueueForStandby("the box went into standby")
	s.stopQueueForStandby("the box was powered off")
	if !resumeLocked(t, s) {
		t.Fatal("the second standby report dropped the kept folder")
	}
}

// A folder on a key survives an agent restart: the small NAND file names the
// key, the position and the order, and the tracks come from the key.
func TestAKeysFolderComesBackAfterAnAgentRestart(t *testing.T) {
	s, _ := folderResumeServer(t)
	s.lastPlayPath = filepath.Join(t.TempDir(), "last-play.json")
	items := resumeTracks(30)
	p := seedFolderKey(t, s, 2, "All music", items)
	before := playingFolder(s, items, slotQueueCard(2, p))
	s.stopQueueForStandby("the box went into standby")
	if _, err := os.Stat(s.queueResumePath()); err != nil {
		t.Fatalf("the key's folder was not kept on NAND: %v", err)
	}

	s2, _ := folderResumeServer(t)
	s2.presets = s.presets
	s2.lastPlayPath = s.lastPlayPath
	s2.loadLastPlay()
	s2.loadQueueResume()
	if !resumeLocked(t, s2) {
		t.Fatal("the folder did not come back after the restart")
	}
	after := s2.queue.save()
	if !slices.Equal(after.order, before.order) || after.pos != before.pos || !after.shuffle || after.repeat != repeatAll {
		t.Fatal("the folder came back in another order or with other flags after the restart")
	}
	if _, err := os.Stat(s.queueResumePath()); !os.IsNotExist(err) {
		t.Fatal("the NAND copy is still there after the folder was started again")
	}
}

// A folder on no key is kept in memory only: nothing is written to NAND.
func TestALibraryFolderIsNotWrittenToNAND(t *testing.T) {
	s, _ := folderResumeServer(t)
	s.lastPlayPath = filepath.Join(t.TempDir(), "last-play.json")
	playingFolder(s, resumeTracks(5), recentCardCtx{key: "queue:srv:1"})
	s.stopQueueForStandby("the box went into standby")
	if _, err := os.Stat(s.queueResumePath()); !os.IsNotExist(err) {
		t.Fatal("a folder on no key was written to NAND")
	}
}

// After a restart a key that was given another folder plays the track alone.
func TestARestoredFolderWhoseKeyChangedPlaysTheTrackAlone(t *testing.T) {
	s, _ := folderResumeServer(t)
	s.lastPlayPath = filepath.Join(t.TempDir(), "last-play.json")
	items := resumeTracks(10)
	p := seedFolderKey(t, s, 2, "All music", items)
	playingFolder(s, items, slotQueueCard(2, p))
	s.stopQueueForStandby("the box went into standby")
	seedFolderKey(t, s, 2, "Other", resumeTracks(4))

	s2, _ := folderResumeServer(t)
	s2.presets = s.presets
	s2.lastPlayPath = s.lastPlayPath
	s2.loadLastPlay()
	s2.loadQueueResume()
	if resumeLocked(t, s2) {
		t.Fatal("the old folder came back from a key that holds another one")
	}
}
