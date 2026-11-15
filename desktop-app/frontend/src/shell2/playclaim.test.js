// One "Playing on <speaker>" line per speaker (a mail report, 2026-11: while
// a speaker switched stations, the line stood under the old and the new
// station at once, and one station could be listed twice).
import { describe, it, expect, beforeEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { claimPlay, joinReview, onPlayClaim, ownsPlay, playLine, playLineOutdated, playLinesToDrop, resetPlayClaims, statusNowName } from './playclaim.js';
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
  it('names what stops and what plays instead', () => {
    const r = joinReview({ name: 'Kitchen', playing: true, stops: 'Jazz24', plays: 'FluxFM', master: 'Living room' });
    expect(r.text).toBe('Kitchen stops Jazz24 and plays FluxFM.');
    expect(r).toMatchObject({ stops: 'Jazz24', plays: 'FluxFM' });
  });

  it('names the main speaker when the group music has no name, and says so when the speaker names nothing', () => {
    expect(joinReview({ name: 'Kitchen', playing: true, stops: 'Jazz24', plays: '', master: 'Living room' }).text)
      .toBe('Kitchen stops Jazz24 and plays along with Living room.');
    expect(joinReview({ name: 'Kitchen', playing: true, stops: '', plays: 'FluxFM', master: 'Living room' }).text)
      .toBe('Kitchen stops what it is playing and plays FluxFM.');
    expect(joinReview({ name: 'Kitchen', playing: true, master: 'Living room' }).text)
      .toBe('Kitchen stops what it is playing and plays along with Living room.');
  });

  it('asks nothing of an idle speaker, one in standby or one that already plays the same station', () => {
    expect(joinReview({ name: 'Kitchen', playing: false, stops: 'Jazz24', plays: 'FluxFM' })).toBe(null);
    expect(joinReview({ name: 'Kitchen', playing: true, stops: 'FluxFM', plays: ' fluxfm ' })).toBe(null);
    expect(joinReview({ name: '', playing: true, stops: 'Jazz24', plays: 'FluxFM' })).toBe(null);
    expect(joinReview()).toBe(null);
  });

  it('reads the name from a now_playing answer, station first, entities decoded', () => {
    expect(statusNowName('<nowPlaying source="INTERNET_RADIO"><ContentItem><itemName>Jazz &amp; Soul</itemName></ContentItem><playStatus>PLAY_STATE</playStatus></nowPlaying>'))
      .toBe('Jazz & Soul');
    expect(statusNowName('<nowPlaying><ContentItem><itemName>Song title</itemName></ContentItem><stationName>Jazz24</stationName></nowPlaying>'))
      .toBe('Jazz24');
    expect(statusNowName('<nowPlaying source="STANDBY"><ContentItem source="STANDBY" /></nowPlaying>')).toBe('');
    expect(statusNowName(null)).toBe('');
  });
});
