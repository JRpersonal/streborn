import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// The phone remote's update card says beforehand that a grouped speaker leaves
// its group when it restarts for the update (#1339). A reporter updated the
// main speaker of a three-speaker group with "Only this speaker" and the note
// never showed: the page had read the group before it was formed in the
// desktop app, and its slow group poll only kept asking while a group was
// already known (or the Speakers tab was open), so the More tab never learned
// about it. A single failed read had the same effect on a known group. These
// tests lift the group-state code off the served page, the same way
// phonestoppedkey.test.js does it, and run it against the two shapes the
// agent's /api/box/zone/volume serves: the leader's and a follower's.

const here = dirname(fileURLToPath(import.meta.url));
const page = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8');

function lift(signature) {
  const start = page.indexOf(signature);
  expect(start, `${signature} missing from the phone page`).toBeGreaterThan(-1);
  const end = page.indexOf('\n}\n', start);
  return page.slice(start, end + 2);
}

const src = [
  lift('function suGroupChanged('),
  lift('function groupNoteDue('),
  lift('function zonePollDue('),
  lift('async function loadZone('),
].join('\n');

// What the agent answers on the speaker that leads the group: the stored
// group document, the leader first and flagged as this speaker.
const leader = {
  grouped: true, stereo: false, average: 20, loudest: 25,
  members: [
    { name: 'Living room', ip: '127.0.0.1', isSelf: true, isMaster: true, volume: 20 },
    { name: 'Kitchen', ip: '192.0.2.4', isSelf: false, isMaster: false, volume: 25 },
    { name: 'Bath', ip: '192.0.2.60', isSelf: false, isMaster: false, volume: 15 },
  ],
};
// What a follower answers: the leader's list, read from the leader, with this
// speaker flagged as self but not as the leader.
const follower = {
  grouped: true, stereo: false, average: 20, loudest: 25,
  members: [
    { name: 'Living room', ip: '192.0.2.10', isSelf: false, isMaster: true, volume: 20 },
    { name: 'Kitchen', ip: '127.0.0.1', isSelf: true, isMaster: false, volume: 25 },
    { name: 'Bath', ip: '192.0.2.60', isSelf: false, isMaster: false, volume: 15 },
  ],
};
const pair = {
  grouped: true, stereo: true, stereoName: 'Pair', average: 20, loudest: 20,
  members: [
    { name: 'Left', ip: '127.0.0.1', isSelf: true, isMaster: true, volume: 20 },
    { name: 'Right', ip: '192.0.2.4', isSelf: false, isMaster: false, volume: 20 },
  ],
};

function load({ panels = {} } = {}) {
  const env = { answers: [], renders: 0 };
  const els = {
    panelSpeakers: { hidden: panels.speakers !== true },
    panelMore: { hidden: panels.more !== true },
  };
  const document = { getElementById: (id) => els[id] || null };
  const window = { suRender: () => { env.renders++; } };
  const fns = new Function('document', 'window', 'env', 'T',
    `var zone = { grouped:false, stereo:false, pairName:'', members:[], master:false, masterName:'' };
     var zoneSeq = 0, volScope = 'one', volLast = 0, lastBoxName = 'This speaker';
     var grpVol = { pending: null, shown: null };
     function grpVolSettling() { return false; }
     function grpVolAwaitingConfirm() { return false; }
     function fmt(s) { return s; }
     function updateTopName() {}
     function applyScopeUI() {}
     function renderMembers() {}
     function renderGroupAdd() {}
     function api() { return env.answers.shift(); }
     ${src}
     return { loadZone, groupNoteDue, zonePollDue, zone: function(){ return zone; },
              noteDue: function(rows){ return groupNoteDue(zone, rows); } };`)(
    document, window, env, { grpSum: '', scPair: '', grp: '', permGrp: '', permGrpSum: '', unpair: '', ungroup: '', grpFollow: '', pairSum: '' },
  );
  return { ...fns, env, els };
}

describe('phone update card: group note', () => {
  it('is due on the leader and on a follower, never on a stereo pair', async () => {
    for (const [shape, want] of [[leader, true], [follower, true], [pair, false], [{ grouped: false }, false]]) {
      const p = load();
      p.env.answers.push(Promise.resolve(shape));
      await p.loadZone();
      expect(p.noteDue(), JSON.stringify(shape).slice(0, 40)).toBe(want);
    }
  });

  it('marks the leader and the follower correctly', async () => {
    const a = load();
    a.env.answers.push(Promise.resolve(leader));
    await a.loadZone();
    expect(a.zone().master).toBe(true);
    const b = load();
    b.env.answers.push(Promise.resolve(follower));
    await b.loadZone();
    expect(b.zone().master).toBe(false);
    expect(b.zone().grouped).toBe(true);
  });

  it('warns on the fleet plan when the run restarts this speaker or a member', async () => {
    const p = load();
    p.env.answers.push(Promise.resolve(follower));
    await p.loadZone();
    // This speaker is waiting.
    expect(p.noteDue([{ self: true, state: 'waiting' }])).toBe(true);
    // Only another member is waiting.
    expect(p.noteDue([{ self: true, state: 'current' }, { ip: '192.0.2.60', state: 'waiting' }])).toBe(true);
    // The leader is waiting.
    expect(p.noteDue([{ self: true, state: 'current' }, { ip: '192.0.2.10', state: 'waiting' }])).toBe(true);
    // Only a speaker outside the group is waiting.
    expect(p.noteDue([{ self: true, state: 'current' }, { ip: '192.0.2.99', state: 'waiting' }])).toBe(false);
  });

  it('repaints the update card when the group forms and when it ends', async () => {
    const p = load();
    p.env.answers.push(Promise.resolve({ grouped: false }));
    await p.loadZone();
    expect(p.env.renders).toBe(0); // alone before, alone now
    p.env.answers.push(Promise.resolve(leader));
    await p.loadZone();
    expect(p.env.renders).toBe(1);
    p.env.answers.push(Promise.resolve(leader));
    await p.loadZone();
    expect(p.env.renders).toBe(1); // unchanged, no repaint
    p.env.answers.push(Promise.resolve({ grouped: false }));
    await p.loadZone();
    expect(p.env.renders).toBe(2);
  });

  it('keeps a known group through a failed read', async () => {
    const p = load();
    p.env.answers.push(Promise.resolve(leader));
    await p.loadZone();
    p.env.answers.push(Promise.resolve(null));
    await p.loadZone();
    expect(p.zone().grouped).toBe(true);
    expect(p.noteDue()).toBe(true);
  });

  it('lets only the newest read decide', async () => {
    const p = load();
    let late;
    p.env.answers.push(new Promise((r) => { late = r; })); // started first, answers last
    p.env.answers.push(Promise.resolve(leader));
    const first = p.loadZone();
    await p.loadZone();
    late({ grouped: false });
    await first;
    expect(p.zone().grouped).toBe(true);
  });

  it('keeps polling the group while the More tab with the update card is open', () => {
    expect(load({ panels: { more: true } }).zonePollDue()).toBe(true);
    expect(load({ panels: { speakers: true } }).zonePollDue()).toBe(true);
    expect(load().zonePollDue()).toBe(false);
  });
});
