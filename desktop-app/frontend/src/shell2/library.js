// shell2/library.js: the Library view as the UI 2.0 preview draws it (#1286).
//
// Phase 2 of docs/UI-2.0.md: one level per step. The media servers, then a
// server's folders, then the next folder and so on down to the tracks; each
// level replaces the one before it, and a trail of crumbs above it (each one a
// button) plus Back lead up again. The view module stays views/library.js: the
// same state, the same browse, play, queue and key paths. This file only turns
// what that module knows into markup, so it is pure (data in, markup out) and
// can be tested without a DOM.
//
// The four rules, as this view keeps them:
//   - the speaker is named on every play and key action ("Play folder on
//     Kitchen"), because the sidebar chooses it and the view must say whom it
//     acts on;
//   - honest feedback: a play reads "Starting on Kitchen" until the speaker
//     itself reports that it plays, never on the click;
//   - errors in place: a track that does not start says so in its own row, a
//     server that does not answer in the level it was asked for;
//   - nothing disappears: play a track or a folder, shuffle and repeat, a
//     track or a folder on a key, the folder filter, the search across every
//     server, adding a server by address, putting a server on the speakers and
//     the notes about servers that refuse or stay dark all have a place here.

import './library.css';
import { t } from '../i18n/index.js';
import { escapeAttr, escapeHtml } from '../utils.js';
import { KEYS_ICON, keyButtonHTML } from './keybtn.js';
import { statusHTML } from './state.js';

// The feedback line and the empty, loading and error states moved to
// shell2/state.js (phase 5), shared by every view of the preview. Re-exported
// here unchanged: the views and the tests import them from this file.
export { loadingHTML, stateHTML, statusHTML } from './state.js';

// The depth the first crumb stands for: the list of media servers, one step
// above a server's own root (depth 0 of libState.stack).
export const SERVERS_DEPTH = -1;

const ICONS = {
  server: '<svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true" focusable="false"><rect x="4" y="3" width="16" height="7" rx="1.5" fill="none" stroke="currentColor" stroke-width="2"/><rect x="4" y="14" width="16" height="7" rx="1.5" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="8" cy="6.5" r="1.2" fill="currentColor"/><circle cx="8" cy="17.5" r="1.2" fill="currentColor"/></svg>',
  folder: '<svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true" focusable="false"><path d="M3 6.5A1.5 1.5 0 0 1 4.5 5H9l2 2h8.5A1.5 1.5 0 0 1 21 8.5v9A1.5 1.5 0 0 1 19.5 19h-15A1.5 1.5 0 0 1 3 17.5z" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/></svg>',
  track: '<svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true" focusable="false"><path d="M9 17V5l10-2v12" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"/><circle cx="6.5" cy="17.5" r="2.5" fill="none" stroke="currentColor" stroke-width="2"/><circle cx="16.5" cy="15.5" r="2.5" fill="none" stroke="currentColor" stroke-width="2"/></svg>',
  play: '<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" focusable="false"><path d="M8 5v14l11-7z" fill="currentColor"/></svg>',
  back: '<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" focusable="false"><path d="M15 5l-7 7 7 7" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"/></svg>',
};
const chevron = '<span class="lib2-chev" aria-hidden="true">&rsaquo;</span>';

// libraryTrail is the crumb list for where the view is: the media servers,
// then every step of the open server's stack. While the search across every
// server shows its results, they are one more step at the end, so the trail
// still says where Back leads. The last crumb is the current level and is not
// a link.
//
//   stack     libState.stack ([{ id, title }], the server root first)
//   opts.serverName   the open server's name (the root's own title may be '')
//   opts.inServer     a server is open (libState.currentUDN)
//   opts.searchQuery  set while the search results stand in for the level
export function libraryTrail(stack, { serverName = '', inServer = false, searchQuery = null } = {}) {
  const crumbs = [{ depth: SERVERS_DEPTH, label: t('ui2.library.servers') }];
  if (inServer) {
    (stack || []).forEach((s, i) => {
      const label = (s && s.title) || (i === 0 ? serverName : '') || t('library.root');
      crumbs.push({ depth: i, label });
    });
  }
  if (searchQuery != null) crumbs.push({ depth: null, label: t('ui2.library.results', { q: searchQuery }) });
  return crumbs.map((c, i) => ({ ...c, current: i === crumbs.length - 1 }));
}

// trailParent is the crumb Back leads to: the one before the current level,
// or null at the top (the server list has nothing above it).
export function trailParent(crumbs) {
  return crumbs && crumbs.length > 1 ? crumbs[crumbs.length - 2] : null;
}

// repeatLabelKey names the repeat button for the mode it is in. Three modes
// do not fit a pressed / not pressed toggle, so the words carry the state.
export function repeatLabelKey(mode) {
  if (mode === 'all') return 'ui2.library.repeatAll';
  if (mode === 'one') return 'ui2.library.repeatOne';
  return 'ui2.library.repeatOff';
}

// countLine is the line under a level's heading: how many folders and tracks
// it holds, each part only when there is one.
export function countLine(nFolders, nTracks) {
  const parts = [];
  if (nFolders > 0) parts.push(t('ui2.library.countFolders', { n: nFolders }));
  if (nTracks > 0) parts.push(t('ui2.library.countTracks', { n: nTracks }));
  return parts.join(' · ');
}

// headerHTML is the top of the view on every level: the title, which speaker
// plays what is started here, and the search across every media server.
export function headerHTML({ speaker = '', search = null, fields = [] } = {}) {
  const target = speaker
    ? `<p class="lib2-target">${escapeHtml(t('ui2.library.playsOn', { name: speaker }))}</p>`
    : `<p class="lib2-target lib2-target--none">${escapeHtml(t('ui2.library.noSpeaker'))}</p>`;
  let searchRow = '';
  if (search) {
    const opts = fields.map(([v, k]) =>
      `<option value="${escapeAttr(v)}"${search.field === v ? ' selected' : ''}>${escapeHtml(t(k))}</option>`).join('');
    searchRow = `
      <div class="lib2-search" role="search">
        <label class="u-sr-only" for="libWholeQ">${escapeHtml(t('ui2.library.searchLabel'))}</label>
        <input type="search" class="lib2-input" id="libWholeQ" placeholder="${escapeAttr(t('library.wholeSearchPlaceholder'))}" value="${escapeAttr(search.query || '')}">
        <label class="lib2-search-in" for="libWholeField">${escapeHtml(t('library.searchIn'))}</label>
        <select class="lib2-select" id="libWholeField">${opts}</select>
        <button type="button" class="u-btn u-btn--secondary" id="libWholeBtn">${escapeHtml(t('library.wholeSearchBtn'))}</button>
      </div>`;
  }
  return `
    <div class="lib2-head">
      <h2 class="lib2-title">${escapeHtml(t('library.title'))}</h2>
      ${target}
      ${searchRow}
    </div>`;
}

// trailHTML is Back plus the crumbs. Back names where it leads; every crumb but
// the current one is a button, so the whole trail is reachable by keyboard.
export function trailHTML(crumbs) {
  const parent = trailParent(crumbs);
  const back = parent
    ? `<button type="button" class="u-btn u-btn--secondary lib2-back" data-depth="${parent.depth}" aria-label="${escapeAttr(t('ui2.library.backTo', { name: parent.label }))}">${ICONS.back}<span>${escapeHtml(t('ui2.library.back'))}</span></button>`
    : '';
  const items = crumbs.map((c) => (c.current
    ? `<li><span class="lib2-crumb lib2-crumb--current" aria-current="location">${escapeHtml(c.label)}</span></li>`
    : `<li><button type="button" class="lib2-crumb" data-depth="${c.depth}">${escapeHtml(c.label)}</button></li>`)).join('');
  return `
    <nav class="lib2-trail" aria-label="${escapeAttr(t('ui2.library.trailLabel'))}">
      ${back}
      <ol class="lib2-crumbs">${items}</ol>
    </nav>`;
}

// levelHeadHTML is a level's own heading (focused after a step, so a screen
// reader says where it landed) and its count line.
export function levelHeadHTML(title, sub = '') {
  return `
    <div class="lib2-level-head">
      <h3 class="lib2-level-title lib2-focus-target" id="lib2LevelTitle" tabindex="-1">${escapeHtml(title)}</h3>
      ${sub ? `<p class="lib2-level-sub">${escapeHtml(sub)}</p>` : ''}
    </div>`;
}

// serverRowsHTML lists the media servers, one button each. One that the last
// scan only knew from memory says so in words, in a pill.
export function serverRowsHTML(servers) {
  return (servers || []).map((s) => {
    const name = s.friendlyName || s.address || '';
    const sub = [s.modelName, s.address].filter(Boolean).join(' · ');
    const pill = s.notAnswering
      ? `<span class="u-pill u-pill--warn lib2-pill"><span class="u-pill-dot" aria-hidden="true"></span>${escapeHtml(t('library.serverNotAnswering'))}</span>`
      : '';
    return `
      <li class="lib2-item">
        <button type="button" class="lib2-row lib2-server" data-udn="${escapeAttr(s.udn)}">
          <span class="lib2-icon">${ICONS.server}</span>
          <span class="lib2-text"><span class="lib2-name">${escapeHtml(name)}</span>${sub ? `<span class="lib2-sub">${escapeHtml(sub)}</span>` : ''}</span>
          ${pill}
          ${chevron}
        </button>
      </li>`;
  }).join('');
}

// folderRowHTML is one sub-folder: a single button that opens it. The
// library-row-folder class is kept so the checks that look for "the Library
// has loaded" find a row in both layouts.
export function folderRowHTML(c) {
  const n = c.childCount > 0
    ? `<span class="lib2-meta"><span class="u-sr-only">${escapeHtml(t('ui2.library.entries'))} </span>${c.childCount}</span>`
    : '';
  return `
      <li class="lib2-item">
        <button type="button" class="lib2-row lib2-folder library-row-folder" data-cid="${escapeAttr(c.id)}">
          <span class="lib2-icon">${ICONS.folder}</span>
          <span class="lib2-text"><span class="lib2-name">${escapeHtml(c.title)}</span></span>
          ${n}
          ${chevron}
        </button>
      </li>`;
}

// trackRowHTML is one track: what it is, its own feedback line, and its two
// actions, each named with the track and the speaker.
//
//   opts.key       the status key of this row (statusKey in views/library.js)
//   opts.attr      'iid' (a folder track, by id) or 'idx' (a search hit)
//   opts.value     that id or index
//   opts.speaker   the speaker that plays (its name), '' with none chosen
//   opts.meta      the line under the title
//   opts.duration  the formatted length, '' when not known
//   opts.status    the row's current feedback
export function trackRowHTML(it, { key, attr, value, speaker = '', meta = '', duration = '', status = null } = {}) {
  const title = it.title || it.streamURL || '';
  const playName = speaker
    ? t('ui2.library.playTrackOn', { title, name: speaker })
    : t('ui2.library.playTrack', { title });
  const data = `data-${attr}="${escapeAttr(String(value))}"`;
  return `
      <li class="lib2-item lib2-track library-row-track">
        <span class="lib2-icon">${ICONS.track}</span>
        <span class="lib2-text">
          <span class="lib2-name">${escapeHtml(title)}</span>
          ${meta ? `<span class="lib2-sub">${escapeHtml(meta)}</span>` : ''}
          ${statusHTML(key, status)}
        </span>
        ${duration ? `<span class="lib2-dur">${escapeHtml(duration)}</span>` : ''}
        <span class="lib2-acts">
          <button type="button" class="u-btn u-btn--icon lib2-play" ${data} aria-label="${escapeAttr(playName)}" title="${escapeAttr(playName)}">${ICONS.play}</button>
          ${keyButtonHTML({ cls: 'lib2-key', attrs: data, name: title, speaker })}
        </span>
      </li>`;
}

// folderActionsHTML is the bar above a folder that holds tracks: play it all
// as a queue, put it on a key, shuffle and repeat, and the feedback line of
// the two folder actions.
export function folderActionsHTML({ speaker = '', shuffle = false, repeat = 'off', status = null } = {}) {
  const play = speaker ? t('ui2.library.playFolderOn', { name: speaker }) : t('controls.playFolder');
  const key = speaker ? t('ui2.library.keyFolderOn', { name: speaker }) : t('controls.saveFolderPreset');
  return `
      <div class="lib2-folder-acts">
        <div class="lib2-folder-btns">
          <div class="lib2-folder-main">
            <button type="button" class="u-btn u-btn--primary lib2-play-folder">${ICONS.play}<span>${escapeHtml(play)}</span></button>
            <button type="button" class="u-btn u-btn--secondary lib2-key-folder">${KEYS_ICON}<span>${escapeHtml(key)}</span></button>
          </div>
          <div class="lib2-folder-modes">
            <button type="button" class="u-btn u-btn--secondary lib2-shuffle" aria-pressed="${shuffle ? 'true' : 'false'}">${escapeHtml(t('controls.shuffle'))}</button>
            <button type="button" class="u-btn u-btn--secondary lib2-repeat" data-mode="${escapeAttr(repeat)}">${escapeHtml(t(repeatLabelKey(repeat)))}</button>
          </div>
        </div>
        ${statusHTML('folder', status)}
      </div>`;
}

// filterRowHTML is the filter over the open folder (client side, over what is
// loaded), with its visible label and the count it narrows.
export function filterRowHTML(filter, count) {
  return `
      <div class="lib2-filter">
        <label class="lib2-filter-label" for="libSearch">${escapeHtml(t('ui2.library.filterLabel'))}</label>
        <input type="search" class="lib2-input" id="libSearch" placeholder="${escapeAttr(t('library.searchPlaceholder'))}" value="${escapeAttr(filter || '')}">
        <span class="lib2-count" id="libCount" role="status">${escapeHtml(countText(count))}</span>
      </div>`;
}

// countText is the count after the folder filter with its word in front
// ("Entries: 3", "Entries: 1 / 3" while a filter narrows it), so the number
// says what it counts on screen and for a screen reader.
export function countText(count) {
  return `${t('ui2.library.entries')} ${count}`;
}

// manualAddHTML is adding a server by its address, for the servers the
// network search cannot see (#341). The error of a failed add stays under the
// field it belongs to.
export function manualAddHTML({ open = false, error = '' } = {}) {
  return `
    <details class="lib2-manual"${open || error ? ' open' : ''}>
      <summary>${escapeHtml(t('library.addServerManual'))}</summary>
      <div class="lib2-manual-row">
        <label class="u-sr-only" for="libManualUrl">${escapeHtml(t('ui2.library.addServerLabel'))}</label>
        <input type="text" class="lib2-input" id="libManualUrl" placeholder="${escapeAttr(t('library.addServerPlaceholder'))}">
        <button type="button" class="u-btn u-btn--secondary" id="libManualAddBtn">${escapeHtml(t('library.addServerBtn'))}</button>
      </div>
      ${error ? `<p class="lib2-inline-error" role="alert">${escapeHtml(error)}</p>` : ''}
    </details>`;
}
