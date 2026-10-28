// Recently played and Spotify (#1077): a spotify: card is a Spotify card even
// when an older agent filed it as radio, and a star set on such a card is no
// station favourite.
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { groupRecentCards } from './views/recent.js';
import { isSpotifyFav, loadFavStore } from './views/music/favorites.js';

const BOX = { deviceID: 'DEV-A', host: '192.0.2.10', port: 8888 };
const KEY = 'spotify:playlist:abc';

function row(extra) {
  return { _box: BOX, _boxKey: 'DEV-A', _boxName: 'Kitchen', cardKey: KEY, cardName: 'Gypsy Soul', ts: 't1', ...extra };
}

describe('a spotify: card is a Spotify card', () => {
  it('relabels a row an older agent filed as radio and finds its key', () => {
    const cards = groupRecentCards([row({ source: 'radio', _spotifySlots: { [KEY]: 4 } })]);
    expect(cards[0].source).toBe('spotify');
    expect(cards[0].playSlot).toBe(4);
  });
  it('leaves a real radio card alone', () => {
    const cards = groupRecentCards([row({ source: 'radio', cardKey: 'https://stream.example/swr3' })]);
    expect(cards[0].source).toBe('radio');
  });
});

describe('a star on a Spotify playlist is dropped', () => {
  beforeEach(() => {
    const m = new Map();
    vi.stubGlobal('localStorage', {
      getItem: (k) => (m.has(k) ? m.get(k) : null),
      setItem: (k, v) => m.set(k, String(v)),
      removeItem: (k) => m.delete(k),
    });
  });
  it('spots it by its spotify: address', () => {
    expect(isSpotifyFav({ url: KEY, url_resolved: KEY })).toBe(true);
    expect(isSpotifyFav({ url: 'https://stream.example/swr3' })).toBe(false);
  });
  it('the store leaves it out', () => {
    localStorage.setItem('str.favStations', JSON.stringify([
      { stationuuid: KEY, name: 'Gypsy Soul', url: KEY, url_resolved: KEY },
      { stationuuid: 'u1', name: 'SWR3', url: 'https://stream.example/swr3' },
    ]));
    expect(loadFavStore().map((s) => s.name)).toEqual(['SWR3']);
  });
});
