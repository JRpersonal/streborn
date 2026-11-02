// spotifyrefused.js: the short notice that Spotify refuses the speaker's
// account on (STR) (discussion #1671).
//
// Some accounts never play through STR's Spotify engine: free accounts, and
// Premium accounts Spotify moved to its newer DRM. The speaker remembers which
// account it was refused for until that account plays (the agent's
// accountRefused, internal/spotify/accountrefused.go), so the notice stays up
// for as long as it is true instead of flashing past once as a toast while the
// app happened to be open.
//
// An older agent has only audioKeyRefused, its ten-minute flag. That feeds the
// same notice, so there is one message whichever agent answers.

// refusalFromReply turns one /spotify/info reply into what the notice needs,
// or null when there is nothing to say.
export function refusalFromReply(np) {
  if (!np || !(np.accountRefused || np.audioKeyRefused)) return null;
  return { name: np.accountRefused ? String(np.accountRefusedName || '').trim() : '' };
}

// refusalText is the notice itself: with the account's name when the speaker
// knows it, otherwise about "this account".
export function refusalText(ref, t) {
  if (!ref) return '';
  return ref.name ? t('spotify.accountRefusedNamed', { account: ref.name }) : t('spotify.accountRefused');
}

// strSpotifyLocation reports whether the speaker plays from STR's own Spotify
// engine: the engine's stream, or a container location that is not the
// speaker's own Spotify receiver. Same predicate the status poll asks the
// engine under, so the notice shows exactly where its answer comes from.
export function strSpotifyLocation(loc, boxSpotify) {
  return /\/spotify\/stream|\/playback\/container/.test(loc || '') && !boxSpotify;
}

// refusalNoticeFor is the text the now-playing area shows, '' for none: only
// while the selected speaker plays from STR's Spotify, and only from an answer
// that speaker gave.
export function refusalNoticeFor(st, t) {
  const ref = st && st.spotifyRefusal;
  const box = st && st.currentBox;
  if (!ref || !box || ref.host !== box.host || ref.port !== box.port) return '';
  if (!strSpotifyLocation(st.nowLocation, st.nowBoxSpotify)) return '';
  return refusalText(ref, t);
}
