// views/music/deezerlink.js: a Deezer link pasted into the station search.
//
// A Deezer playlist, album or track plays on the speaker's own Deezer client
// (docs/streaming/deezer.md): the search shows one card for the link with a
// Play button instead of a radio lookup, and the agent hands the item to the
// speaker by its id. A key held while it plays keeps it there, through the
// ordinary hold-to-save (Deezer is a native service, nativeservice.js).

import { PlayDeezerLink } from '../../api.js';
import { t } from '../../i18n/index.js';
import { state } from '../../state.js';
import { $, escapeHtml, escapeAttr } from '../../utils.js';
import { deezerLinkKindKey, deezerPlayError, parseDeezerLink } from '../../deezer.js';

function speakerName(box) {
  return (box && (box.name || box.friendlyName || box.host)) || '';
}

// renderDeezerLinkCard draws the card for a pasted link into the result list.
export function renderDeezerLinkCard(q) {
  const res = $('searchResults');
  if (!res) return;
  const link = parseDeezerLink(q);
  if (!link) return;
  state.searchResults = [];
  const lm = $('loadMoreRow');
  if (lm) lm.classList.add('hidden');
  const box = state.currentBox;
  const name = speakerName(box);
  const what = t(deezerLinkKindKey(link));
  const id = link.id ? ` ${escapeHtml(link.id)}` : '';
  res.innerHTML = `<div class="result-row deezer-link-card" data-link="${escapeAttr(String(q).trim())}">
      <div class="result-main">
        <div class="result-name">Deezer · ${escapeHtml(what)}${id}</div>
        <div class="result-meta muted small">${escapeHtml(t('deezer.linkCardNote'))}</div>
        <div class="small" id="deezerLinkMsg" role="status" aria-live="polite"></div>
      </div>
      <button class="btn btn-primary" id="deezerLinkPlay"${box ? '' : ' disabled'}>${escapeHtml(t('deezer.linkPlay', { name }))}</button>
    </div>`;
  const btn = $('deezerLinkPlay');
  if (btn) btn.onclick = () => { playDeezerLink(String(q).trim(), btn).catch(() => {}); };
}

async function playDeezerLink(link, btn) {
  const box = state.currentBox;
  const out = $('deezerLinkMsg');
  if (!box || !out) return;
  const name = speakerName(box);
  btn.disabled = true;
  out.className = 'small muted';
  out.textContent = t('deezer.linkStarting');
  try {
    const got = await PlayDeezerLink(box.host, box.port, link, '');
    out.className = 'small setup-ok';
    out.textContent = t('deezer.linkStarted', { title: (got && got.name) || 'Deezer', name });
  } catch (e) {
    const [k, p] = deezerPlayError(e);
    out.className = 'small setup-err';
    out.textContent = t(k, p);
  }
  btn.disabled = false;
}
