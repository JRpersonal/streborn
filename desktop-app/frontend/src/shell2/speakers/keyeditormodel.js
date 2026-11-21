// shell2/speakers/keyeditormodel.js: the pure half of the key assignment
// editor on a speaker's Keys and alarms tab in the UI 2.0 preview
// (keyeditor.js draws it).
//
// The editor shows the speaker's keys next to a source browser (Radio, Library,
// Spotify), so a station, a folder, a song or a playlist goes onto a key in one
// place, and it has one "Put what's playing on a key" action for every source
// the speaker agent can keep. Everything here is data in, data out: which keys
// there are and which one is chosen first, what a refusal of the agent means in
// plain words, which recent Spotify plays can go onto a key, and how a folder
// is stored. No DOM and no api.js, so vitest reads it as it is.

import { nativeServiceSaveable } from '../../nativeservice.js';
import { parsePresetConflict } from '../../presetmove.js';
import { ROW_SIZE, boxPresetRows, keyLabel, keyNum } from '../../presetrows.js';
import { spotifyPlaying } from '../../views/spotifyui2model.js';
import { keyFailedVerdict } from '../feedback.js';

// The three sources of the browser, in the order of its tabs.
export const EDITOR_SOURCES = Object.freeze(['radio', 'library', 'spotify']);

export const SOURCE_LABEL_KEYS = Object.freeze({
  radio: 'ui2.speakers.keyEditor.srcRadio',
  library: 'ui2.speakers.keyEditor.srcLibrary',
  spotify: 'ui2.speakers.keyEditor.srcSpotify',
});

// The most songs a folder key keeps; the same cap as the Library's folder key
// (views/library.js MAX_QUEUE_PRESET_ITEMS), because the speaker's store
// refuses more and a whole-library folder would crowd its flash.
export const MAX_FOLDER_ITEMS = 1000;

// A play state in which nothing plays that a key could keep.
const IDLE_STATES = new Set(['', 'STANDBY', 'STOP_STATE', 'INVALID_PLAY_STATE']);

// editorKeys is the speaker's keys as the editor lists them: the six of the
// first row, and the six pressed twice when the speaker keeps a second row
// (presetrows.js). An empty key is listed too, it is the likeliest target.
export function editorKeys(box, presets, t) {
  const list = Array.isArray(presets) ? presets : [];
  const max = boxPresetRows(box, list) * ROW_SIZE;
  const out = [];
  for (let slot = 1; slot <= max; slot++) {
    const p = list.find((x) => x && x.slot === slot);
    const name = p && p.name ? String(p.name) : '';
    out.push({ slot, label: keyLabel(slot, t), name, empty: !name, row: Math.ceil(slot / ROW_SIZE) });
  }
  return out;
}

// defaultSlot is the key chosen when the editor opens on a speaker: the first
// empty key of the first row (nothing gets replaced by accident), else key 1.
// A choice the user made stays while that key still exists.
export function defaultSlot(keys, chosen = 0) {
  const list = Array.isArray(keys) ? keys : [];
  if (chosen && list.some((k) => k.slot === chosen)) return chosen;
  const free = list.find((k) => k.row === 1 && k.empty);
  return free ? free.slot : (list.length ? list[0].slot : 1);
}

// keyHintText says what putting something on the chosen key does: fill an
// empty key, or replace what is on it, named.
export function keyHintText(keys, slot, t) {
  const k = (keys || []).find((x) => x.slot === slot);
  const n = keyNum(slot, t);
  if (!k || k.empty) return t('ui2.speakers.keyEditor.hintEmpty', { n });
  return t('ui2.speakers.keyEditor.hintReplace', { n, current: k.name });
}

// The agent's 422 codes of a save of what plays, and the plain words for each.
// The speaker name goes into the ones that talk about what it plays. Only these:
// every save of the editor ends in saveOutcome, and a station, a folder or a
// Pandora station refused for a reason of its own (stream-url-is-webpage,
// queue-empty, native-item-invalid, ...) is not "what plays cannot be kept".
const REFUSALS = Object.freeze({
  'nothing-playing': 'ui2.speakers.keyEditor.nothingPlaying',
  'nothing-saveable': 'ui2.speakers.keyEditor.nothingSaveable',
  'source-preset-empty': 'preset.sourceKeyEmpty',
  'spotify-uri-unplayable': 'preset.spotifyNotSaveable',
});

// refusalCode is the code of an agent's 422 answer as it reaches the app
// ("status 422: {"code":"nothing-saveable",...}"), '' for anything else.
export function refusalCode(err) {
  const s = String((err && err.message) || err || '');
  const m = /status 422: .*"code"\s*:\s*"([a-z-]+)"/.exec(s);
  return m ? m[1] : '';
}

// refusalDetail is the agent's own reason of a 422 answer (the "error" field of
// its body), '' when the body carries none. The agent words each refusal for a
// person ("This preset has no playable stream."), so it is the detail a
// refusal the app has no words of its own for is shown with.
export function refusalDetail(err) {
  const s = String((err && err.message) || err || '');
  const at = s.indexOf('status 422: ');
  if (at < 0) return '';
  try {
    const body = JSON.parse(s.slice(at + 'status 422: '.length));
    return body && typeof body.error === 'string' ? body.error.trim() : '';
  } catch {
    return '';
  }
}

// saveOutcome sorts a failed save: the agent's 409 already-on-slot is a
// question with a way out (move it here), a 422 of a save of what plays is a
// plain-words note, any other 422 says the key was refused with the agent's
// own reason, and everything else is left to the caller (an error with its
// detail). slot is the chosen key.
export function saveOutcome(err, slot, t, speaker = '') {
  const conflict = parsePresetConflict(err);
  if (conflict) {
    const n = keyNum(conflict.slot, t);
    const text = conflict.name
      ? t('ui2.speakers.keyEditor.conflict', { what: conflict.name, n, name: speaker })
      : t('ui2.speakers.keyEditor.conflictUnnamed', { n, name: speaker });
    // Already on the chosen key itself: nothing to move.
    const move = conflict.slot !== slot ? { from: conflict.slot, to: slot, name: conflict.name } : null;
    return { tone: 'warn', text, move };
  }
  const code = refusalCode(err);
  if (code && Object.hasOwn(REFUSALS, code)) {
    return { tone: 'warn', text: t(REFUSALS[code], { name: speaker }) };
  }
  if (code) {
    // The code itself is the last resort: it still names the reason.
    const reason = refusalDetail(err) || code;
    return { tone: 'error', text: keyFailedVerdict(keyNum(slot, t), speaker, reason).text };
  }
  return null;
}

// savePlan is how "Put what's playing on a key" keeps what plays: Spotify and
// Pandora or iHeartRadio go through their own saves (the agent's save-playing
// answers 422 for them, docs/CONTROL-API.md), everything else is the agent's
// own record of the play (POST /api/box/save-playing).
export function savePlan(st) {
  const s = st || {};
  if (spotifyPlaying(s).playing) return 'spotify';
  if (nativeServiceSaveable(s.nowSource)) return 'native';
  return 'agent';
}

// nowPlayingWhat is the name of what the speaker plays, '' when it plays
// nothing a key could keep. A play without a name says that it plays.
export function nowPlayingWhat(st, fallback = '') {
  const s = st || {};
  if (!s.nowLocation || IDLE_STATES.has(s.nowPlayState || '')) return '';
  return String(s.nowName || '').trim() || fallback;
}

// spotifyRecentItems is the Spotify plays of a speaker's Recently played that
// can go onto a key, newest first, one per playlist, album or artist: a card
// whose URI is a spotify: context. A single Spotify song, a radio or a station
// cannot be recalled from a key, so they are left out.
export function spotifyRecentItems(recent) {
  const list = Array.isArray(recent) ? recent : [];
  const seen = new Set();
  const out = [];
  const sorted = list.slice().sort((a, b) => String((b && b.ts) || '').localeCompare(String((a && a.ts) || '')));
  for (const e of sorted) {
    if (!e) continue;
    const uri = [e.cardURL, e.cardKey].map((x) => String(x || '')).find((x) => /^spotify:(playlist|album|artist|show):/i.test(x));
    if (!uri || seen.has(uri)) continue;
    seen.add(uri);
    out.push({ uri, name: String(e.cardName || '').trim() || uri, account: String(e.account || ''), art: String(e.cardArt || '') });
  }
  return out;
}

// radioStations is the radio stations of the favourites store: a starred
// library song or Pandora station has its own way onto a key.
export function radioStations(store) {
  return (Array.isArray(store) ? store : []).filter((s) => s && !s.source && (s.url_resolved || s.url) && s.name);
}

// folderPayload is the folder key of an open media-server folder: its own
// songs (the ones with an address), in the order listed, capped at
// MAX_FOLDER_ITEMS, with the folder's identity on its server so the speaker
// can find it again when the server renumbers its files. null when the folder
// holds no song of its own.
export function folderPayload({ name = '', source = '', udn = '', containerId = '', items = [] } = {}) {
  const songs = (Array.isArray(items) ? items : []).filter((it) => it && it.streamURL).map((it) => ({
    url: it.streamURL,
    title: it.title || '',
    art: it.albumArtURL || '',
    mime: it.mimeType || '',
    duration_sec: it.durationSec || 0,
    artist: it.artist || '',
    album: it.album || '',
    ...(it.id ? { object_id: String(it.id) } : {}),
  }));
  if (!songs.length) return null;
  const payload = { name: name || source, type: 'queue', shuffle: false, source, items: songs.slice(0, MAX_FOLDER_ITEMS) };
  if (udn && containerId) payload.folder = { server_udn: udn, container_id: String(containerId) };
  return { payload, kept: payload.items.length, total: songs.length };
}
