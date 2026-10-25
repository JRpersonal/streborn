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

  it('is OFF by default', () => {
    useStorage(fakeStorage());
    expect(getUi2Preview()).toBe(false);
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

  it('reads anything but the exact value 1 as OFF', () => {
    for (const v of ['true', 'on', 'yes', '01', ' 1', '0', '', 'null']) {
      useStorage(fakeStorage({ 'str.ui2Preview': v }));
      expect(getUi2Preview(), JSON.stringify(v)).toBe(false);
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
