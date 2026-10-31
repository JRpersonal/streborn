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
import { groupMembersOf, stereoPairsOf } from '../groups.js';
import { shellMarkup } from './markup.js';
import { wirePopover } from './popover.js';
import { rovingIndex, sidebarHTML, sidebarModel } from './speakers.js';
import { playerOnScreen } from '../statusrefreshscope.js';
import { initSpeakersArea, openSpeakers, refreshSpeakersArea, syncSpeakersArea } from './speakers/index.js';
import { agoText } from './speakers/list.js';
import {
  configureStates, connectBusy, connectFocus, notifyStates, restoreConnectFocus, paintPlayerState, playerStateModel, setReportHook,
  stateSearchBusy, transportErrorText, wireStates,
} from './state.js';

const VIEW_IDS = ['view-box', 'view-library', 'view-recent', 'view-settings', 'view-setup', 'view-multiroom', 'view-spotify', 'view-wishes'];

let deps = {};
const pops = {};
let lastSpeakersSig = '';
let speakersSettled = false;
// The sidebar row that takes Tab, kept across repaints (roving tabindex).
let rovingHost = '';
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
// The speaker the player's error line spoke about.
let playerStatusHost = '';

const byId = (id) => document.getElementById(id);
const label = (b) => (typeof deps.getBoxLabel === 'function' ? deps.getBoxLabel(b) : '') || (b && (b.friendlyName || b.name || b.host)) || '';

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
    fTransport: q('.ui2-player-transport'),
    fVolume: q('.ui2-player-volume'),
    fMoreBody: q('#ui2MoreBody'),
    fLevelsBody: q('#ui2LevelsBody'),
    fSpeakers: q('#ui2SpkLists'),
    fVersion: q('#ui2Version'),
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
    sourceButtons: byId('sourceButtons'),
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
    n.views.forEach((v) => move(v, n.fMainInner));
    move(n.footer, n.fMainInner);
    move(n.statusBar, n.fNow);
    move(n.titleSwapRow, n.fNow);
    move(n.controls, n.fTransport);
    move(n.volume, n.fVolume);
    move(n.queueControls, n.fMoreBody);
    move(n.sourceButtons, n.fMoreBody);
    move(n.groupControl, n.fLevelsBody);
    move(n.a11y, n.fAppSettings);
    move(n.locale, n.fAppSettings);
    move(n.logo, n.fBrand);
    move(n.brand, n.fBrand);
    move(n.tagline, n.fBrand);
    move(n.supported, n.fBrand);
  } catch (e) {
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
  announceToasts();
  wire(frame, skip);
  state.ui2Shell = true;
  document.documentElement.classList.add('ui2');
  watchAppUpdateBanner(n.appUpdateBanner, n.fVersion, n.fBanners);
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
    btn.addEventListener('keydown', (e) => {
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
      const list = navItems();
      const at = list.indexOf(btn);
      const next = list[e.key === 'ArrowDown' ? Math.min(list.length - 1, at + 1) : Math.max(0, at - 1)];
      if (next) { e.preventDefault(); next.focus(); }
    });
  });

  pops.more = wirePopover(frame.querySelector('#ui2MoreBtn'), frame.querySelector('#ui2MorePop'));
  // Levels: the per-speaker sliders of the group, opened from the player. Only
  // sliders: the chips that choose speakers are hidden there (shell2.css), the
  // sidebar is the one place that chooses.
  pops.levels = wirePopover(frame.querySelector('#ui2LevelsBtn'), frame.querySelector('#ui2LevelsPop'));
  wireSpeakerList(frame.querySelector('#ui2SpkLists'));
  // The Speakers area (phase 3): its row of places and its routes.
  initSpeakersArea(frame, deps);
  wireStatesEnv(frame);
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
  const transport = frame.querySelector('.ui2-player-transport');
  const more = frame.querySelector('#ui2MoreBody');
  // A new press clears the last failure; its own result writes a new one.
  for (const el of [transport, more]) {
    if (el) el.addEventListener('click', () => setPlayerStatus(''), true);
  }
  ensureSpeakerLive(frame);
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
function groupReport(r = {}) {
  const hosts = (r.hosts || []).filter(Boolean);
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
  // A repaint after the confirming zone fetch and after the deadline, in case
  // nothing else repaints then.
  setTimeout(() => paintSpeakers(false), CONFIRM_AFTER_MS + 600);
  setTimeout(() => paintSpeakers(false), CONFIRM_WITHIN_MS + 100);
}

// confirmAwaiting checks the waiting group edits against the zone poll: the
// optimistic update group.js writes at once does not count, only what the
// poll shows after it.
function confirmAwaiting() {
  if (!awaiting.size) return;
  const now = Date.now();
  const boxes = state.boxes || [];
  for (const [h, w] of [...awaiting]) {
    if (now - w.at < CONFIRM_AFTER_MS) continue;
    const m = boxes.find((b) => b && b.host === w.master);
    const inGroup = !!m && groupMembersOf(m, state.zoneLive || {}, boxes).some((x) => x && x.ip === h);
    if ((w.want === 'in') === inGroup) {
      awaiting.delete(h);
      announceSpeakers(t(w.want === 'in' ? 'ui2.state.playsAlong' : 'ui2.state.isOut', { name: w.name }));
    } else if (now - w.at > CONFIRM_WITHIN_MS) {
      awaiting.delete(h);
      rowNotes.set(h, { tone: 'error', text: t('ui2.state.groupNoConfirm', { name: w.name }), action: 'retry', master: w.master, port: w.port, host: h });
    }
  }
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
  root.addEventListener('click', (e) => {
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
    const act = e.target.closest('.ui2-spk-act');
    if (act) {
      rovingHost = act.dataset.host;
      if (typeof deps.toggleGroupMember === 'function') deps.toggleGroupMember(act.dataset.host, parseInt(act.dataset.port, 10), act.dataset.master || '', groupReport);
      return;
    }
    const pick = e.target.closest('.ui2-spk-pick');
    if (!pick) return;
    rovingHost = pick.dataset.host;
    const box = boxFor(pick);
    if (!box || typeof deps.pickBox !== 'function') return;
    Promise.resolve(deps.pickBox(box)).then(() => {
      // Wishes has nothing about a speaker to show: choosing one there opens
      // Radio, so the main area shows the speaker you just chose. Every other
      // view stays open and follows the speaker.
      if (state.view === 'wishes' && state.currentBox && state.currentBox.host === box.host
        && typeof deps.switchView === 'function') deps.switchView('box');
    }).catch(() => {});
  });
  root.addEventListener('focusin', (e) => {
    const btn = e.target.closest('.ui2-spk-pick, .ui2-spk-act');
    if (btn) setRoving(root, btn.dataset.host);
  });
  root.addEventListener('keydown', (e) => {
    const btn = e.target.closest('.ui2-spk-pick, .ui2-spk-act');
    if (!btn) return;
    const li = btn.closest('li');
    const isAct = btn.classList.contains('ui2-spk-act');
    const rtl = getComputedStyle(root).direction === 'rtl';
    const toAct = rtl ? 'ArrowLeft' : 'ArrowRight';
    const toPick = rtl ? 'ArrowRight' : 'ArrowLeft';
    if (!isAct && e.key === toAct) {
      const a = li && li.querySelector('.ui2-spk-act');
      if (a) { e.preventDefault(); a.focus(); }
      return;
    }
    if (isAct && e.key === toPick) {
      const p = li && li.querySelector('.ui2-spk-pick');
      if (p) { e.preventDefault(); p.focus(); }
      return;
    }
    // Up, Down, Home and End move through every row, the pinned group and the
    // others as one sequence.
    const picks = [...root.querySelectorAll('.ui2-spk-pick')];
    const at = picks.indexOf(li ? li.querySelector('.ui2-spk-pick') : null);
    const next = rovingIndex(e.key, at, picks.length);
    if (next < 0) return;
    e.preventDefault();
    const target = picks[next];
    setRoving(root, target.dataset.host);
    target.focus();
    try { target.scrollIntoView({ block: 'nearest' }); } catch (_) { /* old engine */ }
  });
}

// setRoving gives Tab to one row: its pick button and its action, and takes it
// from every other row.
function setRoving(root, host) {
  rovingHost = host || '';
  root.querySelectorAll('.ui2-spk-pick, .ui2-spk-act').forEach((b) => {
    b.tabIndex = b.dataset.host === rovingHost ? 0 : -1;
  });
}

// watchAppUpdateBanner keeps the app's version line inside the frame. Quiet
// ("Version ... Check for app updates") it is a footnote, and sits at the
// bottom of the sidebar; a real notice (an update, a download hand-off) is a
// window-wide banner and goes to the banner region on top. main.js renders
// the banner as it always did; this only follows its quiet class, so every
// path that draws it (the quiet link, the full notice, the mac, deb and
// winget hand-offs) lands in the right place without touching them.
function watchAppUpdateBanner(banner, versionSlot, bannersSlot) {
  if (!banner || !versionSlot || !bannersSlot || typeof MutationObserver === 'undefined') return;
  // A manual check is a press inside the banner; the notice it brings back
  // replaces the link that had focus, so focus follows the moved banner.
  let pressedAt = 0;
  banner.addEventListener('click', () => { pressedAt = Date.now(); });
  const place = () => {
    const quiet = banner.classList.contains('app-update-quiet');
    const target = quiet ? versionSlot : bannersSlot;
    if (banner.parentNode === target) return;
    const active = document.activeElement;
    const hadFocus = banner.contains(active)
      || ((!active || active === document.body) && Date.now() - pressedAt < 20000);
    if (quiet) target.appendChild(banner);
    else target.insertBefore(banner, target.firstChild);
    if (hadFocus) {
      const f = banner.querySelector('button, a[href]');
      if (f) f.focus();
    }
  };
  new MutationObserver(place).observe(banner, { attributes: true, attributeFilter: ['class'] });
  place();
}

// syncShellNav marks the open view in the sidebar and starts it at the top,
// the way a fresh tab did when the whole window scrolled. An area entry
// (Speakers) is marked on every view it stands for (data-views), and the
// area's own head follows the view too.
export function syncShellNav(view) {
  if (!state.ui2Shell) return;
  document.querySelectorAll('#ui2Sidebar .ui2-nav-item[data-view]').forEach((btn) => {
    const on = btn.dataset.view === view || (btn.dataset.views || '').split(' ').includes(view);
    btn.classList.toggle('is-active', on);
    if (on) btn.setAttribute('aria-current', 'page');
    else btn.removeAttribute('aria-current');
  });
  const main = byId('ui2Main');
  if (main) {
    // shell2.css reads it: the music views leave the classic footer out.
    main.dataset.view = view || '';
    main.scrollTop = 0;
  }
  syncSpeakersArea(view);
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
    attention: deps.speakerAttention,
    label,
    offlineTitle: deps.offlineTitle,
    pairName: deps.pairName,
  };
  const model = sidebarModel(state.boxes, ctx);
  const levels = levelsText(model);
  const sig = JSON.stringify({
    pinned: model.pinned, others: model.others, head: model.headingKey,
    alone: model.pairAlone, searching: model.searching, levels,
    empty: model.emptyPhase, emptyError: model.emptyError, allOffline: model.allOffline, busy: stateSearchBusy(),
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
    const keepAct = inList && active.classList.contains('ui2-spk-act');
    const scrolls = [...lists.querySelectorAll('.ui2-spk-scroll, .ui2-spk-pinned .ui2-spk-list')].map((el) => el.scrollTop);
    const listsTop = lists.scrollTop;
    const keepField = connectFocus(lists);
    lists.innerHTML = sidebarHTML(model, { focusHost: keepHost || rovingHost });
    restoreConnectFocus(lists, keepField);
    holdSpeakerFloor(lists);
    [...lists.querySelectorAll('.ui2-spk-scroll, .ui2-spk-pinned .ui2-spk-list')].forEach((el, i) => {
      if (typeof scrolls[i] === 'number') el.scrollTop = scrolls[i];
    });
    lists.scrollTop = listsTop;
    if (keepHost) {
      const sel = (cls) => [...lists.querySelectorAll(cls)].find((b) => b.dataset.host === keepHost);
      const again = (keepAct && sel('.ui2-spk-act')) || sel('.ui2-spk-pick');
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

// holdSpeakerFloor sets how far the speaker section may shrink when the
// sidebar is short: the pinned group down to two and a half rows (its list
// scrolls below that) and the others down to three rows and half of the
// fourth, or all of them when there are fewer. Measured, because a row is one
// line or two depending on its name and pill, and the text size zooms all of
// it. The floor is cut at whole rows plus half the next one, so the cut row
// shows a piece of its name next to a piece of its "+", never a bare button
// edge. The navigation entries give up their room only after that
// (shell2.css).
//
// In a window too short for that (a large text size, a low window) the floor
// is held back so two and a half navigation entries and everything below them
// stay on screen; the others then keep at least one row and a half, as long as
// that leaves the entries one and a half. When even that is too much (Extra
// large in the smallest window), the entries keep one and a half and the
// speakers get the rest, at least their first row whole. Below the floor of the
// two lists the section turns into one scroll box (is-squeezed): the pinned
// group and the others scroll together inside it, and nothing in the sidebar
// is ever drawn over anything else. The entries are counted in entry heights
// of the navigation's scroll box, its first group heading included: at Extra
// large in the 1000 x 600 window the one and a half show the heading and most
// of the first entry, and the rest scrolls. The sidebar's own size is watched,
// so a resize or a text size change sets the floor again.
const OTHERS_FLOOR_ROWS = 3;
const NAV_RESERVE_ENTRIES = 2.5;
const NAV_LEAST_ENTRIES = 1.5;
let floorWatch = null;
let floorWatched = null;
function holdSpeakerFloor(lists) {
  const section = lists.closest('.ui2-spk');
  if (!section) return;
  const side = section.parentElement;
  if (side && side !== floorWatched && typeof ResizeObserver === 'function') {
    if (floorWatch) floorWatch.disconnect();
    floorWatch = new ResizeObserver(() => {
      const l = byId('ui2SpkLists');
      if (l) holdSpeakerFloor(l);
    });
    floorWatch.observe(side);
    floorWatched = side;
  }
  // Measured as the two capped lists, never as the one squeezed scroll box.
  section.classList.remove('is-squeezed');
  const pinned = lists.querySelector('.ui2-spk-pinned');
  const pinnedList = pinned && pinned.querySelector('.ui2-spk-list');
  const scroll = lists.querySelector('.ui2-spk-scroll');
  const pinnedH = pinned
    ? pinned.offsetHeight - (pinnedList ? pinnedList.offsetHeight - rowsHeight(pinnedList, 2) : 0)
    : 0;
  const others = scroll ? rowsHeight(scroll, OTHERS_FLOOR_ROWS) : 0;
  const pad = Math.max(0, section.offsetHeight - lists.offsetHeight);
  let floor = pinnedH + others + pad;
  // squeezed: the floor leaves less than the pinned group at its least and a
  // row and a half of the others, so the two lists share one scroll instead.
  let squeezed = false;
  if (side) {
    const nav = side.querySelector('.ui2-side-nav');
    const entry = side.querySelector('.ui2-side-scroll .ui2-nav-item');
    const small = side.querySelector('.ui2-side-small');
    const entryH = entry ? entry.offsetHeight : 0;
    let fixed = small ? small.offsetHeight : 0;
    for (const el of side.children) if (el !== section && el !== nav) fixed += el.offsetHeight;
    const room = Math.max(0, side.clientHeight - fixed);
    // At the least the first row whole, its sub heading above it included.
    const row = lists.querySelector('.ui2-spk-row');
    const spkLeast = Math.min(floor, (row ? layoutTop(row) - layoutTop(lists) + row.offsetHeight + 4 : 0) + pad);
    const navLeast = Math.max(0, room - entryH * NAV_LEAST_ENTRIES);
    const listsLeast = pinnedH + (scroll ? rowsHeight(scroll, 1) : 0) + pad;
    const spkRoom = Math.min(Math.max(room - entryH * NAV_RESERVE_ENTRIES, spkLeast), navLeast);
    floor = Math.min(floor, Math.max(spkRoom, Math.min(listsLeast, navLeast)));
    squeezed = floor < listsLeast - 1;
  }
  section.classList.toggle('is-squeezed', squeezed);
  // The one scroll box is cut at a row too, measured as it now stands.
  if (squeezed) floor = halfRowCut(lists, floor - pad) + pad;
  section.style.minHeight = floor > 0 ? `${Math.ceil(floor)}px` : '';
  lists.querySelectorAll('.ui2-spk-scroll, .ui2-spk-pinned .ui2-spk-list').forEach((el) => endOnRow(el, squeezed));
}

// endOnRow lets a scrolling list end below a whole row instead of through
// one: the height the sidebar gives it is cut back to the last row that fits
// whole (its focus ring room included). What is left goes to the navigation
// below; the shadow at the cut edge (shell2.css) says the list goes on. A list
// that shows every row, and the squeezed lists (one scroll box), keep theirs.
function endOnRow(list, squeezed) {
  list.style.maxHeight = '';
  if (squeezed) return;
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

// halfRowCut is the largest height up to room that ends halfway down a row of
// the squeezed lists or just below a whole one (up to the 4px its focus ring
// would like), or room itself when not even the first row's half fits.
function halfRowCut(lists, room) {
  let best = 0;
  const top = layoutTop(lists);
  for (const row of lists.querySelectorAll('.ui2-spk-row')) {
    const at = layoutTop(row) - top;
    const half = at + row.offsetHeight / 2;
    const whole = at + row.offsetHeight;
    if (half <= room) best = Math.max(best, half);
    if (whole <= room) best = Math.max(best, Math.min(room, whole + 4));
  }
  return best || Math.max(0, room);
}

// layoutTop is el's distance from the top of the page in layout units: the
// offset chain, which ignores scrolling and, unlike getBoundingClientRect,
// is not scaled by the text-size zoom, so it compares with offsetHeight.
function layoutTop(el) {
  let y = 0;
  for (let n = el; n; n = n.offsetParent) y += n.offsetTop;
  return y;
}

// rowsHeight is the height a scrolling list needs to show its first n rows
// whole and half of the next, its own padding included; the full height when
// it holds no more than that. Layout units throughout (layoutTop): a client
// rect is zoomed by the text size and came out a third too tall at Extra
// large.
function rowsHeight(list, n) {
  const rows = [...list.querySelectorAll('.ui2-spk-row')];
  const full = list.scrollHeight + (list.offsetHeight - list.clientHeight);
  if (rows.length <= n) return full;
  const next = rows[n];
  return Math.min(full, layoutTop(next) - layoutTop(list) + next.offsetHeight / 2);
}

// mirrorShellBadges copies the counts the tab bar shows onto the sidebar:
// speakers due an update on Speaker settings, groups on Multi-room. The badge
// shows the bare number; its accessible name says what is counted, worded so
// it reads right for any number in every language.
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
