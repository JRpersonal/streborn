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
	"net"
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
	return playingStateFn(ctx, host).Location
}

// playingState is what the straggler sweep knows about a former member: the
// source and location it reports while it is actually producing sound. Both
// empty means idle, in standby or unreadable.
type playingState struct {
	Source   string
	Location string
}

// groupSlaveSource is the source a speaker reports while it follows a zone
// master. A former member that STILL says so after the zone is gone is, by its
// own account, carrying the group's audio and nothing of its own.
const groupSlaveSource = "GROUP_SLAVE"

// Seams for the tests. Both calls below reach the firmware on a fixed port, so
// a test server on a random port can never be reached through them: without
// these, every case would silently take the "unreadable, leave it alone" path
// and assert nothing at all. Production always uses the real implementations.
var (
	playingStateFn = func(ctx context.Context, host string) playingState {
		np := fetchNowPlaying(ctx, host)
		switch np.PlayStatus {
		case "PLAY_STATE", "BUFFERING_STATE":
			return playingState{Source: np.Source, Location: np.Location}
		}
		return playingState{}
	}
	stopKeyFn = func(ctx context.Context, host string) error {
		return boxapi.New(host).Key(ctx, "STOP")
	}
)

// stopStragglers silences group members that are still carrying the master's
// content after the firmware teardown. A member is a straggler when it is still
// playing the GROUP's stream, which it can report in either of two shapes:
//
//   - native: its now-playing location equals masterLocation, what the master
//     itself was playing when the dissolve began (the 2026-08-06 missing-member
//     case, where the master pushed the stream to a box it never registered).
//   - mirror: its location's host:port equals mirrorHostPort, the master's mirror
//     proxy (masterIP:17008) that every mirror slave was pointed at. A mirror
//     group has NO firmware zone, so the RemoveZoneSlave loop is a no-op and this
//     is the ONLY thing that stops the followers. masterLocation can never match
//     here: the master reports its own loopback 127.0.0.1:8888/stream/N while the
//     slaves carry masterIP:17008/stream/N, so the old exact-string compare left
//     every mirror follower playing after a "dissolve" (field: the group kept
//     playing). Comparing host:port is exactly the test slaveMirrorAction already
//     uses to recognise a slave on the master's stream.
//
// Two more shapes, both from a field bundle of 2026-09-07 (three speakers
// playing a music library, the firmware dropped the two followers out of the
// zone by itself, and both played on in their rooms while every app showed
// them in standby):
//
//   - same server: the master plays a music-library TRACK, and its location
//     moves on to the next track every few minutes. A follower that dropped out
//     keeps the track it had, so the exact compare fails although it is
//     audibly still on the group's programme. A follower whose location sits on
//     the same host:port as the master's (the media server) is therefore a
//     straggler too. Loopback hosts are excluded from this rule on purpose: a
//     member on 127.0.0.1:8888 plays its OWN proxy, and equality already covers
//     a zone slave mirroring the master's loopback item.
//   - still a slave: a member that reports the GROUP_SLAVE source has, by its
//     own account, nothing of its own playing. Whatever its location says, it
//     is carrying the group's audio.
//
// The master location empty AND no mirror proxy disables the sweep: without
// something to compare against, stopping speakers on a guess is not worth the
// risk. Anything matching none of the shapes moved on to something of its own
// and is deliberately left alone.
func (s *Server) stopStragglers(ctx context.Context, masterLocation, mirrorHostPort string, members []boxapi.ZoneMember) {
	if (masterLocation == "" && mirrorHostPort == "") || len(members) == 0 {
		return
	}
	serverHostPort := groupServerHostPort(masterLocation)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stragglerStopBudget)
	defer cancel()
	for _, m := range members {
		if m.IP == "" || m.IP == s.boxHost {
			continue // no address, or ourselves: the master keeps playing
		}
		st := playingStateFn(ctx, m.IP)
		loc := st.Location
		if loc == "" && st.Source != groupSlaveSource {
			continue // idle or unreadable: nothing to stop
		}
		onGroupStream := (masterLocation != "" && loc == masterLocation) ||
			(mirrorHostPort != "" && hostPortOf(loc) == mirrorHostPort) ||
			(serverHostPort != "" && hostPortOf(loc) == serverHostPort) ||
			st.Source == groupSlaveSource
		if !onGroupStream {
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
		s.logger.Info("dissolve: stopped a member still carrying the group's stream",
			"member", m.IP)
	}
}

// groupServerHostPort is the host:port a group's programme comes from when the
// master plays from a network server (a music library, a stream the members
// pull themselves), or "" when the master's location is empty or on a loopback
// host, where host:port equality would say nothing about the group.
func groupServerHostPort(masterLocation string) string {
	hp := hostPortOf(masterLocation)
	if hp == "" {
		return ""
	}
	host := hp
	if h, _, err := net.SplitHostPort(hp); err == nil {
		host = h
	}
	if host == "localhost" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ""
	}
	return hp
}
