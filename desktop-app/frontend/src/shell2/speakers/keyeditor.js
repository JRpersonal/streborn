// shell2/speakers/keyeditor.js: the key assignment editor on a speaker's Keys
// and alarms tab in the UI 2.0 preview.
//
// Until now a key got its station where the station was: a long press on a
// tile of Radio, the key button of a Library row, the Spotify view's card for
// the playlist that plays. The editor puts the speaker's keys side by side with
// a source browser, so the whole job happens in one place:
//
//   - the keys of the speaker on the left (both rows on a speaker that keeps a
//     second one), one of them chosen, with what it holds and what a save
//     would replace;
//   - "Put what's playing on key N" under them, for every source the speaker
//     agent can keep: a station or a library song or folder through the agent's
//     own record of the play (POST /api/box/save-playing), Spotify and Pandora
//     or iHeartRadio through their own saves (the agent answers 422 for those);
//   - on the right three tabs: Radio (the favourites, and a station search),
//     Library (the media servers, folder by folder: a folder or a song onto the
//     key) and Spotify (the playlists, albums and artists the speaker played
//     lately).
//
// Every outcome is said in the editor's own line, named with the key and the
// speaker (docs/UI-2.0.md rules 1 to 3): "putting it on key 3" while it is out,
// done only after the speaker's preset list read back holds it, the agent's
// 409 already-on-slot as the note plus "Move it to key 3" right there (no
// dialog), and a 422 in plain words. Nothing here is a toast.
//
// The editor sits in its own slot of the Keys panel (speaker.js), outside the
// classic settings body, so a reload of the settings never takes it down. It
// acts on the page's speaker, which is always the speaker you control
// (lock-step, speakers/index.js). The music views' save paths it reuses come in
// through deps (main.js), so this module pulls none of them in at import.

import './keyeditor.css';
import { t } from '../../i18n/index.js';
import { state } from '../../state.js';
import { escapeAttr, escapeHtml, savePlayingFallsThrough } from '../../utils.js';
import { sameBoxIdentity } from '../../groups.js';
import { keyNum } from '../../presetrows.js';
import { nativeServiceLabel } from '../../nativeservice.js';
import { libraryTrackRef, saveLibraryTrackPreset } from '../../librarytrackref.js';
import {
  BrowseLibrary, GetPresets, ListMediaServers, MovePreset, RadioSearch, RecentPlayed,
  SaveFolderPreset, SaveNativePreset, SavePlayingPreset, SaveSpotifyPreset,
} from '../../api.js';
import { keyFailedVerdict, keyHolds, keySavingVerdict, keyVerdict } from '../feedback.js';
import { errorText } from './sectionmsg.js';
import { tabIndexFor } from './routes.js';
import {
  EDITOR_SOURCES, SOURCE_LABEL_KEYS, defaultSlot, editorKeys, folderPayload, keyHintText,
  nowPlayingWhat, radioStations, savePlan, saveOutcome, spotifyRecentItems,
} from './keyeditormodel.js';

// The editor's model, about one speaker at a time.
const fresh = () => ({
  host: '',
  port: 0,
  presets: null,
  keysBusy: false,
  keysErr: '',
  slot: 0,
  busy: false,
  result: null,
  radio: { q: '', shownQ: '', busy: false, hits: null, err: '' },
  lib: { servers: null, busy: false, err: '', udn: '', stack: [], page: null, pageErr: '' },
  spotify: { items: null, busy: false, err: '' },
});

let ed = fresh();
let source = EDITOR_SOURCES[0];
let root = null;
let deps = {};
let speaker = '';
let box = null;
let sig = '';
// Bumped on every switch of speaker: an answer for the previous one is dropped.
let gen = 0;

const L = (k, v) => t(`ui2.speakers.keyEditor.${k}`, v);
const n = () => keyNum(ed.slot, t);
const same = (b) => !!b && !!box && b.host === box.host && b.port === box.port;

// paintKeyEditor draws the editor into el for ctx.box (the page's speaker),
// named ctx.name. Called on every repaint of the page; it redraws only when
// something it shows changed. ctx.deps are the music views' save paths;
// ctx.reread asks for the keys again (the tab or the page was just opened).
export function paintKeyEditor(el, ctx = {}) {
  if (!el) return;
  // A rebuilt page brings an empty slot: draw it whatever the last picture was.
  const moved = el !== root || !el.firstChild;
  root = el;
  if (ctx.deps) deps = ctx.deps;
  speaker = ctx.name || '';
  const b = ctx.box || null;
  if (!b || !b.host) {
    box = null;
    if (el.innerHTML) el.innerHTML = '';
    sig = '';
    return;
  }
  if (!ed.host || b.host !== ed.host || b.port !== ed.port) {
    gen++;
    const keepQ = ed.radio.q;
    ed = fresh();
    ed.host = b.host;
    ed.port = b.port;
    ed.radio.q = keepQ;
    box = b;
    loadKeys();
    ensureSource();
  } else if (ctx.reread && !ed.busy) {
    loadKeys();
  }
  box = b;
  paint(moved);
}

// resetKeyEditor forgets the speaker (tests, and a page without a speaker).
export function resetKeyEditor() {
  ed = fresh();
  source = EDITOR_SOURCES[0];
  sig = '';
  gen++;
}

function paint(force = false) {
  if (!root || !box) return;
  const what = nowPlayingWhat(sameBoxIdentity(state.currentBox, box) ? state : {}, L('nowUnnamed'));
  // The favourites live in this computer's storage and change on Radio.
  const s = JSON.stringify([speaker, ed, source, what, source === 'radio' ? favStations().length : 0]);
  if (!force && s === sig) return;
  sig = s;
  const active = typeof document !== 'undefined' ? document.activeElement : null;
  const keep = active && root.contains(active) && active.dataset ? active.dataset.kf || '' : '';
  root.innerHTML = editorHTML(what);
  if (keep) {
    // A control that went with the repaint (Move, once the move is done)
    // hands the keyboard to the chosen key, the one the outcome is about.
    const again = root.querySelector(`[data-kf="${cssEscape(keep)}"]`)
      || root.querySelector(`[data-kf="key-${ed.slot}"]`);
    if (again && typeof again.focus === 'function') again.focus();
  }
  wire();
}

function cssEscape(s) {
  return String(s).replace(/["\\]/g, '\\$&');
}

// ---------- markup ----------

function editorHTML(what) {
  const keys = ed.presets ? editorKeys(box, ed.presets, t) : [];
  return `
    <section class="u-card kae" aria-labelledby="kaeTitle">
      <h3 class="kae-title" id="kaeTitle">${escapeHtml(L('title', { name: speaker }))}</h3>
      <p class="kae-intro">${escapeHtml(L('intro'))}</p>
      <div class="kae-grid">
        <div class="kae-keys">
          <h4 class="kae-sub" id="kaeKeysLabel">${escapeHtml(L('keysLabel', { name: speaker }))}</h4>
          ${keysHTML(keys)}
          ${ed.presets ? `<p class="kae-hint" id="kaeHint">${escapeHtml(keyHintText(keys, ed.slot, t))}</p>` : ''}
          <div class="kae-now">
            <p class="kae-now-text">${escapeHtml(what ? L('nowPlays', { name: speaker, what }) : L('nowNothing', { name: speaker }))}</p>
            <button type="button" class="u-btn u-btn--primary kae-now-put" id="kaeSaveNow" data-kf="savenow"${busyAttr()}${ed.presets ? ' aria-describedby="kaeHint"' : ''}>${escapeHtml(ed.slot ? L('nowPut', { n: n() }) : L('nowPutNoKey'))}</button>
          </div>
          ${resultHTML()}
        </div>
        <div class="kae-src">
          <div class="kae-tabs" role="tablist" aria-label="${escapeAttr(L('srcLabel'))}">${tabsHTML()}</div>
          <div class="kae-panel" role="tabpanel" id="kaePanel" aria-labelledby="kaeTab-${source}" tabindex="0">${panelHTML()}</div>
        </div>
      </div>
    </section>`;
}

const busyAttr = () => (ed.busy || !ed.slot ? ' aria-disabled="true"' : '');

function keysHTML(keys) {
  if (ed.keysErr) {
    return `<div class="kae-state kae-state--error" role="alert"><p>${escapeHtml(L('keysFailed', { name: speaker, detail: ed.keysErr }))}</p>`
      + `<button type="button" class="u-btn u-btn--secondary kae-keys-retry" data-kf="keysretry">${escapeHtml(t('ui2.speakers.speaker.tryAgain'))}</button></div>`;
  }
  if (!ed.presets) return `<p class="kae-state" role="status">${escapeHtml(L('keysLoading', { name: speaker }))}</p>`;
  const rows = [1, 2].map((row) => {
    const list = keys.filter((k) => k.row === row);
    if (!list.length) return '';
    const head = row === 2 ? `<p class="kae-row-label">${escapeHtml(L('rowTwice'))}</p>` : '';
    return head + `<div class="kae-keylist">${list.map((k) => {
      const on = k.slot === ed.slot;
      return `<button type="button" class="kae-key${on ? ' is-chosen' : ''}${k.empty ? ' is-empty' : ''}" data-slot="${k.slot}" data-kf="key-${k.slot}" aria-pressed="${on}">`
        + `<span class="kae-key-n">${escapeHtml(k.label)}</span>`
        + `<span class="kae-key-name">${escapeHtml(k.empty ? L('keyEmpty') : k.name)}</span></button>`;
    }).join('')}</div>`;
  }).join('');
  return `<div class="kae-keyrows" role="group" aria-labelledby="kaeKeysLabel">${rows}</div>`;
}

function resultHTML() {
  const r = ed.result;
  const tone = r ? r.tone : '';
  const move = r && r.move
    ? `<button type="button" class="u-btn u-btn--secondary kae-move" data-kf="move"${busyAttr()}>${escapeHtml(t('preset.moveButton', { n: keyNum(r.move.to, t) }))}</button>`
    : '';
  // The line is always there, so a screen reader hears it change.
  return `<div class="kae-result${tone ? ` kae-result--${escapeAttr(tone)}` : ''}" id="kaeResult" role="status" aria-live="polite">`
    + `${r ? `<span class="kae-result-text">${escapeHtml(r.text)}</span>${move}` : ''}</div>`;
}

function tabsHTML() {
  return EDITOR_SOURCES.map((s) => {
    const on = s === source;
    return `<button type="button" role="tab" class="kae-tab" id="kaeTab-${s}" data-src="${s}" data-kf="tab-${s}" aria-controls="kaePanel" aria-selected="${on}" tabindex="${on ? 0 : -1}">${escapeHtml(t(SOURCE_LABEL_KEYS[s]))}</button>`;
  }).join('');
}

function panelHTML() {
  if (source === 'library') return libraryHTML();
  if (source === 'spotify') return spotifyHTML();
  return radioHTML();
}

// putButton is the one action of a source row: its words say the key, its name
// what goes on which key of which speaker.
function putButton(kind, i, what, words = '') {
  const label = ed.slot ? L('putName', { what, n: n(), name: speaker }) : `${L('putNoKey')}: ${what}`;
  const text = ed.slot ? (words || L('put', { n: n() })) : L('putNoKey');
  return `<button type="button" class="u-btn u-btn--secondary kae-put" data-kind="${kind}" data-i="${i}" data-kf="put-${kind}-${i}" aria-label="${escapeAttr(label)}" title="${escapeAttr(label)}"${busyAttr()}>${escapeHtml(text)}</button>`;
}

function itemHTML(name, sub, button) {
  return `<li class="kae-item"><span class="kae-item-text"><span class="kae-item-name">${escapeHtml(name)}</span>`
    + `${sub ? `<span class="kae-item-sub">${escapeHtml(sub)}</span>` : ''}</span>${button}</li>`;
}

function stateLine(text, tone = 'info') {
  return `<p class="kae-state${tone === 'error' ? ' kae-state--error' : ''}" role="${tone === 'error' ? 'alert' : 'status'}">${escapeHtml(text)}</p>`;
}

function radioHTML() {
  const r = ed.radio;
  const form = `<form class="kae-search" role="search" id="kaeRadioForm">`
    + `<label class="kae-label" for="kaeRadioQ">${escapeHtml(L('radioSearchLabel'))}</label>`
    + `<span class="kae-search-row"><input type="search" class="kae-input" id="kaeRadioQ" data-kf="radioq" value="${escapeAttr(r.q)}" autocomplete="off">`
    + `<button type="submit" class="u-btn u-btn--secondary" data-kf="radiogo">${escapeHtml(L('search'))}</button></span></form>`;
  let body;
  if (r.busy) body = stateLine(L('searching'));
  else if (r.err) body = stateLine(L('searchFailed', { detail: r.err }), 'error');
  else if (r.hits) {
    body = `<h4 class="kae-sub">${escapeHtml(L('radioHits', { q: r.shownQ }))}</h4>`
      + (r.hits.length
        ? `<ul class="kae-items">${r.hits.map((s, i) => itemHTML(s.name, stationSub(s), putButton('hit', i, s.name))).join('')}</ul>`
        : stateLine(L('radioNoHits', { q: r.shownQ })))
      + `<button type="button" class="u-btn u-btn--secondary kae-favs" data-kf="favs">${escapeHtml(L('radioShowFavs'))}</button>`;
  } else {
    const favs = favStations();
    body = `<h4 class="kae-sub">${escapeHtml(t('ui2.radio.favorites'))}</h4>`
      + (favs.length
        ? `<ul class="kae-items">${favs.map((s, i) => itemHTML(s.name, stationSub(s), putButton('fav', i, s.name))).join('')}</ul>`
        : stateLine(L('radioNoFavs')));
  }
  return form + body;
}

function stationSub(s) {
  return [s.countrycode, s.codec, s.bitrate ? `${s.bitrate} kbit/s` : ''].filter(Boolean).join(' · ');
}

function favStations() {
  let store = [];
  try { store = typeof deps.loadFavStore === 'function' ? deps.loadFavStore() : []; } catch { store = []; }
  return radioStations(store);
}

function libraryHTML() {
  const l = ed.lib;
  if (l.busy) return stateLine(L('libLoading'));
  if (l.err) {
    return `<div class="kae-state kae-state--error" role="alert"><p>${escapeHtml(L('libFailed', { detail: l.err }))}</p>`
      + `<button type="button" class="u-btn u-btn--secondary kae-lib-retry" data-kf="libretry">${escapeHtml(t('ui2.speakers.speaker.tryAgain'))}</button></div>`;
  }
  if (!l.servers) return stateLine(L('libLoading'));
  if (!l.udn) {
    if (!l.servers.length) return stateLine(L('libNone'));
    return `<h4 class="kae-sub">${escapeHtml(L('libServers'))}</h4><ul class="kae-items">${l.servers.map((s, i) => {
      const name = s.friendlyName || s.address || '';
      return `<li class="kae-item"><button type="button" class="kae-open" data-srv="${i}" data-kf="srv-${i}">`
        + `<span class="kae-item-name">${escapeHtml(name)}</span><span class="kae-chev" aria-hidden="true">&rsaquo;</span></button></li>`;
    }).join('')}</ul>`;
  }
  const top = l.stack[l.stack.length - 1] || { title: '' };
  const up = l.stack.length > 1 ? l.stack[l.stack.length - 2].title : L('libServers');
  const back = `<button type="button" class="u-btn u-btn--secondary kae-back" data-kf="libback" aria-label="${escapeAttr(t('ui2.library.backTo', { name: up }))}">${escapeHtml(t('ui2.library.back'))}</button>`;
  const head = `<div class="kae-lib-head">${back}<h4 class="kae-sub kae-lib-title">${escapeHtml(top.title)}</h4></div>`;
  if (l.pageErr) return head + stateLine(L('libFailed', { detail: l.pageErr }), 'error');
  if (!l.page) return head + stateLine(L('libLoading'));
  const folders = (l.page.containers || []).map((c, i) => `<li class="kae-item"><button type="button" class="kae-open" data-cid="${i}" data-kf="cid-${i}">`
    + `<span class="kae-item-name">${escapeHtml(c.title || '')}</span><span class="kae-chev" aria-hidden="true">&rsaquo;</span></button></li>`).join('');
  const songs = (l.page.items || []).filter((it) => it && it.streamURL);
  const tracks = songs.map((it, i) => itemHTML(it.title || '', [it.artist, it.album].filter(Boolean).join(' · '), putButton('track', i, it.title || ''))).join('');
  const folderAct = l.stack.length > 1
    ? (songs.length
      ? `<div class="kae-folder-act">${putButton('folder', 0, top.title, L('putFolder', { n: n() }))}</div>`
      : `<p class="kae-note">${escapeHtml(L('libNoSongs'))}</p>`)
    : '';
  const list = folders || tracks ? `<ul class="kae-items">${folders}${tracks}</ul>` : stateLine(L('libEmpty'));
  return head + folderAct + list;
}

function spotifyHTML() {
  const sp = ed.spotify;
  const intro = `<p class="kae-note">${escapeHtml(L('spotifyIntro', { name: speaker }))}</p>`;
  if (sp.busy || !sp.items) return intro + stateLine(L('spotifyLoading', { name: speaker }));
  if (sp.err) return intro + stateLine(L('spotifyFailed', { name: speaker, detail: sp.err }), 'error');
  if (!sp.items.length) return intro + stateLine(L('spotifyNone', { name: speaker }));
  return `${intro}<ul class="kae-items">${sp.items.map((it, i) => itemHTML(it.name, '', putButton('spotify', i, it.name))).join('')}</ul>`;
}

// ---------- wiring ----------

function wire() {
  root.querySelectorAll('.kae-key').forEach((b) => b.addEventListener('click', () => {
    if (ed.busy) return;
    ed.slot = Number(b.dataset.slot) || ed.slot;
    ed.result = null;
    paint();
  }));
  const retry = root.querySelector('.kae-keys-retry');
  if (retry) retry.addEventListener('click', () => loadKeys());
  const now = root.querySelector('#kaeSaveNow');
  if (now) now.addEventListener('click', () => { if (!ed.busy) saveWhatPlays(); });
  const move = root.querySelector('.kae-move');
  if (move) move.addEventListener('click', () => { if (!ed.busy) moveHere(); });
  const tabs = root.querySelector('.kae-tabs');
  if (tabs) {
    tabs.addEventListener('click', (e) => {
      const b = e.target.closest('.kae-tab');
      if (b) pickSource(b.dataset.src);
    });
    tabs.addEventListener('keydown', (e) => {
      const b = e.target.closest('.kae-tab');
      if (!b) return;
      const list = [...tabs.querySelectorAll('.kae-tab')];
      const rtl = getComputedStyle(tabs).direction === 'rtl';
      const to = tabIndexFor(e.key, list.indexOf(b), list.length, rtl);
      if (to < 0) return;
      e.preventDefault();
      pickSource(list[to].dataset.src, true);
    });
  }
  const panel = root.querySelector('#kaePanel');
  if (!panel) return;
  const form = panel.querySelector('#kaeRadioForm');
  if (form) {
    const q = form.querySelector('#kaeRadioQ');
    // Kept as typed, so a repaint does not throw the words away.
    if (q) q.addEventListener('input', () => { ed.radio.q = q.value; sig = ''; });
    form.addEventListener('submit', (e) => { e.preventDefault(); searchRadio(q ? q.value : ''); });
  }
  panel.addEventListener('click', (e) => {
    const put = e.target.closest('.kae-put');
    if (put) { if (!ed.busy) putItem(put.dataset.kind, Number(put.dataset.i)); return; }
    const srv = e.target.closest('.kae-open[data-srv]');
    if (srv) { openServer(Number(srv.dataset.srv)); return; }
    const cid = e.target.closest('.kae-open[data-cid]');
    if (cid) { openFolder(Number(cid.dataset.cid)); return; }
    if (e.target.closest('.kae-back')) { libBack(); return; }
    if (e.target.closest('.kae-lib-retry')) { loadServers(); return; }
    if (e.target.closest('.kae-favs')) {
      ed.radio = { ...ed.radio, q: '', hits: null, err: '', shownQ: '' };
      paint();
    }
  });
}

function pickSource(src, focus = false) {
  if (!EDITOR_SOURCES.includes(src)) return;
  source = src;
  ensureSource();
  paint();
  if (focus) {
    const tab = root && root.querySelector(`#kaeTab-${src}`);
    if (tab) tab.focus();
  }
}

// ensureSource reads what the open source tab lists, the first time it is
// shown for this speaker.
function ensureSource() {
  if (source === 'library' && !ed.lib.servers && !ed.lib.busy && !ed.lib.err) loadServers();
  if (source === 'spotify' && !ed.spotify.items && !ed.spotify.busy && !ed.spotify.err) loadSpotify();
}

// ---------- reads ----------

async function loadKeys() {
  const g = gen;
  const b = { host: ed.host, port: ed.port };
  ed.keysBusy = true;
  ed.keysErr = '';
  paint();
  try {
    const list = await GetPresets(b.host, b.port);
    if (g !== gen) return;
    ed.presets = Array.isArray(list) ? list : [];
    ed.slot = defaultSlot(editorKeys(box, ed.presets, t), ed.slot);
  } catch (err) {
    if (g !== gen) return;
    ed.keysErr = errorText(err);
  } finally {
    if (g === gen) {
      ed.keysBusy = false;
      paint();
    }
  }
}

async function searchRadio(q) {
  const query = String(q || '').trim();
  ed.radio.q = query;
  if (!query) {
    ed.radio = { ...ed.radio, hits: null, err: '', shownQ: '' };
    paint();
    return;
  }
  const g = gen;
  ed.radio = { ...ed.radio, busy: true, err: '', hits: null, shownQ: query };
  paint();
  try {
    const hits = await RadioSearch({ q: query, limit: 30, order: 'votes', top: false, onlyok: true });
    if (g !== gen) return;
    ed.radio.hits = (Array.isArray(hits) ? hits : []).filter((s) => s && s.name && (s.url_resolved || s.url));
  } catch (err) {
    if (g !== gen) return;
    ed.radio.err = errorText(err);
  } finally {
    if (g === gen) {
      ed.radio.busy = false;
      paint();
    }
  }
}

async function loadServers() {
  const g = gen;
  ed.lib = { ...ed.lib, busy: true, err: '' };
  paint();
  try {
    const list = await ListMediaServers(3);
    if (g !== gen) return;
    ed.lib.servers = Array.isArray(list) ? list : [];
  } catch (err) {
    if (g !== gen) return;
    ed.lib.err = errorText(err);
  } finally {
    if (g === gen) {
      ed.lib.busy = false;
      paint();
    }
  }
}

function openServer(i) {
  const srv = (ed.lib.servers || [])[i];
  if (!srv) return;
  ed.lib.udn = srv.udn;
  ed.lib.stack = [{ id: '0', title: srv.friendlyName || srv.address || '' }];
  browse(true);
}

function openFolder(i) {
  const c = ((ed.lib.page && ed.lib.page.containers) || [])[i];
  if (!c) return;
  ed.lib.stack = [...ed.lib.stack, { id: String(c.id), title: c.title || '' }];
  browse(true);
}

function libBack() {
  if (ed.lib.stack.length > 1) {
    ed.lib.stack = ed.lib.stack.slice(0, -1);
    browse(true);
    return;
  }
  ed.lib = { ...ed.lib, udn: '', stack: [], page: null, pageErr: '' };
  paint();
  focusIn('.kae-open');
}

async function browse(focusHead) {
  const g = gen;
  const top = ed.lib.stack[ed.lib.stack.length - 1];
  const at = ed.lib.stack.length;
  ed.lib.page = null;
  ed.lib.pageErr = '';
  paint();
  if (focusHead) focusIn('.kae-back');
  try {
    const page = await BrowseLibrary(ed.lib.udn, top.id, 0, 200);
    if (g !== gen || ed.lib.stack.length !== at) return;
    ed.lib.page = page || { containers: [], items: [] };
  } catch (err) {
    if (g !== gen || ed.lib.stack.length !== at) return;
    ed.lib.pageErr = errorText(err);
  }
  paint();
}

async function loadSpotify() {
  const g = gen;
  const b = { host: ed.host, port: ed.port };
  ed.spotify = { items: null, busy: true, err: '' };
  paint();
  try {
    const recent = await RecentPlayed(b.host, b.port);
    if (g !== gen) return;
    ed.spotify.items = spotifyRecentItems(recent);
  } catch (err) {
    if (g !== gen) return;
    ed.spotify.items = [];
    ed.spotify.err = errorText(err);
  } finally {
    if (g === gen) {
      ed.spotify.busy = false;
      paint();
    }
  }
}

function focusIn(sel) {
  const el = root && root.querySelector(`#kaePanel ${sel}`);
  if (el && typeof el.focus === 'function') el.focus();
}

// ---------- saves ----------

// runSave is every write of the editor: the busy line, the save itself, and
// done only once the preset list read back from the speaker holds it.
// doSave(b, slot) resolves { name } of what it stored (name may be ''), or
// { result } when it ended with its own line.
async function runSave(doSave) {
  if (ed.busy || !box || !ed.slot) return;
  const b = box;
  const g = gen;
  const slot = ed.slot;
  const num = keyNum(slot, t);
  const spk = speaker;
  ed.busy = true;
  ed.result = { tone: 'busy', text: keySavingVerdict(num, spk).text };
  paint();
  let result;
  try {
    const out = (await doSave(b, slot)) || {};
    result = out.result || (await confirmKey(b, slot, out.name || '', out.notes || []));
  } catch (err) {
    result = saveOutcome(err, slot, t, spk)
      || { tone: 'error', text: keyFailedVerdict(num, spk, errorText(err)).text };
  }
  if (g !== gen) return;
  ed.busy = false;
  ed.result = result;
  paint();
}

// confirmKey reads the speaker's keys back and says done only when the key
// holds it, under the name it was stored with when that is known.
async function confirmKey(b, slot, name, notes = []) {
  const num = keyNum(slot, t);
  let list = null;
  try { list = await GetPresets(b.host, b.port); } catch { list = null; }
  if (Array.isArray(list) && same(b)) ed.presets = list;
  // The music views show the same keys: keep them current when they are this
  // speaker's.
  if (sameBoxIdentity(state.currentBox, b) && typeof deps.loadPresets === 'function') {
    try { await deps.loadPresets(); } catch { /* the editor's own read decides */ }
  }
  const v = keyVerdict(num, speaker, keyHolds(list, slot, name));
  const text = [v.text, ...notes].join(' ');
  return { tone: notes.length && v.kind === 'ok' ? 'warn' : v.kind, text };
}

function putItem(kind, i) {
  if (kind === 'fav' || kind === 'hit') {
    const s = (kind === 'fav' ? favStations() : ed.radio.hits || [])[i];
    if (!s) return;
    runSave(async (b, slot) => {
      await deps.putStationOnKey(b, slot, s);
      return { name: s.name };
    });
    return;
  }
  if (kind === 'track') {
    const songs = ((ed.lib.page && ed.lib.page.items) || []).filter((it) => it && it.streamURL);
    const it = songs[i];
    if (!it) return;
    const srv = (ed.lib.servers || []).find((x) => x.udn === ed.lib.udn);
    const sourceName = (srv && (srv.friendlyName || srv.address)) || '';
    runSave(async (b, slot) => {
      const name = it.title || '(track)';
      await saveLibraryTrackPreset(b.host, b.port, slot, {
        name, url: it.streamURL, art: it.albumArtURL || '', source: sourceName,
        durationSec: it.durationSec || 0, ref: libraryTrackRef(it, ed.lib.udn),
        artist: it.artist || '', album: it.album || '',
      });
      return { name };
    });
    return;
  }
  if (kind === 'folder') {
    const top = ed.lib.stack[ed.lib.stack.length - 1];
    const srv = (ed.lib.servers || []).find((x) => x.udn === ed.lib.udn);
    const sourceName = (srv && (srv.friendlyName || srv.address)) || '';
    const made = folderPayload({
      name: top && top.title, source: sourceName, udn: ed.lib.udn, containerId: top && top.id,
      items: (ed.lib.page && ed.lib.page.items) || [],
    });
    if (!made) return;
    runSave(async (b, slot) => {
      await SaveFolderPreset(b.host, b.port, slot, JSON.stringify(made.payload));
      const notes = made.kept < made.total ? [L('folderCapped', { kept: made.kept, total: made.total })] : [];
      return { name: made.payload.name, notes };
    });
    return;
  }
  if (kind === 'spotify') {
    const it = (ed.spotify.items || [])[i];
    if (!it) return;
    runSave(async (b, slot) => {
      await SaveSpotifyPreset(b.host, b.port, slot, it.name, it.uri, it.account);
      return { name: it.name };
    });
  }
}

// saveWhatPlays keeps what the speaker plays on the chosen key, for every
// source the agent can keep (savePlan): the agent's own record of the play,
// or the Spotify and Pandora / iHeartRadio saves the agent leaves to the app.
function saveWhatPlays() {
  runSave(async (b, slot) => {
    // What plays now, not what the last poll saw.
    try { if (typeof deps.refreshStatus === 'function') await deps.refreshStatus(); } catch { /* the agent reads the speaker itself */ }
    if (!sameBoxIdentity(state.currentBox, b)) return { result: { tone: 'error', text: t('ui2.spotify.speakerChanged', { name: speaker }) } };
    const plan = savePlan(state);
    if (plan === 'spotify') return saveSpotifyPlaying(b, slot);
    if (plan === 'native') {
      const name = state.nowName || nativeServiceLabel(state.nowSource);
      await SaveNativePreset(b.host, b.port, slot, name);
      return { name };
    }
    try {
      const saved = (await SavePlayingPreset(b.host, b.port, slot)) || {};
      return { name: saved.name || '' };
    } catch (err) {
      if (!savePlayingFallsThrough(err) || typeof deps.saveCurrentToSlot !== 'function') throw err;
    }
    // An agent (or an app build) without save-playing: the app's own save of
    // what plays, which reports through the same kind of line.
    let last = null;
    await deps.saveCurrentToSlot(slot, (text, kind) => { last = text == null ? null : { tone: kind, text }; });
    return { result: last || { tone: 'warn', text: keyVerdict(keyNum(slot, t), speaker, false).text } };
  });
}

// saveSpotifyPlaying is the Spotify case: the Spotify view's own save of the
// playing playlist (the live context read, the login and plan notices).
async function saveSpotifyPlaying(b, slot) {
  if (typeof deps.saveSpotifyToSlot !== 'function') return { result: { tone: 'error', text: keyFailedVerdict(keyNum(slot, t), speaker, t('modal.unknownError')).text } };
  const notes = [];
  let failure = null;
  let note = '';
  const ok = await deps.saveSpotifyToSlot(slot, {
    saved: () => {},
    notice: (k) => notes.push(t(k)),
    error: (k) => { note = t(k); },
    failed: (err) => { failure = err; },
    switched: () => { note = t('ui2.spotify.speakerChanged', { name: speaker }); },
  }, b);
  if (failure) throw failure;
  if (note) return { result: { tone: 'warn', text: note } };
  if (!ok) return { result: { tone: 'warn', text: keyVerdict(keyNum(slot, t), speaker, false).text } };
  return { name: '', notes };
}

// moveHere takes what the speaker refused to store twice off its other key
// and onto the chosen one, the way out of a 409 already-on-slot.
function moveHere() {
  const m = ed.result && ed.result.move;
  if (!m || m.to !== ed.slot) return;
  runSave(async (b) => {
    await MovePreset(b.host, b.port, m.from, m.to);
    return { name: m.name || '' };
  });
}
