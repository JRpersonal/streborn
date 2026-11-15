// shell2/playclaim.js: one "Playing on <speaker>" line per speaker in the
// UI 2.0 music views.
//
// A station row (views/music/ui2radio.js) and a Recently played card
// (views/recent.js) each keep the line of their last play: "Starting on
// Kitchen...", "Sent to Kitchen...", "Playing on Kitchen". A speaker plays one
// thing at a time, so a new play on it ends every other line about it. Before
// this, the line of the previous station stayed, and while a speaker switched
// stations "Playing on Kitchen" stood under the old and the new one at once
// (a mail report, 2026-11).
//
// A play line is a verdict ({ kind, text }) tagged with the speaker it is
// about (playLine). A view that starts a play claims the speaker for its row
// (claimPlay); every view drops the tagged lines of its other rows for that
// speaker (onPlayClaim + playLinesToDrop). A play whose answer arrives after
// another row took the speaker over says nothing (ownsPlay).
//
// Joining a group is a claim too: "+" on a speaker that plays something else
// takes it over for the group's music, so joinReview says beforehand what
// stops and what plays instead.

import { t } from '../i18n/index.js';
import { decodeXmlEntities } from '../utils.js';

const listeners = new Set();
// host -> owner of the newest play on that speaker.
const owners = new Map();

// playLine tags a verdict as the line of a play on host, with the station it
// started (url, name), so the line can be matched against what the speaker
// reports. A verdict without a speaker stays as it is.
export function playLine(verdict, host, what = {}) {
  if (!verdict || !host) return verdict;
  return { ...verdict, playHost: host, playUrl: what.url || '', playName: what.name || '' };
}

// onPlayClaim registers fn(host, owner), called on every claim. Returns the
// unsubscribe function.
export function onPlayClaim(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

// claimPlay says that owner (a row id such as "radio:<key>" or
// "recent:<cardKey>") just started a play on host. Every listener drops its
// other lines about host.
export function claimPlay(host, owner) {
  if (!host) return;
  owners.set(host, owner);
  for (const fn of listeners) {
    try { fn(host, owner); } catch { /* one view's repaint must not stop the others */ }
  }
}

// ownsPlay reports whether owner still holds the newest play on host, so a
// slow answer of an earlier play does not paint its line back.
export function ownsPlay(host, owner) {
  return !host || !owners.has(host) || owners.get(host) === owner;
}

// playLinesToDrop lists the keys of statuses (a Map key -> line) whose play
// line is about host, all but keep's.
export function playLinesToDrop(statuses, host, keep) {
  const out = [];
  if (!statuses || !host) return out;
  for (const [key, st] of statuses) {
    if (key !== keep && st && st.playHost === host) out.push(key);
  }
  return out;
}

// playLineOutdated reports whether a "Playing on" line no longer holds: the
// speaker it names reports that it plays something, and that is neither the
// line's address nor its station name. A speaker that stopped, buffers or
// reports nothing keeps the line (no guessing while it switches).
export function playLineOutdated(st, now) {
  if (!st || st.kind !== 'ok' || !st.playHost || !now) return false;
  if (st.playHost !== now.host || now.playState !== 'PLAY_STATE' || !now.location) return false;
  if (!st.playUrl && !st.playName) return false;
  if (st.playUrl && now.location === st.playUrl) return false;
  if (st.playName && now.name === st.playName) return false;
  return true;
}

// statusNowItem is what a speaker's /now_playing answer says it plays:
// { name, source, location }. The name is the station when the speaker
// reports one, else the item (status.js reads the speaker you control the
// same way); source and location are the ones status.js keeps as
// state.nowSource / state.nowLocation. A speaker in standby names nothing.
// AUX and Bluetooth do name something (the input, the phone), and many
// Spotify playlists share a name across accounts, so the name alone never
// says two speakers play the same music (sameMusic).
export function statusNowItem(xml) {
  const none = { name: '', source: '', location: '' };
  if (typeof xml !== 'string' || !xml) return none;
  const raw = (xml.match(/<stationName>([^<]+)<\/stationName>/) || [])[1]
    || (xml.match(/<itemName>([^<]+)<\/itemName>/) || [])[1] || '';
  return {
    name: String(decodeXmlEntities(raw) || '').trim(),
    source: (xml.match(/source="([^"]+)"/) || [])[1] || '',
    location: String(decodeXmlEntities((xml.match(/location="([^"]+)"/) || [])[1] || '') || ''),
  };
}

// The sources whose stream is one public station that every speaker plays
// alike. Everything else is a local input (AUX, Bluetooth, TV), an account
// (Spotify, Deezer, Amazon) or a phone (AirPlay), where two speakers with the
// same name on them may still play different music.
const STATION_SOURCES = new Set(['INTERNET_RADIO', 'LOCAL_INTERNET_RADIO', 'TUNEIN']);
// STR's own station stream on the agent (/stream/<slot> or /stream/raw?u=),
// which the speaker reports as UPNP. Its Spotify stream (/spotify/stream) and
// a library track are not stations.
const STR_STATION = /\/stream\/(?:\d+|raw)(?:[?#]|$)/;

// nowItem takes what joinReview is handed: an item from statusNowItem, or a
// bare name (which can never match, having no source or location).
function nowItem(v) {
  if (v && typeof v === 'object') {
    return {
      name: String(v.name || '').trim(),
      source: String(v.source || '').trim().toUpperCase(),
      location: String(v.location || '').trim(),
    };
  }
  return { name: String(v || '').trim(), source: '', location: '' };
}

// sameMusic says two speakers play the very same station: same name, same
// source, same stream, and a source that is a public station. Anything less
// is treated as different music, since a wrong "same" adds a speaker without
// a word while a wrong "different" only costs one more click.
export function sameMusic(a, b) {
  const x = nowItem(a);
  const y = nowItem(b);
  if (!x.name || !y.name || x.name.toLowerCase() !== y.name.toLowerCase()) return false;
  if (!x.source || x.source !== y.source) return false;
  if (!x.location || x.location !== y.location) return false;
  return STATION_SOURCES.has(x.source) || (x.source === 'UPNP' && STR_STATION.test(x.location));
}

// joinReview is the sentence the sidebar shows before "+" takes a speaker
// that plays something of its own into the group: joining a zone ends that
// music without a word, so the row first says what stops and what follows.
//
//   name    the speaker that would join
//   playing it plays right now
//   stops   what it plays ({ name, source, location } from statusNowItem;
//           a name '' when the speaker names nothing)
//   plays   what the group plays (the same shape; a name '' when unknown)
//   master  the name of the group's main speaker
//
// null when there is nothing to review: an idle speaker or one in standby
// joins at once, as before, and so does one that already plays the very
// station the group plays (sameMusic), since joining changes nothing anyone
// hears.
export function joinReview({ name, playing, stops, plays, master } = {}) {
  if (!playing || !name) return null;
  if (sameMusic(stops, plays)) return null;
  const s = nowItem(stops).name;
  const p = nowItem(plays).name;
  // The same name on different music ("Kitchen stops AUX IN and plays AUX
  // IN") would read as if nothing changed, so the sentence names the main
  // speaker it plays along with instead.
  const alike = !!s && !!p && s.toLowerCase() === p.toLowerCase();
  let text;
  if (alike) text = t('ui2.side.joinJoins', { name, master: master || '' });
  else if (s && p) text = t('ui2.side.joinStopsPlays', { name, stops: s, plays: p });
  else if (s) text = t('ui2.side.joinStopsJoins', { name, stops: s, master: master || '' });
  else if (p) text = t('ui2.side.joinPlays', { name, plays: p });
  else text = t('ui2.side.joinJoins', { name, master: master || '' });
  return { text, stops: s, plays: p };
}

// resetPlayClaims empties the claims and listeners (tests only).
export function resetPlayClaims() {
  listeners.clear();
  owners.clear();
}
