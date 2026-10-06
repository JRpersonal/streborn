import { describe, it, expect } from 'vitest';
import { pngBlob, copyScreenshot, scoreText, roundIsNew, newHighscore, announceHighscores } from './arcadeshare.js';

const PNG = 'data:image/png;base64,iVBORw0KGgo=';

describe('pngBlob', () => {
  it('decodes a PNG data URL', () => {
    const b = pngBlob(PNG);
    expect(b.type).toBe('image/png');
    expect(b.size).toBe(8);
  });
  it('refuses anything else', () => {
    expect(pngBlob('')).toBe(null);
    expect(pngBlob('data:text/html;base64,PGI+')).toBe(null);
  });
});

describe('copyScreenshot', () => {
  class Item { constructor(parts) { this.parts = parts; } }
  it('writes one image item', async () => {
    const written = [];
    const nav = { clipboard: { write: async (items) => { written.push(...items); } } };
    expect(await copyScreenshot(PNG, nav, Item)).toBe(true);
    expect(written).toHaveLength(1);
    expect(Object.keys(written[0].parts)).toEqual(['image/png']);
  });
  it('reports false where the webview cannot hold an image', async () => {
    expect(await copyScreenshot(PNG, {}, Item)).toBe(false);
    expect(await copyScreenshot(PNG, { clipboard: { write: async () => { throw new Error('denied'); } } }, Item)).toBe(false);
    expect(await copyScreenshot(PNG, { clipboard: { write: async () => {} } }, undefined)).toBe(false);
    expect(await copyScreenshot('', { clipboard: { write: async () => {} } }, Item)).toBe(false);
  });
});

describe('scoreText', () => {
  it('names the game, the model and both scores', () => {
    expect(scoreText({ id: 'blockfall', title: 'Blockfall', best: 1234, last: 800, rows: 9 }, 'SoundTouch Portable'))
      .toBe('My Blockfall highscore on the SoundTouch Portable: **1234 points**. Last round: 800 points, 9 rows.');
    expect(scoreText({ id: 'starguard', title: 'Starguard', best: 5, last: 5, rows: 2 }, ''))
      .toBe('My Starguard highscore on my SoundTouch: **5 points**. Last round: 5 points, 2 waves.');
  });
});

describe('highscore notice', () => {
  const now = 1_800_000_000;
  it('only reacts to a round newer than the last look', () => {
    const box = { deviceID: 'D1', arcadeAt: String(now - 30) };
    expect(roundIsNew(box, { D1: now - 30 }, now)).toBe(0);
    expect(roundIsNew(box, { D1: now - 600 }, now)).toBe(now - 30);
    expect(roundIsNew({ deviceID: 'D1' }, {}, now)).toBe(0);
  });
  it('a speaker seen for the first time sets a baseline unless its round is fresh', () => {
    expect(roundIsNew({ deviceID: 'D1', arcadeAt: String(now - 60) }, {}, now)).toBe(now - 60);
    expect(roundIsNew({ deviceID: 'D1', arcadeAt: String(now - 86400) }, {}, now)).toBe(-(now - 86400));
  });
  it('names the game of the latest round only when that round set the highscore', () => {
    const best = { id: 'starguard', title: 'STARGUARD', rounds: 2, best: 900, last: 900, bestAt: '2026-10-06T14:00:00Z', lastAt: '2026-10-06T14:00:00Z' };
    const older = { id: 'blockfall', title: 'BLOCKFALL', rounds: 4, best: 50, last: 50, bestAt: '2026-10-06T12:00:00Z', lastAt: '2026-10-06T12:00:00Z' };
    expect(newHighscore([older, best])).toEqual({ id: 'starguard', title: 'Starguard', best: 900 });
    const plain = { ...best, last: 300, lastAt: '2026-10-06T15:00:00Z' };
    expect(newHighscore([older, plain])).toBe(null);
  });
  it('opens the popup once per new highscore and remembers it', async () => {
    const store = new Map();
    const storage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) };
    const shown = [];
    const deps = {
      getArcade: async () => [{ id: 'blockfall', title: 'BLOCKFALL', rounds: 1, best: 29, last: 29, lastRows: 0, bestAt: 'x', lastAt: 'x' }],
      popup: (game, box) => shown.push(`${game.title} ${game.best} ${box.friendlyName}`),
      storage,
      nowSec: now,
    };
    const boxes = [{ deviceID: 'D1', host: '192.0.2.1', friendlyName: 'Portable', arcadeAt: String(now - 20) }];
    await announceHighscores(boxes, deps);
    await announceHighscores(boxes, deps);
    expect(shown).toEqual(['Blockfall 29 Portable']);
  });
});
