// Tests for the pure decision helpers in utils.js.
import { describe, it, expect } from 'vitest';
import { savePresetCase } from './utils.js';

const FRESH_MS = 2 * 60 * 1000;
const NOW = 1_000_000_000;
const freshPlay = { url: 'http://radio.example.com/stream', at: NOW - 5_000 };
const stalePlay = { url: 'http://radio.example.com/stream', at: NOW - FRESH_MS - 1 };

describe('savePresetCase', () => {
  it('classifies Spotify locations first, whatever the slot says', () => {
    expect(savePresetCase('http://192.0.2.1:8888/spotify/stream-3.ogg', 3, freshPlay, NOW, FRESH_MS)).toBe('spotify');
    expect(savePresetCase('/playback/container/c3BvdGlmeQ==', null, null, NOW, FRESH_MS)).toBe('spotify');
  });
  it('prefers the app`s own fresh play record over a proxy-slot copy (#252)', () => {
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, freshPlay, NOW, FRESH_MS)).toBe('app-play');
  });
  it('copies the source preset when the app record is stale or absent', () => {
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, stalePlay, NOW, FRESH_MS)).toBe('copy-slot');
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, null, NOW, FRESH_MS)).toBe('copy-slot');
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, { at: NOW }, NOW, FRESH_MS)).toBe('copy-slot'); // no url
  });
  it('saves the box-reported now-playing for non-proxy streams', () => {
    expect(savePresetCase('http://radio.example.com/live.mp3', null, freshPlay, NOW, FRESH_MS)).toBe('direct');
    expect(savePresetCase('', null, null, NOW, FRESH_MS)).toBe('direct');
  });
});
