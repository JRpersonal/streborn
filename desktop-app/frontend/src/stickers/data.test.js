// What goes on a sticker, read from a speaker's presets, and what this
// computer remembers about the stickers.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { collectSpeakers } from './collect.js';
import {
  forgetReadings,
  finishHint,
  hintState,
  housingOf,
  keysFingerprint,
  logoRequest,
  noteSpeakerKeys,
  offerHint,
  presetKind,
  printedFingerprints,
  rememberPrinted,
  setHousing,
  speakerId,
  stickerKeys,
  stickersStale,
} from './data.js';

function fakeStorage() {
  const m = new Map();
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k) };
}
const broken = { getItem() { throw new Error('blocked'); }, setItem() { throw new Error('blocked'); } };

const KITCHEN = { host: '192.0.2.1', port: 8888, deviceID: 'AABBCCDDEEFF', friendlyName: 'Kitchen', model: 'SoundTouch 20', kind: 'STR', presetRows: '2' };
const OFFICE = { host: '192.0.2.2', port: 8888, deviceID: '112233445566', friendlyName: 'Office', model: 'SoundTouch 10', kind: 'STR' };
const PRESETS = [
  { slot: 1, name: 'Jazz FM', type: 'radio', stream_url: 'https://stream.example.com/jazz', homepage: 'https://jazz.example.com/', art: 'https://jazz.example.com/logo.png' },
  { slot: 3, name: 'Road Trip', type: 'spotify', uri: 'spotify:playlist:abc', art: 'https://covers.example.com/road.jpg' },
  { slot: 4, name: 'Album', type: 'queue', items: [{ art: 'http://192.0.2.9:9790/cover.jpg' }] },
  { slot: 7, name: 'News', type: 'radio', stream_url: 'https://stream.example.com/news' },
  { slot: 9, name: 'Evening', type: 'spotify', uri: 'spotify:playlist:def' },
];

describe('stickerKeys', () => {
  it('puts slots 1 to 6 on the keys and 7 to 12 on the same keys pressed twice', () => {
    const keys = stickerKeys(KITCHEN, PRESETS);
    expect(keys.map((k) => k.n)).toEqual([1, 2, 3, 4, 5, 6]);
    expect(keys.map((k) => k.name)).toEqual(['Jazz FM', '', 'Road Trip', 'Album', '', '']);
    expect(keys.map((k) => k.kind)).toEqual(['station', 'station', 'playlist', 'playlist', 'station', 'station']);
    expect(keys[0].twice).toEqual({ name: 'News', kind: 'station' });
    expect(keys[2].twice).toEqual({ name: 'Evening', kind: 'playlist' });
    expect(keys[1].twice).toBeNull();
  });

  it('leaves the second row off a speaker that does not keep one', () => {
    const oneRow = PRESETS.filter((p) => p.slot <= 6);
    expect(stickerKeys(OFFICE, oneRow).every((k) => k.twice === null)).toBe(true);
    // A store that already holds a row-2 key proves the speaker keeps them.
    expect(stickerKeys(OFFICE, PRESETS)[0].twice).toEqual({ name: 'News', kind: 'station' });
  });

  it('gives six empty keys for a speaker with nothing saved', () => {
    for (const presets of [[], null, undefined]) {
      expect(stickerKeys(KITCHEN, presets).map((k) => [k.n, k.name, k.twice])).toEqual([1, 2, 3, 4, 5, 6].map((n) => [n, '', null]));
    }
  });

  it('ignores a slot without a name', () => {
    expect(stickerKeys(KITCHEN, [{ slot: 2, name: '' }])[1].name).toBe('');
  });

  it('calls a Spotify key and a library album a playlist', () => {
    expect(presetKind({ type: 'spotify' })).toBe('playlist');
    expect(presetKind({ type: 'queue' })).toBe('playlist');
    expect(presetKind({ type: 'radio' })).toBe('station');
    expect(presetKind({})).toBe('station');
  });
});

describe('logoRequest', () => {
  it('hands a station to the logo lookup with its own art and its hosts', () => {
    const r = logoRequest(PRESETS[0]);
    expect(r.direct).toBe('https://jazz.example.com/logo.png');
    expect(r.lookup.fav).toBe('https://jazz.example.com/logo.png');
    expect(r.lookup.brand).toBe('jazz.example.com');
    expect(r.lookup.hosts).toContain('jazz.example.com');
  });

  it('takes only the stored cover for a Spotify key', () => {
    expect(logoRequest(PRESETS[1])).toEqual({ inline: '', direct: 'https://covers.example.com/road.jpg', lookup: null });
  });

  it('takes the first track cover for a library album', () => {
    expect(logoRequest(PRESETS[2]).lookup.fav).toBe('http://192.0.2.9:9790/cover.jpg');
  });

  it('unwraps art that went through the speaker art proxy and drops speaker-local art', () => {
    const real = 'https://jazz.example.com/logo.png';
    const wrapped = 'http://127.0.0.1:8888/art?u=' + btoa(real).replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_');
    expect(logoRequest({ type: 'radio', art: wrapped }).direct).toBe(real);
    expect(logoRequest({ type: 'radio', art: 'http://127.0.0.1:8888/icon.png' }).direct).toBe('');
  });

  it('skips an icon service entry of an older chain and uses a data URI as it is', () => {
    const r = logoRequest({ type: 'radio', art: 'https://icons.duckduckgo.com/ip3/example.com.ico|https://example.com/l.png' });
    expect(r.direct).toBe('https://example.com/l.png');
    expect(logoRequest({ type: 'radio', art: 'data:image/png;base64,AAAA' }).inline).toBe('data:image/png;base64,AAAA');
  });

  it('answers for a missing preset', () => {
    expect(logoRequest(null)).toEqual({ inline: '', direct: '', lookup: null });
  });
});

describe('fingerprint and the out-of-date marker', () => {
  beforeEach(() => forgetReadings());

  it('is equal for the same assignment and differs for a renamed, moved or new key', () => {
    const base = keysFingerprint(stickerKeys(KITCHEN, PRESETS));
    expect(keysFingerprint(stickerKeys(KITCHEN, PRESETS.slice().reverse()))).toBe(base);
    const renamed = PRESETS.map((p) => (p.slot === 1 ? { ...p, name: 'Jazz 24' } : p));
    const moved = PRESETS.map((p) => (p.slot === 1 ? { ...p, slot: 2 } : p));
    const twice = [...PRESETS, { slot: 8, name: 'Talk', type: 'radio' }];
    for (const other of [renamed, moved, twice]) expect(keysFingerprint(stickerKeys(KITCHEN, other))).not.toBe(base);
  });

  it('does not depend on the logo', () => {
    const keys = stickerKeys(KITCHEN, PRESETS);
    const withLogo = keys.map((k) => ({ ...k, logo: 'data:image/png;base64,AAAA' }));
    expect(keysFingerprint(withLogo)).toBe(keysFingerprint(keys));
  });

  it('marks nothing before the first sheet was saved', () => {
    const storage = fakeStorage();
    noteSpeakerKeys(KITCHEN, PRESETS, storage);
    expect(stickersStale()).toBe(false);
  });

  it('marks a speaker whose keys changed after printing, until it is printed again', () => {
    const storage = fakeStorage();
    rememberPrinted({ [speakerId(KITCHEN)]: keysFingerprint(stickerKeys(KITCHEN, PRESETS)) }, storage);
    noteSpeakerKeys(KITCHEN, PRESETS, storage);
    expect(stickersStale()).toBe(false);

    const changed = PRESETS.map((p) => (p.slot === 1 ? { ...p, name: 'Jazz 24' } : p));
    noteSpeakerKeys(KITCHEN, changed, storage);
    expect(stickersStale()).toBe(true);
    // Looking at another speaker that is unchanged does not clear it.
    noteSpeakerKeys(OFFICE, [], storage);
    expect(stickersStale()).toBe(true);

    rememberPrinted({ [speakerId(KITCHEN)]: keysFingerprint(stickerKeys(KITCHEN, changed)) }, storage);
    noteSpeakerKeys(KITCHEN, changed, storage);
    expect(stickersStale()).toBe(false);
  });

  it('clears the mark when the key goes back to what was printed', () => {
    const storage = fakeStorage();
    rememberPrinted({ [speakerId(KITCHEN)]: keysFingerprint(stickerKeys(KITCHEN, PRESETS)) }, storage);
    noteSpeakerKeys(KITCHEN, [], storage);
    expect(stickersStale()).toBe(true);
    noteSpeakerKeys(KITCHEN, PRESETS, storage);
    expect(stickersStale()).toBe(false);
  });

  it('keeps what was remembered for speakers that were not on the new sheet', () => {
    const storage = fakeStorage();
    rememberPrinted({ a: '1', b: '2' }, storage);
    rememberPrinted({ b: '3' }, storage);
    expect(printedFingerprints(storage)).toEqual({ a: '1', b: '3' });
  });

  it('survives storage that throws or holds rubbish', () => {
    expect(() => noteSpeakerKeys(KITCHEN, PRESETS, broken)).not.toThrow();
    expect(printedFingerprints(broken)).toEqual({});
    const storage = fakeStorage();
    storage.setItem('str.stickerPrinted', '[1,2');
    expect(printedFingerprints(storage)).toEqual({});
  });
});

describe('housing colour', () => {
  it('is dark until chosen and remembered per speaker', () => {
    const storage = fakeStorage();
    expect(housingOf('a', storage)).toBe('dark');
    setHousing('a', 'white', storage);
    expect(housingOf('a', storage)).toBe('white');
    expect(housingOf('b', storage)).toBe('dark');
    setHousing('a', 'dark', storage);
    expect(housingOf('a', storage)).toBe('dark');
    expect(housingOf('a', broken)).toBe('dark');
  });
});

describe('the one-time hint', () => {
  it('shows from the first saved preset until it is closed, then never again', () => {
    const storage = fakeStorage();
    expect(hintState(storage)).toBe('');
    offerHint(storage);
    expect(hintState(storage)).toBe('show');
    offerHint(storage);
    expect(hintState(storage)).toBe('show');
    finishHint(storage);
    expect(hintState(storage)).toBe('done');
    offerHint(storage);
    expect(hintState(storage)).toBe('done');
  });

  it('is not offered to someone who already used the stickers', () => {
    const storage = fakeStorage();
    finishHint(storage);
    offerHint(storage);
    expect(hintState(storage)).toBe('done');
  });

  it('stays away where nothing can be remembered', () => {
    offerHint(broken);
    expect(hintState(broken)).toBe('done');
  });

  describe('is offered by a save, whichever way the key was saved', () => {
    beforeEach(() => forgetReadings());
    const added = [...PRESETS, { slot: 2, name: 'Talk', type: 'radio' }];

    it('not by the first reading of a speaker, nor by reading another speaker', () => {
      const storage = fakeStorage();
      noteSpeakerKeys(KITCHEN, PRESETS, storage);
      noteSpeakerKeys(OFFICE, [], storage);
      noteSpeakerKeys(KITCHEN, PRESETS, storage);
      expect(hintState(storage)).toBe('');
    });

    it('by a key that was filled or renamed since the last reading', () => {
      const storage = fakeStorage();
      noteSpeakerKeys(KITCHEN, PRESETS, storage);
      noteSpeakerKeys(KITCHEN, added, storage);
      expect(hintState(storage)).toBe('show');

      forgetReadings();
      const other = fakeStorage();
      noteSpeakerKeys(KITCHEN, PRESETS, other);
      noteSpeakerKeys(KITCHEN, PRESETS.map((p) => (p.slot === 1 ? { ...p, name: 'Jazz 24' } : p)), other);
      expect(hintState(other)).toBe('show');
    });

    it('not by a key that was only cleared', () => {
      const storage = fakeStorage();
      noteSpeakerKeys(KITCHEN, added, storage);
      noteSpeakerKeys(KITCHEN, PRESETS, storage);
      expect(hintState(storage)).toBe('');
    });

    it('not again once it was closed', () => {
      const storage = fakeStorage();
      finishHint(storage);
      noteSpeakerKeys(KITCHEN, PRESETS, storage);
      noteSpeakerKeys(KITCHEN, added, storage);
      expect(hintState(storage)).toBe('done');
    });
  });
});

describe('collectSpeakers', () => {
  const QR = 'data:image/png;base64,QRQR';
  const LOGO = 'data:image/jpeg;base64,LOGO';
  const api = (over = {}) => ({
    GetPresets: vi.fn(async () => PRESETS),
    PhoneAddressFor: vi.fn(async (host, port) => ({ url: `http://kitchen.example.net:${port}/`, addressUrl: `http://${host}:${port}/` })),
    PhoneQR: vi.fn(async () => QR),
    ResolveStationLogo: vi.fn(async (fav) => fav),
    StickerImage: vi.fn(async () => LOGO),
    ...over,
  });

  it('reads the keys, the code for the proven address and the logos of a speaker', async () => {
    const a = api();
    const storage = fakeStorage();
    setHousing(speakerId(KITCHEN), 'white', storage);
    const { speakers, skipped } = await collectSpeakers([KITCHEN], a, storage);
    expect(skipped).toEqual([]);
    expect(speakers).toHaveLength(1);
    const sp = speakers[0];
    expect(sp).toMatchObject({ id: 'AABBCCDDEEFF', name: 'Kitchen', model: 'SoundTouch 20', housing: 'white', qr: QR });
    // The code carries the name the backend proved, not the bare address.
    expect(a.PhoneQR).toHaveBeenCalledWith('http://kitchen.example.net:8888/');
    expect(sp.keys.map((k) => k.logo)).toEqual([LOGO, null, LOGO, LOGO, null, null]);
    expect(sp.keys.every((k) => !('preset' in k))).toBe(true);
    // The Spotify cover goes straight to the fetch, a station through the lookup first.
    expect(a.StickerImage).toHaveBeenCalledWith('https://covers.example.com/road.jpg');
    expect(a.ResolveStationLogo).toHaveBeenCalledWith('https://jazz.example.com/logo.png', 'jazz.example.com', expect.any(Array));
  });

  it('takes a cover the lookup already returned as a data URI without fetching it', async () => {
    const a = api({ ResolveStationLogo: vi.fn(async () => 'data:image/jpeg;base64,LAN') });
    const { speakers } = await collectSpeakers([{ ...KITCHEN }], a, fakeStorage());
    expect(speakers[0].keys[3].logo).toBe('data:image/jpeg;base64,LAN');
  });

  it('walks the lookup chain and falls back to the letter tile', async () => {
    const a = api({
      ResolveStationLogo: vi.fn(async () => 'https://a.example.com/x.png|https://b.example.com/y.png'),
      StickerImage: vi.fn(async (u) => (u === 'https://b.example.com/y.png' ? LOGO : '')),
    });
    const { speakers } = await collectSpeakers([KITCHEN], a, fakeStorage());
    expect(speakers[0].keys[0].logo).toBe(LOGO);
    // The Spotify cover could not be fetched: no picture, the tile shows the symbol.
    expect(speakers[0].keys[2].logo).toBeNull();
  });

  it('keeps the sheet when a logo call throws', async () => {
    const a = api({
      ResolveStationLogo: vi.fn(async () => { throw new Error('down'); }),
      StickerImage: vi.fn(async () => { throw new Error('down'); }),
    });
    const { speakers } = await collectSpeakers([KITCHEN], a, fakeStorage());
    expect(speakers[0].keys.every((k) => k.logo === null)).toBe(true);
  });

  it('refuses anything from the fetch that is not an image data URI', async () => {
    const a = api({ StickerImage: vi.fn(async () => 'https://example.com/x.png') });
    const { speakers } = await collectSpeakers([KITCHEN], a, fakeStorage());
    expect(speakers[0].keys[0].logo).toBeNull();
  });

  it('skips a speaker that does not answer and names it, without failing the rest', async () => {
    const a = api({ GetPresets: vi.fn(async (host) => { if (host === OFFICE.host) throw new Error('timeout'); return PRESETS; }) });
    const { speakers, skipped } = await collectSpeakers([KITCHEN, OFFICE], a, fakeStorage());
    expect(speakers.map((s) => s.name)).toEqual(['Kitchen']);
    expect(skipped).toEqual(['Office']);
  });

  it('skips a speaker without a code, an offline one unasked, and never a Bose original', async () => {
    const a = api({ PhoneQR: vi.fn(async (url) => { if (url.includes(':17008')) throw new Error('no'); return QR; }) });
    const boxes = [
      KITCHEN,
      { ...OFFICE, port: 17008 },
      { host: '192.0.2.3', friendlyName: 'Den', kind: 'STR', offline: true },
      { host: '192.0.2.4', friendlyName: 'Stock', kind: 'stock' },
    ];
    const { speakers, skipped } = await collectSpeakers(boxes, a, fakeStorage());
    expect(speakers.map((s) => s.name)).toEqual(['Kitchen']);
    expect(skipped).toEqual(['Office', 'Den']);
    expect(a.GetPresets).not.toHaveBeenCalledWith('192.0.2.3', expect.anything());
    expect(a.GetPresets).not.toHaveBeenCalledWith('192.0.2.4', expect.anything());
  });
});
