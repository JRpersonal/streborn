// libraryfav.js: music-library favourites (#1605).
//
// The star on a Recently-played card is shown for library cards too (an album,
// a playlist or a track from the media server), but the favourite only ever
// carried the radio-station shape. Played back from Favourites it went the
// radio way: the speaker filed it under Radio with no cover, a folder played
// its first track only (#978) and a single file could loop in the stream
// proxy (#817). These helpers keep the library identity on the favourite and
// turn it back into the card shape the Recently-played replay understands.
// Self-contained (no imports), so the tests load it directly.

// isFolderKey reports whether a card key names a library folder: the app's
// "queue:" key, or the phone page's older "folder:<server>/<container>".
export function isFolderKey(key) {
  return /^(queue:|folder:[^:/]+\/)/.test(String(key || ''));
}

// isLibraryFav reports whether a favourite is a library item. A favourite
// starred before the mark existed is recognised only when it is a folder: its
// key says so. An old single-track favourite cannot be told from a station by
// its URL alone and keeps playing the radio way.
export function isLibraryFav(s) {
  return !!s && (s.source === 'upnp' || isFolderKey(s.stationuuid));
}

// libraryFavFields is what a Recently-played card adds to the favourite it is
// starred as: nothing for radio, the library mark plus MIME and length for a
// library card. The card key already rides along as the station UUID.
export function libraryFavFields(c) {
  if (!c || c.source !== 'upnp') return {};
  const out = { source: 'upnp' };
  if (c.mime) out.mime = c.mime;
  if (c.durationSec > 0) out.durationSec = Math.round(c.durationSec);
  return out;
}

// libraryFavCard turns a library favourite back into the Recently-played card
// shape replayLibraryCard plays.
export function libraryFavCard(s) {
  return {
    source: 'upnp',
    cardKey: s.stationuuid || '',
    name: s.name || '',
    art: s.favicon || '',
    url: s.url_resolved || s.url || '',
    mime: s.mime || '',
    durationSec: s.durationSec || 0,
  };
}
