// views/recent.js — the "Recently played" view (#135).
//
// First view extracted out of the monolithic main.js: a self-contained module
// that pulls everything it needs from the shared modules (state, utils, i18n,
// api). The only main.js-local dependency is the slot-picker modal, injected via
// setRecentSlotPicker so this file does not have to import back into main.js
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
import {
  RecentPlayed,
  PlayURL,
  SetPreset,
  SaveSpotifyPreset,
  SaveLibraryPreset,
} from '../api.js';

// showSlotPicker lives in main.js (the shared modal). Injected once at startup
// so the Save-as-preset action can reuse the same picker as the rest of the app.
let showSlotPicker = null;
export function setRecentSlotPicker(fn) {
  showSlotPicker = fn;
}

// recentStrBoxes returns the discovered STR speakers (skip unflashed stock ones).
function recentStrBoxes() {
  return (state.boxes || []).filter((b) => b && b.kind !== 'stock' && b.host);
}

// groupRecentCards turns a newest-first, box-tagged entry list into source cards:
// a card is a contiguous run of the same (box, cardKey). Tracks within stay
// newest-first; an empty-track placeholder row just yields a card with no tracks.
function groupRecentCards(entries) {
  const cards = [];
  for (const e of entries) {
    const last = cards[cards.length - 1];
    if (last && last.boxKey === e._boxKey && last.cardKey === e.cardKey) {
      if (e.track) last.tracks.push({ track: e.track, ts: e.ts });
      continue;
    }
    cards.push({
      boxKey: e._boxKey, box: e._box, boxName: e._boxName,
      source: e.source, cardKey: e.cardKey, name: e.cardName,
      art: e.cardArt, url: e.cardURL, account: e.account, ts: e.ts,
      tracks: e.track ? [{ track: e.track, ts: e.ts }] : [],
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

function recentCardHTML(c, i, showBox) {
  const logo = c.art
    ? `<img class="rc-logo" src="${escapeAttr(c.art)}" alt="" onerror="this.style.visibility='hidden'">`
    : `<div class="rc-logo rc-logo-ph"></div>`;
  const canPlay = c.source !== 'spotify'; // no ad-hoc Spotify play yet (preset-only)
  const sub = `<span class="rc-src">${escapeHtml(recentSourceLabel(c.source))}</span>`
    + (c.account ? ` &middot; ${escapeHtml(c.account)}` : '')
    + (showBox && c.boxName ? ` &middot; <span class="rc-box">${escapeHtml(c.boxName)}</span>` : '');
  const tracks = c.tracks.length
    ? `<div class="rc-tracks">` + c.tracks.map((tr) =>
        `<div class="rc-track"><span class="rc-tr-name">${escapeHtml(tr.track)}</span>`
        + `<span class="rc-tr-time">${escapeHtml(recentClock(tr.ts))}</span></div>`).join('') + `</div>`
    : '';
  return `<div class="recent-card rc-${escapeAttr(c.source)}">`
    + `<div class="rc-head">${logo}`
    + `<div class="rc-meta"><div class="rc-name">${escapeHtml(c.name || recentSourceLabel(c.source))}</div>`
    + `<div class="rc-sub">${sub}</div></div>`
    + `<div class="rc-actions">`
    + (canPlay ? `<button class="btn btn-mini btn-primary" id="recPlay${i}" title="${escapeAttr(t('recent.play'))}">&#9654;</button>` : '')
    + `<button class="btn btn-mini" id="recSave${i}" title="${escapeAttr(t('recent.save'))}">&#9733;</button>`
    + `</div></div>${tracks}</div>`;
}

async function recentPlayCard(c) {
  if (!c.box || c.source === 'spotify') return;
  try {
    await PlayURL(c.box.host, c.box.port, c.url, c.name || '', c.art || '', '', '');
    showToast(t('recent.playing', { name: c.name || '' }));
  } catch (err) {
    showError(err);
  }
}

function recentSaveCard(c) {
  const box = c.box || state.currentBox;
  if (!box || !showSlotPicker) return;
  showSlotPicker({
    title: t('recent.saveTitle'),
    subtitle: c.name || '',
    onPick: async (i) => {
      if (c.source === 'spotify') {
        await SaveSpotifyPreset(box.host, box.port, i, c.name || '', c.url, c.account || '');
      } else if (c.source === 'upnp') {
        await SaveLibraryPreset(box.host, box.port, i, c.name || '', c.url, c.art || '', 0, recentSourceLabel('upnp'));
      } else {
        await SetPreset(box.host, box.port, i, c.name || '', c.url, c.art || '', 0);
      }
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
  cards.forEach((c, i) => {
    const playBtn = document.getElementById('recPlay' + i);
    const saveBtn = document.getElementById('recSave' + i);
    if (playBtn) playBtn.onclick = () => recentPlayCard(c);
    if (saveBtn) saveBtn.onclick = () => recentSaveCard(c);
  });
}
