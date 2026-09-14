// Renaming a preset key from the app (#812).
//
// Station names come out of the radio directory shouted in capitals, misspelled,
// or long enough to fill the whole tile, and the key belongs to the user. The
// tile grew a pencil for it.
//
// These are source-level assertions, like presettile.test.js next door, and for
// the same reason: vitest runs with `environment: 'node'` (see vitest.config.js)
// and renderPresets is DOM-bound, so the grid deliberately stays in main.js
// rather than moving into an importable view module (the view-extraction trap).
// Reading the file is the only way to pin down the two rules that actually
// matter here, and each assertion fails on the tree before the pencil existed.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const mainJS = readFileSync(join(here, 'main.js'), 'utf-8');
const styleCSS = readFileSync(join(here, 'style.css'), 'utf-8');
const apiJS = readFileSync(join(here, 'api.js'), 'utf-8');

describe('a preset key can be renamed', () => {
  it('offers the pencil on the key and wires it to the rename', () => {
    expect(mainJS).toContain("t('preset.renameTitle')");
    expect(mainJS).toContain("grid.querySelectorAll('.ren')");
    expect(mainJS).toContain('renamePresetKey(parseInt(el.dataset.slot, 10))');
    expect(styleCSS).toContain('.preset .ren');
  });

  it('writes the name alone, through the rename binding', () => {
    expect(apiJS).toContain('RenamePreset');
    const fn = mainJS.slice(
      mainJS.indexOf('async function renamePresetKey(slot)'),
      mainJS.indexOf('\n// applyTrackScroll'),
    );
    expect(fn).toContain('RenamePreset(state.currentBox.host, state.currentBox.port, slot, name)');
    // A rename must never go out as a station save: SetPreset would rewrite the
    // stream URL and a Spotify key's playlist with whatever the app has cached.
    expect(fn).not.toContain('SetPreset(');
    expect(fn).not.toContain('SaveSpotifyPreset(');
    // Nothing is sent when the user cancels, clears the field, or retypes the
    // same name: each of those would cost the speaker a flash write for nothing.
    expect(fn).toContain('if (!await answer) return;');
    expect(fn).toContain('if (!name || name === current) return;');
  });

  it('keeps the icons in the header from pressing the key underneath', () => {
    // The pencil and the X sit INSIDE the element that carries the play click
    // and the hold-to-save, so a tap on either must not reach it.
    const fn = mainJS.slice(
      mainJS.indexOf('function isKeyChrome(target)'),
      mainJS.indexOf('function attachPresetHandlers'),
    );
    expect(fn).toContain("cl.contains('del')");
    expect(fn).toContain("cl.contains('ren')");
    const handlers = mainJS.slice(
      mainJS.indexOf('function attachPresetHandlers'),
      mainJS.indexOf('const APP_PLAY_FRESH_MS'),
    );
    expect(handlers).toContain('if (isKeyChrome(e.target)) return;');
  });

  it('stops at the same name length as the agent', () => {
    // The agent shortens a longer name (maxPresetNameRunes), so the field has to
    // stop at the same count or a typed name comes back cut.
    expect(mainJS).toContain('const PRESET_NAME_MAX = 64;');
    // A regex, not a plain string: the literal "${PRESET_NAME_MAX}" in a quoted
    // string trips eslint's no-template-curly-in-string.
    expect(mainJS).toMatch(/maxlength="\$\{PRESET_NAME_MAX\}"/);
  });
});

// Every string the rename can show exists in all bundles, is translated, and
// keeps its placeholders.
describe('rename i18n', () => {
  const bundles = ['en', 'de', 'fr', 'es', 'nl', 'pl', 'tr', 'uk', 'lt', 'lv', 'ar', 'ja', 'zh-Hant'];
  const read = (loc) => JSON.parse(readFileSync(join(here, 'i18n', 'bundles', `${loc}.json`), 'utf8'));
  const en = read('en');
  const keys = ['preset.renameTitle', 'preset.renameHeading', 'preset.renameBody', 'preset.renamedKey'];

  it('has the strings in English at all', () => {
    for (const k of keys) expect(en[k], `en.json ${k}`).toBeTruthy();
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

  it('uses real umlauts in German, never ae/oe/ue', () => {
    const de = read('de');
    expect(de['preset.renameBody']).toContain('heißen');
    expect(de['preset.renameBody']).toContain('ändert');
  });
});
