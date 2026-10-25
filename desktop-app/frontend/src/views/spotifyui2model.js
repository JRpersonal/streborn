// views/spotifyui2model.js: what the UI 2.0 preview's Spotify view shows,
// decided from the shared state without touching the DOM (unit-tested).
//
// Two questions: is the selected speaker playing something from Spotify that
// can go on one of its keys, and which Spotify login does every speaker hold.
// The answers come from the same reads the Music view uses (the speaker's
// now-playing and the agent's /spotify/info), nothing new is asked.

import { spotifySaveContextURI } from '../utils.js';
import { spotifyAccountName } from '../spotifyaccountlabel.js';

// A location of this shape is Spotify on the speaker: STR's own engine stream
// or the speaker's native Spotify entry. The same test savePresetCase makes
// before it takes the Spotify save path.
const SPOTIFY_LOCATION = /\/spotify\/stream|\/playback\/container/;

// Play states in which nothing is on: the key would be saved from whatever the
// speaker last reported, which is not what the user hears.
const IDLE_STATES = new Set(['', 'STANDBY', 'STOP_STATE', 'INVALID_PLAY_STATE']);

// spotifyContextKind names what a spotify: URI points at: playlist, album,
// artist, track, show or episode, or '' for anything else. The old
// spotify:user:<id>:playlist:<id> form is a playlist too.
export function spotifyContextKind(uri) {
  const parts = String(uri || '').split(':');
  if (parts[0] !== 'spotify' || parts.length < 3) return '';
  const kind = parts[parts.length - 2];
  return ['playlist', 'album', 'artist', 'track', 'show', 'episode'].includes(kind) ? kind : '';
}

// spotifyPlaying reads the selected speaker's now-playing. playing is true
// while Spotify plays (or is paused) on it; uri is the context a key would get,
// '' when the speaker has not told it yet (the save path asks the speaker again
// and says so if it still cannot tell).
export function spotifyPlaying(s) {
  const st = s || {};
  const loc = st.nowLocation || '';
  if (!SPOTIFY_LOCATION.test(loc) || IDLE_STATES.has(st.nowPlayState || '')) {
    return { playing: false, name: '', uri: '', kind: '' };
  }
  const uri = spotifySaveContextURI(st.nowSource, loc, st.spotifyNow ? st.spotifyNow.context : '');
  return { playing: true, name: String(st.nowName || '').trim(), uri, kind: spotifyContextKind(uri) };
}

// keySlots is the six keys of the selected speaker as the view draws them, in
// order, an empty key included. here is true on the key that already holds the
// playing context, which the speaker would refuse to store a second time.
export function keySlots(presets, uri) {
  const list = Array.isArray(presets) ? presets : [];
  const out = [];
  for (let slot = 1; slot <= 6; slot++) {
    const p = list.find((x) => x && x.slot === slot) || null;
    const name = p ? String(p.name || '').trim() : '';
    out.push({
      slot,
      name,
      empty: !p || !name,
      spotify: !!p && p.type === 'spotify',
      here: !!uri && !!p && p.type === 'spotify' && p.uri === uri,
    });
  }
  return out;
}

// accountRow turns one speaker's /spotify/info answer into its row in the
// accounts list.
//
//   offline  the speaker is offline, it was not asked
//   unknown  no answer: unreachable, or an agent older than the login fields
//   in       logged in; account is the display name, '' when the speaker has
//            not learned the name yet (the id itself never goes on screen)
//   saved    no live session, but a stored login the speaker signs in with
//   none     never picked in the Spotify app, nothing stored
//
// plan is 'premium', 'free' (a key cannot start a playlist then) or '' when the
// speaker could not find out. others are the other accounts the speaker keeps
// a login for, by name.
export function accountRow(box, reply, names) {
  const row = { host: box.host, port: box.port, name: box.name, state: 'unknown', account: '', plan: '', others: [] };
  if (box.offline) return { ...row, state: 'offline' };
  const np = reply || null;
  const canRecall = np && np.canRecall !== null && np.canRecall !== undefined ? !!np.canRecall : undefined;
  const id = np ? String(np.account || '').trim() : '';
  if (!np || (!id && canRecall === undefined)) return row;
  const known = { ...(names || {}), ...(np.accountNames || {}) };
  if (id) {
    row.state = 'in';
    row.account = spotifyAccountName(id, known);
  } else {
    row.state = canRecall ? 'saved' : 'none';
  }
  const product = String(np.product || '').toLowerCase();
  if (product === 'premium') row.plan = 'premium';
  else if (product === 'free' || product === 'open' || np.premiumRequired) row.plan = 'free';
  const others = new Set();
  for (const [k, v] of Object.entries(np.accountNames || {})) {
    const n = String(v || '').trim();
    if (k !== id && n && n !== row.account) others.add(n);
  }
  row.others = [...others].sort((a, b) => a.localeCompare(b));
  return row;
}
