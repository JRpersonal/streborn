// stickers/layout.js: where everything on the sticker sheet goes.
//
// Pure: no DOM, no api.js. Speakers in, pages of positioned pieces out, all in
// millimetres, so draw.js only has to paint and a test can check the geometry
// without a canvas. The one thing that needs a browser, the width of a piece
// of text, comes in as measure(text, { size, weight }) and answers in mm.
//
// The stickers are true to size. Their dimensions were fitted to the speakers
// on a 6 px per mm drawing and are kept in those units below (u() converts),
// so a number here can be compared with the drawing it came from.
//
// Input:  [{ name, model, housing: 'white'|'dark',
//            keys: [{ n, name, kind: 'station'|'playlist', logo,
//                     twice: { name, kind } | null }], qr }]
//         Every speaker also gets the 40 x 75 mm sticker for the back of its
//         remote: there is no remote it would not fit, so it is not optional.
// Output: { pages: [{ header, pieces, scissors, control, guide }] }
//         guide holds the text lines of the home screen steps (texts.homeScreen):
//         on the last page under the control line, or on a page of its own.

const u = (px) => px / 6;

export const PAGE = { w: 210, h: 297 };
// Everything stays inside this area, centred on the A4 page. It also fits a
// US Letter sheet (215.9 x 279.4 mm) printed at actual size, which is what a
// North American printer does with an A4 PDF.
export const AREA = { x: 10, y: 21, w: 190, h: 255 };
// The background runs this far past every cut line, so a cut that is slightly
// off still shows no white edge.
export const BLEED = 1.5;
// Half the edge of the square a scissors symbol takes.
export const SCISSORS_HALF = 2.5;
export const CONTROL_LENGTH = 100;

const PALETTES = {
  light: { paper: '#EFE6D6', ink: '#161616', rule: '#D0C7B6', tile: '#DDD4C3', mute: '#6A655C' },
  st10: { paper: '#E5DCC8', ink: '#161616', rule: '#CEC4AC', tile: '#DAD0B8', mute: '#6A6456' },
  dark: { paper: '#141518', ink: '#F0F0EE', rule: '#35373C', tile: '#2C2E33', mute: '#A9AAAE' },
  portable: { paper: '#707274', ink: '#FFFFFF', rule: '#8B8D8F', tile: '#838587', mute: '#FFFFFF' },
};

// stickerKind is the sticker a model gets: the ST10's two arcs, the wide
// sticker of the ST20, the same twice as tall for the larger ST30, the
// Portable's grid, or for everything else (Wave, soundbars such as the
// SoundTouch 300, a model not known here) the ST20's sticker without its
// curve.
export function stickerKind(model) {
  const m = String(model || '').toLowerCase();
  if (/soundtouch\s*portable/.test(m)) return 'portable';
  if (/soundtouch\s*10\b/.test(m)) return 'st10';
  if (/soundtouch\s*20\b/.test(m)) return 'st20';
  if (/soundtouch\s*30\b/.test(m)) return 'st30';
  return 'plain';
}

// stickerPalette matches the sticker to the housing. The colour cannot be read
// from the speaker, so the user says white or dark. The ST10's white and the
// Portable's grey are their own shades; the rest share two.
export function stickerPalette(kind, housing) {
  const white = housing === 'white';
  if (kind === 'st10') return white ? PALETTES.st10 : PALETTES.dark;
  if (kind === 'portable') return white ? PALETTES.light : PALETTES.portable;
  return white ? PALETTES.light : PALETTES.dark;
}

// ---------- text fitting ----------

const ELLIPSIS = '…';
// A name that does not fit two lines is tried narrower and smaller before it
// is cut.
const NAME_SCALES = [1, 0.9, 0.8];
// How narrow a line may be drawn where the system has no condensed font, which
// the stickers were designed with. draw.js squeezes a line into its box, so
// text up to 1/SQUEEZE as wide as the box still fits and the letters look
// like a condensed cut. A real condensed font is only squeezed a little, to
// the width of the one the design was made with (layoutStickers' squeeze
// option).
export const SQUEEZE = 0.82;
export const SQUEEZE_CONDENSED = 0.9;
let squeeze = SQUEEZE;
const loose = (maxW) => maxW / squeeze;

// tokens splits text where a line may break: at spaces (dropped at a break)
// and after hyphens (kept).
function tokens(text) {
  const out = [];
  for (const word of String(text || '').trim().split(/\s+/).filter(Boolean)) {
    const parts = word.match(/[^-]*-+|[^-]+/g) || [word];
    parts.forEach((t, i) => out.push({ t, space: i === 0 }));
  }
  return out;
}

const join = (toks) => toks.map((k, i) => (i > 0 && k.space ? ' ' : '') + k.t).join('');

// ellipsise cuts text to maxW and marks the cut.
export function ellipsise(text, maxW, font, measure) {
  const s = String(text || '');
  if (!s || measure(s, font) <= maxW) return s;
  const chars = Array.from(s);
  while (chars.length > 1) {
    chars.pop();
    const cand = chars.join('').trimEnd() + ELLIPSIS;
    if (measure(cand, font) <= maxW) return cand;
  }
  return chars.join('') + ELLIPSIS;
}

// wrapText breaks text into at most maxLines lines of maxW. A word longer
// than a line is broken inside (a name in a script without spaces is one
// word). What does not fit ends the last line with an ellipsis.
export function wrapText(text, maxW, font, measure, maxLines = 2) {
  const fits = (s) => measure(s, font) <= maxW;
  const lines = [];
  let cur = [];
  for (const tok of tokens(text)) {
    if (fits(join([...cur, tok]))) { cur.push(tok); continue; }
    if (cur.length) { lines.push(cur); cur = []; }
    if (fits(tok.t)) { cur = [tok]; continue; }
    let piece = '';
    let first = true;
    for (const ch of Array.from(tok.t)) {
      if (!piece || fits(piece + ch)) { piece += ch; continue; }
      lines.push([{ t: piece, space: first && tok.space }]);
      first = false;
      piece = ch;
    }
    cur = [{ t: piece, space: first && tok.space }];
  }
  if (cur.length) lines.push(cur);
  if (lines.length <= maxLines) return { lines: lines.map(join), truncated: false };
  // What is left goes back together the way it was written and is cut once.
  const rest = join(lines.slice(maxLines - 1).flat());
  return { lines: [...lines.slice(0, maxLines - 1).map(join), ellipsise(rest, maxW, font, measure)], truncated: true };
}

// fitName lays a key's name out on up to two lines, smaller when that avoids
// cutting it. One line a step smaller is preferred over two: it leaves the
// cell room for the pressed-twice entry.
export function fitName(text, maxW, size, measure) {
  const name = String(text || '').trim();
  for (const scale of NAME_SCALES.slice(0, 2)) {
    if (name && measure(name, { size: size * scale, weight: 700 }) <= loose(maxW)) return { lines: [name], size: size * scale };
  }
  let last = { lines: [], size };
  for (const scale of NAME_SCALES) {
    const font = { size: size * scale, weight: 700 };
    for (const w of [maxW, loose(maxW)]) {
      const r = wrapText(text, w, font, measure, 2);
      last = { lines: r.lines, size: font.size };
      if (!r.truncated) return last;
    }
  }
  return last;
}

// ---------- large text ----------

// Large text (layoutStickers' large option) sets the names larger than
// normal, for eyes that cannot read the normal sizes from where the speaker
// stands. Names are searched from the largest size the height allows (capped
// by LARGE_CAP) down to the normal size in these steps, and the first size at
// which the whole name fits wins; evenly then gives all names of one sticker
// the same size, so the sticker reads as a set.
let large = false;
const LARGE_STEP = 0.1;
// The line height of a name, in sizes.
const NAME_LH = 1.05;
const round3 = (v) => Math.round(v * 1000) / 1000;

// sizesDown lists from, from - LARGE_STEP, ... down to to, which is always
// the last one tried. Empty when from is smaller than to.
function sizesDown(from, to) {
  if (!(from >= to)) return [];
  const out = [];
  for (let i = 0; from - i * LARGE_STEP > to + 1e-9; i++) out.push(round3(from - i * LARGE_STEP));
  out.push(to);
  return out;
}

// largestWhole finds the largest size in sizes at which text fits maxW whole,
// squeezed if need be, on at most maxLines lines that fit roomAt(size), the
// height there is for the text at that size: { lines, size }, or null.
function largestWhole(text, maxW, sizes, maxLines, roomAt, measure) {
  for (const size of sizes) {
    const n = Math.min(maxLines, Math.floor(roomAt(size) / (size * NAME_LH) + 1e-9));
    if (n < 1) continue;
    const font = { size, weight: 700 };
    for (const w of [maxW, loose(maxW)]) {
      const lines = wholeLines(text, w, font, measure, n);
      if (lines) return { lines, size };
    }
  }
  return null;
}

// twiceRoom is the width the name on a pressed-twice line of width w has at
// size: what is left after the marker and, for a playlist, the list symbol
// (the way addTwice lays the line out).
function twiceRoom(twice, w, size, measure) {
  let at = measure('2×', { size, weight: 700 }) + u(4);
  if (twice.kind === 'playlist') at += size + u(4);
  return Math.max(0, w - at);
}

// withSize adds size to a list from sizesDown, in its place, when it is not
// above top.
function withSize(sizes, size, top) {
  if (!(size > 0) || size > top + 1e-9 || sizes.some((z) => Math.abs(z - size) < 1e-9)) return sizes;
  return [...sizes, size].sort((a, b) => b - a);
}

// largeName is the large-text name of a cell together with its pressed-twice
// line: the largest name size from the height down to the normal one at which
// the name fits whole. The pressed-twice line grows with the name in the
// cell's own proportion, never larger than the name, and a step smaller where
// its name would not fit; it never goes below its normal size. roomFor(t)
// is the height the name has beside a pressed-twice line of size t (0: none),
// w the name's width and w2 the pressed-twice line's. Answers { fit, two },
// or null when no size above the normal one fits.
// base is the smallest name size that takes part in the sticker's shared
// size (see evenly). also, where given, is { name, two }: the cell's own
// normal sizes. The name's is tried in its place among the steps, which could
// step over it otherwise; the pressed-twice line's is its lowest size.
//
// Within one sticker the names share one size (see evenly): the size search
// starts no higher than shared.name, and the pressed-twice line no higher
// than shared.two. A name never fills more than LARGE_CAP of its height on
// one line, so a short name does not tower over the others.
function largeName(key, w, w2, s, maxLines, roomFor, measure, twoCap = Infinity, base = s.name, also = null) {
  const ratio = s.two / s.name;
  const twoTop = Math.min(twoCap, shared && shared.two ? shared.two : Infinity);
  // The pressed-twice size at name size z, and whether its name fits whole.
  // The pressed-twice line never goes below its normal size, unless the
  // shared name size is below this cell's normal one: it then keeps its
  // proportion to that name, like the other cells' lines.
  const low = also && also.two > 0 ? Math.max(s.two, Math.min(also.two, shared && shared.name ? round3(shared.name * ratio) : Infinity)) : s.two;
  const twiceAt = (z) => {
    if (!key.twice) return { t: 0, fits: true };
    const top = Math.max(low, Math.min(z * ratio, twoTop));
    for (const t of sizesDown(top, low)) {
      if (measure(key.twice.name, { size: t, weight: 600 }) <= loose(twiceRoom(key.twice, w2, t, measure))) return { t, fits: true };
    }
    return { t: low, fits: false };
  };
  let from = Math.min(roomFor(key.twice ? low : 0) / NAME_LH, Math.max(base, (LARGE_CAP * roomFor(0)) / NAME_LH));
  if (shared && shared.name) from = Math.min(from, shared.name);
  const sizes = withSize(sizesDown(from, s.name), also && also.name, from);
  const fit = largestWhole(String(key.name), w, sizes, maxLines, (z) => roomFor(twiceAt(z).t), measure);
  const tw = fit && key.twice ? twiceAt(fit.size) : null;
  if (shared && shared.probe) shared.probe.push({ size: fit ? fit.size : null, base });
  // Only the names set at the shared size choose the pressed-twice size.
  if (shared && shared.twos && tw && tw.fits && fit.size >= shared.name - 1e-9) shared.twos.push(tw.t);
  return fit ? { fit, two: tw ? tw.t : low } : null;
}

// ---------- one size per sticker ----------

// The share of a cell's height one line of a large name may take at most.
export const LARGE_CAP = 0.6;
// The lines a large name may take.
const LARGE_LINES = 2;
// The shared size is not taken down below this many times the normal size
// for the sake of up to LARGE_OUTLIERS long names: those are set smaller on
// their own instead, so one long name does not undo large text for the rest.
const LARGE_FLOOR = 1.2;
export const LARGE_OUTLIERS = 2;
// What evenly hands the cells while one sticker is built in large text:
// { probe: [] } collects every name's own largest size, { name, twos: [] }
// sets the names at the shared size and collects the pressed-twice sizes,
// { name, two } is the final pass. null outside of it.
let shared = null;

// evenly builds one sticker in large text so that all its names share one
// size: the largest at which every name fits whole. A name that would take
// that size below LARGE_FLOOR times normal (an outlier) does not pull the
// others down, as long as there are no more than LARGE_OUTLIERS of them: it
// is left out of the choice and sets itself as large as it fits on its own,
// never larger than the others, down to normal and cut only where even that
// does not hold it. With more outliers than that the names share the size the
// smallest of them fits, normal size at least. The pressed-twice lines share
// their size the same way. build makes the sticker's pieces and is called up
// to three times.
function evenly(build) {
  if (!large) return build();
  try {
    shared = { probe: [] };
    const first = build();
    const rows = shared.probe;
    const all = rows.filter((r) => r.size !== null && r.size >= r.base - 1e-9);
    const strong = all.filter((r) => r.size >= r.base * LARGE_FLOOR - 1e-9);
    let name;
    if (strong.length && rows.length - strong.length <= LARGE_OUTLIERS) name = Math.min(...strong.map((r) => r.size));
    // Too many long names: all come down to the smallest, a name that does not
    // fit whole even at normal size to normal size.
    else if (rows.length) name = Math.min(...rows.map((r) => (r.size !== null && r.size >= r.base - 1e-9 ? r.size : r.base)));
    else return first;
    shared = { name, twos: [] };
    const second = build();
    if (!shared.twos.length) return second;
    shared = { name, two: Math.min(...shared.twos) };
    return build();
  } finally {
    shared = null;
  }
}

// ---------- one cell: number, tile, name, and the pressed-twice entry ----------

const letterOf = (name) => (Array.from(String(name || '').trim())[0] || '').toUpperCase();

function addTile(piece, key, x, y, size) {
  piece.tiles.push({
    x, y, size, r: size / 4,
    logo: (key && key.logo) || null,
    // Without an image a playlist shows the list symbol, a station its first
    // letter, an empty key nothing.
    icon: !!key && !!key.name && key.kind === 'playlist',
    letter: key && key.name ? letterOf(key.name) : '',
  });
}

function addNumber(piece, n, x, cy, size, pal, measure) {
  const w = measure(String(n), { size, weight: 700 });
  piece.texts.push({ text: String(n), x, y: cy, size, weight: 700, color: pal.mute, align: 'left', maxW: w });
  return w;
}

// addTwice is the line for the key pressed twice: the marker, the list symbol
// for a playlist, the name on one line.
function addTwice(piece, twice, x, cy, w, size, pal, measure) {
  const marker = '2×';
  const mw = measure(marker, { size, weight: 700 });
  piece.texts.push({ text: marker, x, y: cy, size, weight: 700, color: pal.ink, align: 'left', maxW: mw });
  let at = x + mw + u(4);
  if (twice.kind === 'playlist') {
    piece.icons.push({ x: at, y: cy - size / 2, size, color: pal.ink });
    at += size + u(4);
  }
  const maxW = Math.max(0, x + w - at);
  // One step smaller before the name is cut: this line has the least room.
  const fits = measure(twice.name, { size, weight: 600 }) <= loose(maxW);
  const font = { size: fits ? size : size * 0.9, weight: 600 };
  piece.texts.push({ text: ellipsise(twice.name, loose(maxW), font, measure), x: at, y: cy, size: font.size, weight: 600, color: pal.ink, align: 'left', maxW });
}

function addNameLines(piece, fit, x, top, maxW, pal) {
  const lh = fit.size * NAME_LH;
  fit.lines.forEach((text, i) => {
    piece.texts.push({ text, x, y: top + (i + 0.5) * lh, size: fit.size, weight: 700, color: pal.ink, align: 'left', maxW });
  });
}

// rowCell: number, tile and text side by side, the pressed-twice line under
// the name. box is the cell without its padding; s the sizes of its parts.
// box.room, where given, is the height from box.y that stays clear of a
// curved edge over the whole width of the cell: large text keeps inside it.
function rowCell(piece, key, box, s, pal, measure) {
  const cy = box.y + box.h / 2;
  const numW = addNumber(piece, key.n, box.x, cy, s.num, pal, measure);
  const tileX = box.x + numW + u(8);
  addTile(piece, key, tileX, cy - s.tile / 2, s.tile);
  const textX = tileX + s.tile + u(8);
  const textW = Math.max(0, box.x + box.w - textX);
  let fit = key.name ? fitName(key.name, textW, s.name, measure) : { lines: [], size: s.name };
  let two = s.two;
  let textCy = cy;
  if (large && key.name) {
    const room = Math.min(box.h, box.room === undefined ? box.h : box.room);
    const big = largeName(key, textW, textW, s, LARGE_LINES, (t) => room - (t ? t * 1.1 + u(3) : 0), measure);
    if (big) ({ fit, two } = big);
    textCy = box.y + room / 2;
  }
  const nameH = fit.lines.length * fit.size * NAME_LH;
  const twoH = key.twice ? two * 1.1 : 0;
  const gap = nameH && twoH ? u(3) : 0;
  const top = textCy - (nameH + gap + twoH) / 2;
  addNameLines(piece, fit, textX, top, textW, pal);
  if (key.twice) addTwice(piece, key.twice, textX, top + nameH + gap + twoH / 2, textW, two, pal, measure);
}

// stackCell: number, tile and name in one line, the pressed-twice line across
// the whole cell below. For the Portable's narrow cells.
// In large text the name may grow past the head line: the head then grows
// with it, downwards, and the number and tile stay where they are, so they
// still line up with those of the other cells.
function stackCell(piece, key, box, s, pal, measure) {
  const head0 = u(42);
  const cy = box.y + head0 / 2;
  const numW = addNumber(piece, key.n, box.x, cy, s.num, pal, measure);
  const tileX = box.x + numW + u(6);
  addTile(piece, key, tileX, cy - s.tile / 2, s.tile);
  const textX = tileX + s.tile + u(6);
  const textW = Math.max(0, box.x + box.w - textX);
  let fit = key.name ? fitName(key.name, textW, s.name, measure) : { lines: [], size: s.name };
  let two = s.two;
  if (large && key.name) {
    const twiceH = (t) => (t ? u(5) + t * 1.1 : 0);
    // The pressed-twice line must still fit under the head at its normal height.
    const twoCap = (box.h - head0 - u(5)) / 1.1;
    const big = largeName(key, textW, box.w, s, LARGE_LINES, (t) => box.h - twiceH(t), measure, twoCap);
    if (big) ({ fit, two } = big);
  }
  const nameH = fit.lines.length * fit.size * NAME_LH;
  const headH = Math.max(head0, nameH);
  addNameLines(piece, fit, textX, box.y + (headH - nameH) / 2, textW, pal);
  if (key.twice) addTwice(piece, key.twice, box.x, box.y + headH + u(5) + (two * 1.1) / 2, box.w, two, pal, measure);
}

// tallCell is the ST30's cell: twice as tall as the ST20's but no wider. The
// number and the tile stand in a head line and the name runs under them
// across the whole cell, with the pressed-twice line below it: beside a tile
// this large a name would have less width than on the ST20, and long words
// would come out smaller there. The name is set as large as s.name and
// shrinks in small steps to s.floor (the ST20's size) before it is broken or
// cut; the pressed-twice line keeps its proportion to the name.
function tallCell(piece, key, box, s, pal, measure) {
  const numW = addNumber(piece, key.n, box.x, box.y + s.tile / 2, s.num, pal, measure);
  addTile(piece, key, box.x + numW + u(10), box.y, s.tile);
  const areaY = box.y + s.tile + u(8);
  const areaH = box.h - s.tile - u(8);
  const ratio = s.two / s.name;
  const twiceH = (t) => (t ? t * 1.1 + u(4) : 0);
  let fit = { lines: [], size: s.name };
  let two = s.two;
  if (key.name) {
    const twoAt = (z) => (key.twice ? Math.min(s.two, z * ratio) : 0);
    fit = largestWhole(String(key.name), box.w, sizesDown(s.name, s.floor), 3, (z) => areaH - twiceH(twoAt(z)), measure)
      || fitName(key.name, box.w, s.floor, measure);
    two = Math.min(s.two, fit.size * ratio);
    if (large) {
      // Searched down to s.floor, the size normal text shrinks to before it
      // breaks a name: the names of the sticker share one size, and a short
      // name must be able to come down to the size a longer one needs. Up to
      // three lines, like normal text on this tall cell.
      const big = largeName(key, box.w, box.w, { name: s.floor, two: s.floor * ratio }, 3, (t) => areaH - twiceH(t), measure, Infinity, s.floor, { name: fit.size, two });
      if (big) ({ fit, two } = big);
    }
  }
  const nameH = fit.lines.length * fit.size * NAME_LH;
  const twoH = key.twice ? twiceH(two) : 0;
  const top = areaY + (areaH - nameH - twoH) / 2;
  addNameLines(piece, fit, box.x, top, box.w, pal);
  if (key.twice) addTwice(piece, key.twice, box.x, top + nameH + twoH - (two * 1.1) / 2, box.w, two, pal, measure);
}

// ---------- pieces ----------

// outline is the cut line of a piece: a rectangle, or with top/bot (functions
// of x in drawing units) an edge that follows the housing.
function outline(wPx, hPx, top, bot) {
  if (!top && !bot) return [[0, 0], [u(wPx), 0], [u(wPx), u(hPx)], [0, u(hPx)]];
  const xs = [];
  for (let x = 0; x < wPx; x += 12) xs.push(x);
  xs.push(wPx);
  return [
    ...xs.map((x) => [u(x), u(top ? Math.max(0, top(x)) : 0)]),
    ...xs.slice().reverse().map((x) => [u(x), u(bot ? Math.min(hPx, bot(x)) : hPx)]),
  ];
}

function newPiece(speaker, part, wPx, hPx, pal, top, bot) {
  return {
    speaker, part, x: 0, y: 0, w: u(wPx), h: u(hPx), rot: 0, pal,
    outline: outline(wPx, hPx, top, bot), curved: !!(top || bot),
    rules: [], tiles: [], texts: [], icons: [], qr: null, brand: null, remote: null,
  };
}

const keyAt = (sp, n) => (sp.keys || []).find((k) => k && k.n === n) || { n, name: '', kind: 'station', logo: null, twice: null };

// columnName is the name under the code or the mark: one line at size, or
// as far down as min where that keeps it whole, cut at min if it does not
// fit; in large text the largest size, up to two lines, that the room under
// the picture holds whole, with a little air to the column's edges.
function columnName(text, maxW, size, room, measure, min = size) {
  const name = String(text || '');
  const whole = sizesDown(size, min).find((z) => measure(name, { size: z, weight: 700 }) <= loose(maxW));
  const normal = whole ? { lines: [name], size: whole } : { lines: [ellipsise(name, loose(maxW), { size: min, weight: 700 }, measure)], size: min };
  if (!large || !name) return normal;
  const usable = room - u(6);
  const big = largestWhole(name, maxW, sizesDown(usable, normal.size), 2, (z) => usable + z * (NAME_LH - 1), measure);
  return big || normal;
}

// stackUnder places a picture with a name under it, centred in a column, and
// returns where the picture goes. The column is x, y, w, h; pic the edge of
// the square picture.
function stackUnder(piece, text, x, y, w, h, pic, namePx, pal, measure, minPx = namePx) {
  const maxW = w - u(12);
  const fit = columnName(text, maxW, u(namePx), h - pic - u(6), measure, u(minPx));
  const lh = fit.size * NAME_LH;
  const nameH = fit.size + (fit.lines.length - 1) * lh;
  const top = y + (h - (pic + u(6) + nameH)) / 2;
  fit.lines.forEach((line, i) => {
    piece.texts.push({
      text: line, x: x + w / 2, y: top + pic + u(6) + fit.size / 2 + i * lh, size: fit.size, weight: 700, color: pal.ink, align: 'center', maxW,
    });
  });
  return { x: x + (w - pic) / 2, y: top };
}

// addQr places the code with the speaker's name under it, centred in a column.
function addQr(piece, sp, x, y, w, h, qrPx, namePx, pal, measure, minPx = namePx) {
  const qr = u(qrPx);
  const at = stackUnder(piece, sp.name, x, y, w, h, qr, namePx, pal, measure, minPx);
  piece.qr = { x: at.x, y: at.y, size: qr, r: u(4), src: sp.qr || null };
}

function qrPiece(sp, idx, wPx, hPx, qrPx, namePx, pal, measure) {
  const p = newPiece(idx, 'qr', wPx, hPx, pal);
  addQr(p, sp, 0, 0, p.w, p.h, qrPx, namePx, pal, measure);
  return p;
}

// st10Arc is one of the ST10's two arcs: three keys in a row. The back arc
// (keys 1 2 3) is curved on top, the front arc (4 5 6) at the bottom, and the
// cells keep clear of the curve.
function st10Arc(sp, idx, back, pal, measure) {
  const W = 720;
  const H = back ? 105 : 102;
  const bot = (x) => H - 33 * ((x - 360) / 360) ** 2;
  const p = newPiece(idx, back ? 'back' : 'front', W, H, pal,
    back ? (x) => 27 * ((x - 360) / 360) ** 2 : null,
    back ? null : bot);
  const pads = [[26, 10], [12, 10], [12, 26]];
  // The three cells share what is left after their paddings and the two rules.
  const inner = (W - pads.reduce((a, [l, r]) => a + l + r, 0) - 2 * 1.5) / 3;
  const s = { num: u(20), tile: u(46), name: u(20), two: u(19) };
  let x = 0;
  pads.forEach(([l, r], i) => {
    // Top and bottom padding: 24 and 6 on the back arc, 6 and 26 on the front.
    const box = { x: u(x + l), y: u(back ? 24 : 6), w: u(inner), h: u(back ? H - 30 : H - 32) };
    // The front arc's curve rises into the bottom of its outer cells; large
    // text stays above it. (The back arc's cells start below its curve.)
    if (!back) box.room = u(Math.min(bot(x + l), bot(x + l + inner)) - 6);
    rowCell(p, keyAt(sp, (back ? 1 : 4) + i), box, s, pal, measure);
    x += l + inner + r;
    if (i < 2) {
      p.rules.push({ x1: u(x + 0.75), y1: 0, x2: u(x + 0.75), y2: p.h });
      x += 1.5;
    }
  });
  return p;
}

// BRAND is the project's name as it stands under its mark. A name, so it is
// the same in every language, like the speaker's own name under the code.
const BRAND = 'ST Reborn';

// addBrand places the ST Reborn mark with the name under it, centred in a
// column, in the same frame addQr gives the code: the two columns of the wide
// sticker then mirror each other.
function addBrand(piece, x, y, w, h, markPx, namePx, pal, measure, minPx = namePx) {
  const mark = u(markPx);
  const at = stackUnder(piece, BRAND, x, y, w, h, mark, namePx, pal, measure, minPx);
  piece.brand = { x: at.x, y: at.y, size: mark, color: pal.ink };
}

// The two heights of the wide sticker. The ST30's is the ST20's twice as
// tall (175 x 67 mm), for a speaker whose front leaves the room: its keys
// get tallCell and parts about 1.8 times the ST20's, its code and mark fill
// the width of their columns.
const WIDE = {
  st20: { H: 202, cell: rowCell, s: { num: u(22), tile: u(56), name: u(22), two: u(19) }, pic: 112, namePx: 17, minPx: 17 },
  st30: { H: 404, cell: tallCell, s: { num: u(40), tile: u(84), name: u(40), two: u(34), floor: u(22) }, pic: 136, namePx: 30, minPx: 17 },
};

// wideSticker is the ST20 and ST30 sticker: the six keys in two rows and the
// code with the name in a column on the right. Both have the top edge that
// follows the housing, and a column on the left as wide as the code column,
// so the keys sit in the middle and the curve is the same on both sides
// (175 mm wide). That left column carries the ST Reborn mark. Every other
// model ('plain') gets the ST20's without the curve and the left column, a
// 150 x 34 mm rectangle.
function wideSticker(sp, idx, kind, pal, measure) {
  const spec = kind === 'st30' ? WIDE.st30 : WIDE.st20;
  const curved = kind !== 'plain';
  const { H, s } = spec;
  const GRID_W = 750, TOP = 24, QR_W = 150;
  const OFF = curved ? QR_W : 0;
  const W = OFF + GRID_W + QR_W;
  const p = newPiece(idx, 'main', W, H, pal, curved ? (x) => 1.47e-4 * (x - W / 2) ** 2 : null, null);
  const cw = GRID_W / 3;
  const ch = (H - TOP) / 2;
  for (let i = 0; i < 6; i++) {
    const col = i % 3, row = Math.floor(i / 3);
    const x = OFF + col * cw, y = TOP + row * ch;
    const ruleR = col < 2 ? 1.5 : 0;
    spec.cell(p, keyAt(sp, i + 1), { x: u(x + 12), y: u(y), w: u(cw - 22 - ruleR), h: u(ch - (row === 0 ? 1.5 : 0)) }, s, pal, measure);
    if (col < 2) p.rules.push({ x1: u(x + cw - 0.75), y1: u(y), x2: u(x + cw - 0.75), y2: u(y + ch) });
  }
  p.rules.push({ x1: u(OFF), y1: u(TOP + ch - 0.75), x2: u(OFF + GRID_W), y2: u(TOP + ch - 0.75) });
  if (OFF) p.rules.push({ x1: u(OFF - 0.75), y1: 0, x2: u(OFF - 0.75), y2: p.h });
  p.rules.push({ x1: u(OFF + GRID_W + 0.75), y1: 0, x2: u(OFF + GRID_W + 0.75), y2: p.h });
  addQr(p, sp, u(OFF + GRID_W + 1.5), u(40), u(QR_W - 1.5), u(H - 40), spec.pic, spec.namePx, pal, measure, spec.minPx);
  if (OFF) addBrand(p, 0, u(40), u(OFF - 1.5), u(H - 40), spec.pic, spec.namePx, pal, measure, spec.minPx);
  return p;
}

// portableGrid is the Portable's key grid, 80 x 51 mm, curved on top and at
// the bottom like the housing edge it sits against.
function portableGrid(sp, idx, pal, measure) {
  const W = 480, H = 309, TOP = 50, BOTTOM = 46;
  const p = newPiece(idx, 'grid', W, H, pal,
    (x) => 1.12e-4 * (x - 681) ** 2 - 5,
    (x) => 313 - 1e-4 * (x - 681) ** 2);
  const cw = W / 3;
  const ch = (H - TOP - BOTTOM) / 2;
  const s = { num: u(18), tile: u(36), name: u(19), two: u(17) };
  for (let i = 0; i < 6; i++) {
    const col = i % 3, row = Math.floor(i / 3);
    const x = col * cw, y = TOP + row * ch;
    const ruleR = col < 2 ? 1.5 : 0;
    stackCell(p, keyAt(sp, i + 1), { x: u(x + 8), y: u(y + 14), w: u(cw - 14 - ruleR), h: u(ch - 14) }, s, pal, measure);
    if (col < 2) p.rules.push({ x1: u(x + cw - 0.75), y1: u(y), x2: u(x + cw - 0.75), y2: u(y + ch) });
  }
  p.rules.push({ x1: 0, y1: u(TOP + ch - 0.75), x2: p.w, y2: u(TOP + ch - 0.75) });
  return p;
}

// ---------- the remote sticker ----------

// The sticker for the back of the remote: a plain 40 x 75 mm rectangle that
// fits the remote of every model, so every speaker gets the same one. A
// header with a small remote symbol, the word for remote and the speaker's
// name says what it is for; below it the six keys, each with what it plays
// pressed once and pressed twice. It holds no logos: on a remote held in the
// hand, every millimetre goes to the names.
export const REMOTE = { w: 40, h: 75 };
const R_PAD = 2;
// The header band, and where the keys start under its rule.
const R_HEAD = 10;
const R_RULE_Y = R_PAD + R_HEAD + 0.5;
const R_BODY_Y = R_PAD + R_HEAD + 1;
// The free height above and below a key's text, and the space after its
// number.
const R_VPAD = 0.6;
const R_GAP_X = 1.2;
const R_LH = 1.08;
// Between a name and the pressed-twice line under it, in name sizes.
const R_GAP_Y = 0.12;
// The pressed-twice line is set this much smaller than the name above it,
// and the number of a key this much larger.
const R_TWICE = 0.88;
const R_NUM = 1.45;
// The name sizes tried, largest first, in mm: 6 is as large as a name gets,
// 2.2 the smallest that still reads well on a remote in the hand.
export const REMOTE_SIZE_MAX = 6;
export const REMOTE_SIZE_MIN = 2.2;
const R_STEP = 0.1;
// One name may be drawn this much smaller than the others before the size
// of all of them goes down.
const R_SCALES = [1, 0.9, 0.8];
const R_MAX_LINES = 3;
const R_COL_GAP = 2;
// The two ways the keys can stand: one column of six, or two columns of
// three. One column reads like a list from 1 to 6 and is the default; two
// columns are used only where they allow names this much larger.
export const REMOTE_GRIDS = [{ cols: 1, rows: 6 }, { cols: 2, rows: 3 }];
export const REMOTE_TWO_COLUMNS_GAIN = 1.1;
const MARKER = '2×';

// Scripts written without spaces may break between any two characters;
// everything else breaks only between words.
const ANY_BREAK = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}\p{Script=Thai}]/u;

// wholeLines sets text on at most maxLines lines of maxW, or answers null
// when that takes more lines or would break inside a word.
function wholeLines(text, maxW, font, measure, maxLines) {
  const r = wrapText(text, maxW, font, measure, maxLines);
  if (r.truncated) return null;
  if (!ANY_BREAK.test(text) && tokens(text).some((k) => measure(k.t, font) > maxW)) return null;
  return r.lines;
}

// fitRemoteName sets a name at the largest allowed scale at which it fits its
// height whole, one line preferred to two: { lines, size, ok }. A name that
// fits nowhere is cut at the smallest scale and comes back with ok false.
function fitRemoteName(text, maxW, size, availH, measure) {
  const linesAt = (sc) => Math.min(R_MAX_LINES, Math.floor(availH / (size * sc * R_LH) + 1e-9));
  const tries = [[1, 1], [1, 0.9], ...R_SCALES.map((sc) => [R_MAX_LINES, sc])];
  for (const [most, sc] of tries) {
    const n = Math.min(most, linesAt(sc));
    if (n < 1) continue;
    const font = { size: size * sc, weight: 700 };
    for (const w of [maxW, loose(maxW)]) {
      const lines = wholeLines(text, w, font, measure, n);
      if (lines) return { lines, size: font.size, ok: true };
    }
  }
  const sc = R_SCALES[R_SCALES.length - 1];
  const r = wrapText(text, loose(maxW), { size: size * sc, weight: 700 }, measure, Math.max(1, linesAt(sc)));
  return { lines: r.lines, size: size * sc, ok: false };
}

// remoteCells are the boxes of the six keys in one grid, in key order.
function remoteCells(grid) {
  const w = REMOTE.w - 2 * R_PAD;
  const h = REMOTE.h - R_PAD - R_BODY_Y;
  const colW = (w - (grid.cols - 1) * R_COL_GAP) / grid.cols;
  const rowH = h / grid.rows;
  const cells = [];
  for (let i = 0; i < grid.cols * grid.rows; i++) {
    const col = i % grid.cols, row = Math.floor(i / grid.cols);
    cells.push({ x: R_PAD + col * (colW + R_COL_GAP), y: R_BODY_Y + row * rowH, w: colW, h: rowH });
  }
  return cells;
}

// planRemoteKey sets one key at name size s in its box. ok is false when a
// name had to be cut or the key's lines are taller than the box.
function planRemoteKey(key, box, s, numW, measure) {
  const textW = Math.max(0, box.w - numW - R_GAP_X);
  const availH = box.h - 2 * R_VPAD;
  const s2 = s * R_TWICE;
  let ok = true;
  let twice = null;
  if (key.twice && key.twice.name) {
    const markerW = measure(MARKER, { size: s2, weight: 700 });
    const at = markerW + s2 * 0.35;
    const w2 = Math.max(0, textW - at);
    const name = String(key.twice.name);
    const sc = R_SCALES.find((k) => measure(name, { size: s2 * k, weight: 600 }) <= loose(w2));
    const size = s2 * (sc || R_SCALES[R_SCALES.length - 1]);
    if (!sc) ok = false;
    twice = { markerW, at, w2, size, text: sc ? name : ellipsise(name, loose(w2), { size, weight: 600 }, measure) };
  }
  const twiceH = twice ? s2 * R_LH : 0;
  const gap = twice && key.name ? s * R_GAP_Y : 0;
  let once = null;
  if (key.name) {
    // Size s is what the names are set in: one line of it must fit the
    // height. The smaller scales are for a name that is too wide.
    if (s * R_LH + gap + twiceH > availH + 1e-9) ok = false;
    once = fitRemoteName(key.name, textW, s, availH - twiceH - gap, measure);
    if (!once.ok) ok = false;
    // A name drawn smaller to fit keeps the line under it no larger.
    if (twice && twice.size > once.size) twice.size = once.size;
  }
  const onceH = once ? once.lines.length * once.size * R_LH : 0;
  if (onceH + gap + twiceH > availH + 1e-9) ok = false;
  return { ok, once, twice, textW, onceH, gap, twiceH, s2 };
}

// planRemote finds, for one grid, the largest name size at which every key
// fits whole: { size, numSize, numW, cells, keys, ok }. ok is false when even
// the smallest size needs a name cut. from is the first size tried (for the
// tests: that the size found is the largest that fits).
export function planRemote(keys, grid, measure, from = REMOTE_SIZE_MAX) {
  const cells = remoteCells(grid);
  const steps = Math.max(0, Math.round((from - REMOTE_SIZE_MIN) / R_STEP));
  let plan = null;
  for (let i = 0; i <= steps; i++) {
    const s = Math.round((from - i * R_STEP) * 100) / 100;
    // One number size and one text column for all keys, so the names line up.
    const numSize = Math.min(s * R_NUM, cells[0].h - 2 * R_VPAD);
    const numW = Math.max(...[1, 2, 3, 4, 5, 6].map((n) => measure(String(n), { size: numSize, weight: 700 })));
    const planned = keys.map((k, j) => planRemoteKey(k, cells[j], s, numW, measure));
    plan = { size: s, numSize, numW, cells, keys: planned, ok: planned.every((p) => p.ok) };
    if (plan.ok) break;
  }
  return plan;
}

// chooseRemoteGrid measures both grids for the six keys and keeps one column
// unless two columns allow clearly larger names, or are the only way to fit
// them whole. Answers { grid, plan }.
export function chooseRemoteGrid(keys, measure) {
  const [one, two] = REMOTE_GRIDS.map((grid) => ({ grid, plan: planRemote(keys, grid, measure) }));
  const twoWins = two.plan.ok && (!one.plan.ok || two.plan.size >= one.plan.size * REMOTE_TWO_COLUMNS_GAIN);
  return twoWins ? two : one;
}

// fitHeaderName sets the speaker's name in the header: one line, smaller
// before two lines, cut only when two small lines do not hold it either.
function fitHeaderName(name, maxW, measure) {
  for (const size of [5.2, 4.8, 4.4, 4, 3.6, 3.2]) {
    if (measure(name, { size, weight: 700 }) <= loose(maxW)) return { lines: [name], size };
  }
  for (const w of [maxW, loose(maxW)]) {
    const two = wholeLines(name, w, { size: 2.7, weight: 700 }, measure, 2);
    if (two) return { lines: two, size: 2.7 };
  }
  return { lines: [ellipsise(name, loose(maxW), { size: 3.2, weight: 700 }, measure)], size: 3.2 };
}

// remoteSticker builds the remote sticker of one speaker. label is the word
// for remote in the user's language. Its colour follows the housing choice,
// in the shades every model without its own shade gets.
function remoteSticker(sp, idx, label, measure) {
  const pal = stickerPalette('plain', sp.housing);
  const p = newPiece(idx, 'remote', REMOTE.w * 6, REMOTE.h * 6, pal);
  // The header: the symbol, and the word for remote above the speaker's name.
  const iconH = R_HEAD - 0.8;
  p.remote = { x: R_PAD, y: R_PAD + 0.4, w: iconH / 2, h: iconH, color: pal.ink };
  const hx = R_PAD + iconH / 2 + 1.6;
  const hw = REMOTE.w - R_PAD - hx;
  const word = String(label || '').trim();
  if (word) {
    const sc = [1, 0.9, 0.8, 0.7].find((k) => measure(word, { size: 2.6 * k, weight: 700 }) <= loose(hw)) || 0.7;
    const size = 2.6 * sc;
    p.texts.push({ text: ellipsise(word, loose(hw), { size, weight: 700 }, measure), x: hx, y: R_PAD + 0.4 + 1.3, size, weight: 700, color: pal.mute, align: 'left', maxW: hw });
  }
  const head = fitHeaderName(String(sp.name || ''), hw, measure);
  const nameTop = R_PAD + 0.4 + 3.2;
  const nameH = R_HEAD - 0.4 - 3.2;
  const lh = head.size * R_LH;
  head.lines.forEach((text, i) => {
    p.texts.push({ text, x: hx, y: nameTop + (nameH - head.lines.length * lh) / 2 + (i + 0.5) * lh, size: head.size, weight: 700, color: pal.ink, align: 'left', maxW: hw });
  });
  p.rules.push({ x1: 0, y1: R_RULE_Y, x2: p.w, y2: R_RULE_Y });

  // The keys, in the grid that lets their names be largest.
  const keys = [1, 2, 3, 4, 5, 6].map((n) => keyAt(sp, n));
  const { grid, plan } = chooseRemoteGrid(keys, measure);
  p.grid = { cols: grid.cols, rows: grid.rows, size: plan.size, fits: plan.ok };
  plan.cells.forEach((box, j) => {
    const pl = plan.keys[j];
    const cy = box.y + box.h / 2;
    p.texts.push({ text: String(keys[j].n), x: box.x, y: cy, size: plan.numSize, weight: 700, color: pal.ink, align: 'left', maxW: plan.numW });
    const tx = box.x + plan.numW + R_GAP_X;
    let y = cy - (pl.onceH + pl.gap + pl.twiceH) / 2;
    if (pl.once) {
      const l = pl.once.size * R_LH;
      pl.once.lines.forEach((text, i) => {
        p.texts.push({ text, x: tx, y: y + (i + 0.5) * l, size: pl.once.size, weight: 700, color: pal.ink, align: 'left', maxW: pl.textW });
      });
      y += pl.onceH + pl.gap;
    }
    if (pl.twice) {
      const ty = y + pl.twiceH / 2;
      p.texts.push({ text: MARKER, x: tx, y: ty, size: pl.s2, weight: 700, color: pal.mute, align: 'left', maxW: pl.twice.markerW });
      p.texts.push({ text: pl.twice.text, x: tx + pl.twice.at, y: ty, size: pl.twice.size, weight: 600, color: pal.ink, align: 'left', maxW: pl.twice.w2 });
    }
  });
  // Rules between the rows, and between the columns.
  const rowH = plan.cells[0].h;
  for (let r = 1; r < grid.rows; r++) {
    const y = R_BODY_Y + r * rowH;
    p.rules.push({ x1: 0, y1: y, x2: p.w, y2: y });
  }
  for (let c = 1; c < grid.cols; c++) {
    const x = plan.cells[c].x - R_COL_GAP / 2;
    p.rules.push({ x1: x, y1: R_RULE_Y, x2: x, y2: p.h });
  }
  return p;
}

// ---------- groups: the pieces of one speaker (or two wide stickers) ----------

const at = (piece, xPx, yPx, rot = 0) => Object.assign(piece, { x: u(xPx), y: u(yPx), rot });

// A group is cut out as one block first. Pieces that share a cut line lie
// edge to edge, and a scissors symbol marks each of those cuts. Two wide
// stickers pair only when they are equally tall, so their straight edges
// match and one cut separates them: an ST30 pairs with an ST30, an ST20 with
// an ST20 or a plain one.
function buildGroups(speakers, measure, remoteLabel) {
  const groups = [];
  // The group waiting for a second wide sticker, by the height of its first.
  const openWide = new Map();
  speakers.forEach((sp, idx) => {
    const kind = stickerKind(sp.model);
    const pal = stickerPalette(kind, sp.housing);
    if (kind === 'st10') {
      groups.push({
        // Both arcs are one sticker: their six names share one size.
        pieces: [...evenly(() => [at(st10Arc(sp, idx, true, pal, measure), 0, 0), at(st10Arc(sp, idx, false, pal, measure), 0, 105)]), at(qrPiece(sp, idx, 118, 140, 98, 16, pal, measure), 720, 27)],
        scissors: [{ x: -5, y: u(105), rot: 0 }, { x: u(720), y: 1.5, rot: 90 }],
      });
    } else if (kind === 'portable') {
      groups.push({
        pieces: [at(evenly(() => portableGrid(sp, idx, pal, measure)), 0, 0), at(qrPiece(sp, idx, 132, 158, 108, 20, pal, measure), 480, 0)],
        scissors: [{ x: u(480), y: u(309) + 5, rot: -90 }],
      });
    } else {
      const piece = evenly(() => wideSticker(sp, idx, kind, pal, measure));
      const open = openWide.get(piece.h);
      if (open) {
        // The second of a pair stands on its head, straight edge to straight
        // edge, so one cut separates the two.
        const edge = open.pieces[0].h;
        open.pieces.push(Object.assign(piece, { x: 0, y: edge, rot: 180 }));
        open.scissors.push({ x: -5, y: edge, rot: 0 });
        openWide.delete(piece.h);
      } else {
        const group = { pieces: [at(piece, 0, 0)], scissors: [] };
        groups.push(group);
        openWide.set(piece.h, group);
      }
    }
  });
  // The remote stickers come after all of them, side by side in rows, each
  // with its own cut line: they belong to different remotes.
  const remotes = speakers.map((sp, idx) => remoteSticker(sp, idx, remoteLabel, measure));
  for (let i = 0; i < remotes.length; i += REMOTES_PER_ROW) {
    groups.push({ pieces: remotes.slice(i, i + REMOTES_PER_ROW).map((p, j) => Object.assign(p, { x: j * (REMOTE.w + REMOTE_GAP), y: 0, rot: 0 })), scissors: [] });
  }
  return groups;
}

// extent is the box a group needs, bleed and scissors included, relative to
// its origin.
function extent(group) {
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (const p of group.pieces) {
    x0 = Math.min(x0, p.x - BLEED); y0 = Math.min(y0, p.y - BLEED);
    x1 = Math.max(x1, p.x + p.w + BLEED); y1 = Math.max(y1, p.y + p.h + BLEED);
  }
  for (const s of group.scissors) {
    x0 = Math.min(x0, s.x - SCISSORS_HALF); y0 = Math.min(y0, s.y - SCISSORS_HALF);
    x1 = Math.max(x1, s.x + SCISSORS_HALF); y1 = Math.max(y1, s.y + SCISSORS_HALF);
  }
  return { x0, y0, x1, y1 };
}

// ---------- the sheet ----------

const HEAD_FONT = { size: 3, weight: 600 };
const HEAD_LINE = 4;
const CAPTION_FONT = { size: 2.8, weight: 400 };
const GROUP_GAP = 5;
// The control line with its caption, measured from the last group's bleed.
const CONTROL_H = 8;
// Every group starts this far in, which leaves room for the scissors left of
// the widest one and lines all groups up on one edge.
const GROUP_X = AREA.x + SCISSORS_HALF + 5;
const INK = '#111111';
// The space between two remote stickers in a row (their bleeds and a cut's
// width), and how many of them a row holds.
const REMOTE_GAP = 2 * BLEED + 3;
const REMOTES_PER_ROW = Math.floor((AREA.x + AREA.w - BLEED - GROUP_X + REMOTE_GAP) / (REMOTE.w + REMOTE_GAP));

// ---------- the home screen steps ----------

// The steps from the app's "Put it on the phone's home screen" card, in two
// columns: iPhone left, Android right. Plain text in the sheet's ink.
const GUIDE_TITLE_FONT = HEAD_FONT;
const GUIDE_HEAD_FONT = { size: 2.8, weight: 700 };
const GUIDE_STEP_FONT = CAPTION_FONT;
const GUIDE_NOTE_FONT = { size: 2.5, weight: 400 };
// Line height as a share of the font size.
const GUIDE_LH = 1.3;
const GUIDE_COL_GAP = 8;
const GUIDE_COL_W = (AREA.w - GUIDE_COL_GAP) / 2;
// The width the step number takes before its text.
const GUIDE_NUM_W = 4;
const GUIDE_STEP_LINES = 4;
const GUIDE_NOTE_LINES = 3;
// From the control line to the top of the block: past the caption under the
// line and a clear gap.
export const GUIDE_TOP = 10;

const ENTITIES = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", '#39': "'", nbsp: ' ' };

// plainText drops the markup a translated string may carry (<b>, **), so a
// tag is never printed as text.
export function plainText(s) {
  return String(s || '')
    .replace(/<[^>]*>/g, '')
    .replace(/\*\*/g, '')
    .replace(/&(amp|lt|gt|quot|apos|#39|nbsp);/g, (_, e) => ENTITIES[e])
    .replace(/\s+/g, ' ')
    .trim();
}

// guideColumn sets one column's heading, numbered steps and note, with y
// measured from the top of the block. Returns the lines and the height.
function guideColumn(title, steps, note, x, measure) {
  const items = [];
  let y = 0;
  const lines = (text, font, lx, w, max) => {
    const lh = font.size * GUIDE_LH;
    const r = wrapText(text, w, font, measure, max).lines;
    r.forEach((line, i) => items.push({ text: line, x: lx, y: y + (i + 0.5) * lh, size: font.size, weight: font.weight, color: INK, align: 'left', maxW: w }));
    return r.length * lh;
  };
  const head = plainText(title);
  if (head) y += lines(head, GUIDE_HEAD_FONT, x, GUIDE_COL_W, 2) + 1;
  let n = 0;
  for (const raw of Array.isArray(steps) ? steps : []) {
    const step = plainText(raw);
    if (!step) continue;
    n += 1;
    const lh = GUIDE_STEP_FONT.size * GUIDE_LH;
    items.push({ text: `${n}.`, x, y: y + lh / 2, size: GUIDE_STEP_FONT.size, weight: 700, color: INK, align: 'left', maxW: GUIDE_NUM_W });
    y += lines(step, GUIDE_STEP_FONT, x + GUIDE_NUM_W, GUIDE_COL_W - GUIDE_NUM_W, GUIDE_STEP_LINES) + 0.6;
  }
  const foot = plainText(note);
  if (foot) y += 0.8 + lines(foot, GUIDE_NOTE_FONT, x, GUIDE_COL_W, GUIDE_NOTE_LINES);
  return { items, h: items.length ? y : 0 };
}

// homeScreenBlock lays out texts.homeScreen = { title, iosTitle, ios: [..],
// iosNote, androidTitle, android: [..], androidNote } as a block the width of
// the area. Returns { h, at(top) } where at gives the lines placed at top, or
// null when there is nothing to show.
export function homeScreenBlock(hs, measure) {
  if (!hs || typeof hs !== 'object') return null;
  const items = [];
  let y = 0;
  const title = plainText(hs.title);
  if (title) {
    const lh = GUIDE_TITLE_FONT.size * GUIDE_LH;
    const r = wrapText(title, AREA.w, GUIDE_TITLE_FONT, measure, 2).lines;
    r.forEach((line, i) => items.push({ text: line, x: AREA.x, y: (i + 0.5) * lh, size: GUIDE_TITLE_FONT.size, weight: GUIDE_TITLE_FONT.weight, color: INK, align: 'left', maxW: AREA.w }));
    y = r.length * lh + 1.5;
  }
  const ios = guideColumn(hs.iosTitle, hs.ios, hs.iosNote, AREA.x, measure);
  const android = guideColumn(hs.androidTitle, hs.android, hs.androidNote, AREA.x + GUIDE_COL_W + GUIDE_COL_GAP, measure);
  if (!ios.items.length && !android.items.length) return null;
  for (const it of [...ios.items, ...android.items]) items.push({ ...it, y: it.y + y });
  const h = y + Math.max(ios.h, android.h);
  return { h, at: (top) => items.map((it) => ({ ...it, y: it.y + top })) };
}

// layoutStickers places every speaker's pieces on A4 pages. texts carries the
// translated words: the two lines of the sheet and the header of the remote
// sticker (the speakers' stickers hold no words): { instruction, control,
// remote, homeScreen }. Every speaker also gets its remote sticker. The pieces
// of one speaker's own sticker never go on two pages; the remote stickers
// follow them in rows of their own and carry the speaker's name. large: true
// sets every name as large as its box allows (Large text in the dialog).
// homeScreen, when given, adds the steps that put the phone remote on a home
// screen (see homeScreenBlock): under the last page's control line when they
// fit there, else on one more page of their own.
export function layoutStickers(speakers, { measure, texts = {}, squeeze: by = SQUEEZE, large: big = false } = {}) {
  squeeze = by > 0 && by <= 1 ? by : SQUEEZE;
  large = !!big;
  const groups = buildGroups(speakers || [], measure, texts.remote);
  const head = wrapText(texts.instruction || '', AREA.w, HEAD_FONT, measure, 4).lines;
  const header = head.map((text, i) => ({
    text, x: AREA.x, y: AREA.y + (i + 0.5) * HEAD_LINE, size: HEAD_FONT.size, weight: HEAD_FONT.weight, color: INK, align: 'left', maxW: AREA.w,
  }));
  const top = AREA.y + head.length * HEAD_LINE + (head.length ? 3 : 0);
  const bottom = AREA.y + AREA.h;

  const pages = [];
  let page = null;
  let y = 0;
  const close = () => {
    if (!page) return;
    const line = y - GROUP_GAP + 3;
    page.control = {
      x: AREA.x, y: line, length: CONTROL_LENGTH,
      caption: { text: ellipsise(texts.control || '', AREA.w, CAPTION_FONT, measure), x: AREA.x, y: line + 3.2, size: CAPTION_FONT.size, weight: CAPTION_FONT.weight, color: INK, align: 'left', maxW: AREA.w },
    };
    pages.push(page);
    page = null;
  };
  for (const g of groups) {
    const e = extent(g);
    const need = e.y1 - e.y0;
    if (page && y + need + CONTROL_H > bottom) close();
    if (!page) { page = { header, pieces: [], scissors: [], control: null, guide: [] }; y = top; }
    const oy = y - e.y0;
    for (const p of g.pieces) page.pieces.push(Object.assign(p, { x: p.x + GROUP_X, y: p.y + oy }));
    for (const s of g.scissors) page.scissors.push({ x: s.x + GROUP_X, y: s.y + oy, rot: s.rot });
    y += need + GROUP_GAP;
  }
  close();
  const guide = homeScreenBlock(texts.homeScreen, measure);
  if (guide && pages.length) {
    const last = pages[pages.length - 1];
    const below = last.control.y + GUIDE_TOP;
    if (below + guide.h <= bottom) last.guide = guide.at(below);
    else pages.push({ header: [], pieces: [], scissors: [], control: null, guide: guide.at(AREA.y) });
  }
  return { pages };
}
