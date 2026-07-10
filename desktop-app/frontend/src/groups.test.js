// Tests for groups.js — the single source of truth for group membership.
// Pure data-in/data-out plus the shared poll with an injected fetcher; no DOM.
import { describe, it, expect, beforeEach } from 'vitest';
import { state } from './state.js';
import {
  zoneBoxes,
  masterOf,
  isFollower,
  followersOf,
  groupMembersOf,
  resolvePlayTarget,
  mergeZoneLive,
  applyOptimisticZone,
  sameBoxIdentity,
  parsePlayRejection,
  resolveBoxByRef,
  fetchZoneLive,
  resetZoneLivePoll,
} from './groups.js';

// Placeholder LAN (192.0.2.0/24, RFC 5737) and deviceIDs only.
const master = { host: '192.0.2.1', port: 8888, deviceID: 'AA11BB22CC01', kind: 'str' };
const boxA = { host: '192.0.2.2', port: 8888, deviceID: 'AA11BB22CC02', kind: 'str' };
const boxB = { host: '192.0.2.3', port: 8888, deviceID: 'AA11BB22CC03', kind: 'str' };
const stock = { host: '192.0.2.9', port: 8090, deviceID: 'AA11BB22CC09', kind: 'stock' };

// A realistic zoneLive map: master leads, A and B follow.
function liveMap() {
  const members = [
    { deviceID: master.deviceID, ip: master.host },
    { deviceID: boxA.deviceID, ip: boxA.host },
    { deviceID: boxB.deviceID, ip: boxB.host },
  ];
  return {
    [master.deviceID]: { master: master.deviceID, senderIP: master.host, members },
    [boxA.deviceID]: { master: master.deviceID, senderIP: master.host, members },
    [boxB.deviceID]: { master: master.deviceID, senderIP: master.host, members },
  };
}

describe('zoneBoxes', () => {
  it('keeps only STR boxes with host and deviceID', () => {
    const noID = { host: '192.0.2.4', port: 8888, kind: 'str' };
    expect(zoneBoxes([master, stock, noID, null])).toEqual([master]);
  });
});

describe('masterOf / isFollower', () => {
  it('returns the uppercased master and classifies followers', () => {
    const zl = { X1: { master: 'aa11bb22cc01' } };
    expect(masterOf('X1', zl)).toBe('AA11BB22CC01');
    expect(isFollower('X1', zl)).toBe(true);
  });
  it('is empty for standalone, unknown and null entries', () => {
    const zl = { S1: { members: [] }, N1: null };
    expect(masterOf('S1', zl)).toBe('');
    expect(masterOf('N1', zl)).toBe('');
    expect(masterOf('MISSING', zl)).toBe('');
    expect(masterOf('', zl)).toBe('');
    expect(isFollower('S1', zl)).toBe(false);
  });
  it('a master is not a follower of itself', () => {
    const zl = liveMap();
    expect(isFollower(master.deviceID, zl)).toBe(false);
    expect(isFollower(boxA.deviceID, zl)).toBe(true);
  });
});

describe('followersOf', () => {
  it('returns the boxes following the master, excluding the master and stock', () => {
    const zl = liveMap();
    const boxes = [master, boxA, boxB, stock];
    expect(followersOf(master.deviceID, zl, boxes)).toEqual([boxA, boxB]);
  });
  it('a follower with a kept last-good entry still counts after a poll gap', () => {
    // boxB's own poll failed but mergeZoneLive kept its entry: membership holds.
    const zl = liveMap();
    const results = [
      { status: 'fulfilled', value: zl[master.deviceID] },
      { status: 'fulfilled', value: zl[boxA.deviceID] },
      { status: 'rejected', reason: new Error('agent busy') },
    ];
    const merged = mergeZoneLive(zl, [master, boxA, boxB], results);
    expect(followersOf(master.deviceID, merged, [master, boxA, boxB])).toEqual([boxA, boxB]);
  });
});

describe('groupMembersOf', () => {
  it('unions follower self-reports with the master member list', () => {
    const zl = liveMap();
    // boxB flapped out of discovery: it must still be listed (from the
    // master's own member list) so a toggle does not kick it.
    const got = groupMembersOf(master, zl, [master, boxA, stock]);
    expect(got.map(m => m.ip).sort()).toEqual([boxA.host, boxB.host].sort());
    const b = got.find(m => m.ip === boxB.host);
    expect(b.box).toBeNull();
    expect(b.deviceID).toBe(boxB.deviceID);
    const a = got.find(m => m.ip === boxA.host);
    expect(a.box).toBe(boxA);
  });
  it('dedupes by IP when firmware and mDNS deviceIDs differ (two-chip chassis)', () => {
    const zl = liveMap();
    // The master's member list names boxA by its firmware (SCM) ID.
    zl[master.deviceID] = {
      master: master.deviceID,
      senderIP: master.host,
      members: [
        { deviceID: master.deviceID, ip: master.host },
        { deviceID: 'DD44EE55FF06', ip: boxA.host }, // same box, other chip's MAC
      ],
    };
    const got = groupMembersOf(master, zl, [master, boxA]);
    expect(got).toHaveLength(1);
    expect(got[0].ip).toBe(boxA.host);
    expect(got[0].deviceID).toBe(boxA.deviceID); // discovery record wins
  });
  it('never lists the master itself and is empty without a master deviceID', () => {
    const zl = liveMap();
    const got = groupMembersOf(master, zl, [master, boxA, boxB]);
    expect(got.some(m => m.ip === master.host)).toBe(false);
    expect(groupMembersOf({ host: '192.0.2.1' }, zl, [master])).toEqual([]);
  });
});

describe('resolvePlayTarget', () => {
  it('keeps a standalone or leading box as the target', () => {
    const zl = liveMap();
    expect(resolvePlayTarget(master, zl, [master, boxA])).toBe(master);
    expect(resolvePlayTarget(boxA, {}, [master, boxA])).toBe(boxA);
  });
  it('retargets a follower to its discovered master', () => {
    const zl = liveMap();
    expect(resolvePlayTarget(boxA, zl, [master, boxA, boxB])).toBe(master);
  });
  it('falls back to the follower when the master is not discovered', () => {
    const zl = liveMap();
    expect(resolvePlayTarget(boxA, zl, [boxA, boxB])).toBe(boxA);
  });
  it('matches the master case-insensitively', () => {
    const zl = { [boxA.deviceID]: { master: master.deviceID.toLowerCase() } };
    expect(resolvePlayTarget(boxA, zl, [master, boxA])).toBe(master);
  });
});

describe('mergeZoneLive', () => {
  it('replaces entries on success, including a confirmed standalone', () => {
    const prev = liveMap();
    const standalone = { members: [] };
    const results = [
      { status: 'fulfilled', value: prev[master.deviceID] },
      { status: 'fulfilled', value: standalone }, // boxA really left
      { status: 'fulfilled', value: prev[boxB.deviceID] },
    ];
    const merged = mergeZoneLive(prev, [master, boxA, boxB], results);
    expect(merged[boxA.deviceID]).toBe(standalone);
    expect(masterOf(boxA.deviceID, merged)).toBe('');
  });
  it('keeps the last-good entry when a single poll fails', () => {
    const prev = liveMap();
    const results = [
      { status: 'fulfilled', value: prev[master.deviceID] },
      { status: 'rejected', reason: new Error('timeout') },
      { status: 'fulfilled', value: prev[boxB.deviceID] },
    ];
    const merged = mergeZoneLive(prev, [master, boxA, boxB], results);
    expect(merged[boxA.deviceID]).toBe(prev[boxA.deviceID]);
  });
  it('keeps an optimistic null through a failed poll (known standalone)', () => {
    const prev = { [boxA.deviceID]: null };
    const merged = mergeZoneLive(prev, [boxA], [{ status: 'rejected', reason: 'x' }]);
    expect(boxA.deviceID in merged).toBe(true);
    expect(merged[boxA.deviceID]).toBeNull();
  });
  it('leaves a never-fetched box absent (unknown, not standalone)', () => {
    const merged = mergeZoneLive({}, [boxA], [{ status: 'rejected', reason: 'x' }]);
    expect(boxA.deviceID in merged).toBe(false);
  });
  it('drops boxes absent from this round', () => {
    const prev = liveMap();
    const merged = mergeZoneLive(prev, [master], [{ status: 'fulfilled', value: prev[master.deviceID] }]);
    expect(Object.keys(merged)).toEqual([master.deviceID]);
  });
});

describe('applyOptimisticZone', () => {
  it('writes real-shaped entries for the members AND the master', () => {
    const next = [
      { deviceID: boxA.deviceID, ip: boxA.host },
      { deviceID: boxB.deviceID, ip: boxB.host },
    ];
    const zl = applyOptimisticZone({}, master, next);
    // The master keeps its OWN entry so the selector star/frame and the
    // Multi-Room summary agree with the chips right away.
    expect(masterOf(master.deviceID, zl)).toBe(master.deviceID);
    expect(masterOf(boxA.deviceID, zl)).toBe(master.deviceID);
    expect(zl[master.deviceID].members.map(m => m.ip)).toEqual([boxA.host, boxB.host]);
    expect(zl[master.deviceID].senderIP).toBe(master.host);
  });
  it('clears a removed member and every stale entry of this master', () => {
    const zl0 = liveMap();
    const next = [{ deviceID: boxA.deviceID, ip: boxA.host }]; // boxB removed
    const zl = applyOptimisticZone(zl0, master, next);
    expect(zl[boxB.deviceID]).toBeNull();
    expect(masterOf(boxA.deviceID, zl)).toBe(master.deviceID);
  });
  it('dissolve (no members) nulls the whole group including the master', () => {
    const zl = applyOptimisticZone(liveMap(), master, []);
    expect(zl[master.deviceID]).toBeNull();
    expect(zl[boxA.deviceID]).toBeNull();
    expect(zl[boxB.deviceID]).toBeNull();
  });
  it('does not touch entries of an unrelated group', () => {
    const other = { master: 'FF00FF00FF00', senderIP: '192.0.2.7', members: [] };
    const zl = applyOptimisticZone({ OTHER: other }, master, []);
    expect(zl.OTHER).toBe(other);
  });
});

describe('sameBoxIdentity', () => {
  it('matches by deviceID across refreshed box objects', () => {
    const fresh = { ...boxA, host: '192.0.2.20' }; // re-IP'd, same device
    expect(sameBoxIdentity(boxA, fresh)).toBe(true);
  });
  it('falls back to host:port when a deviceID is missing', () => {
    const a = { host: '192.0.2.2', port: 8888 };
    expect(sameBoxIdentity(a, boxA)).toBe(true);
    expect(sameBoxIdentity(a, { host: '192.0.2.2', port: 17008 })).toBe(false);
  });
  it('differs for different devices and handles null', () => {
    expect(sameBoxIdentity(boxA, boxB)).toBe(false);
    expect(sameBoxIdentity(null, boxA)).toBe(false);
    expect(sameBoxIdentity(boxA, null)).toBe(false);
  });
});

describe('parsePlayRejection', () => {
  it('parses the structured 409 body with a master reference', () => {
    const r = parsePlayRejection('status 409: {"error":"box-grouped","master":"AA11BB22CC01"}');
    expect(r).toEqual({ grouped: true, master: 'AA11BB22CC01' });
  });
  it('accepts the reduced marker without a master field', () => {
    expect(parsePlayRejection('box-grouped')).toEqual({ grouped: true, master: '' });
    expect(parsePlayRejection(new Error('box-grouped'))).toEqual({ grouped: true, master: '' });
  });
  it('keeps current behavior for older agents (raw SOAP string)', () => {
    const soap = '<s:Fault><errorCode>501</errorCode>Cant control member of group</s:Fault>';
    expect(parsePlayRejection(soap).grouped).toBe(false);
    expect(parsePlayRejection('').grouped).toBe(false);
    expect(parsePlayRejection(null).grouped).toBe(false);
  });
  it('tolerates a malformed JSON fragment around the marker', () => {
    const r = parsePlayRejection('{"error":"box-grouped","master":broken}');
    expect(r.grouped).toBe(true);
    expect(r.master).toBe('');
  });
});

describe('resolveBoxByRef', () => {
  const boxes = [master, boxA, stock];
  it('resolves a deviceID case-insensitively and an IP directly', () => {
    expect(resolveBoxByRef(master.deviceID.toLowerCase(), boxes)).toBe(master);
    expect(resolveBoxByRef(boxA.host, boxes)).toBe(boxA);
  });
  it('never resolves to a stock box and returns null when unknown', () => {
    expect(resolveBoxByRef(stock.deviceID, boxes)).toBeNull();
    expect(resolveBoxByRef('', boxes)).toBeNull();
    expect(resolveBoxByRef('device-id-here', boxes)).toBeNull();
  });
});

describe('fetchZoneLive', () => {
  beforeEach(() => {
    resetZoneLivePoll();
    state.zoneLive = {};
    state.boxes = [master, boxA];
  });

  const okZone = (z) => Promise.resolve(z);

  it('fetches, merges into state.zoneLive and reports a repaint', async () => {
    const zl = liveMap();
    const fetcher = (host) => okZone(host === master.host ? zl[master.deviceID] : zl[boxA.deviceID]);
    const ran = await fetchZoneLive([master, boxA], { maxAgeMs: 0 }, fetcher);
    expect(ran).toBe(true);
    expect(masterOf(boxA.deviceID, state.zoneLive)).toBe(master.deviceID);
  });

  it('respects the minBoxes gate without calling the fetcher', async () => {
    let calls = 0;
    const ran = await fetchZoneLive([master], { maxAgeMs: 0, minBoxes: 2 }, () => { calls++; return okZone({}); });
    expect(ran).toBe(false);
    expect(calls).toBe(0);
  });

  it('debounces within maxAgeMs (the 8s music-tab cadence)', async () => {
    let calls = 0;
    const fetcher = () => { calls++; return okZone({ members: [] }); };
    expect(await fetchZoneLive([master, boxA], { maxAgeMs: 8000 }, fetcher)).toBe(true);
    expect(await fetchZoneLive([master, boxA], { maxAgeMs: 8000 }, fetcher)).toBe(false);
    expect(calls).toBe(2); // one round of two boxes, no second round
  });

  it('maxAgeMs 0 (Multi-Room tab / forced confirm) always fetches', async () => {
    let rounds = 0;
    const fetcher = () => { rounds++; return okZone({ members: [] }); };
    await fetchZoneLive([master], { maxAgeMs: 0 }, fetcher);
    await fetchZoneLive([master], { maxAgeMs: 0 }, fetcher);
    expect(rounds).toBe(2);
  });

  it('a concurrent caller shares the in-flight round instead of skipping', async () => {
    let resolveSlow;
    let calls = 0;
    const fetcher = () => { calls++; return new Promise(r => { resolveSlow = r; }); };
    const first = fetchZoneLive([master], { maxAgeMs: 0 }, fetcher);
    // Yield so the first call reaches its fetch before the second starts.
    await Promise.resolve();
    const second = fetchZoneLive([master], { maxAgeMs: 0 }, () => { throw new Error('must not refetch'); });
    resolveSlow({ members: [] });
    expect(await first).toBe(true);
    expect(await second).toBe(true);
    expect(calls).toBe(1);
  });

  it('keeps the last-good entry when one box rejects (poll gap)', async () => {
    state.zoneLive = liveMap();
    const fetcher = (host) => host === boxA.host
      ? Promise.reject(new Error('agent busy'))
      : okZone(liveMap()[master.deviceID]);
    await fetchZoneLive([master, boxA], { maxAgeMs: 0 }, fetcher);
    expect(masterOf(boxA.deviceID, state.zoneLive)).toBe(master.deviceID); // membership held
  });
});
