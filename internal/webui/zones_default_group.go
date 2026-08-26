package webui

// The permanent ("default") group. A firmware zone dies with every reboot and
// every standby, and the persisted zone document (internal/zones) so far fed
// only the guarded mirror re-push and an opt-in native tick that stayed off
// (zoneReconcileEnabled). This file makes the document what users keep asking
// for (#70; mail 2026-08-04; again 2026-08-26): the group the user formed IS
// the default group, and whenever its master starts music, the master re-forms
// it and wakes the stored members.
//
// The trigger is the play kick (kickMirrorAfterPlay), never a timer: an idle
// fleet is never touched, and the objection that killed the periodic native
// re-assert (solo speakers dragged back into the group every five minutes,
// Albrecht/Michal 2026-06-19) is answered by classifyMemberForRejoin below: a
// member that is deliberately playing its own source, or that belongs to
// another group, stays out. Dissolving the group deletes the document, which
// is the opt-out. Waking members on a play is sanctioned by design (Jens,
// 2026-08-26); preventing their later standby stays forbidden
// (feedback_never_prevent_deep_sleep) and nothing here holds a member awake.

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// rejoinAction is what the play-triggered re-form does with one stored member.
type rejoinAction int

const (
	rejoinJoin            rejoinAction = iota // idle, or already ours: goes (back) into the zone
	rejoinWake                                // standby: wake it first, then join
	rejoinSkipSolo                            // deliberately playing its own source: leave it alone
	rejoinSkipOtherZone                       // grouped under another master: not ours to take
	rejoinSkipUnreachable                     // not answering: nothing to do this round
)

func (a rejoinAction) String() string {
	switch a {
	case rejoinJoin:
		return "join"
	case rejoinWake:
		return "wake+join"
	case rejoinSkipSolo:
		return "skip-solo"
	case rejoinSkipOtherZone:
		return "skip-other-zone"
	default:
		return "skip-unreachable"
	}
}

// classifyMemberForRejoin decides from a member's own now_playing (source +
// play status) and its own zone master. Pure function: this is the decision
// the old periodic re-assert got wrong when it dragged solo players back.
func classifyMemberForRejoin(npSource, playStatus, memberZoneMaster, ourMaster string) rejoinAction {
	if npSource == "" {
		return rejoinSkipUnreachable
	}
	if npSource == "STANDBY" {
		return rejoinWake
	}
	if memberZoneMaster != "" {
		if memberZoneMaster == ourMaster {
			return rejoinJoin // already (partly) ours; the re-assert repairs it
		}
		return rejoinSkipOtherZone
	}
	// Standalone. Actually rendering audio means somebody chose that on
	// purpose; a source that merely sits selected but stopped is idle enough.
	if playStatus == "PLAY_STATE" || playStatus == "BUFFERING_STATE" {
		return rejoinSkipSolo
	}
	return rejoinJoin
}

// Seams for the tests: every real implementation reaches a speaker on a fixed
// port, which a test server on a random port can never be (same pattern as
// hushforupload.go and dissolvestragglers.go).
var (
	rejoinReadNowPlaying = func(ctx context.Context, ip string) nowPlayingSnapshot {
		return fetchNowPlaying(ctx, ip)
	}
	rejoinReadZoneMaster = func(ctx context.Context, ip string) string {
		z, err := boxapi.New(ip).GetZone(ctx)
		if err != nil {
			return ""
		}
		return z.Master
	}
	rejoinWakeMember = func(ctx context.Context, ip string, logger *slog.Logger) {
		wakeMemberAgent(ctx, ip, logger)
	}
	rejoinLiveZone = func(ctx context.Context, host string) (boxapi.Zone, error) {
		return boxapi.New(host).GetZone(ctx)
	}
	rejoinSetZone = func(ctx context.Context, host string, master boxapi.ZoneMember, slaves []boxapi.ZoneMember) error {
		return boxapi.New(host).SetZone(ctx, master, slaves)
	}
)

// wakeMemberAgent wakes one stored member through its own agent
// (POST /api/box/wake, the same TAP wake the desktop app uses when enrolling a
// switched-off member, #70). The chassis decides the agent port, so both are
// tried; waking an already-awake box is a fast no-op on the member side.
func wakeMemberAgent(ctx context.Context, ip string, logger *slog.Logger) {
	for _, port := range []string{"17008", "8888"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"http://"+net.JoinHostPort(ip, port)+"/api/box/wake", nil)
		if err != nil {
			continue
		}
		resp, err := wakeMemberClient.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			logger.Info("default group: member woken for the group", "ip", ip, "port", port)
			return
		}
	}
	// Not fatal: an old agent without /api/box/wake still joins the zone once
	// the firmware pulls it in, it just starts silent until used (#70).
	logger.Info("default group: member wake not confirmed, joining it anyway", "ip", ip)
}

// wakeMemberClient allows the full WakeAndWait on the member side (up to ~10 s)
// without stalling forever on a dead address.
var wakeMemberClient = &http.Client{Timeout: 12 * time.Second}

// defaultGroupFormCooldown keeps repeated play kicks (a user zapping through
// presets) from driving the firmware with back-to-back setZone rounds.
const defaultGroupFormCooldown = 30 * time.Second

// formDefaultGroupOnPlay re-forms the persisted group because this master just
// started music. Wakes stored members in parallel, skips members that are
// deliberately elsewhere, then one native setZone when the live zone does not
// already carry every qualifying member.
func (s *Server) formDefaultGroupOnPlay(z zones.Zone) {
	if !z.Permanent {
		// Opt-in (Jens, 2026-08-26): a group formed without the permanent
		// choice keeps the old behaviour, nothing re-forms or wakes on play.
		return
	}
	s.defaultFormMu.Lock()
	if time.Since(s.lastDefaultFormAt) < defaultGroupFormCooldown {
		s.defaultFormMu.Unlock()
		return
	}
	s.lastDefaultFormAt = time.Now()
	s.defaultFormMu.Unlock()

	// Own context: member wakes legitimately take ~10 s each (in parallel),
	// and the kick that got us here has long returned.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	type verdict struct {
		m   zones.Member
		act rejoinAction
	}
	verdicts := make([]verdict, len(z.Slaves))
	var wg sync.WaitGroup
	for i, m := range z.Slaves {
		wg.Add(1)
		go func(i int, m zones.Member) {
			defer wg.Done()
			mctx, mcancel := context.WithTimeout(ctx, 20*time.Second)
			defer mcancel()
			np := rejoinReadNowPlaying(mctx, m.IP)
			act := classifyMemberForRejoin(np.Source, np.PlayStatus, rejoinReadZoneMaster(mctx, m.IP), z.Master)
			if act == rejoinWake {
				rejoinWakeMember(mctx, m.IP, s.logger)
				act = rejoinJoin
			}
			verdicts[i] = verdict{m: m, act: act}
		}(i, m)
	}
	wg.Wait()

	joiners := make([]boxapi.ZoneMember, 0, len(z.Slaves))
	for _, v := range verdicts {
		s.logger.Info("default group: member classified", "ip", v.m.IP, "action", v.act.String())
		if v.act == rejoinJoin {
			joiners = append(joiners, boxapi.ZoneMember{DeviceID: v.m.DeviceID, IP: v.m.IP})
		}
	}
	if len(joiners) == 0 {
		return
	}
	// Already complete? Then the firmware is left alone.
	if live, err := rejoinLiveZone(ctx, s.boxHost); err == nil && live.Master == z.Master {
		have := make(map[string]bool, len(live.Members))
		for _, m := range live.Members {
			have[m.DeviceID] = true
		}
		complete := true
		for _, j := range joiners {
			if !have[j.DeviceID] {
				complete = false
				break
			}
		}
		if complete {
			return
		}
	}
	master := boxapi.ZoneMember{DeviceID: z.Master, IP: z.MasterIP}
	if err := rejoinSetZone(ctx, s.boxHost, master, joiners); err != nil {
		s.logger.Warn("default group: re-form on play failed", "err", err, "members", len(joiners))
		return
	}
	s.logger.Info("default group: re-formed on play", "members", len(joiners))
}

// wakeStoredMembersForPlay wakes every stored member that reports STANDBY, in
// parallel, and returns once they answered or timed out. The mirror pass that
// follows then re-points the just-woken members in the same round. Members
// that are awake, busy or unreachable are left exactly as they are.
func (s *Server) wakeStoredMembersForPlay(z zones.Zone) {
	if !z.Permanent {
		return // opt-in, see formDefaultGroupOnPlay
	}
	s.defaultFormMu.Lock()
	if time.Since(s.lastDefaultFormAt) < defaultGroupFormCooldown {
		s.defaultFormMu.Unlock()
		return
	}
	s.lastDefaultFormAt = time.Now()
	s.defaultFormMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, m := range z.Slaves {
		wg.Add(1)
		go func(m zones.Member) {
			defer wg.Done()
			mctx, mcancel := context.WithTimeout(ctx, 20*time.Second)
			defer mcancel()
			if rejoinReadNowPlaying(mctx, m.IP).Source == "STANDBY" {
				rejoinWakeMember(mctx, m.IP, s.logger)
			}
		}(m)
	}
	wg.Wait()
}

// KickDefaultGroup is the exported play trigger for callers outside the
// package (the gabbo handler firing on a hardware-key or Connect start).
// Same debounce as every app-driven play.
func (s *Server) KickDefaultGroup() { s.kickMirrorAfterPlay() }
