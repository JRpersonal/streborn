import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

// Both conditions for a Spotify preset were already written down and neither
// was where somebody saving one would stand: "Spotify Premium" was a
// parenthesis in the What-works card, and the "(STR)" entry was explained in
// the Connect card above, hedged as "on some speaker models".
//
// What that cost is on record in #948 and #950: three speakers' worth of logs
// with account="" on every recall, because the reporter had been picking the
// speaker's own Spotify entry, and "it took me a while to figure out that I
// needed a Spotify Premium account".

const view = readFileSync(new URL('./views/spotify.js', import.meta.url), 'utf8');
const bundle = (l) =>
  JSON.parse(readFileSync(new URL(`./i18n/bundles/${l}.json`, import.meta.url), 'utf8'));
const LANGS = ['en', 'de', 'nl', 'fr', 'es', 'pl', 'tr', 'uk', 'lt', 'lv', 'ja', 'zh-Hant', 'ar'];
const KEYS = ['spotify.presetsNeedTitle', 'spotify.presetsNeedPremium', 'spotify.presetsNeedStr'];

describe('the Spotify presets card states its conditions', () => {
  it('renders them in the presets card', () => {
    for (const k of KEYS) expect(view).toContain(k);
  });

  // Before the steps, not after: somebody who does not meet them cannot follow
  // the steps at all, and finding that out afterwards is the failure being fixed.
  it('puts them before the steps', () => {
    expect(view.indexOf('spotify.presetsNeedTitle')).toBeLessThan(view.indexOf('spotify.presetsStep1'));
    expect(view.indexOf('spotify.presetsIntro')).toBeLessThan(view.indexOf('spotify.presetsNeedTitle'));
  });

  it('has all three in every language', () => {
    for (const l of LANGS) {
      const b = bundle(l);
      for (const k of KEYS) expect(b[k], `${k} missing in ${l}`).toBeTruthy();
    }
  });

  // The exact string is the whole point: "(STR)" is what the user has to look
  // for in Spotify's device list, and a translation that drops it is useless.
  it('names the (STR) entry verbatim in every language', () => {
    for (const l of LANGS) {
      expect(bundle(l)['spotify.presetsNeedStr'], `(STR) missing in ${l}`).toContain('(STR)');
    }
  });

  // Naming a requirement without its consequence leaves the reader guessing
  // whether it matters. Every language says Premium and says what it is for.
  it('names Premium in every language', () => {
    for (const l of LANGS) {
      expect(bundle(l)['spotify.presetsNeedPremium'], `Premium missing in ${l}`).toContain('Premium');
    }
  });
});
