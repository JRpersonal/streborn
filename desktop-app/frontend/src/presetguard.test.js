// Every AUTOMATIC preset write (a measured bitrate, an adopted logo, a healed
// art chain) goes through setPresetIfUnchanged, which re-reads the slot from
// the box and refuses to write when another client changed it meanwhile.
//
// Field evidence (#758, bundle 2026-08-28): the reporter saved a station from
// the phone page and the desktop app wrote the previous station back over it
// six seconds later, built from a preset cache up to PRESET_RECHECK_MS stale.
// The marge trail shows them fighting the app through six rounds before they
// gave up and filed the bug. A raw fire-and-forget SetPreset in a self-heal
// path is that bug coming back, so these are source-level assertions in the
// style of presettile.test.js (renderPresets is DOM-bound and deliberately not
// extractable, see the view-extraction trap). srcOf cuts each function out by
// name and throws on a renamed one, so a refactor fails loudly instead of
// silently matching nothing.
import { describe, it, expect } from 'vitest';
import { srcOf } from './test/source.js';

describe('automatic preset writes are freshness-guarded (#758)', () => {
  it('the guard re-reads the slot and compares the station identity', () => {
    const guard = srcOf('setPresetIfUnchanged');
    expect(guard).toContain('GetPresets(');
    expect(guard).toContain('live.stream_url === cached.stream_url');
    expect(guard).toContain('live.name === cached.name');
  });

  it('the bitrate stamp goes through the guard, gated on the playing identity', () => {
    const fn = srcOf('scheduleLiveBitrate');
    expect(fn).toContain('setPresetIfUnchanged(');
    expect(fn).toContain('nativeSlotStale(');
    expect(fn).not.toMatch(/\bSetPreset\(/);
  });

  it('the render-time art adopt and bitrate mirror go through the guard', () => {
    const fn = srcOf('renderPresets');
    expect(fn).toContain('setPresetIfUnchanged(');
    expect(fn).not.toMatch(/\bSetPreset\(/);
  });

  it('the logo heal goes through the guard', () => {
    const fn = srcOf('healPresetLogos');
    expect(fn).toContain('setPresetIfUnchanged(');
    expect(fn).not.toMatch(/\bSetPreset\(/);
  });
});
