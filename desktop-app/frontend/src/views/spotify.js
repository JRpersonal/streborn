// views/spotify.js — the "Spotify" (Beta) info view (#78).
//
// Extracted from the main.js monolith, same pattern as views/recent.js. This
// view is static info (how to use native Spotify Connect, what works / what
// does not) with two links; switchView is injected so the "update" link can
// jump to the speaker settings tab without importing back into main.js.

import { $, escapeHtml, escapeAttr, showToast, showError } from '../utils.js';
import { t } from '../i18n/index.js';
import { BrowserOpenURL, SyncSpotifyLogin, SpotifyQuality, SetSpotifyQuality } from '../api.js';

let deps = { switchView: () => {}, strBoxes: () => [] };
export function initSpotifyView(d) {
  deps = { ...deps, ...d };
}

// renderSpotifyAlpha paints the Spotify Beta info view (once).
export function renderSpotifyAlpha() {
  const root = $('view-spotify');
  if (!root) return;
  if (root.dataset.rendered === '1') {
    // The static info is painted once, but the per-speaker quality rows read
    // live state, so they refresh on every visit.
    refreshQualityList();
    return;
  }
  root.dataset.rendered = '1';
  const badge = `<span class="str-badge" title="${escapeAttr(t('common.strOnlyHint'))}">${escapeHtml(t('common.strOnly'))}</span>`;
  root.innerHTML = `
    <div class="spotify-page">
      <header class="spotify-hero">
        <h2>${escapeHtml(t('spotify.heading'))}</h2>
        <p class="spotify-lead">${escapeHtml(t('spotify.nativeIntro'))}</p>
      </header>

      <section class="sp-card">
        <h3 class="sp-card-title">${escapeHtml(t('spotify.playTitle'))}</h3>
        <ol class="sp-steps">
          <li>${escapeHtml(t('spotify.nativeStep1'))}</li>
          <li>${escapeHtml(t('spotify.nativeStep2'))}</li>
          <li>${escapeHtml(t('spotify.nativeStep3'))}</li>
        </ol>
        <div class="spotify-str-hint">${escapeHtml(t('spotify.dualConnectNote'))}</div>
        <p class="muted small sp-version">${escapeHtml(t('spotify.versionNote'))} <a href="#" id="spotifyUpdateLink">${escapeHtml(t('spotify.updateLink'))}</a></p>
      </section>

      <section class="sp-card">
        <h3 class="sp-card-title">${escapeHtml(t('spotify.presetsTitle'))}</h3>
        <p>${escapeHtml(t('spotify.presetsIntro'))}</p>
        <ol class="sp-steps">
          <li>${escapeHtml(t('spotify.presetsStep1'))}</li>
          <li>${escapeHtml(t('spotify.presetsStep2'))}</li>
          <li>${escapeHtml(t('spotify.presetsStep3'))}</li>
        </ol>
      </section>

      <section class="sp-card sp-tools">
        <div class="sp-tool">
          <h3 class="sp-card-title">${escapeHtml(t('spotify.qualityTitle'))}${badge}</h3>
          <p class="muted small">${escapeHtml(t('spotify.qualityDesc'))}</p>
          <div id="spotifyQualityList"></div>
        </div>
        <div class="sp-tool">
          <h3 class="sp-card-title">${escapeHtml(t('spotify.syncTitle'))}${badge}</h3>
          <p class="muted small">${escapeHtml(t('spotify.syncDesc'))}</p>
          <button class="btn btn-primary" id="spotifySyncBtn">${escapeHtml(t('spotify.syncBtn'))}</button>
        </div>
      </section>

      <details class="sp-details">
        <summary>${escapeHtml(t('spotify.worksTitle'))}</summary>
        <ul class="spotify-status">
          <li>${escapeHtml(t('spotify.works1'))}</li>
          <li>${escapeHtml(t('spotify.works2'))}</li>
          <li>${escapeHtml(t('spotify.works3'))}</li>
          <li>${escapeHtml(t('spotify.works4'))}</li>
        </ul>
      </details>
      <details class="sp-details">
        <summary>${escapeHtml(t('spotify.notesTitle'))}</summary>
        <ul class="spotify-status">
          <li>${escapeHtml(t('spotify.limit1'))}</li>
          <li>${escapeHtml(t('spotify.limit2'))}</li>
          <li>${escapeHtml(t('spotify.limit3'))}</li>
        </ul>
      </details>

      <p class="muted small sp-foot">${escapeHtml(t('spotify.nativeNote'))}</p>
      <p class="sp-foot sp-feedback">${escapeHtml(t('spotify.feedbackNote'))} <a href="#" id="spotifyIssueLink">${escapeHtml(t('spotify.issueLink'))}</a></p>
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
      // Name the SOURCE speaker. The backend picks it itself (the first
      // speaker that turns out to hold a Spotify login), and saying only "copied
      // to 4 speakers" left the user unable to tell what had just been copied
      // from where, or which speaker to log in differently if the answer was
      // wrong (asked in as many words, 2026-08-22).
      const src = (res && res.source) || '';
      if (n > 0 && failed === 0) showToast(src ? t('spotify.syncDoneFrom', { n, source: src }) : t('spotify.syncDone', { n }));
      else if (n > 0) showToast(src ? t('spotify.syncPartialFrom', { n, failed, source: src }) : t('spotify.syncPartial', { n, failed }));
      else showToast(t('spotify.syncNone'));
    } catch (e) {
      const s = String(e);
      // The backend rejects with a plain-English "no speaker is logged into
      // Spotify yet" error; show the localized equivalent (same text as the
      // empty-result path) instead of the raw string. Other errors fall through.
      if (s.toLowerCase().includes('no speaker is logged into spotify')) {
        showError(t('spotify.syncNone'));
      } else {
        showError(s);
      }
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
  refreshQualityList();
}

// refreshQualityList paints one row per STR speaker with its configured
// engine bitrate (#728). Speakers whose agent predates /spotify/quality (or
// is unreachable) show a hint instead of a selector. 320 kbps only reaches
// Premium accounts, which is why the option says so.
async function refreshQualityList() {
  const root = $('spotifyQualityList');
  if (!root) return;
  const boxes = (deps.strBoxes ? deps.strBoxes() : []) || [];
  if (!boxes.length) {
    root.innerHTML = `<p class="muted small">${escapeHtml(t('spotify.qualityNoBoxes'))}</p>`;
    return;
  }
  const states = await Promise.allSettled(boxes.map(b => SpotifyQuality(b.host, b.port)));
  root.innerHTML = boxes.map((b, i) => {
    const st = states[i].status === 'fulfilled' ? states[i].value : null;
    if (!st || !st.ok) {
      return `<div class="quality-row"><span>${escapeHtml(b.name)}</span>` +
        `<span class="muted small">${escapeHtml(t('spotify.qualityUnavailable'))}</span></div>`;
    }
    const high = st.bitrate === 320;
    return `<div class="quality-row"><span>${escapeHtml(b.name)}</span>` +
      `<select class="quality-select" data-host="${escapeHtml(b.host)}" data-port="${b.port}">` +
      `<option value="160"${high ? '' : ' selected'}>160 kbps</option>` +
      `<option value="320"${high ? ' selected' : ''}>320 kbps (Premium)</option>` +
      `</select>` +
      (st.pending ? `<span class="muted small">${escapeHtml(t('spotify.qualityPending'))}</span>` : '') +
      `</div>`;
  }).join('');
  root.querySelectorAll('select.quality-select').forEach(sel => {
    sel.onchange = async () => {
      sel.disabled = true;
      try {
        const res = await SetSpotifyQuality(sel.dataset.host, parseInt(sel.dataset.port, 10), parseInt(sel.value, 10));
        if (res && res.ok) showToast(res.applied ? t('spotify.qualitySet') : t('spotify.qualityPending'));
        else showError(t('spotify.qualityFailed'));
      } catch (e) {
        showError(String(e));
      } finally {
        sel.disabled = false;
      }
    };
  });
}
