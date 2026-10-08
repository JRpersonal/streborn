// A key holding a single music-library track showed "- kbit/s". Since v1.0.6
// such a track plays straight from the media server, so there is no measured
// rate and the placeholder only made the key look broken (#1065).
import { describe, it, expect } from 'vitest';
import { presetBitrateLine } from './utils.js';

describe('presetBitrateLine', () => {
  it('leaves the line out for a library track with no rate', () => {
    expect(presetBitrateLine({ type: 'radio', source: 'Synology', stream_url: 'http://nas/1.flac' }, 0)).toBe('');
  });

  it('leaves the line out for a saved folder and the queue', () => {
    expect(presetBitrateLine({ type: 'radio', items: [{ url: 'http://nas/1.flac' }] }, 0)).toBe('');
    expect(presetBitrateLine({ type: 'queue' }, 0)).toBe('');
  });

  it('still shows a rate a library key does carry', () => {
    expect(presetBitrateLine({ source: 'Synology' }, 320)).toBe('320 kbit/s');
  });

  it('keeps the placeholder on a radio key that has no rate yet', () => {
    expect(presetBitrateLine({ type: 'radio', stream_url: 'http://s/x' }, 0)).toBe('- kbit/s');
    expect(presetBitrateLine({ type: 'radio', stream_url: 'http://s/x' }, 128)).toBe('128 kbit/s');
  });
});
