import { describe, it, expect } from 'vitest';
import { markLeft, pctFromOffset, THUMB_PX } from './startvolmark.js';

// Track widths that actually occur: a narrow settings panel, a normal one, and
// a maximised window on a wide screen.
const WIDTHS = [160, 240, 360, 520, 900];

describe('the marker lands where the handle would', () => {
  it('puts 0 and 100 at the ends of the thumb travel, not at the ends of the box', () => {
    // This is the whole reason THUMB_PX exists. A naive percentage would put
    // 0 at x=0 and 100 at x=width, which is half a handle out at each end.
    for (const w of WIDTHS) {
      expect(markLeft(w, 0)).toBeCloseTo(THUMB_PX / 2, 6);
      expect(markLeft(w, 100)).toBeCloseTo(w - THUMB_PX / 2, 6);
    }
  });

  it('puts 50 in the middle', () => {
    for (const w of WIDTHS) {
      expect(markLeft(w, 50)).toBeCloseTo(w / 2, 6);
    }
  });

  it('keeps a level inside the track whatever it is handed', () => {
    for (const w of WIDTHS) {
      for (const bad of [-20, 1e6, NaN, undefined, null, 'x']) {
        const x = markLeft(w, bad);
        expect(x).toBeGreaterThanOrEqual(THUMB_PX / 2 - 0.001);
        expect(x).toBeLessThanOrEqual(Math.max(THUMB_PX / 2, w - THUMB_PX / 2) + 0.001);
      }
    }
  });
});

describe('a drag means what it looked like', () => {
  // The property that matters: place a level, drag nothing, read it back, and
  // get the same level. If these two ever drift apart the marker shows one
  // number and the speaker is told another, and both halves look correct.
  it('round-trips every level on every width', () => {
    for (const w of WIDTHS) {
      for (let pct = 1; pct <= 100; pct++) {
        expect(pctFromOffset(w, markLeft(w, pct))).toBe(pct);
      }
    }
  });

  it('never returns 0, because 0 is how the speaker stores off', () => {
    for (const w of WIDTHS) {
      for (const x of [-500, -1, 0, 1, THUMB_PX / 2]) {
        expect(pctFromOffset(w, x)).toBeGreaterThanOrEqual(1);
      }
    }
  });

  it('never runs past 100 when the pointer leaves the track', () => {
    for (const w of WIDTHS) {
      expect(pctFromOffset(w, w + 400)).toBe(100);
    }
  });

  it('survives a track that has no width yet', () => {
    // The settings panel measures zero for a frame while it is still hidden.
    expect(() => markLeft(0, 50)).not.toThrow();
    expect(pctFromOffset(0, 10)).toBeGreaterThanOrEqual(1);
    expect(pctFromOffset(0, 10)).toBeLessThanOrEqual(100);
  });
});
