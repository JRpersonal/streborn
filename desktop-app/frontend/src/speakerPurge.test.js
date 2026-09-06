import { describe, it, expect, beforeEach, vi } from 'vitest';

// The Wails bindings do not exist in node. stereoNames.js only needs the two
// stereo-name calls; a read that never resolves keeps its key "pending".
vi.mock('./api.js', () => ({
  GetStereoPairName: () => new Promise(() => {}),
  SetStereoPairName: async () => {},
}));

// The suite runs in node, which has no localStorage. This stand-in also
// supports enumeration (length/key) like the real one, so the prefix scan for
// warnDismiss keys is exercised.
const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
  clear: () => store.clear(),
  key: (i) => [...store.keys()][i] ?? null,
  get length() { return store.size; },
};

import { purgeSpeakerLocalState, onSpeakerPurge, speakerIdentity } from './speakerPurge.js';
import { pairDisplayName, setPairName, forgetPairNamesFor } from './stereoNames.js';

const GONE = { host: '192.0.2.10', deviceID: 'DEV-GONE', port: 17008, friendlyName: 'Gone', kind: 'str' };
const STAYS = { host: '192.0.2.11', deviceID: 'DEV-STAYS', port: 8888, friendlyName: 'Stays', kind: 'str' };

function seedStorage() {
  localStorage.setItem('cachedBoxes', JSON.stringify([GONE, STAYS]));
  localStorage.setItem('lastBoxDeviceID', 'DEV-GONE');
  for (const id of ['192.0.2.10', 'DEV-GONE']) {
    localStorage.setItem('warnDismiss:' + id + ':ssh', '1');
    localStorage.setItem('warnDismiss:' + id + ':conflict', '1');
    localStorage.setItem('warnDismiss:' + id + ':nowifi', '1');
    localStorage.setItem('warnDismiss:' + id + ':future-type', '1');
    localStorage.setItem('otaStuck:' + id, JSON.stringify({ n: 3 }));
    localStorage.setItem('str.worldMapProvisioned.' + id, '1');
  }
  localStorage.setItem('warnDismiss:192.0.2.11:ssh', '1');
  localStorage.setItem('otaStuck:DEV-STAYS', '1');
  localStorage.setItem('str.worldMapProvisioned.DEV-STAYS', '1');
  // Kept on purpose: not about one speaker.
  localStorage.setItem('str.worldMapInvited', '1');
  localStorage.setItem('str.favStations', '[]');
  localStorage.setItem('locale', 'de');
}

function seedState() {
  return {
    boxes: [GONE, STAYS],
    currentBox: GONE,
    settingsBox: GONE,
    setupTarget: { kind: 'str', box: GONE },
    otaTargetHost: '192.0.2.10',
    recentBoxKey: '192.0.2.10:17008',
    lastBalancePair: 'DEV-GONE+DEV-STAYS',
    pendingNames: { 'DEV-GONE': { name: 'x', until: 1 }, 'DEV-STAYS': { name: 'y', until: 1 } },
    zoneLive: { 'dev-gone': { master: 'DEV-STAYS' }, 'DEV-STAYS': { master: '' } },
    boxPlaying: { 'DEV-GONE': true, 'DEV-STAYS': false },
  };
}

// STR was removed from a speaker (Settings > Remove STR) and the speaker
// rebooted as a plain stock box, but the app kept painting it from its cache,
// re-selecting it at launch, and holding the OTA give-up and dismissed
// warnings against it. Everything about that one speaker has to go; nothing
// about any other speaker, and nothing app-wide, may go with it.
describe('purgeSpeakerLocalState', () => {
  beforeEach(() => localStorage.clear());

  it('drops every localStorage record of the removed speaker and no other', () => {
    seedStorage();
    purgeSpeakerLocalState(GONE, {});

    const cached = JSON.parse(localStorage.getItem('cachedBoxes'));
    expect(cached.map(b => b.host)).toEqual(['192.0.2.11']);
    expect(localStorage.getItem('lastBoxDeviceID')).toBeNull();
    for (const id of ['192.0.2.10', 'DEV-GONE']) {
      for (const type of ['ssh', 'conflict', 'nowifi', 'future-type']) {
        expect(localStorage.getItem('warnDismiss:' + id + ':' + type)).toBeNull();
      }
      expect(localStorage.getItem('otaStuck:' + id)).toBeNull();
      expect(localStorage.getItem('str.worldMapProvisioned.' + id)).toBeNull();
    }
    // The bystander and the app-wide keys are untouched.
    expect(localStorage.getItem('warnDismiss:192.0.2.11:ssh')).toBe('1');
    expect(localStorage.getItem('otaStuck:DEV-STAYS')).toBe('1');
    expect(localStorage.getItem('str.worldMapProvisioned.DEV-STAYS')).toBe('1');
    expect(localStorage.getItem('str.worldMapInvited')).toBe('1');
    expect(localStorage.getItem('str.favStations')).toBe('[]');
    expect(localStorage.getItem('locale')).toBe('de');
  });

  it('keeps the remembered selection when it is another speaker', () => {
    seedStorage();
    localStorage.setItem('lastBoxDeviceID', 'DEV-STAYS');
    purgeSpeakerLocalState(GONE, {});
    expect(localStorage.getItem('lastBoxDeviceID')).toBe('DEV-STAYS');
  });

  it('matches the cached list by deviceID as well as by host', () => {
    localStorage.setItem('cachedBoxes', JSON.stringify([
      { ...GONE, host: '192.0.2.99' }, // the speaker under an old lease
      STAYS,
    ]));
    purgeSpeakerLocalState(GONE, {});
    const cached = JSON.parse(localStorage.getItem('cachedBoxes'));
    expect(cached.map(b => b.deviceID)).toEqual(['DEV-STAYS']);
  });

  it('does not create a cachedBoxes entry when none was stored', () => {
    purgeSpeakerLocalState(GONE, {});
    expect(localStorage.getItem('cachedBoxes')).toBeNull();
  });

  it('clears the in-memory state that points at the removed speaker', () => {
    const state = seedState();
    purgeSpeakerLocalState(GONE, state);

    expect(state.currentBox).toBeNull();
    expect(state.settingsBox).toBeNull();
    expect(state.setupTarget).toBeNull();
    expect(state.otaTargetHost).toBeNull();
    expect(state.recentBoxKey).toBeNull();
    expect(state.lastBalancePair).toBeNull();
    expect(Object.keys(state.pendingNames)).toEqual(['DEV-STAYS']);
    expect(Object.keys(state.zoneLive)).toEqual(['DEV-STAYS']); // case-insensitive key match
    expect(Object.keys(state.boxPlaying)).toEqual(['DEV-STAYS']);
    // The discovery list itself is not the purge's to edit.
    expect(state.boxes).toHaveLength(2);
  });

  it('leaves selections alone when they point at another speaker', () => {
    const state = seedState();
    state.currentBox = STAYS;
    state.settingsBox = STAYS;
    state.setupTarget = { kind: 'str', box: STAYS };
    state.otaTargetHost = '192.0.2.11';
    state.recentBoxKey = 'DEV-STAYS';
    state.lastBalancePair = 'DEV-STAYS+DEV-THIRD';
    purgeSpeakerLocalState(GONE, state);
    expect(state.currentBox).toBe(STAYS);
    expect(state.settingsBox).toBe(STAYS);
    expect(state.setupTarget.box).toBe(STAYS);
    expect(state.otaTargetHost).toBe('192.0.2.11');
    expect(state.recentBoxKey).toBe('DEV-STAYS');
    expect(state.lastBalancePair).toBe('DEV-STAYS+DEV-THIRD');
  });

  it('calls the registered hooks with the identity and survives a throwing hook', () => {
    const seen = [];
    onSpeakerPurge(() => { throw new Error('boom'); });
    onSpeakerPurge((id) => seen.push(id));
    const identity = purgeSpeakerLocalState(GONE, {});
    expect(identity).toEqual({ host: '192.0.2.10', deviceID: 'DEV-GONE', ids: ['DEV-GONE', '192.0.2.10'] });
    expect(seen).toHaveLength(1);
    expect(seen[0]).toEqual(identity);
  });

  it('copes with a speaker that has no deviceID, with a missing state, and with no box at all', () => {
    seedStorage();
    const stock = { host: '192.0.2.10' };
    expect(() => purgeSpeakerLocalState(stock, undefined)).not.toThrow();
    expect(localStorage.getItem('warnDismiss:192.0.2.10:ssh')).toBeNull();
    // The deviceID-keyed records of the same speaker are unknown to a
    // host-only record and stay: nothing else can be matched.
    expect(localStorage.getItem('otaStuck:DEV-GONE')).toBe(JSON.stringify({ n: 3 }));
    expect(() => purgeSpeakerLocalState(null, {})).not.toThrow();
    expect(() => purgeSpeakerLocalState({}, {})).not.toThrow();
    expect(speakerIdentity(null)).toEqual({ host: '', deviceID: '', ids: [] });
  });

  it('collects every id spelling the app keys records on', () => {
    expect(speakerIdentity({ host: ' 192.0.2.5 ', deviceId: 'dev-5', mac: 'AA:BB', id: 'dev-5' }))
      .toEqual({ host: '192.0.2.5', deviceID: 'dev-5', ids: ['dev-5', 'AA:BB', '192.0.2.5'] });
  });
});

describe('forgetPairNamesFor', () => {
  it('drops the cached, pending and generation entries of every pair the speaker is in', async () => {
    // An in-flight read (never resolves under the mock) leaves a pending
    // marker; a write leaves a cache entry and a generation.
    pairDisplayName({ members: [{ deviceID: 'dev-gone' }, { deviceID: 'DEV-STAYS' }] });
    await setPairName({ members: [{ deviceID: 'DEV-GONE' }, { deviceID: 'DEV-THIRD' }] }, 'Old pair');
    await setPairName({ members: [{ deviceID: 'DEV-STAYS' }, { deviceID: 'DEV-THIRD' }] }, 'Other pair');

    // pending(DEV-GONE+DEV-STAYS) + cache(DEV-GONE+DEV-THIRD) + generation(DEV-GONE+DEV-THIRD)
    expect(forgetPairNamesFor('DEV-GONE')).toBe(3);
    expect(forgetPairNamesFor('DEV-GONE')).toBe(0);
    expect(forgetPairNamesFor('')).toBe(0);
    // The unrelated pair still renders its name from cache.
    expect(pairDisplayName({ members: [{ deviceID: 'DEV-THIRD' }, { deviceID: 'DEV-STAYS' }] })).toBe('Other pair');
  });
});
