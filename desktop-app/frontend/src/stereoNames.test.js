import { describe, it, expect, beforeEach, vi } from 'vitest';

// The Wails bindings do not exist in node. The store read is driven per test
// through storedNames.
const storedNames = new Map();
vi.mock('./api.js', () => ({
  GetStereoPairName: async (key) => storedNames.get(key) || '',
  SetStereoPairName: async () => {},
  PushStereoPairNameToBox: async () => {},
}));

import {
  pairNameNeedsPush, healPairNames, storedPairName, resetHealedPairNames, BOX_DEFAULT_PAIR_NAME,
} from './stereoNames.js';

const A = 'AAAAAAAAAAAA';
const B = 'BBBBBBBBBBBB';
const KEY = `${A}+${B}`;
const boxes = [
  { deviceID: A, host: '192.0.2.10', port: 8888 },
  { deviceID: B, host: '192.0.2.11', port: 8888 },
];
const livePair = (name) => ({
  id: 'g', master: A, name,
  members: [{ deviceID: A, role: 'LEFT' }, { deviceID: B, role: 'RIGHT' }],
});

beforeEach(() => {
  storedNames.clear();
  resetHealedPairNames();
});

describe('pairNameNeedsPush', () => {
  it('replaces the agent default and a blank name with the stored one', () => {
    expect(pairNameNeedsPush(BOX_DEFAULT_PAIR_NAME, 'Grace pair')).toBe(true);
    expect(pairNameNeedsPush('', 'Grace pair')).toBe(true);
  });
  it('leaves a matching name, a Bose-app rename, and a pair with no stored name alone', () => {
    expect(pairNameNeedsPush('Grace pair', 'Grace pair')).toBe(false);
    expect(pairNameNeedsPush('Living room', 'Grace pair')).toBe(false);
    expect(pairNameNeedsPush(BOX_DEFAULT_PAIR_NAME, '')).toBe(false);
  });
});

describe('storedPairName', () => {
  it('waits for the store instead of answering empty while the read is in flight', async () => {
    storedNames.set(KEY, 'Grace pair');
    // Member order does not matter: the key is the sorted set.
    expect(await storedPairName({ members: [{ deviceID: B }, { deviceID: A }] })).toBe('Grace pair');
  });
});

describe('healPairNames', () => {
  // #1077 issue 5: a pair undone and formed again with the name field empty
  // carried "Stereo pair" on the speakers while the app still said "Grace pair".
  it('writes the stored name onto both members of a pair that came back as "Stereo pair"', async () => {
    storedNames.set(KEY, 'Grace pair');
    const push = vi.fn(async () => {});
    await healPairNames([livePair('Stereo pair')], boxes, push);
    expect(push).toHaveBeenCalledTimes(2);
    expect(push).toHaveBeenCalledWith('192.0.2.10', 8888, 'Grace pair');
    expect(push).toHaveBeenCalledWith('192.0.2.11', 8888, 'Grace pair');
  });

  it('asks only once per session, not on every zone poll', async () => {
    storedNames.set(KEY, 'Grace pair');
    const push = vi.fn(async () => {});
    await healPairNames([livePair('Stereo pair')], boxes, push);
    await healPairNames([livePair('Stereo pair')], boxes, push);
    expect(push).toHaveBeenCalledTimes(2);
  });

  it('does nothing in the steady state or for a pair renamed in the Bose app', async () => {
    storedNames.set(KEY, 'Grace pair');
    const push = vi.fn(async () => {});
    await healPairNames([livePair('Grace pair')], boxes, push);
    await healPairNames([livePair('Living room')], boxes, push);
    expect(push).not.toHaveBeenCalled();
  });

  it('waits until both members are discovered', async () => {
    storedNames.set(KEY, 'Grace pair');
    const push = vi.fn(async () => {});
    await healPairNames([livePair('Stereo pair')], boxes.slice(0, 1), push);
    expect(push).not.toHaveBeenCalled();
    await healPairNames([livePair('Stereo pair')], boxes, push);
    expect(push).toHaveBeenCalledTimes(2);
  });
});
