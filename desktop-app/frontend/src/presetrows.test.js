// The second row of six keys (#1110): a hardware key pressed twice quickly
// plays slot 6+n. The app shows "Key 1 · 2×", never "7", and only on a speaker
// whose agent reports presetRows "2"; every other speaker keeps the one row it
// always had.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import {
  ROW_SIZE,
  boxMaxSlot,
  boxPresetRows,
  clampRow,
  copyRowsNote,
  keyLabel,
  keyNum,
  markRow2Seen,
  nowKeyTag,
  presetsWithin,
  row2Seen,
  rowCount,
  rowSlot,
  rowSlots,
  showNewMarker,
  slotKey,
  slotRow,
} from './presetrows.js';
import { offerPresetMove, presetConflictNote } from './presetmove.js';
import { isStrOriginLocation, activeSlotFromLocation } from './utils.js';

const dir = fileURLToPath(new URL('./i18n/bundles/', import.meta.url));
const langs = readdirSync(dir).filter((f) => f.endsWith('.json')).map((f) => f.replace('.json', ''));
const bundle = (l) => JSON.parse(readFileSync(new URL(`./i18n/bundles/${l}.json`, import.meta.url), 'utf8'));
const translator = (l = 'en') => {
  const b = bundle(l);
  return (key, vals) => String(b[key] || key).replace(/\{\{(\w+)\}\}/g, (_, k) => (vals && vals[k] != null ? String(vals[k]) : ''));
};

describe('row mapping', () => {
  it('puts key n pressed twice on slot 6+n', () => {
    expect(ROW_SIZE).toBe(6);
    expect(rowSlot(1, 1)).toBe(1);
    expect(rowSlot(2, 1)).toBe(7);
    expect(rowSlot(2, 6)).toBe(12);
    expect(rowSlots(1)).toEqual([1, 2, 3, 4, 5, 6]);
    expect(rowSlots(2)).toEqual([7, 8, 9, 10, 11, 12]);
  });

  it('reads the row and the hardware key back from a slot', () => {
    for (let s = 1; s <= 12; s++) {
      expect(rowSlot(slotRow(s), slotKey(s))).toBe(s);
    }
    expect(slotRow(6)).toBe(1);
    expect(slotRow(7)).toBe(2);
    expect(slotKey(9)).toBe(3);
  });
});

describe('labels', () => {
  const t = translator('en');
  it('never shows 7 to 12', () => {
    expect(keyLabel(1, t)).toBe('Key 1');
    expect(keyLabel(7, t)).toBe('Key 1 · 2×');
    expect(keyLabel(12, t)).toBe('Key 6 · 2×');
    for (let s = 7; s <= 12; s++) {
      expect(keyLabel(s, t)).not.toMatch(/\b(7|8|9|10|11|12)\b/);
    }
  });

  it('fits a row-2 key into every "key {{n}}" sentence', () => {
    expect(keyNum(3, t)).toBe(3);
    expect(keyNum(8, t)).toBe('2 · 2×');
    expect(t('preset.savedToKey', { n: keyNum(8, t), name: 'WDR 5' })).toBe('Saved to key 2 · 2×: WDR 5');
  });

  it('names the playing key on the now-playing line, either row, only for an STR key', () => {
    expect(nowKeyTag(2, t)).toBe('Key 2');
    expect(nowKeyTag(7, t)).toBe('Key 1 · 2×');
    expect(nowKeyTag(null, t)).toBe('');
    expect(nowKeyTag(13, t)).toBe('');
    expect(nowKeyTag(2, translator('de'))).toBe('Taste 2');
  });

  it('reads "Taste 1 · 2×" in German', () => {
    expect(keyLabel(7, translator('de'))).toBe('Taste 1 · 2×');
    expect(translator('de')('preset.row2.tabTwice')).toBe('Zweimal');
    expect(translator('de')('preset.row2.tabOnce')).toBe('Einmal');
  });

  it('is translated in every bundle, with the same placeholders and no dashes', () => {
    const en = bundle('en');
    const keys = Object.keys(en).filter((k) => k.startsWith('preset.row2.')
      || /^settingsView\.copyPresets(HelpAll|Row1Only|Row1OnlyList)$/.test(k)
      || k === 'presets.transferReplacesBoth');
    expect(keys.length).toBe(17);
    const ph = (s) => (String(s).match(/\{\{\s*\w+\s*\}\}/g) || []).sort().join('|');
    for (const l of langs) {
      const b = bundle(l);
      for (const k of keys) {
        expect(b[k], `${l} ${k}`).toBeTruthy();
        expect(ph(b[k]), `${l} ${k}`).toBe(ph(en[k]));
        expect(b[k], `${l} ${k}`).not.toMatch(/[–—]| - /);
        if (l !== 'en' && !['preset.row2.num', 'preset.row2.badge'].includes(k)) {
          expect(b[k], `${l} ${k} is still English`).not.toBe(en[k]);
        }
      }
    }
  });
});

describe('capability gate', () => {
  it('offers row 2 only when the agent reports presetRows "2"', () => {
    expect(boxPresetRows({ kind: 'str', presetRows: '2' })).toBe(2);
    expect(boxPresetRows({ kind: 'str' })).toBe(1);
    expect(boxPresetRows({ kind: 'str', presetRows: '1' })).toBe(1);
    expect(boxPresetRows(null)).toBe(1);
    expect(boxPresetRows({ kind: 'stock', presetRows: '2' })).toBe(1);
    expect(boxMaxSlot({ kind: 'str', presetRows: '2' })).toBe(12);
    expect(boxMaxSlot({ kind: 'str' })).toBe(6);
  });

  it('trusts a store that already holds a row-2 key', () => {
    expect(boxPresetRows({ kind: 'str' }, [{ slot: 9, name: 'X' }])).toBe(2);
    expect(boxPresetRows({ kind: 'str' }, [{ slot: 3, name: 'X' }])).toBe(1);
  });

  it('falls back to row 1 on a speaker without row 2', () => {
    expect(clampRow(2, 1)).toBe(1);
    expect(clampRow(2, 2)).toBe(2);
    expect(clampRow(undefined, 2)).toBe(1);
  });
});

describe('counts and the New marker', () => {
  const presets = [{ slot: 1, name: 'A' }, { slot: 7, name: 'B' }, { slot: 11, name: 'C' }, { slot: 8, name: '' }];
  it('counts the occupied keys of a row', () => {
    expect(rowCount(presets, 1)).toBe(1);
    expect(rowCount(presets, 2)).toBe(2);
    expect(rowCount([], 2)).toBe(0);
  });

  it('shows "New" only while row 2 is empty, supported and never opened', () => {
    expect(showNewMarker(2, 0, false)).toBe(true);
    expect(showNewMarker(2, 0, true)).toBe(false);
    expect(showNewMarker(2, 1, false)).toBe(false);
    expect(showNewMarker(1, 0, false)).toBe(false);
  });

  it('remembers the first visit and survives storage that throws', () => {
    const mem = new Map();
    const storage = { getItem: (k) => mem.get(k) ?? null, setItem: (k, v) => mem.set(k, v) };
    expect(row2Seen(storage)).toBe(false);
    markRow2Seen(storage);
    expect(row2Seen(storage)).toBe(true);
    const broken = { getItem: () => { throw new Error('blocked'); }, setItem: () => { throw new Error('blocked'); } };
    expect(row2Seen(broken)).toBe(false);
    expect(() => markRow2Seen(broken)).not.toThrow();
  });
});

describe('copying to other speakers', () => {
  const src = [{ slot: 1, name: 'A' }, { slot: 7, name: 'B' }, { slot: 13, name: 'never' }, { slot: 2, name: '' }];
  it('keeps both rows for a target with two rows and row 1 for one without', () => {
    expect(presetsWithin(src, 2).map((p) => p.slot)).toEqual([1, 7]);
    expect(presetsWithin(src, 1).map((p) => p.slot)).toEqual([1]);
  });

  it('says when the keys pressed twice stayed behind', () => {
    expect(copyRowsNote(src, { kind: 'str', presetRows: '2' })).toBe('both');
    expect(copyRowsNote(src, { kind: 'str' })).toBe('row1Only');
    expect(copyRowsNote([{ slot: 1, name: 'A' }], { kind: 'str' })).toBe('');
  });
});

describe('the move offer across rows', () => {
  const t = translator('en');
  it('names a row-2 key the way the grid does', () => {
    expect(presetConflictNote({ slot: 9, name: 'WDR 5' }, t)).toBe('"WDR 5" is already on key 3 · 2×. Start the one you want on the speaker first, then save it.');
  });

  it('moves a station from row 1 to row 2', async () => {
    const calls = { confirms: [], moves: [], toasts: [] };
    const moved = await offerPresetMove({
      conflict: { slot: 2, name: 'WDR 5' },
      toSlot: 8,
      t,
      confirm: (title, body, opts) => { calls.confirms.push(opts.confirmLabel); return Promise.resolve(true); },
      move: (from, to) => { calls.moves.push([from, to]); return Promise.resolve(); },
      toast: (m) => calls.toasts.push(m),
      fail: () => {},
      done: () => Promise.resolve(),
    });
    expect(moved).toBe(true);
    expect(calls.moves).toEqual([[2, 8]]);
    expect(calls.confirms[0]).toBe('Move it to key 2 · 2×');
    expect(calls.toasts[0]).toBe('Now on key 2 · 2×. Key 2 is free again.');
  });
});

describe('locations of row-2 keys', () => {
  it('reads slots 7 to 12 from the proxy forms', () => {
    expect(activeSlotFromLocation('http://127.0.0.1:8888/stream/9')).toBe(9);
    expect(activeSlotFromLocation('http://127.0.0.1:8888/spotify/stream-12.ogg')).toBe(12);
    expect(isStrOriginLocation('http://127.0.0.1:8888/stream/12')).toBe(true);
    expect(isStrOriginLocation('http://127.0.0.1:8888/spotify/stream-10.ogg')).toBe(true);
    expect(isStrOriginLocation('http://127.0.0.1:8888/stream/13')).toBe(false);
  });
});
