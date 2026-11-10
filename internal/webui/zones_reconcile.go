// The mirror push to the members of a mirror group and the periodic
// reconcile that re-forms a stored group. Split out of zones_stereo.go.

package webui

import (
	"context"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/upnp"
	"github.com/JRpersonal/streborn/internal/zones"
)

// mirrorToSlaves points each slave's box at the master's current stream URL over
// UPnP (the mirror path). The master's stream is whatever STR last told the
// master box to play (s.lastPlay), which the slaves can pull from the master
// agent's stream proxy. Looser than firmware sync, but works when the firmware
// refuses to distribute STR's source. Best-effort + heavily logged.
//
// reconcile marks the periodic 5-minute re-form (PeriodicZoneReconcile) as
// opposed to the user just having formed the group. The reconcile must only
// repair what is actually broken: lastPlay is PERSISTED across reboots, so
// without state guards an idle or standby master sprayed its stale last stream
// onto every slave each tick — a slave busy with a Spotify playlist was yanked
// to the master's old radio station every 5 minutes (#342), and a healthy
// mirroring slave was re-pushed into a re-buffer hiccup each tick. With
// reconcile set, the master must be actively playing the mirrored stream, and
// each slave is only (re)pointed per slaveMirrorAction (never woken from
// standby, never taken off another source it is playing).
func (s *Server) mirrorToSlaves(ctx context.Context, z zones.Zone, reconcile bool) {
	lp := s.play.current()
	if lp == nil || lp.boxURL == "" {
		s.logger.Info("zone mirror: master is not playing yet; slaves will mirror once you start playback and the reconcile fires (beta)")
		return
	}
	// A deliberate form/remove must NOT resurrect playback the user stopped.
	// lastPlay outlives a stop, so pushing it here on the unconditional
	// (reconcile=false) path restarted a group the user had just stopped:
	// stopping a mirror group and then removing one slave restarted the stream
	// on ALL members including the removed one (live, Portable master + 2 ST10,
	// 2026-07-10). Only push when the master is actually playing its stream and
	// no user stop is in effect; otherwise just update the membership silently.
	// The reconcile=true path has its own per-box now-playing guards below.
	if !reconcile {
		if standby, busy := s.boxPlayState(); standby || !busy || s.userStoppedRecently() {
			s.logger.Info("zone mirror: master is stopped, updating group membership without restarting playback (beta)")
			return
		}
	}
	if reconcile {
		np := s.snapshotNowPlaying(ctx)
		if reason := masterMirrorSkipReason(np, lp.boxURL); reason != "" {
			s.logMirrorSkip("master", reason)
			return
		}
		s.clearMirrorSkip("master")
	}
	// lp.boxURL points the MASTER's own box at its loopback stream proxy
	// (http://127.0.0.1:8888/...). A slave cannot fetch that; it must reach the
	// master across the LAN. Rewrite the host to the master's LAN IP so each
	// slave pulls the master's stream (#70: the slave's display updated but its
	// audio kept its old stream because it was handed the master's loopback URL).
	slaveURL := s.mirrorURLForSlaves(ctx, lp.boxURL, z.MasterIP)
	for _, m := range z.Slaves {
		if m.IP == "" {
			continue
		}
		if reconcile {
			push, reason := slaveMirrorAction(fetchNowPlaying(ctx, m.IP), slaveURL)
			if !push {
				s.logMirrorSkip("slave "+m.IP, reason)
				continue
			}
			s.clearMirrorSkip("slave " + m.IP)
			s.logger.Info("zone mirror: re-forming slave (beta)", "slave", m.IP, "reason", reason)
		}
		rr := upnp.NewBoseRenderer(m.IP)
		var err error
		if lp.mime != "" {
			err = rr.PlayURLMime(ctx, slaveURL, lp.title, lp.art, lp.mime)
		} else {
			err = rr.PlayURL(ctx, slaveURL, lp.title, lp.art)
		}
		if err != nil {
			s.logger.Warn("zone mirror: slave play failed", "slave", m.IP, "err", err)
		} else {
			s.logger.Info("zone mirror: slave mirroring master stream (beta)", "slave", m.IP, "url", slaveURL)
		}
	}
}

// mirrorStreamPort is the port a SLAVE box uses to reach the master agent's
// stream proxy. The proxy listens on :8888, but a remote box cannot use that
// directly: on a BCO/whitelisted chassis (ST20 spotty/scm, Portable) the SMSC
// chipset drops an external :8888 connection, routing external TCP only to
// Bose-binary-owned listeners. Every chassis instead REDIRECTs :17008
// (SoftwareUpdate, whitelisted) to the agent's loopback :8888, which is exactly
// how the desktop app already reaches every box, so the mirror uses it too.
const mirrorStreamPort = "17008"

// mirrorURLForSlaves rewrites the master's own loopback stream URL
// (http://127.0.0.1:8888/...) into one a SLAVE box can fetch over the LAN: the
// master's LAN IP on the externally reachable :17008 redirect (mirrorStreamPort).
// masterIP comes from the persisted zone; when it is empty we fall back to the
// firmware /info IP. If no LAN IP can be resolved we return the URL unchanged
// (a no-op push beats pointing a slave at the wrong host).
func (s *Server) mirrorURLForSlaves(ctx context.Context, boxURL, masterIP string) string {
	u, err := url.Parse(boxURL)
	if err != nil {
		return boxURL
	}
	if strings.TrimSpace(masterIP) == "" {
		if info, ierr := boxapi.New(s.boxHost).GetInfo(ctx); ierr == nil {
			masterIP = strings.TrimSpace(info.IP)
		}
	}
	if masterIP == "" {
		return boxURL
	}
	u.Host = net.JoinHostPort(masterIP, mirrorStreamPort)
	return u.String()
}

// defaultZoneReconcilePath is the NAND flag file that opts a box INTO the
// periodic zone reconcile (#70 beta). Absent (the default) means OFF: the box
// never re-asserts a persisted native zone, so a speaker the user plays on its
// own is never dragged back into a group. Only an explicit "1"/"true"/"on"/"yes"
// turns it on. The default is OFF after multi-speaker users (Albrecht 5-box,
// Michal multi-ST10, 2026-06-19) reported standalone speakers being pulled into
// the master's zone every few minutes: when a member leaves to play its own
// source the master's match-before-assert guard sees a missing member and
// re-asserts setZone, dragging it back. On 0.8.x the native setZone does not even
// distribute (slaves never join, "master read-back empty"), so the periodic
// re-assert is pure churn with a real downside and no upside. Re-enable per box
// once the native path is verified on hardware (#70).
const defaultZoneReconcilePath = "/mnt/nv/streborn/zone-reconcile"

// zoneReconcileEnabled reports whether the periodic NATIVE zone re-assert runs on
// this box. Default OFF (opt-in): the flag file must explicitly say
// "1"/"true"/"on"/"yes" to turn it on. See defaultZoneReconcilePath for why the
// default flipped to OFF. A mirror zone is not gated here: its re-push has its
// own per-tick state guards (see mirrorToSlaves/slaveMirrorAction — the master
// must be actively playing, and standby or busy slaves are left alone, #342),
// so this gate only governs the broken/harmful native re-assert.
func (s *Server) zoneReconcileEnabled() bool {
	return readBoolFlag(defaultZoneReconcilePath, false) // default OFF (opt-in)
}

// PeriodicZoneReconcile re-pushes a persisted mirror group so it survives
// reboot/standby/Wi-Fi outage (#70 beta), and re-asserts a native zone only when
// the box is opted in (see zoneReconcileEnabled, default OFF). No-op when
// standalone. Started by cmd/agent after the server is built. Lives on the Server
// so the mirror path can reach s.lastPlay + the UPnP renderer.
func (s *Server) PeriodicZoneReconcile() {
	if s.zones == nil || s.boxHost == "" {
		return
	}
	time.Sleep(45 * time.Second) // let the box finish booting
	s.reconcileZoneOnce(false)
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.reconcileZoneOnce(false)
		case <-s.mirrorKick:
			// A play kick: the master just started music, which is the one
			// moment the persisted group re-forms with full force (members
			// woken, zone re-asserted). The tick above stays hands-off.
			s.reconcileZoneOnce(true)
		}
	}
}

// kickMirrorAfterPlay asks for an out-of-turn reconcile shortly after a fresh
// play, so the other speakers in a group join within seconds.
//
// Waiting for the 5-minute tick is what users experience as losing their
// group. The speakers come out of standby, the user presses play on the main
// one, and for up to five minutes it is the only one playing - long enough
// that people conclude the group is gone, start the desktop app and build it
// again. It is asked about constantly ("can the group be stored permanently on
// the speakers so I don't have to start the PC after every standby", 2026-08-04).
//
// The delay lets the master's stream actually start: the reconcile requires the
// master to be audibly playing the stream it was told to play, and a speaker
// reports the new stream in now_playing a few seconds after the push.
//
// Nothing here weakens the #342 guards. This only changes WHEN a round runs;
// which speakers it touches is still slaveMirrorAction's decision, so a speaker
// in standby is left asleep and one playing its own source is left alone.
func (s *Server) kickMirrorAfterPlay() {
	if s.zones == nil || s.mirrorKick == nil {
		return
	}
	// An alarm is per speaker: it wakes the bedroom, not the house. The hush is
	// checked here rather than at the send because BOTH kick paths funnel
	// through this one function - this in-process call from setLastPlay, and
	// the firmware's own "started playing" event over gabbo (KickDefaultGroup,
	// wired as onSourcePlaying) - so one check covers both. It suppresses only
	// the automatic re-form; the stored group document is never touched.
	if s.groupFormHushed() {
		s.logger.Info("group re-form: hushed, leaving the other speakers asleep")
		return
	}
	if z, ok := s.zones.Get(); !ok || z.Stereo {
		// Standalone, or a stereo pair (the firmware persists a pair itself).
		// Native zones pass since the default group (#70): the play kick is
		// their re-form trigger too, routed in reconcileZoneOnce. The first
		// live test (2026-08-26, .59 master) died on the old !z.Mirror() gate
		// here: the members stayed in standby because the kick never left.
		return
	}
	// One pending kick at a time. Skipping a play that lands inside the window
	// loses nothing: the round reads the speaker's live state when it runs, so
	// it acts on the LATEST stream either way. Deduplicating here rather than
	// at the send is what keeps a burst of plays (a user stepping through
	// presets) to a single reconcile.
	if !s.mirrorKickPending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		time.Sleep(s.zoneTimes().mirrorKickDelay)
		s.mirrorKickPending.Store(false)
		select {
		case s.mirrorKick <- struct{}{}:
		default: // a round is already queued and has not started yet
		}
	}()
}

func (s *Server) reconcileZoneOnce(playKick bool) {
	z, ok := s.zones.Get()
	if !ok {
		return // standalone
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if z.Stereo {
		// A left/right stereo pair is a firmware-native group, not a multiroom
		// zone. Re-asserting it with the zone API (/setZone) would use the wrong
		// endpoint and could fight the firmware's own pairing, so leave a native
		// stereo pair alone; the firmware persists it across reboot/standby itself.
		return
	}
	if z.Mirror() {
		if playKick {
			// The master just started music: the stored members come along,
			// standby ones included (the default-group design, 2026-08-26).
			// The wake runs before the mirror pass so a just-woken member is
			// re-pointed in the same round instead of at the next tick.
			s.wakeStoredMembersForPlay(z)
		}
		// Re-form the mirror group, guarded (best-effort): the master must be
		// actively playing the mirrored stream, and a slave is only (re)pointed
		// when it is idle, dropped off the mirror, or on a stale master stream.
		// A busy speaker is left alone — the unguarded version of this path
		// hijacked a slave's Spotify playback with the master's persisted last
		// station every 5 minutes (#342). Not gated by the native opt-in below;
		// the guards make it safe on their own.
		s.mirrorToSlaves(ctx, z, true)
		return
	}
	if playKick {
		// Native default group: the play kick is the trigger the periodic
		// re-assert never had a safe answer for. Member classification keeps
		// deliberately-solo speakers out (zones_default_group.go).
		s.formDefaultGroupOnPlay(z)
		return
	}
	if z.Permanent {
		// The tick's one job for a permanent group: members that dropped out
		// of the LIVE group (an update reboot, a Wi-Fi hiccup) come back while
		// the master plays, with the same member classification as the play
		// kick and without waking anyone who was switched off on purpose.
		// When the zone is gone altogether (a lone follower rebooted, and the
		// master's firmware ended the zone with it), it is formed once more
		// while that member is fresh (#1339).
		if !s.rejoinMissingMembers(ctx, z) {
			s.reformAfterMemberReboot(ctx, z)
		}
		return
	}
	if !s.zoneReconcileEnabled() {
		// The periodic native re-assert stays opt-in (default OFF): on a tick
		// there is no user intent to lean on, and re-asserting whenever a
		// member is missing dragged solo speakers back. See zoneReconcileEnabled.
		return
	}
	// Native: only re-assert when the live zone does not already match.
	c := boxapi.New(s.boxHost)
	if live, err := c.GetZone(ctx); err == nil && live.Master == z.Master && len(live.Members) == len(z.Slaves) {
		return
	}
	master := boxapi.ZoneMember{DeviceID: z.Master, IP: z.MasterIP}
	slaves := make([]boxapi.ZoneMember, 0, len(z.Slaves))
	for _, m := range z.Slaves {
		slaves = append(slaves, boxapi.ZoneMember{DeviceID: m.DeviceID, IP: m.IP})
	}
	s.logger.Info("zone reconcile: re-asserting native zone (beta)", "master", z.Master, "slaves", len(slaves))
	if err := c.SetZone(ctx, master, slaves); err != nil {
		s.logger.Warn("zone reconcile: setZone failed", "err", err, "master", z.Master)
	}
}
