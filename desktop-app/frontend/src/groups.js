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
export function mergeZoneLive(prev, boxes, results) {
  const map = {};
  (boxes || []).forEach((b, i) => {
    const r = results && results[i];
    if (r && r.status === 'fulfilled' && r.value) {
      map[b.deviceID] = r.value;
    } else if (prev && Object.prototype.hasOwnProperty.call(prev, b.deviceID)) {
      map[b.deviceID] = prev[b.deviceID];
    }
  });
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
    } catch { /* keep previous entries */ }
  })();
  try {
    await _inFlight;
  } finally {
    _inFlight = null;
  }
  return true;
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

// stereoPairOf returns the live firmware stereo pair as
// {id, master, members:[{deviceID, ip, role}]}, or null when no speaker
// reports one. Any member's self-report describes the whole pair, so the
// first one found wins.
export function stereoPairOf(zoneLive) {
  for (const key of Object.keys(zoneLive || {})) {
    const st = (zoneLive[key] || {}).stereo;
    if (st && ((st.members || []).length || st.id)) {
      return {
        id: st.id || '',
        // The agent names this field masterDeviceID. Reading only st.master
        // left pair.master empty on every pair, so "ask the pair's MASTER for
        // the balance" silently asked whichever half happened to be selected,
        // which is the bug that was supposed to be fixed (#70). Both spellings
        // are accepted so an older agent keeps working.
        master: String(st.masterDeviceID || st.master || '').toUpperCase(),
        members: st.members || [],
      };
    }
  }
  return null;
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
