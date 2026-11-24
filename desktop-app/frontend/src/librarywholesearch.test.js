// #1325: the Library searches the whole music library of every media server
// the app knows, narrowed to All, Album, Artist or Track, and it does so the
// way the phone page's Find tab does: the same four categories in the same
// order, the same row (title, then artist · album · server) and the same three
// notes (a server that did not answer, a partial list, nothing found).
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { allSource, srcOf } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const src = allSource();
const phone = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8');
const bundleDir = join(here, 'i18n', 'bundles');

// eslint-disable-next-line no-new-func
const note = new Function('t', `${srcOf('libraryWholeSearchNote')}\nreturn libraryWholeSearchNote;`)(
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
    // #1705: a walk cut short names its server; only too many hits asks for a more exact search.
    expect(note({ unanswered: [], incomplete: ['NAS'], partial: true, items: [{}] })).toBe('library.wholeSearchIncomplete:NAS');
    expect(note({ unanswered: ['PC'], incomplete: ['NAS'], partial: true, items: [] })).toBe('library.wholeSearchOffline:PC');
    expect(note({ unanswered: [], partial: false, items: [] })).toBe('library.wholeSearchNone');
    expect(note({ unanswered: [], partial: false, items: [{}] })).toBe('');
  });

  it('sends the server a hit came from with a key saved from it', () => {
    expect(src).toContain('const source = item.server || librarySourceName();');
  });

  it('has every string in every language', () => {
    const keys = ['wholeSearchPlaceholder', 'wholeSearchBtn', 'wholeSearching', 'searchIn', 'fieldAll',
      'fieldAlbum', 'fieldArtist', 'fieldTrack', 'wholeSearchSec', 'wholeSearchNone',
      'wholeSearchOffline', 'wholeSearchPartial', 'wholeSearchIncomplete', 'wholeSearchClose'].map((k) => `library.${k}`);
    for (const f of readdirSync(bundleDir).filter((n) => n.endsWith('.json'))) {
      const b = JSON.parse(readFileSync(join(bundleDir, f), 'utf8'));
      for (const k of keys) expect(b[k], `${f} ${k}`).toBeTruthy();
      expect(b['library.wholeSearchOffline'], f).toContain('{{name}}');
      expect(b['library.wholeSearchIncomplete'], f).toContain('{{name}}');
    }
  });
});

// #1705: a search that finds nothing, or finds something on one server only,
// says which servers it asked and which of them had nothing.
describe('the servers a whole-library search asked', () => {
  const coverage = (servers, s) => new Function('t', 'libState', `${srcOf('libraryWholeSearchCoverage')}\nreturn libraryWholeSearchCoverage;`)(
    (k, v) => `${k}:${v.names}`, { servers })({ loading: false, items: [], unanswered: [], incomplete: [], ...s });
  const SERVERS = [{ friendlyName: 'PC one' }, { friendlyName: 'PC two' }, { friendlyName: 'NAS' }, { address: 'http://192.0.2.1:8200/' }];

  it('names every server asked, and the ones with no hit', () => {
    expect(coverage(SERVERS, {})).toBe('library.wholeSearchAsked:PC one, PC two, NAS, http://192.0.2.1:8200/ library.wholeSearchNoHits:PC one, PC two, NAS, http://192.0.2.1:8200/');
    expect(coverage(SERVERS, { items: [{ server: 'NAS' }, { server: 'PC one' }] }))
      .toBe('library.wholeSearchAsked:PC one, PC two, NAS, http://192.0.2.1:8200/ library.wholeSearchNoHits:PC two, http://192.0.2.1:8200/');
  });

  it('leaves out a server that did not answer, and one cut short keeps its own note', () => {
    expect(coverage(SERVERS, { unanswered: ['PC two'], incomplete: ['NAS'], items: [{ server: 'PC one' }, { server: 'http://192.0.2.1:8200/' }] }))
      .toBe('library.wholeSearchAsked:PC one, NAS, http://192.0.2.1:8200/');
  });

  it('says nothing while searching or with no server', () => {
    expect(coverage(SERVERS, { loading: true })).toBe('');
    expect(coverage([], {})).toBe('');
  });

  it('shows the line in both layouts', () => {
    expect(src).toContain('library-search-coverage');
    expect(src).toContain('lib2-search-coverage');
  });
});
