import { describe, it, expect, beforeEach } from 'vitest';
import {
  PAIR_NAME_MEMORY_KEY, rememberPairName, rememberedPairName, forgetRememberedPairNamesFor,
} from './pairnamememory.js';

// A minimal localStorage: vitest runs in node, without a DOM.
const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => { store.set(k, String(v)); },
  removeItem: (k) => { store.delete(k); },
  clear: () => store.clear(),
};

const pair = (a, b) => ({ members: [{ deviceID: a }, { deviceID: b }] });

describe('remembered stereo-pair name (#1683)', () => {
  beforeEach(() => localStorage.clear());

  it('suggests nothing until a name was typed', () => {
    expect(rememberedPairName(pair('aa', 'bb'))).toBe('');
  });

  it('suggests the name typed for these two speakers, in either order', () => {
    rememberPairName(pair('aa', 'bb'), '  Living room  ');
    expect(rememberedPairName(pair('AA', 'BB'))).toBe('Living room');
    expect(rememberedPairName(pair('bb', 'aa'))).toBe('Living room');
  });

  it('falls back to the name typed last for any pair', () => {
    rememberPairName(pair('aa', 'bb'), 'Living room');
    rememberPairName(pair('cc', 'dd'), 'Terrace');
    expect(rememberedPairName(pair('ee', 'ff'))).toBe('Terrace');
    // The pair's own name wins over the last one typed elsewhere.
    expect(rememberedPairName(pair('aa', 'bb'))).toBe('Living room');
  });

  it('ignores a blank name: it means "keep the name", not "forget it"', () => {
    rememberPairName(pair('aa', 'bb'), 'Living room');
    rememberPairName(pair('aa', 'bb'), '   ');
    expect(rememberedPairName(pair('aa', 'bb'))).toBe('Living room');
  });

  it('survives damaged or blocked storage', () => {
    localStorage.setItem(PAIR_NAME_MEMORY_KEY, '{not json');
    expect(rememberedPairName(pair('aa', 'bb'))).toBe('');
    const saved = globalThis.localStorage;
    globalThis.localStorage = { getItem() { throw new Error('blocked'); }, setItem() { throw new Error('blocked'); } };
    try {
      expect(() => rememberPairName(pair('aa', 'bb'), 'X')).not.toThrow();
      expect(rememberedPairName(pair('aa', 'bb'))).toBe('');
    } finally {
      globalThis.localStorage = saved;
    }
  });

  it('keeps at most twenty pairs, dropping the oldest', () => {
    for (let i = 0; i < 25; i++) rememberPairName(pair(`a${i}`, `b${i}`), `Pair ${i}`);
    const mem = JSON.parse(localStorage.getItem(PAIR_NAME_MEMORY_KEY));
    expect(Object.keys(mem.byPair)).toHaveLength(20);
    expect(rememberedPairName(pair('a24', 'b24'))).toBe('Pair 24');
    // The dropped pair gets the last name typed instead.
    expect(rememberedPairName(pair('a0', 'b0'))).toBe('Pair 24');
  });

  it('forgets the pairs of a removed speaker, keeps the last name', () => {
    rememberPairName(pair('aa', 'bb'), 'Living room');
    rememberPairName(pair('cc', 'dd'), 'Terrace');
    expect(forgetRememberedPairNamesFor('aa')).toBe(1);
    expect(rememberedPairName(pair('aa', 'bb'))).toBe('Terrace');
    expect(forgetRememberedPairNamesFor('')).toBe(0);
  });
});
