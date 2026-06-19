// views/spotify.js — the "Spotify" (Beta) info view (#78).
//
// Extracted from the main.js monolith, same pattern as views/recent.js. This
// view is static info (how to use native Spotify Connect, what works / what
// does not) with two links; switchView is injected so the "update" link can
// jump to the speaker settings tab without importing back into main.js.

import { $, escapeHtml, showToast, showError } from '../utils.js';
import { t } from '../i18n/index.js';
import { BrowserOpenURL, SyncSpotifyLogin } from '../api.js';

let deps = { switchView: () => {}, strBoxes: () => [] };
export function initSpotifyView(d) {
  deps = { ...deps, ...d };
}

// renderSpotifyAlpha paints the Spotify Beta info view (once).
export function renderSpotifyAlpha() {
  const root = $('view-spotify');
  if (!root || root.dataset.rendered === '1') return;
  root.dataset.rendered = '1';
  root.innerHTML = `
    <div class="alpha-stage">
      <h2>${escapeHtml(t('spotify.heading'))} <span class="beta-pill">${escapeHtml(t('common.beta'))}</span></h2>
      <p>${escapeHtml(t('spotify.nativeIntro'))}</p>
      <ol class="alpha-checklist">
        <li>${escapeHtml(t('spotify.nativeStep1'))}</li>
        <li>${escapeHtml(t('spotify.nativeStep2'))}</li>
        <li>${escapeHtml(t('spotify.nativeStep3'))}</li>
      </ol>
      <p class="muted small">${escapeHtml(t('spotify.versionNote'))} <a href="#" id="spotifyUpdateLink">${escapeHtml(t('spotify.updateLink'))}</a></p>
      <h3>${escapeHtml(t('spotify.presetsTitle'))}</h3>
      <p>${escapeHtml(t('spotify.presetsIntro'))}</p>
      <ol class="alpha-checklist">
        <li>${escapeHtml(t('spotify.presetsStep1'))}</li>
        <li>${escapeHtml(t('spotify.presetsStep2'))}</li>
        <li>${escapeHtml(t('spotify.presetsStep3'))}</li>
      </ol>
      <h3>${escapeHtml(t('spotify.worksTitle'))}</h3>
      <ul class="spotify-status">
        <li>${escapeHtml(t('spotify.works1'))}</li>
        <li>${escapeHtml(t('spotify.works2'))}</li>
        <li>${escapeHtml(t('spotify.works3'))}</li>
        <li>${escapeHtml(t('spotify.works4'))}</li>
      </ul>
      <h3>${escapeHtml(t('spotify.limitsTitle'))}</h3>
      <ul class="spotify-status">
        <li>${escapeHtml(t('spotify.limit1'))}</li>
        <li>${escapeHtml(t('spotify.limit2'))}</li>
        <li>${escapeHtml(t('spotify.limit3'))}</li>
      </ul>
      <h3>${escapeHtml(t('spotify.syncTitle'))}</h3>
      <p>${escapeHtml(t('spotify.syncDesc'))}</p>
      <button class="btn btn-primary" id="spotifySyncBtn">${escapeHtml(t('spotify.syncBtn'))}</button>
      <p class="muted small">${escapeHtml(t('spotify.nativeNote'))}</p>
      <p>${escapeHtml(t('spotify.feedbackNote'))} <a href="#" id="spotifyIssueLink">${escapeHtml(t('spotify.issueLink'))}</a></p>
    </div>
  `;
  const sync = $('spotifySyncBtn');
  if (sync) sync.onclick = async () => {
    const boxes = (deps.strBoxes ? deps.strBoxes() : []) || [];
    if (boxes.length < 2) { showToast(t('spotify.syncNeedTwo')); return; }
    sync.disabled = true;
    const orig = sync.textContent;
    sync.textContent = t('spotify.syncRunning');
    try {
      const res = await SyncSpotifyLogin(boxes);
      const n = (res && Array.isArray(res.synced)) ? res.synced.length : 0;
      const failed = (res && res.failed) ? Object.keys(res.failed).length : 0;
      if (n > 0 && failed === 0) showToast(t('spotify.syncDone', { n }));
      else if (n > 0) showToast(t('spotify.syncPartial', { n, failed }));
      else showToast(t('spotify.syncNone'));
    } catch (e) {
      showError(String(e));
    } finally {
      sync.disabled = false;
      sync.textContent = orig;
    }
  };
  const upd = $('spotifyUpdateLink');
  if (upd) upd.onclick = (e) => { e.preventDefault(); deps.switchView('settings'); };
  const link = $('spotifyIssueLink');
  if (link) link.onclick = (e) => {
    e.preventDefault();
    try { BrowserOpenURL('https://github.com/JRpersonal/streborn/issues/78'); } catch {}
  };
}
