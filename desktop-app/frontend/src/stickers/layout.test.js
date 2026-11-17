// The sticker sheet's geometry, checked without a canvas: which model gets
// which sticker, how two wide stickers pair, where a page ends, and that
// nothing is placed outside the area that prints on A4 and on US Letter.
import { describe, expect, it } from 'vitest';
import {
  AREA,
  BLEED,
  CONTROL_LENGTH,
  PAGE,
  SCISSORS_HALF,
  SQUEEZE,
  ellipsise,
  fitName,
  layoutStickers,
  stickerKind,
  stickerPalette,
  wrapText,
} from './layout.js';

// Every character is half its font size wide: enough to make names wrap and
// overflow on purpose.
const measure = (text, { size }) => Array.from(String(text)).length * size * 0.5;
const texts = { instruction: 'Print at actual size. Cut along the dashed lines.', control: 'This line is 100 mm long' };

const key = (n, name = '', twice = null, kind = 'station') => ({ n, name, kind, logo: null, twice });
const KEYS = [
  key(1, 'Radio One', { name: 'Morning Mix', kind: 'playlist' }), key(2, 'Jazz'), key(3, 'News'),
  key(4, 'Rock', { name: 'Talk', kind: 'station' }), key(5, 'Classic'), key(6, 'Evening', null, 'playlist'),
];
const speaker = (name, model, housing = 'dark', keys = KEYS) => ({ name, model, housing, keys, qr: 'data:image/png;base64,AAAA' });
const lay = (speakers) => layoutStickers(speakers, { measure, texts });
const parts = (page, i) => page.pieces.filter((p) => p.speaker === i).map((p) => p.part);

describe('model to sticker', () => {
  it('maps each model to its layout', () => {
    expect(stickerKind('SoundTouch 10')).toBe('st10');
    expect(stickerKind('SoundTouch 20')).toBe('st20');
    expect(stickerKind('SoundTouch 30')).toBe('st20');
    expect(stickerKind('SoundTouch Portable')).toBe('portable');
    expect(stickerKind('soundtouch 20 series III')).toBe('st20');
  });

  it('gives every other model the plain sticker', () => {
    // A SoundTouch 300 is a soundbar, not a 30.
    for (const m of ['SoundTouch 300', 'Wave SoundTouch', 'SoundTouch SA-5', '', undefined]) {
      expect(stickerKind(m)).toBe('plain');
    }
  });

  it('builds the pieces of each layout at their true size', () => {
    const { pages } = lay([
      speaker('Kitchen', 'SoundTouch 10'), speaker('Office', 'SoundTouch Portable'),
      speaker('Den', 'SoundTouch 30'), speaker('Hall', 'Wave SoundTouch'),
    ]);
    const page = pages[0];
    expect(parts(page, 0)).toEqual(['back', 'front', 'qr']);
    expect(parts(page, 1)).toEqual(['grid', 'qr']);
    expect(parts(page, 2)).toEqual(['main']);
    expect(parts(page, 3)).toEqual(['main']);
    const size = (i, part) => {
      const p = page.pieces.find((x) => x.speaker === i && x.part === part);
      return [Math.round(p.w * 10) / 10, Math.round(p.h * 10) / 10];
    };
    expect(size(0, 'back')).toEqual([120, 17.5]);
    expect(size(0, 'front')).toEqual([120, 17]);
    expect(size(0, 'qr')).toEqual([19.7, 23.3]);
    expect(size(1, 'grid')).toEqual([80, 51.5]);
    expect(size(1, 'qr')).toEqual([22, 26.3]);
    expect(size(2, 'main')).toEqual([175, 33.7]);
    expect(size(3, 'main')).toEqual([150, 33.7]);
    // The ST20 edge follows the housing; any other model gets a rectangle.
    expect(page.pieces.find((x) => x.speaker === 2).curved).toBe(true);
    const plain = page.pieces.find((x) => x.speaker === 3);
    expect(plain.curved).toBe(false);
    expect(plain.outline).toHaveLength(4);
  });

  it('keeps the pieces of an ST10 edge to edge, with scissors at both shared cuts', () => {
    const page = lay([speaker('Kitchen', 'SoundTouch 10')]).pages[0];
    const [back, front, qr] = page.pieces;
    expect(front.y).toBeCloseTo(back.y + back.h, 5);
    expect(front.x).toBeCloseTo(back.x, 5);
    expect(qr.x).toBeCloseTo(back.x + back.w, 5);
    expect(page.scissors).toHaveLength(2);
    // One on the cut between the arcs, left of them; one on the cut to the QR piece.
    expect(page.scissors[0].y).toBeCloseTo(front.y, 5);
    expect(page.scissors[0].x).toBeLessThan(back.x);
    expect(page.scissors[1].x).toBeCloseTo(qr.x, 5);
  });

  it('puts the Portable QR piece against the straight right edge of the grid', () => {
    const page = lay([speaker('Office', 'SoundTouch Portable')]).pages[0];
    const [grid, qr] = page.pieces;
    expect(qr.x).toBeCloseTo(grid.x + grid.w, 5);
    expect(qr.y).toBeCloseTo(grid.y, 5);
    expect(page.scissors).toEqual([{ x: grid.x + grid.w, y: expect.any(Number), rot: -90 }]);
    expect(page.scissors[0].y).toBeGreaterThan(grid.y + grid.h);
  });
});

describe('housing colour', () => {
  it('uses the shades of the approved print', () => {
    expect(stickerPalette('st10', 'white').paper).toBe('#E5DCC8');
    expect(stickerPalette('st20', 'white').paper).toBe('#EFE6D6');
    expect(stickerPalette('st20', 'dark').paper).toBe('#141518');
    expect(stickerPalette('portable', 'dark').paper).toBe('#707274');
  });

  it('reuses them for the combinations that were not printed', () => {
    expect(stickerPalette('st10', 'dark').paper).toBe('#141518');
    expect(stickerPalette('portable', 'white').paper).toBe('#EFE6D6');
    expect(stickerPalette('plain', 'white').paper).toBe('#EFE6D6');
    expect(stickerPalette('plain', 'dark').paper).toBe('#141518');
  });

  it('colours each piece by its own speaker', () => {
    const page = lay([speaker('Den', 'SoundTouch 20', 'white'), speaker('Hall', 'SoundTouch 20', 'dark')]).pages[0];
    expect(page.pieces.map((p) => p.pal.paper)).toEqual(['#EFE6D6', '#141518']);
  });
});

describe('ST20 pairing', () => {
  it('sets two wide stickers back to back, the second on its head', () => {
    const page = lay([speaker('Den', 'SoundTouch 20'), speaker('Hall', 'SoundTouch 30')]).pages[0];
    const [a, b] = page.pieces;
    expect([a.rot, b.rot]).toEqual([0, 180]);
    expect(b.y).toBeCloseTo(a.y + a.h, 5);
    expect(b.x).toBeCloseTo(a.x, 5);
    // One cut separates them, and it is marked.
    expect(page.scissors).toHaveLength(1);
    expect(page.scissors[0].y).toBeCloseTo(b.y, 5);
  });

  it('pairs wide stickers that are not next to each other in the list', () => {
    const page = lay([speaker('Den', 'SoundTouch 20'), speaker('Kitchen', 'SoundTouch 10'), speaker('Hall', 'Wave SoundTouch')]).pages[0];
    const den = page.pieces.find((p) => p.speaker === 0);
    const hall = page.pieces.find((p) => p.speaker === 2);
    expect(hall.rot).toBe(180);
    expect(hall.y).toBeCloseTo(den.y + den.h, 5);
  });

  it('leaves a single wide sticker upright and without scissors', () => {
    const page = lay([speaker('Den', 'SoundTouch 20')]).pages[0];
    expect(page.pieces).toHaveLength(1);
    expect(page.pieces[0].rot).toBe(0);
    expect(page.scissors).toEqual([]);
  });

  it('starts a new pair with the third', () => {
    const page = lay(['A', 'B', 'C'].map((n) => speaker(n, 'SoundTouch 20'))).pages[0];
    expect(page.pieces.map((p) => p.rot)).toEqual([0, 180, 0]);
    expect(page.pieces[2].y).toBeGreaterThan(page.pieces[1].y + page.pieces[1].h);
  });
});

describe('keys', () => {
  const textsOf = (piece) => piece.texts.map((x) => x.text);

  it('shows the pressed-twice line only on a key that has one', () => {
    const page = lay([speaker('Den', 'SoundTouch 20')]).pages[0];
    const all = textsOf(page.pieces[0]);
    // Keys 1 and 4 have an entry, so the marker is there twice.
    expect(all.filter((x) => x === '2×')).toHaveLength(2);
    expect(all).toContain('Morning Mix');
    expect(all).toContain('Talk');
    // Only the playlist entry gets the list symbol on its line.
    expect(page.pieces[0].icons).toHaveLength(1);
  });

  it('draws an empty key as its number and an empty tile', () => {
    const keys = [key(1, 'Jazz'), key(2), key(3), key(4), key(5), key(6)];
    const piece = lay([speaker('Den', 'SoundTouch 20', 'dark', keys)]).pages[0].pieces[0];
    expect(piece.tiles).toHaveLength(6);
    expect(piece.tiles.map((x) => x.letter)).toEqual(['J', '', '', '', '', '']);
    expect(piece.tiles.every((x) => !x.icon)).toBe(true);
    expect(textsOf(piece)).toEqual(['1', 'Jazz', '2', '3', '4', '5', '6', 'Den']);
  });

  it('fills in keys the speaker did not report', () => {
    const piece = lay([speaker('Den', 'SoundTouch 20', 'dark', [key(3, 'News')])]).pages[0].pieces[0];
    expect(textsOf(piece).filter((x) => /^[1-6]$/.test(x))).toEqual(['1', '2', '3', '4', '5', '6']);
  });

  it('carries the logo, the list symbol or the first letter on the tile', () => {
    const keys = [
      { ...key(1, 'Jazz'), logo: 'data:image/png;base64,BBBB' }, key(2, 'évasion'), key(3, 'Mix', null, 'playlist'),
      key(4, '日本'), key(5), key(6),
    ];
    const tiles = lay([speaker('Den', 'SoundTouch 20', 'dark', keys)]).pages[0].pieces[0].tiles;
    expect(tiles[0].logo).toBe('data:image/png;base64,BBBB');
    expect(tiles[1]).toMatchObject({ logo: null, letter: 'É', icon: false });
    expect(tiles[2]).toMatchObject({ logo: null, icon: true });
    expect(tiles[3].letter).toBe('日');
  });

  it('prints nothing but numbers, the marker and names on a sticker', () => {
    const page = lay([speaker('Kitchen', 'SoundTouch 10'), speaker('Office', 'SoundTouch Portable')]).pages[0];
    const allowed = new Set(['1', '2', '3', '4', '5', '6', '2×', 'Kitchen', 'Office', ...KEYS.flatMap((k) => [k.name, k.twice && k.twice.name])]);
    const words = (s) => s.replace(/…$/, '');
    for (const p of page.pieces) {
      for (const x of p.texts) {
        expect([...allowed].some((a) => a && (a === x.text || a.startsWith(words(x.text)) || a.includes(x.text)))).toBe(true);
      }
    }
  });
});

describe('text fitting', () => {
  const font = { size: 2, weight: 700 }; // one character is 1 wide

  it('keeps a name that fits on one line', () => {
    expect(wrapText('Jazz FM', 10, font, measure)).toEqual({ lines: ['Jazz FM'], truncated: false });
  });

  it('wraps a long name to two lines at a space', () => {
    expect(wrapText('Radio Paradise', 10, font, measure)).toEqual({ lines: ['Radio', 'Paradise'], truncated: false });
  });

  it('breaks after a hyphen and keeps it', () => {
    expect(wrapText('Gute-Laune-Mix', 11, font, measure).lines).toEqual(['Gute-Laune-', 'Mix']);
  });

  it('ellipsises what does not fit the second line', () => {
    const r = wrapText('A very long station name that never fits', 10, font, measure);
    expect(r.truncated).toBe(true);
    expect(r.lines).toHaveLength(2);
    expect(r.lines[0]).toBe('A very');
    expect(r.lines[1].endsWith('…')).toBe(true);
    expect(measure(r.lines[1], font)).toBeLessThanOrEqual(10);
    expect(r.lines[1].startsWith('long stat')).toBe(true);
  });

  it('breaks a word that is longer than a line, as a name without spaces is', () => {
    expect(wrapText('日本放送協会ラジオ', 5, font, measure)).toEqual({ lines: ['日本放送協', '会ラジオ'], truncated: false });
  });

  it('counts characters, not code units', () => {
    expect(ellipsise('😀😀😀😀😀😀', 4, font, measure)).toBe('😀😀😀…');
  });

  it('keeps a name on one line when one step smaller is enough', () => {
    expect(fitName('Radio Jazz', 10, 2, measure)).toEqual({ lines: ['Radio Jazz'], size: 2 });
    // 13 characters: too wide even squeezed, one line at nine tenths.
    const smaller = fitName('Radio Jazz FM', 10, 2, measure);
    expect(smaller.lines).toEqual(['Radio Jazz FM']);
    expect(smaller.size).toBeCloseTo(1.8, 5);
  });

  it('tries a name narrower and smaller before cutting it', () => {
    // Fits two lines of 10 as it is.
    expect(fitName('Radio Paradise', 10, 2, measure)).toEqual({ lines: ['Radio', 'Paradise'], size: 2 });
    // 24 characters need more than two lines of 10, but fit when squeezed.
    const squeezed = fitName('abcdefghijkl mnopqrstuvwx', 10, 2, measure);
    expect(squeezed.size).toBe(2);
    expect(squeezed.lines).toEqual(['abcdefghijkl', 'mnopqrstuvwx']);
    expect(measure(squeezed.lines[0], font) * SQUEEZE).toBeLessThanOrEqual(10);
    // Still too long: smaller, and only then cut.
    const cut = fitName('x'.repeat(80), 10, 2, measure);
    expect(cut.size).toBeCloseTo(1.6, 5);
    expect(cut.lines).toHaveLength(2);
    expect(cut.lines[1].endsWith('…')).toBe(true);
  });

  it('never lets a line on a sticker be wider than its box allows', () => {
    const long = 'An extraordinarily long station name with many words in it';
    const keys = [1, 2, 3, 4, 5, 6].map((n) => key(n, long, { name: long, kind: 'playlist' }));
    const { pages } = lay([
      speaker(long, 'SoundTouch 10', 'dark', keys), speaker(long, 'SoundTouch 20', 'dark', keys), speaker(long, 'SoundTouch Portable', 'dark', keys),
    ]);
    for (const p of pages.flatMap((pg) => pg.pieces)) {
      for (const x of p.texts) {
        expect(measure(x.text, { size: x.size }) * SQUEEZE).toBeLessThanOrEqual(x.maxW + 1e-9);
        // And the box itself stays inside the piece.
        const left = x.align === 'center' ? x.x - x.maxW / 2 : x.x;
        expect(left).toBeGreaterThanOrEqual(-1e-9);
        expect(left + x.maxW).toBeLessThanOrEqual(p.w + 1e-9);
        expect(x.y - x.size / 2).toBeGreaterThanOrEqual(0);
        expect(x.y + x.size / 2).toBeLessThanOrEqual(p.h);
      }
    }
  });
});

describe('pages', () => {
  const many = (n, model) => Array.from({ length: n }, (_, i) => speaker(`Room ${i + 1}`, model));

  // Everything a page holds, as boxes in mm.
  const boxesOf = (page) => [
    ...page.pieces.map((p) => ({ x0: p.x - BLEED, y0: p.y - BLEED, x1: p.x + p.w + BLEED, y1: p.y + p.h + BLEED })),
    ...page.scissors.map((s) => ({ x0: s.x - SCISSORS_HALF, y0: s.y - SCISSORS_HALF, x1: s.x + SCISSORS_HALF, y1: s.y + SCISSORS_HALF })),
    ...[...page.header, page.control.caption].map((x) => ({ x0: x.x, y0: x.y - x.size / 2, x1: x.x + x.maxW, y1: x.y + x.size / 2 })),
    { x0: page.control.x, y0: page.control.y, x1: page.control.x + page.control.length, y1: page.control.y },
  ];

  it('keeps everything inside the printable area, on every page', () => {
    const speakers = [
      ...many(3, 'SoundTouch 10'), ...many(5, 'SoundTouch 20'), ...many(3, 'SoundTouch Portable'), ...many(2, 'Wave SoundTouch'),
    ];
    const { pages } = lay(speakers);
    expect(pages.length).toBeGreaterThan(1);
    for (const page of pages) {
      for (const b of boxesOf(page)) {
        expect(b.x0).toBeGreaterThanOrEqual(AREA.x - 1e-9);
        expect(b.y0).toBeGreaterThanOrEqual(AREA.y - 1e-9);
        expect(b.x1).toBeLessThanOrEqual(AREA.x + AREA.w + 1e-9);
        expect(b.y1).toBeLessThanOrEqual(AREA.y + AREA.h + 1e-9);
      }
    }
  });

  it('has an area that fits A4 and US Letter at actual size, centred', () => {
    expect(AREA.x + AREA.w / 2).toBeCloseTo(PAGE.w / 2, 5);
    expect(AREA.y + AREA.h / 2).toBeCloseTo(PAGE.h / 2, 5);
    // Letter is 215.9 x 279.4 mm; a printer centres the A4 page on it.
    expect(AREA.w).toBeLessThanOrEqual(215.9 - 2 * 6.35);
    expect(AREA.h).toBeLessThanOrEqual(279.4 - 2 * 6.35);
  });

  it('starts a new page when the next group does not fit', () => {
    // An ST10 group is 34.5 mm high plus bleed and gap: five fit one page,
    // the sixth does not.
    expect(lay(many(5, 'SoundTouch 10')).pages).toHaveLength(1);
    const { pages } = lay(many(6, 'SoundTouch 10'));
    expect(pages).toHaveLength(2);
    expect(new Set(pages[1].pieces.map((p) => p.speaker))).toEqual(new Set([5]));
  });

  it('never splits one speaker, or a pair, across pages', () => {
    const speakers = [...many(4, 'SoundTouch Portable'), ...many(6, 'SoundTouch 20'), ...many(4, 'SoundTouch 10')];
    const { pages } = lay(speakers);
    const pageOf = new Map();
    pages.forEach((page, n) => page.pieces.forEach((p) => {
      if (pageOf.has(p.speaker)) expect(pageOf.get(p.speaker)).toBe(n);
      pageOf.set(p.speaker, n);
    }));
    expect(pageOf.size).toBe(speakers.length);
    // A piece on its head shares its page with the one it is paired with.
    for (const page of pages) {
      for (const p of page.pieces.filter((x) => x.rot === 180)) {
        expect(page.pieces.some((q) => q.rot === 0 && q.part === 'main' && Math.abs(q.y + q.h - p.y) < 1e-6)).toBe(true);
      }
    }
  });

  it('does not let groups overlap', () => {
    for (const page of lay([...many(2, 'SoundTouch 10'), ...many(3, 'SoundTouch 20'), ...many(2, 'SoundTouch Portable')]).pages) {
      const bySpeaker = new Map();
      for (const p of page.pieces) {
        const b = bySpeaker.get(p.speaker) || { y0: Infinity, y1: -Infinity };
        bySpeaker.set(p.speaker, { y0: Math.min(b.y0, p.y), y1: Math.max(b.y1, p.y + p.h) });
      }
      const spans = [...bySpeaker.values()].sort((a, b) => a.y0 - b.y0);
      for (let i = 1; i < spans.length; i++) expect(spans[i].y0).toBeGreaterThanOrEqual(spans[i - 1].y1 - 1e-6);
    }
  });

  it('puts the instruction and a 100 mm control line on every page', () => {
    const { pages } = lay(many(7, 'SoundTouch 10'));
    for (const page of pages) {
      expect(page.header.map((x) => x.text).join(' ')).toBe(texts.instruction);
      expect(page.control.length).toBe(CONTROL_LENGTH);
      expect(page.control.caption.text).toBe(texts.control);
      const lowest = Math.max(...page.pieces.map((p) => p.y + p.h + BLEED));
      expect(page.control.y).toBeGreaterThan(lowest);
    }
  });

  it('wraps a long instruction instead of running off the page', () => {
    const long = Array.from({ length: 60 }, () => 'word').join(' ');
    const page = layoutStickers([speaker('Den', 'SoundTouch 20')], { measure, texts: { instruction: long, control: 'c' } }).pages[0];
    expect(page.header.length).toBeGreaterThan(1);
    for (const x of page.header) expect(measure(x.text, { size: x.size })).toBeLessThanOrEqual(AREA.w);
    expect(page.pieces[0].y - BLEED).toBeGreaterThan(Math.max(...page.header.map((x) => x.y + x.size / 2)));
  });

  it('makes no page for no speaker', () => {
    expect(lay([]).pages).toEqual([]);
    expect(layoutStickers(null, { measure, texts }).pages).toEqual([]);
  });
});
