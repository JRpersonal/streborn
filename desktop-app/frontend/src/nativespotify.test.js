import { describe, it, expect } from 'vitest';

// shorty310, discussion #950, 2026-09-14. Three ST10s, firmware 27.0.6, the
// Bose cloud gone, a FREE Spotify account, and a playlist started from the
// phone with the plain speaker picked in Connect. I told them twice that the
// song could not be shown for that session. Their own capture says otherwise:
// the speaker names the track, the artist, the album and the cover, and the app
// was reading only <itemName> out of it.
//
// Re-implemented here rather than imported, as errornoise.test.js does: main.js
// starts the whole application at module load. The SHAPE of the decision is
// what matters.

const RE = {
  source: /source="([^"]+)"/,
  track: /<track>([^<]*)<\/track>/,
  artist: /<artist>([^<]*)<\/artist>/,
  art: /<art\b[^>]*>([^<]*)<\/art>/,
  itemName: /<itemName>([^<]*)<\/itemName>/,
};

// Only the speaker's own Spotify receiver. On radio <track> repeats the station
// name, so a general rule would print the station where the song belongs.
function boxSpotify(xml) {
  const src = (xml.match(RE.source) || [])[1] || '';
  if (src !== 'SPOTIFY') return null;
  const track = (xml.match(RE.track) || [])[1] || '';
  if (!track) return null;
  return {
    track,
    artist: (xml.match(RE.artist) || [])[1] || '',
    cover: (xml.match(RE.art) || [])[1] || '',
  };
}

// The line the user reads, with the playlist kept as the heading.
function displayLine(xml) {
  const name = (xml.match(RE.itemName) || [])[1] || '';
  const np = boxSpotify(xml);
  if (!np) return name;
  const song = np.artist ? `${np.artist} - ${np.track}` : np.track;
  return name ? `Playlist: "${name}" · ${song}` : song;
}

const NATIVE_SPOTIFY =
  '<?xml version="1.0" encoding="UTF-8" ?><nowPlaying deviceID="DEV#c91ce962" source="SPOTIFY" ' +
  'sourceAccount="SpotifyConnectUserName"><ContentItem source="SPOTIFY" type="DO_NOT_RESUME" ' +
  'location="/playback/container/c3BvdGlmeTpwbGF5bGlzdDo1MHU4OFZqOXYwZ1RnbjM4ZGQ4WUNL" ' +
  'sourceAccount="SpotifyConnectUserName" isPresetable="false"><itemName>Best of Matthew west</itemName>' +
  '</ContentItem><track>You Changed My Name</track><artist>Matthew West</artist>' +
  '<album>My Story Your Glory</album><stationName></stationName>' +
  '<art artImageStatus="IMAGE_PRESENT">https://i.scdn.co/image/ab67616d0000b273a8b09534c0a1d25edbcfd094</art>' +
  '<time total="236">25</time><playStatus>PLAY_STATE</playStatus></nowPlaying>';

const NATIVE_RADIO =
  '<nowPlaying source="LOCAL_INTERNET_RADIO"><ContentItem source="LOCAL_INTERNET_RADIO" ' +
  'location="http://192.0.2.10:8888/stream/3"><itemName>1LIVE</itemName></ContentItem>' +
  '<track>1LIVE</track><artist></artist><playStatus>PLAY_STATE</playStatus></nowPlaying>';

describe('a Spotify session the phone opened on the speaker itself', () => {
  it('takes the song out of the status response it already polls', () => {
    expect(boxSpotify(NATIVE_SPOTIFY)).toEqual({
      track: 'You Changed My Name',
      artist: 'Matthew West',
      cover: 'https://i.scdn.co/image/ab67616d0000b273a8b09534c0a1d25edbcfd094',
    });
  });

  it('keeps the playlist as the heading and puts the song beside it', () => {
    expect(displayLine(NATIVE_SPOTIFY))
      .toBe('Playlist: "Best of Matthew west" · Matthew West - You Changed My Name');
  });

  it('does not ask STR\'s own engine about it', () => {
    // The container location matches the engine-fetch predicate, which is how
    // this session ended up asking an engine that is not playing it and getting
    // a blank line back.
    const loc = (NATIVE_SPOTIFY.match(/location="([^"]*)"/) || [])[1];
    expect(/\/spotify\/stream|\/playback\/container/.test(loc)).toBe(true);
    const shouldAskEngine = /\/spotify\/stream|\/playback\/container/.test(loc) && !boxSpotify(NATIVE_SPOTIFY);
    expect(shouldAskEngine).toBe(false);
  });
});

describe('what this rule must not break', () => {
  it('leaves radio alone, where <track> is the station name', () => {
    expect(boxSpotify(NATIVE_RADIO)).toBeNull();
    expect(displayLine(NATIVE_RADIO)).toBe('1LIVE');
  });

  it('says nothing when the speaker has nothing to say', () => {
    for (const xml of [
      '<nowPlaying source="STANDBY"></nowPlaying>',
      '<nowPlaying source="SPOTIFY"><track></track></nowPlaying>',
      '<nowPlaying source="AUX"><track>line in</track></nowPlaying>',
      '',
    ]) {
      expect(boxSpotify(xml)).toBeNull();
    }
  });

  it('still shows the track when the speaker reports no artist', () => {
    const xml = '<nowPlaying source="SPOTIFY"><itemName>Mix</itemName><track>Some Song</track></nowPlaying>';
    expect(displayLine(xml)).toBe('Playlist: "Mix" · Some Song');
  });
});
