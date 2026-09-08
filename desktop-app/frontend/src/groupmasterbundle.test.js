// Adversarial review scratch tests: pin masterBoxForKey against the EXACT
// zone shapes in the reporter's bundles (gh883, 2026-09-07), not a
// hand-drawn approximation of them.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { masterBoxForKey, storedGroupHostsOf, pairBlockedHosts } from './groups.js';

// Line endings normalised: the sources are checked out with CRLF on some
// machines, and the source slices below key on a bare newline.
const read = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8').replace(/\r\n/g, '\n');
const mainJS = read('./main.js');

// Bundle 20260907-185945: three SoundTouch 10s, master .239.
// The app's records are keyed on the mDNS-announced id (the FormZone call in
// app.log used DEV#8783465f), while every speaker names the master by the
// firmware id (DEV#871b2608). The two are different values.
const A = { deviceID: 'MDNS-A', host: '192.0.2.239', model: 'SoundTouch 10' };
const B = { deviceID: 'MDNS-B', host: '192.0.2.4', model: 'SoundTouch 10' };
const C = { deviceID: 'MDNS-C', host: '192.0.2.60', model: 'SoundTouch 10' };
const boxes = [A, B, C];
const membersAll = [
  { deviceID: 'FW-B', ip: '192.0.2.4' },
  { deviceID: 'FW-C', ip: '192.0.2.60' },
];

describe('masterBoxForKey against the reporter bundle shapes', () => {
  it('resolves the 3-box group of bundle 185945 (senderIP path)', () => {
    const zoneLive = {
      'MDNS-A': { master: 'FW-A', members: membersAll },
      'MDNS-B': { master: 'FW-A', members: membersAll, senderIP: '192.0.2.239' },
      'MDNS-C': { master: 'FW-A', members: membersAll, senderIP: '192.0.2.239' },
    };
    expect(masterBoxForKey('FW-A', zoneLive, boxes)).toBe(A);
  });

  it('resolves the same group with no senderIP anywhere (shape path)', () => {
    const zoneLive = {
      'MDNS-A': { master: 'FW-A', members: membersAll },
      'MDNS-B': { master: 'FW-A', members: membersAll },
      'MDNS-C': { master: 'FW-A', members: membersAll },
    };
    expect(masterBoxForKey('FW-A', zoneLive, boxes)).toBe(A);
  });

  it('resolves the 2-box group of bundle 191742 (master .60)', () => {
    const members = [{ deviceID: 'FW-B', ip: '192.0.2.4' }];
    const zoneLive = {
      'MDNS-B': { master: 'FW-C', members, senderIP: '192.0.2.60' },
      'MDNS-C': { master: 'FW-C', members },
    };
    expect(masterBoxForKey('FW-C', zoneLive, boxes)).toBe(C);
  });

  it('never renders a raw key: an unresolvable key answers null', () => {
    expect(masterBoxForKey('FW-A', {}, boxes)).toBeNull();
  });

  it('refuses when the master is only known through its followers', () => {
    // The master's own poll failed, so its entry is absent. Both ID-free steps
    // require the nominated box to name the key itself, so this answers null
    // and the caller shows the generic label instead of guessing.
    const zoneLive = {
      'MDNS-B': { master: 'FW-A', members: membersAll, senderIP: '192.0.2.239' },
      'MDNS-C': { master: 'FW-A', members: membersAll, senderIP: '192.0.2.239' },
    };
    expect(masterBoxForKey('FW-A', zoneLive, boxes)).toBeNull();
  });

  it('a stale senderIP pointing at a box in a different group nominates nobody', () => {
    const zoneLive = {
      'MDNS-B': { master: 'FW-A', members: membersAll, senderIP: '192.0.2.60' },
      'MDNS-C': { master: 'FW-OTHER', members: [] },
    };
    // B lists itself, C names another master, so neither step can answer.
    expect(masterBoxForKey('FW-A', zoneLive, boxes)).toBeNull();
  });

  it('matches the second announced identity (boxDeviceID)', () => {
    const boxesWithBox = [{ ...A, boxDeviceID: 'FW-A' }, B, C];
    expect(masterBoxForKey('fw-a', {}, boxesWithBox)).toBe(boxesWithBox[0]);
  });

  it('never nominates a stock speaker', () => {
    const stock = { deviceID: 'FW-A', host: '192.0.2.9', kind: 'stock' };
    expect(masterBoxForKey('FW-A', {}, [stock])).toBeNull();
  });
});

describe('storedGroupHostsOf', () => {
  it('covers the main speaker and every remembered member', () => {
    const zoneLive = {
      'MDNS-A': {
        permanent: true,
        members: [],
        remembered: [{ deviceID: 'FW-B', ip: '192.0.2.4' }, { ip: '192.0.2.99', name: 'gone' }],
      },
    };
    const hosts = storedGroupHostsOf(zoneLive, boxes);
    expect([...hosts].sort()).toEqual(['192.0.2.239', '192.0.2.4', '192.0.2.99']);
  });

  it('does not fence off a non-permanent stored group', () => {
    // The agent refuses a pairing for ANY stored group with slaves, permanent
    // or not, but the picker only hides the permanent ones.
    const zoneLive = {
      'MDNS-A': { members: [], remembered: [{ deviceID: 'FW-B', ip: '192.0.2.4' }] },
    };
    expect(storedGroupHostsOf(zoneLive, boxes).size).toBe(0);
  });
});

// The Music tab draws the SAME group frame from the SAME master key as the
// Multi-Room tab, and it was left on the plain deviceID lookup when that tab
// moved off it. On the reporter's fleet the lookup misses, so the frame came up
// with no name on it and no x to take the group apart, and no pill was starred
// as the main speaker.
describe('the music-tab group frame survives the id mismatch', () => {
  // The reporter's shape: the app's records are keyed on one identity, the
  // speakers name the master by another.
  const zoneLive = {
    'MDNS-A': { master: 'FW-A', members: membersAll },
    'MDNS-B': { master: 'FW-A', members: membersAll, senderIP: '192.0.2.239' },
    'MDNS-C': { master: 'FW-A', members: membersAll, senderIP: '192.0.2.239' },
  };

  it('the plain lookup the frame used finds nobody on this shape', () => {
    // What the label and the x were both gated on, so both were dropped.
    expect(boxes.find(b => (b.deviceID || '').toUpperCase() === 'FW-A')).toBeUndefined();
  });

  it('the resolver names the main speaker, so the frame gets a label and an x', () => {
    const masterBox = masterBoxForKey('FW-A', zoneLive, boxes);
    expect(masterBox).toBe(A);
    // The frame's label is the master's name and its x is drawn only when a
    // master box resolved, so one non-null answer restores both.
    expect(!!masterBox).toBe(true);
  });

  it('and exactly one pill wears the main-speaker star, matched by host', () => {
    const masterBox = masterBoxForKey('FW-A', zoneLive, boxes);
    const starred = boxes.filter(b => b.host === masterBox.host);
    expect(starred).toEqual([A]);
  });

  it('renderBoxSelect resolves through the helper and stars by host', () => {
    expect(mainJS).toContain('masterBoxForKey(m, zlMap, state.boxes)');
    expect(mainJS).toContain('const masterBox = masterBoxOfKey(m);');
    expect(mainJS).toContain('resolved.host === b.host');
    expect(mainJS).not.toContain("const masterBox = state.boxes.find(b => (b.deviceID || '').toUpperCase() === m)");
  });

  it('the frame x aims through the same resolver instead of returning in silence', () => {
    const start = mainJS.indexOf('async function dissolveGroupFrame(');
    const fnSrc = mainJS.slice(start, mainJS.indexOf('\n}\n', start));
    expect(fnSrc).toContain('masterBoxForKey(mk, state.zoneLive, state.boxes)');
    expect(fnSrc).toContain("t('multiroom.dissolveIncomplete')");
    expect(fnSrc).not.toContain("state.boxes.find(b => (b.deviceID || '').toUpperCase() === mk)");
  });
});

// The stored-group fence must not swallow a pair that is standing right now:
// the picker is the only place a pair can be renamed or undone.
describe('pairBlockedHosts', () => {
  const paired = {
    stereo: {
      id: 'pair-1', masterDeviceID: 'FW-B',
      members: [{ deviceID: 'FW-B', ip: '192.0.2.4', role: 'LEFT' },
        { deviceID: 'FW-C', ip: '192.0.2.60', role: 'RIGHT' }],
    },
  };

  it('still fences off the speakers of a stored group that is not paired', () => {
    const zoneLive = {
      'MDNS-A': { permanent: true, members: [], remembered: [{ deviceID: 'FW-B', ip: '192.0.2.4' }] },
    };
    expect([...pairBlockedHosts(zoneLive, boxes)].sort()).toEqual(['192.0.2.239', '192.0.2.4']);
  });

  it('leaves a LIVE pair pairable even when both halves are remembered in a stored group', () => {
    const zoneLive = {
      'MDNS-A': {
        permanent: true, members: [],
        remembered: [{ deviceID: 'FW-B', ip: '192.0.2.4' }, { deviceID: 'FW-C', ip: '192.0.2.60' }],
      },
      'MDNS-B': paired,
      'MDNS-C': paired,
    };
    const blocked = pairBlockedHosts(zoneLive, boxes);
    // Both halves stay selectable, so the pair can still be renamed and undone.
    expect(blocked.has('192.0.2.4')).toBe(false);
    expect(blocked.has('192.0.2.60')).toBe(false);
    // The group's main speaker is not part of that pair and stays fenced off.
    expect(blocked.has('192.0.2.239')).toBe(true);
  });

  it('is the set the picker and the submit guard both use', () => {
    const multiroomJS = read('./views/multiroom.js');
    expect(multiroomJS).toContain('pairBlockedHosts(state.zoneLive, strBoxes)');
    expect(multiroomJS).toContain('pairBlockedHosts(state.zoneLive, allBoxes || pairCands)');
    expect(multiroomJS).not.toContain('storedGroupHostsOf(');
  });
});
