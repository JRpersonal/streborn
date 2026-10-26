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
// long-press preset save tolerates the empty uuid/codec (RadioVote and
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

// addStationGuideFold decides the fold state of the "add a missing station"
// guide that sits above the result list: { open: true } unfolds it, false folds
// it, null leaves it exactly as it is, and auto records whether it is open
// because of this decision or because the user clicked the line.
//
// It unfolds only when a name search came back with nothing at all from the
// directory: then the station really is absent from radio-browser.info and
// adding it there is the answer (discussion #619, three stations missing until
// they were entered by hand). When the directory did return stations and only
// the local Bose/bitrate filters emptied the list, the station exists and the
// guide would be the wrong advice, so fetchedCount counts what the directory
// sent, not what survived the filters. A guide unfolded by the app folds again
// on the next search that finds something, so it cannot become a nag; one the
// user opened stays open until they close it.
export function addStationGuideFold(autoOpened, mode, fetchedCount) {
  if (mode === 'search' && fetchedCount === 0) return { open: true, auto: true };
  if (autoOpened) return { open: false, auto: false };
  return { open: null, auto: false };
}

// relaxedHintVisible decides whether the "showing unverified results too"
// hint renders above the results: only when the last fetch actually relaxed
// the quality filters, the user has not dismissed it for this result set,
// and the list on screen came from a fetch (the favorites view reuses the
// same renderer but never relaxes anything).
export function relaxedHintVisible(relaxed, dismissed, mode) {
  return !!relaxed && !dismissed && (mode === 'search' || mode === 'top');
}

// stationBitrateCell is the bitrate cell of a station row's meta line, or ''
// for no cell at all. Most radio-browser stations, and every library item or
// own station in Favorites, report no bitrate; the "- kbit/s" placeholder that
// used to stand in for it read as a broken stream on every such row
// (discussion #1521), so an unknown rate now simply leaves the cell out.
export function stationBitrateCell(s) {
  const br = Number(s && s.bitrate);
  return br > 0 ? `${br} kbit/s` : '';
}

// stationCheckDot is the reachability dot of a station row: 'ok' (green),
// 'bad' (red) or '' for no dot. Only a radio-browser station carries a check
// result (lastcheckok 0/1); a library playlist or album starred from Recently
// played, an own station and the synthetic play-this-URL card were never
// checked by anyone, and a red "not reachable" dot on them was a false alarm
// (discussion #1521). A directory entry radio-browser has not checked yet
// (lastchecktime empty or all zeros) has no result either.
export function stationCheckDot(s) {
  if (!s || s.synthetic || !s.stationuuid) return '';
  const ok = s.lastcheckok;
  if (typeof ok !== 'boolean' && ok !== 0 && ok !== 1) return '';
  if (typeof s.lastchecktime === 'string') {
    const t = s.lastchecktime.trim();
    if (t === '' || t.startsWith('0000-00-00')) return '';
  }
  return ok ? 'ok' : 'bad';
}
