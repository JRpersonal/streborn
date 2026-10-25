// The UI 2.0 Spotify view (#1286): what plays from Spotify, the six keys, the
// login of every speaker, and the strings and gates around them.
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { accountRow, keySlots, spotifyContextKind, spotifyPlaying } from './spotifyui2model.js';
import { fileSource, srcOf } from '../test/source.js';

const PLAYLIST = 'spotify:playlist:37i9dQZF1DXcBWIGoYBM5M';
const container = (uri) => `/playback/container/${Buffer.from(uri).toString('base64')}`;

describe('spotifyContextKind', () => {
  it('names what a context URI points at', () => {
    expect(spotifyContextKind(PLAYLIST)).toBe('playlist');
    expect(spotifyContextKind('spotify:album:abc')).toBe('album');
    expect(spotifyContextKind('spotify:user:someone:playlist:abc')).toBe('playlist');
    expect(spotifyContextKind('spotify:station:abc')).toBe('');
    expect(spotifyContextKind('')).toBe('');
    expect(spotifyContextKind('http://x/playlist:abc')).toBe('');
  });
});

describe('spotifyPlaying', () => {
  it('reads the native Spotify entry from the speaker location', () => {
    const r = spotifyPlaying({ nowLocation: container(PLAYLIST), nowSource: 'SPOTIFY', nowPlayState: 'PLAY_STATE', nowName: ' Discover Weekly ' });
    expect(r).toEqual({ playing: true, name: 'Discover Weekly', uri: PLAYLIST, kind: 'playlist' });
  });

  it('takes the engine context for STR\'s own Spotify stream', () => {
    const r = spotifyPlaying({
      nowLocation: 'http://127.0.0.1:8888/spotify/stream.ogg', nowSource: 'UPNP', nowPlayState: 'PLAY_STATE',
      nowName: 'Jazz', spotifyNow: { context: 'spotify:album:xyz' },
    });
    expect(r.playing).toBe(true);
    expect(r.uri).toBe('spotify:album:xyz');
    expect(r.kind).toBe('album');
  });

  it('counts a paused playlist, but not standby or a stopped speaker', () => {
    const base = { nowLocation: container(PLAYLIST), nowSource: 'SPOTIFY' };
    expect(spotifyPlaying({ ...base, nowPlayState: 'PAUSE_STATE' }).playing).toBe(true);
    expect(spotifyPlaying({ ...base, nowPlayState: 'STANDBY' }).playing).toBe(false);
    expect(spotifyPlaying({ ...base, nowPlayState: 'STOP_STATE' }).playing).toBe(false);
    expect(spotifyPlaying({ ...base, nowPlayState: '' }).playing).toBe(false);
  });

  it('is not Spotify for a radio stream', () => {
    expect(spotifyPlaying({ nowLocation: 'http://demobox.local:8888/stream/2', nowPlayState: 'PLAY_STATE' }).playing).toBe(false);
    expect(spotifyPlaying(null).playing).toBe(false);
  });

  it('plays with an unknown context when the speaker has not told it yet', () => {
    const r = spotifyPlaying({ nowLocation: 'http://127.0.0.1:8888/spotify/stream.ogg', nowSource: 'UPNP', nowPlayState: 'PLAY_STATE' });
    expect(r).toMatchObject({ playing: true, uri: '', kind: '' });
  });
});

describe('keySlots', () => {
  const presets = [
    { slot: 1, name: 'BBC Radio 1', type: 'radio' },
    { slot: 3, name: 'Discover Weekly', type: 'spotify', uri: PLAYLIST },
    { slot: 4, name: '', type: 'radio' },
  ];

  it('always lists six keys in order, empty ones included', () => {
    const s = keySlots(presets, '');
    expect(s.map((k) => k.slot)).toEqual([1, 2, 3, 4, 5, 6]);
    expect(s[1]).toMatchObject({ empty: true, name: '' });
    expect(s[3].empty).toBe(true);
    expect(s[0]).toMatchObject({ empty: false, name: 'BBC Radio 1', spotify: false });
    expect(keySlots(undefined, '')).toHaveLength(6);
  });

  it('marks the key that already holds the playing context', () => {
    const s = keySlots(presets, PLAYLIST);
    expect(s.filter((k) => k.here).map((k) => k.slot)).toEqual([3]);
    expect(keySlots(presets, '').some((k) => k.here)).toBe(false);
  });
});

describe('accountRow', () => {
  const box = { host: '192.0.2.50', port: 8888, name: 'Living Room' };
  const opaque = 'a1b2c3d4e5f6a7b8c9d0e1f2a3';

  it('says offline without asking', () => {
    expect(accountRow({ ...box, offline: true }, null, {}).state).toBe('offline');
  });

  it('says unknown when the speaker gave no usable answer', () => {
    expect(accountRow(box, null, {}).state).toBe('unknown');
    // The zero answer the app gets for an unreachable speaker or an old agent.
    expect(accountRow(box, { account: '', canRecall: null, product: '' }, {}).state).toBe('unknown');
  });

  it('names the logged-in account and never prints its id', () => {
    const r = accountRow(box, { account: opaque, canRecall: true, product: 'premium', accountNames: { [opaque]: 'Anna' } }, {});
    expect(r).toMatchObject({ state: 'in', account: 'Anna', plan: 'premium', others: [] });
    const nameless = accountRow(box, { account: opaque, canRecall: true }, {});
    expect(nameless.state).toBe('in');
    expect(nameless.account).toBe('');
    expect(JSON.stringify(nameless)).not.toContain(opaque);
  });

  it('uses names another speaker taught the app', () => {
    expect(accountRow(box, { account: opaque, canRecall: true }, { [opaque]: 'Ben' }).account).toBe('Ben');
  });

  it('tells a saved login from none at all', () => {
    expect(accountRow(box, { account: '', canRecall: true }, {}).state).toBe('saved');
    expect(accountRow(box, { account: '', canRecall: false }, {}).state).toBe('none');
  });

  it('reads the plan, and stays silent when the speaker could not find out', () => {
    expect(accountRow(box, { account: 'anna', canRecall: true, product: 'free' }, {}).plan).toBe('free');
    expect(accountRow(box, { account: 'anna', canRecall: true, product: 'open' }, {}).plan).toBe('free');
    expect(accountRow(box, { account: 'anna', canRecall: true, premiumRequired: true }, {}).plan).toBe('free');
    expect(accountRow(box, { account: 'anna', canRecall: true, product: '' }, {}).plan).toBe('');
  });

  it('lists the other accounts the speaker keeps, by name only', () => {
    const r = accountRow(box, {
      account: opaque, canRecall: true,
      accountNames: { [opaque]: 'Anna', zzzzzzzzzzzzzzzzzzzzzz: 'Ben', yyyyyyyyyyyyyyyyyyyyyy: 'Cleo', xxxxxxxxxxxxxxxxxxxxxx: '' },
    }, {});
    expect(r.others).toEqual(['Ben', 'Cleo']);
  });
});

describe('UI 2.0 Spotify strings', () => {
  const dir = new URL('../i18n/bundles/', import.meta.url);
  const bundles = Object.fromEntries(readdirSync(dir).filter((f) => f.endsWith('.json'))
    .map((f) => [f.replace(/\.json$/, ''), JSON.parse(readFileSync(new URL(f, dir), 'utf8'))]));
  const code = fileSource('views/spotifyui2.js');
  const keys = new Set([...code.matchAll(/'(ui2\.spotify\.[a-zA-Z.]+)'/g)].map((m) => m[1]));
  for (const k of ['playlist', 'album', 'artist', 'track', 'show', 'episode', 'other']) keys.add(`ui2.spotify.kind.${k}`);
  const placeholders = (s) => [...String(s).matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();

  it('finds the keys in the code', () => {
    expect(keys.size).toBeGreaterThan(40);
  });

  for (const [lang, b] of Object.entries(bundles)) {
    it(`${lang} has every string with the English placeholders and no dash`, () => {
      for (const k of keys) {
        expect(typeof b[k] === 'string' && b[k].trim().length > 0, `${lang}: ${k}`).toBe(true);
        expect(placeholders(b[k]), `${lang}: ${k}`).toEqual(placeholders(bundles.en[k]));
        expect(b[k], `${lang}: ${k}`).not.toMatch(/[–—]/);
      }
    });
  }

  it('translates instead of copying English', () => {
    for (const [lang, b] of Object.entries(bundles)) {
      if (lang === 'en') continue;
      expect(b['ui2.spotify.keysTitle'], lang).not.toBe(bundles.en['ui2.spotify.keysTitle']);
      expect(b['ui2.spotify.acct.intro'], lang).not.toBe(bundles.en['ui2.spotify.acct.intro']);
    }
    expect(bundles.de['ui2.spotify.notPlayingHow']).toContain('Öffne');
    expect(bundles.de['ui2.spotify.acct.none']).toContain('Wähle');
  });
});

describe('the classic layout is untouched', () => {
  it('draws the cards and calls into them only with the shell mounted', () => {
    const view = srcOf('renderSpotifyAlpha');
    expect(view).toContain("shell2Mounted() ? spotifyUi2Markup() : ''");
    const calls = [...view.matchAll(/renderSpotifyUi2\(/g)].length;
    const gated = [...view.matchAll(/if \(shell2Mounted\(\)\) renderSpotifyUi2\(/g)].length;
    expect(calls).toBeGreaterThan(0);
    expect(gated).toBe(calls);
  });

  it('keeps every rule of the cards under html.ui2', () => {
    const all = readFileSync(new URL('../shell2/shell2.css', import.meta.url), 'utf8');
    const css = all.slice(all.indexOf('/* ---------- Spotify (phase 2)')).replace(/\/\*[\s\S]*?\*\//g, '');
    const selectors = [...css.matchAll(/([^{}]+)\{/g)].map((m) => m[1].trim()).filter(Boolean);
    expect(selectors.length).toBeGreaterThan(10);
    for (const sel of selectors) {
      for (const part of sel.split(',')) expect(part.trim(), sel).toMatch(/^html\.ui2 #view-spotify /);
    }
  });

  it('saves through the Music view\'s own Spotify path', () => {
    const save = srcOf('saveToChosenKey');
    expect(save).toContain('saveSpotifyToSlot(slot,');
    expect(save).not.toMatch(/SaveSpotifyPreset|SetPreset/);
    // The Music view's long press goes the same way, with its toasts.
    expect(srcOf('saveCurrentToSlot')).toContain('await saveSpotifyToSlot(slot);');
  });
});
