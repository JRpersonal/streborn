// Which preset tile lights up green while something is playing.
//
// The speaker names its content item differently depending on how the station
// is played, and the app has to recognise all of them. Native radio presets
// broke this: the box plays them itself and the location is an ORION
// descriptor, so the plain /stream/<n> match found nothing and no tile lit up
// (discussion #555, with the speaker reporting LOCAL_INTERNET_RADIO +
// PLAY_STATE + the station name, i.e. playing perfectly well).
import { describe, it, expect } from 'vitest';
import { activeSlotFromLocation, slotFromOrionStation, nativeSlotStale } from './utils.js';

// Builds the descriptor exactly as the agent writes it: unpadded base64url.
function orion(payload) {
  const json = JSON.stringify(payload);
  const b64 = Buffer.from(json, 'utf8').toString('base64')
    .replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  return '/station?data=' + b64;
}

describe('activeSlotFromLocation', () => {
  it('finds the slot in a stream proxy URL', () => {
    expect(activeSlotFromLocation('http://127.0.0.1:8888/stream/3')).toBe(3);
    expect(activeSlotFromLocation('http://127.0.0.1:8888/stream/6?x=1')).toBe(6);
  });

  it('prefers the Spotify per-slot URL', () => {
    expect(activeSlotFromLocation('http://192.0.2.1:8888/spotify/stream-4.ogg')).toBe(4);
  });

  it('finds the slot inside a native ORION station descriptor', () => {
    const loc = orion({
      imageUrl: '', isRealtime: true, name: 'absolute relax',
      streamType: 'liveRadio', streamUrl: 'http://127.0.0.1:8888/stream/2',
    });
    expect(activeSlotFromLocation(loc)).toBe(2);
  });

  it('handles the full path form the speaker reports', () => {
    const loc = '/core02/svc-bmx-adapter-orion/prod/orion' + orion({
      name: 'WDR 5', streamUrl: 'http://127.0.0.1:8888/stream/6',
    });
    expect(activeSlotFromLocation(loc)).toBe(6);
  });

  it('accepts the standard base64 alphabet older builds wrote', () => {
    const json = JSON.stringify({ streamUrl: 'http://127.0.0.1:8888/stream/5' });
    const loc = '/station?data=' + Buffer.from(json, 'utf8').toString('base64');
    expect(activeSlotFromLocation(loc)).toBe(5);
  });

  it('returns null rather than throwing on rubbish', () => {
    expect(activeSlotFromLocation('')).toBe(null);
    expect(activeSlotFromLocation(null)).toBe(null);
    expect(activeSlotFromLocation('/station?data=not-base64!!')).toBe(null);
    expect(activeSlotFromLocation('/station?data=' + Buffer.from('{oops', 'utf8').toString('base64'))).toBe(null);
    expect(activeSlotFromLocation('http://example.com/some/other/thing')).toBe(null);
  });

  it('returns null for a station whose payload carries no proxy slot', () => {
    // A native station pointing straight at a CDN has no slot to find, and
    // guessing one would light up the wrong tile.
    const loc = orion({ name: 'Direct', streamUrl: 'https://cdn.example/live.mp3' });
    expect(activeSlotFromLocation(loc)).toBe(null);
  });
});

describe('slotFromOrionStation', () => {
  it('ignores a location that is not a station descriptor', () => {
    expect(slotFromOrionStation('http://127.0.0.1:8888/stream/3')).toBe(null);
  });
});

// A native descriptor keeps playing the recalled station even after the box's
// own preset list is re-synced (edited from the Bose remote / ST Remote), so a
// slot can come to hold a different station than the audio. The slot number
// then still points at that slot, and the tile must not stay lit (#758).
describe('nativeSlotStale', () => {
  it('keeps a matching slot lit (url matches)', () => {
    expect(nativeSlotStale({
      presetName: 'Klove', presetUrl: 'http://cdn/a.mp3',
      playingName: 'Klove', playingUrl: 'http://cdn/a.mp3',
    })).toBe(false);
  });

  it('keeps a matching slot lit when only the name matches', () => {
    // The stored URL can be normalised differently from the descriptor form;
    // an identical station name is enough to treat the slot as the live one.
    expect(nativeSlotStale({
      presetName: 'Essentially Rush', presetUrl: 'http://cdn/rush',
      playingName: 'Essentially Rush', playingUrl: 'http://other/rush2',
    })).toBe(false);
  });

  it('suppresses a slot whose station changed under the live audio (#758)', () => {
    // Key 6 was Rush and is still playing; the list re-synced key 6 to Klove.
    expect(nativeSlotStale({
      presetName: 'Klove', presetUrl: 'http://cdn/klove',
      playingName: 'Essentially Rush', playingUrl: 'http://cdn/rush',
    })).toBe(true);
  });

  it('does not judge when the descriptor yields no identity (stays lit)', () => {
    expect(nativeSlotStale({
      presetName: 'Klove', presetUrl: 'http://cdn/klove',
      playingName: '', playingUrl: '',
    })).toBe(false);
  });

  it('falls back to the url when the playing name is missing', () => {
    expect(nativeSlotStale({
      presetName: 'Klove', presetUrl: 'http://cdn/klove',
      playingName: '', playingUrl: 'http://cdn/rush',
    })).toBe(true);
  });
});
