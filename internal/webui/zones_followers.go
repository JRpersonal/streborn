// The members of a group: checking that each follower really joined, and
// the deviceID a speaker's own firmware reports (resolved for this box,
// remembered for the members). Split out of zones_stereo.go.

package webui

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// followerZoneFetch returns one follower's own zone self-report. Split out so a
// test can inject a fake without standing up a :8090 server (boxapi.New hardcodes
// the :8090 port via Client.url).
type followerZoneFetch func(ctx context.Context, ip string) (boxapi.Zone, error)

// verifyFollowersJoined polls each requested slave's OWN /getZone until it
// reports masterID as its zone master, or a per-follower deadline elapses (#70).
// Trusting only the master's optimistic member list reports a complete group
// while a follower is actually still standalone: the master lists a member the
// firmware announced before the slave actually enrolled, and the slave's own
// zone lags ~100ms to several seconds behind. A follower that never names
// masterID as its master within the budget is returned in "missing" so the app
// can flag it instead of claiming success. Followers with no known IP cannot be
// verified and are returned in "unverifiable" (left to the master's view).
func verifyFollowersJoined(ctx context.Context, logger *slog.Logger, masterID string, slaves []boxapi.ZoneMember, fetch followerZoneFetch) (missing, unverifiable []string) {
	return verifyFollowersJoinedTimed(ctx, logger, masterID, slaves, fetch, defaultFollowerVerifyTiming)
}

// followerVerifyTiming bounds verifyFollowersJoined's polling. Injected so the
// tests can shrink the budget from seconds to milliseconds; production always
// uses defaultFollowerVerifyTiming.
type followerVerifyTiming struct {
	perFollowerBudget time.Duration
	pollInterval      time.Duration
	perCallTimeout    time.Duration
}

var defaultFollowerVerifyTiming = followerVerifyTiming{
	perFollowerBudget: 4 * time.Second,
	pollInterval:      700 * time.Millisecond,
	perCallTimeout:    2 * time.Second,
}

// verifyFollowersJoinedTimed is verifyFollowersJoined with explicit timing;
// see there for the semantics.
func verifyFollowersJoinedTimed(ctx context.Context, logger *slog.Logger, masterID string, slaves []boxapi.ZoneMember, fetch followerZoneFetch, timing followerVerifyTiming) (missing, unverifiable []string) {
	// Every follower is polled AT THE SAME TIME. They are separate speakers on
	// separate addresses and nothing about asking one depends on having asked
	// another, so the old speaker-after-speaker walk bought nothing and cost
	// the whole budget.
	//
	// It cost correctness, not just time. This runs under the form's context,
	// which is bounded by zoneFormBudget, and each follower was given up to
	// four seconds of its own. Eleven followers therefore needed up to
	// forty-four seconds inside a budget that stops at thirty-eight, minus
	// whatever /setZone had already spent. The context died partway down the
	// list and every follower after that point was reported "missing" although
	// it had joined perfectly well.
	//
	// A twelve-speaker household saw exactly that on 2026-08-09: ok=true with
	// all members listed, but verified=3 on one attempt and verified=7 minutes
	// later, a different set each time, and 11 of 11 the day before when
	// /setZone happened to be quick and left more budget. Read as a grouping
	// failure that is baffling; read as a report running out of time it is
	// obvious. Polled together, the whole check costs about one follower's
	// budget no matter how large the fleet.
	type result struct {
		deviceID     string
		unverifiable bool
		joined       bool
	}
	results := make([]result, len(slaves))
	var wg sync.WaitGroup
	for i, sl := range slaves {
		results[i].deviceID = sl.DeviceID
		if sl.IP == "" {
			results[i].unverifiable = true
			continue
		}
		wg.Add(1)
		go func(i int, sl boxapi.ZoneMember) {
			defer wg.Done()
			results[i].joined = pollFollowerJoined(ctx, logger, masterID, sl, fetch, timing)
		}(i, sl)
	}
	wg.Wait()

	// Reported in the order the caller asked for them, so the output does not
	// shuffle between runs just because the speakers answered out of order.
	for _, r := range results {
		switch {
		case r.unverifiable:
			unverifiable = append(unverifiable, r.deviceID)
		case !r.joined:
			missing = append(missing, r.deviceID)
		}
	}
	return missing, unverifiable
}

// pollFollowerJoined polls one follower's own /getZone until it names masterID
// as its master, its own budget runs out, or the surrounding context ends.
func pollFollowerJoined(ctx context.Context, logger *slog.Logger, masterID string, sl boxapi.ZoneMember, fetch followerZoneFetch, timing followerVerifyTiming) bool {
	deadline := time.Now().Add(timing.perFollowerBudget)
	var lastSelfMaster string
	var lastMembers int
	var lastErr error
	for {
		cctx, cancel := context.WithTimeout(ctx, timing.perCallTimeout)
		fz, ferr := fetch(cctx, sl.IP)
		cancel()
		if ferr != nil {
			lastErr = ferr
		} else {
			lastErr = nil
			lastSelfMaster = fz.Master
			lastMembers = len(fz.Members)
			if fz.Master != "" && strings.EqualFold(fz.Master, masterID) {
				logger.Info("zone: follower confirmed", "follower", sl.DeviceID, "ip", sl.IP, "selfMaster", lastSelfMaster)
				return true
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(timing.pollInterval):
		}
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr != nil {
		logger.Info("zone: follower never confirmed (self-report unreachable)", "follower", sl.DeviceID, "ip", sl.IP, "err", lastErr.Error())
	} else {
		logger.Info("zone: follower never confirmed", "follower", sl.DeviceID, "ip", sl.IP, "selfMaster", lastSelfMaster, "selfMembers", lastMembers)
	}
	return false
}

// localDeviceID returns this box's authoritative Bose SoundTouch deviceID, read
// from the local firmware /info, falling back to supplied when /info is
// unreachable or carries no deviceID. The zone protocol (/setZone, /addGroup)
// keys on this exact ID. The desktop derives a member's deviceID from discovery,
// which on a two-chip chassis can be the wlan0 (SMSC) MAC instead of the
// SoundTouch (SCM) deviceID; since the master is always the box this agent runs
// on, the agent is the authority for its own ID and corrects the mismatch (#70).
func (s *Server) localDeviceID(ctx context.Context, c *boxapi.Client, supplied string) string {
	info, err := c.GetInfo(ctx)
	if err != nil {
		return supplied
	}
	real := strings.TrimSpace(info.DeviceID)
	if real == "" {
		return supplied
	}
	if supplied != "" && !strings.EqualFold(real, supplied) {
		s.logger.Info("zone: corrected master deviceID from firmware /info (app sent the chassis wlan0/SMSC MAC, not the SoundTouch ID)",
			"supplied", supplied, "firmware", real)
	}
	return real
}

// rememberMemberDeviceID records what a member speaker's own firmware answered
// for its deviceID, keyed by the IP it was asked on. Only ever called with a
// firmware answer, so the cache cannot be poisoned by a caller-supplied value.
func (s *Server) rememberMemberDeviceID(ip, deviceID string) {
	if ip == "" || deviceID == "" {
		return
	}
	s.memberIDsMu.Lock()
	defer s.memberIDsMu.Unlock()
	if s.memberIDs == nil {
		s.memberIDs = make(map[string]string)
	}
	s.memberIDs[ip] = deviceID
}

// cachedMemberDeviceID returns the last deviceID a speaker at this IP reported
// for itself, and whether there was one. Used when its /info does not answer in
// time during zone forming, so a busy member is still enrolled under the ID its
// firmware actually keys on.
func (s *Server) cachedMemberDeviceID(ip string) (string, bool) {
	s.memberIDsMu.RLock()
	defer s.memberIDsMu.RUnlock()
	v, ok := s.memberIDs[ip]
	return v, ok
}
