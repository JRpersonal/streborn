import { describe, expect, it } from 'vitest';
import { titleWords } from './localization.js';

// radio-browser hands language and genre names over in lower case. The old
// ASCII \b\w capitalisation treated a non-ASCII letter as a word break, so
// "español" came out as "EspañOl" in the language list.
describe('titleWords', () => {
  it('keeps letters after an accented letter in lower case', () => {
    expect(titleWords('español internacional')).toBe('Español Internacional');
    expect(titleWords('castellano. español')).toBe('Castellano. Español');
    expect(titleWords('français')).toBe('Français');
    expect(titleWords('türkçe')).toBe('Türkçe');
  });

  it('capitalises after spaces, hyphens and brackets', () => {
    expect(titleWords('español - latinoamerica')).toBe('Español - Latinoamerica');
    expect(titleWords('hip-hop')).toBe('Hip-Hop');
    expect(titleWords('music (live)')).toBe('Music (Live)');
  });

  it('leaves an apostrophe inside a word alone and tolerates empty input', () => {
    expect(titleWords("rock'n'roll")).toBe("Rock'n'roll");
    expect(titleWords('')).toBe('');
    expect(titleWords(null)).toBe('');
  });
});
