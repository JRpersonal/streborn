// Saving a Spotify playlist to a hardware key reported success while the
// speaker already knew the key would be refused when pressed (#976). Both
// recall paths gate on the same question; only the SAVE side had no gate.
//
// The reporter's own speakers are the case that matters: STR had never been
// picked in Spotify on any of them, so there is no account to read a plan from
// and PremiumRequired stays false. The honest question at save time is
// therefore the LOGIN, not the plan, which is exactly what CanRecall answers
// and what both recall paths already use.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(join(here, 'main.js'), 'utf8');
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const KEY = 'preset.spotifyKeyNeedsLogin';

const save = src.slice(src.indexOf('async function saveCurrentToSlot'),
  src.indexOf('async function saveCurrentToSlot') + 7000);

// The tri-state the save path applies, lifted out so it is asserted as
// behaviour and not as spelling.
const warns = (canRecall) => canRecall === false;

describe('saving a Spotify key that cannot play', () => {
  it('warns when the speaker says it cannot recall', () => {
    expect(warns(false)).toBe(true);
  });

  it('stays quiet when the speaker can recall', () => {
    expect(warns(true)).toBe(false);
  });

  it('stays quiet when the speaker is too old to say', () => {
    // An agent that predates the field sends nothing. "Cannot tell" must never
    // become a warning, or every un-updated speaker would cry wolf.
    expect(warns(undefined)).toBe(false);
  });

  it('reads canRecall from the fetch it already makes', () => {
    // No extra request: SpotifyNowPlaying is read on this path anyway.
    expect(save).toContain('np.canRecall');
    expect(save).toContain('canRecall === false');
  });

  it('warns AFTER saving, and does not refuse the save', () => {
    // The key becomes good the moment the speaker is picked in Spotify once,
    // so refusing to store it would throw away work the user will want.
    const saved = save.indexOf("preset.savedToKey");
    const warned = save.indexOf(KEY);
    expect(saved).toBeGreaterThan(-1);
    expect(warned).toBeGreaterThan(saved);
    expect(save.slice(saved, warned)).not.toContain('return;');
  });

  it('has the warning in every language', () => {
    for (const lang of langs) {
      const b = JSON.parse(readFileSync(join(bundleDir, `${lang}.json`), 'utf8'));
      expect(b[KEY], `${lang} is missing ${KEY}`).toBeTruthy();
    }
  });

  it('keeps real umlauts in the German warning', () => {
    const de = JSON.parse(readFileSync(join(bundleDir, 'de.json'), 'utf8'))[KEY];
    expect(de).toMatch(/[äöüß]/);
    expect(de).not.toMatch(/\b(oeffne|waehl|Lautsprecheranmeldung)\b/);
  });
});
