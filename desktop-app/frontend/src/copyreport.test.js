// Tests for the pure preset-copy rejection summary in copyreport.js.
import { describe, it, expect } from 'vitest';
import { summarizePresetCopyError, countValidPresetSlots, presetCopyConflict } from './copyreport.js';

describe('summarizePresetCopyError', () => {
  it('reconstructs the copied count from the combined per-slot message', () => {
    const msg = 'preset 2 (My Station): target says no; preset 5 (Other): timeout';
    const r = summarizePresetCopyError(msg, 6);
    expect(r.failedSlots).toEqual([2, 5]);
    expect(r.copied).toBe(4);
    expect(r.detail).toBe(msg);
  });
  it('handles a single rejected slot', () => {
    const r = summarizePresetCopyError('preset 3 (Jazz FM): unsupported type', 4);
    expect(r.failedSlots).toEqual([3]);
    expect(r.copied).toBe(3);
  });
  it('reports copied=0 when every valid slot failed (a true total failure)', () => {
    const r = summarizePresetCopyError('preset 1 (A): x; preset 2 (B): y', 2);
    expect(r.copied).toBe(0);
  });
  it('returns null for non-slot errors so they stay all-or-nothing', () => {
    expect(summarizePresetCopyError('read source presets: connection refused', 6)).toBeNull();
    expect(summarizePresetCopyError('source and target are the same speaker', 6)).toBeNull();
    expect(summarizePresetCopyError('', 6)).toBeNull();
    expect(summarizePresetCopyError(null, 6)).toBeNull();
  });
  it('does not trip over "preset N (" inside a station name mid-message', () => {
    // Only separator-anchored entries count; the name fragment is not one.
    const r = summarizePresetCopyError('preset 1 (about preset 4 (weird) name): rejected', 6);
    expect(r.failedSlots).toEqual([1]);
  });
  it('keeps copied null when the source slot count is unknown', () => {
    const r = summarizePresetCopyError('preset 2 (X): rejected', null);
    expect(r.failedSlots).toEqual([2]);
    expect(r.copied).toBeNull();
  });
  it('accepts an Error-like input via string coercion', () => {
    const r = summarizePresetCopyError(new Error('preset 6 (Z): rejected'), 6);
    expect(r.failedSlots).toEqual([6]);
    expect(r.copied).toBe(5);
  });
});

describe('countValidPresetSlots', () => {
  it('mirrors the backend filter: slot 1..6 with a non-empty name', () => {
    expect(countValidPresetSlots([
      { slot: 1, name: 'A' },
      { slot: 6, name: 'B' },
      { slot: 7, name: 'out of range' },
      { slot: 0, name: 'out of range' },
      { slot: 3, name: '' },
      null,
    ])).toBe(2);
  });
  it('tolerates an empty or missing list', () => {
    expect(countValidPresetSlots([])).toBe(0);
    expect(countValidPresetSlots(null)).toBe(0);
    expect(countValidPresetSlots(undefined)).toBe(0);
  });
});

describe('presetCopyConflict', () => {
  it('names the station and the key it already sits on', () => {
    const r = presetCopyConflict('already-on-slot: "Exclusively Rush" is already on key 6');
    expect(r).toEqual({ name: 'Exclusively Rush', slot: 6 });
  });
  it('unescapes a quoted station name, so the user sees what the key shows', () => {
    // Go's %q escapes a quote inside the name; the parser must undo that.
    const r = presetCopyConflict('already-on-slot: "Rush \\"Live\\"" is already on key 4');
    expect(r.name).toBe('Rush "Live"');
    expect(r.slot).toBe(4);
  });
  it('accepts an Error-like input via string coercion', () => {
    const r = presetCopyConflict(new Error('already-on-slot: "101 SMOOTH JAZZ" is already on key 1'));
    expect(r).toEqual({ name: '101 SMOOTH JAZZ', slot: 1 });
  });
  it('returns null for every other failure, so they keep their own handling', () => {
    expect(presetCopyConflict('preset 2 (X): status 500')).toBeNull();
    expect(presetCopyConflict('the target speaker is not answering yet')).toBeNull();
    expect(presetCopyConflict('')).toBeNull();
    expect(presetCopyConflict(null)).toBeNull();
  });
});
