// A refusal with a reason, answered with "send me the logs".
//
// FormZone returns ok:false for two different things. One is the firmware
// shrugging: the master accepted the call and formed nothing (#70). The other
// is the app refusing on purpose because a participant is half of a live
// stereo pair (#792) - that shape starves the audio, so the box is never even
// asked, and the answer carries inPair plus a reason string.
//
// The Multi-Room view has told them apart since #792. The group chips on the
// Musik hoeren tab did not: every ok:false got multiroom.formedNone, which
// ends in "try Mirror mode, hit Refresh, or update the speakers, then send
// logs". A user with a stereo pair on his terrace did all four and sent the
// logs (mail 2026-09-25). Nothing was wrong with his speakers.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');
const dir = fileURLToPath(new URL('./i18n/bundles/', import.meta.url));
const langs = readdirSync(dir).filter((f) => f.endsWith('.json')).map((f) => f.replace('.json', ''));
const bundle = (l) => JSON.parse(readFileSync(new URL(`./i18n/bundles/${l}.json`, import.meta.url), 'utf8'));

describe('the group chips report why the form was refused', () => {
  const at = main.indexOf('// HTTP 200 with ok:false means the firmware formed NOTHING');
  const branch = main.slice(at, at + 1600);

  it('is still the branch that handles ok:false', () => {
    expect(at, 'the ok:false handler moved or went away').toBeGreaterThan(-1);
  });

  it('names the stereo pair instead of asking for logs', () => {
    expect(branch).toContain('res.inPair');
    expect(branch).toContain('multiroom.pairNotGroupable');
  });

  it('passes through any other reason the agent gave', () => {
    expect(branch).toContain('res.error');
  });

  it('keeps formedNone for the case it was written for', () => {
    // A bare ok:false with neither inPair nor a reason is still the firmware
    // forming nothing, and that message is the right one there.
    expect(branch).toContain("multiroom.formedNone");
  });

  it('has both messages in every language', () => {
    for (const l of langs) {
      expect(bundle(l)['multiroom.pairNotGroupable'], l).toBeTruthy();
      expect(bundle(l)['multiroom.formedNone'], l).toBeTruthy();
    }
  });
});
