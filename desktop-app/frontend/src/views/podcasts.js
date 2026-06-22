// views/podcasts.js — the "Podcasts" tab.
//
// Podcast support is planned, not built (see the design/feedback thread on
// GitHub). For now this tab is a placeholder that explains the idea and links
// to the feedback thread so users can shape the feature before it lands. It
// follows the same self-contained view pattern as the other views/*.js modules:
// it pulls everything it needs from the shared modules and exposes a render
// entry point that main.js's switchView calls.

import { $, escapeHtml, escapeAttr, showError } from '../utils.js';
import { t } from '../i18n/index.js';
import { BrowserOpenURL } from '../api.js';

// Design + feedback thread for podcast support. Kept here (not in appInfo)
// because it is a fixed project URL, like the donate links in main.js.
const FEEDBACK_URL = 'https://github.com/JRpersonal/streborn/issues/215';

// initPodcastsView exists for symmetry with the other views (which receive
// injected main.js helpers). This view needs none yet, but keeping the hook
// means wiring it up later — when the real feature lands — matches the others.
export function initPodcastsView() {}

// renderPodcasts paints the placeholder into #view-podcasts. Called from
// main.js's switchView when the Podcasts tab is opened.
export function renderPodcasts() {
  const root = $('view-podcasts');
  if (!root) return;
  root.innerHTML = `
    <div class="podcasts-intro">
      <h2 class="podcasts-title">${escapeHtml(t('podcasts.title'))}
        <span class="beta-pill planned-pill">${escapeHtml(t('common.planned'))}</span>
      </h2>
      <p class="podcasts-lead">${escapeHtml(t('podcasts.lead'))}</p>
      <p class="muted">${escapeHtml(t('podcasts.sources'))}</p>
      <p class="podcasts-ask">${escapeHtml(t('podcasts.ask'))}</p>
      <div class="podcasts-actions">
        <button class="btn btn-primary" id="podcastsFeedbackBtn" title="${escapeAttr(t('podcasts.feedbackHint'))}">
          ${escapeHtml(t('podcasts.feedbackBtn'))}
        </button>
      </div>
      <p class="muted small">${escapeHtml(t('podcasts.feedbackHint'))}</p>
    </div>
  `;
  const btn = $('podcastsFeedbackBtn');
  if (btn) btn.onclick = () => { try { BrowserOpenURL(FEEDBACK_URL); } catch (e) { showError(e); } };
}
