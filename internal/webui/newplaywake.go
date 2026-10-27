package webui

import (
	"context"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
)

// ensureBoxReadyForNewPlay is ensureBoxReady for a play that starts something
// NEW on the speaker: a station, a preset key, a library file or folder.
//
// The firmware's power-on resumes the speaker's own last source at its own
// level, and the play STR is about to send only takes over a second or two
// later. With the plain wake that second was audible: a SoundTouch 10 woken
// for a station picked under Recently played played its previous station
// first (#1557, 13:53:12.3 power toggle, the old station back on the held
// stream connection at 12.7, the new one only at 14.4). The group wake had the
// same symptom and was fixed in v1.1.0 (#1495); this is that technique for a
// play: the level goes to 0 the moment the speaker shows life, the play is
// sent, and the returned playSent puts the level back once the speaker has
// moved on from what it resumed.
//
// before is the speaker's state read just before the wake. Only a speaker that
// SAYS it is in standby is muted; an unreadable state takes the plain wake it
// always took, and an awake speaker is not touched at all, not even read again
// (the plain wake's first read would have returned at once anyway), so an
// ordinary play on a playing speaker costs exactly what it did before.
//
// playSent must be called once the play was handed to the speaker, delivered
// or not. It never blocks: the restore runs in the background.
func (s *Server) ensureBoxReadyForNewPlay(ctx context.Context, before nowPlayingSnapshot) (playSent func()) {
	noop := func() {}
	if s.boxHost == "" || before.Source == "" {
		s.ensureBoxReady(ctx)
		return noop
	}
	if before.Source != "STANDBY" {
		s.refreshPairingInBackground()
		return noop
	}
	ctx = context.WithoutCancel(ctx)
	prevVol := -1
	volCtx, volCancel := quietWakeStepCtx()
	if v, err := s.ownLevel(volCtx); err == nil {
		prevVol = v
	}
	volCancel()
	// Same budget as ensureBoxReady, so a play out of standby takes no longer
	// than it did. The mute watcher shares the wake's deadline and stops with
	// it.
	wakeCtx, wakeCancel := context.WithTimeout(ctx, 8*time.Second)
	muteDone := s.setVolumeOnFirstLife(wakeCtx, 0)
	err := s.wakeForNewPlay(wakeCtx)
	wakeCancel()
	<-muteDone
	if err != nil {
		s.logger.Warn("Box could not be woken from STANDBY", "err", err)
	} else {
		// The watcher may have raced the firmware's own level restore.
		stepCtx, stepCancel := quietWakeStepCtx()
		_ = s.setOwnLevel(stepCtx, 0)
		stepCancel()
	}
	s.refreshPairingInBackground()
	if prevVol < 0 {
		// Nothing to put back. The firmware keeps its own level, so leaving
		// the speaker muted here would be worse than the old second of noise:
		// write nothing more and let the play run at whatever level it has.
		return noop
	}
	readCtx, readCancel := quietWakeStepCtx()
	resumed := s.quietWakeNowPlaying(readCtx, s.boxHost)
	readCancel()
	s.logger.Info("wake: muted the speaker's own resume for a new play", "mutedFrom", prevVol,
		"resumedSource", resumed.Source, "resumedItem", resumed.ItemName)
	return func() { go s.restoreAfterNewPlay(prevVol, resumed) }
}

// ensureBoxReadyForNewPlayRead is ensureBoxReadyForNewPlay for a caller that
// has not read the speaker's state yet.
func (s *Server) ensureBoxReadyForNewPlayRead(ctx context.Context) (playSent func()) {
	if s.boxHost == "" {
		s.ensureBoxReady(ctx)
		return func() {}
	}
	var before nowPlayingSnapshot
	if np, err := s.readBoxNowWithin(quietWakeStepBudget); err == nil {
		before = snapshotOf(np)
	}
	return s.ensureBoxReadyForNewPlay(ctx, before)
}

// newPlayRestoreTiming bounds the wait for the speaker to leave what it
// resumed before the level comes back. A play of the very station the speaker
// resumed never "leaves" it, and then the wait is all the silence there is,
// so it is kept about as short as a station takes to start anyway.
type newPlayRestoreTiming struct {
	wait, poll time.Duration
}

var productionNewPlayRestoreTiming = newPlayRestoreTiming{
	wait: 2 * time.Second,
	poll: 200 * time.Millisecond,
}

func (s *Server) newPlayRestoreTimes() newPlayRestoreTiming {
	if s.newPlayRestoreT != nil {
		return *s.newPlayRestoreT
	}
	return productionNewPlayRestoreTiming
}

// restoreAfterNewPlay puts the level back after a muted wake for a new play.
// It first waits (bounded) for the speaker to report something other than what
// the firmware resumed, so the level does not come back over the tail of the
// old station. A level that is no longer 0 was set by someone else meanwhile
// (the user's volume key, the start volume) and is left as it is.
func (s *Server) restoreAfterNewPlay(prevVol int, resumed nowPlayingSnapshot) {
	t := s.newPlayRestoreTimes()
	deadline := time.Now().Add(t.wait)
	for {
		ctx, cancel := quietWakeStepCtx()
		np := s.quietWakeNowPlaying(ctx, s.boxHost)
		cancel()
		if np.Source != "" && np.Source != "STANDBY" && movedOnFrom(np, resumed) {
			break
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(t.poll)
	}
	ctx, cancel := quietWakeStepCtx()
	defer cancel()
	if cur, err := s.ownLevel(ctx); err == nil && cur != 0 {
		s.logger.Info("wake: the level was changed during the new play, leaving it", "vol", cur, "mutedFrom", prevVol)
		return
	}
	if err := s.setOwnLevel(ctx, prevVol); err != nil {
		s.logger.Warn("wake: could not restore the level after the new play", "vol", prevVol, "err", err)
		return
	}
	s.logger.Info("wake: level restored after the new play", "vol", prevVol)
}

// movedOnFrom reports whether np shows something other than resumed. Only what
// the resumed read actually carried counts: a read taken the instant the
// speaker came up can have the source but not yet the station, and treating
// the station's name arriving a moment later as "moved on" would bring the
// level back over the very station the mute is there to hide. Such a read
// leaves only the source to tell them apart, and the bounded wait covers the
// rest.
func movedOnFrom(np, resumed nowPlayingSnapshot) bool {
	if resumed.Source != "" && np.Source != resumed.Source {
		return true
	}
	if resumed.Location != "" && np.Location != resumed.Location {
		return true
	}
	return resumed.ItemName != "" && np.ItemName != resumed.ItemName
}

// wakeForNewPlay is the standby wake itself, seamed for the tests.
func (s *Server) wakeForNewPlay(ctx context.Context) error {
	if s.newPlayWakeFn != nil {
		return s.newPlayWakeFn(ctx)
	}
	return boxcli.WakeAndWait(ctx, s.boxHost, 6*time.Second, s.logger)
}

// ownLevel reads this speaker's level.
func (s *Server) ownLevel(ctx context.Context) (int, error) {
	if s.ownLevelGetFn != nil {
		return s.ownLevelGetFn(ctx)
	}
	v, err := boxapi.New(s.boxHost).GetVolume(ctx)
	if err != nil {
		return 0, err
	}
	return v.Actual, nil
}

// setOwnLevel writes this speaker's level.
func (s *Server) setOwnLevel(ctx context.Context, vol int) error {
	if s.ownLevelSetFn != nil {
		return s.ownLevelSetFn(ctx, vol)
	}
	return boxapi.New(s.boxHost).SetVolume(ctx, vol)
}
