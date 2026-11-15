// The pure half of the key assignment editor on a speaker's Keys tab
// (keyeditormodel.js): which keys it lists and which one it chooses first,
// what a refusal of the agent says, how "Put what's playing" keeps each
// source, and what the Spotify and Library tabs offer. The browser walk is the
// e2e spec desktop-ui2-assignment-editor.spec.mjs.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  EDITOR_SOURCES, MAX_FOLDER_ITEMS, SOURCE_LABEL_KEYS, defaultSlot, editorKeys, folderPayload, keyHintText,
  nowPlayingWhat, radioStations, refusalCode, saveOutcome, savePlan, spotifyRecentItems,
} from './keyeditormodel.js';
import { setLocale, t } from '../../i18n/index.js';
import en from '../../i18n/bundles/en.json';

setLocale('en');

const BOX = { host: '192.0.2.1', port: 8888, kind: 'str' };
const ROW2 = { ...BOX, presetRows: '2' };
const PRESETS = [
  { slot: 1, name: 'BBC Radio 1' },
  { slot: 2, name: 'Jazz24' },
  { slot: 4, name: 'FIP' },
];

describe('editorKeys', () => {
  it('lists the six keys of one row, the empty ones too', () => {
    const keys = editorKeys(BOX, PRESETS, t);
    expect(keys.map((k) => k.slot)).toEqual([1, 2, 3, 4, 5, 6]);
    expect(keys[0]).toMatchObject({ label: 'Key 1', name: 'BBC Radio 1', empty: false, row: 1 });
    expect(keys[2]).toMatchObject({ label: 'Key 3', name: '', empty: true });
  });

  it('adds the keys pressed twice on a speaker that keeps a second row', () => {
    const keys = editorKeys(ROW2, PRESETS, t);
    expect(keys).toHaveLength(12);
    expect(keys[6]).toMatchObject({ slot: 7, row: 2, label: 'Key 1 · 2×', empty: true });
    // A store that already holds a second-row key proves the row.
    expect(editorKeys(BOX, [...PRESETS, { slot: 9, name: 'KEXP' }], t)).toHaveLength(12);
  });

  it('keeps one row for a Bose original and without a store', () => {
    expect(editorKeys({ ...ROW2, kind: 'stock' }, PRESETS, t)).toHaveLength(6);
    expect(editorKeys(BOX, null, t).every((k) => k.empty)).toBe(true);
  });
});

describe('defaultSlot', () => {
  it('chooses the first empty key of the first row, so nothing is replaced by accident', () => {
    expect(defaultSlot(editorKeys(ROW2, PRESETS, t))).toBe(3);
  });

  it('chooses key 1 when every key holds something', () => {
    const full = [1, 2, 3, 4, 5, 6].map((slot) => ({ slot, name: `S${slot}` }));
    expect(defaultSlot(editorKeys(BOX, full, t))).toBe(1);
  });

  it('keeps the key the user chose while it exists', () => {
    expect(defaultSlot(editorKeys(BOX, PRESETS, t), 4)).toBe(4);
    expect(defaultSlot(editorKeys(BOX, PRESETS, t), 9)).toBe(3);
  });
});

describe('keyHintText', () => {
  const keys = editorKeys(BOX, PRESETS, t);
  it('says an empty key is empty and names what a save replaces', () => {
    expect(keyHintText(keys, 3, t)).toBe('Key 3 is empty.');
    expect(keyHintText(keys, 2, t)).toBe('Key 2 holds Jazz24. What you put on it replaces that.');
  });
});

describe('saveOutcome', () => {
  const conflict = 'status 409: {"code":"already-on-slot","slot":2,"name":"Jazz \\"Live\\""}';

  it('turns the 409 already-on-slot into a note with the way to move it here', () => {
    const out = saveOutcome(new Error(conflict), 5, t, 'Kitchen');
    expect(out).toEqual({
      tone: 'warn',
      text: '"Jazz "Live"" is already on key 2 of Kitchen, and it can be on one key only.',
      move: { from: 2, to: 5, name: 'Jazz "Live"' },
    });
  });

  it('offers no move when it already sits on the chosen key, and copes without a name', () => {
    expect(saveOutcome('status 409: {"code":"already-on-slot","slot":5}', 5, t, 'Kitchen'))
      .toEqual({ tone: 'warn', text: 'This is already on key 5 of Kitchen, and it can be on one key only.', move: null });
  });

  it('says the agent\'s 422 answers in plain words, naming the speaker', () => {
    expect(saveOutcome('status 422: {"code":"nothing-playing","error":"x"}', 1, t, 'Kitchen').text)
      .toBe('Kitchen plays nothing right now, so there is nothing to put on a key.');
    expect(saveOutcome('status 422: {"code":"nothing-saveable"}', 1, t, 'Kitchen').text).toContain('cannot be kept on a key');
    expect(saveOutcome('status 422: {"code":"source-preset-empty"}', 1, t, 'Kitchen').text).toBe(en['preset.sourceKeyEmpty']);
    expect(saveOutcome('status 422: {"code":"spotify-uri-unplayable"}', 1, t, 'Kitchen').text).toBe(en['preset.spotifyNotSaveable']);
    // A 422 code the app does not know yet still reads as "cannot be kept".
    expect(saveOutcome('status 422: {"code":"brand-new"}', 1, t, 'Kitchen')).toMatchObject({ tone: 'warn' });
  });

  it('leaves every other failure to the caller', () => {
    expect(saveOutcome(new Error('dial tcp 192.0.2.1:8888: connection refused'), 1, t)).toBeNull();
    expect(saveOutcome('status 500: boom', 1, t)).toBeNull();
    expect(refusalCode('status 4220: {"code":"x"}')).toBe('');
  });
});

describe('savePlan', () => {
  it('keeps Spotify through its own save', () => {
    expect(savePlan({ nowLocation: 'http://192.0.2.1:8888/spotify/stream-3.ogg', nowPlayState: 'PLAY_STATE', nowSource: 'UPNP' })).toBe('spotify');
  });

  it('keeps Pandora and iHeartRadio as the speaker\'s own item', () => {
    expect(savePlan({ nowLocation: 'x', nowPlayState: 'PLAY_STATE', nowSource: 'PANDORA' })).toBe('native');
    expect(savePlan({ nowLocation: 'x', nowPlayState: 'PLAY_STATE', nowSource: 'IHEART' })).toBe('native');
  });

  it('leaves a station, a library song or folder, a copied key and nothing at all to the agent', () => {
    expect(savePlan({ nowLocation: 'http://example.com/rock', nowPlayState: 'PLAY_STATE', nowSource: 'INTERNET_RADIO' })).toBe('agent');
    expect(savePlan({ nowLocation: 'http://192.0.2.60/1.flac', nowPlayState: 'PLAY_STATE', nowSource: 'UPNP' })).toBe('agent');
    expect(savePlan({})).toBe('agent');
  });
});

describe('nowPlayingWhat', () => {
  it('names what plays, and nothing in standby or after a stop', () => {
    expect(nowPlayingWhat({ nowLocation: 'x', nowPlayState: 'PLAY_STATE', nowName: ' Jazz24 ' })).toBe('Jazz24');
    expect(nowPlayingWhat({ nowLocation: 'x', nowPlayState: 'PAUSE_STATE', nowName: '' }, 'unnamed')).toBe('unnamed');
    expect(nowPlayingWhat({ nowLocation: 'x', nowPlayState: 'STANDBY', nowName: 'Jazz24' })).toBe('');
    expect(nowPlayingWhat({ nowLocation: '', nowPlayState: 'PLAY_STATE', nowName: 'Jazz24' })).toBe('');
  });
});

describe('spotifyRecentItems', () => {
  it('offers each playlist, album or artist once, newest first, and nothing a key cannot recall', () => {
    const recent = [
      { ts: '2026-10-23T10:00:00Z', source: 'spotify', cardKey: 'spotify:playlist:aaa', cardName: 'Morning Mix', account: 'acct1' },
      { ts: '2026-10-23T12:00:00Z', source: 'spotify', cardKey: 'spotify:album:bbb', cardName: 'Night Lines', cardArt: 'http://example.com/c.jpg' },
      { ts: '2026-10-23T13:00:00Z', source: 'spotify', cardKey: 'spotify:playlist:aaa', cardName: 'Morning Mix' },
      { ts: '2026-10-23T14:00:00Z', source: 'spotify', cardKey: 'spotify:track:ccc', cardName: 'One song' },
      { ts: '2026-10-23T15:00:00Z', source: 'radio', cardKey: 'r:rock', cardName: 'Rock' },
      null,
    ];
    expect(spotifyRecentItems(recent)).toEqual([
      { uri: 'spotify:playlist:aaa', name: 'Morning Mix', account: '', art: '' },
      { uri: 'spotify:album:bbb', name: 'Night Lines', account: '', art: 'http://example.com/c.jpg' },
    ]);
    expect(spotifyRecentItems(null)).toEqual([]);
  });
});

describe('radioStations', () => {
  it('keeps the radio stations of the favourites, not a starred song or Pandora station', () => {
    const store = [
      { name: 'Jazz24', url: 'http://example.com/jazz24' },
      { name: 'Song', url: 'http://192.0.2.60/1.flac', source: 'upnp' },
      { name: 'Pandora', source: 'pandora', location: 'p' },
      { name: '', url: 'http://example.com/x' },
    ];
    expect(radioStations(store).map((s) => s.name)).toEqual(['Jazz24']);
    expect(radioStations(undefined)).toEqual([]);
  });
});

describe('folderPayload', () => {
  const song = (n) => ({ id: `t${n}`, title: `Song ${n}`, streamURL: `http://192.0.2.60/${n}.flac`, mimeType: 'audio/flac', durationSec: 100 + n, artist: 'A', album: 'B' });

  it('stores the folder\'s own songs with the folder\'s identity on its server', () => {
    const out = folderPayload({ name: 'Evening Sessions', source: 'NAS', udn: 'uuid:x', containerId: 'c1a', items: [song(1), { title: 'no url' }, song(2)] });
    expect(out.kept).toBe(2);
    expect(out.total).toBe(2);
    expect(out.payload).toMatchObject({ name: 'Evening Sessions', type: 'queue', shuffle: false, source: 'NAS', folder: { server_udn: 'uuid:x', container_id: 'c1a' } });
    expect(out.payload.items[0]).toEqual({ url: 'http://192.0.2.60/1.flac', title: 'Song 1', art: '', mime: 'audio/flac', duration_sec: 101, artist: 'A', album: 'B', object_id: 't1' });
  });

  it('caps a huge folder and says how many it kept', () => {
    const items = Array.from({ length: MAX_FOLDER_ITEMS + 5 }, (_, i) => song(i));
    const out = folderPayload({ name: 'All', items });
    expect(out.kept).toBe(MAX_FOLDER_ITEMS);
    expect(out.total).toBe(MAX_FOLDER_ITEMS + 5);
    expect(out.payload.folder).toBeUndefined();
  });

  it('has nothing to store for a folder without songs of its own', () => {
    expect(folderPayload({ name: 'Empty', items: [] })).toBeNull();
  });
});

describe('the editor\'s strings', () => {
  it('names every source tab', () => {
    expect(EDITOR_SOURCES).toEqual(['radio', 'library', 'spotify']);
    for (const s of EDITOR_SOURCES) expect(en[SOURCE_LABEL_KEYS[s]], s).toBeTruthy();
  });

  it('has every string it uses in English, and none in English in another bundle', () => {
    const src = readFileSync(new URL('./keyeditor.js', import.meta.url), 'utf8');
    const keys = [...src.matchAll(/\bL\('([a-zA-Z]+)'/g)].map((m) => `ui2.speakers.keyEditor.${m[1]}`);
    expect(keys.length).toBeGreaterThan(20);
    for (const k of keys) expect(en[k], k).toBeTruthy();
    const de = JSON.parse(readFileSync(new URL('../../i18n/bundles/de.json', import.meta.url), 'utf8'));
    for (const k of Object.keys(en).filter((x) => x.startsWith('ui2.speakers.keyEditor.') && !/^ui2\.speakers\.keyEditor\.src/.test(x))) {
      expect(de[k], k).not.toBe(en[k]);
    }
    expect(de['ui2.speakers.keyEditor.intro']).toContain('Wähle');
  });
});
