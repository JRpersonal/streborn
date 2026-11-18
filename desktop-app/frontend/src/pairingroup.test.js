// A stereo pair in a multiroom group.
//
// A 12-speaker household grouped a SoundTouch 30 with a pair of SoundTouch 10s
// in the Bose app, and it played (field bundle 2026-11-15): the leader's zone
// named the pair master alone, the pair master reported the zone and its pair,
// the other half only its pair. STR forbade every pair-and-group combination
// and sent the user in a circle between two notes that contradicted each other
// ("dissolve the pair to group" and "dissolve the group to pair").
//
// Kept refused, both firmware facts (#792): a group LED by a pair half (the
// audio never starts), and a pair's other half enrolled on its own (it serves
// two masters).
//
// doFormZone is lifted and run with stubbed calls, the way groupreach.test.js
// does it, because views/multiroom.js paints into a DOM vitest does not have.
import { describe, it, expect, vi } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { srcOf } from './test/source.js';
import { state } from './state.js';
import { boxUnreachable, inAnyStereoPair, isPairMaster, pairUnitBox } from './groups.js';
import { groupCreateBlock, zoneFormFailure } from './zonereach.js';

const box = (n, id, name) => ({ deviceID: id, host: `192.0.2.${n}`, port: 8888, kind: 'str', friendlyName: name, model: 'SoundTouch 10' });
const LEAD = { ...box(10, 'LEAD', 'Dining room'), model: 'SoundTouch 30' };
const LEFT = box(19, 'LEFT', 'Kitchen left');
const RIGHT = box(11, 'RIGHT', 'Kitchen right');
const OTHER = { ...box(30, 'OTHER', 'Office'), model: 'SoundTouch 20' };
const BOXES = [LEAD, LEFT, RIGHT, OTHER];
const PAIR = {
  id: 'str-grp-LEFT', masterDeviceID: 'LEFT', name: 'Kitchen', status: 'GROUP_OK',
  members: [{ deviceID: 'LEFT', ip: LEFT.host, role: 'LEFT' }, { deviceID: 'RIGHT', ip: RIGHT.host, role: 'RIGHT' }],
};
const t = (k, p) => (p ? `${k}${JSON.stringify(p)}` : k);

function liftFormZone(formZone) {
  const env = {
    state,
    pairMemberIds: () => new Set(['LEFT', 'RIGHT']),
    inAnyStereoPair,
    isPairMaster,
    pairUnitBox,
    boxUnreachable,
    setZoneMsg: (html) => { state.zoneMsg = html; },
    escapeHtml: (s) => String(s),
    t,
    renderMultiroom: () => {},
    groupCreateBlock,
    zoneLabel: (b) => b.friendlyName,
    WakeBox: vi.fn().mockResolvedValue(undefined),
    FormZone: formZone,
    deps: { selectBox: vi.fn() },
    zoneFormFailure,
    finishAction: () => {},
  };
  const names = Object.keys(env);
  const make = new Function(...names, `let zoneFormBusy = false;\n${srcOf('doFormZone')}\nreturn doFormZone;`);
  return make(...names.map((n) => env[n]));
}

function zoneState(master, ticked) {
  state.zoneLive = { LEFT: { stereo: PAIR }, RIGHT: { stereo: PAIR }, LEAD: {}, OTHER: {} };
  state.zoneMaster = master;
  state.zoneSlaves = Object.fromEntries(ticked.map((id) => [id, true]));
  state.zonePermanent = false;
  state.currentBox = null;
  state.zoneMsg = '';
}

describe('Create group with a stereo pair (classic Multi-room)', () => {
  it('sends the pair as one member, its pair master', async () => {
    zoneState('LEAD', ['LEFT', 'OTHER']);
    const formZone = vi.fn().mockResolvedValue({ ok: true, verified: 2 });
    await liftFormZone(formZone)(BOXES);
    expect(formZone).toHaveBeenCalledTimes(1);
    const [host, , spec] = formZone.mock.calls[0];
    expect(host).toBe(LEAD.host);
    expect(spec.slaves).toEqual([{ deviceID: 'LEFT', ip: LEFT.host }, { deviceID: 'OTHER', ip: OTHER.host }]);
  });

  it('maps a tick on the other half onto the pair, once', async () => {
    zoneState('LEAD', ['RIGHT', 'LEFT']);
    const formZone = vi.fn().mockResolvedValue({ ok: true, verified: 1 });
    await liftFormZone(formZone)(BOXES);
    expect(formZone.mock.calls[0][2].slaves).toEqual([{ deviceID: 'LEFT', ip: LEFT.host }]);
  });

  it('never sends a pair half as the main speaker, and says why', async () => {
    for (const half of ['LEFT', 'RIGHT']) {
      zoneState(half, ['OTHER']);
      const formZone = vi.fn();
      await liftFormZone(formZone)(BOXES);
      expect(formZone, half).not.toHaveBeenCalled();
      expect(state.zoneMsg, half).toContain('multiroom.pairNotGroupable');
    }
  });

  it('can save a permanent group with the pair in it', async () => {
    zoneState('LEAD', ['LEFT']);
    state.zonePermanent = true;
    const formZone = vi.fn().mockResolvedValue({ ok: true, defined: true });
    await liftFormZone(formZone)(BOXES);
    const spec = formZone.mock.calls[0][2];
    expect(spec.permanent).toBe(true);
    expect(spec.slaves).toEqual([{ deviceID: 'LEFT', ip: LEFT.host }]);
    expect(state.zoneMsg).toContain('multiroom.permanentSaved');
  });
});

describe('the Multi-room grid offers the pair as one card that cannot lead', () => {
  const view = srcOf('renderMultiroom');

  it('draws the cards from the member set, the star from the leaders', () => {
    expect(view).toContain('const memberBoxes = strBoxes.filter(b => !inPair(b) || isPairMaster(b, state.zoneLive, strBoxes));');
    expect(view).toContain('? memberBoxes.map(b => {');
    expect(view).toContain('defaultZoneMasterID(state.zoneLive, strBoxes, starPool.length ? starPool : zoneBoxes)');
  });

  it('gives the pair card its reason instead of "make main", and the pair name', () => {
    const cards = view.slice(view.indexOf('const cards = memberBoxes.length'));
    const foot = cards.slice(0, cards.indexOf('const upd ='));
    expect(foot).toContain("t('multiroom.pairJoinOnly')");
    expect(foot.indexOf('pairCard')).toBeLessThan(foot.indexOf('zone-makemain'));
    expect(cards).toContain('pairCard ? unitLabel(b) : zoneLabel(b)');
  });

  it('shows a pair that is in a group as one chip named after the pair', () => {
    expect(view).toContain('pairChipsDone');
    expect(view).toContain('zone-frame-chip-pair');
  });

  it('no longer tells the user to dissolve the pair to group it', () => {
    expect(view).not.toContain('pairHiddenCount');
  });
});

describe('the two notes no longer send the user in a circle', () => {
  const dir = new URL('./i18n/bundles/', import.meta.url);
  const langs = readdirSync(dir).filter((f) => f.endsWith('.json')).map((f) => f.replace('.json', ''));
  const bundle = (l) => JSON.parse(readFileSync(new URL(`./i18n/bundles/${l}.json`, import.meta.url), 'utf8'));
  const en = bundle('en');

  it('says a pair can join but not lead, and how to pair speakers that are grouped', () => {
    expect(en['multiroom.pairNotGroupable']).toMatch(/can join a group/);
    expect(en['multiroom.pairNotGroupable']).not.toMatch(/Dissolve the pair/i);
    expect(en['multiroom.groupNotPairable']).toMatch(/add the pair to the group/);
    expect(en['ui2.side.pairAlone']).toMatch(/can join a group/);
  });

  it('has every changed string translated in every bundle', () => {
    const keys = ['multiroom.pairNotGroupable', 'multiroom.groupNotPairable', 'multiroom.stereoBlockedByGroup', 'ui2.side.pairAlone', 'multiroom.pairJoinOnly'];
    for (const l of langs) {
      const b = bundle(l);
      for (const k of keys) {
        expect(b[k], `${k} in ${l}`).toBeTruthy();
        if (l !== 'en') expect(b[k], `${k} in ${l} is still English`).not.toBe(en[k]);
      }
    }
  });

  it('speaks German with du', () => {
    const de = bundle('de');
    expect(de['multiroom.pairNotGroupable']).toMatch(/\bWähle\b/);
    expect(de['multiroom.groupNotPairable']).toMatch(/\bNimm\b/);
    expect(de['multiroom.pairNotGroupable']).not.toMatch(/\bSie\b|\bIhr\b/);
  });
});
