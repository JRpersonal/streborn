// The firmware ladder's screens, checked without a DOM.
//
// Background: a SoundTouch 20 Series I on 5.x went 5.x -> 06.02.01 -> 07.00.37
// by USB stick, while 06.02.01 -> 27.0.6 in one jump stalled on an empty bar.
// The ladder walks such a speaker through the in-between updates.
import { describe, it, expect, beforeAll } from 'vitest';
import { setLocale } from './i18n/index.js';
import {
  activeSteps,
  currentStep,
  fallbacksFor,
  firmwareLadderMount,
  formatBytes,
  ladderApplies,
  ladderClickCount,
  ladderErrorText,
  ladderHtml,
  ladderKey,
  ladderKeys,
  ladderMinutes,
  ladderProgressText,
  ladderView,
  loadLadderState,
  loadLadderStateAny,
  noLadderYet,
  persistable,
  powerOffKey,
  resumeState,
  saveLadderState,
  stickVerdict,
  usbUpdateKey,
  usbUpdateUnconfirmed,
  versionReached,
  MANUAL_VERSIONS,
} from './fwladder.js';
import { firmwareGuideMount, plainGuideMount } from './fwguide.js';
import en from './i18n/bundles/en.json';

setLocale('en');
beforeAll(() => { setLocale('en'); });

const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');

const archive = (v, live = false) => ({ id: `st-scm-${v}`, version: v, source: 'archive', url: `https://archive.org/download/bose-soundtouch-software-and-firmware/x/${v}.zip`, innerSize: 93_000_000, liveVerified: live });
const bose = { id: 'st-scm-27.0.6', version: '27.0.6', source: 'bose', url: 'https://downloads.bose.com/x.stu', innerSize: 105_879_988, liveVerified: false, networkOk: true };
const s0602 = archive('06.02.01', true);
const s0700 = archive('07.00.37', true);
const s1400 = archive('14.00.33');
const s1500 = archive('15.00.20');
const fb09 = { ...archive('09.00.41'), fallback: true };
const fb12 = { ...archive('12.00.09'), fallback: true };

const plan5x = {
  status: 'ladder', model: 'SoundTouch 20', moduleType: 'scm', current: '5.2.0', deviceId: 'device-id-here',
  versionFrom: 'lan', steps: [s0602, s0700, s1400, s1500, bose], fallbacks: {},
};
const plan7x = {
  status: 'ladder', model: 'SoundTouch 20', moduleType: 'scm', current: '7.0.37', deviceId: 'device-id-here',
  steps: [s1400, s1500, bose], fallbacks: { 'st-scm-14.00.33': [fb09, fb12, s1400] },
};
const plan1433 = { status: 'ladder', model: 'SoundTouch 30', moduleType: 'scm', current: '14.0.33', steps: [s1500, bose], fallbacks: {} };
const ident = { model: 'SoundTouch 20', moduleType: '', variant: '', current: '5.2.0.11111.222', host: '192.0.2.10' };

function memStore() {
  const m = new Map();
  return { getItem: k => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), m };
}

describe('when the ladder replaces the single-step guide', () => {
  it('takes over for a phase-1 model below 15.x', () => {
    expect(ladderApplies(ident)).toBe(true);
    expect(ladderApplies({ ...ident, model: 'SoundTouch Portable', current: '14.0.15' })).toBe(true);
    expect(ladderApplies({ ...ident, current: '15.0.20' })).toBe(false);
    expect(ladderApplies({ ...ident, current: '27.0.3' })).toBe(false);
    expect(ladderApplies({ ...ident, current: '' })).toBe(false);
  });
  it('leaves sm2 and other models on the old guide, with the "no step-by-step path" line', () => {
    expect(ladderApplies({ ...ident, moduleType: 'sm2', current: '14.0.15' })).toBe(false);
    expect(ladderApplies({ ...ident, model: 'Wave SoundTouch', current: '2.1.51' })).toBe(false);
    expect(noLadderYet({ ...ident, model: 'Wave SoundTouch', current: '2.1.51' })).toBe(true);
    expect(noLadderYet({ ...ident, model: 'SoundTouch 10', current: '27.0.3' })).toBe(false);
    const html = firmwareGuideMount({ model: 'Wave SoundTouch', current: '2.1.51' });
    expect(html).toContain('data-fwguide=');
    expect(html).toContain(esc(en['fwLadder.unsupported']));
  });
  it('mounts the ladder for a 5.x ST20 and the plain guide for 27.0.3', () => {
    expect(firmwareGuideMount(ident)).toContain('data-fwladder=');
    expect(firmwareGuideMount({ ...ident, current: '27.0.3' })).toContain('data-fwguide=');
    expect(firmwareGuideMount({ ...ident, current: '27.0.3' })).not.toContain(esc(en['fwLadder.unsupported']));
    expect(firmwareLadderMount(ident, plainGuideMount)).toContain(esc(en['fwLadder.loading']));
  });
});

describe('screen transitions', () => {
  it('walks A, B, C and on to D for the final Bose file', () => {
    expect(ladderView(null, {})).toBe('loading');
    expect(ladderView(plan5x, {})).toBe('A');
    expect(ladderView(plan5x, { started: true })).toBe('B');
    expect(ladderView(plan5x, { started: true, screen: 'C' })).toBe('C');
    expect(ladderView(plan5x, { started: true, stepId: bose.id })).toBe('D');
    expect(ladderView({ status: 'final', steps: [bose] }, {})).toBe('D');
    expect(ladderView({ status: 'final', steps: [bose] }, { finalByStick: true })).toBe('B');
    expect(ladderView({ status: 'final', steps: [bose] }, { finalByStick: true, screen: 'C' })).toBe('C');
  });
  it('asks for the version, the Bluetooth button, or says unsupported', () => {
    expect(ladderView({ status: 'needs-version', steps: [] }, {})).toBe('manual');
    expect(ladderView(plan5x, { manual: true })).toBe('manual');
    expect(ladderView({ status: 'needs-bluetooth', steps: [] }, {})).toBe('bluetooth');
    expect(ladderView({ status: 'unsupported', steps: [], reason: 'sm2' }, {})).toBe('unsupported');
    expect(ladderView({ status: 'uptodate', steps: [] }, {})).toBe('done');
    expect(ladderView({ status: 'error', steps: [] }, {})).toBe('error');
  });
  it('renders the Bluetooth question with both answers', () => {
    const html = ladderHtml(ident, { status: 'needs-bluetooth', steps: [] }, {});
    expect(html).toContain(esc(en['fwLadder.btQuestion']));
    expect(html).toContain('data-ladder="bt-yes"');
    expect(html).toContain('data-ladder="bt-no"');
  });
  it('the manual picker offers every version range and the 5 + Volume minus hint', () => {
    const html = ladderHtml(ident, { status: 'needs-version', steps: [] }, {});
    expect(html).toContain(esc(en['fwLadder.manualHow']));
    for (const v of MANUAL_VERSIONS) expect(html).toContain(`value="${v.id}"`);
    expect(html).toContain('data-ladder="manual-go"');
  });
});

describe('screen A', () => {
  it('lists every step with its source and tested badge, and blocks Start until the box is ticked', () => {
    const html = ladderHtml(ident, plan5x, {});
    expect(html).toContain(esc(en['fwLadder.title'].replace('{{n}}', '5')));
    for (const s of plan5x.steps.slice(0, 4)) expect(html).toContain(s.version);
    expect(html).toContain(esc(en['fwLadder.stepBose'].replace('{{version}}', '27.0.6')));
    expect(html).toContain(esc(en['fwLadder.badgeArchive']));
    expect(html).toContain(esc(en['fwLadder.untested']));
    expect(html).toContain(esc(en['fwLadder.w1'].replace('{{n}}', '5')));
    expect(html).toMatch(/data-ladder="start" disabled/);
    expect(ladderHtml(ident, plan5x, { ack: true })).not.toMatch(/data-ladder="start" disabled/);
  });
  it('estimates 10 to 20 minutes per step', () => {
    expect(ladderMinutes(plan5x)).toEqual({ min: 50, max: 100 });
  });
});

describe('click count, as in the design', () => {
  it('16 for 5.x, 10 for 7.x to 14.0.15, 7 for 14.0.33', () => {
    expect(ladderClickCount(plan5x)).toBe(16);
    expect(ladderClickCount(plan7x)).toBe(10);
    expect(ladderClickCount(plan1433)).toBe(7);
  });
});

describe('screen B: the stick', () => {
  const ok = { ok: true, isFat32: true, freeBytes: 2e9, totalBytes: 8e9 };
  it('accepts a roomy FAT32 stick and warns, without blocking, over 32 GB', () => {
    expect(stickVerdict(ok, 93e6)).toEqual({ ok: true, problem: '', warnLarge: false });
    expect(stickVerdict({ ...ok, tooLarge: true }, 93e6)).toEqual({ ok: true, problem: '', warnLarge: true });
  });
  it('blocks a 64 KB cluster, a full stick, and passes on CheckStick reasons', () => {
    expect(stickVerdict({ ...ok, clusterTooBig: true }, 93e6).problem).toBe('cluster-too-big');
    expect(stickVerdict({ ...ok, freeBytes: 95e6 }, 93e6).problem).toBe('no-space');
    // An Update.stu already on the stick is replaced, so its space counts, as
    // in the backend's FirmwareStickProblem.
    expect(stickVerdict({ ...ok, freeBytes: 95e6, existingFirmwareBytes: 93e6 }, 93e6).ok).toBe(true);
    expect(stickVerdict({ ok: false, reason: 'not-fat32' }, 93e6).problem).toBe('not-fat32');
    expect(stickVerdict(null, 1).problem).toBe('not-writable');
  });
  it('offers Format only where formatting helps, and Prepare only for a good stick', () => {
    const base = { started: true, drives: [{ path: 'E:\\', label: 'STICK', totalBytes: 8e9, filesystem: 'exFAT' }], drive: 'E:\\' };
    const notFat = ladderHtml(ident, plan5x, { ...base, check: { ok: false, reason: 'not-fat32' } });
    expect(notFat).toContain('data-ladder="format"');
    expect(notFat).toMatch(/data-ladder="prepare" disabled/);
    const good = ladderHtml(ident, plan5x, { ...base, check: ok });
    expect(good).not.toContain('data-ladder="format"');
    expect(good).not.toMatch(/data-ladder="prepare" disabled/);
    expect(good).toContain(esc(en['fwLadder.stickOk']));
    expect(good).toContain('data-ladder="have-file"');
    expect(good).toContain('data-ladder="open-source"');
  });
  it('the Bose step on a stick has no archive link', () => {
    const html = ladderHtml(ident, { status: 'final', steps: [bose] }, { finalByStick: true });
    expect(html).not.toContain('data-ladder="open-source"');
    expect(html).toContain(esc(en['fwLadder.prepIntroBose']));
  });
  it('says what went wrong', () => {
    expect(ladderErrorText({ code: 'fingerprint' })).toBe(en['fwLadder.w7']);
    expect(ladderErrorText({ code: 'stick', detail: 'no-space' })).toBe(en['fwLadder.stick.no-space']);
    expect(ladderErrorText({ code: 'stick', detail: 'weird' })).toBe(en['fwLadder.stick.not-writable']);
    expect(ladderErrorText({ code: 'cancelled' })).toBe(en['fwLadder.errCancelled']);
    expect(ladderErrorText({ code: 'x', detail: 'boom' })).toContain('boom');
  });
  it('shows progress per phase', () => {
    expect(ladderProgressText({ phase: 'download', pct: 42 })).toBe('Downloading: 42 %');
    expect(ladderProgressText({ phase: 'download', pct: -1 })).toBe('Downloading:');
    expect(ladderProgressText({ phase: 'write', pct: 7 })).toBe('Copying to the stick: 7 %');
    expect(ladderProgressText({ phase: 'eject' })).toBe(en['fwLadder.pEject']);
    expect(ladderProgressText({})).toBe(en['fwLadder.pStart']);
  });
  it('formats sizes', () => {
    expect(formatBytes(7.5e9)).toBe('7.5 GB');
    expect(formatBytes(512e6)).toBe('512 MB');
  });
});

describe('screen C: on the speaker', () => {
  it('uses 4 + Volume minus for the SoundTouch family and keeps W2, W4, W5 visible', () => {
    expect(usbUpdateKey('SoundTouch 20')).toBe('fwLadder.c3FourVol');
    expect(usbUpdateKey('Wave SoundTouch')).toBe('fwLadder.c3Generic');
    const html = ladderHtml(ident, plan5x, { started: true, screen: 'C' });
    expect(html).toContain(esc(en['fwLadder.c3FourVol']));
    expect(html).toContain(esc(en['fwLadder.w2']));
    expect(html).toContain(esc(en['fwLadder.w4']));
    expect(html).toContain(esc(en['fwLadder.w5']));
    expect(html).not.toContain(esc(en['fwLadder.w2Battery']));
    expect(html).toContain('data-ladder="recheck"');
  });
  it('adds the battery warning on the Portable', () => {
    const html = ladderHtml(ident, { ...plan1433, model: 'SoundTouch Portable' }, { started: true, screen: 'C' });
    expect(html).toContain(esc(en['fwLadder.w2Battery']));
  });
  it('never tells a Portable to unplug: power button, adapter stays in, combination marked unconfirmed', () => {
    expect(powerOffKey('SoundTouch Portable')).toBe('fwLadder.c1Battery');
    expect(usbUpdateKey('SoundTouch Portable')).toBe('fwLadder.c3Battery');
    expect(usbUpdateUnconfirmed('SoundTouch Portable')).toBe(true);
    const html = ladderHtml(ident, { ...plan1433, model: 'SoundTouch Portable' }, { started: true, screen: 'C' });
    expect(html).toContain(esc(en['fwLadder.c1Battery']));
    expect(html).toContain(esc(en['fwLadder.c3Battery']));
    expect(html).toContain(esc(en['fwLadder.c3Unconfirmed']));
    expect(html).not.toContain(esc(en['fwLadder.c1']));
    expect(html).not.toContain(esc(en['fwLadder.c3FourVol']));
    const st20 = ladderHtml(ident, plan5x, { started: true, screen: 'C' });
    expect(st20).toContain(esc(en['fwLadder.c1']));
    expect(st20).not.toContain(esc(en['fwLadder.c3Unconfirmed']));
    expect(powerOffKey('SoundTouch 30')).toBe('fwLadder.c1');
  });
  it('after an unchanged version shows W6 and offers the smaller steps once', () => {
    const st = { started: true, screen: 'C', recheck: { state: 'same', version: '7.0.37' } };
    const html = ladderHtml(ident, plan7x, st);
    expect(html).toContain(esc(en['fwLadder.w6']));
    expect(html).toContain('data-ladder="retry"');
    expect(html).toContain('data-ladder="smaller"');
    expect(ladderHtml(ident, plan5x, st)).not.toContain('data-ladder="smaller"');
    expect(ladderHtml(ident, plan7x, { ...st, fallbackFor: 'st-scm-14.00.33', stepId: fb09.id })).not.toContain('data-ladder="smaller"');
  });
  it('asks for the version when the speaker cannot be reached', () => {
    const html = ladderHtml(ident, plan5x, { started: true, screen: 'C', recheck: { state: 'unknown' } });
    expect(html).toContain(esc(en['fwLadder.notFoundAfter']));
    expect(html).toContain('data-ladder="recheck-version"');
  });
  it('a step counts as done only when the version reached its target', () => {
    expect(versionReached('6.2.1.4567', '06.02.01')).toBe(true);
    expect(versionReached('7.0.37', '07.00.37')).toBe(true);
    expect(versionReached('6.2.1', '07.00.37')).toBe(false);
    expect(versionReached('27.0.6', '15.00.20')).toBe(true);
    expect(versionReached('', '06.02.01')).toBe(false);
  });
});

describe('the fallback sub-ladder', () => {
  it('runs the smaller steps, then the rest of the plan', () => {
    const st = { fallbackFor: 'st-scm-14.00.33' };
    expect(activeSteps(plan7x, st).map(s => s.version)).toEqual(['09.00.41', '12.00.09', '14.00.33', '15.00.20', '27.0.6']);
    expect(activeSteps(plan7x, {}).map(s => s.version)).toEqual(['14.00.33', '15.00.20', '27.0.6']);
    expect(currentStep(plan7x, st).version).toBe('09.00.41');
  });
  it('never offers a smaller step at or below the version the speaker is on', () => {
    // A plan built for "7 to 13" while the speaker is really on 12.x.
    const on12 = { ...plan7x, current: '12.0.9' };
    expect(fallbacksFor(on12, s1400.id)).toEqual([]);
    expect(activeSteps(on12, { fallbackFor: s1400.id }).map(s => s.version)).toEqual(['14.00.33', '15.00.20', '27.0.6']);
    const on10 = { ...plan7x, current: '10.0.0' };
    expect(fallbacksFor(on10, s1400.id).map(s => s.version)).toEqual(['12.00.09', '14.00.33']);
    const st = { started: true, screen: 'C', recheck: { state: 'same', version: '12.0.9' } };
    expect(ladderHtml(ident, on12, st)).not.toContain('data-ladder="smaller"');
  });
});

describe('screen D', () => {
  it('hands the last step to the normal guide with the one-click update, plus the stick route', () => {
    const html = ladderHtml(ident, { status: 'final', model: 'SoundTouch 20', moduleType: 'scm', current: '15.0.20', steps: [bose] }, { started: true }, plainGuideMount);
    expect(html).toContain('data-fwguide=');
    expect(html).toContain('&quot;moduleType&quot;:&quot;scm&quot;');
    expect(html).toContain('data-ladder="final-stick"');
  });
});

describe('resume per speaker', () => {
  it('keys on the speaker ID first', () => {
    expect(ladderKey(plan5x, ident)).toBe('device-id-here');
    expect(ladderKey({}, ident)).toBe('192.0.2.10');
    expect(ladderKey(null, { model: 'SoundTouch 20' })).toBe('SoundTouch 20');
  });
  it('saves under the ID and the address, so a restart without the ID still resumes', () => {
    expect(ladderKeys(plan5x, ident)).toEqual(['device-id-here', '192.0.2.10']);
    expect(ladderKeys({}, ident)).toEqual(['192.0.2.10']);
    const store = memStore();
    for (const k of ladderKeys(plan5x, ident)) saveLadderState(k, { started: true, stepId: s0700.id, screen: 'C' }, store);
    // Next start: the speaker cannot be read, so the plan has no ID.
    const back = loadLadderStateAny(ladderKeys({ status: 'needs-version' }, ident), store);
    expect(back && back.stepId).toBe(s0700.id);
    expect(loadLadderStateAny(ladderKeys(plan5x, ident), store).stepId).toBe(s0700.id);
  });
  it('saves only what matters and survives a restart', () => {
    const store = memStore();
    saveLadderState('dev1', { started: true, ack: true, stepId: s0700.id, screen: 'C', busy: true, drives: [1] }, store);
    const back = loadLadderState('dev1', store);
    expect(back).toEqual({ started: true, ack: true, stepId: s0700.id, screen: 'C', fallbackFor: '', finalByStick: false });
    expect(persistable({ screen: 'X' }).screen).toBe('B');
    const st = resumeState(plan5x, back);
    expect(ladderView(plan5x, st)).toBe('C');
    expect(currentStep(plan5x, st).id).toBe(s0700.id);
  });
  it('a saved step the speaker has passed falls back to the first one left', () => {
    const st = resumeState(plan1433, { started: true, stepId: s0700.id, screen: 'C' });
    expect(st.stepId).toBe('');
    expect(currentStep(plan1433, st).id).toBe(s1500.id);
    expect(ladderView(plan1433, st)).toBe('B');
  });
  it('storage that throws never breaks the guide', () => {
    const bad = { getItem: () => { throw new Error('blocked'); }, setItem: () => { throw new Error('blocked'); } };
    expect(loadLadderState('x', bad)).toBe(null);
    expect(() => saveLadderState('x', {}, bad)).not.toThrow();
  });
});

describe('every bundle has every ladder string', () => {
  it('EN and DE are complete', async () => {
    const de = (await import('./i18n/bundles/de.json')).default;
    const keys = Object.keys(en).filter(k => k.startsWith('fwLadder.'));
    expect(keys.length).toBeGreaterThan(80);
    for (const k of keys) expect(de[k], k).toBeTruthy();
  });
});
