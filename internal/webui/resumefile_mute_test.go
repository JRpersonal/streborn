package webui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// fakeLevel gives a pausedFileBox server a speaker level: reads answer level,
// writes set it and are recorded in the box's own action list as "Vol=N", so a
// test sees them in order with the UPnP commands.
func fakeLevel(s *Server, box *pausedFileBox, level int) {
	s.ownVolumeFn = func(context.Context) (boxapi.Volume, error) {
		box.mu.Lock()
		defer box.mu.Unlock()
		return boxapi.Volume{Target: level, Actual: level}, nil
	}
	s.ownLevelSetFn = func(_ context.Context, v int) error {
		box.mu.Lock()
		defer box.mu.Unlock()
		level = v
		box.actions = append(box.actions, "Vol="+strconv.Itoa(v))
		return nil
	}
}

// #830: the restart's Play starts the file from its beginning, and the Seek
// lands about two seconds later. The speaker is silent from before the Stop
// until after the Seek, then gets its level back.
func TestFileRestartIsSilentUntilTheSeekLands(t *testing.T) {
	t.Parallel()
	s, box := newPausedFileServer(t, "PAUSED_PLAYBACK")
	fakeLevel(s, box, 30)
	armTestFile(s)

	if status, err := s.resumePlayback(context.Background()); err != nil || status != "playing" {
		t.Fatalf("resumePlayback = %q, %v", status, err)
	}
	got := strings.Join(box.commands(), ",")
	if got != "Play,Vol=0,Stop,Play,Seek,Vol=30" {
		t.Fatalf("commands = %s, want the plain Play, then silent from before the Stop until after the Seek", got)
	}
}

// Somebody turning the speaker up during the restart wins: the level is not
// put back over theirs.
func TestFileRestartLeavesALevelChangedDuringIt(t *testing.T) {
	t.Parallel()
	s, box := newPausedFileServer(t, "PAUSED_PLAYBACK")
	fakeLevel(s, box, 30)
	set := s.ownLevelSetFn
	s.ownLevelSetFn = func(ctx context.Context, v int) error {
		err := set(ctx, v)
		if v == 0 {
			// The listener turns it up right after STR silenced it.
			box.mu.Lock()
			box.actions = append(box.actions, "user")
			box.mu.Unlock()
			_ = set(ctx, 12)
		}
		return err
	}
	armTestFile(s)

	if _, err := s.resumePlayback(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(box.commands(), ",")
	if got != "Play,Vol=0,user,Vol=12,Stop,Play,Seek" {
		t.Fatalf("commands = %s, want no restore over the listener's level", got)
	}
}

// A level that cannot be read is never written: no mute, so nothing can be
// left at 0.
func TestFileRestartWithoutAReadableLevelMutesNothing(t *testing.T) {
	t.Parallel()
	s, box := newPausedFileServer(t, "PAUSED_PLAYBACK")
	fakeLevel(s, box, 30)
	s.ownVolumeFn = func(context.Context) (boxapi.Volume, error) { return boxapi.Volume{}, errors.New("no answer") }
	armTestFile(s)

	if _, err := s.resumePlayback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(box.commands(), ","); got != "Play,Stop,Play,Seek" {
		t.Fatalf("commands = %s, want the restart without any level write", got)
	}
}

// A refused seek plays the track from its start, and that must be heard: the
// level comes back without waiting.
func TestFileRestartWithARefusedSeekPutsTheLevelBack(t *testing.T) {
	t.Parallel()
	s, box := newPausedFileServer(t, "PAUSED_PLAYBACK")
	box.refuseSeek = true
	fakeLevel(s, box, 30)
	armTestFile(s)

	if _, err := s.resumePlayback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(box.commands(), ","); got != "Play,Vol=0,Stop,Play,Seek,Seek,Vol=30" {
		t.Fatalf("commands = %s, want the level back after the refused seeks", got)
	}
}
