// The short notice that Spotify refuses the speaker's account on (STR) (#1671).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { refusalFromReply, refusalNoticeFor, refusalText, strSpotifyLocation } from './spotifyrefused.js';
import { accountRow } from './views/spotifyui2model.js';

const t = (key, p) => (p ? `${key}|${JSON.stringify(p)}` : key);
const BOX = { host: '192.0.2.10', port: 8888 };
const STR_LOC = 'http://192.0.2.10:8888/spotify/stream.ogg';

describe('refusalFromReply', () => {
  it('says nothing for a speaker Spotify does not refuse', () => {
    expect(refusalFromReply(null)).toBe(null);
    expect(refusalFromReply({ accountRefused: false, audioKeyRefused: false })).toBe(null);
  });

  it('takes the sticky flag and the account name', () => {
    expect(refusalFromReply({ accountRefused: true, accountRefusedName: ' Alex ' })).toEqual({ name: 'Alex' });
    expect(refusalFromReply({ accountRefused: true })).toEqual({ name: '' });
  });

  it('falls back to the short-lived flag of an older agent, without a name', () => {
    expect(refusalFromReply({ audioKeyRefused: true })).toEqual({ name: '' });
  });
});

describe('refusalText', () => {
  it('names the account when the speaker knows it', () => {
    expect(refusalText({ name: 'Alex' }, t)).toBe('spotify.accountRefusedNamed|{"account":"Alex"}');
    expect(refusalText({ name: '' }, t)).toBe('spotify.accountRefused');
    expect(refusalText(null, t)).toBe('');
  });
});

describe('refusalNoticeFor', () => {
  const base = { currentBox: BOX, nowLocation: STR_LOC, nowBoxSpotify: null, spotifyRefusal: { ...BOX, name: '' } };

  it('shows while the selected speaker plays from STR Spotify', () => {
    expect(refusalNoticeFor(base, t)).toBe('spotify.accountRefused');
  });

  it('stays away from radio and the speaker\'s own Spotify entry', () => {
    expect(refusalNoticeFor({ ...base, nowLocation: 'http://192.0.2.10:8888/stream/2' }, t)).toBe('');
    expect(refusalNoticeFor({ ...base, nowBoxSpotify: { track: 'x' } }, t)).toBe('');
    expect(strSpotifyLocation('/playback/container/abc', null)).toBe(true);
  });

  it('never carries one speaker\'s answer over to another', () => {
    expect(refusalNoticeFor({ ...base, currentBox: { host: '192.0.2.11', port: 8888 } }, t)).toBe('');
    expect(refusalNoticeFor({ ...base, spotifyRefusal: null }, t)).toBe('');
  });
});

describe('UI 2.0 accounts row', () => {
  it('marks the speaker whose logged-in account Spotify refuses', () => {
    const r = accountRow({ host: BOX.host, port: BOX.port, name: 'Kitchen' }, { account: 'id1', accountRefused: true, canRecall: true }, {});
    expect(r.refused).toBe(true);
    const ok = accountRow({ host: BOX.host, port: BOX.port, name: 'Kitchen' }, { account: 'id1', canRecall: true }, {});
    expect(ok.refused).toBe(false);
  });
});

describe('status poll', () => {
  const src = readFileSync(new URL('./views/music/status.js', import.meta.url), 'utf8');
  it('feeds the notice instead of a one-shot toast', () => {
    expect(src).toContain('refusalFromReply(np)');
    expect(src).not.toContain("t('spotify.audioKeyRefused')");
  });
});
