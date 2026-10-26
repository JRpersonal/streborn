// views/music/search.js: the radio search of the Music view (radio-browser.info
// queries, the filter row, the result rows, playing a station with the
// Bose-compatibility fallbacks).

import {
  boxFetch,
  BrowserOpenURL,
  ClassifyStreamURL,
  isMissingBinding,
  PlayURL,
  RadioClick,
  RadioSearch,
  RadioSearchDetailed,
  RadioStationsByURL,
} from '../../api.js';
import { parsePlayRejection, resolveBoxByRef, resolvePlayTarget, sameBoxIdentity } from '../../groups.js';
import { t } from '../../i18n/index.js';
import {
  COUNTRIES,
  flagFromCC,
  optFlag,
  ORDERS,
  translateCountry,
  translateTags,
} from '../../localization.js';
import { extractHost, logoImgTag, stationLogoChain } from '../../logos.js';
import { playFailedForNoInternet } from '../../nointernet.js';
import {
  addStationGuideFold,
  isStreamURL,
  normalizeDetailedSearch,
  relaxedHintVisible,
  stationBitrateCell,
  syntheticStationForURL,
} from '../../searchflow.js';
import { saveSearchCountry, state } from '../../state.js';
import {
  $,
  confirmWarn,
  escapeAttr,
  escapeHtml,
  formatNumber,
  getBoxLabel,
  searchErrorText,
  showError,
  showToast,
} from '../../utils.js';
import { deps } from './deps.js';
import { isFav, loadFavStore, toggleFav } from './favorites.js';
import { effectivePlayTarget } from './group.js';
import { scheduleLiveBitrate, scheduleLiveTitle } from './live.js';
import { renderNowPlayingBar } from './nowplaying.js';
import { renderPandoraStations } from './pandora.js';
import { openPick } from './picker.js';
import { renderPresets } from './presettiles.js';
import { refreshStatus } from './status.js';
import { loadLanguagesForCountry, renderGenreChips } from './taxonomy.js';

// updateFilterIndicators setzt die has-filter CSS Klasse auf jene Filter
// Dropdowns die einen anderen Wert als "alle" haben. Damit erkennt der
// User sofort wo aktiv gefiltert wird.
export function updateFilterIndicators() {
  const cc = $('searchCountry');
  const lang = $('searchLang');
  if (cc) cc.classList.toggle('has-filter', !!cc.value);
  if (lang) lang.classList.toggle('has-filter', !!lang.value);
}

// doRefilter re-runs the last action (Top or Search) with the new
// filters but keeps the existing query string.
export function doRefilter() {
  state.searchOffset = 0;
  if (state.searchLastMode === 'search' && state.searchLastQuery) {
    doSearch();
  } else {
    doTop();
  }
}

// ---------- Search ----------

const PAGE_SIZE = 30;

async function doSearch() {
  if (!state.currentBox) { showError(t('search.errSelectSpeaker')); return; }
  const q = $('searchQ').value.trim();
  state.searchLastQuery = q;
  state.searchLastMode = q ? 'search' : 'top';
  state.searchOffset = 0;
  if (!q) { return doTop(); }
  // A pasted stream URL is not a name query: look it up in the directory
  // by URL (a known station renders as a normal result, logo and all), or
  // fall back to a single synthetic play-this-URL card.
  if (isStreamURL(q)) { return searchByStreamURL(q); }
  await fetchSearchPage(false);
}

// searchByStreamURL resolves a pasted stream URL. Directory hits render
// through the normal result path; no hits (or an app built against the older
// binding set, or a directory outage) render the synthetic card instead, so
// a pasted URL always ends in something playable and long-press-savable.
async function searchByStreamURL(url) {
  $('searchResults').innerHTML = `<div class="muted">${escapeHtml(t('search.loadingStations'))}</div>`;
  $('loadMoreRow').classList.add('hidden');
  state.searchRelaxed = false;
  let list = [];
  try {
    list = await RadioStationsByURL(url) || [];
  } catch {
    list = [];
  }
  if (list.length === 0) {
    // Nothing in the directory: before offering the play-this-URL card, ask
    // the backend what the URL actually serves. A station HOMEPAGE answers
    // 200 and looks fine, but saving it to a preset produces a key that can
    // never play - a field case cost a user three dead hardware keys and two
    // support rounds (2026-08-02). Only an explicit "website" verdict blocks;
    // unknown/unreachable still renders the card, so an offline station or an
    // odd server never stops a URL that actually works.
    let kind = null;
    try { kind = await ClassifyStreamURL(url); } catch { kind = null; }
    if (kind && kind.kind === 'website') {
      $('searchResults').innerHTML =
        `<div class="muted">${escapeHtml(t('search.urlIsWebsite'))}</div>`
        + `<div class="muted" style="margin-top:.5rem">${escapeHtml(t('search.urlIsWebsiteHint'))}</div>`;
      state.searchResults = [];
      return;
    }
    list = [syntheticStationForURL(url)];
  }
  state.searchResults = list;
  renderSearchResults();
}

async function doTop() {
  if (!state.currentBox) { showError(t('search.errSelectSpeaker')); return; }
  state.searchLastMode = 'top';
  state.searchOffset = 0;
  await fetchSearchPage(false);
}

async function loadMore() {
  state.searchOffset += PAGE_SIZE;
  await fetchSearchPage(true);
}

function buildSearchOpts() {
  const isSearch = state.searchLastMode === 'search' && state.searchLastQuery;
  // For order=name we still fetch 4x the page size so the "Bose-compatible
  // only" filter still has enough left after the strip of HTTPS-only stations
  // like laut.fm. Sorting itself is done client-side in fetchSearchPage.
  const ord = state.searchOrder || 'votes';
  const limit = ord === 'name' ? PAGE_SIZE * 4 : PAGE_SIZE;
  // cc empty = all countries. top:true selects the vote-ordered top list (no
  // free-text query). On a free-text NAME search the language filter is dropped:
  // it defaults to the app language, and many stations (small/US ones especially)
  // have no language tag in radio-browser, so a default language filter silently
  // hid a station the user searched for by name (e.g. "Real Talk 93.3", whose
  // radio-browser entry has an empty language field). Language still scopes the
  // browse/top list, where it aids discovery; a name lookup should find the
  // station regardless of how radio-browser tagged its language. Country is
  // already opt-in (never auto-defaulted), so it stays applied.
  return {
    q: isSearch ? state.searchLastQuery : '',
    cc: state.searchCountry || '',
    lang: isSearch ? '' : (state.searchLang || ''),
    tag: state.searchTag || '',
    order: ord,
    limit: limit,
    offset: state.searchOffset,
    onlyok: !!state.searchOnlyOK,
    top: !isSearch,
  };
}

async function fetchSearchPage(append) {
  if (!append) {
    $('searchResults').innerHTML = `<div class="muted">${escapeHtml(t('search.loadingStations'))}</div>`;
    $('loadMoreRow').classList.add('hidden');
  }
  try {
    // Query radio-browser DIRECTLY from the app (reliable internet, real CPU)
    // instead of routing through the box agent — the box only ever needs the
    // final stream URL. This is the app-first direction and it removes the box
    // as a point of failure for search (the HTTP 502s in #121). The
    // radiobrowser client does its own multi-mirror failover, so no per-call
    // retry is needed here.
    //
    // Prefer the detailed search: it also says whether the backend had to
    // relax the quality filters to find anything, which drives the
    // "showing unverified results too" hint. An app built against the older
    // binding set falls back to the plain search (no hint, same results);
    // a real search failure is NOT swallowed by the fallback and surfaces
    // through the catch below like before.
    let page;
    let relaxed = false;
    try {
      const detailed = normalizeDetailedSearch(await RadioSearchDetailed(buildSearchOpts()));
      page = detailed.stations;
      relaxed = detailed.relaxed;
    } catch (err) {
      if (!isMissingBinding(err)) throw err;
      page = await RadioSearch(buildSearchOpts()) || [];
    }
    // Relaxed is per result set: a fresh search resets it (and the dismiss),
    // a "load more" keeps the hint once any page needed relaxing.
    state.searchRelaxed = append ? (state.searchRelaxed || relaxed) : relaxed;
    if (!append) state.searchRelaxedDismissed = false;
    if (append) {
      state.searchResults = state.searchResults.concat(page);
    } else {
      state.searchResults = page;
    }
    // Dedup by UUID (paginate + local sort can produce duplicates).
    const seen = new Set();
    state.searchResults = state.searchResults.filter(s => {
      const id = s.stationuuid || (s.name + '|' + s.url);
      if (seen.has(id)) return false;
      seen.add(id);
      return true;
    });
    // Local sort. The server always returns order=votes so that the
    // set of stations stays consistent across all sort options.
    const ord = state.searchOrder || 'votes';
    state.searchResults.sort((a, b) => {
      switch (ord) {
        case 'name': {
          const ca = cleanForSort(a.name);
          const cb = cleanForSort(b.name);
          return ca.localeCompare(cb, 'de', { sensitivity: 'base' });
        }
        case 'clickcount':
          return (b.clickcount || 0) - (a.clickcount || 0);
        case 'clicktrend':
          return (b.clicktrend || 0) - (a.clicktrend || 0);
        case 'bitrate':
          return (b.bitrate || 0) - (a.bitrate || 0);
        case 'votes':
        default:
          return (b.votes || 0) - (a.votes || 0);
      }
    });
    renderSearchResults();
    $('loadMoreRow').classList.toggle('hidden', page.length < PAGE_SIZE);
  } catch (e) {
    $('searchResults').innerHTML = `<div class="muted">${escapeHtml(searchErrorText(e))}</div>`;
    $('loadMoreRow').classList.add('hidden');
  }
}

// cleanForSort strips leading non-alphanumeric characters (tab,
// space, dash, dot, asterisk, ...) so that "  ABC" and "ABC" sort
// alike. Robust against webview versions without Unicode property
// escapes: matches the classic ASCII range plus selected extended
// blocks (Latin diacritics, Cyrillic, etc.).
function cleanForSort(name) {
  const raw = (name || '').toString();
  // Strip leading non-alphanumeric characters. Accept A-Z, 0-9 and
  // selected Unicode ranges (German diacritics, Cyrillic, etc.).
  const stripped = raw.replace(/^[^A-Za-z0-9À-ɏͰ-ӿ]+/, '');
  // If nothing remains after the strip (the name was symbols only),
  // fall back to the raw name. Otherwise empty-string stations would
  // all clump together at the top.
  return (stripped || raw).toLowerCase().trim();
}

// isBoseCompatible estimates whether the speaker can reliably play the stream.
// Since stick-agent build 0132 every stream goes through /stream/raw (TLS is no
// longer a concern), and since v0.7.21 STR converts HLS playlists on the fly, so
// HLS stations play too. So:
//   - HLS streams (radio-browser hls=1, or an .m3u8 URL) play via STR's HLS
//     conversion, regardless of the segment codec
//   - MP3 / AAC / AAC+ / AACP / MPEG the box decodes directly
//   - Ogg / Opus / FLAC neither the box nor the proxy can decode
//   - an unknown codec ("UNKNOWN" or empty) is let through, the box tries it
// radio-browser reports the BBC HLS stations (Radio 2/4, which play since
// v0.7.21) as codec "UNKNOWN" with hls=1, and the old check dropped both: it
// only let an EMPTY codec through and knew nothing of HLS, so it treated the
// literal "UNKNOWN" string as incompatible and hid every now-playable HLS
// station when the filter was on (#124).
function isBoseCompatible(s) {
  const url = String(s.url_resolved || s.url || '');
  if (s.hls === 1 || s.hls === '1' || /\.m3u8(\?|#|$)/i.test(url)) return true;
  const codec = String(s.codec || '').toUpperCase();
  if (!codec || codec === 'UNKNOWN') return true; // let the speaker try
  return codec === 'MP3' || codec === 'AAC' || codec === 'AAC+' ||
    codec === 'AACP' || codec === 'MPEG';
}

// streamErrorMessage maps a stream-status reason to a clear, human, localized
// message. The raw HTTP code (403/503) means nothing to a user; the reason
// class does. Falls back to a generic "unreachable" line for unknown reasons.
function streamErrorMessage(reason) {
  switch (reason) {
    case 'blocked':     return t('search.streamBlocked');
    case 'gone':        return t('search.streamGone');
    case 'unavailable': return t('search.streamUnavailable');
    case 'hls':         return t('search.streamHls');
    case 'offline':     return t('search.streamOffline');
    default:            return t('search.streamUnreachable');
  }
}

// openBoxWifiSetup takes the user to a speaker's Wi-Fi setup: it makes the box
// the settings target (keeping the tabs in sync), switches to the settings view,
// and expands + scrolls to the WLAN switch section. Used from the "speaker has
// no internet" prompt so a box that landed on a dead Wi-Fi can be re-provisioned
// in one tap instead of a manual factory reset (#375).
function openBoxWifiSetup(box) {
  if (!box) return;
  deps.speakerPickedInTab(box);
  deps.switchView('settings');
  setTimeout(() => {
    const toggle = $('wlanSwitchToggle');
    if (!toggle) return;
    const form = $('wlanSwitchForm');
    if (form && form.classList.contains('hidden')) toggle.click();
    toggle.scrollIntoView({ behavior: 'smooth', block: 'center' });
  }, 250);
}

// pollStreamFailure asks the agent whether the stream the box just started has
// failed upstream. Radio failures are asynchronous: the box accepts the UPnP
// URL instantly, then the 403/503 only surfaces when it pulls the bytes. We poll
// /api/stream-status for a few seconds; the moment a fresh failure for OUR url
// appears we return it, otherwise we assume the station is playing and return
// null. Best-effort: any fetch error just ends the poll (assume playing).
async function pollStreamFailure(box, url, windowMs = 6000) {
  const deadline = Date.now() + windowMs;
  while (Date.now() < deadline) {
    await new Promise(r => setTimeout(r, 800));
    if (state.nowLocation !== url) return null; // user moved on; stop watching
    let data;
    try {
      const r = await boxFetch(box, '/api/stream-status', {}, 4000);
      if (!r.ok) continue;
      data = await r.json();
    } catch { return null; }
    if (data && data.error && data.url === url) return data;
  }
  return null;
}

// findAlternativeStation looks for ANOTHER radio-browser entry of the same
// station than the ones already tried. Stations are frequently listed several
// times (different mirrors/CDNs); when one URL is geo-blocked or down, a sibling
// entry usually plays. We match by name (exact, then loose) and skip any URL on
// a host we already failed on, preferring entries radio-browser last checked OK.
async function findAlternativeStation(orig, triedHosts) {
  const wanted = (orig.name || '').toLowerCase().trim();
  if (!wanted) return null;
  let list;
  try {
    list = await RadioSearch({ q: orig.name, limit: 20, order: 'votes', top: false }) || [];
  } catch { return null; }
  const candidates = list.filter(s => {
    const u = s.url_resolved || s.url;
    if (!u) return false;
    const h = extractHost(u);
    if (!h || triedHosts.has(h)) return false;
    const n = (s.name || '').toLowerCase().trim();
    return n === wanted || n.includes(wanted) || wanted.includes(n);
  });
  if (candidates.length === 0) return null;
  // Prefer a station radio-browser last checked OK, then by votes (the search
  // already ordered by votes, so a stable partition keeps that secondary order).
  candidates.sort((a, b) => (b.lastcheckok ? 1 : 0) - (a.lastcheckok ? 1 : 0));
  return candidates[0];
}

// preferMp3SiblingForBox returns an MP3-coded sibling entry (same station
// name) when the chosen entry is AAC-coded and the given, already-loaded list
// offers one (#252). The speaker's AAC decoding is the fragile path (HE-AAC
// stations played silence for years under the fixed audio/mpeg label), while
// the same station's MP3 mirror plays rock solid, so for BOX playback the MP3
// sibling wins. No extra network round trip on purpose: with no list or no
// sibling the chosen entry is returned unchanged and now plays with the
// correct audio/aac label instead.
export function preferMp3SiblingForBox(s, list) {
  if (!s || !/aac/i.test(s.codec || '') || !Array.isArray(list)) return s;
  const wanted = (s.name || '').toLowerCase().trim();
  if (!wanted) return s;
  const siblings = list.filter(o => o && o !== s
    && (o.name || '').toLowerCase().trim() === wanted
    && /^mp3$/i.test(o.codec || '')
    && (o.url_resolved || o.url));
  if (siblings.length === 0) return s;
  // Prefer a mirror radio-browser reached on its last check, then the best bitrate.
  siblings.sort((a, b) => ((b.lastcheckok ? 1 : 0) - (a.lastcheckok ? 1 : 0)) || ((b.bitrate || 0) - (a.bitrate || 0)));
  return siblings[0];
}

// playStation plays a radio station and, when its stream fails upstream
// (403 geo-block, 503 down, dead URL), shows a clear reason and automatically
// retries with another radio-browser entry of the SAME station before giving
// up. This turns the most common "every station errors" frustration into a
// usually-silent recovery. Used by every radio play-now button.
export async function playStation(s, options = {}) {
  const isStreamSource = options.streamSource === true;
  // A play aimed at a zone follower must go to its master instead (#70).
  let box = effectivePlayTarget();
  if (!box) return false;
  // The user-facing selection when this play started. Every await below
  // re-checks it: without the guard, a speaker switch during the retry loop
  // kept starting playback on the PREVIOUS speaker and painted its buffering
  // state onto the newly selected one. Identity, not object equality — a
  // background discovery replaces the box object for the same device.
  const startBox = state.currentBox;
  const switched = () => !sameBoxIdentity(state.currentBox, startBox);
  // Box playback prefers an MP3 sibling over an AAC entry of the same station
  // when the already-loaded result list offers one (#252): the speaker's AAC
  // path is the fragile one, the MP3 mirror of the same station is rock solid.
  // No sibling (or no list) plays the chosen entry unchanged; its AAC codec is
  // now labelled correctly, so it works too.
  if (!isStreamSource) s = preferMp3SiblingForBox(s, state.searchResults);
  const tried = new Set();
  let cur = s;
  let retargeted = false;
  for (let attempt = 0; attempt < (isStreamSource ? 1 : 4); attempt++) {
    if (switched()) return false;
    const url = cur.url_resolved || cur.url;
    const host = extractHost(url);
    if (host) tried.add(host);
    const chain = stationLogoChain(cur);
    state.nowPlayState = 'BUFFERING_STATE';
    state.nowLocation = url;
    state.nowName = s.name; // keep the user's chosen station name across retries
    state.nowIcon = chain;
    state.nowBitrate = cur.bitrate || 0;
    // Clear the previous station's track and start the title poll here: a Find
    // stations play has no preset slot, so nothing else started the loop unless
    // one from an earlier preset play happened to be alive (#1190). The loop is
    // self-guarded, so the retry passes calling it again are no-ops.
    state.nowTitle = '';
    scheduleLiveBitrate();
    scheduleLiveTitle();
    state.nowUUID = cur.stationuuid || '';
    renderPresets();

    let fail = null;
    try {
      // 0: a radio station has no length, which is exactly what the field is
      // for. The box then answers total=0 and no bar is drawn, correctly.
      await PlayURL(box.host, box.port, url, s.name, chain, cur.stationuuid || '', '', s.homepage || '', cur.codec || '', 0);
      // Remember the station the APP itself just started. A long-press save
      // must prefer this over the box-reported now-playing: on a speaker that
      // was asleep, the agent's wake resume can race the play and briefly put
      // the PREVIOUS preset back on, and saving from the box report then
      // copied the OLD station onto the key (#252). Cleared by any other play
      // the app issues (preset recall etc.), so a true hardware-key press
      // still saves via the box report.
      state.lastLibraryPlay = null;
      state.lastAppPlay = {
        url, name: s.name || '', icon: chain, bitrate: cur.bitrate || 0,
        uuid: cur.stationuuid || '', homepage: s.homepage || '',
        codec: cur.codec || '', at: Date.now(),
      };
      // Register the play with radio-browser, but ONLY for a real station UUID.
      // Recently-played cards reuse the stream URL as their identity (no UUID), so
      // a plain `if (stationuuid)` fired RadioClick with a URL, which 404s. Guard
      // on the UUID shape, and use .catch() (not try/catch) since the rejection is
      // async: an unhandled 404 promise surfaced as an error toast when playing a
      // recent radio card even though playback itself was fine.
      if (/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(cur.stationuuid || '')) {
        RadioClick(cur.stationuuid).catch(() => {});
      }
      if (isStreamSource) renderNowPlayingBar();
      // Refresh the now-playing bar promptly on the happy path; the upstream
      // verdict (success vs 403/503) arrives asynchronously, so poll for it.
      setTimeout(refreshStatus, 1200);
      fail = isStreamSource ? null : await pollStreamFailure(box, url);
    } catch (err) {
      // Newer agents reject a play sent to a grouped follower with a
      // structured 409 ({"error":"box-grouped","master":...}): retarget the
      // play ONCE to the group master instead of cycling through alternative
      // sources. Older agents send a raw SOAP string, which falls through to
      // the unreachable path below exactly as before.
      const rej = parsePlayRejection(err);
      if (rej.grouped && !retargeted) {
        const mb = resolveBoxByRef(rej.master, state.boxes)
          || resolvePlayTarget(box, state.zoneLive, state.boxes);
        if (mb && mb.host !== box.host) {
          retargeted = true;
          box = mb;
          attempt--; // the retarget must not burn an alternative-source attempt
          continue;
        }
      }
      // A synchronous failure (box refused the URI) reads as unreachable.
      fail = { reason: 'unreachable', status: 0 };
    }
    // The user may have switched speakers during the play/verdict round trip:
    // stop here so the failure handling below cannot repaint the new
    // speaker's UI or start an alternative stream on the old one.
    if (switched()) return false;
    if (!fail) return true; // playing fine

    if (isStreamSource) {
      state.nowPlayState = '';
      state.nowLocation = '';
      state.lastAppPlay = null;
      renderPresets();
      showToast(streamErrorMessage(fail.reason));
      return false;
    }

    if (playFailedForNoInternet(fail, box)) {
      // The speaker itself has no internet: every station will fail the same
      // way, so do not cycle through alternatives. Say so plainly. The app has
      // just reached the speaker over the LAN, so its Wi-Fi works and the
      // place to look is the router (2026-10-14: two hours of rebooting five
      // speakers a router had cut off the internet). The Wi-Fi setup stays
      // offered for a speaker that joined a network with no internet (#375).
      state.nowPlayState = '';
      state.nowLocation = '';
      state.lastAppPlay = null;
      renderPresets();
      const fix = await confirmWarn(
        t('speaker.noInternetTitle'),
        `<b>${escapeHtml(getBoxLabel(box))}</b>: ${escapeHtml(t('speaker.noInternetHint'))}`,
        { icon: null, confirmLabel: t('search.streamOfflineFix'), confirmClass: 'btn btn-primary' }
      );
      if (fix) openBoxWifiSetup(box);
      return false;
    }

    const alt = await findAlternativeStation(s, tried);
    if (switched()) return false; // speaker switched during the search: stop quietly
    if (!alt) {
      state.nowPlayState = '';
      state.nowLocation = '';
      state.lastAppPlay = null; // never long-press-save a station that failed to play
      renderPresets();
      showToast(streamErrorMessage(fail.reason) + ' ' + t('search.allSourcesFailed'));
      return false;
    }
    showToast(t('search.tryingAlternative', { name: s.name || '' }));
    cur = alt;
  }
  // Exhausted the retry budget without a working source.
  state.nowPlayState = '';
  state.nowLocation = '';
  state.lastAppPlay = null; // never long-press-save a station that failed to play
  renderPresets();
  showToast(t('search.allSourcesFailed'));
  return false;
}

export function renderSearchResults() {
  // The Pandora list lives in the same panel but is not a directory result:
  // a filter change while it shows must not repaint radio stations over it.
  if (state.searchLastMode === 'pandora') { renderPandoraStations(); return; }
  const res = $('searchResults');
  // Optional client-side Bose compatibility filter: drop HTTPS
  // streams and exotic codecs so the user does not hit 502 errors
  // on play.
  const totalRaw = (state.searchResults || []).length;
  let list = state.searchResults || [];
  if (state.searchOnlyBose) {
    list = list.filter(isBoseCompatible);
  }
  // Minimum-bitrate filter. Stations with no reported bitrate (very
  // common on radio-browser) are kept, not hidden, so a quality filter
  // does not wipe out most results; only stations with a known bitrate
  // below the threshold are dropped.
  const minBr = state.searchMinBitrate || 0;
  if (minBr) {
    list = list.filter(s => !s.bitrate || s.bitrate >= minBr);
  }
  // Update the counter row. radio-browser does not return a grand
  // total on a filtered search, so we show "X shown" plus a hint
  // that more can arrive via "load more".
  const cnt = $('searchCount');
  if (cnt) {
    if (list.length === 0) {
      cnt.classList.add('hidden');
    } else {
      const moreHint = totalRaw >= PAGE_SIZE ? ' ' + t('search.moreHint') : '';
      const filterHint = state.searchOnlyBose && list.length < totalRaw
        ? ' ' + t('search.filterHiddenCount', { n: totalRaw - list.length })
        : '';
      cnt.innerHTML = t('search.shownCount', { n: `<b>${list.length}</b>` }) + filterHint + moreHint;
      cnt.classList.remove('hidden');
    }
  }
  // The "add a missing station" guide above the list: unfolded by the renderer
  // when the directory answered a name search with nothing, folded back when a
  // later search does return stations. A guide the user unfolded themselves is
  // left alone, so the flag records who opened it.
  const guide = $('addStationBox');
  const fold = addStationGuideFold(state.addStationGuideAuto, state.searchLastMode, totalRaw);
  if (guide && fold.open !== null) guide.open = fold.open;
  state.addStationGuideAuto = fold.auto;
  // Small dismissible hint above the results when the backend had to relax
  // the quality filters: entries may be unverified, and the user should know
  // why. Rendered inside #searchResults so no markup outside src/ changes.
  let hintHtml = '';
  if (relaxedHintVisible(state.searchRelaxed, state.searchRelaxedDismissed, state.searchLastMode)) {
    hintHtml = '<div class="search-relaxed-hint" id="relaxedHint">'
      + '<span>' + escapeHtml(t('search.relaxedHint')) + '</span>'
      + `<button class="search-relaxed-dismiss" id="relaxedHintDismiss" title="${escapeAttr(t('search.relaxedHintDismiss'))}">&times;</button>`
      + '</div>';
  }
  const wireRelaxedDismiss = () => {
    const d = $('relaxedHintDismiss');
    if (!d) return;
    d.onclick = () => {
      state.searchRelaxedDismissed = true;
      const h = $('relaxedHint');
      if (h) h.remove();
    };
  };
  if (list.length === 0) {
    const msg = state.searchLastMode === 'favorites'
      ? t('search.favEmpty')
      : state.searchOnlyBose && (state.searchResults || []).length > 0
        ? t('search.noBoseStations')
        : t('search.noStationsFound');
    const html = '<div class="muted">' + escapeHtml(msg) + '</div>';
    res.innerHTML = hintHtml + html;
    wireRelaxedDismiss();
    return;
  }
  res.innerHTML = hintHtml + list.map((s, i) => {
    const flag = flagFromCC(s.countrycode);
    const okClass = s.lastcheckok ? 'ok' : 'bad';
    const webUrl = (typeof s.homepage === 'string' && /^https?:\/\//i.test(s.homepage)) ? s.homepage : '';
    const okTitle = s.lastcheckok ? t('search.checkOk') : t('search.checkBad');
    let trend = '';
    if (s.clicktrend > 0) trend = `<span class="result-trend" title="${escapeAttr(t('search.trendUp', { n: s.clicktrend }))}">&#9650;</span>`;
    else if (s.clicktrend < 0) trend = `<span class="result-trend up-down" title="${escapeAttr(t('search.trendDown', { n: s.clicktrend }))}">&#9660;</span>`;

    const countryDe = translateCountry(s.country);
    const tagChips = translateTags(s.tags).slice(0, 4).map(tag => `<span class="tag-pill">${escapeHtml(tag)}</span>`).join('');

    const metaBits = [];
    if (s.synthetic) {
      // The synthetic play-this-URL card: no directory metadata exists, so
      // the meta line carries the localized card label instead.
      metaBits.push(escapeHtml(t('search.playUrlCard')));
    } else {
      if (countryDe) metaBits.push(escapeHtml(countryDe));
      // No known bitrate, no cell: a "- kbit/s" placeholder on every such row
      // read as a broken stream (discussion #1521).
      const br = stationBitrateCell(s);
      if (br) metaBits.push(br);
      if (s.votes)   metaBits.push(t('search.votes', { n: formatNumber(s.votes) }));
    }

    // The online dot reflects radio-browser's last check; a synthetic card
    // was never checked, so it gets no dot rather than a false "broken" red.
    const okDot = s.synthetic ? '' : `<span class="result-online-dot ${okClass}" title="${escapeAttr(okTitle)}"></span>`;

    const logo = `
      <div class="result-logo">
        ${logoImgTag(s, 'fav')}
        ${flag ? `<span class="fav-flag" title="${escapeAttr(s.country || '')}">${flag}</span>` : ''}
      </div>`;
    return `
      <div class="result-row" data-i="${i}">
        ${logo}
        <div class="result-text">
          <div class="result-name">
            ${okDot}
            <span class="result-name-text">${escapeHtml(s.name || t('search.unnamed'))}</span>
            ${trend}
          </div>
          <div class="result-meta">${metaBits.join(' &middot; ')}${webUrl ? ` &middot; <a href="#" class="result-site" data-i="${i}" title="${escapeAttr(t('search.openWebsite'))}">${escapeHtml(t('footer.website'))}</a>` : ''}</div>
          ${tagChips ? `<div class="result-tag-chips">${tagChips}</div>` : ''}
        </div>
        <div class="result-actions">
          <button class="btn btn-mini play-now" data-i="${i}" title="${escapeAttr(t('search.playNow'))}">&#9205;</button>
          <button class="btn btn-mini pick" data-i="${i}" title="${escapeAttr(t('search.assignToKey'))}">&#10133;</button>
          <button class="btn btn-mini fav-toggle${isFav(s) ? ' is-fav' : ''}" data-i="${i}" title="${escapeAttr(isFav(s) ? t('search.removeFav') : t('search.addFav'))}">${isFav(s) ? '&#9733;' : '&#9734;'}</button>
        </div>
      </div>
    `;
  }).join('');
  wireRelaxedDismiss();
  res.querySelectorAll('.play-now').forEach(btn => {
    btn.onclick = async (e) => {
      e.stopPropagation();
      const s = list[parseInt(btn.dataset.i, 10)];
      // playStation handles the upstream-failure case (403/503/dead URL): it
      // shows a clear reason and auto-retries another radio-browser entry of the
      // same station before giving up, so a single blocked mirror no longer
      // looks like "every station errors".
      await playStation(s);
    };
  });
  res.querySelectorAll('.pick').forEach(btn => {
    btn.onclick = (e) => { e.stopPropagation(); openPick(list[parseInt(btn.dataset.i, 10)]); };
  });
  res.querySelectorAll('.fav-toggle').forEach(btn => {
    btn.onclick = (e) => {
      e.stopPropagation();
      const s = list[parseInt(btn.dataset.i, 10)];
      const nowFav = toggleFav(s);
      if (state.searchLastMode === 'favorites' && !nowFav) {
        // Unstarred while viewing the favorites list: drop it from the view.
        state.searchResults = loadFavStore();
        renderSearchResults();
        return;
      }
      btn.classList.toggle('is-fav', nowFav);
      btn.innerHTML = nowFav ? '&#9733;' : '&#9734;';
      btn.title = nowFav ? t('search.removeFav') : t('search.addFav');
    };
  });
  res.querySelectorAll('.result-site').forEach(link => {
    link.onclick = (e) => {
      e.preventDefault();
      e.stopPropagation();
      const s = list[parseInt(link.dataset.i, 10)];
      if (s && typeof s.homepage === 'string' && /^https?:\/\//i.test(s.homepage)) BrowserOpenURL(s.homepage);
    };
  });
}

// mountSearch attaches the handlers that used to be wired at module scope in
// main.js, once #view-box exists (called from mountMusicView).
export function mountSearch() {
  // Filter Dropdowns befuellen
  $('searchCountry').innerHTML = COUNTRIES.map(c =>
    `<option value="${c.cc}">${optFlag(c.cc)}${escapeHtml(c.name)}</option>`
  ).join('');
  $('searchOrder').innerHTML = ORDERS.map(o =>
    `<option value="${o.v}">${escapeHtml(o.label)}</option>`
  ).join('');
  $('searchCountry').value = state.searchCountry;
  $('searchOrder').value = state.searchOrder;
  $('searchOnlyOK').checked = state.searchOnlyOK;
  $('searchOnlyBose').checked = state.searchOnlyBose;

  $('searchBtn').onclick = () => doSearch();
  $('topBtn').onclick = () => doTop();

  // Pointer for the users who want a station that radio-browser.info does not
  // list yet: they can add it there and it shows up here after a while. The
  // paragraph used to sit below the load-more button as one long link and nobody
  // found it (discussion #619, three missing stations), so it is now a one-line
  // question above the results that unfolds into the full guide.
  { const ab = $('addStationOpenBtn'); if (ab) ab.onclick = () => { try { BrowserOpenURL('https://www.radio-browser.info/'); } catch {} }; }
  $('loadMoreBtn').onclick = () => loadMore();
  $('searchQ').onkeydown = (e) => { if (e.key === 'Enter') doSearch(); };
  $('searchQ').oninput = () => {
    $('searchQ').classList.toggle('has-query', !!$('searchQ').value.trim());
  };
  $('searchCountry').onchange = () => {
    state.searchCountry = $('searchCountry').value;
    // A country change resets the language to "all". Otherwise a
    // country/language mismatch filter would empty the results.
    state.searchLang = '';
    const ls = $('searchLang');
    if (ls) ls.value = '';
    updateFilterIndicators();
    saveSearchCountry(state.searchCountry);
    // Reload the language list scoped to the selected country so the
    // counts reflect stations in THIS country, not the global pool.
    state.languages = [];
    loadLanguagesForCountry();
    // Country-boost pills depend on the selected country — re-render
    // so the highlighted row matches. Collapse the "More" expansion
    // because the previous tail may no longer apply.
    state.showMoreGenres = false;
    renderGenreChips();
    doRefilter();
  };

  $('searchLang').onchange    = () => {
    state.searchLang = $('searchLang').value;
    updateFilterIndicators();
    doRefilter();
  };

  updateFilterIndicators();
  $('searchOrder').onchange   = () => { state.searchOrder   = $('searchOrder').value;   doRefilter(); };
  $('searchOnlyOK').onchange  = () => { state.searchOnlyOK  = $('searchOnlyOK').checked; doRefilter(); };
  $('searchOnlyBose').onchange = () => { state.searchOnlyBose = $('searchOnlyBose').checked; renderSearchResults(); };
  $('searchBitrate').onchange = () => { state.searchMinBitrate = parseInt($('searchBitrate').value, 10) || 0; renderSearchResults(); };
}
