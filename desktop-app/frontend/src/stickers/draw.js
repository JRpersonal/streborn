// stickers/draw.js: paints one page of the sticker sheet onto a canvas.
//
// The canvas is the reason the sheet is drawn in the window at all: a station
// name can be in any script, and the webview finds a font for each of them,
// which a PDF library with one embedded font does not. The page then goes to
// Go as a picture (SaveStickerPDF).
//
// Everything is drawn in millimetres under one scale, from the layout that
// layout.js made. Images must be data: URIs: one image from another origin
// would taint the canvas, and a tainted canvas cannot be read back.

import { BLEED, PAGE, SQUEEZE, SQUEEZE_CONDENSED } from './layout.js';

// The app's UI font (style.css body): what a script the condensed fonts do
// not cover falls back to, and the whole sheet where there is none of them.
const UI_STACK = "-apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif";
// The stickers were designed with a condensed font, so a condensed system
// font comes first: Bahnschrift on Windows (one variable font, narrowed by
// the stretch keyword), then what macOS and the common Linux desktops have.
export const FONT_STACK = `Bahnschrift, 'Avenir Next Condensed', 'Roboto Condensed', 'DejaVu Sans Condensed', ${UI_STACK}`;
export const PRINT_DPI = 300;

const fontOf = (weight, px) => `condensed ${weight} ${px}px ${FONT_STACK}`;

// hasCondensedFont says whether the stack really draws narrower than the UI
// font here. Asked by measuring: the stretch keyword and the family names
// are accepted everywhere, also where nothing answers to them.
export function hasCondensedFont(doc = document) {
  const ctx = doc.createElement('canvas').getContext('2d');
  const probe = 'Energy Discover 0123';
  ctx.font = `700 100px ${UI_STACK}`;
  const wide = ctx.measureText(probe).width;
  ctx.font = fontOf(700, 100);
  return ctx.measureText(probe).width < wide * 0.93;
}

// squeezeFor is the squeeze layout.js lays out with: a little with a
// condensed font, the full one without.
export function squeezeFor(doc = document) {
  return hasCondensedFont(doc) ? SQUEEZE_CONDENSED : SQUEEZE;
}

// makeMeasure returns the measure(text, { size, weight }) that layout.js
// asks for, answering in the unit size is given in. Text is measured at a
// fixed large size and scaled, so the answer does not depend on hinting at
// small sizes.
export function makeMeasure(doc = document) {
  const ctx = doc.createElement('canvas').getContext('2d');
  const REF = 100;
  const cache = new Map();
  return (text, { size, weight = 400 }) => {
    const key = `${weight}\u001f${text}`;
    let w = cache.get(key);
    if (w === undefined) {
      ctx.font = fontOf(weight, REF);
      w = ctx.measureText(String(text)).width / REF;
      cache.set(key, w);
    }
    return w * size;
  };
}

// loadImages decodes every logo and code a layout refers to. An image that is
// not a data: URI or does not decode is left out; its tile then shows the
// letter instead.
export async function loadImages(layout) {
  const srcs = new Set();
  for (const page of layout.pages) {
    for (const p of page.pieces) {
      if (p.qr && p.qr.src) srcs.add(p.qr.src);
      for (const tile of p.tiles) if (tile.logo) srcs.add(tile.logo);
    }
  }
  const images = new Map();
  await Promise.all([...srcs].map(async (src) => {
    if (!/^data:image\//i.test(src)) return;
    try {
      const img = new Image();
      img.src = src;
      await img.decode();
      if (img.naturalWidth > 0 && img.naturalHeight > 0) images.set(src, img);
    } catch { /* not drawable: the letter tile stands in */ }
  }));
  return images;
}

function roundRect(ctx, x, y, w, h, r) {
  ctx.beginPath();
  ctx.moveTo(x + r, y);
  ctx.arcTo(x + w, y, x + w, y + h, r);
  ctx.arcTo(x + w, y + h, x, y + h, r);
  ctx.arcTo(x, y + h, x, y, r);
  ctx.arcTo(x, y, x + w, y, r);
  ctx.closePath();
}

function outlinePath(ctx, pts) {
  ctx.beginPath();
  pts.forEach(([x, y], i) => (i ? ctx.lineTo(x, y) : ctx.moveTo(x, y)));
  ctx.closePath();
}

// drawText draws one line. The font is set in real pixels (the scale is taken
// out for the call) because a webview may clamp or hint a sub-pixel font size
// differently from the size it ends up with.
function drawText(ctx, k, tx) {
  if (!tx || !tx.text) return;
  ctx.save();
  ctx.translate(tx.x, tx.y);
  ctx.scale(1 / k, 1 / k);
  ctx.font = fontOf(tx.weight, tx.size * k);
  ctx.fillStyle = tx.color;
  ctx.textAlign = tx.align === 'center' ? 'center' : 'left';
  ctx.textBaseline = 'middle';
  // maxWidth squeezes a line that measures a hair wider here than in layout.
  ctx.fillText(tx.text, 0, 0, Math.max(1, tx.maxW * k));
  ctx.restore();
}

// The playlist symbol: three lines of a list and a note, on a 24 unit grid.
function drawPlaylistIcon(ctx, x, y, size, color) {
  ctx.save();
  ctx.translate(x, y);
  ctx.scale(size / 24, size / 24);
  ctx.strokeStyle = color;
  ctx.lineWidth = 2.3;
  ctx.lineCap = 'round';
  ctx.lineJoin = 'round';
  ctx.beginPath();
  ctx.moveTo(3, 6); ctx.lineTo(15, 6);
  ctx.moveTo(3, 12); ctx.lineTo(11, 12);
  ctx.moveTo(3, 18); ctx.lineTo(9, 18);
  ctx.moveTo(18, 18); ctx.lineTo(18, 8); ctx.lineTo(22, 7);
  ctx.stroke();
  ctx.beginPath();
  ctx.arc(16, 18, 2, 0, Math.PI * 2);
  ctx.stroke();
  ctx.restore();
}

// The ST Reborn mark as the app header shows it, on its 64 unit grid: the six
// keys as dots and a line whose middle is a heartbeat. The line and the dots
// take the sticker's ink, the heartbeat keeps its red.
function drawBrand(ctx, b) {
  ctx.save();
  ctx.translate(b.x, b.y);
  ctx.scale(b.size / 64, b.size / 64);
  ctx.fillStyle = b.color;
  for (const cy of [14, 26]) {
    for (const cx of [22, 32, 42]) {
      ctx.beginPath();
      ctx.arc(cx, cy, 3, 0, Math.PI * 2);
      ctx.fill();
    }
  }
  ctx.lineWidth = 3;
  ctx.lineCap = 'round';
  ctx.lineJoin = 'round';
  ctx.strokeStyle = b.color;
  ctx.beginPath();
  ctx.moveTo(4, 44); ctx.lineTo(22, 44);
  ctx.moveTo(42, 44); ctx.lineTo(60, 44);
  ctx.stroke();
  ctx.strokeStyle = '#cc0000';
  ctx.beginPath();
  ctx.moveTo(22, 44); ctx.lineTo(26, 48); ctx.lineTo(30, 32); ctx.lineTo(34, 54); ctx.lineTo(38, 40); ctx.lineTo(42, 44);
  ctx.stroke();
  ctx.restore();
}

// The remote symbol in the header of the remote sticker, on a 24 x 48 unit
// grid: the outline of a remote, its power key, the six keys in two rows of
// three and the ring of the arrow keys. Drawn, so it needs no image and takes
// the sticker's ink.
function drawRemote(ctx, r) {
  ctx.save();
  ctx.translate(r.x, r.y);
  ctx.scale(r.w / 24, r.h / 48);
  ctx.strokeStyle = r.color;
  ctx.fillStyle = r.color;
  ctx.lineWidth = 2;
  roundRect(ctx, 2, 1, 20, 46, 6);
  ctx.stroke();
  ctx.beginPath();
  ctx.arc(12, 8, 2.2, 0, Math.PI * 2);
  ctx.fill();
  for (const cy of [16, 22]) {
    for (const cx of [7.5, 12, 16.5]) {
      ctx.beginPath();
      ctx.arc(cx, cy, 1.5, 0, Math.PI * 2);
      ctx.fill();
    }
  }
  ctx.lineWidth = 1.6;
  ctx.beginPath();
  ctx.arc(12, 34, 5, 0, Math.PI * 2);
  ctx.stroke();
  ctx.beginPath();
  ctx.arc(12, 34, 1.6, 0, Math.PI * 2);
  ctx.fill();
  ctx.restore();
}

// Scissors as paths, handles left and blades pointing right at rot 0. Drawn,
// not typed: the glyph is missing from many fonts and looks different in each
// that has it.
function drawScissors(ctx, s) {
  ctx.save();
  ctx.translate(s.x, s.y);
  ctx.rotate((s.rot * Math.PI) / 180);
  ctx.scale(5 / 24, 5 / 24);
  ctx.translate(-12, -12);
  ctx.lineCap = 'round';
  ctx.beginPath();
  ctx.arc(5.2, 6.6, 2.9, 0, Math.PI * 2);
  ctx.moveTo(8.1, 17.4);
  ctx.arc(5.2, 17.4, 2.9, 0, Math.PI * 2);
  ctx.moveTo(7.6, 8.3); ctx.lineTo(21.5, 17.6);
  ctx.moveTo(7.6, 15.7); ctx.lineTo(21.5, 6.4);
  // A white edge first: a symbol can lie on the bleed of a dark sticker.
  ctx.strokeStyle = '#FFFFFF';
  ctx.lineWidth = 4.4;
  ctx.stroke();
  ctx.strokeStyle = '#111111';
  ctx.lineWidth = 1.7;
  ctx.stroke();
  ctx.restore();
}

function drawTile(ctx, k, tile, pal, images) {
  roundRect(ctx, tile.x, tile.y, tile.size, tile.size, tile.r);
  ctx.fillStyle = pal.tile;
  ctx.fill();
  const img = tile.logo ? images.get(tile.logo) : null;
  if (img) {
    // The whole logo, centred, on the tile colour: a wide logo keeps its
    // sides instead of losing them to a crop.
    const scale = Math.min(tile.size / img.naturalWidth, tile.size / img.naturalHeight);
    const w = img.naturalWidth * scale;
    const h = img.naturalHeight * scale;
    ctx.save();
    roundRect(ctx, tile.x, tile.y, tile.size, tile.size, tile.r);
    ctx.clip();
    ctx.drawImage(img, tile.x + (tile.size - w) / 2, tile.y + (tile.size - h) / 2, w, h);
    ctx.restore();
    return;
  }
  if (tile.icon) {
    const s = tile.size * 0.6;
    drawPlaylistIcon(ctx, tile.x + (tile.size - s) / 2, tile.y + (tile.size - s) / 2, s, pal.ink);
  } else if (tile.letter) {
    drawText(ctx, k, {
      text: tile.letter, x: tile.x + tile.size / 2, y: tile.y + tile.size / 2,
      size: tile.size * 0.52, weight: 700, color: pal.mute, align: 'center', maxW: tile.size * 0.8,
    });
  }
}

function drawQr(ctx, qr, images) {
  ctx.save();
  roundRect(ctx, qr.x, qr.y, qr.size, qr.size, qr.r);
  ctx.clip();
  ctx.fillStyle = '#FFFFFF';
  ctx.fillRect(qr.x, qr.y, qr.size, qr.size);
  const img = qr.src ? images.get(qr.src) : null;
  if (img) ctx.drawImage(img, qr.x, qr.y, qr.size, qr.size);
  ctx.restore();
}

function drawPiece(ctx, k, p, images) {
  ctx.save();
  // A piece that stands on its head turns around its own centre.
  ctx.translate(p.x + p.w / 2, p.y + p.h / 2);
  ctx.rotate((p.rot * Math.PI) / 180);
  ctx.translate(-p.w / 2, -p.h / 2);

  ctx.save();
  outlinePath(ctx, p.outline);
  ctx.clip();
  ctx.fillStyle = p.pal.paper;
  ctx.fillRect(0, 0, p.w, p.h);
  // The rules run the height of their cells and end at the curved edge.
  ctx.strokeStyle = p.pal.rule;
  ctx.lineWidth = 0.25;
  ctx.beginPath();
  for (const r of p.rules) { ctx.moveTo(r.x1, r.y1); ctx.lineTo(r.x2, r.y2); }
  ctx.stroke();
  ctx.restore();

  for (const tile of p.tiles) drawTile(ctx, k, tile, p.pal, images);
  for (const ic of p.icons) drawPlaylistIcon(ctx, ic.x, ic.y, ic.size, ic.color);
  for (const tx of p.texts) drawText(ctx, k, tx);
  if (p.qr) drawQr(ctx, p.qr, images);
  if (p.brand) drawBrand(ctx, p.brand);
  if (p.remote) drawRemote(ctx, p.remote);

  // The cut line, dashed, in a colour that shows on this background.
  outlinePath(ctx, p.outline);
  ctx.strokeStyle = p.pal.mute;
  ctx.lineWidth = 0.25;
  ctx.setLineDash([0.85, 0.65]);
  ctx.stroke();
  ctx.restore();
}

// drawPage paints one page of a layout onto canvas, sized as A4 at dpi.
export function drawPage(canvas, page, { dpi = PRINT_DPI, images = new Map() } = {}) {
  const k = dpi / 25.4;
  canvas.width = Math.round(PAGE.w * k);
  canvas.height = Math.round(PAGE.h * k);
  const ctx = canvas.getContext('2d');
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  ctx.fillStyle = '#FFFFFF';
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  ctx.setTransform(k, 0, 0, k, 0, 0);
  ctx.imageSmoothingQuality = 'high';

  for (const tx of page.header) drawText(ctx, k, tx);
  // All backgrounds first: each runs past its cut line, also under the piece
  // next to it, and that piece is then painted over it.
  for (const p of page.pieces) {
    ctx.fillStyle = p.pal.paper;
    ctx.fillRect(p.x - BLEED, p.y - BLEED, p.w + 2 * BLEED, p.h + 2 * BLEED);
  }
  for (const p of page.pieces) drawPiece(ctx, k, p, images);
  for (const s of page.scissors) drawScissors(ctx, s);
  if (page.control) {
    ctx.strokeStyle = '#111111';
    ctx.lineWidth = 0.3;
    ctx.beginPath();
    ctx.moveTo(page.control.x, page.control.y);
    ctx.lineTo(page.control.x + page.control.length, page.control.y);
    ctx.stroke();
    drawText(ctx, k, page.control.caption);
  }
  // The home screen steps: plain lines like the header's.
  for (const tx of page.guide || []) drawText(ctx, k, tx);
  return canvas;
}
