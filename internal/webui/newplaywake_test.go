package webui

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeWakingBox scripts a speaker for the new-play wake: in standby until the
// wake, then on the station it resumed by itself, until the play moves it on.
type fakeWakingBox struct {
	mu     sync.Mutex
	awake  bool
	item   nowPlayingSnapshot
	level  int
	writes []int
	wakes  int
}

func (f *fakeWakingBox) state() nowPlayingSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.awake {
		return nowPlayingSnapshot{Source: "STANDBY"}
	}
	return f.item
}

func (f *fakeWakingBox) play(np nowPlayingSnapshot) {
	f.mu.Lock()
	f.item = np
	f.mu.Unlock()
}

func (f *fakeWakingBox) setLevel(v int) {
	f.mu.Lock()
	f.level = v
	f.mu.Unlock()
}

func (f *fakeWakingBox) levelWrites() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.writes...)
}

var oldStation = nowPlayingSnapshot{
	Source: "LOCAL_INTERNET_RADIO", ItemName: "Old Station", Location: "old-loc", PlayStatus: "PLAY_STATE",
}

func newWakingServer(t *testing.T, box *fakeWakingBox) *Server {
	t.Helper()
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10"}
	s.quietWakeNowPlayingFn = func(context.Context, string) nowPlayingSnapshot { return box.state() }
	s.newPlayWakeFn = func(context.Context) error {
		box.mu.Lock()
		box.wakes++
		box.awake = true
		// The firmware resumes its own last station at its own level.
		box.item = oldStation
		box.mu.Unlock()
		return nil
	}
	s.ownLevelGetFn = func(context.Context) (int, error) {
		box.mu.Lock()
		defer box.mu.Unlock()
		return box.level, nil
	}
	s.ownLevelSetFn = func(_ context.Context, v int) error {
		box.mu.Lock()
		box.level = v
		box.writes = append(box.writes, v)
		box.mu.Unlock()
		return nil
	}
	s.newPlayRestoreT = &newPlayRestoreTiming{wait: 600 * time.Millisecond, poll: 10 * time.Millisecond}
	return s
}

func waitForLevel(t *testing.T, box *fakeWakingBox, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		box.mu.Lock()
		got := box.level
		box.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("level never came back to %d (writes %v)", want, box.levelWrites())
}

// #1557: a sleeping speaker picked for another station resumed its previous
// one, audibly, for the second before STR's play took over. The level must be
// 0 by the time the wake returns, and come back only once the speaker plays
// the new station.
func TestNewPlayWakeKeepsTheResumedStationSilent(t *testing.T) {
	t.Parallel()
	box := &fakeWakingBox{level: 28}
	s := newWakingServer(t, box)

	playSent := s.ensureBoxReadyForNewPlay(context.Background(), nowPlayingSnapshot{Source: "STANDBY"})
	if box.wakes != 1 {
		t.Fatalf("woke %d times, want once", box.wakes)
	}
	box.mu.Lock()
	lvl := box.level
	box.mu.Unlock()
	if lvl != 0 {
		t.Fatalf("level %d after the wake: the resumed station is audible", lvl)
	}
	// The play takes over, then tells the wake it was sent.
	box.play(nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", ItemName: "SWR3", Location: "new-loc", PlayStatus: "BUFFERING_STATE"})
	playSent()
	waitForLevel(t, box, 28)
	for _, w := range box.levelWrites() {
		if w != 0 && w != 28 {
			t.Fatalf("unexpected level write %d (writes %v)", w, box.levelWrites())
		}
	}
}

// The level must not come back while the speaker still plays what it resumed:
// the play is sent, but the speaker has not switched yet.
func TestNewPlayWakeWaitsForTheSpeakerToLeaveTheResumedStation(t *testing.T) {
	t.Parallel()
	box := &fakeWakingBox{level: 30}
	s := newWakingServer(t, box)
	s.newPlayRestoreT = &newPlayRestoreTiming{wait: 2 * time.Second, poll: 10 * time.Millisecond}

	playSent := s.ensureBoxReadyForNewPlay(context.Background(), nowPlayingSnapshot{Source: "STANDBY"})
	playSent()
	time.Sleep(150 * time.Millisecond)
	box.mu.Lock()
	lvl := box.level
	box.mu.Unlock()
	if lvl != 0 {
		t.Fatalf("level back to %d while the resumed station still plays", lvl)
	}
	box.play(nowPlayingSnapshot{Source: "UPNP", ItemName: "Track", Location: "http://192.0.2.10:8888/x", PlayStatus: "PLAY_STATE"})
	waitForLevel(t, box, 30)
}

// The same station picked again never "moves on": the bounded wait still
// brings the level back.
func TestNewPlayWakeRestoresAfterTheBoundedWait(t *testing.T) {
	t.Parallel()
	box := &fakeWakingBox{level: 22}
	s := newWakingServer(t, box)
	start := time.Now()
	s.ensureBoxReadyForNewPlay(context.Background(), nowPlayingSnapshot{Source: "STANDBY"})()
	waitForLevel(t, box, 22)
	if el := time.Since(start); el < 500*time.Millisecond {
		t.Fatalf("restored after %s, before the wait was up", el)
	}
}

// A level somebody set during the play (the volume key, the start volume) is
// theirs: the restore must not overwrite it.
func TestNewPlayWakeLeavesALevelSetMeanwhile(t *testing.T) {
	t.Parallel()
	box := &fakeWakingBox{level: 25}
	s := newWakingServer(t, box)
	s.newPlayRestoreT = &newPlayRestoreTiming{wait: 50 * time.Millisecond, poll: 5 * time.Millisecond}
	playSent := s.ensureBoxReadyForNewPlay(context.Background(), nowPlayingSnapshot{Source: "STANDBY"})
	box.setLevel(12)
	playSent()
	time.Sleep(300 * time.Millisecond)
	box.mu.Lock()
	lvl := box.level
	box.mu.Unlock()
	if lvl != 12 {
		t.Fatalf("level %d, the user's 12 was overwritten", lvl)
	}
}

// An awake speaker has no resume to hide. The play must not be delayed by a
// wake, a read or a level write: a normal play costs what it did before.
func TestNewPlayWakeLeavesAnAwakeSpeakerAlone(t *testing.T) {
	t.Parallel()
	box := &fakeWakingBox{awake: true, item: oldStation, level: 20}
	s := newWakingServer(t, box)
	reads := 0
	s.quietWakeNowPlayingFn = func(context.Context, string) nowPlayingSnapshot { reads++; return box.state() }
	start := time.Now()
	s.ensureBoxReadyForNewPlay(context.Background(), oldStation)()
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Fatalf("took %s on an awake speaker", el)
	}
	time.Sleep(50 * time.Millisecond)
	if box.wakes != 0 || reads != 0 || len(box.levelWrites()) != 0 {
		t.Fatalf("touched an awake speaker: wakes=%d reads=%d writes=%v", box.wakes, reads, box.levelWrites())
	}
}

// Without a speaker configured there is nothing to wake or mute.
func TestNewPlayWakeWithoutABox(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.newPlayWakeFn = func(context.Context) error { t.Fatal("woke without a speaker"); return nil }
	s.ensureBoxReadyForNewPlay(context.Background(), nowPlayingSnapshot{Source: "STANDBY"})()
	s.ensureBoxReadyForNewPlayRead(context.Background())()
}

// A read taken the instant the speaker came up may carry the source but not
// the station yet; the station's name arriving later is not "moved on".
func TestMovedOnFromOnlyCountsWhatTheResumedReadCarried(t *testing.T) {
	t.Parallel()
	partial := nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO"}
	cases := []struct {
		name        string
		np, resumed nowPlayingSnapshot
		want        bool
	}{
		{"same station", oldStation, oldStation, false},
		{"other station same source", nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", ItemName: "SWR3", Location: "new"}, oldStation, true},
		{"other source", nowPlayingSnapshot{Source: "UPNP"}, oldStation, true},
		{"name filled in later", oldStation, partial, false},
		{"other source after a partial read", nowPlayingSnapshot{Source: "UPNP"}, partial, true},
	}
	for _, c := range cases {
		if got := movedOnFrom(c.np, c.resumed); got != c.want {
			t.Errorf("%s: movedOnFrom = %v, want %v", c.name, got, c.want)
		}
	}
}

// Every play that starts something new from standby takes the muted wake: the
// station URL play (Recently played, radio search, a library file), a preset
// recall and a library folder. Spotify has its own (WakeSilentlyForPlay).
func TestNewPlayEntryPointsUseTheMutedWake(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ file, fn string }{
		{"playback.go", "func (s *Server) handlePlay("},
		{"playback.go", "func (s *Server) playSlot("},
		{"queueplay.go", "func (s *Server) startQueueLocked("},
	} {
		src, err := readSourceFile(c.file)
		if err != nil {
			t.Fatalf("read %s: %v", c.file, err)
		}
		i := strings.Index(src, c.fn)
		if i < 0 {
			t.Fatalf("%s not found in %s", c.fn, c.file)
		}
		body := src[i:]
		body = body[:strings.Index(body, "\n}\n")]
		if !strings.Contains(body, "ensureBoxReadyForNewPlay") {
			t.Errorf("%s wakes without the mute: the speaker's last station is heard first (#1557)", c.fn)
		}
		if strings.Contains(body, "s.ensureBoxReady(") {
			t.Errorf("%s still takes the plain wake", c.fn)
		}
	}
}
