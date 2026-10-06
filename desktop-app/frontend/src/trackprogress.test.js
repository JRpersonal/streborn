import { describe, it, expect } from 'vitest';
import { applyPositionReading, resetProgress, wantsFastPoll, FAST_POLL_WINDOW_MS } from './trackprogress.js';

function fresh(key = 'loc|song', now = 1000) {
  const tp = { sec: 0, dur: 0, at: 0, key: '', resetAt: 0 };
  resetProgress(tp, key, now);
  return tp;
}

describe('applyPositionReading', () => {
  it('takes the length from the first reading after a track change', () => {
    const tp = fresh();
    expect(applyPositionReading(tp, { positionSec: 1, durationSec: 200 }, 'loc|song', 2000)).toBe(true);
    expect(tp).toMatchObject({ sec: 1, dur: 200, at: 2000 });
  });

  it('keeps the last reading when the speaker could not be asked', () => {
    const tp = fresh();
    applyPositionReading(tp, { positionSec: 10, durationSec: 200 }, 'loc|song', 2000);
    expect(applyPositionReading(tp, { positionSec: -1, durationSec: -1 }, 'loc|song', 7000)).toBe(false);
    expect(applyPositionReading(tp, null, 'loc|song', 7000)).toBe(false);
    expect(tp).toMatchObject({ sec: 10, dur: 200, at: 2000 });
  });

  it('keeps a known length through a momentary zero length', () => {
    const tp = fresh();
    applyPositionReading(tp, { positionSec: 10, durationSec: 200 }, 'loc|song', 2000);
    applyPositionReading(tp, { positionSec: 15, durationSec: 0 }, 'loc|song', 7000);
    expect(tp.dur).toBe(200);
    expect(tp.sec).toBe(15);
  });

  it('drops a reading that was taken for the previous track', () => {
    const tp = fresh('old|a');
    applyPositionReading(tp, { positionSec: 180, durationSec: 200 }, 'old|a', 2000);
    const keyAtStart = tp.key;
    resetProgress(tp, 'new|b', 3000);
    expect(applyPositionReading(tp, { positionSec: 181, durationSec: 200 }, keyAtStart, 3100)).toBe(false);
    expect(tp).toMatchObject({ sec: 0, dur: 0, key: 'new|b' });
  });

  it('does not run backwards on its own', () => {
    const tp = fresh();
    applyPositionReading(tp, { positionSec: 60, durationSec: 200 }, 'loc|song', 2000);
    expect(applyPositionReading(tp, { positionSec: 30, durationSec: 200 }, 'loc|song', 7000)).toBe(false);
    expect(applyPositionReading(tp, { positionSec: 30, durationSec: 0 }, 'loc|song', 7000)).toBe(false);
    expect(tp.sec).toBe(60);
  });

  it('leaves radio without a length', () => {
    const tp = fresh();
    applyPositionReading(tp, { positionSec: 5, durationSec: 0 }, 'loc|song', 6000);
    expect(tp.dur).toBe(0);
  });
});

describe('wantsFastPoll', () => {
  it('polls fast right after a track change until the length is known', () => {
    const tp = fresh('loc|song', 1000);
    expect(wantsFastPoll(tp, true, 2000)).toBe(true);
    applyPositionReading(tp, { positionSec: 1, durationSec: 200 }, 'loc|song', 2000);
    expect(wantsFastPoll(tp, true, 3000)).toBe(false);
  });

  it('stops after the window so radio does not poll every second', () => {
    const tp = fresh('loc|song', 1000);
    expect(wantsFastPoll(tp, true, 1000 + FAST_POLL_WINDOW_MS + 1)).toBe(false);
  });

  it('never polls while not playing or before any track was seen', () => {
    expect(wantsFastPoll(fresh(), false, 2000)).toBe(false);
    expect(wantsFastPoll({ sec: 0, dur: 0, at: 0, key: '', resetAt: 0 }, true, 2000)).toBe(false);
  });
});
