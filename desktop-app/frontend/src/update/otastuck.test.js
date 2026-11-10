import { describe, it, expect, beforeEach } from 'vitest';

// The suite runs in node, which has no localStorage.
const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
  clear: () => store.clear(),
};

import { state } from '../state.js';
import { otaStuck, noteOTAResult } from './otastuck.js';

const BOX = { host: '192.0.2.10', deviceId: 'device-id-here' };
const miss = (verdict) => ({ confirmed: false, outcome: 'timeout', verdict });

describe('the update loop breaker', () => {
  beforeEach(() => {
    store.clear();
    state.appInfo = { build: '2026-11-10-1200' };
  });

  it('gives up at once on an update that landed and does not start', () => {
    noteOTAResult(BOX, miss('landed-not-running'));
    expect(otaStuck(BOX)).not.toBeNull();
  });

  it('offers one more push after a failed write to the speaker storage', () => {
    noteOTAResult(BOX, miss('swap-failed'));
    expect(otaStuck(BOX)).toBeNull();
    noteOTAResult(BOX, miss('swap-failed'));
    expect(otaStuck(BOX)).not.toBeNull();
  });

  it('gives up after two unconfirmed runs of any other kind', () => {
    noteOTAResult(BOX, miss('not-landed'));
    expect(otaStuck(BOX)).toBeNull();
    noteOTAResult(BOX, miss('not-landed'));
    expect(otaStuck(BOX)).not.toBeNull();
  });

  it('forgets everything once an update is confirmed', () => {
    noteOTAResult(BOX, miss('swap-failed'));
    noteOTAResult(BOX, miss('swap-failed'));
    noteOTAResult(BOX, { confirmed: true });
    expect(otaStuck(BOX)).toBeNull();
  });

  it('starts over with a new app build', () => {
    noteOTAResult(BOX, miss('landed-not-running'));
    state.appInfo = { build: '2026-11-11-0900' };
    expect(otaStuck(BOX)).toBeNull();
  });
});
