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
  DARK_FILES_DOCS_URL,
  DARK_USB_DOCS_URL,
  darkAfterUpdate,
  darkBatchPlan,
  darkChecklistApplies,
  darkChecklistSteps,
  darkChecklistHtml,
  darkChecklistRowHtml,
  darkStickRoute,
  handleDarkChecklistClick,
  updateNoNetworkHere,
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
    'darkCheck.rowSummary', 'darkCheck.usbLink', 'darkCheck.filesLink', 'darkCheck.hardware', 'darkCheck.allDark',
    'update.st300PowerCycle']);
  for (const m of ['SoundTouch 10', 'SoundTouch 20', 'SoundTouch 30', 'SoundTouch Portable', 'SoundTouch 300', 'Wave SoundTouch', '']) {
    for (const s of darkChecklistSteps(m)) {
      keys.add(s.head);
      s.lines.forEach((k) => keys.add(k));
      s.links.forEach((l) => keys.add(l.key));
    }
  }
  return [...keys];
}

// The sentences of the USB stick route, any of which would be wrong for a
// speaker that never reads a stick at boot.
const STICK_KEYS = ['darkCheck.usb', 'darkCheck.usbBattery', 'fwLadder.nsPort', 'fwLadder.nsStick',
  'fwLadder.c3Generic', 'fwLadder.c3FourVol', 'fwLadder.c3Battery', 'fwLadder.c3Unconfirmed', 'fwLadder.c4', 'fwLadder.w2'];

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

  it('never when this computer had no network: the speaker was not judged', () => {
    const noNet = { ...dark, verdict: 'no-network-here' };
    expect(darkAfterUpdate(noNet)).toBe(false);
    expect(updateNoNetworkHere(noNet)).toBe(true);
    expect(updateNoNetworkHere(dark)).toBe(false);
    expect(updateNoNetworkHere({ ...noNet, confirmed: true })).toBe(false);
    expect(updateNoNetworkHere(null)).toBe(false);
  });
});

describe('which speakers get it', () => {
  it('every speaker but the SoundTouch 300, whether it names itself by model or by type', () => {
    expect(darkChecklistApplies({ model: 'SoundTouch 300' })).toBe(false);
    expect(darkChecklistApplies({ type: 'SoundTouch 300' })).toBe(false);
    expect(darkChecklistApplies('SoundTouch 300')).toBe(false);
    expect(darkChecklistApplies({ model: 'SoundTouch 10' })).toBe(true);
    expect(darkChecklistApplies({ model: 'SoundTouch 30' })).toBe(true);
    expect(darkChecklistApplies({ model: 'Wave SoundTouch' })).toBe(true);
    // An install whose speaker is not known yet still gets it.
    expect(darkChecklistApplies(null)).toBe(true);
    expect(darkChecklistApplies({})).toBe(true);
  });

  it('offers the USB stick only to a speaker that reads one at boot', () => {
    expect(darkStickRoute({ model: 'SoundTouch 10' })).toBe(true);
    expect(darkStickRoute({ model: 'SoundTouch Portable' })).toBe(true);
    expect(darkStickRoute({ model: 'Wave SoundTouch' })).toBe(false);
    expect(darkStickRoute({ model: 'SoundTouch SA-4' })).toBe(false);
    expect(darkStickRoute({ model: 'SoundTouch Wireless Link Adapter' })).toBe(false);
    // An old firmware may report the chassis but no usable model.
    expect(darkStickRoute({ model: '', variant: 'binky' })).toBe(false);
  });
});

describe('an Update all run', () => {
  const dark = { outcome: 'timeout', verdict: 'unreachable', confirmed: false };
  const done = { outcome: 'done', confirmed: true };
  const e = (host, result, model, engineOnly) => ({ host, result, box: { host, model }, engineOnly: !!engineOnly });

  it('folds the checklist out under the one dark speaker of a batch', () => {
    const plan = darkBatchPlan([e('a', dark, 'SoundTouch 10'), e('b', done, 'SoundTouch 20')]);
    expect(plan).toEqual({ notice: '', checklist: ['a'] });
  });

  it('says it once when every speaker went silent at once, instead of a checklist on every row', () => {
    const plan = darkBatchPlan([e('a', dark, 'SoundTouch 10'), e('b', dark, 'SoundTouch 20'), e('c', dark, 'SoundTouch 30')]);
    expect(plan).toEqual({ notice: 'allDark', checklist: [] });
  });

  it('says this computer has no network when the classifier saw that, on any row', () => {
    const noNet = { ...dark, verdict: 'no-network-here' };
    expect(darkBatchPlan([e('a', noNet, 'SoundTouch 10'), e('b', dark, 'SoundTouch 20'), e('c', done, 'SoundTouch 30')]))
      .toEqual({ notice: 'noNetwork', checklist: [] });
    // A single speaker too.
    expect(darkBatchPlan([e('a', noNet, 'SoundTouch 10')])).toEqual({ notice: 'noNetwork', checklist: [] });
  });

  it('keeps a single dark speaker on its checklist: one silent box is not this computer', () => {
    expect(darkBatchPlan([e('a', dark, 'SoundTouch 10')])).toEqual({ notice: '', checklist: ['a'] });
  });

  it('counts neither engine-only rows nor the 300 toward "every speaker"', () => {
    // The 300 is always silent after an update until it is unplugged; with
    // one other dark speaker that is still one dark speaker.
    expect(darkBatchPlan([e('a', dark, 'SoundTouch 10'), e('b', dark, 'SoundTouch 300')]))
      .toEqual({ notice: '', checklist: ['a'] });
    expect(darkBatchPlan([e('a', dark, 'SoundTouch 10'), e('b', dark, 'SoundTouch 20', true)]))
      .toEqual({ notice: '', checklist: ['a'] });
    expect(darkBatchPlan([])).toEqual({ notice: '', checklist: [] });
    expect(darkBatchPlan(null)).toEqual({ notice: '', checklist: [] });
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

  it('put the factory reset before the USB stick, and link the guide\'s stick route and Bose\'s files', () => {
    const dark = darkChecklistSteps('SoundTouch 20').find((s) => s.id === 'dark');
    expect(dark.lines[0]).toBe('darkCheck.resetKeys');
    expect(dark.lines.indexOf('darkCheck.usb')).toBeGreaterThan(0);
    expect(dark.lines).toContain('fwLadder.nsStick');
    expect(dark.lines).toContain('fwLadder.c3FourVol');
    expect(dark.links.map((l) => l.url)).toEqual([DARK_USB_DOCS_URL, DARK_FILES_DOCS_URL]);
    expect(DARK_USB_DOCS_URL).toMatch(/docs\/FIRMWARE-UPDATE\.md#5-route-c-usb-stick$/);
    expect(DARK_FILES_DOCS_URL).toMatch(/docs\/FIRMWARE-UPDATE\.md#9-boses-files-for-2706-per-model$/);
    // The stick sentence sends the owner to the file for this model: the two
    // chassis generations of one model take different files.
    expect(bundle('en')['darkCheck.usb']).toMatch(/for your model/);
  });

  it('carry the guide\'s port, "let go" and "never unplug it while it updates" lines, in the guide\'s order', () => {
    const dark = darkChecklistSteps('SoundTouch 10').find((s) => s.id === 'dark').lines;
    const at = (k) => dark.indexOf(k);
    // Port (micro-USB: the OTG adapter) right after "insert the stick".
    expect(at('fwLadder.nsPort')).toBe(at('darkCheck.usb') + 1);
    expect(at('fwLadder.c3FourVol')).toBeGreaterThan(at('fwLadder.nsStick'));
    expect(at('fwLadder.c4')).toBeGreaterThan(at('fwLadder.c3FourVol'));
    expect(at('fwLadder.w2')).toBeGreaterThan(at('fwLadder.c4'));
    expect(bundle('en')['fwLadder.nsPort']).toMatch(/OTG adapter/);
    expect(dark).not.toContain('fwLadder.w2Battery');
  });

  it('links sections the firmware guide really has', () => {
    const doc = readFileSync(new URL('../../../docs/FIRMWARE-UPDATE.md', import.meta.url), 'utf8');
    expect(doc).toMatch(/^## 5\. Route C: USB stick$/m);
    expect(doc).toMatch(/^## 9\. Bose's files for 27\.0\.6, per model$/m);
  });

  it('give the Portable its battery wording: no unplugging, the power button and its buttons', () => {
    const steps = darkChecklistSteps('SoundTouch Portable');
    expect(steps.find((s) => s.id === 'power').lines).toEqual(['darkCheck.powerBattery']);
    const dark = steps.find((s) => s.id === 'dark').lines;
    expect(dark).toContain('darkCheck.usbBattery');
    expect(dark).toContain('fwLadder.c3Battery');
    expect(dark).toContain('fwLadder.c3Unconfirmed');
    expect(dark.indexOf('fwLadder.w2Battery')).toBe(dark.indexOf('fwLadder.w2') + 1);
  });

  it('invent no buttons for a model whose reset and stick start are not known', () => {
    const dark = darkChecklistSteps('').find((s) => s.id === 'dark').lines;
    expect(dark).toContain('darkCheck.resetGeneric');
    expect(dark).toContain('fwLadder.c3Generic');
    expect(dark).not.toContain('darkCheck.resetKeys');
  });

  it('leave the USB stick out for a speaker that never reads one at boot', () => {
    for (const box of [{ model: 'Wave SoundTouch' }, { model: 'SoundTouch SA-4' }, { model: 'SoundTouch SA-5' },
      { model: 'SoundTouch Wireless Link Adapter' }, { model: 'Lifestyle 650' }, { model: 'SoundTouch 300' },
      { type: 'SoundTouch 300' }]) {
      const label = box.model || box.type;
      const dark = darkChecklistSteps(box).find((s) => s.id === 'dark');
      expect(dark.lines, label).toEqual(['darkCheck.resetGeneric']);
      for (const k of STICK_KEYS) expect(dark.lines, `${label}: ${k}`).not.toContain(k);
      expect(dark.links, label).toEqual([]);
      const html = darkChecklistHtml({ name: 'Kitchen', box, context: 'update', framed: true });
      expect(html, label).not.toContain('data-darkopen');
      expect(html, label).not.toContain('Update.stu');
    }
  });

  it('claim no more than it knows in the closing line: no "a software update cannot fix that"', () => {
    const en = bundle('en')['darkCheck.hardware'];
    expect(en).toMatch(/most likely cause/);
    expect(en).not.toMatch(/cannot fix/i);
    expect(bundle('de')['darkCheck.hardware']).not.toMatch(/kein Software-Update/);
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
    expect(html).toContain(`data-darkopen="${DARK_FILES_DOCS_URL}"`);
    expect(html).toContain('data-darkclose="1"');
    expect(html).toContain('often its power supply');
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
  // The update flow left main.js for src/update/: one file per runner, and the
  // loop breaker in a file of its own.
  const single = src('./update/single.js').replace(/\r\n/g, '\n');
  const all = src('./update/all.js').replace(/\r\n/g, '\n');
  const otastuck = src('./update/otastuck.js').replace(/\r\n/g, '\n');
  const setup = src('./views/setup/install.js').replace(/\r\n/g, '\n');
  // The checklist helper left setup.js for the install screens' help module.
  const setupHelp = src('./views/setup/installhelp.js').replace(/\r\n/g, '\n');

  it('replaces the bare "took longer" toast after a single update, except on the 300', () => {
    const at = single.indexOf('darkAfterUpdate(result) && darkChecklistApplies(targetBox)');
    expect(at).toBeGreaterThan(0);
    expect(single.indexOf('showDarkAfterUpdate(targetBox)', at)).toBeGreaterThan(at);
    // Still before the generic toast, which stays for every other timeout.
    expect(single.indexOf("showToast(t('update.tookLongerToast'))", at)).toBeGreaterThan(at);
    // A computer without network gets its own notice first, not the checklist.
    const noNet = single.indexOf('} else if (updateNoNetworkHere(result)) {');
    expect(noNet).toBeGreaterThan(0);
    expect(noNet).toBeLessThan(at);
    expect(single.indexOf('showUpdateNoNetworkNotice()', noNet)).toBeLessThan(at);
  });

  it('keeps a computer without network out of the loop breaker', () => {
    const fn = otastuck.indexOf('function noteOTAResult(box, result) {');
    const count = otastuck.indexOf('noteOTAFailure(box, result.verdict', fn);
    expect(fn).toBeGreaterThan(0);
    expect(otastuck.slice(fn, count)).toContain('if (updateNoNetworkHere(result)) return;');
  });

  it('folds out under the rows of Update all, from the returned rows, through one batch plan', () => {
    const loop = all.indexOf('for (const row of (fleet && fleet.rows) || [])');
    const plan = all.indexOf('const darkPlan = darkBatchPlan(darkEntries);', loop);
    const use = all.indexOf('darkChecklistRowHtml(', plan);
    expect(loop).toBeGreaterThan(0);
    expect(plan).toBeGreaterThan(loop);
    expect(use).toBeGreaterThan(plan);
    expect(all.slice(loop, plan)).toContain('darkEntries.push({ host: row.host, result: row');
    expect(all.slice(plan, use)).toContain('for (const host of darkPlan.checklist)');
    // The batch's one notice instead of a checklist on every row.
    expect(all).toContain("if (darkPlan.notice === 'noNetwork') showUpdateNoNetworkNotice();");
    expect(all).toContain("else if (darkPlan.notice === 'allDark') showError(t('darkCheck.allDark'));");
  });

  it('goes on the install screens that end with a silent speaker, shared by both layouts', () => {
    const giveUp = setup.indexOf('const giveUp = () => {');
    expect(setup.indexOf('installDarkChecklistHtml(foundBox)', giveUp)).toBeGreaterThan(giveUp);
    const incomplete = setup.indexOf("t('setup.installIncomplete')");
    expect(setup.indexOf('installDarkChecklistHtml(foundBox)', incomplete)).toBeGreaterThan(incomplete);
  });

  it('keeps the SoundTouch 300 on its power-cycle step on every install screen', () => {
    // The helper itself renders nothing for a 300.
    const helper = setupHelp.indexOf('function installDarkChecklistHtml(box) {');
    expect(helper).toBeGreaterThan(0);
    expect(setupHelp.slice(helper, helper + 120)).toContain("if (!darkChecklistApplies(box)) return '';");
    // Give-up: the 300 keeps its logs-only list and the red step, as before.
    const giveUp = setup.indexOf('const giveUp = () => {');
    const steps = setup.indexOf('const steps = (inSetup || !darkChecklistApplies(foundBox))', giveUp);
    expect(steps).toBeGreaterThan(giveUp);
    expect(setup.slice(steps, steps + 160)).toContain('? help + powerCycleAdviceHtml(foundBox)');
    // Did not complete: the red step instead of the checklist.
    const incomplete = setup.indexOf("t('setup.installIncomplete')");
    expect(setup.slice(incomplete, incomplete + 400))
      .toContain('darkChecklistApplies(foundBox) ? installDarkChecklistHtml(foundBox) : powerCycleAdviceHtml(foundBox)');
  });

  it('goes on the UI 2.0 wizard\'s Confirm step when the speaker never answered', () => {
    setLocale('en');
    const failed = confirmHTML({ status: 'failed', name: 'Kitchen', model: 'SoundTouch 10' });
    expect(failed).toContain('dark-check');
    expect(failed).toContain(DARK_USB_DOCS_URL);
    expect(confirmHTML({ status: 'waiting', name: 'Kitchen', host: '192.0.2.1' })).not.toContain('dark-check');
    expect(src('./shell2/setup/wizard.js')).toContain('handleDarkChecklistClick(e,');
  });

  it('gives a SoundTouch 300 on the Confirm step its power-cycle step, never the Wi-Fi or reset lines', () => {
    setLocale('en');
    const en = bundle('en');
    const failed = confirmHTML({ status: 'failed', name: 'Living room', model: 'SoundTouch 300' });
    expect(failed).not.toContain('dark-check');
    expect(failed).not.toContain('Add speaker');
    expect(failed).not.toContain(en['darkCheck.resetGeneric']);
    expect(failed).toContain('unplug the soundbar from power once');
    // The wizard asks the speaker's record for a model the target lacks.
    expect(src('./shell2/setup/wizard.js')).toContain('(tgBox && (tgBox.model || tgBox.type))');
  });
});
