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
// extractable, see the view-extraction trap).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const mainJS = readFileSync(join(here, 'main.js'), 'utf-8');

// Slices one top-level function body out of main.js, from its declaration to
// the next unindented closing brace. Throws on a renamed marker so a refactor
// fails loudly instead of silently matching nothing.
function fnBody(marker) {
  const start = mainJS.indexOf(marker);
  if (start < 0) throw new Error(`marker not found: ${marker}`);
  const end = mainJS.indexOf('\n}', start);
  if (end < 0) throw new Error(`no closing brace after: ${marker}`);
  return mainJS.slice(start, end);
}

describe('automatic preset writes are freshness-guarded (#758)', () => {
  it('the guard re-reads the slot and compares the station identity', () => {
    const guard = fnBody('async function setPresetIfUnchanged(');
    expect(guard).toContain('GetPresets(');
    expect(guard).toContain('live.stream_url === cached.stream_url');
    expect(guard).toContain('live.name === cached.name');
  });

  it('the bitrate stamp goes through the guard, gated on the playing identity', () => {
    const fn = fnBody('function scheduleLiveBitrate(');
    expect(fn).toContain('setPresetIfUnchanged(');
    expect(fn).toContain('nativeSlotStale(');
    expect(fn).not.toMatch(/\bSetPreset\(/);
  });

  it('the render-time art adopt and bitrate mirror go through the guard', () => {
    const fn = fnBody('function renderPresets(');
    expect(fn).toContain('setPresetIfUnchanged(');
    expect(fn).not.toMatch(/\bSetPreset\(/);
  });

  it('the logo heal goes through the guard', () => {
    const fn = fnBody('async function healPresetLogos(');
    expect(fn).toContain('setPresetIfUnchanged(');
    expect(fn).not.toMatch(/\bSetPreset\(/);
  });
});
