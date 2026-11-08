// The restart button behind the sign-in warning re-enabled the moment the
// speaker took the restart, and a second press 79 s later landed on a speaker
// mid-boot and got firewall advice (#1768). These pin the wait: off from the
// accepted request until the speaker went away and came back, or the window
// ran out.
import { describe, it, expect } from 'vitest';
import { createRestartTracker, localizeStillRestarting, RESTART_WAIT_MS, STILL_RESTARTING_EN } from './restartwait.js';

function tracker() {
  let t = 1_000_000;
  const tr = createRestartTracker(() => t);
  return { tr, advance: (ms) => { t += ms; } };
}

const H = '192.0.2.30';
const up = { host: H };
const down = { host: H, unreachable: true };

describe('restart wait', () => {
  it('is pending only after an accepted request', () => {
    const { tr } = tracker();
    expect(tr.isPending(H)).toBe(false);
    tr.requested(H);
    expect(tr.isPending(H)).toBe(true);
    expect(tr.isPending('192.0.2.31')).toBe(false);
  });

  it('stays pending while the speaker still answers from before its restart', () => {
    const { tr, advance } = tracker();
    tr.requested(H);
    advance(79_000);
    tr.observe([up]);
    expect(tr.isPending(H)).toBe(true);
  });

  it('ends once the speaker went away and answered again', () => {
    const { tr, advance } = tracker();
    tr.requested(H);
    advance(30_000);
    tr.observe([down]);
    expect(tr.isPending(H)).toBe(true);
    advance(60_000);
    tr.observe([{ host: H, offline: true }]);
    expect(tr.isPending(H)).toBe(true);
    advance(30_000);
    tr.observe([up]);
    expect(tr.isPending(H)).toBe(false);
  });

  it('ends when the window runs out, even if no refresh saw it go', () => {
    const { tr, advance } = tracker();
    tr.requested(H);
    advance(RESTART_WAIT_MS - 1);
    expect(tr.isPending(H)).toBe(true);
    expect(tr.remainingMs(H)).toBe(1);
    advance(1);
    expect(tr.isPending(H)).toBe(false);
    expect(tr.remainingMs(H)).toBe(0);
  });

  it('changes its signature when a wait starts and ends', () => {
    const { tr, advance } = tracker();
    const before = tr.sig();
    tr.requested(H);
    expect(tr.sig()).not.toBe(before);
    advance(RESTART_WAIT_MS);
    expect(tr.sig()).toBe(before);
  });
});

describe('localizeStillRestarting', () => {
  it('swaps the English paragraph for the localized one', () => {
    const raw = `dial tcp 192.0.2.30:17008: connection refused\n\n${STILL_RESTARTING_EN}`;
    expect(localizeStillRestarting(raw, 'Küche startet noch neu, gib ihm ein paar Minuten.'))
      .toBe('dial tcp 192.0.2.30:17008: connection refused\n\nKüche startet noch neu, gib ihm ein paar Minuten.');
  });

  it('leaves any other error alone', () => {
    expect(localizeStillRestarting('status 500: busy', 'x')).toBe('status 500: busy');
  });
});
