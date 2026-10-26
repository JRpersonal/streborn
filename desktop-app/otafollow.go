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

// known reports whether there is anything to compare an answer against.
func (id speakerIdentity) known() bool { return len(id.ids) > 0 || id.mdnsName != "" }

// compare checks a version answer against the identity. comparable is false
// when the answer and the identity share no field to compare (an older agent,
// or a name responder not up yet); match is true when at least one shared
// field agrees. A contradiction needs every shared field to disagree, so a
// speaker that changed one of its ids is not mistaken for another.
func (id speakerIdentity) compare(v map[string]string) (comparable, match bool) {
	if v == nil {
		return false, false
	}
	theirs := appendIDs(nil, v["deviceID"], v["boxDeviceID"])
	if len(id.ids) > 0 && len(theirs) > 0 {
		comparable = true
		for _, a := range id.ids {
			for _, b := range theirs {
				if strings.EqualFold(a, b) {
					match = true
				}
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
// first, and are accepted unless their answer names another speaker. With
// sweep, every other speaker discovery knows as STR is asked too, and one of
// those is accepted only on a positive match: the speaker must name itself.
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
		comparable, match := id.compare(v)
		if match || (c.byID && !comparable) {
			return c.host, c.port, true
		}
	}
	return "", 0, false
}
