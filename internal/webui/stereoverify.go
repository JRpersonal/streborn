package webui

import (
	"context"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// Verifying a stereo pair on BOTH halves before calling it formed.
//
// /addGroup answered 200 and the master read back two members on two SoundTouch
// 10s (2026-10-04), so the pairing reported ok:true. The partner had stored
// nothing (its /getGroup stayed <group />) and the master's own status stayed
// GROUP_ERROR for as long as anybody looked. That is a one-sided pair from the
// first second: the master waits for a partner that never joined and refuses to
// play on its own, which is exactly the state users then find their speakers in
// weeks later without knowing how it happened.
//
// So the pair is read back from both firmwares. The partner's :8090 is reachable
// from the agent even between series-I speakers, where only the agent ports are
// firewalled (healStaleStereoGroups relies on the same).

// stereoVerdict is the outcome of verifyStereoPair.
type stereoVerdict struct {
	OK              bool
	PartnerVerified bool   // false: the partner could not be read, the master looked healthy
	Reason          string // "partnerUnreachable" | "partnerDidNotStore" when !OK
	MasterStatus    string
}

type groupFetch func(ctx context.Context, host string) (boxapi.Group, error)

type stereoVerifyTiming struct {
	budget time.Duration
	gap    time.Duration
}

var defaultStereoVerifyTiming = stereoVerifyTiming{budget: 15 * time.Second, gap: 2 * time.Second}

// pairNames reports whether g is a complete pair containing both device ids.
func pairNames(g boxapi.Group, a, b string) bool {
	if len(g.Members) != 2 {
		return false
	}
	seenA, seenB := false, false
	for _, m := range g.Members {
		if strings.EqualFold(m.DeviceID, a) {
			seenA = true
		}
		if strings.EqualFold(m.DeviceID, b) {
			seenB = true
		}
	}
	return seenA && seenB
}

// verifyStereoPair polls both halves until the pair is confirmed on both or the
// budget runs out. A master reporting GROUP_ERROR at the end means the two
// firmwares could not complete the pair between them; a partner that answers but
// never lists the pair stored nothing. A partner that cannot be read at all is
// not held against the pair when the master is healthy: the verdict then says
// so (PartnerVerified false) instead of guessing.
func verifyStereoPair(ctx context.Context, fetch groupFetch, masterHost, partnerHost, masterID, partnerID string, timing stereoVerifyTiming) stereoVerdict {
	deadline := time.Now().Add(timing.budget)
	var (
		masterG       boxapi.Group
		masterErr     error
		partnerG      boxapi.Group
		partnerErr    error
		partnerAnswer bool
	)
	for {
		mctx, mcancel := context.WithTimeout(ctx, 4*time.Second)
		masterG, masterErr = fetch(mctx, masterHost)
		mcancel()
		if partnerHost != "" {
			pctx, pcancel := context.WithTimeout(ctx, 4*time.Second)
			partnerG, partnerErr = fetch(pctx, partnerHost)
			pcancel()
			if partnerErr == nil {
				partnerAnswer = true
			}
		}
		masterOK := masterErr == nil && pairNames(masterG, masterID, partnerID) &&
			!strings.EqualFold(masterG.Status, "GROUP_ERROR")
		partnerOK := partnerErr == nil && pairNames(partnerG, masterID, partnerID)
		if masterOK && partnerOK {
			return stereoVerdict{OK: true, PartnerVerified: true, MasterStatus: masterG.Status}
		}
		if time.Now().Add(timing.gap).After(deadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(timing.gap):
		}
	}
	status := strings.TrimSpace(masterG.Status)
	switch {
	case masterErr == nil && strings.EqualFold(status, "GROUP_ERROR"):
		return stereoVerdict{Reason: "partnerUnreachable", MasterStatus: status}
	case partnerAnswer && partnerErr == nil && !pairNames(partnerG, masterID, partnerID):
		return stereoVerdict{Reason: "partnerDidNotStore", MasterStatus: status}
	case masterErr == nil && pairNames(masterG, masterID, partnerID):
		// Master healthy, partner never readable: cannot prove it either way.
		return stereoVerdict{OK: true, PartnerVerified: false, MasterStatus: status}
	default:
		return stereoVerdict{Reason: "partnerUnreachable", MasterStatus: status}
	}
}

// stereoFailureText is the message the app shows for a pair that did not form
// on both speakers.
func stereoFailureText(reason string) string {
	if reason == "partnerDidNotStore" {
		return "the second speaker did not join the pair, so it was undone again. Check that both speakers are on the same network and try again."
	}
	return "the two speakers could not reach each other, so the pair was undone again. They need to be on the same network and able to talk to each other directly."
}

// fetchGroupVia is the production group read.
func fetchGroupVia(ctx context.Context, host string) (boxapi.Group, error) {
	return boxapi.New(host).GetGroup(ctx)
}

// stereoGroupFetch returns the test seam or the production read.
func (s *Server) stereoGroupFetch() groupFetch {
	if s.stereoVerifyFetch != nil {
		return s.stereoVerifyFetch
	}
	return fetchGroupVia
}

func stereoVerifyTimingFor(s *Server) stereoVerifyTiming {
	if s.stereoVerifyTiming != nil {
		return *s.stereoVerifyTiming
	}
	return defaultStereoVerifyTiming
}

// undoUnconfirmedPair takes back a pair that did not form on both speakers:
// both firmwares (whichever half stored it), this box's marge record, the
// partner's marge record and the pair document in the zone store. A half-formed
// pair left in place is the stuck state this check exists to prevent. Returns
// whether the partner's marge record was cleared directly (the app relays the
// clear otherwise, as on a normal dissolve).
func (s *Server) undoUnconfirmedPair(ctx context.Context, c *boxapi.Client, partner boxapi.ZoneMember) bool {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := c.RemoveGroup(rctx); err != nil {
		s.logger.Warn("stereo: undoing the unconfirmed pair on this speaker failed", "err", err)
	}
	if partner.IP != "" {
		pc := boxapi.New(partner.IP)
		if pg, err := pc.GetGroup(rctx); err == nil && (pg.ID != "" || len(pg.Members) > 0) {
			if err := pc.RemoveGroup(rctx); err != nil {
				s.logger.Warn("stereo: undoing the unconfirmed pair on the partner failed", "err", err, "partnerIP", partner.IP)
			}
		}
	}
	if s.margeGroupClear != nil {
		s.margeGroupClear("the pair did not form on both speakers")
	}
	cleared := s.pushGroupDocToPartner(rctx, partner.IP, "")
	s.dropStereoDocAfterFailure("the pair did not form on both speakers")
	return cleared
}
