// The running title is shown ONCE, in the now-playing bar above the preset
// grid, and never again inside a preset key.
//
// Field evidence (user report, 2026-08-23, with a screenshot): "The STR app
// looks busy with the ticker bar scrolling type of elements rendered in the
// selected preset key and also in the rectangular block above the preset keys.
// Why display the same information in two places? The song title/artist almost
// always scrolls in the preset key area because the space is not wide enough
// for the string." The key is one third of the window minus a 32px logo, so the
// duplicate line practically always overflowed and marqueed, a few centimetres
// under the bar scrolling the identical text.
//
// The first two groups are source-level assertions, not DOM ones, and that is
// forced: vitest runs with `environment: 'node'` on purpose (see
// vitest.config.js) and renderPresets is DOM-bound, so it deliberately stays in
// main.js rather than being extracted into an importable view module (the
// view-extraction trap). Reading the two files the key is built from is the
// only way to pin down "the title is rendered in exactly one place"; each of
// those assertions fails on the tree that shipped the duplicate. They are kept
// deliberately few, and about this rule alone: the rest of the key's markup and
// the theming of the playing key are not this test's business, and asserting
// them here would only break the next honest refactor.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { shouldAdoptPresetArt, isStrOriginLocation, isLostStrKey, WEBHOOK_PLACEHOLDER_NAME } from './utils.js';

const here = dirname(fileURLToPath(import.meta.url));
const mainJS = readFileSync(join(here, 'main.js'), 'utf-8');
const styleCSS = readFileSync(join(here, 'style.css'), 'utf-8');

// The preset key template: from the innerHTML that opens with .preset-head to
// the long-press bar that closes it. Everything a key renders is in here.
const tileTemplate = (() => {
  const start = mainJS.indexOf('<div class="preset-head">');
  const end = mainJS.indexOf('<div class="long-press-bar" id="lp-bar-', start);
  // Throw rather than assert: this runs at module load, outside any test, and a
  // renamed marker must fail loudly instead of silently matching an empty span.
  if (start < 0 || end <= start) {
    throw new Error('preset tile template not found in main.js; update the markers in this test');
  }
  return mainJS.slice(start, end);
})();

// The rule bodies of style.css with the comments stripped, so a class named in
// a WHY comment (this file's own history is written in those comments) is not
// mistaken for a live rule.
const cssRules = styleCSS.replace(/\/\*[\s\S]*?\*\//g, '');

describe('a preset key shows what is saved on it, not the running title', () => {
  it('does not render the live title inside the key', () => {
    expect(tileTemplate).not.toContain('state.nowTitle');
    expect(tileTemplate).not.toContain('preset-track');
  });

  it('has no marquee hook anywhere in the key', () => {
    // .track-inner is what applyTrackScroll measures and animates. No element
    // of the key may carry it, or the key starts scrolling again. This also
    // covers the saved name: giving that one a marquee would re-create the
    // reported problem with a different string.
    expect(tileTemplate).not.toContain('track-inner');
    expect(cssRules).not.toContain('.preset-track');
  });

  it('never runs the marquee pass over the grid', () => {
    expect(mainJS).not.toContain("applyTrackScroll('.preset-track");
    // The helper's default selector must not reach into the grid either.
    const def = mainJS.match(/function applyTrackScroll\(selector = '([^']*)'\)/);
    expect(def).not.toBeNull();
    expect(def[1]).toBe('.status-bar .now');
  });

  it('keeps the state line, which is what still marks the playing key', () => {
    // A regex, not a plain string: the literal "${stateLabel}" in a quoted
    // string trips eslint's no-template-curly-in-string.
    expect(tileTemplate).toMatch(/\$\{stateLabel\}/);
  });
});

// A speaker-side key STR itself wrote whose store slot is empty is a DEAD key
// (#882): the firmware kept it across a removal and reinstall, the stream
// behind it went with the store. It must never render as the playable
// box-native tile.
const orionLoc = (streamUrl) => '/station?data=' +
  Buffer.from(JSON.stringify({ streamUrl, name: 'WDCB Jazz', imageUrl: '' })).toString('base64url');

describe('isStrOriginLocation', () => {
  it('recognises every form STR writes into a key', () => {
    expect(isStrOriginLocation('http://127.0.0.1:8888/stream/3')).toBe(true);
    expect(isStrOriginLocation('http://127.0.0.1:17008/spotify/stream-2.ogg')).toBe(true);
    expect(isStrOriginLocation(orionLoc('http://127.0.0.1:8888/stream/3'))).toBe(true);
    expect(isStrOriginLocation('/core02/svc-bmx-adapter-orion/prod/orion' + orionLoc('http://127.0.0.1:8888/stream/6'))).toBe(true);
  });
  it('never claims a foreign or ad-hoc location', () => {
    expect(isStrOriginLocation('https://api.deezer.com/user/me/flow')).toBe(false);
    expect(isStrOriginLocation('http://icecast.example.com/stream/1')).toBe(false);
    expect(isStrOriginLocation(orionLoc('https://stream.example.com/live.mp3'))).toBe(false);
    expect(isStrOriginLocation(orionLoc('http://127.0.0.1:8888/stream/raw?u=abc'))).toBe(false);
    expect(isStrOriginLocation('/station?data=!!!')).toBe(false);
    expect(isStrOriginLocation('')).toBe(false);
    expect(isStrOriginLocation(null)).toBe(false);
  });
});

describe('a dead STR key renders as lost, not as playable', () => {
  // The branch between the store tile and the box-native tile.
  const lostBranch = (() => {
    const start = mainJS.indexOf('} else if (bp && lostStrKey(bp)) {');
    const end = mainJS.indexOf('} else if (bp) {', start);
    if (start < 0 || end <= start) throw new Error('lost-key branch not found in main.js; update the markers');
    return mainJS.slice(start, end);
  })();
  it('explains the key instead of offering to play it', () => {
    expect(lostBranch).toContain("t('preset.lostOnSpeaker')");
    expect(lostBranch).not.toContain('boxNativeHint');
    expect(lostBranch).toContain("div.classList.add('lost')");
  });
  it('wires a tap to the explanation, never to the hardware recall', () => {
    const start = mainJS.indexOf('if (bp && lostStrKey(bp)) {', mainJS.indexOf('} else if (bp) {'));
    const wiring = mainJS.slice(start, mainJS.indexOf('} else if (bp) {', start));
    expect(wiring).toContain("showToast(t('preset.lostOnSpeakerToast'))");
    expect(wiring).not.toContain('recallBoxPreset');
  });
  it('delegates the decision to isLostStrKey', () => {
    const start = mainJS.indexOf('function lostStrKey(bp)');
    const fn = mainJS.slice(start, mainJS.indexOf('\n}', start));
    expect(fn).toContain('isLostStrKey(bp)');
  });
  it('has the lost-key styling', () => {
    expect(cssRules).toContain('.preset.lost');
  });
});

// A "webhook only" key (#536) is STR-origin and store-less BY DESIGN: the
// agent parks a placeholder in its own /stream/N form on it so the firmware
// reports the press, and keeps it out of the store. From the location alone
// it is a dead key; it must never render as one, or a shipped feature turns
// into an amber "lost in the reinstall" tile whose tap explains instead of
// pressing the key, plus a false reinstall banner on every visit.
describe('isLostStrKey', () => {
  const own = 'http://127.0.0.1:8888/stream/2';
  const deezer = 'https://api.deezer.com/user/me/flow';

  it('takes the verdict of an agent that sends one', () => {
    expect(isLostStrKey({ slot: 1, name: 'WDCB Jazz', location: orionLoc('http://127.0.0.1:8888/stream/1'), strOrigin: true, lost: true })).toBe(true);
    // The hooked key: STR-origin, store-less, and the agent knows it is hooked.
    expect(isLostStrKey({ slot: 2, name: WEBHOOK_PLACEHOLDER_NAME, location: own, strOrigin: true, lost: false })).toBe(false);
    // A stored slot the agent read back before the store loaded is not lost either.
    expect(isLostStrKey({ slot: 3, name: 'Kept', location: own, strOrigin: true, lost: false })).toBe(false);
    expect(isLostStrKey({ slot: 4, name: 'Flow', location: deezer, strOrigin: false, lost: false })).toBe(false);
  });

  it('judges by the location for an older agent, and spares the webhook placeholder', () => {
    expect(isLostStrKey({ slot: 1, name: 'WDCB Jazz', location: orionLoc('http://127.0.0.1:8888/stream/1') })).toBe(true);
    expect(isLostStrKey({ slot: 2, name: WEBHOOK_PLACEHOLDER_NAME, location: own })).toBe(false);
    expect(isLostStrKey({ slot: 4, name: 'Flow', location: deezer })).toBe(false);
    expect(isLostStrKey(null)).toBe(false);
  });

  it('keeps the placeholder name in step with the agent', () => {
    const agentGo = readFileSync(join(here, '..', '..', '..', 'cmd', 'agent', 'reconcile.go'), 'utf8');
    expect(agentGo).toContain(`const webhookPlaceholderName = "${WEBHOOK_PLACEHOLDER_NAME}"`);
  });
});

// Every string the lost-key tile and banner can show exists in all bundles and
// is not left in English in a non-English one.
describe('lost-key i18n', () => {
  const bundles = ['en', 'de', 'fr', 'es', 'nl', 'pl', 'tr', 'uk', 'lt', 'lv', 'ar', 'ja', 'zh-Hant'];
  const read = (loc) => JSON.parse(readFileSync(join(here, 'i18n', 'bundles', `${loc}.json`), 'utf8'));
  const en = read('en');
  const keys = Object.keys(en).filter((k) => k.startsWith('preset.lost'));
  it('has the strings in English at all', () => {
    expect(keys).toEqual(expect.arrayContaining(['preset.lostOnSpeaker', 'preset.lostOnSpeakerToast', 'preset.lostKeysTitle', 'preset.lostKeysBody']));
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

describe('the block above the keys still carries the title', () => {
  it('renders it there and marquees it there', () => {
    expect(mainJS).toContain('<span class="now"><span class="track-inner">');
    expect(mainJS).toContain("applyTrackScroll('.status-bar .now')");
    expect(cssRules).toContain('.status-bar .now');
    expect(cssRules).toContain('.track-inner.scrolling');
    expect(cssRules).toContain('@keyframes track-marquee');
  });
});

// Fallout of the removal, and the reason this helper exists. The grid used to
// be rebuilt every 12 s by the live-title poller, and a station logo the
// speaker reported one poll after the location was picked up by that rebuild.
// With the key no longer showing the title the rebuild is gone, so the status
// poll now has to recognise the one case that still needs a redraw.
describe('a logo arriving late still reaches the key it belongs to', () => {
  it('adopts the playing station logo onto a key that has none', () => {
    expect(shouldAdoptPresetArt('http://192.0.2.1:8888/art?u=abc', { slot: 3, art: '' })).toBe(true);
  });

  it('leaves a key that already has a logo alone', () => {
    // The key saved its logo on the first adoption; re-adopting on every poll
    // would redraw the whole grid for a value that cannot change.
    expect(shouldAdoptPresetArt('http://192.0.2.1:8888/art?u=abc', { slot: 3, art: 'http://192.0.2.1:8888/art?u=abc' })).toBe(false);
  });

  it('never writes art onto a Spotify key', () => {
    // Those draw the Spotify logo on purpose, and SetPreset is radio-only: a
    // save there would overwrite the preset's Spotify URI.
    expect(shouldAdoptPresetArt('http://192.0.2.1:8888/art?u=abc', { slot: 4, art: '', type: 'spotify' })).toBe(false);
  });

  it('does nothing when there is no logo or no key playing', () => {
    expect(shouldAdoptPresetArt('', { slot: 3, art: '' })).toBe(false);
    expect(shouldAdoptPresetArt('http://192.0.2.1:8888/art?u=abc', null)).toBe(false);
    expect(shouldAdoptPresetArt('', null)).toBe(false);
  });
});
