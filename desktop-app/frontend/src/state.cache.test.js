import { describe, it, expect, beforeEach } from 'vitest';

// The suite runs in node, which has no localStorage. A tiny stand-in is enough:
// the cache only ever does getItem, setItem and clear.
const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
  clear: () => store.clear(),
};

import { loadCachedBoxes, saveCachedBoxes } from './state.js';

// Reported by a user whose speaker had refused every station days earlier: the
// warning was still on screen after the fault was fixed, after the speaker was
// restarted, and after the app was restarted. His speaker reported healthy; the
// state lived in this cache on his PC, which is why restarting the speaker
// could never clear it.
describe('the speaker cache', () => {
  beforeEach(() => localStorage.clear());

  it('does not carry a speaker condition across a restart', () => {
    localStorage.setItem('cachedBoxes', JSON.stringify([{
      host: '192.168.178.44', deviceID: 'DEV#1', friendlyName: 'Wohnzimmer',
      model: 'SoundTouch 30', storm1036: true, recallRefusal: true,
      boxHealth: 'wedged', conflictingMod: 'AfterTouch',
    }]));
    const [box] = loadCachedBoxes();
    expect(box.friendlyName).toBe('Wohnzimmer');
    expect(box.model).toBe('SoundTouch 30');
    expect(box.storm1036).toBeUndefined();
    expect(box.recallRefusal).toBeUndefined();
    expect(box.boxHealth).toBeUndefined();
    expect(box.conflictingMod).toBeUndefined();
  });

  it('does not write a condition into the cache either', () => {
    saveCachedBoxes([{ host: '192.168.178.44', friendlyName: 'Wohnzimmer', storm1036: true }]);
    const stored = JSON.parse(localStorage.getItem('cachedBoxes'));
    expect(stored[0].friendlyName).toBe('Wohnzimmer');
    expect(stored[0].storm1036).toBeUndefined();
  });

  it('still keeps what the list is for', () => {
    saveCachedBoxes([{ host: '192.168.178.44', deviceID: 'DEV#1', friendlyName: 'Küche', model: 'SoundTouch 10', port: 8888 }]);
    const [box] = loadCachedBoxes();
    expect(box).toMatchObject({ host: '192.168.178.44', deviceID: 'DEV#1', friendlyName: 'Küche', model: 'SoundTouch 10', port: 8888 });
  });

  it('still drops the USB gadget address', () => {
    saveCachedBoxes([{ host: '203.0.113.1', friendlyName: 'gadget' }]);
    expect(loadCachedBoxes()).toHaveLength(0);
  });
});
