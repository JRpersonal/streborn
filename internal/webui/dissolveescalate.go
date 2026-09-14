package webui

import (
	"context"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// dissolveEscalationBudget bounds the second and third attempt together. The
// user is waiting on the answer, and a truthful "still grouped" after six
// seconds beats an instant green tick that is wrong.
const dissolveEscalationBudget = 6 * time.Second

// dissolveEscalationStep is the per-speaker budget inside that. A follower that
// is asleep or unreachable must not eat the whole window.
const dissolveEscalationStep = 2 * time.Second

// escalateDissolve is what happens when the master takes the teardown, answers
// 2xx, and keeps every member anyway.
//
// Juergen's eleven-speaker fleet, 2026-09-14: a seven-box group on a
// SoundTouch 30 master. Nine dissolve attempts, four retries each, every one
// answered without an error and every re-read of the zone came back with all
// six members. The app said the group was gone, the speakers kept playing
// together, and only pulling the master's power ended it. Every teardown on
// the user-driven path posts at the MASTER, and a master in this state is
// exactly the one speaker that will not act on it.
//
// Two things are tried, cheapest first, and the zone is re-read after each:
//
//  1. One member per call instead of all of them in one body. The firmware
//     takes the whole list in a single POST, and a member it dislikes anywhere
//     in that list can take the rest down with it; one at a time at least
//     frees the others and names the one that sticks.
//  2. The teardown posted AT EACH FOLLOWER. dissolvesweep.go has used this for
//     stale memberships since it was written, with the comment that a teardown
//     at the master is a no-op once the master's firmware has stopped
//     cooperating. That is this case, and the user-driven dissolve never got
//     to use it.
//
// Returns the members the master still reports, and the still-unreadable
// reason if the zone could not be re-read.
func (s *Server) escalateDissolve(c *boxapi.Client, master boxapi.ZoneMember, cur []boxapi.ZoneMember) (remaining []boxapi.ZoneMember, unverified string) {
	if len(cur) == 0 {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), dissolveEscalationBudget)
	defer cancel()

	// Instrumented on purpose. When this ran on eleven speakers the bundle
	// could not say WHY the firmware refused: the member list that was sent,
	// the answer that came back and the members each re-read returned were all
	// invisible, so the only honest verdict was "the firmware took it and did
	// nothing". Every line below exists to make the next bundle decide it.
	s.logger.Warn("zone: the speaker keeps the group after a batch teardown, escalating",
		"remaining", len(cur), "members", memberIDs(cur),
		"master", master.DeviceID, "masterIP", master.IP)

	// 1. one at a time, at the master.
	for _, m := range cur {
		if ctx.Err() != nil {
			break
		}
		stepCtx, stepCancel := context.WithTimeout(ctx, dissolveEscalationStep)
		err := c.RemoveZoneSlave(stepCtx, master, []boxapi.ZoneMember{m})
		stepCancel()
		s.logger.Info("zone: single-member teardown at the master",
			"member", memberID(m), "memberIP", m.IP, "err", errText(err))
	}
	left, reason := s.readZoneMembers(ctx, c)
	s.logger.Info("zone: what the master reports after the single-member teardown",
		"remaining", len(left), "members", memberIDs(left), "unreadable", reason)
	if reason == "" && len(left) == 0 {
		s.logger.Info("zone: the group came apart one member at a time, the batch teardown was the problem")
		return nil, ""
	}
	if reason == "" {
		cur = left
	}

	// 2. at each follower's own speaker.
	posted := 0
	for _, m := range cur {
		if ctx.Err() != nil {
			break
		}
		if m.IP == "" {
			continue
		}
		stepCtx, stepCancel := context.WithTimeout(ctx, dissolveEscalationStep)
		err := leaveZoneAtFollowerFn(stepCtx, m.IP, master, m)
		stepCancel()
		posted++
		s.logger.Info("zone: teardown posted at the follower itself",
			"member", memberID(m), "memberIP", m.IP, "err", errText(err))
	}
	s.logger.Info("zone: teardown posted at the followers themselves", "speakers", posted)

	left, reason = s.readZoneMembers(ctx, c)
	if reason != "" {
		return cur, reason
	}
	if len(left) == 0 {
		s.logger.Info("zone: the group came apart once the teardown was posted at the followers")
	} else {
		s.logger.Warn("zone: the speaker still reports the group after every teardown route",
			"remaining", len(left), "members", memberIDs(left))
	}
	return left, ""
}

// readZoneMembers reads the master's live zone. The reason is non-empty only
// when the zone could not be read at all, which is not the same answer as an
// empty one and must never be reported as a successful dissolve.
func (s *Server) readZoneMembers(ctx context.Context, c *boxapi.Client) (members []boxapi.ZoneMember, unverified string) {
	readCtx, cancel := context.WithTimeout(ctx, dissolveEscalationStep)
	defer cancel()
	z, err := c.GetZone(readCtx)
	if err != nil {
		return nil, err.Error()
	}
	if z.Master == "" {
		return nil, ""
	}
	return z.Members, ""
}

// memberID names a speaker for a log line without putting a device id in it.
// The ids are hashed in a diagnostic bundle anyway, and a short stable tag is
// what a reader needs to follow one speaker across several lines.
func memberID(m boxapi.ZoneMember) string {
	if m.DeviceID != "" {
		return m.DeviceID
	}
	return m.IP
}

func memberIDs(ms []boxapi.ZoneMember) string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, memberID(m))
	}
	return strings.Join(out, ",")
}

// errText renders an error for a log field without turning a nil into "<nil>",
// so a line reads err="" on success rather than looking like a failure.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
