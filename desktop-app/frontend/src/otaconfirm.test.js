// What the app accepts as proof that an update took effect.
//
// This is the predicate the post-OTA poll runs every two seconds for up to six
// minutes. When it never says yes, the app reports "the update did not take
// effect" and arms the loop breaker, and on v0.9.88 it said no to eleven
// speakers that had all updated perfectly: the release stamped the app
// 2026-09-26-2023 and the agent it embeds 2026-09-26-2022, one minute apart,
// same commit. Three users wrote in on the same day, one of them with the
// journal line in it.
//
// The other direction is #381, a push that reaches the box's disk and then does
// not boot. Confirming on the ON-DISK hash would call that a success and disarm
// the loop breaker written to stop it repeating forever. So the two hashes are
// separate fields and only the running one confirms.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const main = readFileSync(join(here, 'main.js'), 'utf8').replace(/\r\n/g, '\n');

// The real closure, lifted with the four values it reads injected around it.
const at = main.indexOf('  const updated = (v) => {');
if (at < 0) throw new Error('the post-OTA confirmation predicate moved; fix this test rather than deleting it');
const src = main.slice(at, main.indexOf('\n  };\n', at) + 6);

function confirms({ appSha, appBuild, preBuild, preVersion }, v) {
  // eslint-disable-next-line no-new-func
  return new Function('appSha', 'appBuild', 'preBuild', 'preVersion',
    `${src}\nreturn updated;`)(appSha, appBuild, preBuild, preVersion)(v);
}

const APP = 'a'.repeat(64);
const OTHER = 'b'.repeat(64);

describe('what counts as a confirmed agent update', () => {
  it('confirms on the binary the speaker runs, whatever the stamps say', () => {
    // v0.9.88 exactly: same version, stamps a minute apart, nothing else to go
    // on. This is the case that cost three users an afternoon.
    expect(confirms(
      { appSha: APP, appBuild: '2026-09-26-2023', preBuild: '2026-09-26-2022', preVersion: 'v0.9.88' },
      { version: 'v0.9.88', build: '2026-09-26-2022', agentRunningSha256: APP },
    )).toBe(true);
  });

  it('does NOT confirm when the binary only reached the disk', () => {
    // #381: the disk carries this build, the agent boots the old one. The app
    // must keep waiting and then classify, not declare victory.
    expect(confirms(
      { appSha: APP, appBuild: '2026-09-26-2023', preBuild: '2026-09-25-1715', preVersion: 'v0.9.87' },
      { version: 'v0.9.87', build: '2026-09-25-1715', agentBinarySha256: APP, agentRunningSha256: OTHER },
    )).toBe(false);
  });

  it('does not confirm a speaker that came back on the old software', () => {
    expect(confirms(
      { appSha: APP, appBuild: '2026-09-26-2023', preBuild: '2026-09-25-1715', preVersion: 'v0.9.87' },
      { version: 'v0.9.87', build: '2026-09-25-1715', agentRunningSha256: OTHER },
    )).toBe(false);
  });

  it('still confirms an older agent by its build, which is all it reports', () => {
    expect(confirms(
      { appSha: APP, appBuild: '2026-09-26-2023', preBuild: '2026-09-25-1715', preVersion: 'v0.9.87' },
      { version: 'v0.9.88', build: '2026-09-26-2023' },
    )).toBe(true);
  });

  it('confirms when the version moved at all, for an agent reporting no build', () => {
    expect(confirms(
      { appSha: APP, appBuild: '', preBuild: '', preVersion: 'v0.9.87' },
      { version: 'v0.9.88' },
    )).toBe(true);
  });

  it('says no to nothing at all rather than throwing', () => {
    expect(confirms({ appSha: APP, appBuild: 'x', preBuild: 'y', preVersion: 'z' }, null)).toBe(false);
  });
});
