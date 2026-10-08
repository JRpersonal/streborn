import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
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

// The bar hid for a few seconds mid-song (1:34 to 1:39) while the elapsed clock
// kept counting: the speaker briefly reported a different name for the same
// track, which reset the record, and then answered without a length (#1190).
describe('a briefly different track name mid-song', () => {
  function playing() {
    const tp = fresh('loc|Song');
    applyPositionReading(tp, { positionSec: 90, durationSec: 240 }, 'loc|Song', 10_000);
    return tp;
  }

  it('keeps the length when the position continues the previous clock', () => {
    const tp = playing();
    resetProgress(tp, 'loc|Song (Live)', 14_000);
    expect(applyPositionReading(tp, { positionSec: 94, durationSec: 0 }, 'loc|Song (Live)', 14_000)).toBe(true);
    expect(tp).toMatchObject({ sec: 94, dur: 240 });
  });

  it('survives the name flipping away and straight back', () => {
    const tp = playing();
    resetProgress(tp, 'loc|Song (Live)', 14_000);
    resetProgress(tp, 'loc|Song', 15_000);
    applyPositionReading(tp, { positionSec: 95.5, durationSec: 0 }, 'loc|Song', 15_500);
    expect(tp.dur).toBe(240);
  });

  it('does not carry the length onto a real new track', () => {
    const tp = playing();
    resetProgress(tp, 'loc|Next', 14_000);
    applyPositionReading(tp, { positionSec: 1, durationSec: 0 }, 'loc|Next', 15_000);
    expect(tp.dur).toBe(0);
  });

  it('does not carry the length onto radio that follows a track', () => {
    const tp = playing();
    resetProgress(tp, 'radio|Station', 14_000);
    applyPositionReading(tp, { positionSec: 3, durationSec: 0 }, 'radio|Station', 17_000);
    expect(tp.dur).toBe(0);
  });

  it('takes a length the speaker does report', () => {
    const tp = playing();
    resetProgress(tp, 'loc|Song (Live)', 14_000);
    applyPositionReading(tp, { positionSec: 94, durationSec: 250 }, 'loc|Song (Live)', 14_000);
    expect(tp.dur).toBe(250);
  });
});

// Source-level, because refreshStatus is DOM-bound: a track-name change alone
// must not rebuild the preset grid, whose keys never show the running name.
describe('refreshStatus and the preset grid', () => {
  it('rebuilds the grid on a play state or location change, not on a name change', () => {
    const main = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'main.js'), 'utf-8');
    expect(main).toContain('const gridChanged = state.nowPlayState !== ps || state.nowLocation !== newLoc;');
    expect(main).toContain('if ((gridChanged || iconAdoptable) && state.presets.length > 0) {');
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
