// #1394 on v1.0.21: on the phone remote, switching from a radio station to a
// music library track showed the track's title with the radio station's last
// song under it for a few seconds. The page asked the stream proxy for the
// song whenever the source was UPNP, and a library track is UPNP too; the
// proxy still held the station's song until its end-of-stream wipe. The page
// now asks only while the location is a proxied radio stream, the rule the
// desktop app's proxiedRadioPlaying has used all along.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { proxiedRadioPlaying } from './utils.js';

const here = dirname(fileURLToPath(import.meta.url));
const phone = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8').replace(/\r\n/g, '\n');

function phoneFn(name) {
  const start = phone.indexOf(`function ${name}(`);
  if (start < 0) throw new Error(`${name} not found on the phone page`);
  const end = phone.indexOf('\n}\n', start);
  return phone.slice(start, end + 2);
}

// eslint-disable-next-line no-new-func
const proxiedRadioLoc = new Function('atob', `${phoneFn('proxiedRadioLoc')}\nreturn proxiedRadioLoc;`)(
  (s) => Buffer.from(s, 'base64').toString('binary'));

const orion = (streamUrl) => '/station?data=' + Buffer.from(JSON.stringify({ name: 'Gold', streamUrl })).toString('base64');

const cases = [
  ['a library track from the media server', 'http://192.0.2.10:8200/MediaItems/42.flac', false],
  ['a preset recall through the proxy', 'http://192.0.2.1:8888/stream/3', true],
  ['an ad-hoc station through the proxy', 'http://192.0.2.1:8888/stream/raw?u=aHR0cDovL3g=', true],
  ['the ORION descriptor of a proxied preset', orion('http://192.0.2.1:8888/stream/1'), true],
  ['the ORION descriptor of a native station', orion('https://cdn.example/gold.mp3'), false],
  ['the Spotify engine stream', 'http://192.0.2.1:8888/spotify/stream-2.ogg', false],
  ['nothing', '', false],
];

describe('the phone page asks the stream proxy for the song only for proxied radio', () => {
  for (const [what, loc, want] of cases) {
    it(`${what}: ${want}`, () => {
      expect(proxiedRadioLoc(loc)).toBe(want);
      // Same answer as the desktop app, so both show the same line.
      expect(proxiedRadioPlaying(loc)).toBe(want);
    });
  }

  it('gates the proxy title on the location, not only on the UPNP source', () => {
    expect(phone).toContain("song = (playing && proxySrc && proxiedRadioLoc(nowLoc)) ? await currentSong() : '';");
  });
});
