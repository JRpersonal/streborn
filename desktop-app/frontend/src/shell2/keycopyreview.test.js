import { describe, expect, it, vi } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import {
  REVIEW_ACTIONS, gatherKeyCopyPreviews, reviewKeys, reviewModel, reviewStepLine,
} from './keycopyreview.js';
import en from '../i18n/bundles/en.json';

// A t() over the real English bundle, so the sentences checked are the ones
// the user reads.
function t(key, vars = {}) {
  const s = en[key];
  if (s == null) throw new Error(`missing i18n key ${key}`);
  return s.replace(/\{\{(\w+)\}\}/g, (_, k) => (vars[k] == null ? '' : String(vars[k])));
}

const fill = (key, vars) => t(key, vars);

describe('reviewStepLine', () => {
  it('says what happens to each key, naming key and station', () => {
    expect(reviewStepLine({ slot: 1, name: 'BBC Radio 1', action: 'copy' }, t))
      .toEqual({ slot: 1, action: 'copy', kind: 'change', text: fill('ui2.keyReview.copy', { key: 'Key 1', name: 'BBC Radio 1' }) });
    expect(reviewStepLine({ slot: 3, name: 'Jazz24', action: 'replace', replaces: 'Old Station' }, t).text)
      .toBe(fill('ui2.keyReview.replace', { key: 'Key 3', name: 'Jazz24', old: 'Old Station' }));
    expect(reviewStepLine({ slot: 2, name: 'FIP', action: 'same' }, t).text)
      .toBe(fill('ui2.keyReview.same', { key: 'Key 2', name: 'FIP' }));
    expect(reviewStepLine({ slot: 4, name: 'FIP', action: 'onOtherKey', onSlot: 6 }, t).text)
      .toBe(fill('ui2.keyReview.onOtherKey', { key: 'Key 4', name: 'FIP', n: 6 }));
  });

  it('names a key pressed twice the way the keys do', () => {
    const line = reviewStepLine({ slot: 8, name: 'Row Two', action: 'notKept' }, t);
    expect(line.text).toBe(fill('ui2.keyReview.notKept', { key: fill('preset.row2.key', { n: 2 }), name: 'Row Two' }));
    expect(line.kind).toBe('skip');
  });

  it('marks a replacement apart from a plain copy and from the keys left alone', () => {
    const kinds = Object.fromEntries(REVIEW_ACTIONS.map((a) => [a, reviewStepLine({ slot: 1, name: 'X', action: a, onSlot: 2 }, t).kind]));
    expect(kinds).toEqual({ copy: 'change', replace: 'replace', same: 'skip', onOtherKey: 'skip', notKept: 'skip' });
  });

  it('treats an unknown action as a copy rather than hiding the key', () => {
    expect(reviewStepLine({ slot: 5, name: 'X', action: 'future' }, t).action).toBe('copy');
  });
});

describe('reviewModel', () => {
  it('has one block per speaker, its keys in slot order, and counts the writes', () => {
    const m = reviewModel([
      { target: 'Bedroom', steps: [{ slot: 3, name: 'C', action: 'replace', replaces: 'Z' }, { slot: 1, name: 'A', action: 'copy' }] },
      { target: 'Office', steps: [{ slot: 1, name: 'A', action: 'same' }] },
    ], t);
    expect(m.blocks.map((b) => b.target)).toEqual(['Bedroom', 'Office']);
    expect(m.blocks[0].lines.map((l) => l.slot)).toEqual([1, 3]);
    expect(m.writes).toBe(2);
    expect(m.canCopy).toBe(true);
  });

  it('offers no copy when nothing would change anywhere', () => {
    const m = reviewModel([{ target: 'Bedroom', steps: [{ slot: 1, name: 'A', action: 'same' }, { slot: 8, name: 'B', action: 'notKept' }] }], t);
    expect(m.writes).toBe(0);
    expect(m.canCopy).toBe(false);
  });

  it('says which speaker could not be checked, and still lets the copy try it', () => {
    const m = reviewModel([{ target: 'Kitchen', error: 'not answering' }], t);
    expect(m.blocks[0].error).toBe(fill('ui2.keyReview.unchecked', { reason: 'not answering' }));
    expect(m.unchecked).toBe(1);
    expect(m.canCopy).toBe(true);
  });
});

describe('gatherKeyCopyPreviews', () => {
  const box = { host: 'demobox.local', port: 8888 };
  const targets = [{ host: '192.0.2.52', port: 8888, label: 'Bedroom' }, { host: '192.0.2.53', port: 0, label: '' }];

  it('asks every target with the copy\'s own arguments', async () => {
    const preview = vi.fn(async () => ({ steps: [{ slot: 2, name: 'B', action: 'copy' }] }));
    const got = await gatherKeyCopyPreviews({ box, targets, slots: [2], preview, isMissing: () => false });
    expect(preview.mock.calls).toEqual([
      ['demobox.local', 8888, '192.0.2.52', 8888, [2]],
      ['demobox.local', 8888, '192.0.2.53', 0, [2]],
    ]);
    expect(got.unavailable).toBe(false);
    expect(got.outcomes.map((o) => o.target)).toEqual(['Bedroom', '192.0.2.53']);
  });

  it('keeps going past a speaker that does not answer', async () => {
    const preview = vi.fn()
      .mockRejectedValueOnce(new Error('read target presets: timeout'))
      .mockResolvedValueOnce({ steps: [] });
    const got = await gatherKeyCopyPreviews({ box, targets, slots: [1], preview, isMissing: () => false, explain: (s) => `[${s}]` });
    expect(got.outcomes[0]).toEqual({ target: 'Bedroom', error: '[read target presets: timeout]' });
    expect(got.outcomes[1]).toEqual({ target: '192.0.2.53', steps: [] });
  });

  it('reports a build without the preview, so the copy runs as before', async () => {
    const missing = new Error('STR_MISSING_BINDING');
    const preview = vi.fn().mockRejectedValue(missing);
    const got = await gatherKeyCopyPreviews({ box, targets, slots: [1], preview, isMissing: (e) => e === missing });
    expect(got.unavailable).toBe(true);
    expect(preview).toHaveBeenCalledTimes(1);
  });
});

describe('review strings', () => {
  const dir = new URL('../i18n/bundles/', import.meta.url);
  const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
    .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));

  it('are translated in every language, not English copies', () => {
    for (const [code, b] of Object.entries(bundles)) {
      for (const k of [...reviewKeys(), 'presets.keyCopySame']) {
        expect(typeof b[k] === 'string' && b[k].trim().length > 0, `${code}: ${k}`).toBe(true);
        if (code !== 'en') expect(b[k], `${code}: ${k}`).not.toBe(en[k]);
      }
    }
  });

  it('use no em dash', () => {
    for (const b of Object.values(bundles)) {
      for (const k of reviewKeys()) expect(b[k]).not.toContain('—');
    }
  });
});
