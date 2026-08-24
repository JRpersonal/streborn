// Tests for the pure decision helpers in utils.js.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { savePresetCase, bassControlsDisabled, bassSliderProps } from './utils.js';

const FRESH_MS = 2 * 60 * 1000;
const NOW = 1_000_000_000;
const freshPlay = { url: 'http://radio.example.com/stream', at: NOW - 5_000 };
const stalePlay = { url: 'http://radio.example.com/stream', at: NOW - FRESH_MS - 1 };

describe('savePresetCase', () => {
  it('classifies Spotify locations first, whatever the slot says', () => {
    expect(savePresetCase('http://192.0.2.1:8888/spotify/stream-3.ogg', 3, freshPlay, NOW, FRESH_MS)).toBe('spotify');
    expect(savePresetCase('/playback/container/c3BvdGlmeQ==', null, null, NOW, FRESH_MS)).toBe('spotify');
  });
  it('prefers the app`s own fresh play record over a proxy-slot copy (#252)', () => {
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, freshPlay, NOW, FRESH_MS)).toBe('app-play');
  });
  it('copies the source preset when the app record is stale or absent', () => {
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, stalePlay, NOW, FRESH_MS)).toBe('copy-slot');
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, null, NOW, FRESH_MS)).toBe('copy-slot');
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, { at: NOW }, NOW, FRESH_MS)).toBe('copy-slot'); // no url
  });
  it('saves the box-reported now-playing for non-proxy streams', () => {
    expect(savePresetCase('http://radio.example.com/live.mp3', null, freshPlay, NOW, FRESH_MS)).toBe('direct');
    expect(savePresetCase('', null, null, NOW, FRESH_MS)).toBe('direct');
  });

  // #696: a NATIVE ad-hoc play reports the ORION descriptor, which carries no
  // slot, so sourceSlot is null and the old decision fell to 'direct'. That
  // path saved the descriptor's box-loopback art-proxy URL as the key's
  // artwork (dead from the user's machine, so the tile ended on the grey
  // chevron), even though the app had started the station seconds earlier
  // and holds its full logo chain. When the box report resolves to the SAME
  // URL the app played (the caller passes it as nowStreamUrl), the app's
  // record must win.
  describe('native ad-hoc play, no slot in the location (#696)', () => {
    const orionLoc = '/station?data=irrelevant-here-the-caller-decodes-it';
    it('prefers the fresh app record when the box plays exactly that URL', () => {
      expect(savePresetCase(orionLoc, null, freshPlay, NOW, FRESH_MS, freshPlay.url)).toBe('app-play');
    });
    it('still trusts the box report when it plays something else', () => {
      // No wake-resume race to excuse a mismatch here (unlike #252's proxy
      // branch): a station started from a hardware key or another client
      // must be saved as the box reports it.
      expect(savePresetCase(orionLoc, null, freshPlay, NOW, FRESH_MS, 'http://other.example.com/live')).toBe('direct');
    });
    it('still trusts the box report when the app record has gone stale', () => {
      expect(savePresetCase(orionLoc, null, stalePlay, NOW, FRESH_MS, stalePlay.url)).toBe('direct');
    });
    it('falls back to direct when the caller cannot resolve the box URL', () => {
      expect(savePresetCase(orionLoc, null, freshPlay, NOW, FRESH_MS, '')).toBe('direct');
      expect(savePresetCase(orionLoc, null, freshPlay, NOW, FRESH_MS)).toBe('direct');
    });
  });
});

// The bass slider and the "default" reset button next to it must read the SAME
// gate. Until 2026-08-23 only the slider was disabled on a speaker that reports
// bassAvailable=false, so the button stayed clickable and posted /bass to a
// device with no bass endpoint (Lifestyle 650, mail with photo 2026-08-22).
// The rendering itself is not testable here on purpose: the settings view
// writes innerHTML and the vitest environment is DOM-free, so the shared helper
// is where the gate gets pinned.
describe('bassControlsDisabled', () => {
  it('enables the controls only when the speaker reports bass capability', () => {
    expect(bassControlsDisabled({ available: true, min: -10, max: 10, default: 0 })).toBe(false);
  });
  it('disables them when the speaker says bass is not available', () => {
    expect(bassControlsDisabled({ available: false, min: 0, max: 0, default: 0 })).toBe(true);
  });
  it('disables them when availability is unknown or the reading is missing', () => {
    expect(bassControlsDisabled({})).toBe(true);
    expect(bassControlsDisabled(undefined)).toBe(true);
    expect(bassControlsDisabled(null)).toBe(true);
  });
  // The defect was never in the helper, it was one control skipping it. There
  // is no DOM to render into here, so the settings view's markup is read as
  // source and both controls are checked for the gate. This is the assertion
  // that fails if the reset button ever loses it again. Matched on the helper
  // NAME rather than a literal `bassControlsDisabled(bass)` call so renaming
  // the argument or hoisting the call into a local does not fail a view that
  // is still correct; what must never come back is a control with no gate.
  it('is applied to the slider AND the reset button in the settings view', () => {
    const src = readFileSync(new URL('./views/settings.js', import.meta.url), 'utf8');
    const slider = src.match(/<input type="range" id="boxBass"[\s\S]*?\/>/);
    const resetBtn = src.match(/<button[^>]*id="boxBassReset"[\s\S]*?>/);
    expect(slider, 'the bass slider markup must still be findable').not.toBeNull();
    expect(resetBtn, 'the bass reset button markup must still be findable').not.toBeNull();
    expect(slider[0]).toContain('bassControlsDisabled');
    expect(resetBtn[0]).toContain('bassControlsDisabled');
  });
  // Greying the row without a word is what put the Lifestyle 650 owner in the
  // inbox: he could see that slider and button were dead, not why. The view
  // must therefore carry a reason string on the disabled branch, and it must
  // not be the how-to-use help, which is instructions for a control that
  // cannot move.
  it('the settings view explains the disabled row instead of showing the usage help', () => {
    const src = readFileSync(new URL('./views/settings.js', import.meta.url), 'utf8');
    // Anchor on the slider's id, not on the heading key: the heading key also
    // appears in the section-name lookup table further up the file.
    const start = src.indexOf('id="boxBass"');
    const section = src.slice(start, src.indexOf('settings-section', start + 1));
    expect(start, 'the bass section must still be findable').toBeGreaterThan(-1);
    expect(section).toContain('boxBassReset');
    expect(section).toContain('settingsView.bassUnavailable');
    // The reason replaces the help; both showing at once would tell a user
    // with no bass control how to use one.
    const help = section.indexOf('settingsView.bassHelp');
    const reason = section.indexOf('settingsView.bassUnavailable');
    expect(help, 'the usage help must still exist for speakers that DO have bass').toBeGreaterThan(-1);
    expect(section.slice(Math.min(help, reason), Math.max(help, reason)))
      .toMatch(/[?:]/); // the two sit on opposite sides of one conditional
  });
});

// The bass slider's range, step and position come from one shared mapping.
// The slider is relative to the speaker's default (0 = default); step exists
// for the capability-gated tone-controls route (the Lifestyle 650 case),
// whose write granularity can exceed 1. The agent maps that route's default
// to 0, so relative equals absolute there.
describe('bassSliderProps', () => {
  it('maps the classic reading relative to the default with step 1', () => {
    expect(bassSliderProps({ available: true, min: -9, max: 0, default: 0, actual: -4 }))
      .toEqual({ min: -9, max: 0, step: 1, value: -4 });
    // a non-zero default shifts the whole scale so 0 keeps meaning "default"
    expect(bassSliderProps({ available: true, min: -5, max: 5, default: 2, actual: 4 }))
      .toEqual({ min: -7, max: 3, step: 1, value: 2 });
  });
  it('passes a tone-controls step above 1 through', () => {
    expect(bassSliderProps({ available: true, min: -100, max: 100, default: 0, step: 50, actual: 50 }))
      .toEqual({ min: -100, max: 100, step: 50, value: 50 });
  });
  it('falls back to step 1 when the speaker reports none', () => {
    expect(bassSliderProps({ min: -9, max: 0, step: 0, actual: 0 })).toMatchObject({ step: 1 });
    expect(bassSliderProps({})).toEqual({ min: 0, max: 0, step: 1, value: 0 });
    expect(bassSliderProps(undefined)).toEqual({ min: 0, max: 0, step: 1, value: 0 });
  });
  // Source check in the style of the gate check above: the slider must take
  // all four properties from the helper. Until 2026-08-24 the template
  // hardcoded step="1", which posts values a tone-controls speaker is free
  // to refuse.
  it('the settings view slider takes min/max/step/value from this helper', () => {
    const src = readFileSync(new URL('./views/settings.js', import.meta.url), 'utf8');
    const slider = src.match(/<input type="range" id="boxBass"[\s\S]*?\/>/);
    expect(slider, 'the bass slider markup must still be findable').not.toBeNull();
    expect(slider[0]).not.toContain('step="1"');
    for (const key of ['min', 'max', 'step', 'value']) {
      expect(slider[0]).toContain('${bassProps.' + key + '}');
    }
  });
});

// Every user-facing string the bass row can show must exist in all bundles and
// must not be left in English in a non-English one. The i18n coverage script
// checks presence across the whole bundle set; this pins the one string added
// on 2026-08-23, because a reason line that renders as a raw key or as English
// on a German speaker's screen is worse than no reason line at all.
describe('settingsView.bassUnavailable', () => {
  const bundles = ['en', 'de', 'fr', 'es', 'nl', 'pl', 'tr', 'uk', 'lt', 'lv', 'ar', 'ja', 'zh-Hant'];
  const read = (loc) => JSON.parse(
    readFileSync(new URL(`./i18n/bundles/${loc}.json`, import.meta.url), 'utf8'));
  it('is present in every bundle', () => {
    for (const loc of bundles) {
      expect(read(loc)['settingsView.bassUnavailable'], `${loc}.json`).toBeTruthy();
    }
  });
  it('is translated, not the English text copied', () => {
    const en = read('en')['settingsView.bassUnavailable'];
    for (const loc of bundles.filter((l) => l !== 'en')) {
      expect(read(loc)['settingsView.bassUnavailable'], `${loc}.json`).not.toBe(en);
    }
  });
});
