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
// Output: { pages: [{ header, pieces, scissors, control }] }

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
// sticker of the ST20 and ST30, the Portable's grid, or for everything else
// (Wave, soundbars, a model not known here) the wide sticker without its curve.
export function stickerKind(model) {
  const m = String(model || '').toLowerCase();
  if (/soundtouch\s*portable/.test(m)) return 'portable';
  if (/soundtouch\s*10\b/.test(m)) return 'st10';
  if (/soundtouch\s*(20|30)\b/.test(m)) return 'st20';
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
  const lh = fit.size * 1.05;
  fit.lines.forEach((text, i) => {
    piece.texts.push({ text, x, y: top + (i + 0.5) * lh, size: fit.size, weight: 700, color: pal.ink, align: 'left', maxW });
  });
}

// rowCell: number, tile and text side by side, the pressed-twice line under
// the name. box is the cell without its padding; s the sizes of its parts.
function rowCell(piece, key, box, s, pal, measure) {
  const cy = box.y + box.h / 2;
  const numW = addNumber(piece, key.n, box.x, cy, s.num, pal, measure);
  const tileX = box.x + numW + u(8);
  addTile(piece, key, tileX, cy - s.tile / 2, s.tile);
  const textX = tileX + s.tile + u(8);
  const textW = Math.max(0, box.x + box.w - textX);
  const fit = key.name ? fitName(key.name, textW, s.name, measure) : { lines: [], size: s.name };
  const nameH = fit.lines.length * fit.size * 1.05;
  const twoH = key.twice ? s.two * 1.1 : 0;
  const gap = nameH && twoH ? u(3) : 0;
  const top = cy - (nameH + gap + twoH) / 2;
  addNameLines(piece, fit, textX, top, textW, pal);
  if (key.twice) addTwice(piece, key.twice, textX, top + nameH + gap + twoH / 2, textW, s.two, pal, measure);
}

// stackCell: number, tile and name in one line, the pressed-twice line across
// the whole cell below. For the Portable's narrow cells.
function stackCell(piece, key, box, s, pal, measure) {
  const headH = u(42);
  const cy = box.y + headH / 2;
  const numW = addNumber(piece, key.n, box.x, cy, s.num, pal, measure);
  const tileX = box.x + numW + u(6);
  addTile(piece, key, tileX, cy - s.tile / 2, s.tile);
  const textX = tileX + s.tile + u(6);
  const textW = Math.max(0, box.x + box.w - textX);
  const fit = key.name ? fitName(key.name, textW, s.name, measure) : { lines: [], size: s.name };
  addNameLines(piece, fit, textX, cy - (fit.lines.length * fit.size * 1.05) / 2, textW, pal);
  if (key.twice) addTwice(piece, key.twice, box.x, box.y + headH + u(5) + (s.two * 1.1) / 2, box.w, s.two, pal, measure);
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
    rules: [], tiles: [], texts: [], icons: [], qr: null, brand: null,
  };
}

const keyAt = (sp, n) => (sp.keys || []).find((k) => k && k.n === n) || { n, name: '', kind: 'station', logo: null, twice: null };

// addQr places the code with the speaker's name under it, centred in a column.
function addQr(piece, sp, x, y, w, h, qrPx, namePx, pal, measure) {
  const qr = u(qrPx);
  const size = u(namePx);
  const top = y + (h - (qr + u(6) + size)) / 2;
  piece.qr = { x: x + (w - qr) / 2, y: top, size: qr, r: u(4), src: sp.qr || null };
  const maxW = w - u(12);
  piece.texts.push({
    text: ellipsise(sp.name, loose(maxW), { size, weight: 700 }, measure),
    x: x + w / 2, y: top + qr + u(6) + size / 2, size, weight: 700, color: pal.ink, align: 'center', maxW,
  });
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
  const p = newPiece(idx, back ? 'back' : 'front', W, H, pal,
    back ? (x) => 27 * ((x - 360) / 360) ** 2 : null,
    back ? null : (x) => H - 33 * ((x - 360) / 360) ** 2);
  const pads = [[26, 10], [12, 10], [12, 26]];
  // The three cells share what is left after their paddings and the two rules.
  const inner = (W - pads.reduce((a, [l, r]) => a + l + r, 0) - 2 * 1.5) / 3;
  const s = { num: u(20), tile: u(46), name: u(20), two: u(17) };
  let x = 0;
  pads.forEach(([l, r], i) => {
    // Top and bottom padding: 24 and 6 on the back arc, 6 and 26 on the front.
    const box = { x: u(x + l), y: u(back ? 24 : 6), w: u(inner), h: u(back ? H - 30 : H - 32) };
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
function addBrand(piece, x, y, w, h, markPx, namePx, pal) {
  const mark = u(markPx);
  const size = u(namePx);
  const top = y + (h - (mark + u(6) + size)) / 2;
  piece.brand = { x: x + (w - mark) / 2, y: top, size: mark, color: pal.ink };
  piece.texts.push({
    text: BRAND, x: x + w / 2, y: top + mark + u(6) + size / 2, size, weight: 700, color: pal.ink, align: 'center', maxW: w - u(12),
  });
}

// wideSticker is the ST20 and ST30 sticker: the six keys in two rows and the
// code with the name in a column on the right. curved gives it the top edge
// that follows the housing, and a column on the left as wide as the code
// column, so the keys sit in the middle and the curve is the same on both
// sides (175 x 34 mm). That left column carries the ST Reborn mark. Without
// the curve it is the plain 150 x 34 mm rectangle every other model gets.
function wideSticker(sp, idx, curved, pal, measure) {
  const H = 202, GRID_W = 750, TOP = 24, QR_W = 150;
  const OFF = curved ? QR_W : 0;
  const W = OFF + GRID_W + QR_W;
  const p = newPiece(idx, 'main', W, H, pal, curved ? (x) => 1.47e-4 * (x - W / 2) ** 2 : null, null);
  const cw = GRID_W / 3;
  const ch = (H - TOP) / 2;
  const s = { num: u(22), tile: u(56), name: u(22), two: u(17) };
  for (let i = 0; i < 6; i++) {
    const col = i % 3, row = Math.floor(i / 3);
    const x = OFF + col * cw, y = TOP + row * ch;
    const ruleR = col < 2 ? 1.5 : 0;
    rowCell(p, keyAt(sp, i + 1), { x: u(x + 12), y: u(y), w: u(cw - 22 - ruleR), h: u(ch - (row === 0 ? 1.5 : 0)) }, s, pal, measure);
    if (col < 2) p.rules.push({ x1: u(x + cw - 0.75), y1: u(y), x2: u(x + cw - 0.75), y2: u(y + ch) });
  }
  p.rules.push({ x1: u(OFF), y1: u(TOP + ch - 0.75), x2: u(OFF + GRID_W), y2: u(TOP + ch - 0.75) });
  if (OFF) p.rules.push({ x1: u(OFF - 0.75), y1: 0, x2: u(OFF - 0.75), y2: p.h });
  p.rules.push({ x1: u(OFF + GRID_W + 0.75), y1: 0, x2: u(OFF + GRID_W + 0.75), y2: p.h });
  addQr(p, sp, u(OFF + GRID_W + 1.5), u(40), u(QR_W - 1.5), u(H - 40), 112, 17, pal, measure);
  if (OFF) addBrand(p, 0, u(40), u(OFF - 1.5), u(H - 40), 112, 17, pal);
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
  const s = { num: u(18), tile: u(36), name: u(19), two: u(15) };
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

// ---------- groups: the pieces of one speaker (or two wide stickers) ----------

const at = (piece, xPx, yPx, rot = 0) => Object.assign(piece, { x: u(xPx), y: u(yPx), rot });

// A group is cut out as one block first. Pieces that share a cut line lie
// edge to edge, and a scissors symbol marks each of those cuts.
function buildGroups(speakers, measure) {
  const groups = [];
  let openWide = null;
  speakers.forEach((sp, idx) => {
    const kind = stickerKind(sp.model);
    const pal = stickerPalette(kind, sp.housing);
    if (kind === 'st10') {
      groups.push({
        pieces: [at(st10Arc(sp, idx, true, pal, measure), 0, 0), at(st10Arc(sp, idx, false, pal, measure), 0, 105), at(qrPiece(sp, idx, 118, 140, 98, 16, pal, measure), 720, 27)],
        scissors: [{ x: -5, y: u(105), rot: 0 }, { x: u(720), y: 1.5, rot: 90 }],
      });
    } else if (kind === 'portable') {
      groups.push({
        pieces: [at(portableGrid(sp, idx, pal, measure), 0, 0), at(qrPiece(sp, idx, 132, 158, 108, 20, pal, measure), 480, 0)],
        scissors: [{ x: u(480), y: u(309) + 5, rot: -90 }],
      });
    } else {
      const piece = wideSticker(sp, idx, kind === 'st20', pal, measure);
      if (openWide) {
        // The second of a pair stands on its head, straight edge to straight
        // edge, so one cut separates the two.
        openWide.pieces.push(at(piece, 0, 202, 180));
        openWide.scissors.push({ x: -5, y: u(202), rot: 0 });
        openWide = null;
      } else {
        openWide = { pieces: [at(piece, 0, 0)], scissors: [] };
        groups.push(openWide);
      }
    }
  });
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

// layoutStickers places every speaker's pieces on A4 pages. texts carries the
// two translated lines of the sheet (the stickers themselves hold no words):
// { instruction, control }. A speaker's pieces never go on two pages.
export function layoutStickers(speakers, { measure, texts = {}, squeeze: by = SQUEEZE } = {}) {
  squeeze = by > 0 && by <= 1 ? by : SQUEEZE;
  const groups = buildGroups(speakers || [], measure);
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
    if (!page) { page = { header, pieces: [], scissors: [], control: null }; y = top; }
    const oy = y - e.y0;
    for (const p of g.pieces) page.pieces.push(Object.assign(p, { x: p.x + GROUP_X, y: p.y + oy }));
    for (const s of g.scissors) page.scissors.push({ x: s.x + GROUP_X, y: s.y + oy, rot: s.rot });
    y += need + GROUP_GAP;
  }
  close();
  return { pages };
}
