// stalestereo.js: a speaker stuck as half of a stereo pair that no longer
// exists.
//
// A stereo pair lives in the speakers' own firmware. When one half lets go and
// the other does not, the half that still holds the pair refuses to play
// anything on its own: it wakes, falls back into standby and every play request
// runs into a timeout. Nothing in the app said so. An owner with two SoundTouch
// 10s reinstalled STR, rebooted, and pressed every key for an hour while the app
// reported "the speaker is not ready" (mail, 2026-10-04). The second speaker
// still held the pair document naming the first as master; the first held
// nothing. The fix was one click in the Multi-Room tab.
//
// This module only READS what the shared zone poll already fetched
// (state.zoneLive): no extra request to any speaker. Pure data in, data out, so
// stalestereo.test.js covers it without a DOM.
//
// Three findings, strongest first:
//   partner-gone    the holder's own agent found the other half missing at
//                   start (pairPartnerGone in its zone answer).
//   group-error     the holder's firmware itself flags the pair as broken
//                   (<status>GROUP_ERROR</status> in /getGroup).
//   partner-denies  the holder is NOT the master, the master it names is a
//                   speaker the app knows, that speaker answered in this very
//                   round, and it reports no such pair. A healthy pair is
//                   reported by its master; a non-master holding the document
//                   while the master denies it is the leftover.
//
// A finding is only shown once it held across two zone rounds and at least
// STALE_GRACE_MS, so a pair being formed or dissolved (where the halves
// disagree for a moment) never raises it. A partner that is offline or did not
// answer this round raises nothing: the app cannot tell "gone" from "busy".

import { stereoPairKey } from './groups.js';

export const STALE_ROUNDS = 2;
export const STALE_GRACE_MS = 10000;

const up = (s) => String(s || '').toUpperCase();

// boxLabel is the same order as utils.getBoxLabel: the speaker's own name
// first. box.name is only a discovery placeholder ("str-<ip>" from the IP
// probe, "STR-<MAC tail>" from mDNS) and flips between the two as discovery
// refreshes the box, so the notice named one speaker two ways (#1208).
const boxLabel = (b) => (b && (b.friendlyName || b.name || b.host)) || '';

// freshEntry returns the speaker's zone answer from the latest round, or null
// when it did not answer this round (carried entries have staleSince) or was
// written by an optimistic edit (null).
function freshEntry(zoneLive, box) {
  if (!box || !box.deviceID) return null;
  const e = (zoneLive || {})[box.deviceID];
  if (!e || typeof e.staleSince === 'number') return null;
  return e;
}

function boxFor(ref, boxes) {
  const id = up(ref && ref.deviceID);
  const ip = (ref && ref.ip) || '';
  return (boxes || []).find(b => b && b.kind !== 'stock'
    && ((id && up(b.deviceID) === id) || (ip && b.host === ip))) || null;
}

function pairRecord(st) {
  return {
    id: st.id || '',
    master: up(st.masterDeviceID || st.master),
    members: st.members || [],
    name: st.name || '',
  };
}

// findStalePairs lists the speakers that are stuck in a pair that does not
// exist any more, from one zone round. Each finding names the holder, the
// partner (a discovered box, or null), a label for the partner, the reason and
// the pair in the shape doDissolveStereoPair takes.
export function findStalePairs(zoneLive, boxes) {
  const out = [];
  for (const holder of (boxes || [])) {
    if (!holder || holder.kind === 'stock' || !holder.deviceID || holder.offline) continue;
    const e = freshEntry(zoneLive, holder);
    if (!e) continue;
    const st = e.stereo && ((e.stereo.members || []).length || e.stereo.id) ? e.stereo : null;
    const goneIP = e.pairPartnerGone || '';
    if (!st && !goneIP) continue;
    const selfId = up(holder.deviceID);
    const partnerMember = st
      ? (st.members || []).find(m => up(m && m.deviceID) !== selfId && ((m && m.ip) || '') !== holder.host) || null
      : null;
    const partner = partnerMember
      ? boxFor(partnerMember, boxes)
      : boxFor({ deviceID: e.pairPartnerGoneId, ip: goneIP }, boxes);
    const partnerLabel = partner ? boxLabel(partner)
      : ((partnerMember && (partnerMember.ip || partnerMember.deviceID)) || goneIP || '');
    // Without a pair document (partner-gone found at agent start, /getGroup not
    // read this round) the undo still has to reach the holder: a pair of one
    // member is enough for stereoUndoTargets to address it.
    const pair = st ? pairRecord(st)
      : { id: '', master: '', members: [{ deviceID: holder.deviceID, ip: holder.host }], name: '' };
    const base = { holder, partner, partnerLabel, pair };
    if (goneIP) { out.push({ ...base, reason: 'partner-gone' }); continue; }
    if (up(st.status) === 'GROUP_ERROR') { out.push({ ...base, reason: 'group-error' }); continue; }
    const master = up(st.masterDeviceID || st.master);
    if (!master || master === selfId || !partner || partner.offline) continue;
    if (up(partner.deviceID) !== master && up(partnerMember && partnerMember.deviceID) !== master) continue;
    const pe = freshEntry(zoneLive, partner);
    if (!pe) continue;
    const ps = pe.stereo && ((pe.stereo.members || []).length || pe.stereo.id) ? pe.stereo : null;
    if (ps) {
      const same = (st.id && ps.id && st.id === ps.id)
        || (stereoPairKey(pairRecord(ps)) && stereoPairKey(pairRecord(ps)) === stereoPairKey(pairRecord(st)));
      if (same) continue;
    }
    out.push({ ...base, reason: 'partner-denies' });
  }
  return out;
}

// confirmStalePairs keeps only the findings that held long enough to be real.
// tracker is a Map the caller keeps across rounds (holder|reason -> {count,
// first}); it is rewritten to the current round, so a finding that disappears
// starts from zero next time.
export function confirmStalePairs(findings, tracker, now = Date.now(),
  rounds = STALE_ROUNDS, graceMs = STALE_GRACE_MS) {
  const next = new Map();
  const out = [];
  for (const f of findings || []) {
    const k = up(f.holder && f.holder.deviceID) + '|' + f.reason;
    const prev = tracker.get(k);
    const rec = prev ? { count: prev.count + 1, first: prev.first } : { count: 1, first: now };
    next.set(k, rec);
    if (rec.count >= rounds && now - rec.first >= graceMs) out.push(f);
  }
  tracker.clear();
  for (const [k, v] of next) tracker.set(k, v);
  return out;
}

// staleFindingFor returns the confirmed finding for a speaker, or null.
export function staleFindingFor(host, findings) {
  return (findings || []).find(f => f && f.holder && f.holder.host === host) || null;
}

// staleNoticeText is the sentence for a finding, through the caller's t().
export function staleNoticeText(f, t) {
  const holder = boxLabel(f.holder);
  const partner = f.partnerLabel || '?';
  if (f.reason === 'partner-gone') return t('stereoStale.partnerGone', { name: holder, partner });
  return t('stereoStale.partnerDenies', { name: holder, partner });
}
