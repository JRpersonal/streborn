package webui

import (
	"strings"
	"testing"
	"time"
)

// #830, third field report (jungundeck, ST10 FW 27.0.6, agent v1.5.0,
// 2026-11-03): the speaker's own resume of a NAS MP3 played on from the pause
// at once, while RelTime and the syslog offset stood on the paused 14m27s and
// the stale-URL line was there. Its audio path logged "Audio Path state
// 'PLAYING error #0 for stale UrlID 1" once a second for thirteen seconds, and
// the repair then threw the listener back to 14m27s. These tests pin the
// repair continuing where the listener is.

// staleRun scripts SetBoxLogStalePlayRunFn: a run that began ago before the
// read, with its newest line newest before it. Relative to the read, not to
// the table, so a parallel subtest that starts late sees the same run.
func staleRun(ago, newest time.Duration, lines int, stalled bool) func() (time.Time, time.Time, int, bool) {
	return func() (time.Time, time.Time, int, bool) {
		now := time.Now()
		return now.Add(-ago), now.Add(-newest), lines, stalled
	}
}

func TestTheRepairOfAResumeThatPlaysOnUnderAStaleURLContinuesWhereTheListenerIs(t *testing.T) {
	t.Parallel()
	s, box, logs := newLoggedPausedFileServer(t)
	armTestFile(s)
	// Everything the bundle showed: RelTime frozen on the paused second, the
	// offset not moving, a stale-URL line since the resume.
	s.relPositionFn = (&scriptedRelPosition{pos: 15 * time.Second}).read
	s.SetBoxLogPlayClockFns((&scriptedOffset{sec: 15}).read, staleAfter(time.Now()))
	s.SetBoxLogStalePlayRunFn(staleRun(12*time.Second, 400*time.Millisecond, 13, false))

	s.repairFrozenFileResume(testPodcast(), s.RecallGeneration(), 15*time.Second, time.Now().Add(-time.Minute))

	if got := strings.Join(box.commands(), ","); got != "Stop,Play,Seek" {
		t.Fatalf("commands = %s, want the restart Stop,Play,Seek", got)
	}
	// 15 s paused plus the twelve seconds it has played on since.
	if len(box.seekTo) != 1 || !strings.HasPrefix(box.seekTo[0], "0:00:27") {
		t.Fatalf("seek target = %v, want 0:00:27 (paused 15 s + 12 s played on), not the paused 0:00:15", box.seekTo)
	}
	out := logs.String()
	if !strings.Contains(out, "plays on under a stale URL id") || !strings.Contains(out, "13 lines over 11.6s") {
		t.Fatalf("the restart does not log the stale-id run as its deciding signal:\n%s", out)
	}
}

// Every run that does not prove the audio played on keeps the restart at the
// paused position, and says why.
func TestTheRepairKeepsThePausedPositionWithoutAPlayingStaleRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func() (time.Time, time.Time, int, bool)
		why  string
	}{
		{"no run", staleRun(0, 0, 0, false), "no stale-id PLAYING run"},
		{"buffer stalled", staleRun(12*time.Second, 400*time.Millisecond, 13, true), "buffer writer stalled"},
		{"too short", staleRun(1*time.Second, 400*time.Millisecond, 2, false), "too short"},
		{"stopped", staleRun(20*time.Second, 8*time.Second, 13, false), "has stopped"},
		{"older than the resume", staleRun(2*time.Minute, 400*time.Millisecond, 100, false), "began before this resume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, box, logs := newLoggedPausedFileServer(t)
			armTestFile(s)
			s.relPositionFn = (&scriptedRelPosition{pos: 15 * time.Second}).read
			s.SetBoxLogPlayClockFns((&scriptedOffset{sec: 15}).read, staleAfter(time.Now()))
			s.SetBoxLogStalePlayRunFn(tc.run)

			s.repairFrozenFileResume(testPodcast(), s.RecallGeneration(), 15*time.Second, time.Now().Add(-time.Minute))

			if got := strings.Join(box.commands(), ","); got != "Stop,Play,Seek" {
				t.Fatalf("commands = %s, want the restart Stop,Play,Seek", got)
			}
			if len(box.seekTo) != 1 || box.seekTo[0] != "0:00:15.000" {
				t.Fatalf("seek target = %v, want the paused 0:00:15", box.seekTo)
			}
			if out := logs.String(); !strings.Contains(out, "froze on the speaker") || !strings.Contains(out, tc.why) {
				t.Fatalf("the restart does not log why the run gave no credit (%q):\n%s", tc.why, out)
			}
		})
	}
}

// A run that has carried the file past its end leaves nothing to restart.
func TestTheRepairDoesNotRestartAFileThatPlayedOnToItsEnd(t *testing.T) {
	t.Parallel()
	s, box, logs := newLoggedPausedFileServer(t)
	armTestFile(s)
	s.relPositionFn = (&scriptedRelPosition{pos: 235 * time.Second}).read
	s.SetBoxLogPlayClockFns((&scriptedOffset{sec: 235}).read, staleAfter(time.Now()))
	s.SetBoxLogStalePlayRunFn(staleRun(12*time.Second, 400*time.Millisecond, 13, false))

	s.repairFrozenFileResume(testPodcast(), s.RecallGeneration(), 235*time.Second, time.Now().Add(-time.Minute))

	if got := box.commands(); len(got) != 0 {
		t.Fatalf("a file the speaker played on to its end was restarted: %v", got)
	}
	if !strings.Contains(logs.String(), "to the end of the file, no restart") {
		t.Fatalf("the skipped restart is not logged:\n%s", logs.String())
	}
}
