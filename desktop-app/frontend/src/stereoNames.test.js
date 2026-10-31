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
  pairDisplayName, setPairName,
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
  it('replaces only a blank name with the stored one', () => {
    expect(pairNameNeedsPush('', 'Grace pair')).toBe(true);
  });
  // #1579: a pair formed again without a name carries the default, and the
  // name of the pair that was undone must not come back over it.
  it('leaves the agent default alone', () => {
    expect(pairNameNeedsPush(BOX_DEFAULT_PAIR_NAME, 'Grace pair')).toBe(false);
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
  it('writes the stored name onto both members of a pair whose speakers carry none', async () => {
    storedNames.set(KEY, 'Grace pair');
    const push = vi.fn(async () => {});
    await healPairNames([livePair('')], boxes, push);
    expect(push).toHaveBeenCalledTimes(2);
    expect(push).toHaveBeenCalledWith('192.0.2.10', 8888, 'Grace pair');
    expect(push).toHaveBeenCalledWith('192.0.2.11', 8888, 'Grace pair');
  });

  it('asks only once per session, not on every zone poll', async () => {
    storedNames.set(KEY, 'Grace pair');
    const push = vi.fn(async () => {});
    await healPairNames([livePair('')], boxes, push);
    await healPairNames([livePair('')], boxes, push);
    expect(push).toHaveBeenCalledTimes(2);
  });

  it('does nothing in the steady state, for a pair renamed in the Bose app, or for a pair formed again without a name', async () => {
    storedNames.set(KEY, 'Grace pair');
    const push = vi.fn(async () => {});
    await healPairNames([livePair('Grace pair')], boxes, push);
    await healPairNames([livePair('Stereo pair')], boxes, push);
    await healPairNames([livePair('Living room')], boxes, push);
    expect(push).not.toHaveBeenCalled();
  });

  it('waits until both members are discovered', async () => {
    storedNames.set(KEY, 'Grace pair');
    const push = vi.fn(async () => {});
    await healPairNames([livePair('')], boxes.slice(0, 1), push);
    expect(push).not.toHaveBeenCalled();
    await healPairNames([livePair('')], boxes, push);
    expect(push).toHaveBeenCalledTimes(2);
  });
});

describe('pairDisplayName (#1579)', () => {
  const settle = () => new Promise(r => setTimeout(r, 0));

  it('shows the name the speakers carry over an old stored one', async () => {
    storedNames.set(KEY, 'Old pair');
    pairDisplayName(livePair('Stereo pair'));
    await settle();
    expect(pairDisplayName(livePair('Named in STR Remote'))).toBe('Named in STR Remote');
  });

  it('falls back to the stored name while the speakers carry the default', async () => {
    storedNames.set(KEY, 'Grace pair');
    pairDisplayName(livePair('Stereo pair'));
    await settle();
    expect(pairDisplayName(livePair('Stereo pair'))).toBe('Grace pair');
  });

  it('shows a rename made here until the speakers report it back, then follows the speakers', async () => {
    await setPairName(livePair('Old'), 'New');
    expect(pairDisplayName(livePair('Old'))).toBe('New');
    expect(pairDisplayName(livePair('New'))).toBe('New');
    expect(pairDisplayName(livePair('Renamed elsewhere'))).toBe('Renamed elsewhere');
  });

  it('forgets the name once it is cleared, as an undo does', async () => {
    await setPairName(livePair(''), 'Grace pair');
    await setPairName(livePair(''), '');
    expect(pairDisplayName(livePair('Stereo pair'))).toBe('');
  });
});
