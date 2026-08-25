// An update whose agent half landed but whose engine cannot fit (insufficient
// NAND) must CLEAR its update intent: the supervisor toast exists for updates
// cut short, and keeping the record for a state that cannot change by itself
// turned it into a permanent per-session "unvollständig" nag (Walter's
// CineMate, 2026-08-25, green toast on every update while everything worked).
//
// Source-level, like presetart.test.js: the flow is DOM- and speaker-bound, so
// the assertion pins the one line that matters in the too-full branch.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const mainJS = readFileSync(join(here, 'main.js'), 'utf-8');

describe('update intent on a box the engine cannot fit', () => {
  it('the too-full branch clears the intent before breaking', () => {
    // Anchor on the flag assignment: the same detector regex exists in other
    // engine paths, but only the update flow's branch sets engineTooFull.
    const i = mainJS.indexOf('engineTooFull = true;');
    expect(i, 'the engineTooFull branch is gone from main.js').toBeGreaterThan(-1);
    // The clear must sit inside this branch, between the flag and its break,
    // not somewhere later in the loop.
    const branch = mainJS.slice(i, mainJS.indexOf('break;', i));
    expect(branch, 'engineTooFull no longer clears the update intent:\n' + branch)
      .toContain('ClearUpdateIntent(box.host, box.port)');
  });
});
