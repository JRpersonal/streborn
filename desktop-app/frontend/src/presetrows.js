// presetrows.js: the second row of six keys (#1110).
//
// A speaker whose agent reports presetRows "2" in /api/agent/version keeps
// twelve keys: slots 1 to 6 are the hardware keys pressed once, slots 7 to 12
// are the same keys pressed twice quickly (slot 6+n is key n pressed twice).
// The app never shows "7" to "12": a row-2 key is "Key 1 · 2×". An older agent
// reports nothing and keeps exactly the one row it always had.
//
// Pure (no DOM, no api.js), so the mapping and the gate are unit-tested.

export const ROW_SIZE = 6;

// boxPresetRows is how many rows of keys the speaker offers: 2 when its agent
// says so, 1 for everything else (an older agent, a stock speaker, no speaker).
// presets is the store the app just read: a store that already holds a row-2
// key proves the agent keeps them, even before discovery has re-read the
// version (right after an update to a supporting agent).
export function boxPresetRows(box, presets) {
  if (!box || box.kind === 'stock') return 1;
  if (String(box.presetRows || '') === '2') return 2;
  if (Array.isArray(presets) && presets.some((p) => p && p.slot > ROW_SIZE && p.slot <= 2 * ROW_SIZE)) return 2;
  return 1;
}

// boxMaxSlot is the highest slot the speaker accepts.
export function boxMaxSlot(box, presets) {
  return boxPresetRows(box, presets) * ROW_SIZE;
}

// slotRow is 1 for slots 1..6 and 2 for 7..12.
export function slotRow(slot) {
  return Number(slot) > ROW_SIZE ? 2 : 1;
}

// slotKey is the hardware key a slot sits on, 1..6 for either row.
export function slotKey(slot) {
  const n = Number(slot);
  return n > ROW_SIZE ? n - ROW_SIZE : n;
}

// rowSlot is the slot of hardware key `key` (1..6) in row `row` (1|2).
export function rowSlot(row, key) {
  return (row === 2 ? ROW_SIZE : 0) + Number(key);
}

// rowSlots lists the six slots of a row.
export function rowSlots(row) {
  const out = [];
  for (let k = 1; k <= ROW_SIZE; k++) out.push(rowSlot(row, k));
  return out;
}

// keyNum is what goes into a sentence that says "key {{n}}": the plain number
// for a row-1 key, "1 · 2×" for a row-2 key. t is the i18n lookup.
export function keyNum(slot, t) {
  const n = Number(slot);
  if (!(n > ROW_SIZE)) return n;
  return t('preset.row2.num', { n: n - ROW_SIZE });
}

// keyLabel is the label of a key on its own: "Key 1" or "Key 1 · 2×".
export function keyLabel(slot, t) {
  const n = Number(slot);
  if (!(n > ROW_SIZE)) return t('preset.key', { n });
  return t('preset.row2.key', { n: n - ROW_SIZE });
}

// rowCount is how many keys of a row hold something.
export function rowCount(presets, row) {
  const slots = new Set(rowSlots(row));
  return (presets || []).filter((p) => p && slots.has(p.slot) && p.name).length;
}

// clampRow keeps a remembered row choice valid for a speaker: row 2 only where
// the speaker has one.
export function clampRow(row, rows) {
  return row === 2 && rows >= 2 ? 2 : 1;
}

// presetsWithin keeps the presets a speaker with `rows` rows can hold, named
// and on a valid slot. The copy and the restore send nothing else.
export function presetsWithin(presets, rows) {
  const max = (rows >= 2 ? 2 : 1) * ROW_SIZE;
  return (presets || []).filter((p) => p && p.slot >= 1 && p.slot <= max && p.name);
}

// copyRowsNote tells the copy result what happened to row 2. 'both' when the
// target keeps both rows, 'row1Only' when the source has row-2 keys the target
// cannot take (so the result has to say they stayed behind), '' otherwise.
export function copyRowsNote(sourcePresets, targetBox) {
  const hasRow2 = (sourcePresets || []).some((p) => p && p.slot > ROW_SIZE && p.slot <= 2 * ROW_SIZE && p.name);
  if (!hasRow2) return '';
  return boxPresetRows(targetBox) >= 2 ? 'both' : 'row1Only';
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
