// The Multi-room editors and speakers the app cannot reach (#1741).
//
// Field report, three SoundTouch 10s, app v1.5.0 on a Mac:
//   1. Ten minutes into a Wi-Fi outage (router off) all three speakers could
//      still be ticked and "Create group" pressed; the answer was "Could not
//      change the group: Error: box_not_ready: master". The backend served the
//      speakers unchanged through their reboot grace (which the Mac's sleep
//      had paused), so nothing told the editor they had not answered.
//   2. "Create stereo pair" said the same raw code.
//   3. With the router back, three speakers ticked and "Create group" pressed,
//      the page first showed "Tap at least one other speaker", then "Group
//      active". The log has ONE create. The note was left over from a press
//      during the outage (the ticks had been dropped as offline), and the
//      create's "loading" note was written to the DOM only, so the 5 s live
//      repaint put the stale note back while the speakers woke.
//
// The pure decisions are tested from zonereach.js and groups.js; the two
// press handlers are lifted from source and run with stubbed calls, because
// views/multiroom.js paints into a DOM vitest does not have.
import { describe, it, expect, vi } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { srcOf } from './test/source.js';
import { state } from './state.js';
import { boxUnreachable, dropOfflineMembers, stereoPairsOf, livePairFor, pairBlockedHosts, masterOf as zoneMasterOf } from './groups.js';
import { groupCreateBlock, zoneFormFailure, stereoReachBlock, stereoFormFailure, isNotReachableError } from './zonereach.js';
import { stereoFormMessage } from './stereoformmsg.js';
import { clearZoneNoteOnEdit, setZoneMsg, resetMultiroomNotes } from './views/multiroom.js';

const nameOf = (b) => b.friendlyName;
const box = (id, extra = {}) => ({ deviceID: id, host: `192.0.2.${id.charCodeAt(0)}`, port: 8888, kind: 'str', friendlyName: `Room ${id}`, model: 'SoundTouch 10', ...extra });
const t = (k, p) => (p ? `${k}${JSON.stringify(p)}` : k);

describe('boxUnreachable', () => {
  it('is the offline tile and the speaker that missed the latest refresh', () => {
    expect(boxUnreachable(box('A'))).toBe(false);
    expect(boxUnreachable(box('A', { offline: true }))).toBe(true);
    expect(boxUnreachable(box('A', { unreachable: true }))).toBe(true);
    expect(boxUnreachable(null)).toBe(false);
  });

  it('drops the ticks of a speaker that missed the latest refresh too', () => {
    const ticks = { A: true, B: true };
    dropOfflineMembers(ticks, [box('A', { unreachable: true }), box('B')]);
    expect(ticks).toEqual({ B: true });
  });
});

describe('groupCreateBlock', () => {
  it('blocks the outage: nothing answers, so no group, in plain words', () => {
    const all = [box('A', { unreachable: true }), box('B', { unreachable: true }), box('C', { unreachable: true })];
    expect(groupCreateBlock(all[0], all, nameOf)).toEqual({ key: 'multiroom.groupNeedsReachable', params: {} });
  });

  it('blocks a main speaker that does not answer while two others do, and names it', () => {
    const all = [box('A', { unreachable: true }), box('B'), box('C')];
    expect(groupCreateBlock(all[0], all, nameOf)).toEqual({ key: 'multiroom.mainNotReachable', params: { name: 'Room A' } });
  });

  it('lets the create through when the main speaker and another one answer', () => {
    const all = [box('A'), box('B'), box('C', { offline: true })];
    expect(groupCreateBlock(all[0], all, nameOf)).toBe(null);
    expect(groupCreateBlock(all[0], [box('A'), box('B')], nameOf)).toBe(null);
  });

  it('judges reachability only: a short list with everyone answering is not its business', () => {
    expect(groupCreateBlock(box('A'), [box('A')], nameOf)).toBe(null);
  });
});

describe('failure wording', () => {
  it('recognises the readiness gate and the connect errors, not a plain read timeout', () => {
    expect(isNotReachableError(new Error('box_not_ready: master'))).toBe(true);
    expect(isNotReachableError('Post "http://192.0.2.1:8888/api/box/zone": dial tcp 192.0.2.1:8888: connect: no route to host')).toBe(true);
    expect(isNotReachableError('dial tcp 192.0.2.1:8888: i/o timeout')).toBe(true);
    expect(isNotReachableError('connect: network is unreachable')).toBe(true);
    expect(isNotReachableError('context deadline exceeded (Client.Timeout exceeded while awaiting headers)')).toBe(false);
    expect(isNotReachableError('setZone: firmware refused')).toBe(false);
  });

  it('a group create that could not reach the main speaker says so, without the code', () => {
    expect(zoneFormFailure(new Error('box_not_ready: master'), 'Room A'))
      .toEqual({ key: 'multiroom.groupNotReachable', params: { names: 'Room A' } });
  });

  it('any other failure keeps its text for the bug report', () => {
    expect(zoneFormFailure(new Error('setZone: firmware refused'), 'Room A'))
      .toEqual({ key: 'multiroom.formFailed', params: { err: 'Error: setZone: firmware refused' } });
  });

  it('a stereo pairing names the left speaker for the gate, both for a connect error', () => {
    expect(stereoFormFailure(new Error('box_not_ready: master'), 'Left', 'Right'))
      .toEqual({ key: 'multiroom.stereoNotReachable', params: { names: 'Left' } });
    expect(stereoFormFailure('dial tcp 192.0.2.1:8888: connect: host is down', 'Left', 'Right'))
      .toEqual({ key: 'multiroom.stereoNotReachable', params: { names: 'Left, Right' } });
    expect(stereoFormFailure('addGroup: refused', 'Left', 'Right').key).toBe('multiroom.formFailed');
  });

  it('stereoReachBlock names only the halves that do not answer', () => {
    expect(stereoReachBlock(box('A'), box('B'), nameOf)).toBe(null);
    expect(stereoReachBlock(box('A', { offline: true }), box('B'), nameOf))
      .toEqual({ key: 'multiroom.stereoNotReachable', params: { names: 'Room A' } });
  });
});

// doFormZone, lifted and run. zoneFormBusy is module state in the view, so the
// lifted copy gets its own.
function liftFormZone({ formZone, renders }) {
  const deps = { selectBox: vi.fn() };
  const env = {
    state,
    pairMemberIds: () => new Set(),
    boxUnreachable,
    // The one writer, minus its flash timer.
    setZoneMsg: (html) => { state.zoneMsg = html; },
    escapeHtml: (s) => String(s),
    t,
    // A repaint: what the user sees in #zoneResult is state.zoneMsg.
    renderMultiroom: () => { renders.push(state.zoneMsg); },
    groupCreateBlock,
    zoneLabel: nameOf,
    WakeBox: vi.fn().mockResolvedValue(undefined),
    FormZone: formZone,
    deps,
    zoneFormFailure,
    finishAction: () => { renders.push(state.zoneMsg); },
  };
  const names = Object.keys(env);
  const make = new Function(...names, `let zoneFormBusy = false;\n${srcOf('doFormZone')}\nreturn doFormZone;`);
  return { fn: make(...names.map((n) => env[n])), env };
}

function freshZoneState(master, ticked) {
  state.zoneLive = {};
  state.zoneMaster = master;
  state.zoneSlaves = Object.fromEntries(ticked.map((id) => [id, true]));
  state.zonePermanent = false;
  state.currentBox = null;
}

describe('Create group with the router back (#1741, finding 3)', () => {
  it('never shows the stale "tap a speaker" note while the create is on its way, then confirms', async () => {
    const boxes = [box('A'), box('B'), box('C')];
    freshZoneState('A', ['B', 'C']);
    // Left over from a press during the outage.
    state.zoneMsg = '<div class="setup-warn">multiroom.pickAtLeastOne</div>';
    let answer;
    const formZone = vi.fn(() => new Promise((r) => { answer = r; }));
    const renders = [];
    const { fn } = liftFormZone({ formZone, renders });

    const run = fn(boxes);
    // The speakers are waking; the live poll repaints meanwhile.
    await vi.waitFor(() => expect(formZone).toHaveBeenCalledTimes(1));
    renders.push(state.zoneMsg);
    expect(state.zoneMsg).toContain('common.loading');

    // A second press while it is on its way does not send the group again.
    await fn(boxes);
    expect(formZone).toHaveBeenCalledTimes(1);

    answer({ ok: true, verified: 2, missing: [] });
    await run;
    expect(state.zoneMsg).toContain('multiroom.formedN');
    expect(state.zoneMsg).toContain('setup-ok');
    for (const shown of renders) expect(shown).not.toContain('multiroom.pickAtLeastOne');
    expect(formZone.mock.calls[0][2].slaves.map((s) => s.deviceID)).toEqual(['B', 'C']);
  });

  it('a create that failed lets the next press through', async () => {
    const boxes = [box('A'), box('B')];
    freshZoneState('A', ['B']);
    state.zoneMsg = '';
    const formZone = vi.fn().mockRejectedValueOnce(new Error('setZone: firmware refused')).mockResolvedValueOnce({ ok: true, verified: 1 });
    const { fn } = liftFormZone({ formZone, renders: [] });
    await fn(boxes);
    expect(state.zoneMsg).toContain('multiroom.formFailed');
    await fn(boxes);
    expect(formZone).toHaveBeenCalledTimes(2);
    expect(state.zoneMsg).toContain('multiroom.formedN');
  });

  it('a change to the selection takes the last note down', () => {
    state.view = 'multiroom';
    resetMultiroomNotes();
    setZoneMsg('<div class="setup-warn">multiroom.pickAtLeastOne</div>', { transient: false });
    clearZoneNoteOnEdit();
    expect(state.zoneMsg).toBe('');
  });

  it('the card tick and "set as main" both take it down before the repaint', () => {
    const render = srcOf('renderMultiroom');
    const cards = render.slice(render.indexOf(".querySelectorAll('.zone-card')"));
    const handler = cards.slice(0, cards.indexOf('if (enough) {'));
    expect(handler.match(/clearZoneNoteOnEdit\(\);\n\s*renderMultiroom\(\);/g) || []).toHaveLength(2);
  });

  it('the press reads the speaker list at the moment of the press, not from the paint', () => {
    expect(srcOf('renderMultiroom')).toContain("$('zoneCreate').onclick = () => doFormZone(multiroomStrBoxes());");
    expect(srcOf('renderMultiroom')).toContain("$('stereoCreate').onclick = () => doFormStereo(pairCands, multiroomStrBoxes());");
  });
});

describe('Create group during the outage (#1741, finding 1)', () => {
  it('sends nothing when no speaker answers and says why in plain words', async () => {
    const boxes = [box('A', { unreachable: true }), box('B', { unreachable: true }), box('C', { unreachable: true })];
    freshZoneState('A', ['B', 'C']);
    const formZone = vi.fn();
    const { fn } = liftFormZone({ formZone, renders: [] });
    await fn(boxes);
    expect(formZone).not.toHaveBeenCalled();
    expect(state.zoneMsg).toContain('multiroom.groupNeedsReachable');
    expect(state.zoneMsg).not.toContain('pickAtLeastOne');
  });

  it('names a main speaker that does not answer', async () => {
    const boxes = [box('A', { unreachable: true }), box('B'), box('C')];
    freshZoneState('A', ['B', 'C']);
    const formZone = vi.fn();
    const { fn } = liftFormZone({ formZone, renders: [] });
    await fn(boxes);
    expect(formZone).not.toHaveBeenCalled();
    expect(state.zoneMsg).toContain('multiroom.mainNotReachable');
    expect(state.zoneMsg).toContain('Room A');
  });

  it('a create that still fails on an unreachable speaker says so, not "box_not_ready"', async () => {
    const boxes = [box('A'), box('B')];
    freshZoneState('A', ['B']);
    const formZone = vi.fn().mockRejectedValue(new Error('box_not_ready: master'));
    const { fn } = liftFormZone({ formZone, renders: [] });
    await fn(boxes);
    expect(state.zoneMsg).toContain('multiroom.groupNotReachable');
    expect(state.zoneMsg).toContain('Room A');
    expect(state.zoneMsg).not.toContain('box_not_ready');
  });

  it('members that all missed the readiness probe are named, not "try Mirror mode"', async () => {
    const boxes = [box('A'), box('B')];
    freshZoneState('A', ['B']);
    const formZone = vi.fn().mockResolvedValue({ ok: false, notReady: [boxes[1].host] });
    const { fn } = liftFormZone({ formZone, renders: [] });
    await fn(boxes);
    expect(state.zoneMsg).toContain('multiroom.notReady');
    expect(state.zoneMsg).toContain('Room B');
    expect(state.zoneMsg).not.toContain('multiroom.formedNone');
  });

  it('the Create button carries the block and is disabled with it', () => {
    const render = srcOf('renderMultiroom');
    expect(render).toContain('const createBlock = enough ? groupCreateBlock(zoneMasterBox, zoneBoxes, zoneLabel) : null;');
    expect(render).toContain("const createDis = (!enough || createBlock || zoneFormBusy) ? ' disabled' : '';");
    expect(render).toContain('<button id="zoneCreate" class="btn"$' + '{createDis}>');
    expect(render).toContain('$' + '{createWhy}');
  });
});

// doFormStereo, lifted and run.
function liftFormStereo({ formZone, left, right }) {
  const els = { stereoLeft: { value: left }, stereoRight: { value: right }, stereoName: { value: '' } };
  const env = {
    $: (id) => els[id] || null,
    state,
    escapeHtml: (s) => String(s),
    t,
    renderMultiroom: () => {},
    livePairFor,
    stereoPairsOf,
    pairBlockedHosts,
    zoneMasterOf,
    stereoReachBlock,
    zoneLabel: nameOf,
    storedPairName: vi.fn().mockResolvedValue(''),
    FormZone: formZone,
    stereoFormMessage,
    setPairName: vi.fn(),
    rememberPairName: vi.fn(),
    flashStereoMsg: (html) => { state.stereoMsg = html; },
    stereoFormFailure,
    finishAction: () => {},
  };
  const names = Object.keys(env);
  const make = new Function(...names, `${srcOf('doFormStereo')}\nreturn doFormStereo;`);
  return make(...names.map((n) => env[n]));
}

describe('Create stereo pair during the outage (#1741, finding 2)', () => {
  it('does not send a pairing to a half the app knows is out, and names it', async () => {
    state.zoneLive = {};
    state.stereoMsg = '';
    const cands = [box('A'), box('B')];
    const now = [box('A', { unreachable: true }), box('B')];
    const formZone = vi.fn();
    await liftFormStereo({ formZone, left: 'A', right: 'B' })(cands, now);
    expect(formZone).not.toHaveBeenCalled();
    expect(state.stereoMsg).toContain('multiroom.stereoNotReachable');
    expect(state.stereoMsg).toContain('Room A');
  });

  it('a pairing that fails on the gate says the speaker is not reachable, without the code', async () => {
    state.zoneLive = {};
    state.stereoMsg = '';
    const cands = [box('A'), box('B')];
    const formZone = vi.fn().mockRejectedValue(new Error('box_not_ready: master'));
    await liftFormStereo({ formZone, left: 'A', right: 'B' })(cands, cands);
    expect(formZone).toHaveBeenCalledTimes(1);
    expect(state.stereoMsg).toContain('multiroom.stereoNotReachable');
    expect(state.stereoMsg).toContain('Room A');
    expect(state.stereoMsg).not.toContain('box_not_ready');
  });

  it('keeps "loading" in state while the pairing is on its way', async () => {
    state.zoneLive = {};
    state.stereoMsg = '<div class="setup-err">old</div>';
    const cands = [box('A'), box('B')];
    let answer;
    const formZone = vi.fn(() => new Promise((r) => { answer = r; }));
    const run = liftFormStereo({ formZone, left: 'A', right: 'B' })(cands, cands);
    await vi.waitFor(() => expect(formZone).toHaveBeenCalledTimes(1));
    expect(state.stereoMsg).toContain('common.loading');
    answer({ ok: true });
    await run;
    expect(state.stereoMsg).toContain('multiroom.stereoFormed');
  });
});

describe('bundles', () => {
  const BUNDLE_DIR = new URL('./i18n/bundles/', import.meta.url);
  const bundles = Object.fromEntries(readdirSync(BUNDLE_DIR)
    .filter((f) => f.endsWith('.json'))
    .map((f) => [f.slice(0, -5), JSON.parse(readFileSync(new URL(f, BUNDLE_DIR), 'utf8'))]));
  const NEW = {
    'multiroom.notReachableNotSelectable': [],
    'multiroom.groupNeedsReachable': [],
    'multiroom.mainNotReachable': ['{{name}}'],
    'multiroom.groupNotReachable': ['{{names}}'],
    'multiroom.stereoNotReachable': ['{{names}}'],
  };

  it('has every new key in all thirteen bundles, translated, with its placeholder', () => {
    expect(Object.keys(bundles)).toHaveLength(13);
    for (const [code, b] of Object.entries(bundles)) {
      for (const [k, ph] of Object.entries(NEW)) {
        expect(typeof b[k], `${code} ${k}`).toBe('string');
        expect(b[k].trim(), `${code} ${k}`).not.toBe('');
        if (code !== 'en') expect(b[k], `${code} ${k} is still English`).not.toBe(bundles.en[k]);
        for (const p of ph) expect(b[k], `${code} ${k} lost ${p}`).toContain(p);
        expect(b[k], `${code} ${k}: no dash as punctuation`).not.toMatch(/\s[-–—]\s|—|–/);
      }
    }
  });

  it('German says du and spells umlauts', () => {
    const de = bundles.de;
    expect(de['multiroom.groupNotReachable']).toMatch(/deinem Netzwerk/);
    expect(de['multiroom.mainNotReachable']).toMatch(/Hauptlautsprecher/);
    expect(de['multiroom.groupNeedsReachable']).toMatch(/lässt/);
    for (const k of Object.keys(NEW)) expect(de[k]).not.toMatch(/\bSie\b|\bIhr(em|en)?\b/);
  });
});
