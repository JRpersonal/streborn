// Library keys follow their music server while somebody is looking at them.
//
// The folder and single-track heals (queuepresetheal.go, librarykeyheal.go)
// run on a key press, on "Play folder" and when the app browses the server.
// None of those happens when the owner only opens the app and looks at the
// keys, and that is exactly when a missing cover is noticed: in #1366 the
// reporter's MinimServer (on the same Mac as the desktop app) changed its
// address between 08:05 and 08:16, the v1.0.21 agent was in place since
// 08:04, and the album key on 6 and the playlist key on 2 kept naming the
// dead address, cover included, until each one was pressed at 09:05 and
// 09:06. Until then the app drew the letter placeholder for both.
//
// So a preset read (GET /api/presets, the app's and the phone page's list)
// starts one bounded sweep over the library keys in the background. The read
// itself is answered at once with what the store holds; the sweep writes only
// what moved, and the next read (the app re-reads every 15 s) shows it. The
// key's own address is probed first, a single TCP connect, so a key on a
// server that is still there costs milliseconds and no write.

package webui

import (
	"context"
	"time"
)

// libraryKeySweepEvery is the least time between two sweeps. A server that
// moves is caught within this window of an open app, and a polling app costs
// one TCP connect per library key per window, not per read.
const libraryKeySweepEvery = 5 * time.Minute

// libraryKeySweepTimeout bounds one whole sweep: six keys, each at most a
// probe and one bounded resolve.
const libraryKeySweepTimeout = 90 * time.Second

// kickLibraryKeySweep starts a background sweep of the library keys unless
// one is running or the last one is younger than libraryKeySweepEvery.
func (s *Server) kickLibraryKeySweep() {
	if s.presets == nil || s.mediaServers == nil || len(s.mediaServers.List()) == 0 {
		return
	}
	now := time.Now()
	s.libSweepMu.Lock()
	if s.libSweepRunning || (!s.libSweepLast.IsZero() && now.Sub(s.libSweepLast) < libraryKeySweepEvery) {
		s.libSweepMu.Unlock()
		return
	}
	s.libSweepRunning = true
	s.libSweepLast = now
	s.libSweepMu.Unlock()
	go func() {
		defer func() {
			s.libSweepMu.Lock()
			s.libSweepRunning = false
			done := s.libSweepDone
			s.libSweepMu.Unlock()
			if done != nil {
				done()
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), libraryKeySweepTimeout)
		defer cancel()
		s.sweepLibraryKeys(ctx)
	}()
}

// sweepLibraryKeys runs the press-time heal over every folder key and every
// single library track on a key, without a press. A key the owner changes
// while it is being checked is left alone (storeHealedKey).
func (s *Server) sweepLibraryKeys(ctx context.Context) {
	for _, orig := range s.presets.All() {
		if ctx.Err() != nil {
			return
		}
		p := orig
		switch {
		case p.Type == "queue" && len(p.Items) > 0:
			s.healQueuePreset(ctx, p.Slot, &p, &orig)
		case isLibraryTrackKey(p):
			s.healLibraryKey(ctx, p.Slot, &p, &orig)
		}
	}
}
