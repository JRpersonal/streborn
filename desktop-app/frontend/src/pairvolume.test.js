// One volume control for a stereo pair (idea #1093): which speakers the slider
// moves, what it reads, and how a move keeps the halves' spacing.
import { describe, it, expect } from 'vitest';
import {
  pairVolumeTarget,
  pairShownLevel,
  createPairVolume,
  notePairRead,
  pairLevelsFor,
  pairAnchored,
  makePerHostThrottle,
} from './pairVolume.js';

const L = { host: '192.0.2.19', port: 8888, deviceID: 'AAA' };
const R = { host: '192.0.2.32', port: 8888, deviceID: 'BBB' };
const OTHER = { host: '192.0.2.55', port: 8888, deviceID: 'CCC' };
const boxes = [R, L, OTHER];
const pairLive = {
  AAA: {
    stereo: {
      id: 'str-grp-AAA', masterDeviceID: 'AAA',
      members: [
        { deviceID: 'BBB', ip: '192.0.2.32', role: 'RIGHT' },
        { deviceID: 'AAA', ip: '192.0.2.19', role: 'LEFT' },
      ],
    },
  },
};

describe('pairVolumeTarget', () => {
  it('returns both halves, left first, from either half', () => {
    for (const sel of [L, R]) {
      const got = pairVolumeTarget(sel, pairLive, boxes);
      expect(got).not.toBeNull();
      expect(got.halves.map(b => b.host)).toEqual([L.host, R.host]);
      expect(got.key).toBe('AAA+BBB');
    }
  });

  it('is null for a speaker outside the pair, so it keeps its own slider', () => {
    expect(pairVolumeTarget(OTHER, pairLive, boxes)).toBeNull();
  });

  it('is null for an ordinary multiroom group, which keeps the group panel', () => {
    const groupLive = {
      AAA: { master: 'AAA', members: [{ deviceID: 'AAA' }, { deviceID: 'CCC' }] },
    };
    expect(pairVolumeTarget(L, groupLive, boxes)).toBeNull();
  });

  it('is null when only one half is on the network', () => {
    expect(pairVolumeTarget(L, pairLive, [L, OTHER])).toBeNull();
  });

  it('is null with nothing selected or no zone data', () => {
    expect(pairVolumeTarget(null, pairLive, boxes)).toBeNull();
    expect(pairVolumeTarget(L, null, boxes)).toBeNull();
  });
});

describe('pairShownLevel', () => {
  it('reads the louder half, like the phone pair scope', () => {
    expect(pairShownLevel({ a: 40, b: 36 })).toBe(40);
  });
  it('ignores halves that did not answer', () => {
    expect(pairShownLevel({ a: -1, b: 22 })).toBe(22);
    expect(pairShownLevel({})).toBeNull();
  });
});

describe('pair slider moves', () => {
  const hosts = [L.host, R.host];

  it('shifts both halves by the same offset from the read levels', () => {
    const st = createPairVolume();
    expect(notePairRead(st, 'AAA+BBB', { [L.host]: 40, [R.host]: 36 })).toBe(40);
    expect(pairLevelsFor(st, 'AAA+BBB', hosts, 30)).toEqual({ [L.host]: 30, [R.host]: 26 });
    expect(pairLevelsFor(st, 'AAA+BBB', hosts, 45)).toEqual({ [L.host]: 45, [R.host]: 41 });
  });

  it('gives the spacing back after a half was pinned at 0', () => {
    const st = createPairVolume();
    notePairRead(st, 'AAA+BBB', { [L.host]: 20, [R.host]: 10 });
    expect(pairLevelsFor(st, 'AAA+BBB', hosts, 5)).toEqual({ [L.host]: 5, [R.host]: 0 });
    expect(pairLevelsFor(st, 'AAA+BBB', hosts, 20)).toEqual({ [L.host]: 20, [R.host]: 10 });
  });

  it('clamps at 100', () => {
    const st = createPairVolume();
    notePairRead(st, 'AAA+BBB', { [L.host]: 90, [R.host]: 80 });
    expect(pairLevelsFor(st, 'AAA+BBB', hosts, 100)).toEqual({ [L.host]: 100, [R.host]: 90 });
  });

  it('leaves a half that did not answer where it is', () => {
    const st = createPairVolume();
    notePairRead(st, 'AAA+BBB', { [L.host]: 30, [R.host]: -1 });
    expect(pairLevelsFor(st, 'AAA+BBB', hosts, 25)).toEqual({ [L.host]: 25 });
  });

  it('refuses to move a pair whose levels were never read, so its balance cannot be flattened', () => {
    const st = createPairVolume();
    expect(pairAnchored(st, 'AAA+BBB')).toBe(false);
    expect(pairLevelsFor(st, 'AAA+BBB', hosts, 27)).toBeNull();
    notePairRead(st, 'AAA+BBB', { [L.host]: 34, [R.host]: 28 });
    expect(pairAnchored(st, 'AAA+BBB')).toBe(true);
    expect(pairLevelsFor(st, 'AAA+BBB', hosts, 33)).toEqual({ [L.host]: 33, [R.host]: 27 });
  });

  it('does not carry one pair\'s baseline over to another pair', () => {
    const st = createPairVolume();
    notePairRead(st, 'AAA+BBB', { [L.host]: 40, [R.host]: 10 });
    expect(pairLevelsFor(st, 'CCC+DDD', ['x', 'y'], 30)).toBeNull();
  });

  it('re-anchors on a fresh read so the next move starts from the speakers', () => {
    const st = createPairVolume();
    notePairRead(st, 'AAA+BBB', { [L.host]: 40, [R.host]: 36 });
    pairLevelsFor(st, 'AAA+BBB', hosts, 30);
    // Somebody turned the right half up on the speaker itself.
    expect(notePairRead(st, 'AAA+BBB', { [L.host]: 30, [R.host]: 34 })).toBe(34);
    expect(pairLevelsFor(st, 'AAA+BBB', hosts, 24)).toEqual({ [L.host]: 20, [R.host]: 24 });
  });
});

describe('makePerHostThrottle', () => {
  it('never lets one half drop the other half\'s pending level', async () => {
    const calls = [];
    const gates = [];
    const fn = (host, port, v) => new Promise((resolve) => { calls.push([host, v]); gates.push(resolve); });
    const send = makePerHostThrottle(fn);
    send('a', 1, 10);
    send('b', 1, 10);
    send('a', 1, 11);
    send('b', 1, 11);
    send('a', 1, 12);
    expect(calls).toEqual([['a', 10], ['b', 10]]);
    gates.splice(0).forEach(r => r());
    await new Promise(r => setTimeout(r, 0));
    // Each host sends only its latest queued level, and neither is lost.
    expect(calls).toEqual([['a', 10], ['b', 10], ['a', 12], ['b', 11]]);
  });
});
