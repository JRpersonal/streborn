// keycopyreport.js: what copying CHOSEN keys to other speakers did, per
// speaker (#1551).
//
// The Go side (CopyPresetKeysAcrossBoxes) answers per target with the keys it
// copied, the keys it left out because the station already sits on another key
// there (the duplicate guard, #836), and the keys pressed twice a speaker with
// one row cannot hold. This module turns those answers into one sentence per
// speaker and an overall kind for the toast or the UI 2.0 status line.
// Pure (no DOM, no api.js), so it is vitest-covered.

import { keyLabel, keyNum, MAX_SLOT } from './presetrows.js';

// keyList is "Key 1, Key 1 · 2×" for a list of slots.
function keyList(slots, t) {
  return (slots || []).map((s) => keyLabel(s, t)).join(', ');
}

// keyCopyOutcomeLines is the sentences for ONE target speaker. outcome is
// { target, result } with result the Go KeyCopyResult, or { target, error }.
export function keyCopyOutcomeLines(outcome, t) {
  const target = outcome.target;
  if (outcome.error != null) {
    return [t('presets.keyCopyFailed', { target, reason: String(outcome.error) })];
  }
  const r = outcome.result || {};
  const copied = r.copied || [];
  const skipped = r.skipped || [];
  const notKept = r.notKept || [];
  const same = r.same || [];
  const lines = [];
  if (copied.length) lines.push(t('presets.keyCopyCopied', { target, keys: keyList(copied, t) }));
  // Keys the speaker already had exactly like this were left alone (not
  // written again); that is a finished key, not a failure.
  if (same.length) lines.push(t('presets.keyCopySame', { target, keys: keyList(same, t) }));
  for (const s of skipped) {
    if (s.onSlot) {
      lines.push(t('presets.keyCopyOnOtherKey', { target, key: keyLabel(s.slot, t), name: s.name, n: keyNum(s.onSlot, t) }));
    } else {
      lines.push(t('presets.keyCopyRefused', { target, key: keyLabel(s.slot, t), reason: s.reason || '' }));
    }
  }
  if (notKept.length) lines.push(t('presets.keyCopyNotKept', { target, keys: keyList(notKept, t) }));
  if (!lines.length) lines.push(t('presets.keyCopyNothing', { target }));
  return lines;
}

// summarizeKeyCopy is the whole report: text (one line per speaker, joined
// with a space) and kind: 'ok' when every speaker took every chosen key (or
// already had it exactly like this), 'error' when no speaker took any, 'warn'
// in between.
export function summarizeKeyCopy(outcomes, t) {
  const list = outcomes || [];
  let anyCopied = false;
  let allClean = list.length > 0;
  const lines = [];
  for (const o of list) {
    lines.push(...keyCopyOutcomeLines(o, t));
    const r = o.result || {};
    const done = (r.copied || []).length + (r.same || []).length;
    if (done) anyCopied = true;
    if (o.error != null || !done || (r.skipped || []).length || (r.notKept || []).length) allClean = false;
  }
  const kind = allClean ? 'ok' : (anyCopied ? 'warn' : 'error');
  return { text: lines.join(' '), lines, kind };
}

// classicKeyCopyResult is a result in the classic layout's words: a key the
// speaker already had counts as copied, as it did before the copy learned to
// leave such a key alone. The classic layout keeps its report unchanged.
export function classicKeyCopyResult(result) {
  const r = result || {};
  const copied = [...(r.copied || []), ...(r.same || [])].sort((a, b) => a - b);
  return { ...r, copied, same: [] };
}

// chosenSlots reads the ticked keys of the transfer dialog's checkboxes
// (value = slot), in slot order.
export function chosenSlots(checkboxes) {
  return [...(checkboxes || [])]
    .filter((c) => c && c.checked)
    .map((c) => parseInt(c.value, 10))
    .filter((n) => Number.isInteger(n) && n >= 1 && n <= MAX_SLOT)
    .sort((a, b) => a - b);
}
