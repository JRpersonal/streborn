// shell2/speakers/sectionmsg.js: the outcome of a setting, shown in the card
// it belongs to (UI 2.0 phase 5, docs/UI-2.0.md "Decided for later" and rule
// 3, errors where they happen).
//
// In the classic layout every refused write on Speaker settings opened the
// window-wide error dialog, and every success a toast at the bottom of the
// window. On the preview's speaker page the settings sit in cards, one tab at
// a time, so a refusal belongs into the card the user just pressed in:
//
//   - one line per card, <div class="sps-msg sps-msg--error" role="alert">,
//     that names the speaker and says what it answered (settingFailed), with
//     Details (the error dialog with the full, copyable text) and Close;
//   - placed right after the control's .setting-row, or under the pressed
//     button in a long card, never inside a <form> (a line inside the Wi-Fi
//     form would be read as one of its fields);
//   - the next success in the same card takes it away (clearSectionMsg).
//
// A warning (a copy that only partly worked) uses the same line with
// sps-msg--warn and no Details. Field errors (a missing webhook address) mark
// the field itself: aria-invalid plus aria-describedby on a line under it.
//
// No DOM at import: every function takes the elements it works on, so the
// view modules can import this before main.js has built the containers.

import { t } from '../../i18n/index.js';
import { friendlyErrorText, showError } from '../../utils.js';

const MSG_CLASS = 'sps-msg';
const FORM_SELECTOR = 'form, #wlanSwitchForm';

// errorText is the detail a line shows: the friendly text of the error
// dialog, without the "Error: " prefix Go errors carry through Wails.
export function errorText(e) {
  const raw = String((e && e.message) || e || '').replace(/^Error:\s*/, '');
  return friendlyErrorText(raw || t('modal.unknownError'));
}

// openSection resolves anchor (an element or an id) to the card of the
// speaker page it sits in, and only while that card is on screen: inside
// #ui2SpkPanels, in the open tab's wrapper. null otherwise, which sends the
// caller back to the window-wide dialog (a card nobody can see would swallow
// the error).
export function openSection(anchor, doc = (typeof document !== 'undefined' ? document : null)) {
  if (!doc || !anchor) return null;
  const el = typeof anchor === 'string' ? doc.getElementById(anchor) : anchor;
  if (!el || typeof el.closest !== 'function') return null;
  const section = el.closest('.settings-section');
  const panels = el.closest('#ui2SpkPanels');
  if (!section || !panels) return null;
  const wrap = section.closest('.sps-sec[data-ui2-tab]');
  if (wrap && panels.dataset.tab && wrap.dataset.ui2Tab !== panels.dataset.tab) return null;
  const panel = section.closest('.sps-panel');
  if (panel && panel.hidden) return null;
  if (section.classList.contains('hidden')) return null;
  return { section, anchor: el };
}

// placeAfter is the node the line goes after: the control's .setting-row, or
// the pressed control itself in a card without rows, lifted out of any form;
// null appends to the card.
export function placeAfter(section, anchor) {
  if (!section || !anchor || anchor === section || !section.contains(anchor)) return null;
  let node = anchor.closest('.setting-row');
  if (!node || !section.contains(node) || node === section) node = anchor;
  // The Wi-Fi switch form is a <div>, and it closes itself once a switch went
  // out: a line inside it would leave with it.
  const form = node.closest(FORM_SELECTOR);
  if (form && section.contains(form) && form !== section) node = form;
  return node;
}

function insertLine(section, anchor, line) {
  clearSectionMsg(section);
  const after = placeAfter(section, anchor);
  if (after && after.parentNode) after.parentNode.insertBefore(line, after.nextSibling);
  else section.appendChild(line);
  try { line.scrollIntoView({ block: 'nearest' }); } catch (_) { /* older webview */ }
  return line;
}

function button(doc, cls, text) {
  const b = doc.createElement('button');
  b.type = 'button';
  b.className = `u-btn u-btn--secondary ${cls}`;
  b.textContent = text;
  return b;
}

// showSectionError puts the refusal line into the card. e is kept as it came,
// so Details opens the same dialog text the classic layout showed.
export function showSectionError(section, anchor, e, { name = '' } = {}) {
  if (!section) return null;
  const doc = section.ownerDocument;
  const line = doc.createElement('div');
  line.className = `${MSG_CLASS} ${MSG_CLASS}--error`;
  line.setAttribute('role', 'alert');
  const text = doc.createElement('span');
  text.className = 'sps-msg-text';
  text.textContent = t('ui2.speakers.speaker.settingFailed', { name, detail: errorText(e) });
  const acts = doc.createElement('span');
  acts.className = 'sps-msg-acts';
  const details = button(doc, 'sps-msg-details', t('ui2.speakers.speaker.details'));
  details.addEventListener('click', () => showError(e));
  const close = button(doc, 'sps-msg-close', t('common.close'));
  close.addEventListener('click', () => {
    line.remove();
    if (anchor && typeof anchor.focus === 'function' && section.contains(anchor)) anchor.focus();
  });
  acts.append(details, close);
  line.append(text, acts);
  return insertLine(section, anchor, line);
}

// showSectionWarn: a partial outcome (a copy that reached some keys only).
export function showSectionWarn(section, anchor, message) {
  if (!section) return null;
  const doc = section.ownerDocument;
  const line = doc.createElement('div');
  line.className = `${MSG_CLASS} ${MSG_CLASS}--warn`;
  line.setAttribute('role', 'status');
  const text = doc.createElement('span');
  text.className = 'sps-msg-text';
  text.textContent = String(message || '');
  const close = button(doc, 'sps-msg-close', t('common.close'));
  close.addEventListener('click', () => line.remove());
  const acts = doc.createElement('span');
  acts.className = 'sps-msg-acts';
  acts.append(close);
  line.append(text, acts);
  return insertLine(section, anchor, line);
}

// clearSectionMsg takes a card's line away (the next success in that card).
export function clearSectionMsg(section) {
  if (!section || typeof section.querySelectorAll !== 'function') return;
  section.querySelectorAll(`.${MSG_CLASS}`).forEach((el) => el.remove());
}

// showFieldError marks one field as wrong: aria-invalid, and a line under it
// that the field names through aria-describedby. Focus goes to the field.
export function showFieldError(field, message) {
  if (!field) return null;
  const doc = field.ownerDocument;
  clearFieldError(field);
  const id = `${field.id || 'field'}-err`;
  const line = doc.createElement('div');
  line.className = `${MSG_CLASS} ${MSG_CLASS}--error sps-field-err`;
  line.id = id;
  line.setAttribute('role', 'alert');
  line.textContent = String(message || '');
  field.setAttribute('aria-invalid', 'true');
  const before = field.getAttribute('aria-describedby') || '';
  field.dataset.spsDescribedBy = before;
  field.setAttribute('aria-describedby', [before, id].filter(Boolean).join(' '));
  const after = field.closest('.setting-row') || field;
  after.parentNode.insertBefore(line, after.nextSibling);
  // The mark goes as soon as the field is changed.
  const clear = () => { clearFieldError(field); field.removeEventListener('input', clear); };
  field.addEventListener('input', clear);
  try { field.focus(); } catch (_) { /* detached */ }
  return line;
}

export function clearFieldError(field) {
  if (!field || field.getAttribute('aria-invalid') !== 'true') return;
  const id = `${field.id || 'field'}-err`;
  const doc = field.ownerDocument;
  const line = doc.getElementById(id);
  if (line) line.remove();
  field.removeAttribute('aria-invalid');
  const before = field.dataset.spsDescribedBy || '';
  if (before) field.setAttribute('aria-describedby', before);
  else field.removeAttribute('aria-describedby');
  delete field.dataset.spsDescribedBy;
}

// paintReadState shows or clears a card's "could not read" status: the
// toggle buttons are aria-disabled (the press is refused in shared.js), and a
// .u-sec-status line names the speaker with Try again, which reads again.
export function paintReadState(host, failed, { name = '', buttons = [], retry = null } = {}) {
  if (!host) return;
  const doc = host.ownerDocument;
  const inside = host.classList.contains('settings-section');
  const find = () => (inside ? host.querySelector(':scope > .u-sec-status')
    : (host.nextElementSibling && host.nextElementSibling.classList.contains('u-sec-status') ? host.nextElementSibling : null));
  buttons.forEach((b) => {
    if (!b) return;
    if (failed) b.setAttribute('aria-disabled', 'true');
    else b.removeAttribute('aria-disabled');
  });
  const old = find();
  if (!failed) { if (old) old.remove(); return; }
  if (old) old.remove();
  const line = doc.createElement('div');
  line.className = 'u-sec-status';
  line.setAttribute('role', 'status');
  const text = doc.createElement('span');
  text.className = 'u-sec-status-text';
  text.textContent = t('ui2.speakers.speaker.readFailed', { name });
  const again = button(doc, 'u-sec-retry', t('ui2.speakers.speaker.tryAgain'));
  again.addEventListener('click', () => { if (typeof retry === 'function') retry(); });
  line.append(text, again);
  if (inside) host.appendChild(line);
  else host.parentNode.insertBefore(line, host.nextSibling);
}
