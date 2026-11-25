// The station address on a preset key's bitrate line (discussion #2103).
import { describe, it, expect } from 'vitest';
import { presetAddress } from './utils.js';
import { decodeProxyUrl } from './views/music/presets.js';

const b64url = (s) => btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

describe('presetAddress', () => {
  it('shows the stored station address of a radio key', () => {
    expect(presetAddress({ stream_url: 'https://edge.example.com/live.mp3' }, decodeProxyUrl))
      .toBe('https://edge.example.com/live.mp3');
  });

  it('decodes a key saved through the stream proxy back to the station address', () => {
    const real = 'http://stream.example.org:8000/radio?ads=1';
    const proxied = `http://192.0.2.1:8888/stream/raw?u=${b64url(real)}`;
    expect(presetAddress({ stream_url: proxied }, decodeProxyUrl)).toBe(real);
  });

  it('shows nothing for Spotify, library, queue or empty keys', () => {
    expect(presetAddress({ type: 'spotify', stream_url: 'spotify:playlist:x' }, decodeProxyUrl)).toBe('');
    expect(presetAddress({ source: 'NAS', stream_url: 'http://192.0.2.5/track.flac' }, decodeProxyUrl)).toBe('');
    expect(presetAddress({ type: 'queue', stream_url: '' }, decodeProxyUrl)).toBe('');
    expect(presetAddress({ items: [{}], stream_url: 'http://x/y' }, decodeProxyUrl)).toBe('');
    expect(presetAddress(null, decodeProxyUrl)).toBe('');
    expect(presetAddress({ stream_url: '' }, decodeProxyUrl)).toBe('');
  });

  it('shows nothing when the stored value is not a web address', () => {
    expect(presetAddress({ stream_url: 'ORION:station:abc' }, decodeProxyUrl)).toBe('');
  });
});
