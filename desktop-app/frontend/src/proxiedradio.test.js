// #922, reported by shorty310 eight hours after v0.9.79 shipped: a station
// started from Find stations plays, but the app shows no artist and no track,
// while the page the speaker itself serves shows both.
//
// The app asked the speaker for the live title only when it could resolve a
// preset SLOT number out of the now-playing location. A station started from
// Find stations is pushed as /stream/raw?u=<base64> and belongs to no preset, so
// there is no slot, so the poll never ran. The agent had "Rush - Freewill" the
// whole time, which is exactly why the remote showed it.
//
// A slot number was always the wrong question. What both call sites actually
// want to know is whether the stream proxy is in the path, because that is where
// an ICY title comes from.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { proxiedRadioPlaying, activeSlotFromLocation } from './utils.js';

const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');

// A real ORION descriptor: /station?data=<base64url of the JSON>.
const orion = (streamUrl) =>
  '/station?data=' + Buffer.from(JSON.stringify({ streamUrl, name: 'Some Station' }))
    .toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

describe('what counts as proxied radio', () => {
  it('says yes to the ad-hoc station that started this bug', () => {
    // The exact location from the reporter's bundle.
    const raw = 'http://192.0.2.1:8888/stream/raw?u=aHR0cHM6Ly8yLm15c3RyZWFtaW5nLm5ldC9lci1hcHAvcnVzaC9pY2VjYXN0LmF1ZGlv';
    expect(proxiedRadioPlaying(raw)).toBe(true);
    // And the reason it was broken: there is no slot to find in it.
    expect(activeSlotFromLocation(raw)).toBe(null);
  });

  it('still says yes to everything it said yes to before', () => {
    expect(proxiedRadioPlaying('http://192.0.2.1:8888/stream/3')).toBe(true);
    expect(proxiedRadioPlaying(orion('http://192.0.2.1:8888/stream/5'))).toBe(true);
  });

  it('covers an ORION descriptor wrapping the ad-hoc form too', () => {
    expect(proxiedRadioPlaying(orion('http://192.0.2.1:8888/stream/raw?u=aHR0cDovL3g='))).toBe(true);
  });

  // The two cases that must stay false, for opposite reasons: Spotify carries its
  // own now-playing fields, and a native station goes straight to the station's
  // own server with the proxy nowhere in the path, so there is no title to ask
  // anybody for.
  it('says no to Spotify and to native playback', () => {
    expect(proxiedRadioPlaying('http://192.0.2.1:8888/spotify/stream-2.ogg')).toBe(false);
    expect(proxiedRadioPlaying('https://2.mystreaming.net/er-app/rush/icecast.audio')).toBe(false);
    expect(proxiedRadioPlaying(orion('https://stream.example.org/live.mp3'))).toBe(false);
    expect(proxiedRadioPlaying('')).toBe(false);
    expect(proxiedRadioPlaying(null)).toBe(false);
  });
});

describe('both places that ask', () => {
  // Fixing only the poll would fetch a title the status line then refused to
  // print, so the two have to agree.
  it('use the one predicate, not a slot number', () => {
    const poll = main.slice(main.indexOf('const isRadio ='), main.indexOf('const isRadio =') + 120);
    expect(poll).toContain('proxiedRadioPlaying(loc)');
    expect(poll).not.toContain('activeSlotFromLocation');

    const at = main.indexOf("} else if (proxiedRadioPlaying(loc) && state.nowTitle) {");
    expect(at, 'the status line must use it as well').toBeGreaterThan(-1);
  });

  // activeSlotFromLocation is still the right tool for lighting up a preset
  // tile; this change was not about deleting it.
  it('leaves the preset highlighting on the slot lookup', () => {
    expect(main).toContain('const activeSlot = activeSlotFromLocation(state.nowLocation);');
  });
});
