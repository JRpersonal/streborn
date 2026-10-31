// Speakers > list (#1287, phase 3): the pill precedence of a card, the fixed
// groups, the cards the list builds from the sidebar's rows, and the markup
// (named actions, words for every state, the offline card's note).
import { describe, it, expect } from 'vitest';
import {
  LIST_PILL_ORDER, agoText, fixedGroupRoles, listPillHTML, listPillKind, listPillLabel,
  speakerListHTML, speakerListModel,
} from './list.js';
import { setLocale } from '../../i18n/index.js';

setLocale('en');

const ALL = {
  offline: true, startingUp: true, strNotRunning: true, stock: true, playing: true,
  standby: true, updateReady: true, stereoPair: true, fixedGroup: true,
};
const FLAG = {
  notReachable: 'offline', startingUp: 'startingUp', strNotRunning: 'strNotRunning', boseOriginal: 'stock',
  playing: 'playing', standby: 'standby', updateReady: 'updateReady', stereoPair: 'stereoPair', fixedGroup: 'fixedGroup',
};

describe('listPillKind', () => {
  it('names every state alone', () => {
    for (const kind of LIST_PILL_ORDER) {
      const f = { [FLAG[kind]]: true };
      // A speaker whose STR stopped is a Bose original with the marker.
      if (kind === 'strNotRunning') f.stock = true;
      expect(listPillKind(f), kind).toBe(kind);
    }
    expect(listPillKind({})).toBe('');
    expect(listPillKind(null)).toBe('');
  });

  it('keeps the precedence: not reachable, starting up, kind, doing, needs, belongs', () => {
    expect(LIST_PILL_ORDER).toEqual(['notReachable', 'startingUp', 'strNotRunning', 'boseOriginal',
      'playing', 'standby', 'updateReady', 'stereoPair', 'fixedGroup']);
    // Take the winner away one by one: the next in line shows.
    const f = { ...ALL };
    const seen = [];
    for (;;) {
      const k = listPillKind(f);
      if (!k) break;
      seen.push(k);
      if (k === 'strNotRunning') f.strNotRunning = false;
      else f[FLAG[k]] = false;
    }
    expect(seen).toEqual(LIST_PILL_ORDER);
  });

  it('shows a repair, not a first install, for a speaker whose STR stopped', () => {
    expect(listPillKind({ stock: true, strNotRunning: true })).toBe('strNotRunning');
    expect(listPillKind({ stock: true })).toBe('boseOriginal');
  });

  it('says what a playing speaker does before it says it is due an update or paired', () => {
    expect(listPillKind({ playing: true, updateReady: true, stereoPair: true })).toBe('playing');
    expect(listPillKind({ updateReady: true, fixedGroup: true })).toBe('updateReady');
    expect(listPillKind({ offline: true, playing: true })).toBe('notReachable');
  });

  it('draws every pill with words, the dot hidden', () => {
    for (const kind of LIST_PILL_ORDER) {
      const html = listPillHTML(kind);
      expect(html, kind).toContain(`data-kind="${kind}"`);
      expect(html, kind).toContain('<span class="u-pill-dot" aria-hidden="true"></span>');
      expect(listPillLabel(kind), kind).toBeTruthy();
      expect(html, kind).toContain(listPillLabel(kind));
    }
    expect(listPillLabel('updateReady')).toBe('Update ready');
    expect(listPillLabel('fixedGroup')).toBe('Fixed group');
    expect(listPillLabel('boseOriginal')).toBe('Ready for STR');
    expect(listPillHTML('')).toBe('');
  });
});

const box = (n, id, name, extra = {}) => ({
  host: `192.0.2.${n}`, port: 8888, deviceID: id, friendlyName: name, model: 'SoundTouch 10',
  version: 'v1.0.19', kind: 'STR', ...extra,
});
const LR = box(50, 'AAA', 'Living room', { model: 'SoundTouch 30' });
const BED = box(52, 'BBB', 'Bedroom');
const OLD = box(54, 'CCC', 'Hallway', { model: 'SoundTouch 20', version: 'v1.0.10' });
const NUR = box(75, 'DDD', 'Nursery', { strSilent: true });
const OFF = box(53, 'EEE', 'Office', { offline: true, offlineSinceSec: 600 });
const TL = box(71, 'F01', 'Terrace left');
const TR = box(72, 'F02', 'Terrace right');
const DIN = box(73, 'F03', 'Dining room', { model: 'SoundTouch 20' });
const HALL = box(74, 'F04', 'Hall');
const KIT = { host: '192.0.2.51', port: 8090, deviceID: 'KKK', friendlyName: 'Kitchen', model: 'SoundTouch 10', kind: 'stock' };
const BOXES = [LR, BED, OLD, NUR, OFF, TL, TR, DIN, HALL, KIT];
const PAIR = { id: 'p', master: 'F01', name: 'Terrace', members: [{ deviceID: 'F01', ip: TL.host, role: 'LEFT' }, { deviceID: 'F02', ip: TR.host, role: 'RIGHT' }] };
const ZONES = {
  F03: { members: [], permanent: true, remembered: [{ deviceID: 'F04', ip: HALL.host, name: 'Hall' }] },
};

const ctx = (extra = {}) => ({
  currentBox: LR,
  nowPlayState: 'PLAY_STATE',
  nowSource: 'INTERNET_RADIO',
  nowName: 'Best Of Rock.FM',
  boxPlaying: { AAA: true },
  needsUpdate: (b) => b.version === 'v1.0.10',
  stereoPairs: [PAIR],
  zoneLive: ZONES,
  pairName: (p) => p.name,
  appVersion: 'v1.0.19',
  ...extra,
});

describe('fixedGroupRoles', () => {
  it('finds a stored fixed group: the main speaker and its members', () => {
    const roles = fixedGroupRoles(ZONES, BOXES);
    expect(roles.get(DIN.host)).toEqual({ role: 'main', members: ['Hall'] });
    expect(roles.get(HALL.host)).toEqual({ role: 'member', main: 'Dining room' });
    expect(roles.size).toBe(2);
  });

  it('finds a live fixed group by its members, and ignores a plain live group', () => {
    const live = { F03: { permanent: true, members: [{ deviceID: 'F03', ip: DIN.host }, { deviceID: 'F04', ip: HALL.host }] } };
    expect(fixedGroupRoles(live, BOXES).get(DIN.host)).toEqual({ role: 'main', members: ['Hall'] });
    const plain = { F03: { members: [{ deviceID: 'F03' }, { deviceID: 'F04' }] } };
    expect(fixedGroupRoles(plain, BOXES).size).toBe(0);
    expect(fixedGroupRoles(null, BOXES).size).toBe(0);
  });
});

describe('speakerListModel', () => {
  const model = speakerListModel(BOXES, ctx());
  const row = (host) => model.rows.find((r) => r.hosts.includes(host));

  it('has one card per speaker and one for the stereo pair, in the app order', () => {
    expect(model.rows.map((r) => r.name)).toEqual(['Living room', 'Bedroom', 'Hallway', 'Nursery', 'Office',
      'Terrace', 'Dining room', 'Hall', 'Kitchen']);
    expect(row(TL.host)).toBe(row(TR.host));
    expect(row(TL.host).hosts).toEqual([TL.host, TR.host]);
  });

  it('gives every speaker the pill of its state', () => {
    expect(row(LR.host).pill).toBe('playing');
    expect(row(OLD.host).pill).toBe('updateReady');
    expect(row(NUR.host).pill).toBe('startingUp');
    expect(row(OFF.host).pill).toBe('notReachable');
    expect(row(TL.host).pill).toBe('stereoPair');
    expect(row(DIN.host).pill).toBe('fixedGroup');
    expect(row(HALL.host).pill).toBe('fixedGroup');
    expect(row(KIT.host).pill).toBe('boseOriginal');
    // Standby is known for the speaker you control only, as in the sidebar.
    expect(row(BED.host).pill).toBe('');
    const asleep = speakerListModel(BOXES, ctx({ currentBox: BED, nowSource: 'STANDBY', nowPlayState: '', boxPlaying: {} }));
    expect(asleep.rows.find((r) => r.host === BED.host).pill).toBe('standby');
  });

  it('says what the speaker is, what plays and which group it belongs to', () => {
    expect(row(LR.host).meta).toBe('SoundTouch 30 · STR v1.0.19');
    expect(row(LR.host).now).toBe('Best Of Rock.FM');
    expect(row(BED.host).now).toBe('');
    expect(row(TL.host).meta).toBe('Stereo pair of Terrace left + Terrace right');
    // A pair without a name of its own is named by its halves: no repeat.
    const unnamed = speakerListModel(BOXES, ctx({ pairName: () => '' }));
    expect(unnamed.rows.find((r) => r.host === TL.host).name).toBe('Terrace left + Terrace right');
    expect(unnamed.rows.find((r) => r.host === TL.host).meta).toBe('SoundTouch 10');
    expect(row(KIT.host).meta).toBe('SoundTouch 10 · Bose original');
    expect(row(DIN.host).group).toBe('Main speaker of a fixed group with Hall');
    expect(row(HALL.host).group).toBe('In the fixed group of Dining room');
    expect(row(OLD.host).updateLine).toBe('STR v1.0.10 installed, v1.0.19 is ready. The speaker restarts once.');
  });

  it('offers the list-level actions: update, set up STR, nothing on an offline or starting speaker', () => {
    expect(row(OLD.host).action).toBe('update');
    expect(row(KIT.host).action).toBe('setup');
    expect(row(LR.host).action).toBe('');
    const due = speakerListModel(BOXES, ctx({ needsUpdate: () => true }));
    expect(due.rows.find((r) => r.host === OFF.host).action).toBe('');
    expect(due.rows.find((r) => r.host === NUR.host).action).toBe('');
    // A playing speaker that is due an update keeps "Playing" and gets the chip.
    expect(due.rows.find((r) => r.host === LR.host).pill).toBe('playing');
    expect(due.rows.find((r) => r.host === LR.host).chips).toEqual(['update']);
    expect(due.rows.find((r) => r.host === OLD.host).chips).toEqual([]);
    expect(due.rows.find((r) => r.host === OFF.host).chips).toEqual([]);
    // While its update runs, the card says so instead of offering it again.
    const running = speakerListModel(BOXES, ctx({ otaHost: OLD.host, otaRunning: true }));
    expect(running.rows.find((r) => r.host === OLD.host).action).toBe('updating');
    expect(running.updating).toBe(true);
  });

  it('counts the reachable speakers waiting for an update', () => {
    expect(model.waiting).toBe(1);
    expect(speakerListModel(BOXES, ctx({ needsUpdate: () => true })).waiting).toBe(8);
    expect(speakerListModel(BOXES, ctx({ needsUpdate: undefined })).waiting).toBe(0);
  });

  it('carries the offline speaker\'s last sighting and its search state', () => {
    expect(row(OFF.host).offlineSince).toBe(600);
    const s = speakerListModel(BOXES, ctx({ search: new Map([[OFF.host, 'still']]) }));
    expect(s.rows.find((r) => r.host === OFF.host).search).toBe('still');
  });

  it('marks the speaker you control', () => {
    expect(model.rows.filter((r) => r.current).map((r) => r.host)).toEqual([LR.host]);
  });

  it('keeps a way to each half of a stereo pair', () => {
    expect(row(TL.host).halves).toEqual([{ host: TL.host, name: 'Terrace left' }, { host: TR.host, name: 'Terrace right' }]);
    expect(row(LR.host).halves).toEqual([]);
  });

  it('offers no setup on a Bose original that is not reachable', () => {
    const gone = speakerListModel([{ ...KIT, offline: true }], ctx());
    expect(gone.rows[0].pill).toBe('notReachable');
    expect(gone.rows[0].action).toBe('');
  });
});

describe('speakerListHTML', () => {
  const html = speakerListHTML(speakerListModel(BOXES, ctx()));

  it('opens each speaker, a Bose original the setup, and marks the current one', () => {
    expect(html).toContain(`data-host="${LR.host}" data-route="speaker" aria-current="true"`);
    expect(html).toContain(`data-host="${KIT.host}" data-route="add" aria-label="Kitchen, Ready for STR. Opens the setup"`);
    expect((html.match(/aria-current="true"/g) || []).length).toBe(1);
    expect((html.match(/class="spl-open /g) || []).length).toBe(9);
  });

  it('names each half of a pair on its button, and searches for an offline Bose original', () => {
    expect(html).toContain(`data-act="half" data-host="${TR.host}">Settings of Terrace right</button>`);
    const gone = speakerListHTML(speakerListModel([{ ...KIT, offline: true }], ctx()));
    expect(gone).toContain(`data-host="${KIT.host}" data-route="search"`);
    expect(gone).not.toContain('Opens the setup');
    expect(gone).not.toContain('data-act="setup"');
  });

  it('names the speaker in every action', () => {
    expect(html).toContain('data-act="update" data-host="192.0.2.54" aria-label="Update STR on Hallway">Update</button>');
    expect(html).toContain('data-act="setup" data-host="192.0.2.51" aria-label="Set up STR on Kitchen">Set up STR</button>');
    expect(html).toContain('data-act="search" data-host="192.0.2.53" aria-label="Search again for Office">Search again</button>');
  });

  it('explains an offline speaker in its own card', () => {
    expect(html).toContain('Not reachable, last seen 10 minutes ago.');
    expect(html).toContain('Check that it has power and is in the same network as this computer, then search again.');
    const busy = speakerListHTML(speakerListModel(BOXES, ctx({ search: new Map([[OFF.host, 'busy']]) })));
    expect(busy).toContain('aria-disabled="true" aria-busy="true"');
    const still = speakerListHTML(speakerListModel(BOXES, ctx({ search: new Map([[OFF.host, 'still']]) })));
    expect(still).toContain('class="spl-note-result">Office is still not reachable.');
  });

  it('explains a speaker starting up', () => {
    expect(html).toContain('STR is starting on this speaker.');
  });

  it('ends with groups, update every speaker and add speaker', () => {
    expect(html).toContain('data-act="groups" aria-label="Set up stereo pairs and fixed groups">Set up</button>');
    expect(html).toContain('data-act="updateAll" aria-label="Update every speaker, 1 waiting">1 waiting</button>');
    expect(html).toContain('data-act="add">Add speaker</button>');
    const none = speakerListHTML(speakerListModel(BOXES, ctx({ needsUpdate: () => false })));
    expect(none).not.toContain('data-act="updateAll"');
  });

  it('escapes names and says so when there is no speaker', () => {
    const odd = speakerListHTML(speakerListModel([{ ...LR, friendlyName: 'Living <Room>' }], ctx()));
    expect(odd).toContain('Living &lt;Room&gt;');
    expect(speakerListHTML(speakerListModel([], ctx()))).toContain('No speakers found yet.');
    expect(speakerListHTML(null)).toContain('No speakers found yet.');
  });
});

describe('agoText', () => {
  it('says how long ago, in the app language', () => {
    expect(agoText(600, 'en')).toBe('10 minutes ago');
    expect(agoText(7200, 'en')).toBe('2 hours ago');
    expect(agoText(30, 'en')).toBe('1 minute ago');
    expect(agoText(600, 'de')).toBe('vor 10 Minuten');
  });
});
