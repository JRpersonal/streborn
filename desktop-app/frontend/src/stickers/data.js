// stickers/data.js: what goes on a speaker's sticker, read from its presets,
// and the three things this computer remembers about the stickers.
//
// No api.js and no DOM here: turning presets into keys, the fingerprint of a
// key assignment and the storage are unit-tested as they are. Reading the
// speakers is in collect.js.

import { extractHost, logoHostsFor } from '../logos.js';
import { ROW_SIZE, boxPresetRows } from '../presetrows.js';
import { stickerKind } from './layout.js';
import { appArtFromBoxArt, presetOwnArt } from '../utils.js';

// speakerId names a speaker in what is remembered: its device ID, or its
// address for a record that has none.
export function speakerId(box) {
  return String((box && (box.deviceID || box.host)) || '');
}

// presetKind: a key that plays a list of tracks (a Spotify playlist or album,
// a music-library album or folder) gets the list symbol, everything else is a
// station.
export function presetKind(p) {
  return p && (p.type === 'spotify' || p.type === 'queue') ? 'playlist' : 'station';
}

// stickerKeys turns a speaker's presets into the six keys of its sticker.
// Slots 1 to 6 are the keys pressed once; slot 6+n is key n pressed twice and
// only counts on a speaker that keeps the second row (presetrows.js). Every
// key is there, also an empty one: it still shows its number.
export function stickerKeys(box, presets) {
  const list = Array.isArray(presets) ? presets : [];
  const named = (slot) => list.find((p) => p && p.slot === slot && p.name) || null;
  const twoRows = boxPresetRows(box, list) >= 2;
  const keys = [];
  for (let n = 1; n <= ROW_SIZE; n++) {
    const once = named(n);
    const twice = twoRows ? named(ROW_SIZE + n) : null;
    keys.push({
      n,
      name: once ? String(once.name) : '',
      kind: once ? presetKind(once) : 'station',
      logo: null,
      preset: once,
      twice: twice ? { name: String(twice.name), kind: presetKind(twice) } : null,
    });
  }
  return keys;
}

// logoRequest is where a key's picture can come from, in the terms the
// resolvers take. It follows the preset tile (views/music/presettiles.js): the
// art the key carries, unwrapped from the speaker's own art proxy, and for a
// station the hosts its logo can be looked up by.
//   inline  a data: URI the key already carries, usable as it is
//   direct  the first real image address
//   lookup  { fav, brand, hosts } for ResolveStationLogo, null for a Spotify
//           key (its cover is the only candidate, as on the tile)
export function logoRequest(p) {
  if (!p) return { inline: '', direct: '', lookup: null };
  const spotify = p.type === 'spotify';
  const cands = appArtFromBoxArt(spotify ? p.art : presetOwnArt(p)).split('|').map((c) => c.trim()).filter(Boolean);
  const inline = cands.find((c) => /^data:image\//i.test(c)) || '';
  const direct = cands.find((c) => /^https?:\/\//i.test(c) && !c.startsWith('https://icons.duckduckgo.com/')) || '';
  if (spotify) return { inline, direct, lookup: null };
  const station = { homepage: p.homepage || '', url: p.stream_url || '', url_resolved: p.stream_url || '' };
  return { inline, direct, lookup: { fav: direct, brand: extractHost(station.homepage), hosts: logoHostsFor(station) } };
}

// keysFingerprint is the key assignment as one short string: equal for two
// readings that would print the same names on the same keys. Logos and the
// housing colour are not part of it.
export function keysFingerprint(keys) {
  const text = JSON.stringify((keys || []).map((k) => [k.n, k.name, k.kind, k.twice ? k.twice.name : '', k.twice ? k.twice.kind : '']));
  // FNV-1a, 32 bit: a fingerprint to compare, not a secret.
  let h = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0).toString(16).padStart(8, '0');
}

// ---------- remembered on this computer ----------

const HOUSING_KEY = 'str.stickerHousing';
const PRINTED_KEY = 'str.stickerPrinted';
const HINT_KEY = 'str.stickerHint';
const REMOTE_KEY = 'str.stickerRemote';

const store = (storage) => (storage === undefined ? globalThis.localStorage : storage);

function readMap(key, storage) {
  try {
    const v = JSON.parse(store(storage).getItem(key) || '{}');
    return v && typeof v === 'object' && !Array.isArray(v) ? v : {};
  } catch {
    return {};
  }
}

function writeMap(key, map, storage) {
  try { store(storage).setItem(key, JSON.stringify(map)); } catch { /* blocked storage: asked again next time */ }
}

// The housing colour cannot be read from a speaker, so the user's choice per
// speaker is kept. Until chosen it is white, and dark for the Portable, whose
// sticker is only made in its grey.
export function housingOf(id, storage, model = '') {
  const chosen = readMap(HOUSING_KEY, storage)[id];
  if (chosen === 'white' || chosen === 'dark') return chosen;
  return stickerKind(model) === 'portable' ? 'dark' : 'white';
}

export function setHousing(id, housing, storage) {
  const map = readMap(HOUSING_KEY, storage);
  map[id] = housing === 'white' ? 'white' : 'dark';
  writeMap(HOUSING_KEY, map, storage);
}

// Whether a speaker's sheet also carries the sticker for the back of its
// remote. Any model can have one, so it is offered for every speaker; it
// is on until the user unticks it (Jens, 2026-11-22), and the choice is kept
// per speaker: only an explicit false is stored.
export function remoteOf(id, storage) {
  return readMap(REMOTE_KEY, storage)[id] !== false;
}

export function setRemote(id, on, storage) {
  const map = readMap(REMOTE_KEY, storage);
  if (on) delete map[id];
  else map[id] = false;
  writeMap(REMOTE_KEY, map, storage);
}

// printedFingerprints is what was on each speaker's keys when the stickers
// were last saved as a PDF, by speaker.
export function printedFingerprints(storage) {
  return readMap(PRINTED_KEY, storage);
}

// rememberPrinted records a saved sheet. Speakers that were not on it keep
// what was remembered for them.
export function rememberPrinted(fingerprints, storage) {
  writeMap(PRINTED_KEY, { ...readMap(PRINTED_KEY, storage), ...fingerprints }, storage);
}

// The stale set: speakers whose keys were seen to differ from their printed
// sticker since the app started. A speaker enters and leaves it whenever its
// presets are read for the keys view, so no speaker is asked just for this.
const stale = new Set();
// What each speaker's slots played at the last reading, to notice a save.
const lastSeen = new Map();

// slotSources is what every filled slot plays, without its name: a rename
// leaves it as it is, another station or playlist on the key does not.
function slotSources(presets) {
  const out = new Map();
  for (const p of Array.isArray(presets) ? presets : []) {
    if (!p || !p.name) continue;
    out.set(p.slot, JSON.stringify([p.type || '', p.stream_url || '', p.uri || '', p.native || null, p.folder || null, p.track || null, p.items || null]));
  }
  return out;
}

// noteSpeakerKeys takes every fresh reading of a speaker's presets for the
// keys view and does two things with it. It compares the keys with what was
// printed for that speaker (one that was never printed is never stale). And
// it notices a save: every way of putting something on a key (the search, a
// hold, the library, Spotify, the phone) ends in such a reading, so a slot
// that was empty at the reading before, or played something else, is a key
// that was just saved, and that offers the one-time hint. A rename or a
// cleared key does not.
export function noteSpeakerKeys(box, presets, storage) {
  const id = speakerId(box);
  if (!id) return;
  const keys = stickerKeys(box, presets);
  const now = slotSources(presets);
  const before = lastSeen.get(id);
  lastSeen.set(id, now);
  if (before && [...now].some(([slot, src]) => before.get(slot) !== src)) offerHint(storage);
  const printed = printedFingerprints(storage)[id];
  if (printed && printed !== keysFingerprint(keys)) stale.add(id);
  else stale.delete(id);
}

export function stickersStale() {
  return stale.size > 0;
}

export function clearStale(ids) {
  for (const id of ids || [...stale]) stale.delete(id);
}

// forgetReadings drops what noteSpeakerKeys has seen, for the tests.
export function forgetReadings() {
  stale.clear();
  lastSeen.clear();
}

// The one-time hint: 'show' from the first preset saved since the feature
// exists until the user closes it or opens the stickers, then 'done' for good.
export function hintState(storage) {
  try { return store(storage).getItem(HINT_KEY) || ''; } catch { return 'done'; }
}

// offerHint is called when a key was saved (noteSpeakerKeys).
export function offerHint(storage) {
  try {
    const s = store(storage);
    if (!s.getItem(HINT_KEY)) s.setItem(HINT_KEY, 'show');
  } catch { /* nowhere to remember it: then no hint, rather than one that returns */ }
}

export function finishHint(storage) {
  try { store(storage).setItem(HINT_KEY, 'done'); } catch { /* see offerHint */ }
}
