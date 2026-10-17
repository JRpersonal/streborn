// #1217: a key saved by holding it while STR plays must light up as playing,
// and the app must show the saved key without waiting out its 15 s timer.
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { presetRevChanged, queueSlotActive } from './utils.js';
import { spotifySlotForContext } from './spotifynowplaying.js';

describe('spotifySlotForContext', () => {
  const presets = [
    { slot: 1, type: 'spotify', name: 'Spotify', uri: 'spotify:playlist:other' },
    { slot: 4, type: 'spotify', name: 'Morning', uri: 'spotify:playlist:abc' },
    { slot: 5, type: 'radio', name: 'spotify:playlist:abc', stream_url: 'http://radio.example.com/' },
  ];

  it('lights the key that holds the playing context', () => {
    expect(spotifySlotForContext(presets, { context: 'spotify:playlist:abc' })).toBe(4);
  });

  it('matches the station wrapper the engine announces', () => {
    expect(spotifySlotForContext(presets, { context: 'spotify:station:playlist:abc' })).toBe(4);
  });

  it('never matches on the name (#1077)', () => {
    expect(spotifySlotForContext(presets, { context: 'spotify:playlist:none' })).toBe(null);
  });

  it('names no key without a context', () => {
    expect(spotifySlotForContext(presets, { context: '' })).toBe(null);
    expect(spotifySlotForContext(presets, null)).toBe(null);
    expect(spotifySlotForContext(null, { context: 'spotify:playlist:abc' })).toBe(null);
  });

  it('takes the lowest key when an older store holds the context twice', () => {
    const dup = [...presets, { slot: 2, type: 'spotify', uri: 'spotify:playlist:abc' }];
    expect(spotifySlotForContext(dup, { context: 'spotify:playlist:abc' })).toBe(2);
  });
});

describe('queueSlotActive with a saved folder', () => {
  const key = (slot) => ({ slot, type: 'queue', name: 'Abbey Road' });
  const q = { active: true, card: 'queue:uuid:abc:64$1', slot: 3 };

  it('lights the key the playing folder was saved on', () => {
    expect(queueSlotActive(key(3), q)).toBe(true);
    expect(queueSlotActive(key(2), q)).toBe(false);
  });

  it('not once the queue stopped, or while Spotify plays', () => {
    expect(queueSlotActive(key(3), { ...q, active: false })).toBe(false);
    expect(queueSlotActive(key(3), q, 'http://192.0.2.1:8888/spotify/stream.ogg')).toBe(false);
  });

  it('only for a folder key', () => {
    expect(queueSlotActive({ slot: 3, type: 'radio' }, q)).toBe(false);
  });
});

describe('presetRevChanged', () => {
  it('the first reading is only the baseline', () => {
    expect(presetRevChanged(undefined, 0)).toBe(false);
    expect(presetRevChanged(undefined, 7)).toBe(false);
  });

  it('a write moves it, a restart of the agent too', () => {
    expect(presetRevChanged(0, 1)).toBe(true);
    expect(presetRevChanged(7, 0)).toBe(true);
    expect(presetRevChanged(7, 7)).toBe(false);
  });

  it('an agent that reports none never triggers a re-read', () => {
    expect(presetRevChanged(7, undefined)).toBe(false);
    expect(presetRevChanged(undefined, undefined)).toBe(false);
  });
});

describe('main.js wiring', () => {
  const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');

  it('refreshQueue re-reads the presets when the revision moved', () => {
    const at = main.indexOf('async function refreshQueue(');
    const fn = main.slice(at, main.indexOf('\n}\n', at));
    expect(fn).toContain('presetRevChanged(state.presetRev, rev)');
    expect(fn).toContain('_lastPresetCheck = 0');
    expect(fn).toContain('refreshPresetsIfChanged()');
  });

  it('renderPresets lights the Spotify key by its context', () => {
    const at = main.indexOf('function renderPresets(');
    const fn = main.slice(at, at + 4000);
    expect(fn).toContain('spotifySlotForContext(state.presets, state.spotifyNow)');
  });
});
