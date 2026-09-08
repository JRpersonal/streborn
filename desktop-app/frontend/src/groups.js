// groups.js — the single source of truth for multiroom group membership on
// the frontend.
//
// Before this module, "who follows master X" was derived independently in
// three places (the music-tab selector frames, the group chips under the
// presets, and the Multi-Room tab) with two different data shapes, and two
// parallel pollers refreshed state.zoneLive with different debounce and
// error policies. Everything below is pure data-in/data-out except
// fetchZoneLive, which owns the one shared poll (busy flag, debounce, merge
// policy) and touches only state.zoneLive.
//
// IMPORTANT: no DOM access at import time (the view-extraction trap: a
// module-level DOM read blanks the whole app when the module is evaluated
// before the skeleton exists). This module has no DOM access at all.
//
// zoneLive shape: state.zoneLive[deviceID] is the box's own /api/box/zone
// self-report {master, senderIP, members: [{deviceID, ip}]}. A standalone box
// reports no master. `null` means "known standalone / left the group"
// (written by the optimistic update); an absent key means "never fetched"
// (unknown), which renders as not-fetched, not as standalone.

import { state } from './state.js';
import { GetZoneState } from './api.js';

// zoneBoxes filters a box list down to the STR speakers a zone can contain:
// live agents with a host and a deviceID (the zoneLive map is keyed by
// deviceID, so a box without one would collide under `undefined`).
export function zoneBoxes(boxes) {
  return (boxes || []).filter(b => b && b.kind !== 'stock' && b.host && b.deviceID);
}

// masterOf returns the UPPERCASED deviceID of the master the given box
// follows (its own ID when it leads a group), or '' when it is standalone,
// unknown, or has no zoneLive entry yet.
export function masterOf(deviceID, zoneLive) {
  if (!deviceID) return '';
  const zl = (zoneLive || {})[deviceID];
  return (zl && zl.master) ? String(zl.master).toUpperCase() : '';
}

// isFollower reports whether the box is a zone FOLLOWER (grouped under a
// master that is not itself). A master or a standalone box is not a follower.
export function isFollower(deviceID, zoneLive) {
  const m = masterOf(deviceID, zoneLive);
  return !!m && m !== String(deviceID || '').toUpperCase();
}

// followersOf returns the boxes from the given list that currently follow
// the given master (by their own zoneLive self-report), excluding the master
// itself. Only returns discovered boxes; for the authoritative membership of
// a destructive group edit use groupMembersOf, which also keeps members that
// briefly dropped out of the box list.
export function followersOf(deviceID, zoneLive, boxes) {
  const masterUp = String(deviceID || '').toUpperCase();
  if (!masterUp) return [];
  return (boxes || []).filter(b => {
    if (!b || b.kind === 'stock' || !b.deviceID) return false;
    if (String(b.deviceID).toUpperCase() === masterUp) return false;
    return masterOf(b.deviceID, zoneLive) === masterUp;
  });
}

// groupMembersOf returns the current followers of masterBox as
// {deviceID, ip, box} records — the authoritative list a group EDIT must
// start from. It is the union of two sources:
//   1. each discovered box's own zoneLive self-report (fresh IPs), and
//   2. the master's own member list, which still names a follower whose box
//      flapped out of discovery or whose single self-report poll failed.
// Without (2), a chip toggle rebuilt the slave list from (1) alone and
// silently kicked such a follower from the zone. Dedupe keys on IP first:
// on two-chip chassis (Portable, ST20 spotty) the firmware deviceIDs in the
// master's members[] differ from the mDNS-derived ones in the box list.
export function groupMembersOf(masterBox, zoneLive, boxes) {
  const masterUp = ((masterBox && masterBox.deviceID) || '').toUpperCase();
  if (!masterUp) return [];
  const masterHost = masterBox.host || '';
  const out = [];
  const seenIP = new Set();
  const seenID = new Set();
  const add = (deviceID, ip, box) => {
    const idUp = String(deviceID || '').toUpperCase();
    if (!idUp && !ip) return;
    if (ip && seenIP.has(ip)) return;
    if (idUp && seenID.has(idUp)) return;
    if (ip) seenIP.add(ip);
    if (idUp) seenID.add(idUp);
    out.push({ deviceID: deviceID || '', ip: ip || '', box: box || null });
  };
  for (const b of followersOf(masterBox.deviceID, zoneLive, boxes)) {
    if (b.host === masterHost) continue;
    add(b.deviceID, b.host, b);
  }
  const own = (zoneLive || {})[masterBox.deviceID];
  for (const m of ((own && own.members) || [])) {
    const ip = (m && m.ip) || '';
    const idUp = String((m && m.deviceID) || '').toUpperCase();
    if (!ip && !idUp) continue;
    // Skip the master's own row in its member list (by IP or by ID).
    if ((ip && ip === masterHost) || (idUp && idUp === masterUp)) continue;
    const box = (boxes || []).find(b => b && b.kind !== 'stock'
      && ((ip && b.host === ip) || (idUp && String(b.deviceID || '').toUpperCase() === idUp))) || null;
    add((box && box.deviceID) || (m && m.deviceID), (box && box.host) || ip, box);
  }
  return out;
}

// resolvePlayTarget is the pure decision behind effectivePlayTarget: a zone
// FOLLOWER rejects direct UPnP control (the firmware answers 501 "Can't
// control member of group", #70), so a play aimed at a follower belongs on
// its master, which distributes the audio to the group. Falls back to the
// given box (standalone, master, or master not discovered).
export function resolvePlayTarget(box, zoneLive, boxes) {
  if (!box || !box.deviceID) return box;
  const master = masterOf(box.deviceID, zoneLive);
  if (!master || String(box.deviceID).toUpperCase() === master) return box;
  const mb = (boxes || []).find(b =>
    b && b.kind !== 'stock' && b.deviceID && String(b.deviceID).toUpperCase() === master);
  return mb || box;
}

// mergeZoneLive folds one poll round into the previous zoneLive map:
//   - a fulfilled per-box call replaces that box's entry (including a
//     confirmed "standalone", which has no master),
//   - a rejected call KEEPS the last-good entry — a busy agent routinely
//     misses one 6s window, and treating that as "left the group" made the
//     next chip toggle silently kick the follower from the zone,
//   - boxes absent from this round's list are dropped,
//   - a box that has never answered stays absent (unknown, not standalone).
// ZONE_CARRY_MS is how long a speaker's last known zone survives while the
// speaker itself does not answer. Carrying it over covers a poll that lost a
// race or a speaker that is briefly busy; carrying it forever is how a group
// outlived the speakers in it (live 2026-08-15: a speaker had been offline for
// hours and the app still showed it grouped with another, complete with a
// group volume slider and no frames, because its entry was renewed from itself
// on every round). Ninety seconds is several poll rounds, well past any
// transient, and far short of "all evening".
export const ZONE_CARRY_MS = 90 * 1000;

// zoneSaysNoGroup reports whether an answer means "this speaker is in no
// group". A speaker that ANSWERED is the authority on the group it supposedly
// leads, which is what makes it safe to drop the stale entries pointing at it.
function zoneSaysNoGroup(z) {
  if (!z) return false;
  return !String(z.master || '') && !((z.members || []).length);
}

export function mergeZoneLive(prev, boxes, results, now = Date.now()) {
  const map = {};
  const answeredEmpty = new Set();
  (boxes || []).forEach((b, i) => {
    const r = results && results[i];
    if (r && r.status === 'fulfilled' && r.value) {
      // A fresh answer is stored exactly as it came, with no bookkeeping added.
      map[b.deviceID] = r.value;
      if (zoneSaysNoGroup(r.value)) answeredEmpty.add(String(b.deviceID).toUpperCase());
      return;
    }
    if (!prev || !Object.prototype.hasOwnProperty.call(prev, b.deviceID)) return;
    const carried = prev[b.deviceID];
    // A null entry means "known standalone" and is set deliberately by
    // applyOptimisticZone. It says something, so it is kept as it is.
    if (!carried) { map[b.deviceID] = carried; return; }
    // The clock starts at the first round this speaker missed, not at the last
    // one it answered: that is the moment the entry stopped being observed.
    const staleSince = typeof carried.staleSince === 'number' ? carried.staleSince : now;
    if (now - staleSince < ZONE_CARRY_MS) map[b.deviceID] = { ...carried, staleSince };
  });
  // Drop what the speakers themselves contradict: an entry naming a master
  // that just told us it has no group describes a group that no longer exists.
  if (answeredEmpty.size) {
    for (const id of Object.keys(map)) {
      const e = map[id];
      if (e && answeredEmpty.has(String(e.master || '').toUpperCase())) delete map[id];
    }
  }
  return map;
}

// applyOptimisticZone returns a new zoneLive map reflecting a group change
// the app just issued, so chips, frames and the Multi-Room summary agree at
// once (the confirming poll corrects it shortly after). Every entry that
// followed masterBox is cleared; each next member AND the master itself get
// an entry in the same shape a real poll returns ({master, senderIP,
// members}) — the earlier {master}-only optimistic shape desynced the
// selector frame/star and the Multi-Room summary from the chips. An empty
// nextMembers means the zone was dissolved.
export function applyOptimisticZone(zoneLive, masterBox, nextMembers) {
  const zl = { ...(zoneLive || {}) };
  const masterUp = ((masterBox && masterBox.deviceID) || '').toUpperCase();
  if (!masterUp) return zl;
  for (const id of Object.keys(zl)) {
    const e = zl[id];
    if (e && String(e.master || '').toUpperCase() === masterUp) zl[id] = null;
  }
  const next = nextMembers || [];
  if (next.length === 0) return zl;
  const members = next.map(m => ({ deviceID: (m && m.deviceID) || '', ip: (m && m.ip) || '' }));
  const entry = { master: masterBox.deviceID, senderIP: masterBox.host || '', members };
  for (const m of members) {
    if (m.deviceID) zl[m.deviceID] = entry;
  }
  zl[masterBox.deviceID] = entry;
  return zl;
}

// sameBoxIdentity reports whether two box records mean the same physical
// speaker. Object identity is NOT enough for in-flight guards: a background
// discovery replaces state.currentBox with a fresh object for the same
// device, which must not read as "the user switched speakers".
export function sameBoxIdentity(a, b) {
  if (!a || !b) return false;
  if (a === b) return true;
  if (a.deviceID && b.deviceID) return a.deviceID === b.deviceID;
  return !!a.host && a.host === b.host && a.port === b.port;
}

// parsePlayRejection classifies a play error thrown by the PlayURL/PlaySlot
// bindings. Newer agents reject a play aimed at a grouped follower with
// HTTP 409 {"error":"box-grouped","master":"<deviceID or IP>"}; depending on
// the Go error path the frontend sees either that raw JSON or just the
// reduced "box-grouped" marker, so both are accepted and the master field is
// optional. Older agents send a raw SOAP/UPnP string — those return
// {grouped:false} so current behavior is kept for them.
export function parsePlayRejection(err) {
  const s = String((err && err.message) || err || '');
  if (!s.includes('box-grouped')) return { grouped: false, master: '' };
  let master = '';
  const m = s.match(/\{[^{}]*"error"\s*:\s*"box-grouped"[^{}]*\}/);
  if (m) {
    try {
      const o = JSON.parse(m[0]);
      if (o && typeof o.master === 'string') master = o.master;
    } catch { /* malformed JSON fragment: master stays unknown */ }
  }
  return { grouped: true, master };
}

// resolveBoxByRef finds the discovered box a deviceID-or-IP reference names
// (the shape of the `master` field in a box-grouped rejection). Returns null
// when the reference is empty or not discovered.
export function resolveBoxByRef(ref, boxes) {
  const s = String(ref || '').trim();
  if (!s) return null;
  const up = s.toUpperCase();
  return (boxes || []).find(b => b && b.kind !== 'stock'
    && ((b.deviceID && String(b.deviceID).toUpperCase() === up) || b.host === s)) || null;
}

// ---- The one shared zoneLive poll ----

let _fetchedAt = 0;
let _inFlight = null;

// fetchZoneLive queries every STR speaker's live zone in parallel and folds
// the round into state.zoneLive via mergeZoneLive. Returns true when the
// caller should repaint (a fetch ran, or an already-running fetch was shared
// and has completed), false when nothing changed (too few boxes, or the
// debounce skipped the round).
//
//   maxAgeMs  debounce: skip when the last round started less than this ago
//             (0 = always fetch; the music tab uses 8000, NOT a tight loop).
//   minBoxes  minimum STR boxes required (a zone needs 2; the Multi-Room tab
//             also renders live badges for a single box).
//   fetchZone injectable for tests; defaults to the GetZoneState binding.
export async function fetchZoneLive(boxes, { maxAgeMs = 0, minBoxes = 1 } = {}, fetchZone = GetZoneState) {
  const strBoxes = zoneBoxes(boxes);
  if (strBoxes.length < minBoxes) return false;
  if (_inFlight) {
    // Share the in-flight round instead of skipping: the caller still gets
    // fresh data to repaint from (previously the Multi-Room tab rendered
    // stale badges whenever the music-tab poll happened to be running).
    await _inFlight;
    return true;
  }
  if (maxAgeMs > 0 && Date.now() - _fetchedAt < maxAgeMs) return false;
  _fetchedAt = Date.now();
  _inFlight = (async () => {
    try {
      const results = await Promise.allSettled(strBoxes.map(b => fetchZone(b.host, b.port)));
      state.zoneLive = mergeZoneLive(state.zoneLive, strBoxes, results);
      notifyZoneLive();
    } catch { /* keep previous entries */ }
  })();
  try {
    await _inFlight;
  } finally {
    _inFlight = null;
  }
  return true;
}

// ---- Zone change listeners ----
//
// UI that hangs off state.zoneLive OUTSIDE the two views (the group-count
// badge on the Multi-Room tab) must follow EVERY refresh path: the music-tab
// poll, the Multi-Room poll and the optimistic group edits. Rather than each
// caller remembering to repaint it (the same drift that once left the
// Multi-Room tab stale), the shared poll notifies registered listeners after
// every completed round, and an optimistic edit calls notifyZoneLive itself.
// No poll of its own, no timer.
const _zoneListeners = new Set();

// onZoneLive registers fn to run after each zoneLive change. Returns the
// unsubscribe function.
export function onZoneLive(fn) {
  _zoneListeners.add(fn);
  return () => _zoneListeners.delete(fn);
}

// notifyZoneLive runs every listener; one that throws must not break the poll
// or starve the others.
export function notifyZoneLive() {
  for (const fn of _zoneListeners) {
    try { fn(); } catch { /* a listener bug must not stop the poll */ }
  }
}

// resetZoneLivePoll clears the poll's debounce/busy bookkeeping. Test-only.
export function resetZoneLivePoll() {
  _fetchedAt = 0;
  _inFlight = null;
}

// ---- Stereo pairs ----
//
// A stereo pair is a firmware GROUP, not a multiroom zone. /getZone reports
// {"members":[]} on a paired speaker exactly as on a standalone one, so the
// zone data says nothing about pairs. The agent reports the firmware group
// alongside the zone as `stereo`; everything below reads that.
//
// Without it the Multi-Room tab was blind: its pair dropdowns just offered the
// first two candidate speakers and its "undo pair" targeted whichever speaker
// the multiroom master selection pointed at. With three SoundTouch 10s that is
// usually not a paired one, so undo was sent to an uninvolved speaker twice in
// a row while the pair stayed up (field, 2026-08-04).

// stereoPairsOf returns EVERY distinct live firmware stereo pair as
// [{id, master, members:[{deviceID, ip, role}]}], in first-seen order. Both
// halves of one pair self-report the same group, so entries are deduped on
// stereoPairKey (the sorted member set); a transient record that carries an id
// but not yet two members has no such key, so it is bucketed under 'id:'+id
// instead, which keeps two distinct in-formation pairs from collapsing under
// the same empty key. A household with two pairs (field: Buero + Wohnwagen)
// gets both, so the picker and the multi-room view can frame and name each.
export function stereoPairsOf(zoneLive) {
  const out = [];
  const seen = new Set();
  for (const key of Object.keys(zoneLive || {})) {
    const st = (zoneLive[key] || {}).stereo;
    if (!st || !((st.members || []).length || st.id)) continue;
    const rec = {
      id: st.id || '',
      // The agent names this field masterDeviceID. Reading only st.master
      // left pair.master empty on every pair, so "ask the pair's MASTER for
      // the balance" silently asked whichever half happened to be selected,
      // which is the bug that was supposed to be fixed (#70). Both spellings
      // are accepted so an older agent keeps working.
      master: String(st.masterDeviceID || st.master || '').toUpperCase(),
      members: st.members || [],
    };
    const k = stereoPairKey(rec) || (rec.id ? 'id:' + rec.id : '');
    if (!k || seen.has(k)) continue;
    seen.add(k);
    out.push(rec);
  }
  return out;
}

// stereoPairOf returns the FIRST live stereo pair, or null. Kept as a thin
// wrapper over stereoPairsOf so the callers that only need one (the balance
// source box, the undo fallback) and the existing tests keep their exact
// first-pair/null contract.
export function stereoPairOf(zoneLive) {
  return stereoPairsOf(zoneLive)[0] || null;
}

// stereoPairKey turns a live pair into a stable, order-independent key for the
// app-side pair-name store (stereoNames.js): both member deviceIDs uppercased,
// sorted, joined with "+". The SORTED SET is deliberate, not the master alone,
// so the name survives an L/R swap or a re-pair that picks the other box as
// master. The key computed when the pair is FORMED (from the picked left/right
// boxes) equals the key computed from the LIVE pair at display time because the
// desktop normalizes every BoxInfo.DeviceID to the box's own firmware /info
// deviceID (the SCM MAC), so both sides use the same identity even on two-chip
// chassis where mDNS reports a different id.
//
// Returns "" when fewer than two member deviceIDs are present (a transient
// /getGroup that carries an id/master but no members). We deliberately do NOT
// fall back to the master alone: that would compute a DIFFERENT key than the
// full "A+B" the name is stored under, so a lookup would miss (blanking the
// label) and a save would strand the name under a bare-master key that a later
// pair sharing that master could pick up. "" instead means "cannot identify
// this pair right now", so the label falls back to the default heading and a
// save is a no-op until both members are reported again. Pure: no binding
// import, so groups.test.js can cover it.
export function stereoPairKey(pair) {
  if (!pair) return '';
  const ids = (pair.members || [])
    .map((m) => String((m && m.deviceID) || '').toUpperCase())
    .filter(Boolean)
    .sort();
  return ids.length >= 2 ? ids.join('+') : '';
}

// pairMemberBoxes maps a live pair onto discovered boxes, in LEFT/RIGHT order
// (unknown roles keep their reported order). Matches by deviceID first and by
// IP second: on two-chip chassis the firmware's deviceID differs from the
// mDNS-derived one, the same mismatch groupMembersOf handles.
export function pairMemberBoxes(pair, boxes) {
  if (!pair) return [];
  const ordered = [...(pair.members || [])].sort((a, b) => {
    const rank = (m) => (String((m && m.role) || '').toUpperCase() === 'LEFT' ? 0
      : String((m && m.role) || '').toUpperCase() === 'RIGHT' ? 1 : 2);
    return rank(a) - rank(b);
  });
  return ordered.map(m => {
    const idUp = String((m && m.deviceID) || '').toUpperCase();
    const ip = (m && m.ip) || '';
    const box = (boxes || []).find(b => b && b.kind !== 'stock'
      && ((idUp && String(b.deviceID || '').toUpperCase() === idUp) || (ip && b.host === ip))) || null;
    return { member: m, box };
  });
}

// inStereoPair reports whether a discovered box is a member of the live pair.
export function inStereoPair(box, pair) {
  if (!box || !pair) return false;
  const idUp = String(box.deviceID || '').toUpperCase();
  return (pair.members || []).some(m =>
    (idUp && String((m && m.deviceID) || '').toUpperCase() === idUp) ||
    (box.host && (m && m.ip) === box.host));
}

// stereoUndoTargets lists the speakers an "undo pair" has to be sent to, master
// first.
//
// It returns BOTH halves rather than the master alone. "Only the master's
// firmware reports the pair" was true of a healthy pair and is not true of a
// broken one: measured 2026-08-10 on two SoundTouch 10s, the master answered
// /getGroup with an empty group while the other half still held the whole
// document naming the master as LEFT. An undo aimed at the master was told
// there was nothing to undo, so the leftover could not be cleared from the app
// at all, and the panel contradicted itself, naming the pair and denying it in
// the same breath.
//
// Master first because on a healthy pair it is the half that owns the pair, and
// asking it first keeps the common case a single call's worth of work. Asking
// the other half after costs nothing: a speaker that is not in a pair answers
// "nothing to undo" and is left untouched.
export function stereoUndoTargets(pair, boxes) {
  const live = pairMemberBoxes(pair, boxes).map(x => x.box)
    .filter(b => b && b.kind !== 'stock');
  const masterUp = String((pair && pair.master) || '').toUpperCase();
  const isMaster = (b) => masterUp && String(b.deviceID || '').toUpperCase() === masterUp;
  return [...live.filter(isMaster), ...live.filter(b => !isMaster(b))];
}

// balanceSourceBox picks which speaker to ask for a stereo pair's balance.
//
// Only the MASTER of a pair reports a balance; ask the other half and it says
// it has none. The picker offers the two speakers of a pair individually, with
// nothing marking which one is the master, so a user who selects the other half
// sees no balance at all and concludes the feature is missing. Reported
// 2026-08-06: "There are two speakers in a stereo pair, but there is no way to
// select a stereo pair entity. I am selecting one of the speakers within the
// stereo pair."
//
// So the question "what is this pair's balance" is answered from the master
// whichever half is selected. A speaker that is not in a pair answers for
// itself, which is also the only way an unpaired speaker can report "none".
export function balanceSourceBox(selected, pair, boxes) {
  if (!selected) return null;
  if (!pair || !inStereoPair(selected, pair)) return selected;
  const master = pairMemberBoxes(pair, boxes)
    .map(x => x.box)
    .find(b => b && String(b.deviceID || '').toUpperCase() === String(pair.master || '').toUpperCase());
  return master || selected;
}

// stereoSelectionPick decides which two speakers the stereo dropdowns show.
// Priority per slot: a still-valid USER pick wins; the live pair only fills
// slots the user has not (or no longer validly) chosen; the first candidates
// are the last resort. The old order put the live pair first, and that
// reverted every dropdown change on the spot: with any pair live, the
// UNTOUCHED second select still matched that pair, the whole pair snapped
// back in, and forming a NEW pair while one existed was impossible (field
// report 2026-08-29: "haelt an dem alten fest und aendert die Auswahl
// nicht"). The 2026-08-04 default (controls sit on a real pair, not an
// unpaired speaker) is preserved: on first paint nothing is remembered and
// the live slots win.
export function stereoSelectionPick({ left, right, liveIDs, candIDs }) {
  const still = (id) => !!id && candIDs.includes(id);
  const picks = [];
  [left, right].forEach((remembered, i) => {
    // The two channels must never resolve to the SAME speaker (#775). A stale
    // remembered pick for one channel would otherwise collide with the live pair
    // member filling the other, showing "Grace right / Grace right" in the L and
    // R slots and a bottom card that reads "no stereo pair" while the real pair
    // sits up top. So each channel skips whatever the first one already took.
    const taken = picks[0];
    let pick = '';
    if (still(remembered) && remembered !== taken) pick = remembered;
    else if (liveIDs[i] && liveIDs[i] !== taken) pick = liveIDs[i];
    else pick = candIDs.find((c) => c && c !== taken) || '';
    picks.push(pick);
  });
  return picks;
}

// zoneOrPairMaster returns the master KEY (uppercase deviceID) a box currently
// belongs to, counting both native zones AND stereo pairs, or "" when the box
// is on its own. Shared so the speaker picker and the multi-room page agree on
// which speakers form which group.
export function zoneOrPairMaster(box, zoneLive, boxes) {
  if (!box || box.kind === 'stock') return '';
  const native = masterOf(box.deviceID, zoneLive);
  if (native) return native;
  const up = (s) => String(s || '').toUpperCase();
  for (const p of stereoPairsOf(zoneLive)) {
    const bxs = pairMemberBoxes(p, boxes).map((x) => x.box).filter(Boolean);
    if (!bxs.some((b) => b.host === box.host)) continue;
    const mb = bxs.find((b) => up(b.deviceID) === up(p.master)) || bxs[0] || null;
    return mb ? up(mb.deviceID) : '';
  }
  return '';
}

// masterBoxForKey resolves a master KEY (what zoneOrPairMaster returns) back to
// the discovered box that leads that group, or null when nothing resolves.
//
// The key is a deviceID, and the deviceID is the one field on a box record that
// cannot be trusted. The key comes from the speakers' own zone documents, so it
// names the master by the identity the SPEAKER answers with, while the box
// record carries whatever the app resolved for that speaker - and the two can
// be different values for the same box, whatever the chassis. The bundle that
// forced this was three single-chip SoundTouch 10s: the master's own /info id
// WAS the id its zone document named, and the app's record for it carried the
// other MAC. The group then matched no box at all: the multi-room frame printed
// the raw hex key as its label, and its x had no box to send the dissolve to,
// so it was a silent no-op (field bundle 2026-09-07). Which side is "wrong"
// does not matter here; the frame has to survive the disagreement.
//
// So the ID is only the FIRST of three ways to ask, and each later one needs
// less of it than the one before:
//   1. by uppercased deviceID OR boxDeviceID, the second identity a speaker
//      publishes for exactly this reason: boxDeviceID is what its own firmware
//      calls it, so it matches a key taken from a zone document even when the
//      two disagree. An older agent announces none, which is what the two
//      ID-free steps below are for,
//   2. by address: a zone entry naming mk also carries the leader's senderIP,
//      and the box at that address is the leader - but only when that box's own
//      answer names mk too, so a senderIP left over from a group that has since
//      been rebuilt can never nominate a stranger,
//   3. ID-free: among the boxes whose entry names mk, the leader is the one
//      whose own answer does not list itself in members[]. A follower's
//      /getZone names the master and lists only itself; the leader's lists the
//      followers. Ambiguity (none, or several) resolves to null rather than to
//      a guess: the caller then falls back to a generic label, which is honest,
//      while a guessed box would send a dissolve to the wrong speaker.
export function masterBoxForKey(mk, zoneLive, boxes) {
  const up = (s) => String(s || '').toUpperCase();
  const key = up(mk);
  if (!key) return null;
  const strBoxes = (boxes || []).filter((b) => b && b.kind !== 'stock');
  const byID = strBoxes.find((b) => (b.deviceID && up(b.deviceID) === key)
    || (b.boxDeviceID && up(b.boxDeviceID) === key));
  if (byID) return byID;
  const entryOf = (b) => (zoneLive || {})[b.deviceID] || null;
  const namesKey = (b) => {
    const e = entryOf(b);
    return !!e && up(e.master) === key;
  };
  const inGroup = strBoxes.filter(namesKey);
  for (const b of inGroup) {
    const ip = (entryOf(b) || {}).senderIP || '';
    if (!ip) continue;
    const at = strBoxes.find((x) => x.host === ip);
    if (at && namesKey(at)) return at;
  }
  const leaders = inGroup.filter((b) => {
    const members = (entryOf(b) || {}).members || [];
    return !members.some((m) => (m && m.ip && m.ip === b.host)
      || (m && m.deviceID && b.deviceID && up(m.deviceID) === up(b.deviceID)));
  });
  return leaders.length === 1 ? leaders[0] : null;
}

// groupColorMap assigns each live group and stereo pair (a master with two or
// more discovered members) a stable colour slot 1..4, hashed from its master
// key with linear probing into the next free slot. This is the EXACT scheme
// renderBoxSelect uses, so a group frame on the multi-room page carries the same
// colour it has in the speaker picker. Returns { [MASTER_KEY_UPPER]: slot }.
export function groupColorMap(zoneLive, boxes) {
  const memberCount = {};
  (boxes || []).forEach((b) => {
    const m = zoneOrPairMaster(b, zoneLive, boxes);
    if (m) memberCount[m] = (memberCount[m] || 0) + 1;
  });
  const groups = Object.keys(memberCount).filter((m) => memberCount[m] >= 2).sort();
  const colorOf = {};
  const taken = new Set();
  for (const m of groups) {
    let h = 0;
    for (let i = 0; i < m.length; i++) h = (h * 31 + m.charCodeAt(i)) >>> 0;
    let slot = h % 4;
    for (let k = 0; k < 4 && taken.has(slot); k++) slot = (slot + 1) % 4;
    taken.add(slot);
    colorOf[m] = slot + 1;
  }
  return colorOf;
}

// storedPermanentGroupsOf lists the permanent groups that are STORED on a main
// speaker but not live right now. The main speaker's own zone answer carries
// them as `remembered` (its follower IPs and names) with `permanent` set and
// no live members. Each entry names the main speaker's box and its members,
// resolved to discovered boxes where possible (by deviceID, then by IP) so the
// multiroom page can frame the group and offer to remove it.
// Returns [{ masterKey, masterBox, members: [{ box, ip, name }] }].
export function storedPermanentGroupsOf(zoneLive, boxes) {
  const up = (s) => String(s || '').toUpperCase();
  const out = [];
  (boxes || []).forEach((b) => {
    if (!b || b.kind === 'stock' || !b.deviceID) return;
    const e = zoneLive && zoneLive[b.deviceID];
    if (!e || !e.permanent || (e.members || []).length || !(e.remembered || []).length) return;
    const members = e.remembered.map((m) => {
      const box = (boxes || []).find((x) => x && x.deviceID && m.deviceID && up(x.deviceID) === up(m.deviceID))
        || (boxes || []).find((x) => x && x.host && m.ip && x.host === m.ip)
        || null;
      return { box, ip: m.ip || '', name: m.name || '' };
    });
    out.push({ masterKey: up(b.deviceID), masterBox: b, members });
  });
  return out;
}

// storedGroupHostsOf returns the addresses of every speaker that belongs to a
// permanent group STORED on a main speaker: the main speaker itself and each of
// its remembered members. Hosts, not deviceIDs, because a remembered member is
// only reliably known by the address the group was saved with.
//
// A stored group is invisible to every LIVE check: its master is idle, so both
// it and its members report no master and no members, exactly like a standalone
// speaker. Pairing one of them destroys the group - a speaker keeps ONE zone
// document and the pair takes its place - so the stereo picker must not offer
// those speakers at all. The agent refuses the same combination and is the
// authority; this only keeps the UI from proposing it.
export function storedGroupHostsOf(zoneLive, boxes) {
  const hosts = new Set();
  for (const g of storedPermanentGroupsOf(zoneLive, boxes)) {
    if (g.masterBox && g.masterBox.host) hosts.add(g.masterBox.host);
    for (const m of g.members || []) {
      const h = (m.box && m.box.host) || m.ip || '';
      if (h) hosts.add(h);
    }
  }
  return hosts;
}

// pairBlockedHosts returns the speakers the stereo controls must not act on:
// storedGroupHostsOf MINUS the speakers that are half of a LIVE stereo pair
// right now.
//
// The subtraction is the point. A pair and a stored group can name the same two
// speakers: the group was saved first and the pair took the document's slot
// afterwards, or the group was saved on a main speaker that lists a paired one
// among its remembered members. Fencing those speakers off wholesale removed
// the pair from the picker as well, and the picker is the only place a pair can
// be renamed or undone - so the fence stranded the very pair it had nothing to
// say about. Nothing is at risk there either: the slot already holds the pair
// document, so re-forming or renaming it cannot overwrite a group with it.
export function pairBlockedHosts(zoneLive, boxes) {
  const hosts = storedGroupHostsOf(zoneLive, boxes);
  if (!hosts.size) return hosts;
  for (const p of stereoPairsOf(zoneLive)) {
    for (const x of pairMemberBoxes(p, boxes || [])) {
      const h = (x.box && x.box.host) || (x.member && x.member.ip) || '';
      if (h) hosts.delete(h);
    }
  }
  return hosts;
}

// groupCount returns how many groups currently shape playback across the
// discovered speakers, for the count badge on the Multi-Room tab:
//
//   - every distinct LIVE multiroom zone: a master with at least one member,
//     proven either by a discovered box that reports following it or by a
//     row other than the master itself in the master's own member list, so a
//     zone whose followers dropped out of discovery still counts once and a
//     master whose members[] came back empty does not count at all;
//   - plus every STORED permanent group that is not live right now. A live
//     permanent group is one group, never two, so a stored entry whose master
//     is already counted live is skipped.
//
// Stereo pairs are firmware groups, not zones: masterOf reads the zone field
// only, so a pair never shows up here. Pure, so groups.test.js covers it.
export function groupCount(zoneLive, boxes) {
  const up = (s) => String(s || '').toUpperCase();
  const strBoxes = (boxes || []).filter((b) => b && b.kind !== 'stock' && b.deviceID);
  const live = new Set();
  for (const b of strBoxes) {
    const m = masterOf(b.deviceID, zoneLive);
    if (!m || live.has(m)) continue;
    if (m !== up(b.deviceID)) {
      // A follower's self-report is proof the zone exists.
      live.add(m);
      continue;
    }
    const own = (zoneLive || {})[b.deviceID] || {};
    const host = b.host || '';
    const others = (own.members || []).filter((x) => {
      const id = up(x && x.deviceID);
      const ip = (x && x.ip) || '';
      if (!id && !ip) return false;
      return !((id && id === m) || (ip && ip === host));
    });
    if (others.length || followersOf(b.deviceID, zoneLive, strBoxes).length) live.add(m);
  }
  const stored = storedPermanentGroupsOf(zoneLive, strBoxes).filter((g) => !live.has(g.masterKey));
  return live.size + stored.length;
}
