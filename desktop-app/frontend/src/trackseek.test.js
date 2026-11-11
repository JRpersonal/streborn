import { readFileSync } from 'node:fs';
import { describe, it, expect } from 'vitest';
import {
  applyPositionReading,
  beginSeek,
  canSeek,
  failSeek,
  resetProgress,
  seekKeyTargetSec,
  seekTargetSec,
  shownSec,
  SEEK_KEY_STEP_SEC,
  SEEK_SETTLE_MS,
} from './trackprogress.js';
import { fileSource, srcOf } from './test/source.js';

// Discussion #1207: a click on the progress bar jumps inside a music-library
// track. The bar is clickable only when the speaker says the track is
// seekable; radio stays the inert bar it was.

const KEY = 'loc|song';

// A record with one reading: second `sec` of a `dur` s track at time `now`.
function reading(sec, dur, seekable, now = 2000) {
  const tp = { sec: 0, dur: 0, at: 0, key: '', resetAt: 0 };
  resetProgress(tp, KEY, 1000);
  applyPositionReading(tp, { positionSec: sec, durationSec: dur, seekable }, KEY, now);
  return tp;
}

describe('the seekable gate', () => {
  it('opens only on the speaker saying so', () => {
    expect(canSeek(reading(10, 200, true))).toBe(true);
    expect(canSeek(reading(10, 200, false))).toBe(false);
  });

  it('stays shut for an agent that does not send the field', () => {
    expect(canSeek(reading(10, 200, undefined))).toBe(false);
    // Anything but a real true is a no: a string from a proxy, a number.
    expect(canSeek(reading(10, 200, 'true'))).toBe(false);
    expect(canSeek(reading(10, 200, 1))).toBe(false);
  });

  it('stays shut for radio, which has no length, whatever the flag says', () => {
    expect(canSeek(reading(10, 0, true))).toBe(false);
  });

  it('stays shut before any reading and for a missing record', () => {
    const tp = { sec: 0, dur: 0, at: 0, key: '', resetAt: 0 };
    expect(canSeek(tp)).toBe(false);
    expect(canSeek(null)).toBe(false);
  });

  it('closes on a track change until the speaker answers for the new track', () => {
    const tp = reading(10, 200, true);
    resetProgress(tp, 'loc|next', 3000);
    expect(canSeek(tp)).toBe(false);
    applyPositionReading(tp, { positionSec: 1, durationSec: 180, seekable: true }, 'loc|next', 3500);
    expect(canSeek(tp)).toBe(true);
  });

  it('follows the speaker when the same track stops being seekable (a pause)', () => {
    const tp = reading(10, 200, true);
    applyPositionReading(tp, { positionSec: 12, durationSec: 200, seekable: false }, KEY, 4000);
    expect(canSeek(tp)).toBe(false);
  });

  it('does not take the flag from a reading of the previous track', () => {
    const tp = reading(10, 200, false);
    applyPositionReading(tp, { positionSec: 11, durationSec: 200, seekable: true }, 'old|track', 3000);
    expect(canSeek(tp)).toBe(false);
  });
});

describe('seekTargetSec', () => {
  // A 96 px bar starting at x=100, a 240 s track.
  const at = (x, rtl = false) => seekTargetSec(x, 100, 96, 240, rtl);

  it('maps the pointer to a whole second of the track', () => {
    expect(at(100)).toBe(0);
    expect(at(148)).toBe(120);
    expect(at(196)).toBe(240);
    expect(at(124)).toBe(60);
    expect(Number.isInteger(at(131.7))).toBe(true);
  });

  it('lands on the nearer end when the pointer left the bar', () => {
    expect(at(40)).toBe(0);
    expect(at(500)).toBe(240);
  });

  it('mirrors in a right-to-left layout, where the bar fills from the right', () => {
    expect(at(196, true)).toBe(0);
    expect(at(100, true)).toBe(240);
    expect(at(124, true)).toBe(180);
  });

  it('answers -1 when there is nothing to compute from', () => {
    expect(seekTargetSec(120, 100, 0, 240, false)).toBe(-1);
    expect(seekTargetSec(120, 100, 96, 0, false)).toBe(-1);
    expect(seekTargetSec(NaN, 100, 96, 240, false)).toBe(-1);
    expect(seekTargetSec(undefined, 100, 96, 240, false)).toBe(-1);
  });
});

describe('seekKeyTargetSec', () => {
  it('steps forward and back from where the bar stands', () => {
    const tp = reading(60, 240, true, 2000);
    // 5 s after the reading, playing: the bar stands on 65.
    expect(seekKeyTargetSec(tp, 'ArrowRight', false, true, 7000)).toBe(65 + SEEK_KEY_STEP_SEC);
    expect(seekKeyTargetSec(tp, 'ArrowLeft', false, true, 7000)).toBe(65 - SEEK_KEY_STEP_SEC);
    expect(seekKeyTargetSec(tp, 'ArrowUp', false, true, 7000)).toBe(65 + SEEK_KEY_STEP_SEC);
    expect(seekKeyTargetSec(tp, 'ArrowDown', false, true, 7000)).toBe(65 - SEEK_KEY_STEP_SEC);
  });

  it('swaps left and right in a right-to-left layout', () => {
    const tp = reading(60, 240, true, 2000);
    expect(seekKeyTargetSec(tp, 'ArrowLeft', true, false, 2000)).toBe(60 + SEEK_KEY_STEP_SEC);
    expect(seekKeyTargetSec(tp, 'ArrowRight', true, false, 2000)).toBe(60 - SEEK_KEY_STEP_SEC);
  });

  it('stays inside the track', () => {
    expect(seekKeyTargetSec(reading(3, 240, true), 'ArrowLeft', false, false, 2000)).toBe(0);
    expect(seekKeyTargetSec(reading(236, 240, true), 'ArrowRight', false, false, 2000)).toBe(240);
    expect(seekKeyTargetSec(reading(100, 240, true), 'Home', false, false, 2000)).toBe(0);
  });

  it('leaves every other key alone', () => {
    const tp = reading(60, 240, true);
    for (const key of ['Enter', ' ', 'Tab', 'a', 'Escape', 'End']) {
      expect(seekKeyTargetSec(tp, key, false, true, 2000)).toBe(-1);
    }
  });
});

describe('shownSec', () => {
  it('runs on between readings while playing and stops at the end', () => {
    const tp = reading(60, 240, true, 2000);
    expect(shownSec(tp, true, 5000)).toBe(63);
    expect(shownSec(tp, false, 5000)).toBe(60);
    expect(shownSec(tp, true, 2000 + 600_000)).toBe(240);
  });
});

describe('a jump shows at once and then follows the speaker', () => {
  it('shows the target before the speaker answered', () => {
    const tp = reading(60, 240, true, 2000);
    beginSeek(tp, 150, 3000);
    expect(shownSec(tp, true, 3000)).toBe(150);
    expect(shownSec(tp, true, 4000)).toBe(151);
  });

  it('takes a reading near the target, also a jump BACK', () => {
    const tp = reading(180, 240, true, 2000);
    beginSeek(tp, 30, 3000);
    // Without the jump, 31 against a clock at 181 is "running backwards" and dropped.
    expect(applyPositionReading(tp, { positionSec: 31, durationSec: 240, seekable: true }, KEY, 4000)).toBe(true);
    expect(tp.sec).toBe(31);
    expect(tp.seekAt).toBe(0);
    // And from then on the usual rule is back: no running backwards.
    expect(applyPositionReading(tp, { positionSec: 5, durationSec: 240, seekable: true }, KEY, 6000)).toBe(false);
    expect(tp.sec).toBe(31);
  });

  it('ignores a reading from before the speaker jumped, while the jump settles', () => {
    const tp = reading(60, 240, true, 2000);
    beginSeek(tp, 150, 3000);
    // The speaker still reports the old position a moment later.
    expect(applyPositionReading(tp, { positionSec: 62, durationSec: 240, seekable: true }, KEY, 4000)).toBe(false);
    expect(shownSec(tp, true, 4000)).toBe(151);
    // Then it has jumped.
    expect(applyPositionReading(tp, { positionSec: 152, durationSec: 240, seekable: true }, KEY, 5000)).toBe(true);
    expect(tp.sec).toBe(152);
  });

  it('gives in to the speaker when the jump never happened', () => {
    const tp = reading(60, 240, true, 2000);
    beginSeek(tp, 150, 3000);
    const late = 3000 + SEEK_SETTLE_MS + 1;
    expect(applyPositionReading(tp, { positionSec: 66, durationSec: 240, seekable: true }, KEY, late)).toBe(true);
    expect(tp.sec).toBe(66);
    expect(tp.seekAt).toBe(0);
  });

  it('keeps the length through a settling reading without one', () => {
    const tp = reading(60, 240, true, 2000);
    beginSeek(tp, 150, 3000);
    applyPositionReading(tp, { positionSec: 151, durationSec: 0, seekable: true }, KEY, 4000);
    expect(tp.dur).toBe(240);
  });

  it('goes back to the real clock when the speaker refused', () => {
    const tp = reading(60, 240, true, 2000);
    beginSeek(tp, 150, 3000);
    failSeek(tp);
    expect(shownSec(tp, true, 5000)).toBe(63);
    expect(tp.seekAt).toBe(0);
    // A second refusal with nothing to undo changes nothing.
    failSeek(tp);
    expect(shownSec(tp, true, 5000)).toBe(63);
  });

  it('undoes to the clock before the FIRST of several quick jumps', () => {
    const tp = reading(60, 240, true, 2000);
    beginSeek(tp, 70, 3000);
    beginSeek(tp, 80, 3200);
    failSeek(tp);
    expect(shownSec(tp, false, 3200)).toBe(60);
  });

  it('forgets a jump on a track change', () => {
    const tp = reading(60, 240, true, 2000);
    beginSeek(tp, 150, 3000);
    resetProgress(tp, 'loc|next', 3500);
    expect(tp.seekAt).toBe(0);
    expect(tp.seekUndo).toBe(null);
    expect(applyPositionReading(tp, { positionSec: 1, durationSec: 200, seekable: true }, 'loc|next', 4000)).toBe(true);
    expect(tp).toMatchObject({ sec: 1, dur: 200 });
  });
});

// Source-level, because the bar itself is DOM-bound.
describe('the bar in the page', () => {
  it('is a slider only behind the gate, and plain again without it', () => {
    const paint = srcOf('paintSeekable');
    expect(paint).toContain('const on = canSeek(trackPos);');
    expect(paint).toContain("bar.classList.toggle('seekable', on);");
    expect(paint).toContain("bar.setAttribute('role', 'slider');");
    // Everything the slider got is taken off again for radio.
    expect(paint).toMatch(/if \(!on\) \{[\s\S]*'role', 'tabindex'[\s\S]*removeAttribute\(a\)[\s\S]*return;/);
  });

  it('asks the gate again in every handler, so radio has no live handler', () => {
    const mount = srcOf('mountTrackSeek');
    expect(mount).toMatch(/'pointerdown', \(e\) => \{\s*if \(!canSeek\(trackPos\)/);
    expect(mount).toMatch(/'keydown', \(e\) => \{\s*if \(!canSeek\(trackPos\)\) return;/);
    expect(srcOf('seekTo')).toContain('!canSeek(trackPos)) return;');
  });

  it('is wired once, for the classic layout and the preview (one shared bar)', () => {
    expect(fileSource('views/music/wiring.js')).toContain('mountTrackSeek();');
    // The preview moves the classic status bar into its player instead of
    // drawing a second one, so the one wiring serves both.
    expect(fileSource('shell2/mount.js')).toContain("statusBar: byId('statusBar')");
  });

  it('gets the pointer cursor from the seekable class alone', () => {
    const css = readFileSync(new URL('./style.css', import.meta.url), 'utf8');
    expect(css).toMatch(/\.track-bar\.seekable \{[^}]*cursor: pointer;/);
    expect(css).not.toMatch(/\.track-bar \{[^}]*cursor/);
  });

  it('goes through the shared box transport, as an optional binding', () => {
    expect(srcOf('SeekTrack')).toContain("callOptionalBinding('SeekTrack', [host, port, positionSec])");
  });
});
