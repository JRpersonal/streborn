// keycopyreview.js: the review a copy of keys to other speakers shows in the
// UI 2.0 preview BEFORE anything is written (docs/UI-2.0.md, rule 2: honest
// feedback). Per speaker and per key it says what the copy will do: copied,
// replacing another station, skipped because that speaker already has it
// exactly like this, skipped because the station is on another key there, or
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
    'ui2.keyReview.unchecked', 'ui2.keyReview.nothing', 'ui2.keyReview.copyBtn', 'ui2.keyReview.cancelled'];
}

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
// outcomes: [{ target, steps }] or [{ target, error }].
//   blocks   one per speaker: { target, lines, error }
//   writes   how many keys would be written over all speakers
//   canCopy  false when nothing would change anywhere; a speaker that could not
//            be checked counts as a reason to try, the copy then says what
//            happened there.
export function reviewModel(outcomes, t) {
  let writes = 0;
  let unchecked = 0;
  const blocks = (outcomes || []).map((o) => {
    if (o.error != null) {
      unchecked++;
      return { target: o.target, lines: [], error: t('ui2.keyReview.unchecked', { reason: String(o.error) }) };
    }
    const lines = (o.steps || []).slice().sort((a, b) => a.slot - b.slot).map((s) => reviewStepLine(s, t));
    writes += lines.filter((l) => l.kind !== 'skip').length;
    return { target: o.target, lines, error: '' };
  });
  return { blocks, writes, unchecked, canCopy: writes > 0 || unchecked > 0 };
}

// gatherKeyCopyPreviews asks PreviewPresetKeyCopy for every target, one after
// the other (a read each, nothing written). It answers { unavailable: true }
// when this build has no preview binding, so the caller copies as before.
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
      outcomes.push({ target, error: explain(String((e && e.message) || e)) });
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

// closeKeyCopyReview removes the review, for a new one or a view change.
export function closeKeyCopyReview() {
  const old = document.getElementById(PANEL_ID);
  if (old) old.remove();
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
  status.textContent = model.canCopy ? '' : t('ui2.keyReview.nothing');
  copy.disabled = !model.canCopy;
  copy.hidden = !model.canCopy;
  cancel.textContent = model.canCopy ? t('common.cancel') : t('common.close');
}

// reviewKeyCopy shows the review and waits for the user: 'copy', 'cancel', or
// 'unavailable' when this build cannot preview (the caller then copies as it
// always did). The keyboard lands on the review's heading once the answers
// are in; Escape is Cancel. The caller puts the keyboard back on anchor once
// anchor is enabled again (a disabled button cannot take focus).
export async function reviewKeyCopy({ box, sourceName, targets, slots, anchor, t, preview, isMissing, explain, checkingTarget }) {
  const parts = mountPanel(anchor, t, t('ui2.keyReview.checking', { target: checkingTarget || '' }), sourceName);
  let settle;
  const answer = new Promise((res) => { settle = res; });
  const finish = (v) => {
    parts.panel.remove();
    settle(v);
  };
  parts.cancel.addEventListener('click', () => finish('cancel'));
  parts.copy.addEventListener('click', () => { if (!parts.copy.disabled) finish('copy'); });
  parts.panel.addEventListener('keydown', (ev) => {
    if (ev.key === 'Escape') { ev.preventDefault(); finish('cancel'); }
  });
  gatherKeyCopyPreviews({ box, targets, slots, preview, isMissing, explain }).then((got) => {
    if (!parts.panel.isConnected) return;
    if (got.unavailable) { parts.panel.remove(); settle('unavailable'); return; }
    renderModel(parts, reviewModel(got.outcomes, t), t);
    parts.title.focus();
  });
  return answer;
}
