package webui

import (
	"context"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// memberCarryBudget bounds the whole "is every member already hearing this?"
// question. It runs inside the post-response goroutine, so it never delays the
// group the user just formed; the budget exists so ONE unreachable member
// cannot hold the recovery push back behind its own timeout.
const memberCarryBudget = 3 * time.Second

// everyMemberCarriesTheGroup reports whether every member is audibly on the
// master's stream already, which is the only state in which skipping the
// post-form re-push is safe.
//
// The re-push exists because a fresh group's members start with nothing, and a
// skip left them silent while the master played on (Martin, 2026-08-24). That
// was fixed by always pushing on a fresh form, and the cost landed on everyone:
// the master IS still playing at that moment, so the push stops a stream nobody
// asked to stop. Measured on a SoundTouch 10 on 2026-09-24: the master reported
// PLAY_STATE 1.5 s after the form, the push went out anyway, and the speaker was
// silent for about 2.5 s.
//
// So the question is asked of the members instead of inferred from the master.
// Deliberately pessimistic, because every wrong "yes" is Martin's bug again:
//
//   - no members at all -> false. A caller that does not know who joined cannot
//     claim they are served.
//   - a member that cannot be read -> false. Unreadable is not "fine"; the
//     historical safe behaviour is to push.
//   - a member that is not in a playing state -> false, even when it reports
//     GROUP_SLAVE. A follower sitting in STOP_STATE is exactly the silent
//     member this guard exists to catch.
//   - a member on something of its own -> false.
//
// Only a member that is BOTH playing and on the group's audio counts as served.
func (s *Server) everyMemberCarriesTheGroup(ctx context.Context, members []boxapi.ZoneMember, masterLocation string) (bool, string) {
	if len(members) == 0 {
		return false, "no members recorded for this form"
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), memberCarryBudget)
	defer cancel()

	masterHostPort := hostPortOf(masterLocation)
	asked := 0
	for _, m := range members {
		ip := strings.TrimSpace(m.IP)
		if ip == s.boxHost {
			continue // the master itself is not a member to ask
		}
		// A member with no address cannot be asked, and this function is
		// deliberately pessimistic: anything it cannot confirm is doubt, and
		// doubt means push. Skipping it meant one verified member could vouch
		// for a second nobody had checked, which is the one outcome this check
		// exists to prevent, a speaker left silent in a group that reported
		// itself served.
		if ip == "" {
			return false, "member " + m.DeviceID + " has no address to ask"
		}
		asked++
		st := memberNowPlaying(ctx, ip)
		if !isPlayingStatus(st.PlayStatus) {
			return false, "member " + ip + " is not playing (" + st.PlayStatus + ")"
		}
		// The same shapes the dissolve path already trusts to decide that a
		// member is on the group's audio rather than on something of its own.
		onGroupStream := st.Source == groupSlaveSource ||
			(masterLocation != "" && st.Location == masterLocation) ||
			(masterHostPort != "" && hostPortOf(st.Location) == masterHostPort)
		if !onGroupStream {
			return false, "member " + ip + " is on " + st.Source + ", not on the group's stream"
		}
	}
	if asked == 0 {
		return false, "no reachable member to ask"
	}
	return true, "every member is playing the group's stream"
}
