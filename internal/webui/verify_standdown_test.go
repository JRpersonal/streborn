package webui

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// verifyStandDownReason is the single policy behind verifyRecall's abort
// decision. It must stop a verify that was superseded by a newer play (two
// rapid preset presses used to spawn dueling verifies that ping-ponged the
// stations for ~15s) and one the user overrode with a stop/pause/power-off
// after the recall started - while never stopping a verify because of a stop
// that PRECEDED the recall (stop, then recall something else, is normal).
func TestVerifyStandDownReason(t *testing.T) {
	start := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	after := start.Add(3 * time.Second)
	before := start.Add(-3 * time.Second)
	none := time.Time{}

	cases := []struct {
		name                 string
		recallGen, curGen    uint64
		userStop, standbyOff time.Time
		wantContains         string // "" = keep verifying
	}{
		{"current recall keeps verifying", 4, 4, none, none, ""},
		{"newer play supersedes", 4, 5, none, none, "superseded"},
		{"user stop after recall stands down", 4, 4, after, none, "stopped"},
		{"user stop before recall is ignored", 4, 4, before, none, ""},
		{"power-off after recall stands down", 4, 4, none, after, "powered off"},
		{"power-off before recall is ignored", 4, 4, none, before, ""},
		// noteStandbyStop stamps BOTH; the power-off must win the log reason.
		{"power-off wins over the paired user stop", 4, 4, after, after, "powered off"},
		{"supersession wins over everything", 4, 6, after, after, "superseded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := verifyStandDownReason(tc.recallGen, tc.curGen, start, tc.userStop, tc.standbyOff)
			if tc.wantContains == "" {
				if got != "" {
					t.Errorf("verifyStandDownReason() = %q, want empty (keep verifying)", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("verifyStandDownReason() = %q, want reason containing %q", got, tc.wantContains)
			}
		})
	}
}

// Every setLastPlay must bump the recall generation, and the live stand-down
// read must see it: that is what makes an older recall's verify abort the
// moment a newer play lands.
func TestSetLastPlayBumpsRecallGeneration(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	start := time.Now()

	g1 := s.setLastPlay("http://127.0.0.1:8888/stream/4", "A", "", "")
	if reason := s.recallStandDownReason(g1, start); reason != "" {
		t.Fatalf("fresh recall: stand-down reason %q, want none", reason)
	}

	g2 := s.setLastPlay("http://127.0.0.1:8888/stream/5", "B", "", "")
	if g2 != g1+1 {
		t.Fatalf("generations: got %d then %d, want a +1 bump", g1, g2)
	}
	if reason := s.recallStandDownReason(g1, start); !strings.Contains(reason, "superseded") {
		t.Errorf("old recall after a newer play: reason %q, want superseded", reason)
	}
	if reason := s.recallStandDownReason(g2, start); reason != "" {
		t.Errorf("newest recall: stand-down reason %q, want none", reason)
	}
}

// TestLoginErrorWindows pins the two windows hung off the same 1036 stamp: the
// short retry stand-down (recentLoginError) and the wider wedge-attribution
// skip that stops an exhausted, login-broken recall from counting as a wedge
// strike (the field bundle latched boxHealth=wedged ~25s after a 1036 and told
// the user to power-cycle a box that only needed the re-login to stick).
func TestLoginErrorWindows(t *testing.T) {
	s := &Server{}
	if s.loginErrorRecentWithin(loginErrWedgeSkipWindow) {
		t.Fatal("no login error recorded yet, want false")
	}
	s.loginErr.mu.Lock()
	s.loginErr.last = time.Now().Add(-30 * time.Second)
	s.loginErr.mu.Unlock()
	if s.recentLoginError() {
		t.Fatal("a 30s-old login error is outside the 20s retry stand-down window")
	}
	if !s.loginErrorRecentWithin(loginErrWedgeSkipWindow) {
		t.Fatal("a 30s-old login error is inside the wedge-skip window: the exhaustion it caused must not count a strike")
	}
	s.loginErr.mu.Lock()
	s.loginErr.last = time.Now().Add(-loginErrWedgeSkipWindow - time.Second)
	s.loginErr.mu.Unlock()
	if s.loginErrorRecentWithin(loginErrWedgeSkipWindow) {
		t.Fatal("an old login error must not suppress wedge accounting")
	}
}

// TestSilentRefusalLatch covers the quiet sibling of the 1036 storm: recalls
// that exhaust while the box dropped its source to STANDBY on its own (no
// adjacent key press, no 1036). Field case 2026-08-01: two independent ST10s
// failed every recall this way and the app showed nothing, because the
// standby read as a user power-off and no 1036 ever fired.
func TestSilentRefusalLatch(t *testing.T) {
	disc := slog.New(slog.NewTextHandler(io.Discard, nil))

	// User power-off: STANDBY with no non-user drop classified -> ignored.
	s := &Server{logger: disc}
	s.noteRecallExhaustedWithSource("STANDBY")
	if active, _ := s.RecallRefusal(); active {
		t.Fatal("user power-off must not latch the refusal state")
	}

	// Box self-drop (classifier stamped non-user): two strikes latch.
	s.NoteNonUserStandbyDrop()
	s.noteRecallExhaustedWithSource("STANDBY")
	if active, _ := s.RecallRefusal(); active {
		t.Fatal("one silent failure must stay quiet")
	}
	s.NoteNonUserStandbyDrop()
	s.noteRecallExhaustedWithSource("STANDBY")
	if active, _ := s.RecallRefusal(); !active {
		t.Fatal("two consecutive silent failures must latch the refusal state")
	}

	// Observed playback clears it.
	s.NoteBoxHealthy()
	if active, _ := s.RecallRefusal(); active {
		t.Fatal("playback must clear the refusal state")
	}

	// A recent 1036 used to hand the messaging to the storm marker and latch
	// nothing here. The field disproved that contract on 2026-08-02: the storm
	// marker needs six rejections in ten minutes, a box that refused three
	// times left its owner with no message at all, and the cure (soft restart)
	// is identical either way. So a 1036-attributed refusal now latches too.
	s2 := &Server{logger: disc}
	s2.NoteNonUserStandbyDrop()
	s2.NoteBoxLoginError()
	s2.noteRecallExhaustedWithSource("STANDBY")
	s2.NoteNonUserStandbyDrop()
	s2.noteRecallExhaustedWithSource("STANDBY")
	if active, _ := s2.RecallRefusal(); !active {
		t.Fatal("1036-attributed refusals must latch the restart hint too")
	}

	// Recent stream activity absolves: the content failed, not the box.
	s3 := &Server{logger: disc}
	now := time.Now()
	s3.SetStreamActivityFn(func() (time.Time, time.Time) { return now, time.Time{} })
	s3.NoteNonUserStandbyDrop()
	s3.noteRecallExhaustedWithSource("STANDBY")
	s3.NoteNonUserStandbyDrop()
	s3.noteRecallExhaustedWithSource("STANDBY")
	if active, _ := s3.RecallRefusal(); active {
		t.Fatal("recent stream activity must absolve the silent failures")
	}
}

// TestLoginRefusalLatchesToo closes the gap the 2026-08-02 field bundle
// exposed: a box that answers 1036 for every press latched NOTHING (the wedge
// path skips it as a login failure, and the 1036 storm marker needs six
// rejections in ten minutes), so a user with three rejections saw no banner
// and no restart button - in the one case where the soft restart is the cure.
func TestLoginRefusalLatchesToo(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Box awake (not STANDBY) and a fresh 1036: the wedge attribution skips,
	// but the refusal latch must count it.
	s.NoteBoxLoginError()
	s.noteRecallExhaustedWithSource("INVALID_SOURCE")
	if active, _ := s.RecallRefusal(); active {
		t.Fatal("one refused recall must stay quiet")
	}
	s.NoteBoxLoginError()
	s.noteRecallExhaustedWithSource("INVALID_SOURCE")
	if active, _ := s.RecallRefusal(); !active {
		t.Fatal("two consecutive 1036-refused recalls must latch the restart hint")
	}
	// The wedge state must NOT latch from these: the advice differs (a wedge
	// needs a power-cycle, a login refusal a soft restart).
	if status, _ := s.BoxHealth(); status != "ok" {
		t.Fatalf("login refusals must not latch a wedge, got %q", status)
	}
	s.NoteBoxHealthy()
	if active, _ := s.RecallRefusal(); active {
		t.Fatal("observed playback must clear the refusal latch")
	}
}

// A station that answers an HTTP error must never be reported as a speaker
// problem. Field case #582 (ST10, 2026-08-12): preset 4 pointed at a URL
// serving 401 on every fetch, so every recall exhausted, and STR latched the
// "restart the speaker" hint while the neighbouring presets played fine
// seconds earlier through the same box. The 1036 that rides along on every
// press of that firmware must not claim the failure for the login family
// either, which is why the station check runs first.
func TestStationRefusalIsNotABoxProblem(t *testing.T) {
	disc := slog.New(slog.NewTextHandler(io.Discard, nil))

	s := &Server{logger: disc}
	failedNow := time.Now()
	s.SetStreamActivityFn(func() (time.Time, time.Time) { return time.Time{}, failedNow })
	// The 1036 is present, as it is on the real box, and must not decide this.
	s.NoteBoxLoginError()
	s.noteRecallExhaustedWithSource("UPNP")
	s.NoteBoxLoginError()
	s.noteRecallExhaustedWithSource("UPNP")
	if active, _ := s.RecallRefusal(); active {
		t.Fatal("a station refusing the stream must not latch the restart hint")
	}

	// The same two exhaustions with no station failure in the window DO latch:
	// this is the case the hint exists for.
	s2 := &Server{logger: disc}
	old := time.Now().Add(-wedgeStrikeWindow - time.Second)
	s2.SetStreamActivityFn(func() (time.Time, time.Time) { return time.Time{}, old })
	s2.NoteBoxLoginError()
	s2.noteRecallExhaustedWithSource("UPNP")
	s2.NoteBoxLoginError()
	s2.noteRecallExhaustedWithSource("UPNP")
	if active, _ := s2.RecallRefusal(); !active {
		t.Fatal("a box that refuses recalls with no station failure must still latch the hint")
	}
}
