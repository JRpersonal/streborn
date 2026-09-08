// Tests for groups.js — the single source of truth for group membership.
// Pure data-in/data-out plus the shared poll with an injected fetcher; no DOM.
import { describe, it, expect, beforeEach } from 'vitest';
import { state } from './state.js';
import {
  zoneBoxes,
  storedPermanentGroupsOf,
  groupCount,
  onZoneLive,
  notifyZoneLive,
  masterOf,
  isFollower,
  followersOf,
  groupMembersOf,
  resolvePlayTarget,
  mergeZoneLive,
  balanceSourceBox,
  applyOptimisticZone,
  sameBoxIdentity,
  parsePlayRejection,
  resolveBoxByRef,
  fetchZoneLive,
  resetZoneLivePoll,
  stereoPairOf,
  stereoPairsOf,
  stereoPairKey,
  pairMemberBoxes,
  inStereoPair,
  stereoUndoTargets,
  stereoSelectionPick,
  masterBoxForKey,
  storedGroupHostsOf,
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

describe('stereoPairKey', () => {
  it('is the sorted set of member deviceIDs, joined with +', () => {
    const pair = { master: 'AA11', members: [{ deviceID: 'BB22' }, { deviceID: 'AA11' }] };
    expect(stereoPairKey(pair)).toBe('AA11+BB22');
  });
  it('is identical after an L/R swap (same two members, roles flipped)', () => {
    const a = { master: 'AA11', members: [{ deviceID: 'AA11', role: 'LEFT' }, { deviceID: 'BB22', role: 'RIGHT' }] };
    const b = { master: 'BB22', members: [{ deviceID: 'BB22', role: 'LEFT' }, { deviceID: 'AA11', role: 'RIGHT' }] };
    expect(stereoPairKey(a)).toBe(stereoPairKey(b));
  });
  it('uppercases deviceIDs so casing never splits the key', () => {
    expect(stereoPairKey({ members: [{ deviceID: 'aa11' }, { deviceID: 'bb22' }] })).toBe('AA11+BB22');
  });
  it('returns "" (not a bare master) when fewer than two member deviceIDs are present', () => {
    // A master-only fallback would compute a different key than the stored
    // "A+B", blanking the label and stranding a saved name; "" means
    // "cannot identify the pair right now" instead.
    expect(stereoPairKey({ master: 'aa11', members: [{ deviceID: '' }] })).toBe('');
    expect(stereoPairKey({ master: 'aa11', members: [] })).toBe('');
  });
  it('returns "" for a null pair', () => {
    expect(stereoPairKey(null)).toBe('');
  });
});

describe('stereoUndoTargets', () => {
  const pair = {
    id: 'str-grp-1', master: master.deviceID,
    members: [
      { deviceID: master.deviceID, ip: master.host, role: 'LEFT' },
      { deviceID: boxA.deviceID, ip: boxA.host, role: 'RIGHT' },
    ],
  };
  it('returns both halves, master first', () => {
    expect(stereoUndoTargets(pair, [boxA, boxB, master])).toEqual([master, boxA]);
  });
  it('still returns the other half when the master is not discovered', () => {
    // The leftover case: the half that answers is not the recorded master.
    expect(stereoUndoTargets(pair, [boxA, boxB])).toEqual([boxA]);
  });
  it('skips stock speakers, which have no agent to ask', () => {
    const stockPair = {
      master: stock.deviceID,
      members: [
        { deviceID: stock.deviceID, ip: stock.host, role: 'LEFT' },
        { deviceID: boxA.deviceID, ip: boxA.host, role: 'RIGHT' },
      ],
    };
    expect(stereoUndoTargets(stockPair, [stock, boxA])).toEqual([boxA]);
  });
  it('is empty without a pair', () => {
    expect(stereoUndoTargets(null, [master, boxA])).toEqual([]);
  });
});

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
    // Equality, not identity: a carried entry now records WHEN the speaker
    // stopped answering, so it can be dropped once that is no longer a gap but
    // an absence. The membership it carries is what matters here.
    expect(merged[boxA.deviceID]).toMatchObject(prev[boxA.deviceID]);
    expect(typeof merged[boxA.deviceID].staleSince).toBe('number');
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

// A stereo pair is a firmware GROUP, not a zone: /getZone reports
// {"members":[]} on a paired speaker exactly as on a standalone one. The agent
// therefore reports the firmware group as `stereo` alongside the zone, and the
// helpers below are what let the Multi-Room tab see a pair at all. Without
// them the tab offered the first two candidate speakers and sent "undo pair"
// to whichever speaker the multiroom master selection pointed at - which, with
// three SoundTouch 10s, was twice a speaker that was not in the pair while the
// app reported success (field, 2026-08-04).
describe('stereoPairOf', () => {
  const pairEntry = {
    master: '',
    members: [],
    stereo: {
      id: 'str-grp-AAA', master: 'AAA',
      members: [
        { deviceID: 'BBB', ip: '192.0.2.32', role: 'RIGHT' },
        { deviceID: 'AAA', ip: '192.0.2.19', role: 'LEFT' },
      ],
    },
  };

  it('returns null when no speaker reports a pair', () => {
    expect(stereoPairOf({ AAA: { members: [] }, BBB: { members: [] } })).toBe(null);
    expect(stereoPairOf({})).toBe(null);
    expect(stereoPairOf(null)).toBe(null);
  });

  it('finds the pair from any member self-report', () => {
    const pair = stereoPairOf({ CCC: { members: [] }, BBB: pairEntry });
    expect(pair.master).toBe('AAA');
    expect(pair.members).toHaveLength(2);
  });

  it('orders members LEFT before RIGHT regardless of report order', () => {
    const boxes = [
      { deviceID: 'AAA', host: '192.0.2.19', kind: 'str' },
      { deviceID: 'BBB', host: '192.0.2.32', kind: 'str' },
      { deviceID: 'CCC', host: '192.0.2.55', kind: 'str' },
    ];
    const mapped = pairMemberBoxes(stereoPairOf({ AAA: pairEntry }), boxes);
    expect(mapped.map(x => x.box.deviceID)).toEqual(['AAA', 'BBB']);
  });

  it('matches a member by IP when the deviceID differs (two-chip chassis)', () => {
    const boxes = [{ deviceID: 'MDNS-DERIVED', host: '192.0.2.32', kind: 'str' }];
    const mapped = pairMemberBoxes(stereoPairOf({ AAA: pairEntry }), boxes);
    expect(mapped.find(x => x.box)?.box.deviceID).toBe('MDNS-DERIVED');
  });

  it('keeps an undiscovered member as a null box rather than dropping it', () => {
    const mapped = pairMemberBoxes(stereoPairOf({ AAA: pairEntry }), []);
    expect(mapped).toHaveLength(2);
    expect(mapped.every(x => x.box === null)).toBe(true);
  });

  it('tells a paired speaker from an uninvolved one', () => {
    const pair = stereoPairOf({ AAA: pairEntry });
    expect(inStereoPair({ deviceID: 'AAA', host: '192.0.2.19' }, pair)).toBe(true);
    expect(inStereoPair({ deviceID: 'CCC', host: '192.0.2.55' }, pair)).toBe(false);
    expect(inStereoPair({ deviceID: 'CCC', host: '192.0.2.55' }, null)).toBe(false);
  });
});

// Only the master of a stereo pair reports a balance. The picker offers both
// halves separately, so selecting the other one must still answer for the pair
// rather than showing nothing (reported 2026-08-06).
describe('balanceSourceBox', () => {
  const left = { deviceID: 'AAA', host: '192.0.2.10', kind: 'str' };
  const right = { deviceID: 'BBB', host: '192.0.2.11', kind: 'str' };
  const lone = { deviceID: 'CCC', host: '192.0.2.12', kind: 'str' };
  const pair = {
    id: 'pair-1',
    master: 'AAA',
    members: [
      { deviceID: 'AAA', ip: '192.0.2.10', role: 'LEFT' },
      { deviceID: 'BBB', ip: '192.0.2.11', role: 'RIGHT' },
    ],
  };
  const boxes = [left, right, lone];

  it('asks the master when the non-master half is selected', () => {
    expect(balanceSourceBox(right, pair, boxes)).toBe(left);
  });

  it('asks the master when the master itself is selected', () => {
    expect(balanceSourceBox(left, pair, boxes)).toBe(left);
  });

  it('leaves a speaker outside the pair answering for itself', () => {
    expect(balanceSourceBox(lone, pair, boxes)).toBe(lone);
  });

  it('answers for the speaker itself when there is no pair', () => {
    expect(balanceSourceBox(right, null, boxes)).toBe(right);
  });

  it('falls back to the selected speaker when the master is not discovered', () => {
    expect(balanceSourceBox(right, pair, [right])).toBe(right);
  });
});

describe('stereoPairsOf', () => {
  const pairEntry = {
    stereo: {
      id: 'str-grp-AAA', master: 'AAA',
      members: [
        { deviceID: 'BBB', ip: '192.0.2.32', role: 'RIGHT' },
        { deviceID: 'AAA', ip: '192.0.2.19', role: 'LEFT' },
      ],
    },
  };
  const pairEntry2 = {
    stereo: {
      id: 'str-grp-CCC', master: 'CCC',
      members: [
        { deviceID: 'CCC', ip: '192.0.2.40', role: 'LEFT' },
        { deviceID: 'DDD', ip: '192.0.2.41', role: 'RIGHT' },
      ],
    },
  };

  it('returns [] when nothing reports a pair', () => {
    expect(stereoPairsOf({})).toEqual([]);
    expect(stereoPairsOf(null)).toEqual([]);
    expect(stereoPairsOf({ AAA: { members: [] }, BBB: { members: [] } })).toEqual([]);
  });

  it('dedupes the two self-reports of one pair into a single entry', () => {
    const got = stereoPairsOf({ AAA: pairEntry, BBB: pairEntry });
    expect(got).toHaveLength(1);
    expect(got[0].master).toBe('AAA');
  });

  it('returns two distinct pairs, in first-seen order', () => {
    const got = stereoPairsOf({ AAA: pairEntry, CCC: pairEntry2 });
    expect(got).toHaveLength(2);
    expect(got.map(p => p.master)).toEqual(['AAA', 'CCC']);
  });

  it('[0] equals stereoPairOf for a multi-pair map', () => {
    const zl = { AAA: pairEntry, CCC: pairEntry2 };
    expect(stereoPairsOf(zl)[0]).toEqual(stereoPairOf(zl));
  });

  it('keeps two transient (<2-member) records apart via their ids', () => {
    const got = stereoPairsOf({
      A: { stereo: { id: 'x', members: [] } },
      B: { stereo: { id: 'y', members: [] } },
    });
    expect(got).toHaveLength(2);
    expect(got.map(p => p.id).sort()).toEqual(['x', 'y']);
  });
});

describe('stereoPairOf master field', () => {
  it('reads the masterDeviceID the agent actually sends', () => {
    const zoneLive = {
      A: {
        members: [],
        stereo: {
          id: 'str-grp-AA', name: 'Stereo pair', masterDeviceID: 'AA',
          members: [{ deviceID: 'AA', ip: '192.0.2.1', role: 'LEFT' },
                    { deviceID: 'BB', ip: '192.0.2.2', role: 'RIGHT' }],
        },
      },
    };
    expect(stereoPairOf(zoneLive).master).toBe('AA');
  });

  it('still accepts the older master spelling', () => {
    const zoneLive = { A: { stereo: { id: 'x', master: 'cc', members: [] } } };
    expect(stereoPairOf(zoneLive).master).toBe('CC');
  });
});

// stereoSelectionPick: a still-valid user pick must beat the live pair, or
// forming a SECOND pair is impossible (every click reverted, field report
// 2026-08-29); the live pair stays the default for untouched slots
// (2026-08-04 behaviour), candidates are the last resort.
describe('stereoSelectionPick', () => {
  const candIDs = ['A', 'B', 'C', 'D'];
  const liveIDs = ['A', 'B']; // the existing pair

  it('first paint without a remembered pick sits on the live pair', () => {
    expect(stereoSelectionPick({ left: '', right: '', liveIDs, candIDs })).toEqual(['A', 'B']);
  });

  it('a fresh pick of an unpaired speaker sticks while a pair is live', () => {
    // She picked C on the left; the right still holds B from the default.
    expect(stereoSelectionPick({ left: 'C', right: 'B', liveIDs, candIDs })).toEqual(['C', 'B']);
    // Then D on the right: the new pair C+D survives the repaint.
    expect(stereoSelectionPick({ left: 'C', right: 'D', liveIDs, candIDs })).toEqual(['C', 'D']);
  });

  it('a stale pick (speaker gone) falls back to the live pair slot', () => {
    expect(stereoSelectionPick({ left: 'GONE', right: 'D', liveIDs, candIDs })).toEqual(['A', 'D']);
  });

  it('no live pair and nothing remembered falls back to the first candidates', () => {
    expect(stereoSelectionPick({ left: '', right: '', liveIDs: [], candIDs })).toEqual(['A', 'B']);
  });

  it('never resolves both channels to the SAME speaker (#775)', () => {
    // The remembered left is the live pair's RIGHT member, and the right channel
    // would otherwise fall back to that same live member, showing "B / B" in the
    // L and R slots and a bottom card that reads "no pair" while the real pair
    // sits up top. The two channels must stay distinct.
    const got = stereoSelectionPick({ left: 'B', right: '', liveIDs, candIDs });
    expect(got[0]).not.toEqual(got[1]);
    expect(got).toEqual(['B', 'A']);
  });
});

describe('storedPermanentGroupsOf', () => {
  it('frames a stored permanent group that is not live and resolves its members', () => {
    const boxes = [
      { deviceID: 'AAA', host: '10.0.0.1', kind: 'str' },
      { deviceID: 'BBB', host: '10.0.0.2', kind: 'str' },
      { deviceID: 'CCC', host: '10.0.0.3', kind: 'str' },
    ];
    const zoneLive = {
      AAA: { master: '', members: [], permanent: true, remembered: [{ ip: '10.0.0.2', deviceID: 'bbb' }, { ip: '10.0.0.9', name: 'Flur' }] },
      BBB: { master: '', members: [] },
      CCC: { master: '', members: [], remembered: [{ ip: '10.0.0.2' }] }, // remembered but NOT permanent
    };
    const got = storedPermanentGroupsOf(zoneLive, boxes);
    expect(got).toHaveLength(1);
    expect(got[0].masterKey).toBe('AAA');
    expect(got[0].members[0].box.deviceID).toBe('BBB');
    expect(got[0].members[1].box).toBeNull();
    expect(got[0].members[1].name).toBe('Flur');
  });
  it('does not frame a permanent group while it is live', () => {
    const boxes = [{ deviceID: 'AAA', host: '10.0.0.1', kind: 'str' }];
    const zoneLive = { AAA: { master: 'AAA', members: [{ deviceID: 'BBB', ip: '10.0.0.2' }], permanent: true, remembered: [{ ip: '10.0.0.2' }] } };
    expect(storedPermanentGroupsOf(zoneLive, boxes)).toEqual([]);
  });
});

// A speaker keeps ONE zone document, so pairing a speaker that belongs to a
// saved permanent group replaces that group with the pair and the group is gone
// with nothing to restore it from. While its main speaker is idle the group is
// invisible to every live check, which is exactly when the stereo picker used
// to offer its speakers as if they were free.
describe('storedGroupHostsOf', () => {
  const boxes = [
    { deviceID: 'AAA', host: '10.0.0.1', kind: 'str' },
    { deviceID: 'BBB', host: '10.0.0.2', kind: 'str' },
    { deviceID: 'CCC', host: '10.0.0.3', kind: 'str' },
  ];

  it('names the main speaker and every remembered member', () => {
    const zoneLive = {
      AAA: { master: '', members: [], permanent: true, remembered: [{ ip: '10.0.0.2', deviceID: 'bbb' }, { ip: '10.0.0.9', name: 'Flur' }] },
      BBB: { master: '', members: [] },
      CCC: { master: '', members: [] },
    };
    const got = storedGroupHostsOf(zoneLive, boxes);
    expect(got.has('10.0.0.1')).toBe(true); // the main speaker
    expect(got.has('10.0.0.2')).toBe(true); // a member the app has discovered
    expect(got.has('10.0.0.9')).toBe(true); // a member it has not: the saved address still counts
    expect(got.has('10.0.0.3')).toBe(false); // a free speaker stays free
  });

  it('is empty without a saved group, so nothing is hidden for no reason', () => {
    expect(storedGroupHostsOf({ AAA: { master: '', members: [] } }, boxes).size).toBe(0);
    expect(storedGroupHostsOf({}, boxes).size).toBe(0);
    expect(storedGroupHostsOf(undefined, undefined).size).toBe(0);
  });
});

describe('groupCount', () => {
  const boxes = [master, boxA, boxB, stock];

  it('is 0 with no zone data', () => {
    expect(groupCount({}, boxes)).toBe(0);
    expect(groupCount(undefined, boxes)).toBe(0);
  });

  it('counts one live zone once, however many members report it', () => {
    expect(groupCount(liveMap(), boxes)).toBe(1);
  });

  it('counts a zone whose followers dropped out of discovery, from the master row alone', () => {
    const zl = { [master.deviceID]: liveMap()[master.deviceID] };
    expect(groupCount(zl, [master])).toBe(1);
  });

  it('counts a zone a follower alone reports (master not discovered)', () => {
    const zl = { [boxA.deviceID]: liveMap()[boxA.deviceID] };
    expect(groupCount(zl, [boxA, boxB])).toBe(1);
  });

  it('ignores a master whose member list holds only itself', () => {
    const zl = { [master.deviceID]: { master: master.deviceID, members: [{ deviceID: master.deviceID, ip: master.host }] } };
    expect(groupCount(zl, boxes)).toBe(0);
  });

  it('counts two separate zones as two', () => {
    const zl = {
      [master.deviceID]: { master: master.deviceID, members: [{ deviceID: master.deviceID, ip: master.host }, { deviceID: boxA.deviceID, ip: boxA.host }] },
      [boxA.deviceID]: { master: master.deviceID, members: [] },
      [boxB.deviceID]: { master: boxB.deviceID, members: [{ deviceID: boxB.deviceID, ip: boxB.host }, { ip: '192.0.2.7' }] },
    };
    expect(groupCount(zl, boxes)).toBe(2);
  });

  it('adds a stored permanent group that is not live', () => {
    const zl = {
      ...liveMap(),
      [boxB.deviceID]: { master: '', members: [], permanent: true, remembered: [{ ip: '192.0.2.7', name: 'Kueche' }] },
    };
    expect(groupCount(zl, boxes)).toBe(2);
  });

  it('counts a live permanent group once, not as live plus stored', () => {
    const zl = liveMap();
    zl[master.deviceID] = { ...zl[master.deviceID], permanent: true, remembered: [{ ip: boxA.host }, { ip: boxB.host }] };
    expect(groupCount(zl, boxes)).toBe(1);
  });

  it('does not count a stereo pair as a group', () => {
    const zl = {
      [boxA.deviceID]: { master: '', members: [], stereo: { id: 'pair-1', masterDeviceID: boxA.deviceID, members: [{ deviceID: boxA.deviceID, role: 'LEFT' }, { deviceID: boxB.deviceID, role: 'RIGHT' }] } },
      [boxB.deviceID]: { master: '', members: [], stereo: { id: 'pair-1', masterDeviceID: boxA.deviceID, members: [{ deviceID: boxA.deviceID, role: 'LEFT' }, { deviceID: boxB.deviceID, role: 'RIGHT' }] } },
    };
    expect(groupCount(zl, boxes)).toBe(0);
  });
});

describe('onZoneLive', () => {
  beforeEach(() => resetZoneLivePoll());

  it('runs listeners after a completed poll round and on notifyZoneLive', async () => {
    let calls = 0;
    const off = onZoneLive(() => { calls++; });
    state.zoneLive = {};
    const fetchZone = async () => ({ master: master.deviceID, members: [] });
    await fetchZoneLive([master, boxA], { maxAgeMs: 0, minBoxes: 1 }, fetchZone);
    expect(calls).toBe(1);
    notifyZoneLive();
    expect(calls).toBe(2);
    off();
    notifyZoneLive();
    expect(calls).toBe(2);
  });

  it('keeps polling when a listener throws', async () => {
    const off = onZoneLive(() => { throw new Error('boom'); });
    state.zoneLive = {};
    const fetchZone = async () => ({ master: master.deviceID, members: [] });
    await expect(fetchZoneLive([master], { maxAgeMs: 0, minBoxes: 1 }, fetchZone)).resolves.toBe(true);
    expect(state.zoneLive[master.deviceID]).toBeTruthy();
    off();
  });
});

// The master key comes from the SPEAKERS' zone documents, which name the master
// by the firmware's SoundTouch deviceID (the SCM MAC). The app's box record
// carries whatever the app resolved for that speaker, and the two are not
// always the same value. When they disagree, a plain deviceID lookup finds no
// box at all: the Multi-Room frame then printed the raw hex key as its name and
// its x had nothing to dissolve (field bundle 2026-09-07).
describe('masterBoxForKey', () => {
  // The firmware id of `master`, deliberately not the one it announces.
  const fwID = 'FF99EE88DD07';
  // What every speaker answers while `master` leads and A and B follow, with
  // the master named by its FIRMWARE id throughout.
  function fwMap() {
    return {
      [master.deviceID]: {
        master: fwID,
        senderIP: master.host,
        members: [{ deviceID: boxA.deviceID, ip: boxA.host }, { deviceID: boxB.deviceID, ip: boxB.host }],
      },
      [boxA.deviceID]: { master: fwID, senderIP: master.host, members: [{ deviceID: boxA.deviceID, ip: boxA.host }] },
      [boxB.deviceID]: { master: fwID, senderIP: master.host, members: [{ deviceID: boxB.deviceID, ip: boxB.host }] },
    };
  }

  it('resolves by deviceID when the two identities agree', () => {
    expect(masterBoxForKey(master.deviceID, liveMap(), [master, boxA, boxB])).toBe(master);
  });

  it('takes the key in any case', () => {
    expect(masterBoxForKey(master.deviceID.toLowerCase(), liveMap(), [master, boxA, boxB])).toBe(master);
  });

  it('resolves by the leader address when the announced id is not the firmware id', () => {
    expect(masterBoxForKey(fwID, fwMap(), [master, boxA, boxB])).toBe(master);
  });

  it('resolves by boxDeviceID, the second identity a speaker publishes', () => {
    // An up-to-date agent announces what its own firmware calls it next to its
    // deviceID, so the key from a zone document matches directly - no address
    // and no group shape needed. Nothing about the announced deviceID changes:
    // the same box still answers to it.
    const named = { ...master, boxDeviceID: fwID };
    expect(masterBoxForKey(fwID, {}, [named, boxA, boxB])).toBe(named);
    expect(masterBoxForKey(fwID.toLowerCase(), {}, [named, boxA, boxB])).toBe(named);
    expect(masterBoxForKey(named.deviceID, {}, [named, boxA, boxB])).toBe(named);
  });

  it('refuses a senderIP whose box does not name the same master', () => {
    // A senderIP left over from a group that has since been rebuilt: the box at
    // that address leads something else now and must not be nominated. Nothing
    // else identifies the leader here, so the answer is null, not a stranger.
    const zl = {
      [boxA.deviceID]: { master: fwID, senderIP: master.host, members: [{ deviceID: boxA.deviceID, ip: boxA.host }] },
      [master.deviceID]: { master: 'CC00CC00CC00', senderIP: boxB.host, members: [] },
    };
    expect(masterBoxForKey(fwID, zl, [master, boxA, boxB])).toBeNull();
  });

  it('falls back to the group shape when no address identifies the leader', () => {
    // No senderIP anywhere: a follower's answer lists only itself and the
    // leader's lists the others, so the one that does not list itself leads.
    const zl = fwMap();
    for (const k of Object.keys(zl)) delete zl[k].senderIP;
    expect(masterBoxForKey(fwID, zl, [master, boxA, boxB])).toBe(master);
  });

  it('returns null rather than guessing when nothing resolves', () => {
    expect(masterBoxForKey(fwID, {}, [master, boxA, boxB])).toBeNull();
    expect(masterBoxForKey('', liveMap(), [master, boxA, boxB])).toBeNull();
    expect(masterBoxForKey(master.deviceID, liveMap(), [])).toBeNull();
  });

  it('never returns a stock speaker', () => {
    const stockLeader = { ...stock, deviceID: fwID };
    expect(masterBoxForKey(fwID, {}, [stockLeader])).toBeNull();
  });
});

// The field bundle itself (gh883, 2026-09-07), because the shapes above are
// tidier than what a speaker actually answers:
//   - the firmware drops the MASTER's own row from /getZone, so the master's
//     members[] holds the followers and a FOLLOWER's holds every follower
//     including itself,
//   - the master's own answer carries NO senderIPAddress at all; only the
//     followers' answers name the leader's address,
//   - exactly one box (the leader) announces an id that is not its firmware id.
// So neither the id nor the leader's own answer can find it: it is a follower's
// senderIP, or the group shape, or nothing.
describe('masterBoxForKey on the field bundle shape', () => {
  const fwID = 'FF99EE88DD07'; // the leader's SCM MAC, what every speaker names
  const followers = [
    { deviceID: boxA.deviceID, ip: boxA.host },
    { deviceID: boxB.deviceID, ip: boxB.host },
  ];
  const bundleMap = (withSenderIP) => ({
    [master.deviceID]: { master: fwID, members: followers },
    [boxA.deviceID]: { master: fwID, ...(withSenderIP ? { senderIP: master.host } : {}), members: followers },
    [boxB.deviceID]: { master: fwID, ...(withSenderIP ? { senderIP: master.host } : {}), members: followers },
  });

  it('finds the leader from a FOLLOWER senderIP when the leader itself reports none', () => {
    expect(masterBoxForKey(fwID, bundleMap(true), [master, boxA, boxB])).toBe(master);
  });

  it('finds it by group shape when not one answer carries a senderIP', () => {
    // The leader is the only box whose own members[] does not list itself.
    expect(masterBoxForKey(fwID, bundleMap(false), [master, boxA, boxB])).toBe(master);
  });

  it('finds it with the leader box listed last, so order is not doing the work', () => {
    expect(masterBoxForKey(fwID, bundleMap(true), [boxA, boxB, master])).toBe(master);
    expect(masterBoxForKey(fwID, bundleMap(false), [boxA, boxB, master])).toBe(master);
  });

  it('gives the caller a name to print, never the raw key', () => {
    const mb = masterBoxForKey(fwID, bundleMap(true), [master, boxA, boxB]);
    expect(mb).not.toBeNull();
    expect(mb.deviceID).not.toBe(fwID);
  });

  it('answers null when the leader is not discovered at all', () => {
    // Only the two followers are in the box list. Their senderIP points at a
    // box the app cannot see and each of them lists itself, so there is no
    // leader to nominate and a guess would send a dissolve to a follower.
    expect(masterBoxForKey(fwID, bundleMap(true), [boxA, boxB])).toBeNull();
  });
});
