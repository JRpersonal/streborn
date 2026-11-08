package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// wakeLevelBox scripts a speaker for the level a muted wake puts back. It reports
// every level change as a gabbo volume frame (NoteBoxVolumeLevel), the way the
// real speaker does for STR's own writes as much as for the user's. Asleep it
// answers with the last level written into it; woken, the firmware comes up at
// its own power-on level and resumes its last station.
type wakeLevelBox struct {
	s *Server

	mu       sync.Mutex
	awake    bool
	stopped  bool
	level    int
	fwLevel  int  // the firmware's own power-on level
	noFrames bool // the gabbo bus is down: no volume frames
	readErr  bool // GetVolume fails
	writes   []int
}

func (b *wakeLevelBox) frame(level int) {
	b.mu.Lock()
	quiet := b.noFrames
	b.mu.Unlock()
	if !quiet {
		b.s.NoteBoxVolumeLevel(level)
	}
}

func (b *wakeLevelBox) state() nowPlayingSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.awake {
		return nowPlayingSnapshot{Source: "STANDBY"}
	}
	np := oldStation
	if b.stopped {
		np.PlayStatus = "STOP_STATE"
	}
	return np
}

func (b *wakeLevelBox) wake() {
	b.mu.Lock()
	b.awake, b.stopped = true, false
	b.level = b.fwLevel
	lvl := b.level
	b.mu.Unlock()
	b.frame(lvl)
}

func (b *wakeLevelBox) sleep() {
	b.mu.Lock()
	b.awake = false
	b.mu.Unlock()
}

// userSet is a level set by somebody else: the group volume, the volume key.
func (b *wakeLevelBox) userSet(v int) {
	b.mu.Lock()
	b.level = v
	b.mu.Unlock()
	b.frame(v)
}

func (b *wakeLevelBox) levelWrites() []int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int(nil), b.writes...)
}

func (b *wakeLevelBox) current() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.level
}

func newLevelServer(t *testing.T, b *wakeLevelBox) *Server {
	t.Helper()
	dir := t.TempDir()
	s := &Server{
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		boxHost:         "192.0.2.10",
		startVolumePath: filepath.Join(dir, "start-volume"),
		maxVolumePath:   filepath.Join(dir, "max-volume"),
	}
	b.s = s
	s.quietWakeNowPlayingFn = func(context.Context, string) nowPlayingSnapshot { return b.state() }
	s.quietWakeStopFn = func(context.Context, string) error {
		b.mu.Lock()
		b.stopped = true
		b.mu.Unlock()
		return nil
	}
	s.quietWakeWakeFn = func(context.Context) error { b.wake(); return nil }
	s.newPlayWakeFn = func(context.Context) error { b.wake(); return nil }
	s.ownVolumeFn = func(context.Context) (boxapi.Volume, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.readErr {
			return boxapi.Volume{}, errors.New("speaker did not answer")
		}
		return boxapi.Volume{Target: b.level, Actual: b.level}, nil
	}
	s.ownLevelSetFn = func(_ context.Context, v int) error {
		b.mu.Lock()
		b.level = v
		b.writes = append(b.writes, v)
		b.mu.Unlock()
		b.frame(v)
		return nil
	}
	s.newPlayRestoreT = &newPlayRestoreTiming{wait: 100 * time.Millisecond, poll: 10 * time.Millisecond}
	return s
}

func waitLevel(t *testing.T, b *wakeLevelBox, want int, step string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b.current() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s: level %d, want %d", step, b.current(), want)
}

func quietWakeOrFail(t *testing.T, s *Server, step string) {
	t.Helper()
	if err := s.quietWake(context.Background()); err != nil {
		t.Fatalf("%s: quiet wake: %v", step, err)
	}
}

// The v1.5.3 bundle (two SoundTouch 10 in a stereo pair): groups and pairs
// formed and dissolved, then a preset. The speaker sat at 0 from an earlier
// quiet wake; every later wake read that 0 from the sleeping speaker or from
// STR's own mute frames and "restored" it, including over the group volume 14
// set during the pairing, and the preset after it played at 0.
func TestGroupPairUndoPairPresetNeverRestoresZero(t *testing.T) {
	t.Parallel()
	b := &wakeLevelBox{level: 0, fwLevel: 10}
	s := newLevelServer(t, b)

	// Group: the sleeping speaker reports the 0 left in it. Nothing is
	// remembered (the agent just started), so the level comes back to the
	// firmware's own power-on level, not to the 0.
	quietWakeOrFail(t, s, "group")
	if got := b.current(); got != 0 {
		t.Fatalf("group: level %d during the quiet wake, want the mute", got)
	}
	s.restoreQuietWakeVolume("zone joined")
	waitLevel(t, b, 10, "group")

	// Dissolve: back to standby.
	b.sleep()

	// Pair: the group volume is set during the pairing; the pair then goes
	// back to standby and the restore must leave the 14.
	quietWakeOrFail(t, s, "pair")
	b.userSet(14)
	s.restoreQuietWakeVolume("pair back to standby")
	waitLevel(t, b, 14, "pair")
	b.sleep()

	// Undo, then pair again: the level from before is the 14.
	quietWakeOrFail(t, s, "pair again")
	s.restoreQuietWakeVolume("pair back to standby")
	waitLevel(t, b, 14, "pair again")
	b.sleep()

	// Preset on the sleeping pair master.
	playSent := s.ensureBoxReadyForNewPlay(context.Background(), nowPlayingSnapshot{Source: "STANDBY"}, radioTarget)
	if got := b.current(); got != 0 {
		t.Fatalf("preset: level %d while the resumed station plays, want the mute", got)
	}
	playSent(true)
	waitLevel(t, b, 14, "preset")
	if got := s.rememberedAwakeLevel(); got != 14 {
		t.Fatalf("remembered level %d after the preset, want 14", got)
	}
}

// A group volume set while the quiet wake still holds the level is the user's
// (or the app's for the group) and must not be overwritten by the release.
func TestQuietWakeReleaseLeavesAGroupVolumeSetMeanwhile(t *testing.T) {
	t.Parallel()
	b := &wakeLevelBox{level: 30, fwLevel: 30}
	s := newLevelServer(t, b)
	s.NoteBoxVolumeLevel(30) // awake earlier at 30
	b.mu.Lock()
	b.level = 0 // the last level written into the sleeping speaker
	b.mu.Unlock()

	quietWakeOrFail(t, s, "wake")
	b.userSet(14)
	writes := len(b.levelWrites())
	s.restoreQuietWakeVolume("zone joined")
	time.Sleep(20 * time.Millisecond)
	if got := b.current(); got != 14 {
		t.Fatalf("level %d, the group volume 14 was overwritten", got)
	}
	if w := b.levelWrites(); len(w) != writes {
		t.Fatalf("the release wrote %v over the group volume", w[writes:])
	}
	// The 14 is now the level the speaker plays at.
	if got := s.rememberedAwakeLevel(); got != 14 {
		t.Fatalf("remembered %d, want the 14 left in place", got)
	}
}

// The frames of a group operation are not the speaker's own level: not STR's
// mute, not the firmware's power-on level, not a level set for the group.
// The first non-zero one is kept as the power-on level instead.
func TestVolumeFramesDuringAGroupWakeAreNotRecorded(t *testing.T) {
	t.Parallel()
	s := quietWakeTestServer()
	s.NoteBoxVolumeLevel(22)
	s.groupWake.begin()
	s.NoteBoxVolumeLevel(0)
	s.NoteBoxVolumeLevel(10)
	s.NoteBoxVolumeLevel(0)
	if got := s.rememberedAwakeLevel(); got != 22 {
		t.Fatalf("remembered %d during the wake, want the 22 from before", got)
	}
	if got := s.groupWake.heldPowerOn(); got != 10 {
		t.Fatalf("power-on level %d, want the firmware's 10", got)
	}
	s.groupWake.arm(wakeLevelInputs{remembered: 22}, time.Minute)
	s.groupWake.end()
	s.NoteBoxVolumeLevel(14) // the group volume while the hold is armed
	if got := s.rememberedAwakeLevel(); got != 22 {
		t.Fatalf("remembered %d while the hold is armed, want 22", got)
	}
	s.restoreQuietWakeVolume("zone joined") // no box host: bookkeeping only
	s.NoteBoxVolumeLevel(0)
	if got := s.rememberedAwakeLevel(); got != 22 {
		t.Fatalf("a 0 was recorded as the speaker's level (%d)", got)
	}
	s.NoteBoxVolumeLevel(18)
	if got := s.rememberedAwakeLevel(); got != 18 {
		t.Fatalf("remembered %d after the hold, want 18", got)
	}
}

// GetVolume failing used to arm nothing, so the quiet wake's mute was never
// lifted. The speaker now comes back to its own power-on level.
func TestQuietWakeWithAFailedVolumeReadDoesNotStayMuted(t *testing.T) {
	t.Parallel()
	b := &wakeLevelBox{level: 0, fwLevel: 12, readErr: true}
	s := newLevelServer(t, b)
	quietWakeOrFail(t, s, "wake")
	if got := b.current(); got != 0 {
		t.Fatalf("level %d during the wake, want the mute", got)
	}
	s.restoreQuietWakeVolume("zone joined")
	waitLevel(t, b, 12, "release")
}

// Nothing known at all (no frames, no read): the start volume when the user
// set one, else the default. Never the 0.
func TestQuietWakeWithNothingKnownFallsBackToAnAudibleLevel(t *testing.T) {
	t.Parallel()
	b := &wakeLevelBox{level: 0, fwLevel: 12, readErr: true, noFrames: true}
	s := newLevelServer(t, b)
	quietWakeOrFail(t, s, "default")
	s.restoreQuietWakeVolume("zone joined")
	waitLevel(t, b, defaultWakeRestoreLevel, "default")

	b2 := &wakeLevelBox{level: 0, fwLevel: 12, readErr: true, noFrames: true}
	s2 := newLevelServer(t, b2)
	if err := os.WriteFile(s2.startVolumePath, []byte("22"), 0o644); err != nil {
		t.Fatal(err)
	}
	quietWakeOrFail(t, s2, "start volume")
	s2.restoreQuietWakeVolume("zone joined")
	waitLevel(t, b2, 22, "start volume")
}

// A wake that fails half way still muted a speaker that came up: it is
// released at once, and not left at 0.
func TestQuietWakeThatFailsDoesNotLeaveTheSpeakerMuted(t *testing.T) {
	t.Parallel()
	b := &wakeLevelBox{level: 0, fwLevel: 16}
	s := newLevelServer(t, b)
	s.quietWakeWakeFn = func(context.Context) error {
		b.wake()
		time.Sleep(300 * time.Millisecond) // the first-life watcher mutes
		return errors.New("did not settle")
	}
	if err := s.quietWake(context.Background()); err == nil {
		t.Fatal("the failed wake was reported as a success")
	}
	waitLevel(t, b, 16, "failed wake")
	if s.quietWakeActive() {
		t.Fatal("a failed wake left the hold armed")
	}
}

// The new-play wake on a speaker nothing is remembered of and that answers 0
// in standby: it used to restore the 0 (or, unreadable, nothing at all).
func TestNewPlayWakeNeverRestoresZero(t *testing.T) {
	t.Parallel()
	b := &wakeLevelBox{level: 0, fwLevel: 9}
	s := newLevelServer(t, b)
	s.ensureBoxReadyForNewPlay(context.Background(), nowPlayingSnapshot{Source: "STANDBY"}, radioTarget)(true)
	waitLevel(t, b, 9, "power-on level")

	b2 := &wakeLevelBox{level: 0, fwLevel: 9, readErr: true, noFrames: true}
	s2 := newLevelServer(t, b2)
	s2.ensureBoxReadyForNewPlay(context.Background(), nowPlayingSnapshot{Source: "STANDBY"}, radioTarget)(true)
	waitLevel(t, b2, defaultWakeRestoreLevel, "nothing known")
}

// The silent wake for a Spotify hand-over had the same capture of the 0.
func TestSilentWakeNeverRestoresZero(t *testing.T) {
	t.Parallel()
	b := &wakeLevelBox{level: 0, fwLevel: 11}
	s := newLevelServer(t, b)
	restore := s.WakeSilentlyForPlay(context.Background())
	if got := b.current(); got != 0 {
		t.Fatalf("level %d after the silent wake, want the mute", got)
	}
	restore()
	restore() // twice is once
	waitLevel(t, b, 11, "silent wake")
	s.groupWake.mu.Lock()
	waking := s.groupWake.waking
	s.groupWake.mu.Unlock()
	if waking != 0 {
		t.Fatalf("the silent wake's hold was not ended (waking=%d)", waking)
	}
}

func TestWakeRestoreTargetIsNeverZero(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := &Server{startVolumePath: filepath.Join(dir, "sv"), maxVolumePath: filepath.Join(dir, "mv")}
	cases := []struct {
		name    string
		in      wakeLevelInputs
		powerOn int
		want    int
	}{
		{"remembered wins", wakeLevelInputs{remembered: 20, standby: 5}, 10, 20},
		{"power-on before the standby read", wakeLevelInputs{standby: 5}, 10, 10},
		{"standby read", wakeLevelInputs{standby: 5}, 0, 5},
		{"nothing known", wakeLevelInputs{}, 0, defaultWakeRestoreLevel},
	}
	for _, c := range cases {
		if got, _ := s.wakeRestoreTarget(c.in, c.powerOn); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
	if err := os.WriteFile(s.maxVolumePath, []byte("12"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.wakeRestoreTarget(wakeLevelInputs{remembered: 40}, 0); got != 12 {
		t.Fatalf("restore %d above the maximum volume 12", got)
	}
}
