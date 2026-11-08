package webui

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func startVolServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		startVolumePath: filepath.Join(t.TempDir(), "start-volume"),
	}
}

func postStartVol(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/box/start-volume", strings.NewReader(body))
	r.RemoteAddr = "192.168.1.20:5555"
	w := httptest.NewRecorder()
	s.handleStartVolume(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// Off by default. A speaker that quietly changes its own volume is worse than
// one that forgets, so nothing happens until the owner asks for it.
func TestStartVolumeIsOffUntilItIsSet(t *testing.T) {
	s := startVolServer(t)
	if _, ok := s.startVolume(); ok {
		t.Error("no file must mean off")
	}
	r := httptest.NewRequest(http.MethodGet, "/api/box/start-volume", nil)
	r.RemoteAddr = "192.168.1.20:5555"
	w := httptest.NewRecorder()
	s.handleStartVolume(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["volume"] != float64(0) {
		t.Errorf("off must read as 0, got %v", out["volume"])
	}
}

func TestStartVolumeRoundTrips(t *testing.T) {
	s := startVolServer(t)
	if code, _ := postStartVol(t, s, `{"volume":35}`); code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	v, ok := s.startVolume()
	if !ok || v != 35 {
		t.Errorf("stored level = %d ok=%v, want 35 true", v, ok)
	}
}

// Zero is the honest way to store "off" and must turn it back off.
func TestStartVolumeZeroTurnsItOff(t *testing.T) {
	s := startVolServer(t)
	postStartVol(t, s, `{"volume":40}`)
	postStartVol(t, s, `{"volume":0}`)
	if v, ok := s.startVolume(); ok {
		t.Errorf("0 must mean off, got %d", v)
	}
}

func TestStartVolumeRejectsOutOfRange(t *testing.T) {
	s := startVolServer(t)
	for _, body := range []string{`{"volume":-1}`, `{"volume":101}`} {
		if code, _ := postStartVol(t, s, body); code != http.StatusBadRequest {
			t.Errorf("%s should be refused, got %d", body, code)
		}
	}
}

// A damaged file is not an instruction. Reading garbage must fall back to off
// rather than setting the speaker to something arbitrary.
func TestStartVolumeIgnoresADamagedFile(t *testing.T) {
	s := startVolServer(t)
	for _, junk := range []string{"", "  ", "loud", "999", "-5"} {
		if err := os.WriteFile(s.startVolumePath, []byte(junk), 0o644); err != nil {
			t.Fatal(err)
		}
		if v, ok := s.startVolume(); ok {
			t.Errorf("%q should read as off, got %d", junk, v)
		}
	}
}

// A broken or missing body must not decode to volume 0 and quietly switch the
// start level off. It is refused, and the stored level stays what it was.
func TestStartVolumeRejectsABrokenBody(t *testing.T) {
	s := startVolServer(t)
	postStartVol(t, s, `{"volume":40}`)
	for _, body := range []string{`{"volume":`, `volume=40`, ``} {
		if code, _ := postStartVol(t, s, body); code != http.StatusBadRequest {
			t.Errorf("%q should be refused, got %d", body, code)
		}
	}
	if v, ok := s.startVolume(); !ok || v != 40 {
		t.Errorf("stored level after refused posts = %d ok=%v, want 40 true", v, ok)
	}
}

func TestStartVolumeIsLANOnly(t *testing.T) {
	s := startVolServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/box/start-volume", strings.NewReader(`{"volume":30}`))
	r.RemoteAddr = "203.0.113.9:5555"
	w := httptest.NewRecorder()
	s.handleStartVolume(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 off-LAN, got %d", w.Code)
	}
}

// It must only ever run on the automatic resume. A level the user chose while
// the music plays is theirs, and a second caller here would start fighting
// their volume knob.
func TestStartVolumeIsOnlyAppliedOnTheWakeResume(t *testing.T) {
	src, err := os.ReadFile("resume_standby.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "s.applyStartVolume("); n != 1 {
		t.Errorf("applyStartVolume should be called once from the wake resume, found %d calls", n)
	}
}

// powerOnServer has a start level of 20, a play state the test controls and a
// volume setter that records what it was asked.
func powerOnServer(t *testing.T, playing *atomic.Bool) (*Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	s := startVolServer(t)
	if err := os.WriteFile(s.startVolumePath, []byte("20"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls, last atomic.Int32
	s.playStateFn = func() (bool, bool) { return !playing.Load(), playing.Load() }
	s.setVolumeFn = func(_ context.Context, v int) error {
		calls.Add(1)
		last.Store(int32(v))
		return nil
	}
	s.startVolumeTiming = &startVolumeTiming{watchFor: 400 * time.Millisecond, watchPoll: 20 * time.Millisecond}
	return s, &calls, &last
}

func waitForStartVol(cond func() bool, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// The Portable resumes its station by itself on a power-on with the remote;
// the start level must apply then too.
func TestStartVolumeAppliesWhenTheSpeakerResumesByItself(t *testing.T) {
	t.Parallel()
	var playing atomic.Bool
	s, calls, last := powerOnServer(t, &playing)
	s.applyStartVolumeAfterPowerOn()
	time.Sleep(60 * time.Millisecond)
	playing.Store(true)
	if !waitForStartVol(func() bool { return calls.Load() == 1 }, startVolumeApplyDelay+2*time.Second) || last.Load() != 20 {
		t.Fatalf("start level set %d times, last %d; want once, 20", calls.Load(), last.Load())
	}
}

func TestStartVolumeLeavesASilentSpeakerAlone(t *testing.T) {
	t.Parallel()
	var playing atomic.Bool
	s, calls, _ := powerOnServer(t, &playing)
	s.applyStartVolumeAfterPowerOn()
	time.Sleep(s.startVolumeTimes().watchFor + startVolumeApplyDelay + 300*time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("a speaker that never plays must not get a level, got %d writes", calls.Load())
	}
}

// STR's own resume and the power-on watch both see the same start: one write.
func TestStartVolumeOncePerPowerOn(t *testing.T) {
	t.Parallel()
	var playing atomic.Bool
	playing.Store(true)
	s, calls, _ := powerOnServer(t, &playing)
	s.applyStartVolumeAfterPowerOn()
	s.applyStartVolume("wake resume")
	time.Sleep(startVolumeApplyDelay + 600*time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("one power-on must set the level once, got %d", calls.Load())
	}
}

// #1774: a preset picked in the app for a speaker in standby makes STR wake it
// itself, and the wake's standby exit is the very edge the power-on watch
// listens on. The play is the user's and the wake puts their level back, so
// the start level must never land on top of it.
func TestStartVolumeStandsDownForAWokenPresetRecall(t *testing.T) {
	t.Parallel()
	box := &fakeWakingBox{level: 12}
	s := newWakingServer(t, box)
	s.startVolumePath = filepath.Join(t.TempDir(), "start-volume")
	if err := os.WriteFile(s.startVolumePath, []byte("71"), 0o644); err != nil {
		t.Fatal(err)
	}
	var startWrites atomic.Int32
	s.setVolumeFn = func(context.Context, int) error {
		startWrites.Add(1)
		return nil
	}
	s.playStateFn = func() (bool, bool) {
		np := box.state()
		return np.Source == "STANDBY", np.Source != "STANDBY"
	}
	s.startVolumeTiming = &startVolumeTiming{watchFor: 400 * time.Millisecond, watchPoll: 20 * time.Millisecond}
	wake := s.newPlayWakeFn
	s.newPlayWakeFn = func(ctx context.Context) error {
		err := wake(ctx)
		// the gabbo standby exit arrives while the wake is still waiting
		s.RunDeferredResume()
		return err
	}

	playSent := s.ensureBoxReadyForNewPlay(context.Background(), nowPlayingSnapshot{Source: "STANDBY"}, radioTarget)
	box.play(nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", ItemName: "Preset 2", Location: "new-loc", PlayStatus: "PLAY_STATE"})
	playSent(true)
	waitForLevel(t, box, 12)
	time.Sleep(s.startVolumeTimes().watchFor + startVolumeApplyDelay + 300*time.Millisecond)
	if n := startWrites.Load(); n != 0 {
		t.Fatalf("the start level was written %d times over a woken preset recall, want never", n)
	}
	box.mu.Lock()
	lvl := box.level
	box.mu.Unlock()
	if lvl != 12 {
		t.Fatalf("level %d after the recall, want the user's 12", lvl)
	}
}

// A wake for a play that starts while the watch already runs stands it down
// too: the next look sees the wake's episode and leaves the level alone.
func TestStartVolumeWatchStandsDownWhenAWakeForAPlayStarts(t *testing.T) {
	t.Parallel()
	var playing atomic.Bool
	s, calls, _ := powerOnServer(t, &playing)
	s.applyStartVolumeAfterPowerOn()
	s.groupWake.noteEpisode(time.Minute)
	playing.Store(true)
	time.Sleep(s.startVolumeTimes().watchFor + startVolumeApplyDelay + 300*time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("a wake for a play must keep the start level away, got %d writes", calls.Load())
	}
}

// The genuine power-on still gets its start level: a standby exit with no
// wake of STR's in flight, and the speaker resuming by itself.
func TestStartVolumeStillAppliesOnAPlainPowerOn(t *testing.T) {
	t.Parallel()
	var playing atomic.Bool
	s, calls, last := powerOnServer(t, &playing)
	s.RunDeferredResume()
	time.Sleep(60 * time.Millisecond)
	playing.Store(true)
	if !waitForStartVol(func() bool { return calls.Load() == 1 }, startVolumeApplyDelay+2*time.Second) || last.Load() != 20 {
		t.Fatalf("start level set %d times, last %d; want once, 20", calls.Load(), last.Load())
	}
}
