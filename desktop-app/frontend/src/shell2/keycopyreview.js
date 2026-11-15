// keycopyreview.js: the review a copy of keys to other speakers shows in the
// UI 2.0 preview BEFORE anything is written (docs/UI-2.0.md, rule 2: honest
// feedback). Per speaker and per key it says what the copy will do: copied,
// replacing another station, already there exactly like this (only the
// speaker's own button is set again), skipped because the station is on
// another key there, or
// not possible because that speaker keeps one row of keys. Copy or Cancel
// follows; nothing is copied before Copy.
//
// The answers come from the Go side's PreviewPresetKeyCopy, which runs the
// same plan CopyPresetKeysAcrossBoxes acts on, so the review cannot promise
// something the copy then does differently.
//
// The review sits inline under the keys' heading row, where the Transfer
// button is, not in a window-wide dialog. Everything the module needs from
// the app (the binding, t, the error wording) comes in as arguments, so the
// model is vitest-covered without a DOM or the Wails bindings.

import { keyLabel, keyNum } from '../presetrows.js';

// The actions PreviewPresetKeyCopy answers with (app_presets_keycopy.go).
export const REVIEW_ACTIONS = ['copy', 'replace', 'same', 'onOtherKey', 'notKept'];

// The i18n key of each action's sentence.
const LINE_KEY = {
  copy: 'ui2.keyReview.copy',
  replace: 'ui2.keyReview.replace',
  same: 'ui2.keyReview.same',
  onOtherKey: 'ui2.keyReview.onOtherKey',
  notKept: 'ui2.keyReview.notKept',
};

// reviewKeys is every i18n key the review shows, for the bundle check.
export function reviewKeys() {
  return [...Object.values(LINE_KEY), 'ui2.keyReview.title', 'ui2.keyReview.intro', 'ui2.keyReview.checking',
    'ui2.keyReview.unchecked', 'ui2.keyReview.sourceUnchecked', 'ui2.keyReview.nothing', 'ui2.keyReview.copyBtn',
    'ui2.keyReview.cancelled'];
}

// SOURCE_ERROR_PREFIX starts the Go side's error when the SOURCE speaker could
// not be read (keyCopySourceErrPrefix in app_presets_keycopy.go). Every
// target's preview fails the same way then, and a copy could only fail the
// same way too, so the review says it once about the source instead of
// blaming each target.
export const SOURCE_ERROR_PREFIX = 'read source presets';

// reviewStepLine is the sentence for one key on one speaker, and how it is
// marked: 'change' for a key that is written, 'replace' when that overwrites
// another station (the one to look at twice), 'skip' for a key left alone.
// The words carry the outcome; the mark only repeats it.
export function reviewStepLine(step, t) {
  const action = REVIEW_ACTIONS.includes(step && step.action) ? step.action : 'copy';
  const vars = {
    key: keyLabel(step.slot, t),
    name: step.name || '',
    old: step.replaces || '',
    n: step.onSlot ? keyNum(step.onSlot, t) : '',
  };
  let kind = 'skip';
  if (action === 'copy') kind = 'change';
  else if (action === 'replace') kind = 'replace';
  return { slot: step.slot, action, kind, text: t(LINE_KEY[action], vars) };
}

// reviewModel turns the per-speaker answers into what the review shows.
// outcomes: [{ target, steps }] or [{ target, error }]; source: { name, error }
// when the source speaker itself could not be read.
//   blocks       one per speaker: { target, lines, error }
//   writes       how many keys would change over all speakers
//   refreshes    how many unchanged keys are sent again, so the speaker's own
//                button is set again (the Go side's keyStepSame)
//   sourceError  the one line about an unreadable source, '' otherwise
//   canCopy      false when the source cannot be read, or when no key would
//                change or be sent again anywhere; a speaker that could not
//                be checked counts as a reason to try, the copy then says
//                what happened there.
export function reviewModel(outcomes, t, source = null) {
  if (source && source.error != null) {
    const sourceError = t('ui2.keyReview.sourceUnchecked', { source: source.name || '', reason: String(source.error) });
    return { blocks: [], writes: 0, refreshes: 0, unchecked: 0, sourceError, canCopy: false };
  }
  let writes = 0;
  let refreshes = 0;
  let unchecked = 0;
  const blocks = (outcomes || []).map((o) => {
    if (o.error != null) {
      unchecked++;
      return { target: o.target, lines: [], error: t('ui2.keyReview.unchecked', { reason: String(o.error) }) };
    }
    const lines = (o.steps || []).slice().sort((a, b) => a.slot - b.slot).map((s) => reviewStepLine(s, t));
    writes += lines.filter((l) => l.kind !== 'skip').length;
    refreshes += lines.filter((l) => l.action === 'same').length;
    return { target: o.target, lines, error: '' };
  });
  return { blocks, writes, refreshes, unchecked, sourceError: '', canCopy: writes > 0 || refreshes > 0 || unchecked > 0 };
}

// gatherKeyCopyPreviews asks PreviewPresetKeyCopy for every target, one after
// the other (a read each, nothing written). It answers { unavailable: true }
// when this build has no preview binding, so the caller copies as before, and
// { sourceError } when the source speaker could not be read: the other targets
// are not asked then, they would only fail the same way.
//   box      the source speaker { host, port }
//   targets  [{ host, port, label }]
//   preview  the binding; isMissing tells a missing binding from a failure
//   explain  turns an error into the words the user reads
export async function gatherKeyCopyPreviews({ box, targets, slots, preview, isMissing, explain = String }) {
  const outcomes = [];
  for (const tb of targets || []) {
    const target = tb.label || tb.host;
    try {
      const res = await preview(box.host, box.port || 0, tb.host, tb.port || 0, slots);
      outcomes.push({ target, steps: (res && res.steps) || [] });
    } catch (e) {
      if (isMissing && isMissing(e)) return { unavailable: true, outcomes: [] };
      const raw = String((e && e.message) || e);
      if (raw.startsWith(SOURCE_ERROR_PREFIX)) {
        return { unavailable: false, outcomes: [], sourceError: explain(raw) };
      }
      outcomes.push({ target, error: explain(raw) });
    }
  }
  return { unavailable: false, outcomes };
}

const PANEL_ID = 'ui2XferReview';

function el(tag, attrs = {}, text = '') {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  if (text) node.textContent = text;
  return node;
}

// The open review: its source speaker and how to answer its caller. One at a
// time; a review that goes away for any reason answers, so the caller's copy
// flow never waits on a review nobody can see any more.
let current = null;

// closeKeyCopyReview removes the open review and answers its caller with
// reason: 'closed' when the page moved on under it (another speaker chosen,
// another view, a new review), or the user's own answer from the review.
export function closeKeyCopyReview(reason = 'closed') {
  const cur = current;
  current = null;
  const node = typeof document !== 'undefined' ? document.getElementById(PANEL_ID) : null;
  if (node) node.remove();
  if (cur) cur.settle(reason);
}

// keyCopyReviewSource is the host of the speaker whose keys the open review
// is about, '' when none is open. The page closes a review whose source is no
// longer the chosen speaker.
export function keyCopyReviewSource() {
  return current ? current.source : '';
}

// mountPanel puts the review under the row that holds anchor (the Transfer
// button), full width, with its heading, the "checking" line and the buttons.
function mountPanel(anchor, t, checkingText, sourceName) {
  closeKeyCopyReview();
  const panel = el('section', { id: PANEL_ID, class: 'ui2-xfer-review', 'aria-labelledby': `${PANEL_ID}Title` });
  const title = el('h3', { id: `${PANEL_ID}Title`, class: 'ui2-xfer-review-title', tabindex: '-1' }, t('ui2.keyReview.title'));
  const intro = el('p', { class: 'ui2-xfer-review-intro' }, t('ui2.keyReview.intro', { source: sourceName || '' }));
  const status = el('p', { class: 'ui2-xfer-review-status', role: 'status' }, checkingText);
  const list = el('div', { class: 'ui2-xfer-review-list' });
  const actions = el('div', { class: 'ui2-xfer-review-actions' });
  const copy = el('button', { type: 'button', id: `${PANEL_ID}Copy`, class: 'u-btn u-btn--primary' }, t('ui2.keyReview.copyBtn'));
  copy.disabled = true;
  const cancel = el('button', { type: 'button', id: `${PANEL_ID}Cancel`, class: 'u-btn u-btn--secondary' }, t('common.cancel'));
  actions.append(copy, cancel);
  panel.append(title, intro, status, list, actions);
  const row = anchor && anchor.closest ? (anchor.closest('.ui2-keys-head') || anchor) : null;
  if (row && row.parentNode) row.insertAdjacentElement('afterend', panel);
  else document.body.appendChild(panel);
  return { panel, title, status, list, copy, cancel };
}

// renderModel fills the list: one heading per speaker and one line per key.
function renderModel(parts, model, t) {
  const { list, status, copy, cancel } = parts;
  list.textContent = '';
  if (model.sourceError) {
    list.appendChild(el('p', { class: 'ui2-xfer-review-line is-unchecked ui2-xfer-review-source' }, model.sourceError));
  }
  for (const b of model.blocks) {
    const block = el('div', { class: 'ui2-xfer-review-target' });
    block.appendChild(el('h4', {}, b.target));
    if (b.error) {
      block.appendChild(el('p', { class: 'ui2-xfer-review-line is-unchecked' }, b.error));
    } else {
      const ul = el('ul');
      for (const l of b.lines) {
        ul.appendChild(el('li', { class: `ui2-xfer-review-line is-${l.kind}`, 'data-slot': String(l.slot), 'data-action': l.action }, l.text));
      }
      block.appendChild(ul);
    }
    list.appendChild(block);
  }
  status.textContent = model.canCopy || model.sourceError ? '' : t('ui2.keyReview.nothing');
  copy.disabled = !model.canCopy;
  copy.hidden = !model.canCopy;
  cancel.textContent = model.canCopy ? t('common.cancel') : t('common.close');
}

// reviewKeyCopy shows the review and waits for the user: 'copy', 'cancel',
// 'closed' when the page moved on under it (closeKeyCopyReview), or
// 'unavailable' when this build cannot preview (the caller then copies as it
// always did). The keyboard moves onto the review's heading right away, while
// the speakers are still being checked: the Transfer button that had it is off
// now, and Escape (Cancel) only works from inside the review. The caller puts
// the keyboard back on anchor once anchor is enabled again (a disabled button
// cannot take focus).
export async function reviewKeyCopy({ box, sourceName, targets, slots, anchor, t, preview, isMissing, explain, checkingTarget }) {
  const parts = mountPanel(anchor, t, t('ui2.keyReview.checking', { target: checkingTarget || '' }), sourceName);
  let settle;
  const answer = new Promise((res) => { settle = res; });
  const me = { source: (box && box.host) || '', settle: (v) => settle(v) };
  current = me;
  // The user's own answer: this review is closed and answers once.
  const finish = (v) => { if (current === me) closeKeyCopyReview(v); };
  parts.cancel.addEventListener('click', () => finish('cancel'));
  parts.copy.addEventListener('click', () => { if (!parts.copy.disabled) finish('copy'); });
  parts.panel.addEventListener('keydown', (ev) => {
    if (ev.key === 'Escape') { ev.preventDefault(); finish('cancel'); }
  });
  focusQuietly(parts.title);
  gatherKeyCopyPreviews({ box, targets, slots, preview, isMissing, explain }).then((got) => {
    if (current !== me || !parts.panel.isConnected) return;
    if (got.unavailable) { finish('unavailable'); return; }
    const source = got.sourceError != null ? { name: sourceName, error: got.sourceError } : null;
    renderModel(parts, reviewModel(got.outcomes, t, source), t);
    // The heading still has the keyboard unless the user moved on (onto
    // Cancel, say); only a keyboard that fell to the page comes back.
    const a = document.activeElement;
    if (!a || a === document.body) focusQuietly(parts.title);
  });
  return answer;
}

// focusQuietly moves the keyboard without scrolling the page to the node.
function focusQuietly(node) {
  try { node.focus({ preventScroll: true }); } catch { node.focus(); }
}
