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
import { $, escapeHtml, escapeAttr, showError, showToast } from '../utils.js';
import { t } from '../i18n/index.js';
import {
  ListMediaServers,
  BrowseLibrary,
  PlayURL,
  StartQueue,
  SaveLibraryPreset,
  SaveFolderPreset,
  Status,
} from '../api.js';

// Injected main.js helpers (see initLibraryView). These stay in main.js because
// they are shared across views; the library code calls them as deps.<name>.
let deps = {
  showSlotPicker() {},
  formatDuration() {},
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
  currentUDN: '',
  stack: [{ id: '0', title: '' }],
  page: null,
  loading: false,
  loadingMore: false, // true while paging the rest of a large folder in the background
  capped: false,      // true when a folder exceeds LIB_MAX and was truncated
  filter: '',         // folder search text (client-side filter over the loaded items)
  browseToken: 0,     // bumped on each browse so a stale background page-loop abandons
  shuffle: false,     // "Play folder" shuffle toggle (UI state, default off)
  repeat: 'off',      // "Play folder" repeat mode: "off" | "all" | "one" (default off)
};

// Media-library paging (#119). DLNA servers (and the Bose box's own UPnP) cap
// NumberReturned per Browse, so a 1500-track folder only returned its first
// page. We now page through the whole folder, but carefully: LIB_PAGE per SOAP
// request, each an await that yields to the event loop (the UI never freezes),
// and a LIB_MAX ceiling so a pathological 50k-track folder cannot exhaust memory
// or choke the DOM. Past the cap the folder search narrows what is shown.
const LIB_PAGE = 200;
const LIB_MAX = 2500;

export async function openLibrary() {
  renderLibrary();
  if (libState.servers.length === 0) {
    await loadMediaServers();
  } else if (libState.currentUDN) {
    await libraryBrowseCurrent();
  }
}

async function loadMediaServers() {
  libState.loading = true;
  renderLibrary();
  try {
    const list = await ListMediaServers(3);
    libState.servers = list || [];
    if (libState.servers.length === 1) {
      libState.currentUDN = libState.servers[0].udn;
      libState.stack = [{ id: '0', title: libState.servers[0].friendlyName || '' }];
      await libraryBrowseCurrent();
      return;
    }
    libState.currentUDN = '';
    libState.page = null;
  } catch (e) {
    showError(`ListMediaServers: ${e}`);
  } finally {
    libState.loading = false;
    renderLibrary();
  }
}

async function libraryPickServer(udn) {
  const srv = libState.servers.find(s => s.udn === udn);
  if (!srv) return;
  libState.currentUDN = udn;
  libState.stack = [{ id: '0', title: srv.friendlyName || '' }];
  await libraryBrowseCurrent();
}

async function libraryBrowseCurrent() {
  if (!libState.currentUDN) return;
  const top = libState.stack[libState.stack.length - 1];
  const token = ++libState.browseToken; // any older background loop sees a mismatch and abandons
  libState.loading = true;
  libState.loadingMore = false;
  libState.capped = false;
  libState.filter = '';
  libState.page = null;
  renderLibrary();

  const acc = { containers: [], items: [], totalMatches: 0, returned: 0 };
  try {
    let start = 0;
    let first = true;
    for (;;) {
      const page = await BrowseLibrary(libState.currentUDN, top.id, start, LIB_PAGE);
      if (token !== libState.browseToken) return; // navigated away mid-fetch: drop it
      acc.containers = acc.containers.concat(page.containers || []);
      acc.items = acc.items.concat(page.items || []);
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
      }
      if (done || capped) {
        libState.capped = capped && !done;
        break;
      }
    }
  } catch (e) {
    showError(`BrowseLibrary: ${e}`);
  } finally {
    if (token === libState.browseToken) {
      libState.loading = false;
      libState.loadingMore = false;
      libState.page = acc.returned ? acc : (libState.page || null);
      renderLibrary();
    }
  }
}

async function libraryEnter(container) {
  libState.stack.push({ id: container.id, title: container.title });
  await libraryBrowseCurrent();
}

async function libraryGoTo(depth) {
  // Truncate breadcrumb to the clicked depth.
  if (depth < 0 || depth >= libState.stack.length) return;
  libState.stack = libState.stack.slice(0, depth + 1);
  await libraryBrowseCurrent();
}

async function libraryPlay(item) {
  if (!state.currentBox) {
    showToast(t('library.toastNoBox') || t('common.pickBox'));
    return;
  }
  if (!item.streamURL) {
    showError(t('library.errorNoURL'));
    return;
  }
  try {
    // Pass the track's real codec MIME so the box decodes FLAC/ALAC/M4A
    // correctly instead of being told audio/mpeg and rejecting it (#139).
    await PlayURL(state.currentBox.host, state.currentBox.port,
      item.streamURL, item.title || '', item.albumArtURL || '', '', item.mimeType || '', '');
    showToast(t('library.toastPlaying') + ': ' + (item.title || ''));
    // Confirm it actually starts (see verifyLibraryPlayback, #139).
    verifyLibraryPlayback(item);
  } catch (e) {
    showError(`PlayURL: ${e}`);
  }
}

// verifyLibraryPlayback confirms a library track actually reaches a playing
// state on the speaker after PlayURL. The SoundTouch's UPnP layer accepts the
// URI but its decoder only handles some formats: a high-resolution FLAC (24-bit
// or above 48 kHz) never decodes, so the track sits at "stream starting"
// forever with no feedback, and users read it as a network or app fault (#139).
// Poll the box play state for a short window; if it never starts (or the box
// reports the source invalid), surface a soft, format-agnostic hint. Run
// fire-and-forget so the click stays responsive.
async function verifyLibraryPlayback(item) {
  const box = state.currentBox;
  if (!box) return;
  const deadline = Date.now() + 12000;
  while (Date.now() < deadline) {
    await new Promise(r => setTimeout(r, 2000));
    // Bail if the user moved to another box or started something else.
    if (!state.currentBox || state.currentBox.host !== box.host) return;
    let xml = '';
    try {
      xml = await Status(box.host, box.port);
    } catch {
      continue;
    }
    const ps = (xml.match(/<playStatus>([^<]+)<\/playStatus>/) || [])[1] || '';
    const src = (xml.match(/source="([^"]+)"/) || [])[1] || '';
    if (ps === 'PLAY_STATE') return; // decoded and playing: all good
    if (src === 'INVALID_SOURCE' || /ERROR/.test(ps)) break; // box rejected it
  }
  showToast(t('library.formatMaybeUnsupported', { title: item.title || '' }), 8000);
}

// libraryPlayFolder starts an auto-advancing queue from every playable track in
// the CURRENTLY OPEN container. It reuses the pagination accumulator that
// libraryBrowseCurrent fills (libState.page.items already holds the whole folder
// up to LIB_MAX), maps each track to the queue item shape the backend expects,
// drops anything without a stream URL, and calls StartQueue with the current
// shuffle/repeat UI state. Single-track clicks stay a plain PlayURL (which clears
// the queue server-side): auto-advance happens ONLY here, by product decision.
async function libraryPlayFolder() {
  if (!state.currentBox) {
    showToast(t('library.toastNoBox') || t('common.pickBox'));
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
    }));
  if (items.length === 0) {
    showError(t('library.errorNoURL'));
    return;
  }
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
    art: items[0].art || '',
  };
  const payload = {
    items,
    start: 0,
    shuffle: !!libState.shuffle,
    repeat: libState.repeat || 'off',
    card,
  };
  try {
    await StartQueue(state.currentBox.host, state.currentBox.port, JSON.stringify(payload));
    showToast(t('library.folderQueued', { n: items.length }));
  } catch (e) {
    showError(`StartQueue: ${e}`);
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
    showToast(t('library.toastNoBox') || t('common.pickBox'));
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
    }));
  if (items.length === 0) {
    showError(t('library.errorNoURL'));
    return;
  }
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
    title: t('library.assignFolderTitle'),
    subtitle: name,
    onPick: async (i) => {
      const payload = {
        name,
        type: 'queue',
        shuffle: !!libState.shuffle,
        source,
        items,
      };
      try {
        await SaveFolderPreset(state.currentBox.host, state.currentBox.port, i, JSON.stringify(payload));
        showToast(t('library.folderPresetSaved', { n: i, name }));
      } catch (e) {
        showError(`SaveFolderPreset: ${e}`);
      }
    },
  });
}

function librarySaveAsPreset(item) {
  if (!state.currentBox) {
    showToast(t('library.toastNoBox') || t('common.pickBox'));
    return;
  }
  if (!item.streamURL) {
    showError(t('library.errorNoURL'));
    return;
  }
  // The media server this track came from, stored on the preset so the tile can
  // show a small "from <server>" badge.
  const srv = libState.servers.find(s => s.udn === libState.currentUDN);
  const source = (srv && (srv.friendlyName || srv.address)) || '';
  deps.showSlotPicker({
    title: t('library.assignTitle'),
    subtitle: [item.artist, item.title].filter(Boolean).join(' — ') || item.title || '',
    onPick: async (i) => {
      await SaveLibraryPreset(state.currentBox.host, state.currentBox.port, i,
        item.title || '(track)', item.streamURL, item.albumArtURL || '', 0, source);
      showToast(t('preset.savedToKey', { n: i, name: item.title || '(track)' }));
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

// libraryListInnerHTML builds the folder/track <li> rows (containers always,
// tracks filtered by the search). Shared by the full render and the search
// refilter so the two never drift.
function libraryListInnerHTML() {
  const containers = ((libState.page && libState.page.containers) || []).map((c) => `
      <li class="library-row library-row-folder" data-cid="${escapeAttr(c.id)}">
        <span class="library-icon">&#128194;</span>
        <span class="library-title">${escapeHtml(c.title)}</span>
        ${c.childCount > 0 ? `<span class="library-meta">${c.childCount}</span>` : ''}
      </li>`).join('');
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
            <button class="btn btn-mini btn-secondary lib-preset-btn" data-iid="${escapeAttr(it.id)}" title="${escapeAttr(t('library.saveAsPreset'))}">${escapeHtml(t('library.saveAsPreset'))}</button>
          </span>
        </li>`;
  }).join('');
  return containers + items;
}

// libraryCountText: "shown / total" for tracks, with a "+" while more are still
// loading or the folder was capped, so the user knows the list is partial.
function libraryCountText() {
  const total = ((libState.page && libState.page.items) || []).length;
  const shown = libraryFilteredItems().length;
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
  const list = document.querySelector('#view-library .library-list');
  if (list) { list.innerHTML = libraryListInnerHTML(); wireLibraryRows(list); }
  const cnt = $('libCount');
  if (cnt) cnt.textContent = libraryCountText();
}

function renderLibrary() {
  const el = $('view-library');
  if (!el) return;
  const intro = `
    <div class="library-header">
      <h2>${escapeHtml(t('library.title'))}</h2>
      <p class="library-sub">${escapeHtml(t('library.subtitle'))}</p>
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
    const opts = libState.servers.map(s => {
      const sel = s.udn === libState.currentUDN ? ' selected' : '';
      const sub = s.modelName ? ` (${escapeHtml(s.modelName)})` : '';
      return `<option value="${escapeAttr(s.udn)}"${sel}>${escapeHtml(s.friendlyName || s.address)}${sub}</option>`;
    }).join('');
    serverPicker = `
      <div class="library-server-row">
        <label class="library-label">${escapeHtml(t('library.server'))}</label>
        <select class="library-select" id="libServerSelect">${opts}</select>
        <button class="btn btn-mini" id="libRefreshBtn" title="${escapeAttr(t('library.refresh'))}">&#8634;</button>
      </div>`;
  }

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
        <button class="btn lib-play-folder-btn">&#9654; ${escapeHtml(t('controls.playFolder'))}</button>
        <button class="btn lib-save-folder-btn" title="${escapeAttr(t('controls.saveFolderPreset'))}">&#11088; ${escapeHtml(t('controls.saveFolderPreset'))}</button>
        <button class="btn btn-mini toggle-btn lib-queue-shuffle${libState.shuffle ? ' active' : ''}" title="${escapeAttr(t('controls.shuffle'))}">&#128256; ${escapeHtml(t('controls.shuffle'))}</button>
        <button class="btn btn-mini toggle-btn lib-queue-repeat${libState.repeat !== 'off' ? ' active' : ''}" title="${escapeAttr(t('controls.repeat'))}">&#128257; ${escapeHtml(t('controls.repeat'))}${libState.repeat === 'one' ? ' ¹' : ''}</button>
      </div>` : '';
    const searchRow = nItems > 0 ? `
      <div class="library-search-row">
        <input type="search" class="library-search" id="libSearch" placeholder="${escapeAttr(t('library.searchPlaceholder'))}" value="${escapeAttr(libState.filter || '')}">
        <span class="library-count" id="libCount">${escapeHtml(libraryCountText())}</span>
      </div>` : '';
    const moreNote = libState.loadingMore
      ? `<p class="library-loading-more">${escapeHtml(t('library.loadingMore'))}</p>`
      : (libState.capped ? `<p class="library-loading-more">${escapeHtml(t('library.capped', { n: LIB_MAX }))}</p>` : '');
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

  el.innerHTML = intro + serverPicker + body;

  // Wire interactions.
  const sel = $('libServerSelect');
  if (sel) sel.onchange = () => libraryPickServer(sel.value);
  const ref = $('libRefreshBtn');
  if (ref) ref.onclick = () => loadMediaServers();

  el.querySelectorAll('.library-crumb').forEach(a => {
    a.onclick = (e) => { e.preventDefault(); libraryGoTo(parseInt(a.dataset.depth, 10)); };
  });
  const playFolderBtn = el.querySelector('.lib-play-folder-btn');
  if (playFolderBtn) playFolderBtn.onclick = () => libraryPlayFolder();
  const saveFolderBtn = el.querySelector('.lib-save-folder-btn');
  if (saveFolderBtn) saveFolderBtn.onclick = () => librarySaveFolderAsPreset();
  const qShuffleBtn = el.querySelector('.lib-queue-shuffle');
  if (qShuffleBtn) qShuffleBtn.onclick = () => {
    libState.shuffle = !libState.shuffle;
    qShuffleBtn.classList.toggle('active', libState.shuffle);
  };
  const qRepeatBtn = el.querySelector('.lib-queue-repeat');
  if (qRepeatBtn) qRepeatBtn.onclick = () => {
    libState.repeat = libState.repeat === 'off' ? 'all' : libState.repeat === 'all' ? 'one' : 'off';
    qRepeatBtn.classList.toggle('active', libState.repeat !== 'off');
    qRepeatBtn.innerHTML = `&#128257; ${escapeHtml(t('controls.repeat'))}${libState.repeat === 'one' ? ' ¹' : ''}`;
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
