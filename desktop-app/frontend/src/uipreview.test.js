import { describe, it, expect, afterEach, vi } from 'vitest';
import { canKeepUi2Preview, getUi2Preview, setUi2Preview } from './uipreview.js';

function fakeStorage(init = {}) {
  const m = new Map(Object.entries(init));
  return {
    getItem: (k) => (m.has(k) ? m.get(k) : null),
    setItem: (k, v) => { m.set(k, String(v)); },
    removeItem: (k) => { m.delete(k); },
    _map: m,
  };
}

// Node 22+ ships its own global localStorage (webstorage), defined as an
// accessor on globalThis: a plain assignment or delete does not replace it,
// and the module would then write to Node's store instead of the fake.
// vi.stubGlobal redefines the property, so the fake wins on every Node.
function useStorage(s) { vi.stubGlobal('localStorage', s); }

describe('UI 2.0 preview switch', () => {
  afterEach(() => { vi.unstubAllGlobals(); });

  it('is ON by default since phase 6 (#1290)', () => {
    useStorage(fakeStorage());
    expect(getUi2Preview()).toBe(true);
  });

  it('round-trips on and off', () => {
    const s = fakeStorage();
    useStorage(s);
    setUi2Preview(true);
    expect(s._map.get('str.ui2Preview')).toBe('1');
    expect(getUi2Preview()).toBe(true);
    setUi2Preview(false);
    expect(getUi2Preview()).toBe(false);
  });

  it('reads only the exact value 0 as classic: somebody who picked it keeps it', () => {
    useStorage(fakeStorage({ 'str.ui2Preview': '0' }));
    expect(getUi2Preview()).toBe(false);
    for (const v of ['1', 'true', 'off', 'no', '00', ' 0', '', 'null']) {
      useStorage(fakeStorage({ 'str.ui2Preview': v }));
      expect(getUi2Preview(), JSON.stringify(v)).toBe(true);
    }
  });

  it('works without localStorage, keeping the choice in memory', () => {
    useStorage(undefined);
    setUi2Preview(true);
    expect(getUi2Preview()).toBe(true);
    setUi2Preview(false);
    expect(getUi2Preview()).toBe(false);
  });

  it('works when localStorage throws on every call', () => {
    useStorage({
      getItem: () => { throw new Error('denied'); },
      setItem: () => { throw new Error('denied'); },
    });
    expect(() => setUi2Preview(true)).not.toThrow();
    expect(getUi2Preview()).toBe(true);
    setUi2Preview(false);
    expect(getUi2Preview()).toBe(false);
  });

  it('can keep the choice only when the store answers', () => {
    const s = fakeStorage();
    useStorage(s);
    expect(canKeepUi2Preview()).toBe(true);
    expect(setUi2Preview(true)).toBe(true);
    expect(s._map.size).toBe(1);
    useStorage(undefined);
    expect(canKeepUi2Preview()).toBe(false);
    expect(setUi2Preview(true)).toBe(false);
  });

  it('cannot keep the choice when localStorage throws', () => {
    useStorage({
      getItem: () => { throw new Error('denied'); },
      setItem: () => { throw new Error('denied'); },
    });
    expect(canKeepUi2Preview()).toBe(false);
    expect(setUi2Preview(true)).toBe(false);
  });
});
