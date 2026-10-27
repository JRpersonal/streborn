package webui

import (
	"context"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/streamproxy"
)

// ensureBoxReadyForNewPlay is ensureBoxReady for a play that starts something
// NEW on the speaker: a station, a preset key, a library file or folder.
//
// The firmware's power-on resumes the speaker's own last station at its own
// level, and the play STR is about to send only takes over a second or two
// later. With the plain wake that second was audible: a SoundTouch 10 woken
// for a station picked under Recently played played its previous station
// first (#1557, 13:53:12.3 power toggle, the old station back on the held
// stream connection at 12.7, the new one only at 14.4).
//
// Two layers, because the first live test of a mute alone lost the race (ST10
// 2026-10-27: source left STANDBY at 10.08, the firmware's own level 10 held
// until about 10.8, audio from 10.92):
//
//  1. The stream proxy gate (streamproxy/newplaygate.go). The station the
//     speaker resumes is fetched from STR's own proxy, so while the new play
//     is being started every fetch that is not the new play's target gets
//     silence, and the connection held from before standby is closed at once.
//     This is what keeps the old station inaudible; it does not depend on any
//     timing.
//  2. The level. It goes to 0 on the first gabbo frame that shows the speaker
//     left standby (NoteBoxSourceChanged), with the polling watcher as the
//     backstop, and a level the firmware sets on top of that while the play
//     is still being started is muted again (NoteBoxVolumeLevel). It comes
//     back, to the level the speaker had before standby, once the speaker has
//     moved on from what it resumed. This covers a resumed source the proxy
//     does not serve.
//
// before is the speaker's state read just before the wake. Only a speaker that
// SAYS it is in standby gets any of this; an unreadable state takes the plain
// wake it always took, and an awake speaker is not touched at all, not even
// read again (the plain wake's first read would have returned at once anyway),
// so an ordinary play on a playing speaker costs exactly what it did before.
//
// target is what the new play makes the speaker fetch from the proxy; empty
// when the play does not go through the proxy (a library file the speaker
// fetches from the media server itself).
//
// playSent must be called exactly once, after the play was handed to the
// speaker, with whether it was delivered. It never blocks.
func (s *Server) ensureBoxReadyForNewPlay(ctx context.Context, before nowPlayingSnapshot, target streamproxy.NewPlayTarget) (playSent func(delivered bool)) {
	noop := func(bool) {}
	if s.boxHost == "" || before.Source == "" {
		s.ensureBoxReady(ctx)
		return noop
	}
	if before.Source != "STANDBY" {
		// A gate an earlier play left waiting for its fetch must not silence
		// this one; one whose play is still being started (a recall going on
		// into a folder) stays (ReleaseSentNewPlayGate).
		if g := s.newPlayGate(); g != nil {
			g.ReleaseSentNewPlayGate("another play on the awake speaker")
		}
		s.refreshPairingInBackground()
		return noop
	}
	ctx = context.WithoutCancel(ctx)
	var gen uint64
	gate := s.newPlayGate()
	if gate != nil {
		gen = gate.ArmNewPlayGate(target, newPlayGateMaxHold)
	}
	prevVol := s.levelBeforeStandby(ctx)
	s.newPlayLevel.arm()
	// Same budget as ensureBoxReady, so a play out of standby takes no longer
	// than it did. The polling watcher is the backstop for a gabbo frame that
	// does not come; it shares the wake's deadline and stops with it.
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
	readCtx, readCancel := quietWakeStepCtx()
	resumed := s.quietWakeNowPlaying(readCtx, s.boxHost)
	readCancel()
	s.logger.Info("wake: muted the speaker's own resume for a new play", "restoreTo", prevVol,
		"resumedSource", resumed.Source, "resumedItem", resumed.ItemName)
	var once sync.Once
	return func(delivered bool) {
		once.Do(func() {
			s.newPlayLevel.stopMuting()
			if gate != nil {
				switch {
				case !delivered:
					// A failed play falls back to what the speaker did before:
					// nothing is left silent behind it.
					gate.ReleaseNewPlayGate(gen, "the new play failed")
				case target.Empty():
					gate.ReleaseNewPlayGate(gen, "the new play does not go through the proxy")
				default:
					gate.NewPlaySent(gen, newPlayGateFetchGrace)
				}
			}
			go s.restoreAfterNewPlay(prevVol, resumed)
		})
	}
}

// ensureBoxReadyForNewPlayRead is ensureBoxReadyForNewPlay for a caller that
// has not read the speaker's state yet.
func (s *Server) ensureBoxReadyForNewPlayRead(ctx context.Context, target streamproxy.NewPlayTarget) (playSent func(delivered bool)) {
	if s.boxHost == "" {
		s.ensureBoxReady(ctx)
		return func(bool) {}
	}
	var before nowPlayingSnapshot
	if np, err := s.readBoxNowWithin(quietWakeStepBudget); err == nil {
		before = snapshotOf(np)
	}
	return s.ensureBoxReadyForNewPlay(ctx, before, target)
}

const (
	// newPlayGateMaxHold bounds how long the proxy gate stays closed when
	// nothing opens it: the wake (up to about 8.5 s) plus the push.
	newPlayGateMaxHold = 15 * time.Second
	// newPlayGateFetchGrace is how long after the play was handed over its
	// own fetch may take to arrive before the gate opens anyway.
	newPlayGateFetchGrace = 5 * time.Second
)

// newPlayGater is the stream proxy's new-play gate; a seam for the tests.
type newPlayGater interface {
	ArmNewPlayGate(target streamproxy.NewPlayTarget, maxHold time.Duration) uint64
	AddNewPlayTarget(url string)
	NewPlaySent(gen uint64, grace time.Duration)
	ReleaseNewPlayGate(gen uint64, why string)
	ReleaseSentNewPlayGate(why string)
}

func (s *Server) newPlayGate() newPlayGater {
	if s.newPlayGateFn != nil {
		return s.newPlayGateFn
	}
	if s.streamProxy == nil {
		return nil
	}
	return s.streamProxy
}

// newPlayLevelState follows the speaker's level around a muted wake for a new
// play, driven by gabbo frames.
type newPlayLevelState struct {
	mu sync.Mutex
	// muting: from the arm until the play was handed over. The first frame
	// that shows the speaker out of standby mutes it, and a level the
	// firmware raises meanwhile is muted again (at most newPlayMaxRemutes).
	muting  bool
	remutes int
	// episodes counts muted wakes whose restore has not finished; while one
	// runs, the frames of STR's own writes are not taken for the user's level.
	episodes int
	// last is the newest level a frame reported outside such an episode.
	last   int
	lastAt time.Time
}

const newPlayMaxRemutes = 3

func (l *newPlayLevelState) arm() {
	l.mu.Lock()
	l.muting = true
	l.remutes = 0
	l.episodes++
	l.mu.Unlock()
}

func (l *newPlayLevelState) stopMuting() {
	l.mu.Lock()
	l.muting = false
	l.mu.Unlock()
}

func (l *newPlayLevelState) episodeDone() {
	l.mu.Lock()
	if l.episodes > 0 {
		l.episodes--
	}
	l.mu.Unlock()
}

// NoteBoxSourceChanged is the gabbo source edge. During a muted wake for a new
// play, the first frame that shows the speaker out of standby mutes it at
// once, instead of a poll noticing it later. Called on the gabbo read loop:
// the write runs in its own goroutine.
func (s *Server) NoteBoxSourceChanged(from, to string) {
	if from != "STANDBY" || to == "STANDBY" || s.boxHost == "" {
		return
	}
	s.newPlayLevel.mu.Lock()
	muting := s.newPlayLevel.muting
	s.newPlayLevel.mu.Unlock()
	if !muting {
		return
	}
	go s.muteForNewPlay("the speaker left standby", to)
}

// NoteBoxVolumeLevel is the gabbo volume frame (-1 when it carried no level).
// Outside a muted wake it records the speaker's level, which is what a muted
// wake later puts back: the level read from a speaker in standby is not the
// one it plays at (live ST10: read 5, resumed at 10). During the mute, a level
// the firmware raises is muted again. Called on the gabbo read loop.
func (s *Server) NoteBoxVolumeLevel(level int) {
	if level < 0 {
		return
	}
	l := &s.newPlayLevel
	l.mu.Lock()
	switch {
	case l.muting:
		if level == 0 || l.remutes >= newPlayMaxRemutes || s.boxHost == "" {
			l.mu.Unlock()
			return
		}
		l.remutes++
		l.mu.Unlock()
		go s.muteForNewPlay("the firmware raised the level during the wake", "")
		return
	case l.episodes == 0:
		l.last = level
		l.lastAt = time.Now()
	}
	l.mu.Unlock()
}

func (s *Server) muteForNewPlay(why, source string) {
	ctx, cancel := quietWakeStepCtx()
	defer cancel()
	if err := s.setOwnLevel(ctx, 0); err != nil {
		s.logger.Warn("wake: could not mute the speaker's own resume", "why", why, "err", err)
		return
	}
	s.logger.Info("wake: muted the speaker's own resume", "why", why, "source", source)
}

// levelBeforeStandby is the level a muted wake puts back: the last one the
// speaker reported while awake, else what it answers now (its target level
// first: the actual level of a speaker in standby can be lower than the one it
// plays at). -1 when nothing is known.
func (s *Server) levelBeforeStandby(ctx context.Context) int {
	l := &s.newPlayLevel
	l.mu.Lock()
	last, at := l.last, l.lastAt
	l.mu.Unlock()
	if !at.IsZero() {
		return last
	}
	volCtx, volCancel := context.WithTimeout(ctx, quietWakeStepBudget)
	defer volCancel()
	v, err := s.ownVolume(volCtx)
	if err != nil {
		return -1
	}
	if v.Target > 0 {
		return v.Target
	}
	return v.Actual
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
	defer s.newPlayLevel.episodeDone()
	if prevVol < 0 {
		return
	}
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
	if v, err := s.ownVolume(ctx); err == nil && v.Actual != 0 {
		s.logger.Info("wake: the level was changed during the new play, leaving it", "vol", v.Actual, "restoreTo", prevVol)
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

// ownVolume reads this speaker's level.
func (s *Server) ownVolume(ctx context.Context) (boxapi.Volume, error) {
	if s.ownVolumeFn != nil {
		return s.ownVolumeFn(ctx)
	}
	return boxapi.New(s.boxHost).GetVolume(ctx)
}

// setOwnLevel writes this speaker's level.
func (s *Server) setOwnLevel(ctx context.Context, vol int) error {
	if s.ownLevelSetFn != nil {
		return s.ownLevelSetFn(ctx, vol)
	}
	return boxapi.New(s.boxHost).SetVolume(ctx, vol)
}
