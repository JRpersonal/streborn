// The donation affordance, in one place so its two homes cannot drift apart:
// the rail down the left edge of the window, and the footer dialog.
//
// The rail alone was not enough. It is a fixed overlay that CSS hides entirely
// below 62em, and because that threshold is in em it scales with the user's
// text size, so a narrow window or a large font removes it. The rule's own
// comment promised "below that the footer donate link carries it" and no such
// link existed, which is how a user who had just written that he wanted to buy
// a coffee reported the coffee button as missing (mail, 2026-09-09).
import { t } from './i18n/index.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { BrowserOpenURL } from './api.js';

// The one place the provider links live, keyed by the data-donate attribute
// donateButtonsHTML writes.
export const DONATE_URLS = {
  gh: 'https://github.com/sponsors/JRpersonal',
  paypal: 'https://paypal.me/JR31337',
  kofi: 'https://ko-fi.com/streborn',
};

// Octicons heart-fill-16, MIT licensed (https://github.com/primer/octicons).
const HEART_SVG = '<svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path fill="currentColor" d="m8 14.25.345.666a.75.75 0 0 1-.69 0l-.008-.004-.018-.01a7.152 7.152 0 0 1-.31-.17 22.055 22.055 0 0 1-3.434-2.414C2.045 10.731 0 8.35 0 5.5 0 2.836 2.086 1 4.25 1 5.797 1 7.153 1.802 8 3.02 8.847 1.802 10.203 1 11.75 1 13.914 1 16 2.836 16 5.5c0 2.85-2.045 5.231-3.885 6.818a22.066 22.066 0 0 1-3.744 2.584l-.018.01-.005.003h-.002Z"/></svg>';
// Ko-fi has no single canonical inline mark; this is a compact coffee-cup glyph
// (Simple Icons / Ko-fi brand kit composition) that stays recognisable at 14px.
const COFFEE_SVG = '<svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true"><path fill="currentColor" d="M20.216 6.415C19.964 5.43 19.066 4.78 18.057 4.78H5.943c-1.009 0-1.907.65-2.159 1.635C2.987 9.085 3 12.34 4.97 14.605c1.236 1.42 3.116 2.13 5.59 2.13 1.85 0 3.62-.404 4.97-1.137A6.43 6.43 0 0 0 19.046 14H19.5a3.5 3.5 0 1 0 0-7h-.07a4.8 4.8 0 0 0-.214-.585zM19 9.5h.5a1.5 1.5 0 0 1 0 3H19a4.21 4.21 0 0 0 .003-.123V9.535c0-.012-.002-.023-.003-.035zM7.5 19h11a.5.5 0 0 1 0 1h-11a.5.5 0 0 1 0-1z"/></svg>';

// donateButtonsHTML renders the three branded buttons. Brand colours and assets
// follow each provider's guidelines: GitHub Sponsors white with a Mona Pink
// border and heart, PayPal on #FFC439 with the two-tone wordmark, Ko-fi on
// #FF5E5B with the cup.
//
// Identified by a data attribute rather than by id, so the same markup can be
// on the page twice (rail and dialog) without two elements sharing an id.
export function donateButtonsHTML() {
  return `
    <button class="donate-btn donate-gh" data-donate="gh" type="button" title="GitHub Sponsors">
      <span class="donate-btn-icon">${HEART_SVG}</span>
      <span class="donate-btn-label">Sponsor</span>
    </button>
    <button class="donate-btn donate-paypal" data-donate="paypal" type="button" title="PayPal">
      <span class="donate-paypal-wordmark"><span class="pay">Pay</span><span class="pal">Pal</span></span>
    </button>
    <button class="donate-btn donate-kofi" data-donate="kofi" type="button" title="Ko-fi">
      <span class="donate-btn-icon">${COFFEE_SVG}</span>
      <span class="donate-btn-label">Ko-fi</span>
    </button>
  `;
}

// wireDonateButtons attaches the links to every donate button inside root.
export function wireDonateButtons(root) {
  if (!root || !root.querySelectorAll) return;
  root.querySelectorAll('[data-donate]').forEach(b => {
    const url = DONATE_URLS[b.dataset.donate];
    if (url) b.onclick = () => BrowserOpenURL(url);
  });
}

// donateFooterLinkHTML is the always-reachable way in. It sits in the footer,
// which is on screen at every window size and text size.
export function donateFooterLinkHTML() {
  return `<a href="#" id="footerDonate" class="footer-link" title="${escapeAttr(t('footer.donateSlogan'))}">&#9749; ${escapeHtml(t('credits.donate'))}</a>`;
}
