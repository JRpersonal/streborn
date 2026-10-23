// #1366: the covers of music-library keys were gone after an app update that
// touched neither presets nor art. Two app-side gaps around that report. The
// radio-directory logo heal looked a library key up by its NAME when the key
// showed no preset-level art (a saved folder stores none when its tracks carry
// covers), so a folder called "Jazz" could be painted with some radio's logo.
// And the app never told the speaker where a music server lives after
// browsing it, so a key whose server moved stayed dark until a press.
import { describe, it, expect } from 'vitest';
import { isLibraryPreset } from './utils.js';
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
