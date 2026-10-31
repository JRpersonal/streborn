// views/spotify.js — the "Spotify" (Beta) info view (#78).
//
// Extracted from the main.js monolith, same pattern as views/recent.js. This
// view is static info (how to use native Spotify Connect, what works / what
// does not) with two links; switchView is injected so the "update" link can
// jump to the speaker settings tab without importing back into main.js.

import { $, escapeHtml, escapeAttr, friendlyErrorText, showToast, showError } from '../utils.js';
import { t } from '../i18n/index.js';
import { BoxAgentVersion, BrowserOpenURL, SyncSpotifyLogin, SpotifyQuality, SetSpotifyQuality } from '../api.js';
import { shell2Mounted } from '../shell2/mount.js';
import { openSpeakers } from '../shell2/speakers/index.js';
import { refreshAccounts, renderSpotifyUi2, spotifyUi2Markup } from './spotifyui2.js';
import { qualityOptions, qualityStatusText } from '../spotifyquality.js';

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
    if (shell2Mounted()) renderSpotifyUi2(deps);
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
${shell2Mounted() ? spotifyUi2Markup() : ''}

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
        <div class="sp-prereq">
          <div class="sp-prereq-title">${escapeHtml(t('spotify.presetsNeedTitle'))}</div>
          <ul>
            <li>${escapeHtml(t('spotify.presetsNeedPremium'))}</li>
            <li>${escapeHtml(t('spotify.presetsNeedStr'))}</li>
          </ul>
        </div>
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
    // The UI 2.0 preview keeps the button in place and says everything in
    // the line under it (syncUi2).
    if (shell2Mounted()) { await syncUi2(sync); return; }
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
      // The UI 2.0 accounts list shows the copied login right away.
      if (shell2Mounted()) refreshAccounts();
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
    try { BrowserOpenURL('https://github.com/JRpersonal/streborn/issues/new?template=bug_report.yml'); } catch {}
  };
  refreshQualityList();
  if (shell2Mounted()) renderSpotifyUi2(deps);
  syncButtonUi2();
}

// syncStatusLine is the preview's line under the copy-login button (role=
// status), made on first use.
function syncStatusLine() {
  const btn = $('spotifySyncBtn');
  if (!btn) return null;
  let line = $('spotifySyncStatus');
  if (!line) {
    line = document.createElement('p');
    line.id = 'spotifySyncStatus';
    line.setAttribute('role', 'status');
    btn.insertAdjacentElement('afterend', line);
  }
  return line;
}

function setSyncStatus(text, kind = '') {
  const line = syncStatusLine();
  if (!line) return;
  line.className = `sp2-sync-status${kind ? ` is-${kind}` : ''}`;
  line.textContent = text || '';
}

// syncButtonUi2 marks the copy-login button in the preview: with fewer than
// two speakers it is aria-disabled (still focusable) and the reason is
// visible text right under it, not a toast after the click.
function syncButtonUi2() {
  if (!shell2Mounted()) return;
  const btn = $('spotifySyncBtn');
  if (!btn) return;
  const boxes = (deps.strBoxes ? deps.strBoxes() : []) || [];
  const line = syncStatusLine();
  if (boxes.length < 2) {
    btn.setAttribute('aria-disabled', 'true');
    if (line) btn.setAttribute('aria-describedby', line.id);
    setSyncStatus(t('spotify.syncNeedTwo'));
    return;
  }
  if (btn.getAttribute('aria-busy') === 'true') return;
  btn.removeAttribute('aria-disabled');
  btn.removeAttribute('aria-describedby');
  if (line && line.textContent === t('spotify.syncNeedTwo')) setSyncStatus('');
}

// syncUi2 copies the Spotify login in the preview and reports in the line
// under the button: the source speaker, how many took it, or why not.
async function syncUi2(btn) {
  if (btn.getAttribute('aria-disabled') === 'true') return;
  const boxes = (deps.strBoxes ? deps.strBoxes() : []) || [];
  if (boxes.length < 2) { syncButtonUi2(); return; }
  const orig = btn.textContent;
  btn.setAttribute('aria-disabled', 'true');
  btn.setAttribute('aria-busy', 'true');
  btn.textContent = t('spotify.syncRunning');
  setSyncStatus(t('spotify.syncRunning'), 'busy');
  try {
    const res = await SyncSpotifyLogin(boxes);
    const n = (res && Array.isArray(res.synced)) ? res.synced.length : 0;
    const failed = (res && res.failed) ? Object.keys(res.failed).length : 0;
    const src = (res && res.source) || '';
    if (n > 0 && failed === 0) setSyncStatus(src ? t('spotify.syncDoneFrom', { n, source: src }) : t('spotify.syncDone', { n }), 'ok');
    else if (n > 0) setSyncStatus(src ? t('spotify.syncPartialFrom', { n, failed, source: src }) : t('spotify.syncPartial', { n, failed }), 'warn');
    else setSyncStatus(t('spotify.syncNone'), 'warn');
    refreshAccounts();
  } catch (e) {
    const s = String(e);
    setSyncStatus(s.toLowerCase().includes('no speaker is logged into spotify') ? t('spotify.syncNone') : friendlyErrorText(s), 'error');
  } finally {
    btn.removeAttribute('aria-busy');
    btn.removeAttribute('aria-disabled');
    btn.textContent = orig;
  }
}

// refreshQualityList paints one row per STR speaker with its configured
// engine bitrate (#728). Speakers whose agent predates /spotify/quality (or
// is unreachable) show a hint instead of a selector. 320 kbps only reaches
// Premium accounts, which is why the option says so. Below the selector the
// row names the active bitrate and why ("320 kbps (Premium default)"), see
// spotifyquality.js.
async function refreshQualityList() {
  const root = $('spotifyQualityList');
  if (!root) return;
  const boxes = (deps.strBoxes ? deps.strBoxes() : []) || [];
  if (!boxes.length) {
    root.innerHTML = `<p class="muted small">${escapeHtml(t('spotify.qualityNoBoxes'))}</p>`;
    return;
  }
  if (shell2Mounted()) { await refreshQualityListUi2(root, boxes); return; }
  const states = await Promise.allSettled(boxes.map(b => SpotifyQuality(b.host, b.port)));
  root.innerHTML = boxes.map((b, i) => {
    const st = states[i].status === 'fulfilled' ? states[i].value : null;
    if (!st || !st.ok) {
      return `<div class="quality-row"><span>${escapeHtml(b.name)}</span>` +
        `<span class="muted small">${escapeHtml(t('spotify.qualityUnavailable'))}</span></div>`;
    }
    const opts = qualityOptions(st, t).map(o =>
      `<option value="${o.value}"${o.selected ? ' selected' : ''}>${escapeHtml(o.label)}</option>`).join('');
    const status = qualityStatusText(st, t);
    return `<div class="quality-row"><span>${escapeHtml(b.name)}</span>` +
      `<select class="quality-select" data-host="${escapeHtml(b.host)}" data-port="${b.port}">` +
      opts +
      `</select>` +
      (status ? `<span class="muted small quality-active">${escapeHtml(status)}</span>` : '') +
      (st.pending ? `<span class="muted small">${escapeHtml(t('spotify.qualityPending'))}</span>` : '') +
      `</div>`;
  }).join('');
  root.querySelectorAll('select.quality-select').forEach(sel => {
    sel.onchange = async () => {
      sel.disabled = true;
      try {
        const res = await SetSpotifyQuality(sel.dataset.host, parseInt(sel.dataset.port, 10), parseInt(sel.value, 10));
        if (res && res.ok) {
          showToast(res.applied ? t('spotify.qualitySet') : t('spotify.qualityPending'));
          // Repaint so the active bitrate and its reason follow the change.
          refreshQualityList();
          return;
        }
        showError(t('spotify.qualityFailed'));
      } catch (e) {
        showError(String(e));
      } finally {
        sel.disabled = false;
      }
    };
  });
}

// What each speaker's quality row last reported in the preview, by host, so
// a repaint of the list keeps "Kitchen: 320 kbps set." until it was read.
const qualityStatus = new Map();

// qualityChoiceText names a choice the way the row says it: "Automatic" or
// "320 kbps".
function qualityChoiceText(kbps) {
  return kbps ? `${kbps} kbps` : t('spotify.qualityAuto');
}

// qualityRowStatusHTML is a row's status line (role=status), empty until
// there is news.
function qualityRowStatusHTML(host) {
  const st = qualityStatus.get(host);
  return `<p class="sp2-q-status${st && st.kind ? ` is-${st.kind}` : ''}" role="status">${escapeHtml((st && st.text) || '')}</p>`;
}

// refreshQualityListUi2 is the preview's quality list: a loading line while
// the speakers are asked, then one row per speaker. A speaker that did not
// answer at all says so, apart from one whose agent is too old for the
// setting (that one gets an Update button to its page); each selector names
// its speaker, and a change reports in its own row, "set" only when the
// speaker applied it.
async function refreshQualityListUi2(root, boxes) {
  root.setAttribute('aria-busy', 'true');
  root.innerHTML = `<p class="sp2-q-loading" role="status">${escapeHtml(t('ui2.feedback.qualityLoading'))}</p>`;
  const states = await Promise.allSettled(boxes.map((b) => SpotifyQuality(b.host, b.port)));
  // No quality answer: ask the agent's version, which every STR agent has.
  // An answer there means the speaker is on but its agent predates the
  // setting; none means the speaker is not reachable.
  const reach = await Promise.allSettled(boxes.map((b, i) => {
    const st = states[i].status === 'fulfilled' ? states[i].value : null;
    if (st && st.ok) return Promise.resolve(true);
    if (b.offline) return Promise.resolve(false);
    return BoxAgentVersion(b.host, b.port).then((v) => !!v);
  }));
  root.setAttribute('aria-busy', 'false');
  root.innerHTML = boxes.map((b, i) => {
    const name = b.name || b.host;
    const st = states[i].status === 'fulfilled' ? states[i].value : null;
    const host = escapeAttr(b.host);
    if (!st || !st.ok) {
      const reachable = reach[i].status === 'fulfilled' && reach[i].value === true;
      if (!reachable) {
        return `<div class="quality-row sp2-q-row" data-host="${host}" data-state="unreachable"><span class="sp2-q-name">${escapeHtml(name)}</span>`
          + `<span class="sp2-q-note">${escapeHtml(t('ui2.feedback.qualityUnreachable', { name }))}</span></div>`;
      }
      return `<div class="quality-row sp2-q-row" data-host="${host}" data-state="update"><span class="sp2-q-name">${escapeHtml(name)}</span>`
        + `<span class="sp2-q-note">${escapeHtml(t('ui2.feedback.qualityNeedsUpdate'))}</span>`
        + `<button type="button" class="u-btn u-btn--secondary sp2-q-update" data-host="${host}">${escapeHtml(t('ui2.feedback.updateSpeaker', { name }))}</button></div>`;
    }
    const opts = qualityOptions(st, t).map((o) =>
      `<option value="${o.value}"${o.selected ? ' selected' : ''}>${escapeHtml(o.label)}</option>`).join('');
    const status = qualityStatusText(st, t);
    if (st.pending && !qualityStatus.has(b.host)) qualityStatus.set(b.host, { kind: 'warn', text: `${name}: ${t('spotify.qualityPending')}` });
    return `<div class="quality-row sp2-q-row" data-host="${host}" data-state="ok"><span class="sp2-q-name">${escapeHtml(name)}</span>`
      + `<select class="quality-select" data-host="${host}" data-port="${b.port}" data-name="${escapeAttr(name)}" aria-label="${escapeAttr(`${t('spotify.qualityTitle')}: ${name}`)}">${opts}</select>`
      + `<span class="muted small quality-active">${escapeHtml(status)}</span>`
      + qualityRowStatusHTML(b.host)
      + '</div>';
  }).join('');
  root.querySelectorAll('.sp2-q-update').forEach((btn) => {
    btn.onclick = () => openSpeakers('speaker', { host: btn.dataset.host, tab: 'overview' });
  });
  root.querySelectorAll('select.quality-select').forEach((sel) => {
    sel.onchange = () => setQualityUi2(sel);
  });
}

// setQualityUi2 sends one speaker's choice and says in its row what came of
// it: "set" only when the speaker applied it, "saved, applies when playback
// stops" when it only stored it, the refusal otherwise. The row stays where
// it is (no repaint of the list), so the keyboard stays on the selector.
async function setQualityUi2(sel) {
  const row = sel.closest('.sp2-q-row');
  const line = row ? row.querySelector('.sp2-q-status') : null;
  const host = sel.dataset.host;
  const name = sel.dataset.name || host;
  const kbps = parseInt(sel.value, 10) || 0;
  const choice = qualityChoiceText(kbps);
  const say = (kind, text) => {
    qualityStatus.set(host, { kind, text });
    if (!line) return;
    line.className = `sp2-q-status is-${kind}`;
    line.textContent = text;
  };
  if (sel.getAttribute('aria-busy') === 'true') return;
  sel.setAttribute('aria-busy', 'true');
  say('busy', t('ui2.feedback.qualitySetting', { name, choice }));
  try {
    const res = await SetSpotifyQuality(host, parseInt(sel.dataset.port, 10), kbps);
    if (res && res.ok) {
      say(res.applied ? 'ok' : 'warn', t(res.applied ? 'ui2.feedback.qualitySet' : 'ui2.feedback.qualityPending', { name, choice }));
      const active = row ? row.querySelector('.quality-active') : null;
      const text = qualityStatusText({ ...res, ok: true }, t);
      if (active && text) active.textContent = text;
      return;
    }
    say('error', t('ui2.feedback.qualityFailed', { name, reason: t('spotify.qualityFailed') }));
  } catch (e) {
    say('error', t('ui2.feedback.qualityFailed', { name, reason: friendlyErrorText(String(e)) }));
  } finally {
    sel.removeAttribute('aria-busy');
  }
}
