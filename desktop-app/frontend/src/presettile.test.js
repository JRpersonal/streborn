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
import { shouldAdoptPresetArt } from './utils.js';

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
