import { describe, it, expect } from 'vitest';
import { allSource, srcOf } from './test/source.js';

// shorty310 pulled the router in the middle of a three-speaker update and got
// the full copyable fault report, about three speakers that were fine
// (discussion #963). The app already knew: the same predicate that greys the
// speaker list out for a computer with no network was sitting unused two
// functions away, and the update path never asked it.

const src = allSource();

// The predicate, as views/settings.js defines it.
const noNetworkHere = (err) => {
  const s = String(err || '').toLowerCase();
  return s.includes('network is unreachable') || s.includes('unreachable network');
};

describe('a speaker update that failed because THIS computer lost its network', () => {
  it('recognises the errors a machine with no network produces', () => {
    for (const e of [
      'Get "http://10.0.0.60:17008/api/agent/version": dial tcp: connect: network is unreachable',
      'dial udp: unreachable network',
      'NETWORK IS UNREACHABLE',
    ]) {
      expect(noNetworkHere(e)).toBe(true);
    }
  });

  it('leaves every real speaker failure to the full report', () => {
    for (const e of [
      'upload accepted but the agent did not come up in time',
      'dial tcp 10.0.0.60:17008: connect: connection refused',
      'context deadline exceeded',
      'rename /mnt/nv/streborn/streborn-armv7l.new: no such file or directory',
      '',
      null,
    ]) {
      expect(noNetworkHere(e)).toBe(false);
    }
  });

  it('is actually consulted before the report is opened', () => {
    // Pinning the wiring, not just the predicate: the predicate existed all
    // along and that is exactly why this went unnoticed.
    const single = srcOf('runSingleBoxUpdate');
    const at = single.indexOf('showUpdateFailureReport(targetBox');
    expect(at).toBeGreaterThan(-1);
    const before = single.slice(Math.max(0, at - 600), at);
    expect(before).toContain('noNetworkHere(msg)');
  });

  it('guards every path that opens the report, not only the single-speaker one', () => {
    // The first version of this fix guarded runSingleBoxUpdate alone, and the
    // reporter had used the batch: his own report header read
    // "failed at: update-all". A test that only pinned the single-speaker
    // wiring is what let that through, so every call site is checked here.
    const batch = srcOf('runUpdateAllBoxes');
    const sites = ['update-all-engine', 'update-all'];
    for (const phase of sites) {
      const at = batch.indexOf("showUpdateFailureReport(b, '" + phase + "'");
      expect(at).toBeGreaterThan(-1);
      const before = batch.slice(Math.max(0, at - 400), at);
      expect(before).toContain('noNetworkHere(');
      expect(before).toContain('showUpdateNoNetworkNotice()');
    }
  });

  it('shows a notice rather than the copyable report', () => {
    expect(src).toContain('function showUpdateNoNetworkNotice()');
    const body = srcOf('showUpdateNoNetworkNotice');
    expect(body).toContain('settingsView.noNetworkTitle');
    expect(body).toContain('settingsView.noNetworkHelp');
    // It must not reach for the report it exists to replace.
    expect(body).not.toContain('showUpdateFailureReport');
  });
});
