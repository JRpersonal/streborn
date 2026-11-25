// views/music/ui2keyedit.js: hold a key to save onto it, and a key's choices,
// in the UI 2.0 preview (docs/UI-2.0.md).
//
// A key of the Radio view does one thing on a short press: it plays. Holding
// it for HOLD_TO_SAVE_MS saves what is playing straight onto it, exactly as
// the classic layout does (25 November 2026, discussion #1077: from 15 to 25
// November the hold opened the choices instead, and testers expected the
// save). holdAction decides: a hold saves while something plays, on any key
// but the speaker's own (a Deezer mix); otherwise it opens the key's choices,
// so a hold always does something visible.
//
// The choices sit in one panel under the grid: save what is playing, find a
// station, pick a favourite, rename, clear. A right click (also macOS
// Ctrl+click, the browser's context menu) opens them and never saves, and so
// do Shift+Enter, the context-menu key and Shift+F10 from the keyboard (the
// grid's hint says so); Escape closes them and puts the keyboard back on the
// key. An empty key has nothing to play and opens them on a short press.
// Every choice is a flow the app already has, so there is no second way to
// write a key: the quick ones stay here (saveCurrentToSlot, the rename and
// clear icons), and "Find a station" and "Choose a favorite" open the one key
// editor, Speakers > the speaker > Keys and alarms
// (shell2/speakers/keyeditor.js), on that key and on its station search or
// its favourites. Without the shell nothing here runs: the current layout
// keeps its own hold-to-save.
//
// The pure parts (the gesture, what a hold does, the choices per key, the
// words) take no DOM, so vitest reads them without a document.

import { t } from '../../i18n/index.js';
import { keyNum } from '../../presetrows.js';
import { state } from '../../state.js';
import { speakerUnitName } from '../../unitname.js';
import { $, escapeAttr, escapeHtml } from '../../utils.js';
import { saveCurrentToSlot } from './save.js';
import { keyReport, restoreKeyFocus } from './ui2radio.js';

// HOLD_TO_SAVE_MS is how long a press must last to count as a hold instead of
// a play: the classic layout's 1.1 s (presettiles.js LONG_PRESS_MS; the
// checks pin the two equal). The hold writes to the speaker, so it keeps the
// classic guard against a slow tap.
export const HOLD_TO_SAVE_MS = 1100;

// The kinds of key the grid draws (presettiles.js renderPresets): one of STR's
// keys with a station, an empty key, a key the speaker keeps for itself (a
// Deezer mix) and a key STR wrote that its store lost after a reinstall.
export const KEY_KINDS = Object.freeze(['station', 'empty', 'native', 'lost']);

// isKeyEditGesture tells whether a keydown on a key asks for its choices:
// Shift+Enter, the context-menu key or Shift+F10. Plain Enter and Space keep
// playing it.
export function isKeyEditGesture(e) {
  if (!e || e.ctrlKey || e.altKey || e.metaKey) return false;
  if (e.key === 'ContextMenu') return true;
  return !!e.shiftKey && (e.key === 'Enter' || e.key === 'F10');
}

// holdAction is what a hold on a key of kind does, playing telling whether
// something plays: 'save' it onto the key, or 'menu', open the key's choices.
// It saves only while something plays, and never over the speaker's own key:
// the current layout refuses a hold-save there so a Deezer key is not
// overwritten by accident, and the preview keeps that.
export function holdAction(kind, playing) {
  return playing && kind !== 'native' ? 'save' : 'menu';
}

// keyEditActions lists the choices of a key, in the order the panel shows
// them; the first is the primary one. "save" exactly where a hold would save
// (holdAction). Rename and clear only on STR's own keys, which are the ones
// that have those icons.
export function keyEditActions(kind, playing) {
  const out = [];
  if (holdAction(kind, playing) === 'save') out.push('save');
  out.push('find', 'favorites');
  if (kind === 'station') out.push('rename', 'clear');
  return out;
}

const ACTION_KEYS = Object.freeze({
  save: 'ui2.radio.keyEditSave',
  find: 'ui2.radio.keyEditFind',
  favorites: 'ui2.radio.keyEditFavorites',
  rename: 'ui2.radio.keyEditRename',
  clear: 'ui2.radio.keyEditClear',
});

// keyEditWords is everything the panel says, for key slot of speaker holding
// name (empty for an empty key).
export function keyEditWords({ kind, slot, speaker, name, actions }) {
  const n = keyNum(slot, t);
  return {
    title: t(kind === 'empty' ? 'ui2.radio.keyAssignTitle' : 'ui2.radio.keyEditTitle', { n, speaker }),
    now: kind === 'empty' || !name ? t('ui2.radio.keyEditEmpty') : t('ui2.radio.keyEditNow', { name }),
    note: actions.includes('find') ? t('ui2.radio.keyEditNoteEditor', { n, speaker }) : '',
    close: t('ui2.popover.close'),
    actions: actions.map((id) => ({ id, label: t(ACTION_KEYS[id]) })),
  };
}

// keyEditHTML draws the panel's inside from keyEditWords.
export function keyEditHTML(words) {
  const acts = words.actions.map((a, i) => `<button type="button" class="u-btn ${i === 0 ? 'u-btn--primary' : 'u-btn--secondary'} ui2-key-edit-act" data-ke="${escapeAttr(a.id)}">${escapeHtml(a.label)}</button>`).join('');
  return '<div class="ui2-key-edit-head">'
    + `<h3 class="ui2-key-edit-title" id="ui2KeyEditTitle">${escapeHtml(words.title)}</h3>`
    + `<button type="button" class="u-btn u-btn--icon ui2-key-edit-close" data-ke="close" aria-label="${escapeAttr(words.close)}" title="${escapeAttr(words.close)}"><span aria-hidden="true">&times;</span></button>`
    + '</div>'
    + `<p class="ui2-key-edit-now" id="ui2KeyEditNow">${escapeHtml(words.now)}</p>`
    + `<div class="ui2-key-edit-acts" role="group" aria-labelledby="ui2KeyEditTitle">${acts}</div>`
    + (words.note ? `<p class="ui2-key-edit-note">${escapeHtml(words.note)}</p>` : '');
}

// ---------- The panel ----------

// edit is the key the panel is open for ({ host, slot, sig }), or null.
let edit = null;

const on = () => !!state.ui2Shell;
const playingNow = () => !!state.nowLocation;

function tileOf(slot) {
  return [...document.querySelectorAll('#presets .preset')].find((el) => parseInt(el.dataset.slot, 10) === slot) || null;
}

// keyFromTile reads what the grid shows for a key: its kind (set by
// ui2KeyHoldOpts when the tile was drawn) and its station name.
function keyFromTile(tile) {
  const kind = (tile && tile.dataset.ui2Kind) || (tile && tile.classList.contains('empty') ? 'empty' : 'station');
  const nameEl = tile && kind !== 'empty' ? tile.querySelector('.preset-body .name') : null;
  return { kind, name: nameEl ? nameEl.textContent.trim() : '' };
}

function panel() {
  let el = $('ui2KeyEdit');
  if (el) return el;
  const grid = $('presets');
  if (!grid) return null;
  el = document.createElement('section');
  el.id = 'ui2KeyEdit';
  el.className = 'ui2-key-edit';
  el.hidden = true;
  el.setAttribute('aria-labelledby', 'ui2KeyEditTitle');
  el.setAttribute('aria-describedby', 'ui2KeyEditNow');
  grid.insertAdjacentElement('afterend', el);
  el.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-ke]');
    if (btn && el.contains(btn)) runAction(btn.dataset.ke);
  });
  el.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    e.preventDefault();
    e.stopPropagation();
    closeKeyEditor(true);
  });
  return el;
}

// draw fills the panel for the key and returns its signature (kind, name,
// choices, speaker), so a repaint of the grid only redraws it when one
// changed. The speaker is named as the heading over the keys names it: a key
// of a stereo pair with the pair (unitname.js).
function draw(el, slot, box) {
  const { kind, name } = keyFromTile(tileOf(slot));
  const actions = keyEditActions(kind, playingNow());
  const speaker = speakerUnitName(box);
  const sig = `${kind}|${name}|${actions.join(',')}|${speaker}`;
  el.innerHTML = keyEditHTML(keyEditWords({ kind, slot, speaker, name, actions }));
  el.dataset.slot = String(slot);
  return sig;
}

function focusFirst(el) {
  const first = el.querySelector('.ui2-key-edit-act') || el.querySelector('[data-ke="close"]');
  if (first) first.focus();
}

// choiceToKeep is where the keyboard goes after a redraw the user did not ask
// for (the status poll repainted the grid): the choice it was on (was) when
// the panel still has it, else the first one that writes nothing, else the
// close button. Never "save" by itself: Enter pressed right after the repaint
// would put the playing station on the key.
export function choiceToKeep(was, actions) {
  if (was && (was === 'close' || actions.includes(was))) return was;
  return actions.find((id) => id !== 'save') || 'close';
}

function refocusChoice(el, was) {
  const actions = [...el.querySelectorAll('.ui2-key-edit-act')].map((b) => b.dataset.ke);
  const target = el.querySelector(`[data-ke="${choiceToKeep(was, actions)}"]`);
  if (target) target.focus();
}

// openKeyEditor opens the choices of key slot on the speaker in control and
// puts the keyboard on the first one. Opening it for the key it is already
// open for only moves the keyboard back into it.
export function openKeyEditor(slot) {
  if (!on() || !(slot > 0)) return;
  const box = state.currentBox;
  const el = box ? panel() : null;
  if (!el) return;
  if (!edit || edit.host !== box.host || edit.slot !== slot || el.hidden) {
    edit = { host: box.host, slot, sig: draw(el, slot, box) };
  }
  el.hidden = false;
  syncTriggers();
  focusFirst(el);
}

// closeKeyEditor hides the panel; back puts the keyboard on the key it was
// open for.
export function closeKeyEditor(back = false) {
  const el = $('ui2KeyEdit');
  const was = edit;
  edit = null;
  if (el) el.hidden = true;
  syncTriggers();
  if (back && was) restoreKeyFocus({ slot: String(was.slot), part: 'body' });
}

// keyEditorSlot is the key the panel is open for, or 0.
export function keyEditorSlot() {
  return edit ? edit.slot : 0;
}

// syncKeyEditor follows the grid after renderPresets rebuilt it: another
// speaker chosen closes the panel, a key whose station or choices changed
// (cleared from the speaker's own button, something started playing) is
// redrawn, with the keyboard kept on the choice it was on.
export function syncKeyEditor() {
  const box = state.currentBox;
  if (!edit) { syncTriggers(); return; }
  const el = $('ui2KeyEdit');
  if (!el || el.hidden || !box || box.host !== edit.host || !tileOf(edit.slot)) { closeKeyEditor(false); return; }
  const { kind, name } = keyFromTile(tileOf(edit.slot));
  const sig = `${kind}|${name}|${keyEditActions(kind, playingNow()).join(',')}|${speakerUnitName(box)}`;
  if (sig !== edit.sig) {
    const active = document.activeElement;
    const inside = el.contains(active);
    const was = inside && active.dataset ? active.dataset.ke : '';
    edit.sig = draw(el, edit.slot, box);
    if (inside) refocusChoice(el, was);
  }
  syncTriggers();
}

// syncTriggers tells an empty key, whose press opens the panel, whether it
// is open for it.
function syncTriggers() {
  document.querySelectorAll('#presets .preset.empty[role="button"]').forEach((tile) => {
    const open = !!edit && parseInt(tile.dataset.slot, 10) === edit.slot;
    tile.setAttribute('aria-expanded', open ? 'true' : 'false');
  });
}

// runAction does what a choice says, through the flow the key already has.
function runAction(id) {
  if (!edit) return;
  const { slot, host } = edit;
  const box = state.currentBox;
  if (id === 'close') { closeKeyEditor(true); return; }
  if (!box || box.host !== host) { closeKeyEditor(false); return; }
  if (id === 'save') {
    closeKeyEditor(true);
    saveCurrentToSlot(slot, keyReport(slot, box));
    return;
  }
  if (id === 'rename' || id === 'clear') {
    // The tile's own icon, clicked with the keyboard on the key, so its
    // confirmation and its report behave exactly as from the icon, and the
    // keyboard comes back to the key afterwards.
    closeKeyEditor(true);
    const icon = tileOf(slot) && tileOf(slot).querySelector(id === 'rename' ? '.ren' : '.del');
    if (icon) icon.click();
    return;
  }
  if (id === 'find' || id === 'favorites') {
    // The speaker's key editor, on this key, with the station search or the
    // favourites in front: one place that writes a key from a choice. The
    // page is the speaker you control, which is the one these keys belong to.
    closeKeyEditor(false);
    openKeyTab(host, slot, id);
  }
}

// openKeyTab opens Speakers > the speaker > Keys and alarms with key slot
// chosen in its editor, mode 'find' or 'favorites'. The router is imported on
// use, as views/settings/shared.js does: a static import would pull the whole
// Speakers area (and the settings view) into every music view. When the page
// cannot open, the keyboard goes back to the key.
async function openKeyTab(host, slot, mode) {
  try {
    const { openSpeakers } = await import('../../shell2/speakers/index.js');
    if (openSpeakers('speaker', { host, tab: 'keys', key: { slot, mode } })) return;
  } catch { /* fall through: back to the key */ }
  restoreKeyFocus({ slot: String(slot), part: 'body' });
}

// ui2KeyHoldOpts is what renderPresets hands attachPresetHandlers for one
// tile in the preview: the hold (holdAction, read when it fires: save what
// plays through the same path as the choice "Save what is playing", reported
// in the key's own line, or open the choices when there is nothing to save),
// the context menu, which opens the choices and never saves, and for an empty
// key a short press that opens them too. An empty object without the shell,
// so the current layout keeps its hold-to-save.
export function ui2KeyHoldOpts(tile, slot, kind) {
  if (!on()) return {};
  tile.dataset.ui2Kind = kind;
  const open = () => openKeyEditor(slot);
  const hold = () => (holdAction(kind, playingNow()) === 'save'
    ? saveCurrentToSlot(slot, keyReport(slot))
    : open());
  return { holdMs: HOLD_TO_SAVE_MS, onHold: hold, onMenu: open, onEmptyPress: kind === 'empty' ? open : null };
}

// reducedMotion tells whether the system asks for less motion: the hold then
// marks the key at once instead of filling its bar.
export function reducedMotion() {
  try {
    return typeof window !== 'undefined' && typeof window.matchMedia === 'function'
      && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  } catch {
    return false;
  }
}
