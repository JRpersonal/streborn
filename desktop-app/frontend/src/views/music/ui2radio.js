// views/music/ui2radio.js: the Radio view of the UI 2.0 preview (#1286 phase 2,
// docs/UI-2.0.md).
//
// Not a second Radio: the preview reuses the Music view's own nodes, state and
// paths (the key tiles, the search box, playStation, the favourites store, the
// slot picker), and this module only adds what the preview shows on top of
// them:
//   - "Keys on <speaker>" over the six key tiles, and the tiles reachable by
//     keyboard with names that say key, speaker and station;
//   - Discover and Favorites as two tabs over the search box (Favorites is
//     the search box's own favourites mode);
//   - station rows whose actions name the station and the speaker, the
//     technical data in a details panel, and a row that reports what happened
//     to it (starting, playing, put on a key, did not play).
//
// Every export does nothing until the shell is mounted (state.ui2Shell), so the
// current layout is untouched. The shell can mount late (main.js retries on the
// first box-list refresh), so the chrome is built on the first call after that
// (ensureUi2Radio) rather than at a fixed point in the boot.

import { BrowserOpenURL, GetPresets } from '../../api.js';
import { t } from '../../i18n/index.js';
import { parsePresetConflict } from '../../presetmove.js';
import { state } from '../../state.js';
import { $, escapeHtml, friendlyErrorText, getBoxLabel } from '../../utils.js';
import { isFav, loadFavorites, loadFavStore, toggleFav } from './favorites.js';
import { putStationOnKey, showSlotPicker } from './picker.js';
import { doRefilter, playStation, preferMp3SiblingForBox, renderSearchResults } from './search.js';
import { keyHoldsStation, rowStatusHTML, stationKey, stationRowHTML, tabFor } from './ui2stations.js';
import './ui2radio.css';

let built = false;
// The list the rows were drawn from, for the delegated row handler.
let rows = [];
// The speaker the rows' actions were named for.
let rowsSpeaker = '';
// Per station (stationKey): what its row reports, and whether its details are
// open. Kept across repaints: a filter change or a star must not wipe a
// "did not play" the user has not read yet.
const status = new Map();
const open = new Set();
// The Discover mode to return to from Favorites (top list, search, Pandora).
let discoverMode = 'top';
// The top list is fetched once on its own when the preview first shows Radio:
// Discover with nothing in it would be an empty page.
let discoverLoaded = false;

const on = () => !!state.ui2Shell;
const speakerName = (box = state.currentBox) => (box ? getBoxLabel(box) : '');

// ensureUi2Radio builds the preview's additions to the Radio view once the
// shell is mounted. Safe to call from any render path.
export function ensureUi2Radio() {
  if (!on() || built) return;
  const view = $('view-box');
  const presets = $('presets');
  const search = view ? view.querySelector('.search') : null;
  const head = view ? view.querySelector('.topbar-head') : null;
  const results = $('searchResults');
  if (!view || !presets || !search || !head || !results) return;
  built = true;

  const title = document.createElement('h1');
  title.className = 'ui2-radio-title';
  title.textContent = t('ui2.nav.radio');
  head.insertBefore(title, head.firstChild);

  const keys = document.createElement('div');
  keys.className = 'ui2-keys-head';
  keys.innerHTML = '<h2 class="ui2-keys-title" id="ui2KeysTitle"></h2>'
    + `<span class="ui2-keys-hint">${escapeHtml(t('ui2.radio.keysHint'))}</span>`;
  presets.parentNode.insertBefore(keys, presets);
  presets.setAttribute('role', 'group');
  presets.setAttribute('aria-labelledby', 'ui2KeysTitle');

  const tabs = document.createElement('div');
  tabs.className = 'ui2-radio-tabs';
  tabs.setAttribute('role', 'tablist');
  tabs.setAttribute('aria-label', t('ui2.radio.tabs'));
  tabs.innerHTML = ['discover', 'favorites'].map((id) => `<button type="button" class="ui2-radio-tab" role="tab" id="ui2RadioTab-${id}" data-tab="${id}" aria-controls="ui2RadioPanel">`
    + `${escapeHtml(t(id === 'discover' ? 'ui2.radio.discover' : 'ui2.radio.favorites'))}</button>`).join('');
  search.parentNode.insertBefore(tabs, search);
  search.id = 'ui2RadioPanel';
  search.setAttribute('role', 'tabpanel');
  wireTabs(tabs);
  wireRows(results);

  syncUi2Keys();
  syncUi2RadioTabs();
}

// syncUi2Keys names the speaker over the key tiles and makes the tiles work
// from the keyboard: the body of a tile with a station is a button (Enter or
// Space plays it, the same press the mouse does), its rename and clear icons
// are buttons too, and every name says which key on which speaker. The tile
// itself is a named group, so no button sits inside another. Called after
// every renderPresets.
export function syncUi2Keys() {
  if (!on()) return;
  ensureUi2Radio();
  const box = state.currentBox;
  const speaker = speakerName(box);
  const title = $('ui2KeysTitle');
  if (title) title.textContent = speaker ? t('ui2.radio.keysOn', { name: speaker }) : '';
  const grid = $('presets');
  if (!grid) return;
  grid.querySelectorAll('.preset').forEach((tile) => {
    // renderPresets builds new tiles each time; a tile already named here
    // only gets its labels refreshed below, never a second handler.
    const fresh = !tile.dataset.ui2Key;
    tile.dataset.ui2Key = '1';
    const n = parseInt(tile.dataset.slot, 10);
    const body = tile.querySelector('.preset-body');
    tile.setAttribute('role', 'group');
    if (!body) {
      tile.setAttribute('aria-label', t('ui2.radio.keyEmpty', { n, speaker }));
      return;
    }
    tile.setAttribute('aria-label', t('preset.key', { n }));
    // The logo only repeats the name.
    body.querySelectorAll('img').forEach((img) => { if (!img.hasAttribute('alt')) img.alt = ''; });
    // The button's name holds everything it shows (name, rate, state), so a
    // voice command that reads the tile finds it.
    const shown = body.textContent.replace(/\s+/g, ' ').trim();
    body.setAttribute('aria-label', t('ui2.radio.keyLabel', { n, speaker, name: shown }));
    if (!fresh) return;
    body.setAttribute('role', 'button');
    body.tabIndex = 0;
    body.addEventListener('keydown', (e) => {
      if (e.target !== body || (e.key !== 'Enter' && e.key !== ' ')) return;
      e.preventDefault();
      // The tile's own press path (presettiles.js attachPresetHandlers): a
      // short press plays, a box-native key recalls, a lost key explains.
      body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 }));
      body.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0 }));
    });
    const chrome = [['.ren', 'ui2.radio.keyRename'], ['.del', 'ui2.radio.keyClear']];
    for (const [sel, key] of chrome) {
      const el = tile.querySelector(sel);
      if (!el) continue;
      const name = t(key, { n, speaker });
      el.setAttribute('role', 'button');
      el.setAttribute('aria-label', name);
      el.title = name;
      el.tabIndex = 0;
      el.addEventListener('keydown', (e) => {
        if (e.key !== 'Enter' && e.key !== ' ') return;
        e.preventDefault();
        e.stopPropagation();
        el.click();
      });
    }
  });
  // Another speaker was chosen: the rows' actions name the new one.
  if (rows.length && speaker !== rowsSpeaker && state.searchLastMode !== 'pandora') renderSearchResults();
  // Discover starts with the top list, once, the first time Radio shows with
  // a speaker to play on.
  const res = $('searchResults');
  if (!discoverLoaded && box && state.view === 'box' && !(state.searchResults || []).length
    && state.searchLastMode === 'top' && res && !res.children.length) {
    discoverLoaded = true;
    doRefilter();
  }
}

// syncUi2RadioTabs marks the tab that matches what the search box shows
// (tabFor) and lets the panel hide what only Discover has (the search field,
// the filters, the genres). Called from renderSearchResults.
export function syncUi2RadioTabs() {
  if (!on()) return;
  ensureUi2Radio();
  const panel = $('ui2RadioPanel');
  if (!panel) return;
  const cur = tabFor(state.searchLastMode);
  if (cur === 'discover' && state.searchLastMode) discoverMode = state.searchLastMode;
  document.querySelectorAll('.ui2-radio-tab').forEach((tab) => {
    const sel = tab.dataset.tab === cur;
    tab.setAttribute('aria-selected', sel ? 'true' : 'false');
    tab.tabIndex = sel ? 0 : -1;
    tab.classList.toggle('is-active', sel);
  });
  panel.dataset.ui2Tab = cur;
  panel.setAttribute('aria-labelledby', `ui2RadioTab-${cur}`);
}

function openTab(id) {
  if (tabFor(state.searchLastMode) === id) return;
  if (id === 'favorites') {
    loadFavorites();
  } else {
    state.searchLastMode = discoverMode || 'top';
    const pandora = $('pandoraModeBtn');
    if (state.searchLastMode === 'pandora' && pandora) pandora.click();
    else doRefilter();
  }
  syncUi2RadioTabs();
}

// wireTabs: a click or Enter opens a tab; the arrow keys, Home and End move
// between the two and open the one they land on (automatic activation).
function wireTabs(list) {
  list.addEventListener('click', (e) => {
    const tab = e.target.closest('.ui2-radio-tab');
    if (tab) openTab(tab.dataset.tab);
  });
  list.addEventListener('keydown', (e) => {
    const tabs = [...list.querySelectorAll('.ui2-radio-tab')];
    const at = tabs.indexOf(e.target.closest('.ui2-radio-tab'));
    if (at < 0) return;
    const rtl = getComputedStyle(list).direction === 'rtl';
    let next = -1;
    if (e.key === (rtl ? 'ArrowLeft' : 'ArrowRight')) next = (at + 1) % tabs.length;
    else if (e.key === (rtl ? 'ArrowRight' : 'ArrowLeft')) next = (at - 1 + tabs.length) % tabs.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = tabs.length - 1;
    if (next < 0) return;
    e.preventDefault();
    tabs[next].focus();
    openTab(tabs[next].dataset.tab);
  });
}

// renderUi2Stations draws the station list of the preview into the results
// box (search.js renderSearchResults hands over here when the shell is on).
export function renderUi2Stations(res, list, hintHtml) {
  rows = list;
  const speaker = speakerName();
  rowsSpeaker = speaker;
  res.innerHTML = `${hintHtml}<ul class="ui2-st-list">${list.map((s, i) => {
    const key = stationKey(s);
    return stationRowHTML(s, i, { speaker, fav: isFav(s), open: open.has(key), status: status.get(key) || null });
  }).join('')}</ul>`;
}

// setStatus records what a station's row reports and repaints only that line,
// so focus and the open details stay where they are.
function setStatus(key, st) {
  if (st) status.set(key, st);
  else status.delete(key);
  const li = [...document.querySelectorAll('#searchResults .ui2-st')].find((el) => el.dataset.key === key);
  const line = li && li.querySelector('.ui2-st-status');
  if (line) line.outerHTML = rowStatusHTML(st);
}

// wireRows handles the row buttons, delegated, so a repaint never loses one.
// Rows of the current layout and the Pandora list keep their own handlers.
function wireRows(res) {
  res.addEventListener('click', (e) => {
    const btn = e.target.closest('.ui2-st [data-act]');
    if (!btn || !res.contains(btn)) return;
    const s = rows[parseInt(btn.dataset.i, 10)];
    if (!s) return;
    e.preventDefault();
    e.stopPropagation();
    const act = btn.dataset.act;
    if (act === 'play') playRow(s);
    else if (act === 'fav') favRow(s, btn);
    else if (act === 'key') putOnKey(s);
    else if (act === 'details') toggleDetails(s, btn);
    else if (act === 'site' && /^https?:\/\//i.test(s.homepage || '')) BrowserOpenURL(s.homepage);
  });
}

// playRow plays through the Music view's own playStation (its fallbacks to
// another entry of the same station included) and tells the row what came of
// it. "Playing" only once the speaker reports it; a station that did not play
// says why, in its row.
async function playRow(s) {
  const key = stationKey(s);
  const speaker = speakerName();
  const url = s.url_resolved || s.url;
  setStatus(key, { kind: 'busy', text: t('ui2.radio.starting', { speaker }) });
  let failed = false;
  const ok = await playStation(s, {
    report: (msg, kind) => {
      if (kind === 'info') { setStatus(key, { kind: 'busy', text: msg }); return; }
      failed = true;
      setStatus(key, { kind: 'error', text: t('ui2.radio.playFailed', { speaker, reason: msg }) });
    },
  });
  if (failed) return;
  // Not ok without a report: the speaker was switched meanwhile, nothing to say.
  const playing = ok && state.nowPlayState === 'PLAY_STATE' && !!state.nowLocation
    && (state.nowLocation === url || state.nowName === s.name);
  setStatus(key, playing ? { kind: 'ok', text: t('ui2.radio.playing', { speaker }) } : null);
}

function favRow(s, btn) {
  const nowFav = toggleFav(s);
  if (state.searchLastMode === 'favorites' && !nowFav) {
    // Unstarred on the Favorites tab: the row leaves the list. Focus goes to
    // the star of the row that took its place, or to the tab.
    const at = parseInt(btn.dataset.i, 10);
    state.searchResults = loadFavStore();
    renderSearchResults();
    const next = document.querySelectorAll('#searchResults .ui2-st-fav');
    const target = next[Math.min(at, next.length - 1)] || $('ui2RadioTab-favorites');
    if (target) target.focus();
    return;
  }
  const name = s.name || t('search.unnamed');
  const label = t(nowFav ? 'ui2.radio.favRemove' : 'ui2.radio.favAdd', { name });
  btn.classList.toggle('is-fav', nowFav);
  btn.setAttribute('aria-pressed', nowFav ? 'true' : 'false');
  btn.setAttribute('aria-label', label);
  btn.title = label;
  btn.innerHTML = `<span aria-hidden="true">${nowFav ? '&#9733;' : '&#9734;'}</span>`;
}

function toggleDetails(s, btn) {
  const key = stationKey(s);
  const panel = document.getElementById(btn.getAttribute('aria-controls'));
  const show = btn.getAttribute('aria-expanded') !== 'true';
  if (show) open.add(key); else open.delete(key);
  btn.setAttribute('aria-expanded', show ? 'true' : 'false');
  if (panel) panel.hidden = !show;
  const li = btn.closest('.ui2-st');
  if (li) li.classList.toggle('is-open', show);
}

// putOnKey opens the shared slot picker for the selected speaker, named in its
// title and on every key, and writes the station the way openPick does. The
// row says "done" only after the preset list read back from the speaker holds
// the station on that key. A key already holding the station elsewhere is
// the picker's own move offer (the error goes back to showSlotPicker); any
// other refusal stays in the row.
function putOnKey(s) {
  const box = state.currentBox;
  if (!box) return;
  const speaker = speakerName(box);
  const key = stationKey(s);
  const name = s.name || t('search.unnamed');
  const station = preferMp3SiblingForBox(s, state.searchResults);
  showSlotPicker({
    title: t('ui2.radio.putOnKeyLabel', { name, speaker }),
    subtitle: t('ui2.radio.pickSub', { speaker }),
    box,
    onPick: async (n) => {
      const url = station.url_resolved || station.url;
      setStatus(key, { kind: 'busy', text: t('ui2.radio.saving', { n, speaker }) });
      lockPicker(true);
      try {
        await putStationOnKey(box, n, station);
      } catch (err) {
        lockPicker(false);
        if (parsePresetConflict(err)) { setStatus(key, null); throw err; }
        setStatus(key, { kind: 'error', text: t('ui2.radio.saveFailed', { n, speaker, error: friendlyErrorText(String((err && err.message) || err || '')) }) });
        return;
      }
      lockPicker(false);
      let fresh = null;
      try { fresh = await GetPresets(box.host, box.port); } catch { fresh = null; }
      setStatus(key, keyHoldsStation(fresh, n, station, url)
        ? { kind: 'ok', text: t('ui2.radio.saved', { n, speaker }) }
        : { kind: 'warn', text: t('ui2.radio.notConfirmed', { n, speaker }) });
    },
  });
  nameSlots(speaker);
}

// nameSlots gives each key in the open picker a name with the speaker in it.
function nameSlots(speaker) {
  const grid = $('pickGrid');
  if (!grid) return;
  grid.querySelectorAll('.pick-slot').forEach((b, i) => {
    const n = i + 1;
    const p = (state.presets || []).find((x) => x.slot === n);
    b.type = 'button';
    b.setAttribute('aria-label', p
      ? t('ui2.radio.pickSlot', { n, speaker, name: p.name || '' })
      : t('ui2.radio.pickSlotEmpty', { n, speaker }));
  });
}

// lockPicker keeps a second key from being pressed while the first one is on
// its way to the speaker.
function lockPicker(lock) {
  const grid = $('pickGrid');
  if (!grid) return;
  grid.setAttribute('aria-busy', lock ? 'true' : 'false');
  grid.querySelectorAll('.pick-slot').forEach((b) => { b.disabled = lock; });
}
