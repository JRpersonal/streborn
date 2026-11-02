// pairnamememory.js: the stereo-pair name the user typed last (#1683).
//
// The name field of the stereo editor used to come back with the name of a
// pair that had been undone, because the app's pair-name store (stereoNames.js)
// kept it. Since #1579 undoing a pair forgets that stored name, so the speakers
// and other clients never get it back on their own, and the field started
// empty every time a pair was formed again.
//
// This is only the field's suggestion: the last name typed for these two
// speakers, else the last name typed at all, remembered in this app's local
// storage. Nothing here is sent anywhere. The name reaches the speakers only
// when the user presses Create with it in the field, the same as a typed one.
//
// Keyed like the store (groups.js stereoPairKey): both member deviceIDs,
// uppercased, sorted, joined with "+".

import { stereoPairKey } from './groups.js';

export const PAIR_NAME_MEMORY_KEY = 'str.stereo.lastNames';

// Pairs are few; this only caps a store that somebody fills by pairing many
// different speakers over the years.
const MAX_PAIRS = 20;

function load() {
  try {
    const v = JSON.parse(localStorage.getItem(PAIR_NAME_MEMORY_KEY) || 'null');
    if (v && typeof v === 'object') {
      return {
        last: typeof v.last === 'string' ? v.last : '',
        byPair: v.byPair && typeof v.byPair === 'object' ? v.byPair : {},
      };
    }
  } catch { /* blocked or damaged storage: nothing remembered */ }
  return { last: '', byPair: {} };
}

function save(mem) {
  try { localStorage.setItem(PAIR_NAME_MEMORY_KEY, JSON.stringify(mem)); } catch { /* blocked storage */ }
}

// rememberPairName records a name the user typed for a pair (formed or
// renamed). A blank name is not a choice and is ignored: it means "keep the
// name these two have", not "forget the last one".
export function rememberPairName(pair, name) {
  const trimmed = String(name || '').trim();
  if (!trimmed) return;
  const mem = load();
  mem.last = trimmed;
  const key = stereoPairKey(pair);
  if (key) {
    delete mem.byPair[key];
    mem.byPair[key] = trimmed;
    const keys = Object.keys(mem.byPair);
    for (const k of keys.slice(0, Math.max(0, keys.length - MAX_PAIRS))) delete mem.byPair[k];
  }
  save(mem);
}

// rememberedPairName is the field's suggestion for a pair: the name typed
// last for these two speakers, else the name typed last at all, else ''.
export function rememberedPairName(pair) {
  const mem = load();
  const key = stereoPairKey(pair);
  const own = key ? mem.byPair[key] : '';
  return (typeof own === 'string' && own.trim()) || mem.last.trim() || '';
}

// forgetRememberedPairNamesFor drops the per-pair entries a removed speaker
// is part of (speakerPurge.js). The last name typed at all stays: it names no
// speaker. Returns how many entries went.
export function forgetRememberedPairNamesFor(deviceID) {
  const id = String(deviceID || '').trim().toUpperCase();
  if (!id) return 0;
  const mem = load();
  let n = 0;
  for (const k of Object.keys(mem.byPair)) {
    if (k.split('+').includes(id)) { delete mem.byPair[k]; n++; }
  }
  if (n) save(mem);
  return n;
}
