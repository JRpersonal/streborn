// Emoji flags are a FONT question, not an operating-system question: the same
// Windows 11 build draws them on one machine and shows two letters on another
// (field pair, 2026-08-12). The decision therefore comes from a measurement,
// and this pins how that measurement is read.
import { describe, it, expect } from 'vitest';
import { flagsSupportedFromWidths, flagFromCC } from './localization.js';

describe('emoji flag support detection', () => {
  it('sees a real flag font, where the pair composes into one glyph', () => {
    // Apple Color Emoji and friends: the pair is one glyph, barely wider than
    // a single regional indicator.
    expect(flagsSupportedFromWidths(17, 16)).toBe(true);
  });

  it('sees a font without flags, where the pair is two letter boxes', () => {
    expect(flagsSupportedFromWidths(32, 16)).toBe(false);
  });

  it('refuses to decide on unusable measurements', () => {
    expect(flagsSupportedFromWidths(0, 0)).toBe(false);
    expect(flagsSupportedFromWidths(NaN, 16)).toBe(false);
    expect(flagsSupportedFromWidths(16, 0)).toBe(false);
  });

  it('still builds the flag codepoints correctly', () => {
    expect(flagFromCC('DE')).toBe('\u{1F1E9}\u{1F1EA}');
    expect(flagFromCC('de')).toBe('\u{1F1E9}\u{1F1EA}');
    expect(flagFromCC('X')).toBe('');
    expect(flagFromCC('')).toBe('');
  });
});
