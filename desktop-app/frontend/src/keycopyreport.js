// keycopyreport.js: what copying CHOSEN keys to other speakers did, per
// speaker (#1551).
//
// The Go side (CopyPresetKeysAcrossBoxes) answers per target with the keys it
// copied, the keys it left out because the station already sits on another key
// there (the duplicate guard, #836), and the keys pressed twice a speaker with
// one row cannot hold. This module turns those answers into one sentence per
// speaker and an overall kind for the toast or the UI 2.0 status line.
// Pure (no DOM, no api.js), so it is vitest-covered.

import { keyLabel, keyNum } from './presetrows.js';

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
  const lines = [];
  if (copied.length) lines.push(t('presets.keyCopyCopied', { target, keys: keyList(copied, t) }));
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
// with a space) and kind: 'ok' when every speaker took every chosen key,
// 'error' when no speaker took any, 'warn' in between.
export function summarizeKeyCopy(outcomes, t) {
  const list = outcomes || [];
  let anyCopied = false;
  let allClean = list.length > 0;
  const lines = [];
  for (const o of list) {
    lines.push(...keyCopyOutcomeLines(o, t));
    const r = o.result || {};
    const copied = (r.copied || []).length;
    if (copied) anyCopied = true;
    if (o.error != null || !copied || (r.skipped || []).length || (r.notKept || []).length) allClean = false;
  }
  const kind = allClean ? 'ok' : (anyCopied ? 'warn' : 'error');
  return { text: lines.join(' '), lines, kind };
}

// chosenSlots reads the ticked keys of the transfer dialog's checkboxes
// (value = slot), in slot order.
export function chosenSlots(checkboxes) {
  return [...(checkboxes || [])]
    .filter((c) => c && c.checked)
    .map((c) => parseInt(c.value, 10))
    .filter((n) => Number.isInteger(n) && n >= 1 && n <= 12)
    .sort((a, b) => a - b);
}
