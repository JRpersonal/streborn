// shell2/speakers/index.js: the router of the UI 2.0 Speakers area (phase 3,
// #1287; docs/UI-2.0.md, "Navigation, current and new").
//
// One sidebar entry, Speakers, and four places under it (routes.js):
//
//   list     every speaker with its state             view 'speakers' (list.js)
//   speaker  one speaker, five tabs                   view 'settings' (speaker.js)
//   groups   stereo pairs and fixed groups            view 'multiroom' (groups.js)
//   add      the existing setup (wizard: phase 4)     view 'setup'
//
// A route is a view: opening one calls the app's own switchView, so the
// classic lifecycle of each view (the settings load and reconnect loop, the
// Multi-room live poll and its stop on leaving, the setup drive scan) runs
// exactly as in the classic layout. On top of the view sits the area head
// (#ui2SpkArea, markup.js): the title and the row All speakers / Groups / Add
// speaker, and on the speaker route the speaker's page head and tabs.
//
// The sidebar speaker list and the Speakers area answer different questions.
// A sidebar row chooses the speaker you control (phase 1, unchanged); "+" and
// "-" there choose who plays along. The settings of a speaker live on its
// page, Speakers > one speaker, which always shows the speaker you control:
// opening a speaker from the list makes it the controlled one (lock-step, the
// classic Settings dropdown does the same), and choosing another sidebar row
// while the page is open moves the page with it (pickBoxFromShell reloads the
// settings, renderShellSpeakers repaints the head through refreshSpeakersArea).
//
// Routing API:
//   openSpeakers(route = 'list', { host, tab })  go to a place; host picks the
//       speaker for 'speaker' (a Bose original goes to 'add' instead) and the
//       setup target for 'add'; tab picks the speaker page's tab. Returns
//       false (and does nothing) without the preview shell.
//   speakersRoute()            { route, host, tab } of what is on screen; route
//                              is '' outside the area.
//   syncSpeakersArea(view)     mount.js syncShellNav: shows or hides the head,
//                              marks the row, renders the route's part.
//   refreshSpeakersArea()      mount.js renderShellSpeakers: repaints the open
//                              part from the shared state.
//   initSpeakersArea(frame, deps)  mount.js wire(): the mount deps (switchView,
//                              speakerPicked, getBoxLabel, boxNeedsUpdate, ...).
//
// Nothing here runs without the shell (every entry checks state.ui2Shell),
// and nothing touches the DOM at import.

import { state } from '../../state.js';
import { routeOfView, ROUTE_VIEW, SPEAKER_ROUTES, normalizeTab } from './routes.js';
import { renderSpeakerList } from './list.js';
import { currentSpeakerTab, renderSpeakerPage } from './speaker.js';
import { mountGroups } from './groups.js';
import './speakers.css';

let deps = {};
let frameRoot = null;
// A tab asked for by openSpeakers, applied on the next paint of the page.
let wantTab = '';

const byId = (id) => (frameRoot && frameRoot.querySelector(`#${id}`)) || document.getElementById(id);
const label = (b) => (typeof deps.getBoxLabel === 'function' ? deps.getBoxLabel(b) : '') || (b && (b.friendlyName || b.name || b.host)) || '';
const findBox = (host) => (state.boxes || []).find((b) => b && b.host === host) || null;

export function initSpeakersArea(frame, d = {}) {
  deps = d;
  frameRoot = frame || null;
  const nav = frame && frame.querySelector('#ui2SpkSubnav');
  if (!nav) return;
  nav.addEventListener('click', (e) => {
    const b = e.target.closest('.spa-sub[data-route]');
    if (b) openSpeakers(b.dataset.route);
  });
}

export function speakersRoute() {
  const route = routeOfView(state.view);
  const box = route === 'speaker' ? state.settingsBox : null;
  return { route, host: box ? box.host : '', tab: route === 'speaker' ? currentSpeakerTab() : '' };
}

export function openSpeakers(route = 'list', opts = {}) {
  if (!state.ui2Shell || typeof deps.switchView !== 'function') return false;
  let r = SPEAKER_ROUTES.includes(route) ? route : 'list';
  const box = opts.host ? findBox(opts.host) : null;
  if (r === 'speaker' && opts.host && !box) r = 'list';
  // A Bose original has no STR settings yet: its place is the setup.
  if (r === 'speaker' && box && box.kind === 'stock') r = 'add';
  if (r === 'speaker') {
    if (opts.tab) wantTab = normalizeTab(opts.tab);
    if (box) {
      const prev = state.settingsBox;
      // Lock-step, as the classic Settings dropdown: the page's speaker is the
      // one you control (speakerPickedInTab).
      if (typeof deps.speakerPicked === 'function') deps.speakerPicked(box);
      else state.settingsBox = box;
      if (state.view === 'settings' && state.settingsBox !== prev && state.settingsReconnect) {
        // The same reset the Settings picker's own change does: the previous
        // speaker's reconnect loop must not run on against the new one.
        if (state.settingsReconnect.timer) clearTimeout(state.settingsReconnect.timer);
        state.settingsReconnect = null;
      }
    }
  } else if (r === 'add' && box) {
    // The setup opens on this speaker (a stock one: the network install).
    if (typeof deps.speakerPicked === 'function') deps.speakerPicked(box);
  }
  // Always through switchView, also for the view already open: the same as
  // clicking the open tab in the classic layout (it loads the settings again,
  // redraws the setup picker, repaints Multi-room).
  deps.switchView(ROUTE_VIEW[r]);
  return true;
}

// syncSpeakersArea shows the area head on the four routes and paints the
// route's part. Called from syncShellNav on every view switch.
export function syncSpeakersArea(view) {
  if (!state.ui2Shell) return;
  const area = byId('ui2SpkArea');
  if (!area) return;
  const route = routeOfView(view);
  area.hidden = !route;
  area.dataset.route = route;
  area.querySelectorAll('.spa-sub[data-route]').forEach((b) => {
    const on = b.dataset.route === route;
    b.classList.toggle('is-active', on);
    if (on) b.setAttribute('aria-current', 'page');
    else b.removeAttribute('aria-current');
  });
  const page = byId('ui2SpkPage');
  if (page) page.hidden = route !== 'speaker';
  paintRoute(route);
}

export function refreshSpeakersArea() {
  if (!state.ui2Shell) return;
  const route = routeOfView(state.view);
  if (route) paintRoute(route);
}

function paintRoute(route) {
  if (route === 'list') {
    renderSpeakerList(byId('view-speakers'), {
      boxes: state.boxes || [],
      currentBox: state.currentBox,
      label,
      open: openSpeakers,
      needsUpdate: deps.boxNeedsUpdate,
      cannotReachInternet: deps.boxCannotReachInternet,
      zoneLive: state.zoneLive || {},
      boxPlaying: state.boxPlaying || {},
      offlineTitle: deps.offlineTitle,
      pairName: deps.pairName,
    });
  } else if (route === 'speaker') {
    const tab = wantTab;
    wantTab = '';
    renderSpeakerPage(byId('ui2SpkPage'), {
      box: state.settingsBox || null,
      tab: tab || undefined,
      label,
      // The head's state pill reads the sidebar's own checks.
      needsUpdate: deps.boxNeedsUpdate,
      cannotReachInternet: deps.boxCannotReachInternet,
    });
  } else if (route === 'groups') {
    mountGroups({ root: byId('view-multiroom'), open: openSpeakers, label });
  }
}
