// #1325: the Library searches the whole music library of every media server
// the app knows, narrowed to All, Album, Artist or Track, and it does so the
// way the phone page's Find tab does: the same four categories in the same
// order, the same row (title, then artist · album · server) and the same three
// notes (a server that did not answer, a partial list, nothing found).
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(join(here, 'views', 'library.js'), 'utf8').replace(/\r\n/g, '\n');
const phone = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8');
const bundleDir = join(here, 'i18n', 'bundles');

function cut(name) {
  const at = src.indexOf(`function ${name}(`);
  return src.slice(at, src.indexOf('\n}\n', at) + 2);
}

// eslint-disable-next-line no-new-func
const note = new Function('t', `${cut('libraryWholeSearchNote')}\nreturn libraryWholeSearchNote;`)(
  (k, v) => (v ? `${k}:${v.name}` : k));

describe('the whole-library search', () => {
  it('offers the phone page categories, in the phone page order', () => {
    const desktop = [...src.matchAll(/\['(any|album|artist|title)', 'library\.field/g)].map((m) => m[1]);
    const onPhone = [...phone.matchAll(/<option value="(any|album|artist|title)">/g)].map((m) => m[1]);
    expect(desktop).toEqual(['any', 'album', 'artist', 'title']);
    expect(onPhone).toEqual(desktop);
  });

  it('names the server on every row, after the artist and the album', () => {
    expect(src).toContain("[it.artist, it.album, it.server].filter(Boolean).join(' · ')");
    expect(phone).toContain("[it.artist, it.album, it.server].filter(Boolean).join(' · ')");
  });

  it('says which of the three it is, in the phone page order', () => {
    expect(note({ unanswered: ['NAS'], partial: true, items: [] })).toBe('library.wholeSearchOffline:NAS');
    expect(note({ unanswered: [], partial: true, items: [{}] })).toBe('library.wholeSearchPartial');
    expect(note({ unanswered: [], partial: false, items: [] })).toBe('library.wholeSearchNone');
    expect(note({ unanswered: [], partial: false, items: [{}] })).toBe('');
  });

  it('sends the server a hit came from with a key saved from it', () => {
    expect(src).toContain('const source = item.server || librarySourceName();');
  });

  it('has every string in every language', () => {
    const keys = ['wholeSearchPlaceholder', 'wholeSearchBtn', 'wholeSearching', 'searchIn', 'fieldAll',
      'fieldAlbum', 'fieldArtist', 'fieldTrack', 'wholeSearchSec', 'wholeSearchNone',
      'wholeSearchOffline', 'wholeSearchPartial', 'wholeSearchClose'].map((k) => `library.${k}`);
    for (const f of readdirSync(bundleDir).filter((n) => n.endsWith('.json'))) {
      const b = JSON.parse(readFileSync(join(bundleDir, f), 'utf8'));
      for (const k of keys) expect(b[k], `${f} ${k}`).toBeTruthy();
      expect(b['library.wholeSearchOffline'], f).toContain('{{name}}');
    }
  });
});
