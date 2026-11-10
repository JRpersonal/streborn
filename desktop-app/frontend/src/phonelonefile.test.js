// #1839: on the phone page a single library track showed its title and then
// just "Playing", while the desktop app showed artist and album for the same
// play. The speaker reports neither for a file it fetches itself, and the app
// only knew them from its own Library row. The agent now keeps what the start
// named and answers it in GET /api/queue ("file"); the page prints it under the
// title while the speaker's location is that file.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const phone = readFileSync(join(here, '..', '..', '..', 'internal', 'webui', 'assets', 'index.html'), 'utf8').replace(/\r\n/g, '\n');

function phoneFn(name) {
  const start = phone.indexOf(`function ${name}(`);
  if (start < 0) throw new Error(`${name} not found on the phone page`);
  const end = phone.indexOf('\n}\n', start);
  return phone.slice(start, end + 2);
}

// eslint-disable-next-line no-new-func
const loneFileMeta = new Function(`${phoneFn('loneFileMeta')}\nreturn loneFileMeta;`)();

// currentQueue with the page's api() replaced; returns what it resolved to and
// the lone file it left behind.
async function readQueue(answer) {
  // eslint-disable-next-line no-new-func
  const run = new Function('api', `var loneFile = 'unset';\nasync ${phoneFn('currentQueue')}\n` +
    'return currentQueue().then(function (q) { return { queue: q, loneFile: loneFile }; });');
  return run(async () => answer);
}

const url = 'http://192.0.2.10:8200/MediaItems/42.flac';
const file = { url, title: 'Amanda', artist: 'Boston', album: 'Third Stage' };

describe('the phone page names a single library track', () => {
  it('as "Artist · Album", the shape of a folder song', () => {
    expect(loneFileMeta(file, url)).toBe('Boston · Third Stage');
  });

  it('with what is known when one of them is missing', () => {
    expect(loneFileMeta({ url, artist: 'Boston' }, url)).toBe('Boston');
    expect(loneFileMeta({ url, album: 'Third Stage' }, url)).toBe('Third Stage');
    expect(loneFileMeta({ url, title: 'Amanda' }, url)).toBe('');
  });

  it('only while the speaker plays that file', () => {
    expect(loneFileMeta(file, 'http://192.0.2.10:8200/MediaItems/43.flac')).toBe('');
    expect(loneFileMeta(file, 'http://192.0.2.1:8888/stream/3')).toBe('');
    expect(loneFileMeta(file, '')).toBe('');
    expect(loneFileMeta(null, url)).toBe('');
    expect(loneFileMeta({ artist: 'Boston' }, url)).toBe('');
  });

  it('matches a location that still carries its XML escapes', () => {
    const q = 'http://192.0.2.10:8200/get?id=42&ext=.flac';
    expect(loneFileMeta({ url: q, artist: 'Boston' }, 'http://192.0.2.10:8200/get?id=42&amp;ext=.flac')).toBe('Boston');
  });

  it('ignores anything that is not text', () => {
    expect(loneFileMeta({ url, artist: { a: 1 }, album: 7 }, url)).toBe('');
  });
});

describe('the queue read hands over the lone file', () => {
  it('when no queue runs, and still answers "no queue"', async () => {
    const r = await readQueue({ active: false, pos: -1, items: [], file });
    expect(r.queue).toBe(null);
    expect(r.loneFile).toEqual(file);
  });

  it('never beside a running queue', async () => {
    const q = { active: true, pos: 0, items: [{ title: 'a' }], file };
    const r = await readQueue(q);
    expect(r.queue).toBe(q);
    expect(r.loneFile).toBe(null);
  });

  it('nothing from an older agent or an unreachable speaker', async () => {
    expect((await readQueue({ active: false, items: [] })).loneFile).toBe(null);
    expect((await readQueue(null)).loneFile).toBe(null);
  });
});

describe('the status line', () => {
  it('prints the lone file only for a UPnP play that is neither a queue nor proxied radio', () => {
    expect(phone).toContain('if (!song && playing && upnpPlay && !queue && !proxiedRadioLoc(nowLoc)) song = loneFileMeta(loneFile, nowLoc);');
    expect(phone).toContain("const upnpPlay = upSrc === 'UPNP' && !/\\/spotify\\/stream/.test(nowLoc);");
  });

  it('comes before the stream proxy is asked, which stays radio only', () => {
    const lone = phone.indexOf('song = loneFileMeta(loneFile, nowLoc);');
    const proxy = phone.indexOf("song = (playing && proxySrc && proxiedRadioLoc(nowLoc)) ? await currentSong() : '';");
    expect(lone).toBeGreaterThan(0);
    expect(proxy).toBeGreaterThan(lone);
  });

  it('both library plays of the page send artist and album', () => {
    for (const name of ['playLibTrack', 'playBrowseTrack']) {
      const fn = phoneFn(name);
      expect(fn, name).toContain("artist: it.artist || ''");
      expect(fn, name).toContain("album: it.album || ''");
    }
  });
});
