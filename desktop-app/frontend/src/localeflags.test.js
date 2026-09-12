// Every language the app offers has to show something in the picker. Arabic
// drew Argentina's flag (an emoji Windows cannot render anyway) and Traditional
// Chinese drew nothing at all, because both fell through a lookup that
// upper-cases the locale code and hopes it is a country.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { localeFlagSvg, flagSvg } from './localization.js';

const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');
const dir = fileURLToPath(new URL('./i18n/bundles/', import.meta.url));
const locales = readdirSync(dir).filter((f) => f.endsWith('.json')).map((f) => f.replace('.json', ''));

// The picker's own mapping, mirrored here so the test exercises what ships.
const ccFor = (code) => {
  const m = main.match(/const LOCALE_FLAG_CC = \{([\s\S]*?)\n\};/);
  const map = {};
  for (const line of m[1].split('\n')) {
    const kv = line.match(/^\s*'?([\w-]+)'?:\s*'([^']*)'/);
    if (kv) map[kv[1]] = kv[2];
  }
  return map[code] ?? code.toUpperCase();
};

describe('the language picker', () => {
  it('draws a mark for every language the app ships', () => {
    expect(locales.length).toBeGreaterThan(10);
    for (const code of locales) {
      const svg = localeFlagSvg(code, ccFor(code));
      expect(svg, `${code} has no flag and no mark, so the picker shows a blank`).toBeTruthy();
      expect(svg, `${code} is not an inline SVG, so Windows will not render it`).toContain('<svg');
    }
  });

  it('never labels Arabic with Argentina', () => {
    expect(ccFor('ar')).toBe('');
    expect(flagSvg('AR')).toBe('');
    const svg = localeFlagSvg('ar', ccFor('ar'));
    expect(svg).toContain('\u0639');
  });

  it('labels Traditional Chinese without picking a territory', () => {
    const svg = localeFlagSvg('zh-Hant', ccFor('zh-Hant'));
    expect(svg).toContain('\u7e41');
    for (const cc of ['TW', 'CN', 'HK', 'MO']) {
      expect(svg).not.toContain(`>${cc}<`);
    }
  });

  it('still uses the real flag where a language has one country', () => {
    expect(localeFlagSvg('de', ccFor('de'))).toBe(flagSvg('DE'));
    expect(localeFlagSvg('en', ccFor('en'))).toBe(flagSvg('GB'));
  });
});
