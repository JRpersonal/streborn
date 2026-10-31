// The station list of the UI 2.0 preview keeps a failed directory fetch on
// screen (phase 5): a filter change that repaints the list must not paint the
// old stations over it. The current layout keeps its one muted line.
import { describe, it, expect, vi, beforeEach } from 'vitest';

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
const radio = vi.hoisted(() => ({ rendered: 0 }));

vi.mock('../../api.js', () => api);
vi.mock('../recent.js', () => ({ replayLibraryCard: vi.fn() }));
vi.mock('./favorites.js', () => ({ isFav: () => false, loadFavStore: () => [], toggleFav: () => false }));
vi.mock('./group.js', () => ({ effectivePlayTarget: () => null }));
vi.mock('./live.js', () => ({ scheduleLiveBitrate: vi.fn(), scheduleLiveTitle: vi.fn() }));
vi.mock('./nowplaying.js', () => ({ renderNowPlayingBar: vi.fn() }));
vi.mock('./pandora.js', () => ({ renderPandoraStations: vi.fn() }));
vi.mock('./picker.js', () => ({ openPick: vi.fn() }));
vi.mock('./presettiles.js', () => ({ renderPresets: vi.fn() }));
vi.mock('./status.js', () => ({ refreshStatus: vi.fn() }));
vi.mock('./taxonomy.js', () => ({ loadLanguagesForCountry: vi.fn(), renderGenreChips: vi.fn() }));
vi.mock('./ui2radio.js', () => ({
  openUi2RadioTab: vi.fn(),
  renderUi2Stations: (res, list) => { radio.rendered++; res.innerHTML = `<ul class="ui2-st-list">${list.map((s) => `<li>${s.name}</li>`).join('')}</ul>`; },
  syncFilterSummary: vi.fn(),
  syncUi2RadioTabs: vi.fn(),
}));

// The few nodes the list code touches, kept by id.
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

const STATIONS = [{ name: 'Rock FM', url: 'http://radio.example/rock', stationuuid: 'u1', codec: 'MP3', bitrate: 128 }];
let search;
let state;
const flush = () => new Promise((r) => setTimeout(r, 0));

beforeEach(async () => {
  vi.resetModules();
  Object.values(api).forEach((f) => typeof f.mockReset === 'function' && f.mockReset());
  radio.rendered = 0;
  globalThis.document = fakeDocument();
  ({ state } = await import('../../state.js'));
  (await import('../../i18n/index.js')).setLocale('en');
  state.currentBox = { host: '192.0.2.1', port: 8888, friendlyName: 'Kitchen' };
  state.searchLastMode = 'top';
  state.searchListOpen = true;
  state.searchResults = [];
  state.searchOnlyBose = false;
  state.searchMinBitrate = 0;
  search = await import('./search.js');
});

describe('a failed directory fetch in the preview', () => {
  beforeEach(() => { state.ui2Shell = true; });

  it('shows the error state with the reason and Try again', async () => {
    api.RadioSearchDetailed.mockRejectedValue('all radio-browser mirrors failed');
    search.doRefilter();
    await flush();
    const html = document.getElementById('searchResults').innerHTML;
    expect(html).toContain('role="alert"');
    expect(html).toContain('The station directory did not answer');
    expect(html).toContain('all radio-browser mirrors failed');
    expect(html).toContain('id="ui2StationsRetry"');
    expect(typeof document.getElementById('ui2StationsRetry').onclick).toBe('function');
    expect(search.searchListError()).not.toBeNull();
    expect(document.getElementById('ui2RadioPanel').getAttribute('aria-busy')).toBe('false');
  });

  it('survives a repaint by a filter change', async () => {
    api.RadioSearchDetailed.mockRejectedValue('all radio-browser mirrors failed');
    search.doRefilter();
    await flush();
    state.searchResults = STATIONS;
    search.renderSearchResults();
    const html = document.getElementById('searchResults').innerHTML;
    expect(html).toContain('The station directory did not answer');
    expect(html).not.toContain('Rock FM');
    expect(radio.rendered).toBe(0);
  });

  it('goes away with the next fetch that answers', async () => {
    api.RadioSearchDetailed.mockRejectedValueOnce('down').mockResolvedValueOnce({ stations: STATIONS, relaxed: false });
    search.doRefilter();
    await flush();
    search.doRefilter();
    await flush();
    expect(search.searchListError()).toBeNull();
    expect(document.getElementById('searchResults').innerHTML).toContain('Rock FM');
  });
});

describe('the current layout', () => {
  beforeEach(() => { state.ui2Shell = false; });

  it('keeps its one muted line for a failed fetch', async () => {
    api.RadioSearchDetailed.mockRejectedValue('all radio-browser mirrors failed');
    search.doRefilter();
    await flush();
    expect(document.getElementById('searchResults').innerHTML).toBe('<div class="muted">Error: all radio-browser mirrors failed</div>');
    expect(search.searchListError()).toBeNull();
    expect(document.getElementById('ui2RadioPanel').getAttribute('aria-busy')).toBeNull();
  });

  it('keeps its muted loading line and its plain empty line', async () => {
    let answer;
    api.RadioSearchDetailed.mockReturnValue(new Promise((r) => { answer = r; }));
    search.doRefilter();
    expect(document.getElementById('searchResults').innerHTML).toBe('<div class="muted">Stations are loading...</div>');
    answer({ stations: [], relaxed: false });
    await flush();
    expect(document.getElementById('searchResults').innerHTML).toBe('<div class="muted">No stations found.</div>');
  });
});
