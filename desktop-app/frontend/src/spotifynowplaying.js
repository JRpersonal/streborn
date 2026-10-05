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

// spotifySongText is the "Artist - Track" part of the line, or '' when the
// snapshot has no song yet (the line then shows the heading alone).
export function spotifySongText(snapshot) {
  if (!snapshot || !snapshot.track) return '';
  return snapshot.artist ? `${snapshot.artist} - ${snapshot.track}` : snapshot.track;
}
