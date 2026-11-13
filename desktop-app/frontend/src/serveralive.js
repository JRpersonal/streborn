// serveralive.js: a music server that comes back clears its keys' red mark
// (#1890).
//
// A library key whose music server is off is refused, and the key shows
// "music server not answering" in red. That stayed until the key was pressed
// again or played, also long after the server was back. The app's SSDP
// listener hears a media server announce itself when it comes back, and the
// backend passes that on as one "mediaserver:alive" event ({location, uuid}).
// The marks of the keys that belong to that server are dropped then: matched
// by the server UDN the key recorded, else by the host of the key's file URL.
// No request to the server, no timer.

import { LIBRARY_SERVER_OFFLINE } from './libraryserveroff.js';

export const MEDIA_SERVER_ALIVE_EVENT = 'mediaserver:alive';

// isServerOffRefusal says whether a play error is the server-off refusal, the
// one kind of red mark a returning server clears.
export function isServerOffRefusal(errStr) {
  return String(errStr || '').includes(LIBRARY_SERVER_OFFLINE);
}

function hostOf(u) {
  if (!u) return '';
  try { return new URL(u).hostname.toLowerCase(); } catch { return ''; }
}

function bareUUID(u) {
  return String(u || '').trim().toLowerCase().replace(/^uuid:/, '');
}

// presetServer is what identifies a library key's music server: the UDN it
// recorded and the host of its file URL (either may be '').
export function presetServer(p) {
  if (!p) return { udn: '', host: '' };
  const udn = bareUUID((p.track && p.track.server_udn) || (p.folder && p.folder.server_udn) || '');
  const url = p.stream_url || p.url || (Array.isArray(p.items) && p.items[0] && p.items[0].url) || '';
  return { udn, host: hostOf(url) };
}

// serverMatches says whether an alive event is the server of key p.
export function serverMatches(p, ev) {
  if (!p || !ev) return false;
  const k = presetServer(p);
  const uuid = bareUUID(ev.uuid);
  if (k.udn && uuid) return k.udn === uuid;
  const host = hostOf(ev.location);
  return !!(k.host && host && k.host === host);
}

// clearForAliveServer drops the server-off marks of the keys whose server just
// announced itself, and reports the slots it cleared. errors and serverOff are
// state.presetErrors and state.presetServerOff, changed in place.
export function clearForAliveServer(errors, serverOff, presets, ev) {
  const cleared = [];
  for (const slot of Object.keys(serverOff || {})) {
    if (!serverOff[slot]) continue;
    if (!errors[slot]) { delete serverOff[slot]; continue; }
    const p = (presets || []).find(x => x && String(x.slot) === String(slot));
    if (!serverMatches(p, ev)) continue;
    delete errors[slot];
    delete serverOff[slot];
    cleared.push(Number(slot));
  }
  return cleared;
}
