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
)

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
		s.runStragglerSweepAfterFirmwareDissolve(zoneDocFingerprint(z), masterLocation, z.Slaves)
	}()
}

// runStragglerSweepAfterFirmwareDissolve is the pass loop. It returns after the
// first pass that actually swept, or once the group is back or gone for good.
func (s *Server) runStragglerSweepAfterFirmwareDissolve(fingerprint, masterLocation string, slaves []zones.Member) {
	if masterLocation == "" {
		s.logger.Info("zone: firmware dissolve, the master plays nothing to compare against, followers are left alone")
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
