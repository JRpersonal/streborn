// One "Playing on <speaker>" line per speaker (a mail report, 2026-11: while
// a speaker switched stations, the line stood under the old and the new
// station at once, and one station could be listed twice).
import { describe, it, expect, beforeEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { claimPlay, onPlayClaim, ownsPlay, playLine, playLineOutdated, playLinesToDrop, resetPlayClaims } from './playclaim.js';
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
