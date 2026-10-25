// views/recent.js — the "Recently played" view (#135).
//
// First view extracted out of the monolithic main.js: a self-contained module
// that pulls everything it needs from the shared modules (state, utils, i18n,
// api, logos). The main.js-local helpers it reuses so its RADIO cards behave
// EXACTLY like the radio search rows (play / save preset / favourite) are
// injected via initRecentView, so this file does not import back into main.js
// (which would create a cycle). New views should follow this pattern so main.js
// stops growing.
//
// Cross-source listening history: read each box's /api/recent ring, merge by
// time, and show each station once, at its newest listening (newest card on
// top, tracks of that newest session newest-first). The box keeps one session
// per station itself; the cross-speaker merge and rendering happen here in the
// app (App-First, keep the box light).

import { state } from '../state.js';
import { $, escapeHtml, escapeAttr, showError, showToast, confirmWarn, getBoxLabel, decodeXmlEntities, beginLibraryNowPlaying, friendlyErrorText } from '../utils.js';
import { t, getLocale } from '../i18n/index.js';
import { RecentPlayed, SaveSpotifyPreset, GetPresets, PlaySlot, PlayURL, BrowserOpenURL, ClearRecent, DeleteRecentCard, Status, ReplayFolderCard, PlayPandoraStation, isMissingBinding } from '../api.js';
import { logoImgTag, SPOTIFY_LOGO } from '../logos.js';
import { stereoPairsOf } from '../groups.js';
import { pairDisplayName } from '../stereoNames.js';
import { spotifyAccountName } from '../spotifyaccountlabel.js';
import { splitStreamTitle } from '../titleswap.js';
import { shell2Mounted } from '../shell2/mount.js';
import { groupCardsByDay } from './recentdays.js';
// The preview's look of this view; every rule sits under html.ui2.
import '../shell2/recent.css';

// Injected main.js helpers (see initRecentView). showSlotPicker is the shared
// modal; playStation/openPick/toggleFav/isFav are the exact radio-search-row
// actions, reused so the radio cards are pixel- and behaviour-identical.
// openPandoraPick is the Pandora station row's save-to-key action (#1101).
let deps = {
  showSlotPicker: null,
  playStation: null,
  openPick: null,
  toggleFav: null,
  isFav: null,
  openPandoraPick: null,
  // The now-playing bar and the status poll, so a replayed library track
  // replaces the previous station's song on the line at once instead of on
  // the next slow poll (#1394). Both live in views/music; main.js hands them in.
  renderNowPlayingBar() {},
  refreshStatus() {},
  // The quick check for a favourite removed on a phone (#1339), from
  // views/music/favorites.js. Resolves to whether it took a star away.
  pollFavRemovals: null,
};
export function initRecentView(d) {
  deps = { ...deps, ...d };
}

// recentStrBoxes returns the discovered STR speakers (skip unflashed stock ones).
function recentStrBoxes() {
  return (state.boxes || []).filter((b) => b && b.kind !== 'stock' && b.host);
}

// recentBoxKey is the stable identity used both for the dropdown option values
// and the per-entry _boxKey tag, so a picked speaker matches its history.
function recentBoxKey(b) {
  return b.deviceID || (b.host + ':' + b.port);
}

// recentSelectedBoxes resolves the view scope (#221) to the speakers to read:
// every STR speaker when "All speakers" is chosen, the explicitly picked one
// otherwise, falling back to the app-wide current box (then the first speaker)
// so the view is never empty before the user touches the dropdown.
function recentSelectedBoxes() {
  const all = recentStrBoxes();
  if (state.recentAllBoxes) return all;
  if (state.recentBoxKey) {
    const found = all.find((b) => recentBoxKey(b) === state.recentBoxKey);
    if (found) return [found];
  }
  if (state.currentBox) return [state.currentBox];
  return all.length ? [all[0]] : [];
}

// recentBoxDisplayLabel is the speaker name shown on a played card. When the
// speaker is half of a live firmware stereo pair, the pair's own STR name is
// shown instead of the single box name (#775), so the history reads as one
// speaker exactly like the multi-room view names it. The name is kept app-side
// (stereoNames.js); its first lookup is async, so pairDisplayName repaints the
// list once the name lands. Falls back to the plain box label when the box is
// not paired or the pair has no stored name.
function recentBoxDisplayLabel(b) {
  const dev = String(b.deviceID || '').toUpperCase();
  if (dev) {
    const pair = stereoPairsOf(state.zoneLive || {}).find((p) =>
      (p.members || []).some((m) => String(m.deviceID || '').toUpperCase() === dev));
    if (pair) {
      const nm = pairDisplayName(pair, () => refreshRecentList());
      if (nm) return nm;
    }
  }
  return getBoxLabel(b);
}

// cardStation projects a recent card into the radio-station shape the search-row
// helpers (playStation/openPick/toggleFav/isFav, stationLogoCandidates) expect,
// so a radio card reuses them verbatim. CardKey is the stable favourite identity.
function cardStation(c) {
  // c.art from a real play is the pipe-separated stationLogoChain (the station's
  // own favicon first, then DuckDuckGo derivations). logoImgTag wants a SINGLE
  // favicon, so take the first candidate; passing the whole chain made it fall
  // through to the wrong stream-host favicon. The rest is rederived from hosts.
  const art = (c.art || '').split('|').map((x) => x.trim()).filter(Boolean);
  return {
    stationuuid: c.cardKey,
    name: c.name || '',
    url: c.url || '',
    url_resolved: c.url || '',
    favicon: art[0] || '',
    bitrate: 0,
    homepage: c.homepage || '',
    tags: '',
    country: '',
    countrycode: '',
  };
}

// groupRecentCards turns a newest-first, box-tagged entry list into source
// cards, ONE card per station (cardKey), placed at its newest listening
// (a user wish, 2026-10-19). The agent already keeps a station once per
// speaker; this does the same across speakers for the "All speakers" view, and
// for a speaker whose agent still keeps the older history.
//
// The card belongs to the speaker that played the station last: play, the
// speaker label and the tracks come from that speaker's newest session. Every
// other speaker that has the station is listed in members, so a delete removes
// it from all of them and the card does not reappear from another speaker.
// A session is a run of the same cardKey in ONE speaker's own sequence;
// another speaker's rows interleaved by time do not split it. Tracks stay
// newest-first; an empty-track placeholder row yields a card with no tracks.
// Self-contained (no imports): the tests evaluate it on its own.
export function groupRecentCards(entries) {
  const cards = [];
  const byKey = new Map();
  const boxCur = new Map(); // boxKey -> cardKey of that box's previous (newer) row
  const left = new Set();   // "boxKey\n cardKey" whose newest session is over
  for (const e of entries) {
    if (!e || !e.cardKey) continue;
    const prev = boxCur.get(e._boxKey);
    if (prev !== undefined && prev !== e.cardKey) left.add(e._boxKey + '\n' + prev);
    boxCur.set(e._boxKey, e.cardKey);
    // An older session of a station this speaker played again later: the
    // newer session already stands for it.
    if (left.has(e._boxKey + '\n' + e.cardKey)) continue;

    const card = byKey.get(e.cardKey);
    if (card && card.boxKey === e._boxKey) {
      if (!card.homepage && e.homepage) card.homepage = e.homepage;
      if (!card.durationSec && e.durationSec > 0) card.durationSec = e.durationSec;
      // Dedup repeated titles within a session. Many stations flip the ICY title
      // between the song and talk/promo/contact lines (SWR3: "TALK mit ...",
      // "Kontakt zu SWR3: ..."), which otherwise fills the card with the same few
      // strings. Keep the newest occurrence of each distinct title only (entries
      // arrive newest-first), so each line shows once.
      if (e.track && !card._seen.has(e.track)) {
        card._seen.add(e.track);
        card.tracks.push({ track: e.track, ts: e.ts });
      }
      continue;
    }
    if (card) {
      // Same station on another speaker, heard earlier: remember the speaker
      // (its newest row identifies the card there for a delete), no tracks.
      if (!card.members.some((m) => m.boxKey === e._boxKey)) {
        card.members.push({ box: e._box, boxKey: e._boxKey, boxName: e._boxName, ts: e.ts });
      }
      continue;
    }
    const seen = new Set();
    if (e.track) seen.add(e.track);
    // Spotify playable when its playlist URI matches a preset slot on its box.
    const playSlot = e.source === 'spotify' && e._spotifySlots
      ? e._spotifySlots[e.cardKey]
      : undefined;
    const c = {
      boxKey: e._boxKey, box: e._box, boxName: e._boxName,
      source: e.source, cardKey: e.cardKey, name: e.cardName,
      art: e.cardArt, url: e.cardURL, mime: e.mime, durationSec: e.durationSec || 0, account: e.account, ts: e.ts,
      itemType: e.itemType || '',
      homepage: e.homepage || '', playSlot,
      tracks: e.track ? [{ track: e.track, ts: e.ts }] : [],
      members: [{ box: e._box, boxKey: e._boxKey, boxName: e._boxName, ts: e.ts }],
      _seen: seen,
    };
    byKey.set(e.cardKey, c);
    cards.push(c);
  }
  return cards;
}

// loadRecentCards fetches /api/recent from the selected scope (this box vs all),
// tags each entry with its box, merges newest-first and groups into cards. It
// also fetches each box's presets so a Spotify card whose playlist is saved as a
// preset (= the box holds that account's token) can offer a play button that
// recalls the slot. One presets fetch per box on view-open: app-side, no box poll.
async function loadRecentCards() {
  const boxes = recentSelectedBoxes();
  const results = await Promise.all(boxes.map(async (b) => {
    const boxKey = recentBoxKey(b);
    // friendlyName first: the backend always fills Name with a "str-<IP>" fallback,
    // so b.name is never empty. This matches the box switcher and the rest of the app.
    // A stereo pair shows its own STR name here instead of the single box (#775).
    const boxName = recentBoxDisplayLabel(b);
    let list = [];
    try { list = await RecentPlayed(b.host, b.port) || []; } catch { list = []; }
    // Map Spotify playlist URI -> preset slot on this box. A match means the box
    // can recall (and holds the token), so the card gets a play button.
    const spotifySlots = {};
    try {
      const presets = await GetPresets(b.host, b.port) || [];
      for (const p of presets) {
        if (p && p.type === 'spotify' && p.uri) spotifySlots[p.uri] = p.slot;
      }
    } catch { /* presets unreachable: no play buttons, save still works */ }
    return list.map((e) => ({ ...e, _box: b, _boxKey: boxKey, _boxName: boxName, _spotifySlots: spotifySlots }));
  }));
  const merged = results.flat().sort((a, b) => (a.ts < b.ts ? 1 : a.ts > b.ts ? -1 : 0));
  return groupRecentCards(merged);
}

function recentSourceLabel(src) {
  if (src === 'spotify') return t('recent.srcSpotify');
  if (src === 'upnp') return t('recent.srcLibrary');
  if (src === 'pandora') return 'Pandora';
  return t('recent.srcRadio');
}

function recentClock(ts) {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return '';
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

// formatTrack splits an ICY title into artist + track, shows the artist
// emphasised (lead) and the track in dark grey (sub). The StreamTitle never
// labels which part is which, but the separator is a reliable tell across
// stations: " - " is the Shoutcast de-facto standard "Artist - Title", while
// " / " is "Title / Artist" (e.g. SWR3, verified live: "Don't let me go /
// Kelvin Jones"). We normalise both to artist-first so the lead line is always
// the artist. A dash wins over a slash, since a slash is also part of artist
// names ("Axwell / Ingrosso - More Than You Know"); see splitStreamTitle. No
// separator: shown as-is on the lead line.
function formatTrack(raw) {
  const { artist, track } = splitStreamTitle(raw);
  if (artist) {
    return `<span class="rc-tr-lead">${escapeHtml(artist)}</span>`
      + `<span class="rc-tr-sub">${escapeHtml(track)}</span>`;
  }
  return `<span class="rc-tr-lead">${escapeHtml((raw || '').trim())}</span>`;
}

// logoImg builds the card logo as an <img> with the same data-fallbacks cascade
// the preset/search tiles use (a global error-listener walks the chain). Spotify
// shows its glyph; radio/NAS derive favicons from the URL, ending on a monogram
// so a logo-less station still gets a clean letter tile instead of a blank box.
function logoImg(c) {
  if (c.source === 'spotify') {
    return `<img class="rc-logo" src="${escapeAttr(SPOTIFY_LOGO)}" alt="">`;
  }
  // Radio/NAS reuse the exact search/preset tile path (logoImgTag), so they go
  // through the SAME global, async, non-blocking Go-resolved hydration that
  // validates the favicon and rejects DuckDuckGo's grey "no icon" chevron. The
  // previous raw data-fallbacks cascade could not reject that chevron and showed
  // the wrong favicon derived from the stream CDN host (the SWR3 case).
  const tag = logoImgTag(cardStation(c), 'rc-logo');
  // UI 2.0: the station's name sits next to its logo, so the logo is
  // decoration and says so (an empty alt), instead of an unnamed image.
  return shell2Mounted() ? tag.replace('<img ', '<img alt="" ') : tag;
}

// cardWebURL returns the card's website, shown as a "website" link like the radio
// search rows. Radio: the station homepage captured at play time. Spotify: the
// open.spotify.com page derived from the playlist/album URI. Empty -> no link.
function cardWebURL(c) {
  if (c.source === 'spotify') {
    const m = (c.cardKey || '').match(/^spotify:([a-z]+):([A-Za-z0-9]+)/);
    return m ? `https://open.spotify.com/${m[1]}/${m[2]}` : '';
  }
  const hp = c.homepage || '';
  return /^https?:\/\//i.test(hp) ? hp : '';
}

// cardIsPlaying reports whether this card is the speaker's current source, so it
// gets the green "playing" highlight. Radio/NAS match the now-playing name or the
// exact stream URL. Spotify matches the live slot from the per-slot stream URL
// (or the remembered slot, so a next/prev that drops the slot still counts)
// against the card's preset slot, since nowName for Spotify is the song, not the
// playlist. Best-effort: a card with no matching preset slot cannot be matched.
//
// state.nowName / nowLocation describe the CURRENT speaker only, so a card from
// any other speaker can never match them: without this guard, viewing speaker
// B's history while speaker A plays a station that also sits in B's history put
// the badge on the wrong card (#710). A non-current speaker's cards simply show
// no badge; polling every box's now-playing just for it is not worth the box
// traffic.
export function cardIsPlaying(c) {
  if (!state.currentBox) return false;
  // A card that stands for several speakers (one card per station) counts for
  // each of them.
  const curKey = recentBoxKey(state.currentBox);
  const keys = (c.members && c.members.length) ? c.members.map((m) => m.boxKey) : [c.boxKey];
  if (!keys.includes(curKey)) return false;
  const loc = state.nowLocation || '';
  if (c.source === 'spotify') {
    if (!/\/spotify\/stream/.test(loc)) return false;
    const m = loc.match(/\/spotify\/stream-(\d+)\.ogg/);
    const liveSlot = m ? parseInt(m[1], 10) : state.nowSpotifySlot;
    return c.playSlot != null && liveSlot != null && c.playSlot === liveSlot;
  }
  // A folder card carries only its first track, so the name/URL match below
  // dropped the mark from the second track on and brought it back when the
  // queue wrapped (#1065). While a queue plays, the speaker names the card it
  // belongs to; match on that. An older agent without the field falls through.
  const q = state.queue;
  if (String(c.cardKey || '').startsWith('queue:') && q && q.active && q.card) {
    return q.card === c.cardKey;
  }
  if (state.nowName && c.name && state.nowName === c.name) return true;
  return !!(c.url && loc && loc === c.url);
}

function recentCardHTML(c, i, nowPlaying) {
  // Pandora is played by the speaker's own client, so its card has play and
  // save-to-key like the Find tab's Pandora rows, but no radio favourite.
  const isRadio = c.source !== 'spotify' && c.source !== 'pandora';
  const webUrl = cardWebURL(c);
  // Always show which box played it (Jens) plus the source, Spotify account and,
  // like the radio search rows, a "website" link.
  // The Spotify account goes through the same shortener as the preset tile.
  // It did not, and that was half a fix: the tile was shortened on 2026-10-01
  // because a full canonical Spotify user id had reached a support mail and was
  // about to reach a public screenshot, and this card was never touched. It
  // then appeared in a public screenshot anyway, on 2026-10-02. One identifier,
  // two places, and only one of them was looked at.
  const accountLabel = c.account ? spotifyAccountName(c.account, state.spotifyAccountNames) : '';
  // Every speaker that played this station, the most recent one first.
  const boxNames = [...new Set((c.members && c.members.length ? c.members.map((m) => m.boxName) : [c.boxName]).filter(Boolean))];
  const sub = `<span class="rc-src">${escapeHtml(recentSourceLabel(c.source))}</span>`
    + (accountLabel ? ` &middot; ${escapeHtml(accountLabel)}` : '')
    + (boxNames.length ? ` &middot; <span class="rc-box">${escapeHtml(boxNames.join(', '))}</span>` : '')
    + (webUrl ? ` &middot; <a href="#" class="rc-site" id="recSite${i}" title="${escapeAttr(t('search.openWebsite'))}">${escapeHtml(t('footer.website'))}</a>` : '');
  const tracks = c.tracks.length
    ? `<div class="rc-tracks">` + c.tracks.map((tr) =>
        `<div class="rc-track"><span class="rc-tr-main">${formatTrack(tr.track)}</span>`
        + `<span class="rc-tr-time">${escapeHtml(recentClock(tr.ts))}</span></div>`).join('') + `</div>`
    : '';
  // Buttons identical to the radio search rows: play, save-to-preset, favourite.
  // Spotify is not radio-favouritable, so it gets save plus, when the box holds
  // the token for this playlist (a Spotify preset with the same URI exists), a
  // play button that recalls that slot.
  let actions;
  if (shell2Mounted()) {
    actions = ui2CardActions(c, i, isRadio);
  } else if (isRadio) {
    const fav = deps.isFav ? deps.isFav(cardStation(c)) : false;
    actions = `<button class="btn btn-mini rc-play" id="recPlay${i}" title="${escapeAttr(t('search.playNow'))}">&#9205;</button>`
      + `<button class="btn btn-mini rc-pick" id="recPick${i}" title="${escapeAttr(t('search.assignToKey'))}">&#10133;</button>`
      + `<button class="btn btn-mini rc-fav${fav ? ' is-fav' : ''}" id="recFav${i}" title="${escapeAttr(fav ? t('search.removeFav') : t('search.addFav'))}">${fav ? '&#9733;' : '&#9734;'}</button>`;
  } else {
    const canPlay = c.source === 'pandora' ? !!c.url : c.playSlot != null;
    actions = (canPlay
      ? `<button class="btn btn-mini rc-play" id="recPlay${i}" title="${escapeAttr(t('search.playNow'))}">&#9205;</button>`
      : '')
      + `<button class="btn btn-mini rc-pick" id="recPick${i}" title="${escapeAttr(t('search.assignToKey'))}">&#10133;</button>`;
  }
  // Remove-this-card button: drops the card from the box's history.
  if (!shell2Mounted()) actions += `<button class="btn btn-mini rc-del" id="recDel${i}" title="${escapeAttr(t('recent.removeCard'))}">&times;</button>`;
  // nowPlaying is decided by the caller (the newest matching card only, #761),
  // not per-card, so a station in history several times lights up once.
  // "Now playing" badge, reusing the preset tile's own state label so the wording
  // matches the preset card exactly (and is already translated in every bundle).
  const nowBadge = nowPlaying
    ? ` <span class="rc-now-badge">&#9205; ${escapeHtml(t('preset.statePlay'))}</span>`
    : '';
  return `<div class="recent-card rc-${escapeAttr(c.source)}${nowPlaying ? ' rc-now' : ''}">`
    + `<div class="rc-head">${logoImg(c)}`
    + `<div class="rc-meta"><div class="rc-name">${escapeHtml(c.name || recentSourceLabel(c.source))}${nowBadge}</div>`
    + `<div class="rc-sub">${sub}</div></div>`
    + `<div class="rc-actions">${actions}</div></div>${tracks}`
    // UI 2.0: a failed action reports in its own card (rule 3), not in a modal.
    + (shell2Mounted() ? cardErrorLine(c, i) : '')
    + `</div>`;
}

// cardPlayBox is the speaker a card's play button acts on: a radio card plays
// through the radio search's path, on the speaker you control; every other
// card replays on the speaker that played it, which holds its Spotify key, its
// Pandora login or its library track.
export function cardPlayBox(c) {
  const isRadio = c.source !== 'spotify' && c.source !== 'pandora' && c.source !== 'upnp';
  return isRadio ? state.currentBox : (c.box || state.currentBox);
}

// cardKeyBox is the speaker whose keys the "put on a key" picker writes:
// Spotify saves on the card's own speaker (saveSpotifyCard), radio and Pandora
// on the speaker you control (openPick, openPandoraPick).
export function cardKeyBox(c) {
  return c.source === 'spotify' ? (c.box || state.currentBox) : state.currentBox;
}

// ui2CardActions draws a card's buttons in the UI 2.0 preview: the same
// actions as the current layout, as 40px icon buttons whose names say the
// station and the speaker they act on (rule 1). The favourite star is
// app-wide, so it names no speaker; the remove button names every speaker
// whose history loses the card.
function ui2CardActions(c, i, isRadio) {
  const name = c.name || recentSourceLabel(c.source);
  const label = (b) => (b ? recentBoxDisplayLabel(b) : '');
  const btn = (cls, id, text, glyph, extra = '') =>
    `<button type="button" class="u-btn u-btn--icon ${cls}" id="${id}" aria-label="${escapeAttr(text)}" title="${escapeAttr(text)}"${extra}>${glyph}</button>`;
  const canPlay = isRadio || (c.source === 'pandora' ? !!c.url : c.playSlot != null);
  let html = '';
  if (canPlay) html += btn('rc-play', 'recPlay' + i, t('ui2.recent.playOn', { name, speaker: label(cardPlayBox(c)) }), '&#9205;');
  html += btn('rc-pick', 'recPick' + i, t('ui2.recent.keyOn', { name, speaker: label(cardKeyBox(c)) }), '+');
  if (isRadio) {
    const fav = deps.isFav ? deps.isFav(cardStation(c)) : false;
    html += btn('rc-fav' + (fav ? ' is-fav' : ''), 'recFav' + i, favLabel(name, fav), fav ? '&#9733;' : '&#9734;', ` aria-pressed="${fav}"`);
  }
  const speakers = [...new Set((c.members && c.members.length ? c.members.map((m) => m.boxName) : [c.boxName]).filter(Boolean))];
  html += btn('rc-del', 'recDel' + i, t('ui2.recent.removeFrom', { name, speakers: speakers.join(', ') }), '&times;');
  return html;
}

// favLabel is the favourite star's name. The current layout keeps its short
// tooltip; the preview names the station, since the star is an icon only.
function favLabel(name, fav) {
  if (!shell2Mounted()) return fav ? t('search.removeFav') : t('search.addFav');
  return t(fav ? 'ui2.recent.favRemove' : 'ui2.recent.favAdd', { name });
}

// cardErrors keeps the last failed action of each card (by cardKey) in the
// preview, so the 30 s repaint does not wipe the message before it was read.
// The card's next action clears it.
const cardErrors = new Map();

// showCardError reports a failed card action. The preview writes it into the
// card's own error line (rule 3, errors where they happen); the current layout
// keeps its error dialog.
function showCardError(c, i, err) {
  const line = shell2Mounted() ? document.getElementById('recErr' + i) : null;
  if (!line) { showError(err); return; }
  const msg = friendlyErrorText(String(err && err.message ? err.message : err || t('modal.unknownError')));
  cardErrors.set(c.cardKey, msg);
  line.textContent = msg;
}

// clearCardError empties the card's error line as its next action starts, and
// makes the line an alert again for that action's answer.
function clearCardError(c, i) {
  cardErrors.delete(c.cardKey);
  const line = shell2Mounted() ? document.getElementById('recErr' + i) : null;
  if (line) { line.textContent = ''; line.setAttribute('role', 'alert'); }
}

// cardErrorLine is the card's error line. Empty, it is an alert that speaks
// the next failure; repainted with a kept message, it is plain text, so a
// screen reader does not hear the same error again on every refresh.
function cardErrorLine(c, i) {
  const msg = cardErrors.get(c.cardKey) || '';
  return `<div class="rc-err" id="recErr${i}"${msg ? '' : ' role="alert"'}>${escapeHtml(msg)}</div>`;
}

// replayFolderCard starts a folder card as a whole queue again. Returns true when
// it took the play, false when this card is not a folder card or the speaker
// cannot do it yet, in which case the caller replays the single stored track.
async function replayFolderCard(c, box) {
  if (!box || !String(c.cardKey || '').startsWith('queue:')) return false;
  const art = (c.art || '').split('|')[0].trim();
  try {
    await ReplayFolderCard(box.host, box.port, c.cardKey, c.name || '', art);
    showToast(t('recent.playing', { name: c.name || '' }));
    return true;
  } catch (err) {
    const msg = String(err && err.message ? err.message : err);
    if (isMissingBinding(err) || msg.includes('folder_replay_unsupported')) return false;
    // A real refusal (server offline, folder gone) is the user's answer. Playing
    // the one stored track instead would hide it and look like the old bug.
    showError(err);
    return true;
  }
}

// replayLibraryTrack plays a network-library card's stored file directly, with
// its MIME and its length. The length is what makes the speaker report a total,
// so without it a replayed track showed no progress bar while the same track
// from the Library did (#845). 0 (a card recorded before the length was kept)
// still plays, just without the bar.
async function replayLibraryTrack(c, box, art) {
  const dur = c.durationSec > 0 ? Math.round(c.durationSec) : 0;
  await PlayURL(box.host, box.port, c.url, c.name || '', art, '', c.mime || '', '', '', dur);
  // The bar describes the selected speaker, so only a replay on that speaker
  // moves the line (a card can belong to another one). Same instant change as
  // a Library click (#1394).
  if (state.currentBox && box.host === state.currentBox.host) {
    beginLibraryNowPlaying(state, { url: c.url, name: c.name || '', art }, Date.now());
    deps.renderNowPlayingBar();
    setTimeout(deps.refreshStatus, 1500);
  }
}

// replayPandoraCard starts a Pandora card on the speaker's own Pandora client,
// exactly like the Find tab's Pandora row. The stored item type lets the agent
// rebuild the item when it no longer holds the station list (#1101).
async function replayPandoraCard(c, box) {
  await PlayPandoraStation(box.host, box.port, c.url, c.name || '', c.itemType || '');
}

function wireCard(c, i) {
  const playBtn = document.getElementById('recPlay' + i);
  const pickBtn = document.getElementById('recPick' + i);
  const favBtn = document.getElementById('recFav' + i);
  const siteBtn = document.getElementById('recSite' + i);
  if (siteBtn) {
    siteBtn.onclick = (e) => {
      e.preventDefault();
      const url = cardWebURL(c);
      if (url) { try { BrowserOpenURL(url); } catch {} }
    };
  }
  if (playBtn) {
    playBtn.onclick = async () => {
      // UI 2.0: the button is busy until the speaker answered, so a second
      // click cannot start the same station twice and nothing claims "playing"
      // before the speaker took it (rule 2). The current layout is unchanged.
      const busy = shell2Mounted();
      if (busy) { clearCardError(c, i); playBtn.disabled = true; playBtn.setAttribute('aria-busy', 'true'); }
      try {
        if (c.source === 'spotify') {
          // Token present (matched a preset slot): recall it, same as a preset.
          await PlaySlot(c.box.host, c.box.port, c.playSlot);
          showToast(t('recent.playing', { name: c.name || '' }));
        } else if (c.source === 'pandora') {
          await replayPandoraCard(c, c.box || state.currentBox);
          showToast(t('recent.playing', { name: c.name || '' }));
        } else if (c.source === 'upnp') {
          // A network-library (NAS) track must replay the DIRECT library way, not
          // the radio path. The radio path runs the file through the stream proxy
          // built for endless radio, which loops forever on a finite file (#817);
          // PlayURL with the stored MIME makes the box play it straight instead.
          const box = c.box || state.currentBox;
          const art = (c.art || '').split('|')[0].trim();
          // A FOLDER card is not a track. Its key names the media server and the
          // container, so the speaker can rebuild the whole queue; the stored URL
          // is only its first track, and playing that clears the queue, which is
          // why a replayed folder used to play one song and then stop with the
          // indicator sitting amber (#978). Falls back to the single track when
          // the speaker's agent is older than the endpoint.
          if (await replayFolderCard(c, box)) return;
          await replayLibraryTrack(c, box, art);
          showToast(t('recent.playing', { name: c.name || '' }));
        } else {
          await deps.playStation(cardStation(c));
        }
      } catch (err) { showCardError(c, i, err); } finally {
        if (busy) { playBtn.disabled = false; playBtn.removeAttribute('aria-busy'); }
      }
    };
  }
  if (pickBtn) {
    pickBtn.onclick = () => {
      if (c.source === 'spotify') return saveSpotifyCard(c);
      if (c.source === 'pandora') {
        if (deps.openPandoraPick) deps.openPandoraPick({ location: c.url, name: c.name || '', type: c.itemType || '' });
        return;
      }
      deps.openPick(cardStation(c)); // radio: identical to the search "+" action
    };
  }
  if (favBtn) {
    favBtn.onclick = () => {
      const nowFav = deps.toggleFav(cardStation(c));
      favBtn.classList.toggle('is-fav', nowFav);
      favBtn.innerHTML = nowFav ? '&#9733;' : '&#9734;';
      favBtn.title = favLabel(c.name || recentSourceLabel(c.source), nowFav);
      if (shell2Mounted()) { favBtn.setAttribute('aria-label', favBtn.title); favBtn.setAttribute('aria-pressed', String(nowFav)); }
    };
  }
  const delBtn = document.getElementById('recDel' + i);
  if (delBtn) {
    delBtn.onclick = async () => {
      delBtn.disabled = true;
      clearCardError(c, i);
      try {
        await deleteRecentStation(c, DeleteRecentCard, state.currentBox);
        await refreshRecentList();
      } catch (err) { showCardError(c, i, err); delBtn.disabled = false; }
    };
  }
}

// deleteRecentStation removes a card's station from every speaker it stands
// for, each identified by its own newest row, so the station does not come
// back from another speaker on the next refresh. A speaker that is unreachable
// does not stop the others; the first error is reported once all were tried.
export async function deleteRecentStation(c, del, fallbackBox) {
  const members = (c.members && c.members.length)
    ? c.members
    : [{ box: c.box || fallbackBox, ts: c.ts }];
  let firstErr = null;
  for (const m of members) {
    const box = m.box || fallbackBox;
    if (!box) continue;
    try { await del(box.host, box.port, c.cardKey, m.ts); } catch (err) { if (!firstErr) firstErr = err; }
  }
  if (firstErr) throw firstErr;
}

function saveSpotifyCard(c) {
  const box = c.box || state.currentBox;
  if (!box || !deps.showSlotPicker) return;
  deps.showSlotPicker({
    title: t('recent.saveTitle'),
    subtitle: c.name || '',
    // A card can belong to another speaker than the selected one, and the picker
    // needs to know which speaker it is writing to: a refusal there is answered
    // with a move on that same speaker.
    box,
    onPick: async (i) => {
      await SaveSpotifyPreset(box.host, box.port, i, c.name || '', c.url, c.account || '');
      showToast(t('recent.saved', { name: c.name || '' }));
    },
  });
}

// recentTimer drives the auto-refresh while the Recently-played tab is open, so
// a freshly played station/song shows up (and the green "now playing" mark
// follows the speaker) without the user re-opening the tab. Cleared on
// navigate-away and on re-entry so timers never stack.
let recentTimer = null;
// favTimer asks the selected speaker every few seconds whether a favourite was
// removed on a phone, so the star here goes out within seconds instead of
// after the minute-long full sync plus this view's 30 s repaint (#1339).
let favTimer = null;
function stopRecentAutoRefresh() {
  if (recentTimer) { clearInterval(recentTimer); recentTimer = null; }
  if (favTimer) { clearInterval(favTimer); favTimer = null; }
}

// RECENT_FAV_POLL_MS is how often the open view checks for a phone removal.
// One small read of one speaker, answered from the agent's memory, and only
// while this tab is the one on screen and the window is visible.
export const RECENT_FAV_POLL_MS = 5000;

// recentFavPollDue says whether a fav-removal check may run right now.
export function recentFavPollDue({ view, visibility }) {
  return view === 'recent' && visibility === 'visible';
}

// The cards last painted, so a star can be repainted without fetching the
// history from every speaker again.
let paintedCards = [];

// repaintRecentFavStars brings every radio card's star in line with the
// favourites store. Called when a phone removal took a star off the list.
// lookup is injectable for the tests (the suite runs without a DOM).
export function repaintRecentFavStars(lookup = (id) => document.getElementById(id)) {
  if (!deps.isFav) return 0;
  let n = 0;
  paintedCards.forEach((c, i) => {
    const btn = lookup('recFav' + i);
    if (!btn) return;
    const fav = !!deps.isFav(cardStation(c));
    if (btn.classList.contains('is-fav') === fav) return;
    btn.classList.toggle('is-fav', fav);
    btn.innerHTML = fav ? '&#9733;' : '&#9734;';
    btn.title = favLabel(c.name || recentSourceLabel(c.source), fav);
    if (shell2Mounted()) { btn.setAttribute('aria-label', btn.title); btn.setAttribute('aria-pressed', String(fav)); }
    n++;
  });
  return n;
}

// setPaintedRecentCards is the tests' way to stand in for a painted list.
export function setPaintedRecentCards(cards) {
  paintedCards = Array.isArray(cards) ? cards : [];
}

// refreshRecentList re-fetches and repaints only the card list (not the header /
// scope chips), so the auto-refresh does not disturb the controls. /api/recent
// is a cheap in-RAM read on the box; this only runs while the tab is visible.
// Exported so main.js's status poll can repaint it the instant the speaker's
// source changes from another controller, instead of waiting for the 30 s timer
// (#810).
// syncCurrentNowPlaying refreshes state.nowLocation/nowName from the scoped box
// so the "now playing" mark tracks the speaker while this tab is open. The
// box-view status poll (main.js refreshStatus) returns early on any non-box
// view, so on the Recently-played tab nothing updated now-playing and a station
// changed from another controller (the phone ST Remote) left the badge on the
// old card until the user switched screens (#810; the earlier repaint-in-
// refreshStatus fix was unreachable because that function is box-view only).
// Parse mirrors refreshStatus. Current-box only, since the mark is scoped to it.
async function syncCurrentNowPlaying() {
  const box = state.currentBox;
  if (!box) return;
  try {
    const xml = await Status(box.host, box.port);
    if (state.view !== 'recent') return;
    if (Date.now() < (state.optimisticUntil || 0)) return; // let a fresh local click settle first
    state.nowName = decodeXmlEntities((xml.match(/<stationName>([^<]+)<\/stationName>/) || [])[1]
      || (xml.match(/<itemName>([^<]+)<\/itemName>/) || [])[1] || '');
    state.nowLocation = decodeXmlEntities((xml.match(/location="([^"]+)"/) || [])[1] || '');
    state.nowSource = (xml.match(/source="([^"]+)"/) || [])[1] || '';
  } catch { /* box busy or rebooting: keep the last known mark */ }
}

export async function refreshRecentList() {
  if (state.view !== 'recent') { stopRecentAutoRefresh(); return; }
  await syncCurrentNowPlaying();
  const cards = await loadRecentCards();
  const listEl = $('recentList');
  if (!listEl || state.view !== 'recent') { stopRecentAutoRefresh(); return; } // navigated away mid-fetch
  if (!cards.length) {
    paintedCards = [];
    listEl.innerHTML = shell2Mounted()
      ? ui2EmptyHTML()
      : `<div class="recent-empty">${escapeHtml(t('recent.empty'))}</div>`;
    return;
  }
  // Only ONE card wears the now-playing badge: the NEWEST card of the current
  // source. Each station is one card now, but two stations can share a name
  // (cardIsPlaying is name-matched for radio), and before that a station played
  // several times lit up every one of its cards (#761). Cards are newest-first,
  // so the first match is the running session.
  const playingIdx = cards.findIndex(cardIsPlaying);
  paintedCards = cards;
  listEl.innerHTML = shell2Mounted()
    ? ui2DaysHTML(cards, playingIdx)
    : cards.map((c, i) => recentCardHTML(c, i, i === playingIdx)).join('');
  cards.forEach((c, i) => wireCard(c, i));
}

// ui2DaysHTML paints the cards of the UI 2.0 preview under one heading per
// day ("Today", "Yesterday", then the date in the app's language), each card
// under the day of its newest listening (views/recentdays.js). The cards keep
// their index, so wireCard and the star repaint find their buttons.
function ui2DaysHTML(cards, playingIdx) {
  const labels = { today: t('ui2.recent.today'), yesterday: t('ui2.recent.yesterday'), earlier: t('ui2.recent.earlier') };
  return groupCardsByDay(cards, { locale: getLocale(), labels }).map((g, n) =>
    `<section class="rc-day" aria-labelledby="recDay${n}">`
    + `<h3 class="rc-day-head" id="recDay${n}">${escapeHtml(g.label)}</h3>`
    + `<div class="rc-day-list">${g.items.map(({ card, index }) => recentCardHTML(card, index, index === playingIdx)).join('')}</div>`
    + `</section>`).join('');
}

// ui2EmptyHTML explains an empty history in the preview: what fills it, for
// the speaker (or all of them) the view shows.
function ui2EmptyHTML() {
  const boxes = recentSelectedBoxes();
  const body = (state.recentAllBoxes || boxes.length !== 1)
    ? t('ui2.recent.emptyAll')
    : t('ui2.recent.emptyOne', { speaker: recentBoxDisplayLabel(boxes[0]) });
  return `<div class="recent-empty rc-empty2"><p class="rc-empty-title">${escapeHtml(t('ui2.recent.emptyTitle'))}</p>`
    + `<p class="rc-empty-body">${escapeHtml(body)}</p></div>`;
}

// recentFollowsShellPick is called when a speaker is chosen in the UI 2.0
// sidebar while this view is open: the history follows it, the same as it
// follows the speaker you control before a speaker was picked here, and the
// buttons name the new speaker. "All speakers" stays as chosen.
export function recentFollowsShellPick() {
  if (!shell2Mounted() || state.view !== 'recent') return;
  if (!state.recentAllBoxes) state.recentBoxKey = null;
  renderRecent();
}

// renderRecent paints the view into #view-recent. Called from main.js's
// switchView when the Recently-played tab is opened.
export async function renderRecent() {
  const root = $('view-recent');
  if (!root) return;
  stopRecentAutoRefresh(); // re-entry / scope toggle: never stack timers
  const boxesList = recentStrBoxes();
  const multi = boxesList.length > 1;
  // Speaker picker (#221): a dropdown so it is explicit WHICH speaker's history
  // is shown (the old "This speaker" chip implicitly followed the Music tab), plus
  // an "All speakers" merge. The currently scoped speaker is preselected.
  const selectedBoxes = recentSelectedBoxes();
  const selectedKey = (!state.recentAllBoxes && selectedBoxes.length === 1)
    ? recentBoxKey(selectedBoxes[0]) : '';
  let html = `<div class="recent-head"><h2 class="recent-title">${escapeHtml(t('recent.title'))}<span class="str-badge" title="${escapeAttr(t('common.strOnlyHint'))}">${escapeHtml(t('common.strOnly'))}</span></h2>`;
  if (multi) {
    const opts = `<option value="__all__"${state.recentAllBoxes ? ' selected' : ''}>${escapeHtml(t('recent.allBoxes'))}</option>`
      + boxesList.map((b) => {
        const k = recentBoxKey(b);
        const sel = (!state.recentAllBoxes && k === selectedKey) ? ' selected' : '';
        return `<option value="${escapeAttr(k)}"${sel}>${escapeHtml(getBoxLabel(b))}</option>`;
      }).join('');
    html += `<div class="recent-scope"><select class="recent-box-select" id="recentBoxSelect" `
      + `title="${escapeAttr(t('recent.thisBox'))}">${opts}</select></div>`;
  }
  // Clear the whole list. Clears every box in the current scope.
  const ui2 = shell2Mounted();
  html += ui2
    ? `<button type="button" class="u-btn u-btn--secondary recent-clear" id="recentClear">${escapeHtml(t('recent.clearAll'))}</button>`
    : `<button class="btn btn-mini recent-clear" id="recentClear" title="${escapeAttr(t('recent.clearAll'))}">${escapeHtml(t('recent.clearAll'))}</button>`;
  html += `</div><div class="recent-sub muted">${escapeHtml(t('recent.subtitle'))}</div>`
    // UI 2.0: what clearing could not do is said under the header (rule 3).
    + (ui2 ? `<div class="rc-clear-err" id="recentClearErr" role="alert"></div>` : '')
    + `<div class="recent-list" id="recentList"><div class="muted recent-loading">${escapeHtml(t('recent.loading'))}</div></div>`;
  root.innerHTML = html;

  const sel = $('recentBoxSelect');
  if (sel) sel.onchange = () => {
    if (sel.value === '__all__') {
      state.recentAllBoxes = true;
    } else {
      state.recentAllBoxes = false;
      state.recentBoxKey = sel.value;
    }
    renderRecent();
  };

  const clr = $('recentClear');
  if (clr) clr.onclick = async () => {
    const ok = await confirmWarn(t('recent.clearConfirmTitle'), t('recent.clearConfirmBody'));
    if (!ok) return;
    const boxes = recentSelectedBoxes();
    clr.disabled = true;
    const failed = [];
    for (const b of boxes) { try { await ClearRecent(b.host, b.port); } catch { failed.push(recentBoxDisplayLabel(b)); /* skip unreachable box */ } }
    await refreshRecentList();
    clr.disabled = false;
    // The preview names the speakers whose list stayed, instead of a list that
    // silently comes back on the next refresh (rule 2).
    const errLine = $('recentClearErr');
    if (errLine) errLine.textContent = failed.length ? t('ui2.recent.clearFailed', { speakers: failed.join(', ') }) : '';
  };

  await refreshRecentList();
  // Auto-refresh every 30s while the tab stays open. The interval self-cancels
  // once the user leaves the Recently-played view.
  recentTimer = setInterval(() => {
    if (state.view !== 'recent') { stopRecentAutoRefresh(); return; }
    refreshRecentList();
  }, 30000);
  // A favourite removed on a phone: check once now (the tab was just opened)
  // and then every few seconds while it stays on screen (#1339).
  if (deps.pollFavRemovals) {
    const pollFav = () => {
      if (state.view !== 'recent') { stopRecentAutoRefresh(); return; }
      if (!recentFavPollDue({ view: state.view, visibility: document.visibilityState })) return;
      deps.pollFavRemovals().catch(() => {});
    };
    pollFav();
    favTimer = setInterval(pollFav, RECENT_FAV_POLL_MS);
  }
}
