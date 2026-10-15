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

// titleSwapOffered says whether the toggle belongs on screen: a radio station
// the agent proxies is playing, and its live title has an artist and a title
// to swap. Spotify and the library carry real tags and never get it.
export function titleSwapOffered(isProxiedRadio, title) {
  return !!isProxiedRadio && hasTitleSeparator(title);
}
