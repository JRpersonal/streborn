// The three per-language tables that live in main.js instead of the i18n
// bundles (the tagline, the "for every model" line under it, and the room
// name suggestions) cover every language that has a bundle.
//
// They fall back to English silently, so a language added to i18n/bundles
// without a line here showed an English tagline in an otherwise translated
// window: zh-Hant did for the tagline and the model line, zh-Hant and ar for
// the room names.
import { describe, expect, it } from 'vitest';
import { AVAILABLE_LOCALES } from './i18n/index.js';
import { srcOf } from './test/source.js';

// tableOf evaluates one of the tables. They are plain object literals of
// strings, so the declaration can be run on its own.
function tableOf(name) {
  return new Function(`${srcOf(name)}; return ${name};`)();
}

const codes = AVAILABLE_LOCALES.map((l) => (typeof l === 'string' ? l : l.code));

describe('per-language tables in main.js', () => {
  it('knows the bundled languages', () => {
    expect(codes).toContain('en');
    expect(codes).toContain('zh-Hant');
    expect(codes.length).toBeGreaterThanOrEqual(13);
  });

  for (const name of ['TAGLINES', 'SUPPORTED_LINE']) {
    it(`${name} has a line for every bundled language`, () => {
      const table = tableOf(name);
      const missing = codes.filter((c) => typeof table[c] !== 'string' || !table[c].trim());
      expect(missing).toEqual([]);
    });
  }

  it('TAGLINES and SUPPORTED_LINE name the product in every language', () => {
    for (const name of ['TAGLINES', 'SUPPORTED_LINE']) {
      for (const [code, line] of Object.entries(tableOf(name))) {
        expect(line, `${name}.${code}`).toContain('SoundTouch');
      }
    }
  });

  it('only English reads like the English line', () => {
    const taglines = tableOf('TAGLINES');
    const supported = tableOf('SUPPORTED_LINE');
    for (const c of codes.filter((x) => x !== 'en')) {
      expect(taglines[c], c).not.toBe(taglines.en);
      expect(supported[c], c).not.toBe(supported.en);
    }
  });

  it('ROOM_NAMES_BY_LOCALE has a full list for every bundled language', () => {
    const rooms = tableOf('ROOM_NAMES_BY_LOCALE');
    const missing = codes.filter((c) => !Array.isArray(rooms[c]));
    expect(missing).toEqual([]);
    for (const c of codes) {
      expect(rooms[c].length, c).toBeGreaterThanOrEqual(rooms.en.length);
      expect(new Set(rooms[c]).size, c).toBe(rooms[c].length);
      for (const r of rooms[c]) expect(typeof r === 'string' && r.trim().length > 0, `${c}: ${r}`).toBe(true);
    }
    for (const c of codes.filter((x) => x !== 'en')) expect(rooms[c], c).not.toEqual(rooms.en);
  });
});
