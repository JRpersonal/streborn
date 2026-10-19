// The "could not be kept" banner used to give Spotify advice for every
// refused long press, also on a speaker playing radio (#1217). The Spotify
// sentence now only shows when Spotify was playing.
import { describe, it, expect } from 'vitest';
import { holdRefusedBannerKey } from './utils.js';
import { t, setLocale } from './i18n/index.js';

describe('hold refused banner text', () => {
  it('gives the Spotify advice only for a Spotify hold', () => {
    expect(holdRefusedBannerKey('SPOTIFY')).toBe('speaker.holdRefusedBanner');
    expect(holdRefusedBannerKey('spotify')).toBe('speaker.holdRefusedBanner');
  });

  it('states the refusal plainly for any other source', () => {
    setLocale('en');
    for (const src of ['LOCAL_INTERNET_RADIO', 'UPNP', 'TUNEIN', '', undefined]) {
      expect(holdRefusedBannerKey(src)).toBe('speaker.holdRefusedBannerGeneric');
    }
    const text = t(holdRefusedBannerKey('LOCAL_INTERNET_RADIO'), { name: 'Kitchen', slot: '6' });
    expect(text).toContain('key 6');
    expect(text).not.toContain('Spotify');
  });
});
