package main

// Following a speaker that gets a new address while it updates.
//
// A router that hands out fresh leases on every reboot (a mesh system did it to
// three of four speakers in one fleet update, 2026-10-26) can give one
// speaker's address to another while both restart. Everything in the update
// sequence used to be keyed by the address alone, so:
//
//   - the speaker that now held the old address answered on the new build and
//     "confirmed" an update that was not its own,
//   - the Spotify engine went to that speaker, and the one that had dropped its
//     engine for the update never got it back,
//   - and the two speakers that simply moved were journaled NOT CONFIRMED with
//     firewall advice, although both had updated fine.
//
// The speaker says who it is on /api/agent/version: mdnsName (a per-speaker
// name, since v0.9.90) and, from this version on, deviceID. The reading taken
// before the push is the reference. A later answer that names another speaker
// is never taken as this one's, and the sequence looks for this speaker at its
// new address and carries on there.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// errOtherSpeaker is what a version read returns when the answer came from a
// different speaker than the one being updated.
var errOtherSpeaker = errors.New("another speaker answers at this address")

// verdictAddressChanged is the verify window's verdict when another speaker
// took this one's address and this one was not found at a new one in time.
// It doubles as the outcome's Unmet, so the window can say what happened
// instead of blaming a firewall.
const verdictAddressChanged = "address-changed"

// speakerIdentity is who a speaker said it was before its update.
type speakerIdentity struct {
	// ids are the ids the speaker reported for itself (deviceID, boxDeviceID).
	ids []string
	// mdnsName is the name the speaker answers for itself (str-xxxxxx.local).
	mdnsName string
	// hints are ids the discovery records carry for this speaker. They only
	// pick which cached addresses to try first; a candidate is believed only
	// on its own answer.
	hints []string
}

// identityFrom reads the identity out of a version answer.
func identityFrom(v map[string]string) speakerIdentity {
	var id speakerIdentity
	if v == nil {
		return id
	}
	id.ids = appendIDs(nil, v["deviceID"], v["boxDeviceID"])
	id.mdnsName = normMDNSName(v["mdnsName"])
	return id
}

func appendIDs(dst []string, ids ...string) []string {
	for _, s := range ids {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		dup := false
		for _, have := range dst {
			if strings.EqualFold(have, s) {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, s)
		}
	}
	return dst
}

func normMDNSName(n string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(n), "."))
}

// reported reports whether the speaker itself has said who it is.
func (id speakerIdentity) reported() bool { return len(id.ids) > 0 || id.mdnsName != "" }

// known reports whether there is anything to compare an answer against.
func (id speakerIdentity) known() bool { return id.reported() || len(id.hints) > 0 }

// compare checks a version answer against the identity. comparable is false
// when the answer and the identity share no field to compare (an older agent,
// or a name responder not up yet); match is true when at least one shared
// field agrees. A contradiction needs every shared field to disagree, so a
// speaker that changed one of its ids is not mistaken for another.
//
// The discovery hints count too. A speaker on an agent that reports only its
// name before the update leaves a name-only reference, and a speaker whose
// network was not up when its agent started answers without a name for that
// whole boot: without the hints, another speaker that took the address and
// answered like that would share no field with the reference and be taken for
// this one. A hint that equals one of the answer's ids is a match. A hint only
// contradicts an answer that carries BOTH ids (the agent's own and the
// firmware's): discovery may hold either one for a speaker, and the agent's
// own id can be a guess from the other network interface, so a lone id that
// differs from the hint proves nothing.
func (id speakerIdentity) compare(v map[string]string) (comparable, match bool) {
	if v == nil {
		return false, false
	}
	theirs := appendIDs(nil, v["deviceID"], v["boxDeviceID"])
	if len(theirs) > 0 {
		if len(id.ids) > 0 {
			comparable = true
			if overlapIDs(id.ids, theirs) {
				match = true
			}
		}
		if len(id.hints) > 0 {
			if overlapIDs(id.hints, theirs) {
				comparable, match = true, true
			} else if len(theirs) >= 2 {
				comparable = true
			}
		}
	}
	if n := normMDNSName(v["mdnsName"]); id.mdnsName != "" && n != "" {
		comparable = true
		if n == id.mdnsName {
			match = true
		}
	}
	return comparable, match
}

func overlapIDs(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}

// contradicts reports whether v is provably another speaker's answer.
func (id speakerIdentity) contradicts(v map[string]string) bool {
	c, m := id.compare(v)
	return c && !m
}

// learn adds what a matching answer reports beyond what is already known, so a
// reference taken from an older agent (name only) also knows the id the new
// agent reports, and the guard holds while the new agent's name responder is
// not up yet.
func (id *speakerIdentity) learn(v map[string]string) {
	if c, m := id.compare(v); !c || !m {
		return
	}
	id.ids = appendIDs(id.ids, v["deviceID"], v["boxDeviceID"])
	if id.mdnsName == "" {
		id.mdnsName = normMDNSName(v["mdnsName"])
	}
}

// describe names the identity for a journal line.
func describeIdentity(v map[string]string) string {
	if n := normMDNSName(v["mdnsName"]); n != "" {
		return n
	}
	if id := strings.TrimSpace(v["deviceID"]); id != "" {
		return "device " + id
	}
	return "unknown"
}

// read asks the speaker being updated for its version, at the address it is
// on now. An answer from another speaker is never returned as this one's: the
// sequence then looks for this speaker at a new address, and reads there if it
// finds it. A speaker that does not answer at all is looked for too, but only
// among addresses discovery already ties to it.
func (u *speakerUpdater) read() (map[string]string, error) {
	host, port := u.addr()
	v, err := u.seams.readVersion(host, port)
	if err == nil && v != nil {
		if !u.ident.contradicts(v) {
			u.ident.learn(v)
			u.foreign = false
			return v, nil
		}
		u.foreign = true
		u.sawForeign = true
		if !u.foreignNoted[u.live] {
			if u.foreignNoted == nil {
				u.foreignNoted = map[string]bool{}
			}
			u.foreignNoted[u.live] = true
			u.record(fmt.Sprintf("another speaker (%s) now answers at %s: this speaker got a new address while it restarted; it is not taken as this speaker's answer, looking for this speaker at its new address",
				describeIdentity(v), u.live))
			u.log().Info("speaker update: another speaker answers at this address, looking for ours",
				"host", u.host, "live", u.live, "other", describeIdentity(v))
		}
		if !u.follow(true) {
			return nil, errOtherSpeaker
		}
	} else {
		u.foreign = false
		if !u.follow(false) {
			return v, err
		}
	}
	// Followed: one read at the new address, held to the same rule.
	v, err = u.seams.readVersion(u.live, u.livePort)
	if err != nil || v == nil {
		return v, err
	}
	if u.ident.contradicts(v) {
		u.foreign = true
		u.sawForeign = true
		return nil, errOtherSpeaker
	}
	u.ident.learn(v)
	u.foreign = false
	return v, nil
}

// addressChanged ends the sequence for a speaker whose address another
// speaker answers on, where this one was not found at a new address.
// Classifying the address would judge the OTHER speaker (it may well be on the
// new build, which reads as "confirmed"), and the unreachable advice blames a
// firewall for what is a router handing out new addresses. Say what happened;
// a later discovery sighting of this speaker corrects the journal. stopped,
// when set, names what was not done because of it.
func (u *speakerUpdater) addressChanged(base UpdateOutcome, attempt int, stopped string) UpdateOutcome {
	live, _ := u.addr()
	line := "NOT CONFIRMED - another speaker now answers at " + live + ": this speaker got a new address while it restarted and was not found at the new one"
	if stopped != "" {
		line += "; " + stopped + " so it cannot land on the other speaker"
	} else {
		line += " before the verify window ended"
	}
	u.record(line + "; the update has most likely landed, a later discovery sighting adds a corrective line")
	if u.seams.noteUnconfirmed != nil {
		u.seams.noteUnconfirmed(u.host, verdictAddressChanged)
	}
	base.Verdict = verdictAddressChanged
	base.Outcome = outcomeTimeout
	base.Unmet = verdictAddressChanged
	base = u.restoreEngineAfterIncompleteUpdate(base)
	u.phase(updatePhase{Phase: base.Outcome, Attempt: attempt, Outcome: base.Outcome, Unmet: base.Unmet})
	return base
}

// engineTarget asks the speaker who it is right before an engine push, and
// returns the address to push to. It runs inside the upload gate: a push can
// queue there for minutes behind other speakers' updates, long enough for a
// speaker to restart once more and lose its address to another one, so the
// read before the queue says nothing about where the push goes. ok is false
// when the speaker does not answer or another speaker answers at its address.
func (u *speakerUpdater) engineTarget() (host string, port int, ok bool) {
	if _, err := u.read(); err != nil || u.foreign {
		return "", 0, false
	}
	host, port = u.addr()
	return host, port, true
}

// follow looks for the speaker at another address and moves the sequence
// there when it finds it. sweep widens the search from the addresses
// discovery ties to this speaker to every speaker discovery knows; it is used
// only once another speaker has been seen at this one's address, which is the
// moment the speaker is known to have moved. Throttled, so a speaker that is
// merely rebooting costs one cache lookup per followEvery, not one per poll.
func (u *speakerUpdater) follow(sweep bool) bool {
	if u.seams.locate == nil || !u.ident.known() {
		return false
	}
	now := u.seams.now()
	if !u.lastLocate.IsZero() && now.Sub(u.lastLocate) < u.policy.followEvery {
		return false
	}
	u.lastLocate = now
	host, port, ok := u.seams.locate(u.ident, u.live, sweep)
	if !ok || host == "" || host == u.live {
		return false
	}
	from := u.live
	u.live = host
	if port > 0 {
		u.livePort = port
	}
	u.foreign = false
	u.sawForeign = false
	u.moveIntent(u.live, u.livePort)
	u.record(fmt.Sprintf("speaker moved from %s to %s during the update (new address from the router); following it there", from, host))
	u.log().Info("speaker update: the speaker got a new address during the update, following it",
		"host", u.host, "from", from, "to", host, "port", u.livePort)
	return true
}

// locateSpeaker finds the address a speaker is on now, by asking the speakers
// discovery knows who they are. probe is the version read; exclude is the
// address the speaker is NOT on (its old one).
//
// Addresses whose discovery record carries one of the speaker's ids are asked
// first. With sweep, every other speaker discovery knows as STR is asked too.
// Every candidate is accepted only on a positive match: the speaker must name
// itself (its name, or an id the reference or discovery holds for it). A
// discovery record alone is no proof, since the address it names may belong
// to another speaker by now.
func (a *App) locateSpeaker(id speakerIdentity, exclude string, sweep bool, probe func(host string, port int) (map[string]string, error)) (string, int, bool) {
	if probe == nil || !id.known() {
		return "", 0, false
	}
	type cand struct {
		host string
		port int
		byID bool
	}
	ids := appendIDs(append([]string(nil), id.ids...), id.hints...)
	hasID := func(b BoxInfo) bool {
		for _, s := range ids {
			if strings.EqualFold(b.DeviceID, s) || (b.BoxDeviceID != "" && strings.EqualFold(b.BoxDeviceID, s)) {
				return true
			}
		}
		return false
	}
	byHost := map[string]cand{}
	add := func(b BoxInfo) {
		if b.Host == "" || b.Host == exclude {
			return
		}
		match := hasID(b)
		if !match && (!sweep || b.Kind != "str") {
			return
		}
		c := byHost[b.Host]
		c.host = b.Host
		if c.port == 0 || match {
			c.port = b.Port
		}
		c.byID = c.byID || match
		byHost[b.Host] = c
	}
	a.discMu.Lock()
	for _, e := range a.discCache {
		add(e.box)
	}
	for _, e := range a.strKnown {
		add(e.box)
	}
	a.discMu.Unlock()
	if len(byHost) == 0 {
		return "", 0, false
	}
	cands := make([]cand, 0, len(byHost))
	for _, c := range byHost {
		if c.port == 0 || c.port == 8090 {
			c.port = 8888
		}
		cands = append(cands, c)
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].byID != cands[j].byID {
			return cands[i].byID
		}
		return cands[i].host < cands[j].host
	})
	const maxProbes = 16
	if len(cands) > maxProbes {
		cands = cands[:maxProbes]
	}
	answers := make([]map[string]string, len(cands))
	var wg sync.WaitGroup
	for i, c := range cands {
		wg.Add(1)
		go func(i int, c cand) {
			defer wg.Done()
			if v, err := probe(c.host, c.port); err == nil {
				answers[i] = v
			}
		}(i, c)
	}
	wg.Wait()
	for i, c := range cands {
		v := answers[i]
		if v == nil {
			continue
		}
		if _, match := id.compare(v); match {
			return c.host, c.port, true
		}
	}
	return "", 0, false
}
