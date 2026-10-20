// The sleep-timer key helpers, checked without a DOM, plus the i18n parity of
// every string the key map and the group-keys section show for it.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { normalizeKeyActions, sleepOn, withSleep, sleepBlockedBy } from './keyactions.js';

describe('normalizeKeyActions', () => {
  it('accepts nothing, an older agent, and a full document', () => {
    expect(normalizeKeyActions(null)).toEqual({ keys: {} });
    expect(normalizeKeyActions({})).toEqual({ keys: {} });
    // only the thumbs keys, only known actions
    expect(normalizeKeyActions({ keys: { thumbsUp: ' Sleep ', prev: 'sleep', thumbsDown: 'reboot' } }))
      .toEqual({ keys: { thumbsUp: 'sleep' } });
  });
});

describe('withSleep', () => {
  it('switches the timer on and off one key, leaving the other alone', () => {
    let d = withSleep({ keys: {} }, 'thumbsUp', true);
    expect(sleepOn(d, 'thumbsUp')).toBe(true);
    d = withSleep(d, 'thumbsDown', true);
    d = withSleep(d, 'thumbsUp', false);
    expect(d).toEqual({ keys: { thumbsDown: 'sleep' } });
  });
  it('never puts the timer on a key that cannot carry it', () => {
    expect(withSleep({ keys: {} }, 'prev', true)).toEqual({ keys: {} });
    expect(withSleep(null, 'playPause', true)).toEqual({ keys: {} });
  });
  it('does not change the document it was given', () => {
    const d = { keys: { thumbsUp: 'sleep' } };
    withSleep(d, 'thumbsUp', false);
    expect(d).toEqual({ keys: { thumbsUp: 'sleep' } });
  });
});

describe('sleepBlockedBy', () => {
  it('names the group that sits on the key', () => {
    expect(sleepBlockedBy({ thumbsUp: 'Evening' }, 'thumbsUp')).toBe('Evening');
    expect(sleepBlockedBy({ thumbsUp: 'Evening' }, 'thumbsDown')).toBe('');
    expect(sleepBlockedBy(null, 'thumbsUp')).toBe('');
  });
});

describe('sleep key i18n', () => {
  const bundles = ['en', 'de', 'fr', 'es', 'nl', 'pl', 'tr', 'uk', 'lt', 'lv', 'ar', 'ja', 'zh-Hant'];
  const read = (loc) => JSON.parse(
    readFileSync(new URL(`./i18n/bundles/${loc}.json`, import.meta.url), 'utf8'));
  const en = read('en');
  const keys = Object.keys(en).filter((k) => k.startsWith('settingsView.sleepKey')
    || k === 'settingsView.webhookSleepKeyNote' || k === 'settingsView.webhookRemoteLegendSleep'
    || k === 'multiroom.groupKeysKeySleep' || k === 'multiroom.groupKeysSleepHint');
  it('has the strings in English at all', () => {
    expect(keys.length).toBe(10);
  });
  it('is present and translated in every bundle, placeholders intact', () => {
    const ph = (s) => (String(s).match(/\{\{\s*\w+\s*\}\}/g) || []).sort().join('|');
    for (const loc of bundles.filter((l) => l !== 'en')) {
      const b = read(loc);
      for (const k of keys) {
        expect(b[k], `${loc}.json ${k}`).toBeTruthy();
        expect(b[k], `${loc}.json ${k} untranslated`).not.toBe(en[k]);
        expect(ph(b[k]), `${loc}.json ${k} placeholders`).toBe(ph(en[k]));
      }
    }
  });
});
