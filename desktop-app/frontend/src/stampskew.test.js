// v0.9.88 shipped with its two halves stamped a minute apart, and every speaker
// in the field then looked permanently out of date to its own app.
//
// The release workflow took the build stamp inside a three-leg matrix, so the
// job output was whatever leg finished last: the desktop apps were stamped
// 2026-09-26-2023 while the ARM agent they embed and push carried
// 2026-09-26-2022. Same commit. The badge compared the stamps, so it stayed lit
// however often a user updated, and a retry ran the full wait and ended in "the
// update did not take effect", which was false.
//
// The stamp is fixed at the source. This pins the other half: the binary itself
// decides, and a clock only gets a say when there is no binary to compare.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const main = readFileSync(join(here, 'main.js'), 'utf8').replace(/\r\n/g, '\n');

// The real function, with the module-level state it reads injected.
const at = main.indexOf('function boxNeedsUpdate(b) {');
const src = main.slice(at, main.indexOf('\n}\n', at) + 2);
function needsUpdate(appInfo, box) {
  // eslint-disable-next-line no-new-func
  const fn = new Function('state', 'compareVerBuild', `${src}\nreturn boxNeedsUpdate;`)(
    { appInfo },
    // The comparison as utils.js defines it, reduced to what this test needs.
    (aVer, aBuild, bVer, bBuild) => {
      if (aVer !== bVer) return aVer > bVer ? 1 : -1;
      if (String(aBuild) === String(bBuild)) return 0;
      return String(aBuild) < String(bBuild) ? -1 : 1;
    },
  );
  return fn(box);
}

const SHA = 'a'.repeat(64);

describe('whether a speaker needs an update', () => {
  it('says no when the speaker runs exactly the agent this app carries', () => {
    // The v0.9.88 case, exactly: same version, stamps a minute apart.
    const app = { version: 'v0.9.88', build: '2026-09-26-2023', agentSha256: SHA };
    const box = { kind: 'str', version: 'v0.9.88', build: '2026-09-26-2022', agentRunningSha256: SHA };
    expect(needsUpdate(app, box)).toBe(false);
  });

  it('still says yes when the speaker runs a different binary', () => {
    const app = { version: 'v0.9.88', build: '2026-09-26-2023', agentSha256: SHA };
    const box = { kind: 'str', version: 'v0.9.87', build: '2026-09-25-1715', agentRunningSha256: 'b'.repeat(64) };
    expect(needsUpdate(app, box)).toBe(true);
  });

  it('falls back to the version when the speaker cannot say which binary it runs', () => {
    // An older agent reports no hash. Nothing changes for it.
    const app = { version: 'v0.9.88', build: '2026-09-26-2023', agentSha256: SHA };
    expect(needsUpdate(app, { kind: 'str', version: 'v0.9.87', build: '2026-09-25-1715' })).toBe(true);
    expect(needsUpdate(app, { kind: 'str', version: 'v0.9.88', build: '2026-09-26-2023' })).toBe(false);
  });

  it('still offers the update when the binary landed but the box boots the old one', () => {
    // The #381 case. The disk carries exactly this build, the agent runs
    // something else. Reading the DISK hash as "current" would hide a speaker
    // that genuinely needs help, and it is why the two fields are separate.
    const app = { version: 'v0.9.88', build: '2026-09-26-2023', agentSha256: SHA };
    const box = {
      kind: 'str', version: 'v0.9.87', build: '2026-09-25-1715',
      agentBinarySha256: SHA, agentRunningSha256: 'b'.repeat(64),
    };
    expect(needsUpdate(app, box)).toBe(true);
  });

  it('trusts the on-disk hash only from an agent too old to say what it runs', () => {
    // No agentRunningSha256 at all: the field is new. The on-disk hash is then
    // the best evidence there is, and better than a stamp.
    const app = { version: 'v0.9.88', build: '2026-09-26-2023', agentSha256: SHA };
    const box = { kind: 'str', version: 'v0.9.88', build: '2026-09-26-2022', agentBinarySha256: SHA };
    expect(needsUpdate(app, box)).toBe(false);
  });
  it('never offers a downgrade', () => {
    // The speaker is ahead of the app: that is an app update, not a speaker one.
    const app = { version: 'v0.9.87', build: '2026-09-25-1715', agentSha256: SHA };
    expect(needsUpdate(app, { kind: 'str', version: 'v0.9.88', build: '2026-09-26-2022' })).toBe(false);
  });
});
