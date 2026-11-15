// views/music/ui2keyedit.js: hold a key to change it, in the UI 2.0 preview
// (docs/UI-2.0.md).
//
// A key of the Radio view does one thing on a short press: it plays. Holding
// it used to save what is playing straight onto it, which is easy to do by
// accident and the only way to change a key from the grid. In the preview a
// hold of HOLD_TO_EDIT_MS opens the key's choices instead, in one panel under
// the grid: save what is playing, find a station, pick a favourite, rename,
// clear. Every choice is the flow the key already had (saveCurrentToSlot,
// the rename and clear icons, the station rows' "Put on key" picker), so
// there is no second way to write a key. An empty key has nothing to play and
// opens the same panel on a short press.
//
// From the keyboard Shift+Enter, the context-menu key or Shift+F10 open it
// (the grid's hint says so); Escape closes it and puts the keyboard back on
// the key. Without the shell nothing here runs: the current layout keeps its
// 1.1 s hold-to-save.
//
// The pure parts (the gesture, the choices per key, the words) take no DOM, so
// vitest reads them without a document.

import { t } from '../../i18n/index.js';
import { keyNum } from '../../presetrows.js';
import { state } from '../../state.js';
import { $, escapeAttr, escapeHtml, getBoxLabel } from '../../utils.js';
import { saveCurrentToSlot } from './save.js';
import { keyReport, keyStatusOf, openUi2RadioTab, restoreKeyFocus } from './ui2radio.js';

// HOLD_TO_EDIT_MS is how long a press must last to open the choices instead
// of playing. Shorter than the current layout's 1.1 s save: opening a panel
// writes nothing, so it does not need the guard against a slow tap that a
// save does, and a long wait reads as "nothing happens".
export const HOLD_TO_EDIT_MS = 700;

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

// keyEditActions lists the choices of a key, in the order the panel shows
// them; the first is the primary one. "save" only while something plays, and
// never on the speaker's own key: the current layout refuses a hold-save
// there so a Deezer key is not overwritten by accident, and the preview keeps
// that. Rename and clear only on STR's own keys, which are the ones that have
// those icons.
export function keyEditActions(kind, playing) {
  const out = [];
  if (playing && kind !== 'native') out.push('save');
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
    note: actions.includes('find') ? t('ui2.radio.keyEditNote', { n }) : '',
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

// ---------- The assignment a "Find a station" or a favourite leads to ----------

// assign is the key the user chose "Find a station" or "From favorites" for:
// the next "Put on key" picker on that speaker starts on it.
let assign = null;

// takeKeyAssignTarget hands the picker the key to start on, once: the target
// is for the speaker the picker writes to, and it is used up by that picker.
export function takeKeyAssignTarget(box) {
  const a = assign;
  if (!a || !box || a.host !== box.host) return null;
  dropAssign();
  return a.slot;
}

// dropAssign forgets the target and takes its line off the key, unless the
// key has said something else since.
function dropAssign() {
  const a = assign;
  assign = null;
  if (!a) return;
  // The key line is kept per speaker address; the host is all it needs.
  const box = { host: a.host };
  const st = keyStatusOf(a.slot, box);
  if (!st || st.kind !== 'hint') return;
  const report = keyReport(a.slot, box);
  if (report) report(null);
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
// choices), so a repaint of the grid only redraws it when one changed.
function draw(el, slot, box) {
  const { kind, name } = keyFromTile(tileOf(slot));
  const actions = keyEditActions(kind, playingNow());
  const sig = `${kind}|${name}|${actions.join(',')}`;
  el.innerHTML = keyEditHTML(keyEditWords({ kind, slot, speaker: getBoxLabel(box), name, actions }));
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
    // A key chosen to be changed again replaces a station search started
    // from it or from another key.
    dropAssign();
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
// speaker chosen closes the panel (and forgets a search started for one of
// its keys), a key whose station or choices changed (cleared from the
// speaker's own button, something started playing) is redrawn, with the
// keyboard kept on the choice it was on.
export function syncKeyEditor() {
  const box = state.currentBox;
  // A station search started for a key of another speaker has nothing to put
  // on this one; its line stays with that speaker and is taken off.
  if (assign && (!box || box.host !== assign.host)) dropAssign();
  if (!edit) { syncTriggers(); return; }
  const el = $('ui2KeyEdit');
  if (!el || el.hidden || !box || box.host !== edit.host || !tileOf(edit.slot)) { closeKeyEditor(false); return; }
  const { kind, name } = keyFromTile(tileOf(edit.slot));
  const sig = `${kind}|${name}|${keyEditActions(kind, playingNow()).join(',')}`;
  if (sig !== edit.sig) {
    const active = document.activeElement;
    const inside = el.contains(active);
    const was = inside && active.dataset ? active.dataset.ke : '';
    edit.sig = draw(el, edit.slot, box);
    if (inside) refocusChoice(el, was);
  }
  syncTriggers();
}

// leaveKeyEditView is called when the user leaves the Radio view: a station
// search started for a key ends there, so a later "Put on key" (also the one
// in Library or Recent) no longer starts on that key.
export function leaveKeyEditView() {
  dropAssign();
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
    closeKeyEditor(false);
    assign = { host, slot };
    const report = keyReport(slot, box);
    if (report) report(t('ui2.radio.keyAssignPending', { n: keyNum(slot, t) }), 'hint');
    openUi2RadioTab(id === 'find' ? 'discover' : 'favorites');
    if (id === 'find') {
      const q = $('searchQ');
      if (q && q.offsetParent !== null) q.focus();
    }
  }
}

// ui2KeyHoldOpts is what renderPresets hands attachPresetHandlers for one
// tile in the preview: the shorter hold that opens the choices instead of
// saving, and for an empty key a short press that opens them too. An empty
// object without the shell, so the current layout keeps its hold-to-save.
export function ui2KeyHoldOpts(tile, slot, kind) {
  if (!on()) return {};
  tile.dataset.ui2Kind = kind;
  const open = () => openKeyEditor(slot);
  return { holdMs: HOLD_TO_EDIT_MS, onHold: open, onEmptyPress: kind === 'empty' ? open : null };
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
