// The bundle registry and the locale picker, checked without a DOM.
//
// zh-Hant is the first bundle whose code is not a bare language tag, and the
// detection path cut navigator.language at the dash, so zh-TW resolved to "zh",
// matched no bundle, and a Taiwanese user silently got English.
import { describe, it, expect } from 'vitest';
import { AVAILABLE_LOCALES, t, setLocale, getLocale } from './index.js';
import en from './bundles/en.json';
import zhHant from './bundles/zh-Hant.json';

describe('bundle registry', () => {
  it('offers Traditional Chinese under its own endonym', () => {
    const codes = AVAILABLE_LOCALES.map((l) => l.code);
    expect(codes).toContain('zh-Hant');
    const zh = AVAILABLE_LOCALES.find((l) => l.code === 'zh-Hant');
    expect(zh.label).toBe('繁體中文');
  });

  it('carries every English key, so nothing falls back silently', () => {
    const missing = Object.keys(en).filter((k) => !(k in zhHant));
    expect(missing).toEqual([]);
  });

  it('keeps every placeholder the English string declares', () => {
    const ph = (s) => (String(s).match(/\{\{\s*\w+\s*\}\}/g) || []).sort();
    const broken = Object.keys(en).filter(
      (k) => ph(en[k]).join('|') !== ph(zhHant[k]).join('|'),
    );
    expect(broken).toEqual([]);
  });

  it('leaves the product names alone', () => {
    expect(zhHant['locale.label']).toBe('繁體中文');
    for (const k of Object.keys(en)) {
      // Anywhere English says Bose or Spotify, so must the translation.
      for (const name of ['Bose', 'Spotify', 'SoundTouch', 'Wi-Fi']) {
        if (String(en[k]).includes(name)) {
          expect(String(zhHant[k])).toContain(name);
        }
      }
    }
  });
});

describe('setLocale', () => {
  it('switches to Traditional Chinese and translates', () => {
    const before = getLocale();
    expect(setLocale('zh-Hant')).toBe(true);
    expect(getLocale()).toBe('zh-Hant');
    expect(t('common.save')).toBe(zhHant['common.save']);
    expect(t('common.save')).not.toBe(en['common.save']);
    setLocale(before);
  });

  it('interpolates placeholders in the translated string', () => {
    setLocale('zh-Hant');
    const out = t('update.speakerUpdateAvailFor', { name: 'Küche' });
    expect(out).toContain('Küche');
    expect(out).not.toContain('{{name}}');
    setLocale('en');
  });

  it('refuses a code with no bundle', () => {
    expect(setLocale('zh-Hans')).toBe(false);
    expect(setLocale('kl')).toBe(false);
  });
});
