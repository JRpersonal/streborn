// shell2/gotosource.js: where the music in the UI 2.0 player comes from, as
// the tab that holds it.
//
// The player names what plays on every view, but the place it was started
// from (a station in Radio, a folder in the Library, a playlist in Spotify)
// was three or four clicks away. The button next to the player's title opens
// that tab, and for a music-library folder the folder itself. Sources without
// a tab of their own (Bluetooth, AUX, TV, AirPlay, anything STR cannot name)
// get no button rather than a guess.
//
// Pure: plain data in, plain data out, so vitest reads it without a DOM.

import { agentLoneFile, libraryPlayMatches, orionStationPayload, proxiedRadioPlaying } from '../utils.js';
import { queueNowItem } from '../librarynow.js';

// The words of the button for each tab, as its name and its tooltip. The tab's
// own name is the visible text (nav.js), and every translation keeps that name
// whole inside the sentence, so what is seen is part of what is heard.
export const GO_SOURCE_LABEL_KEYS = Object.freeze({
  box: 'ui2.player.goRadio',
  library: 'ui2.player.goLibrary',
  spotify: 'ui2.player.goSpotify',
});

// Inputs and receivers the speaker plays on its own: no tab of the app holds
// them, so a stale location left over from before must not name one.
const NO_TAB = /^(BLUETOOTH|AUX|LOCAL|PRODUCT|HDMI|TV|OPTICAL|AIRPLAY.*|NOTIFICATION|ALEXA)$/;
// The speaker's names for a station. Pandora and iHeartRadio sit in Radio too.
const RADIO_SOURCES = new Set(['TUNEIN', 'LOCAL_INTERNET_RADIO', 'INTERNET_RADIO', 'RADIOPLAYER', 'PANDORA', 'IHEART']);
// The speaker's own music-library sources (a media server it browses itself).
const LIBRARY_SOURCES = new Set(['STORED_MUSIC', 'STORED_MUSIC_MEDIA_SERVER', 'STORED_MUSIC_MEDIA_RENDERER', 'LOCAL_MUSIC']);

// folderFromCard is the folder a running queue was started from, read off the
// agent's card "queue:<server udn>:<container id>" (GET /api/queue card). A
// key's card ("queue:slot:<n>") names a key, not a folder: null.
export function folderFromCard(card) {
  const s = String(card || '');
  if (!s.startsWith('queue:') || s.startsWith('queue:slot:')) return null;
  const rest = s.slice('queue:'.length);
  return rest ? { card: rest } : null;
}

// splitCardFolder splits the rest of a folder card into its server and its
// container. The server's UDN has colons of its own ("uuid:..."), so a server
// the Library knows is matched whole first (the longest one wins); a server
// not listed (yet) is split after the UDN's usual "uuid:<id>" shape.
export function splitCardFolder(rest, udns = []) {
  const s = String(rest || '');
  const known = (udns || []).filter((u) => u && s.startsWith(u + ':') && s.length > u.length + 1)
    .sort((a, b) => b.length - a.length)[0];
  if (known) return { udn: known, id: s.slice(known.length + 1) };
  const m = s.match(/^(uuid:[^:]+):(.+)$/i) || s.match(/^([^:]+):(.+)$/);
  return m ? { udn: m[1], id: m[2] } : null;
}

// folderStack is the Library trail that leads to folder id: the first of the
// known trails (the open one, the ones walked this session, the remembered
// one) that passes through it, cut after it. A folder no trail passes through
// gets the server root and the folder, so Back still leads somewhere real.
export function folderStack(id, known = [], title = '') {
  const want = String(id || '');
  if (!want) return null;
  for (const stack of known || []) {
    if (!Array.isArray(stack)) continue;
    const at = stack.findIndex((s) => s && String(s.id) === want);
    if (at >= 0) return stack.slice(0, at + 1).map((s) => ({ ...s }));
  }
  if (want === '0') return [{ id: '0', title: '' }];
  return [{ id: '0', title: '' }, { id: want, title: title || '' }];
}

// goSourceTarget is where the button leads for what the speaker plays now:
// { view, folder } with view one of 'box' (Radio), 'library', 'spotify', and
// folder the Library folder to open ({ card } or { udn, id }, with a title for
// the trail) when it is known; null when no tab holds the source.
//
//   source           the speaker's now-playing source (state.nowSource)
//   location         its now-playing location
//   decodedLocation  the location with an agent relay wrapper taken off
//   queue            the agent's GET /api/queue reply (state.queue)
//   lastLibraryPlay  the track this app last started from the Library
//   optimistic       the app has just started something itself: the source
//                    the speaker reports may still be the previous one
export function goSourceTarget({
  source = '', location = '', decodedLocation = '', queue = null, lastLibraryPlay = null, optimistic = false,
} = {}) {
  const src = String(source || '').toUpperCase();
  const loc = String(location || '');
  const decoded = String(decodedLocation || '');
  if (NO_TAB.test(src)) return null;
  if (src === 'SPOTIFY' || /\/spotify\//.test(loc) || /\/playback\/container\//.test(loc)) {
    return { view: 'spotify', folder: null };
  }
  // A folder that plays as a queue, before the radio tests: a file from an
  // HTTPS media server is relayed through /stream/raw like a station.
  const item = queueNowItem(queue, { location: loc, optimistic });
  if (item) {
    const folder = folderFromCard(queue.card);
    return { view: 'library', folder: folder ? { ...folder, title: item.album || '' } : null };
  }
  if (libraryPlayMatches(lastLibraryPlay, loc, decoded)) {
    const ref = lastLibraryPlay.ref;
    const folder = ref && ref.parent_id
      ? { udn: ref.server_udn || '', id: ref.parent_id, title: lastLibraryPlay.album || '' }
      : null;
    return { view: 'library', folder };
  }
  const lone = agentLoneFile(queue);
  if (lone && loc && (lone.url === loc || lone.url === decoded)) return { view: 'library', folder: null };
  if (proxiedRadioPlaying(loc) || orionStationPayload(loc)) return { view: 'box', folder: null };
  // A station by the speaker's own word. Right after the app started a library
  // file the source still names what played before, while the location is
  // already the file's address: no button until the speaker catches up.
  if (RADIO_SOURCES.has(src) && !(optimistic && /^https?:\/\//i.test(loc))) return { view: 'box', folder: null };
  if (LIBRARY_SOURCES.has(src)) return { view: 'library', folder: null };
  return null;
}

// goSourceLabelKey is the i18n key of the button's name for a target view.
export function goSourceLabelKey(view) {
  return GO_SOURCE_LABEL_KEYS[view] || '';
}
