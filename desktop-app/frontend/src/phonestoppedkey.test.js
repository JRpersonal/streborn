import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// The phone remote (internal/webui/assets/index.html) lit a preset key from
// the speaker's location alone. A stopped speaker still reports the station's
// location, so a group wake that left the main speaker stopped on a Pandora
// station lit that key on the phone while the desktop app kept it dark (#1526,
// #1629). markPlaying now has the desktop app's STOP_STATE gate. Lifted off the
// served page, the same way phonepairfollower.test.js does it.

const here = dirname(fileURLToPath(import.meta.url));
const page = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8');

function lift(signature) {
  const start = page.indexOf(signature);
  expect(start, `${signature} missing from the phone page`).toBeGreaterThan(-1);
  const end = page.indexOf('\n}\n', start);
  return page.slice(start, end + 2);
}

const src = [
  lift('function playingSlot('),
  lift('function queueSlot('),
  lift('function nativeSlot('),
  lift('function markPlaying('),
].join('\n');

const presets = [{ slot: 5, type: 'native', native: { location: 'station-location-here' } }];

function load(pending = { slot: 0, until: 0 }) {
  const lit = [];
  const env = { pendingStart: pending };
  const fns = new Function('document', 'lastPresetList', 'env', 'setLit', 'setNow', 'T',
    `var pendingStart = env.pendingStart;
     function clearPendingStart() { pendingStart = { slot: 0, until: 0 }; }
     ${src}; return { markPlaying };`)(
    { getElementById: () => ({}) },
    presets,
    env,
    (n) => lit.push(n),
    () => {},
    { cantStart: '', tapAgain: '' },
  );
  return { ...fns, lit };
}

describe('phone page preset light on a stopped speaker', () => {
  it('lights the Pandora key while it plays or is paused', () => {
    for (const st of ['PLAY_STATE', 'BUFFERING_STATE', 'PAUSE_STATE']) {
      const p = load();
      p.markPlaying('station-location-here', null, 0, st);
      expect(p.lit).toEqual([5]);
    }
  });

  it('keeps the key dark when the speaker is stopped on that station', () => {
    const p = load();
    p.markPlaying('station-location-here', null, 0, 'STOP_STATE');
    expect(p.lit).toEqual([0]);
  });

  it('still lights a key just pressed before the speaker leaves STOP_STATE', () => {
    const p = load({ slot: 5, until: Date.now() + 10000 });
    p.markPlaying('station-location-here', null, 0, 'STOP_STATE');
    expect(p.lit).toEqual([5]);
  });
});
