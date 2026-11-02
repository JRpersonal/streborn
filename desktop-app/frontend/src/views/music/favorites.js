// views/music/favorites.js: the favourite stations store, its sync to the speakers
// and the Favourites mode of the search box.

import { FavoriteRemovals, PushFavorites, PushFavoritesAt, isMissingBinding } from '../../api.js';
import { applyFavAdditions, applyFavRemovals, parseFavAdditions, parseFavRemovals } from '../../favremovals.js';
import { applyFavOrder, moveFav, newestFavOrder, parseFavOrder } from '../../favorder.js';
import { t } from '../../i18n/index.js';
import { state } from '../../state.js';
import { $, showError } from '../../utils.js';
import { listButtonClick, renderSearchResults, syncSearchModeButtons } from './search.js';
import { ensureUi2Radio } from './ui2radio.js';

// Radio favorites: a per-machine list of starred stations kept in
// localStorage (no agent change, no preset-schema change). It stores only
// the minimal Station fields renderSearchResults needs, so a favorite renders
// through the exact same row path as a search result and inherits play, the
// pick -> assign-to-key modal, and the long-press-to-tile fast path for free.
const FAV_KEY = 'str.favStations';
// FAV_ORDER_AT_KEY is when the order of the list was last set (Unix ms, #1519):
// moved here, or taken over from a phone page. Each push names it, so a
// speaker can tell an order set on a phone after it from an older list.
const FAV_ORDER_AT_KEY = 'str.favOrderAt';

function loadFavOrderAt() {
  try { return Number(localStorage.getItem(FAV_ORDER_AT_KEY)) || 0; } catch { return 0; }
}
function saveFavOrderAt(at) {
  try { localStorage.setItem(FAV_ORDER_AT_KEY, String(at || 0)); } catch {}
}

export function loadFavStore() {
  let arr;
  try { arr = JSON.parse(localStorage.getItem(FAV_KEY)) || []; } catch { return []; }
  if (!Array.isArray(arr)) return [];
  const kept = arr.filter((s) => !isSpotifyFav(s));
  // Drop such stars from the store itself, so this happens once and not on
  // every read. Only the local copy: the speakers get the cleaned list with
  // the next save, as every list change does.
  if (kept.length !== arr.length) {
    try { localStorage.setItem(FAV_KEY, JSON.stringify(kept)); } catch {}
  }
  return kept;
}
// isSpotifyFav spots a star set on a Spotify playlist that Recently played
// showed as a radio card (#1077). It is no station: played as one, the speaker
// refused the spotify: URI as a bad stream URL, and pushed to the speakers it
// kept the phone page's favourite replaying that error. Such a star is dropped;
// the playlist plays from its preset key.
export function isSpotifyFav(s) {
  return !!s && /^spotify:/i.test(String(s.url_resolved || s.url || ''));
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
  const orderAt = loadFavOrderAt();
  (state.boxes || []).forEach((b) => {
    if (!b || b.offline || b.kind === 'stock') return;
    // An app build without the newer binding pushes the list without its
    // order time, as before #1519.
    PushFavoritesAt(b.host, b.port, json, orderAt)
      .catch((err) => (isMissingBinding(err) ? PushFavorites(b.host, b.port, json) : undefined))
      .catch(() => {});
  });
}
function favMinimal(s) {
  return {
    stationuuid: s.stationuuid, name: s.name, url: s.url, url_resolved: s.url_resolved,
    bitrate: s.bitrate || 0, country: s.country, countrycode: s.countrycode,
    codec: s.codec, hls: s.hls || 0, tags: s.tags, votes: s.votes || 0, homepage: s.homepage,
    favicon: s.favicon, lastcheckok: s.lastcheckok,
    // Kept so a starred station radio-browser has not checked yet stays
    // without a reachability dot (#1521); undefined (library items) is dropped.
    lastchecktime: s.lastchecktime,
    // When it was starred (#1389). A removal on a phone page only takes a star
    // set BEFORE it, so starring a removed station again here sticks.
    starredAt: s.starredAt || 0,
    // A music-library item starred from Recently played (#1605): the mark
    // plus MIME and length, so Favourites plays it the library way. Undefined
    // on a radio station, and dropped with it.
    source: s.source === 'upnp' || s.source === 'pandora' ? s.source : undefined,
    mime: s.mime || undefined,
    durationSec: s.durationSec > 0 ? s.durationSec : undefined,
    // A Pandora station (#1663): the station reference and item type the
    // speaker listed, which its own Pandora client plays. Undefined, and so
    // dropped, on everything else.
    location: s.source === 'pandora' && s.location ? s.location : undefined,
    itemType: s.source === 'pandora' && s.itemType ? s.itemType : undefined,
  };
}

// syncFavRemovals takes the favourites removed on any speaker's phone page off
// this app's list (#1389) and adds the ones starred there or in a remote app
// (#1621), then pushes the list to every speaker, so one that was off at the
// time catches up too. Throttled: the box list refreshes often and the notes
// change only when somebody taps a star on a phone. Best effort, like the push
// itself. It runs on an empty list too: the first star may come from a phone.
let favRemovalsCheckedAt = 0;
let favRemovalsRunning = false;
export async function syncFavRemovals() {
  const now = Date.now();
  if (favRemovalsRunning || now - favRemovalsCheckedAt < 60000) return;
  favRemovalsCheckedAt = now;
  favRemovalsRunning = true;
  try {
    const boxes = (state.boxes || []).filter((b) => b && !b.offline && b.kind !== 'stock');
    const answers = await Promise.all(boxes.map((b) =>
      FavoriteRemovals(b.host, b.port).then(parseFavNotes).catch(() => parseFavNotes(''))));
    takeFavRemovals(answers.flatMap((a) => a.removed), answers.flatMap((a) => a.added),
      newestFavOrder(answers.map((a) => a.order)));
  } finally {
    favRemovalsRunning = false;
  }
}

// pollFavRemovalsOnCurrentBox is the quick half of syncFavRemovals, for while
// a screen that shows stars is open (#1339). A favourite removed on the phone
// kept its star in Recently played for over a minute: the full sync above runs
// at most once a minute on the box-list refresh, and the view only repainted on
// its own 30 s timer after that, so the star lagged by up to one and a half
// minutes. This asks ONE speaker, the selected one: the speaker the phone
// removed it on hands the removal on to every other STR speaker at once, so the
// selected one knows within a second. One small read the agent answers from
// memory, only while the window is on screen; the caller sets the cadence.
let favQuickRunning = false;
export async function pollFavRemovalsOnCurrentBox() {
  const box = state.currentBox;
  if (favQuickRunning || !box || box.offline || box.kind === 'stock') return false;
  favQuickRunning = true;
  try {
    let notes;
    try {
      notes = parseFavNotes(await FavoriteRemovals(box.host, box.port));
    } catch {
      return false; // busy or asleep: the next tick, or the minute sync, catches up
    }
    return takeFavRemovals(notes.removed, notes.added, notes.order);
  } finally {
    favQuickRunning = false;
  }
}

// favChangeListeners repaint the stars outside the search rows (Recently
// played) when a phone removal took one off this list.
const favChangeListeners = [];
export function onFavRemovalsApplied(fn) {
  if (typeof fn === 'function') favChangeListeners.push(fn);
}

// parseFavNotes reads both kinds of note from one speaker's answer.
function parseFavNotes(json) {
  return { removed: parseFavRemovals(json), added: parseFavAdditions(json), order: parseFavOrder(json) };
}

// takeFavRemovals applies the removal and addition notes to the stored list
// and, when that changed it, saves, pushes and repaints everything that shows
// stars. The push is what settles an addition note on the speakers. An order
// set on a phone after this app's own (#1519) is taken over the same way; the
// push then carries its time, which settles the order note too.
function takeFavRemovals(removals, additions, order) {
  const r = applyFavRemovals(loadFavStore(), removals, favId);
  const a = applyFavAdditions(r.kept, additions, removals, favId);
  let kept = a.changed ? a.kept.map(favMinimal) : a.kept;
  const takeOrder = !!order && order.at > loadFavOrderAt();
  if (takeOrder) {
    kept = applyFavOrder(kept, order.ids, favId).kept;
    saveFavOrderAt(order.at);
  }
  if (!r.changed && !a.changed && !takeOrder) return false;
  saveFavStore(kept);
  updateFavModeBtn();
  if (state.searchLastMode === 'favorites') {
    state.searchResults = kept;
    renderSearchResults();
  }
  for (const fn of favChangeListeners) {
    try { fn(); } catch { /* one repaint failing must not stop the others */ }
  }
  return true;
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
// moveFavorite moves the favourite shown at index i of the list on screen one
// place up (dir -1) or down (dir +1) and pushes the new order to every speaker
// (#1519). Returns the new list, or null when nothing moved (already first or
// last).
export function moveFavorite(shown, i, dir) {
  const m = moveFav(loadFavStore(), shown, i, dir, favId);
  if (!m.changed) return null;
  saveFavOrderAt(Date.now());
  saveFavStore(m.kept);
  return m.kept;
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
  // Called on every box-list refresh, right after a late mount of the UI 2.0
  // shell, so the preview's Radio additions are built there too.
  ensureUi2Radio();
}
// loadFavorites renders the saved stations through the normal search-result
// path. No server fetch and no load-more: the list is exactly the store.
export function loadFavorites() {
  if (!state.currentBox) { showError(t('search.errSelectSpeaker')); return; }
  state.searchLastMode = 'favorites';
  state.searchListOpen = true;
  syncSearchModeButtons();
  state.searchResults = loadFavStore();
  const lm = $('loadMoreRow');
  if (lm) lm.classList.add('hidden');
  renderSearchResults();
}

// mountFavorites attaches the handlers that used to be wired at module scope in
// main.js, once #view-box exists (called from mountMusicView).
export function mountFavorites() {
  $('favModeBtn').onclick = listButtonClick('favModeBtn', () => loadFavorites());
  updateFavModeBtn();
}
