// Speakers > Groups in the UI 2.0 preview: which click is which action, when
// the speakers have confirmed it, and how a control is found again after the
// classic view rebuilt its markup.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  ACTIONS, ACTION_SELECTOR, CONFIRM_LATE_MS, DONE_SHOWN_MS, actionOf, expectationMet, focusKeyOf,
  liveLeaderOf, noteVerdict, pairKeyOf, pendingPhase,
} from './groupsmodel.js';

// A stand-in for an element: id, classes and attributes, nothing else.
const el = (id, classes = [], attrs = {}) => ({
  id,
  classList: { contains: (c) => classes.includes(c) },
  getAttribute: (n) => (n in attrs ? attrs[n] : null),
});

const L = { host: '192.0.2.71', deviceID: 'E1F2A3B4C5D6', friendlyName: 'Terrace left' };
const R = { host: '192.0.2.72', deviceID: 'E2F3A4B5C6D7', friendlyName: 'Terrace right' };
const D = { host: '192.0.2.73', deviceID: 'E3F4A5B6C7D8', friendlyName: 'Dining room' };
const H = { host: '192.0.2.74', deviceID: 'E4F5A6B7C8D9', friendlyName: 'Hall' };
const BOXES = [L, R, D, H];
const PAIR = {
  stereo: {
    id: 'pair-terrace', masterDeviceID: L.deviceID, name: 'Terrace',
    members: [{ deviceID: L.deviceID, ip: L.host, role: 'LEFT' }, { deviceID: R.deviceID, ip: R.host, role: 'RIGHT' }],
  },
};
const STORED = { members: [], permanent: true, remembered: [{ deviceID: H.deviceID, ip: H.host, name: 'Hall' }] };
const LIVE = (master, ...members) => ({ master: master.deviceID, senderIP: master.host, members: [master, ...members].map((b) => ({ deviceID: b.deviceID, ip: b.host })) });

describe('actionOf', () => {
  it('names every classic control that changes a pair or a group', () => {
    expect(actionOf(el('stereoCreate'))).toEqual({ kind: 'pair-create' });
    expect(actionOf(el('stereoNameSave'))).toEqual({ kind: 'pair-rename' });
    expect(actionOf(el('stereoDissolve'))).toEqual({ kind: 'pair-undo-sel' });
    expect(actionOf(el('zoneCreate'))).toEqual({ kind: 'group-create' });
    expect(actionOf(el('zoneUngroup'))).toEqual({ kind: 'group-ungroup' });
    expect(actionOf(el('', ['box-group-x'], { 'data-dissolve': 'e1f2', 'data-dissolve-kind': 'pair' }))).toEqual({ kind: 'pair-undo', key: 'E1F2' });
    expect(actionOf(el('', ['box-group-x'], { 'data-dissolve': 'AB', 'data-dissolve-kind': 'stored' }))).toEqual({ kind: 'group-forget', key: 'AB' });
    expect(actionOf(el('', ['box-group-x'], { 'data-dissolve': 'AB', 'data-dissolve-kind': 'zone' }))).toEqual({ kind: 'group-dissolve', key: 'AB' });
    expect(actionOf(el('', ['zone-chip-x'], { 'data-gkill': 'ab', 'data-gkill-ip': '192.0.2.74' }))).toEqual({ kind: 'member-remove', key: 'AB', ip: '192.0.2.74' });
    expect(actionOf(el('', ['stale-stereo-fix'], { 'data-stale': '1' }))).toEqual({ kind: 'stale-fix', index: 1 });
    expect(actionOf(el('', ['gk-remove'], { 'data-gk': 'Evening' }))).toEqual({ kind: 'gk-remove', template: 'Evening' });
    expect(actionOf(el('zoneRefresh'))).toBeNull();
    expect(actionOf(null)).toBeNull();
  });

  it('asks first for every destructive one, and only for those', () => {
    const asked = Object.entries(ACTIONS).filter(([, a]) => a.confirm).map(([k]) => k).sort();
    expect(asked).toEqual(['gk-remove', 'group-dissolve', 'group-forget', 'group-ungroup', 'member-remove', 'pair-undo', 'pair-undo-sel', 'stale-fix']);
  });

  it('finds every control it names with one selector', () => {
    for (const s of ['#stereoCreate', '#stereoNameSave', '#stereoDissolve', '#zoneCreate', '#zoneUngroup', '.box-group-x', '.zone-chip-x', '.stale-stereo-fix', '.gk-remove']) {
      expect(ACTION_SELECTOR).toContain(s);
    }
  });
});

describe('focusKeyOf', () => {
  it('finds a control again by its id or its data', () => {
    expect(focusKeyOf(el('gkRemote'))).toBe('#gkRemote');
    expect(focusKeyOf(el('', ['box-group-x'], { 'data-dissolve': 'AB', 'data-dissolve-kind': 'pair' })))
      .toBe('.box-group-x[data-dissolve="AB"][data-dissolve-kind="pair"]');
    expect(focusKeyOf(el('', ['ui2-grp-act'], { 'data-ui2-act': 'edit-pair', 'data-key': 'A+B' })))
      .toBe('.ui2-grp-act[data-ui2-act="edit-pair"][data-key="A+B"]');
    expect(focusKeyOf(el('', ['gk-remove'], { 'data-gk': 'Say "hi"' }))).toBe('.gk-remove[data-gk="Say \\"hi\\""]');
    expect(focusKeyOf(el('', ['muted']))).toBe('');
  });
});

describe('expectationMet', () => {
  const key = pairKeyOf(L.deviceID, R.deviceID);

  it('a pair is formed when a live pair holds both, and gone when none does', () => {
    expect(key).toBe(`${L.deviceID}+${R.deviceID}`);
    expect(expectationMet({ type: 'pairFormed', key }, {}, BOXES)).toBe(false);
    expect(expectationMet({ type: 'pairFormed', key }, { [L.deviceID]: PAIR, [R.deviceID]: PAIR }, BOXES)).toBe(true);
    expect(expectationMet({ type: 'pairGone', key }, { [L.deviceID]: PAIR }, BOXES)).toBe(false);
    expect(expectationMet({ type: 'pairGone', key }, { [L.deviceID]: {}, [R.deviceID]: {} }, BOXES)).toBe(true);
  });

  it('a one-sided leftover is gone only when its holder reports no pair', () => {
    const half = { stereo: { id: 'x', members: [{ deviceID: L.deviceID, ip: L.host }] } };
    expect(expectationMet({ type: 'pairGone', key: '', holder: L.deviceID }, { [L.deviceID]: half }, BOXES)).toBe(false);
    expect(expectationMet({ type: 'pairGone', key: '', holder: L.deviceID }, { [L.deviceID]: {} }, BOXES)).toBe(true);
  });

  it('a rename is confirmed when the speakers carry the name', () => {
    expect(expectationMet({ type: 'pairName', key, name: 'Patio' }, { [L.deviceID]: PAIR }, BOXES)).toBe(false);
    const renamed = { stereo: { ...PAIR.stereo, name: 'Patio' } };
    expect(expectationMet({ type: 'pairName', key, name: 'Patio' }, { [L.deviceID]: renamed }, BOXES)).toBe(true);
  });

  it('a group is formed when every member follows the main speaker, or a fixed one is stored', () => {
    const exp = { type: 'groupFormed', master: D.deviceID, members: [H.deviceID], permanent: false };
    expect(expectationMet(exp, {}, BOXES)).toBe(false);
    const live = LIVE(D, H);
    expect(expectationMet(exp, { [D.deviceID]: live, [H.deviceID]: live }, BOXES)).toBe(true);
    expect(expectationMet(exp, { [D.deviceID]: STORED }, BOXES)).toBe(false);
    expect(expectationMet({ ...exp, permanent: true }, { [D.deviceID]: STORED }, BOXES)).toBe(true);
    expect(expectationMet({ ...exp, permanent: true, type: 'groupLive' }, { [D.deviceID]: STORED }, BOXES)).toBe(false);
  });

  it('a group is gone when nobody names its main speaker any more', () => {
    const live = LIVE(D, H);
    const exp = { type: 'groupGone', master: D.deviceID };
    expect(expectationMet(exp, { [D.deviceID]: live, [H.deviceID]: live }, BOXES)).toBe(false);
    expect(expectationMet(exp, { [D.deviceID]: {}, [H.deviceID]: live }, BOXES)).toBe(false);
    expect(expectationMet(exp, { [D.deviceID]: {}, [H.deviceID]: {} }, BOXES)).toBe(true);
  });

  it('a fixed group is deleted, or a member taken out, once the main speaker stops listing it', () => {
    expect(expectationMet({ type: 'storedGone', masterID: D.deviceID }, { [D.deviceID]: STORED }, BOXES)).toBe(false);
    expect(expectationMet({ type: 'storedGone', masterID: D.deviceID }, { [D.deviceID]: {} }, BOXES)).toBe(true);
    const out = { type: 'memberOut', masterID: D.deviceID, ip: H.host, memberID: H.deviceID };
    expect(expectationMet(out, { [D.deviceID]: STORED }, BOXES)).toBe(false);
    expect(expectationMet(out, { [D.deviceID]: { ...STORED, remembered: [] } }, BOXES)).toBe(true);
  });
});

describe('pendingPhase', () => {
  const p = (extra = {}) => ({ at: 1000, expect: { type: 'groupGone', master: D.deviceID }, ...extra });
  const live = LIVE(D, H);
  const grouped = { [D.deviceID]: live, [H.deviceID]: live };

  it('waits, then says the speakers did not confirm, then lets go', () => {
    expect(pendingPhase(p(), grouped, BOXES, 1000)).toBe('waiting');
    expect(pendingPhase(p(), grouped, BOXES, 1000 + CONFIRM_LATE_MS + 1)).toBe('late');
    expect(pendingPhase(p(), grouped, BOXES, 1000 + CONFIRM_LATE_MS + 60001)).toBe('expired');
  });

  it('is done only when the speakers report it, and stays done for a while', () => {
    expect(pendingPhase(p(), {}, BOXES, 2000)).toBe('done');
    expect(pendingPhase(p({ doneAt: 2000 }), grouped, BOXES, 2000 + DONE_SHOWN_MS)).toBe('done');
    expect(pendingPhase(p({ doneAt: 2000 }), grouped, BOXES, 2001 + DONE_SHOWN_MS)).toBe('expired');
  });

  it('a refusal is never done', () => {
    expect(pendingPhase(p({ failed: true }), {}, BOXES, 2000)).toBe('failed');
    expect(pendingPhase(null, {}, BOXES)).toBe('expired');
  });
});

describe('noteVerdict', () => {
  it('reads the classic note classes', () => {
    expect(noteVerdict('<div class="setup-err">x</div>')).toBe('error');
    expect(noteVerdict('<div class="setup-warn">x</div>')).toBe('error');
    expect(noteVerdict('<div class="setup-ok">x</div>')).toBe('ok');
    expect(noteVerdict('<div class="muted">Loading...</div>')).toBe('');
    expect(noteVerdict('')).toBe('');
  });
});

describe('liveLeaderOf', () => {
  it('is the speaker leading a group, the picked one first', () => {
    const a = LIVE(D, H);
    const b = LIVE(L);
    const zl = { [D.deviceID]: a, [H.deviceID]: a, [L.deviceID]: b };
    expect(liveLeaderOf(BOXES, zl)).toBe(L);
    expect(liveLeaderOf(BOXES, zl, D.deviceID)).toBe(D);
    expect(liveLeaderOf(BOXES, zl, H.deviceID)).toBe(L);
    expect(liveLeaderOf(BOXES, {})).toBeNull();
  });
});

// The part's wiring, pinned by source text (groups.js needs the DOM).
describe('Speakers > Groups wiring', () => {
  const src = readFileSync(new URL('./groups.js', import.meta.url), 'utf8');
  const view = readFileSync(new URL('../../views/multiroom.js', import.meta.url), 'utf8');

  it('arranges the classic view after every paint and forks none of it', () => {
    expect(src).toContain('setMultiroomRenderHooks({ before: beforePaint, after: afterPaint })');
    expect(view).toContain('if (renderHooks && renderHooks.after) renderHooks.after(root, hookCtx);');
    expect(view).toMatch(/const hookCtx = renderHooks && renderHooks\.before \? renderHooks\.before\(root\) : null;\r?\n {2}root\.innerHTML = /);
    // Nothing here sends a group or pair request: every action is the classic
    // control's own click, replayed after the confirmation.
    expect(src).not.toMatch(/from '\.\.\/\.\.\/api\.js'/);
    expect(src).toContain('try { again.click(); } finally { bypass = false; }');
  });

  it('runs only in the preview and falls back to the classic markup on a fault', () => {
    expect(src).toContain("const isOn = (root) => !!(state.ui2Shell && root && root.dataset.ui2Route === 'groups');");
    expect(src).toMatch(/catch \(e\) \{[\s\S]*?setMultiroomRenderHooks\(null\);\s*renderMultiroom\(false\);/);
  });
});
