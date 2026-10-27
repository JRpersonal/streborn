package webui

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/streamproxy"
)

// fakeGate records what the muted wake asks of the stream proxy gate.
type fakeGate struct {
	mu    sync.Mutex
	calls []string
}

func (g *fakeGate) note(c string) {
	g.mu.Lock()
	g.calls = append(g.calls, c)
	g.mu.Unlock()
}

func (g *fakeGate) ArmNewPlayGate(streamproxy.NewPlayTarget, time.Duration) uint64 {
	g.note("arm")
	return 7
}
func (g *fakeGate) AddNewPlayTarget(string)           { g.note("add") }
func (g *fakeGate) NewPlaySent(uint64, time.Duration) { g.note("sent") }
func (g *fakeGate) ReleaseNewPlayGate(uint64, string) { g.note("release") }
func (g *fakeGate) ReleaseSentNewPlayGate(string)     { g.note("releaseSent") }
func (g *fakeGate) got() string                       { g.mu.Lock(); defer g.mu.Unlock(); return strings.Join(g.calls, ",") }

// The gate closes before the wake (the held connection must be gone before
// the speaker asks for it again) and, once the play went out, waits for the
// play's own fetch. A failed play opens it at once, and so does a play the
// proxy never serves.
func TestNewPlayWakeDrivesTheProxyGate(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		target    streamproxy.NewPlayTarget
		delivered bool
		want      string
	}{
		{"delivered through the proxy", radioTarget, true, "arm,sent"},
		{"failed", radioTarget, false, "arm,release"},
		{"delivered outside the proxy", streamproxy.NewPlayTarget{}, true, "arm,release"},
	} {
		box := &fakeWakingBox{level: 20}
		s := newWakingServer(t, box)
		g := &fakeGate{}
		s.newPlayGateFn = g
		s.newPlayWakeFn = func(context.Context) error {
			if g.got() != "arm" {
				t.Errorf("%s: the gate was not closed before the wake (%q)", c.name, g.got())
			}
			box.mu.Lock()
			box.awake, box.item = true, oldStation
			box.mu.Unlock()
			return nil
		}
		s.ensureBoxReadyForNewPlay(context.Background(), nowPlayingSnapshot{Source: "STANDBY"}, c.target)(c.delivered)
		if got := g.got(); got != c.want {
			t.Errorf("%s: gate calls %q, want %q", c.name, got, c.want)
		}
	}
}

// A play on an awake speaker never closes the gate, and only opens one an
// earlier play left waiting for its fetch.
func TestNewPlayOnAnAwakeSpeakerOnlyOpensAWaitingGate(t *testing.T) {
	t.Parallel()
	box := &fakeWakingBox{awake: true, item: oldStation, level: 20}
	s := newWakingServer(t, box)
	g := &fakeGate{}
	s.newPlayGateFn = g
	s.ensureBoxReadyForNewPlay(context.Background(), oldStation, radioTarget)(true)
	if got := g.got(); got != "releaseSent" {
		t.Fatalf("gate calls %q on an awake speaker, want only releaseSent", got)
	}
}

// The mute lands on the gabbo frame that shows the speaker out of standby,
// not when a poll or the end of the wake notices it (live ST10: the old
// station at level 10 for 0.7 s after the source change).
func TestNewPlayMuteFollowsTheFirstSourceFrame(t *testing.T) {
	t.Parallel()
	box := &fakeWakingBox{level: 20}
	s := newWakingServer(t, box)
	// The poll never sees life: only the frame can mute.
	s.quietWakeNowPlayingFn = func(context.Context, string) nowPlayingSnapshot { return nowPlayingSnapshot{Source: "STANDBY"} }
	muted := make(chan bool, 1)
	s.newPlayWakeFn = func(context.Context) error {
		box.setLevel(10) // the firmware's own resume level
		s.NoteBoxSourceChanged("STANDBY", "LOCAL_INTERNET_RADIO")
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			for _, w := range box.levelWrites() {
				if w == 0 {
					muted <- true
					return nil
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
		muted <- false
		return nil
	}
	s.ensureBoxReadyForNewPlay(context.Background(), nowPlayingSnapshot{Source: "STANDBY"}, radioTarget)(true)
	if !<-muted {
		t.Fatal("the source frame did not mute the speaker during the wake")
	}
}

// Outside a muted wake the source frame does nothing.
func TestSourceFrameOutsideAMutedWakeIsIgnored(t *testing.T) {
	t.Parallel()
	box := &fakeWakingBox{level: 20}
	s := newWakingServer(t, box)
	s.NoteBoxSourceChanged("STANDBY", "LOCAL_INTERNET_RADIO")
	time.Sleep(30 * time.Millisecond)
	if w := box.levelWrites(); len(w) != 0 {
		t.Fatalf("a user's own power-on was muted: %v", w)
	}
}

// A level the firmware raises while the play is still being started is muted
// again, a bounded number of times; after the play went out, a level change
// is the user's and is left alone.
func TestNewPlayRemutesALevelTheFirmwareRaises(t *testing.T) {
	t.Parallel()
	box := &fakeWakingBox{level: 20}
	s := newWakingServer(t, box)
	s.newPlayLevel.arm()
	for i := 0; i < newPlayMaxRemutes+2; i++ {
		s.NoteBoxVolumeLevel(10)
	}
	deadline := time.Now().Add(time.Second)
	for len(box.levelWrites()) < newPlayMaxRemutes && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(box.levelWrites()); n != newPlayMaxRemutes {
		t.Fatalf("%d re-mutes, want %d", n, newPlayMaxRemutes)
	}
	s.newPlayLevel.stopMuting()
	s.NoteBoxVolumeLevel(15)
	time.Sleep(20 * time.Millisecond)
	if n := len(box.levelWrites()); n != newPlayMaxRemutes {
		t.Fatalf("muted the user's own level after the play went out (writes %v)", box.levelWrites())
	}
	// Still inside the episode: neither the firmware's 10 nor the user's 15
	// is taken as the level the speaker had before standby.
	s.newPlayLevel.mu.Lock()
	recorded := !s.newPlayLevel.lastAt.IsZero()
	s.newPlayLevel.mu.Unlock()
	if recorded {
		t.Fatal("a level seen during the muted wake was recorded as the speaker's own")
	}
	s.newPlayLevel.episodeDone()
	s.NoteBoxVolumeLevel(15)
	if got := s.levelBeforeStandby(context.Background()); got != 15 {
		t.Fatalf("level after the episode %d, want 15", got)
	}
}

// The level put back is the one the speaker reported while awake, not what a
// speaker in standby answers (live ST10: read 5, played at 10).
func TestNewPlayRestoresTheLevelFromBeforeStandby(t *testing.T) {
	t.Parallel()
	box := &fakeWakingBox{level: 5}
	s := newWakingServer(t, box)
	s.NoteBoxVolumeLevel(10)
	playSent := s.ensureBoxReadyForNewPlay(context.Background(), nowPlayingSnapshot{Source: "STANDBY"}, radioTarget)
	box.play(nowPlayingSnapshot{Source: "UPNP", ItemName: "New", Location: "new"})
	playSent(true)
	waitForLevel(t, box, 10)
}

// Without a level seen while awake, the speaker's target level is preferred
// over its actual one.
func TestLevelBeforeStandbyPrefersTheTargetLevel(t *testing.T) {
	t.Parallel()
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10"}
	s.ownVolumeFn = func(context.Context) (boxapi.Volume, error) { return boxapi.Volume{Target: 10, Actual: 5}, nil }
	if got := s.levelBeforeStandby(context.Background()); got != 10 {
		t.Fatalf("level %d, want the target 10", got)
	}
}
