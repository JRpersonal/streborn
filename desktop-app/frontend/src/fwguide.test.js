// The firmware update guide's rendering decisions, checked without a DOM.
//
// Background: an ST20 on firmware 5.2.0 was told to use "Bose's USB update tool"
// and a settings section its stock speaker cannot open (2026-10-10). The guide
// replaces that with Bose's own procedure and Bose's own download link.
import { describe, it, expect, beforeAll } from 'vitest';
import { readFileSync } from 'node:fs';
import { setLocale } from './i18n/index.js';
import {
  fwGuidePlan,
  firmwareGuideHtml,
  firmwareGuideMount,
  firmwareGuideNeeded,
  setupModeKey,
  UPDATE_PAGE,
  UPDATE_PAGE_ALT,
  UPDATE_PAGE_USB,
  BOSE_SUPPORT_URL,
} from './fwguide.js';
import en from './i18n/bundles/en.json';

const BASE = 'https://downloads.bose.com/ced/soundtouch/downloads_stockholm/';
const scm = { name: 'Update_ti_27.0.6.46330.5043500.scm.stu', url: BASE + 'Update_ti_27.0.6.46330.5043500.scm.stu', generation: 'scm' };
const sm2 = { name: 'Update_ti_27.0.6.46330.5043500.sm2.stu', url: BASE + 'Update_ti_27.0.6.46330.5043500.sm2.stu', generation: 'sm2' };
const st20 = { model: 'SoundTouch 20', moduleType: '', variant: '', current: '5.2.0.11111.222' };

// Before any describe body runs: the last block renders at collection time.
setLocale('en');
beforeAll(() => { setLocale('en'); });

// The guide escapes its text, so an apostrophe or a quote is an entity.
const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');

describe('when the guide shows at all', () => {
  it('shows for a firmware older than 27.0.6 and never for a current or unreadable one', () => {
    expect(firmwareGuideNeeded('5.2.0')).toBe(true);
    expect(firmwareGuideNeeded('27.0.3')).toBe(true);
    expect(firmwareGuideNeeded('27.0.6')).toBe(false);
    expect(firmwareGuideNeeded('')).toBe(false);
    expect(firmwareGuideNeeded(undefined)).toBe(false);
  });
});

describe('the download step', () => {
  it('says it is looking while the backend has not answered', () => {
    expect(fwGuidePlan(st20, null).download).toBe('loading');
    expect(firmwareGuideHtml(st20, null)).toContain(esc(en['fwGuide.loading']));
  });

  it('offers exactly one Bose button for a speaker whose file is known', () => {
    const html = firmwareGuideHtml({ ...st20, moduleType: 'sm2' }, { files: [sm2], required: '27.0.6' });
    expect(html).toContain(`data-fwopen="${sm2.url}"`);
    expect(html).toContain(esc(en['fwGuide.downloadBtn']));
    expect(html).not.toContain(esc(en['fwGuide.twoFiles']));
    expect(html.match(/fw-guide-download/g)).toHaveLength(1);
  });

  it('offers both files, older hardware first, with the sentence on which is which', () => {
    const plan = fwGuidePlan(st20, { files: [scm, sm2] });
    expect(plan.download).toBe('two');
    const html = firmwareGuideHtml(st20, { files: [scm, sm2] });
    expect(html.indexOf(scm.url)).toBeLessThan(html.indexOf(sm2.url));
    expect(html).toContain(esc(en['fwGuide.downloadOlder']));
    expect(html).toContain(esc(en['fwGuide.downloadNewer']));
    expect(html).toContain(esc(en['fwGuide.twoFiles']));
  });

  it('points a model without a file to Bose\'s article when there is one', () => {
    const html = firmwareGuideHtml({ model: 'SoundTouch 10', current: '18.0.0' }, { files: [] });
    expect(html).toContain(esc(en['fwGuide.noFile']));
    expect(html).toContain('soundtouch-10-updating');
    expect(html).not.toContain(BOSE_SUPPORT_URL + '"');
  });

  it('points a model with neither file nor article to Bose support, not to a guessed file', () => {
    const html = firmwareGuideHtml({ model: 'CineMate 520', current: '18.0.0' }, { files: [] });
    expect(html).toContain(esc(en['fwGuide.noFileNoArticle']));
    expect(html).toContain(`data-fwopen="${BOSE_SUPPORT_URL}"`);
    expect(html).not.toContain('downloads.bose.com');
  });

  it('only ever links Bose\'s own download host for a firmware file', () => {
    const html = firmwareGuideHtml(st20, { files: [scm, sm2] });
    const opens = [...html.matchAll(/data-fwopen="([^"]+)"/g)].map(m => m[1]);
    for (const u of opens.filter(u => u.endsWith('.stu'))) {
      expect(u.startsWith(BASE)).toBe(true);
    }
  });
});

describe('the setup-mode step', () => {
  it('uses the buttons Bose names for each model, and the generic text otherwise', () => {
    for (const m of ['SoundTouch 10', 'SoundTouch 20', 'SoundTouch 30', 'SoundTouch Portable']) {
      expect(setupModeKey(m)).toBe('fwGuide.setupTwoVol');
    }
    expect(setupModeKey('SoundTouch SA-5')).toBe('fwGuide.setupSa5');
    expect(setupModeKey('SoundTouch 300')).toBe('fwGuide.setupSt300');
    expect(setupModeKey('Wave SoundTouch')).toBe('fwGuide.setupWave');
    expect(setupModeKey('Wave SoundTouch music system IV')).toBe('fwGuide.setupWave');
    expect(setupModeKey('SoundTouch Wireless Link Adapter')).toBe('fwGuide.setupLink');
    for (const m of ['SoundTouch SA-4', 'Lifestyle', 'Cinemate', 'CineMate 520', '']) {
      expect(setupModeKey(m), m).toBe('fwGuide.setupGeneric');
    }
  });

  it('renders the model\'s instruction into the steps', () => {
    expect(firmwareGuideHtml(st20, null)).toContain(esc(en['fwGuide.setupTwoVol']));
    expect(firmwareGuideHtml({ model: 'Lifestyle', current: '1.0.0' }, null)).toContain(esc(en['fwGuide.setupGeneric']));
  });
});

describe('the rest of the guide', () => {
  const html = firmwareGuideHtml(st20, { files: [scm, sm2], required: '27.0.6' });

  it('names both versions', () => {
    expect(html).toContain('5.2.0');
    expect(html).toContain('27.0.6');
    expect(html).not.toContain('5.2.0.11111');
  });

  it('gives the update page with copy and open buttons, plus the other address and the USB route', () => {
    expect(html).toContain(`<code>${UPDATE_PAGE}</code>`);
    expect(html).toContain(`data-fwcopy="${UPDATE_PAGE}"`);
    expect(html).toContain(UPDATE_PAGE_ALT);
    expect(html).toContain(UPDATE_PAGE_USB);
  });

  it('says not to rename the file and not to unplug, and what to do afterwards', () => {
    expect(html).toContain('Do not rename the file');
    expect(html).toContain('Do not unplug');
    expect(html).toContain(esc(en['fwGuide.after']));
    const inSettings = firmwareGuideHtml({ ...st20, context: 'settings' }, null);
    expect(inSettings).toContain(esc(en['fwGuide.afterInstalled']));
    expect(inSettings).not.toContain(esc(en['fwGuide.after']));
  });

  it('never mentions the routes that do not exist', () => {
    for (const bad of ['btu.bose.com', 'USB update tool', 'another stick']) {
      expect(html).not.toContain(bad);
    }
  });

  it('escapes what the speaker reports', () => {
    const mount = firmwareGuideMount({ model: '<img src=x>', current: '5.2.0' });
    expect(mount).not.toContain('<img src=x>');
  });
});

describe('the strings', () => {
  it('have no em dash in any language', () => {
    for (const lang of ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant']) {
      const b = JSON.parse(readFileSync(new URL(`./i18n/bundles/${lang}.json`, import.meta.url), 'utf8'));
      for (const [k, v] of Object.entries(b)) {
        if (k.startsWith('fwGuide.')) expect(v, `${lang} ${k}`).not.toMatch(/\u2014/);
      }
      expect(Object.keys(b).filter(k => k.startsWith('fwGuide.')).length, lang)
        .toBe(Object.keys(en).filter(k => k.startsWith('fwGuide.')).length);
    }
  });
});
