// favorder.test.js: moving the starred stations and taking over an order set on
// a speaker's phone page (#1519).
import { describe, it, expect } from 'vitest';
import { applyFavOrder, moveFav, newestFavOrder, parseFavOrder } from './favorder.js';

const favId = (s) => s && (s.stationuuid || (s.name + '|' + (s.url || '')));
const st = (id) => ({ stationuuid: id, name: id.toUpperCase() });
const ids = (list) => list.map(favId).join(',');

describe('moveFav', () => {
  const store = ['a', 'b', 'c', 'd'].map(st);

  it('moves an entry one place up or down', () => {
    expect(ids(moveFav(store, store, 2, -1, favId).kept)).toBe('a,c,b,d');
    expect(ids(moveFav(store, store, 0, 1, favId).kept)).toBe('b,a,c,d');
  });

  it('does nothing at either end', () => {
    expect(moveFav(store, store, 0, -1, favId).changed).toBe(false);
    expect(moveFav(store, store, 3, 1, favId).changed).toBe(false);
  });

  it('moves past its neighbour on screen when the screen shows a filtered list', () => {
    // b is hidden by a filter: d moved up lands right before a, its neighbour on screen.
    const shown = [store[0], store[2], store[3]];
    expect(ids(moveFav(store, shown, 2, -1, favId).kept)).toBe('a,b,d,c');
    expect(ids(moveFav(store, shown, 0, 1, favId).kept)).toBe('b,c,a,d');
  });

  it('leaves the store alone', () => {
    const copy = store.slice();
    moveFav(store, store, 1, 1, favId);
    expect(store).toEqual(copy);
  });
});

describe('parseFavOrder', () => {
  it('reads the order note', () => {
    expect(parseFavOrder('{"removed":[],"added":[],"order":{"at":5,"ids":["b","a",""]}}')).toEqual({ at: 5, ids: ['b', 'a'] });
  });

  it('reads no note from an agent before #1519, a null note, or junk', () => {
    expect(parseFavOrder('{"removed":[],"added":[]}')).toBe(null);
    expect(parseFavOrder('{"order":null}')).toBe(null);
    expect(parseFavOrder('[{"name":"x"}]')).toBe(null);
    expect(parseFavOrder('not json')).toBe(null);
    expect(parseFavOrder('{"order":{"at":"x","ids":["a"]}}')).toBe(null);
  });
});

describe('newestFavOrder', () => {
  it('picks the newest note of all speakers', () => {
    expect(newestFavOrder([null, { at: 3, ids: ['a'] }, { at: 9, ids: ['b'] }, null])).toEqual({ at: 9, ids: ['b'] });
    expect(newestFavOrder([null, null])).toBe(null);
  });
});

describe('applyFavOrder', () => {
  const store = ['a', 'b', 'c', 'd'].map(st);

  it('puts the named entries in the noted order, the others after them', () => {
    const r = applyFavOrder(store, ['c', 'x', 'a'], favId);
    expect(r.changed).toBe(true);
    expect(ids(r.kept)).toBe('c,a,b,d');
  });

  it('reports no change when the order already matches', () => {
    const r = applyFavOrder(store, ['a', 'b'], favId);
    expect(r.changed).toBe(false);
    expect(r.kept).toBe(store);
  });
});
