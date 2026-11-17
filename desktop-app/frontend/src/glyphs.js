// glyphs.js: the symbols the app draws as inline SVG instead of as a character.
//
// Power (U+23FB), the medium play triangle (U+23F5) and the circled i (U+24D8)
// are in Segoe UI Symbol and in the macOS fonts, but not in what a Debian
// desktop ships (DejaVu Sans plus Noto Color Emoji): there each one was a
// tofu box, the On/Off button of the Music view first (#1948). An SVG needs no
// font. Each icon takes the text colour (currentColor) and, sized in em, the
// size of the text it stands in, so it sits where the character sat. They are
// decorative (aria-hidden): the button around them keeps its aria-label or
// its words.
//
// Pure strings, so vitest reads them without a DOM. The `glyph` class
// (style.css) aligns an icon on the text baseline.

const svg = (body, attrs = '') =>
  `<svg class="glyph" viewBox="0 0 24 24" width="1em" height="1em" aria-hidden="true" focusable="false"${attrs}>${body}</svg>`;

// PLAY_ICON is the play triangle. data-glyph="play" is how the UI 2.0 player
// tells a Play/Pause button that offers Play (shell2/transport.js), now that
// the button's text no longer holds a play character.
export const PLAY_ICON = svg('<path d="M7.5 5v14l11.5-7z" fill="currentColor"/>', ' data-glyph="play"');

// POWER_ICON is the power symbol of the phone remote's power button
// (internal/webui), the same drawing at the size of the input row's
// Bluetooth icon.
export const POWER_ICON = '<svg class="glyph" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" aria-hidden="true" focusable="false"><line x1="12" y1="2.5" x2="12" y2="12"></line><path d="M7.5 6.3a7 7 0 1 0 9 0"></path></svg>';

// INFO_ICON is the circled "i" in front of a hint and on a station row's
// details button.
export const INFO_ICON = svg('<circle cx="12" cy="12" r="9.5" fill="none" stroke="currentColor" stroke-width="1.8"/>'
  + '<circle cx="12" cy="7.6" r="1.3" fill="currentColor"/>'
  + '<path d="M12 11v6.5" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"/>');

// POWER_ICON_PATHS is the power symbol as bare SVG paths, for the remote drawing
// in Settings (an <svg> of its own, where a nested icon is placed by a
// transform): a 24 x 24 box, stroked by the caller's class.
export const POWER_ICON_PATHS = '<line x1="12" y1="2.5" x2="12" y2="12"></line><path d="M7.5 6.3a7 7 0 1 0 9 0"></path>';
