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
import { groupMembersOf, masterBoxForKey, masterOf as zoneMasterOf, stereoPairsOf } from '../groups.js';
import { shellMarkup } from './markup.js';
import { wirePopover } from './popover.js';
import { roomButtonHTML, roomButtonLabel, roomItems, roomSwitcherHTML } from './rooms.js';
import { playerOnScreen } from '../statusrefreshscope.js';

const VIEW_IDS = ['view-box', 'view-library', 'view-recent', 'view-settings', 'view-setup', 'view-multiroom', 'view-spotify', 'view-wishes'];

let deps = {};
const pops = {};
let lastRoomsSig = '';
let roomsSettled = false;

const byId = (id) => document.getElementById(id);
const label = (b) => (typeof deps.getBoxLabel === 'function' ? deps.getBoxLabel(b) : '') || (b && (b.friendlyName || b.name || b.host)) || '';

export function shell2Mounted() {
  return !!state.ui2Shell;
}

// shellRoomPopOpen reports whether the room switcher's list is open. While it
// is, the every-speaker poll runs off the Music tab too, so the list it shows
// is live and not a snapshot from the last visit to that tab.
export function shellRoomPopOpen() {
  return !!(state.ui2Shell && pops.rooms && pops.rooms.isOpen());
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
    fOutputBody: q('#ui2OutputBody'),
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
    move(n.groupControl, n.fOutputBody);
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
  wire(frame, skip);
  state.ui2Shell = true;
  document.documentElement.classList.add('ui2');
  syncShellNav(state.view);
  syncShellPlayer(!!state.currentBox);
  paintRooms(true);
  return true;
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

  pops.rooms = wirePopover(frame.querySelector('#ui2RoomBtn'), frame.querySelector('#ui2RoomPop'), {
    roving: true,
    onOpen: () => {
      paintRooms(true);
      // One immediate round of the every-speaker poll, so the list is live the
      // moment it opens; main.js keeps it going while it stays open.
      if (typeof deps.onRoomsOpen === 'function') deps.onRoomsOpen();
    },
  });
  pops.more = wirePopover(frame.querySelector('#ui2MoreBtn'), frame.querySelector('#ui2MorePop'), { roving: false });
  pops.output = wirePopover(frame.querySelector('#ui2OutputBtn'), frame.querySelector('#ui2OutputPop'), { roving: false });

  // The way to add a speaker sits outside the repainted list (markup.js), so
  // it keeps focus across a live repaint and has its own handler.
  const add = frame.querySelector('#ui2RoomAdd');
  if (add) {
    add.addEventListener('click', () => {
      if (pops.rooms) pops.rooms.close(true);
      if (typeof deps.switchView === 'function') deps.switchView('setup');
    });
  }
  const list = frame.querySelector('#ui2RoomList');
  if (list) {
    list.addEventListener('click', (e) => {
      const row = e.target.closest('.ui2-room-row');
      if (!row) return;
      const host = row.dataset.host;
      const port = parseInt(row.dataset.port, 10);
      const box = (state.boxes || []).find((b) => b && b.host === host && b.port === port);
      if (pops.rooms) pops.rooms.close(true);
      if (box && typeof deps.pickBox === 'function') deps.pickBox(box);
    });
  }
}

// syncShellNav marks the open view in the sidebar and starts it at the top,
// the way a fresh tab did when the whole window scrolled.
export function syncShellNav(view) {
  if (!state.ui2Shell) return;
  document.querySelectorAll('#ui2Sidebar .ui2-nav-item[data-view]').forEach((btn) => {
    const on = btn.dataset.view === view;
    btn.classList.toggle('is-active', on);
    if (on) btn.setAttribute('aria-current', 'page');
    else btn.removeAttribute('aria-current');
  });
  const main = byId('ui2Main');
  if (main) main.scrollTop = 0;
}

// syncShellPlayer shows the player only with a speaker to control. Without
// one its controls are hidden AND inert, so neither a click nor the keyboard
// can reach a transport that has nothing to act on, and the player says what
// to do instead.
//
// The volume, the transport and More (the queue) also follow the polling
// scope (statusrefreshscope.js): in Setup and while a speaker update runs
// nothing keeps them in sync, so they are not on screen there either, and a
// slider never shows a level the speaker left long ago. What is playing and
// the Output stay, they are kept current there too.
export function syncShellPlayer(hasBox = !!state.currentBox) {
  if (!state.ui2Shell) return;
  const player = byId('ui2Player');
  if (!player) return;
  const live = !!hasBox && playerOnScreen({ view: state.view, persistentPlayer: true, otaInProgress: !!state.otaInProgress });
  const show = (sel, on) => player.querySelectorAll(sel).forEach((el) => {
    el.hidden = !on;
    el.inert = !on;
  });
  show('.ui2-player-now, .ui2-player-output', !!hasBox);
  show('.ui2-player-transport, .ui2-player-more, .ui2-player-volume', live);
  const empty = player.querySelector('.ui2-player-empty');
  if (empty) empty.hidden = !!hasBox;
  if (!live && pops.more) pops.more.close(false);
  if (!hasBox && pops.output) pops.output.close(false);
}

// outputState names who plays along with the controlled speaker: alone, or
// with how many others in its live group. Read from the shared zone poll, the
// same membership the group chips in the Output popover act on.
function outputState() {
  const cur = state.currentBox;
  if (!cur) return '';
  const name = label(cur);
  const zl = state.zoneLive || {};
  const mk = zoneMasterOf(cur.deviceID, zl);
  let others = 0;
  if (mk) {
    const master = masterBoxForKey(mk, zl, state.boxes) || cur;
    others = groupMembersOf(master, zl, state.boxes).length;
  }
  return others > 0 ? t('ui2.player.outputGroup', { name, n: others }) : t('ui2.player.outputSolo', { name });
}

// renderShellRooms repaints the room switcher and the Output button from the
// shared state. Called from every place the speaker list or a speaker's live
// state changes; a signature of what it would draw skips the repaint when
// nothing did, so the poll paths can call it freely.
export function renderShellRooms(force) {
  if (!state.ui2Shell) return;
  roomsSettled = true;
  paintRooms(!!force);
}

function paintRooms(force) {
  if (!state.ui2Shell) return;
  const ctx = {
    currentBox: state.currentBox,
    nowPlayState: state.nowPlayState,
    nowSource: state.nowSource,
    boxPlaying: state.boxPlaying,
    needsUpdate: deps.boxNeedsUpdate,
    cannotReachInternet: deps.boxCannotReachInternet,
    stereoPairs: stereoPairsOf(state.zoneLive || {}),
    label,
    offlineTitle: deps.offlineTitle,
    pairName: deps.pairName,
  };
  const items = roomItems(state.boxes, ctx);
  const searching = !roomsSettled && !(state.boxes || []).length;
  let current = items.find((i) => i.isCurrent) || null;
  if (!current && state.currentBox) current = { host: state.currentBox.host, name: label(state.currentBox), pill: '', chips: [] };
  const output = outputState();
  const sig = JSON.stringify({ items, current, searching, output });
  if (!force && sig === lastRoomsSig) return;
  lastRoomsSig = sig;

  const btn = byId('ui2RoomBtn');
  const body = byId('ui2RoomBtnBody');
  if (btn && body) {
    body.innerHTML = roomButtonHTML(current);
    btn.setAttribute('aria-label', roomButtonLabel(current));
  }
  const list = byId('ui2RoomList');
  if (list) {
    const pop = byId('ui2RoomPop');
    const active = document.activeElement;
    const keepHost = pop && active && pop.contains(active) && active.classList.contains('ui2-room-row')
      ? active.dataset.host : '';
    list.innerHTML = roomSwitcherHTML({ items, searching });
    if (keepHost) {
      const again = [...list.querySelectorAll('.ui2-room-row')].find((r) => r.dataset.host === keepHost);
      if (again) again.focus();
    }
  }
  const outText = byId('ui2OutputText');
  if (outText) outText.textContent = output;
  const summary = byId('ui2OutputSummary');
  if (summary) summary.textContent = output;
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
