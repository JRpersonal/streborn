import { describe, it, expect } from 'vitest';
import { parseFavRemovals, applyFavRemovals, parseFavAdditions, applyFavAdditions } from './favremovals.js';

const favId = (s) => s && (s.stationuuid || (s.name + '|' + (s.url || '')));

describe('parseFavRemovals', () => {
  it('reads the notes a speaker answers', () => {
    expect(parseFavRemovals('{"removed":[{"id":"u-1","at":5}]}')).toEqual([{ id: 'u-1', at: 5 }]);
  });
  it('treats an older agent (plain list) and junk as no notes', () => {
    expect(parseFavRemovals('[{"name":"x"}]')).toEqual([]);
    expect(parseFavRemovals('not json')).toEqual([]);
    expect(parseFavRemovals('{"removed":[{"id":""},{"at":3},null]}')).toEqual([]);
  });
});

describe('applyFavRemovals (#1389)', () => {
  const store = [
    { stationuuid: 'u-1', name: '1LIVE' },
    { name: 'NAS folder', url: 'http://192.0.2.5:8200/folder', starredAt: 100 },
    { stationuuid: 'u-3', name: 'WDR 2', starredAt: 900 },
  ];

  it('drops a favourite removed on the phone after it was starred', () => {
    const { kept, changed } = applyFavRemovals(store, [
      { id: 'u-1', at: 50 },
      { id: 'NAS folder|http://192.0.2.5:8200/folder', at: 500 },
    ], favId);
    expect(changed).toBe(true);
    expect(kept.map((s) => s.name)).toEqual(['WDR 2']);
  });

  it('keeps a favourite starred again after the removal', () => {
    const { kept, changed } = applyFavRemovals(store, [{ id: 'u-3', at: 500 }], favId);
    expect(changed).toBe(false);
    expect(kept).toHaveLength(3);
  });

  it('uses the newest note when several speakers noted the same key', () => {
    const { kept } = applyFavRemovals(store, [{ id: 'u-3', at: 500 }, { id: 'u-3', at: 1000 }], favId);
    expect(kept.map((s) => s.name)).toEqual(['1LIVE', 'NAS folder']);
  });

  it('leaves the list alone without notes', () => {
    const { kept, changed } = applyFavRemovals(store, [], favId);
    expect(changed).toBe(false);
    expect(kept).toBe(store);
  });
});

describe('favourites starred elsewhere (#1621)', () => {
  const swr3 = { stationuuid: 'u-9', name: 'SWR3', url_resolved: 'http://example/swr3' };
  const store = [{ stationuuid: 'u-1', name: '1LIVE', starredAt: 100 }];

  it('reads the addition notes, and nothing from an older agent or junk', () => {
    expect(parseFavAdditions(JSON.stringify({ removed: [], added: [{ id: 'u-9', at: 700, fav: swr3 }] })))
      .toEqual([{ id: 'u-9', at: 700, fav: swr3 }]);
    expect(parseFavAdditions('{"removed":[]}')).toEqual([]);
    expect(parseFavAdditions('[{"name":"x"}]')).toEqual([]);
    expect(parseFavAdditions('{"added":[{"id":"u-9","at":1},{"id":"x","at":2,"fav":{}}]}')).toEqual([]);
  });

  it('adds a favourite starred on a phone, with the star time', () => {
    const { kept, changed } = applyFavAdditions(store, [{ id: 'u-9', at: 700, fav: swr3 }], [], favId);
    expect(changed).toBe(true);
    expect(kept.map((s) => s.name)).toEqual(['1LIVE', 'SWR3']);
    expect(kept[1].starredAt).toBe(700);
  });

  it('leaves alone what the app already has, and adds a station once across speakers', () => {
    const notes = [{ id: 'u-9', at: 700, fav: swr3 }, { id: 'u-9', at: 700, fav: swr3 }];
    expect(applyFavAdditions(store, notes, [], favId).kept).toHaveLength(2);
    expect(applyFavAdditions([...store, swr3], notes, [], favId).changed).toBe(false);
  });

  it('lets a removal noted after the star win', () => {
    const { changed } = applyFavAdditions(store, [{ id: 'u-9', at: 700, fav: swr3 }], [{ id: 'u-9', at: 800 }], favId);
    expect(changed).toBe(false);
  });
});
