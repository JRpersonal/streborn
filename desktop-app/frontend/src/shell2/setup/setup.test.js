// The UI 2.0 setup wizard (#1288, phase 4): Find's model, the reading of the
// classic install screens, the Confirm loop, the stepper, the check contract,
// and the rules every part keeps (strings in every bundle, CSS only under
// html.ui2 with known tokens, import without a DOM).
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { setLocale } from '../../i18n/index.js';
import { FACTORY, findHTML, findModel, selectedCandidate } from './find.js';
import { installBusy, installIntroHTML, installNoteHTML, installOutcome, outcomeStepState } from './install.js';
import { confirmHTML, confirmLiveText, confirmLoop, newStrHost, strSnapshot } from './confirm.js';
import { EXTERNAL_FLASH_QUIET_MS, clearFlashMark, flashMark, markFlash } from './check.js';
import {
  SETUP_PLACES, STATE_LABEL_KEYS, STEP_LABEL_KEYS, WIZARD_STEPS,
  adaptCheckStep, backTarget, checkAborted, checkRoute, checkRunner, checkSkipped,
  stepperHTML, stepperModel, wizardBusy,
} from './wizard.js';

setLocale('en');

const STOCK = (n, name, extra = {}) => ({ host: `192.0.2.${n}`, port: 8090, name, friendlyName: name, model: 'SoundTouch 10', kind: 'stock', ...extra });
const STR = (n, name, extra = {}) => ({ host: `192.0.2.${n}`, port: 8888, name, friendlyName: name, model: 'SoundTouch 20', kind: 'str', version: 'v1.0.19', ...extra });

// A stand-in for the few element methods installOutcome reads.
function fakeEl({ has = [], warns = [], oks = [], text = '' } = {}) {
  const mk = (s) => ({ textContent: s });
  return {
    textContent: text || [...warns, ...oks].join(' '),
    querySelector: (sel) => (sel.split(',').map((s) => s.trim()).some((s) => has.includes(s)) ? {} : null),
    querySelectorAll: (sel) => (sel === '.setup-warn' ? warns.map(mk) : sel === '.setup-ok' ? oks.map(mk) : []),
  };
}

describe('Find', () => {
  const boxes = [
    STR(10, 'Living room'),
    STOCK(51, 'Kitchen'),
    STOCK(52, 'Attic', { offline: true }),
    STOCK(53, 'Bar', { model: 'SoundTouch 300' }),
    STOCK(51, 'Kitchen again'), // announced twice
    STOCK(54, 'Cellar', { strNotRunning: true }),
    STR(11, 'Bedroom', { offline: true }),
  ];

  it('offers every Bose original once, sorted, and lists STR speakers apart', () => {
    const m = findModel(boxes, {});
    expect(m.candidates.map((c) => c.name)).toEqual(['Attic', 'Bar', 'Cellar', 'Kitchen']);
    expect(m.onStr.map((r) => r.name)).toEqual(['Bedroom', 'Living room']);
    expect(m.empty).toBe(false);
    expect(m.factory).toBe(false);
  });

  it('never selects an offline speaker, marks limited models and repairs', () => {
    const m = findModel(boxes, { selected: '192.0.2.52' });
    expect(selectedCandidate(m)).toBeNull();
    const by = Object.fromEntries(m.candidates.map((c) => [c.name, c]));
    expect(by.Attic.offline).toBe(true);
    expect(by.Bar.limited).toBe(true);
    expect(by.Cellar.strNotRunning).toBe(true);
    expect(selectedCandidate(findModel(boxes, { selected: '192.0.2.51' })).name).toBe('Kitchen');
    expect(findModel(boxes, { selected: FACTORY }).factory).toBe(true);
  });

  it('uses the app label for the name', () => {
    const m = findModel([STOCK(51, 'Kitchen')], { label: () => 'Kitchen (Bose)' });
    expect(m.candidates[0].name).toBe('Kitchen (Bose)');
  });

  it('renders radios for the originals and the network-less speaker, never for an STR speaker', () => {
    const html = findHTML(findModel(boxes, { selected: '192.0.2.51' }));
    expect(html.match(/type="radio"/g)).toHaveLength(5); // four originals + not on the network
    expect(html).toContain(`value="${FACTORY}"`);
    expect(html).toMatch(/value="192\.0\.2\.51" checked/);
    expect(html).toMatch(/value="192\.0\.2\.52" disabled/);
    expect(html).not.toContain('value="192.0.2.10"');
    expect(html).toContain('aria-label="Open the page of Living room"');
    expect(html).toContain('Attic is not answering right now.');
    expect(html).toContain('<legend');
    // Status in words next to the dot.
    expect(html).toContain('Ready for STR');
    expect(html).toContain('Offline');
    expect(html).toContain('On STR');
  });

  it('explains an empty list and still offers the speaker that is not on the network', () => {
    const m = findModel([STR(10, 'Living room')], {});
    expect(m.empty).toBe(true);
    const html = findHTML(m);
    expect(html).toContain('No Bose original found');
    expect(html).toContain(`value="${FACTORY}"`);
  });
});

describe('Install: reading the classic screens', () => {
  const texts = { finishing: 'Update finishing.', done: 'STR is installed.' };
  const cases = [
    [{}, ''],
    [{ text: 'Installing STR over the network on Kitchen' }, 'running'],
    [{ has: ['.setup-checklist'] }, 'running'],
    [{ has: ['.setup-pair-choices'], warns: ['Stereo pair found'] }, 'running'],
    [{ has: ['#setupWaitPanel'] }, 'waiting'],
    [{ has: ['.setup-err'] }, 'failed'],
    [{ has: ['#setupUpdateAgentBtn'] }, 'str-running'],
    [{ warns: ['Update finishing.'] }, 'update-finishing'],
    [{ has: ['#installGoMusic', '.setup-checklist'] }, 'done'],
    [{ oks: ['STR is installed.'] }, 'done'],
    [{ oks: ['STR is installed.'], has: ['.setup-checklist'] }, 'running'],
  ];
  for (const [spec, want] of cases) {
    it(`${JSON.stringify(spec)} reads as '${want}'`, () => {
      expect(installOutcome(fakeEl(spec), fakeEl(), texts)).toBe(want);
    });
  }

  it('reads the stick wait panel when the result is empty', () => {
    expect(installOutcome(fakeEl(), fakeEl({ has: ['.setup-await-ready'] }), texts)).toBe('stick');
  });

  it('marks the step: a refusal is never a failure', () => {
    expect(outcomeStepState('failed')).toBe('failed');
    expect(outcomeStepState('done')).toBe('done');
    expect(outcomeStepState('str-running')).toBe('done');
    expect(outcomeStepState('update-finishing')).toBe('');
    expect(installBusy('running')).toBe(true);
    expect(installBusy('waiting')).toBe(true);
    expect(installBusy('update-finishing')).toBe(false);
  });

  it('says the refusals calmly, with the way on, and the failure as one', () => {
    const calm = installNoteHTML('str-running', 'Kitchen');
    expect(calm).toContain('role="status"');
    expect(calm).toContain('STR is already running on Kitchen.');
    expect(calm).toContain('data-act="confirm"');
    expect(calm).not.toContain('w2-note--failed');
    const fin = installNoteHTML('update-finishing', 'Kitchen');
    expect(fin).toContain('An update is still finishing on Kitchen.');
    expect(fin).toContain('aria-label="Go to Confirm and wait until Kitchen answers with STR"');
    expect(fin).not.toContain('role="alert"');
    const failed = installNoteHTML('failed', 'Kitchen');
    expect(failed).toContain('role="alert"');
    expect(failed).toContain('The install on Kitchen did not finish.');
    expect(installNoteHTML('', 'Kitchen')).toBe('');
  });

  it('leads with the route and names the speaker, or says it is gone', () => {
    expect(installIntroHTML('network', 'Kitchen', false)).toContain('over your network');
    expect(installIntroHTML('stick', 'Kitchen', false)).toContain('with a USB stick');
    const gone = installIntroHTML('network', 'Kitchen', true);
    expect(gone).toContain('Kitchen is not on the network right now.');
    expect(gone).toContain('data-act="search"');
  });
});

describe('Confirm', () => {
  const clock = () => {
    let now = 0;
    return { now: () => now, sleep: async (ms) => { now += ms; } };
  };

  it('is done only when the agent reports a version', async () => {
    const c = clock();
    const answers = [null, { version: '' }, 'throw', { version: 'v1.0.19', build: 'b' }];
    let i = 0;
    const states = [];
    const r = await confirmLoop({
      probe: async () => { const a = answers[i++]; if (a === 'throw') throw new Error('no route'); return a; },
      onState: (s) => states.push(s.attempt),
      ...c,
    });
    expect(r).toEqual({ ok: true, version: { version: 'v1.0.19', build: 'b' } });
    expect(states).toEqual([1, 2, 3]);
  });

  it('gives up when the window runs out', async () => {
    const c = clock();
    const r = await confirmLoop({ probe: async () => null, windowMs: 10_000, pollMs: 3000, ...c });
    expect(r).toEqual({ ok: false, reason: 'timeout' });
  });

  it('stops when the user leaves', async () => {
    const c = clock();
    let alive = true;
    const r = await confirmLoop({ probe: async () => { alive = false; return { version: 'v1' }; }, alive: () => alive, ...c });
    expect(r).toEqual({ ok: false, reason: 'stopped' });
  });

  it('finds the one speaker a stick install brought in', () => {
    const before = new Set(['192.0.2.10']);
    expect(newStrHost([STR(10, 'Living room'), STOCK(51, 'Kitchen')], before)).toBe('');
    expect(newStrHost([STR(10, 'Living room'), STR(51, 'Kitchen')], before)).toBe('192.0.2.51');
    expect(newStrHost([STR(51, 'Kitchen'), STR(52, 'Attic')], before)).toBe('');
    expect(newStrHost([STR(51, 'Kitchen', { offline: true })], before)).toBe('');
  });

  it('does not take a known STR speaker under a new address for the new one', () => {
    const snap = strSnapshot([STR(10, 'Living room', { deviceID: 'aabbccddeeff' }), STOCK(51, 'Kitchen', { deviceID: '112233445566' })]);
    expect([...snap.hosts]).toEqual(['192.0.2.10']);
    // DHCP gave Living room a new address: its device ID still says it is known.
    expect(newStrHost([STR(12, 'Living room', { deviceID: 'AABBCCDDEEFF' })], snap)).toBe('');
    // Kitchen, a Bose original in the snapshot, now runs STR: that is the one.
    expect(newStrHost([STR(12, 'Living room', { deviceID: 'AABBCCDDEEFF' }), STR(51, 'Kitchen', { deviceID: '112233445566' })], snap)).toBe('192.0.2.51');
  });

  it('says the result through a live text, and offers a way out when no answer came', () => {
    expect(confirmLiveText({ status: 'waiting', name: 'Kitchen', host: '192.0.2.51' })).toBe('Waiting for Kitchen to answer with STR');
    expect(confirmLiveText({ status: 'done', name: 'Kitchen', version: { version: 'v1.0.19' } })).toContain('Kitchen answered with STR v1.0.19.');
    expect(confirmLiveText({ status: 'failed', name: 'Kitchen' })).toBe('');
    // The visual blocks no longer carry their own status role (or aria-busy):
    // the persistent live region in the wizard speaks for them.
    expect(confirmHTML({ status: 'waiting', name: 'Kitchen', host: '192.0.2.51' })).not.toMatch(/role=|aria-busy/);
    expect(confirmHTML({ status: 'done', name: 'Kitchen', version: { version: 'v1' } })).not.toContain('role=');
    const failed = confirmHTML({ status: 'failed', name: 'Kitchen' });
    expect(failed).toContain('data-act="backInstall"');
    expect(failed).toContain('aria-label="Go back to installing STR on Kitchen"');
    expect(failed).toContain('data-act="another"');
  });

  it('names the speaker in every state', () => {
    expect(confirmHTML({ status: 'waiting', name: 'Kitchen', host: '192.0.2.51' })).toContain('Waiting for Kitchen to answer with STR');
    expect(confirmHTML({ status: 'waiting', name: '', host: '' })).toContain('Looking for your new speaker on the network');
    const done = confirmHTML({ status: 'done', name: 'Kitchen', version: { version: 'v1.0.19' } });
    expect(done).toContain('Kitchen answered with STR v1.0.19.');
    expect(done).toContain('aria-label="Open the page of Kitchen"');
    expect(done).toContain('data-act="another"');
    const failed = confirmHTML({ status: 'failed', name: 'Kitchen' });
    expect(failed).toContain('role="alert"');
    expect(failed).toContain('data-act="recheck"');
  });
});

describe('Wizard', () => {
  it('has four steps, each named', () => {
    expect(WIZARD_STEPS).toEqual(['find', 'check', 'install', 'confirm']);
    for (const s of WIZARD_STEPS) expect(STEP_LABEL_KEYS[s]).toMatch(/^ui2\.setup\.wizard\.step\./);
  });

  it('shows done, current, waiting, failed and skipped', () => {
    expect(stepperModel('find').map((s) => s.state)).toEqual(['current', 'waiting', 'waiting', 'waiting']);
    expect(stepperModel('install', { find: 'done', check: 'done', install: 'failed' }).map((s) => s.state))
      .toEqual(['done', 'done', 'failed', 'waiting']);
    expect(stepperModel('install', { find: 'done', check: 'skipped' }).map((s) => s.state))
      .toEqual(['done', 'skipped', 'current', 'waiting']);
    expect(stepperModel('confirm', { find: 'done', check: 'done', install: 'done', confirm: 'done' }).map((s) => s.state))
      .toEqual(['done', 'done', 'done', 'done']);
  });

  it('says every state in words and marks the current step for screen readers', () => {
    const html = stepperHTML(stepperModel('check', { find: 'done' }));
    expect(html.match(/aria-current="step"/g)).toHaveLength(1);
    expect(html).toMatch(/data-step="check" aria-current="step"/);
    for (const w of ['Step 1', 'Find', 'Done', 'Current step', 'Waiting']) expect(html).toContain(w);
    expect(html).toContain('aria-hidden="true"');
    for (const k of Object.values(STATE_LABEL_KEYS)) expect(k).toMatch(/^ui2\.setup\.wizard\.state\./);
  });

  it('lets Back go only where it is safe', () => {
    expect(backTarget({ step: 'find' })).toBe('');
    expect(backTarget({ step: 'check' })).toBe('find');
    expect(backTarget({ step: 'check', checkBusy: true })).toBe('');
    expect(backTarget({ step: 'install', installKind: '' })).toBe('check');
    expect(backTarget({ step: 'install', installKind: 'failed' })).toBe('check');
    expect(backTarget({ step: 'install', installKind: '', checkSkipped: true })).toBe('find');
    expect(backTarget({ step: 'install', installKind: 'running' })).toBe('');
    expect(backTarget({ step: 'install', installKind: 'waiting' })).toBe('');
    expect(backTarget({ step: 'install', installKind: 'done' })).toBe('');
    expect(backTarget({ step: 'confirm' })).toBe('');
  });

  it('reads the check module and its answer', () => {
    const fn = async () => ({ route: 'network' });
    expect(checkRunner({})).toBeNull();
    expect(checkRunner({ './check.js': { runCheck: fn } })).toBe(fn);
    expect(checkRunner({ './check.js': { default: fn } })).toBe(fn);
    expect(checkRunner({ './check.js': { other: 1 } })).toBeNull();
    expect(checkRoute({ route: 'stick' })).toBe('stick');
    expect(checkRoute('stick')).toBe('stick');
    expect(checkRoute({ stick: true })).toBe('stick');
    expect(checkRoute({ route: 'network' })).toBe('network');
    expect(checkRoute(undefined)).toBe('network');
  });

  it('wires check.js renderCheckStep in: the label, busy only while updating, the answers', async () => {
    const seen = [];
    let ctxSeen = null;
    const render = async (el, ctx) => {
      ctxSeen = ctx;
      for (const phase of ['checking', 'needs-update', 'updating', 'updating', 'ok']) ctx.onState({ phase });
      return { ok: true, firmware: '27.0.6', route: 'route-a' };
    };
    const run = checkRunner({ './check.js': { renderCheckStep: render } });
    expect(typeof run).toBe('function');
    const res = await run({}, { box: STOCK(51, 'kitchen-raw'), label: 'Kitchen', setBusy: (b) => seen.push(b) });
    expect(ctxSeen.box.name).toBe('Kitchen');
    expect(ctxSeen.box.host).toBe('192.0.2.51');
    expect(seen).toEqual([true, false]);
    expect(checkRoute(res)).toBe('network');
    expect(checkSkipped(res)).toBe(false);
    expect(checkSkipped({ ok: true, route: 'skip' })).toBe(true);
    expect(checkSkipped({ ok: true, route: 'unknown' })).toBe(true);
    expect(checkAborted({ ok: false, route: 'aborted' })).toBe(true);
    expect(checkAborted({ ok: true, route: 'current' })).toBe(false);
    expect(typeof adaptCheckStep(render)).toBe('function');
  });

  it('is not busy without the shell', () => {
    expect(wizardBusy()).toBe(false);
  });

  it('gives every place of the classic setup a place in the wizard', () => {
    expect(SETUP_PLACES.length).toBeGreaterThanOrEqual(16);
    for (const p of SETUP_PLACES) expect(WIZARD_STEPS).toContain(p.place);
    for (const id of ['setupTargetBody', 'setupAPPushPanel', 'setupPrimaryAction', 'setupResult', 'setupStickDetails', 'setupAwaitResult', 'setupUpdateAgentBtn', 'setupStrSettings']) {
      expect(SETUP_PLACES.some((p) => p.classic.includes(`#${id}`)), id).toBe(true);
    }
  });
});

describe('Wizard strings and styles', () => {
  const dir = new URL('../../i18n/bundles/', import.meta.url);
  const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
    .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));
  const src = ['wizard.js', 'find.js', 'install.js', 'confirm.js']
    .map((f) => readFileSync(new URL(`./${f}`, import.meta.url), 'utf8')).join('\n');
  const used = new Set([...src.matchAll(/'(ui2\.[a-zA-Z.]+)'/g)].map((m) => m[1]));
  // Keys built from a prefix ('ui2.setup.wizard.install.toConfirm' + 'Label').
  for (const k of [...used]) if (/install\.(toConfirm|waitConfirm)$/.test(k)) used.add(`${k}Label`);
  for (const k of [...Object.values(STEP_LABEL_KEYS), ...Object.values(STATE_LABEL_KEYS)]) used.add(k);
  const placeholders = (s) => [...String(s).matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();

  it('finds its keys', () => {
    expect([...used].filter((k) => k.startsWith('ui2.setup.wizard.')).length).toBeGreaterThan(60);
  });

  for (const [code, b] of Object.entries(bundles)) {
    it(`${code} has every wizard string with the English placeholders, no dash as punctuation`, () => {
      for (const k of used) {
        expect(typeof b[k] === 'string' && b[k].trim().length > 0, `${code}: ${k}`).toBe(true);
        expect(placeholders(b[k]), `${code}: ${k}`).toEqual(placeholders(bundles.en[k]));
        if (k.startsWith('ui2.setup.wizard.')) expect(b[k], `${code}: ${k}`).not.toMatch(/[–—]| - /);
      }
    });
  }

  it('ships every bundle and writes German informally with umlauts', () => {
    expect(Object.keys(bundles)).toHaveLength(13);
    expect(bundles.de['ui2.setup.wizard.find.intro']).toContain('Wähle');
    expect(bundles.de['ui2.setup.wizard.state.skipped']).toBe('Übersprungen');
    expect(bundles.de['ui2.setup.wizard.find.offlineNote']).toContain('such');
  });

  const css = readFileSync(new URL('./setup.css', import.meta.url), 'utf8');
  const shellCss = readFileSync(new URL('../shell2.css', import.meta.url), 'utf8');

  it('styles only under html.ui2', () => {
    const body = css.replace(/\/\*[\s\S]*?\*\//g, '');
    const selectors = [...body.matchAll(/(^|\})\s*([^{}@]+)\{/g)].map((m) => m[2].trim())
      .filter((s) => s && !/^(to|from|\d+%)$/.test(s));
    expect(selectors.length).toBeGreaterThan(20);
    for (const sel of selectors) {
      for (const part of sel.split(',')) expect(part.trim(), sel).toMatch(/^html\.ui2\b/);
    }
  });

  it('reads only tokens the shell defines', () => {
    const defined = new Set([...shellCss.matchAll(/(--u-[\w-]+)\s*:/g)].map((m) => m[1]));
    for (const m of css.matchAll(/var\((--[\w-]+)/g)) expect(defined.has(m[1]), m[1]).toBe(true);
  });
});

describe('A firmware update outlives a reload', () => {
  it('keeps the running update per speaker in sessionStorage until it ends or goes quiet', () => {
    const store = new Map();
    const prev = globalThis.sessionStorage;
    globalThis.sessionStorage = {
      getItem: (k) => (store.has(k) ? store.get(k) : null),
      setItem: (k, v) => { store.set(k, String(v)); },
      removeItem: (k) => { store.delete(k); },
    };
    try {
      expect(flashMark('192.0.2.51')).toBeNull();
      markFlash('192.0.2.51', 'upload', 1000);
      expect(flashMark('192.0.2.51', 2000)).toEqual({ phase: 'upload' });
      expect(flashMark('192.0.2.52', 2000)).toBeNull();
      // Quiet for longer than an install and restart take: forgotten.
      expect(flashMark('192.0.2.51', 1000 + EXTERNAL_FLASH_QUIET_MS + 1)).toBeNull();
      expect(store.size).toBe(0);
      markFlash('192.0.2.51', 'install', 1000);
      clearFlashMark('192.0.2.51');
      expect(flashMark('192.0.2.51', 1001)).toBeNull();
    } finally {
      globalThis.sessionStorage = prev;
    }
  });

  it('works without storage', () => {
    const prev = globalThis.sessionStorage;
    const deny = () => { throw new Error('denied'); };
    globalThis.sessionStorage = { getItem: deny, setItem: deny, removeItem: deny };
    try {
      expect(() => markFlash('192.0.2.51', 'upload')).not.toThrow();
      expect(flashMark('192.0.2.51')).toBeNull();
    } finally {
      globalThis.sessionStorage = prev;
    }
  });
});
