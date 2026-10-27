// The check step of the UI 2.0 setup wizard (checkmodel.js, check.js),
// without a DOM: which firmware gets which route, when "continue without
// updating" is offered, what counts as confirmed, and that every string exists
// in all thirteen bundles.
import { describe, it, expect, beforeAll } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { setLocale } from '../../i18n/index.js';
import { fileSource } from '../../test/source.js';
import {
  STICK_ROUTE_URL,
  canSkip,
  checkCase,
  checkRows,
  firmwareConfirmed,
  hardwareKey,
  identOf,
  rowStatus,
  rowsHtml,
  shortOf,
  speakerLabel,
  stepPhase,
} from './checkmodel.js';

const fw = (short, extra = {}) => ({
  reachable: true, model: 'SoundTouch 20', moduleType: 'scm', variant: 'spotty',
  firmware: short ? `${short}.12345.678` : '', short, latest: '27.0.6', ...extra,
});
const BOX = { host: '192.0.2.51', name: 'Guest room', model: 'SoundTouch 20' };

beforeAll(() => { setLocale('en'); });

describe('checkCase', () => {
  it('no answer is unreachable', () => {
    expect(checkCase(null, BOX)).toBe('unreachable');
    expect(checkCase({ reachable: false }, BOX)).toBe('unreachable');
  });

  it('an answer without a version is unknown', () => {
    expect(checkCase(fw(''), BOX)).toBe('unknown');
  });

  it('27.0.6 and anything newer need nothing', () => {
    expect(checkCase(fw('27.0.6'), BOX)).toBe('current');
    expect(checkCase(fw('27.1.0'), BOX)).toBe('current');
  });

  it('15.x up to 27.0.5 take the one-click update (Route A)', () => {
    for (const v of ['15.0.4', '16.0.9', '20.0.6', '27.0.3', '27.0.5']) {
      expect(checkCase(fw(v), BOX), v).toBe('routeA');
    }
  });

  it('below 15 on a first generation SoundTouch 20/30/Portable takes the stick ladder', () => {
    for (const model of ['SoundTouch 20', 'SoundTouch 30', 'SoundTouch Portable']) {
      expect(checkCase(fw('7.0.37', { model }), BOX), model).toBe('ladder');
      expect(checkCase(fw('14.0.33', { model, moduleType: '' }), BOX), model).toBe('ladder');
    }
  });

  it('below 15 on the newer hardware or another model keeps the single-step guide', () => {
    expect(checkCase(fw('7.0.37', { moduleType: 'sm2' }), BOX)).toBe('guide');
    expect(checkCase(fw('10.0.11', { model: 'SoundTouch 10', moduleType: 'sm2' }), BOX)).toBe('guide');
    expect(checkCase(fw('2.1.51', { model: 'Wave SoundTouch', moduleType: '' }), BOX)).toBe('guide');
  });

  it('falls back to the chosen box for the model', () => {
    expect(checkCase(fw('7.0.37', { model: '' }), { ...BOX, model: 'SoundTouch 30' })).toBe('ladder');
  });
});

describe('canSkip', () => {
  it('lets the install go ahead only where it still starts over the network', () => {
    expect(canSkip('routeA')).toBe(true);
    expect(canSkip('unknown')).toBe(true);
    for (const k of ['ladder', 'guide', 'unreachable', 'current']) expect(canSkip(k), k).toBe(false);
  });
});

describe('firmwareConfirmed', () => {
  it('is true only for a read-back that reached 27.0.6', () => {
    expect(firmwareConfirmed(fw('27.0.6'))).toBe(true);
    expect(firmwareConfirmed(fw('27.0.5'))).toBe(false);
    expect(firmwareConfirmed(fw(''))).toBe(false);
    expect(firmwareConfirmed({ reachable: false, short: '27.0.6' })).toBe(false);
    expect(firmwareConfirmed(null)).toBe(false);
  });
});

describe('helpers', () => {
  it('shortOf cuts the long version', () => {
    expect(shortOf({ firmware: '27.0.6.46330.5043500' })).toBe('27.0.6');
    expect(shortOf({ short: '15.0.4' })).toBe('15.0.4');
    expect(shortOf({})).toBe('');
  });

  it('identOf hands the guide the speaker and its address', () => {
    expect(identOf(fw('7.0.37'), BOX)).toEqual({
      model: 'SoundTouch 20', moduleType: 'scm', variant: 'spotty', current: '7.0.37.12345.678', host: '192.0.2.51',
    });
  });

  it('speakerLabel prefers the name', () => {
    expect(speakerLabel(BOX)).toBe('Guest room');
    expect(speakerLabel({ friendlyName: 'Kitchen', host: '192.0.2.1' })).toBe('Kitchen');
    expect(speakerLabel({ host: '192.0.2.1' })).toBe('192.0.2.1');
  });

  it('hardwareKey names the chassis only where the speaker says it', () => {
    expect(hardwareKey('sm2')).toBe('ui2.setup.check.hwNewer');
    expect(hardwareKey('SCM')).toBe('ui2.setup.check.hwOlder');
    expect(hardwareKey('')).toBe('');
  });

  it('the stick route points at Route C of the firmware guide', () => {
    expect(STICK_ROUTE_URL).toMatch(/FIRMWARE-UPDATE\.md#5-route-c-usb-stick$/);
  });
});

describe('checkRows', () => {
  it('an unreachable speaker gets one row with the error, named', () => {
    const rows = checkRows({ kase: 'unreachable', box: BOX });
    expect(rows).toHaveLength(1);
    expect(rows[0].status).toBe('error');
    expect(rows[0].detail).toContain('Guest room');
    expect(rows[0].detail).toContain('192.0.2.51');
  });

  it('a speaker below 27.0.6 shows the version and what STR needs', () => {
    const rows = checkRows({ kase: 'routeA', box: BOX, fw: fw('15.0.4'), ms: 40.2 });
    expect(rows.map((r) => r.id)).toEqual(['reach', 'model', 'firmware']);
    expect(rows[0].detail).toBe('Answered in 40 ms');
    expect(rows[1].detail).toBe('SoundTouch 20, older hardware');
    expect(rows[2]).toMatchObject({ status: 'warn', detail: '15.0.4 installed, STR needs 27.0.6' });
  });

  it('is busy while the update runs and ok only once confirmed', () => {
    const v = { kase: 'routeA', box: BOX, fw: fw('15.0.4'), ms: 5 };
    expect(checkRows({ ...v, flash: { phase: 'upload' } })[2].status).toBe('busy');
    expect(checkRows({ ...v, flash: { phase: 'done' } })[2].status).toBe('warn');
    expect(checkRows({ ...v, flash: { phase: 'error' } })[2].status).toBe('warn');
    const done = checkRows({ ...v, fw: fw('27.0.6'), confirmed: true })[2];
    expect(done.status).toBe('ok');
    expect(done.detail).toBe('Guest room reports 27.0.6 after the update');
  });

  it('a current speaker is ok, an unreadable version is unknown', () => {
    expect(checkRows({ kase: 'current', box: BOX, fw: fw('27.0.6'), ms: 1 })[2].status).toBe('ok');
    expect(checkRows({ kase: 'unknown', box: BOX, fw: fw(''), ms: 1 })[2].status).toBe('unknown');
  });

  it('renders words and a mark for every status, never colour alone', () => {
    const html = rowsHtml(checkRows({ kase: 'routeA', box: BOX, fw: fw('15.0.4'), ms: 9 }));
    expect(html).toContain('Needs update');
    expect(html).toContain('aria-hidden="true">!</span>');
    for (const k of ['ok', 'warn', 'busy', 'wait', 'error', 'unknown']) {
      expect(rowStatus(k).mark.length, k).toBeGreaterThan(0);
    }
    expect(rowsHtml([{ id: 'x', titleKey: 'ui2.setup.check.model', detail: '<b>', status: 'ok' }])).toContain('&lt;b&gt;');
  });
});

describe('stepPhase', () => {
  it('tells the wizard why Install is still locked', () => {
    expect(stepPhase({ loading: true })).toBe('checking');
    expect(stepPhase({ kase: 'unreachable' })).toBe('unreachable');
    expect(stepPhase({ kase: 'current' })).toBe('ok');
    expect(stepPhase({ kase: 'routeA', confirmed: true })).toBe('ok');
    expect(stepPhase({ kase: 'routeA', flash: { phase: 'install' } })).toBe('updating');
    expect(stepPhase({ kase: 'routeA', flash: { phase: 'error' } })).toBe('failed');
    expect(stepPhase({ kase: 'ladder' })).toBe('needs-update');
  });
});

describe('check.js', () => {
  const src = fileSource('shell2/setup/check.js');

  it('touches no DOM at module scope', () => {
    // Everything with document/window lives inside renderCheckStep: a view
    // module loads before main.js builds the #view-* containers.
    const top = src.split('export function renderCheckStep')[0];
    expect(top).not.toMatch(/\bdocument\./);
    expect(top).not.toMatch(/\bwindow\./);
  });

  it('reuses the classic guide, ladder and update instead of a copy', () => {
    expect(src).toContain("from '../../fwguide.js'");
    expect(src).toContain('firmwareGuideMount(');
    expect(src).toContain('wireFirmwareGuides(');
    expect(src).toContain('FlashBoseFirmware');
    expect(src).toContain('firmwareLadderDeps()');
    expect(src).not.toMatch(/data-ladder=/);
  });

  it('documents the contract with the wizard', () => {
    expect(src).toContain('renderCheckStep(container, ctx) -> Promise<{ ok, firmware, route }>');
  });
});

describe('strings', () => {
  const dir = new URL('../../i18n/bundles/', import.meta.url);
  const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
    .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));
  const used = new Set();
  for (const f of ['shell2/setup/check.js', 'shell2/setup/checkmodel.js']) {
    for (const m of fileSource(f).matchAll(/'(ui2\.setup\.check\.[a-zA-Z]+)'/g)) used.add(m[1]);
  }
  const ph = (s) => [...String(s).matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();

  it('finds the keys in the code', () => {
    expect(used.size).toBeGreaterThan(30);
    expect(Object.keys(bundles)).toHaveLength(13);
  });

  for (const [code, b] of Object.entries(bundles)) {
    it(`${code} has every check string with the English placeholders, no dashes`, () => {
      for (const k of used) {
        expect(typeof b[k] === 'string' && b[k].trim().length > 0, `${code}: ${k}`).toBe(true);
        expect(ph(b[k]), `${code}: ${k}`).toEqual(ph(bundles.en[k]));
        expect(b[k], `${code}: ${k}`).not.toMatch(/[–—]| - /);
      }
    });
  }

  it('writes German informally with real umlauts', () => {
    expect(bundles.de['ui2.setup.check.reachNo']).toContain('Prüfe');
    expect(bundles.de['ui2.setup.check.routeABody']).toContain('über dein Netzwerk');
  });
});
