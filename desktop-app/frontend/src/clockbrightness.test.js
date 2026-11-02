// The standby clock goes through the agent, which keeps it on the speaker and
// puts it back after a restart or when the firmware drops it (#979), and its
// brightness can be set (#1226).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { srcOf, srcBetween } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const keys = ['settingsView.clockBrightnessLabel', 'settingsView.clockBrightnessHelp',
  'settingsView.clockBrightnessSavedToast', 'settingsView.clockBrightnessIgnored'];

describe('the clock write goes through the agent', () => {
  it('postClock uses SetStandbyClock and keeps the brightness', () => {
    const block = srcBetween("// Preselect the box's current 12/24h format", '// Resume last station on power-on');
    const post = block.slice(block.indexOf('const postClock = async'), block.indexOf('const pressClock'));
    expect(post).toContain('await SetStandbyClock(box.host, box.port, enable, tz, offsetMin, fmt24, -1);');
    expect(post).not.toContain('SetClockDisplay(');
  });
});

describe('the brightness slider', () => {
  const fn = srcOf('wireClockBrightness');

  it('shows only where the speaker reports a level', () => {
    expect(fn).toContain('GetClockBrightness(boseHost)');
    expect(fn).toContain("if (typeof b !== 'number' || b < 0) return;");
    expect(fn).toContain("row.classList.remove('hidden');");
  });

  it('writes once, when the slider is let go, and only the brightness', () => {
    expect(fn).toContain("slider.addEventListener('change'");
    expect(fn).toContain('SetClockBrightness(box.host, box.port, v)');
    const input = fn.slice(fn.indexOf("addEventListener('input'"), fn.indexOf("addEventListener('change'"));
    expect(input).not.toContain('SetClockBrightness');
  });

  it('says so when the speaker keeps its own level, and puts the slider back on a refusal', () => {
    expect(fn).toContain('if (honoured === false)');
    expect(fn).toContain('settingsView.clockBrightnessIgnored');
    const c = fn.slice(fn.indexOf('} catch (e) {'));
    expect(c).toContain('paint(last);');
  });
});

describe('the new strings exist in every bundle', () => {
  for (const lang of langs) {
    it(lang, () => {
      const b = JSON.parse(readFileSync(join(bundleDir, `${lang}.json`), 'utf8'));
      for (const k of keys) expect(b[k], k).toBeTruthy();
      expect(b['settingsView.clockBrightnessSavedToast']).toContain('{{v}}');
    });
  }

  it('the German text keeps its umlauts and has no em dash', () => {
    const de = JSON.parse(readFileSync(join(bundleDir, 'de.json'), 'utf8'));
    expect(de['settingsView.clockBrightnessHelp']).toMatch(/[äöüßÄÖÜ]/);
    for (const k of keys) expect(de[k]).not.toContain('—');
  });
});
