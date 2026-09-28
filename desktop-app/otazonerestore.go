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
func planZoneRestore(before zoneRecord, now zoneRecord) zoneRestorePlan {
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
	case before.Permanent:
		// A permanent group re-forms itself when the master next plays. Forming
		// it here as well would form it twice.
		return zoneRestorePlan{false, "zone: the group is a permanent one and re-forms itself on the next play; left alone"}
	case len(now.Members) >= len(before.Members):
		// The zone came through the reboot. Some firmware does hold it, and a
		// speaker that kept its group must not be re-formed underneath the user.
		return zoneRestorePlan{false, fmt.Sprintf("zone: the group survived the update (%d of %d members still live); nothing to rebuild", len(now.Members), len(before.Members))}
	default:
		return zoneRestorePlan{true, fmt.Sprintf("zone: the update reboot dropped the group (%d of %d members left); rebuilding it from %s", len(now.Members), len(before.Members), before.MasterIP)}
	}
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

// restoreZoneAfterOTA rebuilds the zone the update reboot dropped. Called once
// the box is CONFIRMED back on the new build, because a box that never came back
// has a bigger problem than its group membership.
func (a *App) restoreZoneAfterOTA(host string, port int) {
	zonesBeforeOTA.Lock()
	before, ok := zonesBeforeOTA.m[host]
	if ok {
		delete(zonesBeforeOTA.m, host)
	}
	zonesBeforeOTA.Unlock()
	if !ok {
		return
	}
	// Ask the LEADER what the group looks like now, not the box that rebooted: a
	// follower's own view of a dropped zone is empty either way, so it cannot
	// tell a lost group from a kept one.
	masterPort := a.agentPortInUse(before.MasterIP, port)
	nowDoc, err := a.GetZoneState(before.MasterIP, masterPort)
	if err != nil {
		a.logger.Info("zone after update: the group leader did not answer, leaving the group alone", "host", host, "master", before.MasterIP, "err", err)
		return
	}
	now := zoneRecordFromDoc(nowDoc, before.MasterIP)
	plan := planZoneRestore(before, now)
	if plan.Why != "" {
		a.recordOTA(host, plan.Why)
	}
	if !plan.Restore {
		return
	}
	a.logger.Info("zone after update: rebuilding the group the update reboot dropped", "host", host, "master", before.MasterIP, "members", len(before.Members))
	spec := ZoneSpec{
		Master: ZoneMember{DeviceID: before.MasterDevice, IP: before.MasterIP},
		Slaves: before.slavesFor(),
		Mode:   "native",
	}
	if _, ferr := a.FormZone(before.MasterIP, masterPort, spec); ferr != nil {
		a.recordOTA(host, "zone: rebuilding the group after the update failed: "+ferr.Error())
		a.logger.Warn("zone after update: could not rebuild the group", "host", host, "master", before.MasterIP, "err", ferr)
		return
	}
	a.recordOTA(host, "zone: the group is back together after the update")
}
