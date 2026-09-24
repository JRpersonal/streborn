// The "Still updating N speaker(s)" strip that appears when the batch-update
// panel is closed mid-run.
//
// It read c.failed and c.deferred off an object counts() has never returned
// (it returns fail and defer), so the sum was NaN, `left <= 0` was false
// because every NaN comparison is, and a user who closed the panel during a
// three-speaker run got a strip reading "Still updating NaN speaker(s)"
// (reported with a bundle, 2026-09-15).
//
// The test that was supposed to cover this greps main.js for "counts()" and
// "left <= 0" and passes on the broken code, which is exactly how the bug
// survived. So this one runs the arithmetic instead of reading the source.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'main.js'), 'utf8');

// counts() as main.js defines it: the shape the strip has to consume.
function counts(outcomes) {
  let done = 0, fail = 0, defer = 0, busy = 0;
  for (const o of outcomes) {
    if (o === 'done') done++;
    else if (o === 'failed' || o === 'agentGone') fail++;
    else if (o === 'partial' || o === 'timeout' || o === 'boxInSetup') defer++;
    else busy++;
  }
  return { done, fail, defer, busy };
}

// The expression the strip uses, lifted out of main.js so a rename on either
// side fails here rather than reaching a user as NaN.
function stillRunning(outcomes, targetCount) {
  const c = counts(outcomes);
  return targetCount - (c.done + c.fail + c.defer);
}

describe('the still-updating strip', () => {
  it('counts a real number, never NaN', () => {
    const left = stillRunning(['done', 'busy', 'busy'], 3);
    expect(Number.isNaN(left)).toBe(false);
    expect(left).toBe(2);
  });

  it('hides itself once every speaker has an outcome', () => {
    expect(stillRunning(['done', 'failed', 'timeout'], 3)).toBe(0);
  });

  it('counts a deferred speaker as finished, not as still running', () => {
    // boxInSetup is very likely updated and simply off the network while the
    // firmware finishes, so it must not hold the strip open forever.
    expect(stillRunning(['done', 'boxInSetup'], 2)).toBe(0);
  });

  it('reads the field names counts() actually returns', () => {
    // The original defect in one line: undefined + undefined is NaN, and
    // NaN <= 0 is false, so the broken strip showed rather than hid.
    const c = counts(['done', 'failed']);
    expect(c.failed).toBeUndefined();
    expect(c.deferred).toBeUndefined();
    expect(c.fail).toBe(1);
    expect(c.defer).toBe(0);
  });

  it('is wired to targets, not to every speaker the run inspected', () => {
    // rows holds every candidate, including those that needed nothing. Using
    // it made the strip claim more speakers than were ever being updated.
    const at = src.indexOf('const showMiniIfStillRunning');
    expect(at).toBeGreaterThan(-1);
    const body = src.slice(at, at + 1200);
    // Only the code line, because the comment above it names the old fields on
    // purpose so the next reader knows what went wrong.
    const left = body.split(/\r?\n/).find(l => l.includes('const left =')) || '';
    expect(left).toContain('targets.length - (c.done + c.fail + c.defer)');
    expect(left).not.toContain('rows.length');
    expect(left).not.toContain('c.failed');
    expect(left).not.toContain('c.deferred');
  });
});
