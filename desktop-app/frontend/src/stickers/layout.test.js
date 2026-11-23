// The sticker sheet's geometry, checked without a canvas: which model gets
// which sticker, how two wide stickers pair, where a page ends, and that
// nothing is placed outside the area that prints on A4 and on US Letter.
import { describe, expect, it } from 'vitest';
import { PRINT_DPI } from './draw.js';
import {
  AREA,
  BLEED,
  CONTROL_LENGTH,
  PAGE,
  REMOTE,
  REMOTE_GRIDS,
  REMOTE_SIZE_MAX,
  REMOTE_SIZE_MIN,
  REMOTE_TWO_COLUMNS_GAIN,
  SCISSORS_HALF,
  SQUEEZE,
  chooseRemoteGrid,
  ellipsise,
  fitName,
  layoutStickers,
  planRemote,
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
// Every speaker also gets its remote sticker, after all the speaker stickers.
// Most checks are about a speaker's own sticker and leave those out.
const own = (page) => page.pieces.filter((p) => p.part !== 'remote');
const parts = (page, i) => own(page).filter((p) => p.speaker === i).map((p) => p.part);

describe('model to sticker', () => {
  it('maps each model to its layout', () => {
    expect(stickerKind('SoundTouch 10')).toBe('st10');
    expect(stickerKind('SoundTouch 20')).toBe('st20');
    expect(stickerKind('SoundTouch 30')).toBe('st20');
    expect(stickerKind('SoundTouch Portable')).toBe('portable');
    expect(stickerKind('soundtouch 20 series III')).toBe('st20');
  });

  it('keeps a two-line name and its pressed-twice line inside the sticker', () => {
    // The fullest cell there is: a name that wraps to two lines and a second
    // entry on every key. Nothing may run past the top or bottom of its piece.
    const full = [1, 2, 3, 4, 5, 6].map((n) => key(n, 'Deutschlandfunk Kultur Nova', { name: 'Morning Mix Nova', kind: 'playlist' }));
    const { pages } = lay(['SoundTouch 10', 'SoundTouch Portable', 'SoundTouch 30', 'Wave SoundTouch']
      .map((m) => speaker('Kitchen', m, 'dark', full)));
    for (const p of pages[0].pieces) {
      for (const t of p.texts) {
        expect(t.y - t.size / 2).toBeGreaterThanOrEqual(0);
        expect(t.y + t.size / 2).toBeLessThanOrEqual(p.h);
      }
    }
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

  it('puts the ST Reborn mark into the left column of the ST20 and ST30 sticker, mirroring the code', () => {
    const page = lay([speaker('Den', 'SoundTouch 30'), speaker('Hall', 'Wave SoundTouch'), speaker('Kitchen', 'SoundTouch 10')]).pages[0];
    const wide = page.pieces.find((x) => x.speaker === 0);
    expect(wide.brand).toBeTruthy();
    // Same size and height as the code, and as far from the left edge as the code is from the right.
    expect(wide.brand.size).toBeCloseTo(wide.qr.size, 5);
    expect(wide.brand.y).toBeCloseTo(wide.qr.y, 5);
    expect(wide.brand.x).toBeCloseTo(wide.w - (wide.qr.x + wide.qr.size), 5);
    const name = wide.texts.find((t) => t.text === 'ST Reborn');
    expect(name).toBeTruthy();
    expect(name.x).toBeCloseTo(wide.brand.x + wide.brand.size / 2, 5);
    // Models without the left column carry no mark.
    for (const p of page.pieces.filter((x) => x.speaker !== 0)) {
      expect(p.brand).toBeNull();
      expect(p.texts.some((t) => t.text === 'ST Reborn')).toBe(false);
    }
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
    expect(own(page).map((p) => p.pal.paper)).toEqual(['#EFE6D6', '#141518']);
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
    expect(own(page)).toHaveLength(1);
    expect(page.pieces[0].rot).toBe(0);
    expect(page.scissors).toEqual([]);
  });

  it('starts a new pair with the third', () => {
    const page = lay(['A', 'B', 'C'].map((n) => speaker(n, 'SoundTouch 20'))).pages[0];
    expect(own(page).map((p) => p.rot)).toEqual([0, 180, 0]);
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
    expect(textsOf(piece)).toEqual(['1', 'Jazz', '2', '3', '4', '5', '6', 'Den', 'ST Reborn']);
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

describe('large text', () => {
  const MODELS = ['SoundTouch 10', 'SoundTouch 20', 'SoundTouch 30', 'SoundTouch Portable', 'Wave SoundTouch'];
  const both = (speakers) => [false, true].map((large) => layoutStickers(speakers, { measure, texts, large }).pages);
  // The name lines of the keys: bold, left-aligned, in ink, not a key number
  // (on the Portable's grey the numbers are in ink too) and not the marker.
  // The speaker name under the code is centred.
  const nameLines = (p) => p.texts.filter((x) => x.weight === 700 && x.align === 'left' && x.color === p.pal.ink && !/^([1-6]|2×)$/.test(x.text));
  const twiceLines = (p) => p.texts.filter((x) => x.weight === 600);
  const maxSize = (xs) => Math.max(...xs.map((x) => x.size));
  const NAMES = ['Jazz', 'SWR3', 'Radio Paradise', 'Bayern 3 - Die Hits', 'Deutschlandfunk Kultur', 'Norddeutscher Rundfunk NDR Kultur', '日本放送協会ラジオ第一放送'];

  // edgeAt is the top or bottom cut line of a piece at x, from its outline:
  // the first half runs along the top from left to right, the second half
  // along the bottom from right to left.
  const edgeAt = (pts, x) => {
    for (let i = 1; i < pts.length; i++) {
      const [x0, y0] = pts[i - 1], [x1, y1] = pts[i];
      if (x >= Math.min(x0, x1) - 1e-9 && x <= Math.max(x0, x1) + 1e-9) return x1 === x0 ? Math.max(y0, y1) : y0 + ((x - x0) / (x1 - x0)) * (y1 - y0);
    }
    return null;
  };
  // expectInside: no line is wider than its box allows, its box stays inside
  // the piece, and the line stays inside the cut line, the curved one too.
  const expectInside = (pages) => {
    for (const p of pages.flatMap((pg) => pg.pieces)) {
      const n = p.outline.length / 2;
      const top = p.outline.slice(0, n);
      const bottom = p.outline.slice(n).reverse();
      for (const x of p.texts) {
        expect(measure(x.text, { size: x.size }) * SQUEEZE, x.text).toBeLessThanOrEqual(x.maxW + 1e-9);
        const left = x.align === 'center' ? x.x - x.maxW / 2 : x.x;
        expect(left).toBeGreaterThanOrEqual(-1e-9);
        expect(left + x.maxW).toBeLessThanOrEqual(p.w + 1e-9);
        expect(x.y - x.size / 2).toBeGreaterThanOrEqual(0);
        expect(x.y + x.size / 2).toBeLessThanOrEqual(p.h);
        const drawn = Math.min(measure(x.text, { size: x.size }), x.maxW);
        const from = x.align === 'center' ? x.x - drawn / 2 : x.x;
        for (let k = 0; k <= 8; k++) {
          const at = from + (drawn * k) / 8;
          expect(x.y - x.size / 2, `${p.part} ${x.text}`).toBeGreaterThanOrEqual(edgeAt(top, at) - 1e-9);
          expect(x.y + x.size / 2, `${p.part} ${x.text}`).toBeLessThanOrEqual(edgeAt(bottom, at) + 1e-9);
        }
      }
    }
  };

  it('sets a short name larger than normal on every kind of sticker', () => {
    for (const model of MODELS) {
      const keys = [1, 2, 3, 4, 5, 6].map((n) => key(n, 'Jazz', n % 2 ? { name: 'Talk', kind: 'station' } : null));
      const [normal, big] = both([speaker('Den', model, 'dark', keys)]).map((pages) => own(pages[0]));
      for (let i = 0; i < normal.length; i++) {
        if (!nameLines(normal[i]).length) continue;
        expect(maxSize(nameLines(big[i])), model).toBeGreaterThan(maxSize(nameLines(normal[i])) * 1.2);
        // The pressed-twice line grows too, but never past the name.
        expect(maxSize(twiceLines(big[i])), model).toBeGreaterThan(maxSize(twiceLines(normal[i])));
        expect(maxSize(twiceLines(big[i])), model).toBeLessThanOrEqual(Math.min(...nameLines(big[i]).map((x) => x.size)) + 1e-9);
        // The speaker name under the code grows as well.
        const qrName = (p) => p.texts.find((x) => x.align === 'center' && x.text === 'Den');
        const q = normal.findIndex((p) => qrName(p));
        expect(qrName(big[q]).size, model).toBeGreaterThan(qrName(normal[q]).size);
      }
    }
  });

  for (const name of NAMES) {
    it(`never sets a name or a pressed-twice line smaller than normal: ${name}`, () => {
      for (const twice of [null, { name, kind: 'playlist' }]) {
        const keys = [1, 2, 3, 4, 5, 6].map((n) => key(n, name, twice));
        const [normal, big] = both(MODELS.map((m) => speaker(name, m, 'dark', keys))).map((pages) => pages.flatMap(own));
        expect(big.map((p) => p.part)).toEqual(normal.map((p) => p.part));
        for (let i = 0; i < normal.length; i++) {
          if (!nameLines(normal[i]).length) continue;
          const label = `${normal[i].part} ${name}`;
          expect(Math.min(...nameLines(big[i]).map((x) => x.size)), label).toBeGreaterThanOrEqual(Math.min(...nameLines(normal[i]).map((x) => x.size)) - 1e-9);
          if (twice) expect(Math.min(...twiceLines(big[i]).map((x) => x.size)), label).toBeGreaterThanOrEqual(Math.min(...twiceLines(normal[i]).map((x) => x.size)) - 1e-9);
        }
      }
    });
  }

  // One test per set of names: every remote sticker on these sheets is
  // planned too, which takes most of the time.
  const long = 'An extraordinarily long station name with many words in it';
  const SETS = [
    ['short names', KEYS],
    ['a very long name everywhere', [1, 2, 3, 4, 5, 6].map((n) => key(n, long, { name: long, kind: 'playlist' }))],
    ['the fullest cell', [1, 2, 3, 4, 5, 6].map((n) => key(n, 'Deutschlandfunk Kultur Nova', { name: 'Morning Mix Nova', kind: 'playlist' }))],
    ...NAMES.map((name) => [name, [1, 2, 3, 4, 5, 6].map((n) => key(n, name, n % 2 ? { name: 'Bayern 3', kind: 'station' } : null))]),
  ];
  for (const [label, keys] of SETS) {
    it(`keeps every line inside its piece and its cut line, in normal and in large text: ${label}`, () => {
      for (const pages of both(MODELS.map((m) => speaker('Wohnzimmer', m, 'dark', keys)))) expectInside(pages);
    });
  }

  it('leaves the number and tile of every Portable cell where they line up', () => {
    const keys = [key(1, 'Jazz'), key(2, 'Deutschlandfunk Kultur'), key(3, 'SWR3', { name: 'Talk', kind: 'station' }), key(4, 'FIP'), key(5, 'Bayern 3 - Die Hits'), key(6)];
    const [normal, big] = both([speaker('Den', 'SoundTouch Portable', 'dark', keys)]).map((pages) => own(pages[0]));
    const grid = (ps) => ps.find((p) => p.part === 'grid');
    expect(grid(big).tiles.map((x) => x.y)).toEqual(grid(normal).tiles.map((x) => x.y));
  });

  it('leaves the remote sticker as it is: it already sets its names as large as they fit', () => {
    const [normal, big] = both([speaker('Den', 'SoundTouch 20', 'white', KEYS)]);
    const remote = (pages) => pages.flatMap((pg) => pg.pieces).find((p) => p.part === 'remote');
    expect(remote(big).texts).toEqual(remote(normal).texts);
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
    // the sixth does not. The remote stickers follow on the next page.
    const five = lay(many(5, 'SoundTouch 10')).pages;
    expect(new Set(own(five[0]).map((p) => p.speaker))).toEqual(new Set([0, 1, 2, 3, 4]));
    const { pages } = lay(many(6, 'SoundTouch 10'));
    expect(new Set(own(pages[0]).map((p) => p.speaker))).toEqual(new Set([0, 1, 2, 3, 4]));
    expect(new Set(own(pages[1]).map((p) => p.speaker))).toEqual(new Set([5]));
  });

  it('never splits one speaker, or a pair, across pages', () => {
    const speakers = [...many(4, 'SoundTouch Portable'), ...many(6, 'SoundTouch 20'), ...many(4, 'SoundTouch 10')];
    const { pages } = lay(speakers);
    const pageOf = new Map();
    pages.forEach((page, n) => own(page).forEach((p) => {
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
      for (const p of own(page)) {
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

describe('the remote sticker', () => {
  const bundles = import.meta.glob('../i18n/bundles/*.json', { eager: true, import: 'default' });
  const REALISTIC = [
    key(1, 'Radio Paradise', { name: 'Lofi Beats', kind: 'playlist' }),
    key(2, 'SWR3', { name: 'Discover Weekly', kind: 'playlist' }),
    key(3, 'Deutschlandfunk', { name: 'WDR 2', kind: 'station' }),
    key(4, 'BBC Radio 6 Music', { name: 'Klassik Radio', kind: 'station' }),
    key(5, 'FIP', { name: 'Bayern 3', kind: 'station' }),
    key(6, 'Jazz24', { name: 'Release Radar', kind: 'playlist' }),
  ];
  const GERMAN = [
    key(1, 'Deutschlandfunk Kultur', { name: 'Bayerischer Rundfunk Klassik', kind: 'station' }),
    key(2, 'Mitteldeutscher Rundfunk Sputnik', { name: 'Wochenend-Entspannungsmusik', kind: 'playlist' }),
    key(3, 'Donnerstagnachmittagsplaylist', { name: 'Hessischer Rundfunk hr-iNFO', kind: 'station' }),
    key(4, 'Norddeutscher Rundfunk NDR Kultur', { name: 'Rundfunk Berlin-Brandenburg radioeins', kind: 'station' }),
    key(5, 'Südwestrundfunk SWR1 Baden-Württemberg', { name: 'Sonntagmorgenfrühstücksmusik', kind: 'playlist' }),
    key(6, 'Klassik Radio Filmmusik', { name: 'Kinderliederzusammenstellung', kind: 'playlist' }),
  ];
  const remoteSpeaker = (name, model, keys, housing = 'white') => speaker(name, model, housing, keys);
  const remotesOf = (pages) => pages.flatMap((pg) => pg.pieces.filter((p) => p.part === 'remote'));
  const one = (sp, extra = {}) => remotesOf(layoutStickers([sp], { measure, texts: { ...texts, remote: 'Remote' }, ...extra }).pages)[0];
  const textsOf = (piece) => piece.texts.map((x) => x.text);

  it('is a plain 40 x 75 mm rectangle with a cut line, for every model', () => {
    for (const model of ['SoundTouch 10', 'SoundTouch 20', 'SoundTouch 30', 'SoundTouch Portable', 'Wave SoundTouch', 'SoundTouch 300', '', undefined]) {
      const p = one(remoteSpeaker('Kitchen', model, REALISTIC));
      expect(p, String(model)).toBeTruthy();
      expect([p.w, p.h]).toEqual([REMOTE.w, REMOTE.h]);
      expect([REMOTE.w, REMOTE.h]).toEqual([40, 75]);
      expect(p.curved).toBe(false);
      expect(p.outline).toEqual([[0, 0], [40, 0], [40, 75], [0, 75]]);
      expect(p.rot).toBe(0);
    }
  });

  it("is always on the sheet, one per speaker, after the speakers' own stickers", () => {
    const speakers = ['SoundTouch 10', 'SoundTouch 20', 'SoundTouch Portable', 'Wave SoundTouch'].map((m, i) => speaker(`Room ${i + 1}`, m));
    const { pages } = lay(speakers);
    expect(remotesOf(pages).map((p) => p.speaker)).toEqual([0, 1, 2, 3]);
    // Nothing of a speaker's own sticker comes after the first remote sticker.
    const all = pages.flatMap((pg) => pg.pieces);
    const first = all.findIndex((p) => p.part === 'remote');
    expect(all.slice(first).every((p) => p.part === 'remote')).toBe(true);
  });

  it('says it is for the remote: the symbol, the word and the speaker name in the header', () => {
    const p = one(remoteSpeaker('Living Room', 'SoundTouch 10', REALISTIC));
    expect(p.remote).toBeTruthy();
    expect(p.remote.h).toBeCloseTo(p.remote.w * 2, 9);
    // The symbol sits in the header, above the first rule.
    const firstRule = Math.min(...p.rules.filter((r) => r.y1 === r.y2).map((r) => r.y1));
    expect(p.remote.y + p.remote.h).toBeLessThan(firstRule);
    const head = p.texts.filter((x) => x.y < firstRule);
    expect(head.map((x) => x.text)).toEqual(['Remote', 'Living Room']);
    // The speaker stickers carry no symbol.
    const sheet = layoutStickers([remoteSpeaker('Den', 'SoundTouch 20', KEYS)], { measure, texts }).pages[0];
    expect(sheet.pieces.filter((x) => x.part !== 'remote').every((x) => x.remote === null)).toBe(true);
  });

  it('lists all twelve: every key pressed once and, under it, pressed twice', () => {
    const p = one(remoteSpeaker('Kitchen', 'SoundTouch 20', REALISTIC));
    const all = textsOf(p);
    for (const n of ['1', '2', '3', '4', '5', '6']) expect(all).toContain(n);
    for (const k of REALISTIC) {
      expect(all).toContain(k.name);
      expect(all).toContain(k.twice.name);
    }
    expect(all.filter((x) => x === '2×')).toHaveLength(6);
    // Nothing is cut in a realistic set, and the keys stand in one column.
    expect(all.some((x) => x.endsWith('…'))).toBe(false);
    expect(p.grid).toMatchObject({ cols: 1, rows: 6, fits: true });
    // Key n pressed twice is set under key n pressed once.
    for (const k of REALISTIC) {
      const once = p.texts.find((x) => x.text === k.name);
      const twice = p.texts.find((x) => x.text === k.twice.name);
      expect(twice.y).toBeGreaterThan(once.y);
      expect(twice.size).toBeLessThan(once.size + 1e-9);
    }
  });

  it('uses the largest name size that fits, by measurement', () => {
    for (const keys of [REALISTIC, GERMAN, KEYS]) {
      const { grid, plan } = chooseRemoteGrid(keys, measure);
      expect(plan.size).toBeLessThanOrEqual(REMOTE_SIZE_MAX);
      expect(plan.size).toBeGreaterThanOrEqual(REMOTE_SIZE_MIN);
      if (plan.ok && plan.size < REMOTE_SIZE_MAX) {
        // One step larger does not fit whole.
        expect(planRemote(keys, grid, measure, plan.size + 0.1).size).toBeCloseTo(plan.size, 9);
      }
    }
    // Short names are set larger than long ones.
    expect(chooseRemoteGrid(REALISTIC, measure).plan.size).toBeGreaterThan(chooseRemoteGrid(GERMAN, measure).plan.size);
  });

  it('keeps one column unless two allow clearly larger names', () => {
    // Long names need the full width.
    expect(chooseRemoteGrid(GERMAN, measure).grid.cols).toBe(1);
    expect(chooseRemoteGrid(REALISTIC, measure).grid.cols).toBe(1);
    // Short names on every key, pressed once and twice: one column is held
    // down by the height of its rows, two columns are not.
    const short = [1, 2, 3, 4, 5, 6].map((n) => key(n, `FM${n}`, { name: `AM${n}`, kind: 'station' }));
    const pick = chooseRemoteGrid(short, measure);
    const [a, b] = REMOTE_GRIDS.map((g) => planRemote(short, g, measure));
    expect(b.size).toBeGreaterThanOrEqual(a.size * REMOTE_TWO_COLUMNS_GAIN);
    expect(pick.grid.cols).toBe(2);
    const p = one(remoteSpeaker('Den', 'SoundTouch 30', short));
    expect(p.grid.cols).toBe(2);
    // A rule between the columns.
    expect(p.rules.some((r) => r.x1 === r.x2 && r.x1 > 15 && r.x1 < 25)).toBe(true);
  });

  it('keeps every line inside the sticker, with long German names too', () => {
    for (const keys of [REALISTIC, GERMAN, KEYS]) {
      const p = one(remoteSpeaker('Wohnzimmer Lautsprecher am Fenster links', 'SoundTouch 20', keys));
      for (const x of p.texts) {
        expect(measure(x.text, { size: x.size }) * SQUEEZE).toBeLessThanOrEqual(x.maxW + 1e-9);
        expect(x.x).toBeGreaterThanOrEqual(2 - 1e-9);
        expect(x.x + x.maxW).toBeLessThanOrEqual(p.w - 2 + 1e-9);
        expect(x.y - x.size / 2).toBeGreaterThanOrEqual(0);
        expect(x.y + x.size / 2).toBeLessThanOrEqual(p.h);
        expect(x.size).toBeGreaterThanOrEqual(REMOTE_SIZE_MIN * 0.8 - 1e-9);
      }
    }
  });

  it('never breaks a long German word in the middle', () => {
    const p = one(remoteSpeaker('Kitchen', 'SoundTouch 20', GERMAN));
    expect(p.grid.fits).toBe(true);
    const words = GERMAN.flatMap((k) => [k.name, k.twice.name]).flatMap((s) => s.split(/\s+/)).flatMap((w) => [w, ...w.split(/(?<=-)/)]);
    const body = p.texts.filter((x) => x.y > 13 && !/^[1-6]$/.test(x.text) && x.text !== '2×');
    for (const x of body) {
      for (const w of x.text.split(/\s+/)) expect(words, x.text).toContain(w);
    }
  });

  it('breaks a name without spaces between characters', () => {
    const cjk = [1, 2, 3, 4, 5, 6].map((n) => key(n, '日本放送協会ラジオ第一放送東京', { name: 'ＮＨＫ交響楽団', kind: 'station' }));
    const p = one(remoteSpeaker('リビング', 'SoundTouch 10', cjk));
    expect(p.grid.fits).toBe(true);
    expect(textsOf(p).some((x) => x.endsWith('…'))).toBe(false);
  });

  it('shows an empty key as its number, and a key with only its second entry', () => {
    const keys = [key(1, 'Jazz'), key(2), key(3, '', { name: 'Talk', kind: 'station' }), key(4), key(5), key(6)];
    const p = one(remoteSpeaker('Den', 'SoundTouch 20', keys));
    expect(textsOf(p)).toEqual(['Remote', 'Den', '1', 'Jazz', '2', '3', '2×', 'Talk', '4', '5', '6']);
    const empty = one(remoteSpeaker('Den', 'SoundTouch 20', []));
    expect(textsOf(empty)).toEqual(['Remote', 'Den', '1', '2', '3', '4', '5', '6']);
    expect(empty.grid.cols).toBe(1);
  });

  it('cuts a speaker name only when two small lines do not hold it', () => {
    const p = one(remoteSpeaker('Wohnzimmer Lautsprecher', 'SoundTouch 20', KEYS));
    const head = p.texts.filter((x) => x.y < 12.5).map((x) => x.text);
    expect(head.join(' ')).toBe('Remote Wohnzimmer Lautsprecher');
    const cut = one(remoteSpeaker('x'.repeat(90), 'SoundTouch 20', KEYS));
    expect(cut.texts[1].text.endsWith('…')).toBe(true);
  });

  it('fits the word for remote of every language bundle uncut', () => {
    expect(Object.keys(bundles).length).toBeGreaterThanOrEqual(14);
    for (const [file, b] of Object.entries(bundles)) {
      const word = b['stickers.remoteHeader'];
      expect(word, file).toBeTruthy();
      const p = one(remoteSpeaker('Kitchen', 'SoundTouch 20', GERMAN), { texts: { ...texts, remote: word } });
      expect(p.texts[0].text, file).toBe(word);
    }
  });

  it('follows the housing colour', () => {
    expect(one(remoteSpeaker('Den', 'SoundTouch 10', KEYS, 'white')).pal.paper).toBe('#EFE6D6');
    expect(one(remoteSpeaker('Den', 'SoundTouch Portable', KEYS, 'dark')).pal.paper).toBe('#141518');
  });

  it('sets remote stickers side by side after the speaker stickers, inside the area, without overlap', () => {
    const speakers = ['SoundTouch 10', 'SoundTouch 20', 'SoundTouch Portable', 'Wave SoundTouch', 'SoundTouch 30', 'SoundTouch 10']
      .map((m, i) => remoteSpeaker(`Room ${i + 1}`, m, GERMAN));
    const { pages } = layoutStickers(speakers, { measure, texts: { ...texts, remote: 'Remote' } });
    const remotes = remotesOf(pages);
    expect(remotes.map((p) => p.speaker).sort()).toEqual([0, 1, 2, 3, 4, 5]);
    for (const page of pages) {
      const rs = page.pieces.filter((p) => p.part === 'remote');
      const others = page.pieces.filter((p) => p.part !== 'remote');
      for (const r of rs) {
        expect(r.x - BLEED).toBeGreaterThanOrEqual(AREA.x - 1e-9);
        expect(r.x + r.w + BLEED).toBeLessThanOrEqual(AREA.x + AREA.w + 1e-9);
        expect(r.y - BLEED).toBeGreaterThanOrEqual(AREA.y - 1e-9);
        expect(r.y + r.h + BLEED).toBeLessThanOrEqual(AREA.y + AREA.h + 1e-9);
        // Below every speaker sticker of the page.
        for (const o of others) expect(r.y - BLEED).toBeGreaterThan(o.y + o.h + BLEED);
        expect(page.control.y).toBeGreaterThan(r.y + r.h + BLEED);
      }
      // Bleeds never touch: every remote is cut on its own.
      for (let i = 0; i < rs.length; i++) {
        for (let j = i + 1; j < rs.length; j++) {
          const a = rs[i], b = rs[j];
          const apartX = Math.max(a.x, b.x) - Math.min(a.x + a.w, b.x + b.w);
          const apartY = Math.max(a.y, b.y) - Math.min(a.y + a.h, b.y + b.h);
          expect(Math.max(apartX, apartY)).toBeGreaterThanOrEqual(2 * BLEED);
        }
      }
    }
    // Four side by side in a row.
    const rows = new Map();
    for (const r of remotes) rows.set(Math.round(r.y * 1000), (rows.get(Math.round(r.y * 1000)) || 0) + 1);
    expect(Math.max(...rows.values())).toBe(4);
  });

  it('prints at exactly 40 x 75 mm from a 100 % print of the PDF', () => {
    // draw.js paints A4 at PRINT_DPI with the canvas rounded to whole pixels;
    // Go (stickers.go) stretches that image over an A4 page of 595.28 x
    // 841.89 pt. Through both steps a millimetre of the layout stays a
    // millimetre on paper.
    const k = PRINT_DPI / 25.4;
    const canvasW = Math.round(PAGE.w * k);
    const canvasH = Math.round(PAGE.h * k);
    const PT = 25.4 / 72;
    const onPaperX = (mm) => ((mm * k) / canvasW) * 595.28 * PT;
    const onPaperY = (mm) => ((mm * k) / canvasH) * 841.89 * PT;
    expect(595.28 * PT).toBeCloseTo(PAGE.w, 1);
    expect(841.89 * PT).toBeCloseTo(PAGE.h, 1);
    expect(Math.abs(onPaperX(REMOTE.w) - 40)).toBeLessThan(0.02);
    expect(Math.abs(onPaperY(REMOTE.h) - 75)).toBeLessThan(0.02);
    // The 100 mm control line beside it comes out the same way.
    expect(Math.abs(onPaperX(CONTROL_LENGTH) - 100)).toBeLessThan(0.05);
  });
});
