// pandorafav.js: a Pandora station as a favourite (#1663).
//
// The station search lets you star a station; the Pandora list in the same
// panel had no star, so a Pandora station could not sit in Favourites next to
// the other stations. A Pandora station has no stream address: the speaker's
// own Pandora client plays it from the reference the speaker listed. The
// favourite keeps that reference and the item type, marked source "pandora",
// under the key its Recently-played card has ("pandora:<location>"), and plays
// back through the same call as the Pandora list. Self-contained (no imports),
// so the tests load it directly.

// isPandoraFav reports whether a favourite is a Pandora station.
export function isPandoraFav(s) {
  return !!s && s.source === 'pandora' && !!String(s.location || '').trim();
}

// pandoraFavKey is the favourite's key for a station reference.
export function pandoraFavKey(location) {
  return 'pandora:' + String(location || '').trim();
}

// pandoraFavFromRow turns a row of the Pandora list (usservices.js
// pandoraStationRows) into the favourite it is starred as.
export function pandoraFavFromRow(row) {
  const location = String((row && row.location) || '').trim();
  return {
    stationuuid: pandoraFavKey(location),
    name: (row && row.name) || location,
    url: '',
    url_resolved: '',
    favicon: (row && row.art) || '',
    source: 'pandora',
    location,
    itemType: (row && row.type) || 'stationurl',
  };
}

// pandoraRowFromFav turns a Pandora favourite back into the row shape the
// Pandora list plays and saves to a key from.
export function pandoraRowFromFav(s) {
  return {
    name: (s && s.name) || '',
    location: String((s && s.location) || '').trim(),
    type: (s && s.itemType) || 'stationurl',
    art: (s && s.favicon) || '',
    presetable: true,
  };
}
