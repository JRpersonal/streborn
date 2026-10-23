package webui

import (
	"testing"
	"time"
)

// #1077: the decision table of the stuck-buffering rescue.
func TestStuckBufNext(t *testing.T) {
	cases := []struct {
		name                                   string
		afterRecall, standby, busy, buf, acted bool
		want                                   stuckBufStep
	}{
		{"still buffering: recall once", false, false, true, true, false, stuckBufRecall},
		{"still buffering after the recall: stop", true, false, true, true, false, stuckBufStop},
		{"playing again: leave", false, false, true, false, false, stuckBufLeave},
		{"playing after the recall: leave", true, false, true, false, false, stuckBufLeave},
		{"idle: the regular watchdog owns it", false, false, false, false, false, stuckBufLeave},
		{"standby: never wake it", false, true, false, false, false, stuckBufStandDown},
		{"standby beats buffering", true, true, true, true, false, stuckBufStandDown},
		{"user acted: hands off", false, false, true, true, true, stuckBufStandDown},
		{"user acted after the recall: hands off", true, false, true, true, true, stuckBufStandDown},
	}
	for _, c := range cases {
		if got := stuckBufNext(c.afterRecall, c.standby, c.busy, c.buf, c.acted); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestStuckBufEpisodeOpen(t *testing.T) {
	now := time.Now()
	if !stuckBufEpisodeOpen(time.Time{}, time.Time{}) {
		t.Error("no rescue yet must allow one")
	}
	if stuckBufEpisodeOpen(now, now.Add(-time.Minute)) {
		t.Error("a second rescue without a new key press must not run")
	}
	if !stuckBufEpisodeOpen(now, now.Add(time.Second)) {
		t.Error("a key press after the last rescue must open a new episode")
	}
}

// stuckBufServer is a Server whose box keeps answering with the given states,
// one per probe (the last one repeats), on a Spotify slot stream.
func stuckBufServer(t *testing.T, states ...[3]bool) (*Server, *soapRecorder) {
	t.Helper()
	s, rec := newPlayTestServer(t)
	s.displayMsg.path = t.TempDir() + "/display-messages"
	s.stuckBufWait = 10 * time.Millisecond
	s.boxNowReadFn = scriptNowPlaying("UPNP", "", "")
	i := 0
	s.playStateDetailFn = func() (bool, bool, bool) {
		st := states[min(i, len(states)-1)]
		i++
		return st[0], st[1], st[2]
	}
	s.lastPlayMu.Lock()
	s.lastPlay = &lastPlayInfo{boxURL: "http://127.0.0.1:8888/spotify/stream-4.ogg", title: "Key 4", ts: time.Now()}
	s.lastPlayMu.Unlock()
	return s, rec
}

var (
	stBuffering = [3]bool{false, true, true}
	stPlaying   = [3]bool{false, true, false}
	stStandby   = [3]bool{true, false, false}
)

// The field case end to end: buffering at the re-check, the recall (here a
// slot with no preset, so it is refused without touching the box) does not
// help, so the transport is stopped once and the stream is marked given up.
func TestStuckBufferingStopsAfterOneFailedRecall(t *testing.T) {
	s, rec := stuckBufServer(t, stBuffering)
	s.rescueStuckSpotifyBuffering()
	if !rec.has("Stop") {
		t.Fatalf("a speaker stuck buffering after the rescue recall was left loading; SOAP: %v", rec.list())
	}
	s.lastPlayMu.Lock()
	failed := s.lastPlay != nil && s.lastPlay.failed
	at := s.stuckBufRescueAt
	s.lastPlayMu.Unlock()
	if !failed {
		t.Error("the stream was not marked given up, a later detach would re-arm it")
	}
	if at.IsZero() {
		t.Error("the rescue was not recorded, the episode gate cannot hold")
	}

	// A second detach in the same episode must not rescue again.
	n := rec.count()
	s.rescueStuckSpotifyBuffering()
	if rec.count() != n {
		t.Errorf("a second rescue ran without a new key press: %v", rec.list())
	}
}

func TestStuckBufferingStandsDownOnStandby(t *testing.T) {
	s, rec := stuckBufServer(t, stStandby)
	s.rescueStuckSpotifyBuffering()
	if rec.count() != 0 {
		t.Errorf("a speaker in standby got transport commands: %v", rec.list())
	}
	s.lastPlayMu.Lock()
	at := s.stuckBufRescueAt
	s.lastPlayMu.Unlock()
	if !at.IsZero() {
		t.Error("a stand-down spent the episode's one rescue")
	}
}

func TestStuckBufferingLeavesARecoveredSpeaker(t *testing.T) {
	s, rec := stuckBufServer(t, stPlaying)
	s.rescueStuckSpotifyBuffering()
	if rec.count() != 0 {
		t.Errorf("a speaker that started playing by itself got transport commands: %v", rec.list())
	}
}

func TestStuckBufferingStandsDownOnAUserSource(t *testing.T) {
	s, rec := stuckBufServer(t, stBuffering)
	s.boxNowReadFn = scriptNowPlaying("BLUETOOTH", "", "")
	s.rescueStuckSpotifyBuffering()
	if rec.count() != 0 {
		t.Errorf("the rescue fought a source the user picked: %v", rec.list())
	}
}

// Radio and app-driven Spotify sessions are not this rescue's business.
func TestStuckBufferingIgnoresNonPresetStreams(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:8888/stream/2", "http://127.0.0.1:8888/spotify/stream.ogg"} {
		s, rec := stuckBufServer(t, stBuffering)
		s.lastPlayMu.Lock()
		s.lastPlay.boxURL = u
		s.lastPlayMu.Unlock()
		s.rescueStuckSpotifyBuffering()
		if rec.count() != 0 {
			t.Errorf("%s: rescue acted on a stream that is not a Spotify key: %v", u, rec.list())
		}
	}
}
