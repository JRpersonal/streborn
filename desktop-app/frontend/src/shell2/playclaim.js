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

// resetPlayClaims empties the claims and listeners (tests only).
export function resetPlayClaims() {
  listeners.clear();
  owners.clear();
}
