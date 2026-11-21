// presetrows.js: the speaker's rows of six preset keys (#1110, #1657).
//
// /api/agent/version reports how many six-key rows are active: row N is
// reached by pressing one hardware key N times.
// The app shows later rows with a press-count label instead of a bare slot.
//
// Pure (no DOM, no api.js), so the mapping and the gate are unit-tested.

export const ROW_SIZE = 6;
export const MAX_ROWS = 3;
export const MAX_SLOT = ROW_SIZE * MAX_ROWS;

// boxPresetRows is the number of rows the speaker offers. Old agents without
// this field still infer additional rows from existing presets.
// presets is the store the app just read: a store that already holds a row-2
// key proves the agent keeps them, even before discovery has re-read the
// version (right after an update to a supporting agent).
export function boxPresetRows(box, presets) {
  if (!box || box.kind === 'stock') return 1;
  const reported = Number.parseInt(String(box.presetRows || ''), 10);
  if (Number.isInteger(reported) && reported >= 1 && reported <= MAX_ROWS) return reported;
  if (Array.isArray(presets)) {
    const highest = presets.reduce((max, p) => p && p.name && Number.isInteger(p.slot) ? Math.max(max, p.slot) : max, 0);
    if (highest > ROW_SIZE) return Math.min(MAX_ROWS, Math.ceil(highest / ROW_SIZE));
  }
  return 1;
}

// boxMaxSlot is the highest slot the speaker accepts.
export function boxMaxSlot(box, presets) {
  return boxPresetRows(box, presets) * ROW_SIZE;
}

// slotRow maps a preset slot to its press-count row.
export function slotRow(slot) {
  const n = Number(slot);
  return Number.isInteger(n) && n > 0 ? Math.ceil(n / ROW_SIZE) : 1;
}

// slotKey is the hardware key a slot sits on, 1..6 for either row.
export function slotKey(slot) {
  const n = Number(slot);
  return Number.isInteger(n) && n > 0 ? ((n - 1) % ROW_SIZE) + 1 : n;
}

// rowSlot is the slot of hardware key `key` (1..6) in row `row` (1..10).
export function rowSlot(row, key) {
  return (Math.max(1, Number(row) || 1) - 1) * ROW_SIZE + Number(key);
}

// rowSlots lists the six slots of a row.
export function rowSlots(row) {
  const out = [];
  for (let k = 1; k <= ROW_SIZE; k++) out.push(rowSlot(row, k));
  return out;
}

// keyNum is what goes into a sentence that says "key {{n}}": the plain number
// for row 1 and a press-count label for later rows. t is the i18n lookup.
export function keyNum(slot, t) {
  const n = Number(slot);
  if (!(n > ROW_SIZE)) return n;
  const row = slotRow(n);
  if (row === 2) return t('preset.row2.num', { n: slotKey(n) });
  if (row === 3) return t('preset.row3.num', { n: slotKey(n) });
  return t('preset.row3.num', { n: slotKey(n) });
}

// keyLabel is the label of a key on its own: "Key 1" or "Key 1 · 2×".
export function keyLabel(slot, t) {
  const n = Number(slot);
  if (!(n > ROW_SIZE)) return t('preset.key', { n });
  const row = slotRow(n);
  if (row === 2) return t('preset.row2.key', { n: slotKey(n) });
  if (row === 3) return t('preset.row3.key', { n: slotKey(n) });
  return t('preset.row3.key', { n: slotKey(n) });
}

// nowKeyTag is the key the now-playing line names: "Key 2" or "Key 1 · 2×"
// for a lit STR key, '' when no key of the store plays.
export function nowKeyTag(activeSlot, t) {
  const n = Number(activeSlot);
  if (!Number.isInteger(n) || n < 1 || n > MAX_ROWS * ROW_SIZE) return '';
  return keyLabel(n, t);
}

// rowCount is how many keys of a row hold something.
export function rowCount(presets, row) {
  const slots = new Set(rowSlots(row));
  return (presets || []).filter((p) => p && slots.has(p.slot) && p.name).length;
}

// clampRow keeps a remembered row choice valid for a speaker.
export function clampRow(row, rows) {
  const n = Number.parseInt(String(row), 10) || 1;
  return Math.max(1, Math.min(Math.max(1, Number(rows) || 1), n));
}

// presetsWithin keeps the presets a speaker with `rows` rows can hold, named
// and on a valid slot. The copy and the restore send nothing else.
export function presetsWithin(presets, rows) {
  const max = Math.max(1, Math.min(MAX_ROWS, Number(rows) || 1)) * ROW_SIZE;
  return (presets || []).filter((p) => p && p.slot >= 1 && p.slot <= max && p.name);
}

// copyRowsNote tells the copy result what happened to the source's extra rows.
// 'both' when the target keeps all configured rows, 'row1Only' as a compatible
// marker when some later-press keys stayed behind, '' otherwise.
export function copyRowsNote(sourcePresets, targetBox) {
  const maxSource = (sourcePresets || []).reduce((max, p) => p && p.name && p.slot >= 1 && p.slot <= MAX_SLOT ? Math.max(max, p.slot) : max, 0);
  if (maxSource <= ROW_SIZE) return '';
  const targetRows = boxPresetRows(targetBox);
  if (maxSource <= targetRows * ROW_SIZE) return 'both';
  return 'row1Only';
}

// SEEN_KEY remembers that the user opened the Twice tab once, so the "New"
// marker stops showing. Per app, not per speaker.
const SEEN_KEY = 'str.presetRow2Seen';

export function row2Seen(storage) {
  try {
    const s = storage === undefined ? globalThis.localStorage : storage;
    return !!(s && s.getItem(SEEN_KEY) === '1');
  } catch {
    return false;
  }
}

export function markRow2Seen(storage) {
  try {
    const s = storage === undefined ? globalThis.localStorage : storage;
    if (s) s.setItem(SEEN_KEY, '1');
  } catch { /* private window or blocked storage: the marker just stays */ }
}

// showNewMarker: the Twice tab wears "New" while the speaker supports row 2,
// nothing is on it yet, and the user has never opened it.
export function showNewMarker(rows, row2Count, seen) {
  return rows >= 2 && row2Count === 0 && !seen;
}
