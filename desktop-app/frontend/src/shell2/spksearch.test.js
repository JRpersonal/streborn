// Finding a speaker in the UI 2.0 sidebar (spksearch.js): the field shows once
// the list is long, narrows the other speakers by name, model and what they
// play, keeps the pinned group whole, and says so when nothing matches.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  SEARCH_AFTER_ROWS, filterSidebarModel, foldText, noMatchHTML, nowLineOf, rowMatches, searchFieldHTML, searchShown,
  searchStatusText,
} from './spksearch.js';
import { sidebarHTML } from './speakers.js';
import { setLocale } from '../i18n/index.js';

setLocale('en');

const row = (n, name, extra = {}) => ({ host: `192.0.2.${n}`, port: 8888, hosts: [`192.0.2.${n}`], name, kind: 'str', model: '', ...extra });
const LR = row(20, 'Living room', { model: 'SoundTouch 20', playing: true, isMaster: true, pinned: true, isCurrent: true });
const KI = row(21, 'Kitchen', { model: 'SoundTouch 10', pinned: true });
const OTHERS = [
  row(30, 'Office', { model: 'SoundTouch 30' }),
  row(31, 'Bedroom', { model: 'SoundTouch 10' }),
  row(32, 'Küche oben', { model: 'SoundTouch Portable' }),
  row(33, 'Terrace (stereo)', { kind: 'pair', hosts: ['192.0.2.33', '192.0.2.34'] }),
  row(35, 'Hall'),
  row(36, 'Bath'),
  row(37, 'Studio'),
];
const MODEL = { pinned: [LR, KI], others: OTHERS, headingKey: 'ui2.side.together' };

// What each speaker plays, as mount.js hands it in (raw source, its friendly
// name, the station).
const NOW = {
  '192.0.2.31': ['SPOTIFY', 'Spotify', 'Morning Mix'],
  '192.0.2.34': ['LOCAL_INTERNET_RADIO', 'Internet radio', 'Jazz24'],
};
const terms = (h) => NOW[h] || [];

const hosts = (rows) => rows.map((r) => r.host);

describe('sidebar search: when it shows', () => {
  it('shows only once the list has more than eight rows, pinned rows counted', () => {
    expect(SEARCH_AFTER_ROWS).toBe(8);
    expect(searchShown(MODEL)).toBe(true);
    expect(searchShown({ pinned: [LR], others: OTHERS })).toBe(false);
    expect(searchShown({ pinned: [], others: [] })).toBe(false);
    expect(searchShown(null)).toBe(false);
  });
});

describe('sidebar search: matching', () => {
  it('folds case, accents, underscores and spaces', () => {
    expect(foldText('  Küche_OBEN  ')).toBe('kuche oben');
    expect(foldText('LOCAL_INTERNET_RADIO')).toBe('local internet radio');
    expect(foldText(null)).toBe('');
  });

  it('finds a speaker by its name, its model and what it plays', () => {
    expect(rowMatches(OTHERS[0], 'off', terms)).toBe(true);
    expect(rowMatches(OTHERS[0], 'soundtouch 30', terms)).toBe(true);
    expect(rowMatches(OTHERS[2], 'kuche', terms)).toBe(true);
    expect(rowMatches(OTHERS[2], 'portable', terms)).toBe(true);
    expect(rowMatches(OTHERS[1], 'spotify', terms)).toBe(true);
    expect(rowMatches(OTHERS[1], 'morning', terms)).toBe(true);
    expect(rowMatches(OTHERS[0], 'spotify', terms)).toBe(false);
  });

  it('reads every half of a stereo pair, and needs every word of the query', () => {
    // The right half plays: the pair row is found by it.
    expect(rowMatches(OTHERS[3], 'jazz', terms)).toBe(true);
    expect(rowMatches(OTHERS[3], 'internet radio', terms)).toBe(true);
    expect(rowMatches(OTHERS[3], 'jazz terrace', terms)).toBe(true);
    expect(rowMatches(OTHERS[3], 'jazz kitchen', terms)).toBe(false);
  });

  it('matches everything on an empty query, and survives a throwing lookup', () => {
    expect(rowMatches(OTHERS[0], '   ', terms)).toBe(true);
    expect(rowMatches(OTHERS[0], 'office', () => { throw new Error('boom'); })).toBe(true);
  });
});

describe('sidebar search: the model', () => {
  it('without a query leaves the model as it is', () => {
    const m = filterSidebarModel(MODEL, '', terms);
    expect(m.others).toBe(OTHERS);
    expect(m.search).toEqual({ query: '', total: 9, shown: 9, noMatch: false });
  });

  it('narrows the others in their own order and keeps the pinned group whole', () => {
    const m = filterSidebarModel(MODEL, 'soundtouch', terms);
    expect(hosts(m.pinned)).toEqual([LR.host, KI.host]);
    expect(hosts(m.others)).toEqual(['192.0.2.30', '192.0.2.31', '192.0.2.32']);
    expect(m.headingKey).toBe('ui2.side.together');
    expect(m.search).toEqual({ query: 'soundtouch', total: 9, shown: 5, noMatch: false });
  });

  it('a match only in the pinned group is a match', () => {
    const m = filterSidebarModel(MODEL, 'living', terms);
    expect(m.others).toEqual([]);
    expect(m.search.noMatch).toBe(false);
  });

  it('nothing anywhere is no match', () => {
    const m = filterSidebarModel(MODEL, 'garage', terms);
    expect(m.others).toEqual([]);
    expect(m.pinned).toHaveLength(2);
    expect(m.search).toEqual({ query: 'garage', total: 9, shown: 2, noMatch: true });
  });
});

describe('sidebar search: what it draws and says', () => {
  it('draws the no-match line and its Clear button inline, never the "no speakers" state', () => {
    const html = sidebarHTML(filterSidebarModel({ pinned: [], others: OTHERS }, 'garage <b>', terms));
    expect(html).toContain('No speaker matches &quot;garage &lt;b&gt;&quot;.');
    expect(html).toContain('data-spk-find-clear');
    expect(html).toContain('>Clear the search</button>');
    expect(html).not.toContain('ui2SpkState');
    expect(html).not.toContain('No speakers found yet.');
  });

  it('keeps the pinned group on screen above the no-match line', () => {
    const html = sidebarHTML(filterSidebarModel(MODEL, 'garage', terms));
    expect(html).toContain('ui2-spk-pinned');
    expect(html).toContain(`data-host="${LR.host}"`);
    expect(html.indexOf('ui2-spk-pinned')).toBeLessThan(html.indexOf('ui2-spk-nomatch'));
  });

  it('without a search the empty list still says why (discovery)', () => {
    const html = sidebarHTML(filterSidebarModel({ pinned: [], others: [], emptyPhase: '', searching: false }, '', terms));
    expect(html).toContain('No speakers found yet.');
    expect(html).not.toContain('ui2-spk-nomatch');
  });

  it('names the field and starts it hidden, outside the repainted lists', () => {
    const html = searchFieldHTML();
    expect(html).toMatch(/^<div class="ui2-spk-find" id="ui2SpkFind" hidden>/);
    expect(html).toContain('type="search"');
    expect(html).toContain('aria-label="Find a speaker by name, model or what it plays"');
    expect(html).toContain('placeholder="Find a speaker"');
    expect(html).toContain('aria-controls="ui2SpkLists"');
    expect(noMatchHTML('x')).toContain('No speaker matches &quot;x&quot;.');
  });

  it('tells the live region the count, or that nothing matches', () => {
    expect(searchStatusText(null)).toBe('');
    expect(searchStatusText({ query: '', shown: 9, total: 9 })).toBe('');
    expect(searchStatusText({ query: 'sound', shown: 5, total: 9, noMatch: false })).toBe('Speakers shown: 5 of 9');
    expect(searchStatusText({ query: 'garage', shown: 2, total: 9, noMatch: true })).toBe('No speaker matches "garage".');
  });
});

describe('sidebar search: what a speaker plays', () => {
  const xml = (source, inner = '') => `<?xml version="1.0"?><nowPlaying deviceID="AAA" source="${source}">`
    + `<ContentItem source="${source}">${inner}</ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>`;

  it('reads the source and the station, the station name winning over the item', () => {
    expect(nowLineOf(xml('LOCAL_INTERNET_RADIO', '<itemName>Rock &amp; Roll FM</itemName>')))
      .toEqual({ source: 'LOCAL_INTERNET_RADIO', station: 'Rock & Roll FM' });
    expect(nowLineOf(xml('TUNEIN', '<itemName>Song title</itemName>') .replace('</ContentItem>', '</ContentItem><stationName>Jazz24</stationName>')))
      .toEqual({ source: 'TUNEIN', station: 'Jazz24' });
    expect(nowLineOf(xml('BLUETOOTH'))).toEqual({ source: 'BLUETOOTH', station: '' });
  });

  it('has nothing for a sleeping speaker or a broken answer', () => {
    expect(nowLineOf(xml('STANDBY'))).toBeNull();
    expect(nowLineOf(xml('INVALID_SOURCE'))).toBeNull();
    expect(nowLineOf('')).toBeNull();
    expect(nowLineOf(undefined)).toBeNull();
  });
});

describe('sidebar search: wiring', () => {
  const src = (f) => readFileSync(new URL(f, import.meta.url), 'utf8');

  it('sits in the sidebar above the lists, styled with the 3:1 field border', () => {
    const markup = src('./markup.js');
    expect(markup.indexOf('searchFieldHTML()')).toBeGreaterThan(markup.indexOf('id="ui2SpkHead"'));
    expect(markup.indexOf('searchFieldHTML()')).toBeLessThan(markup.indexOf('id="ui2SpkLists"'));
    expect(src('./shell2.css')).toMatch(/html\.ui2 \.ui2-spk-q \{[^}]*border: 1px solid var\(--u-border\)/);
  });

  it('filters only what the list draws; Escape clears in the field and on a row', () => {
    const mount = src('./mount.js');
    expect(mount).toContain("filterSidebarModel(full, findShown ? spkQuery : '', nowTerms)");
    expect(mount).toContain('levelsText(full)');
    expect(mount).toMatch(/e\.key === 'Escape' && field\.value/);
    expect(mount).toMatch(/e\.key === 'Escape' && spkQuery/);
  });
});
