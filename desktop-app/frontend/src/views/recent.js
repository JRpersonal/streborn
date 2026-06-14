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
// time, group consecutive same-card rows into source cards (newest card on top,
// tracks newest-first). The box only appends in-RAM; ALL the merge, grouping and
// rendering happen here in the app (App-First, keep the box light).

import { state } from '../state.js';
import { $, escapeHtml, escapeAttr, showError, showToast } from '../utils.js';
import { t } from '../i18n/index.js';
import { RecentPlayed, SaveSpotifyPreset } from '../api.js';
import { logoImgTag, SPOTIFY_LOGO } from '../logos.js';

// Injected main.js helpers (see initRecentView). showSlotPicker is the shared
// modal; playStation/openPick/toggleFav/isFav are the exact radio-search-row
// actions, reused so the radio cards are pixel- and behaviour-identical.
let deps = {
  showSlotPicker: null,
  playStation: null,
  openPick: null,
  toggleFav: null,
  isFav: null,
};
export function initRecentView(d) {
  deps = { ...deps, ...d };
}

// recentStrBoxes returns the discovered STR speakers (skip unflashed stock ones).
function recentStrBoxes() {
  return (state.boxes || []).filter((b) => b && b.kind !== 'stock' && b.host);
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
    homepage: '',
    tags: '',
    country: '',
    countrycode: '',
  };
}

// groupRecentCards turns a newest-first, box-tagged entry list into source cards:
// a card is a contiguous run of the same (box, cardKey). Tracks within stay
// newest-first; an empty-track placeholder row just yields a card with no tracks.
function groupRecentCards(entries) {
  const cards = [];
  for (const e of entries) {
    const last = cards[cards.length - 1];
    if (last && last.boxKey === e._boxKey && last.cardKey === e.cardKey) {
      // Dedup repeated titles within a session. Many stations flip the ICY title
      // between the song and talk/promo/contact lines (SWR3: "TALK mit ...",
      // "Kontakt zu SWR3: ..."), which otherwise fills the card with the same few
      // strings. Keep the newest occurrence of each distinct title only (entries
      // arrive newest-first), so each line shows once.
      if (e.track && !last._seen.has(e.track)) {
        last._seen.add(e.track);
        last.tracks.push({ track: e.track, ts: e.ts });
      }
      continue;
    }
    const seen = new Set();
    if (e.track) seen.add(e.track);
    cards.push({
      boxKey: e._boxKey, box: e._box, boxName: e._boxName,
      source: e.source, cardKey: e.cardKey, name: e.cardName,
      art: e.cardArt, url: e.cardURL, account: e.account, ts: e.ts,
      tracks: e.track ? [{ track: e.track, ts: e.ts }] : [],
      _seen: seen,
    });
  }
  return cards;
}

// loadRecentCards fetches /api/recent from the selected scope (this box vs all),
// tags each entry with its box, merges newest-first and groups into cards.
async function loadRecentCards() {
  const boxes = state.recentAllBoxes
    ? recentStrBoxes()
    : (state.currentBox ? [state.currentBox] : []);
  const results = await Promise.all(boxes.map(async (b) => {
    try {
      const list = await RecentPlayed(b.host, b.port);
      const boxKey = b.deviceID || (b.host + ':' + b.port);
      const boxName = b.name || b.friendlyName || b.host;
      return (list || []).map((e) => ({ ...e, _box: b, _boxKey: boxKey, _boxName: boxName }));
    } catch {
      return [];
    }
  }));
  const merged = results.flat().sort((a, b) => (a.ts < b.ts ? 1 : a.ts > b.ts ? -1 : 0));
  return groupRecentCards(merged);
}

function recentSourceLabel(src) {
  if (src === 'spotify') return t('recent.srcSpotify');
  if (src === 'upnp') return t('recent.srcLibrary');
  return t('recent.srcRadio');
}

function recentClock(ts) {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return '';
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

// formatTrack splits an ICY title into its two parts ("Title / Artist" or
// "Artist - Title") and formats them as an emphasised primary + a muted
// secondary. Which side is title vs artist varies by station, so we do not
// mislabel them, just emphasise the first part. No separator: shown as-is.
function formatTrack(raw) {
  const m = (raw || '').match(/^(.*?)\s+[/–—-]\s+(.*)$/);
  if (m && m[1].trim() && m[2].trim()) {
    return `<span class="rc-tr-title">${escapeHtml(m[1].trim())}</span>`
      + `<span class="rc-tr-artist">${escapeHtml(m[2].trim())}</span>`;
  }
  return `<span class="rc-tr-title">${escapeHtml((raw || '').trim())}</span>`;
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
  return logoImgTag(cardStation(c), 'rc-logo');
}

function recentCardHTML(c, i, showBox) {
  const isRadio = c.source !== 'spotify';
  const sub = `<span class="rc-src">${escapeHtml(recentSourceLabel(c.source))}</span>`
    + (c.account ? ` &middot; ${escapeHtml(c.account)}` : '')
    + (showBox && c.boxName ? ` &middot; <span class="rc-box">${escapeHtml(c.boxName)}</span>` : '');
  const tracks = c.tracks.length
    ? `<div class="rc-tracks">` + c.tracks.map((tr) =>
        `<div class="rc-track"><span class="rc-tr-main">${formatTrack(tr.track)}</span>`
        + `<span class="rc-tr-time">${escapeHtml(recentClock(tr.ts))}</span></div>`).join('') + `</div>`
    : '';
  // Buttons identical to the radio search rows: play, save-to-preset, favourite.
  // Spotify is preset-only and not radio-favouritable, so it gets just the save
  // button (saved as a real Spotify preset).
  let actions;
  if (isRadio) {
    const fav = deps.isFav ? deps.isFav(cardStation(c)) : false;
    actions = `<button class="btn btn-mini rc-play" id="recPlay${i}" title="${escapeAttr(t('search.playNow'))}">&#9654;</button>`
      + `<button class="btn btn-mini rc-pick" id="recPick${i}" title="${escapeAttr(t('search.assignToKey'))}">&#10133;</button>`
      + `<button class="btn btn-mini rc-fav${fav ? ' is-fav' : ''}" id="recFav${i}" title="${escapeAttr(fav ? t('search.removeFav') : t('search.addFav'))}">${fav ? '&#9733;' : '&#9734;'}</button>`;
  } else {
    actions = `<button class="btn btn-mini rc-pick" id="recPick${i}" title="${escapeAttr(t('search.assignToKey'))}">&#10133;</button>`;
  }
  const nowPlaying = c.source === 'radio' && state.nowName && c.name && state.nowName === c.name;
  return `<div class="recent-card rc-${escapeAttr(c.source)}${nowPlaying ? ' rc-now' : ''}">`
    + `<div class="rc-head">${logoImg(c)}`
    + `<div class="rc-meta"><div class="rc-name">${escapeHtml(c.name || recentSourceLabel(c.source))}</div>`
    + `<div class="rc-sub">${sub}</div></div>`
    + `<div class="rc-actions">${actions}</div></div>${tracks}</div>`;
}

function wireCard(c, i) {
  const playBtn = document.getElementById('recPlay' + i);
  const pickBtn = document.getElementById('recPick' + i);
  const favBtn = document.getElementById('recFav' + i);
  if (playBtn) {
    playBtn.onclick = async () => {
      try { await deps.playStation(cardStation(c)); } catch (err) { showError(err); }
    };
  }
  if (pickBtn) {
    pickBtn.onclick = () => {
      if (c.source === 'spotify') return saveSpotifyCard(c);
      deps.openPick(cardStation(c)); // radio: identical to the search "+" action
    };
  }
  if (favBtn) {
    favBtn.onclick = () => {
      const nowFav = deps.toggleFav(cardStation(c));
      favBtn.classList.toggle('is-fav', nowFav);
      favBtn.innerHTML = nowFav ? '&#9733;' : '&#9734;';
      favBtn.title = nowFav ? t('search.removeFav') : t('search.addFav');
    };
  }
}

function saveSpotifyCard(c) {
  const box = c.box || state.currentBox;
  if (!box || !deps.showSlotPicker) return;
  deps.showSlotPicker({
    title: t('recent.saveTitle'),
    subtitle: c.name || '',
    onPick: async (i) => {
      await SaveSpotifyPreset(box.host, box.port, i, c.name || '', c.url, c.account || '');
      showToast(t('recent.saved', { name: c.name || '' }));
    },
  });
}

// renderRecent paints the view into #view-recent. Called from main.js's
// switchView when the Recently-played tab is opened.
export async function renderRecent() {
  const root = $('view-recent');
  if (!root) return;
  const multi = recentStrBoxes().length > 1;
  const showAll = multi && !!state.recentAllBoxes;
  let html = `<div class="recent-head"><h2 class="recent-title">${escapeHtml(t('recent.title'))}</h2>`;
  if (multi) {
    html += `<div class="recent-scope">`
      + `<button class="chip${showAll ? '' : ' active'}" id="recentThisBox">${escapeHtml(t('recent.thisBox'))}</button>`
      + `<button class="chip${showAll ? ' active' : ''}" id="recentAllBoxes">${escapeHtml(t('recent.allBoxes'))}</button>`
      + `</div>`;
  }
  html += `</div><div class="recent-sub muted">${escapeHtml(t('recent.subtitle'))}</div>`
    + `<div class="recent-list" id="recentList"><div class="muted recent-loading">${escapeHtml(t('recent.loading'))}</div></div>`;
  root.innerHTML = html;

  const tb = $('recentThisBox'), ab = $('recentAllBoxes');
  if (tb) tb.onclick = () => { state.recentAllBoxes = false; renderRecent(); };
  if (ab) ab.onclick = () => { state.recentAllBoxes = true; renderRecent(); };

  const cards = await loadRecentCards();
  const listEl = $('recentList');
  if (!listEl || state.view !== 'recent') return; // navigated away mid-fetch
  if (!cards.length) {
    listEl.innerHTML = `<div class="recent-empty">${escapeHtml(t('recent.empty'))}</div>`;
    return;
  }
  listEl.innerHTML = cards.map((c, i) => recentCardHTML(c, i, showAll)).join('');
  cards.forEach((c, i) => wireCard(c, i));
}
