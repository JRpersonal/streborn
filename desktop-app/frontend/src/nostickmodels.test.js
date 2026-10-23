// A Wave SoundTouch on Bose firmware 2.1.51 (2026-10-22) was restarted about
// twelve times by seven network installs that could never work, and the
// failure screen then opened the USB-stick section for a speaker that never
// reads a stick. The owner wrote one anyway. These guard the two halves of
// the fix on the setup screen: no stick section for a no-stick model, and a
// firmware-first checklist for the new firmware-too-old code.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { neverReadsStick } from './utils.js';
import { allSource, srcOf } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const bundleDir = join(here, 'i18n', 'bundles');

describe('neverReadsStick', () => {
  it('knows the models that never read a stick at boot', () => {
    expect(neverReadsStick({ model: 'Wave SoundTouch' })).toBe(true);
    expect(neverReadsStick({ type: 'SoundTouch 300' })).toBe(true);
    expect(neverReadsStick({ model: '' }, { variant: 'lisa' })).toBe(true);
    expect(neverReadsStick(null, { model: 'Wave SoundTouch', variant: '' })).toBe(true);
    expect(neverReadsStick({ model: 'SoundTouch SA-5', variant: 'burns' })).toBe(true);
    expect(neverReadsStick({ model: 'Lifestyle', variant: 'bardeen' })).toBe(true);
  });
  it('leaves the stick-reading models and unknown boxes alone', () => {
    for (const m of ['SoundTouch 10', 'SoundTouch 20', 'SoundTouch 30', 'SoundTouch Portable', '']) {
      expect(neverReadsStick({ model: m })).toBe(false);
    }
    expect(neverReadsStick(null, undefined)).toBe(false);
  });
});

describe('install failure screen', () => {
  it('does not open the stick section for a no-stick model', () => {
    const wait = srcOf('waitForBoxAfterSetup');
    const i = wait.indexOf("stickDetails.open = true");
    expect(i).toBeGreaterThan(0);
    const guard = wait.slice(Math.max(0, i - 600), i);
    expect(guard).toContain('neverReadsStick(foundBox, result)');
  });
  it('has a firmware-first checklist for firmware-too-old on both paths', () => {
    const n = allSource().split("'firmware-too-old': ['firmwareFirst'").length - 1;
    expect(n).toBe(2);
  });
  it('every bundle carries setup.help.firmwareFirst', () => {
    for (const lang of ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant']) {
      const b = JSON.parse(readFileSync(join(bundleDir, lang + '.json'), 'utf8'));
      expect(b['setup.help.firmwareFirst'], lang).toBeTruthy();
    }
  });
});
