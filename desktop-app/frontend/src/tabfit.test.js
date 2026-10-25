import { describe, it, expect } from 'vitest';
import { fitTabs, watchTabs } from './tabfit.js';

// A stand-in bar whose content width depends on the classes it carries, the
// way the real one does: tighter padding saves some width, wrapping fits all.
function fakeBar(widths, clientWidth) {
  const set = new Set();
  return {
    clientWidth,
    classList: {
      add: (c) => set.add(c),
      remove: (...cs) => cs.forEach((c) => set.delete(c)),
      contains: (c) => set.has(c),
    },
    get scrollWidth() {
      if (set.has('tabs-wrap')) return clientWidth;
      return set.has('tabs-compact') ? widths.compact : widths.normal;
    },
    querySelectorAll: () => [],
  };
}

describe('fitTabs', () => {
  it('keeps the usual row when it fits', () => {
    const bar = fakeBar({ normal: 690, compact: 600 }, 692);
    expect(fitTabs(bar)).toBe('');
  });

  it('tightens the padding before it wraps', () => {
    const bar = fakeBar({ normal: 766, compact: 640 }, 692);
    expect(fitTabs(bar)).toBe('tabs-compact');
  });

  it('wraps when even the tight row overflows', () => {
    const bar = fakeBar({ normal: 1100, compact: 960 }, 604);
    expect(fitTabs(bar)).toBe('tabs-compact tabs-wrap');
  });

  it('goes back to the usual row once there is room again', () => {
    const bar = fakeBar({ normal: 900, compact: 800 }, 604);
    expect(fitTabs(bar)).toBe('tabs-compact tabs-wrap');
    bar.clientWidth = 920;
    expect(fitTabs(bar)).toBe('');
  });

  it('ignores a missing bar', () => {
    expect(fitTabs(null)).toBe('');
  });
});

describe('watchTabs', () => {
  it('refits in the next frame, once per burst of resize notifications', () => {
    let notify;
    const frames = [];
    const win = {
      ResizeObserver: class { constructor(cb) { notify = cb; } observe() {} },
      requestAnimationFrame: (fn) => frames.push(fn),
    };
    const bar = fakeBar({ normal: 690, compact: 600 }, 692);
    watchTabs(bar, win);
    expect(bar.classList.contains('tabs-compact')).toBe(false);
    bar.clientWidth = 604;
    notify();
    notify();
    expect(frames).toHaveLength(1);
    expect(bar.classList.contains('tabs-compact')).toBe(false);
    frames[0]();
    expect(bar.classList.contains('tabs-compact')).toBe(true);
    expect(bar.classList.contains('tabs-wrap')).toBe(false);
  });

  it('still fits once without a ResizeObserver', () => {
    const bar = fakeBar({ normal: 766, compact: 640 }, 692);
    watchTabs(bar, {});
    expect(bar.classList.contains('tabs-compact')).toBe(true);
  });
});
