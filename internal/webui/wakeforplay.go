package webui

import (
	"context"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
)

// WakeSilentlyForPlay wakes a sleeping speaker for audio STR is about to send
// it, without letting the speaker play anything of its own first.
//
// The firmware's power-on resumes the speaker's own last source at its own
// level. With a plain wake, starting Spotify on a sleeping speaker therefore
// played its last radio station for about five seconds until the push took
// over, which sounded like a stray song left in a buffer (Bad ST10,
// 2026-10-13: woke on preset 3, "Sunshine Live - Die 90er" audible from
// 05:57:05.9 to 05:57:10.6, Spotify attached at 05:57:12.1).
//
// So the level goes to 0 the moment the speaker shows life (the same race the
// quiet group wake runs, see setVolumeOnFirstLife), and the returned restore
// puts it back. The caller pushes its stream and calls restore right after,
// success or not: the push stops whatever the firmware resumed, so the level
// returns over the right audio. An awake speaker is left alone and restore is
// a no-op.
//
// The settling window is stamped like a group wake's, so the power-on resume
// and the forced preset pass both stand down: either would start the last
// station again on a speaker STR woke for something else.
func (s *Server) WakeSilentlyForPlay(ctx context.Context) (restore func()) {
	noop := func() {}
	if s.boxHost == "" {
		return noop
	}
	ctx = context.WithoutCancel(ctx)
	// Only a speaker that SAYS it is in standby is muted. The group wake's gate
	// (quietWakeNeeded) also takes a failed read as standby, which is right
	// for a group but wrong here: this runs on every Spotify activation,
	// including a skip in the Spotify app, and a speaker busy re-pointing its
	// stream can miss the read. That muted a playing speaker for a second on
	// a skip (Portable, 2026-10-13 14:30:03). A failed read is retried once;
	// still unknown, the speaker is left alone.
	np := s.wakeForPlayRead(ctx)
	if np.Source == "" {
		time.Sleep(wakeForPlayReadRetry)
		np = s.wakeForPlayRead(ctx)
	}
	if np.Source != "STANDBY" {
		if np.Source == "" {
			s.logger.Info("wake: state unreadable before a play, not muting", "host", s.boxHost)
		}
		return noop
	}
	s.noteQuietWakeEpisode()
	prevVol := -1
	volCtx, volCancel := context.WithTimeout(ctx, quietWakeStepBudget)
	if v, err := boxapi.New(s.boxHost).GetVolume(volCtx); err == nil {
		prevVol = v.Actual
	}
	volCancel()
	wakeCtx, wakeCancel := context.WithTimeout(ctx, quietWakeWakeBudget)
	muteDone := s.setVolumeOnFirstLife(wakeCtx, 0)
	err := boxcli.WakeAndWait(wakeCtx, s.boxHost, quietWakeWakeBudget-2*time.Second, s.logger)
	wakeCancel()
	<-muteDone
	if err == nil {
		// The watcher may have raced the firmware's own level restore.
		stepCtx, stepCancel := quietWakeStepCtx()
		_ = boxapi.New(s.boxHost).SetVolume(stepCtx, 0)
		stepCancel()
	} else {
		s.logger.Warn("wake: the speaker did not come out of standby for the play", "err", err)
	}
	s.logger.Info("wake: silent wake for a play", "mutedFrom", prevVol)
	if prevVol < 0 {
		return noop
	}
	return func() {
		stepCtx, stepCancel := quietWakeStepCtx()
		defer stepCancel()
		if err := boxapi.New(s.boxHost).SetVolume(stepCtx, prevVol); err != nil {
			s.logger.Warn("wake: could not restore the level after a silent wake", "vol", prevVol, "err", err)
			return
		}
		s.logger.Info("wake: level restored after a silent wake", "vol", prevVol)
	}
}

// wakeForPlayReadRetry is the pause before the second state read.
var wakeForPlayReadRetry = 300 * time.Millisecond

func (s *Server) wakeForPlayRead(ctx context.Context) nowPlayingSnapshot {
	readCtx, readCancel := context.WithTimeout(ctx, quietWakeStepBudget)
	defer readCancel()
	return quietWakeNowPlaying(readCtx, s.boxHost)
}
