// The UI 2.0 sidebar speaker list (#1284): one row per speaker, the playing
// group pinned, "+" and "-" exactly where the group edit would act.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { actionLabel, rovingIndex, sidebarHTML, sidebarModel, speakerRowLabel } from './speakers.js';
import { groupMemberBoxes, stereoPairsOf } from '../groups.js';
import en from '../i18n/bundles/en.json';
import { setLocale } from '../i18n/index.js';

setLocale('en');

const css = readFileSync(new URL('./shell2.css', import.meta.url), 'utf8');

const box = (n, id, name, extra = {}) => ({ host: `192.0.2.${n}`, port: 8888, deviceID: id, kind: 'STR', friendlyName: name, ...extra });
const M = box(20, 'MMM', 'Living room');
const F = box(21, 'FFF', 'Kitchen');
const P1 = box(22, 'P1P', 'Terrace left');
const P2 = box(23, 'P2P', 'Terrace right');
const O = box(24, 'OOO', 'Garage', { offline: true });
const S = box(25, 'SSS', 'Bedroom', { kind: 'stock', port: 8090 });
const I1 = box(26, 'II1', 'Office');
const I2 = box(27, 'II2', 'Hall');
const I3 = box(28, 'II3', 'Bath');
const NET = [M, F, P1, P2, O, S, I1, I2, I3];

const ZONES = {
  MMM: { master: 'MMM', members: [{ deviceID: 'MMM', ip: M.host }, { deviceID: 'FFF', ip: F.host }] },
  FFF: { master: 'MMM' },
  P1P: { stereo: { masterDeviceID: 'P1P', name: 'Terrace', members: [{ deviceID: 'P1P', role: 'LEFT' }, { deviceID: 'P2P', role: 'RIGHT' }] } },
  P2P: {},
  II1: {},
  II2: {},
  II3: {},
};
const PLAYING = { MMM: true, FFF: true, P1P: false, P2P: false, II1: false, II2: false, II3: false };

function model(over = {}) {
  const zoneLive = over.zoneLive || ZONES;
  return sidebarModel(over.boxes || NET, {
    currentBox: M,
    zoneLive,
    stereoPairs: stereoPairsOf(zoneLive),
    boxPlaying: PLAYING,
    pairName: (p) => p.name || '',
    ...over,
  });
}
const byHost = (m, b) => [...m.pinned, ...m.others].find((r) => r.hosts.includes(b.host));

describe('sidebar speaker model', () => {
  it('lists every speaker once, a live pair as one row named "(stereo)"', () => {
    const m = model();
    const rows = [...m.pinned, ...m.others];
    expect(rows).toHaveLength(8);
    const pair = byHost(m, P2);
    expect(pair).toBe(byHost(m, P1));
    expect(pair.kind).toBe('pair');
    expect(pair.name).toBe('Terrace (stereo)');
    expect(pair.pill).toBe('');
    expect(pair.action).toBe('');
    expect(pair.host).toBe(P1.host);
  });

  it('pins the group you control, master first, and keeps the others in list order', () => {
    const m = model();
    expect(m.pinned.map((r) => r.host)).toEqual([M.host, F.host]);
    expect(m.headingKey).toBe('ui2.side.together');
    expect(m.others.map((r) => r.host)).toEqual([P1.host, O.host, S.host, I1.host, I2.host, I3.host]);
    // A selected follower pins its master's group, master first.
    const f = model({ currentBox: F });
    expect(f.pinned.map((r) => r.host)).toEqual([M.host, F.host]);
    // Alone and with nothing playing, the speaker you control is pinned by itself.
    const solo = model({ currentBox: I1, boxPlaying: { ...PLAYING, MMM: false, FFF: false } });
    expect(solo.pinned.map((r) => r.host)).toEqual([I1.host]);
    expect(solo.headingKey).toBe('ui2.side.controlling');
    expect(solo.memberCount).toBe(0);
  });

  it('pins the group that plays while you control an idle speaker, and "+" adds to it (#1284)', () => {
    // You control the idle Office while Living room + Kitchen play.
    const m = model({ currentBox: I1 });
    expect(m.pinned.map((r) => r.host)).toEqual([M.host, F.host]);
    expect(m.headingKey).toBe('ui2.side.together');
    const office = byHost(m, I1);
    expect(office.pinned).toBe(false);
    expect(office.isCurrent).toBe(true);
    // "+" on Hall (and on the Office you control) goes to the playing group's master.
    expect(byHost(m, I2)).toMatchObject({ action: 'add', master: M.host });
    expect(byHost(m, I1)).toMatchObject({ action: 'add', master: M.host });
    expect(byHost(m, F)).toMatchObject({ action: 'remove', master: M.host });
    expect(sidebarHTML(m)).toMatch(/data-host="192\.0\.2\.27"[^>]*data-master="192\.0\.2\.20"/);
    // The player still counts the speaker you control, which plays alone.
    expect(m.memberCount).toBe(0);
    // A single speaker playing that you do not control is headed "Playing".
    const one = model({ currentBox: I1, boxPlaying: { ...PLAYING, MMM: false, FFF: false, II2: true } });
    expect(one.pinned.map((r) => r.host)).toEqual([I2.host]);
    expect(one.headingKey).toBe('ui2.pill.playing');
    expect(byHost(one, I1)).toMatchObject({ action: 'add', master: I2.host });
    // Nothing plays: the speaker you control is pinned, to build a group before Play.
    const quiet = model({ currentBox: I1, boxPlaying: { ...PLAYING, MMM: false, FFF: false } });
    expect(quiet.pinned.map((r) => r.host)).toEqual([I1.host]);
    expect(quiet.headingKey).toBe('ui2.side.controlling');
    expect(byHost(quiet, I2)).toMatchObject({ action: 'add', master: I1.host });
    // Your own group plays too: yours comes first, the other one stays below.
    const both = model({
      currentBox: I1,
      boxPlaying: { ...PLAYING, II1: true },
    });
    expect(both.pinned.map((r) => r.host)).toEqual([I1.host]);
    expect(byHost(both, M).pinned).toBe(false);
  });

  it('offers "+" only on idle, online, known STR speakers outside the group', () => {
    const m = model();
    expect([...m.pinned, ...m.others].filter((r) => r.action === 'add').map((r) => r.host)).toEqual([I1.host, I2.host, I3.host]);
    for (const b of [P1, O, S]) expect(byHost(m, b).action, b.friendlyName).toBe('');
    // Playing elsewhere, play state not read yet, or in another zone: no "+".
    const busy = model({ boxPlaying: { ...PLAYING, II1: true } });
    expect(byHost(busy, I1).action).toBe('');
    const { II2: _unknown, ...noI2 } = PLAYING;
    expect(byHost(model({ boxPlaying: noI2 }), I2).action).toBe('');
    const zoned = model({ zoneLive: { ...ZONES, II3: { master: 'XXX' } } });
    expect(byHost(zoned, I3).action).toBe('');
    const { II3: _gone, ...noZoneI3 } = ZONES;
    expect(byHost(model({ zoneLive: noZoneI3 }), I3).action).toBe('');
  });

  it('offers nothing while you control a stock speaker and nothing plays, or a stereo pair', () => {
    const stock = model({ currentBox: S, boxPlaying: { ...PLAYING, MMM: false, FFF: false } });
    expect(stock.pinned).toEqual([]);
    expect([...stock.pinned, ...stock.others].some((r) => r.action)).toBe(false);
    // With music playing, a Bose original selected still lets you add to it.
    const busy = model({ currentBox: S });
    expect(busy.pinned.map((r) => r.host)).toEqual([M.host, F.host]);
    expect(byHost(busy, I1)).toMatchObject({ action: 'add', master: M.host });
    const pair = model({ currentBox: P2, boxPlaying: { ...PLAYING, MMM: false, FFF: false } });
    expect(pair.pairAlone).toBe(true);
    expect(pair.groupable).toBe(false);
    expect(pair.pinned.map((r) => r.kind)).toEqual(['pair']);
    expect([...pair.pinned, ...pair.others].some((r) => r.action)).toBe(false);
    expect(sidebarHTML(pair)).toContain(en['ui2.side.pairAlone']);
  });

  it('offers "-" on the members, never on the master, from the edit\'s own membership', () => {
    const m = model();
    expect(byHost(m, M).action).toBe('');
    expect(byHost(m, F).action).toBe('remove');
    // A follower only the master still lists (its own report is missing).
    const { FFF: _self, ...masterOnly } = ZONES;
    const k = model({ zoneLive: masterOnly });
    expect(k.pinned.map((r) => r.host)).toEqual([M.host, F.host]);
    expect(byHost(k, F).action).toBe('remove');
  });

  it('says "Playing together" only while the group plays', () => {
    const idle = model({ boxPlaying: { ...PLAYING, MMM: false, FFF: false } });
    expect(idle.pinned.map((r) => r.host)).toEqual([M.host, F.host]);
    expect(idle.headingKey).toBe('ui2.side.group');
    expect(sidebarHTML(idle)).toContain(en['ui2.side.group']);
    expect(model().headingKey).toBe('ui2.side.together');
  });

  it('counts the same members the player shows, a master-listed one included', () => {
    // Kitchen has no self-report; only the master's member list names it.
    const { FFF: _self, ...masterOnly } = ZONES;
    const k = model({ zoneLive: masterOnly });
    expect(k.memberCount).toBe(1);
    expect(groupMemberBoxes(M, masterOnly, NET)).toEqual([F]);
    // A member the master lists that discovery does not know has no row and no
    // slider, so it is not counted either.
    const ghost = { ...masterOnly, MMM: { master: 'MMM', members: [...masterOnly.MMM.members, { deviceID: 'GGG', ip: '192.0.2.99' }] } };
    expect(model({ zoneLive: ghost }).memberCount).toBe(1);
    expect(groupMemberBoxes(M, ghost, NET)).toEqual([F]);
  });

  it('never offers a lone pair half for a group', () => {
    const m = model({ boxes: [M, F, P1, I1] });
    const half = byHost(m, P1);
    expect(half.kind).toBe('str');
    expect(half.action).toBe('');
  });

  it('works for nine speakers: a group of two pinned, six others below', () => {
    const m = model();
    expect(m.pinned).toHaveLength(2);
    expect(m.others).toHaveLength(6);
  });

  it('pins a big group whole, and the pinned list is capped so the navigation stays', () => {
    const many = Array.from({ length: 8 }, (_, i) => box(40 + i, `G${i}G`, `Room ${i}`));
    const zl = {
      G0G: { master: 'G0G', members: many.map((b) => ({ deviceID: b.deviceID, ip: b.host })) },
      ...Object.fromEntries(many.slice(1).map((b) => [b.deviceID, { master: 'G0G' }])),
    };
    const m = model({ boxes: [...many, I1], currentBox: many[0], zoneLive: zl, stereoPairs: [] });
    expect(m.pinned).toHaveLength(8);
    expect(m.memberCount).toBe(7);
    expect(css).toMatch(/\.ui2-spk-pinned \.ui2-spk-list \{[^}]*max-height: calc\(var\(--u-spk-row\) \* var\(--u-spk-pinned\)\)[^}]*overflow-y: auto/);
    expect(css).toMatch(/\.ui2-spk-scroll \{[^}]*max-height: calc\(var\(--u-spk-row\) \* var\(--u-spk-visible\)\)[^}]*overflow-y: auto/);
  });
});

describe('sidebar speaker markup', () => {
  it('says Offline in words and in the name, and keeps the row clickable', () => {
    const out = sidebarHTML(model());
    const row = out.match(/<li class="[^"]*is-offline[^"]*">[\s\S]*?<\/li>/)[0];
    expect(row).toContain(`>${en['ui2.pill.offline']}<`);
    expect(row).toContain(`aria-label="Garage, ${en['ui2.pill.offline']}"`);
    expect(row).not.toContain('disabled');
  });

  it('names a Bose original as ready for STR and that it opens the setup', () => {
    const m = model();
    const s = byHost(m, S);
    expect(speakerRowLabel(s)).toBe(`Bedroom, ${en['speaker.needsInstallBadge']}. Opens the setup`);
    expect(sidebarHTML(m)).toContain(en['speaker.needsInstallBadge']);
  });

  it('marks exactly one current row and gives Tab to one row', () => {
    const out = sidebarHTML(model());
    expect(out.match(/aria-current="true"/g)).toHaveLength(1);
    expect(out).toMatch(/data-host="192\.0\.2\.20"[^>]*tabindex="0"[^>]*aria-current="true"/);
    expect(out.match(/class="ui2-spk-pick"[^>]*tabindex="0"/g)).toHaveLength(1);
    // Only that row's action could take Tab too; the master has none.
    expect(out.match(/ui2-spk-act[^>]*tabindex="0"/g)).toBeNull();
    const moved = sidebarHTML(model(), { focusHost: F.host });
    expect(moved).toMatch(/class="ui2-spk-pick" data-host="192\.0\.2\.21"[^>]*tabindex="0"/);
    expect(moved.match(/ui2-spk-act[^>]*tabindex="0"/g)).toHaveLength(1);
  });

  it('names the action with the speaker, and says while it runs', () => {
    const m = model({ pending: new Set([I1.host]) });
    const r = byHost(m, I1);
    expect(r.pending).toBe(true);
    expect(actionLabel(r)).toBe('Adding Office...');
    expect(actionLabel(byHost(m, I2))).toBe('Play on Hall too');
    expect(actionLabel(byHost(m, F))).toBe('Take Kitchen out');
    const out = sidebarHTML(m);
    expect(out).toMatch(/class="u-btn u-btn--icon ui2-spk-act pending"[^>]*data-host="192\.0\.2\.26"[^>]*aria-busy="true"/);
    expect(out.match(/aria-busy="true"/g)).toHaveLength(1);
  });

  it('includes the state in every row name', () => {
    const m = model();
    expect(speakerRowLabel(byHost(m, M))).toBe(`Living room, ${en['ui2.pill.playing']}`);
    expect(speakerRowLabel(byHost(m, I1))).toBe('Office');
  });

  it('escapes speaker names', () => {
    const evil = box(30, 'EEE', '<img src=x onerror=alert(1)>');
    const out = sidebarHTML(model({ boxes: [M, evil] }));
    expect(out).not.toContain('<img');
    expect(out).toContain('&lt;img');
  });

  it('explains an empty list', () => {
    const empty = { pinned: [], others: [] };
    expect(sidebarHTML({ ...empty, searching: true })).toContain(en['speaker.searching']);
    expect(sidebarHTML(empty)).toContain(en['ui2.side.empty']);
  });
});

describe('rovingIndex', () => {
  it('steps and stops at the ends, jumps with Home and End', () => {
    expect(rovingIndex('ArrowDown', 0, 3)).toBe(1);
    expect(rovingIndex('ArrowDown', 2, 3)).toBe(2);
    expect(rovingIndex('ArrowUp', 0, 3)).toBe(0);
    expect(rovingIndex('ArrowUp', 2, 3)).toBe(1);
    expect(rovingIndex('Home', 2, 3)).toBe(0);
    expect(rovingIndex('End', 0, 3)).toBe(2);
    expect(rovingIndex('ArrowDown', -1, 3)).toBe(0);
  });
  it('leaves every other key alone', () => {
    expect(rovingIndex('Enter', 1, 3)).toBe(-1);
    expect(rovingIndex('ArrowRight', 1, 3)).toBe(-1);
    expect(rovingIndex('ArrowDown', 0, 0)).toBe(-1);
  });
});

// Phase 5: the states of the list. The empty list says why from the
// discovery itself, every speaker unreachable is said above the rows, and a
// row carries a note (an offline pick, a failed group edit) and the speaker
// package's attention chip, all part of the model and so of the signature.
describe('sidebar states (phase 5)', () => {
  const sig = (m) => JSON.stringify({ pinned: m.pinned, others: m.others, empty: m.emptyPhase, allOffline: m.allOffline });

  it('stays on searching until discovery is done, never a guess from an empty list', () => {
    for (const discovery of [null, { phase: 'searching', last: null }]) {
      const m = sidebarModel([], { discovery });
      expect(m.emptyPhase).toBe('searching');
      expect(m.searching).toBe(true);
      expect(sidebarHTML(m)).toContain(en['ui2.state.searchingTitle']);
    }
    const done = sidebarModel([], { discovery: { phase: 'done', found: 0 } });
    expect(done.emptyPhase).toBe('none');
    const html = sidebarHTML(done);
    expect(html).toContain(en['ui2.side.empty']);
    // Search again, Add speaker, Connect by address, in that order.
    expect([...html.matchAll(/data-state-act="(\w+)"/g)].map((x) => x[1])).toEqual(['search', 'add', 'connect']);
  });

  it('shows a failed search with its detail folded and Search again', () => {
    const m = sidebarModel([], { discovery: { phase: 'failed', error: 'mdns: no interface' } });
    expect(m.emptyPhase).toBe('failed');
    const html = sidebarHTML(m);
    expect(html).toContain(en['ui2.state.failedTitle']);
    expect(html).toContain('<details class="u-state-detail">');
    expect(html).toContain('mdns: no interface');
    expect(html).toContain('data-state-act="search"');
    expect(html).toContain('role="alert"');
  });

  it('keeps the old one-line text for a model without the phase', () => {
    expect(sidebarHTML({ pinned: [], others: [] })).toContain('class="ui2-spk-empty"');
  });

  it('says all offline above the rows, never instead of them', () => {
    const off = [box(40, 'O1', 'Attic', { offline: true }), box(41, 'O2', 'Cellar', { offline: true })];
    const m = sidebarModel(off, { discovery: { phase: 'done' } });
    expect(m.allOffline).toBe(true);
    const html = sidebarHTML(m);
    const summary = html.indexOf('data-phase="allOffline"');
    expect(summary).toBeGreaterThan(-1);
    expect(summary).toBeLessThan(html.indexOf('class="ui2-spk-row'));
    expect((html.match(/class="ui2-spk-row/g) || []).length).toBe(2);
    expect(model().allOffline).toBe(false);
  });

  it('puts the attention chip into the row, its name and the signature', () => {
    const plain = model();
    const marked = model({ attention: (h) => h === I1.host });
    expect(byHost(plain, I1).attention).toBeUndefined();
    expect(byHost(marked, I1).attention).toBe(en['ui2.state.needsAttention']);
    expect(sig(marked)).not.toBe(sig(plain));
    const html = sidebarHTML(marked);
    expect(html).toContain(`data-chip="attention">${en['ui2.state.needsAttention']}</span>`);
    expect(speakerRowLabel(byHost(marked, I1))).toContain(en['ui2.state.needsAttention']);
    // Worded by the speaker package itself.
    expect(byHost(model({ attention: () => 'Update failed' }), I1).attention).toBe('Update failed');
    // Without the dep: nothing.
    expect(sidebarHTML(plain)).not.toContain('data-chip="attention"');
  });

  it('puts an offline pick note into the row model, the signature and the row', () => {
    const notes = new Map([[O.host, { tone: 'busy', text: 'Garage is not reachable. Searching again...', host: O.host }]]);
    const plain = model();
    const noted = model({ notes });
    expect(byHost(noted, O).note).toMatchObject({ tone: 'busy', text: 'Garage is not reachable. Searching again...' });
    expect(sig(noted)).not.toBe(sig(plain));
    const html = sidebarHTML(noted);
    expect(html).toMatch(/<li class="ui2-spk-row[^"]*has-note[^"]*">[\s\S]*?<p class="ui2-spk-note ui2-spk-rownote" data-tone="busy">Garage is not reachable\. Searching again\.\.\.<\/p><\/li>/);
    // Not a failure: no alert, said by the live region instead.
    expect(html).not.toContain('role="alert"');
    const still = model({ notes: new Map([[O.host, { tone: 'warn', text: 'Still not reachable.', action: 'whatToTry', host: O.host }]]) });
    expect(sidebarHTML(still)).toContain(`data-note-act="whatToTry" data-host="${O.host}" aria-label="What to try for Garage">What to try</button>`);
  });

  it('gives a failed group edit an alert and Try again with the same master', () => {
    const notes = { [I1.host]: { tone: 'error', text: 'The group did not change.', action: 'retry', master: M.host, port: 8888, host: I1.host } };
    const html = sidebarHTML(model({ notes }));
    expect(html).toContain('data-tone="error" role="alert">The group did not change.</p>');
    expect(html).toContain(`data-note-act="retry" data-host="${I1.host}" data-port="8888" data-master="${M.host}" aria-label="Try again for Office">Try again</button>`);
  });
});
