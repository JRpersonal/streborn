package webui

// A group can end without anybody pressing "dissolve": the master's firmware
// drops followers that stop answering (a follower on a bad Wi-Fi minute) and
// reports the zone gone. Nothing then tells those followers to stop, and a
// follower that reconnects a moment later carries on with the programme it
// had, alone in its room, while its own state says STANDBY to every app.
//
// A field bundle of 2026-09-07 shows exactly that on two ST10s: the master
// (a music library) dropped them three times in fifteen minutes, both played
// on, and the owner's report was "the speakers play although they are not
// grouped; the app shows them in standby". The only sweep that existed ran on
// the user's own dissolve, and by then the followers were on the track before
// the master's and read as "playing something else".
//
// So a firmware dissolve schedules the same straggler sweep, delayed and
// re-checked: the firmware also announces a dissolve on its way THROUGH a
// group change (/setZone tears the old zone down first), and a sweep fired at
// that moment would silence members the re-form is about to serve. Each pass
// therefore waits, then runs only while the stored document is unchanged, the
// firmware still reports no zone, and no form/dissolve drive holds the serial.

import (
	"context"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// firmwareDissolveSweepDelays are the waits before each sweep pass, measured
// from the previous pass. The first pass sits well past the ~300 ms self-
// dissolve of a fresh zone (#70) and a /setZone rebuild; the later passes
// catch a follower that was unreachable (the very Wi-Fi drop that cost it the
// zone) when the first pass looked.
var firmwareDissolveSweepDelays = []time.Duration{20 * time.Second, 40 * time.Second, 2 * time.Minute}

// Seams for the tests: the firmware zone read and the sleep.
var (
	firmwareZoneFn = func(ctx context.Context, host string) (boxapi.Zone, error) {
		return boxapi.New(host).GetZone(ctx)
	}
	dissolveSweepSleep = time.Sleep
	// leaveZoneAtFollowerFn posts the teardown AT THE FOLLOWER. Every other
	// teardown in the codebase posts it at the master, which is a no-op once
	// the master's firmware has forgotten the zone, and that is precisely the
	// state a stale membership survives in.
	leaveZoneAtFollowerFn = func(ctx context.Context, followerIP string, master, follower boxapi.ZoneMember) error {
		return boxapi.New(followerIP).RemoveZoneSlave(ctx, master, []boxapi.ZoneMember{follower})
	}
)

// staleMembershipBudget is the per-follower time for the read and the write.
// Short on purpose: this runs over speakers that may be asleep or, between two
// rhino ST10s, unreachable from here at all, and none of them is worth waiting
// on. A follower missed now is looked at again on the next pass.
const staleMembershipBudget = 4 * time.Second

// scheduleStragglerSweepAfterFirmwareDissolve starts the delayed sweep for the
// stored group z (already known to be a native multiroom document). The
// master's location is captured NOW, while it still names the programme the
// followers were given; by the time a pass runs the master may have moved on.
func (s *Server) scheduleStragglerSweepAfterFirmwareDissolve(z zones.Zone) {
	if len(z.Slaves) == 0 {
		return
	}
	if !s.dissolveSweepBusy.CompareAndSwap(false, true) {
		return // a sweep for this collapse is already pending
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	masterLocation := playingLocation(ctx, s.boxHost)
	cancel()
	go func() {
		defer s.dissolveSweepBusy.Store(false)
		s.runStragglerSweepAfterFirmwareDissolve(zoneDocFingerprint(z), z.Master, masterLocation, z.Slaves)
	}()
}

// runStragglerSweepAfterFirmwareDissolve is the pass loop. It returns after the
// first pass that actually swept, or once the group is back or gone for good.
func (s *Server) runStragglerSweepAfterFirmwareDissolve(fingerprint, masterID, masterLocation string, slaves []zones.Member) {
	if masterLocation == "" {
		// No programme to compare a follower's audio against, so the playback
		// sweep below cannot run: silencing a speaker that legitimately plays
		// something of its own is the worse mistake of the two.
		//
		// The MEMBERSHIP still has an answer, and leaving it alone is what
		// produced the ghost: a follower whose firmware still names this master
		// goes on powering off with it and drifting behind it, while no app
		// shows it as grouped. Clearing that touches no audio.
		s.clearStaleZoneMembership(masterID, slaves)
		return
	}
	members := make([]boxapi.ZoneMember, 0, len(slaves))
	for _, m := range slaves {
		members = append(members, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP, Role: m.Role})
	}
	for i, delay := range firmwareDissolveSweepDelays {
		dissolveSweepSleep(delay)
		if s.zones == nil {
			return
		}
		z, ok := s.zones.Get()
		if !ok {
			// STR itself dissolved the group meanwhile; that path ran its
			// own sweep.
			return
		}
		if zoneDocFingerprint(z) != fingerprint {
			// A different group was formed since; it takes care of itself.
			return
		}
		if !s.zoneFormSerial.TryLock() {
			// A form or dissolve drive is running right now: not the moment
			// to touch members. The next pass looks again.
			continue
		}
		swept := s.sweepIfFirmwareZoneStillGone(masterLocation, members, i)
		s.zoneFormSerial.Unlock()
		if swept {
			return
		}
	}
}

// sweepIfFirmwareZoneStillGone reads the master's live zone and sweeps only when
// the firmware still reports none. It reports whether a sweep ran.
func (s *Server) sweepIfFirmwareZoneStillGone(masterLocation string, members []boxapi.ZoneMember, pass int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), stragglerStopBudget)
	defer cancel()
	fz, err := firmwareZoneFn(ctx, s.boxHost)
	if err != nil {
		s.logger.Info("zone: firmware dissolve sweep skipped, the master's zone is unreadable", "pass", pass, "err", err)
		return false
	}
	if fz.Master != "" && len(fz.Members) > 0 {
		s.logger.Info("zone: the group is live again after the firmware's dissolve, no sweep", "pass", pass, "members", len(fz.Members))
		return true
	}
	s.logger.Info("zone: the firmware dropped the group by itself, sweeping followers still carrying its programme",
		"pass", pass, "followers", len(members))
	s.stopStragglers(ctx, masterLocation, "", members)
	return true
}

// clearStaleZoneMembership tells each follower that still names this master to
// leave, once the master's own firmware has reported the zone gone.
//
// Read before write, and only on positive evidence. A follower that does not
// answer is left alone, because unreachable is not the same as wrong, and one
// that names a different master has joined somebody else's group since and is
// none of our business. Nothing in here calls playback: the worst case for a
// wrong guess is a membership the firmware had already dropped being dropped a
// second time.
func (s *Server) clearStaleZoneMembership(masterID string, slaves []zones.Member) {
	masterID = strings.TrimSpace(masterID)
	if masterID == "" {
		return
	}
	master := boxapi.ZoneMember{DeviceID: masterID, IP: s.boxHost}
	cleared, checked := 0, 0
	for _, m := range slaves {
		ip := strings.TrimSpace(m.IP)
		if ip == "" || ip == s.boxHost {
			continue
		}
		checked++
		ctx, cancel := context.WithTimeout(context.Background(), staleMembershipBudget)
		fz, err := firmwareZoneFn(ctx, ip)
		if err != nil {
			s.logger.Info("zone: a follower did not answer the stale-membership check, left as it is",
				"follower", ip, "err", err)
			cancel()
			continue
		}
		switch {
		case strings.TrimSpace(fz.Master) == "":
			cancel()
			continue // it already knows the group is gone
		case !strings.EqualFold(strings.TrimSpace(fz.Master), masterID):
			s.logger.Info("zone: a follower belongs to another group now, left alone",
				"follower", ip, "itsMaster", fz.Master)
			cancel()
			continue
		}
		self := boxapi.ZoneMember{DeviceID: strings.TrimSpace(m.DeviceID), IP: ip, Role: m.Role}
		if err := leaveZoneAtFollowerFn(ctx, ip, master, self); err != nil {
			s.logger.Info("zone: could not clear a follower's stale group membership",
				"follower", ip, "err", err)
			cancel()
			continue
		}
		cleared++
		s.logger.Info("zone: cleared a follower's stale group membership, the master's firmware had already dropped the group",
			"follower", ip)
		cancel()
	}
	if checked > 0 {
		s.logger.Info("zone: stale-membership pass done", "checked", checked, "cleared", cleared)
	}
}
