import { describe, it, expect } from 'vitest';
import { sshBannerShow } from './sshbanner.js';

describe('sshBannerShow', () => {
  it('warns about a speaker whose SSH is open because a stick is in', () => {
    expect(sshBannerShow({ reachable: true, status: { sshOpen: true }, dismissed: false })).toBe(true);
  });

  it('stays down on the steady state after a clean install', () => {
    // What all five of the maintainer's speakers answer: mounted:false and no
    // sshOpen field at all.
    expect(sshBannerShow({ reachable: true, status: { mounted: false }, dismissed: false })).toBe(false);
  });

  it('stays down when SSH is deliberately kept open', () => {
    expect(sshBannerShow({
      reachable: true,
      status: { sshOpen: true, sshPersistent: true },
      dismissed: false,
    })).toBe(false);
  });

  it('stays down once the reminder has been dismissed for this speaker', () => {
    expect(sshBannerShow({ reachable: true, status: { sshOpen: true }, dismissed: true })).toBe(false);
  });

  it('comes down when the speaker cannot be asked', () => {
    // The bug of 2026-10-02: an unreachable or non-ok answer used to leave the
    // previous verdict on screen, and the banner says "this speaker", so it
    // became a false statement about whichever speaker was selected.
    expect(sshBannerShow({ reachable: false, status: null, dismissed: false })).toBe(false);
    expect(sshBannerShow({ reachable: false, status: { sshOpen: true }, dismissed: false })).toBe(false);
  });

  it('comes down for an answer that is not a status', () => {
    // An HTML error page parsed to nothing, or an agent too old to answer.
    expect(sshBannerShow({ reachable: true, status: undefined, dismissed: false })).toBe(false);
  });
});
