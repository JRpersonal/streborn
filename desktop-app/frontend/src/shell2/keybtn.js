// shell2/keybtn.js: the one "Put on key" control of the UI 2.0 preview
// (#1286 phase 2). Radio, Library and Recently played all put something on one
// of the six keys of a speaker; they draw that action the same way: the six
// keys icon and the words "Put on key", and a name that starts with those
// words and adds what goes on which speaker ("Put on key: Jazz24 on Kitchen"),
// so a voice command that says what it sees finds the button. Where a row has
// no room for the words (a larger text size in a narrow window) the CSS
// (shell2.css .u-keybtn) leaves the icon; the name stays.
//
// Pure: strings only, so vitest reads it without a DOM.

import { t } from '../i18n/index.js';
import { escapeAttr, escapeHtml } from '../utils.js';

// The six keys on top of a speaker, two rows of three.
export const KEYS_ICON = '<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" focusable="false"><g fill="none" stroke="currentColor" stroke-width="1.8"><rect x="2.5" y="6" width="5" height="5" rx="1"/><rect x="9.5" y="6" width="5" height="5" rx="1"/><rect x="16.5" y="6" width="5" height="5" rx="1"/><rect x="2.5" y="13" width="5" height="5" rx="1"/><rect x="9.5" y="13" width="5" height="5" rx="1"/><rect x="16.5" y="13" width="5" height="5" rx="1"/></g></svg>';

// keyButtonName is the accessible name: the visible words first, then what
// and where. Without a speaker (none chosen yet) it names only what.
export function keyButtonName(name, speaker) {
  return speaker
    ? t('ui2.radio.putOnKeyName', { name, speaker })
    : `${t('ui2.radio.putOnKey')}: ${name}`;
}

// keyButtonHTML is the button. cls adds the view's own class (the hook its
// handler looks for), attrs any data-* or id the view needs, raw.
export function keyButtonHTML({ cls = '', attrs = '', name = '', speaker = '', extra = '' } = {}) {
  const label = keyButtonName(name, speaker);
  return `<button type="button" class="u-btn u-btn--secondary u-keybtn${cls ? ` ${cls}` : ''}" ${attrs} aria-label="${escapeAttr(label)}" title="${escapeAttr(label)}"${extra}>`
    + `${KEYS_ICON}<span class="u-keybtn-text">${escapeHtml(t('ui2.radio.putOnKey'))}</span></button>`;
}
