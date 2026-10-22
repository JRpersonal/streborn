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
