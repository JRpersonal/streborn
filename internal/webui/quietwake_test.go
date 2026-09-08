package webui

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"testing"
	"time"
)

// A quiet wake exists to keep a SLEEPING speaker quiet while it comes up. The
// app asks for it on the full desired member list of a group edit, so it must
// be a no-op on the members that are already playing.
func TestQuietWakeNeededOnlyForASleepingSpeaker(t *testing.T) {
	cases := []struct {
		name string
		np   nowPlayingSnapshot
		want bool
	}{
		{"asleep", nowPlayingSnapshot{Source: "STANDBY"}, true},
		{"unreadable state", nowPlayingSnapshot{}, true},
		{"playing a station", nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "PLAY_STATE"}, false},
		{"idle but awake", nowPlayingSnapshot{Source: "STORED_MUSIC", PlayStatus: "STOP_STATE"}, false},
		{"playing in the group being edited", nowPlayingSnapshot{Source: "SPOTIFY", PlayStatus: "BUFFERING_STATE"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quietWakeNeeded(tc.np); got != tc.want {
				t.Fatalf("quietWakeNeeded(%+v) = %v, want %v", tc.np, got, tc.want)
			}
		})
	}
}

// The gate has to short-circuit before ANY write reaches the speaker: adding one
// member to a playing group used to mute every other member to 0 and send it a
// STOP. boxHost is a TEST-NET-1 address, so a call that did go out would take
// seconds and fail; returning fast and clean is the proof it went nowhere.
func TestQuietWakeLeavesAnAwakeSpeakerUntouched(t *testing.T) {
	prev := quietWakeNowPlaying
	t.Cleanup(func() { quietWakeNowPlaying = prev })
	reads := 0
	quietWakeNowPlaying = func(context.Context, string) nowPlayingSnapshot {
		reads++
		return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", PlayStatus: "PLAY_STATE"}
	}
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10"}
	start := time.Now()
	if err := s.quietWake(context.Background()); err != nil {
		t.Fatalf("quietWake on an awake speaker: %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("quietWake took %s: it drove the speaker instead of standing down", el)
	}
	if reads != 1 {
		t.Fatalf("read the state %d times, want the single gate read", reads)
	}
	s.quietWakeMu.Lock()
	armed := !s.quietWakeUntil.IsZero()
	s.quietWakeMu.Unlock()
	if armed {
		t.Fatal("armed a level restore for a speaker it never muted")
	}
}

// boxHost is empty on purpose in the arming tests: the restore then changes the
// bookkeeping and stops, so no timer of a finished test reaches a speaker.
func quietWakeTestServer() *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// The app races each wake against 4 s and retries, while a real standby wake
// needs longer, so a second quiet wake inside the window is the normal case.
// It must not snapshot the mute the first one wrote.
func TestArmQuietWakeRestoreKeepsTheFirstLevel(t *testing.T) {
	s := quietWakeTestServer()
	s.armQuietWakeRestore(30)
	s.quietWakeMu.Lock()
	firstUntil := s.quietWakeUntil
	s.quietWakeMu.Unlock()

	time.Sleep(5 * time.Millisecond)
	s.armQuietWakeRestore(0) // the retry reads the level the first wake muted

	s.quietWakeMu.Lock()
	vol, until := s.quietWakeVol, s.quietWakeUntil
	s.quietWakeMu.Unlock()
	if vol != 30 {
		t.Fatalf("armed level = %d, want the 30 the first wake muted from", vol)
	}
	if !until.After(firstUntil) {
		t.Fatalf("deadline %s did not move past %s", until, firstUntil)
	}
}

// The first wake's 20 s timer fires while the second wake still holds the
// speaker muted. It must leave the arming alone; the later timer restores.
func TestEarlierQuietWakeTimeoutDoesNotUnmute(t *testing.T) {
	s := quietWakeTestServer()
	s.armQuietWakeRestore(30)
	time.Sleep(5 * time.Millisecond)
	s.armQuietWakeRestore(0)

	s.restoreQuietWakeVolume("timeout") // the earlier wake's timer

	s.quietWakeMu.Lock()
	vol, armed := s.quietWakeVol, !s.quietWakeUntil.IsZero()
	s.quietWakeMu.Unlock()
	if !armed {
		t.Fatal("the earlier timer disarmed a restore that a later quiet wake still owns")
	}
	if vol != 30 {
		t.Fatalf("armed level = %d, want 30", vol)
	}
}

// A zone join is not a deadline: the member is in the group, so the level comes
// back at once even though the window is still open.
func TestZoneJoinRestoresInsideTheWindow(t *testing.T) {
	s := quietWakeTestServer()
	s.armQuietWakeRestore(30)

	s.restoreQuietWakeVolume("zone joined")

	s.quietWakeMu.Lock()
	armed := !s.quietWakeUntil.IsZero()
	s.quietWakeMu.Unlock()
	if armed {
		t.Fatal("a zone join left the restore armed")
	}
}

// After the window has actually passed, the timeout path restores once and the
// second run of it does nothing.
func TestQuietWakeTimeoutRestoresOnceAfterTheWindow(t *testing.T) {
	s := quietWakeTestServer()
	s.quietWakeMu.Lock()
	s.quietWakeVol = 30
	s.quietWakeUntil = time.Now().Add(-time.Second)
	s.quietWakeMu.Unlock()

	s.restoreQuietWakeVolume("timeout")

	s.quietWakeMu.Lock()
	armed := !s.quietWakeUntil.IsZero()
	s.quietWakeMu.Unlock()
	if armed {
		t.Fatal("the expired window stayed armed")
	}
}

// The app asks a speaker for a quiet wake by a name that only an agent WITH the
// gate above knows. v0.9.74 and v0.9.75 honour quiet=1 ungated: they mute the
// box to 0 and stop it whether or not it was asleep, and the app wakes the full
// member list of every group edit, so against those agents the old name would
// silence every playing member of a group when one speaker is added. Users
// update the app before they update their speakers, so this is the normal path.
func TestWakeQuietRequestedAcceptsBothSpellings(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"quietifasleep=1", true}, // what a current app sends
		{"quiet=1", true},         // what an older app and an older agent send
		{"", false},               // a plain wake stays plain
		{"quiet=0", false},
		{"quietifasleep=0", false},
		{"quietly=1", false}, // an unknown key is not a quiet wake
	}
	for _, tc := range cases {
		q, err := url.ParseQuery(tc.query)
		if err != nil {
			t.Fatalf("bad test query %q: %v", tc.query, err)
		}
		if got := wakeQuietRequested(q); got != tc.want {
			t.Errorf("wakeQuietRequested(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}
