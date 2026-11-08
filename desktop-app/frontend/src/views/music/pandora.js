// views/music/pandora.js: the Pandora station list of the search box (US services).

import { PandoraStations, PlayPandoraStation, SavePandoraStationPreset, USServicesStatus } from '../../api.js';
import { t } from '../../i18n/index.js';
import { state } from '../../state.js';
import { agentErrorCode, agentErrorText, pandoraFindMode, pandoraStationRows } from '../../usservices.js';
import { $, escapeAttr, escapeHtml, showError, showToast } from '../../utils.js';
import { keyNum } from '../../presetrows.js';
import { pandoraFavFromRow } from '../../pandorafav.js';
import { isFav, toggleFav } from './favorites.js';
import { showSlotPicker } from './picker.js';
import { listButtonClick, syncSearchModeButtons } from './search.js';
import { refreshStatus } from './status.js';

// ---------- Pandora stations ----------
//
// A speaker with a linked Pandora account plays its stations itself; the agent
// reads the station list from the speaker and hands a station back to it
// (docs/streaming/us-services.md). The entry sits next to Top/Search/Favorites
// and opens the stations while the speaker lists Pandora as READY. With an
// account on the speaker that it lists UNAVAILABLE the entry stays, greyed, and
// says so when pressed instead of vanishing (pandoraFindMode, #1763).

// How long one speaker's Pandora state is trusted before the speaker is asked
// again. Short enough that a fresh link shows up on the next visit, long enough
// that the status poll and tab switches do not keep asking.
const PANDORA_STATE_TTL_MS = 60 * 1000;
let pandoraState = { host: '', at: 0, mode: 'hidden' };
let pandoraRows = [];

export async function updatePandoraModeBtn(force) {
  const b = $('pandoraModeBtn');
  if (!b) return;
  const box = state.currentBox;
  if (!box || !box.host || box.kind === 'stock') {
    applyPandoraMode(b, 'hidden');
    return;
  }
  const fresh = pandoraState.host === box.host && Date.now() - pandoraState.at < PANDORA_STATE_TTL_MS;
  if (!force && fresh) {
    applyPandoraMode(b, pandoraState.mode);
    return;
  }
  let mode = 'hidden';
  let answered = true;
  try {
    mode = pandoraFindMode(await USServicesStatus(box.host, box.port));
  } catch {
    // An older agent or app build has no such call, or the speaker did not
    // answer: no Pandora entry for now. Not cached, so the next view switch
    // or discovery cycle asks again instead of trusting a timeout (#1388).
    mode = 'hidden';
    answered = false;
  }
  // Another speaker was picked while the answer was on its way.
  if (!state.currentBox || state.currentBox.host !== box.host) return;
  pandoraState = { host: box.host, at: answered ? Date.now() : 0, mode };
  applyPandoraMode(b, mode);
  if (mode !== 'ready' && state.searchLastMode === 'pandora') {
    state.searchLastMode = 'top';
    state.searchListOpen = false;
    syncSearchModeButtons();
    $('searchResults').innerHTML = '';
  }
}

// applyPandoraMode draws the entry for one pandoraFindMode answer. The greyed
// form keeps the button focusable (aria-disabled, not disabled) so its hint
// can be read and is said when it is pressed.
export function applyPandoraMode(b, mode) {
  const unavailable = mode === 'unavailable';
  b.classList.toggle('hidden', mode !== 'ready' && !unavailable);
  b.classList.toggle('is-unavailable', unavailable);
  if (unavailable) b.setAttribute('aria-disabled', 'true');
  else b.removeAttribute('aria-disabled');
  b.title = unavailable ? t('pandora.unavailableHint') : t('pandora.btnTitle');
}

async function loadPandora() {
  if (!state.currentBox) { showError(t('search.errSelectSpeaker')); return; }
  const box = state.currentBox;
  state.searchLastMode = 'pandora';
  state.searchListOpen = true;
  syncSearchModeButtons();
  pandoraRows = [];
  const lm = $('loadMoreRow');
  if (lm) lm.classList.add('hidden');
  const cnt = $('searchCount');
  if (cnt) cnt.classList.add('hidden');
  $('searchResults').innerHTML = `<div class="muted">${escapeHtml(t('pandora.loading'))}</div>`;
  try {
    pandoraRows = pandoraStationRows(await PandoraStations(box.host, box.port));
  } catch (err) {
    if (state.searchLastMode !== 'pandora' || !state.searchListOpen) return;
    const code = agentErrorCode(err);
    const msg = code === 'not-linked' || code === 'service-off'
      ? t('pandora.notLinked')
      : t('pandora.loadFailed', { err: agentErrorText(err) });
    $('searchResults').innerHTML = `<div class="muted">${escapeHtml(msg)}</div>`;
    if (code === 'not-linked' || code === 'service-off') updatePandoraModeBtn(true);
    return;
  }
  if (state.searchLastMode !== 'pandora' || !state.searchListOpen || !state.currentBox || state.currentBox.host !== box.host) return;
  renderPandoraStations();
}

export function renderPandoraStations() {
  const res = $('searchResults');
  if (!res) return;
  if (!pandoraRows.length) {
    res.innerHTML = `<div class="muted">${escapeHtml(t('pandora.empty'))}</div>`;
    return;
  }
  res.innerHTML = pandoraRows.map((s, i) => {
    // The star keeps the station in Favourites next to the other stations
    // (#1663), the same star as on a station of the search.
    const fav = isFav(pandoraFavFromRow(s));
    return `
      <div class="result-row" data-i="${i}">
        <div class="result-logo">
          ${s.art ? `<img src="${escapeAttr(s.art)}" alt="" loading="lazy" referrerpolicy="no-referrer" class="fav pandora-art" />` : '<div class="fav-empty"></div>'}
        </div>
        <div class="result-text">
          <div class="result-name"><span class="result-name-text">${escapeHtml(s.name)}</span></div>
          <div class="result-meta">Pandora</div>
        </div>
        <div class="result-actions">
          <button class="btn btn-mini pandora-play" data-i="${i}" title="${escapeAttr(t('search.playNow'))}">&#9205;</button>
          ${s.presetable ? `<button class="btn btn-mini pandora-pick" data-i="${i}" title="${escapeAttr(t('search.assignToKey'))}">&#10133;</button>` : ''}
          <button class="btn btn-mini fav-toggle pandora-fav${fav ? ' is-fav' : ''}" data-i="${i}" aria-pressed="${fav ? 'true' : 'false'}" title="${escapeAttr(fav ? t('search.removeFav') : t('search.addFav'))}">${fav ? '&#9733;' : '&#9734;'}</button>
        </div>
      </div>`;
  }).join('');
  // No inline onerror (the CSP forbids inline script): a cover that does not
  // load is simply hidden.
  res.querySelectorAll('img.pandora-art').forEach(img => {
    img.onerror = () => { img.style.display = 'none'; };
  });
  res.querySelectorAll('.pandora-play').forEach(btn => {
    btn.onclick = async (e) => {
      e.stopPropagation();
      const s = pandoraRows[parseInt(btn.dataset.i, 10)];
      if (!s || !state.currentBox) return;
      btn.disabled = true;
      await playPandoraRow(s);
      btn.disabled = false;
    };
  });
  res.querySelectorAll('.pandora-fav').forEach(btn => {
    btn.onclick = (e) => {
      e.stopPropagation();
      const s = pandoraRows[parseInt(btn.dataset.i, 10)];
      if (!s) return;
      const nowFav = toggleFav(pandoraFavFromRow(s));
      btn.classList.toggle('is-fav', nowFav);
      btn.setAttribute('aria-pressed', nowFav ? 'true' : 'false');
      btn.innerHTML = nowFav ? '&#9733;' : '&#9734;';
      btn.title = nowFav ? t('search.removeFav') : t('search.addFav');
    };
  });
  res.querySelectorAll('.pandora-pick').forEach(btn => {
    btn.onclick = (e) => {
      e.stopPropagation();
      const s = pandoraRows[parseInt(btn.dataset.i, 10)];
      if (s) openPandoraPick(s);
    };
  });
}

// playPandoraRow starts a Pandora station on the selected speaker's own Pandora
// client: a row of the Pandora list, or a Pandora favourite (#1663). Reports
// what came of it as a toast or an error, and resolves to whether it started.
export async function playPandoraRow(s, report) {
  if (!s || !state.currentBox) return false;
  try {
    await PlayPandoraStation(state.currentBox.host, state.currentBox.port, s.location, s.name, s.type);
  } catch (err) {
    const code = agentErrorCode(err);
    const msg = code === 'not-linked' || code === 'service-off'
      ? t('pandora.notLinked')
      : t('pandora.playFailed', { err: agentErrorText(err) });
    if (report) report(msg, 'error'); else showError(msg);
    return false;
  }
  if (!report) showToast(t('pandora.starting', { name: s.name }));
  setTimeout(() => { refreshStatus(); }, 1500);
  setTimeout(() => { refreshStatus(); }, 4000);
  return true;
}

// The key is saved from the station item the list carried, so the station is
// not started first (#1595); a save from the Library does not play either.
export function openPandoraPick(s) {
  showSlotPicker({
    title: t('preset.assignStationTitle'),
    subtitle: s.name + ' (Pandora)',
    onPick: async (slot) => {
      await SavePandoraStationPreset(state.currentBox.host, state.currentBox.port, slot, s.name, s.location, s.type || '', s.art || '');
      showToast(t('preset.savedToKey', { n: keyNum(slot, t), name: s.name }));
    },
  });
}

// mountPandora attaches the handlers that used to be wired at module scope in
// main.js, once #view-box exists (called from mountMusicView).
export function mountPandora() {
  const open = listButtonClick('pandoraModeBtn', () => loadPandora());
  $('pandoraModeBtn').onclick = () => {
    // Greyed while the speaker lists Pandora UNAVAILABLE: say why, and ask the
    // speaker again so the entry opens again the moment Pandora is back (#1763).
    if ($('pandoraModeBtn').classList.contains('is-unavailable')) {
      showToast(t('pandora.unavailableHint'));
      updatePandoraModeBtn(true);
      return undefined;
    }
    return open();
  };
  // The settings card links or unlinks an account (or flips the opt-in): ask the
  // speaker again instead of waiting for the cache to run out.
  document.addEventListener('str:usservices-changed', () => { updatePandoraModeBtn(true); });
}
