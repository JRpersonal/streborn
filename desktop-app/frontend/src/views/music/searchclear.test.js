// Emptying the radio search field takes the old results off the screen at
// once, without a click on Search, in the current layout and in the UI 2.0
// preview (which moves the same field); and the preview's Radio starts with an
// empty list like the current layout, so the last list does not seem to come
// back after the app was closed (discussion #1587).
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { readFileSync } from 'node:fs';

const api = vi.hoisted(() => ({
  boxFetch: vi.fn(),
  BrowserOpenURL: vi.fn(),
  ClassifyStreamURL: vi.fn(),
  isMissingBinding: () => false,
  PlayURL: vi.fn(),
  RadioClick: vi.fn(),
  RadioSearch: vi.fn(),
  RadioSearchDetailed: vi.fn(),
  RadioStationsByURL: vi.fn(),
}));

vi.mock('../../api.js', () => api);
vi.mock('../recent.js', () => ({ replayLibraryCard: vi.fn() }));
vi.mock('./favorites.js', () => ({ isFav: () => false, loadFavStore: () => [], moveFavorite: vi.fn(), toggleFav: () => false }));
vi.mock('./group.js', () => ({ effectivePlayTarget: () => null }));
vi.mock('./live.js', () => ({ scheduleLiveBitrate: vi.fn(), scheduleLiveTitle: vi.fn() }));
vi.mock('./nowplaying.js', () => ({ renderNowPlayingBar: vi.fn() }));
vi.mock('./pandora.js', () => ({ openPandoraPick: vi.fn(), playPandoraRow: vi.fn(), renderPandoraStations: vi.fn() }));
vi.mock('./picker.js', () => ({ openPick: vi.fn() }));
vi.mock('./presettiles.js', () => ({ renderPresets: vi.fn() }));
vi.mock('./status.js', () => ({ refreshStatus: vi.fn() }));
vi.mock('./taxonomy.js', () => ({ loadLanguagesForCountry: vi.fn(), renderGenreChips: vi.fn() }));
vi.mock('./ui2radio.js', () => ({
  openUi2RadioTab: vi.fn(),
  renderUi2Stations: (res, list) => { res.innerHTML = `<ul class="ui2-st-list">${list.map((s) => `<li>${s.name}</li>`).join('')}</ul>`; },
  syncFilterSummary: vi.fn(),
  syncUi2RadioTabs: vi.fn(),
}));

// The few nodes the search code touches, kept by id.
function fakeDocument() {
  const nodes = {};
  const node = (id) => {
    if (!nodes[id]) {
      const attrs = {};
      const classes = new Set();
      nodes[id] = {
        id, innerHTML: '', value: '', checked: false, open: false, onclick: null, attrs,
        classList: {
          add: (c) => classes.add(c), remove: (c) => classes.delete(c),
          toggle: (c, on) => (on === undefined ? (classes.has(c) ? classes.delete(c) : classes.add(c)) : (on ? classes.add(c) : classes.delete(c))),
          contains: (c) => classes.has(c),
        },
        setAttribute: (k, v) => { attrs[k] = String(v); },
        getAttribute: (k) => (k in attrs ? attrs[k] : null),
        removeAttribute: (k) => { delete attrs[k]; },
        querySelector: () => null,
        querySelectorAll: () => [],
      };
    }
    return nodes[id];
  };
  return { getElementById: node, querySelectorAll: () => [], nodes };
}

const RUSH = [{ name: 'Rush Radio', url: 'http://radio.example/rush', stationuuid: 'u1', codec: 'MP3', bitrate: 128 }];
let state;
const flush = () => new Promise((r) => setTimeout(r, 0));
const el = (id) => document.getElementById(id);

// type puts text into the search field the way the keyboard does: the value
// changes, then the input event fires.
function type(text) {
  el('searchQ').value = text;
  el('searchQ').oninput();
}

async function searchFor(text) {
  type(text);
  await el('searchBtn').onclick();
  await flush();
}

beforeEach(async () => {
  vi.resetModules();
  Object.values(api).forEach((f) => typeof f.mockReset === 'function' && f.mockReset());
  globalThis.document = fakeDocument();
  ({ state } = await import('../../state.js'));
  (await import('../../i18n/index.js')).setLocale('en');
  state.currentBox = { host: '192.0.2.1', port: 8888, friendlyName: 'Kitchen' };
  state.searchLastMode = 'top';
  state.searchListOpen = false;
  state.searchResults = [];
  state.searchOnlyBose = false;
  state.searchMinBitrate = 0;
  (await import('./search.js')).mountSearch();
});

for (const [layout, ui2] of [['the current layout', false], ['the UI 2.0 preview', true]]) {
  describe(`emptying the search field in ${layout}`, () => {
    beforeEach(() => { state.ui2Shell = ui2; });

    it('clears the results at once, without a click on Search', async () => {
      api.RadioSearchDetailed.mockResolvedValue({ stations: RUSH, relaxed: false });
      await searchFor('rush');
      expect(el('searchResults').innerHTML).toContain('Rush Radio');

      type('');
      expect(el('searchResults').innerHTML).toBe('');
      expect(state.searchResults).toEqual([]);
      expect(state.searchListOpen).toBe(false);
      expect(state.searchLastQuery).toBe('');
      expect(el('searchCount').classList.contains('hidden')).toBe(true);
      expect(el('loadMoreRow').classList.contains('hidden')).toBe(true);
      expect(el('searchQ').classList.contains('has-query')).toBe(false);
      // Only the Search click asked the directory; clearing asks nothing.
      expect(api.RadioSearchDetailed).toHaveBeenCalledTimes(1);
    });

    it('answers Search on an empty field with an empty list, not the top list', async () => {
      api.RadioSearchDetailed.mockResolvedValue({ stations: RUSH, relaxed: false });
      await searchFor('rush');
      type('');
      await el('searchBtn').onclick();
      await flush();
      expect(el('searchResults').innerHTML).toBe('');
      expect(state.searchListOpen).toBe(false);
      expect(api.RadioSearchDetailed).toHaveBeenCalledTimes(1);
    });

    it('answers Search on a field of only spaces with an empty list', async () => {
      api.RadioSearchDetailed.mockResolvedValue({ stations: RUSH, relaxed: false });
      type('   ');
      await el('searchBtn').onclick();
      await flush();
      expect(el('searchResults').innerHTML).toBe('');
      expect(api.RadioSearchDetailed).not.toHaveBeenCalled();
    });

    it('keeps the results while part of the text is deleted', async () => {
      api.RadioSearchDetailed.mockResolvedValue({ stations: RUSH, relaxed: false });
      await searchFor('rush');
      type('ru');
      expect(el('searchResults').innerHTML).toContain('Rush Radio');
      expect(state.searchListOpen).toBe(true);
    });

    it('drops a search that was still on its way when the field was emptied', async () => {
      let answer;
      api.RadioSearchDetailed.mockReturnValue(new Promise((r) => { answer = r; }));
      type('rush');
      const pending = el('searchBtn').onclick();
      type('');
      answer({ stations: RUSH, relaxed: false });
      await pending;
      await flush();
      expect(el('searchResults').innerHTML).toBe('');
      expect(state.searchResults).toEqual([]);
    });

    it('leaves the top list on screen', async () => {
      api.RadioSearchDetailed.mockResolvedValue({ stations: RUSH, relaxed: false });
      await el('topBtn').onclick();
      await flush();
      type('x');
      type('');
      expect(el('searchResults').innerHTML).toContain('Rush Radio');
      expect(state.searchListOpen).toBe(true);
    });

    it('opens the top list on a later filter change, not the deleted search', async () => {
      api.RadioSearchDetailed.mockResolvedValue({ stations: RUSH, relaxed: false });
      await searchFor('rush');
      type('');
      el('searchOrder').value = 'name';
      el('searchOrder').onchange();
      await flush();
      const opts = api.RadioSearchDetailed.mock.calls.at(-1)[0];
      expect(opts.q).toBe('');
      expect(opts.top).toBe(true);
    });
  });
}

describe('the UI 2.0 preview at start', () => {
  const src = readFileSync(new URL('./ui2radio.js', import.meta.url), 'utf8');

  it('fetches no list on its own when Radio first shows, like the current layout', () => {
    const body = src.slice(src.indexOf('export function syncUi2Keys()'), src.indexOf('function wireKeyEdit('));
    expect(body.length).toBeGreaterThan(0);
    expect(body).not.toContain('doRefilter(');
    expect(src).not.toContain('discoverLoaded');
  });
});
