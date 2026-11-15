// One "Playing on <speaker>" line per speaker (a mail report, 2026-11: while
// a speaker switched stations, the line stood under the old and the new
// station at once, and one station could be listed twice).
import { describe, it, expect, beforeEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { claimPlay, joinReview, onPlayClaim, ownsPlay, playLine, playLineOutdated, playLinesToDrop, resetPlayClaims, sameMusic, statusNowItem } from './playclaim.js';
import { setLocale } from '../i18n/index.js';

setLocale('en');
import { uniqueStations } from '../views/music/ui2radio.js';

const ok = (text = 'Playing on Kitchen') => ({ kind: 'ok', text });

describe('play claims', () => {
  beforeEach(() => resetPlayClaims());

  it('tags a verdict with its speaker and station, and leaves an untagged one alone', () => {
    expect(playLine(ok(), '192.0.2.1', { url: 'http://a', name: 'A' }))
      .toEqual({ kind: 'ok', text: 'Playing on Kitchen', playHost: '192.0.2.1', playUrl: 'http://a', playName: 'A' });
    expect(playLine(ok(), '')).toEqual(ok());
    expect(playLine(null, '192.0.2.1')).toBe(null);
  });

  it('a new play on a speaker drops the old station line, not the new one, nor another speaker', () => {
    const status = new Map([
      ['old', playLine(ok(), '192.0.2.1', { name: 'Old' })],
      ['new', playLine({ kind: 'busy', text: 'Starting on Kitchen...' }, '192.0.2.1', { name: 'New' })],
      ['other', playLine(ok('Playing on Bath'), '192.0.2.2', { name: 'Other' })],
      ['failed', { kind: 'error', text: 'did not play' }],
    ]);
    expect(playLinesToDrop(status, '192.0.2.1', 'new')).toEqual(['old']);
    expect(playLinesToDrop(status, '192.0.2.1', null)).toEqual(['old', 'new']);
    expect(playLinesToDrop(status, '', 'new')).toEqual([]);
  });

  it('tells every view of a claim, and only the newest play owns the speaker', () => {
    const seen = [];
    const off = onPlayClaim((host, owner) => seen.push(`${host} ${owner}`));
    claimPlay('192.0.2.1', 'radio:a');
    expect(ownsPlay('192.0.2.1', 'radio:a')).toBe(true);
    claimPlay('192.0.2.1', 'recent:b');
    expect(ownsPlay('192.0.2.1', 'radio:a')).toBe(false);
    expect(ownsPlay('192.0.2.1', 'recent:b')).toBe(true);
    expect(ownsPlay('192.0.2.2', 'radio:a')).toBe(true);
    off();
    claimPlay('192.0.2.1', 'radio:c');
    expect(seen).toEqual(['192.0.2.1 radio:a', '192.0.2.1 recent:b']);
  });

  it('a listener that throws does not stop the others', () => {
    let reached = false;
    onPlayClaim(() => { throw new Error('boom'); });
    onPlayClaim(() => { reached = true; });
    claimPlay('192.0.2.1', 'radio:a');
    expect(reached).toBe(true);
  });

  it('a "Playing" line goes once the speaker reports another station, and stays while it switches', () => {
    const st = playLine(ok(), '192.0.2.1', { url: 'http://old', name: 'Old' });
    const now = (o) => ({ host: '192.0.2.1', playState: 'PLAY_STATE', location: 'http://new', name: 'New', ...o });
    expect(playLineOutdated(st, now())).toBe(true);
    expect(playLineOutdated(st, now({ location: 'http://old' }))).toBe(false);
    expect(playLineOutdated(st, now({ name: 'Old' }))).toBe(false);
    expect(playLineOutdated(st, now({ playState: 'BUFFERING_STATE' }))).toBe(false);
    expect(playLineOutdated(st, now({ location: '' }))).toBe(false);
    expect(playLineOutdated(st, now({ host: '192.0.2.2' }))).toBe(false);
    // Only a confirmed "Playing" line: a "Sent" line waits for its own upgrade.
    expect(playLineOutdated({ ...st, kind: 'warn' }, now())).toBe(false);
    expect(playLineOutdated(ok(), now())).toBe(false);
  });
});

describe('station rows', () => {
  it('lists a station once, in its first place', () => {
    const a = { stationuuid: 'u1', name: 'A', url: 'http://a' };
    const b = { name: 'B', url: 'http://b' };
    const out = uniqueStations([a, b, { ...a }, { ...b }, { stationuuid: 'u2', name: 'A', url: 'http://a' }]);
    expect(out.map((s) => s.stationuuid || s.name)).toEqual(['u1', 'B', 'u2']);
    expect(uniqueStations(null)).toEqual([]);
  });

  it('both music views claim the speaker on a play and drop the other lines', () => {
    for (const f of ['../views/music/ui2radio.js', '../views/recent.js']) {
      const src = readFileSync(new URL(f, import.meta.url), 'utf8');
      expect(src, f).toMatch(/claimPlay\(host, owner\)/);
      expect(src, f).toMatch(/onPlayClaim\(/);
      expect(src, f).toMatch(/ownsPlay\(host, owner\)/);
    }
  });
});

// "+" on a speaker that plays something else (the join review): joining a
// zone ends that speaker's own music, so the row first says what stops and
// what plays instead.
describe('join review', () => {
  const radio = (name, location = `http://radio.example/${name.toLowerCase()}`) => ({ name, source: 'INTERNET_RADIO', location });

  it('names what stops and what plays instead', () => {
    const r = joinReview({ name: 'Kitchen', playing: true, stops: radio('Jazz24'), plays: radio('FluxFM'), master: 'Living room' });
    expect(r.text).toBe('Kitchen stops Jazz24 and plays FluxFM.');
    expect(r).toMatchObject({ stops: 'Jazz24', plays: 'FluxFM' });
  });

  it('names the main speaker when the group music has no name, and says so when the speaker names nothing', () => {
    expect(joinReview({ name: 'Kitchen', playing: true, stops: radio('Jazz24'), plays: { name: '' }, master: 'Living room' }).text)
      .toBe('Kitchen stops Jazz24 and plays along with Living room.');
    expect(joinReview({ name: 'Kitchen', playing: true, stops: { name: '' }, plays: radio('FluxFM'), master: 'Living room' }).text)
      .toBe('Kitchen stops what it is playing and plays FluxFM.');
    expect(joinReview({ name: 'Kitchen', playing: true, master: 'Living room' }).text)
      .toBe('Kitchen stops what it is playing and plays along with Living room.');
  });

  it('asks nothing of an idle speaker, one in standby or one that already plays the same station', () => {
    expect(joinReview({ name: 'Kitchen', playing: false, stops: radio('Jazz24'), plays: radio('FluxFM') })).toBe(null);
    expect(joinReview({ name: 'Kitchen', playing: true, stops: radio('FluxFM', 'http://flux.example/live'), plays: { ...radio(' fluxfm ', 'http://flux.example/live'), source: 'internet_radio' } })).toBe(null);
    // STR's own station stream on the agent, which the speaker reports as UPNP.
    const str = { name: 'FluxFM', source: 'UPNP', location: 'http://127.0.0.1:8888/stream/raw?u=aHR0cDovL3g' };
    expect(joinReview({ name: 'Kitchen', playing: true, stops: str, plays: { ...str } })).toBe(null);
    expect(joinReview({ name: '', playing: true, stops: radio('Jazz24'), plays: radio('FluxFM') })).toBe(null);
    expect(joinReview()).toBe(null);
  });

  // The name alone is not the music: AUX names the input, Bluetooth the
  // phone, and family Spotify accounts share playlist names.
  it('still reviews when both speakers report the same name from a different source or stream', () => {
    const cases = [
      // The same station name on another stream (or another source).
      [radio('FluxFM', 'http://flux.example/a'), radio('FluxFM', 'http://flux.example/b')],
      [radio('FluxFM'), { name: 'FluxFM', source: 'TUNEIN', location: '/v1/playback/station/s1' }],
      // Each speaker on its own AUX input.
      [{ name: 'AUX IN', source: 'AUX', location: '' }, { name: 'AUX IN', source: 'AUX', location: '' }],
      [{ name: 'AUX IN', source: 'AUX', location: 'AUX' }, { name: 'AUX IN', source: 'AUX', location: 'AUX' }],
      // Two phones with the same name over Bluetooth.
      [{ name: 'iPhone', source: 'BLUETOOTH', location: '' }, { name: 'iPhone', source: 'BLUETOOTH', location: '' }],
      // Two accounts, one playlist name, even on the same container path.
      [{ name: 'Liked Songs', source: 'SPOTIFY', location: '/playback/container/c3BvdGlmeQ==' },
        { name: 'Liked Songs', source: 'SPOTIFY', location: '/playback/container/c3BvdGlmeQ==' }],
      // STR's Spotify stream is the same address on every speaker.
      [{ name: 'Daily Mix 1', source: 'UPNP', location: 'http://127.0.0.1:8888/spotify/stream' },
        { name: 'Daily Mix 1', source: 'UPNP', location: 'http://127.0.0.1:8888/spotify/stream' }],
      // A bare name has no source and no stream to compare.
      ['FluxFM', 'FluxFM'],
    ];
    for (const [stops, plays] of cases) {
      expect(sameMusic(stops, plays)).toBe(false);
      const r = joinReview({ name: 'Kitchen', playing: true, stops, plays, master: 'Living room' });
      expect(r).not.toBe(null);
      // Not "stops AUX IN and plays AUX IN", which reads as if nothing changed.
      expect(r.text).toBe('Kitchen stops what it is playing and plays along with Living room.');
    }
  });

  it('reads name, source and stream from a now_playing answer, station first, entities decoded', () => {
    expect(statusNowItem('<nowPlaying deviceID="x" source="INTERNET_RADIO"><ContentItem source="INTERNET_RADIO" location="http://radio.example/?a=1&amp;b=2"><itemName>Jazz &amp; Soul</itemName></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>'))
      .toEqual({ name: 'Jazz & Soul', source: 'INTERNET_RADIO', location: 'http://radio.example/?a=1&b=2' });
    expect(statusNowItem('<nowPlaying source="UPNP"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/2"><itemName>Song title</itemName></ContentItem><stationName>Jazz24</stationName></nowPlaying>'))
      .toEqual({ name: 'Jazz24', source: 'UPNP', location: 'http://127.0.0.1:8888/stream/2' });
    expect(statusNowItem('<nowPlaying source="STANDBY"><ContentItem source="STANDBY" /></nowPlaying>')).toEqual({ name: '', source: 'STANDBY', location: '' });
    expect(statusNowItem(null)).toEqual({ name: '', source: '', location: '' });
  });
});