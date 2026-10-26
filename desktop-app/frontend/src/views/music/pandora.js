// views/music/pandora.js: the Pandora station list of the search box (US services).

import { PandoraStations, PlayPandoraStation, SaveNativePreset, USServicesStatus } from '../../api.js';
import { t } from '../../i18n/index.js';
import { state } from '../../state.js';
import { agentErrorCode, agentErrorText, pandoraFindVisible, pandoraStationRows } from '../../usservices.js';
import { $, escapeAttr, escapeHtml, showError, showToast, sleep } from '../../utils.js';
import { showSlotPicker } from './picker.js';
import { syncSearchModeButtons } from './search.js';
import { refreshStatus } from './status.js';

// ---------- Pandora stations ----------
//
// A speaker with a linked Pandora account plays its stations itself; the agent
// reads the station list from the speaker and hands a station back to it
// (docs/streaming/us-services.md). The entry sits next to Top/Search/Favorites
// and only appears while the speaker lists Pandora as READY (pandoraFindVisible).

// How long one speaker's Pandora state is trusted before the speaker is asked
// again. Short enough that a fresh link shows up on the next visit, long enough
// that the status poll and tab switches do not keep asking.
const PANDORA_STATE_TTL_MS = 60 * 1000;
let pandoraState = { host: '', at: 0, visible: false };
let pandoraRows = [];

export async function updatePandoraModeBtn(force) {
  const b = $('pandoraModeBtn');
  if (!b) return;
  const box = state.currentBox;
  if (!box || !box.host || box.kind === 'stock') {
    b.classList.add('hidden');
    return;
  }
  const fresh = pandoraState.host === box.host && Date.now() - pandoraState.at < PANDORA_STATE_TTL_MS;
  if (!force && fresh) {
    b.classList.toggle('hidden', !pandoraState.visible);
    return;
  }
  let visible = false;
  let answered = true;
  try {
    visible = pandoraFindVisible(await USServicesStatus(box.host, box.port));
  } catch {
    // An older agent or app build has no such call, or the speaker did not
    // answer: no Pandora entry for now. Not cached, so the next view switch
    // or discovery cycle asks again instead of trusting a timeout (#1388).
    visible = false;
    answered = false;
  }
  // Another speaker was picked while the answer was on its way.
  if (!state.currentBox || state.currentBox.host !== box.host) return;
  pandoraState = { host: box.host, at: answered ? Date.now() : 0, visible };
  b.classList.toggle('hidden', !visible);
  if (!visible && state.searchLastMode === 'pandora') {
    state.searchLastMode = 'top';
    state.searchListOpen = false;
    syncSearchModeButtons();
    $('searchResults').innerHTML = '';
  }
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
    if (state.searchLastMode !== 'pandora') return;
    const code = agentErrorCode(err);
    const msg = code === 'not-linked' || code === 'service-off'
      ? t('pandora.notLinked')
      : t('pandora.loadFailed', { err: agentErrorText(err) });
    $('searchResults').innerHTML = `<div class="muted">${escapeHtml(msg)}</div>`;
    if (code === 'not-linked' || code === 'service-off') updatePandoraModeBtn(true);
    return;
  }
  if (state.searchLastMode !== 'pandora' || !state.currentBox || state.currentBox.host !== box.host) return;
  renderPandoraStations();
}

export function renderPandoraStations() {
  const res = $('searchResults');
  if (!res) return;
  if (!pandoraRows.length) {
    res.innerHTML = `<div class="muted">${escapeHtml(t('pandora.empty'))}</div>`;
    return;
  }
  res.innerHTML = pandoraRows.map((s, i) => `
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
        </div>
      </div>`).join('');
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
      try {
        await PlayPandoraStation(state.currentBox.host, state.currentBox.port, s.location, s.name, s.type);
        showToast(t('pandora.starting', { name: s.name }));
        setTimeout(() => { refreshStatus(); }, 1500);
        setTimeout(() => { refreshStatus(); }, 4000);
      } catch (err) {
        showError(t('pandora.playFailed', { err: agentErrorText(err) }));
      }
      btn.disabled = false;
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

// playPandoraAndWait starts the station unless it already plays and waits
// until the speaker reports it. The key is saved from the speaker's own
// now-playing (SaveNativePreset), so it has to be the station on the key.
async function playPandoraAndWait(s) {
  const box = state.currentBox;
  try { await refreshStatus(); } catch {}
  if (state.nowLocation === s.location) return;
  await PlayPandoraStation(box.host, box.port, s.location, s.name, s.type);
  for (let i = 0; i < 12; i++) {
    await sleep(1000);
    try { await refreshStatus(); } catch {}
    if (state.nowLocation === s.location) return;
  }
}

export function openPandoraPick(s) {
  showSlotPicker({
    title: t('preset.assignStationTitle'),
    subtitle: s.name + ' (Pandora)',
    onPick: async (slot) => {
      showToast(t('pandora.savePlayingFirst', { name: s.name }));
      await playPandoraAndWait(s);
      await SaveNativePreset(state.currentBox.host, state.currentBox.port, slot, s.name);
      showToast(t('preset.savedToKey', { n: slot, name: s.name }));
    },
  });
}

// mountPandora attaches the handlers that used to be wired at module scope in
// main.js, once #view-box exists (called from mountMusicView).
export function mountPandora() {
  $('pandoraModeBtn').onclick = () => loadPandora();
  // The settings card links or unlinks an account (or flips the opt-in): ask the
  // speaker again instead of waiting for the cache to run out.
  document.addEventListener('str:usservices-changed', () => { updatePandoraModeBtn(true); });
}
