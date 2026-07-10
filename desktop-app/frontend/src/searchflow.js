// searchflow.js — pure decision helpers for the radio-search flow.
//
// No DOM access and no api.js import on purpose: main.js owns rendering and
// backend calls, this module owns the decisions, and vitest covers them in a
// plain node environment (same pattern as utils.js/groups.js).

// isStreamURL says whether the search input is a pasted stream URL rather
// than a station-name query. Only absolute http(s) URLs count; anything else
// stays a name search (a user typing "radio 91.8" must not be URL-routed).
export function isStreamURL(q) {
  return /^https?:\/\//i.test(String(q == null ? '' : q).trim());
}

// hostnameOf extracts the host part of a URL for the synthetic card's
// display name. Falls back to a regex when the URL constructor rejects the
// input (playable but non-conforming URLs exist in the wild), and to the raw
// input when even that fails, so the card never renders nameless.
export function hostnameOf(url) {
  const raw = String(url == null ? '' : url).trim();
  try {
    const h = new URL(raw).hostname;
    if (h) return h;
  } catch {
    // fall through to the regex
  }
  const m = /^https?:\/\/([^/:?#]+)/i.exec(raw);
  return (m && m[1]) || raw;
}

// syntheticStationForURL builds the minimal station object for the
// "Play this stream URL" card shown when the pasted URL is not in the
// radio-browser directory. Shaped like a radio-browser Station where it
// matters: playStation reads url_resolved/url/name/codec/bitrate, the
// long-press preset save tolerates the empty uuid/codec (VoteStation and
// RadioClick are both guarded on a non-empty/valid UUID), and favorites key
// off name|url when there is no UUID.
export function syntheticStationForURL(url) {
  const raw = String(url == null ? '' : url).trim();
  return {
    synthetic: true,
    stationuuid: '',
    name: hostnameOf(raw),
    url: raw,
    url_resolved: raw,
    codec: '',
    bitrate: 0,
    hls: 0,
    tags: '',
    country: '',
    countrycode: '',
    votes: 0,
    homepage: '',
    favicon: '',
    lastcheckok: 0,
  };
}

// normalizeDetailedSearch tolerates a null/partial result from the detailed
// search binding so the caller never touches undefined fields.
export function normalizeDetailedSearch(res) {
  return {
    stations: (res && Array.isArray(res.stations)) ? res.stations : [],
    relaxed: !!(res && res.relaxed),
  };
}

// relaxedHintVisible decides whether the "showing unverified results too"
// hint renders above the results: only when the last fetch actually relaxed
// the quality filters, the user has not dismissed it for this result set,
// and the list on screen came from a fetch (the favorites view reuses the
// same renderer but never relaxes anything).
export function relaxedHintVisible(relaxed, dismissed, mode) {
  return !!relaxed && !dismissed && (mode === 'search' || mode === 'top');
}
