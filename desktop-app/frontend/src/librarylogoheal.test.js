// #1366: the covers of music-library keys were gone after an app update that
// touched neither presets nor art. Two app-side gaps around that report. The
// radio-directory logo heal looked a library key up by its NAME when the key
// showed no preset-level art (a saved folder stores none when its tracks carry
// covers), so a folder called "Jazz" could be painted with some radio's logo.
// And the app never told the speaker where a music server lives after
// browsing it, so a key whose server moved stayed dark until a press.
import { describe, it, expect } from 'vitest';
import { isLibraryPreset, presetSignature } from './utils.js';
import { srcOf } from './test/source.js';

describe('isLibraryPreset', () => {
  it('names a saved folder, a single server track, and a key with a track list', () => {
    expect(isLibraryPreset({ type: 'queue', name: 'Jazz', items: [{ url: 'http://nas:9790/1.flac' }] })).toBe(true);
    expect(isLibraryPreset({ type: 'radio', name: 'Track', source: 'MinimServer', stream_url: 'http://nas:9790/1.flac' })).toBe(true);
    expect(isLibraryPreset({ type: 'radio', name: 'Old folder', items: [{ url: 'http://nas:9790/1.flac' }] })).toBe(true);
  });

  it('leaves radio, Spotify and native keys to their own rules', () => {
    expect(isLibraryPreset({ type: 'radio', name: 'NDR 2', stream_url: 'http://s/x' })).toBe(false);
    expect(isLibraryPreset({ type: 'spotify', name: 'Mix', uri: 'spotify:playlist:x' })).toBe(false);
    expect(isLibraryPreset({ type: 'native', name: 'Pandora' })).toBe(false);
    expect(isLibraryPreset(null)).toBe(false);
  });
});

describe('the radio-directory logo heal never touches a library key (#1366)', () => {
  const fn = srcOf('healPresetLogos');

  it('skips library keys when it picks what to look up', () => {
    // The filter, not only the persist guard: an in-memory p.art = logo paints
    // the tile until the next reload even when the write is refused.
    const filter = fn.slice(fn.indexOf('const missing'), fn.indexOf('if (missing.length'));
    expect(filter).toContain('!isLibraryPreset(p)');
    expect(filter).toContain('!isNativeServicePreset(p)');
  });

  it('refuses to persist a directory logo onto one either', () => {
    const adopt = fn.slice(fn.indexOf('if (!pick) return;'));
    expect(adopt).toContain('isLibraryPreset(p)) return;');
    expect(adopt).toContain('setPresetIfUnchanged(');
  });
});

describe('browsing a music server tells the speaker to look it up (#1366)', () => {
  it('nudges the agent after the first page of a server, once per server and address', () => {
    const nudge = srcOf('nudgeBoxLibrary');
    expect(nudge).toContain('/api/library/browse?udn=');
    expect(nudge).toContain('libState.nudged.has(key)');
    // The address stays on this side: the speaker resolves the server itself.
    expect(nudge).not.toMatch(/address=|location=/);
    expect(nudge).toContain("box.kind === 'stock'");
    const browse = srcOf('libraryBrowseCurrent');
    expect(browse).toContain('nudgeBoxLibrary(libState.currentUDN)');
  });
});

// #1366, v1.0.21 bundle: the speaker moved the album key on 6 and the playlist
// key on 2 to the music server's new address on their press, covers included,
// but the app's 15 s preset recheck compared only the preset-level art. A
// saved album carries its cover on its tracks, so nothing looked different and
// the keys kept the letter placeholder until the app was restarted.
describe('presetSignature', () => {
  const album = (host) => [{
    slot: 6, type: 'queue', name: 'Moving Pictures', source: 'MinimServer',
    items: [{ url: `http://${host}:9790/m/01.m4a`, art: `http://${host}:9790/m/cover.jpg` }],
  }];

  it("changes when a folder key moves to its server's new address", () => {
    expect(presetSignature(album('192.0.2.47'))).not.toBe(presetSignature(album('192.0.2.11')));
  });

  it('stays the same for an identical re-read, so nothing redraws', () => {
    expect(presetSignature(album('192.0.2.47'))).toBe(presetSignature(album('192.0.2.47')));
    const radio = [{ slot: 1, type: 'radio', name: 'FIP', stream_url: 'http://s/fip', art: 'https://s/logo.png' }];
    expect(presetSignature(radio)).toBe(presetSignature(radio.map((p) => ({ ...p }))));
  });

  it('is what the preset recheck compares', () => {
    expect(srcOf('refreshPresetsIfChanged')).toContain('presetSignature(fresh) === presetSignature(state.presets)');
  });
});
