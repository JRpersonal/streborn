import { describe, it, expect } from 'vitest';
import { hasTitleSeparator, titleSwapOffered } from './titleswap.js';

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
