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
// key -> generation counter, bumped on every write. A read captures the
// generation before it starts and only commits its result if nothing was
// written meanwhile, so a slow initial GetStereoPairName cannot clobber a name
// the user just saved.
const generations = new Map();

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
}
