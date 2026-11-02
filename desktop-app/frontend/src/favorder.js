// favorder.js: the order of the starred stations (#1519).
//
// The favourites showed in the order they were starred, with no way to move
// one. They are moved here with up and down buttons, and on a speaker's phone
// page the same way. The app owns the list and pushes it to every speaker, so
// an order set on a phone is noted by the speaker, like a removal (#1389), and
// this module turns that note into the app's own order. Self-contained (no
// imports), so the tests load it directly.

// moveFav moves the entry shown at index i of the list on screen one place up
// (dir -1) or down (dir +1) in the stored list. The list on screen can be a
// filtered view of the store, so the entry goes right before or after its
// neighbour on screen, not by index. Returns the new store and whether it
// changed.
export function moveFav(store, shown, i, dir, favId) {
  const arr = Array.isArray(store) ? store.slice() : [];
  const list = Array.isArray(shown) ? shown : [];
  const me = list[i];
  const other = list[i + dir];
  if (!me || !other || (dir !== -1 && dir !== 1)) return { kept: arr, changed: false };
  const id = favId(me);
  const otherId = favId(other);
  const from = arr.findIndex((s) => favId(s) === id);
  if (from < 0 || id === otherId) return { kept: arr, changed: false };
  const [item] = arr.splice(from, 1);
  const at = arr.findIndex((s) => favId(s) === otherId);
  if (at < 0) return { kept: store, changed: false };
  arr.splice(dir < 0 ? at : at + 1, 0, item);
  return { kept: arr, changed: true };
}

// parseFavOrder reads the order note from one speaker's answer to
// GET /api/favorites?removed=1: { at, ids } or null. An agent from before
// #1519 has no "order" field, which means "no order set on a phone".
export function parseFavOrder(json) {
  let v;
  try { v = JSON.parse(json); } catch { return null; }
  const o = v && !Array.isArray(v) ? v.order : null;
  if (!o || !Number.isFinite(o.at) || !Array.isArray(o.ids)) return null;
  const ids = o.ids.filter((id) => typeof id === 'string' && id);
  return ids.length ? { at: o.at, ids } : null;
}

// newestFavOrder picks the newest of the speakers' order notes, or null.
export function newestFavOrder(orders) {
  let best = null;
  for (const o of orders || []) {
    if (o && (!best || o.at > best.at)) best = o;
  }
  return best;
}

// applyFavOrder puts the stored list into the noted order. Entries the note
// does not name (starred after it was set, or unknown to that speaker) follow
// in the order they had, so nothing is lost. Returns the list and whether the
// order changed.
export function applyFavOrder(store, ids, favId) {
  const list = Array.isArray(store) ? store : [];
  const pos = new Map();
  (ids || []).forEach((id, i) => { if (!pos.has(id)) pos.set(id, i); });
  if (!pos.size || list.length < 2) return { kept: list, changed: false };
  const placed = [];
  const rest = [];
  for (const s of list) {
    const p = pos.get(favId(s));
    if (p === undefined || placed[p]) rest.push(s);
    else placed[p] = s;
  }
  const kept = placed.filter(Boolean).concat(rest);
  const changed = kept.some((s, i) => s !== list[i]);
  return { kept: changed ? kept : list, changed };
}
