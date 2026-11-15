// The UI 2.0 player's "Show in Radio / Library / Spotify" button: which tab a
// source belongs to, the folder a library play came from, the trail the
// Library opens it with, and how the button is wired into the player.
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { fileSource, srcOf } from '../test/source.js';
import { navEntryFor } from './nav.js';
import {
  GO_SOURCE_LABEL_KEYS, folderFromCard, folderStack, goSourceLabelKey, goSourceTarget, splitCardFolder,
} from './gotosource.js';

const dir = new URL('../i18n/bundles/', import.meta.url);
const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
  .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));
const css = readFileSync(new URL('./shell2.css', import.meta.url), 'utf8').replace(/\r\n/g, '\n');

const UDN = 'uuid:4d696e69-444c-164e-9d41-001122334455';
const FILE = 'http://192.0.2.60:8200/MediaItems/1.flac';
const queue = (over = {}) => ({
  active: true,
  pos: 1,
  items: [{ title: 'Opening', album: 'Evening Sessions' }, { title: 'Blue Hour', album: 'Evening Sessions' }],
  url: FILE,
  card: `queue:${UDN}:c1a`,
  ...over,
});
const view = (np) => {
  const tgt = goSourceTarget(np);
  return tgt ? tgt.view : null;
};

describe('goSourceTarget: the tab a source belongs to', () => {
  it('sends a station to Radio', () => {
    // A preset key through the proxy, a station from Find stations, a native
    // station descriptor, and the speaker's own station sources.
    expect(view({ source: 'UPNP', location: 'http://192.0.2.50:8888/stream/2' })).toBe('box');
    expect(view({ source: 'INTERNET_RADIO', location: 'http://192.0.2.50:8888/stream/raw?u=aHR0cDovL3g' })).toBe('box');
    const desc = btoa(JSON.stringify({ streamUrl: 'http://192.0.2.50:8888/stream/3' })).replace(/=+$/, '');
    expect(view({ source: 'LOCAL_INTERNET_RADIO', location: `/station?data=${desc}` })).toBe('box');
    expect(view({ source: 'TUNEIN', location: '/v1/playback/station/s24896' })).toBe('box');
    expect(view({ source: 'LOCAL_INTERNET_RADIO', location: 'http://example.com/rock' })).toBe('box');
    expect(view({ source: 'PANDORA', location: '' })).toBe('box');
  });

  it('sends Spotify to Spotify, STR\'s own stream and the speaker\'s own Connect', () => {
    expect(view({ source: 'UPNP', location: 'http://192.0.2.50:8888/spotify/stream-3.ogg' })).toBe('spotify');
    expect(view({ source: 'SPOTIFY', location: '/playback/container/c3BvdGlmeTpwbGF5bGlzdDox' })).toBe('spotify');
    expect(view({ source: 'SPOTIFY', location: '' })).toBe('spotify');
  });

  it('sends a folder that plays as a queue to its folder in the Library', () => {
    const tgt = goSourceTarget({ source: 'UPNP', location: 'http://192.0.2.60:8200/MediaItems/2.flac', queue: queue() });
    expect(tgt).toEqual({ view: 'library', folder: { card: `${UDN}:c1a`, title: 'Evening Sessions' } });
    // An HTTPS media server's file is relayed like a station; the queue wins.
    expect(view({ source: 'UPNP', location: 'http://192.0.2.50:8888/stream/raw?u=aHR0cHM6Ly94', queue: queue() })).toBe('library');
  });

  it('sends a folder started from a key to the Library, without a folder', () => {
    const tgt = goSourceTarget({ source: 'UPNP', location: FILE, queue: queue({ card: 'queue:slot:3' }) });
    expect(tgt).toEqual({ view: 'library', folder: null });
  });

  it('sends a single track the app started to the folder it was listed in', () => {
    const lastLibraryPlay = {
      url: FILE, album: 'Evening Sessions',
      ref: { server_udn: UDN, object_id: 't1', parent_id: 'c1a', title: 'Opening' },
    };
    expect(goSourceTarget({ source: 'UPNP', location: FILE, lastLibraryPlay }))
      .toEqual({ view: 'library', folder: { udn: UDN, id: 'c1a', title: 'Evening Sessions' } });
    // Without its place on the server (an older play): the Library itself.
    expect(goSourceTarget({ source: 'UPNP', location: FILE, lastLibraryPlay: { url: FILE } }))
      .toEqual({ view: 'library', folder: null });
    // The speaker moved on to something else: not the track any more.
    expect(view({ source: 'UPNP', location: 'http://192.0.2.70/other.mp3', lastLibraryPlay })).toBe(null);
  });

  it('sends a lone file the phone page started to the Library', () => {
    const q = { active: false, file: { url: FILE, title: 'Opening' } };
    expect(view({ source: 'UPNP', location: FILE, queue: q })).toBe('library');
    expect(view({ source: 'UPNP', location: 'http://192.0.2.70/other.mp3', queue: q })).toBe(null);
  });

  it('sends the speaker\'s own music library to the Library', () => {
    expect(view({ source: 'STORED_MUSIC', location: '1$4$12' })).toBe('library');
  });

  it('has no tab for inputs and receivers, even with a location left over', () => {
    for (const source of ['BLUETOOTH', 'AUX', 'LOCAL', 'PRODUCT', 'AIRPLAY', 'AirPlay2']) {
      expect(view({ source, location: 'http://192.0.2.50:8888/stream/2' }), source).toBe(null);
    }
  });

  it('has none with nothing playing or a source it does not know', () => {
    expect(goSourceTarget()).toBe(null);
    expect(view({ source: 'STANDBY', location: '' })).toBe(null);
    expect(view({ source: 'INVALID_SOURCE', location: '' })).toBe(null);
    expect(view({ source: 'DEEZER', location: 'deezer:track:1' })).toBe(null);
    // A file another app pushed: STR cannot tell where it came from.
    expect(view({ source: 'UPNP', location: 'http://192.0.2.70/other.mp3' })).toBe(null);
  });

  it('waits for the speaker right after a library play while it still names a station', () => {
    expect(view({ source: 'TUNEIN', location: FILE, optimistic: true })).toBe(null);
    expect(view({ source: 'TUNEIN', location: '/v1/playback/station/s1', optimistic: true })).toBe('box');
  });
});

describe('the folder a queue card names', () => {
  it('reads a folder card and leaves a key card alone', () => {
    expect(folderFromCard(`queue:${UDN}:c1a`)).toEqual({ card: `${UDN}:c1a` });
    expect(folderFromCard('queue:slot:3')).toBe(null);
    expect(folderFromCard('')).toBe(null);
    expect(folderFromCard('radio:x')).toBe(null);
    expect(folderFromCard('queue:')).toBe(null);
  });

  it('splits server and folder at the known server, the longest first', () => {
    expect(splitCardFolder(`${UDN}:64$0$1`, [UDN])).toEqual({ udn: UDN, id: '64$0$1' });
    // A container id with colons of its own stays whole.
    expect(splitCardFolder(`${UDN}:a:b`, ['uuid:4d696e69', UDN])).toEqual({ udn: UDN, id: 'a:b' });
  });

  it('splits after the usual uuid shape when the server is not listed', () => {
    expect(splitCardFolder(`${UDN}:c1a`, [])).toEqual({ udn: UDN, id: 'c1a' });
    expect(splitCardFolder('server-1:c1a')).toEqual({ udn: 'server-1', id: 'c1a' });
    expect(splitCardFolder('nocolon')).toBe(null);
  });
});

describe('the trail the Library opens a folder with', () => {
  const walked = [{ id: '0', title: 'NAS' }, { id: 'c1', title: 'Music' }, { id: 'c1a', title: 'Evening Sessions' }];

  it('takes the first known trail through the folder, cut after it', () => {
    expect(folderStack('c1a', [null, walked])).toEqual(walked);
    expect(folderStack('c1', [walked])).toEqual(walked.slice(0, 2));
    const other = [{ id: '0', title: '' }, { id: 'c9', title: 'Other' }];
    expect(folderStack('c1a', [other, walked])).toEqual(walked);
    // A copy: the Library's own stack is not changed through it.
    const got = folderStack('c1a', [walked]);
    got[2].title = 'x';
    expect(walked[2].title).toBe('Evening Sessions');
  });

  it('falls back to the server root and the folder', () => {
    expect(folderStack('c1a', [], 'Evening Sessions')).toEqual([{ id: '0', title: '' }, { id: 'c1a', title: 'Evening Sessions' }]);
    expect(folderStack('0', [])).toEqual([{ id: '0', title: '' }]);
    expect(folderStack('', [walked])).toBe(null);
  });
});

describe('the button\'s words', () => {
  it('has a name for each tab it leads to, and only those', () => {
    expect(Object.keys(GO_SOURCE_LABEL_KEYS).sort()).toEqual(['box', 'library', 'spotify']);
    expect(goSourceLabelKey('box')).toBe('ui2.player.goRadio');
    expect(goSourceLabelKey('help')).toBe('');
  });

  for (const [code, b] of Object.entries(bundles)) {
    it(`${code}: keeps the tab's own name in the button's name, translated`, () => {
      for (const [v, key] of Object.entries(GO_SOURCE_LABEL_KEYS)) {
        const tab = b[navEntryFor(v).labelKey];
        expect(typeof b[key] === 'string' && b[key].trim(), `${code}: ${key}`).toBeTruthy();
        // What is seen (the tab's name) is part of what is heard.
        expect(b[key].toLowerCase(), `${code}: ${key}`).toContain(tab.toLowerCase());
        if (code !== 'en') expect(b[key], `${code}: ${key} is still English`).not.toBe(bundles.en[key]);
      }
    });
  }

  it('writes German with the tab\'s own name in its case', () => {
    expect(bundles.de['ui2.player.goLibrary']).toBe('In der Mediathek zeigen');
  });
});

describe('the button in the player', () => {
  it('sits next to the title line, hidden until a source has a tab', () => {
    const markup = fileSource('shell2/markup.js');
    expect(markup).toMatch(/<div class="ui2-now-line" id="ui2NowLine"><\/div>\s*<button type="button" class="u-btn u-btn--secondary ui2-go-source" id="ui2GoSource" hidden>/);
    expect(srcOf('mountShell2')).toContain('move(n.statusBar, n.fNowLine);');
    // hidden must win over the button's own display.
    expect(css).toContain('html.ui2 .ui2-go-source[hidden] { display: none; }');
  });

  it('follows the now-playing line through a hook only the shell sets', () => {
    expect(srcOf('renderNowPlayingBar')).toContain("reportHook('nowPlaying')");
    expect(srcOf('wireGoSource')).toContain("setReportHook('nowPlaying'");
    expect(srcOf('wireStatesEnv')).toContain('wireGoSource(frame);');
  });

  it('opens the tab the way a tab does, and the folder first', () => {
    const wire = srcOf('wireGoSource');
    const folder = wire.indexOf('deps.showLibraryFolder(tgt.folder)');
    const sw = wire.indexOf('deps.switchView(tgt.view)');
    expect(folder).toBeGreaterThan(0);
    expect(sw).toBeGreaterThan(folder);
    expect(fileSource('main.js')).toMatch(/mountShell2\(\{[\s\S]*?showLibraryFolder,/);
  });

  it('names itself by the tab and hides for an offline speaker', () => {
    const paint = srcOf('paintGoSource');
    expect(paint).toContain('!box.offline');
    expect(paint).toContain("btn.setAttribute('aria-label', name)");
    expect(paint).toContain('btn.title = name');
    expect(paint).toContain('t(entry.labelKey)');
    expect(srcOf('syncShellPlayer')).toContain('paintGoSource();');
  });

  it('lets the Library take the asked-for folder before the remembered one', () => {
    expect(srcOf('openLibrary')).toContain('placePendingFolder() || libState.currentUDN');
    const load = srcOf('loadMediaServers');
    expect(load.indexOf('placePendingFolder()')).toBeGreaterThan(0);
    expect(load.indexOf('placePendingFolder()')).toBeLessThan(load.indexOf('rememberedServerUDN()'));
    // The search across every server gives way to the folder.
    expect(srcOf('placePendingFolder')).toContain('libState.search.active = false');
  });
});
