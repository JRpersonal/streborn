// favremovals.js: favourites removed on a speaker's phone page (#1389).
//
// The desktop app owns the starred list and pushes all of it to every speaker
// whenever it changes. Before #1389 nothing ever came back the other way, so a
// favourite removed on the phone page returned with the next star set here.
// Each speaker now notes what was removed on its page, and when; this module
// turns those notes into the app's own list.

// parseFavRemovals reads one speaker's answer to GET /api/favorites?removed=1.
// An older agent answers that path with the plain favourites list, which
// means "no notes", never an error.
export function parseFavRemovals(json) {
  let v;
  try { v = JSON.parse(json); } catch { return []; }
  if (!v || Array.isArray(v) || !Array.isArray(v.removed)) return [];
  return v.removed.filter((r) => r && typeof r.id === 'string' && r.id && Number.isFinite(r.at));
}

// applyFavRemovals drops every starred entry that was removed on a phone page
// after it was starred. An entry starred again after the removal stays: the
// newer decision wins, the same rule the speaker applies to a pushed list.
// Entries from before starredAt existed count as starred long ago.
//
// removals is the notes of all speakers together; the newest note per key is
// the one that counts. Returns the kept list and whether anything was dropped.
export function applyFavRemovals(store, removals, favId) {
  const at = new Map();
  for (const r of removals || []) {
    if (!(at.get(r.id) >= r.at)) at.set(r.id, r.at);
  }
  if (!at.size) return { kept: store, changed: false };
  const kept = (store || []).filter((s) => {
    const t = at.get(favId(s));
    return t === undefined || (s.starredAt || 0) > t;
  });
  return { kept, changed: kept.length !== (store || []).length };
}

// parseFavAdditions reads the favourites starred on a speaker's phone page or
// in a remote app (#1621) from the same answer as the removals. Each note
// carries the whole row, so nothing has to be looked up. An agent from before
// #1621 has no "added" field, which means "nothing added".
export function parseFavAdditions(json) {
  let v;
  try { v = JSON.parse(json); } catch { return []; }
  if (!v || Array.isArray(v) || !Array.isArray(v.added)) return [];
  return v.added.filter((a) => a && typeof a.id === 'string' && a.id && Number.isFinite(a.at) &&
    a.fav && typeof a.fav === 'object' && a.fav.name);
}

// applyFavAdditions adds every favourite starred elsewhere that this app does
// not have yet. A removal noted after the star wins, the same newest-decision
// rule as above, so a station starred on one phone and unstarred on another
// stays off. The added row takes the star's time, which keeps a later removal
// on a phone able to take it off again. Returns the list and whether it grew.
export function applyFavAdditions(store, additions, removals, favId) {
  const removedAt = new Map();
  for (const r of removals || []) {
    if (!(removedAt.get(r.id) >= r.at)) removedAt.set(r.id, r.at);
  }
  const have = new Set((store || []).map(favId));
  const out = (store || []).slice();
  const newest = new Map();
  for (const a of additions || []) {
    if (!(newest.has(a.id) && newest.get(a.id).at >= a.at)) newest.set(a.id, a);
  }
  for (const [id, a] of newest) {
    if (have.has(id) || removedAt.get(id) >= a.at) continue;
    have.add(id);
    out.push({ ...a.fav, starredAt: a.at });
  }
  return { kept: out, changed: out.length !== (store || []).length };
}
