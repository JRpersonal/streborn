import { describe, it, expect } from 'vitest';
import { pinPromptKey } from './worldmapinvite.js';

// The invite must always ASK for a pin. The count is decoration on top of that
// ask, never its precondition: the fetch behind it returns 0 on every failure,
// silently, so hanging the ask on it meant users with no route to the website
// got confetti and no request.
describe('pinPromptKey', () => {
  it('uses the counted sentence when a real count arrives', () => {
    expect(pinPromptKey(1)).toEqual({ key: 'worldMap.countLine', params: { n: 1 } });
    expect(pinPromptKey(4213)).toEqual({ key: 'worldMap.countLine', params: { n: 4213 } });
  });

  it('still asks when the count fetch failed, which it does by returning 0', () => {
    for (const n of [0, -1, null, undefined, NaN, Infinity, '12', {}, []]) {
      expect(pinPromptKey(n).key, String(n)).toBe('worldMap.pinPrompt');
    }
  });

  it('never interpolates a fractional count into the sentence', () => {
    expect(pinPromptKey(7.9)).toEqual({ key: 'worldMap.countLine', params: { n: 7 } });
  });
});
