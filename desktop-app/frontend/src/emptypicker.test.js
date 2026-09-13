// #935: start the app with no Wi-Fi and the speaker list never recovers.
//
// Two mechanisms guard the empty list and each one pointed at the other. The
// minute timer skipped it ("the recovery burst owns the empty case") and the
// recovery burst declines it on purpose (`if (!hadBoxesBefore) return`, so an
// empty LAN is never swept every six seconds). A session that never saw a
// speaker therefore had nobody watching for one.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');

// The interval that re-probes known speakers, sliced out by its cadence.
function minuteTimer() {
  const at = main.indexOf('setInterval(async () => {');
  const end = main.indexOf('}, 60000);', at);
  expect(end, 'the minute timer is gone').toBeGreaterThan(at);
  return main.slice(at, end);
}

describe('the empty speaker picker', () => {
  it('is no longer skipped by the minute timer', () => {
    expect(minuteTimer()).not.toContain('!state.boxes.length) return');
  });

  it('gets a full sweep, since there are no known boxes to re-probe', () => {
    const t = minuteTimer();
    expect(t).toContain('!state.boxes.length');
    expect(t).toContain('discoverBoxes()');
    const emptyBranch = t.slice(t.indexOf('!state.boxes.length'));
    expect(
      emptyBranch.indexOf('discoverBoxes()'),
      'the empty branch must sweep, not call RefreshKnownBoxes'
    ).toBeLessThan(emptyBranch.indexOf('RefreshKnownBoxes') === -1 ? Infinity : emptyBranch.indexOf('RefreshKnownBoxes'));
  });

  it('still stands off while an OTA or an install is running', () => {
    const t = minuteTimer();
    expect(t).toContain('state.otaInProgress');
    expect(t).toContain('installRunActive()');
  });

  it('leaves the recovery burst refusing an empty LAN', () => {
    // The burst must keep its guard: sweeping every 6 s forever for somebody
    // who owns no speakers is what that line prevents.
    expect(main).toContain('if (!hadBoxesBefore) return;');
  });
});
