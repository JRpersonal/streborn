// shell2/transport.js: what the player's round Play/Pause button draws and
// says in the UI 2.0 preview.
//
// The button is the Music view's own (#pauseBtn): nowplaying.js writes it as a
// symbol and the word, "&#9205; Play" or "&#9208; Pause", in every language.
// The preview draws the glyph itself (shell2.css) and keeps the word as the
// button's name and tooltip (mount.js nameTransport), so it only needs to know
// which of the two the button holds right now, and the word without the
// symbol.
//
// Pure: strings in, strings out, so vitest can read it without a DOM. The
// symbols are written as code points, so this file stays plain ASCII.

const PLAY = String.fromCodePoint(9205);
// Play, Pause, Stop (&#9205; &#9208; &#9209;) and the two variation selectors
// a platform may put behind them.
const SYMBOLS = [9205, 9208, 9209, 0xFE0E, 0xFE0F].map((c) => String.fromCodePoint(c));

// transportGlyph: 'play' while the button offers Play, 'pause' otherwise
// (Pause is also what an empty or unknown label draws: the button's first
// state in the markup).
export function transportGlyph(text) {
  return String(text || '').includes(PLAY) ? 'play' : 'pause';
}

// transportLabel: the words of a transport button without its leading symbol
// and the space after it.
export function transportLabel(text) {
  const s = String(text || '');
  let at = 0;
  while (at < s.length && (SYMBOLS.includes(s[at]) || s[at].trim() === '')) at++;
  return s.slice(at).trim();
}
