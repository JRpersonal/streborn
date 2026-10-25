import { describe, it, expect } from 'vitest';
import { roomItems } from './rooms.js';
import { setLocale } from '../i18n/index.js';

// The assertions compare with the English bundle, whatever the machine's language.
setLocale('en');

const A = { host: '192.0.2.10', port: 8888, deviceID: 'AAA', kind: 'STR', friendlyName: 'Kitchen' };
const B = { host: '192.0.2.11', port: 8888, deviceID: 'BBB', kind: 'STR', friendlyName: 'Office' };
const C = { host: '192.0.2.12', port: 8888, deviceID: 'CCC', kind: 'STR', friendlyName: 'Hall' };
const pair = { id: 'p1', master: 'BBB', members: [{ deviceID: 'AAA', role: 'LEFT' }, { deviceID: 'BBB', role: 'RIGHT' }] };

describe('speaker rows', () => {
  it('gives offline, stock and STR-not-running speakers their pills', () => {
    const items = roomItems([
      { ...A, offline: true },
      { ...B, kind: 'stock' },
      { ...C, kind: 'stock', strNotRunning: true },
    ], { offlineTitle: () => 'Offline, last seen 5 min ago' });
    expect(items.map((i) => i.pill)).toEqual(['notReachable', 'boseOriginal', 'strNotRunning']);
    expect(items.map((i) => i.kind)).toEqual(['str', 'stock', 'stock']);
    expect(items[0].offline).toBe(true);
    expect(items[0].title).toBe('Offline, last seen 5 min ago');
  });

  it('collapses a live stereo pair into one row led by its master', () => {
    const items = roomItems([A, B, C], { stereoPairs: [pair], currentBox: A });
    expect(items).toHaveLength(2);
    expect(items[0].host).toBe(B.host);
    expect(items[0].kind).toBe('pair');
    expect(items[0].hosts).toEqual([A.host, B.host]);
    expect(items[0].name).toBe('Kitchen + Office');
    expect(items[0].pill).toBe('stereoPair');
    // Selecting either half marks the pair row as current.
    expect(items[0].isCurrent).toBe(true);
    const named = roomItems([A, B], { stereoPairs: [pair], pairName: () => 'Living pair' });
    expect(named[0].name).toBe('Living pair');
  });

  it('selects the master half on a two-chip chassis whose firmware id differs', () => {
    // The master is the RIGHT half and is known to discovery only by its IP.
    const fwPair = {
      id: 'p2', master: 'FW-B',
      members: [{ deviceID: 'FW-A', ip: A.host, role: 'LEFT' }, { deviceID: 'FW-B', ip: B.host, role: 'RIGHT' }],
    };
    const items = roomItems([A, B], { stereoPairs: [fwPair] });
    expect(items).toHaveLength(1);
    expect(items[0].host).toBe(B.host);
  });

  it('selects the half that answers when the pair master is offline', () => {
    const items = roomItems([A, { ...B, offline: true }], { stereoPairs: [pair] });
    expect(items).toHaveLength(1);
    expect(items[0].host).toBe(A.host);
    expect(items[0].offline).toBe(false);
  });

  it('marks a lone pair half as in a pair, so it is never offered for a group', () => {
    // B dropped out of discovery: the pair is not collapsed, but A is still half of it.
    const items = roomItems([A, C], { stereoPairs: [pair] });
    expect(items).toHaveLength(2);
    expect(items[0].kind).toBe('str');
    expect(items[0].inPair).toBe(true);
    expect(items[1].inPair).toBe(false);
  });

  it('carries the chips next to the pill', () => {
    const [item] = roomItems([A], {
      currentBox: A,
      nowPlayState: 'PLAY_STATE',
      needsUpdate: () => true,
      cannotReachInternet: () => true,
    });
    expect(item.pill).toBe('playing');
    expect(item.playing).toBe(true);
    expect(item.chips).toEqual(['update', 'noInternet']);
  });
});
