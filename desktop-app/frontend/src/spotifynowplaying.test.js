// Switching from the Spotify key holding "Tom Sawyer" to the one holding the
// playlist "Purpose for Pain", the now-playing line read for about ten seconds
// `Playlist: "Purpose for Pain" · Rush - Tom Sawyer`: the new preset's name over
// the old song (discussion #1077).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import {
  RECALL_EXPECT_MS, acceptSpotifyReply, beginRecall, normalizeSpotifyContext,
  recallSettled, snapshotFromReply, spotifySlotStillHolds, spotifySongText,
  nativeSpotifySlot,
} from './spotifynowplaying.js';

const here = dirname(fileURLToPath(import.meta.url));
const main = readFileSync(join(here, 'main.js'), 'utf8').replace(/\r\n/g, '\n');

const OLD = { track: 'Tom Sawyer', artist: 'Rush', cover: 'http://c/old.jpg', context: 'spotify:playlist:OLD' };
const WANT = 'spotify:playlist:NEW';
const T0 = 1_000_000;

describe('which /spotify/info reply may paint the line after a preset click', () => {
  const recall = beginRecall(WANT, OLD, T0);
  const judge = (reply, extra = {}) => acceptSpotifyReply(reply, {
    sentGen: 2, currentGen: 2, recall, now: T0 + 1000, ...extra,
  });

  it('refuses the speaker still naming the old song next to the new playlist', () => {
    // What an agent without the server-side fix answers mid-recall.
    expect(judge({ ...OLD, context: WANT })).toBe(false);
  });

  it('refuses a reply about the previous playlist', () => {
    expect(judge({ track: 'Limelight', artist: 'Rush', context: 'spotify:playlist:OLD' })).toBe(false);
  });

  it('refuses a reply to a request sent before the click', () => {
    expect(judge({ track: 'Purpose', artist: 'B', context: WANT }, { sentGen: 1 })).toBe(false);
  });

  it('refuses a reply with no context while the switch is in flight', () => {
    expect(judge({ track: 'Something', artist: 'X', context: '' })).toBe(false);
  });

  it('accepts the new playlist, also through the station wrapper', () => {
    expect(judge({ track: 'Purpose', artist: 'B', context: WANT })).toBe(true);
    expect(judge({ track: 'Purpose', artist: 'B', context: 'spotify:station:playlist:NEW' })).toBe(true);
  });

  it('accepts the new playlist with no song yet: the line then shows the name alone', () => {
    const reply = { track: '', artist: '', context: WANT };
    expect(judge(reply)).toBe(true);
    expect(spotifySongText(snapshotFromReply(reply))).toBe('');
    expect(recallSettled(snapshotFromReply(reply))).toBe(false);
  });

  it('goes back to trusting the speaker once the click is old', () => {
    // An engine that never reports a context must not hide the song for good.
    expect(judge({ track: 'Purpose', artist: 'B', context: '' }, { now: T0 + RECALL_EXPECT_MS })).toBe(true);
  });

  it('accepts anything current when no click is pending', () => {
    expect(acceptSpotifyReply(OLD, { sentGen: 0, currentGen: 0, recall: null, now: T0 })).toBe(true);
    expect(acceptSpotifyReply(OLD, { sentGen: 0, currentGen: 1, recall: null, now: T0 })).toBe(false);
    expect(acceptSpotifyReply(null, { sentGen: 0, currentGen: 0, recall: null, now: T0 })).toBe(false);
  });
});

describe('the snapshot', () => {
  it('keeps one reply together and composes the song from it alone', () => {
    const s = snapshotFromReply({ track: 'Purpose', artist: 'B', cover: 'c', context: WANT, account: 'a' });
    expect(s).toEqual({ track: 'Purpose', artist: 'B', cover: 'c', context: WANT, account: 'a' });
    expect(spotifySongText(s)).toBe('B - Purpose');
    expect(spotifySongText({ track: 'Solo', artist: '' })).toBe('Solo');
    expect(spotifySongText(null)).toBe('');
    expect(recallSettled(s)).toBe(true);
  });

  it('normalizes the station wrapper like the agent does', () => {
    expect(normalizeSpotifyContext(' spotify:station:album:X ')).toBe('spotify:album:X');
    expect(normalizeSpotifyContext('spotify:station:')).toBe('spotify:station:');
    expect(normalizeSpotifyContext(undefined)).toBe('');
  });
});

describe('main.js wiring', () => {
  it('drops the old song and starts a new generation on a preset click', () => {
    const at = main.indexOf('async function play(slot)');
    const play = main.slice(at, main.indexOf('await PlaySlot(state.currentBox.host', at));
    expect(play).toContain('beginRecall(p.uri, state.spotifyNow, Date.now())');
    expect(play).toContain('state.spotifyNow = null;');
    expect(play).toContain('state.spotifyNowGen = (state.spotifyNowGen || 0) + 1;');
    expect(play).toContain('state.lastSpotifyNowFetch = 0;');
  });

  it('gates every reply and draws the line from the snapshot only', () => {
    expect(main).toContain('if (acceptSpotifyReply(np, {');
    expect(main).toContain('state.spotifyNow = snap;');
    expect(main).not.toMatch(/state\.nowSpotify(Track|Artist|Cover|Context|Account)\b/);
  });
});

// The Spotify app took over the "(STR)" speaker while key 5 was playing: the
// location fell back to the generic stream and key 5 kept "now playing" over a
// song picked in the app (discussion #1077, issue 14).
describe('spotifySlotStillHolds', () => {
  const KEY = 'spotify:track:SPIRIT';

  it('drops the key when the engine plays a context the key does not hold', () => {
    expect(spotifySlotStillHolds(KEY, { context: 'spotify:album:OTHER' })).toBe(false);
  });

  it('keeps the key through a next/prev inside its own context', () => {
    expect(spotifySlotStillHolds('spotify:playlist:P', { context: 'spotify:playlist:P' })).toBe(true);
  });

  it('keeps the key when the engine wraps the context in a station', () => {
    expect(spotifySlotStillHolds(KEY, { context: 'spotify:station:track:SPIRIT' })).toBe(true);
  });

  it('keeps the key when there is no context to judge by', () => {
    expect(spotifySlotStillHolds(KEY, null)).toBe(true);
    expect(spotifySlotStillHolds(KEY, { context: '' })).toBe(true);
    expect(spotifySlotStillHolds('', { context: 'spotify:album:OTHER' })).toBe(true);
  });

  it('is what renderPresets asks before keeping the remembered slot', () => {
    expect(main).toContain('!spotifySlotStillHolds(held.uri, state.spotifyNow)');
  });
});

describe('a key holding what the speaker\'s own Spotify client plays (#1077 issue 18)', () => {
  // The form the speaker reports for its native Spotify entry (the "(L+R)"
  // speaker in the Spotify app): standard base64 of the context URI, exactly
  // as in the reporter's bundle, where the hold had stored the same URI on key 1.
  const container = (uri) => `/playback/container/${btoa(uri)}`;
  const presets = [
    { slot: 1, type: 'spotify', name: 'Top 100 Worship', uri: 'spotify:playlist:SAVED1' },
    { slot: 2, type: 'stationurl', name: 'Jazz', stream_url: 'http://192.0.2.1/jazz' },
    { slot: 4, type: 'spotify', name: 'Spotify', uri: 'spotify:album:OTHER' },
  ];

  it('lights the key that holds the playing context right after the save', () => {
    expect(nativeSpotifySlot(presets, container('spotify:playlist:SAVED1'))).toBe(1);
  });

  it('decodes the URL-safe form STR itself writes as well', () => {
    const loc = '/playback/container/' + btoa('spotify:album:OTHER').replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    expect(nativeSpotifySlot(presets, loc)).toBe(4);
  });

  it('turns off as soon as another playlist plays', () => {
    expect(nativeSpotifySlot(presets, container('spotify:playlist:SOMETHINGELSE'))).toBe(null);
  });

  it('names no key for other sources, an empty location or STR\'s own stream', () => {
    expect(nativeSpotifySlot(presets, '')).toBe(null);
    expect(nativeSpotifySlot(presets, 'http://192.0.2.1/jazz')).toBe(null);
    expect(nativeSpotifySlot(presets, 'http://192.0.2.1:8888/spotify/stream.ogg')).toBe(null);
    expect(nativeSpotifySlot(presets, '/playback/container/not-base64!')).toBe(null);
  });

  it('never matches on the name, only on the stored URI', () => {
    const named = [{ slot: 3, type: 'spotify', name: 'Top 100 Worship', uri: '' }];
    expect(nativeSpotifySlot(named, container('spotify:playlist:SAVED1'))).toBe(null);
  });

  it('is wired into the preset tile highlight', () => {
    expect(main).toMatch(/const nativeSpotifyKey = spotifyPlaying \? null : nativeSpotifySlot\(state\.presets, state\.nowLocation\);/);
    expect(main).toMatch(/p\.type === 'spotify' && nativeSpotifyKey != null && p\.slot === nativeSpotifyKey/);
  });
});
