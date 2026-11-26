// Tests for carrying on with the music after an agent update
// (resume_update.go): which speakers count as "was playing" when the update
// is accepted, and what the restarted agent does with the marker.

package webui

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const updateResumeTestURL = "http://127.0.0.1:8888/stream/2"

// fakeUpdateResumeBox is the speaker the resume talks to. Its clock moves
// only when the code under test sleeps, so no test waits for real time.
type fakeUpdateResumeBox struct {
	mu sync.Mutex
	// clock; clockBadFor sleeps pass before it counts as set.
	t           time.Time
	clockBadFor int
	sleeps      int
	np          nowPlayingSnapshot
	// unreadable reads fail before the speaker answers.
	unreadable int
	vol        int
	volOK      bool
	volSets    []int
	follows    bool
	wakes      int
	pushes     []string
	// pushErrs are returned by the first pushes, in order.
	pushErrs []error
	// markerAtPush records whether the marker file still existed when the
	// first push ran.
	path         string
	markerAtPush []bool
}

func (b *fakeUpdateResumeBox) fx() *updateResumeFx {
	return &updateResumeFx{
		now: func() time.Time {
			b.mu.Lock()
			defer b.mu.Unlock()
			return b.t
		},
		sleep: func(d time.Duration) {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.t = b.t.Add(d)
			b.sleeps++
		},
		clockOK: func(time.Time) bool {
			b.mu.Lock()
			defer b.mu.Unlock()
			return b.sleeps >= b.clockBadFor
		},
		nowPlaying: func() nowPlayingSnapshot {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.unreadable > 0 {
				b.unreadable--
				return nowPlayingSnapshot{}
			}
			return b.np
		},
		volume: func() (int, bool) {
			b.mu.Lock()
			defer b.mu.Unlock()
			return b.vol, b.volOK
		},
		setVolume: func(v int) error {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.volSets = append(b.volSets, v)
			b.vol = v
			return nil
		},
		follower: func() (bool, string) {
			b.mu.Lock()
			defer b.mu.Unlock()
			return b.follows, "this speaker is a group follower"
		},
		wake: func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.wakes++
		},
		push: func(m updateResumeMarker) (bool, error) {
			_, statErr := os.Stat(b.path)
			b.mu.Lock()
			defer b.mu.Unlock()
			b.markerAtPush = append(b.markerAtPush, statErr == nil)
			b.pushes = append(b.pushes, m.BoxURL)
			if n := len(b.pushes); n <= len(b.pushErrs) {
				return false, b.pushErrs[n-1]
			}
			return false, nil
		},
	}
}

func (b *fakeUpdateResumeBox) pushCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pushes)
}

// newUpdateResumeServer is an agent with a radio station on record and a
// sleeping speaker behind it, as the agent that starts after an update finds
// it.
func newUpdateResumeServer(t *testing.T) (*Server, *fakeUpdateResumeBox) {
	t.Helper()
	box := &fakeUpdateResumeBox{
		// A second behind the real clock, which stamps the stop latches:
		// "stopped after the hush" must not hang on the timer resolution.
		t:     time.Now().Add(-time.Second),
		np:    nowPlayingSnapshot{Source: "STANDBY"},
		vol:   20,
		volOK: true,
		path:  filepath.Join(t.TempDir(), "resume-after-update"),
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.updateResumePath = box.path
	s.updateResumeFx = box.fx()
	// The production bounds with waits too short to matter: the fake clock
	// stays next to the real one, which the stop latches are stamped with.
	tm := productionUpdateResumeTiming
	tm.settle, tm.clockStep, tm.boxReadGap, tm.pushGap, tm.volumeSettle = 1, 1, 1, 1, 1
	tm.volumeCheckGap, tm.selfStartGap = 1, 1
	tm.clockWait = 20
	s.updateResumeTiming = &tm
	s.play.last = &lastPlayInfo{boxURL: updateResumeTestURL, title: "Last Station", ts: box.t.Add(-time.Hour)}
	return s, box
}

// armUpdateResume writes the marker an update leaves for the station on
// record, written ago before the fake clock's now, and loads it the way the
// starting agent does.
func armUpdateResume(t *testing.T, s *Server, box *fakeUpdateResumeBox, ago time.Duration, volume int) {
	t.Helper()
	m := updateResumeMarker{BoxURL: s.play.last.boxURL, PlayTS: s.play.last.ts, Volume: volume, At: box.t.Add(-ago)}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(box.path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	s.loadUpdateResume()
}

// waitUpdateResume waits for the resume goroutine to finish.
func waitUpdateResume(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.updResume.mu.Lock()
		running := s.updResume.running
		s.updResume.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the resume after update did not finish")
}

func markerExists(box *fakeUpdateResumeBox) bool {
	_, err := os.Stat(box.path)
	return err == nil
}

func TestUpdateResumeSkipReason(t *testing.T) {
	t.Parallel()
	now := time.Now()
	last := lastPlayInfo{boxURL: updateResumeTestURL, title: "Last Station", ts: now.Add(-time.Hour)}
	playing := nowPlayingSnapshot{Source: "UPNP", Location: updateResumeTestURL, PlayStatus: "PLAY_STATE"}
	silent := nowPlayingSnapshot{Source: "STANDBY"}
	tests := []struct {
		name string
		in   updateResumeInput
		// want is a fragment of the reason, "" for "resume".
		want string
	}{
		{name: "playing STR's station", in: updateResumeInput{np: playing, last: last, hasLast: true}},
		{name: "still buffering STR's station", in: updateResumeInput{
			np: nowPlayingSnapshot{Source: "UPNP", PlayStatus: "BUFFERING_STATE"}, last: last, hasLast: true}},
		{name: "playing a native radio preset", in: updateResumeInput{
			np: nowPlayingSnapshot{Source: nativeRadioSource, Location: "/station?data=abc", PlayStatus: "PLAY_STATE"}, last: last, hasLast: true}},
		{name: "paused", in: updateResumeInput{
			np: nowPlayingSnapshot{Source: "UPNP", Location: updateResumeTestURL, PlayStatus: "PAUSE_STATE"}, last: last, hasLast: true},
			want: "was not playing"},
		{name: "standby", in: updateResumeInput{np: silent, last: last, hasLast: true}, want: "was not playing"},
		{name: "unreadable speaker", in: updateResumeInput{last: last, hasLast: true}, want: "was not playing"},
		{name: "Bluetooth", in: updateResumeInput{
			np: nowPlayingSnapshot{Source: "BLUETOOTH", PlayStatus: "PLAY_STATE"}, last: last, hasLast: true},
			want: "source of its own (BLUETOOTH)"},
		{name: "AUX", in: updateResumeInput{
			np: nowPlayingSnapshot{Source: "AUX", PlayStatus: "PLAY_STATE"}, last: last, hasLast: true},
			want: "source of its own (AUX)"},
		{name: "AirPlay", in: updateResumeInput{
			np: nowPlayingSnapshot{Source: "AIRPLAY", PlayStatus: "PLAY_STATE"}, last: last, hasLast: true},
			want: "source of its own (AIRPLAY)"},
		{name: "Spotify through STR's engine", in: updateResumeInput{
			np:   nowPlayingSnapshot{Source: "UPNP", Location: "http://127.0.0.1:8888/spotify/stream-4.ogg", PlayStatus: "PLAY_STATE"},
			last: lastPlayInfo{boxURL: "http://127.0.0.1:8888/spotify/stream-4.ogg", ts: now}, hasLast: true},
			want: "Spotify is not started again"},
		{name: "a stream somebody else pushed", in: updateResumeInput{
			np:   nowPlayingSnapshot{Source: "UPNP", Location: "http://192.0.2.50:8095/stream/flow.mp3", PlayStatus: "PLAY_STATE"},
			last: last, hasLast: true},
			want: "STR did not start"},
		{name: "nothing on record", in: updateResumeInput{np: playing}, want: "no record"},
		{name: "group member", in: updateResumeInput{np: playing, last: last, hasLast: true,
			follower: func() (bool, string) { return true, "this speaker is a group follower" }},
			want: "group member and not the master"},
		{name: "group master or standalone", in: updateResumeInput{np: playing, last: last, hasLast: true,
			follower: func() (bool, string) { return false, "" }}},
		// The desktop app delivers the Spotify engine right before the agent:
		// that transfer already quietened the speaker.
		{name: "quietened by the engine delivery a moment ago", in: updateResumeInput{np: silent, last: last, hasLast: true,
			hushedAt: now.Add(-40 * time.Second), hushedURL: last.boxURL, hushedTS: last.ts}},
		{name: "quietened, then stopped by the user", in: updateResumeInput{np: silent, last: last, hasLast: true,
			hushedAt: now.Add(-40 * time.Second), hushedURL: last.boxURL, hushedTS: last.ts, stoppedSince: true},
			want: "stopped or paused"},
		{name: "quietened, then something else was played and stopped", in: updateResumeInput{np: silent, last: last, hasLast: true,
			hushedAt: now.Add(-40 * time.Second), hushedURL: "http://127.0.0.1:8888/stream/5", hushedTS: now.Add(-2 * time.Hour)},
			want: "something else was played"},
		{name: "quietened, then switched to AUX", in: updateResumeInput{
			np: nowPlayingSnapshot{Source: "AUX", PlayStatus: "STOP_STATE"}, last: last, hasLast: true,
			hushedAt: now.Add(-40 * time.Second), hushedURL: last.boxURL, hushedTS: last.ts},
			want: "another source was selected"},
		{name: "quietened long ago", in: updateResumeInput{np: silent, last: last, hasLast: true,
			hushedAt: now.Add(-updateResumeMaxAge - time.Minute), hushedURL: last.boxURL, hushedTS: last.ts},
			want: "was not playing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.now = now
			got := updateResumeSkipReason(tt.in)
			if tt.want == "" && got != "" {
				t.Fatalf("resume refused: %q", got)
			}
			if tt.want != "" && !strings.Contains(got, tt.want) {
				t.Fatalf("reason = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

// The whole way: an update is accepted while the speaker plays, the marker
// goes to the NAND with the level, and the agent that starts afterwards
// plays the station again.
func TestUpdateResumeMarkerWrittenAndUsedOnce(t *testing.T) {
	t.Parallel()
	s, box := newUpdateResumeServer(t)
	box.vol = 34
	playing := nowPlayingSnapshot{Source: "UPNP", Location: updateResumeTestURL, PlayStatus: "PLAY_STATE"}
	m := s.planUpdateResume(playing)
	if m == nil {
		t.Fatal("a playing speaker got no marker")
	}
	s.writeUpdateResumeMarker(m)
	b, err := os.ReadFile(box.path)
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	var onNAND updateResumeMarker
	if err := json.Unmarshal(b, &onNAND); err != nil {
		t.Fatal(err)
	}
	if onNAND.BoxURL != updateResumeTestURL || onNAND.Volume != 34 || onNAND.At.IsZero() {
		t.Fatalf("marker = %+v", onNAND)
	}

	// The restarted agent: the record comes back from the NAND, the level
	// the firmware kept is the same.
	s2, box2 := newUpdateResumeServer(t)
	box2.path = box.path
	s2.updateResumePath = box.path
	s2.play.last = &lastPlayInfo{boxURL: updateResumeTestURL, title: "Last Station", ts: onNAND.PlayTS}
	box2.t = onNAND.At.Add(90 * time.Second)
	box2.vol = 34
	s2.loadUpdateResume()
	if !markerExists(box2) {
		t.Fatal("loading the marker must leave it on the NAND until the resume is decided")
	}
	if !s2.ResumeAfterUpdate() {
		t.Fatal("ResumeAfterUpdate found no marker")
	}
	waitUpdateResume(t, s2)
	if got := box2.pushCount(); got != 1 {
		t.Fatalf("pushes = %d, want 1", got)
	}
	if box2.pushes[0] != updateResumeTestURL || box2.wakes != 1 {
		t.Fatalf("pushed %v after %d wakes", box2.pushes, box2.wakes)
	}
	if box2.markerAtPush[0] {
		t.Fatal("the marker must be gone before the stream starts, so a stream that takes the speaker down is not tried again")
	}
	if len(box2.volSets) != 0 {
		t.Fatalf("the level was the same, nothing to set: %v", box2.volSets)
	}
	// Used once: a later reconnect, or the next boot, finds nothing.
	if s2.ResumeAfterUpdate() {
		t.Fatal("a used marker started a second resume")
	}
	s3, box3 := newUpdateResumeServer(t)
	box3.path = box.path
	s3.updateResumePath = box.path
	s3.loadUpdateResume()
	if s3.ResumeAfterUpdate() || box3.pushCount() != 0 {
		t.Fatal("the next boot must not find the marker again")
	}
}

func TestUpdateResumeNotPlayingWritesNoMarker(t *testing.T) {
	t.Parallel()
	s, box := newUpdateResumeServer(t)
	// A marker a failed earlier attempt left behind goes with it.
	if err := os.WriteFile(box.path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := s.planUpdateResume(nowPlayingSnapshot{Source: "UPNP", Location: updateResumeTestURL, PlayStatus: "PAUSE_STATE"})
	if m != nil {
		t.Fatalf("a paused speaker got a marker: %+v", m)
	}
	s.writeUpdateResumeMarker(m)
	if markerExists(box) {
		t.Fatal("no marker may remain for a speaker that was not playing")
	}
}

// The engine goes first and quietens the speaker; the agent upload behind it
// finds a silent speaker and still has to know it was playing.
func TestUpdateResumeRemembersTheEarlierHush(t *testing.T) {
	t.Parallel()
	s, _ := newUpdateResumeServer(t)
	withNowPlaying(s, nowPlayingSnapshot{Source: "UPNP", Location: updateResumeTestURL, PlayStatus: "PLAY_STATE"})
	withStopSeam(s, &stopCounter{})
	s.hushForUpload("sidecar")

	withNowPlaying(s, nowPlayingSnapshot{Source: "STANDBY"})
	if m := s.planUpdateResume(s.hushForUpload("agent-update")); m == nil {
		t.Fatal("the speaker was playing until the engine delivery quietened it, and got no marker")
	}
	// A stop asked for in between is the user's last word.
	s.NoteExplicitStop()
	if m := s.planUpdateResume(s.hushForUpload("agent-update")); m != nil {
		t.Fatal("a speaker the user stopped after the hush got a marker")
	}
}

func TestUpdateResumeDecisionsAfterRestart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// ago is the marker's age.
		ago    time.Duration
		setup  func(s *Server, box *fakeUpdateResumeBox)
		pushes int
	}{
		{name: "fresh marker, sleeping speaker", ago: 2 * time.Minute, pushes: 1},
		{name: "awake and idle on STR's source", ago: 2 * time.Minute, pushes: 1,
			setup: func(_ *Server, box *fakeUpdateResumeBox) {
				box.np = nowPlayingSnapshot{Source: "INVALID_SOURCE", PlayStatus: "STOP_STATE"}
			}},
		{name: "stale marker", ago: updateResumeMaxAge + time.Minute},
		{name: "marker from the future", ago: -10 * time.Minute},
		{name: "already playing", ago: 2 * time.Minute,
			setup: func(_ *Server, box *fakeUpdateResumeBox) {
				box.np = nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE"}
			}},
		{name: "playing Bluetooth by now", ago: 2 * time.Minute,
			setup: func(_ *Server, box *fakeUpdateResumeBox) {
				box.np = nowPlayingSnapshot{Source: "BLUETOOTH", PlayStatus: "PLAY_STATE"}
			}},
		{name: "paused", ago: 2 * time.Minute,
			setup: func(_ *Server, box *fakeUpdateResumeBox) {
				box.np = nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PAUSE_STATE"}
			}},
		{name: "AUX selected", ago: 2 * time.Minute,
			setup: func(_ *Server, box *fakeUpdateResumeBox) {
				box.np = nowPlayingSnapshot{Source: "AUX", PlayStatus: "STOP_STATE"}
			}},
		{name: "group member", ago: 2 * time.Minute,
			setup: func(_ *Server, box *fakeUpdateResumeBox) { box.follows = true }},
		{name: "something else was played since", ago: 2 * time.Minute,
			setup: func(s *Server, _ *fakeUpdateResumeBox) {
				s.play.record(&lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/5", ts: time.Now()})
			}},
		{name: "record gone", ago: 2 * time.Minute,
			setup: func(s *Server, _ *fakeUpdateResumeBox) { s.play.restore(nil, 0, time.Time{}) }},
		{name: "crash-loop guard tripped", ago: 2 * time.Minute,
			setup: func(s *Server, _ *fakeUpdateResumeBox) {
				s.play.resumeAttempts, s.play.lastResumeAt = maxAutoResumeAttempts, time.Now()
			}},
		{name: "speaker answers on the third read", ago: 2 * time.Minute, pushes: 1,
			setup: func(_ *Server, box *fakeUpdateResumeBox) { box.unreadable = 2 }},
		{name: "speaker never answers", ago: 2 * time.Minute,
			setup: func(_ *Server, box *fakeUpdateResumeBox) { box.unreadable = 1000 }},
		{name: "clock set after a few seconds", ago: 2 * time.Minute, pushes: 1,
			setup: func(_ *Server, box *fakeUpdateResumeBox) { box.clockBadFor = 3 }},
		{name: "clock never set", ago: 2 * time.Minute,
			setup: func(_ *Server, box *fakeUpdateResumeBox) { box.clockBadFor = 1 << 30 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, box := newUpdateResumeServer(t)
			armUpdateResume(t, s, box, tt.ago, 20)
			if tt.setup != nil {
				tt.setup(s, box)
			}
			if !s.ResumeAfterUpdate() {
				t.Fatal("ResumeAfterUpdate found no marker")
			}
			waitUpdateResume(t, s)
			if got := box.pushCount(); got != tt.pushes {
				t.Fatalf("pushes = %d, want %d", got, tt.pushes)
			}
			if tt.pushes == 0 && box.wakes != 0 {
				t.Fatal("a speaker that is not resumed must not be woken")
			}
			// Decided either way: the marker is used up.
			if markerExists(box) {
				t.Fatal("the marker is still on the NAND after the decision")
			}
			if s.ResumeAfterUpdate() {
				t.Fatal("the marker was not used up in memory")
			}
		})
	}
}

func TestUpdateResumeWithoutMarkerDoesNothing(t *testing.T) {
	t.Parallel()
	s, box := newUpdateResumeServer(t)
	s.loadUpdateResume()
	if s.ResumeAfterUpdate() {
		t.Fatal("no marker, yet a resume started")
	}
	if err := os.WriteFile(box.path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.loadUpdateResume()
	if s.ResumeAfterUpdate() || markerExists(box) {
		t.Fatal("a damaged marker must be dropped, not used")
	}
}

// The level the speaker had comes back when it restarted louder, and is never
// turned up when it restarted quieter.
func TestUpdateResumeVolumeIsOnlyEverLowered(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		before, after int
		want          []int
	}{
		{name: "came back louder", before: 20, after: 45, want: []int{20}},
		{name: "came back quieter", before: 40, after: 10},
		{name: "same level", before: 25, after: 25},
		{name: "level unknown at update time", before: -1, after: 60},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, box := newUpdateResumeServer(t)
			armUpdateResume(t, s, box, time.Minute, tt.before)
			box.vol = tt.after
			s.ResumeAfterUpdate()
			waitUpdateResume(t, s)
			if box.pushCount() != 1 {
				t.Fatalf("pushes = %d, want 1", box.pushCount())
			}
			if len(box.volSets) != len(tt.want) || (len(tt.want) == 1 && box.volSets[0] != tt.want[0]) {
				t.Fatalf("volume writes = %v, want %v", box.volSets, tt.want)
			}
		})
	}
}

// Right after a boot the renderer can refuse the first start. The marker is
// already used up by then, so the retries are bounded and in memory only.
func TestUpdateResumeRetriesTheStartBounded(t *testing.T) {
	t.Parallel()
	refused := errors.New("renderer not ready")

	s, box := newUpdateResumeServer(t)
	armUpdateResume(t, s, box, time.Minute, 20)
	box.pushErrs = []error{refused, refused}
	s.ResumeAfterUpdate()
	waitUpdateResume(t, s)
	if got := box.pushCount(); got != 3 {
		t.Fatalf("pushes = %d, want 3 (two refused, one taken)", got)
	}

	s, box = newUpdateResumeServer(t)
	armUpdateResume(t, s, box, time.Minute, 20)
	box.pushErrs = []error{refused, refused, refused, refused}
	s.ResumeAfterUpdate()
	waitUpdateResume(t, s)
	if got := box.pushCount(); got != productionUpdateResumeTiming.pushes {
		t.Fatalf("pushes = %d, want the bound %d", got, productionUpdateResumeTiming.pushes)
	}
	if markerExists(box) || s.ResumeAfterUpdate() {
		t.Fatal("a resume that could not start must not be tried again later")
	}
}

// An engine that could not be delivered before the restart arrives right
// after it. The resume stands back for the transfer and runs when it is over.
func TestUpdateResumeWaitsForAnEngineTransfer(t *testing.T) {
	t.Parallel()
	s, box := newUpdateResumeServer(t)
	armUpdateResume(t, s, box, time.Minute, 20)
	s.engineUploads.Add(1)
	s.ResumeAfterUpdate()
	waitUpdateResume(t, s)
	if box.pushCount() != 0 {
		t.Fatal("the resume started the stream into a running engine transfer")
	}
	if !markerExists(box) {
		t.Fatal("a resume that stood back must keep its marker for a boot that ends before it runs")
	}
	// What installEngineUpload does when the transfer is over.
	s.engineUploads.Add(-1)
	s.afterEngineUpload()
	waitUpdateResume(t, s)
	if box.pushCount() != 1 {
		t.Fatalf("pushes after the transfer = %d, want 1", box.pushCount())
	}
	if markerExists(box) {
		t.Fatal("the marker outlived the resume")
	}
}

// The other order: the speaker already carries on when the engine arrives,
// and the transfer quietens it again. It gets its music back once, and only
// when nobody stopped it meanwhile.
func TestUpdateResumeComesBackAfterTheEngineDelivery(t *testing.T) {
	t.Parallel()
	resumed := func(t *testing.T) (*Server, *fakeUpdateResumeBox) {
		s, box := newUpdateResumeServer(t)
		armUpdateResume(t, s, box, time.Minute, 20)
		s.ResumeAfterUpdate()
		waitUpdateResume(t, s)
		if box.pushCount() != 1 {
			t.Fatalf("pushes = %d, want 1", box.pushCount())
		}
		// the engine transfer starts and stops the stream
		box.fx().sleep(time.Microsecond)
		withNowPlaying(s, nowPlayingSnapshot{Source: "UPNP", Location: updateResumeTestURL, PlayStatus: "PLAY_STATE"})
		withStopSeam(s, &stopCounter{})
		s.hushForUpload("sidecar")
		return s, box
	}

	t.Run("comes back once", func(t *testing.T) {
		t.Parallel()
		s, box := resumed(t)
		s.afterEngineUpload()
		waitUpdateResume(t, s)
		if box.pushCount() != 2 {
			t.Fatalf("pushes = %d, want 2", box.pushCount())
		}
		// A later engine delivery is an ordinary one again.
		s.hushForUpload("sidecar")
		s.afterEngineUpload()
		waitUpdateResume(t, s)
		if box.pushCount() != 2 {
			t.Fatalf("pushes = %d, a later delivery must not start the speaker", box.pushCount())
		}
	})

	t.Run("not after a stop", func(t *testing.T) {
		t.Parallel()
		s, box := resumed(t)
		s.NoteExplicitStop()
		s.afterEngineUpload()
		waitUpdateResume(t, s)
		if box.pushCount() != 1 {
			t.Fatalf("pushes = %d, a stopped speaker was started again", box.pushCount())
		}
	})

	t.Run("an engine delivery without an update changes nothing", func(t *testing.T) {
		t.Parallel()
		s, box := newUpdateResumeServer(t)
		withNowPlaying(s, nowPlayingSnapshot{Source: "UPNP", Location: updateResumeTestURL, PlayStatus: "PLAY_STATE"})
		withStopSeam(s, &stopCounter{})
		s.hushForUpload("sidecar")
		s.afterEngineUpload()
		waitUpdateResume(t, s)
		if box.pushCount() != 0 {
			t.Fatal("a plain engine delivery started the speaker")
		}
	})
}

// The real push: the play on record goes out through the renderer the
// power-on resume uses, also with the power-on setting switched off.
func TestUpdateResumePushesTheRecordThroughTheRenderer(t *testing.T) {
	t.Parallel()
	s, rec := resumeTestServer(t, `<nowPlaying source="STANDBY"></nowPlaying>`)
	if err := os.WriteFile(s.resumeOnPowerOnPath, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.updateResumePath = filepath.Join(t.TempDir(), "resume-after-update")
	tm := productionUpdateResumeTiming
	tm.settle, tm.volumeSettle = 10*time.Millisecond, 10*time.Millisecond
	s.updateResumeTiming = &tm
	lp, _ := s.play.snapshot()
	b, _ := json.Marshal(updateResumeMarker{BoxURL: lp.boxURL, PlayTS: lp.ts, Volume: -1, At: time.Now().Add(-time.Minute)})
	if err := os.WriteFile(s.updateResumePath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	s.loadUpdateResume()

	// The reconnect hook is what starts it, and the ordinary recovery (off
	// with this setting) stays out of it.
	s.RecoverAfterReconnect()
	waitUpdateResume(t, s)
	if !waitForSOAP(rec, time.Second) {
		t.Fatal("nothing was pushed to the speaker")
	}
	got := strings.Join(rec.list(), ",")
	if !strings.Contains(got, "SetAVTransportURI") || !strings.Contains(got, "Play") {
		t.Fatalf("SOAP actions = %s, want the station set and played", got)
	}
	if _, err := os.Stat(s.updateResumePath); !os.IsNotExist(err) {
		t.Fatalf("marker still there, stat err = %v", err)
	}
}

// nativeWakeServer is a restarted agent whose speaker was playing a native
// radio preset: on waking, the firmware announces that preset again, which
// records the same station as a fresh play (NoteNativeLastPlay). Everything
// else is the production path, renderer included.
func nativeWakeServer(t *testing.T) (*Server, *soapRecorder) {
	t.Helper()
	s, rec := resumeTestServer(t, `<nowPlaying source="STANDBY"></nowPlaying>`)
	s.updateResumePath = filepath.Join(t.TempDir(), "resume-after-update")
	tm := productionUpdateResumeTiming
	tm.settle, tm.volumeSettle, tm.selfStartGap = 5*time.Millisecond, 5*time.Millisecond, 5*time.Millisecond
	s.updateResumeTiming = &tm
	// Started three minutes ago, as in the field run: an announcement within
	// seconds of STR's own play would count as its echo and change nothing.
	s.play.mu.Lock()
	s.play.last.ts = time.Now().Add(-3 * time.Minute)
	s.play.mu.Unlock()
	lp, _ := s.play.snapshot()
	b, _ := json.Marshal(updateResumeMarker{BoxURL: lp.boxURL, PlayTS: lp.ts, Volume: -1, At: time.Now().Add(-2 * time.Minute)})
	if err := os.WriteFile(s.updateResumePath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	s.loadUpdateResume()
	fx := *s.updateResumeEffects()
	fx.wake = func() { s.NoteNativeLastPlay(lp.boxURL, lp.title, lp.art, lp.mime) }
	s.updateResumeFx = &fx
	return s, rec
}

// Field test of 2026-11-18, Portable: a native radio preset played, the
// update restarted the speaker, and the resume gave up with "something else
// was played since the update" although nothing else had been. The waking
// firmware had announced the same preset, the record got a new timestamp, and
// the timestamp comparison read that as a newer choice. The speaker stayed on
// INVALID_SOURCE, silent.
func TestUpdateResumeSurvivesTheFirmwareAnnouncingTheSameStation(t *testing.T) {
	t.Parallel()
	s, rec := nativeWakeServer(t)
	before, _ := s.play.snapshot()
	s.ResumeAfterUpdate()
	waitUpdateResume(t, s)
	after, _ := s.play.snapshot()
	if after.ts.Equal(before.ts) || !after.fromBox {
		t.Fatal("test setup: the wake did not re-record the station as the firmware's own")
	}
	if !waitForSOAP(rec, time.Second) {
		t.Fatal("the same station announced again by the waking firmware called the resume off")
	}
	if got := strings.Join(rec.list(), ","); !strings.Contains(got, "SetAVTransportURI") {
		t.Fatalf("SOAP actions = %s, want the station pushed", got)
	}
}

// When the firmware does start the announced station by itself, nothing is
// pushed on top of it.
func TestUpdateResumeLeavesAFirmwareSelfStartAlone(t *testing.T) {
	t.Parallel()
	s, rec := nativeWakeServer(t)
	s.playStateFn = func() (standby, busy bool) { return false, true }
	s.ResumeAfterUpdate()
	waitUpdateResume(t, s)
	if waitForSOAP(rec, 100*time.Millisecond) {
		t.Fatalf("pushed onto a station the speaker had started itself: %v", rec.list())
	}
	if _, err := os.Stat(s.updateResumePath); !os.IsNotExist(err) {
		t.Fatalf("marker still there, stat err = %v", err)
	}
}

func TestUpdateResumeSupersededIsAboutTheStation(t *testing.T) {
	t.Parallel()
	ts := time.Now()
	m := updateResumeMarker{BoxURL: updateResumeTestURL, PlayTS: ts}
	if updateResumeSuperseded(m, &lastPlayInfo{boxURL: updateResumeTestURL, ts: ts.Add(3 * time.Minute), fromBox: true}) {
		t.Fatal("the same station recorded again counts as superseded")
	}
	if !updateResumeSuperseded(m, &lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/5", ts: ts}) {
		t.Fatal("another station does not count as superseded")
	}
	if !updateResumeSuperseded(m, nil) {
		t.Fatal("no record does not count as superseded")
	}
}

// A stop asked for after the restart is the user's last word, also when the
// station on record is still the same.
func TestUpdateResumeNotAfterAStopSinceTheRestart(t *testing.T) {
	t.Parallel()
	s, box := newUpdateResumeServer(t)
	armUpdateResume(t, s, box, time.Minute, 20)
	s.NoteExplicitStop()
	s.ResumeAfterUpdate()
	waitUpdateResume(t, s)
	if box.pushCount() != 0 || box.wakes != 0 {
		t.Fatal("a speaker the user stopped after the restart was started again")
	}
}

// The Portable of the field test played at 5 and restarted at 30. The level
// must be back down before the speaker is woken, not after the first notes.
func TestUpdateResumeTurnsDownBeforeTheWake(t *testing.T) {
	t.Parallel()
	s, box := newUpdateResumeServer(t)
	armUpdateResume(t, s, box, time.Minute, 5)
	box.vol = 30
	var order []string
	var mu sync.Mutex
	note := func(what string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, what)
	}
	fx := *s.updateResumeFx
	setVolume, wake, push := fx.setVolume, fx.wake, fx.push
	fx.setVolume = func(v int) error { note("volume"); return setVolume(v) }
	fx.wake = func() { note("wake"); wake() }
	fx.push = func(m updateResumeMarker) (bool, error) { note("push"); return push(m) }
	s.updateResumeFx = &fx
	s.ResumeAfterUpdate()
	waitUpdateResume(t, s)
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(order, ","); got != "volume,wake,push" {
		t.Fatalf("order = %s, want volume,wake,push", got)
	}
	if len(box.volSets) != 1 || box.volSets[0] != 5 {
		t.Fatalf("volume writes = %v, want [5]", box.volSets)
	}
}

// Second field run, same Portable: the level was back at 5 before the wake,
// and the WAKE put it at 30 again, because the firmware applies its own
// power-on volume when it leaves standby. The station then started at 30.
// Audio must never start above the level the speaker had: the level is
// brought down after the wake and before the push, which is also before a
// firmware that starts the station by itself (detected inside the push) is
// left to play.
func TestUpdateResumeTurnsDownAgainAfterTheWake(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// lateReads is how many level reads after the wake still show the
		// old level before the firmware applies its own.
		lateReads int
		wantOrder string
	}{
		{name: "the wake resets the level at once", wantOrder: "volume,wake,volume,push"},
		{name: "the firmware applies its level a moment after the wake", lateReads: 1, wantOrder: "volume,wake,volume,push"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, box := newUpdateResumeServer(t)
			armUpdateResume(t, s, box, time.Minute, 5)
			box.vol = 30
			var (
				mu         sync.Mutex
				order      []string
				woken      bool
				readsSince int
				reset      bool
				volAtPush  = -1
			)
			fx := *s.updateResumeFx
			volume, setVolume, wake, push := fx.volume, fx.setVolume, fx.wake, fx.push
			fx.volume = func() (int, bool) {
				mu.Lock()
				if woken && !reset {
					if readsSince >= tt.lateReads {
						reset = true
						box.mu.Lock()
						box.vol = 30 // the firmware's power-on level
						box.mu.Unlock()
					}
					readsSince++
				}
				mu.Unlock()
				return volume()
			}
			fx.setVolume = func(v int) error {
				mu.Lock()
				order = append(order, "volume")
				mu.Unlock()
				return setVolume(v)
			}
			fx.wake = func() {
				mu.Lock()
				order = append(order, "wake")
				woken = true
				mu.Unlock()
				wake()
			}
			fx.push = func(m updateResumeMarker) (bool, error) {
				v, _ := volume()
				mu.Lock()
				order = append(order, "push")
				volAtPush = v
				mu.Unlock()
				return push(m)
			}
			s.updateResumeFx = &fx
			s.ResumeAfterUpdate()
			waitUpdateResume(t, s)
			mu.Lock()
			defer mu.Unlock()
			if !reset {
				t.Fatal("test setup: the firmware never applied its power-on level")
			}
			if volAtPush != 5 {
				t.Fatalf("level when the stream was pushed = %d, want 5 (order %v)", volAtPush, order)
			}
			if got := strings.Join(order, ","); got != tt.wantOrder {
				t.Fatalf("order = %s, want %s", got, tt.wantOrder)
			}
			box.mu.Lock()
			defer box.mu.Unlock()
			if box.vol != 5 {
				t.Fatalf("level at the end = %d, want 5", box.vol)
			}
		})
	}
}

// A level that cannot be read after the wake must not hold the resume up
// for long, and nothing is written blind.
func TestUpdateResumeUnreadableLevelAfterTheWakeIsBounded(t *testing.T) {
	t.Parallel()
	s, box := newUpdateResumeServer(t)
	armUpdateResume(t, s, box, time.Minute, 5)
	box.volOK = false
	s.ResumeAfterUpdate()
	waitUpdateResume(t, s)
	if box.pushCount() != 1 {
		t.Fatalf("pushes = %d, want 1", box.pushCount())
	}
	if len(box.volSets) != 0 {
		t.Fatalf("volume writes without a reading: %v", box.volSets)
	}
}

// Field case 2026-11-26, ST30 main speaker of a permanent group: a native
// station on key 6 played through the agent's own stream proxy, the update
// restarted the agent, and the speaker played out its buffer for almost a
// minute. The restarted agent read PLAY_STATE 27 s after the old one exited,
// called it "already playing", and the speaker fell silent 22 s later; the
// station never came back on the group. A speaker that names this agent's
// proxy while the proxy has served nothing since the start is started again.
func TestUpdateResumeStartsAgainWhatOnlyTheOldAgentsBufferPlays(t *testing.T) {
	t.Parallel()
	own := OrionStationLocation("http://127.0.0.1:8888/stream/6", "Absolut Relax", "")
	for _, tc := range []struct {
		name     string
		np       nowPlayingSnapshot
		feedLost bool
		pushes   int
	}{
		{"native station, the new proxy served nothing", nowPlayingSnapshot{Source: nativeRadioSource, Location: own, PlayStatus: "PLAY_STATE"}, true, 1},
		{"native station, the new proxy serves it", nowPlayingSnapshot{Source: nativeRadioSource, Location: own, PlayStatus: "PLAY_STATE"}, false, 0},
		{"UPnP push of a key, the new proxy served nothing", nowPlayingSnapshot{Source: "UPNP", Location: "http://127.0.0.1:8888/stream/2", PlayStatus: "PLAY_STATE"}, true, 1},
		{"paused is never a buffer to replace", nowPlayingSnapshot{Source: "UPNP", Location: "http://127.0.0.1:8888/stream/2", PlayStatus: "PAUSE_STATE"}, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, box := newUpdateResumeServer(t)
			armUpdateResume(t, s, box, 2*time.Minute, 30)
			box.np = tc.np
			box.vol = 30
			feedLost := tc.feedLost
			s.updateResumeFx.feedLost = func(nowPlayingSnapshot) bool { return feedLost }
			s.ResumeAfterUpdate()
			waitUpdateResume(t, s)
			if got := box.pushCount(); got != tc.pushes {
				t.Fatalf("pushes = %d, want %d", got, tc.pushes)
			}
		})
	}
}

// The same case through the production signal and push: the speaker reports
// it plays STR's key 6 (so every "busy" read says yes), the stream proxy has
// served nothing since the restart, and the station is pushed anyway. Once the
// proxy serves the speaker, a busy speaker is left to itself as before.
func TestUpdateResumeFeedLostReadsTheStreamProxy(t *testing.T) {
	t.Parallel()
	own := OrionStationLocation("http://127.0.0.1:8888/stream/6", "Absolut Relax", "")
	doc := `<nowPlaying source="LOCAL_INTERNET_RADIO"><ContentItem source="LOCAL_INTERNET_RADIO" location="` + own + `"/>` +
		`<playStatus>PLAY_STATE</playStatus></nowPlaying>`
	for _, tc := range []struct {
		name      string
		lastFetch time.Time
		wantPush  bool
	}{
		{"nothing served since the start", time.Time{}, true},
		{"the speaker reconnected to the new proxy", time.Now(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, rec := resumeTestServer(t, doc)
			s.playStateFn = func() (standby, busy bool) { return false, true }
			fetch := tc.lastFetch
			s.SetStreamActivityFn(func() (time.Time, time.Time) { return fetch, time.Time{} })
			s.updateResumePath = filepath.Join(t.TempDir(), "resume-after-update")
			tm := productionUpdateResumeTiming
			tm.settle, tm.volumeSettle, tm.selfStartGap = 5*time.Millisecond, 5*time.Millisecond, 5*time.Millisecond
			s.updateResumeTiming = &tm
			lp, _ := s.play.snapshot()
			b, _ := json.Marshal(updateResumeMarker{BoxURL: lp.boxURL, PlayTS: lp.ts, Volume: -1, At: time.Now().Add(-time.Minute)})
			if err := os.WriteFile(s.updateResumePath, b, 0o644); err != nil {
				t.Fatal(err)
			}
			s.loadUpdateResume()
			s.ResumeAfterUpdate()
			waitUpdateResume(t, s)
			if got := waitForSOAP(rec, 300*time.Millisecond); got != tc.wantPush {
				t.Fatalf("pushed = %v, want %v (SOAP %v)", got, tc.wantPush, rec.list())
			}
		})
	}
}

func TestPlaysOwnProxyStream(t *testing.T) {
	t.Parallel()
	for loc, want := range map[string]bool{
		OrionStationLocation("http://127.0.0.1:8888/stream/6", "X", ""):     true,
		"http://127.0.0.1:8888/stream/2":                                    true,
		OrionStationLocation("https://radio.example.com/live.mp3", "X", ""): false,
		"http://192.0.2.50:8095/stream/flow.mp3":                            false,
		"":                                                                  false,
		"http://127.0.0.1:8888/spotify/stream.ogg":                          false,
	} {
		if got := playsOwnProxyStream(loc); got != want {
			t.Errorf("playsOwnProxyStream(%q) = %v, want %v", loc, got, want)
		}
	}
}
