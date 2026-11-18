// Finding a speaker in the UI 2.0 sidebar (spksearch.js): the field shows once
// the list is long, narrows the other speakers by name, model and what they
// play, keeps the pinned group whole, and says so when nothing matches.
import { afterEach, describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  SEARCH_AFTER_ROWS, filterSidebarModel, foldText, noMatchHTML, nowLineOf, rowMatches, searchFieldHTML, searchShown,
  searchStatusText, sourceNames, speakerTerms,
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
  it('shows only once the list has more than ten rows, pinned rows counted', () => {
    expect(SEARCH_AFTER_ROWS).toBe(10);
    const more = (n) => Array.from({ length: n }, (_, i) => row(40 + i, `Room ${i}`, { model: 'SoundTouch 10' }));
    // Ten rows fit the default window whole: no field. Eleven: the field.
    expect(searchShown({ pinned: [LR, KI], others: [...OTHERS, ...more(10 - 2 - OTHERS.length)] })).toBe(false);
    expect(searchShown({ pinned: [LR, KI], others: [...OTHERS, ...more(11 - 2 - OTHERS.length)] })).toBe(true);
    expect(searchShown(MODEL)).toBe(false);
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

describe('sidebar search: what one speaker is found by (speakerTerms)', () => {
  afterEach(() => setLocale('en'));

  const box = (n, name, extra = {}) => ({ host: `192.0.2.${n}`, port: 8888, deviceID: `DEV${n}`, friendlyName: name, ...extra });
  const label = (b) => b.friendlyName;
  // The key tiles' English names, as main.js hands in boxSourceLabel.
  const sourceLabel = (s) => ({ LOCAL_INTERNET_RADIO: 'Internet radio', SPOTIFY: 'Spotify', STORED_MUSIC: 'Library' })[s] || s;
  const CUR = box(1, 'Living room');
  const OTHER = box(2, 'Bedroom');
  const OFF = box(3, 'Office', { offline: true });
  const base = (extra = {}) => ({
    boxes: [CUR, OTHER, OFF],
    currentBox: CUR,
    nowSource: 'LOCAL_INTERNET_RADIO',
    nowName: 'Jazz24',
    boxNowLine: {
      DEV1: { source: 'SPOTIFY', station: 'Stale poll' },
      DEV2: { source: 'SPOTIFY', station: 'Morning Mix' },
      DEV3: { source: 'SPOTIFY', station: 'Before it went' },
    },
    ...extra,
  });
  const termsOf = (host, s) => speakerTerms(host, s, { label, sourceLabel });

  it('the speaker you control reads its own status poll, not the every-speaker poll', () => {
    expect(termsOf(CUR.host, base())).toEqual(['Living room', 'LOCAL_INTERNET_RADIO', 'Internet radio', 'Jazz24']);
  });

  it('another speaker reads the every-speaker poll', () => {
    expect(termsOf(OTHER.host, base())).toEqual(['Bedroom', 'SPOTIFY', 'Spotify', 'Spotify Connect', 'Morning Mix']);
  });

  it('a controlled speaker in standby or without a source plays nothing to find it by', () => {
    expect(termsOf(CUR.host, base({ nowSource: 'STANDBY' }))).toEqual(['Living room']);
    expect(termsOf(CUR.host, base({ nowSource: 'INVALID_SOURCE' }))).toEqual(['Living room']);
  });

  it('before its first status answer (nowSource reset to empty) it falls back to the every-speaker poll', () => {
    expect(termsOf(CUR.host, base({ nowSource: '', nowName: '' }))).toEqual(['Living room', 'SPOTIFY', 'Spotify', 'Spotify Connect', 'Stale poll']);
    expect(termsOf(CUR.host, base({ nowSource: undefined, boxNowLine: undefined }))).toEqual(['Living room']);
  });

  it('an unreachable speaker keeps its name and nothing it played', () => {
    expect(termsOf(OFF.host, base())).toEqual(['Office']);
  });

  it('an unknown host has nothing, and no state is no crash', () => {
    expect(termsOf('192.0.2.99', base())).toEqual([]);
    expect(speakerTerms(CUR.host, null)).toEqual([]);
    expect(speakerTerms(CUR.host, base())).toEqual(['LOCAL_INTERNET_RADIO', 'Internet radio', 'Jazz24']);
  });

  it('names a source in the app language as well as in English', () => {
    setLocale('de');
    expect(sourceNames('LOCAL_INTERNET_RADIO', sourceLabel)).toEqual(['Internet radio', 'Internetradio']);
    expect(sourceNames('INTERNET_RADIO')).toEqual(['Internetradio']);
    expect(sourceNames('STORED_MUSIC', sourceLabel)).toEqual(['Library', 'Musikserver', 'Mediathek']);
    expect(sourceNames('')).toEqual([]);
    // A German user finds the speaker on internet radio by the German word.
    const row1 = row(1, 'Living room');
    expect(rowMatches(row1, 'internetradio', (h) => termsOf(h, base()))).toBe(true);
    expect(rowMatches(row1, 'internet radio', (h) => termsOf(h, base()))).toBe(true);
    expect(rowMatches(row1, 'local_internet_radio', (h) => termsOf(h, base()))).toBe(true);
    setLocale('ja');
    expect(rowMatches(row1, 'インターネットラジオ', (h) => termsOf(h, base()))).toBe(true);
  });

  it('survives a throwing English label', () => {
    expect(sourceNames('LOCAL_INTERNET_RADIO', () => { throw new Error('boom'); })).toEqual(['Internet radio']);
  });

  it('finds a stereo pair stored under its own name by the name of either half', () => {
    const L = box(40, 'Kitchen left');
    const Rt = box(41, 'Window');
    const s = { boxes: [L, Rt], currentBox: null, boxNowLine: {} };
    const pair = row(40, 'Terrace (stereo)', { kind: 'pair', hosts: [L.host, Rt.host] });
    const model = { pinned: [], others: [pair, row(42, 'Hall')] };
    const t2 = (h) => termsOf(h, s);
    expect(hosts(filterSidebarModel(model, 'kitchen', t2).others)).toEqual([L.host]);
    expect(hosts(filterSidebarModel(model, 'window', t2).others)).toEqual([L.host]);
    expect(hosts(filterSidebarModel(model, 'terrace', t2).others)).toEqual([L.host]);
    expect(filterSidebarModel(model, 'garage', t2).search.noMatch).toBe(true);
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
    expect(mount).toContain("filterSidebarModel(full, findShown ? spkQuery : '', findTerms)");
    expect(mount).toContain('speakerTerms(host, state, { label, sourceLabel: deps.sourceLabel })');
    // An input method's keys (Down picks a candidate, Escape cancels) stay with it.
    expect(mount).toContain('if (e.isComposing || e.keyCode === 229) return;');
    expect(mount).toContain('levelsText(full)');
    expect(mount).toMatch(/e\.key === 'Escape' && field\.value/);
    expect(mount).toMatch(/e\.key === 'Escape' && spkQuery/);
  });
});
