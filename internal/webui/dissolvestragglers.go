package webui

// Dissolving a group has to reach every speaker that is playing because of it.
//
// The teardown drives the MASTER's firmware: RemoveZoneSlave, repeated until the
// master reports an empty zone. That covers every member the master actually
// registered, and misses the ones it never did.
//
// Those exist. A field bundle from 2026-08-06 shows a group formed with
//
//	requested:2  verified:1  missing:[DEV#...]  ok:true
//
// The speaker in `missing` got audio all the same, because the mirror path
// pushes the stream to it directly. When the group was dissolved it was not in
// the master's member list, nothing told it to stop, and it kept playing in an
// empty room. The owner reported exactly that: "when the group was dissolved the
// Wave carried on playing."
//
// The rule here is deliberately narrow. Silencing a speaker that is meanwhile
// playing something ELSE would be a worse bug than the one being fixed, so a
// straggler is only stopped when it is demonstrably still carrying the group's
// content: same now-playing location as the master had at the moment the
// dissolve started. Anything else is left alone.

import (
	"context"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// stragglerStopBudget bounds the whole sweep. It runs after the user pressed
// "dissolve" and is best-effort: a speaker that cannot be reached in time keeps
// playing, which is exactly the state we started from, so a long wait buys
// nothing.
const stragglerStopBudget = 8 * time.Second

// playingLocation returns a speaker's now-playing location if it is actually
// producing sound, and "" otherwise. A speaker that is idle, in standby or
// unreadable is not a straggler and must not be touched.
func playingLocation(ctx context.Context, host string) string {
	return playingLocationFn(ctx, host)
}

// Seams for the tests. Both calls below reach the firmware on a fixed port, so
// a test server on a random port can never be reached through them: without
// these, every case would silently take the "unreadable, leave it alone" path
// and assert nothing at all. Production always uses the real implementations.
var (
	playingLocationFn = func(ctx context.Context, host string) string {
		np := fetchNowPlaying(ctx, host)
		switch np.PlayStatus {
		case "PLAY_STATE", "BUFFERING_STATE":
			return np.Location
		}
		return ""
	}
	stopKeyFn = func(ctx context.Context, host string) error {
		return boxapi.New(host).Key(ctx, "STOP")
	}
)

// stopStragglers silences group members that are still carrying the master's
// content after the firmware teardown. masterLocation is what the master was
// playing when the dissolve began; an empty value disables the sweep entirely,
// because without it there is nothing to compare against and stopping speakers
// on a guess is not worth the risk.
func (s *Server) stopStragglers(ctx context.Context, masterLocation string, members []boxapi.ZoneMember) {
	if masterLocation == "" || len(members) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stragglerStopBudget)
	defer cancel()
	for _, m := range members {
		if m.IP == "" || m.IP == s.boxHost {
			continue // no address, or ourselves: the master keeps playing
		}
		loc := playingLocation(ctx, m.IP)
		if loc == "" {
			continue // idle or unreadable: nothing to stop
		}
		if loc != masterLocation {
			// It moved on to something of its own while the group ran. Leaving
			// it alone is the whole point of comparing at all.
			s.logger.Info("dissolve: a former member plays something else now, leaving it alone",
				"member", m.IP)
			continue
		}
		if err := stopKeyFn(ctx, m.IP); err != nil {
			s.logger.Warn("dissolve: a member kept playing and did not take the stop key",
				"member", m.IP, "err", err)
			continue
		}
		s.logger.Info("dissolve: stopped a member the master never registered, which was still playing the group's stream",
			"member", m.IP)
	}
}
