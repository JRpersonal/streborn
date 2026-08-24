// The search country dropdown is STR's own hardcoded list, not something
// radio-browser hands us. A Kosovar user reported (2026-08-23) that Kosovo
// could not be selected at all even though radio-browser carries 18 stations
// under XK; the whole Western Balkans neighbourhood was missing with it.
// These tests pin the six added entries and the translated bundle coverage
// behind them, so the gap cannot silently reopen.
import { describe, it, expect } from 'vitest';
import { getCountries } from './localization.js';
import { setLocale, getLocale } from './i18n/index.js';
import en from './i18n/bundles/en.json';
import de from './i18n/bundles/de.json';
import fr from './i18n/bundles/fr.json';
import es from './i18n/bundles/es.json';
import ja from './i18n/bundles/ja.json';
import uk from './i18n/bundles/uk.json';
import nl from './i18n/bundles/nl.json';
import pl from './i18n/bundles/pl.json';
import lt from './i18n/bundles/lt.json';
import lv from './i18n/bundles/lv.json';
import tr from './i18n/bundles/tr.json';
import ar from './i18n/bundles/ar.json';
import zhHant from './i18n/bundles/zh-Hant.json';

const BUNDLES = { en, de, fr, es, ja, uk, nl, pl, lt, lv, tr, ar, 'zh-Hant': zhHant };

const BALKAN_KEYS = [
  'country.serbia',
  'country.albania',
  'country.bosnia and herzegovina',
  'country.north macedonia',
  'country.montenegro',
  'country.kosovo',
];

describe('Western Balkans in the country dropdown', () => {
  it('offers Kosovo and its neighbours', () => {
    const ccs = getCountries().map((c) => c.cc);
    for (const cc of ['XK', 'RS', 'AL', 'BA', 'MK', 'ME']) {
      expect(ccs).toContain(cc);
    }
  });

  it('labels Kosovo by name in English', () => {
    const before = getLocale();
    setLocale('en');
    const kosovo = getCountries().find((c) => c.cc === 'XK');
    setLocale(before);
    expect(kosovo).toBeTruthy();
    expect(kosovo.name).toBe('Kosovo');
  });

  it('carries the six country keys translated in every bundle', () => {
    for (const [loc, bundle] of Object.entries(BUNDLES)) {
      for (const key of BALKAN_KEYS) {
        expect(bundle[key], `${loc} ${key}`).toBeTruthy();
      }
    }
    // Spot checks that the entries are translations, not English copies.
    expect(de['country.serbia']).toBe('Serbien');
    expect(zhHant['country.kosovo']).toBe('科索沃');
  });
});

describe('language and region placeholder wording', () => {
  it('says the value was not reported yet, not that nothing answered', () => {
    // The old label said "no response" even when the speaker answered fine
    // and simply had no region saved yet, which read as a connection
    // failure (sweep 2026-08-24, fix 7).
    expect(en['settingsView.langUnreachable']).toBe('not reported yet');
    for (const [loc, bundle] of Object.entries(BUNDLES)) {
      expect(bundle['settingsView.langUnreachable'], loc).toBeTruthy();
    }
    expect(de['settingsView.langUnreachable']).toBe('noch nicht gemeldet');
  });
});
