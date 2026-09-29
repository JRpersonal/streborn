package main

// A speaker that STR updates reboots, and a reboot wipes the firmware's
// multiroom zone. That is STR's own doing, so putting the zone back is STR's
// job.
//
// Field report, 2026-09-28 (five speakers): the user built a group of four at
// 09:31:58, STR started updating that very master 22 seconds later, and by the
// time the diagnostic was taken every box reported an empty zone with the three
// followers in standby. The master still listed them under "remembered", so STR
// knew exactly which speakers had just been taken out of the group and said
// nothing. From the user's chair: speakers added, speakers gone. He wrote that
// he could not get any speaker to play along.
//
// A PERMANENT group already survives this by re-forming on the master's next
// play, which is why this went unnoticed for so long: only an ordinary group is
// lost, and an ordinary group is what the multiroom screen creates by default.

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// zoneRecord is the live zone a speaker belonged to when STR decided to update
// it, flattened to the addresses FormZone needs to build it again.
type zoneRecord struct {
	// MasterIP is the zone leader's LAN address, which is NOT necessarily the
	// box being updated: a follower's zone names its leader in senderIP.
	MasterIP     string
	MasterDevice string
	Members      []ZoneMember
	// Permanent and Stereo are the two reasons NOT to touch a zone, recorded
	// so the decision can be made without another box round trip.
	Permanent bool
	Stereo    bool
	At        time.Time
}

// zonesBeforeOTA holds one record per host STR is currently updating. Keyed by
// the box being updated, not by the master, because that is the address the OTA
// flow has in hand on both legs.
var zonesBeforeOTA struct {
	sync.Mutex
	m map[string]zoneRecord
}

// zoneRecordTTL bounds how long a remembered zone stays worth acting on. An
// update that never confirmed leaves its record behind, and without this a
// later update of the same speaker in the same session would rebuild a group
// the user may have dissolved hours ago.
const zoneRecordTTL = 30 * time.Minute

// zoneRestorePlan is what should happen to a zone after the box came back, and
// why. The why is not decoration: it goes into the OTA journal, so a report
// three weeks later still says whether STR rebuilt a group, deliberately left
// it alone, or never saw one.
type zoneRestorePlan struct {
	Restore bool
	Why     string
}

// planZoneRestore is the whole decision, with no network in it, so the cases it
// has to get right are written down in otazonerestore_test.go rather than
// discovered in somebody's living room.
//
// before is the zone as it stood just before STR pushed the binary; now is what
// the leader reports once the box is back.
func planZoneRestore(before zoneRecord, now zoneRecord, masterPlaying bool) zoneRestorePlan {
	switch {
	case before.MasterIP == "" || len(before.Members) == 0:
		// The box was standing alone. Nothing was taken from the user, so
		// nothing is owed back. This is the overwhelmingly common case and it
		// must stay silent.
		return zoneRestorePlan{false, ""}
	case before.Stereo:
		// A stereo pair is not a zone: it lives in the marge group record and
		// has its own re-form path. Rebuilding it as a plain zone would leave a
		// pair playing in mono and a one-sided leftover to clean up.
		return zoneRestorePlan{false, "zone: the speaker was in a stereo pair, which re-forms through its own path; left alone"}
	case before.Permanent || now.Permanent:
		// A permanent group re-forms itself when the master next plays. Forming
		// it here as well would form it twice.
		//
		// The LEADER's flag matters as much as the updated speaker's. Updating a
		// FOLLOWER of a permanent group reads permanent=false from that follower,
		// and re-forming from there posts the group without Permanent and without
		// its Name, overwriting the leader's stored permanent template with an
		// ordinary one. The user would lose the durable choice by updating the
		// wrong speaker.
		return zoneRestorePlan{false, "zone: the group is a permanent one and re-forms itself on the next play; left alone"}
	case !masterPlaying:
		// Forming a zone WAKES the leader and every member, and a speaker woken
		// with an internet-radio preset as its last source resumes that preset by
		// itself. Two seconds later the group-forming code reads the leader as
		// audibly playing and hands its station to everyone (#975, still open in
		// the case where the leader cannot see that STR woke a member a moment
		// earlier). Rebuilding a silent group would therefore start music nobody
		// asked for, in every room, at whatever hour the update ran. A fleet
		// update once woke a household at 03:28 that way.
		//
		// So the group is rebuilt only while the leader is already audible: then
		// the zone spreads music that is playing anyway, and wakes nothing. A
		// silent group is left dissolved, which is the safe failure: nobody is
		// listening to it at that moment, and forming one costs a tap.
		return zoneRestorePlan{false, "zone: the group was dropped by the update reboot, but the speaker is silent; rebuilding it would wake every member and start playback nobody asked for, so it is left alone"}
	case len(now.Members) >= len(before.Members):
		// The zone came through the reboot. Some firmware does hold it, and a
		// speaker that kept its group must not be re-formed underneath the user.
		return zoneRestorePlan{false, fmt.Sprintf("zone: the group survived the update (%d of %d members still live); nothing to rebuild", len(now.Members), len(before.Members))}
	default:
		return zoneRestorePlan{true, fmt.Sprintf("zone: the update reboot dropped the group (%d of %d members left); rebuilding it from %s", len(now.Members), len(before.Members), before.MasterIP)}
	}
}

// audiblyPlaying reports whether a speaker's now_playing document describes
// sound coming out of it right now. Matched on the raw XML rather than parsed,
// because the field is a flat attribute and the document shape differs between
// firmware sources; a parse that silently yields the zero value would read as
// "not playing", which is the answer that suppresses the restore, so a parser
// mistake here would be invisible.
func audiblyPlaying(nowPlayingXML string) bool {
	return strings.Contains(nowPlayingXML, "PLAY_STATE") ||
		strings.Contains(nowPlayingXML, "BUFFERING_STATE")
}

// zoneRecordFromDoc reads what GetZoneState returned into a zoneRecord. host is
// the box that answered, which is how a leader is told from a follower: a
// leader is not listed among its own members, a follower is.
func zoneRecordFromDoc(doc map[string]any, host string) zoneRecord {
	rec := zoneRecord{}
	if doc == nil {
		return rec
	}
	if s, ok := doc["master"].(string); ok {
		rec.MasterDevice = s
	}
	if b, ok := doc["permanent"].(bool); ok {
		rec.Permanent = b
	}
	if _, ok := doc["stereo"]; ok {
		rec.Stereo = true
	}
	raw, _ := doc["members"].([]any)
	selfListed := false
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		ip, _ := m["ip"].(string)
		id, _ := m["deviceID"].(string)
		if ip == "" && id == "" {
			continue
		}
		if ip == host {
			selfListed = true
		}
		rec.Members = append(rec.Members, ZoneMember{DeviceID: id, IP: ip})
	}
	// The leader's address. A follower's zone document carries it in senderIP;
	// a leader answering about itself is the host we just asked.
	if selfListed {
		if s, ok := doc["senderIP"].(string); ok && s != "" {
			rec.MasterIP = s
		}
	} else if len(rec.Members) > 0 {
		rec.MasterIP = host
	}
	return rec
}

// slavesFor returns the members of the remembered zone minus the leader, which
// is what FormZone takes. A leader listed among its own members (which some
// firmware does) would otherwise be enrolled as its own follower.
func (z zoneRecord) slavesFor() []ZoneMember {
	out := make([]ZoneMember, 0, len(z.Members))
	for _, m := range z.Members {
		if m.IP != "" && m.IP == z.MasterIP {
			continue
		}
		out = append(out, m)
	}
	return out
}

// noteZoneBeforeOTA records the zone this speaker is in, just before STR pushes
// a binary that will reboot it. Best-effort throughout: a box that will not
// answer its zone is simply a box whose group STR cannot promise to restore,
// and that must never hold up the update itself.
func (a *App) noteZoneBeforeOTA(host string, port int) {
	doc, err := a.GetZoneState(host, port)
	if err != nil {
		a.logger.Info("zone before update: the speaker did not report its group, so STR cannot put it back if the reboot drops it", "host", host, "err", err)
		return
	}
	rec := zoneRecordFromDoc(doc, host)
	rec.At = time.Now()
	if rec.MasterIP == "" || len(rec.Members) == 0 {
		// This speaker stands alone NOW, so any record from an earlier update in
		// this session describes a group that no longer exists. Returning without
		// clearing it would let the next confirmed update rebuild it.
		forgetZoneBeforeOTA(host)
		return
	}
	zonesBeforeOTA.Lock()
	if zonesBeforeOTA.m == nil {
		zonesBeforeOTA.m = map[string]zoneRecord{}
	}
	zonesBeforeOTA.m[host] = rec
	zonesBeforeOTA.Unlock()
	a.recordOTA(host, fmt.Sprintf("zone: this speaker is in a group of %d led by %s; STR will rebuild it if the update reboot drops it", len(rec.Members), rec.MasterIP))
	a.logger.Info("zone before update: remembered the group so the update reboot cannot lose it", "host", host, "master", rec.MasterIP, "members", len(rec.Members), "permanent", rec.Permanent)
}

// forgetZoneBeforeOTA drops the remembered zone for a host. Consuming the record
// is a decision in itself, so it happens only where one was actually reached.
func forgetZoneBeforeOTA(host string) {
	zonesBeforeOTA.Lock()
	delete(zonesBeforeOTA.m, host)
	zonesBeforeOTA.Unlock()
}

// RestoreGroupAfterUpdate rebuilds the multiroom group the update reboot
// dropped. Bound for the frontend, which calls it at the ONE point every
// confirmed update passes through, whether the speaker came back inside the
// verify window or late.
//
// The first version hung off ClassifyOTAResult, which the frontend calls only
// when the verify window expired WITHOUT a confirmation. A speaker that came
// back on time never reached it, which is every normal update: the fix would
// have done nothing for almost everybody, and doing nothing looks exactly like
// the bug it was written to fix.
func (a *App) RestoreGroupAfterUpdate(host string, port int) {
	a.restoreZoneAfterOTA(host, port)
}

// restoreZoneAfterOTA rebuilds the zone the update reboot dropped.
func (a *App) restoreZoneAfterOTA(host string, port int) {
	zonesBeforeOTA.Lock()
	before, ok := zonesBeforeOTA.m[host]
	zonesBeforeOTA.Unlock()
	if !ok {
		return
	}
	if !before.At.IsZero() && time.Since(before.At) > zoneRecordTTL {
		forgetZoneBeforeOTA(host)
		a.logger.Info("zone after update: the remembered group is too old to act on, dropping it",
			"host", host, "age", time.Since(before.At).Round(time.Second).String())
		return
	}
	// Ask the LEADER what the group looks like now, not the box that rebooted: a
	// follower's own view of a dropped zone is empty either way, so it cannot
	// tell a lost group from a kept one.
	masterPort := a.agentPortInUse(before.MasterIP, port)
	nowDoc, err := a.GetZoneState(before.MasterIP, masterPort)
	if err != nil {
		// The record is KEPT. A leader that is itself mid-update answers nothing
		// for a few minutes, and consuming the record here would lose the group
		// for good with only a log line to show for it. The next confirmed update
		// in this session retries; the TTL above stops it living forever.
		a.logger.Info("zone after update: the group leader did not answer, keeping the remembered group for a later attempt",
			"host", host, "master", before.MasterIP, "err", err)
		return
	}
	now := zoneRecordFromDoc(nowDoc, before.MasterIP)
	// Ask the leader whether it is audible before deciding. A read that fails
	// counts as silent: the restore is the action with a side effect, so an
	// unknown answer must not authorise it.
	npXML, nperr := a.Status(before.MasterIP, masterPort)
	if nperr != nil {
		a.logger.Info("zone after update: could not read what the group leader is playing, treating it as silent",
			"master", before.MasterIP, "err", nperr)
	}
	plan := planZoneRestore(before, now, audiblyPlaying(npXML))
	// A decision was reached, so the record has done its job either way.
	forgetZoneBeforeOTA(host)
	if plan.Why != "" {
		a.recordOTA(host, plan.Why)
	}
	if !plan.Restore {
		return
	}
	a.logger.Info("zone after update: rebuilding the group the update reboot dropped",
		"host", host, "master", before.MasterIP, "members", len(before.Members))
	spec := ZoneSpec{
		Master: ZoneMember{DeviceID: before.MasterDevice, IP: before.MasterIP},
		Slaves: before.slavesFor(),
		Mode:   "native",
	}
	out, ferr := a.FormZone(before.MasterIP, masterPort, spec)
	if ferr != nil {
		a.recordOTA(host, "zone: rebuilding the group after the update failed: "+ferr.Error())
		a.logger.Warn("zone after update: could not rebuild the group",
			"host", host, "master", before.MasterIP, "err", ferr)
		return
	}
	// A nil error is NOT success. FormZone refuses in four shapes without
	// returning one: no slave ready, the leader is half of a stereo pair, every
	// member is, or the agent itself answered ok:false. Journalling "the group is
	// back together" on any of those would put a false success into the one
	// record a later report gets read from.
	if why, good := formZoneSucceeded(out); !good {
		a.recordOTA(host, "zone: rebuilding the group after the update did not take: "+why)
		a.logger.Warn("zone after update: the group was not rebuilt",
			"host", host, "master", before.MasterIP, "why", why, "result", out)
		return
	}
	a.recordOTA(host, "zone: the group is back together after the update")
}

// formZoneSucceeded reads FormZone's result document. FormZone answers a nil
// error with ok:false whenever the readiness gate or the firmware refused, so
// the error alone cannot tell success from a polite no.
func formZoneSucceeded(out map[string]any) (why string, ok bool) {
	if out == nil {
		return "the speaker returned no result", false
	}
	if v, has := out["ok"].(bool); has && !v {
		switch nr := out["notReady"].(type) {
		case []string:
			if len(nr) > 0 {
				return fmt.Sprintf("%d member(s) were still starting", len(nr)), false
			}
		case []any:
			if len(nr) > 0 {
				return fmt.Sprintf("%d member(s) were still starting", len(nr)), false
			}
		}
		return "the speaker refused to form the group", false
	}
	if m, has := out["members"].([]any); has && len(m) == 0 {
		return "the group came back with no members", false
	}
	return "", true
}
