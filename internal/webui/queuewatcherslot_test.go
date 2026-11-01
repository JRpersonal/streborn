// A folder that played its first song and then stopped, three times in a row.
//
// Field (SA-5, v1.3.0): every folder started from standby played its first
// track and never the next one. The speaker had slept on one of its own radio
// stations, so the wake resumed that station, and the firmware reports that
// resume as a preset selected on the speaker. STR takes such a frame for a
// hardware key press, which ends any queue. It arrived during the wake, after
// the new queue was loaded and before its first track was handed over: the
// queue was cleared, the first track was pushed anyway, and the watcher started
// for it found no queue and left. Leaving did not free the watcher slot, so
// every later folder (no native resume needed any more) started without a
// watcher and stopped after its first track as well, until the agent restarted.

package webui

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

func waitForQueuePos(s *Server, want int, within time.Duration) int {
	deadline := time.Now().Add(within)
	for s.queue.snapshot().Pos != want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return s.queue.snapshot().Pos
}

func watcherSlotTaken(s *Server) bool {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return s.queueCancel != nil
}

func twoTrackQueue(first time.Duration) []queueItem {
	return []queueItem{
		{URL: "http://192.0.2.10:10243/1.mp3", Title: "01 First", Mime: "audio/mpeg", Duration: first},
		{URL: "http://192.0.2.10:10243/2.mp3", Title: "02 Second", Mime: "audio/mpeg", Duration: 5 * time.Minute},
	}
}

// The second half of the field failure: a watcher that found its queue gone
// left the slot taken, and the next folder got no watcher at all.
func TestAWatcherThatFindsNoQueueLeavesTheSlotForTheNextFolder(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("real-time watcher test")
	}
	s, _ := newPlayTestServer(t)
	s.queueTiming = fastQueueTiming()
	s.queueInZoneFn = func() bool { return false }
	box := &scriptedQueueBox{tick: qsec}
	s.nowPlayingFn = box.poll

	// The field order: the queue is ended before its watcher exists, so the
	// cancel has nothing to cancel, and the watcher then starts on no queue.
	s.queue.load(twoTrackQueue(2*qsec), 0, false, repeatOff)
	s.stopQueue("a hardware preset key was pressed")
	s.setQueueTiming(2 * qsec)
	s.ensureWatcher()
	t.Cleanup(s.cancelWatcher)
	deadline := time.Now().Add(2 * time.Second)
	for watcherSlotTaken(s) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if watcherSlotTaken(s) {
		t.Fatal("the watcher of a queue that was already gone still holds the slot")
	}

	// The next folder, on an awake speaker: its first track ends frozen on its
	// last second, the ST10/SA-5 way, and the queue has to move on.
	box.set("PLAY_STATE", 2*qsec, 2*qsec, false)
	s.queue.load(twoTrackQueue(2*qsec), 0, false, repeatOff)
	s.setQueueTiming(2 * qsec)
	s.ensureWatcher()
	if pos := waitForQueuePos(s, 1, 2*time.Second); pos != 1 {
		t.Fatalf("the next folder stayed on track %d; it was started without a watcher", pos)
	}
}

// A watcher that leaves frees only its own slot: a newer watcher that took the
// slot after a cancel keeps it.
func TestAnEndingWatcherDoesNotFreeANewerWatchersSlot(t *testing.T) {
	t.Parallel()
	s := quietServer("")
	s.queue = newPlayQueue()
	s.queueMu.Lock()
	s.queueWatcherID = 2
	newer := false
	s.queueCancel = func() { newer = true }
	s.queueMu.Unlock()

	s.releaseWatcher(1)
	if !watcherSlotTaken(s) || newer {
		t.Fatal("an old watcher's exit cancelled the watcher that replaced it")
	}
	if !s.retireIdleWatcher(1) {
		t.Fatal("a watcher of an inactive queue was told to stay")
	}
	if !watcherSlotTaken(s) || newer {
		t.Fatal("an old idle watcher freed the slot of the watcher that replaced it")
	}
	s.releaseWatcher(2)
	if watcherSlotTaken(s) || !newer {
		t.Fatal("a watcher's own exit did not free its slot")
	}
}

// A queue loaded again before the idle watcher leaves keeps that watcher.
func TestAnIdleWatcherStaysForAQueueLoadedMeanwhile(t *testing.T) {
	t.Parallel()
	s := quietServer("")
	s.queue = newPlayQueue()
	s.queue.load(twoTrackQueue(time.Minute), 0, false, repeatOff)
	s.queueMu.Lock()
	s.queueWatcherID = 1
	s.queueCancel = func() {}
	s.queueMu.Unlock()
	if s.retireIdleWatcher(1) {
		t.Fatal("the watcher left although a queue is active; that queue would play without one")
	}
	if !watcherSlotTaken(s) {
		t.Fatal("the slot was freed for a watcher that stays")
	}
}

// wakingBoxNowPlaying renders a scripted state as the speaker's now_playing reply.
func wakingBoxNowPlaying(np nowPlayingSnapshot) *boxapi.NowPlaying {
	if np.Source == "STANDBY" {
		return boxapi.ParseNowPlaying([]byte(`<nowPlaying deviceID="AABBCCDDEEFF" source="STANDBY"><ContentItem source="STANDBY" isPresetable="false" /></nowPlaying>`))
	}
	return boxapi.ParseNowPlaying([]byte(fmt.Sprintf(
		`<nowPlaying deviceID="AABBCCDDEEFF" source="%s"><ContentItem source="%s" location="%s"><itemName>%s</itemName></ContentItem><playStatus>%s</playStatus></nowPlaying>`,
		np.Source, np.Source, np.Location, np.ItemName, np.PlayStatus)))
}

// The first half: the speaker's own resume of its station, reported as a
// preset selected on the speaker while STR wakes it for a folder, must not
// take that folder's queue away. The folder plays on to its second track.
func TestAFolderStartedFromStandbySurvivesTheSpeakersOwnStationResume(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("real-time watcher test")
	}
	box := &fakeWakingBox{level: 20}
	s := newWakingServer(t, box)
	play, _ := newPlayTestServer(t)
	s.queue, s.renderer, s.presets = play.queue, play.renderer, play.presets
	t.Cleanup(func() { s.stopQueue("test cleanup") })
	s.queueTiming = fastQueueTiming()
	s.queueInZoneFn = func() bool { return false }
	s.boxNowReadFn = func(context.Context) (*boxapi.NowPlaying, error) { return wakingBoxNowPlaying(box.state()), nil }
	s.nativeStopFn = func(context.Context) bool {
		box.play(nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE"})
		return true
	}
	wake := s.newPlayWakeFn
	s.newPlayWakeFn = func(ctx context.Context) error {
		err := wake(ctx)
		// The frame the firmware sends for its power-on resume of preset 3,
		// handled the way cmd/agent handles a native radio preset.
		s.NoteNativeLastPlay("http://127.0.0.1:8888/stream/3", "Old Station", "", "audio/mpeg")
		return err
	}
	frozen := &scriptedQueueBox{tick: qsec}
	frozen.set("PLAY_STATE", 2*qsec, 2*qsec, false)
	s.nowPlayingFn = frozen.poll

	if err := s.startQueue(context.Background(), twoTrackQueue(2*qsec), 0, false, repeatOff, recentCardCtx{}); err != nil {
		t.Fatalf("startQueue: %v", err)
	}
	if !s.queue.isActive() {
		t.Fatal("the folder's queue was gone right after its first track was handed over")
	}
	if pos := waitForQueuePos(s, 1, 2*time.Second); pos != 1 {
		t.Fatalf("the folder stayed on track %d after the first one ended", pos)
	}
}
