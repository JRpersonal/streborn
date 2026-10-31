// The UI 2.0 preview's Radio (#1286 phase 2): the station rows and their
// details panel (ui2stations.js), the preview-only styles (ui2radio.css) and
// the gates that keep the current layout exactly as it was.
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { setLocale } from '../../i18n/index.js';
import { fileSource, srcOf } from '../../test/source.js';
import {
  genreLine,
  keyHoldsStation,
  rowStatusHTML,
  stationBroken,
  stationDetails,
  stationKey,
  stationRowHTML,
  tabFor,
  discoverFiltersApply,
} from './ui2stations.js';

setLocale('en');

const ROCK = {
  name: 'Best Of Rock.FM', url: 'http://example.com/rock', url_resolved: 'http://example.com/rock/hi',
  stationuuid: 's1', country: 'Germany', countrycode: 'DE', bitrate: 256, votes: 4210, clicktrend: 12,
  codec: 'MP3', tags: 'rock,alternative,metal', homepage: 'https://rock.example', lastcheckok: 1,
};

describe('station rows', () => {
  const html = stationRowHTML(ROCK, 3, { speaker: 'Living Room', fav: false, open: false, status: null });

  it('names the station and the speaker in every action', () => {
    expect(html).toContain('aria-label="Play Best Of Rock.FM on Living Room"');
    expect(html).toContain('aria-label="Add Best Of Rock.FM to favorites"');
    expect(html).toContain('aria-label="Put on key: Best Of Rock.FM on Living Room"');
    expect(html).toContain('aria-label="Details of Best Of Rock.FM"');
    expect(html).toContain('<span class="u-keybtn-text">Put on key</span></button>');
  });

  it('keeps favorite and key as two separate buttons', () => {
    expect(html).toMatch(/data-act="fav"[^>]*aria-pressed="false"/);
    expect(html).toContain('data-act="key"');
  });

  it('keeps the technical data out of the row and in the closed details panel', () => {
    const main = html.slice(0, html.indexOf('ui2-st-details'));
    for (const tech of ['256 kbit/s', 'MP3', '4.210', 'example.com/rock', 'Germany']) expect(main).not.toContain(tech);
    const details = html.slice(html.indexOf('ui2-st-details'));
    expect(details).toContain(' hidden>');
    for (const tech of ['256 kbit/s', 'MP3', '4.210', 'http://example.com/rock/hi', 'Germany', 'https://rock.example']) {
      expect(details).toContain(tech);
    }
    expect(html).toMatch(/aria-expanded="false" aria-controls="ui2StDetails3"/);
  });

  it('opens the details and shows a star when asked', () => {
    const open = stationRowHTML(ROCK, 0, { speaker: 'X', fav: true, open: true });
    expect(open).toMatch(/id="ui2StDetails0" role="region" aria-labelledby="ui2StName0">/);
    expect(open).toContain('aria-pressed="true"');
    expect(open).toContain('aria-label="Remove Best Of Rock.FM from favorites"');
  });

  it('shows the genres, at most two', () => {
    expect(genreLine(ROCK)).toBe('Rock, Alternative');
    expect(genreLine({ synthetic: true })).toBe('Play this stream URL');
    expect(genreLine({ name: 'x' })).toBe('');
  });

  it('says in the row when the directory could not reach a station, in words', () => {
    const bad = { ...ROCK, lastcheckok: 0 };
    expect(stationBroken(bad)).toBe(true);
    expect(stationBroken(ROCK)).toBe(false);
    expect(stationBroken({ synthetic: true })).toBe(false);
    expect(stationRowHTML(bad, 0, {})).toContain('Not reachable at the last directory check');
    expect(stationRowHTML(bad, 0, {})).toContain('class="ui2-st is-broken"');
  });

  it('carries the row status as text with its kind', () => {
    expect(rowStatusHTML({ kind: 'error', text: 'Did not play <x>' }))
      .toBe('<span class="ui2-st-status is-error" role="status">Did not play &lt;x&gt;</span>');
    expect(rowStatusHTML(null)).toBe('<span class="ui2-st-status" role="status"></span>');
    expect(stationRowHTML(ROCK, 0, { status: { kind: 'ok', text: 'Playing on Kitchen' } }))
      .toContain('is-ok" role="status">Playing on Kitchen<');
  });

  it('escapes what the directory sends', () => {
    const evil = { ...ROCK, name: '<img src=x onerror=alert(1)>', homepage: 'javascript:alert(1)' };
    const out = stationRowHTML(evil, 0, { speaker: '"Kitchen"' });
    expect(out).not.toContain('<img src=x');
    expect(out).not.toContain('javascript:');
    expect(out).toContain('&quot;Kitchen&quot;');
  });
});

describe('station details', () => {
  it('lists format and bitrate even when unknown, and leaves out what is missing otherwise', () => {
    const d = stationDetails({ name: 'n', url: 'http://a/b', codec: 'UNKNOWN', hls: 1 });
    const labels = d.rows.map((r) => r[0]);
    expect(labels).toEqual(['Format', 'Bitrate', 'Last check']);
    expect(d.rows[0][1]).toBe('Unknown (HLS)');
    expect(d.rows[1][1]).toBe('Unknown');
    expect(d.stream).toBe('http://a/b');
    expect(d.website).toBe('');
  });

  it('shows the trend as a signed number', () => {
    expect(stationDetails(ROCK).rows.find((r) => r[0] === 'Trend')[1]).toBe('+12');
    expect(stationDetails({ ...ROCK, clicktrend: -3 }).rows.find((r) => r[0] === 'Trend')[1]).toBe('-3');
  });

  it('has no directory data for a pasted stream address', () => {
    const d = stationDetails({ synthetic: true, name: 'x', url: 'http://s' });
    expect(d.rows.map((r) => r[0])).toEqual(['Format', 'Bitrate']);
  });
});

describe('helpers', () => {
  it('keys a station by uuid, else name and address', () => {
    expect(stationKey(ROCK)).toBe('s1');
    expect(stationKey({ name: 'A', url: 'u' })).toBe('A|u');
    expect(stationKey(null)).toBe('');
  });

  it('maps the search mode to a tab', () => {
    expect(tabFor('favorites')).toBe('favorites');
    for (const m of ['top', 'search', 'pandora', '', undefined]) expect(tabFor(m)).toBe('discover');
  });

  it('keeps the hidden Discover filters off the preview Favorites tab only', () => {
    expect(discoverFiltersApply(true, 'favorites')).toBe(false);
    expect(discoverFiltersApply(true, 'search')).toBe(true);
    expect(discoverFiltersApply(true, 'top')).toBe(true);
    // The current layout shows the filters above favourites and applies them.
    expect(discoverFiltersApply(false, 'favorites')).toBe(true);
  });

  it('calls a key done only when the speaker read back holds the station there', () => {
    const url = ROCK.url_resolved;
    expect(keyHoldsStation([{ slot: 2, stream_url: url, name: 'other' }], 2, ROCK, url)).toBe(true);
    expect(keyHoldsStation([{ slot: 2, stream_url: 'x', name: ROCK.name }], 2, ROCK, url)).toBe(true);
    expect(keyHoldsStation([{ slot: 3, stream_url: url, name: ROCK.name }], 2, ROCK, url)).toBe(false);
    expect(keyHoldsStation([{ slot: 2, stream_url: 'x', name: 'y' }], 2, ROCK, url)).toBe(false);
    expect(keyHoldsStation(null, 2, ROCK, url)).toBe(false);
  });
});

describe('the current layout stays as it was', () => {
  const css = readFileSync(new URL('./ui2radio.css', import.meta.url), 'utf8');

  it('scopes every rule of ui2radio.css under html.ui2 and reads only the --u-* tokens', () => {
    const noComments = css.replace(/\/\*[\s\S]*?\*\//g, '');
    const selectors = [];
    for (const m of noComments.matchAll(/([^{}]+)\{/g)) {
      const sel = m[1].trim();
      if (sel.startsWith('@')) continue;
      for (const s of sel.split(/,(?![^(]*\))/)) selectors.push(s.trim());
    }
    expect(selectors.length).toBeGreaterThan(30);
    for (const s of selectors) expect(s, s).toMatch(/^html\.ui2($|[\s.:])/);
    expect(css).not.toMatch(/var\(--c-/);
  });

  it('hands the rows to the preview only while the shell is mounted', () => {
    const render = srcOf('renderSearchResults');
    expect(render).toMatch(/if \(state\.ui2Shell\) \{\s*renderUi2Stations\(/);
  });

  it('keeps the toast for every play failure outside the preview', () => {
    const play = srcOf('playStation', { file: 'views/music/search.js' });
    expect(play).toContain("typeof options.report === 'function' ? options.report : (msg) => showToast(msg)");
    expect(play).not.toMatch(/showToast\(t\('search\.(allSourcesFailed|tryingAlternative)'/);
  });

  it('does nothing in ui2radio.js before the shell is mounted', () => {
    const src = fileSource('views/music/ui2radio.js');
    for (const fn of ['ensureUi2Radio', 'syncUi2Keys', 'syncUi2RadioTabs']) {
      expect(srcOf(fn), fn).toMatch(/if \(!on\(\)(\s*\|\|\s*built)?\) return;/);
    }
    expect(src).toContain('const on = () => !!state.ui2Shell;');
  });
});

describe('strings', () => {
  const dir = new URL('../../i18n/bundles/', import.meta.url);
  const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
    .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));
  const used = new Set();
  for (const f of ['views/music/ui2radio.js', 'views/music/ui2stations.js']) {
    for (const m of fileSource(f).matchAll(/'(ui2\.[a-zA-Z.]+)'/g)) used.add(m[1]);
  }
  const placeholders = (s) => [...String(s).matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();

  it('finds the Radio keys in the code', () => {
    expect(used.size).toBeGreaterThan(30);
  });

  for (const [code, b] of Object.entries(bundles)) {
    it(`${code} has every Radio string, translated, with the English placeholders and no dashes`, () => {
      for (const k of used) {
        expect(typeof b[k] === 'string' && b[k].trim().length > 0, `${code}: ${k}`).toBe(true);
        expect(placeholders(b[k]), `${code}: ${k}`).toEqual(placeholders(bundles.en[k]));
        expect(b[k], `${code}: ${k}`).not.toMatch(/[–—]/);
      }
      // The button shows "Put on key"; its spoken name starts with the same
      // words, so a voice command that says what it sees reaches it.
      expect(b['ui2.radio.putOnKeyName'].startsWith(b['ui2.radio.putOnKey']), code).toBe(true);
      if (code !== 'en') {
        const same = [...used].filter((k) => b[k] === bundles.en[k] && !/\{\{/.test(b[k]));
        // Words that are the same in some languages (Details, Format, Trend, ...).
        expect(same.length, `${code}: ${same.join(', ')}`).toBeLessThan(8);
      }
    });
  }

  it('writes German with real umlauts and du', () => {
    expect(bundles.de['ui2.radio.pickSub']).toBe('Wähle eine der sechs Tasten auf {{speaker}}.');
    expect(bundles.de['ui2.radio.favAdd']).toContain('hinzufügen');
  });
});
