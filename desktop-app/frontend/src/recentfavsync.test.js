// A favourite removed on the phone page kept its star in the app's Recently
// played for about 1 min 14 s (#1339). The removal reached the app only through
// the once-a-minute full sync on the box-list refresh, and the view repainted
// only on its own 30 s timer after that. The open view now asks the selected
// speaker every few seconds and repaints the star the moment the list changes.
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

vi.mock('./api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  FavoriteRemovals: vi.fn(),
  PushFavorites: vi.fn(() => Promise.resolve()),
  PushFavoritesAt: vi.fn(() => Promise.resolve()),
}));

const api = await import('./api.js');
const { state } = await import('./state.js');
const fav = await import('./views/music/favorites.js');
const recent = await import('./views/recent.js');

function memoryStorage() {
  const m = new Map();
  return {
    getItem: (k) => (m.has(k) ? m.get(k) : null),
    setItem: (k, v) => m.set(k, String(v)),
    removeItem: (k) => m.delete(k),
  };
}

const BOX = { deviceID: 'DEV-A', host: '192.0.2.10', port: 8888, kind: 'str' };
const OTHER = { deviceID: 'DEV-B', host: '192.0.2.11', port: 8888, kind: 'str' };

beforeEach(() => {
  vi.stubGlobal('localStorage', memoryStorage());
  // No DOM in this suite: the Favourites button lookup finds nothing.
  vi.stubGlobal('document', { getElementById: () => null, visibilityState: 'visible' });
  localStorage.setItem('str.favStations', JSON.stringify([
    { stationuuid: 'u-1', name: '1LIVE', url: 'http://example/1live', starredAt: 100 },
    { stationuuid: 'u-2', name: 'WDR 2', url: 'http://example/wdr2', starredAt: 100 },
  ]));
  state.boxes = [BOX, OTHER];
  state.currentBox = BOX;
  state.searchLastMode = '';
  api.FavoriteRemovals.mockReset();
  api.PushFavorites.mockClear();
  api.PushFavoritesAt.mockReset();
  api.PushFavoritesAt.mockImplementation(() => Promise.resolve());
});

describe('pollFavRemovalsOnCurrentBox (#1339)', () => {
  it('takes a star removed on the phone off the list, asking only the selected speaker', async () => {
    api.FavoriteRemovals.mockResolvedValue('{"removed":[{"id":"u-1","at":500}]}');
    const seen = vi.fn();
    fav.onFavRemovalsApplied(seen);
    const changed = await fav.pollFavRemovalsOnCurrentBox();
    expect(changed).toBe(true);
    expect(api.FavoriteRemovals).toHaveBeenCalledTimes(1);
    expect(api.FavoriteRemovals).toHaveBeenCalledWith(BOX.host, BOX.port);
    expect(fav.isFav({ stationuuid: 'u-1' })).toBe(false);
    expect(fav.isFav({ stationuuid: 'u-2' })).toBe(true);
    expect(seen).toHaveBeenCalled();
    // The shorter list goes back out to every speaker, as on any change.
    expect(api.PushFavoritesAt).toHaveBeenCalledTimes(2);
  });

  it('changes nothing and pushes nothing when no star was removed', async () => {
    api.FavoriteRemovals.mockResolvedValue('{"removed":[]}');
    expect(await fav.pollFavRemovalsOnCurrentBox()).toBe(false);
    expect(api.PushFavoritesAt).not.toHaveBeenCalled();
    expect(api.PushFavorites).not.toHaveBeenCalled();
  });

  it('stays quiet for a stock speaker and a speaker that does not answer', async () => {
    state.currentBox = { ...BOX, kind: 'stock' };
    expect(await fav.pollFavRemovalsOnCurrentBox()).toBe(false);
    state.currentBox = BOX;
    api.FavoriteRemovals.mockRejectedValue(new Error('timeout'));
    expect(await fav.pollFavRemovalsOnCurrentBox()).toBe(false);
  });

  // #1621: the first star may come from a phone, so an empty list still asks.
  it('adds a favourite starred on the phone, even to an empty list', async () => {
    localStorage.setItem('str.favStations', '[]');
    api.FavoriteRemovals.mockResolvedValue(JSON.stringify({
      removed: [],
      added: [{ id: 'u-9', at: 700, fav: { stationuuid: 'u-9', name: 'SWR3', url_resolved: 'http://example/swr3' } }],
    }));
    expect(await fav.pollFavRemovalsOnCurrentBox()).toBe(true);
    expect(fav.isFav({ stationuuid: 'u-9' })).toBe(true);
    // Pushed to every speaker: that push settles the note.
    expect(api.PushFavoritesAt).toHaveBeenCalledTimes(2);
    expect(JSON.parse(api.PushFavoritesAt.mock.calls[0][2])[0]).toMatchObject({ stationuuid: 'u-9', starredAt: 700 });
  });

  // #1663: a Pandora station starred on the phone keeps what plays it.
  it('takes over a Pandora station starred on the phone with its station reference', async () => {
    api.FavoriteRemovals.mockResolvedValue(JSON.stringify({
      removed: [],
      added: [{ id: 'pandora:111', at: 800, fav: { stationuuid: 'pandora:111', name: 'Jazz Radio', source: 'pandora', location: '111', itemType: 'stationurl' } }],
    }));
    expect(await fav.pollFavRemovalsOnCurrentBox()).toBe(true);
    const pushed = JSON.parse(api.PushFavoritesAt.mock.calls[0][2]);
    expect(pushed[2]).toMatchObject({ stationuuid: 'pandora:111', source: 'pandora', location: '111', itemType: 'stationurl' });
  });

  // #1519: an order set on the phone after the app's own is taken over, and
  // the push names its time, which settles the note on the speakers.
  it('takes over an order set on the phone and pushes it with its time', async () => {
    api.FavoriteRemovals.mockResolvedValue('{"removed":[],"added":[],"order":{"at":900,"ids":["u-2","u-1"]}}');
    expect(await fav.pollFavRemovalsOnCurrentBox()).toBe(true);
    expect(fav.loadFavStore().map((s) => s.stationuuid)).toEqual(['u-2', 'u-1']);
    expect(api.PushFavoritesAt).toHaveBeenCalledTimes(2);
    expect(api.PushFavoritesAt.mock.calls[0][3]).toBe(900);
    // The same note again is old news.
    api.PushFavoritesAt.mockClear();
    expect(await fav.pollFavRemovalsOnCurrentBox()).toBe(false);
    expect(api.PushFavoritesAt).not.toHaveBeenCalled();
  });

  it('keeps its own newer order over an older one from a phone', async () => {
    localStorage.setItem('str.favOrderAt', '5000');
    api.FavoriteRemovals.mockResolvedValue('{"order":{"at":900,"ids":["u-2","u-1"]}}');
    expect(await fav.pollFavRemovalsOnCurrentBox()).toBe(false);
    expect(fav.loadFavStore().map((s) => s.stationuuid)).toEqual(['u-1', 'u-2']);
  });

  it('moves a favourite and pushes the new order with the time of the move', () => {
    const store = fav.loadFavStore();
    const before = Date.now();
    const next = fav.moveFavorite(store, 1, -1);
    expect(next.map((s) => s.stationuuid)).toEqual(['u-2', 'u-1']);
    expect(fav.loadFavStore().map((s) => s.stationuuid)).toEqual(['u-2', 'u-1']);
    expect(api.PushFavoritesAt).toHaveBeenCalledTimes(2);
    expect(api.PushFavoritesAt.mock.calls[0][3]).toBeGreaterThanOrEqual(before);
    expect(fav.moveFavorite(fav.loadFavStore(), 0, -1)).toBe(null);
  });

  it('falls back to the plain push on an app build without the newer binding', async () => {
    const missing = new Error('STR_MISSING_BINDING: PushFavoritesAt is not available in this build');
    missing.code = 'STR_MISSING_BINDING';
    api.PushFavoritesAt.mockImplementation(() => Promise.reject(missing));
    fav.moveFavorite(fav.loadFavStore(), 0, 1);
    await new Promise((r) => setTimeout(r, 0));
    expect(api.PushFavorites).toHaveBeenCalledTimes(2);
  });
});

describe('Recently played star repaint (#1339)', () => {
  function fakeButton(isFav) {
    const cls = new Set(isFav ? ['is-fav'] : []);
    return {
      innerHTML: isFav ? '&#9733;' : '&#9734;',
      title: '',
      classList: {
        contains: (c) => cls.has(c),
        toggle: (c, on) => { if (on) cls.add(c); else cls.delete(c); },
      },
    };
  }

  it('turns off the star of a card whose favourite was removed', () => {
    recent.initRecentView({ isFav: fav.isFav });
    recent.setPaintedRecentCards([
      { source: 'radio', name: '1LIVE', url: 'http://example/1live', cardKey: 'u-1' },
      { source: 'radio', name: 'WDR 2', url: 'http://example/wdr2', cardKey: 'u-2' },
    ]);
    const buttons = { recFav0: fakeButton(true), recFav1: fakeButton(true) };
    localStorage.setItem('str.favStations', JSON.stringify([{ stationuuid: 'u-2', name: 'WDR 2' }]));
    const n = recent.repaintRecentFavStars((id) => buttons[id]);
    expect(n).toBe(1);
    expect(buttons.recFav0.classList.contains('is-fav')).toBe(false);
    expect(buttons.recFav0.innerHTML).toBe('&#9734;');
    expect(buttons.recFav1.classList.contains('is-fav')).toBe(true);
  });

  it('polls only while the tab is open and the window is on screen', () => {
    expect(recent.recentFavPollDue({ view: 'recent', visibility: 'visible' })).toBe(true);
    expect(recent.recentFavPollDue({ view: 'recent', visibility: 'hidden' })).toBe(false);
    expect(recent.recentFavPollDue({ view: 'box', visibility: 'visible' })).toBe(false);
    expect(recent.RECENT_FAV_POLL_MS).toBeLessThanOrEqual(5000);
  });

  it('is wired: the view starts the quick check, main.js hands it in and repaints on a change', () => {
    const src = (p) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8');
    const rec = src('./views/recent.js');
    const render = rec.slice(rec.indexOf('export async function renderRecent('));
    expect(render).toContain('favTimer = setInterval(pollFav, RECENT_FAV_POLL_MS)');
    expect(rec).toMatch(/function stopRecentAutoRefresh\(\) \{[\s\S]*?clearInterval\(favTimer\)/);
    const main = src('./main.js');
    expect(main).toContain('pollFavRemovals: pollFavRemovalsOnCurrentBox');
    expect(main).toMatch(/onFavRemovalsApplied\(\(\) => \{ if \(state\.view === 'recent'\) repaintRecentFavStars\(\); \}\)/);
  });
});
