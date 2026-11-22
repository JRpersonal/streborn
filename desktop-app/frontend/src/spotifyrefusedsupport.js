// spotifyrefusedsupport.js: the two buttons the Spotify refused notice offers
// when Spotify itself refuses a Premium account on (STR) (spotifyrefused.js,
// refusalSupportFor): open Spotify's contact page, and copy a ready message
// for Spotify support to the clipboard.
//
// The notice is drawn as innerHTML in two places (the now-playing row and the
// UI 2.0 accounts row), so the clicks are wired by delegation: one listener on
// the document, installed once, that acts on the data-str-refusal attribute.
// The "Copied" feedback is set on the live button and put back after a short
// while; it never goes into the notice's HTML, so the now-playing row's
// rebuild-on-change check is not tripped by it.

import { BrowserOpenURL, ClipboardSetText } from './api.js';
import { t } from './i18n/index.js';
import { SPOTIFY_SUPPORT_URL } from './spotifyrefused.js';

export const COPIED_MS = 2000;

// refusalSupportAction does what one click asks for: 'open' opens the support
// page, 'copy' copies the message and shows "Copied" on the button for
// COPIED_MS. deps carries the side effects so a test can drive it without a
// Wails runtime or a DOM. Returns the action it took, '' for none.
export function refusalSupportAction(btn, deps) {
  const action = btn && btn.getAttribute ? btn.getAttribute('data-str-refusal') : '';
  if (action === 'open') {
    deps.open(SPOTIFY_SUPPORT_URL);
    return action;
  }
  if (action !== 'copy') return '';
  const label = deps.t('spotify.refusedSupportCopy');
  Promise.resolve()
    .then(() => deps.copy(deps.t('spotify.refusedSupportMessage')))
    .then((ok) => {
      // Wails answers false when the clipboard refused the text.
      if (ok === false) return;
      btn.textContent = deps.t('spotify.refusedSupportCopied');
      deps.later(() => { btn.textContent = label; }, COPIED_MS);
    })
    .catch(() => {});
  return action;
}

let installed = false;

// installRefusalSupport puts the one document-level listener in place. Safe
// to call from every render: it installs only once.
export function installRefusalSupport(doc = globalThis.document) {
  if (installed || !doc || !doc.addEventListener) return;
  installed = true;
  doc.addEventListener('click', (e) => {
    const btn = e.target && e.target.closest ? e.target.closest('[data-str-refusal]') : null;
    if (!btn) return;
    e.preventDefault();
    refusalSupportAction(btn, {
      open: (url) => BrowserOpenURL(url),
      copy: (text) => ClipboardSetText(text),
      later: (fn, ms) => setTimeout(fn, ms),
      t,
    });
  });
}
