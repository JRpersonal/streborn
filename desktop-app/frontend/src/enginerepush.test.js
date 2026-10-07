// What counts as a speaker having reached the target build, and why a re-push
// of the SAME version is not the harmless no-op it looks like.
//
// A speaker that has the Spotify engine is by definition NAND-tight: the engine
// is about 16 MB and the free space on a speaker that holds one is around 11.
// So when an update is pushed, the agent drops the engine to make it fit, and
// the app is supposed to put it back once the update is confirmed.
//
// The confirmation asked "did the version STRING move". A push of the same
// version cannot satisfy that, so the flow returned before the re-delivery step
// and the engine stayed gone. The push that was meant to change nothing
// destroyed a working Spotify install instead.
//
// That path was reached in bulk by the v0.9.88 stamp skew, which told every user
// their speakers needed updating when they did not. One household ended up with
// four of six speakers with no engine, the owner having "run it a few times on a
// couple of speakers", each run doing more damage than the last. The stamp is
// fixed, but a same-version push still happens on purpose (a rollback repair, a
// reinstall), so the destructive half is fixed here rather than left to the
// trigger being gone.
import { describe, it, expect } from 'vitest';
import { speakerReachedTarget } from './agentsettle.js';

function reached(appInfo, live, preVersion, wantEngine) {
  return speakerReachedTarget(live, preVersion, wantEngine, appInfo && appInfo.agentSha256);
}

const SHA = 'a'.repeat(64);
const OTHER = 'b'.repeat(64);
const APP = { version: 'v0.9.88', build: '2026-09-26-2023', agentSha256: SHA };

describe('whether a speaker reached the build that was pushed at it', () => {
  it('confirms a push of the SAME version by the binary that is running', () => {
    // The case that used to be unconfirmable, and therefore the case where the
    // engine was dropped and never restored.
    const live = { version: 'v0.9.88', agentRunningSha256: SHA, goLibrespot: 'present' };
    expect(reached(APP, live, 'v0.9.88', false)).toEqual({ done: true, missing: '' });
  });

  it('still reports the engine as outstanding on a same-version push', () => {
    // The agent half is done; the engine half is what the flow must go on to do.
    const live = { version: 'v0.9.88', agentRunningSha256: SHA, goLibrespot: 'missing' };
    expect(reached(APP, live, 'v0.9.88', true)).toEqual({ done: false, missing: 'engine' });
  });

  it('does not confirm a speaker that came back on a different binary', () => {
    const live = { version: 'v0.9.88', agentRunningSha256: OTHER };
    expect(reached(APP, live, 'v0.9.88', false)).toEqual({ done: false, missing: 'agent' });
  });

  it('keeps the version-string route for an agent too old to report a hash', () => {
    expect(reached(APP, { version: 'v0.9.88' }, 'v0.9.87', false)).toEqual({ done: true, missing: '' });
    expect(reached(APP, { version: 'v0.9.87' }, 'v0.9.87', false)).toEqual({ done: false, missing: 'agent' });
  });

  it('reports an unreachable speaker as unreachable, not as failed', () => {
    expect(reached(APP, null, 'v0.9.87', false)).toEqual({ done: false, missing: 'unreachable' });
  });

  it('does not need a hash from the app to work at all', () => {
    // A dev build carries an empty embed slot and therefore no hash.
    const noSha = { version: 'v0.9.88', build: 'dev' };
    expect(reached(noSha, { version: 'v0.9.88' }, 'v0.9.87', false)).toEqual({ done: true, missing: '' });
  });
});
