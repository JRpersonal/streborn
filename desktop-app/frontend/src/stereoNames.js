// App-side stereo-pair display names (Rolf Krause, 2026-08-27, points 1+2).
//
// STR keeps its own name per stereo pair in the desktop app's durable config
// (Go side: stereo_names.go), keyed on the sorted set of the pair's member
// deviceIDs. This module is the frontend bridge: a small read-through cache so
// the synchronous render code can ask for a name without turning async, plus a
// setter. The name only renders when a live pair with those two members is
// reported, so a stale store entry never produces a stale label.

import { GetStereoPairName, SetStereoPairName } from './api.js';
import { stereoPairKey } from './groups.js';

// key -> name ('' means "looked up, none stored"); undefined means "not yet
// looked up". Kept for the life of the window; pairs are few.
const cache = new Map();
const pending = new Set();

// pairDisplayName returns the stored name for a pair synchronously from cache,
// or '' while the first lookup is in flight. onResolved (optional) is called
// once the async lookup lands so the caller can repaint; pass the view's
// render function. Returns '' for a pair with no usable key.
export function pairDisplayName(pair, onResolved) {
  const key = stereoPairKey(pair);
  if (!key) return '';
  if (cache.has(key)) return cache.get(key);
  if (!pending.has(key)) {
    pending.add(key);
    GetStereoPairName(key)
      .then((name) => {
        cache.set(key, name || '');
        pending.delete(key);
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
  await SetStereoPairName(key, trimmed);
  cache.set(key, trimmed);
}

// forgetPairNameCache drops a cached entry so a later lookup re-reads it from
// disk. Used after a rename from another surface; harmless if the key is absent.
export function forgetPairNameCache(pair) {
  const key = stereoPairKey(pair);
  if (key) cache.delete(key);
}
