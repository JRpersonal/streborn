// A NAS owner opened "by Album" in the Library tab, got 2500 albums and the note
// "Showing the first 2500 tracks. Use the search to find a specific one." There
// was no search anywhere on that screen, and they were albums, not tracks (#1026).
//
// Three separate slips, all in the same view: the search row was tied to the
// number of TRACKS, so a list of only sub-folders never got one; the filter only
// ever matched tracks, so it could not have narrowed an album list even when it
// was visible; and the cap counts folders and tracks together while the note
// named only tracks.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(join(here, 'views', 'library.js'), 'utf8').replace(/\r\n/g, '\n');
const en = JSON.parse(readFileSync(join(here, 'i18n', 'bundles', 'en.json'), 'utf8'));
const de = JSON.parse(readFileSync(join(here, 'i18n', 'bundles', 'de.json'), 'utf8'));

// The real helpers, sliced out of the view and run against a fake libState.
function build(page, filter) {
  const libState = { page, filter };
  const cut = (name) => {
    const at = src.indexOf(`function ${name}(`);
    return src.slice(at, src.indexOf('\n}\n', at) + 2);
  };
  // eslint-disable-next-line no-new-func
  return new Function('libState', `${cut('libraryFilteredItems')}\n${cut('libraryFilteredContainers')}\n${cut('cappedKey')}\n${cut('libraryCountText')}\nreturn { libraryFilteredItems, libraryFilteredContainers, cappedKey, libraryCountText };`)(libState);
}

const albums = {
  containers: [{ id: '1', title: 'Abbey Road' }, { id: '2', title: 'Synchronicity' }, { id: '3', title: 'Ten' }],
  items: [],
};
const tracks = {
  containers: [],
  items: [
    { id: 't1', title: 'Come Together', artist: 'The Beatles', album: 'Abbey Road' },
    { id: 't2', title: 'Every Breath You Take', artist: 'The Police', album: 'Synchronicity' },
  ],
};

describe('the folder search in a list of albums', () => {
  it('narrows the folders, which it never did before', () => {
    const lib = build(albums, 'synchro');
    expect(lib.libraryFilteredContainers().map((c) => c.title)).toEqual(['Synchronicity']);
  });

  it('still narrows tracks by title, artist and album', () => {
    expect(build(tracks, 'beatles').libraryFilteredItems().map((i) => i.title)).toEqual(['Come Together']);
    expect(build(tracks, 'synchronicity').libraryFilteredItems().map((i) => i.title)).toEqual(['Every Breath You Take']);
    expect(build(tracks, 'come').libraryFilteredItems().map((i) => i.title)).toEqual(['Come Together']);
  });

  it('is offered whenever the folder has rows at all', () => {
    // The row used to be rendered only for nItems > 0, which is exactly the case
    // the reporter did not have.
    expect(src).toContain('const searchRow = (nItems > 0 || nContainers > 0)');
  });
});

describe('the counts and the capped note match what is on screen', () => {
  it('counts folders as rows', () => {
    // The old track-only count read "0" next to a screen full of albums.
    expect(build(albums, '').libraryCountText()).toBe('3');
    expect(build(albums, 'synchro').libraryCountText()).toBe('1 / 3');
  });

  it('names folders, tracks or entries, whichever the folder holds', () => {
    const lib = build(albums, '');
    expect(lib.cappedKey(2500, 0)).toBe('library.cappedFolders');
    expect(lib.cappedKey(0, 2500)).toBe('library.capped');
    expect(lib.cappedKey(40, 2460)).toBe('library.cappedMixed');
  });

  it('has the wording in the bundles', () => {
    for (const b of [en, de]) {
      expect(b['library.cappedFolders']).toBeTruthy();
      expect(b['library.cappedMixed']).toBeTruthy();
      expect(b['library.cappedFolders']).toContain('{{n}}');
      expect(b['library.cappedMixed']).toContain('{{n}}');
    }
    // The folder wording must not call them tracks again.
    expect(en['library.cappedFolders']).not.toMatch(/track/i);
    expect(de['library.cappedFolders']).not.toMatch(/Titel/);
  });
});
