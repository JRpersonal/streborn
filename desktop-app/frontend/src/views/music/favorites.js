// views/music/favorites.js: the favourite stations store, its sync to the speakers
// and the Favourites mode of the search box.

import { FavoriteRemovals, PushFavorites } from '../../api.js';
import { applyFavRemovals, parseFavRemovals } from '../../favremovals.js';
import { t } from '../../i18n/index.js';
import { state } from '../../state.js';
import { $, showError } from '../../utils.js';
import { renderSearchResults } from './search.js';

// Radio favorites: a per-machine list of starred stations kept in
// localStorage (no agent change, no preset-schema change). It stores only
// the minimal Station fields renderSearchResults needs, so a favorite renders
// through the exact same row path as a search result and inherits play, the
// pick -> assign-to-key modal, and the long-press-to-tile fast path for free.
const FAV_KEY = 'str.favStations';

export function loadFavStore() {
  try { return JSON.parse(localStorage.getItem(FAV_KEY)) || []; } catch { return []; }
}
function saveFavStore(arr) {
  try { localStorage.setItem(FAV_KEY, JSON.stringify(arr)); } catch {}
  // And onto the speakers, so the phone page shows the same list. Until this
  // the stars lived only here, on one PC, and a user asking where to find them
  // on his phone got the honest answer: nowhere (2026-09-26).
  pushFavoritesToBoxes(arr);
}

// pushFavoritesToBoxes stores the list on every speaker that runs STR.
//
// Every speaker, not one: whichever speaker's page the phone opens has to show
// the same stars, and there is no central place to put them. The agent ignores
// an unchanged list instead of writing it again, so this is cheap to repeat
// and costs nothing on the speakers' flash.
//
// Best effort by design. A speaker that is asleep or off simply misses this
// round and gets the list on the next save or the next app start; a failure
// here must never make starring a station look broken.
function pushFavoritesToBoxes(arr) {
  const json = JSON.stringify(arr || []);
  (state.boxes || []).forEach((b) => {
    if (!b || b.offline || b.kind === 'stock') return;
    PushFavorites(b.host, b.port, json).catch(() => {});
  });
}
function favMinimal(s) {
  return {
    stationuuid: s.stationuuid, name: s.name, url: s.url, url_resolved: s.url_resolved,
    bitrate: s.bitrate || 0, country: s.country, countrycode: s.countrycode,
    codec: s.codec, hls: s.hls || 0, tags: s.tags, votes: s.votes || 0, homepage: s.homepage,
    favicon: s.favicon, lastcheckok: s.lastcheckok,
    // When it was starred (#1389). A removal on a phone page only takes a star
    // set BEFORE it, so starring a removed station again here sticks.
    starredAt: s.starredAt || 0,
  };
}

// syncFavRemovals takes the favourites removed on any speaker's phone page off
// this app's list (#1389), then pushes the shorter list to every speaker, so
// one that was off when the phone removed it catches up too. Throttled: the
// box list refreshes often and the notes change only when somebody taps
// remove on a phone. Best effort, like the push itself.
let favRemovalsCheckedAt = 0;
let favRemovalsRunning = false;
export async function syncFavRemovals() {
  const now = Date.now();
  if (favRemovalsRunning || now - favRemovalsCheckedAt < 60000) return;
  if (!loadFavStore().length) return;
  favRemovalsCheckedAt = now;
  favRemovalsRunning = true;
  try {
    const boxes = (state.boxes || []).filter((b) => b && !b.offline && b.kind !== 'stock');
    const answers = await Promise.all(boxes.map((b) =>
      FavoriteRemovals(b.host, b.port).then(parseFavRemovals).catch(() => [])));
    const { kept, changed } = applyFavRemovals(loadFavStore(), answers.flat(), favId);
    if (!changed) return;
    saveFavStore(kept);
    updateFavModeBtn();
    if (state.searchLastMode === 'favorites') {
      state.searchResults = kept;
      renderSearchResults();
    }
  } finally {
    favRemovalsRunning = false;
  }
}
function favId(s) { return s && (s.stationuuid || (s.name + '|' + (s.url || ''))); }
// favStationsJSON / mergeFavStations are the backup hooks (#778): export hands
// the raw store to the backup file, import merges the file's list back in.
// Merging (not replacing) is deliberate: restoring onto a damaged install adds
// everything back, and restoring an old file onto a healthy one cannot delete
// the stars added since.
export function favStationsJSON() {
  try { return localStorage.getItem(FAV_KEY) || '[]'; } catch { return '[]'; }
}
export function mergeFavStations(json) {
  let incoming = [];
  try { incoming = JSON.parse(json) || []; } catch { /* not a list: nothing to merge */ }
  if (!Array.isArray(incoming)) incoming = [];
  const arr = loadFavStore();
  const have = new Set(arr.map(favId));
  let added = 0;
  for (const s of incoming) {
    const id = favId(s);
    if (!id || have.has(id)) continue;
    arr.push(favMinimal(s));
    have.add(id);
    added++;
  }
  if (added) saveFavStore(arr);
  updateFavModeBtn();
  return { added, total: arr.length };
}
export function isFav(s) {
  const id = favId(s);
  return !!id && loadFavStore().some(x => favId(x) === id);
}
export function toggleFav(s) {
  const id = favId(s);
  if (!id) return false;
  const arr = loadFavStore();
  const idx = arr.findIndex(x => favId(x) === id);
  let nowFav;
  if (idx >= 0) { arr.splice(idx, 1); nowFav = false; }
  else { arr.push(favMinimal({ ...s, starredAt: Date.now() })); nowFav = true; }
  saveFavStore(arr);
  updateFavModeBtn();
  return nowFav;
}
// updateFavModeBtn shows the "Favorites" mode entry next to Top/Search only
// once at least one station is starred, so a user who never uses favorites
// sees the unchanged toggle (the "appears only after the first star" design).
export function updateFavModeBtn() {
  const b = $('favModeBtn');
  if (!b) return;
  b.classList.toggle('hidden', loadFavStore().length === 0);
}
// loadFavorites renders the saved stations through the normal search-result
// path. No server fetch and no load-more: the list is exactly the store.
function loadFavorites() {
  if (!state.currentBox) { showError(t('search.errSelectSpeaker')); return; }
  state.searchLastMode = 'favorites';
  state.searchResults = loadFavStore();
  const lm = $('loadMoreRow');
  if (lm) lm.classList.add('hidden');
  renderSearchResults();
}

// mountFavorites attaches the handlers that used to be wired at module scope in
// main.js, once #view-box exists (called from mountMusicView).
export function mountFavorites() {
  $('favModeBtn').onclick = () => loadFavorites();
  updateFavModeBtn();
}
