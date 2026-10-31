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
import { keyLabel, keyNum } from '../../presetrows.js';
import { state } from '../../state.js';
import { $, escapeHtml, friendlyErrorText, getBoxLabel } from '../../utils.js';
import { playVerdict, startingVerdict } from '../../shell2/feedback.js';
import { isFav, loadFavorites, loadFavStore, toggleFav } from './favorites.js';
import { putStationOnKey, showSlotPicker } from './picker.js';
import { doRefilter, openBoxWifiSetup, playStation, preferMp3SiblingForBox, renderSearchResults } from './search.js';
import { keyHoldsStation, stationKey, stationRowHTML, tabFor } from './ui2stations.js';
import './ui2radio.css';

// What each key tile reports in the preview (phase 5, rule 2 and 3): the last
// recall, save, rename, clear or pick of that key, per speaker and slot, in
// the tile's own status line. Kept across the tile rebuilds renderPresets
// does on every status change, so an answer stays until it was read.
const keyStatus = new Map();
const keyStatusId = (host, slot) => `${host}|${slot}`;

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

  // The hold hint is said once here, not on every tile (ui2radio.css hides
  // the tiles' own line), and Transfer sits at the end of the same row, so
  // the keys take two rows of tiles and one line.
  const keys = document.createElement('div');
  keys.className = 'ui2-keys-head';
  keys.innerHTML = '<h2 class="ui2-keys-title" id="ui2KeysTitle"></h2>'
    + `<span class="ui2-keys-hint" id="ui2KeysHint">${escapeHtml(t('ui2.radio.keysHint'))}<span class="ui2-keys-sep"> · </span>${escapeHtml(t('ui2.radio.keysHoldHint'))}</span>`;
  // A speaker with the second row of keys (#1110) shows its Once | Twice tabs
  // in this same line, after the heading, so the second row costs no height;
  // the empty-row explainer stays between the line and the keys.
  const rowTabs = $('presetRowTabs');
  presets.parentNode.insertBefore(keys, rowTabs || presets);
  if (rowTabs) keys.insertBefore(rowTabs, keys.querySelector('.ui2-keys-hint'));
  presets.setAttribute('role', 'group');
  presets.setAttribute('aria-labelledby', 'ui2KeysTitle');
  presets.setAttribute('aria-describedby', 'ui2KeysHint');
  const copyRow = view.querySelector('.preset-copy-row');
  if (copyRow) keys.appendChild(copyRow);

  wrapFilters(search);

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

// wrapFilters folds the Discover filters (country, language, sort, bitrate,
// the two checkboxes) into one "Filters" disclosure, closed by default, whose
// summary line names what is set. The selects and checkboxes are the Music
// view's own, moved, so every handler and saved choice keeps working. Each
// select's words go into a span above it (the classic colon dropped), so the
// pairs line up as columns.
function wrapFilters(search) {
  const filters = search.querySelector('.search-filters');
  if (!filters || $('ui2Filters')) return;
  const box = document.createElement('details');
  box.className = 'ui2-filters';
  box.id = 'ui2Filters';
  box.innerHTML = '<summary class="ui2-filters-head">'
    + `<span class="ui2-filters-title">${escapeHtml(t('ui2.radio.filters'))}</span>`
    + '<span class="ui2-filters-sum" id="ui2FiltersSum"></span></summary>';
  filters.parentNode.insertBefore(box, filters);
  box.appendChild(filters);
  filters.querySelectorAll('label').forEach((lab) => {
    if (!lab.querySelector('select')) {
      lab.classList.add('ui2-filter-check');
      return;
    }
    lab.classList.add('ui2-filter-field');
    for (const node of [...lab.childNodes]) {
      if (node.nodeType !== 3) continue;
      const words = node.textContent.replace(/[:：]\s*$/, '').trim();
      if (!words) { node.remove(); continue; }
      const span = document.createElement('span');
      span.className = 'ui2-filter-name';
      span.textContent = words;
      lab.replaceChild(span, node);
    }
  });
  filters.addEventListener('change', syncFilterSummary);
  syncFilterSummary();
}

// syncFilterSummary writes the active filters into the closed disclosure's
// line: a select that is not on its first choice ("all countries", "any")
// and each ticked checkbox, in the words the controls show.
export function syncFilterSummary() {
  if (!on()) return;
  const sum = $('ui2FiltersSum');
  if (!sum) return;
  const parts = [];
  for (const id of ['searchCountry', 'searchLang', 'searchOrder', 'searchBitrate']) {
    const el = $(id);
    if (!el || el.selectedIndex <= 0 || !el.options[el.selectedIndex]) continue;
    // "English (12000)": the station count is for choosing, not for the line.
    const words = el.options[el.selectedIndex].textContent.replace(/\s*\([\d.,\s]+\)\s*$/, '').trim();
    if (words) parts.push(words);
  }
  for (const id of ['searchOnlyOK', 'searchOnlyBose']) {
    const el = $(id);
    const lab = el && el.closest('label');
    if (el && el.checked && lab) parts.push(lab.textContent.trim());
  }
  sum.textContent = parts.join(' · ');
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
  // The heading names the keys; on a two-row speaker the grid is the panel of
  // the Once | Twice tabs instead (presettiles.js), which name the speaker too.
  if (grid.getAttribute('role') !== 'tabpanel') {
    grid.setAttribute('role', 'group');
    grid.setAttribute('aria-labelledby', 'ui2KeysTitle');
  }
  grid.querySelectorAll('.preset').forEach((tile) => {
    // renderPresets builds new tiles each time; a tile already named here
    // only gets its labels refreshed below, never a second handler.
    const fresh = !tile.dataset.ui2Key;
    tile.dataset.ui2Key = '1';
    const slot = parseInt(tile.dataset.slot, 10);
    keyStatusLine(tile, box, slot);
    // "3" on the first row, "3 · 2×" on the second (#1110).
    const n = keyNum(slot, t);
    const body = tile.querySelector('.preset-body');
    tile.setAttribute('role', 'group');
    if (!body) {
      tile.setAttribute('aria-label', t('ui2.radio.keyEmpty', { n, speaker }));
      return;
    }
    tile.setAttribute('aria-label', keyLabel(slot, t));
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
  // A row that was sent but not yet playing turns "Playing" once the status
  // poll (which rebuilt these tiles) says the speaker plays it.
  upgradeSentRows();
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

// keyFocusMark notes which control of the key tiles has the keyboard before
// renderPresets rebuilds them (the grid starts with innerHTML = ''): the slot
// and whether it was the tile body, its rename or its clear icon.
export function keyFocusMark() {
  if (!on() || typeof document === 'undefined') return null;
  const grid = $('presets');
  const a = document.activeElement;
  if (!grid || !a || !grid.contains(a)) return null;
  const tile = a.closest('.preset');
  if (!tile) return null;
  const part = a.classList.contains('ren') ? 'ren' : a.classList.contains('del') ? 'del' : 'body';
  return { slot: tile.dataset.slot, part };
}

// restoreKeyFocus puts the keyboard back on the same control of the rebuilt
// tile, after syncUi2Keys made it focusable. A tile that lost the control (a
// key cleared meanwhile) takes the focus itself.
export function restoreKeyFocus(mark) {
  if (!mark || !on()) return;
  const grid = $('presets');
  if (!grid) return;
  const tile = [...grid.querySelectorAll('.preset')].find((el) => el.dataset.slot === mark.slot);
  if (!tile) return;
  const sel = mark.part === 'ren' ? '.ren' : mark.part === 'del' ? '.del' : '.preset-body[role="button"]';
  let el = tile.querySelector(sel) || tile.querySelector('.preset-body[role="button"]');
  if (!el) { tile.tabIndex = -1; el = tile; }
  try { el.focus({ preventScroll: true }); } catch { el.focus(); }
}

// keyStatusLine gives a tile its status line (role=status), filled with what
// that key of that speaker last reported. Each rebuilt tile gets a new line;
// the words in it are the kept ones, so nothing is read out twice.
function keyStatusLine(tile, box, slot) {
  let line = tile.querySelector('.ui2-key-status');
  if (!line) {
    line = document.createElement('p');
    line.setAttribute('role', 'status');
    tile.appendChild(line);
  }
  const st = box ? keyStatus.get(keyStatusId(box.host, slot)) : null;
  line.className = `ui2-key-status${st && st.kind ? ` is-${st.kind}` : ''}`;
  line.textContent = (st && st.text) || '';
}

// keyReport is the report hook a key action takes in the preview: report(text,
// kind) writes the answer into that key's status line (kind busy, ok, warn or
// error; a null text clears it). The line belongs to the speaker the action
// was for, so an answer that arrives after another speaker was chosen waits
// for that speaker's tiles. Null without the shell: every key action then
// keeps its toasts and dialogs exactly as before.
export function keyReport(slot, box = state.currentBox) {
  if (!on() || !box || !(slot > 0)) return null;
  const id = keyStatusId(box.host, slot);
  const host = box.host;
  return (text, kind = 'ok') => {
    if (text) keyStatus.set(id, { kind, text }); else keyStatus.delete(id);
    if (!state.currentBox || state.currentBox.host !== host) return;
    const tile = [...document.querySelectorAll('#presets .preset')].find((el) => parseInt(el.dataset.slot, 10) === slot);
    if (tile) keyStatusLine(tile, state.currentBox, slot);
  };
}

// keyStatusOf is what a key's status line says now ({ kind, text }), for the
// checks.
export function keyStatusOf(slot, box = state.currentBox) {
  return box ? keyStatus.get(keyStatusId(box.host, slot)) || null : null;
}

// transferReport is the report hook of the Transfer button in the preview:
// report(text, kind) writes the result into the line under the button
// (role=status) instead of a toast or the error dialog. Null without the
// shell.
export function transferReport() {
  if (!on()) return null;
  const btn = $('presetTransferBtn');
  if (!btn) return null;
  return (text, kind = 'ok') => {
    let line = $('ui2XferStatus');
    if (!line) {
      line = document.createElement('p');
      line.id = 'ui2XferStatus';
      line.setAttribute('role', 'status');
      btn.insertAdjacentElement('afterend', line);
    }
    line.className = `ui2-xfer-status${kind ? ` is-${kind}` : ''}`;
    line.textContent = text || '';
  };
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
  // A saved country or language arrives with the first list.
  syncFilterSummary();
}

// openUi2RadioTab opens Discover or Favorites as a click on the tab does (the
// empty Favorites list's "Open Discover"), and puts the keyboard on the tab.
export function openUi2RadioTab(id) {
  if (!on()) return;
  openTab(id);
  const tab = $(`ui2RadioTab-${id}`);
  if (tab) tab.focus();
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
  res.querySelectorAll('.ui2-st').forEach((li) => rowFix(li, status.get(li.dataset.key) || null));
}

// setStatus records what a station's row reports and repaints only that line,
// so focus and the open details stay where they are.
function setStatus(key, st) {
  if (st) status.set(key, st);
  else status.delete(key);
  const li = [...document.querySelectorAll('#searchResults .ui2-st')].find((el) => el.dataset.key === key);
  const line = li && li.querySelector('.ui2-st-status');
  if (!line) return;
  // The same element, only its class and text: a live region that is
  // replaced arrives already holding its words, and is not read out.
  line.className = `ui2-st-status${st && st.kind ? ` is-${st.kind}` : ''}`;
  line.textContent = (st && st.text) || '';
  rowFix(li, st);
}

// rowFix puts the way out of a row's problem next to its status line: for a
// speaker without internet, a button to that speaker's Wi-Fi. Every other
// status has none, and a row whose status changed loses the button.
function rowFix(li, st) {
  const old = li.querySelector('.ui2-st-fix');
  const box = st && st.wifiHost ? (state.boxes || []).find((b) => b && b.host === st.wifiHost) : null;
  if (!box) { if (old) old.remove(); return; }
  if (old && old.dataset.host === box.host) return;
  if (old) old.remove();
  const line = li.querySelector('.ui2-st-status');
  if (!line) return;
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'u-btn u-btn--secondary ui2-st-fix';
  btn.dataset.act = 'wifi';
  btn.dataset.host = box.host;
  const play = li.querySelector('[data-act="play"]');
  if (play) btn.dataset.i = play.dataset.i;
  btn.textContent = t('ui2.feedback.wifiOf', { speaker: getBoxLabel(box) });
  line.insertAdjacentElement('afterend', btn);
}

// upgradeSentRows turns a row that says "Sent to Kitchen. It has not started
// yet." into "Playing on Kitchen" once the speaker reports that it plays that
// station, and only then (rule 2).
function upgradeSentRows() {
  const box = state.currentBox;
  if (!box || state.nowPlayState !== 'PLAY_STATE' || !state.nowLocation) return;
  for (const [key, st] of status) {
    const p = st && st.pending;
    if (!p || p.host !== box.host) continue;
    if (state.nowLocation === p.url || state.nowName === p.name) setStatus(key, playVerdict(p.speaker, true));
  }
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
    else if (act === 'wifi') openBoxWifiSetup((state.boxes || []).find((b) => b && b.host === btn.dataset.host));
  });
}

// playRow plays through the Music view's own playStation (its fallbacks to
// another entry of the same station included) and tells the row what came of
// it. "Playing" only once the speaker reports it; a station that did not play
// says why, in its row.
async function playRow(s) {
  const key = stationKey(s);
  const box = state.currentBox;
  const speaker = speakerName();
  const url = s.url_resolved || s.url;
  setStatus(key, startingVerdict(speaker));
  let failed = false;
  const ok = await playStation(s, {
    report: (msg, kind, onBox) => {
      if (kind === 'info') { setStatus(key, { kind: 'busy', text: msg }); return; }
      failed = true;
      // The speaker has no internet: the row says so, with its Wi-Fi as the
      // way out (no modal, rule 3).
      if (kind === 'nointernet') { setStatus(key, { kind: 'error', text: msg, wifiHost: onBox ? onBox.host : '' }); return; }
      setStatus(key, { kind: 'error', text: t('ui2.radio.playFailed', { speaker, reason: msg }) });
    },
  });
  if (failed) return;
  // Not ok without a report: the speaker was switched meanwhile, nothing to say.
  if (!ok) { setStatus(key, null); return; }
  const playing = state.nowPlayState === 'PLAY_STATE' && !!state.nowLocation
    && (state.nowLocation === url || state.nowName === s.name);
  // Sent is not playing: the speaker took the station but does not report it
  // yet. The status poll upgrades the line once it does (upgradeSentRows).
  const verdict = playVerdict(speaker, playing);
  if (!playing && box) verdict.pending = { host: box.host, url, name: s.name, speaker };
  setStatus(key, verdict);
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
  // aria-disabled, not disabled: a disabled key would drop the focus to the
  // page. The picker ignores a press on a key marked so.
  grid.querySelectorAll('.pick-slot').forEach((b) => {
    if (lock) b.setAttribute('aria-disabled', 'true'); else b.removeAttribute('aria-disabled');
  });
}
