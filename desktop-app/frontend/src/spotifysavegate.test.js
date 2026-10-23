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
import { allSource, srcBetween, srcOf } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const main = allSource();
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const KEY = 'preset.spotifyKeyNeedsLogin';
const PREMIUM = 'preset.spotifyKeyNeedsPremium';
const PLAN = 'preset.spotifyKeyPlanUnknown';

const save = srcOf('saveCurrentToSlot');
const bundle = (lang) => JSON.parse(readFileSync(join(bundleDir, `${lang}.json`), 'utf8'));

// The gate as the save path applies it, lifted out of the save function so the
// order of the two questions is asserted as behaviour and not as spelling.
const gate = srcBetween('      // Same order the speaker applies when the key is pressed',
  '      // Which notice was shown');

// One app run: the once-per-run flag lives as long as the returned function,
// the way the module-level flag in the app lives as long as the app.
function appRun() {
  let shown = null;
  // eslint-disable-next-line no-new-func
  const run = new Function('showToast', 't', `let spotifyPlanHintShown = false;
return (canRecall, premiumRequired, planKnown) => {
${gate}
return notice;
};`)((msg) => { shown = msg; }, (k) => k);
  return (canRecall, premiumRequired, planKnown) => {
    shown = null;
    run(canRecall, premiumRequired, planKnown);
    return shown;
  };
}

function decide(canRecall, premiumRequired, planKnown) {
  return appRun()(canRecall, premiumRequired, planKnown);
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

  it('hints softly when the speaker could not read the plan (#1314)', () => {
    // premiumRequired false with no plan is Premium OR an unanswered question
    // (Spotify answering 429). A free account would make the key silent, so
    // the save says the plan went unchecked and what the key needs.
    expect(decide(true, false, false)).toBe(PLAN);
    expect(decide(true, false, true)).toBeNull();
    // The login notice and the free-account notice still win.
    expect(decide(false, false, false)).toBe(KEY);
    expect(decide(true, true, false)).toBe(PREMIUM);
    // An unreadable speaker stays silent, as every other gate does.
    expect(decide(true, false, undefined)).toBeNull();
    expect(decide(undefined, undefined, undefined)).toBeNull();
  });

  it('gives the plan hint once per app run, not on every save', () => {
    // A Premium account whose token cannot read the plan stays "unknown" for
    // good; repeating the hint on every save would nag exactly those users.
    const run = appRun();
    expect(run(true, false, false)).toBe(PLAN);
    expect(run(true, false, false)).toBeNull();
    expect(run(true, false, false)).toBeNull();
    // The real warnings are not rationed.
    expect(run(false, false, false)).toBe(KEY);
    expect(run(true, true, false)).toBe(PREMIUM);
    expect(run(true, true, false)).toBe(PREMIUM);
    // A new run shows it again.
    expect(appRun()(true, false, false)).toBe(PLAN);
  });

  it('keeps the once-per-run flag outside the save function', () => {
    // Declared inside saveCurrentToSlot it would reset on every save.
    expect(main).toContain('let spotifyPlanHintShown = false;');
    expect(save).not.toContain('let spotifyPlanHintShown');
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
      expect(b[PLAN], `${lang} is missing ${PLAN}`).toBeTruthy();
      expect(b[PLAN], `${lang} does not name Premium`).toMatch(/Premium/);
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
