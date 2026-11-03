// The checklist for a speaker that does not come back after an update or an
// install (darkafterupdate.js).
//
// Background: about ten owners wrote in with a speaker that went dark right at
// an ST Reborn update or install, most of them a worn power supply or a failing
// amplifier that gave out at the restart (mails 2026-09-30, 2026-10-20,
// 2026-10-24, #763). The app said "taking longer than expected" and nothing
// else. These pin the gate, the order of the stages, the per-model wording,
// the link to the firmware guide's USB stick route, and the places that show it.
import { describe, it, expect, beforeAll } from 'vitest';
import { readFileSync } from 'node:fs';
import { setLocale } from './i18n/index.js';
import {
  DARK_USB_DOCS_URL,
  darkAfterUpdate,
  darkChecklistSteps,
  darkChecklistHtml,
  darkChecklistRowHtml,
  handleDarkChecklistClick,
} from './darkafterupdate.js';
import { confirmHTML } from './shell2/setup/confirm.js';

setLocale('en');
beforeAll(() => { setLocale('en'); });

const LANGS = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const bundle = (l) => JSON.parse(readFileSync(new URL(`./i18n/bundles/${l}.json`, import.meta.url), 'utf8'));
const src = (p) => readFileSync(new URL(p, import.meta.url), 'utf8');

// The keys the checklist renders, collected from every model's step list.
function usedKeys() {
  const keys = new Set(['darkCheck.title', 'darkCheck.introUpdate', 'darkCheck.introInstall', 'darkCheck.lead',
    'darkCheck.rowSummary', 'darkCheck.usbLink', 'darkCheck.hardware']);
  for (const m of ['SoundTouch 10', 'SoundTouch 20', 'SoundTouch 30', 'SoundTouch Portable', 'SoundTouch 300', 'Wave SoundTouch', '']) {
    for (const s of darkChecklistSteps(m)) {
      keys.add(s.head);
      s.lines.forEach((k) => keys.add(k));
    }
  }
  return [...keys];
}

describe('when the checklist shows', () => {
  const dark = { outcome: 'timeout', verdict: 'unreachable', confirmed: false };

  it('shows for an update that ended with nothing answering at the address', () => {
    expect(darkAfterUpdate(dark)).toBe(true);
  });

  it('never for a confirmed update, a speaker that answers, one in setup, or one that moved address', () => {
    expect(darkAfterUpdate(null)).toBe(false);
    expect(darkAfterUpdate({ ...dark, confirmed: true })).toBe(false);
    expect(darkAfterUpdate({ ...dark, outcome: 'done' })).toBe(false);
    // agentGone: the speaker answers its own Bose API, a power cycle is the fix.
    expect(darkAfterUpdate({ ...dark, outcome: 'agentGone', verdict: 'agent-gone' })).toBe(false);
    // boxInSetup: pulling the plug mid-setup is the one wrong advice.
    expect(darkAfterUpdate({ ...dark, outcome: 'boxInSetup', verdict: 'box-in-setup' })).toBe(false);
    // The speaker answered with the old build: not a dark speaker.
    expect(darkAfterUpdate({ ...dark, verdict: 'not-landed' })).toBe(false);
    expect(darkAfterUpdate({ ...dark, verdict: 'landed-not-running' })).toBe(false);
    expect(darkAfterUpdate({ ...dark, unmet: 'address-changed' })).toBe(false);
  });
});

describe('the stages', () => {
  it('go wait, power, Bose app, then resets and the firmware by stick, in that order', () => {
    expect(darkChecklistSteps('SoundTouch 10').map((s) => s.id)).toEqual(['wait', 'power', 'wifi', 'dark']);
  });

  it('say up to 30 minutes, another socket and another matching power supply', () => {
    setLocale('en');
    const en = bundle('en');
    expect(en['darkCheck.wait']).toMatch(/30 minutes/);
    expect(en['darkCheck.power']).toMatch(/10 seconds/);
    expect(en['darkCheck.power']).toMatch(/another wall socket/);
    expect(en['darkCheck.power']).toMatch(/another power supply of the same type/);
  });

  it('send a speaker that lights up but is missing to the Bose app, "Add speaker"', () => {
    expect(bundle('en')['darkCheck.wifi']).toMatch(/Bose SoundTouch app/);
    expect(bundle('en')['darkCheck.wifi']).toMatch(/Add speaker/);
  });

  it('put the factory reset before the USB stick, and link the guide\'s stick route', () => {
    const dark = darkChecklistSteps('SoundTouch 20').find((s) => s.id === 'dark');
    expect(dark.lines[0]).toBe('darkCheck.resetKeys');
    expect(dark.lines.indexOf('darkCheck.usb')).toBeGreaterThan(0);
    expect(dark.lines).toContain('fwLadder.nsStick');
    expect(dark.lines).toContain('fwLadder.c3FourVol');
    expect(dark.link).toBe(DARK_USB_DOCS_URL);
    expect(DARK_USB_DOCS_URL).toMatch(/docs\/FIRMWARE-UPDATE\.md#5-route-c-usb-stick$/);
  });

  it('links a section the firmware guide really has', () => {
    const doc = readFileSync(new URL('../../../docs/FIRMWARE-UPDATE.md', import.meta.url), 'utf8');
    expect(doc).toMatch(/^## 5\. Route C: USB stick$/m);
  });

  it('give the Portable its battery wording: no unplugging, the power button and its buttons', () => {
    const steps = darkChecklistSteps('SoundTouch Portable');
    expect(steps.find((s) => s.id === 'power').lines).toEqual(['darkCheck.powerBattery']);
    const dark = steps.find((s) => s.id === 'dark').lines;
    expect(dark).toContain('darkCheck.usbBattery');
    expect(dark).toContain('fwLadder.c3Battery');
    expect(dark).toContain('fwLadder.c3Unconfirmed');
  });

  it('invent no buttons for a model whose reset and stick start are not known', () => {
    for (const m of ['SoundTouch 300', 'Wave SoundTouch', '']) {
      const dark = darkChecklistSteps(m).find((s) => s.id === 'dark').lines;
      expect(dark, m).toContain('darkCheck.resetGeneric');
      expect(dark, m).toContain('fwLadder.c3Generic');
      expect(dark, m).not.toContain('darkCheck.resetKeys');
    }
  });
});

describe('the markup', () => {
  it('frames the update panel with the name, the stages, the guide link and Close', () => {
    setLocale('en');
    const html = darkChecklistHtml({ name: 'Kitchen', model: 'SoundTouch 10', context: 'update', framed: true });
    expect(html).toContain('Kitchen has not come back yet');
    expect(html).toContain('The update reached Kitchen');
    expect(html.match(/<li /g)).toHaveLength(4);
    expect(html).toContain(`data-darkopen="${DARK_USB_DOCS_URL}"`);
    expect(html).toContain('data-darkclose="1"');
    expect(html).toContain('most often the power supply');
  });

  it('says STR is on the speaker after an install, and leaves the frame to the screen around it', () => {
    setLocale('en');
    const html = darkChecklistHtml({ name: 'Kitchen', model: 'SoundTouch 10', context: 'install', framed: false });
    expect(html).toContain('Go through these steps in order');
    expect(html).not.toContain('data-darkclose');
    expect(html).not.toContain('has not come back yet');
    expect(darkChecklistHtml({ name: 'Kitchen', context: 'install', framed: true })).toContain('STR is on Kitchen');
  });

  it('escapes the speaker name', () => {
    const html = darkChecklistHtml({ name: '<b>x</b>', context: 'update', framed: true });
    expect(html).not.toContain('<b>x</b>');
    expect(html).toContain('&lt;b&gt;x&lt;/b&gt;');
  });

  it('folds out under an Update all row', () => {
    setLocale('en');
    const html = darkChecklistRowHtml({ name: 'Kitchen', model: 'SoundTouch 20' });
    expect(html.startsWith('<details')).toBe(true);
    expect(html).toContain('Not back yet: what you can do');
    expect(html).toContain('data-darkopen');
  });
});

describe('clicks', () => {
  const ev = (attrs) => {
    let prevented = false;
    const node = { dataset: attrs };
    return {
      target: { closest: (sel) => (Object.keys(attrs).some((k) => sel === `[data-${k}]`) ? node : null) },
      preventDefault: () => { prevented = true; },
      get prevented() { return prevented; },
    };
  };

  it('opens the guide link in the browser', () => {
    const opened = [];
    const e = ev({ darkopen: DARK_USB_DOCS_URL });
    expect(handleDarkChecklistClick(e, { openURL: (u) => opened.push(u) })).toBe(true);
    expect(opened).toEqual([DARK_USB_DOCS_URL]);
    expect(e.prevented).toBe(true);
  });

  it('closes on Close and ignores every other click', () => {
    let closed = 0;
    expect(handleDarkChecklistClick(ev({ darkclose: '1' }), { close: () => { closed++; } })).toBe(true);
    expect(closed).toBe(1);
    expect(handleDarkChecklistClick({ target: { closest: () => null } }, {})).toBe(false);
    expect(handleDarkChecklistClick({}, {})).toBe(false);
  });
});

describe('the words', () => {
  it('exist in all 13 languages, translated, as real text', () => {
    const en = bundle('en');
    for (const l of LANGS) {
      const b = bundle(l);
      for (const k of usedKeys()) {
        expect(typeof b[k], `${l}: ${k}`).toBe('string');
        expect(b[k].trim(), `${l}: ${k}`).not.toBe('');
        if (l !== 'en' && k.startsWith('darkCheck.')) expect(b[k], `${l}: ${k} is untranslated`).not.toBe(en[k]);
      }
    }
  });

  it('keep the placeholders, use no dashes as punctuation, and blame no band, WPA or firewall', () => {
    for (const l of LANGS) {
      const b = bundle(l);
      for (const k of Object.keys(b).filter((x) => x.startsWith('darkCheck.'))) {
        const v = b[k];
        expect(v, `${l}: ${k}`).not.toMatch(/[–—]| - /);
        expect(v, `${l}: ${k}`).not.toMatch(/GHz|WPA|firewall/i);
        if (['darkCheck.title', 'darkCheck.introUpdate', 'darkCheck.introInstall'].includes(k)) {
          expect(v, `${l}: ${k}`).toContain('{{name}}');
        }
      }
    }
  });

  it('talk to the user as du in German', () => {
    const de = bundle('de');
    for (const k of Object.keys(de).filter((x) => x.startsWith('darkCheck.'))) {
      expect(de[k], k).not.toMatch(/\b(Sie|Ihr|Ihren|Ihre)\b/);
    }
  });
});

describe('where it shows', () => {
  const main = src('./main.js');
  const setup = src('./views/setup.js');

  it('replaces the bare "took longer" toast after a single update, except on the 300', () => {
    const at = main.indexOf('darkAfterUpdate(result) && !(targetBox.model');
    expect(at).toBeGreaterThan(0);
    expect(main.indexOf('showDarkAfterUpdate(targetBox)', at)).toBeGreaterThan(at);
    // Still before the generic toast, which stays for every other timeout.
    expect(main.indexOf("showToast(t('update.tookLongerToast'))", at)).toBeGreaterThan(at);
  });

  it('folds out under the rows of Update all, from the returned rows that carry the verdict', () => {
    const loop = main.indexOf('for (const row of (fleet && fleet.rows) || [])');
    const use = main.indexOf('darkChecklistRowHtml(', loop);
    expect(loop).toBeGreaterThan(0);
    expect(use).toBeGreaterThan(loop);
    expect(main.slice(loop, use)).toContain('darkAfterUpdate(row)');
  });

  it('goes on the install screens that end with a silent speaker, shared by both layouts', () => {
    const giveUp = setup.indexOf('const giveUp = () => {');
    expect(setup.indexOf('installDarkChecklistHtml(foundBox)', giveUp)).toBeGreaterThan(giveUp);
    const incomplete = setup.indexOf("t('setup.installIncomplete')");
    expect(setup.indexOf('installDarkChecklistHtml(foundBox)', incomplete)).toBeGreaterThan(incomplete);
  });

  it('goes on the UI 2.0 wizard\'s Confirm step when the speaker never answered', () => {
    setLocale('en');
    const failed = confirmHTML({ status: 'failed', name: 'Kitchen', model: 'SoundTouch 10' });
    expect(failed).toContain('dark-check');
    expect(failed).toContain(DARK_USB_DOCS_URL);
    expect(confirmHTML({ status: 'waiting', name: 'Kitchen', host: '192.0.2.1' })).not.toContain('dark-check');
    expect(src('./shell2/setup/wizard.js')).toContain('handleDarkChecklistClick(e,');
  });
});
