import { describe, expect, it } from 'vitest';
import { chosenSlots, keyCopyOutcomeLines, summarizeKeyCopy } from './keycopyreport.js';
import en from './i18n/bundles/en.json';

// A t() over the real English bundle, so the sentences checked are the ones
// the user reads.
function t(key, vars = {}) {
  const s = en[key];
  if (s == null) throw new Error(`missing i18n key ${key}`);
  return s.replace(/\{\{(\w+)\}\}/g, (_, k) => (vars[k] == null ? '' : String(vars[k])));
}

describe('keyCopyOutcomeLines', () => {
  it('names the keys that went across', () => {
    expect(keyCopyOutcomeLines({ target: 'Bedroom', result: { copied: [2, 8], skipped: [], notKept: [] } }, t))
      .toEqual(['Bedroom: Key 2, Key 2 · 2× copied.']);
  });

  it('says which key was left out because the station is already on another key there', () => {
    const lines = keyCopyOutcomeLines({
      target: 'Kitchen',
      result: { copied: [3], skipped: [{ slot: 1, name: 'WDR 5', onSlot: 5 }], notKept: [] },
    }, t);
    expect(lines).toEqual([
      'Kitchen: Key 3 copied.',
      'Kitchen: Key 1 not copied, WDR 5 is already on key 5 there.',
    ]);
  });

  it('names a key on the second row of the target the way the app shows it', () => {
    const [line] = keyCopyOutcomeLines({
      target: 'Office',
      result: { copied: [], skipped: [{ slot: 2, name: 'Jazz', onSlot: 9 }], notKept: [] },
    }, t);
    expect(line).toBe('Office: Key 2 not copied, Jazz is already on key 3 · 2× there.');
  });

  it('passes on a refusal that is not a duplicate', () => {
    const [line] = keyCopyOutcomeLines({
      target: 'Office',
      result: { copied: [], skipped: [{ slot: 4, name: 'X', reason: 'status 422: no stream' }], notKept: [] },
    }, t);
    expect(line).toBe('Office: Key 4 not copied (status 422: no stream).');
  });

  it('says when a speaker with one row cannot hold a key pressed twice', () => {
    expect(keyCopyOutcomeLines({ target: 'Bath', result: { copied: [1], skipped: [], notKept: [7] } }, t))
      .toEqual(['Bath: Key 1 copied.', 'Bath: Key 1 · 2× not copied, this speaker has only one row of keys.']);
  });

  it('reports a speaker that took nothing', () => {
    expect(keyCopyOutcomeLines({ target: 'Bath', error: 'not answering' }, t))
      .toEqual(['Bath: nothing copied. not answering']);
    expect(keyCopyOutcomeLines({ target: 'Bath', result: { copied: [], skipped: [], notKept: [] } }, t))
      .toEqual([en['presets.keyCopyNothing'].replace('{{target}}', 'Bath')]);
  });
});

describe('summarizeKeyCopy', () => {
  it('is ok when every speaker took every key', () => {
    const r = summarizeKeyCopy([
      { target: 'A', result: { copied: [1], skipped: [], notKept: [] } },
      { target: 'B', result: { copied: [1], skipped: [], notKept: [] } },
    ], t);
    expect(r.kind).toBe('ok');
    expect(r.text).toBe('A: Key 1 copied. B: Key 1 copied.');
    expect(r.lines).toHaveLength(2);
  });

  it('warns when one speaker left a key out', () => {
    const r = summarizeKeyCopy([
      { target: 'A', result: { copied: [1], skipped: [], notKept: [] } },
      { target: 'B', result: { copied: [], skipped: [{ slot: 1, name: 'S', onSlot: 4 }], notKept: [] } },
    ], t);
    expect(r.kind).toBe('warn');
  });

  it('is an error when no speaker took anything', () => {
    expect(summarizeKeyCopy([{ target: 'A', error: 'x' }], t).kind).toBe('error');
    expect(summarizeKeyCopy([], t).kind).toBe('error');
  });
});

describe('chosenSlots', () => {
  it('reads the ticked keys in slot order', () => {
    const boxes = [
      { checked: true, value: '8' }, { checked: false, value: '2' }, { checked: true, value: '1' },
      { checked: true, value: '13' }, { checked: true, value: 'x' },
    ];
    expect(chosenSlots(boxes)).toEqual([1, 8]);
    expect(chosenSlots(null)).toEqual([]);
  });
});
