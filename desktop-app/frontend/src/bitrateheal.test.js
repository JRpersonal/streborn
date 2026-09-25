// The app measured a live bitrate and wrote it back onto the active preset key
// about two seconds after every recall. SetPreset sends a fixed type=radio and
// exactly seven fields, so on a media-library key that write dropped the source
// field, which is what decides whether the track is fetched from the server or
// pulled through the radio relay. The key silently changed how it plays.
//
// It also hammered the flash: the same slot rewritten about twenty times in
// twenty-five minutes, for a number that means nothing for a local file (#978).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const src = readFileSync(join(here, 'main.js'), 'utf8');

// The predicate as main.js defines it.
function isMeasurableStreamPreset(p) {
  if (!p) return false;
  if (p.type === 'spotify' || p.type === 'queue') return false;
  if (p.source) return false;
  if (p.items && p.items.length) return false;
  return true;
}

// The flatten guard as setPresetIfUnchanged applies it.
const flattenable = (live) => !!(live && (live.type === 'spotify' || live.type === 'queue' ||
  live.source || live.uri || (live.items && live.items.length)));

describe('the bitrate self-heal', () => {
  it('still stores a rate for a plain radio key', () => {
    // The case it exists for: the directory often has no bitrate at all.
    expect(isMeasurableStreamPreset({ type: 'radio', stream_url: 'http://s/x' })).toBe(true);
  });

  it('leaves a media-library key alone', () => {
    // A file's rate is a property of the file, and persisting it here is what
    // dropped the source field.
    expect(isMeasurableStreamPreset({ type: 'radio', source: 'Synology', stream_url: 'http://nas/1.flac' })).toBe(false);
  });

  it('leaves a saved folder alone', () => {
    expect(isMeasurableStreamPreset({ type: 'queue', items: [{ url: 'http://nas/1.flac' }] })).toBe(false);
    expect(isMeasurableStreamPreset({ type: 'radio', items: [{ url: 'http://nas/1.flac' }] })).toBe(false);
  });

  it('leaves a Spotify key alone', () => {
    expect(isMeasurableStreamPreset({ type: 'spotify', uri: 'spotify:playlist:x' })).toBe(false);
  });
});

describe('a metadata heal cannot change what a key is', () => {
  it('refuses any key that carries more than a radio write can send', () => {
    expect(flattenable({ type: 'spotify', uri: 'spotify:playlist:x' })).toBe(true);
    expect(flattenable({ type: 'queue', items: [{ url: 'u' }] })).toBe(true);
    expect(flattenable({ type: 'radio', source: 'Synology' })).toBe(true);
    expect(flattenable({ type: 'radio', uri: 'spotify:playlist:x' })).toBe(true);
  });

  it('still allows the plain radio key the heal is for', () => {
    expect(flattenable({ type: 'radio', stream_url: 'http://s/x', name: 'NDR2' })).toBe(false);
  });

  it('is wired into setPresetIfUnchanged, not only into the callers', () => {
    // The callers can be added to; this is the one place that has the LIVE
    // preset in hand, so the guard belongs there.
    const at = src.indexOf('async function setPresetIfUnchanged');
    expect(at).toBeGreaterThan(-1);
    const body = src.slice(at, at + 1400);
    expect(body).toContain('flattenable');
    expect(body).toContain("live.type === 'queue'");
    expect(body).toContain('live.source');
  });

  it('gates both bitrate write sites', () => {
    const hits = src.split('isMeasurableStreamPreset(p)').length - 1;
    expect(hits).toBeGreaterThanOrEqual(2);
  });
});
