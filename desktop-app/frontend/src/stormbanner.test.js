// A wedged speaker (the "unplug it" banner) used to get the soft-restart notice
// as well, so the app gave two contradicting pieces of advice for the same
// speaker (discussion #1065). The restart notice now leaves it out.
import { describe, it, expect } from 'vitest';
import { stormBannerBoxes } from './utils.js';

describe('restart notice versus the unplug banner', () => {
  it('leaves a wedged speaker out of the restart notice', () => {
    const boxes = [
      { name: 'A', storm1036: true, boxHealth: 'wedged' },
      { name: 'B', recallRefusal: true, boxHealth: 'wedged' },
      { name: 'C', storm1036: true, boxHealth: 'ok' },
      { name: 'D', recallRefusal: true },
      { name: 'E' },
      null,
    ];
    expect(stormBannerBoxes(boxes).map(b => b.name)).toEqual(['C', 'D']);
  });

  it('leaves a speaker whose Bose software stopped answering out too', () => {
    const boxes = [
      { name: 'A', storm1036: true, boxHealth: 'firmware-unresponsive' },
      { name: 'B', recallRefusal: true, boxHealth: 'ok' },
    ];
    expect(stormBannerBoxes(boxes).map(b => b.name)).toEqual(['B']);
  });

  it('copes with no speaker list', () => {
    expect(stormBannerBoxes(undefined)).toEqual([]);
  });
});
