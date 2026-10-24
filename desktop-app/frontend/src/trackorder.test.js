import { describe, it, expect } from 'vitest';
import { albumTrackOrder, isPlaylistClass, orderLongFolder } from './trackorder.js';

const tr = (title, trackNumber, discNumber) => ({ title, trackNumber, discNumber });
const names = (items) => items.map((x) => x.title).join(',');

// Mirror of dlna/trackorder_test.go (#1487): a long folder is ordered here,
// once every page is in, by the same rule the Go side applies to one page.
describe('albumTrackOrder', () => {
  it('puts an alphabetical album in track order', () => {
    const items = [tr('Angel', 3), tr('Black', 1), tr('Come', 2)];
    expect(names(albumTrackOrder(items))).toBe('Black,Come,Angel');
    expect(names(items)).toBe('Angel,Black,Come');
  });

  it('orders a multi-disc album disc first', () => {
    expect(names(albumTrackOrder([tr('A', 1, 2), tr('B', 2, 1), tr('C', 2, 2), tr('D', 1, 1)])))
      .toBe('D,B,A,C');
  });

  it('keeps the server order of a mixed folder, one without numbers, or one with duplicates', () => {
    for (const items of [
      [tr('B', 2), tr('A', 0), tr('C', 1)],
      [tr('B'), tr('A')],
      [tr('B', 2), tr('A', 1), tr('C', 1)],
    ]) {
      expect(albumTrackOrder(items)).toBe(items);
    }
  });
});

describe('orderLongFolder', () => {
  const items = [tr('B', 2), tr('A', 1)];
  it('orders an album folder', () => {
    expect(names(orderLongFolder(items, 'object.container.album.musicAlbum'))).toBe('A,B');
  });
  it('never re-orders a playlist, and keeps the server order when the class is unknown', () => {
    expect(isPlaylistClass('object.container.playlistContainer')).toBe(true);
    expect(orderLongFolder(items, 'object.container.playlistContainer')).toBe(items);
    expect(orderLongFolder(items, '')).toBe(items);
  });
});
