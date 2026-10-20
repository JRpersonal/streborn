import { describe, it, expect } from 'vitest';
import { hasTitleSeparator, splitStreamTitle, titleSwapOffered } from './titleswap.js';

describe('titleSwapOffered', () => {
  it('offers the switch for a proxied station with a two-part title', () => {
    expect(titleSwapOffered(true, 'Sultans of swing - Dire Straits')).toBe(true);
    expect(titleSwapOffered(true, 'Song – Artist')).toBe(true);
    expect(titleSwapOffered(true, 'Song / Artist')).toBe(true);
  });

  it('stays hidden without a radio station or a separator', () => {
    expect(titleSwapOffered(false, 'Sultans of swing - Dire Straits')).toBe(false);
    expect(titleSwapOffered(true, '')).toBe(false);
    expect(titleSwapOffered(true, 'News at noon')).toBe(false);
    expect(titleSwapOffered(true, '- Dire Straits')).toBe(false);
    expect(titleSwapOffered(true, null)).toBe(false);
  });
});

describe('hasTitleSeparator', () => {
  it('needs text on both sides', () => {
    expect(hasTitleSeparator('A - B')).toBe(true);
    expect(hasTitleSeparator('A -')).toBe(false);
    expect(hasTitleSeparator('A-B')).toBe(false);
  });
});

describe('splitStreamTitle', () => {
  // [raw, artist, track]; mirrors TestSplitStreamTitle in internal/webui.
  const cases = [
    ['Nacho Sotomayor - I wonder', 'Nacho Sotomayor', 'I wonder'],
    ["Don't let me go / Kelvin Jones", 'Kelvin Jones', "Don't let me go"], // SWR3 flips
    ['Axwell /\\ Ingrosso - More Than You Know', 'Axwell /\\ Ingrosso', 'More Than You Know'],
    ['Axwell / Ingrosso - More Than You Know', 'Axwell / Ingrosso', 'More Than You Know'],
    ['Axwell Λ Ingrosso - More Than You Know', 'Axwell Λ Ingrosso', 'More Than You Know'],
    ['AC/DC - Highway to Hell', 'AC/DC', 'Highway to Hell'],
    ['Simon & Garfunkel - The Boxer', 'Simon & Garfunkel', 'The Boxer'],
    ['Avicii - Levels - Skrillex Remix', 'Avicii', 'Levels - Skrillex Remix'],
    ['Queen – Under Pressure', 'Queen', 'Under Pressure'],
    ['Just A Station Name', '', 'Just A Station Name'],
    ['- leading dash', '', '- leading dash'],
    ['Axwell /\\ Ingrosso', '', 'Axwell /\\ Ingrosso'],
    ['', '', ''],
    [null, '', ''],
  ];
  for (const [raw, artist, track] of cases) {
    it(`splits ${JSON.stringify(raw)}`, () => {
      expect(splitStreamTitle(raw)).toEqual({ artist, track });
    });
  }
});
