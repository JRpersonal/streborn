// titleswap.js: when to offer "artist and title are the wrong way round for
// this station".
//
// Most stations send their live title as "Artist - Title"; some send "Title -
// Artist" (NDR 1 Radio MV: "Sultans of swing - Dire Straits"), and the text
// alone cannot tell which. The agent keeps a per-station flag and turns that
// station's titles round for everything that reads them; the app only offers
// the switch, next to the now-playing line.

// The separators the agent splits a StreamTitle on (internal/webui
// splitStreamTitle). A title without one has nothing to swap.
const SEPARATORS = [' - ', ' – ', ' — ', ' / '];

// hasTitleSeparator says whether a live title reads as two parts.
export function hasTitleSeparator(title) {
  const s = String(title || '').trim();
  return SEPARATORS.some((sep) => {
    const i = s.indexOf(sep);
    return i > 0 && i + sep.length < s.length;
  });
}

const DASHES = [' - ', ' – ', ' — '];

// cutFirst cuts s at the earliest of seps that leaves text on both sides, or
// returns null.
function cutFirst(s, seps) {
  let best = -1, len = 0;
  for (const sep of seps) {
    const i = s.indexOf(sep);
    if (i > 0 && (best < 0 || i < best)) { best = i; len = sep.length; }
  }
  if (best < 0) return null;
  const left = s.slice(0, best).trim(), right = s.slice(best + len).trim();
  return left && right ? [left, right] : null;
}

// splitStreamTitle mirrors the agent's splitStreamTitle (internal/webui): the
// earliest dash makes "Artist - Title"; " / " makes "Title / Artist" (SWR3)
// only when the title has no dash at all, because a slash is also part of
// artist names ("AC/DC", "Axwell / Ingrosso - More Than You Know"). No
// separator: artist empty, the whole text is the track.
export function splitStreamTitle(raw) {
  const s = String(raw || '').trim();
  const dash = cutFirst(s, DASHES);
  if (dash) return { artist: dash[0], track: dash[1] };
  const slash = cutFirst(s, [' / ']);
  if (slash) return { artist: slash[1], track: slash[0] };
  return { artist: '', track: s };
}

// titleSwapOffered says whether the toggle belongs on screen: a radio station
// the agent proxies is playing, and its live title has an artist and a title
// to swap. Spotify and the library carry real tags and never get it.
export function titleSwapOffered(isProxiedRadio, title) {
  return !!isProxiedRadio && hasTitleSeparator(title);
}
