// views/library.js — the "Library" (DLNA MediaServer browse, BETA) view.
//
// Extracted from the main.js monolith, same pattern as views/recent.js and
// views/settings.js: the module pulls shared things (state, utils, i18n, api)
// from their own modules and receives the few main.js-local helpers it needs
// (showSlotPicker, formatDuration) via initLibraryView, so it never imports
// back into main.js (which would create a cycle). New views should follow this
// pattern so main.js stops growing.
//
// Entry point: openLibrary() is called from main.js's switchView when the
// library tab is opened. The view renders lazily (openLibrary/renderLibrary
// build the DOM inside the functions); there are NO top-level DOM statements,
// because this module is imported at the top of main.js, before the
// #view-library container is created further down. The module-level consts
// (libState/LIB_PAGE/LIB_MAX) are pure data and safe at module scope.

import { state } from '../state.js';
import { $, escapeHtml, escapeAttr, showError, showToast, getBoxLabel, confirmWarn, folderFallbackArt, beginLibraryNowPlaying, friendlyErrorText } from '../utils.js';
import { t } from '../i18n/index.js';
import { keyNum } from '../presetrows.js';
import {
  ProbeTrackDelivery,
  ListMediaServers,
  LibraryRefusedServers,
  SearchLibrary,
  BrowseLibrary,
  AddMediaServerByURL,
  RemoveManualMediaServer,
  PlayURL,
  StartQueue,
  SaveFolderPreset,
  Status,
  EnableBoxMediaServer,
  boxFetch,
} from '../api.js';
import { orderLongFolder } from '../trackorder.js';
import {
  SERVERS_DEPTH, libraryTrail, headerHTML, trailHTML, stateHTML, loadingHTML, levelHeadHTML,
  serverRowsHTML, folderRowHTML, trackRowHTML, folderActionsHTML, filterRowHTML, manualAddHTML,
  countLine, countText, repeatLabelKey, trailParent,
} from '../shell2/library.js';
import { libraryTrackRef, saveLibraryTrackPreset } from '../librarytrackref.js';
import { withoutMacMetadata, speakerRefusingPushes } from '../libraryplayfail.js';

// Injected main.js helpers (see initLibraryView). These stay in main.js because
// they are shared across views; the library code calls them as deps.<name>.
let deps = {
  showSlotPicker() {},
  formatDuration() {},
  // Resolves the box a play must go to (the group master when the selected
  // box is a zone follower, #70). Overridden by main.js; the fallback keeps
  // the old behavior if the dep is ever missing.
  effectivePlayTarget: () => state.currentBox,
  // Told about a speaker picked here so the music tab and the setup target
  // follow along, the same lock-step speaker settings already does.
  speakerPicked() {},
  // The now-playing bar, repainted from the cached state the moment a track
  // or folder is sent, and the status poll, asked again shortly after, so the
  // bar does not keep the previous station's song until the next slow poll
  // (#1394). Both live in views/music; main.js hands them in.
  renderNowPlayingBar() {},
  refreshStatus() {},
};
export function initLibraryView(d) {
  deps = { ...deps, ...d };
}

// ---------- Library (DLNA MediaServer browse, BETA) ----------
//
// Per-session state. servers is the discovered MediaServer list,
// keyed by UDN. currentUDN is the one the user picked. stack is the
// folder navigation breadcrumb, where each entry is {id, title}.
const libState = {
  servers: [],
  refused: [],        // media servers that refused to describe themselves to this PC
  currentUDN: '',
  stack: [{ id: '0', title: '' }],
  page: null,
  loading: false,
  loadingMore: false, // true while paging the rest of a large folder in the background
  capped: false,      // true when a folder exceeds LIB_MAX and was truncated
  filter: '',         // folder search text (client-side filter over the loaded items)
  browseToken: 0,     // bumped on each browse so a stale background page-loop abandons
  restoredPath: false, // the stack about to be browsed is the remembered one (#642), not a step the user took
  nudged: new Set(),  // "box|udn|address" the speaker was told to look up (nudgeBoxLibrary)
  shuffle: false,     // "Play folder" shuffle toggle (UI state, default off)
  repeat: 'off',      // "Play folder" repeat mode: "off" | "all" | "one" (default off)
  // The whole-library search across every media server (#1325). active means
  // its results stand in for the folder list until the user goes back.
  search: { active: false, query: '', field: 'any', loading: false, items: [], unanswered: [], partial: false, token: 0 },
  // What only the UI 2.0 preview draws (#1286, shell2/library.js): the
  // feedback line of each row and of the folder actions, keyed by statusKey;
  // the error of the level that failed, shown in its place instead of the
  // error window; and where the keyboard goes once a new level is drawn.
  ui2: { status: new Map(), error: null, serversError: '', addError: '', focus: null },
};

// ui2 says whether the UI 2.0 preview shell is up. Every behaviour that
// differs between the two layouts asks it, so the classic layout keeps its
// toasts and its error window exactly as they were.
const ui2 = () => !!state.ui2Shell;

// The whole-library search categories: the same four, in the same order, as
// the phone page's Find tab offers.
const LIB_SEARCH_FIELDS = [
  ['any', 'library.fieldAll'],
  ['album', 'library.fieldAlbum'],
  ['artist', 'library.fieldArtist'],
  ['title', 'library.fieldTrack'],
];

// Media-library paging (#119). DLNA servers (and the Bose box's own UPnP) cap
// NumberReturned per Browse, so a 1500-track folder only returned its first
// page. We now page through the whole folder, but carefully: LIB_PAGE per SOAP
// request, each an await that yields to the event loop (the UI never freezes),
// and a LIB_MAX ceiling so a pathological 50k-track folder cannot exhaust memory
// or choke the DOM. Past the cap the folder search narrows what is shown.
const LIB_PAGE = 200;
const LIB_MAX = 2500;
// Max tracks a queue preset stores. The full list is serialized into presets.json
// on the box, so a whole-library folder can leave it too tight on flash for the
// next agent update (juelo, 2026-08-31). Keep in sync with presets.MaxQueueItems
// (internal/presets/presets.go), which enforces the same cap box-side.
const MAX_QUEUE_PRESET_ITEMS = 1000;

// The media server the user last browsed. Stored in the app rather than on a
// speaker: it is a preference of this person at this computer, and the
// speakers are shared.
const LIB_SERVER_KEY = 'str.library.lastServerUDN';

function rememberedServerUDN() {
  try {
    return localStorage.getItem(LIB_SERVER_KEY) || '';
  } catch {
    return '';
  }
}

export function rememberServerUDN(udn) {
  if (!udn) return;
  try {
    localStorage.setItem(LIB_SERVER_KEY, udn);
  } catch {
    // A blocked storage is not worth an error: the user simply gets the
    // list again next time, which is exactly today's behaviour.
  }
}

// The folder the user last had open, alongside the server it belongs to
// (#642): reopening the library lands where they left off instead of at the
// server root. One path, not one per server: the request was "continue where
// I was", and a map of stale paths for servers no longer around is exactly
// the kind of leftover the server memory above deliberately avoids.
const LIB_PATH_KEY = 'str.library.lastPath';

function rememberPath() {
  try {
    localStorage.setItem(LIB_PATH_KEY, JSON.stringify({
      udn: libState.currentUDN,
      stack: (libState.stack || []).map(s => ({ id: s.id, title: s.title, art: s.art || '' })),
    }));
  } catch { /* same trade-off as rememberServerUDN */ }
}

function rememberedPathFor(udn) {
  try {
    const p = JSON.parse(localStorage.getItem(LIB_PATH_KEY) || 'null');
    if (p && p.udn === udn && Array.isArray(p.stack) && p.stack.length > 1 &&
        p.stack.every(s => s && typeof s.id === 'string')) {
      return p.stack;
    }
  } catch { /* corrupt entry: fall back to the root */ }
  return null;
}

export async function openLibrary() {
  syncPlayModeFromBox();
  renderLibrary();
  if (libState.servers.length === 0) {
    await loadMediaServers();
  } else if (libState.currentUDN) {
    await libraryBrowseCurrent();
  }
}

// The queue's shuffle/repeat mode is sticky on the SPEAKER: the agent persists
// the last explicit choice and applies it to every queue start, hardware key
// presses included. Mirror it into the toggles when the library opens, so the
// UI shows the mode the next press will actually use instead of silently
// resetting to off (the "Zufall un-ticks itself" report, 2026-08-20). Fire and
// forget; an older agent without the endpoint just keeps the local defaults.
async function syncPlayModeFromBox() {
  const box = state.currentBox;
  if (!box || box.kind === 'stock') return;
  try {
    const r = await boxFetch(box, '/api/queue/mode', {}, 4000);
    if (!r.ok) return;
    const m = await r.json();
    if (m && typeof m.shuffle !== 'undefined') {
      libState.shuffle = !!m.shuffle;
      libState.repeat = m.repeat || 'off';
      renderLibrary();
    }
  } catch { /* box asleep or older agent: keep the local defaults */ }
}

// opts.autoOpen (default true) opens the remembered server, or the only one,
// straight away. The preview's "Search again" on the server list passes false:
// it asks for a fresh list and stays on it.
async function loadMediaServers(opts = {}) {
  const autoOpen = opts.autoOpen !== false;
  libState.loading = true;
  libState.ui2.serversError = '';
  renderLibrary();
  try {
    const list = await ListMediaServers(3);
    libState.servers = list || [];
    // A server that answers this PC's description request with a SOAP fault is
    // not listed, and without a word about it the user only sees whatever else
    // that host announces (a QNAP's admin device, which cannot be browsed).
    try {
      libState.refused = (await LibraryRefusedServers()) || [];
    } catch {
      libState.refused = []; // older build without the binding
    }
    // Open the server the user actually uses, rather than always asking.
    // Reported by a user whose router offers a media server he never plays
    // from: with two on the network he had to pick the right one every single
    // time, although the answer had been the same for months (#629).
    //
    // Remembering the last choice rather than offering a hide list: it needs
    // nothing set up, it is right from the second visit, and a server that
    // disappears simply falls back to the list instead of leaving a stale
    // exclusion behind that nobody remembers making.
    const preferred = rememberedServerUDN();
    const auto = !autoOpen ? null : libState.servers.length === 1
      ? libState.servers[0]
      : libState.servers.find(s => s.udn === preferred);
    if (auto) {
      libState.currentUDN = auto.udn;
      const restored = rememberedPathFor(auto.udn);
      libState.restoredPath = !!restored;
      libState.stack = restored || [{ id: '0', title: auto.friendlyName || '' }];
      await libraryBrowseCurrent();
      return;
    }
    libState.currentUDN = '';
    libState.page = null;
  } catch (e) {
    // In the preview the failed search is the server list's own state, with
    // the way out next to it, not a window over everything.
    if (ui2()) libState.ui2.serversError = String((e && e.message) || e || '');
    else showError(`ListMediaServers: ${e}`);
  } finally {
    libState.loading = false;
    renderLibrary();
  }
}

// Manual add-server fallback (#341): some servers are invisible to the
// SSDP sweep (UDP filtered by a firewall, another subnet, exotic
// same-host setups). The user can add one by IP or description URL;
// the backend probes well-known description endpoints, persists the
// entry, and merges it into every later scan.
async function libraryAddManualServer(value, btn) {
  const v = (value || '').trim();
  if (!v) return;
  if (btn) btn.disabled = true;
  libState.ui2.addError = '';
  try {
    const srv = await AddMediaServerByURL(v);
    const i = libState.servers.findIndex(s => s.udn === srv.udn);
    if (i >= 0) libState.servers[i] = srv; else libState.servers.push(srv);
    libState.currentUDN = srv.udn;
    rememberServerUDN(srv.udn);
    libState.stack = [{ id: '0', title: srv.friendlyName || '' }];
    await libraryBrowseCurrent();
  } catch (e) {
    // The preview keeps the error under the address field it is about.
    if (ui2()) libState.ui2.addError = `${t('library.addServerError')}: ${e}`;
    else showError(`${t('library.addServerError')}: ${e}`);
    renderLibrary();
  } finally {
    if (btn) btn.disabled = false;
  }
}

// serverNotAnswering says whether a list entry is one the last scan could only
// take from memory: nothing answered at its address (the backend's notAnswering).
export function serverNotAnswering(srv) {
  return !!(srv && srv.notAnswering);
}

// isUniversalMediaServer spots Universal Media Server. Out of the box it
// converts songs for any receiver it has no profile for, and a SoundTouch
// speaker then gets a stream it cannot decode: the song loads and stays silent
// with a decoder error. An owner found the setting that stops it (2026-10-10),
// so the open server gets that recipe as a folded note.
export function isUniversalMediaServer(srv) {
  if (!srv) return false;
  const s = `${srv.manufacturer || ''} ${srv.modelName || ''} ${srv.friendlyName || ''}`;
  return /universal media server/i.test(s) || /^ums$/i.test(String(srv.modelName || '').trim());
}

// libraryUMSNote is that folded note, or nothing for any other server.
export function libraryUMSNote(srv) {
  if (!isUniversalMediaServer(srv)) return '';
  return `<details class="library-ums-note"><summary>${escapeHtml(t('library.umsSummary'))}</summary><p>${escapeHtml(t('library.umsBody'))}</p></details>`;
}

// libraryRefusedNote names the media servers that refused to describe
// themselves to this PC. They cannot be listed, and the fix is on the server:
// a QNAP NAS that served the speaker fine answered the PC with a SOAP fault, most
// likely because the PC was not on its list of allowed media receivers.
export function libraryRefusedNote(refused) {
  const list = (refused || []).filter(r => r && r.address);
  if (!list.length) return '';
  return list.map(r =>
    `<div class="library-server-dark-note" title="${escapeAttr(r.detail || '')}">${escapeHtml(t('library.serverRefusesPC', { address: r.address }))}</div>`,
  ).join('');
}

// libraryServerLabel is the text the server picker shows for one entry. An entry
// nothing answered for carries the "not answering" mark, because a <select>
// cannot hold a styled badge inside an <option>; the chip beside the picker
// repeats it for the entry that is open.
export function libraryServerLabel(srv) {
  const name = (srv && (srv.friendlyName || srv.address)) || '';
  const model = srv && srv.modelName ? ` (${srv.modelName})` : '';
  const mark = serverNotAnswering(srv) ? ` · ${t('library.serverNotAnswering')}` : '';
  return `${name}${model}${mark}`;
}

// libraryPutServerOnSpeakers registers the selected media server as a music
// source on every ST Reborn speaker, so it appears in the speaker's own menu
// (and therefore in the Bose app too) whether or not this app is running.
//
// The id the speakers use is the UPnP UDN WITHOUT the "uuid:" prefix, while the
// PC's own discovery reports it with the prefix, so it is stripped here. Every
// speaker sees the same server under the same id, which is what makes one
// button enough for the whole house.
//
// server is the entry to add; the button passes nothing and gets the open one.
export async function libraryPutServerOnSpeakers(btn, server) {
  const srv = server || libState.servers.find(s => s.udn === libState.currentUDN);
  const msg = $('libOnSpeakersMsg');
  if (!srv) return;
  const id = String(srv.udn || '').replace(/^uuid:/i, '');
  const boxes = (state.boxes || []).filter(b => b && b.host && b.kind !== 'stock' && !b.offline);
  if (!boxes.length) {
    if (msg) msg.innerHTML = `<span class="setup-warn">${escapeHtml(t('library.onSpeakersNoBoxes'))}</span>`;
    return;
  }
  // #733: the reporter added a NAS that was switched off to all three of his
  // speakers, every browse of it failed afterwards, and nothing had said the
  // server was never seen alive. Say it here, once, and let him carry on: a NAS
  // that is off for the evening or wakes on access is a normal setup, so this
  // cannot be a refusal (#770).
  if (serverNotAnswering(srv)) {
    const proceed = await confirmWarn(
      t('library.notAnsweringWarnTitle'),
      escapeHtml(t('library.notAnsweringWarnBody', { name: srv.friendlyName || srv.address || '' })),
    );
    if (!proceed) return;
  }
  btn.disabled = true;
  if (msg) msg.innerHTML = `<span class="muted small">${escapeHtml(t('common.loading'))}</span>`;
  let ok = 0;
  const failed = [];
  for (const b of boxes) {
    try {
      await EnableBoxMediaServer(b.host, b.port || 0, id, srv.friendlyName || '');
      ok++;
    } catch {
      // A speaker that is asleep or off is exactly the one worth naming; the
      // others still get the server.
      failed.push(getBoxLabel(b));
    }
  }
  btn.disabled = false;
  if (msg) {
    msg.innerHTML = failed.length
      ? `<span class="setup-warn">${escapeHtml(t('library.onSpeakersPartly', { ok, failed: failed.join(', ') }))}</span>`
      : `<span class="setup-ok">${escapeHtml(t('library.onSpeakersDone', { n: ok }))}</span>`;
  }
}

async function libraryRemoveManualServer(udn) {
  try {
    await RemoveManualMediaServer(udn);
  } catch (e) {
    const msg = ui2() && $('libOnSpeakersMsg');
    if (msg) msg.innerHTML = `<span class="setup-warn" role="alert">${escapeHtml(`${t('library.removeServerBtn')}: ${e}`)}</span>`;
    else showError(`RemoveManualMediaServer: ${e}`);
    return;
  }
  libState.servers = libState.servers.filter(s => s.udn !== udn);
  if (libState.currentUDN === udn) {
    libState.currentUDN = '';
    libState.page = null;
    libState.stack = [{ id: '0', title: '' }];
  }
  renderLibrary();
}

// atRoot opens the server at its root instead of the folder last open there:
// the preview's server list is one step above the server, so choosing a server
// there is a step down to its root, not a jump to somewhere deep inside.
async function libraryPickServer(udn, atRoot = false) {
  const srv = libState.servers.find(s => s.udn === udn);
  if (!srv) return;
  libState.currentUDN = udn;
  rememberServerUDN(udn);
  const restored = !atRoot && rememberedPathFor(udn);
  libState.restoredPath = !!restored;
  libState.stack = restored || [{ id: '0', title: srv.friendlyName || '' }];
  await libraryBrowseCurrent();
}

// libraryShowServers is the preview's first crumb: back up to the list of
// media servers. A browse still paging in the background is abandoned.
function libraryShowServers() {
  libState.browseToken++;
  libState.currentUDN = '';
  libState.page = null;
  libState.loading = false;
  libState.loadingMore = false;
  libState.filter = '';
  libState.stack = [{ id: '0', title: '' }];
  libState.ui2.error = null;
  renderLibrary();
}

// looksUnreachable reports whether an error means the server did not answer,
// as opposed to answering with a refusal. Matched on the text because the error
// crosses the Wails boundary as a string and its type is gone by the time it
// gets here.
export function looksUnreachable(e) {
  const m = String((e && e.message) || e || "").toLowerCase();
  return [
    'deadline exceeded', 'timeout', 'timed out', 'connection refused',
    'actively refused', 'connection reset', 'no route to host',
    'no such host', 'unreachable', 'eof',
  ].some(x => m.includes(x));
}

// nudgeBoxLibrary has the speaker look the server up itself, once per server
// and address this session, right after this app browsed it.
//
// The app browses a media server on its own, so the speaker only hears where
// the server lives when a folder is played from here. A folder key whose
// server got a new address therefore stayed on the old one, dark and with a
// placeholder for a cover, until somebody pressed it or played its folder
// (#1366). The agent's own browse of the server's root resolves the server
// afresh and moves that server's keys to the address it answers at; its reply
// is of no interest here. The address is not handed over: the speaker finds
// the server itself, so this cannot point a key anywhere a browse would not.
// Fire and forget, generously timed: on a box that cannot see the server at
// once, the resolve walks the whole chain before it answers, and an aborted
// request would cancel that walk. An older agent simply answers the page.
function nudgeBoxLibrary(udn) {
  const box = state.currentBox;
  if (!box || box.kind === 'stock' || !udn) return;
  const srv = libState.servers.find(s => s.udn === udn);
  const key = `${box.host}|${udn}|${(srv && srv.address) || ''}`;
  if (libState.nudged.has(key)) return;
  libState.nudged.add(key);
  const path = `/api/library/browse?udn=${encodeURIComponent(udn)}&id=0&start=0`;
  boxFetch(box, path, {}, 75000).catch(() => { /* box asleep or older agent */ });
}

async function libraryBrowseCurrent() {
  if (!libState.currentUDN) return;
  const top = libState.stack[libState.stack.length - 1];
  const restoring = libState.restoredPath;
  libState.restoredPath = false;
  const token = ++libState.browseToken; // any older background loop sees a mismatch and abandons
  libState.loading = true;
  libState.loadingMore = false;
  libState.capped = false;
  libState.filter = '';
  libState.page = null;
  libState.ui2.error = null;
  libState.ui2.status.clear();
  renderLibrary();

  const acc = { containers: [], items: [], totalMatches: 0, returned: 0 };
  try {
    let start = 0;
    let first = true;
    let containerClass = '';
    for (;;) {
      const page = await BrowseLibrary(libState.currentUDN, top.id, start, LIB_PAGE);
      if (token !== libState.browseToken) return; // navigated away mid-fetch: drop it
      acc.containers = acc.containers.concat(page.containers || []);
      // A Mac's "._" companion files are no audio (libraryplayfail.js). Paging
      // below still counts what the server returned, so offsets stay right.
      acc.items = acc.items.concat(withoutMacMetadata(page.items));
      acc.totalMatches = page.totalMatches || acc.totalMatches;
      acc.returned = acc.containers.length + acc.items.length;
      const got = (page.containers || []).length + (page.items || []).length;
      start += LIB_PAGE;
      const done = got < LIB_PAGE || (acc.totalMatches && start >= acc.totalMatches);
      const capped = acc.returned >= LIB_MAX;
      if (first) {
        // Show the first page immediately, keep paging the rest in the background.
        libState.page = acc;
        libState.loading = false;
        libState.loadingMore = !done && !capped;
        renderLibrary();
        first = false;
        nudgeBoxLibrary(libState.currentUDN);
      }
      if (first) containerClass = page.containerClass || '';
      if (done || capped) {
        libState.capped = capped && !done;
        // A folder longer than one page is put in album track order here,
        // once all of it is in (#1487); a one-page folder already arrived in
        // that order. Play folder and the folder key take this order.
        if (done && !capped && start > LIB_PAGE) {
          acc.items = orderLongFolder(acc.items, containerClass);
        }
        break;
      }
    }
  } catch (e) {
    // A remembered folder can stop existing: DLNA object ids are not
    // guaranteed stable across a server restart. Fall back to the server
    // root once instead of showing an error for a folder the user never
    // asked for this session (#642).
    // The preview keeps that to the remembered folder: a folder the user just
    // opened that fails says so in its own place, with Try again and the step
    // back up, instead of the trail silently dropping to the root.
    if (libState.stack.length > 1 && top !== libState.stack[0] && (restoring || !ui2())) {
      libState.stack = libState.stack.slice(0, 1);
      await libraryBrowseCurrent();
      return;
    }
    // A sleeping or switched-off server is the commonest reason a browse
    // fails, and until now the user got the raw Go error, "context deadline
    // exceeded", which says nothing about what to do (shorty310, #1047 and
    // #1065, who reached this state deliberately by letting the host sleep).
    // Name the server and the likely reason instead; anything that is not a
    // reachability failure still shows the underlying error, because inventing
    // a friendly sentence for an unknown fault would hide it.
    const srv = libState.servers.find(x => x.udn === libState.currentUDN);
    if (ui2()) {
      // The preview shows the failure as the level itself, with Try again
      // and Back next to it (shell2/library.js stateHTML).
      libState.ui2.error = {
        asleep: looksUnreachable(e),
        name: (srv && srv.friendlyName) || t('library.serverFallbackName'),
        detail: String((e && e.message) || e || ''),
      };
    } else if (looksUnreachable(e)) {
      showError(t('library.serverAsleep', {
        name: (srv && srv.friendlyName) || t('library.serverFallbackName'),
      }));
    } else {
      showError(`BrowseLibrary: ${e}`);
    }
  } finally {
    if (token === libState.browseToken) {
      libState.loading = false;
      libState.loadingMore = false;
      libState.page = acc.returned ? acc : (libState.page || null);
      renderLibrary();
      if (!libState.loading && acc.returned >= 0 && libState.currentUDN) rememberPath();
    }
  }
}

async function libraryEnter(container) {
  // The folder's own cover rides along: a folder key saved from here falls
  // back to it when none of the tracks carries one (#1301).
  libState.stack.push({ id: container.id, title: container.title, art: container.albumArtURL || '' });
  await libraryBrowseCurrent();
}

async function libraryGoTo(depth) {
  // Truncate breadcrumb to the clicked depth.
  if (depth < 0 || depth >= libState.stack.length) return;
  libState.stack = libState.stack.slice(0, depth + 1);
  await libraryBrowseCurrent();
}

// Some DLNA servers omit the MIME on a browse result. Without one, a single
// track was played with an empty MIME, and the box-side /play handler then filed
// it under "radio" instead of "upnp" (its only discriminator is a non-empty
// MIME). On replay from Recently played a radio-typed card takes the streaming
// route and loops forever on a finite file (#817: folders were fixed in #818,
// single tracks still looped). Inferring the MIME from the file extension keeps
// the track typed as a library file, so it plays direct and replays cleanly.
// Only known audio extensions map; an unknown one stays empty rather than risk a
// wrong label the box would reject (#139).
const AUDIO_MIME_BY_EXT = {
  mp3: 'audio/mpeg', m4a: 'audio/mp4', mp4: 'audio/mp4', aac: 'audio/aac',
  flac: 'audio/flac', wav: 'audio/wav', wave: 'audio/wav', ogg: 'audio/ogg',
  oga: 'audio/ogg', opus: 'audio/opus', wma: 'audio/x-ms-wma',
  aif: 'audio/aiff', aiff: 'audio/aiff', alac: 'audio/mp4',
};
function guessAudioMime(url) {
  const m = String(url || '').toLowerCase().match(/\.([a-z0-9]+)(?:[?#]|$)/);
  return (m && AUDIO_MIME_BY_EXT[m[1]]) || '';
}

// statusKey names the feedback line of one track row (the preview's rows,
// shell2/library.js): the stream URL, the same in a folder and in the search.
export function statusKey(item) {
  return `u:${(item && item.streamURL) || (item && item.id) || ''}`;
}

// setLibStatus stores a row's feedback and writes it into the row if the row
// is on screen, without redrawing the list (focus and scroll stay). null
// clears it. Only the preview draws these lines.
function setLibStatus(key, st) {
  if (st) libState.ui2.status.set(key, st); else libState.ui2.status.delete(key);
  if (typeof document === 'undefined') return;
  document.querySelectorAll('#view-library .lib2-status').forEach((el) => {
    if (el.dataset.skey !== key) return;
    el.className = `lib2-status${st && st.tone ? ` lib2-status--${st.tone}` : ''}`;
    el.textContent = (st && st.text) || '';
  });
}

// openFolderId names the folder open in the Library: its server and its
// container. A folder action's answer that arrives after the user moved on
// belongs to the folder it was asked for, not to the action bar on screen.
function openFolderId() {
  const top = libState.stack[libState.stack.length - 1];
  return `${libState.currentUDN || ''}|${(top && top.id) || ''}`;
}

// speakerName is the name of the speaker a play from here goes to, as the
// preview's actions and feedback name it.
function speakerName() {
  return state.currentBox ? getBoxLabel(state.currentBox) : '';
}

async function libraryPlay(item) {
  const key = statusKey(item);
  if (!state.currentBox) {
    if (ui2()) setLibStatus(key, { tone: 'error', text: t('ui2.library.pickSpeakerFirst') });
    else showToast(t('library.toastNoBox') || t('common.pickBox'));
    return;
  }
  if (!item.streamURL) {
    if (ui2()) setLibStatus(key, { tone: 'error', text: t('library.errorNoURL') });
    else showError(t('library.errorNoURL'));
    return;
  }
  // A play aimed at a zone follower must go to its master (#70): the follower's
  // own UPnP endpoint rejects control with 501 "Can't control member of group".
  const target = deps.effectivePlayTarget() || state.currentBox;
  const name = speakerName();
  // Sent is not playing: the preview says "Starting" until the speaker
  // reports that it plays (verifyLibraryPlayback).
  if (ui2()) setLibStatus(key, { tone: 'busy', text: t('ui2.library.starting', { name }) });
  try {
    // Pass the track's real codec MIME so the box decodes FLAC/ALAC/M4A
    // correctly instead of being told audio/mpeg and rejecting it (#139).
    // The duration goes with it: without it the box answers total=0 and neither
    // the app nor the phone page can draw a progress bar or see the track end
    // (#845, #844). The folder path has always sent it, which is why a folder
    // drew a bar and a single click never did.
    await PlayURL(target.host, target.port,
      item.streamURL, item.title || '', item.albumArtURL || '', '',
      item.mimeType || guessAudioMime(item.streamURL), '', '', item.durationSec || 0);
    // A library play supersedes any ad-hoc radio station the app started, so
    // a later long-press save must not resurrect that station (#252).
    state.lastAppPlay = null;
    // Remembered so holding a key while this track plays saves it with its
    // media server, like the star button does (#1065). The save only uses it
    // while the speaker still reports this URL.
    state.lastLibraryPlay = {
      url: item.streamURL,
      name: item.title || '(track)',
      art: item.albumArtURL || '',
      // A whole-library search hit names its own server (#1325).
      source: item.server || librarySourceName(),
      durationSec: item.durationSec || 0,
      // Shown next to the title while this track plays, as a folder's queue
      // line shows them; the speaker reports neither for a direct file (#1394).
      artist: item.artist || '',
      album: item.album || '',
      // The track's object on its server, so a key saved from this play
      // survives the server renumbering its file addresses (#919).
      ref: libraryTrackRef(item, libraryItemUDN(item)),
      at: Date.now(),
    };
    // Put the track on the now-playing line now, as a station play does, and
    // ask the speaker again soon: left to the regular poll, the bar kept the
    // previous radio station's song for 5 to 15 s after the click (#1394).
    beginLibraryNowPlaying(state, state.lastLibraryPlay, Date.now());
    deps.renderNowPlayingBar();
    setTimeout(deps.refreshStatus, 1500);
    if (ui2()) {
      verifyLibraryPlayback(item, target, (r) => {
        if (!r) setLibStatus(key, null);
        else if (r.ok) setLibStatus(key, { tone: 'ok', text: t('ui2.library.playingOn', { name }) });
        else setLibStatus(key, { tone: 'error', text: r.msg });
      });
      return;
    }
    showToast(t('library.toastPlaying') + ': ' + (item.title || ''));
    // Confirm it actually starts (see verifyLibraryPlayback, #139).
    verifyLibraryPlayback(item, target);
  } catch (e) {
    if (ui2()) setLibStatus(key, { tone: 'error', text: t('ui2.library.playFailed', { name, detail: String(e) }) });
    else showError(`PlayURL: ${e}`);
  }
}

// verifyLibraryPlayback confirms a library track actually reaches a playing
// state on the speaker after PlayURL, and, when it does not, says which of the
// two possible reasons it was.
//
// The original reason for it (#139): the SoundTouch's UPnP layer accepts the
// URI but its decoder only handles some formats, so a high-resolution FLAC
// (24-bit or above 48 kHz) sits at "stream starting" forever with no feedback
// and users read it as a network or app fault.
//
// The reason it had to learn a second answer (#915, 2026-09-10): a reporter
// whose files are plain mp3 was told his format was probably unsupported. His
// speaker logged AUDIO_ERROR_TIMEOUT and ERROR_NO_DECODED_DATA twenty seconds
// into every attempt, which is the box saying nothing ever arrived to decode,
// and his media server did not answer its own description fetch either. The
// format was never in question; the file never reached the speaker.
//
// Two changes follow. The window now outlasts the box's OWN verdict, which
// lands at about twenty seconds, so the app stops guessing before the speaker
// has decided. And when the track did not start, the app fetches a few bytes of
// the URL itself: if the server will not hand over the beginning of the file,
// the delivery is the problem and the format is not worth mentioning. Run
// fire-and-forget so the click stays responsive.
//
// report, when given (the UI 2.0 preview), gets the verdict instead of the
// toast: { ok: true } once the speaker plays, { ok: false, msg } when it did
// not, null when the user moved on and there is nothing left to say.
async function verifyLibraryPlayback(item, target, report) {
  // Watch the box the play was actually sent to (the group master when the
  // selected box is a follower, #70), not blindly the selected box.
  const box = target || state.currentBox;
  if (!box) return;
  // Past the box's own AUDIO_ERROR_TIMEOUT, which arrives ~20 s in.
  const deadline = Date.now() + 26000;
  const verdict = await waitForPlayState(box, deadline);
  if (verdict === 'gone') { if (report) report(null); return; }
  if (verdict === 'playing') { if (report) report({ ok: true }); return; }
  // A speaker refusing everything STR pushes never tried the file, so neither
  // the format nor the server is the answer: the restart banner's text is
  // (#1634, libraryplayfail.js).
  if (speakerRefusingPushes(state.boxes, box)) {
    const storm = t('speaker.stormBanner', { name: getBoxLabel(box) });
    if (report) report({ ok: false, msg: storm });
    else showToast(storm, 9000);
    return;
  }
  const probe = await probeDelivery(item.streamURL);
  // Only a probe that actually answered may change the wording. "Not known" is
  // the state a blocked or unreachable probe lands in, and the app can be kept
  // from a server the speaker reaches perfectly well, so it keeps the older
  // format wording rather than inventing a verdict.
  const msg = probe.known && !probe.delivered
    ? t('library.serverNotDelivering', { title: item.title || '' })
    : t('library.formatMaybeUnsupported', { title: item.title || '' });
  if (report) report({ ok: false, msg });
  else showToast(msg, 9000);
}

// waitForPlayState asks the speaker every two seconds, until deadline, whether
// what was just sent plays: 'playing' once it does, 'rejected' when the speaker
// turned it down, 'timeout' when it never got there, 'gone' when no speaker is
// selected any more.
export async function waitForPlayState(box, deadline) {
  while (Date.now() < deadline) {
    await new Promise(r => setTimeout(r, 2000));
    // Bail if the user moved to another box entirely.
    if (!state.currentBox) return 'gone';
    let xml = '';
    try {
      xml = await Status(box.host, box.port);
    } catch {
      continue;
    }
    const ps = (xml.match(/<playStatus>([^<]+)<\/playStatus>/) || [])[1] || '';
    const src = (xml.match(/source="([^"]+)"/) || [])[1] || '';
    if (ps === 'PLAY_STATE') return 'playing'; // decoded and playing: all good
    if (src === 'INVALID_SOURCE' || /ERROR/.test(ps)) return 'rejected'; // box rejected it
  }
  return 'timeout';
}

// probeDelivery asks the Go side whether the media server hands over the first
// bytes of the track. It is the one measurement that separates a file the
// speaker cannot decode from a server that is not serving.
//
// It must NOT be a fetch() from here. This page has its own origin, a DLNA
// server sends no Access-Control-Allow-Origin, and the browser then refuses the
// answer however healthy the server is, so a frontend probe would report every
// server as dead. That is how the first version of this went wrong, and it would
// have told a FLAC owner to go and check his NAS.
async function probeDelivery(url) {
  try {
    const r = await ProbeTrackDelivery(url);
    return { known: !!(r && r.known), delivered: !!(r && r.delivered) };
  } catch {
    return { known: false, delivered: false };
  }
}

// libraryPlayFolder starts an auto-advancing queue from every playable track in
// the CURRENTLY OPEN container. It reuses the pagination accumulator that
// libraryBrowseCurrent fills (libState.page.items already holds the whole folder
// up to LIB_MAX), maps each track to the queue item shape the backend expects,
// drops anything without a stream URL, and calls StartQueue with the current
// shuffle/repeat UI state. Single-track clicks stay a plain PlayURL (which clears
// the queue server-side): auto-advance happens ONLY here, by product decision.
//
// fromId starts the queue at that track instead of the first (#1623): an
// audiobook picked up at chapter 7 plays on from there to the end, and the
// earlier chapters stay one Previous away.
async function libraryPlayFolder(fromId) {
  if (!state.currentBox) {
    if (ui2()) setLibStatus('folder', { tone: 'error', text: t('ui2.library.pickSpeakerFirst') });
    else showToast(t('library.toastNoBox') || t('common.pickBox'));
    return;
  }
  const all = (libState.page && libState.page.items) || [];
  const items = all
    .filter((it) => it.streamURL)
    .map((it) => ({
      url: it.streamURL,
      title: it.title || '',
      art: it.albumArtURL || '',
      mime: it.mimeType || '',
      duration_sec: it.durationSec || 0,
      // Filed as "Artist - Title" in Recently played, like a Spotify song (#1077).
      artist: it.artist || '',
      // Shown with the artist next to the running song, as on this row (#1033).
      album: it.album || '',
      id: it.id,
    }));
  if (items.length === 0) {
    if (ui2()) setLibStatus('folder', { tone: 'error', text: t('library.errorNoURL') });
    else showError(t('library.errorNoURL'));
    return;
  }
  const start = fromId ? Math.max(0, items.findIndex((it) => it.id === fromId)) : 0;
  for (const it of items) delete it.id;
  // Recently-played card (#220): name the card after the open folder (last
  // breadcrumb), key it on the server UDN + container id so repeated plays of the
  // same folder group together, and seed its cover from the first track's art.
  const stack = libState.stack || [];
  const last = stack.length > 0 ? stack[stack.length - 1] : null;
  const srv = libState.servers.find((s) => s.udn === libState.currentUDN);
  const card = {
    key: `queue:${libState.currentUDN || ''}:${(last && last.id) || ''}`,
    name: (last && last.title)
      || (srv && (srv.friendlyName || srv.address))
      || t('controls.playFolder'),
    // The folder's own cover when no track has one (#1301): holding a key while
    // this folder plays saves it with that cover.
    art: items[0].art || folderFallbackArt(items, last && last.art),
    // The media server, so holding a key while this folder plays saves it with
    // the same "from <server>" line the star button gives it (#1030).
    source: (srv && (srv.friendlyName || srv.address)) || '',
  };
  const payload = {
    items,
    start,
    shuffle: !!libState.shuffle,
    repeat: libState.repeat || 'off',
    card,
  };
  // A queue aimed at a zone follower must start on its master (#70): the
  // follower's UPnP endpoint rejects control with 501 "Can't control member
  // of group" (that raw SOAP fault is exactly what users saw).
  const target = deps.effectivePlayTarget() || state.currentBox;
  const spk = speakerName();
  const folder = openFolderId();
  if (ui2()) setLibStatus('folder', { tone: 'busy', text: t('ui2.library.folderStarting', { name: spk }) });
  try {
    await StartQueue(target.host, target.port, JSON.stringify(payload));
    // A folder play supersedes any ad-hoc radio station the app started (#252).
    state.lastAppPlay = null;
    state.lastLibraryPlay = null;
    // Same instant line change as a single track (#1394). A shuffled folder
    // does not know its first track, so it names the folder without a URL and
    // leaves the location to the speaker's next answer.
    beginLibraryNowPlaying(state, {
      url: payload.shuffle ? '' : items[start].url,
      name: payload.shuffle ? card.name : (items[start].title || card.name),
      art: card.art,
    }, Date.now());
    deps.renderNowPlayingBar();
    setTimeout(deps.refreshStatus, 1500);
    if (ui2()) {
      // Done only once the speaker plays; a folder whose first file the
      // speaker cannot start says so next to the button that started it.
      waitForPlayState(target, Date.now() + 26000).then((v) => {
        if (openFolderId() !== folder) return; // another folder is open by now
        if (v === 'gone') setLibStatus('folder', null);
        else if (v === 'playing') setLibStatus('folder', { tone: 'ok', text: t('ui2.library.folderPlaying', { name: spk }) });
        else setLibStatus('folder', { tone: 'warn', text: t('ui2.library.folderNotStarted', { name: spk }) });
      });
      return;
    }
    showToast(t('library.folderQueued', { n: items.length - start }));
  } catch (e) {
    if (ui2()) {
      if (openFolderId() === folder) setLibStatus('folder', { tone: 'error', text: t('ui2.library.playFailed', { name: spk, detail: String(e) }) });
    } else showError(`StartQueue: ${e}`);
  }
}

// librarySaveFolderAsPreset saves the CURRENTLY OPEN container as a queue preset
// (type=queue) on a hardware slot, mirroring libraryPlayFolder's item mapping
// and the current shuffle toggle. A later recall (app tile or hardware button)
// restarts the whole folder as an auto-advancing queue. Repeat is intentionally
// not persisted: the preset stores the folder + its shuffle setting, matching
// the product scope ("save a DLNA folder as a preset including shuffle").
function librarySaveFolderAsPreset() {
  if (!state.currentBox) {
    if (ui2()) setLibStatus('folder', { tone: 'error', text: t('ui2.library.pickSpeakerFirst') });
    else showToast(t('library.toastNoBox') || t('common.pickBox'));
    return;
  }
  const all = (libState.page && libState.page.items) || [];
  const items = all
    .filter((it) => it.streamURL)
    .map((it) => ({
      url: it.streamURL,
      title: it.title || '',
      art: it.albumArtURL || '',
      mime: it.mimeType || '',
      duration_sec: it.durationSec || 0,
      // Filed as "Artist - Title" in Recently played, like a Spotify song (#1077).
      artist: it.artist || '',
      // Shown with the artist next to the running song, as on this row (#1033).
      album: it.album || '',
      // The track's object on the server, so the speaker can match it up again
      // when the server renumbers its file addresses (#919).
      ...(it.id ? { object_id: String(it.id) } : {}),
    }));
  if (items.length === 0) {
    if (ui2()) setLibStatus('folder', { tone: 'error', text: t('library.errorNoURL') });
    else showError(t('library.errorNoURL'));
    return;
  }
  // Cap how many tracks a queue preset stores: the whole list is serialized into
  // presets.json on the box, and a whole-library folder (2600 tracks was ~1.5 MB
  // with the backup) can leave the box too tight on flash for its next agent
  // update. The box store enforces the same MAX_QUEUE_PRESET_ITEMS as a safety
  // net; capping here lets us tell the user their large folder was trimmed.
  const totalItems = items.length;
  const queueItems = totalItems > MAX_QUEUE_PRESET_ITEMS
    ? items.slice(0, MAX_QUEUE_PRESET_ITEMS)
    : items;
  // Name the preset after the open folder (last breadcrumb), falling back to the
  // media server name, so the tile and the box display show something meaningful.
  const stack = libState.stack || [];
  const last = stack.length > 0 ? stack[stack.length - 1] : null;
  const srv = libState.servers.find(s => s.udn === libState.currentUDN);
  const name = (last && last.title)
    || (srv && (srv.friendlyName || srv.address))
    || t('controls.playFolder');
  const source = (srv && (srv.friendlyName || srv.address)) || '';
  deps.showSlotPicker({
    // The preview names the speaker whose keys these are (rule 1).
    title: ui2() ? t('ui2.library.keyFolderTitle', { name: speakerName() }) : t('library.assignFolderTitle'),
    subtitle: name,
    onPick: async (i) => {
      const payload = {
        name,
        type: 'queue',
        shuffle: !!libState.shuffle,
        source,
        items: queueItems,
      };
      // No track carries a cover: the key shows the folder's own (#1301).
      const art = folderFallbackArt(queueItems, last && last.art);
      if (art) payload.art = art;
      const folder = openFolderId();
      // The folder's identity apart from its file paths: a server that
      // changes every path (a swapped USB stick) can then be browsed again by
      // id. An agent that does not know the field ignores it.
      if (libState.currentUDN && last && last.id) {
        payload.folder = { server_udn: libState.currentUDN, container_id: String(last.id) };
      }
      try {
        await SaveFolderPreset(state.currentBox.host, state.currentBox.port, i, JSON.stringify(payload));
        if (ui2()) {
          if (openFolderId() !== folder) return; // another folder is open by now
          // The speaker answered the save: say so next to the folder button.
          const spk = speakerName();
          setLibStatus('folder', {
            tone: 'ok',
            text: queueItems.length < totalItems
              ? t('ui2.library.onKeyCapped', { n: keyNum(i, t), name: spk, kept: queueItems.length, total: totalItems })
              : t('ui2.library.onKey', { n: keyNum(i, t), name: spk }),
          });
        } else if (queueItems.length < totalItems) {
          showToast(t('library.folderPresetSavedCapped', { n: keyNum(i, t), name, kept: queueItems.length, total: totalItems }));
        } else {
          showToast(t('library.folderPresetSaved', { n: keyNum(i, t), name }));
        }
      } catch (e) {
        if (ui2()) {
          if (openFolderId() === folder) setLibStatus('folder', { tone: 'error', text: t('ui2.library.keyFailed', { name: speakerName(), detail: String(e) }) });
        } else showError(`SaveFolderPreset: ${e}`);
      }
    },
  });
}

// librarySourceName is the open media server's name as a preset stores it for
// the "from <server>" line: the star button and the hold-to-save of a track
// started here must agree on it.
function librarySourceName() {
  const srv = libState.servers.find(s => s.udn === libState.currentUDN);
  return (srv && (srv.friendlyName || srv.address)) || '';
}

// libraryItemUDN is the media server a track belongs to: the server a
// whole-library search hit names, else the open one. '' when a search hit's
// server is not in the list; the speaker then finds it by name.
function libraryItemUDN(item) {
  if (item && item.server) {
    const srv = libState.servers.find(s => (s.friendlyName || s.address) === item.server);
    return (srv && srv.udn) || '';
  }
  return libState.currentUDN || '';
}

function librarySaveAsPreset(item) {
  const key = statusKey(item);
  if (!state.currentBox) {
    if (ui2()) setLibStatus(key, { tone: 'error', text: t('ui2.library.pickSpeakerFirst') });
    else showToast(t('library.toastNoBox') || t('common.pickBox'));
    return;
  }
  if (!item.streamURL) {
    if (ui2()) setLibStatus(key, { tone: 'error', text: t('library.errorNoURL') });
    else showError(t('library.errorNoURL'));
    return;
  }
  // The media server this track came from, stored on the preset so the tile can
  // show a small "from <server>" badge.
  const source = item.server || librarySourceName();
  deps.showSlotPicker({
    title: t('library.assignTitle', { name: getBoxLabel(state.currentBox) }),
    subtitle: [item.artist, item.title].filter(Boolean).join(' — ') || item.title || '',
    onPick: async (i) => {
      await saveLibraryTrackPreset(state.currentBox.host, state.currentBox.port, i, {
        name: item.title || '(track)', url: item.streamURL, art: item.albumArtURL || '',
        source, durationSec: item.durationSec || 0,
        // The track's object on its server (#919).
        ref: libraryTrackRef(item, libraryItemUDN(item)),
      });
      // A refusal (already on another key) stays with the key picker, which
      // offers the move; a save the speaker took is said in the track's row.
      if (ui2()) setLibStatus(key, { tone: 'ok', text: t('ui2.library.onKey', { n: keyNum(i, t), name: speakerName() }) });
      else showToast(t('preset.savedToKey', { n: keyNum(i, t), name: item.title || '(track)' }));
    },
  });
}

// libraryFilteredItems applies the folder search (client-side, over what has been
// paged in so far) to the loaded items. Matches title, artist or album.
function libraryFilteredItems() {
  const all = (libState.page && libState.page.items) || [];
  const f = (libState.filter || '').trim().toLowerCase();
  if (!f) return all;
  return all.filter((it) =>
    (it.title || '').toLowerCase().includes(f)
    || (it.artist || '').toLowerCase().includes(f)
    || (it.album || '').toLowerCase().includes(f));
}

// libraryFilteredContainers applies the same search to the SUB-FOLDERS. They used
// to be emitted in full whatever was typed, so in an album or artist list, which
// is folders and nothing else, the search box did visibly nothing (#1026).
function libraryFilteredContainers() {
  const all = (libState.page && libState.page.containers) || [];
  const f = (libState.filter || '').trim().toLowerCase();
  if (!f) return all;
  return all.filter((c) => (c.title || '').toLowerCase().includes(f));
}

// libraryListInnerHTML builds the folder/track <li> rows (containers always,
// tracks filtered by the search). Shared by the full render and the search
// refilter so the two never drift.
function libraryListInnerHTML() {
  const containers = libraryFilteredContainers().map((c) => `
      <li class="library-row library-row-folder" data-cid="${escapeAttr(c.id)}">
        <span class="library-icon">&#128194;</span>
        <span class="library-title">${escapeHtml(c.title)}</span>
        ${c.childCount > 0 ? `<span class="library-meta">${c.childCount}</span>` : ''}
      </li>`).join('');
  // "From here" only where there is a "rest of the folder" to play: two or
  // more playable tracks, and no search filter hiding part of the folder.
  const playable = ((libState.page && libState.page.items) || []).filter((it) => it.streamURL).length;
  const fromHere = playable > 1 && !libState.filter;
  const items = libraryFilteredItems().map((it) => {
    const meta = [it.artist, it.album].filter(Boolean).join(' — ');
    const dur = it.durationSec > 0 ? ` <span class="library-duration">${deps.formatDuration(it.durationSec)}</span>` : '';
    return `
        <li class="library-row library-row-track" data-iid="${escapeAttr(it.id)}">
          <span class="library-icon">&#9835;</span>
          <span class="library-title">
            <span class="library-track-title">${escapeHtml(it.title)}</span>
            ${meta ? `<span class="library-track-meta">${escapeHtml(meta)}</span>` : ''}
          </span>
          ${dur}
          <span class="library-actions">
            <button class="btn btn-mini lib-play-btn" data-iid="${escapeAttr(it.id)}" title="${escapeAttr(t('library.play'))}">${escapeHtml(t('library.play'))}</button>
            ${fromHere ? `<button class="btn btn-mini btn-secondary lib-from-here-btn" data-iid="${escapeAttr(it.id)}" title="${escapeAttr(t('library.playFromHereHint'))}">${escapeHtml(t('library.playFromHere'))}</button>` : ''}
            <button class="btn btn-mini btn-secondary lib-preset-btn" data-iid="${escapeAttr(it.id)}" title="${escapeAttr(t('library.saveAsPreset'))}">${escapeHtml(t('library.saveAsPreset'))}</button>
          </span>
        </li>`;
  }).join('');
  return containers + items;
}

// cappedKey picks the wording for the "only the first LIB_MAX are here" note. The
// cap counts folders AND tracks, but the note only ever said "tracks", so a user
// who hit it on an album list was told he was looking at 2500 tracks when they
// were 2500 albums (#1026).
function cappedKey(nContainers, nItems) {
  if (nItems === 0) return 'library.cappedFolders';
  if (nContainers === 0) return 'library.capped';
  return 'library.cappedMixed';
}

// libraryCountText: "shown / total" for tracks, with a "+" while more are still
// loading or the folder was capped, so the user knows the list is partial.
function libraryCountText() {
  // Folders count as rows. In an album list the old track-only count read "0"
  // next to 2500 albums (#1026).
  const items = (libState.page && libState.page.items) || [];
  const containers = (libState.page && libState.page.containers) || [];
  const total = items.length + containers.length;
  const shown = libraryFilteredItems().length + libraryFilteredContainers().length;
  const more = libState.loadingMore || libState.capped ? '+' : '';
  return (libState.filter ? `${shown} / ${total}${more}` : `${total}${more}`);
}

// wireLibraryRows attaches folder-enter, play and save-as-preset handlers to the
// rows inside a scope element. Re-run after the list is (re)built.
function wireLibraryRows(scope) {
  scope.querySelectorAll('.library-row-folder').forEach((row) => {
    row.onclick = () => {
      const c = ((libState.page && libState.page.containers) || []).find((x) => x.id === row.dataset.cid);
      if (c) libraryEnter(c);
    };
  });
  scope.querySelectorAll('.lib-play-btn').forEach((btn) => {
    btn.onclick = (e) => {
      e.stopPropagation();
      const it = ((libState.page && libState.page.items) || []).find((x) => x.id === btn.dataset.iid);
      if (it) libraryPlay(it);
    };
  });
  scope.querySelectorAll('.lib-from-here-btn').forEach((btn) => {
    btn.onclick = (e) => {
      e.stopPropagation();
      libraryPlayFolder(btn.dataset.iid);
    };
  });
  scope.querySelectorAll('.lib-preset-btn').forEach((btn) => {
    btn.onclick = (e) => {
      e.stopPropagation();
      const it = ((libState.page && libState.page.items) || []).find((x) => x.id === btn.dataset.iid);
      if (it) librarySaveAsPreset(it);
    };
  });
}

// libRefilter updates ONLY the list + count on a search keystroke, so the search
// input keeps focus (re-rendering the whole view would steal it) and large
// folders do not rebuild the entire view per character.
function libRefilter() {
  if (ui2()) {
    const l2 = document.querySelector('#view-library .lib2-list[data-list="folder"]');
    if (l2) l2.innerHTML = ui2FolderRowsHTML();
    const c2 = $('libCount');
    if (c2) c2.textContent = countText(libraryCountText());
    return;
  }
  const list = document.querySelector('#view-library .library-list');
  if (list) { list.innerHTML = libraryListInnerHTML(); wireLibraryRows(list); }
  const cnt = $('libCount');
  if (cnt) cnt.textContent = libraryCountText();
}

// libraryWholeSearch runs the whole-library search (#1325). Every media server
// the app knows is asked at once by the backend, each with its own timeout, and
// the token drops an answer the user has already moved past, the same guard the
// phone page's libToken is.
export async function libraryWholeSearch(query, field) {
  const s = libState.search;
  const q = (query || '').trim();
  s.query = query || '';
  s.field = field || 'any';
  if (!q) return;
  const token = ++s.token;
  Object.assign(s, { active: true, loading: true, items: [], unanswered: [], partial: false, failed: '' });
  renderLibrary();
  let res = null;
  let failed = false;
  try {
    res = await SearchLibrary(q, s.field);
  } catch (e) {
    failed = true;
    // The UI 2.0 preview tells a search that did not run apart from servers
    // that stayed silent (ui2SearchHTML); the current layout keeps its note.
    s.failed = String((e && e.message) || e || t('modal.unknownError'));
  }
  if (token !== s.token) return;
  s.loading = false;
  if (failed || !res) {
    // No answer at all reads like "every server was silent", which is the
    // offline note naming all of them.
    s.unanswered = libState.servers.map(libraryServerLabel);
  } else {
    s.items = res.items || [];
    s.unanswered = (res.unanswered || []).filter(Boolean);
    s.partial = !!res.partial;
  }
  renderLibrary();
}

// libraryWholeSearchNote picks the one note under the results, in the phone
// page's order: a server that did not answer, then a partial list, then no hits.
function libraryWholeSearchNote(s) {
  if (s.unanswered.length) return t('library.wholeSearchOffline', { name: s.unanswered.join(', ') });
  if (s.partial) return t('library.wholeSearchPartial');
  if (!s.items.length) return t('library.wholeSearchNone');
  return '';
}

// libraryWholeSearchBox is the search row above the folder: the query, the
// category and the button.
function libraryWholeSearchBox() {
  const s = libState.search;
  const opts = LIB_SEARCH_FIELDS.map(([v, k]) =>
    `<option value="${v}"${s.field === v ? ' selected' : ''}>${escapeHtml(t(k))}</option>`).join('');
  return `
      <div class="library-search-row library-whole-search">
        <input type="search" class="library-search" id="libWholeQ" placeholder="${escapeAttr(t('library.wholeSearchPlaceholder'))}" value="${escapeAttr(s.query)}">
        <label class="muted small" for="libWholeField">${escapeHtml(t('library.searchIn'))}</label>
        <select id="libWholeField">${opts}</select>
        <button class="btn btn-mini" id="libWholeBtn">${escapeHtml(t('library.wholeSearchBtn'))}</button>
      </div>`;
}

// libraryWholeSearchBody lists the hits the way the phone page does: the title,
// then artist, album and server on one line.
function libraryWholeSearchBody() {
  const s = libState.search;
  const head = `
      <div class="library-crumbs">
        <a href="#" class="library-ws-back" id="libWholeClose">&lsaquo; ${escapeHtml(t('library.wholeSearchClose'))}</a>
        <span class="library-crumb-sep">&rsaquo;</span>
        <span class="library-crumb-active">${escapeHtml(t('library.wholeSearchSec'))}</span>
      </div>`;
  if (s.loading) return head + `<p class="library-loading">${escapeHtml(t('library.wholeSearching'))}</p>`;
  const rows = s.items.map((it, idx) => {
    const meta = [it.artist, it.album, it.server].filter(Boolean).join(' · ');
    const dur = it.durationSec > 0 ? ` <span class="library-duration">${deps.formatDuration(it.durationSec)}</span>` : '';
    return `
        <li class="library-row library-row-track">
          <span class="library-icon">&#9835;</span>
          <span class="library-title">
            <span class="library-track-title">${escapeHtml(it.title || it.streamURL || '')}</span>
            ${meta ? `<span class="library-track-meta">${escapeHtml(meta)}</span>` : ''}
          </span>
          ${dur}
          <span class="library-actions">
            <button class="btn btn-mini lib-ws-play" data-idx="${idx}" title="${escapeAttr(t('library.play'))}">${escapeHtml(t('library.play'))}</button>
            <button class="btn btn-mini btn-secondary lib-ws-preset" data-idx="${idx}" title="${escapeAttr(t('library.saveAsPreset'))}">${escapeHtml(t('library.saveAsPreset'))}</button>
          </span>
        </li>`;
  }).join('');
  const note = libraryWholeSearchNote(s);
  return head
    + (rows ? `<ul class="library-list">${rows}</ul>` : '')
    + (note ? `<p class="library-empty-folder">${escapeHtml(note)}</p>` : '');
}

function wireWholeSearch(el) {
  const q = $('libWholeQ');
  const f = $('libWholeField');
  const go = () => libraryWholeSearch(q ? q.value : '', f ? f.value : 'any');
  const btn = $('libWholeBtn');
  if (btn) btn.onclick = go;
  if (q) q.onkeydown = (e) => { if (e.key === 'Enter') go(); };
  // Picking a category narrows the search already typed, as on the phone.
  if (f) f.onchange = () => { libState.search.field = f.value; if (q && q.value.trim()) go(); };
  const close = $('libWholeClose');
  if (close) close.onclick = (e) => {
    e.preventDefault();
    libState.search.token++;
    Object.assign(libState.search, { active: false, loading: false, items: [] });
    renderLibrary();
  };
  el.querySelectorAll('.lib-ws-play').forEach((b) => {
    b.onclick = () => { const it = libState.search.items[+b.dataset.idx]; if (it) libraryPlay(it); };
  });
  el.querySelectorAll('.lib-ws-preset').forEach((b) => {
    b.onclick = () => { const it = libState.search.items[+b.dataset.idx]; if (it) librarySaveAsPreset(it); };
  });
}

// pushQueueMode sends a shuffle or repeat choice to the speaker right away
// (best-effort), so it sticks for the hardware keys too, even when no folder
// play follows.
function pushQueueMode(path, body) {
  const box = state.currentBox;
  if (!box || box.kind === 'stock') return;
  boxFetch(box, path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }, 4000).catch(() => { /* box asleep or older agent */ });
}

// ---------- The UI 2.0 preview (#1286) ----------
//
// One level per step: the media servers, a server's root, each folder below
// it, and the search results across every server as one more step. The markup
// is shell2/library.js; the state, the browse, the plays and the keys are the
// ones above, so the two layouts never drift apart in what they can do.

// libraryFollowSpeaker redraws the preview's Library for a speaker chosen in
// the sidebar, so every play and key action names the new one, and reads that
// speaker's shuffle and repeat. A no-op in the classic layout and off the
// Library view.
export function libraryFollowSpeaker() {
  if (!ui2() || state.view !== 'library') return;
  renderLibrary();
  syncPlayModeFromBox();
}

// ui2ServerName is the open server's name, for the trail and the headings.
function ui2ServerName() {
  const srv = libState.servers.find((s) => s.udn === libState.currentUDN);
  return (srv && (srv.friendlyName || srv.address)) || '';
}

function ui2Crumbs() {
  const s = libState.search;
  return libraryTrail(libState.stack, {
    serverName: ui2ServerName(),
    inServer: !!libState.currentUDN,
    searchQuery: s.active ? (s.query || '').trim() : null,
  });
}

// ui2FolderRowsHTML is the open folder's rows, filtered by the folder filter:
// sub-folders first, then the tracks, each track with its feedback line.
function ui2FolderRowsHTML() {
  const speaker = speakerName();
  const rows = libraryFilteredContainers().map(folderRowHTML).join('')
    + libraryFilteredItems().map((it) => trackRowHTML(it, {
      key: statusKey(it),
      attr: 'iid',
      value: it.id,
      speaker,
      meta: [it.artist, it.album].filter(Boolean).join(' · '),
      duration: it.durationSec > 0 ? deps.formatDuration(it.durationSec) : '',
      status: libState.ui2.status.get(statusKey(it)),
    })).join('');
  if (!rows && libState.filter) {
    return `<li class="lib2-none">${escapeHtml(t('ui2.library.filterNone', { q: libState.filter.trim() }))}</li>`;
  }
  return rows;
}

// ui2ServerToolsHTML is what belongs to the open server rather than to one of
// its folders, shown at its root: putting it on every speaker, removing one
// added by hand, and the notes about a server that stays dark or is Universal
// Media Server.
function ui2ServerToolsHTML() {
  const cur = libState.servers.find((s) => s.udn === libState.currentUDN);
  if (!cur) return '';
  const remove = cur.manual
    ? `<button type="button" class="u-btn u-btn--secondary" id="libManualRemoveBtn">${escapeHtml(t('library.removeServerBtn'))}</button>`
    : '';
  const dark = serverNotAnswering(cur)
    ? `<p class="library-server-dark-note lib2-note">${escapeHtml(t('library.serverNotAnsweringNote'))}</p>`
    : '';
  return `
      <div class="lib2-server-tools">
        ${dark}
        <div class="lib2-tool-row">
          <button type="button" class="u-btn u-btn--secondary" id="libOnSpeakersBtn" aria-describedby="lib2OnSpeakersHint">${escapeHtml(t('library.onSpeakersBtn'))}</button>
          ${remove}
        </div>
        <p class="lib2-hint" id="lib2OnSpeakersHint">${escapeHtml(t('library.onSpeakersHint'))}</p>
        <div class="library-onspeakers-msg lib2-msg" id="libOnSpeakersMsg" role="status"></div>
        ${libraryUMSNote(cur)}
      </div>`;
}

function ui2FolderHTML() {
  const page = libState.page || {};
  const nC = (page.containers || []).length;
  const nI = (page.items || []).length;
  const top = libState.stack[libState.stack.length - 1] || {};
  const atRoot = libState.stack.length === 1;
  const title = top.title || (atRoot ? ui2ServerName() : '') || t('library.root');
  let html = levelHeadHTML(title, countLine(nC, nI));
  if (atRoot) html += ui2ServerToolsHTML();
  if (nI > 0) {
    html += folderActionsHTML({
      speaker: speakerName(),
      shuffle: !!libState.shuffle,
      repeat: libState.repeat || 'off',
      status: libState.ui2.status.get('folder'),
    });
  }
  if (nI > 0 || nC > 0) html += filterRowHTML(libState.filter, libraryCountText());
  html += `<ul class="lib2-list" data-list="folder">${ui2FolderRowsHTML()}</ul>`;
  if (libState.loadingMore) html += loadingHTML(t('library.loadingMore'));
  else if (libState.capped) html += `<p class="lib2-note">${escapeHtml(t(cappedKey(nC, nI), { n: LIB_MAX }))}</p>`;
  if (nC === 0 && nI === 0) {
    const up = trailParent(ui2Crumbs());
    html += stateHTML({
      tone: 'info',
      title: t('library.emptyFolder'),
      body: t('ui2.library.emptyFolderBody'),
      actions: up ? [{ id: 'lib2Up', label: t('ui2.library.backTo', { name: up.label }) }] : [],
    });
  }
  return html;
}


// ui2SearchHTML is the search across every server as a level of its own: the
// hits with the server each one is on, and the one note the classic layout
// shows under them (a server that did not answer, a partial list, nothing).
function ui2SearchHTML() {
  const s = libState.search;
  let html = levelHeadHTML(t('ui2.library.results', { q: (s.query || '').trim() }));
  if (s.loading) return html + loadingHTML(t('library.wholeSearching'));
  // The call itself failed: not "every server was silent" but a search that
  // did not run, with Try again for the same words and field.
  if (s.failed) {
    return html + stateHTML({
      tone: 'error',
      title: t('ui2.feedback.searchFailedTitle'),
      body: t('ui2.feedback.searchFailedBody'),
      detail: friendlyErrorText(s.failed),
      actions: [{ id: 'lib2SearchRetry', label: t('ui2.feedback.tryAgain'), primary: true }],
    });
  }
  const speaker = speakerName();
  const rows = s.items.map((it, idx) => trackRowHTML(it, {
    key: statusKey(it),
    attr: 'idx',
    value: idx,
    speaker,
    meta: [it.artist, it.album, it.server].filter(Boolean).join(' · '),
    duration: it.durationSec > 0 ? deps.formatDuration(it.durationSec) : '',
    status: libState.ui2.status.get(statusKey(it)),
  })).join('');
  if (rows) html += `<ul class="lib2-list" data-list="search">${rows}</ul>`;
  const note = libraryWholeSearchNote(s);
  // No hits from servers that all answered: what to try next, and the way
  // back to the search field.
  if (note && !s.items.length && !s.unanswered.length && !s.partial) {
    const any = (LIB_SEARCH_FIELDS.find(([v]) => v === 'any') || [])[1] || 'library.fieldAll';
    return html + stateHTML({
      tone: 'info',
      title: note,
      body: s.field !== 'any' ? t('ui2.feedback.tryAnotherField', { field: t(any) }) : '',
      actions: [{ id: 'lib2SearchFocus', label: t('ui2.library.searchAgain'), primary: true }],
    });
  }
  if (note) html += `<p class="lib2-note" role="status">${escapeHtml(note)}</p>`;
  return html;
}

// ui2ServersHTML is the top level: the media servers on the network, or what
// to do when there are none or the search for them failed.
function ui2ServersHTML() {
  const u = libState.ui2;
  const manual = (open) => manualAddHTML({ open: open || !!u.addError, error: u.addError });
  if (libState.loading) return loadingHTML(t('ui2.library.searchingServers'));
  if (u.serversError) {
    return stateHTML({
      tone: 'error',
      title: t('ui2.library.serversErrorTitle'),
      body: t('ui2.library.serversErrorBody'),
      detail: u.serversError,
      actions: [{ id: 'libRefreshBtn', label: t('ui2.library.tryAgain'), primary: true }],
    }) + manual(false);
  }
  if (!libState.servers.length) {
    return stateHTML({
      tone: 'info',
      title: t('ui2.library.noServersTitle'),
      body: t('ui2.library.noServersBody'),
      actions: [{ id: 'libRefreshBtn', label: t('ui2.library.searchAgain'), primary: true }],
    }) + libraryRefusedNote(libState.refused) + manual(true);
  }
  return levelHeadHTML(t('ui2.library.serversTitle'))
    + libraryRefusedNote(libState.refused)
    + `<ul class="lib2-list" data-list="servers">${serverRowsHTML(libState.servers)}</ul>`
    + `<div class="lib2-tool-row"><button type="button" class="u-btn u-btn--secondary" id="libRefreshBtn">${escapeHtml(t('ui2.library.searchAgain'))}</button></div>`
    + manual(false);
}

// ui2ErrorHTML is a folder (or a server's root) that could not be opened, in
// its place: what happened, Try again and the step back up.
function ui2ErrorHTML() {
  const err = libState.ui2.error;
  const up = trailParent(ui2Crumbs());
  const actions = [{ id: 'lib2Retry', label: t('ui2.library.tryAgain'), primary: true }];
  if (up) actions.push({ id: 'lib2Up', label: t('ui2.library.backTo', { name: up.label }) });
  return err.asleep
    ? stateHTML({ tone: 'error', title: t('ui2.library.asleepTitle', { name: err.name }), body: t('ui2.library.asleepBody'), actions })
    : stateHTML({ tone: 'error', title: t('ui2.library.folderErrorTitle'), body: t('ui2.library.folderErrorBody'), detail: err.detail, actions });
}

// focusMark and findMark keep the keyboard on the same control across a
// redraw: by id, or by its lib2-* class and data attributes.
function focusMark(root) {
  const a = typeof document !== 'undefined' ? document.activeElement : null;
  if (!a || a === root || !root.contains(a)) return null;
  const sel = typeof a.selectionStart === 'number' ? [a.selectionStart, a.selectionEnd] : null;
  if (a.id) return { id: a.id, sel };
  const cls = [...a.classList].find((c) => c.startsWith('lib2-'));
  return cls ? { cls, data: { ...a.dataset }, sel } : null;
}
function findMark(root, m) {
  if (!m) return null;
  if (m.id) {
    const n = document.getElementById(m.id);
    return n && root.contains(n) ? n : null;
  }
  return [...root.querySelectorAll(`.${m.cls}`)]
    .find((n) => Object.entries(m.data).every(([k, v]) => n.dataset[k] === v)) || null;
}

function renderLibraryUi2(el) {
  const s = libState.search;
  const u = libState.ui2;
  const inServer = !!libState.currentUDN;
  let body;
  if (s.active) body = trailHTML(ui2Crumbs()) + ui2SearchHTML();
  else if (!inServer) body = ui2ServersHTML();
  else {
    body = trailHTML(ui2Crumbs());
    const top = libState.stack[libState.stack.length - 1] || {};
    if (libState.loading) body += loadingHTML(t('ui2.library.opening', { name: top.title || ui2ServerName() || t('library.root') }));
    else if (u.error) body += ui2ErrorHTML();
    else if (libState.page) body += ui2FolderHTML();
  }
  const keep = focusMark(el);
  el.innerHTML = headerHTML({
    speaker: speakerName(),
    search: libState.servers.length ? s : null,
    fields: LIB_SEARCH_FIELDS,
  }) + `<div class="lib2-level">${body}</div>`;
  wireWholeSearch(el);
  wireLibraryUi2(el);

  // A step to a new level puts the keyboard on its heading, or, one step up,
  // on the row it came from; any other redraw keeps it where it was.
  const busy = libState.loading || s.loading;
  const f = u.focus;
  let target = null;
  if (f && !busy) {
    u.focus = null;
    if (f.cid) target = [...el.querySelectorAll('.lib2-folder')].find((b) => b.dataset.cid === f.cid) || null;
    if (f.udn) target = [...el.querySelectorAll('.lib2-server')].find((b) => b.dataset.udn === f.udn) || null;
    if (!target) target = el.querySelector('.lib2-focus-target');
  } else if (!f) {
    target = findMark(el, keep);
  }
  if (target) {
    try { target.focus({ preventScroll: !f }); } catch { target.focus(); }
    if (keep && keep.sel && target === findMark(el, keep) && typeof target.setSelectionRange === 'function') {
      try { target.setSelectionRange(keep.sel[0], keep.sel[1]); } catch { /* not a text field */ }
    }
  }
}

// ui2GoTo takes the preview one or more steps up: depth is a crumb's
// (SERVERS_DEPTH for the server list). Leaving the search results back to the
// folder they were searched from shows that folder again without asking the
// server.
function ui2GoTo(depth) {
  const s = libState.search;
  const fromSearch = s.active;
  if (fromSearch) {
    s.token++;
    Object.assign(s, { active: false, loading: false, items: [] });
  }
  if (depth === SERVERS_DEPTH) {
    libState.ui2.focus = libState.currentUDN ? { udn: libState.currentUDN } : 'heading';
    libraryShowServers();
    return;
  }
  const last = libState.stack.length - 1;
  if (depth > last || depth < 0) return;
  if (fromSearch && depth === last && (libState.page || libState.ui2.error)) {
    libState.ui2.focus = 'heading';
    renderLibrary();
    return;
  }
  const leaving = libState.stack[depth + 1];
  libState.ui2.focus = leaving ? { cid: leaving.id } : 'heading';
  libraryGoTo(depth);
}

// ui2ItemFor finds the track a row's button stands for: by id in the folder,
// by index in the search results.
function ui2ItemFor(btn) {
  if (btn.dataset.idx != null) return libState.search.items[+btn.dataset.idx] || null;
  return ((libState.page && libState.page.items) || []).find((x) => x.id === btn.dataset.iid) || null;
}

// wireLibraryUi2 handles every control of the preview's Library, delegated on
// the view, so a list the filter rebuilds needs no wiring of its own.
function wireLibraryUi2(el) {
  el.onclick = (e) => {
    const hit = (sel) => e.target.closest(sel);
    let b;
    if ((b = hit('.lib2-crumb[data-depth], .lib2-back'))) { ui2GoTo(parseInt(b.dataset.depth, 10)); return; }
    if ((b = hit('.lib2-server'))) {
      libState.ui2.focus = 'heading';
      libraryPickServer(b.dataset.udn, true);
      return;
    }
    if ((b = hit('.lib2-folder'))) {
      const c = ((libState.page && libState.page.containers) || []).find((x) => x.id === b.dataset.cid);
      if (c) { libState.ui2.focus = 'heading'; libraryEnter(c); }
      return;
    }
    if ((b = hit('.lib2-play'))) { const it = ui2ItemFor(b); if (it) libraryPlay(it); return; }
    if ((b = hit('.lib2-key'))) { const it = ui2ItemFor(b); if (it) librarySaveAsPreset(it); return; }
    if (hit('.lib2-play-folder')) { libraryPlayFolder(); return; }
    if (hit('.lib2-key-folder')) { librarySaveFolderAsPreset(); return; }
    if ((b = hit('.lib2-shuffle'))) {
      libState.shuffle = !libState.shuffle;
      b.setAttribute('aria-pressed', libState.shuffle ? 'true' : 'false');
      pushQueueMode('/api/queue/shuffle', { on: libState.shuffle });
      return;
    }
    if ((b = hit('.lib2-repeat'))) {
      libState.repeat = libState.repeat === 'off' ? 'all' : libState.repeat === 'all' ? 'one' : 'off';
      b.dataset.mode = libState.repeat;
      b.textContent = t(repeatLabelKey(libState.repeat));
      pushQueueMode('/api/queue/repeat', { mode: libState.repeat });
      return;
    }
    if (hit('#lib2Retry')) { libState.ui2.focus = 'heading'; libraryBrowseCurrent(); return; }
    // The search that did not run, again with the same words and field.
    if (hit('#lib2SearchRetry')) {
      libState.ui2.focus = 'heading';
      libraryWholeSearch(libState.search.query, libState.search.field);
      return;
    }
    // No hits: back to the search field, its words selected.
    if (hit('#lib2SearchFocus')) {
      const q = $('libWholeQ');
      if (q) { q.focus(); q.select(); }
      return;
    }
    if (hit('#lib2Up')) { const up = trailParent(ui2Crumbs()); if (up) ui2GoTo(up.depth); return; }
    if (hit('#libRefreshBtn')) { libState.ui2.focus = 'heading'; loadMediaServers({ autoOpen: false }); return; }
    if ((b = hit('#libOnSpeakersBtn'))) { libraryPutServerOnSpeakers(b); return; }
    if ((b = hit('#libManualAddBtn'))) { const i = $('libManualUrl'); libraryAddManualServer(i ? i.value : '', b); return; }
    if (hit('#libManualRemoveBtn')) { libraryRemoveManualServer(libState.currentUDN); }
  };
  el.onkeydown = (e) => {
    if (e.key === 'Enter' && e.target && e.target.id === 'libManualUrl') {
      libraryAddManualServer(e.target.value, $('libManualAddBtn'));
    }
  };
  el.oninput = (e) => {
    if (e.target && e.target.id === 'libSearch') {
      libState.filter = e.target.value;
      libRefilter();
    }
  };
}

function renderLibrary() {
  const el = $('view-library');
  if (!el) return;
  if (ui2()) { renderLibraryUi2(el); return; }
  // Which speaker this tab acts on has to be visible here. Playing a track and
  // saving a preset both go to the speaker picked on the music tab, and presets
  // live on that speaker, so choosing a preset slot from the library meant
  // choosing blind: nothing on this page said which speaker would get it (Jens,
  // 2026-07-30). Same control as speaker settings, and it moves the shared
  // selection so the whole app stays in lock-step.
  const boxOpts = state.boxes.length
    ? state.boxes.map(b => {
        const sel = state.currentBox && b.deviceID === state.currentBox.deviceID ? ' selected' : '';
        return `<option value="${escapeAttr(b.deviceID || '')}"${sel}>${escapeHtml(getBoxLabel(b))}</option>`;
      }).join('')
    : `<option value="">${escapeHtml(t('library.noSpeaker'))}</option>`;
  const intro = `
    <div class="library-header">
      <h2>${escapeHtml(t('library.title'))}</h2>
      <p class="library-sub">${escapeHtml(t('library.subtitle'))}</p>
      <div class="library-box-switcher">
        <span class="muted small">${escapeHtml(t('library.forSpeaker'))}</span>
        <select id="libraryBoxSelect">${boxOpts}</select>
      </div>
    </div>`;

  if (libState.loading) {
    el.innerHTML = intro + `<p class="library-loading">${escapeHtml(t('library.loading'))}</p>`;
    return;
  }

  // Server picker section.
  let serverPicker = '';
  if (libState.servers.length === 0) {
    serverPicker = `
      <div class="library-empty">
        <p>${escapeHtml(t('library.noServers'))}</p>
        <button class="btn" id="libRefreshBtn">${escapeHtml(t('library.refresh'))}</button>
      </div>`;
  } else {
    // With more than one server nothing is selected yet, and a <select> shows
    // its FIRST option regardless. So the box read as if the Synology were
    // already chosen while the page below still said "pick a server", and
    // clicking that same entry fired no change event: a dead end with no way
    // forward (reported with a screenshot, 2026-07-28). An explicit placeholder
    // makes the control tell the truth and makes every pick a real change.
    const placeholder = libState.currentUDN
      ? ''
      : `<option value="" selected disabled>${escapeHtml(t('library.chooseServer'))}</option>`;
    const opts = placeholder + libState.servers.map(s => {
      const sel = s.udn === libState.currentUDN ? ' selected' : '';
      return `<option value="${escapeAttr(s.udn)}"${sel}>${escapeHtml(libraryServerLabel(s))}</option>`;
    }).join('');
    // Manually added servers (#341) get a remove control when selected.
    const cur = libState.servers.find(s => s.udn === libState.currentUDN);
    const removeBtn = cur && cur.manual
      ? `<button class="btn btn-mini btn-secondary" id="libManualRemoveBtn" title="${escapeAttr(t('library.removeServerBtn'))}">&#10005;</button>`
      : '';
    // The entry that is open and did not answer gets the chip plus one plain
    // line. Without it the list showed a remembered server exactly like a live
    // one, which is how a switched-off NAS ended up as a source on three
    // speakers (#733, #770).
    const notAnsweringChip = serverNotAnswering(cur)
      ? `<span class="library-server-dark" title="${escapeAttr(t('library.serverNotAnsweringNote'))}">${escapeHtml(t('library.serverNotAnswering'))}</span>`
      : '';
    const notAnsweringNote = serverNotAnswering(cur)
      ? `<div class="library-server-dark-note">${escapeHtml(t('library.serverNotAnsweringNote'))}</div>`
      : '';
    // Put the selected server on the speakers themselves.
    //
    // Browsing here runs on the PC, so the moment the app is closed the music
    // is gone from the speaker's own menu. Making the server a source ON the
    // speaker is a different thing entirely, and it lived only in Speaker
    // settings, one speaker at a time, where nobody looking at their library
    // would think to look. An owner wrote in with exactly that gap: his media
    // servers were no longer offered on the speaker and he had no idea the app
    // could put them back.
    //
    // Whole-household by default, because a server belongs to the house rather
    // than to one speaker, and every speaker discovers it under the same id.
    const onSpeakersBtn = libState.currentUDN
      ? `<button class="btn btn-mini" id="libOnSpeakersBtn" title="${escapeAttr(t('library.onSpeakersHint'))}">${escapeHtml(t('library.onSpeakersBtn'))}</button>`
      : '';
    serverPicker = `
      <div class="library-server-row">
        <label class="library-label">${escapeHtml(t('library.server'))}</label>
        <select class="library-select" id="libServerSelect">${opts}</select>
        ${notAnsweringChip}
        <button class="btn btn-mini" id="libRefreshBtn" title="${escapeAttr(t('library.refresh'))}">&#8634;</button>
        ${onSpeakersBtn}
        ${removeBtn}
      </div>
      ${notAnsweringNote}
      ${libraryUMSNote(cur)}
      <div class="library-onspeakers-msg" id="libOnSpeakersMsg"></div>`;
  }

  // Manual fallback (#341): always available, discreet. For servers the
  // SSDP sweep cannot see, the user adds an IP or a description URL.
  const manualAdd = `
    <details class="library-manual-add">
      <summary>${escapeHtml(t('library.addServerManual'))}</summary>
      <div class="library-manual-row">
        <input type="text" class="library-manual-input" id="libManualUrl" placeholder="${escapeAttr(t('library.addServerPlaceholder'))}">
        <button class="btn btn-mini" id="libManualAddBtn">${escapeHtml(t('library.addServerBtn'))}</button>
      </div>
    </details>`;

  // Breadcrumb + folder/track listing.
  let body = '';
  if (libState.currentUDN && libState.page) {
    const crumbs = libState.stack.map((s, i) => {
      const lbl = s.title || t('library.root');
      const isLast = i === libState.stack.length - 1;
      return isLast
        ? `<span class="library-crumb-active">${escapeHtml(lbl)}</span>`
        : `<a href="#" class="library-crumb" data-depth="${i}">${escapeHtml(lbl)}</a>`;
    }).join(' <span class="library-crumb-sep">&rsaquo;</span> ');

    const nContainers = (libState.page.containers || []).length;
    const nItems = (libState.page.items || []).length;
    // Folder actions: "Play folder" starts an auto-advancing queue from every
    // playable track in this container, with shuffle/repeat toggles that feed the
    // initial queue state. Shown only when the folder has playable items.
    const folderActions = nItems > 0 ? `
      <div class="library-folder-actions">
        <button class="btn lib-play-folder-btn">&#9205; ${escapeHtml(t('controls.playFolder'))}</button>
        <button class="btn lib-save-folder-btn" title="${escapeAttr(t('controls.saveFolderPreset'))}">&#11088; ${escapeHtml(t('controls.saveFolderPreset'))}</button>
        <button class="btn btn-mini toggle-btn lib-queue-shuffle${libState.shuffle ? ' active' : ''}" title="${escapeAttr(t('controls.shuffle'))}">&#128256; ${escapeHtml(t('controls.shuffle'))}</button>
        <button class="btn btn-mini toggle-btn lib-queue-repeat${libState.repeat !== 'off' ? ' active' : ''}" title="${escapeAttr(t('controls.repeat'))}">&#128257; ${escapeHtml(t('controls.repeat'))}${libState.repeat === 'one' ? ' ¹' : ''}</button>
      </div>` : '';
    // The search row used to be tied to nItems, so a folder holding only
    // sub-folders (every "by Album" / "by Artist" list there is) had no search
    // box at all, while the capped note below still told the user to use it. That
    // is what #1026 reported: 2500 albums, a note pointing at a control that was
    // not on screen. Any row at all is reason enough to offer the filter.
    const searchRow = (nItems > 0 || nContainers > 0) ? `
      <div class="library-search-row">
        <input type="search" class="library-search" id="libSearch" placeholder="${escapeAttr(t('library.searchPlaceholder'))}" value="${escapeAttr(libState.filter || '')}">
        <span class="library-count" id="libCount">${escapeHtml(libraryCountText())}</span>
      </div>` : '';
    const moreNote = libState.loadingMore
      ? `<p class="library-loading-more">${escapeHtml(t('library.loadingMore'))}</p>`
      : (libState.capped ? `<p class="library-loading-more">${escapeHtml(t(cappedKey(nContainers, nItems), { n: LIB_MAX }))}</p>` : '');
    const empty = (nContainers === 0 && nItems === 0)
      ? `<p class="library-empty-folder">${escapeHtml(t('library.emptyFolder'))}</p>` : '';

    body = `
      <div class="library-crumbs">${crumbs}</div>
      ${folderActions}
      ${searchRow}
      <ul class="library-list">${libraryListInnerHTML()}</ul>
      ${moreNote}
      ${empty}`;
  } else if (libState.servers.length > 0 && !libState.currentUDN) {
    body = `<p class="library-pick-server">${escapeHtml(t('library.pickServer'))}</p>`;
  }

  // The whole-library search sits above the folder whenever there is a server
  // to search, and its results stand in for the folder until "back" (#1325).
  const wholeSearch = libState.servers.length > 0 ? libraryWholeSearchBox() : '';
  if (libState.search.active) body = libraryWholeSearchBody();

  el.innerHTML = intro + serverPicker + libraryRefusedNote(libState.refused) + manualAdd + wholeSearch + body;
  wireWholeSearch(el);

  // Wire interactions.
  const boxSel = $('libraryBoxSelect');
  if (boxSel) {
    boxSel.onchange = () => {
      const box = state.boxes.find(b => (b.deviceID || '') === boxSel.value);
      if (!box) return;
      // speakerPicked is what sets state.currentBox, and setting it here first
      // stopped it doing so. Its guard switches only when the host DIFFERS from
      // the current one, so assigning the new box a line early made that test
      // false and the whole switch was skipped: no selectBox, and therefore no
      // loadPresets. "Save as preset" then offered the PREVIOUS speaker's six
      // slots, with the previous speaker's labels on them, which is a reliable
      // way to overwrite the wrong key (reported 2026-10-01 against v0.9.92).
      //
      // Every other view calls this and leaves the state alone, which is what
      // the contract in setup.js already says it does.
      deps.speakerPicked(box);
      renderLibrary();
    };
  }
  const sel = $('libServerSelect');
  if (sel) sel.onchange = () => libraryPickServer(sel.value);
  const ref = $('libRefreshBtn');
  if (ref) ref.onclick = () => loadMediaServers();
  const onSpk = $('libOnSpeakersBtn');
  if (onSpk) onSpk.onclick = () => libraryPutServerOnSpeakers(onSpk);
  const addBtn = $('libManualAddBtn');
  const addInput = $('libManualUrl');
  if (addBtn && addInput) {
    addBtn.onclick = () => libraryAddManualServer(addInput.value, addBtn);
    addInput.onkeydown = (e) => { if (e.key === 'Enter') libraryAddManualServer(addInput.value, addBtn); };
  }
  const rmBtn = $('libManualRemoveBtn');
  if (rmBtn) rmBtn.onclick = () => libraryRemoveManualServer(libState.currentUDN);

  el.querySelectorAll('.library-crumb').forEach(a => {
    a.onclick = (e) => { e.preventDefault(); libraryGoTo(parseInt(a.dataset.depth, 10)); };
  });
  const playFolderBtn = el.querySelector('.lib-play-folder-btn');
  if (playFolderBtn) playFolderBtn.onclick = () => libraryPlayFolder();
  const saveFolderBtn = el.querySelector('.lib-save-folder-btn');
  if (saveFolderBtn) saveFolderBtn.onclick = () => librarySaveFolderAsPreset();
  // The toggles push their choice to the speaker right away (pushQueueMode).
  const pushMode = pushQueueMode;
  const qShuffleBtn = el.querySelector('.lib-queue-shuffle');
  if (qShuffleBtn) qShuffleBtn.onclick = () => {
    libState.shuffle = !libState.shuffle;
    qShuffleBtn.classList.toggle('active', libState.shuffle);
    pushMode('/api/queue/shuffle', { on: libState.shuffle });
  };
  const qRepeatBtn = el.querySelector('.lib-queue-repeat');
  if (qRepeatBtn) qRepeatBtn.onclick = () => {
    libState.repeat = libState.repeat === 'off' ? 'all' : libState.repeat === 'all' ? 'one' : 'off';
    qRepeatBtn.classList.toggle('active', libState.repeat !== 'off');
    qRepeatBtn.innerHTML = `&#128257; ${escapeHtml(t('controls.repeat'))}${libState.repeat === 'one' ? ' ¹' : ''}`;
    pushMode('/api/queue/repeat', { mode: libState.repeat });
  };
  wireLibraryRows(el);
  const libSearch = $('libSearch');
  if (libSearch) {
    // Filter the loaded list as the user types. Updates only the list + count
    // (libRefilter), so the input keeps focus and a big folder is not fully
    // re-rendered per keystroke.
    libSearch.oninput = () => { libState.filter = libSearch.value; libRefilter(); };
  }
}
