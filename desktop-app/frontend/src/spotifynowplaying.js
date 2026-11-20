// Which /spotify/info reply may paint the now-playing line.
//
// Switching from the Spotify key holding "Tom Sawyer" (Rush) to the one holding
// the playlist "Purpose for Pain", the line read for about ten seconds:
//
//   Playlist: "Purpose for Pain" · Rush - Tom Sawyer
//
// The heading comes from the preset the user just clicked and is set at once.
// The song came from separate track/artist/cover fields that only a background
// /spotify/info poll refreshed, at most every three seconds, and nothing tied
// such a reply to the click: a reply to a request sent before it, or one in
// which the speaker still described the previous song, was painted under the
// new name (discussion #1077).
//
// The rule now: a click starts a new generation and names the context it wants.
// A reply is stored as ONE snapshot, and only when it answers a request of the
// current generation and describes the wanted context. Until then the line
// shows the heading alone.

import { spotifyURIFromContainer } from './utils.js';

// How long a click's expectations hold. Past this the app goes back to trusting
// the speaker's reply as it comes, so an engine that never reports a context
// (it can stay empty, see the preset save in main.js) still gets its song shown.
export const RECALL_EXPECT_MS = 20000;

// normalizeSpotifyContext makes two spellings of the same context compare
// equal: the engine announces a generated playlist through a station wrapper
// (spotify:station:playlist:X) for what the preset stores as spotify:playlist:X.
// Same rule as the agent's normalizeContextURI.
export function normalizeSpotifyContext(uri) {
  const u = String(uri || '').trim();
  if (u.startsWith('spotify:station:') && u.length > 'spotify:station:'.length) {
    return 'spotify:' + u.slice('spotify:station:'.length);
  }
  return u;
}

// songKey identifies a song for the "is this still the old one" check.
function songKey(track, artist) {
  return track ? `${artist || ''}\u0000${track}` : '';
}

// beginRecall is what a Spotify preset click records: the context it wants and
// the song that was showing before, which a reply must not bring back while the
// switch is in flight (an older agent answers with the new context next to the
// old song for several seconds).
export function beginRecall(wantContext, previous, now) {
  return {
    want: normalizeSpotifyContext(wantContext),
    staleKey: previous ? songKey(previous.track, previous.artist) : '',
    until: now + RECALL_EXPECT_MS,
  };
}

// acceptSpotifyReply decides whether a reply may become the snapshot.
//
// sentGen is the generation the request was sent under, currentGen the one now:
// a click in between makes every reply still in flight an answer about the
// previous preset. recall is the record of the latest click, or null.
export function acceptSpotifyReply(reply, { sentGen, currentGen, recall, now }) {
  if (!reply) return false;
  if (sentGen !== currentGen) return false;
  if (!recall || now >= recall.until) return true;
  if (recall.want && normalizeSpotifyContext(reply.context) !== recall.want) return false;
  if (recall.staleKey && songKey(reply.track, reply.artist) === recall.staleKey) return false;
  return true;
}

// recallSettled reports whether an accepted snapshot has fulfilled the click,
// after which the next replies are judged as usual (the same playlist moving to
// its next song must not be held to the click's expectations).
export function recallSettled(snapshot) {
  return !!(snapshot && snapshot.track);
}

// snapshotFromReply keeps the fields of one reply together, so the line can
// never mix a song from one reply with a context from another.
export function snapshotFromReply(reply) {
  return {
    track: (reply && reply.track) || '',
    artist: (reply && reply.artist) || '',
    cover: (reply && reply.cover) || '',
    context: (reply && reply.context) || '',
    account: (reply && reply.account) || '',
  };
}

// Liked Songs as Spotify Connect announces it: one playlist id for EVERY
// account. In the #1983 bundle STR's engine reported it for one household
// member and the speaker's own Spotify client reported the very same id while
// another member played theirs.
const LIKED_SONGS_ALIAS = 'spotify:playlist:37i9dQZF1F5p3rmiWPIYgZ';

// accountBoundSpotifyContext reports whether a context URI names a different
// list for every account although the URI is the same for all of them: Liked
// Songs, as the alias above or as the bare collection. For those the URI alone
// does not say whose list plays. spotify:user:<id>:collection carries its
// owner and is compared like any other URI. Same rule as the agent's
// accountBoundSpotifyURI (internal/webui/spotify_playing_key.go).
export function accountBoundSpotifyContext(uri) {
  const u = normalizeSpotifyContext(uri);
  return u === LIKED_SONGS_ALIAS || u === 'spotify:collection' || u.startsWith('spotify:collection:');
}

// sameKnownSpotifyAccount is true only when both accounts are known and equal.
function sameKnownSpotifyAccount(a, b) {
  const x = String(a || '').trim();
  const y = String(b || '').trim();
  return !!x && x === y;
}

// spotifySlotStillHolds decides whether the remembered Spotify key may stay lit
// while the speaker reports only the generic /spotify/stream.ogg.
//
// The key is remembered so a next/prev on the remote, which can drop the slot
// from the location, does not unlight it. But the Spotify app taking over the
// "(STR)" speaker drops the slot the same way, and the key kept showing "now
// playing" over a song the user had picked in the app (discussion #1077, issue
// 14). The two differ in the context: next/prev stays in the key's playlist, a
// takeover plays something else. An empty context proves nothing (the engine
// can leave it empty), so the key keeps its light then.
//
// Liked Songs has one URI for every account, so there another household member
// taking over with their own Liked Songs keeps the context and only the account
// tells (#1983). The key was lit by its own recall, so it is dropped only on
// proof: both accounts known and different.
export function spotifySlotStillHolds(presetUri, snapshot, presetAccount = '') {
  const ctx = normalizeSpotifyContext(snapshot && snapshot.context);
  if (!ctx || !presetUri) return true;
  if (ctx !== normalizeSpotifyContext(presetUri)) return false;
  if (!accountBoundSpotifyContext(ctx)) return true;
  const playing = String((snapshot && snapshot.account) || '').trim();
  const saved = String(presetAccount || '').trim();
  return !playing || !saved || playing === saved;
}

// spotifySlotForContext names the Spotify key that holds what the engine plays,
// for playback no key started: a playlist started from the Spotify app plays on
// the generic /spotify/stream.ogg, so the per-slot URL that lights a recalled
// key never comes, and a key just saved from it by a hold stayed dark (#1217).
//
// Matched on the context URI only, never on the name: a preset literally named
// "Spotify" must not light up for whatever plays (#1077). The store refuses the
// same playlist on two keys (#836), so more than one match is an older store;
// the lowest key wins then, so the highlight does not hop between renders. An
// empty context proves nothing and names no key.
//
// Liked Songs is the exception to "the URI is enough": every account plays it
// under the same URI, so a key holding it is named only when the account that
// plays is known to be the one the key was saved under (#1983).
export function spotifySlotForContext(presets, snapshot) {
  const ctx = normalizeSpotifyContext(snapshot && snapshot.context);
  if (!ctx) return null;
  const bound = accountBoundSpotifyContext(ctx);
  let best = null;
  for (const p of presets || []) {
    if (!p || p.type !== 'spotify' || !p.uri) continue;
    if (normalizeSpotifyContext(p.uri) !== ctx) continue;
    if (bound && !sameKnownSpotifyAccount(p.account, snapshot && snapshot.account)) continue;
    if (best === null || p.slot < best) best = p.slot;
  }
  return best;
}

// spotifyKeyGone reports whether the key remembered as the playing Spotify key
// no longer exists as a Spotify key: it was emptied, or something else was
// saved on it. An empty key list is no evidence (the keys may not be loaded
// yet), so it never reports true then.
export function spotifyKeyGone(presets, slot) {
  if (slot == null || !presets || !presets.length) return false;
  return !presets.some(p => p && p.slot === slot && p.type === 'spotify');
}

// nativeSpotifySlot names the Spotify key that holds what the speaker's OWN
// Spotify client plays (the "<speaker> (L+R)" entry in the Spotify app, not
// STR's "(STR)" one). That session never goes through STR's engine, so neither
// the per-slot URL nor the engine context can light a key, and a key saved from
// it by a hold stayed dark although it holds exactly what plays (#1077 issue
// 18). The speaker reports the playing context itself, as
// /playback/container/<base64 spotify:...>, and that URI is what the save
// stored on the key, so it is matched on that URI and nothing else. Another
// playlist, another source or STR's own stream (whose location carries no
// context) names no key.
//
// The speaker names no account for that session (sourceAccount is the fixed
// placeholder "SpotifyConnectUserName"), so a Liked Songs key, whose URI is the
// same for every account, is never named from here: it lit up while another
// household member played their own Liked Songs (#1983).
export function nativeSpotifySlot(presets, nowLocation) {
  const loc = nowLocation || '';
  if (/\/spotify\/stream/.test(loc)) return null;
  const uri = spotifyURIFromContainer(loc);
  if (!uri) return null;
  return spotifySlotForContext(presets, { context: uri });
}

// spotifySongText is the "Artist - Track" part of the line, or '' when the
// snapshot has no song yet (the line then shows the heading alone).
export function spotifySongText(snapshot) {
  if (!snapshot || !snapshot.track) return '';
  return snapshot.artist ? `${snapshot.artist} - ${snapshot.track}` : snapshot.track;
}
