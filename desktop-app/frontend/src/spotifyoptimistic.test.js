import { describe, it, expect } from 'vitest';
import { optimisticSpotifyLocation, activeSlotFromLocation } from './utils.js';

const LOOP = 'http://127.0.0.1:8888';

// #1077 issue 13: clicking Spotify key 1 while key 4 played left key 4 lit as
// "stream starting" and lit key 1 only about six seconds later.
describe('optimisticSpotifyLocation', () => {
  it('names the clicked key, so that key lights up at once', () => {
    const loc = optimisticSpotifyLocation(LOOP, 1);
    expect(loc).toBe('http://127.0.0.1:8888/spotify/stream-1.ogg');
    expect(activeSlotFromLocation(loc)).toBe(1);
  });

  it('is exactly what the speaker reports, so the optimistic window releases early', () => {
    for (let n = 1; n <= 6; n++) {
      expect(optimisticSpotifyLocation(LOOP, n)).toBe(`${LOOP}/spotify/stream-${n}.ogg`);
    }
  });

  it('falls back to the generic stream for a slot that is not a key', () => {
    expect(optimisticSpotifyLocation(LOOP, 0)).toBe(`${LOOP}/spotify/stream.ogg`);
    expect(optimisticSpotifyLocation(LOOP, undefined)).toBe(`${LOOP}/spotify/stream.ogg`);
    expect(optimisticSpotifyLocation(LOOP, 7)).toBe(`${LOOP}/spotify/stream.ogg`);
  });
});
