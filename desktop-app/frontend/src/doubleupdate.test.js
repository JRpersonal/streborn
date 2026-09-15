import { describe, it, expect } from 'vitest';

// A second Update click on a speaker whose update is already running.
//
// Sascha's SoundTouch 30, 2026-09-14: the first update was uploading, he
// pressed Update again, and the app showed him a failure report with a
// copyable Go error. The update itself was fine and finished a minute later on
// v0.9.82. Two things were wrong and both are pinned here.
//
// The functions are re-implemented rather than imported, the same way
// errornoise.test.js does it: main.js runs its app bootstrap at module load, so
// importing it into a test environment starts the whole application. What
// matters is the SHAPE of the two decisions, and that is what this pins.

// 1. The latch. state.otaInProgress is only raised AFTER the stick gate and the
// Wi-Fi preflight, two awaited network round-trips, so it cannot stop a second
// click that arrives inside that window. A synchronous flag set before the
// first await can.
function makeUpdater(body) {
  let busy = false;
  const refused = [];
  return {
    refused,
    async update(name) {
      if (busy) {
        refused.push(name);
        return 'refused';
      }
      busy = true;
      try {
        return await body(name);
      } finally {
        busy = false;
      }
    },
  };
}

// 2. The classification. The one-write-per-speaker guard marks its refusal with
// a stable token so the window can tell "your second start was turned away"
// apart from "the update failed". Matching the prose instead would break on the
// next rewording, and it is the prose that reached the user as an error.
const BOX_BUSY_TOKEN = 'STR_BUSY:';
const isBusyRefusal = (msg) => String(msg || '').includes(BOX_BUSY_TOKEN);

describe('a second Update click while one is already running', () => {
  it('is refused before the first awaited call, not after it', async () => {
    const started = [];
    let release;
    const gate = new Promise((r) => { release = r; });
    const u = makeUpdater(async (name) => {
      started.push(name);
      // Stands in for the stick gate and the Wi-Fi preflight: seconds of
      // network work during which the old check was still false.
      await gate;
      return 'done';
    });

    const first = u.update('ST30');
    const second = await u.update('ST30');

    expect(second).toBe('refused');
    expect(u.refused).toEqual(['ST30']);
    // Only ONE run may have reached the backend. Two is what produced the
    // duplicate start, the refusal, and the failure report.
    expect(started).toEqual(['ST30']);

    release();
    expect(await first).toBe('done');
  });

  it('lets the next click through once the first run is finished', async () => {
    const started = [];
    const u = makeUpdater(async (name) => { started.push(name); return 'done'; });
    expect(await u.update('ST30')).toBe('done');
    expect(await u.update('ST30')).toBe('done');
    expect(started).toEqual(['ST30', 'ST30']);
    expect(u.refused).toEqual([]);
  });

  it('clears the latch when the run throws, so the button is not dead afterwards', async () => {
    const u = makeUpdater(async () => { throw new Error('box unreachable'); });
    await expect(u.update('ST30')).rejects.toThrow('box unreachable');
    // A latch left set by a throw is the dead-button bug the toast exists for.
    const second = await u.update('ST30').catch((e) => String(e));
    expect(second).toContain('box unreachable');
    expect(u.refused).toEqual([]);
  });
});

describe('telling a busy refusal apart from a failed update', () => {
  it('recognises the guard refusal by its token, whatever the prose says', () => {
    // Verbatim from Sascha's failure report, with the token the guard now adds.
    expect(isBusyRefusal(
      'STR_BUSY: this speaker is already being written to (an update is running); wait for that to finish',
    )).toBe(true);
    expect(isBusyRefusal('STR_BUSY: this speaker is already being written to (an install is running)')).toBe(true);
  });

  it('does not swallow a real failure', () => {
    for (const msg of [
      'Error: HTTP preflight rejected the listener at :8888 and SSH fallback also failed',
      'box unreachable: context deadline exceeded',
      'rename /mnt/nv/streborn/streborn-armv7l.new: no such file or directory',
      '',
      null,
      undefined,
    ]) {
      expect(isBusyRefusal(msg)).toBe(false);
    }
  });
});
