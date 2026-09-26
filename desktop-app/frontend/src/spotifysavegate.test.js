// Saving a Spotify playlist to a hardware key reported success while the
// speaker already knew the key would be refused when pressed (#976). Both
// recall paths gate on the same question; only the SAVE side had no gate.
//
// The first half shipped in v0.9.86 and asked the LOGIN question, which is what
// CanRecall answers. The half #976 actually asked for is the ENTITLEMENT: a free
// Spotify account cannot do the on-demand playback a recall needs, so a key saved
// on one stores fine and then never plays, and nothing at save time said so.
//
// The reporter also read the login warning as a claim about his account while he
// was streaming Spotify at that moment. He was: to the speaker's own Bose
// receiver, not to STR's "(STR)" entry, which is the thing that had not been
// picked. The sentence now says which one it means.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(join(here, 'main.js'), 'utf8');
const main = src.replace(/\r\n/g, '\n');
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const KEY = 'preset.spotifyKeyNeedsLogin';
const PREMIUM = 'preset.spotifyKeyNeedsPremium';

const save = src.slice(src.indexOf('async function saveCurrentToSlot'),
  src.indexOf('async function saveCurrentToSlot') + 8000);
const bundle = (lang) => JSON.parse(readFileSync(join(bundleDir, `${lang}.json`), 'utf8'));

// The gate as the save path applies it, lifted out of main.js so the order of the
// two questions is asserted as behaviour and not as spelling.
const at = main.indexOf('      // Same order the speaker applies when the key is pressed');
const gate = main.slice(at, main.indexOf('      // Which notice was shown', at));

function decide(canRecall, premiumRequired) {
  let shown = null;
  // eslint-disable-next-line no-new-func
  new Function('canRecall', 'premiumRequired', 'showToast', 't', `${gate}\nreturn notice;`)(
    canRecall, premiumRequired, (msg) => { shown = msg; }, (k) => k,
  );
  return shown;
}

describe('saving a Spotify key that cannot play', () => {
  it('warns when the speaker says it cannot recall', () => {
    expect(decide(false, false)).toBe(KEY);
  });

  it('warns about a free account, which is what #976 asked for', () => {
    // The speaker is picked and streaming; the plan is what will refuse the key.
    expect(decide(true, true)).toBe(PREMIUM);
  });

  it('stays quiet when the speaker can recall', () => {
    expect(decide(true, false)).toBeNull();
  });

  it('stays quiet when the speaker is too old to say', () => {
    // An agent that predates the fields sends nothing. "Cannot tell" must never
    // become a warning, or every un-updated speaker would cry wolf.
    expect(decide(undefined, undefined)).toBeNull();
    expect(decide(true, undefined)).toBeNull();
  });

  it('reads both answers from the fetch it already makes', () => {
    // No extra request: SpotifyNowPlaying is read on this path anyway.
    expect(save).toContain('np.canRecall');
    expect(save).toContain('np.premiumRequired');
    expect(save).toContain('canRecall === false');
  });

  it('warns AFTER saving, and does not refuse the save', () => {
    // The key becomes good the moment the speaker is picked in Spotify once,
    // so refusing to store it would throw away work the user will want.
    const saved = save.indexOf('preset.savedToKey');
    const warned = save.indexOf(KEY);
    expect(saved).toBeGreaterThan(-1);
    expect(warned).toBeGreaterThan(saved);
    expect(save.slice(saved, warned)).not.toContain('return;');
  });
});

describe('the wording claims only what STR can see', () => {
  it('names the (STR) entry instead of claiming there is no Spotify login', () => {
    const en = bundle('en');
    expect(en[KEY]).toContain('(STR)');
    expect(en[KEY]).not.toMatch(/no Spotify login/i);
    // The free-account limitation belongs in the same sentence the speaker's own
    // refusal (recallgate.go) and the phone remote already put it in.
    expect(en[KEY]).toMatch(/free/i);
  });

  it('has both warnings in every language', () => {
    for (const lang of langs) {
      const b = bundle(lang);
      expect(b[KEY], `${lang} is missing ${KEY}`).toBeTruthy();
      expect(b[PREMIUM], `${lang} is missing ${PREMIUM}`).toBeTruthy();
      expect(b[KEY], `${lang} does not name the (STR) entry`).toContain('(STR)');
      expect(b[PREMIUM], `${lang} does not name Premium`).toMatch(/Premium/);
    }
  });

  it('keeps real umlauts in the German warnings', () => {
    for (const k of [KEY, PREMIUM]) {
      const de = bundle('de')[k];
      expect(de).toMatch(/[äöüß]/);
      expect(de).not.toMatch(/\b(oeffne|waehl|Lautsprecheranmeldung)\b/);
    }
  });
});

describe('the save leaves a trace', () => {
  it('records what the gate decided and what it decided it from', () => {
    // "I saw two different notices for one long press" was unanswerable from a
    // diagnostic bundle: the app logged nothing for a save at all.
    expect(main).toContain('LogSpotifySaveGate(state.currentBox.host, slot,');
    expect(main).toContain("canRecall === undefined ? 'unknown' :");
    expect(main).toContain("premiumRequired === undefined ? 'unknown' :");
  });
});
