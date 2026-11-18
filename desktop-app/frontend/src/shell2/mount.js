// shell2/mount.js: builds the UI 2.0 preview shell around the existing app.
//
// The shell is a new frame, not a new app: the eight views, the status bar,
// the transport, the volume and the group controls are the SAME nodes the
// current layout uses, moved into the frame with appendChild. Nothing is
// cloned and no existing container is rewritten, so every handler, every id
// lookup and every poll keeps working, and a feature added to a view lands in
// both layouts at once.
//
// Mounting happens in three phases so a half-built shell can never be left on
// screen:
//   1. build the frame detached and look up EVERY node that will move; one
//      required node missing and nothing has changed yet;
//   2. move them, logging where each one came from; any throw replays the log
//      backwards and the window is exactly as it was;
//   3. wire the frame and switch it on (state.ui2Shell, html.ui2).
//
// Every export other than mountShell2 does nothing until the shell is
// mounted, so main.js can call them unconditionally from its hooks and the
// app with the preview off behaves exactly as before.
//
// Nothing here imports main.js: what the shell needs from it comes in as deps.

import { state } from '../state.js';
import { t } from '../i18n/index.js';
import { groupMembersOf, lastZoneRound, onZoneLive, stereoPairsOf } from '../groups.js';
import { shellMarkup } from './markup.js';
import { navEntryFor, navStep } from './nav.js';
import { goSourceLabelKey, goSourceTarget } from './gotosource.js';
import { wirePopover } from './popover.js';
import { transportGlyph, transportLabel } from './transport.js';
import { rovingIndex, sidebarHTML, sidebarModel } from './speakers.js';
import { TRANSPORT_SEND_MS, TRANSPORT_WITHIN_MS, transportVerdict } from './sessionplay.js';
import { Pause, Resume } from '../api.js';
import { filterSidebarModel, searchShown, searchStatusText, speakerTerms } from './spksearch.js';
import { playerOnScreen } from '../statusrefreshscope.js';
import { initSpeakersArea, openSpeakers, refreshSpeakersArea, syncSpeakersArea } from './speakers/index.js';
import { agoText } from '../format.js';
import { labelVia } from '../boxes.js';
import {
  configureStates, connectBusy, connectFocus, groupEditVerdict, notifyStates, restoreConnectFocus, paintPlayerState, playerStateModel,
  setReportHook, stateSearchBusy, transportErrorText, wireStates,
} from './state.js';
// Phase 5: the banner region's cards in the shell's tokens (html.ui2 only).
import './banners.css';
import { mountHelp, syncHelpView } from './help/help.js';
import { wireDonateButtons } from '../donate.js';
import { wireSliderWheel } from '../sliderwheel.js';

const VIEW_IDS = ['view-box', 'view-library', 'view-recent', 'view-settings', 'view-setup', 'view-multiroom', 'view-spotify', 'view-wishes'];

let deps = {};
const pops = {};
let lastSpeakersSig = '';
let speakersSettled = false;
// The sidebar row that takes Tab, kept across repaints (roving tabindex).
let rovingHost = '';
// The sidebar search (spksearch.js): what is typed, what the list last drew
// for, and the pending word to the live region.
let spkQuery = '';
let paintedQuery = '';
let lastFindSearch = null;
let findSayTimer = null;
// The player's group state: on screen (live) and grouped (group.js decides,
// through syncShellGroupPanel). One function applies both (applyPlayerGroup),
// so the two never fight over the same flag.
let playerLive = false;
let groupShown = false;
// Phase 5 (states): what happened to a sidebar row, host -> note (an offline
// pick that searches again, a group edit that failed or was not confirmed).
// Part of the row model and of the repaint signature.
const rowNotes = new Map();
// A group edit the speakers have accepted but the zone poll has not shown
// yet: host -> { want: 'in' | 'out', master, name, port, at }. The row stays
// pending until the poll shows the change (honest feedback).
const awaiting = new Map();
const CONFIRM_AFTER_MS = 1000;
const CONFIRM_WITHIN_MS = 20000;
// A row's own Play/Pause press (sessionplay.js) that waits for the speaker:
// host -> { want: 'pause' | 'resume', at, sent, sentAt, name, verdict }. The
// row stays pending until the poll shows the change (honest feedback, as a
// group edit).
const transportWait = new Map();
// The latest press per row, settled or not: a binding that answers after its
// press timed out only speaks for the row while no newer press replaced it.
const lastTransportPress = new Map();
// The buttons of one sidebar row, in their order on screen: the name (pick),
// the row's own Play/Pause, "+" or "-".
const ROW_BUTTONS = '.ui2-spk-pick, .ui2-spk-tp, .ui2-spk-act';
// The speaker the player's error line spoke about.
let playerStatusHost = '';
// The sidebar row whose join review is open (host, '' for none): "+" on a
// speaker that plays something else first says, in that row, what it stops
// and what it plays instead. One at a time; part of the row model.
let joinReviewHost = '';

const byId = (id) => document.getElementById(id);
const label = (b) => labelVia(deps.getBoxLabel, b);

export function shell2Mounted() {
  return !!state.ui2Shell;
}

// shellListsSpeakers reports whether the sidebar's speaker list needs the
// every-speaker poll off the Music tab: always while the shell is mounted,
// limited to where the persistent player is on screen (statusrefreshscope.js:
// not in Setup, not during a speaker update), so the list is live without
// polling speakers that are about to restart.
export function shellListsSpeakers() {
  return !!state.ui2Shell
    && playerOnScreen({ view: state.view, persistentPlayer: true, otaInProgress: !!state.otaInProgress });
}

// collect finds every node the shell will take, before anything moves.
// Returns null (and says which) when a required one is missing.
function collect(frame) {
  const q = (sel) => frame.querySelector(sel);
  const app = byId('app');
  const layout = document.querySelector('.layout');
  const controls = document.querySelector('#boxControls .controls');
  const required = {
    app,
    layout,
    statusBar: byId('statusBar'),
    titleSwapRow: byId('titleSwapRow'),
    controls,
    volume: controls ? controls.querySelector('.volume-control') : null,
    groupControl: byId('groupControl'),
    a11y: document.querySelector('.app-header .app-a11y'),
    locale: document.querySelector('.app-header .app-locale'),
    logo: document.querySelector('.app-header .app-logo'),
    brand: document.querySelector('.app-header .app-brand'),
    footer: byId('appFooter'),
    globalBanner: byId('globalSecurityBanner'),
    speakerUpdateCard: byId('speakerUpdateCard'),
    fBanners: q('#ui2Banners'),
    fBrand: q('.ui2-brand'),
    fAppSettings: q('#ui2AppSettings'),
    fMainInner: q('#ui2MainInner'),
    fNow: q('.ui2-player-now'),
    fNowLine: q('#ui2NowLine'),
    fTransport: q('.ui2-player-transport'),
    fVolume: q('.ui2-player-volume'),
    fMoreBody: q('#ui2MoreBody'),
    fLevelsBody: q('#ui2LevelsBody'),
    fSpeakers: q('#ui2SpkLists'),
    fVersion: q('#ui2Version'),
    fUpdateLine: q('#ui2UpdateLine'),
    fGroupVolSlot: q('#ui2GroupVolSlot'),
  };
  const views = VIEW_IDS.map(byId);
  const missing = Object.keys(required).filter((k) => !required[k]);
  VIEW_IDS.forEach((id, i) => { if (!views[i]) missing.push(id); });
  if (missing.length) {
    try { console.warn('UI 2.0 preview: not mounted, missing', missing.join(', ')); } catch (_) { /* no console */ }
    return null;
  }
  // The window-wide banners sit in <body> before the layout; the speaker
  // update card is one of them. Every one of them goes to the banner region.
  const banners = [...document.body.children].filter((el) => el !== layout
    && el.tagName !== 'SCRIPT'
    && (layout.compareDocumentPosition(el) & Node.DOCUMENT_POSITION_PRECEDING));
  if (!banners.includes(required.speakerUpdateCard)) {
    try { console.warn('UI 2.0 preview: not mounted, the speaker update card is not where it was expected'); } catch (_) { /* no console */ }
    return null;
  }
  return {
    ...required,
    views,
    banners: [...banners, required.globalBanner],
    // Optional: skipped when absent.
    queueControls: byId('queueControls'),
    spotifyRefusedRow: byId('spotifyRefusedRow'),
    sourceButtons: byId('sourceButtons'),
    power: byId('powerBtn'),
    tagline: byId('appTagline'),
    supported: byId('appSupported'),
    appUpdateBanner: byId('appUpdateBanner'),
  };
}

export function mountShell2(d) {
  if (state.ui2Shell) return true;
  if (typeof document === 'undefined') return false;
  deps = d || {};

  // Phase 1: build and validate. Nothing visible changes.
  const holder = document.createElement('div');
  holder.innerHTML = shellMarkup();
  const skip = holder.querySelector('#ui2Skip');
  const frame = holder.querySelector('#ui2Shell');
  if (!skip || !frame) return false;
  const n = collect(frame);
  if (!n) return false;

  // Phase 2: move, with an undo log.
  const log = [];
  const insert = (node, parent) => {
    log.push({ node, parent: null, next: null });
    parent.appendChild(node);
  };
  const move = (node, parent) => {
    if (!node) return;
    log.push({ node, parent: node.parentNode, next: node.nextSibling });
    parent.appendChild(node);
  };
  try {
    insert(skip, n.app);
    insert(frame, n.app);
    n.banners.forEach((b) => move(b, n.fBanners));
    // The speaker update card is a landmark of its own in the banner region,
    // named by its title (the attributes go with the undo log's node, and
    // mean nothing to the classic layout's styles).
    n.speakerUpdateCard.setAttribute('role', 'region');
    n.speakerUpdateCard.setAttribute('aria-label', t('speakerUpdate.title'));
    n.views.forEach((v) => move(v, n.fMainInner));
    move(n.footer, n.fMainInner);
    // The title line shares a row with the button that opens where the music
    // comes from (wireGoSource).
    move(n.statusBar, n.fNowLine);
    move(n.titleSwapRow, n.fNow);
    move(n.spotifyRefusedRow, n.fNow);
    move(n.controls, n.fTransport);
    move(n.volume, n.fVolume);
    move(n.queueControls, n.fMoreBody);
    move(n.sourceButtons, n.fMoreBody);
    // Power sits in the transport, right after Stop (#1796): switching the
    // speaker off is a basic function with its own key on the speaker and the
    // remote, not something to look for under More. It is the classic input
    // row's own toggle (sources.js), so its state, its hold while the speaker
    // wakes and its call are the same; queue, inputs and the volume have left
    // the row by now, so it lands right after Stop.
    move(n.power, n.controls);
    move(n.groupControl, n.fLevelsBody);
    move(n.a11y, n.fAppSettings);
    move(n.locale, n.fAppSettings);
    move(n.logo, n.fBrand);
    move(n.brand, n.fBrand);
    move(n.tagline, n.fBrand);
    move(n.supported, n.fBrand);
  } catch (e) {
    n.speakerUpdateCard.removeAttribute('role');
    n.speakerUpdateCard.removeAttribute('aria-label');
    for (let i = log.length - 1; i >= 0; i--) {
      const { node, parent, next } = log[i];
      try {
        if (!parent) { node.remove(); continue; }
        if (next && next.parentNode === parent) parent.insertBefore(node, next);
        else parent.appendChild(node);
      } catch (_) { /* keep undoing the rest */ }
    }
    try { console.warn('UI 2.0 preview: mount rolled back', e); } catch (_) { /* no console */ }
    return false;
  }

  // Phase 3: wire and activate.
  quietPlayerClock();
  nameTransport();
  announceToasts();
  wire(frame, skip);
  state.ui2Shell = true;
  document.documentElement.classList.add('ui2');
  watchAppUpdateBanner(n.appUpdateBanner, n.fVersion, n.fBanners, n.fUpdateLine);
  syncShellNav(state.view);
  syncShellPlayer(!!state.currentBox);
  paintSpeakers(true);
  return true;
}

// quietPlayerClock keeps the persistent player's live region to real news.
// The status bar was one polite live region, track line AND elapsed clock;
// that was fine while it lived on the Music tab only, but in the shell it is on
// screen on every view, and the clock ticks once a second, so a screen reader
// would hear the time on every page. Only the track line (#statusMain, which
// changes on a new station, track or play state) stays announced; the clock is
// still in the page to read, just no longer pushed.
function quietPlayerClock() {
  const bar = byId('statusBar');
  const main = byId('statusMain');
  if (!bar || !main) return;
  bar.removeAttribute('role');
  bar.removeAttribute('aria-live');
  main.setAttribute('role', 'status');
  main.setAttribute('aria-live', 'polite');
}

// nameTransport keeps the player's round transport buttons told apart and
// named. The preview draws Play, Pause and Stop as glyphs (shell2.css); the
// words nowplaying.js writes into the buttons stay their accessible names and
// become their tooltips here, and the Play/Pause button says which glyph it
// shows (data-ui2-glyph), on every change of its words. The classic layout
// never runs this.
function nameTransport() {
  const pp = byId('pauseBtn');
  const stop = byId('stopBtn');
  if (stop) {
    const label = transportLabel(stop.textContent);
    if (label) stop.title = label;
  }
  if (!pp) return;
  const sync = () => {
    pp.dataset.ui2Glyph = transportGlyph(pp.innerHTML);
    const label = transportLabel(pp.textContent);
    if (label && pp.title !== label) pp.title = label;
  };
  sync();
  if (typeof MutationObserver === 'undefined') return;
  new MutationObserver(sync).observe(pp, { childList: true, characterData: true, subtree: true });
}

// announceToasts makes the window's toast a polite status in the preview, so
// what it says is heard and not only seen; state.css gives it the preview's
// colours. The classic layout keeps its toast as it was.
function announceToasts() {
  const toast = byId('toast');
  if (!toast) return;
  toast.setAttribute('role', 'status');
  toast.setAttribute('aria-live', 'polite');
}

function wire(frame, skip) {
  const main = frame.querySelector('#ui2Main');
  skip.addEventListener('click', () => { if (main) main.focus(); });

  const navItems = () => [...frame.querySelectorAll('.ui2-nav-item[data-view]')];
  navItems().forEach((btn) => {
    btn.addEventListener('click', () => {
      if (typeof deps.switchView === 'function') deps.switchView(btn.dataset.view);
    });
    // The tab row: Left and Right move along it (Up and Down too), Home and
    // End to its ends. Every entry stays a Tab stop of its own.
    btn.addEventListener('keydown', (e) => {
      if (e.ctrlKey || e.altKey || e.shiftKey) return;
      const list = navItems();
      const rtl = getComputedStyle(btn).direction === 'rtl';
      const to = navStep(e.key, list.indexOf(btn), list.length, rtl);
      const next = to >= 0 ? list[to] : null;
      if (!next) return;
      e.preventDefault();
      next.focus();
      if (typeof next.scrollIntoView === 'function') next.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    });
  });

  pops.more = wirePopover(frame.querySelector('#ui2MoreBtn'), frame.querySelector('#ui2MorePop'));
  // Levels: the per-speaker sliders of the group, opened from the player. Only
  // sliders: the chips that choose speakers are hidden there (shell2.css), the
  // sidebar is the one place that chooses.
  pops.levels = wirePopover(frame.querySelector('#ui2LevelsBtn'), frame.querySelector('#ui2LevelsPop'));
  wireSpeakerList(frame.querySelector('#ui2SpkLists'));
  wireSpeakerSearch(frame.querySelector('#ui2SpkQ'));
  // Support STR, always on screen at the foot of the sidebar (Jens,
  // 2026-11-02: the classic layout's rail had no equivalent in the preview).
  wireDonateButtons(frame.querySelector('#ui2Support'));
  // A group edit waiting for the speakers is decided when a zone round lands.
  onZoneLive(() => { if (state.ui2Shell && awaiting.size) paintSpeakers(false); });
  // The Speakers area (phase 3): its row of places and its routes.
  initSpeakersArea(frame, deps);
  wireStatesEnv(frame);
  wireSliders(frame);
}

// wireSliders gives every slider in the shell the mouse wheel and the touchpad
// (sliderwheel.js: one delegated listener, so the Levels rows and the Sound
// tab's sliders that re-renders create are covered too), and a tooltip that
// says so on the sliders that have none of their own.
function wireSliders(frame) {
  wireSliderWheel(frame);
  frame.addEventListener('pointerover', (e) => {
    const el = e.target && typeof e.target.closest === 'function' ? e.target.closest('input[type="range"]') : null;
    if (!el) return;
    // A disabled slider (bass on a speaker without it) ignores the wheel
    // (sliderwheel.js), so it must not promise it; a hint written while it was
    // still enabled goes too.
    if (el.disabled) {
      if (el.dataset.ui2WheelHint && el.title === el.dataset.ui2WheelHint) el.removeAttribute('title');
      delete el.dataset.ui2WheelHint;
      return;
    }
    // Only a title this code wrote is replaced (it follows the language); a
    // slider whose own code names it (balance, the player volume) keeps that.
    if (el.title && el.title !== el.dataset.ui2WheelHint) return;
    const hint = t('ui2.slider.wheelHint');
    el.title = hint;
    el.dataset.ui2WheelHint = hint;
  });
}

// wireStatesEnv hands the states of phase 5 (state.js) what their buttons
// act with, and wires them once, by delegation. Search again, Add speaker,
// Connect by address and Go to the speaker list work in every view that
// draws a state.
function wireStatesEnv(frame) {
  configureStates({
    discoverBoxes: () => (typeof deps.discoverBoxes === 'function' ? deps.discoverBoxes() : Promise.resolve()),
    addBoxByIp: (ip) => (typeof deps.addBoxByIp === 'function' ? deps.addBoxByIp(ip) : Promise.reject(new Error('no addBoxByIp'))),
    open: (route, opts) => openSpeakers(route, opts),
    focusSpeakerList,
    boxes: () => state.boxes || [],
    repaint: () => { if (state.ui2Shell) renderShellSpeakers(true); },
  });
  wireStates(document);
  // The transport and the queue report their failures in the player, naming
  // the speaker (nowplaying.js asks for this hook; without it, the dialog).
  setReportHook('transport', reportTransport);
  wireGoSource(frame);
  const transport = frame.querySelector('.ui2-player-transport');
  const more = frame.querySelector('#ui2MoreBody');
  // A new press clears the last failure; its own result writes a new one.
  for (const el of [transport, more]) {
    if (el) el.addEventListener('click', () => setPlayerStatus(''), true);
  }
  ensureSpeakerLive(frame);
}

// The player's "Show in Radio / Library / Spotify" button (gotosource.js).
// goSourceNow is what the speaker plays, as the last now-playing line handed
// it over (nowplaying.js renderNowPlayingBar, through the 'nowPlaying' hook);
// goTarget is where the button leads for it.
let goSourceNow = null;
let goTarget = null;

// wireGoSource follows the now-playing line and opens the source's tab on a
// press: the same switch a tab does, and for a music-library folder the
// folder too (main.js hands in the Library's showLibraryFolder).
function wireGoSource(frame) {
  const btn = frame.querySelector('#ui2GoSource');
  if (!btn) return;
  setReportHook('nowPlaying', (np) => {
    goSourceNow = np || null;
    paintGoSource();
  });
  btn.addEventListener('click', () => {
    const tgt = goTarget;
    if (!tgt) return;
    if (tgt.view === 'library' && tgt.folder && typeof deps.showLibraryFolder === 'function') deps.showLibraryFolder(tgt.folder);
    if (typeof deps.switchView === 'function') deps.switchView(tgt.view);
  });
  // What plays already when the shell comes up, before the next status poll
  // draws the line again.
  goSourceNow = {
    source: state.nowSource || '',
    location: state.nowLocation || '',
    queue: state.queue,
    lastLibraryPlay: state.lastLibraryPlay,
  };
  paintGoSource();
}

// paintGoSource shows the button for a source a tab holds and names it: the
// tab's name as its words, "Show in <tab>" as its name and tooltip. No button
// for an input without a tab, with no speaker, or for one that is offline
// (what it played last is not what plays).
function paintGoSource() {
  const btn = byId('ui2GoSource');
  if (!btn) return;
  const box = state.currentBox;
  const tgt = box && !box.offline && goSourceNow ? goSourceTarget(goSourceNow) : null;
  goTarget = tgt;
  btn.hidden = !tgt;
  if (!tgt) return;
  const entry = navEntryFor(tgt.view);
  const words = entry ? t(entry.labelKey) : '';
  const name = t(goSourceLabelKey(tgt.view));
  const text = btn.querySelector('.ui2-go-source-text');
  if (text && text.textContent !== words) text.textContent = words;
  if (btn.getAttribute('aria-label') !== name) btn.setAttribute('aria-label', name);
  if (btn.title !== name) btn.title = name;
  btn.dataset.view = tgt.view;
}

// focusSpeakerList is "Go to the speaker list": the keyboard lands on the row
// that takes Tab in the sidebar (roving), or the sidebar's first row.
function focusSpeakerList() {
  const lists = byId('ui2SpkLists');
  if (!lists) return;
  const btn = lists.querySelector('.ui2-spk-pick[tabindex="0"]') || lists.querySelector('.ui2-spk-pick');
  if (btn) {
    btn.focus();
    try { btn.scrollIntoView({ block: 'nearest' }); } catch (_) { /* old engine */ }
  }
}

// The sidebar's live region: outside the repainted lists, in the page before
// the first result is written into it.
function ensureSpeakerLive(frame) {
  const section = (frame && frame.querySelector('#ui2Speakers')) || byId('ui2Speakers');
  if (!section || section.querySelector('#ui2SpkLive')) return;
  const live = document.createElement('div');
  live.id = 'ui2SpkLive';
  live.className = 'u-sr-only';
  live.setAttribute('role', 'status');
  live.setAttribute('aria-live', 'polite');
  section.appendChild(live);
}

function announceSpeakers(text) {
  const live = byId('ui2SpkLive');
  if (!live || !text) return;
  live.textContent = '';
  setTimeout(() => { live.textContent = text; }, 50);
}

// reportTransport is the transport's and the queue's failure, in the player:
// "Kitchen did not pause: ...".
function reportTransport({ kind, error, box } = {}) {
  const b = box || state.currentBox;
  setPlayerStatus(transportErrorText(kind, label(b), error), b ? b.host : '');
}

function setPlayerStatus(text, host = '') {
  const el = byId('ui2PlayerStatus');
  if (!el) return;
  playerStatusHost = text ? host : '';
  // The same words again are news too: clear first, so the alert speaks.
  el.textContent = '';
  if (text) setTimeout(() => { el.textContent = text; }, 30);
}

// shellOfflinePick is choosing an unreachable speaker in the preview: no
// toast across the window, a note in the speaker's own row ("Kitchen is not
// reachable, last seen 5 minutes ago. Searching again..."), and when the
// search comes back without it, "Still not reachable." with What to try.
// search is the discovery the pick started (main.js pickBox).
export function shellOfflinePick(box, search) {
  if (!state.ui2Shell || !box || !box.host) return false;
  const host = box.host;
  const name = label(box);
  const sec = typeof box.offlineSinceSec === 'number' && box.offlineSinceSec > 0 ? box.offlineSinceSec : 0;
  const ago = sec ? agoText(sec) : '';
  const text = ago ? t('ui2.state.offlinePick', { name, ago }) : t('ui2.state.offlinePickNoAgo', { name });
  rowNotes.set(host, { tone: 'busy', text, host });
  rovingHost = host;
  paintSpeakers(true);
  announceSpeakers(text);
  Promise.resolve(search).catch(() => {}).then(() => {
    const again = (state.boxes || []).find((b) => b && b.host === host);
    const note = rowNotes.get(host);
    if (!note || note.text !== text) return;
    if (again && !again.offline) {
      rowNotes.delete(host);
      paintSpeakers(true);
      announceSpeakers(t('ui2.state.backOnline', { name: label(again) }));
      return;
    }
    rowNotes.set(host, { tone: 'warn', text: t('ui2.state.stillOffline'), action: 'whatToTry', host });
    paintSpeakers(true);
    announceSpeakers(`${name}: ${t('ui2.state.stillOffline')}`);
  });
  return true;
}

// groupReport is the sidebar's "+" and "-" result (group.js, report hook):
//   ok     the speakers accepted it; the row stays pending until the zone
//          poll shows it, then "Kitchen plays along" / "Kitchen is out"
//   error  a note in the row (alert) with Try again: the same toggle, the
//          same master
//   warn   no Try again that would make it worse:
//          stale    the group changed before the edit ran and nothing was
//                   sent; the note says so, and the row's own "+" or "-"
//                   already aims at the group as it is now
//          partial  a dissolve the master did not finish: every speaker
//                   waits for the zone poll; one that left is said to be
//                   out, one still in gets the not-confirmed note with Try
//                   again, which then takes out only that one
function groupReport(r = {}) {
  const hosts = (r.hosts || []).filter(Boolean);
  if (r.tone === 'warn' && (r.kind === 'stale' || r.kind === 'partial')) {
    const now = Date.now();
    for (const h of hosts) {
      const b = (state.boxes || []).find((x) => x && x.host === h);
      rowNotes.set(h, { tone: 'warn', text: r.text || t('common.error'), kind: r.kind, host: h });
      if (r.kind === 'partial') {
        awaiting.set(h, { want: 'out', master: r.master || '', name: b ? label(b) : h, port: (r.ports && r.ports[h]) || '', at: now, partial: true });
      } else {
        awaiting.delete(h);
      }
    }
    paintSpeakers(true);
    if (r.text) announceSpeakers(r.text);
    if (r.kind === 'partial') setTimeout(() => paintSpeakers(false), CONFIRM_WITHIN_MS + 100);
    // The stale note is news about one moment: it goes after a while, unless
    // something newer took its place.
    else {
      setTimeout(() => {
        let gone = false;
        for (const h of hosts) {
          const n = rowNotes.get(h);
          if (n && n.kind === 'stale' && n.text === r.text) { rowNotes.delete(h); gone = true; }
        }
        if (gone) paintSpeakers(true);
      }, CONFIRM_WITHIN_MS);
    }
    return;
  }
  if (r.tone === 'error' || r.tone === 'warn') {
    for (const h of hosts) {
      awaiting.delete(h);
      rowNotes.set(h, {
        tone: 'error', text: r.text || t('common.error'), action: 'retry',
        master: r.master || '', port: (r.ports && r.ports[h]) || '', host: h,
      });
    }
    paintSpeakers(true);
    return;
  }
  const now = Date.now();
  const mark = (list, want) => (list || []).forEach((h) => {
    const b = (state.boxes || []).find((x) => x && x.host === h);
    rowNotes.delete(h);
    awaiting.set(h, { want, master: r.master || '', name: b ? label(b) : h, port: (r.ports && r.ports[h]) || '', at: now });
  });
  mark(r.added, 'in');
  mark(r.removed, 'out');
  paintSpeakers(true);
  // A repaint after the deadline, in case nothing else repaints then. A zone
  // round that lands repaints by itself (onZoneLive in wire).
  setTimeout(() => paintSpeakers(false), CONFIRM_WITHIN_MS + 100);
}

// confirmAwaiting checks the waiting group edits against the zone poll
// (groupEditVerdict): neither the optimistic update group.js writes at once
// nor an entry carried over from a round a speaker missed counts, only a
// round both speakers answered after the edit.
function confirmAwaiting() {
  if (!awaiting.size) return;
  const now = Date.now();
  const boxes = state.boxes || [];
  const round = lastZoneRound();
  for (const [h, w] of [...awaiting]) {
    const m = boxes.find((b) => b && b.host === w.master);
    const inGroup = !!m && groupMembersOf(m, state.zoneLive || {}, boxes).some((x) => x && x.ip === h);
    const verdict = groupEditVerdict(w, h, round, inGroup, now, { after: CONFIRM_AFTER_MS, within: CONFIRM_WITHIN_MS });
    if (verdict === 'confirmed') {
      awaiting.delete(h);
      if (w.partial) rowNotes.delete(h);
      announceSpeakers(t(w.want === 'in' ? 'ui2.state.playsAlong' : 'ui2.state.isOut', { name: w.name }));
    } else if (verdict === 'failed') {
      awaiting.delete(h);
      rowNotes.set(h, { tone: 'error', text: t('ui2.state.groupNoConfirm', { name: w.name }), action: 'retry', master: w.master, port: w.port, host: h });
    }
  }
}

// rowTransportPress is a row's own Play/Pause: Pause or Resume on that row's
// host and port (a group's master, a pair's master, a single speaker), never
// through the speaker you control, which stays the one it was. The row waits
// for the speaker (settleTransport); a refusal is an alert in the row itself
// ("Kitchen did not pause: ..."), never a toast across the window.
function rowTransportPress(host, port, want, name) {
  if (!host || transportWait.has(host)) return;
  // The note of an earlier failed press goes with the next one.
  const old = rowNotes.get(host);
  if (old && old.kind === 'transport') rowNotes.delete(host);
  const w = { want, at: Date.now(), sent: false, sentAt: 0, name, verdict: '' };
  transportWait.set(host, w);
  lastTransportPress.set(host, w);
  paintSpeakers(true);
  const fn = want === 'pause' ? Pause : Resume;
  // A press that settleTransport already gave up on while the binding had
  // not answered yet, with no newer press on the row since: its late answer
  // still belongs to it.
  const lateOwn = () => !transportWait.has(host) && lastTransportPress.get(host) === w && w.verdict === 'timeout';
  Promise.resolve().then(() => fn(host, port)).then(() => {
    if (transportWait.get(host) !== w) {
      if (!lateOwn()) return;
      // Accepted after all: the press waits for the speaker again instead of
      // leaving its warning standing next to a speaker that did what it was
      // told.
      dropTransportNote(host, 'warn');
      w.verdict = '';
      transportWait.set(host, w);
    }
    w.sent = true;
    w.sentAt = Date.now();
    askWhoPlays();
    // The confirm window counts from here, so its deadline repaint does too.
    setTimeout(() => paintSpeakers(false), TRANSPORT_WITHIN_MS + 100);
    paintSpeakers(true);
  }, (err) => {
    const own = transportWait.get(host) === w;
    if (!own && !lateOwn()) return;
    if (own) transportWait.delete(host);
    // A late refusal takes the place of the "has not confirmed it yet"
    // warning: the speaker did answer, and with a no (rule 3).
    const text = transportErrorText(want, name, err);
    rowNotes.set(host, { tone: 'error', text, kind: 'transport', host });
    paintSpeakers(true);
    // News about one press: it goes after a while, unless something newer
    // took its place.
    setTimeout(() => {
      const n = rowNotes.get(host);
      if (n && n.kind === 'transport' && n.text === text) { rowNotes.delete(host); paintSpeakers(true); }
    }, CONFIRM_WITHIN_MS);
  });
  // A repaint once the binding should have answered, in case nothing else
  // repaints then.
  setTimeout(() => paintSpeakers(false), TRANSPORT_SEND_MS + 100);
}

function dropTransportNote(host, tone) {
  const n = rowNotes.get(host);
  if (n && n.kind === 'transport' && n.tone === tone) rowNotes.delete(host);
}

// playReadAt is when the last every-speaker round that read this speaker
// successfully started (main.js refreshBoxPlaying, state.boxPlayReadAt), 0
// when none did. A failed read marks a speaker as not playing, so a pause is
// only confirmed by a read that worked (sessionplay.js transportVerdict).
function playReadAt(host) {
  const b = (state.boxes || []).find((x) => x && x.host === host);
  const at = b && b.deviceID && state.boxPlayReadAt ? state.boxPlayReadAt[b.deviceID] : 0;
  return typeof at === 'number' ? at : 0;
}

// askWhoPlays reads every speaker's play state soon after a press instead of
// on the next 15 s round. The speaker takes a moment to report the change, so
// a second read follows. A round already in flight started before the press:
// refreshBoxPlaying then runs one more round right after it.
function askWhoPlays() {
  if (typeof deps.refreshBoxPlaying !== 'function') return;
  setTimeout(() => { try { deps.refreshBoxPlaying(); } catch (_) { /* best effort */ } }, 800);
  setTimeout(() => { try { deps.refreshBoxPlaying(); } catch (_) { /* best effort */ } }, 3000);
}

// settleTransport checks the waiting presses against the rows as the poll
// draws them now (sessionplay.js transportVerdict). Confirmed: said once
// through the sidebar's live region. Not confirmed in time: a note in the
// row, which then shows what the speaker reports. Returns whether anything
// changed, so the model is built again without the pending mark.
function settleTransport(model) {
  if (!transportWait.size) return false;
  const rows = [...(model.pinned || []), ...(model.others || [])];
  const now = Date.now();
  let changed = false;
  for (const [h, w] of [...transportWait]) {
    const row = rows.find((r) => r.host === h) || null;
    const verdict = transportVerdict(w, row && { ...row, readAt: playReadAt(h) }, now);
    if (verdict === 'wait') continue;
    transportWait.delete(h);
    w.verdict = verdict;
    changed = true;
    if (verdict === 'confirmed') {
      announceSpeakers(t(w.want === 'pause' ? 'ui2.side.paused' : 'ui2.side.resumed', { name: w.name }));
    } else if (verdict === 'timeout') {
      const text = t('ui2.side.noConfirm', { name: w.name });
      rowNotes.set(h, { tone: 'warn', text, kind: 'transport', host: h });
      announceSpeakers(text);
      setTimeout(() => {
        const n = rowNotes.get(h);
        if (n && n.kind === 'transport' && n.text === text) { rowNotes.delete(h); paintSpeakers(true); }
      }, CONFIRM_WITHIN_MS);
    }
  }
  return changed;
}

// wireSpeakerList handles the sidebar list, delegated, so a repaint never
// loses a handler. A row's pick button chooses the speaker you control
// (pickBox: an offline speaker explains itself and rediscovers, a Bose
// original opens the setup); its "+" or "-" is the group edit
// (toggleGroupMember, the Music view's own path), aimed at the pinned
// group's master, which the row carries in data-master.
function wireSpeakerList(root) {
  if (!root) return;
  const boxFor = (btn) => {
    const host = btn.dataset.host;
    const port = parseInt(btn.dataset.port, 10);
    return (state.boxes || []).find((b) => b && b.host === host && b.port === port) || null;
  };
  // focusRow puts the keyboard back on a row's "+" (or its pick button when
  // the "+" is gone), after the join review closed under it.
  const focusRow = (host) => {
    const find = (cls) => [...root.querySelectorAll(cls)].find((b) => b.dataset.host === host);
    const target = find('.ui2-spk-act') || find('.ui2-spk-pick');
    if (target) target.focus();
  };
  const closeJoin = (host, add) => {
    const btn = [...root.querySelectorAll('.ui2-spk-join-btn[data-join="add"]')].find((b) => b.dataset.host === host);
    joinReviewHost = '';
    rovingHost = host;
    if (add && btn && typeof deps.toggleGroupMember === 'function') {
      deps.toggleGroupMember(host, parseInt(btn.dataset.port, 10), btn.dataset.master || '', groupReport);
    }
    paintSpeakers(true);
    focusRow(host);
  };
  root.addEventListener('click', (e) => {
    const join = e.target.closest('.ui2-spk-join-btn');
    if (join) {
      closeJoin(join.dataset.host, join.dataset.join === 'add');
      return;
    }
    if (e.target.closest('[data-spk-find-clear]')) {
      clearSpeakerSearch(true);
      return;
    }
    const note = e.target.closest('.ui2-spk-note-act');
    if (note) {
      const host = note.dataset.host;
      rovingHost = host;
      if (note.dataset.noteAct === 'retry') {
        rowNotes.delete(host);
        if (typeof deps.toggleGroupMember === 'function') deps.toggleGroupMember(host, parseInt(note.dataset.port, 10), note.dataset.master || '', groupReport);
        paintSpeakers(true);
        const pick = [...root.querySelectorAll('.ui2-spk-pick')].find((b) => b.dataset.host === host);
        if (pick) pick.focus();
      } else if (note.dataset.noteAct === 'whatToTry') {
        openSpeakers('list');
        // The speaker's card, once the list has painted.
        setTimeout(() => {
          const card = document.querySelector(`#view-speakers .spl-item[data-host="${CSS.escape(host)}"]`);
          const target = card && (card.querySelector('.spl-act[data-act="search"]') || card.querySelector('.spl-open'));
          if (target) target.focus();
        }, 0);
      }
      return;
    }
    // A row's own Play/Pause: never a pick, so the speaker you control stays.
    const tp = e.target.closest('.ui2-spk-tp');
    if (tp) {
      rovingHost = tp.dataset.host;
      const li = tp.closest('li');
      const nameEl = li && li.querySelector('.ui2-spk-name');
      const box = boxFor(tp);
      const name = (nameEl && nameEl.textContent) || (box ? label(box) : tp.dataset.host);
      rowTransportPress(tp.dataset.host, parseInt(tp.dataset.port, 10), tp.dataset.tp === 'play' ? 'resume' : 'pause', name);
      return;
    }
    const act = e.target.closest('.ui2-spk-act');
    if (act) {
      rovingHost = act.dataset.host;
      // A speaker that plays something else: the first press opens the
      // review in its row (a second one closes it again), and only Add there
      // makes the edit.
      if (act.dataset.review === '1') {
        const host = act.dataset.host;
        joinReviewHost = joinReviewHost === host ? '' : host;
        paintSpeakers(true);
        const add = joinReviewHost
          ? [...root.querySelectorAll('.ui2-spk-join-btn[data-join="add"]')].find((b) => b.dataset.host === host)
          : null;
        if (add) add.focus();
        else focusRow(host);
        return;
      }
      if (typeof deps.toggleGroupMember === 'function') deps.toggleGroupMember(act.dataset.host, parseInt(act.dataset.port, 10), act.dataset.master || '', groupReport);
      return;
    }
    const pick = e.target.closest('.ui2-spk-pick');
    if (!pick) return;
    rovingHost = pick.dataset.host;
    const box = boxFor(pick);
    if (!box || typeof deps.pickBox !== 'function') return;
    Promise.resolve(deps.pickBox(box)).then(() => {
      // Wishes and Help and info have nothing about a speaker to show:
      // choosing one there opens Radio, so the main area shows the speaker you
      // just chose. Every other view stays open and follows the speaker.
      if ((state.view === 'wishes' || state.view === 'help') && state.currentBox && state.currentBox.host === box.host
        && typeof deps.switchView === 'function') deps.switchView('box');
    }).catch(() => {});
  });
  root.addEventListener('focusin', (e) => {
    const btn = e.target.closest(ROW_BUTTONS);
    if (btn) setRoving(root, btn.dataset.host);
  });
  root.addEventListener('keydown', (e) => {
    // Escape in the join review (or on the "+" that opened it) is Cancel.
    if (e.key === 'Escape' && joinReviewHost) {
      const inJoin = e.target.closest('.ui2-spk-join');
      const opener = e.target.closest('.ui2-spk-act');
      if (inJoin || (opener && opener.dataset.host === joinReviewHost)) {
        e.preventDefault();
        e.stopPropagation();
        closeJoin(joinReviewHost, false);
        return;
      }
    }
    const btn = e.target.closest(ROW_BUTTONS);
    if (!btn) return;
    const li = btn.closest('li');
    const rtl = getComputedStyle(root).direction === 'rtl';
    const toAct = rtl ? 'ArrowLeft' : 'ArrowRight';
    const toPick = rtl ? 'ArrowRight' : 'ArrowLeft';
    // Right and Left move along the row: the name, Play/Pause, "+" or "-".
    if (e.key === toAct || e.key === toPick) {
      const inRow = li ? [...li.querySelectorAll(ROW_BUTTONS)] : [];
      const next = inRow[inRow.indexOf(btn) + (e.key === toAct ? 1 : -1)];
      if (next) { e.preventDefault(); next.focus(); }
      return;
    }
    // Up, Down, Home and End move through every row, the pinned group and the
    // others as one sequence.
    const picks = [...root.querySelectorAll('.ui2-spk-pick')];
    const at = picks.indexOf(li ? li.querySelector('.ui2-spk-pick') : null);
    // Escape in a searched list clears the search and stays on the row; the
    // row is still there, since the full list holds every searched one.
    if (e.key === 'Escape' && spkQuery) {
      e.preventDefault();
      e.stopPropagation();
      clearSpeakerSearch(false);
      return;
    }
    // Up on the first row goes back to the search field above it.
    const field = searchField();
    if (e.key === 'ArrowUp' && at === 0 && field) {
      e.preventDefault();
      field.focus();
      return;
    }
    const next = rovingIndex(e.key, at, picks.length);
    if (next < 0) return;
    e.preventDefault();
    const target = picks[next];
    setRoving(root, target.dataset.host);
    target.focus();
    try { target.scrollIntoView({ block: 'nearest' }); } catch (_) { /* old engine */ }
  });
}

// searchField is the sidebar's search field while it is on screen, else null.
function searchField() {
  const box = byId('ui2SpkFind');
  return box && !box.hidden ? byId('ui2SpkQ') : null;
}

// findTerms is what one speaker can be found by besides its row's name and
// model (spksearch.js speakerTerms, pure, so vitest covers it).
function findTerms(host) {
  return speakerTerms(host, state, { label, sourceLabel: deps.sourceLabel });
}

// syncSpeakerSearch shows the field only while the list is long enough
// (spksearch.js searchShown) and returns whether it shows. When the list
// shrinks below that, a search left in it would hide speakers with no field
// to clear it from: it goes, and the keyboard moves to the list.
function syncSpeakerSearch(shown) {
  const box = byId('ui2SpkFind');
  const field = byId('ui2SpkQ');
  if (!box || !field) return false;
  // Read before hiding: a hidden field may lose focus right away (the
  // browser's focus fixup), and the keyboard would then land on the page.
  const hadFocus = document.activeElement === field;
  if (box.hidden === shown) box.hidden = !shown;
  if (!shown && (spkQuery || field.value)) {
    spkQuery = '';
    field.value = '';
    if (hadFocus) setTimeout(focusSpeakerList, 0);
  }
  return shown;
}

// clearSpeakerSearch empties the search and repaints. toField puts the
// keyboard in the field (the Clear button, which goes away with the search).
function clearSpeakerSearch(toField) {
  const field = byId('ui2SpkQ');
  spkQuery = '';
  if (field) field.value = '';
  paintSpeakers(true);
  sayFind();
  if (toField && field) field.focus();
}

// sayFind tells the live region how the search ended, once typing pauses, so
// a screen reader hears "Speakers shown: 3 of 9" and not every keystroke.
function sayFind() {
  if (findSayTimer) clearTimeout(findSayTimer);
  findSayTimer = setTimeout(() => {
    findSayTimer = null;
    if (!spkQuery) return;
    const text = searchStatusText(lastFindSearch);
    if (text) announceSpeakers(text);
  }, 600);
}

// wireSpeakerSearch: typing narrows the list on every input; Escape clears
// (and only then stops the key, so an empty field lets Escape through);
// Down goes to the first row the search left.
function wireSpeakerSearch(field) {
  if (!field) return;
  field.addEventListener('input', () => {
    spkQuery = field.value;
    paintSpeakers(true);
    sayFind();
  });
  field.addEventListener('keydown', (e) => {
    // While an input method composes (Japanese, Chinese), Down picks a
    // candidate and Escape cancels the composition: both belong to the IME,
    // not to the search. keyCode 229 is how WebKit marks such a key.
    if (e.isComposing || e.keyCode === 229) return;
    if (e.key === 'Escape' && field.value) {
      e.preventDefault();
      e.stopPropagation();
      clearSpeakerSearch(true);
      return;
    }
    if (e.key === 'ArrowDown') {
      const lists = byId('ui2SpkLists');
      const first = lists && lists.querySelector('.ui2-spk-pick');
      if (!first) return;
      e.preventDefault();
      setRoving(lists, first.dataset.host);
      first.focus();
    }
  });
}

// setRoving gives Tab to one row: its pick button and its action, and takes it
// from every other row.
function setRoving(root, host) {
  rovingHost = host || '';
  root.querySelectorAll(ROW_BUTTONS).forEach((b) => {
    b.tabIndex = b.dataset.host === rovingHost ? 0 : -1;
  });
}

// watchAppUpdateBanner keeps the app's version line inside the frame. A real
// notice (an update, a download hand-off) is a window-wide banner and goes to
// the banner region on top. Quiet ("Version ... Check for app updates") it
// waits out of sight in the sidebar's update slot (#ui2Version): the version
// and the update check live on Help and info, About, and the sidebar's
// resting state belongs to the speakers. Only while an update is waiting and
// its notice was dismissed does the slot show one small line, "New app version
// available: 1.4.0", whose press brings the notice back (the quiet link's own
// path: the dismissal is cleared and the check runs again). main.js renders
// the banner as it always did; this only follows its quiet class, so every
// path that draws it (the quiet link, the full notice, the mac, deb and
// winget hand-offs) lands in the right place without touching them.
function watchAppUpdateBanner(banner, versionSlot, bannersSlot, line) {
  if (!banner || !versionSlot || !bannersSlot || typeof MutationObserver === 'undefined') return;
  // A manual check is a press inside the banner (or on the line standing in
  // for it); the notice it brings back replaces what had focus, so focus
  // follows the moved banner.
  let pressedAt = 0;
  banner.addEventListener('click', () => { pressedAt = Date.now(); });
  if (line) {
    line.addEventListener('click', () => {
      pressedAt = Date.now();
      const link = banner.querySelector('#appUpdateCheckLink');
      if (link) link.click();
      else if (typeof deps.checkAppUpdate === 'function') deps.checkAppUpdate(true);
    });
  }
  const place = () => {
    const quiet = banner.classList.contains('app-update-quiet');
    const target = quiet ? versionSlot : bannersSlot;
    // The line: only for an update that is waiting while its notice is not
    // on screen.
    const pending = quiet && !!state.appUpdateVersion && !banner.classList.contains('hidden');
    if (line) line.textContent = pending ? `${t('banner.appUpdateAvail')} ${state.appUpdateVersion}` : '';
    versionSlot.hidden = !pending;
    if (banner.parentNode === target) return;
    const active = document.activeElement;
    const hadFocus = banner.contains(active) || versionSlot.contains(active)
      || ((!active || active === document.body) && Date.now() - pressedAt < 20000);
    if (quiet) target.appendChild(banner);
    else target.insertBefore(banner, target.firstChild);
    if (!hadFocus) return;
    // The notice on top takes the keyboard; a dismissed one hands it to the
    // line that stands in for it (the quiet banner itself is out of sight).
    const f = quiet ? (pending ? line : null) : banner.querySelector('button, a[href]');
    if (f) f.focus();
  };
  new MutationObserver(place).observe(banner, { attributes: true, attributeFilter: ['class'], childList: true });
  place();
}

// syncShellNav marks the open view in the tab row and starts it at the top,
// the way a fresh tab did when the whole window scrolled. An area entry
// (Speakers) is marked on every view it stands for (data-views), and the
// area's own head follows the view too.
export function syncShellNav(view) {
  if (!state.ui2Shell) return;
  document.querySelectorAll('#ui2Tabs .ui2-nav-item[data-view]').forEach((btn) => {
    const on = btn.dataset.view === view || (btn.dataset.views || '').split(' ').includes(view);
    btn.classList.toggle('is-active', on);
    if (on) btn.setAttribute('aria-current', 'page');
    else btn.removeAttribute('aria-current');
  });
  const main = byId('ui2Main');
  if (main) {
    // The open view, for styles and tests that key on it.
    main.dataset.view = view || '';
    main.scrollTop = 0;
  }
  syncSpeakersArea(view);
  // Help and info (shell2/help): wired the first time it opens, painted on
  // every visit.
  if (view === 'help') mountHelp(byId('view-help'), deps);
  syncHelpView(view);
}

// syncShellPlayer shows the player only with a speaker to control. Without
// one its controls are hidden AND inert, so neither a click nor the keyboard
// can reach a transport that has nothing to act on, and the player says what
// to do instead.
//
// The volume, the group volume, the transport, More (the queue) and Levels
// also follow the polling scope (statusrefreshscope.js): in Setup and while a
// speaker update runs nothing keeps them in sync, so they are not on screen
// there either, and a slider never shows a level the speaker left long ago.
// What is playing stays, it is kept current there too.
//
// Phase 5: a controlled speaker that is offline is no live player either
// (its transport would only fail): the controls go, and the player says
// "Kitchen is not reachable." with Search again. Without a speaker the empty
// slot says why (searching, none found, none chosen) with the next step.
export function syncShellPlayer(hasBox = !!state.currentBox) {
  if (!state.ui2Shell) return;
  const player = byId('ui2Player');
  if (!player) return;
  const ps = playerStateModel({ currentBox: hasBox ? state.currentBox : null, boxes: state.boxes || [], discovery: discoveryNow(), label });
  const offline = ps.kind === 'offline';
  const live = !!hasBox && !offline && playerOnScreen({ view: state.view, persistentPlayer: true, otaInProgress: !!state.otaInProgress });
  const show = (sel, on) => player.querySelectorAll(sel).forEach((el) => {
    el.hidden = !on;
    el.inert = !on;
  });
  show('.ui2-player-now', !!hasBox);
  show('.ui2-player-transport, .ui2-player-more, .ui2-player-volume, .ui2-player-groupvol', live);
  const empty = player.querySelector('.ui2-player-empty');
  if (empty) {
    empty.hidden = !!hasBox && !offline;
    paintPlayerState(empty, ps);
  }
  player.classList.toggle('is-offline', offline);
  // The error line spoke about one speaker: another one, or none, clears it.
  const curHost = hasBox && state.currentBox ? state.currentBox.host : '';
  if (playerStatusHost && playerStatusHost !== curHost) setPlayerStatus('');
  if (!live && pops.more) pops.more.close(false);
  playerLive = live;
  applyPlayerGroup();
  // A speaker that went offline (or another one chosen) takes the source
  // button with it until its line is drawn again.
  paintGoSource();
}

// syncShellGroupPanel is how group.js tells the player whether the selected
// speaker leads a group with a group volume (renderGroupControl decides it,
// from the same followers its sliders act on). A no-op without the shell.
export function syncShellGroupPanel(shown) {
  if (!state.ui2Shell) return;
  groupShown = !!shown;
  applyPlayerGroup();
}

// applyPlayerGroup is the one place that shows or hides the grouped player:
// Levels and the group look only when the player is live AND grouped. While
// grouped, the group slider takes the place of the speaker's own volume (its
// level is the first row in Levels); mute stays.
function applyPlayerGroup() {
  const player = byId('ui2Player');
  if (!player) return;
  const on = playerLive && groupShown;
  player.classList.toggle('is-grouped', on);
  player.querySelectorAll('.ui2-player-levels').forEach((el) => {
    el.hidden = !on;
    el.inert = !on;
  });
  if (!on && pops.levels && pops.levels.isOpen()) {
    const panel = byId('ui2LevelsPop');
    pops.levels.close(false);
    // Focus inside a panel that just closed would be lost: it goes back to the
    // player's own volume, which is on screen again.
    if (panel && panel.contains(document.activeElement)) {
      const vol = byId('musicVolume');
      if (vol) vol.focus();
    }
  }
}

// levelsText names who plays along with the controlled speaker, from the same
// group the sidebar pins (sidebarModel), so the two never disagree. full is
// the sentence ("Living Room + 1 more"), the button's title and the end of
// its name; short ("Living Room +1") is what the button shows, so a long word
// for "more" never cuts the name off.
function levelsText(model) {
  const cur = state.currentBox;
  if (!cur) return { full: '', short: '' };
  const name = label(cur);
  const n = model.memberCount || 0;
  if (n > 0) return { full: t('ui2.player.outputGroup', { name, n }), short: `${name} +${n}` };
  const solo = t('ui2.player.outputSolo', { name });
  return { full: solo, short: solo };
}

// renderShellSpeakers repaints the sidebar's speaker list from the shared
// state. Called from every place the speaker list, a speaker's live state, the
// zones or a pending group edit change; a signature of what it would draw
// skips the repaint when nothing did, so the poll paths can call it freely.
export function renderShellSpeakers(force) {
  if (!state.ui2Shell) return;
  speakersSettled = true;
  paintSpeakers(!!force);
  // The player follows the same list: an offline speaker you control loses
  // its controls, and gets them back when it answers again.
  syncShellPlayer();
  // The Speakers area reads the same state: its list and the open speaker's
  // head follow every change the sidebar does.
  refreshSpeakersArea();
  // The views that draw a state of their own (Radio) follow too.
  notifyStates();
}

// discoveryNow is main.js state.discovery, handed in through deps; null
// before the first search started (which reads as searching).
function discoveryNow() {
  if (typeof deps.discovery === 'function') return deps.discovery() || null;
  return state.discovery || null;
}

function paintSpeakers(force) {
  if (!state.ui2Shell) return;
  confirmAwaiting();
  // Notes about speakers that answer again, or left the list, go.
  for (const [h, n] of [...rowNotes]) {
    const b = (state.boxes || []).find((x) => x && x.host === h);
    if (!b || (n.action === 'whatToTry' && !b.offline)) rowNotes.delete(h);
  }
  const pendingEdit = typeof deps.groupEditPendingHosts === 'function' ? deps.groupEditPendingHosts() : new Set();
  const ctx = {
    currentBox: state.currentBox,
    nowPlayState: state.nowPlayState,
    nowSource: state.nowSource,
    boxPlaying: state.boxPlaying,
    needsUpdate: deps.boxNeedsUpdate,
    cannotReachInternet: deps.boxCannotReachInternet,
    stereoPairs: stereoPairsOf(state.zoneLive || {}),
    installHolds: state.installHolds || {},
    zoneLive: state.zoneLive || {},
    pending: new Set([...pendingEdit, ...awaiting.keys()]),
    searching: !speakersSettled && !(state.boxes || []).length,
    // Phase 5: the empty list says why from the discovery itself.
    discovery: typeof deps.discovery === 'function' ? discoveryNow() : undefined,
    notes: rowNotes,
    nowName: state.nowName,
    nowLocation: state.nowLocation,
    boxNow: state.boxNow || {},
    review: joinReviewHost,
    attention: deps.speakerAttention,
    label,
    offlineTitle: deps.offlineTitle,
    pairName: deps.pairName,
    boxPaused: state.boxPaused,
  };
  const waiting = () => new Map([...transportWait].map(([h, w]) => [h, w.want]));
  ctx.transportPending = waiting();
  let full = sidebarModel(state.boxes, ctx);
  // A row's Play/Pause press the poll has now confirmed (or given up on)
  // is no longer pending: the model again, without the mark.
  if (settleTransport(full)) {
    ctx.transportPending = waiting();
    full = sidebarModel(state.boxes, ctx);
  }
  // A review whose row no longer asks for one (the speaker stopped, joined,
  // went offline, left the list) closes by itself; "+" then acts at once.
  if (joinReviewHost && ![...full.pinned, ...full.others].some((r) => r.host === joinReviewHost && r.reviewOpen)) {
    joinReviewHost = '';
  }
  const levels = levelsText(full);
  // The search (spksearch.js) only narrows what the list shows; the player's
  // Levels keep reading the whole model.
  const findShown = syncSpeakerSearch(searchShown(full));
  const model = filterSidebarModel(full, findShown ? spkQuery : '', findTerms);
  lastFindSearch = model.search;
  const sig = JSON.stringify({
    pinned: model.pinned, others: model.others, head: model.headingKey,
    alone: model.pairAlone, searching: model.searching, levels,
    empty: model.emptyPhase, emptyError: model.emptyError, allOffline: model.allOffline, busy: stateSearchBusy(),
    search: model.search,
  });
  if (!force && sig === lastSpeakersSig) return;
  const lists = byId('ui2SpkLists');
  // Connect by address in the empty list holds what is being typed: no
  // repaint (not even a forced one) takes it away; the next one after it
  // catches up.
  if (lists && connectBusy(lists)) return;
  lastSpeakersSig = sig;

  if (lists) {
    // A repaint keeps the keyboard where it was (same speaker, same button)
    // and both lists where they were scrolled to.
    const active = document.activeElement;
    const inList = !!active && lists.contains(active) && !!active.dataset && !!active.dataset.host;
    const keepHost = inList ? active.dataset.host : '';
    // On "+" or "-", or on the row's Play/Pause: the same button again (a
    // Play/Pause that left with the music falls back to the name). In the
    // join review: the same Add or Cancel, else the "+" that opened it.
    let keepCls = '';
    if (inList && active.classList.contains('ui2-spk-act')) keepCls = '.ui2-spk-act';
    else if (inList && active.classList.contains('ui2-spk-tp')) keepCls = '.ui2-spk-tp';
    const keepJoin = inList && active.classList.contains('ui2-spk-join-btn') ? active.dataset.join : '';
    if (keepJoin) keepCls = '.ui2-spk-act';
    // A new search starts the list at its top; a poll keeps where it was.
    const queryChanged = model.search.query !== paintedQuery;
    paintedQuery = model.search.query;
    const scrolls = [...lists.querySelectorAll(SPK_LISTS)].map((el) => el.scrollTop);
    const keepField = connectFocus(lists);
    lists.innerHTML = sidebarHTML(model, { focusHost: keepHost || rovingHost });
    restoreConnectFocus(lists, keepField);
    fitSpeakerLists(lists);
    [...lists.querySelectorAll(SPK_LISTS)].forEach((el, i) => {
      if (typeof scrolls[i] === 'number') el.scrollTop = scrolls[i];
    });
    if (queryChanged) {
      const others = lists.querySelector('.ui2-spk-scroll');
      if (others) others.scrollTop = 0;
    }
    if (keepHost) {
      const sel = (cls) => [...lists.querySelectorAll(cls)].find((b) => b.dataset.host === keepHost);
      const again = (keepJoin && sel(`.ui2-spk-join-btn[data-join="${keepJoin}"]`))
        || (keepCls && sel(keepCls)) || sel('.ui2-spk-pick');
      if (again) again.focus();
    }
  }
  const text = byId('ui2LevelsText');
  if (text) text.textContent = levels.short;
  const btn = byId('ui2LevelsBtn');
  if (btn) {
    if (levels.full) {
      const name = `${t('ui2.player.levels')}: ${levels.full}`;
      btn.setAttribute('aria-label', name);
      btn.title = name;
    } else {
      btn.removeAttribute('aria-label');
      btn.removeAttribute('title');
    }
  }
}

// The two speaker lists of the sidebar: the pinned group (capped at
// --u-spk-pinned rows, shell2.css) and the other speakers, which take the
// rest of the sidebar's height.
const SPK_LISTS = '.ui2-spk-pinned .ui2-spk-list, .ui2-spk-scroll';

// fitSpeakerLists ends each list that scrolls below a whole row (endOnRow)
// and does it again whenever the speaker section changes size (a resize of
// the window, a text size change). The sidebar holds nothing else that wants
// its height since the navigation moved into the tab row (2026-11-02), so the
// section simply takes all of it; nothing is measured against anything else.
let fitWatch = null;
let fitWatched = null;
function fitSpeakerLists(lists) {
  const section = lists.closest('.ui2-spk');
  if (section && section !== fitWatched && typeof ResizeObserver === 'function') {
    if (fitWatch) fitWatch.disconnect();
    fitWatch = new ResizeObserver(() => {
      const l = byId('ui2SpkLists');
      if (l) fitSpeakerLists(l);
    });
    fitWatch.observe(section);
    fitWatched = section;
  }
  lists.querySelectorAll(SPK_LISTS).forEach(endOnRow);
}

// endOnRow lets a scrolling list end below a whole row instead of through
// one: the height the sidebar gives it is cut back to the last row that fits
// whole (its focus ring room included); the shadow at the cut edge
// (shell2.css) says the list goes on. A list that shows every row keeps its
// height.
function endOnRow(list) {
  list.style.maxHeight = '';
  const room = list.clientHeight;
  if (list.scrollHeight <= room + 1) return;
  const top = layoutTop(list);
  const pad = 4;
  const rows = [...list.querySelectorAll('.ui2-spk-row')];
  let best = 0;
  for (let i = 0; i < rows.length; i++) {
    const bottom = layoutTop(rows[i]) - top + rows[i].offsetHeight;
    if (bottom + pad > room) break;
    // The visible box (the padding included) stops where the next row
    // starts, so not even the edge of its button shows.
    const next = rows[i + 1];
    best = next ? Math.min(bottom + pad, layoutTop(next) - top) : bottom + pad;
  }
  // content-box: the max-height counts the rows; the padding comes on top.
  if (best > 2 * pad) list.style.maxHeight = `${best - 2 * pad}px`;
}

// layoutTop is el's distance from the top of the page in layout units: the
// offset chain, which ignores scrolling and, unlike getBoundingClientRect,
// is not scaled by the text-size zoom, so it compares with offsetHeight.
function layoutTop(el) {
  let y = 0;
  for (let n = el; n; n = n.offsetParent) y += n.offsetTop;
  return y;
}

// mirrorShellBadges copies the counts the classic tab bar shows onto the
// preview: speakers due an update on the Speakers tab, groups on Speakers >
// Groups. The badge shows the bare number; its accessible name says what is
// counted, worded so it reads right for any number in every language.
export function mirrorShellBadges(o = {}) {
  if (!state.ui2Shell) return;
  if (Object.prototype.hasOwnProperty.call(o, 'settingsUpdates')) {
    const el = byId('ui2SpeakersBadge');
    if (el) {
      const n = Math.max(0, Number(o.settingsUpdates) || 0);
      el.hidden = n === 0;
      el.textContent = n ? String(n) : '';
      paintBadgeName(el, n ? t('ui2.nav.updatesCount', { n }) : '');
    }
  }
  if (o.groups) {
    const el = byId('ui2GroupsBadge');
    if (el) {
      const n = Math.max(0, Number(o.groups.n) || 0);
      el.hidden = n === 0;
      el.textContent = n ? String(n) : '';
      el.classList.toggle('u-badge--muted', n > 0 && !!o.groups.stored);
      paintBadgeName(el, n ? (o.groups.tip || '') : '');
    }
  }
}

function paintBadgeName(el, name) {
  if (name) {
    el.setAttribute('role', 'img');
    el.setAttribute('aria-label', name);
    el.title = name;
  } else {
    el.removeAttribute('role');
    el.removeAttribute('aria-label');
    el.removeAttribute('title');
  }
}
