package webui

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// What a resume has to survive, measured on a Portable on 2026-08-11 with a
// paused network-library track: Pause reached PAUSE_STATE, the AVTransport Play
// came back without an error, and the speaker went to INVALID_SOURCE and stayed
// silent. The user pressed play and got nothing, while the API reported
// "playing". So a resume is only finished once the speaker says it is playing.

func newResumeServer() *Server {
	return &Server{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		boxHost: "192.0.2.10",
	}
}

// withResumeSeams shrinks the settle window and reports what the box "says",
// returning a channel that receives once a repair is triggered.
func withResumeSeams(t *testing.T, states ...nowPlayingSnapshot) chan struct{} {
	t.Helper()
	prevWindow, prevStep := resumeSettleWindow, resumeSettleStep
	prevRead, prevRepair := resumeReadNowPlaying, resumeRepair
	resumeSettleWindow = 60 * time.Millisecond
	resumeSettleStep = 10 * time.Millisecond

	i := 0
	resumeReadNowPlaying = func(context.Context, string) nowPlayingSnapshot {
		if i < len(states) {
			s := states[i]
			i++
			return s
		}
		if len(states) == 0 {
			return nowPlayingSnapshot{}
		}
		return states[len(states)-1]
	}
	repaired := make(chan struct{}, 1)
	resumeRepair = func(context.Context, *Server) {
		select {
		case repaired <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() {
		resumeSettleWindow, resumeSettleStep = prevWindow, prevStep
		resumeReadNowPlaying, resumeRepair = prevRead, prevRepair
	})
	return repaired
}

// The reported failure: the speaker never comes back, so STR has to put the
// content back instead of leaving the listener in silence.
func TestResumeRepairsASpeakerThatNeverStarts(t *testing.T) {
	repaired := withResumeSeams(t, nowPlayingSnapshot{Source: "INVALID_SOURCE"})
	newResumeServer().watchResume()
	select {
	case <-repaired:
	case <-time.After(2 * time.Second):
		t.Fatal("a speaker that stayed silent after the resume was not repaired")
	}
}

// The opposite mistake, and the more damaging one: a speaker that IS coming
// back, just not instantly, must not be interrupted by a repair it never
// needed. now_playing lags the real state, so the first read can still be
// stale.
func TestResumeLeavesASlowButWorkingSpeakerAlone(t *testing.T) {
	repaired := withResumeSeams(t,
		nowPlayingSnapshot{Source: "UPNP", PlayStatus: "STOP_STATE"},
		nowPlayingSnapshot{Source: "UPNP", PlayStatus: "BUFFERING_STATE"},
	)
	newResumeServer().watchResume()
	select {
	case <-repaired:
		t.Fatal("a speaker that started playing was repaired anyway")
	case <-time.After(300 * time.Millisecond):
	}
}

// The measured shape of the real failure, and what defeated the first two
// attempts at this watcher: the speaker accepts the play, reports PLAY_STATE
// briefly, and only then drops to INVALID_SOURCE. A watcher that stands down on
// the first playing read walks away from exactly this case, so the verdict has
// to be the state at the END of the window.
func TestResumeRepairsAPlayThatDoesNotHold(t *testing.T) {
	repaired := withResumeSeams(t,
		nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE", ItemName: "One Alpha"},
		nowPlayingSnapshot{Source: "UPNP", PlayStatus: "PLAY_STATE", ItemName: "One Alpha"},
		nowPlayingSnapshot{Source: "INVALID_SOURCE"},
	)
	newResumeServer().watchResume()
	select {
	case <-repaired:
	case <-time.After(2 * time.Second):
		t.Fatal("a play that collapsed after a moment was not repaired")
	}
}

// A user who changes their mind mid-window owns the speaker; the stale resume
// must not push its content back on top.
func TestResumeStandsDownWhenTheUserStops(t *testing.T) {
	repaired := withResumeSeams(t, nowPlayingSnapshot{Source: "INVALID_SOURCE"})
	s := newResumeServer()
	s.watchResume()
	time.Sleep(15 * time.Millisecond)
	s.NoteExplicitStop() // the user pressed stop while the watcher was running
	select {
	case <-repaired:
		t.Fatal("repaired although the user had stopped playback")
	case <-time.After(300 * time.Millisecond):
	}
}

// The trap that made the first version of this watcher useless: a speaker
// collapsing out of a resume emits a STOP_STATE, which the gabbo listener
// records as a user stop. Reading THAT as intent means standing down at the one
// moment the repair is needed, so only an explicit request may stop the watcher.
func TestResumeRepairsDespiteTheBoxReportingItsOwnStop(t *testing.T) {
	repaired := withResumeSeams(t, nowPlayingSnapshot{Source: "INVALID_SOURCE"})
	s := newResumeServer()
	s.NoteUserStop() // what the box's own STOP_STATE frame does
	s.watchResume()
	select {
	case <-repaired:
	case <-time.After(2 * time.Second):
		t.Fatal("the box's own stop report suppressed the repair")
	}
}
