// App-side stereo-pair display names (Rolf Krause, 2026-08-27, points 1+2).
//
// STR keeps its own name per stereo pair in the desktop app's durable config
// (Go side: stereo_names.go), keyed on the sorted set of the pair's member
// deviceIDs. This module is the frontend bridge: a small read-through cache so
// the synchronous render code can ask for a name without turning async, plus a
// setter. The name only renders when a live pair with those two members is
// reported, so a stale store entry never produces a stale label.

import { GetStereoPairName, SetStereoPairName, PushStereoPairNameToBox } from './api.js';
import { stereoPairKey, pairMemberBoxes } from './groups.js';

// The name the agent writes into the firmware pair document when it was given
// none (zones_stereo.go formStereoPair). The Bose firmware advertises it to
// Spotify as "Stereo pair (L+R)", the agent's own engine as "Stereo pair
// (STR)", and the phone remote shows it as the pair's heading.
export const BOX_DEFAULT_PAIR_NAME = 'Stereo pair';

// key -> name ('' means "looked up, none stored"); undefined means "not yet
// looked up". Kept for the life of the window; pairs are few.
const cache = new Map();
const pending = new Set();
// key -> generation counter, bumped on every write. A read captures the
// generation before it starts and only commits its result if nothing was
// written meanwhile, so a slow initial GetStereoPairName cannot clobber a name
// the user just saved.
const generations = new Map();
// key -> name written here this session that the speakers have not reported
// back yet; see pairDisplayName.
const awaitingBox = new Map();

// pairDisplayName returns the stored name for a pair synchronously from cache,
// or '' while the first lookup is in flight. onResolved (optional) is called
// once the async lookup lands so the caller can repaint; pass the view's
// render function. Returns '' for a pair with no usable key.
export function pairDisplayName(pair, onResolved) {
  const key = stereoPairKey(pair);
  if (!key) return '';
  // The speakers hold the pair's name, and other clients (the phone page, STR
  // Remote, the Bose app) set it there without ever touching this app's store
  // (#1579). So the speakers' name wins, unless it is blank or the agent's
  // default. The one exception is a rename made here a moment ago: the push
  // to the speakers has gone out but the zone poll has not caught up, so the
  // fresh name is shown until the speakers report it back.
  const boxName = String((pair && pair.name) || '').trim();
  if (awaitingBox.has(key)) {
    const mine = awaitingBox.get(key);
    if (boxName !== mine) return mine;
    awaitingBox.delete(key);
  }
  if (boxName && boxName !== BOX_DEFAULT_PAIR_NAME) return boxName;
  if (cache.has(key)) return cache.get(key);
  if (!pending.has(key)) {
    pending.add(key);
    const gen = generations.get(key) || 0;
    GetStereoPairName(key)
      .then((name) => {
        pending.delete(key);
        // Drop the read if a write landed while it was in flight.
        if ((generations.get(key) || 0) !== gen) return;
        cache.set(key, name || '');
        if (onResolved) onResolved();
      })
      .catch(() => {
        pending.delete(key);
      });
  }
  return '';
}

// setPairName persists a name for a pair and updates the cache so the next
// render shows it immediately. A blank name clears the stored name (the Go side
// deletes the key), reverting to the default heading. No-op for a pair without
// a usable key.
export async function setPairName(pair, name) {
  const key = stereoPairKey(pair);
  if (!key) return;
  const trimmed = (name || '').trim();
  // Bump the generation and clear any in-flight read first, so a read that
  // resolves after this write cannot overwrite the fresh value.
  generations.set(key, (generations.get(key) || 0) + 1);
  pending.delete(key);
  await SetStereoPairName(key, trimmed);
  cache.set(key, trimmed);
  if (trimmed) awaitingBox.set(key, trimmed);
  else awaitingBox.delete(key);
}

// storedPairName is the async form of pairDisplayName: it waits for the store
// instead of answering '' while the first lookup is in flight. For the places
// that act on the name rather than draw it. Returns '' for a pair without a
// usable key or with no stored name.
export async function storedPairName(pair) {
  const key = stereoPairKey(pair);
  if (!key) return '';
  if (cache.has(key)) return cache.get(key);
  const gen = generations.get(key) || 0;
  const name = (await GetStereoPairName(key)) || '';
  if ((generations.get(key) || 0) === gen) cache.set(key, name);
  return cache.has(key) ? cache.get(key) : name;
}

// pairNameNeedsPush reports whether the name the speakers carry for a pair
// should be replaced by the one STR stored. Only a truly blank box name is,
// which is what an agent from before the pair name existed reports. The
// agent's default "Stereo pair" is left alone: it is what a pair formed again
// without a name carries, and pushing the stored name over it brought the name
// of a pair that had been undone back onto the speakers (#1579). Undoing a
// pair here clears the stored name, so a pair formed again in this app starts
// from the default too. A DIFFERENT box name is a rename made elsewhere and is
// never overwritten either.
export function pairNameNeedsPush(boxName, stored) {
  const want = String(stored || '').trim();
  if (!want) return false;
  return String(boxName || '').trim() === '';
}

// One push per pair and name per app session. A firmware that keeps refusing
// the name is not asked again on every zone poll; the next app start retries.
const healed = new Set();

// healPairNames writes the stored name back onto both members of every live
// pair whose speakers lost it. Run after each zone-poll round; it only does
// work for a pair that needs it, so the steady state is no requests at all.
// push is injectable for tests.
export async function healPairNames(pairs, boxes, push = PushStereoPairNameToBox) {
  for (const pair of pairs || []) {
    const key = stereoPairKey(pair);
    if (!key) continue;
    let stored = '';
    try { stored = await storedPairName(pair); } catch { continue; }
    if (!pairNameNeedsPush(pair.name, stored)) continue;
    const tag = key + '|' + stored;
    if (healed.has(tag)) continue;
    const members = pairMemberBoxes(pair, boxes).map(x => x.box).filter(Boolean);
    if (members.length !== 2) continue;
    healed.add(tag);
    await Promise.allSettled(members.map(b => push(b.host, b.port, stored)));
  }
}

// resetHealedPairNames clears the once-per-session guard. Test-only.
export function resetHealedPairNames() {
  healed.clear();
  awaitingBox.clear();
  cache.clear();
  pending.clear();
  generations.clear();
}

// forgetPairNamesFor drops every cached, in-flight and generation entry for a
// pair that has deviceID as a member. Called when STR is removed from that
// speaker (speakerPurge.js): the Go store drops the same entries, and the
// cache must not keep serving the old name to a pair formed after a reinstall.
// No-op for a blank deviceID.
export function forgetPairNamesFor(deviceID) {
  const id = String(deviceID || '').trim().toUpperCase();
  if (!id) return 0;
  let n = 0;
  for (const m of [cache, pending, generations]) {
    for (const key of [...m.keys()]) {
      if (String(key).split('+').includes(id)) { m.delete(key); n++; }
    }
  }
  return n;
}
