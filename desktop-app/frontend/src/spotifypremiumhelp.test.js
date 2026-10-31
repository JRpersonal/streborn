// A Spotify preset on a speaker whose Spotify account is free is refused by the
// agent with the code spotify-premium-required. The tile used to say only
// "Spotify recall needs Premium" and nothing else, while the sibling
// not-logged-in case got a toast with the full how-to (#1636). These pin that
// the Premium case now gets the same treatment, keyed off the stable code.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { srcOf, srcBetween } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const bundle = (l) => JSON.parse(readFileSync(join(here, 'i18n', 'bundles', `${l}.json`), 'utf8'));

function friendlyPlayError() {
  return new Function('t', 'deps', 'escalationForCount', 'noteNotReady',
    `${srcOf('friendlyPlayError')}; return friendlyPlayError;`)(
    (k) => k,
    { stereoStaleForError: () => null },
    () => 'play.errNotReady',
    () => 1,
  );
}

describe('Spotify Premium refusal', () => {
  it('labels the tile from the stable code, with the bare word as fallback', () => {
    const f = friendlyPlayError();
    expect(f('spotify-premium-required: The Spotify account on this speaker is a free account.', ''))
      .toBe('play.errSpotifyPremium');
    expect(f('spotify-premium-required', '')).toBe('play.errSpotifyPremium');
    expect(f('needs Premium', '')).toBe('play.errSpotifyPremium');
    // The not-logged-in message mentions Premium too and must keep its own label.
    expect(f('spotify-not-logged-in: A Spotify preset needs a Spotify Premium account', ''))
      .toBe('play.errSpotifyLogin');
  });

  it('shows the full help as a toast on the code', () => {
    const block = srcBetween("includes('spotify-premium-required')) {", '}');
    expect(block).toContain("showToast(t('play.errSpotifyPremiumHelp'))");
  });

  it('carries a short label and the full help in every locale', () => {
    for (const l of langs) {
      const b = bundle(l);
      expect(b['play.errSpotifyPremium'], l).toBeTruthy();
      expect(b['play.errSpotifyPremiumHelp'], l).toBeTruthy();
      expect(b['play.errSpotifyPremiumHelp'].length, l).toBeGreaterThan(b['play.errSpotifyPremium'].length * 3);
      expect(b['play.errSpotifyPremiumHelp'], l).toContain('(STR)');
    }
  });
});
