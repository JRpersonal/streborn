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

// statusNowName is what a speaker's /now_playing answer names: the station
// when it reports one, else the item (status.js reads the speaker you control
// the same way). '' for a speaker in standby or one that names nothing (AUX,
// Bluetooth without metadata).
export function statusNowName(xml) {
  if (typeof xml !== 'string' || !xml) return '';
  const raw = (xml.match(/<stationName>([^<]+)<\/stationName>/) || [])[1]
    || (xml.match(/<itemName>([^<]+)<\/itemName>/) || [])[1] || '';
  return String(decodeXmlEntities(raw) || '').trim();
}

// joinReview is the sentence the sidebar shows before "+" takes a speaker
// that plays something of its own into the group: joining a zone ends that
// music without a word, so the row first says what stops and what follows.
//
//   name    the speaker that would join
//   playing it plays right now
//   stops   what it plays ('' when the speaker names nothing)
//   plays   what the group plays ('' when unknown)
//   master  the name of the group's main speaker
//
// null when there is nothing to review: an idle speaker or one in standby
// joins at once, as before, and so does one that already plays the very
// station the group plays, since joining changes nothing anyone hears.
export function joinReview({ name, playing, stops, plays, master } = {}) {
  if (!playing || !name) return null;
  const s = String(stops || '').trim();
  const p = String(plays || '').trim();
  if (s && p && s.toLowerCase() === p.toLowerCase()) return null;
  let text;
  if (s && p) text = t('ui2.side.joinStopsPlays', { name, stops: s, plays: p });
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
