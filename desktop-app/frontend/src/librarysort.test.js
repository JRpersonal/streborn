import { describe, it, expect, beforeEach } from 'vitest';
import {
  compareNames, sortFolders, sortTracks, keepsTrackOrder, loadLibrarySort, saveLibrarySort,
  SORT_AZ, SORT_SERVER, LIB_SORT_KEY,
} from './librarysort.js';

// A minimal localStorage: vitest runs in node, without a DOM.
const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => { store.set(k, String(v)); },
  removeItem: (k) => { store.delete(k); },
  clear: () => store.clear(),
};

// The case list internal/webui/librarysort_test.go (libraryAZCases) pins: the
// speaker sorts a folder of folders it walks by the same rule (#1257).
const AZ_CASES = {
  in: ['beta', '_Inbox', '10 Years', '2 Unlimited', 'Alpha', 'Äpfel', '_archive',
    'track 10', 'track 2', 'Zebra', 'apple', 'Ölberg', 'Ostsee'],
  want: ['_archive', '_Inbox', '2 Unlimited', '10 Years', 'Alpha', 'Äpfel', 'apple',
    'beta', 'Ölberg', 'Ostsee', 'track 2', 'track 10', 'Zebra'],
};

describe('library sort order A to Z (#1257)', () => {
  it('puts "_" and digits first, ignores case and accents, counts numbers as numbers', () => {
    expect([...AZ_CASES.in].sort(compareNames)).toEqual(AZ_CASES.want);
  });

  it('sorts folders only in A to Z, and leaves the input alone', () => {
    const folders = AZ_CASES.in.map((title, i) => ({ id: `c${i}`, title }));
    expect(sortFolders(folders, SORT_SERVER)).toBe(folders);
    const az = sortFolders(folders, SORT_AZ);
    expect(az.map((c) => c.title)).toEqual(AZ_CASES.want);
    expect(folders[0].title).toBe('beta');
  });

  it('keeps an album in track order and a playlist in its own order', () => {
    const album = [{ title: 'Zed', trackNumber: 1 }, { title: 'Ace', trackNumber: 2 }];
    expect(sortTracks(album, SORT_AZ, '')).toBe(album);
    const loose = [{ title: 'Zed' }, { title: '_intro' }, { title: 'Ace' }];
    expect(sortTracks(loose, SORT_AZ, 'object.container.storageFolder').map((t) => t.title)).toEqual(['_intro', 'Ace', 'Zed']);
    expect(sortTracks(loose, SORT_AZ, 'object.container.playlistContainer')).toBe(loose);
    expect(sortTracks(loose, SORT_AZ, 'object.container.album.musicAlbum')).toBe(loose);
    expect(sortTracks(loose, SORT_SERVER, '')).toBe(loose);
    // Two tracks sharing a number are not an album's numbering.
    expect(keepsTrackOrder([{ trackNumber: 1 }, { trackNumber: 1 }], '')).toBe(false);
    expect(keepsTrackOrder([{ trackNumber: 1, discNumber: 1 }, { trackNumber: 1, discNumber: 2 }], '')).toBe(true);
  });

  describe('remembered per viewer', () => {
    beforeEach(() => localStorage.clear());
    it('defaults to the server order and remembers A to Z', () => {
      expect(loadLibrarySort()).toBe(SORT_SERVER);
      saveLibrarySort(SORT_AZ);
      expect(localStorage.getItem(LIB_SORT_KEY)).toBe('az');
      expect(loadLibrarySort()).toBe(SORT_AZ);
      saveLibrarySort('nonsense');
      expect(loadLibrarySort()).toBe(SORT_SERVER);
    });
  });
});
