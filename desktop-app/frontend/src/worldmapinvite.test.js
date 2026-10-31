import { describe, it, expect } from 'vitest';
import { pinPromptKey, worldMapURL } from './worldmapinvite.js';
import { setLocale } from './i18n/index.js';

// The invite must always ASK for a pin. The count is decoration on top of that
// ask, never its precondition: the fetch behind it returns 0 on every failure,
// silently, so hanging the ask on it meant users with no route to the website
// got confetti and no request.
describe('pinPromptKey', () => {
  it('uses the counted sentence when a real count arrives', () => {
    expect(pinPromptKey(1)).toEqual({ key: 'worldMap.countLine', params: { n: 1 } });
    expect(pinPromptKey(4213)).toEqual({ key: 'worldMap.countLine', params: { n: 4213 } });
  });

  it('still asks when the count fetch failed, which it does by returning 0', () => {
    for (const n of [0, -1, null, undefined, NaN, Infinity, '12', {}, []]) {
      expect(pinPromptKey(n).key, String(n)).toBe('worldMap.pinPrompt');
    }
  });

  it('never interpolates a fractional count into the sentence', () => {
    expect(pinPromptKey(7.9)).toEqual({ key: 'worldMap.countLine', params: { n: 7 } });
  });
});

// worldMapURL moved out of main.js (UI 2.0 Help and info opens it too). The
// link must come out exactly as the old main.js function built it.
describe('worldMapURL', () => {
  const old = (loc) => {
    const l = loc || 'en';
    const prefix = l === 'en' ? '' : '/' + l;
    return 'https://st-reborn.de' + prefix + '/?share&src=app#community';
  };

  it('builds the same link as the old main.js function for en, de and another locale', () => {
    for (const loc of ['en', 'de', 'ja', 'zh-Hant']) expect(worldMapURL(loc), loc).toBe(old(loc));
    expect(worldMapURL('en')).toBe('https://st-reborn.de/?share&src=app#community');
    expect(worldMapURL('de')).toBe('https://st-reborn.de/de/?share&src=app#community');
    expect(worldMapURL('fr')).toBe('https://st-reborn.de/fr/?share&src=app#community');
  });

  it('follows the app language when called without one, as main.js did', () => {
    for (const loc of ['en', 'de', 'pl']) {
      setLocale(loc);
      expect(worldMapURL(), loc).toBe(old(loc));
    }
    setLocale('en');
  });
});
