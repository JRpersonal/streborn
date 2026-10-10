// Three signposts about the firmware pointed nowhere useful.
//
// 1. The install-failure message told the owner of a too-old speaker that "the
//    Firmware section in the speaker settings has the steps and the link". The
//    settings pane short-circuits a box with kind 'stock' to an empty state with
//    a Setup button and returns before any section renders, and every owner who
//    gets that message has exactly such a box.
// 2. boseFwArticles fell back to the SoundTouch 10 article for any unknown
//    model, so a CineMate, a Lifestyle console, an SA-5 or a soundbar owner was
//    sent to a page showing a small round speaker.
// 3. setup.awaitFirmwareTooOld told the user to update in the official Bose
//    SoundTouch app, which this repo's own code comments call a dead end since
//    the cloud shutdown.
// 4. Its replacement then named "Bose's USB update tool" on btu.bose.com and
//    "write the firmware to another stick", neither of which is Bose's
//    procedure (field report, ST20 on 5.2.0, 2026-10-10). Every screen now
//    renders the one firmware update guide (fwguide.js).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { boseFwArticles, firmwareOlderThanLatest, LATEST_BOSE_FIRMWARE } from './firmware.js';

const here = dirname(fileURLToPath(import.meta.url));
const setup = readFileSync(join(here, 'views', 'setup.js'), 'utf8').replace(/\r\n/g, '\n');
const settings = readFileSync(join(here, 'views', 'settings.js'), 'utf8').replace(/\r\n/g, '\n');
const installGo = readFileSync(join(here, '..', '..', 'install_str.go'), 'utf8').replace(/\r\n/g, '\n');
const firmwareGo = readFileSync(join(here, '..', '..', 'app_firmware.go'), 'utf8').replace(/\r\n/g, '\n');
const firmware = readFileSync(join(here, 'firmware.js'), 'utf8').replace(/\r\n/g, '\n');
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];

describe('the firmware route is rendered where a stock speaker can reach it', () => {
  it('lives in the setup view, next to the failure', () => {
    expect(setup).toContain('function outdatedFirmwareHtml(box, short, result)');
    expect(setup).toContain('firmwareGuideMount(');
    expect(setup).not.toContain('btu.bose.com');
  });

  it('is on all three screens an install can end on', () => {
    // the plain failure, the wait, and the give-up after the watcher's ceiling
    expect(setup).toContain('outdatedFirmwareHtml(foundBox, result && result.firmware, result)');
    expect(setup.match(/\+ \(fwBlock \|\| ''\)/g) || []).toHaveLength(2);
    // 1 definition + 3 install screens + the stick wait
    expect(setup.match(/wireOutdatedFirmwareLinks\(\)/g) || []).toHaveLength(5);
  });

  it('renders nothing for a speaker whose firmware is current', () => {
    const fn = setup.slice(setup.indexOf('function outdatedFirmwareHtml'),
      setup.indexOf('// wireOutdatedFirmwareLinks'));
    expect(fn).toContain("if (!firmwareGuideNeeded(short)) return '';");
  });

  it('no longer sends the owner to a pane that speaker cannot open', () => {
    expect(installGo).not.toContain('the Firmware section in the speaker settings has the steps');
    expect(installGo).toContain('fwNote = firmwareTooOldNote(');
    expect(firmwareGo).not.toContain('btu.bose.com');
    expect(firmwareGo).not.toContain('USB update tool');
  });
});

describe('the version compare matches the Go side', () => {
  it('uses the same last-Bose-firmware constant', () => {
    expect(LATEST_BOSE_FIRMWARE).toBe('27.0.6');
    expect(firmwareGo).toContain('const latestBoseFirmware = "27.0.6"');
  });

  it('judges the versions seen in the field', () => {
    expect(firmwareOlderThanLatest('10.0.11')).toBe(true);  // the 2015 ST30
    expect(firmwareOlderThanLatest('14.0.15')).toBe(true);  // the scm ST20 that boot-looped
    expect(firmwareOlderThanLatest('27.0.6')).toBe(false);
    expect(firmwareOlderThanLatest('27.0.7')).toBe(false);
    expect(firmwareOlderThanLatest('')).toBe(false);
    expect(firmwareOlderThanLatest('nonsense')).toBe(false);
  });
});

describe('an unknown model gets no article rather than the wrong one', () => {
  it('still answers for the four speakers with their own page', () => {
    expect(boseFwArticles('SoundTouch 10')).toHaveLength(1);
    expect(boseFwArticles('SoundTouch 20')).toHaveLength(2);
    expect(boseFwArticles('SoundTouch 30')).toHaveLength(2);
    expect(boseFwArticles('SoundTouch Portable')).toHaveLength(1);
  });

  it('sends a CineMate, a Lifestyle, an SA-5 and a soundbar to no speaker page', () => {
    for (const model of ['CineMate 520', 'Lifestyle 650', 'SA-5 amplifier',
      'SoundTouch 300', 'Wave SoundTouch music system IV', '']) {
      expect(boseFwArticles(model), `${model} must not borrow another product's article`).toEqual([]);
    }
  });

  it('shows the same firmware update guide in the speaker settings', () => {
    expect(settings).toContain('firmwareGuideMount(');
    expect(settings).toContain("context: 'settings'");
    expect(settings).not.toContain('BOSE_FW_USB_URL');
  });

  // The helpers live outside the views because a view reads navigator at module
  // level, which Node 20 does not define: importing settings.js from a test
  // passed on a machine with Node 21+ and failed on the CI runner with
  // "ReferenceError: navigator is not defined".
  it('is importable without a browser', () => {
    // The comments name navigator on purpose; the code must not touch it.
    const code = firmware.split('\n').filter(l => !l.trim().startsWith('//')).join('\n');
    expect(code).not.toContain('navigator');
    expect(code).not.toContain('document.');
    expect(code).not.toContain('window.');
    expect(code).not.toMatch(/^import /m);
  });
});

describe('no screen names a route Bose does not offer', () => {
  for (const lang of langs) {
    it(lang, () => {
      const b = JSON.parse(readFileSync(join(bundleDir, `${lang}.json`), 'utf8'));
      expect(b['setup.awaitFirmwareTooOld']).toBeTruthy();
      expect(b['setup.awaitFirmwareTooOld']).toContain('{{model}}');
      expect(b['setup.awaitFirmwareTooOld']).toContain('{{fw}}');
      // The removed strings: the app route, the btu.bose.com "USB tool", and
      // "write the firmware to another stick".
      for (const gone of ['setup.fwTooOldLine', 'fw.step1', 'fw.step2', 'fw.step3', 'fw.step4', 'fw.hint']) {
        expect(b[gone], `${lang} still carries ${gone}`).toBeUndefined();
      }
      for (const [k, v] of Object.entries(b)) {
        if (!k.startsWith('fw') && k !== 'setup.awaitFirmwareTooOld') continue;
        expect(String(v), `${lang} ${k}`).not.toContain('btu.bose.com');
        expect(String(v), `${lang} ${k} has an em dash`).not.toMatch(/\u2014/);
      }
    });
  }

  it('the German strings keep their umlauts', () => {
    const de = JSON.parse(readFileSync(join(bundleDir, 'de.json'), 'utf8'));
    expect(de['fwGuide.step1']).toMatch(/[äöüß]/);
    expect(de['setup.awaitFirmwareTooOld']).toMatch(/[äöüß]/);
  });
});
