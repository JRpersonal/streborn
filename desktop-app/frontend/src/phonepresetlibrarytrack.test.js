// #1339 (issues 8 and 9) on v1.0.21: a preset key holding ONE music-library
// track went dark on the phone remote about 15 s after a tap while the track
// played on, and a start from the desktop app never lit it on the phone at
// all. The speaker plays that key's media-server URL as is over UPNP, so the
// location is neither a /stream/<n> proxy path nor an ORION descriptor, and the
// phone's playingSlot only matched those two. The tap's pending window was the
// only thing that kept the key lit. The desktop app has always matched the
// location against the key's stored URL (p.stream_url === nowLocation); the
// phone now does the same.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { decodeXmlEntities } from './utils.js';

const here = dirname(fileURLToPath(import.meta.url));
const phone = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8').replace(/\r\n/g, '\n');

function phoneFn(name) {
  const start = phone.indexOf(`function ${name}(`);
  if (start < 0) throw new Error(`${name} not found on the phone page`);
  const end = phone.indexOf('\n}\n', start);
  return phone.slice(start, end + 2);
}

const b64 = (s) => Buffer.from(s, 'binary').toString('base64');
// eslint-disable-next-line no-new-func
const playingSlot = new Function('atob', 'URL', `${phoneFn('playingSlot')}\nreturn playingSlot;`)(
  (s) => Buffer.from(s, 'base64').toString('binary'), URL);

// The reporter's keys, from the bundle's preset store (addresses replaced).
const track = 'http://192.0.2.47:9790/minimserver/*/Media.localized/Music/Artist/Album/03*20Purpose*20For*20Pain.m4a';
const other = 'http://192.0.2.47:9790/minimserver/*/Media.localized/Music/Artist/Other/04*20What*20I*20Deserve.m4a';
const radio = 'https://radio.example/rush/icecast.audio';
const list = [
  { slot: 1, type: 'spotify', stream_url: '' },
  { slot: 2, type: 'queue', stream_url: '' },
  { slot: 3, type: 'radio', stream_url: other },
  { slot: 4, type: 'radio', stream_url: track },
  { slot: 5, type: 'radio', stream_url: radio },
];
const orion = (streamUrl) => '/station?data=' + b64(JSON.stringify({ name: 'X', streamUrl }));

describe('the phone lights the key whose library track the speaker plays', () => {
  it('matches the UPNP location against the key\'s stored track URL', () => {
    expect(playingSlot(track, list)).toBe(4);
    expect(playingSlot(other, list)).toBe(3);
  });

  it('undoes the XML escapes the regex-read location still carries', () => {
    const q = 'http://192.0.2.47:9000/item?id=7&fmt=flac';
    const l = [{ slot: 6, type: 'radio', stream_url: q }];
    const raw = q.replace(/&/g, '&amp;');
    expect(playingSlot(raw, l)).toBe(6);
    // The same string the desktop app compares after its own decode.
    expect(decodeXmlEntities(raw)).toBe(q);
  });

  it('lights nothing for a track no key holds', () => {
    expect(playingSlot('http://192.0.2.47:9790/minimserver/*/elsewhere.m4a', list)).toBe(0);
  });

  it('keeps the proxy, Spotify and descriptor matches it had', () => {
    expect(playingSlot('http://192.0.2.1:8888/stream/2', list)).toBe(2);
    expect(playingSlot('http://192.0.2.1:8888/spotify/stream-1.ogg', list)).toBe(1);
    expect(playingSlot(orion('http://127.0.0.1:8888/stream/raw?u=' + b64(radio)), list)).toBe(5);
    expect(playingSlot(orion('http://127.0.0.1:8888/stream/3'), list)).toBe(3);
    expect(playingSlot('', list)).toBe(0);
  });
});
