import { describe, it, expect } from 'vitest';
import { pngBlob, copyScreenshot, scoreText } from './arcadeshare.js';

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
