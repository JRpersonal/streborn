import { describe, expect, it } from 'vitest';
import { isPlaying, isStr, labelOf, labelVia, strBoxes, up } from './boxes.js';

describe('up', () => {
  it('upper-cases an identifier and tolerates a missing one', () => {
    expect(up('a0f6fd12ab34')).toBe('A0F6FD12AB34');
    expect(up(undefined)).toBe('');
    expect(up(null)).toBe('');
    expect(up(0)).toBe('');
    expect(up(12)).toBe('12');
  });
});

describe('isStr / strBoxes', () => {
  it('tells an STR speaker from a Bose original', () => {
    expect(isStr({ kind: 'str' })).toBe(true);
    expect(isStr({})).toBe(true);
    expect(isStr({ kind: 'stock' })).toBe(false);
    expect(isStr(null)).toBe(false);
    expect(isStr(undefined)).toBe(false);
  });

  it('filters a list, skipping holes and a missing list', () => {
    const a = { kind: 'str', host: '192.0.2.1' };
    const b = { kind: 'stock', host: '192.0.2.2' };
    const c = { host: '192.0.2.3' };
    expect(strBoxes([a, null, b, undefined, c])).toEqual([a, c]);
    expect(strBoxes(null)).toEqual([]);
    expect(strBoxes(undefined)).toEqual([]);
  });

  it('returns a new list and keeps the order', () => {
    const list = [{ host: '192.0.2.2' }, { host: '192.0.2.1' }];
    const out = strBoxes(list);
    expect(out).not.toBe(list);
    expect(out.map((x) => x.host)).toEqual(['192.0.2.2', '192.0.2.1']);
  });
});

describe('labelOf / labelVia', () => {
  it('prefers the friendly name, then the agent name, then the host', () => {
    expect(labelOf({ friendlyName: 'Kitchen', name: 'str-192.0.2.1', host: '192.0.2.1' })).toBe('Kitchen');
    expect(labelOf({ friendlyName: '', name: 'str-192.0.2.1', host: '192.0.2.1' })).toBe('str-192.0.2.1');
    expect(labelOf({ host: '192.0.2.1' })).toBe('192.0.2.1');
    expect(labelOf({})).toBe('');
    expect(labelOf(null)).toBe('');
  });

  it('asks the injected label function first', () => {
    const box = { friendlyName: 'Kitchen', host: '192.0.2.1' };
    expect(labelVia(() => 'Pair name', box)).toBe('Pair name');
    expect(labelVia(() => '', box)).toBe('Kitchen');
    expect(labelVia(undefined, box)).toBe('Kitchen');
    expect(labelVia(null, null)).toBe('');
  });
});

describe('isPlaying', () => {
  it('is true for playing and buffering only', () => {
    expect(isPlaying('PLAY_STATE')).toBe(true);
    expect(isPlaying('BUFFERING_STATE')).toBe(true);
    expect(isPlaying('PAUSE_STATE')).toBe(false);
    expect(isPlaying('STOP_STATE')).toBe(false);
    expect(isPlaying('')).toBe(false);
    expect(isPlaying(undefined)).toBe(false);
  });
});
