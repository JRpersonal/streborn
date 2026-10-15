// #1301: a Spotify album cover showed up on a music-library folder key, and
// library keys never showed their own cover. Two causes, pinned here:
//
// 1. The folder key was lit from the agent's queue card while the speaker
//    actually played Spotify (the agent kept the queue running when the Spotify
//    app took the speaker over), and a key without art adopts the cover of what
//    plays. So the key took the Spotify cover of key 6 as its own.
// 2. A folder key stores no art at preset level; its tracks carry the media
//    server's albumArtURI, and that was never used for the tile.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { folderFallbackArt, presetOwnArt, queueSlotActive, shouldAdoptPresetArt } from './utils.js';

const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');

// Shapes as the #1301 bundles carry them (addresses replaced).
const COVER = 'http://192.0.2.32:9790/minimserver/*/Music/Boston/Third*20Stage/01*20Amanda.m4a/$!picture-14789-2322400.jpg';
const folderKey = (slot, items) => ({ slot, type: 'queue', name: 'Third Stage', items });
const SPOTIFY_LOC = 'http://192.0.2.239:8888/spotify/stream-6.ogg';
const LIB_LOC = 'http://192.0.2.32:9790/minimserver/*/Music/Compilations/x/05*202*20The*20Night.m4a';

describe('queueSlotActive while Spotify plays', () => {
  const q = { active: true, card: 'queue:slot:3' };
  it('does not light the folder key from a stale queue card', () => {
    expect(queueSlotActive(folderKey(3), q, SPOTIFY_LOC)).toBe(false);
  });
  it('still lights it while the library track plays, or before any location', () => {
    expect(queueSlotActive(folderKey(3), q, LIB_LOC)).toBe(true);
    expect(queueSlotActive(folderKey(3), q, '')).toBe(true);
    expect(queueSlotActive(folderKey(3), q)).toBe(true);
  });
});

describe('shouldAdoptPresetArt', () => {
  it('never writes the playing cover onto a folder key', () => {
    expect(shouldAdoptPresetArt('https://i.scdn.co/image/abc', { slot: 3, type: 'queue' })).toBe(false);
  });
  it('still adopts for a radio key without a logo', () => {
    expect(shouldAdoptPresetArt('https://example.org/logo.png', { slot: 2, type: 'radio' })).toBe(true);
  });
});

describe('presetOwnArt', () => {
  it('uses the first track cover of a folder key', () => {
    expect(presetOwnArt(folderKey(2, [{ url: 'u1', art: '' }, { url: 'u2', art: COVER }]))).toBe(COVER);
  });
  it("prefers the key's own art", () => {
    expect(presetOwnArt({ type: 'queue', art: 'https://a.example/x.png', items: [{ art: COVER }] })).toBe('https://a.example/x.png');
    expect(presetOwnArt({ type: 'radio', art: COVER })).toBe(COVER);
  });
  it('is empty when nothing carries art', () => {
    expect(presetOwnArt(folderKey(2, [{ url: 'u1' }]))).toBe('');
    expect(presetOwnArt(folderKey(2))).toBe('');
    expect(presetOwnArt({ type: 'radio' })).toBe('');
    expect(presetOwnArt(null)).toBe('');
  });
  it('is what the preset grid draws from', () => {
    expect(main).toContain('addCands(appArtFromBoxArt(presetOwnArt(p)))');
  });
});

describe('folderFallbackArt', () => {
  const FOLDER = 'http://192.0.2.32:9790/minimserver/*/Music/Boston/Third*20Stage/folder.jpg';
  it('gives the folder cover when no track carries one', () => {
    expect(folderFallbackArt([{ url: 'u1', art: '' }, { url: 'u2' }], FOLDER)).toBe(FOLDER);
    expect(folderFallbackArt([], ' ' + FOLDER + ' ')).toBe(FOLDER);
  });
  it('gives nothing when a track has a cover, so the first track still wins', () => {
    expect(folderFallbackArt([{ url: 'u1' }, { url: 'u2', art: COVER }], FOLDER)).toBe('');
    expect(presetOwnArt(folderKey(2, [{ url: 'u1' }, { url: 'u2', art: COVER }]))).toBe(COVER);
  });
  it('gives nothing without a folder cover', () => {
    expect(folderFallbackArt([{ url: 'u1' }], '')).toBe('');
    expect(folderFallbackArt([{ url: 'u1' }], undefined)).toBe('');
  });
  it('a folder key saved with the fallback shows it', () => {
    const items = [{ url: 'u1' }];
    expect(presetOwnArt({ ...folderKey(2, items), art: folderFallbackArt(items, FOLDER) })).toBe(FOLDER);
  });
});

describe('library folder save and play use the folder cover fallback', () => {
  const lib = readFileSync(new URL('./views/library.js', import.meta.url), 'utf8');
  it('carries the container cover into the breadcrumb and both save paths', () => {
    expect(lib).toContain("art: container.albumArtURL || ''");
    expect(lib).toContain('const art = folderFallbackArt(queueItems, last && last.art);');
    expect(lib).toContain('art: items[0].art || folderFallbackArt(items, last && last.art),');
  });
});
