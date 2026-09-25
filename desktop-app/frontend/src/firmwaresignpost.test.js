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
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { boseFwArticles, firmwareOlderThanLatest, LATEST_BOSE_FIRMWARE } from './views/settings.js';

const here = dirname(fileURLToPath(import.meta.url));
const setup = readFileSync(join(here, 'views', 'setup.js'), 'utf8').replace(/\r\n/g, '\n');
const settings = readFileSync(join(here, 'views', 'settings.js'), 'utf8').replace(/\r\n/g, '\n');
const installGo = readFileSync(join(here, '..', '..', 'install_str.go'), 'utf8').replace(/\r\n/g, '\n');
const firmwareGo = readFileSync(join(here, '..', '..', 'app_firmware.go'), 'utf8').replace(/\r\n/g, '\n');
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];

describe('the firmware route is rendered where a stock speaker can reach it', () => {
  it('lives in the setup view, next to the failure', () => {
    expect(setup).toContain('function outdatedFirmwareHtml(box, short)');
    expect(setup).toContain('btu.bose.com');
    expect(setup).toContain("t('fw.boseGuideLink')");
  });

  it('is on all three screens an install can end on', () => {
    // the plain failure, the wait, and the give-up after the watcher's ceiling
    expect(setup).toContain('outdatedFirmwareHtml(foundBox, result && result.firmware)');
    expect(setup.match(/\+ \(fwBlock \|\| ''\)/g) || []).toHaveLength(2);
    expect(setup.match(/wireOutdatedFirmwareLinks\(\)/g) || []).toHaveLength(4); // 1 definition + 3 calls
  });

  it('renders nothing for a speaker whose firmware is current', () => {
    const fn = setup.slice(setup.indexOf('function outdatedFirmwareHtml'),
      setup.indexOf('// wireOutdatedFirmwareLinks'));
    expect(fn).toContain("if (!short || !firmwareOlderThanLatest(short)) return '';");
  });

  it('no longer sends the owner to a pane that speaker cannot open', () => {
    expect(installGo).not.toContain('the Firmware section in the speaker settings has the steps');
    expect(installGo).toContain('btu.bose.com');
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

  it('keeps the model-independent Bose updater page on screen either way', () => {
    // The guide paragraph is conditional; the btu.bose.com step is not.
    expect(settings).toContain('BOSE_FW_USB_URL');
    expect(settings).toContain('boseFwArticles(info.type).length');
  });
});

describe('no screen tells the user to update in the Bose app any more', () => {
  for (const lang of langs) {
    it(lang, () => {
      const b = JSON.parse(readFileSync(join(bundleDir, `${lang}.json`), 'utf8'));
      expect(b['setup.awaitFirmwareTooOld']).toBeTruthy();
      expect(b['setup.awaitFirmwareTooOld']).toContain('btu.bose.com');
      expect(b['setup.fwTooOldLine']).toBeTruthy();
      expect(b['setup.fwTooOldLine']).toContain('{{fw}}');
      expect(b['setup.fwTooOldLine']).toContain('{{latest}}');
      expect(b['setup.awaitFirmwareTooOld']).toContain('{{model}}');
      expect(b['setup.awaitFirmwareTooOld']).toContain('{{fw}}');
    });
  }

  it('the German strings keep their umlauts', () => {
    const de = JSON.parse(readFileSync(join(bundleDir, 'de.json'), 'utf8'));
    expect(de['setup.fwTooOldLine']).toMatch(/[äöüß]/);
    expect(de['setup.awaitFirmwareTooOld']).toMatch(/[äöüß]/);
  });
});
