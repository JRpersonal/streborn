// shell2/speakers/list.js: Speakers > list, the default place of the UI 2.0
// Speakers area (phase 3, #1287; docs/design/ui-2.0/speakers.png, middle
// column).
//
// Every speaker as one card: its name, what it is (model, STR version, the
// stereo pair or fixed group it belongs to), ONE status pill, and the actions
// that belong at list level:
//
//   - the card itself opens the speaker's page (open('speaker', { host })); a
//     Bose original opens the setup on it instead (open('add', { host }));
//   - "Update" on a speaker with an update ready: the classic single-speaker
//     path (doBoxUpdate), after opening that speaker's page, where the update
//     paints its progress (the same order as the speaker-update card);
//   - "Set up STR" on a Bose original that is reachable;
//   - on a stereo pair, "Settings of <half>" for each half (the card itself
//     opens the pair's main speaker);
//   - "Search again" on an offline speaker, in its own card, which also says
//     what happened and what to do (errors where they happen).
//
// Below the cards: "Stereo pairs and groups: Set up" (open('groups')), "Update
// every speaker: n waiting" (updateAllBoxes, the classic update-all path) and
// "Add speaker" (open('add')).
//
// The rows are rooms.js roomItems, the very rows of the sidebar list, so the
// two never name a state differently: a stereo pair is one card, standby is
// known for the speaker you control only (the every-speaker poll reads
// playing or not, nothing more), "update ready" is boxNeedsUpdate. On top of
// them the list adds what a sidebar row has no room for: "starting up" (the
// record's strSilent marker), membership of a fixed group (the zone poll's
// permanent groups, live or stored), the version and what plays.
//
// Pure parts (pill precedence, groups, model, markup) are exported for vitest;
// the DOM work happens only inside renderSpeakerList, never at import (main.js
// imports this before the view containers exist).

import { getLocale, t } from '../../i18n/index.js';
import { escapeAttr, escapeHtml } from '../../utils.js';
import { roomItems } from '../rooms.js';
import { chipHTML, pillLabel, statusPillHTML, CHIP_LABEL_KEYS } from '../pill.js';
import { attentionItems, attentionNoteHTML, attentionSig, runAttentionAction } from '../attention.js';
import './list.css';

// The pills of the list, in precedence order: a card shows the first that
// holds. Not reachable first (every other fact about the speaker is stale),
// then starting up (the agent is not there yet to ask), then what kind of
// speaker it is (a Bose original, or one whose STR stopped), then what it
// does, then what it needs, then what it belongs to.
export const LIST_PILL_ORDER = Object.freeze([
  'notReachable', 'startingUp', 'strNotRunning', 'boseOriginal',
  'playing', 'standby', 'updateReady', 'stereoPair', 'fixedGroup',
]);

// The two pills only the list draws; the others are pill.js's own.
const LIST_PILLS = Object.freeze({
  updateReady: { key: 'ui2.speakers.list.pill.updateReady', tone: 'accent' },
  fixedGroup: { key: 'ui2.speakers.list.pill.fixedGroup', tone: 'muted' },
});

const FLAG_OF = Object.freeze({
  notReachable: 'offline',
  startingUp: 'startingUp',
  strNotRunning: 'strNotRunning',
  boseOriginal: 'stock',
  playing: 'playing',
  standby: 'standby',
  updateReady: 'updateReady',
  stereoPair: 'stereoPair',
  fixedGroup: 'fixedGroup',
});

// listPillKind picks the one pill of a card from its flags, '' for none. A
// Bose original whose STR stopped is a repair, not a first install, so
// strNotRunning wins over boseOriginal (both come with stock set).
export function listPillKind(f) {
  if (!f) return '';
  for (const kind of LIST_PILL_ORDER) {
    if (kind === 'boseOriginal' && f.strNotRunning) continue;
    if (f[FLAG_OF[kind]]) return kind;
  }
  return '';
}

export function listPillLabel(kind) {
  return LIST_PILLS[kind] ? t(LIST_PILLS[kind].key) : pillLabel(kind);
}

export function listPillHTML(kind) {
  const own = LIST_PILLS[kind];
  if (!own) return statusPillHTML(kind);
  return `<span class="u-pill u-pill--${own.tone}" data-kind="${kind}">`
    + '<span class="u-pill-dot" aria-hidden="true"></span>'
    + `<span class="u-pill-text">${escapeHtml(t(own.key))}</span></span>`;
}

const up = (s) => String(s || '').toUpperCase();

// fixedGroupRoles maps the address of every speaker in a fixed (permanent)
// group to its place in it: { role: 'main', members: [names] } on the main
// speaker, { role: 'member', main: name } on each member. Live and stored
// groups alike: the main speaker's zone answer carries `permanent`, with its
// live members while the group plays and the remembered ones while it does
// not (groups.js storedPermanentGroupsOf reads the same answer).
export function fixedGroupRoles(zoneLive, boxes, label = (b) => b.friendlyName || b.name || b.host) {
  const out = new Map();
  const list = (boxes || []).filter(Boolean);
  const boxOf = (m) => list.find((x) => x.deviceID && m.deviceID && up(x.deviceID) === up(m.deviceID))
    || list.find((x) => x.host && m.ip && x.host === m.ip) || null;
  for (const b of list) {
    if (b.kind === 'stock' || !b.deviceID || !b.host) continue;
    const e = zoneLive && zoneLive[b.deviceID];
    if (!e || !e.permanent) continue;
    const raw = (e.members || []).length ? e.members : (e.remembered || []);
    const members = raw.map((m) => ({ box: boxOf(m), name: m.name || '', ip: m.ip || '' }))
      .filter((m) => !(m.box ? m.box.host === b.host : m.ip === b.host));
    if (!members.length) continue;
    const mainName = label(b) || b.host;
    out.set(b.host, { role: 'main', members: members.map((m) => (m.box && label(m.box)) || m.name || m.ip) });
    for (const m of members) {
      const h = (m.box && m.box.host) || m.ip;
      if (h && !out.has(h)) out.set(h, { role: 'member', main: mainName });
    }
  }
  return out;
}

// agoText says how long ago a speaker was last seen, in the app's language
// (Intl, the same wording as the classic picker's offline tooltip).
export function agoText(sec, locale = getLocale()) {
  const s = Math.max(0, Number(sec) || 0);
  try {
    const rtf = new Intl.RelativeTimeFormat(locale || 'en', { numeric: 'auto' });
    if (s < 3600) return rtf.format(-Math.max(1, Math.round(s / 60)), 'minute');
    if (s < 86400) return rtf.format(-Math.round(s / 3600), 'hour');
    return rtf.format(-Math.round(s / 86400), 'day');
  } catch {
    return `${Math.max(1, Math.round(s / 60))} min`;
  }
}

const ver = (v) => (v ? (/^v/i.test(String(v)) ? String(v) : `v${v}`) : '');

// speakerListModel turns the speaker records into the cards the list draws,
// in the app's own order (state.boxes), and counts the speakers waiting for an
// update.
//
//   ctx  everything roomItems reads (currentBox, nowPlayState, nowSource,
//        boxPlaying, needsUpdate, cannotReachInternet, stereoPairs, label,
//        pairName), plus
//        zoneLive     the zone poll (fixed groups)
//        nowName      what the speaker you control plays
//        appVersion   the version this app installs (the update line)
//        otaHost      the speaker an update runs on right now, '' for none
//        otaRunning   any update runs (one speaker or every speaker)
//        search       Map host -> 'busy' | 'still' (the offline cards' search)
export function speakerListModel(boxes, ctx = {}) {
  const list = (boxes || []).filter((b) => b && b.host);
  const label = typeof ctx.label === 'function' ? ctx.label : (b) => b.friendlyName || b.name || b.host;
  const boxOf = (h) => list.find((b) => b.host === h) || null;
  const roles = fixedGroupRoles(ctx.zoneLive || {}, list, label);
  const cur = ctx.currentBox;
  const search = ctx.search || new Map();
  const rows = roomItems(list, ctx).map((r) => {
    const own = r.hosts.map(boxOf).filter(Boolean);
    const pick = boxOf(r.host) || own[0] || {};
    const stock = r.kind === 'stock';
    const pair = r.kind === 'pair';
    const offline = !!r.offline;
    const startingUp = !stock && !offline && own.some((b) => b.strSilent && !b.offline);
    const chips = r.chips || [];
    const updateReady = chips.includes('update');
    const role = pair ? null : roles.get(r.host) || null;
    const kind = listPillKind({
      offline,
      startingUp,
      stock,
      strNotRunning: r.pill === 'strNotRunning',
      playing: r.playing,
      standby: r.pill === 'standby',
      updateReady,
      stereoPair: pair || r.inPair,
      fixedGroup: !!role,
    });
    // What the card says under the name.
    const meta = [];
    if (pair) {
      // Its halves, unless the name already is them (a pair without a name of
      // its own); then what the halves are.
      const halves = own.map((b) => label(b) || b.host).join(' + ');
      if (r.name !== halves) meta.push(t('ui2.speakers.list.pairOf', { names: halves }));
      else meta.push([...new Set(own.map((b) => b.model).filter((m) => m && m !== 'SoundTouch'))].join(' + '));
    } else if (r.model) {
      meta.push(r.model);
    }
    if (stock) meta.push(t('ui2.speakers.list.boseOriginal'));
    else if (!pair && pick.version) meta.push(t('ui2.speakers.list.version', { version: ver(pick.version) }));
    let group = '';
    if (role && role.role === 'main') group = t('ui2.speakers.list.groupMain', { names: role.members.join(', ') });
    else if (role) group = t('ui2.speakers.list.groupMember', { name: role.main });
    // What plays: known for the speaker you control (and the group it leads).
    const controls = !!cur && r.hosts.includes(cur.host);
    const now = r.playing && controls && ctx.nowName ? String(ctx.nowName) : '';
    let action = '';
    // An offline Bose original cannot be set up until it is found again: its
    // card offers the search (noteHTML), as the sidebar does for that row.
    if (stock && !offline) action = 'setup';
    else if (r.kind === 'str' && updateReady && !offline && !startingUp) {
      action = ctx.otaHost && r.hosts.includes(ctx.otaHost) ? 'updating' : 'update';
    }
    const updateLine = updateReady && !offline && ctx.appVersion && !pair
      ? t('ui2.speakers.list.updateLine', { version: ver(pick.version), next: ver(ctx.appVersion) })
      : '';
    const since = own.map((b) => b.offlineSinceSec).filter((n) => typeof n === 'number' && n > 0);
    return {
      host: r.host,
      hosts: r.hosts,
      // A pair by its own name; the pill and the line below say it is a pair.
      name: r.name,
      kind: r.kind,
      current: !!r.isCurrent,
      offline,
      startingUp,
      pill: kind,
      // The chips hold next to the pill (a playing speaker can be due an
      // update); an offline speaker's facts are stale, so it shows none.
      chips: offline ? [] : chips.filter((c) => !(c === 'update' && kind === 'updateReady')),
      meta: meta.join(' · '),
      group,
      now,
      action,
      // A pair's card opens its main speaker; each half keeps a way to its
      // own settings (the classic Settings dropdown listed both).
      halves: pair ? own.map((b) => ({ host: b.host, name: label(b) || b.host })) : [],
      updateLine,
      offlineSince: offline && since.length ? Math.min(...since) : 0,
      search: offline ? (search.get(r.host) || '') : '',
    };
  });
  // The speakers the update-all path would update: reachable STR speakers
  // with an update ready (runUpdateAllBoxes leaves the offline ones out).
  const waiting = typeof ctx.needsUpdate === 'function'
    ? list.filter((b) => b.kind !== 'stock' && !b.offline && ctx.needsUpdate(b)).length
    : 0;
  return { rows, waiting, updating: !!(ctx.otaRunning || ctx.otaHost) };
}

// cardState is a card's state in words: the pill and every chip.
function cardState(row) {
  return [listPillLabel(row.pill), ...row.chips.map((c) => (CHIP_LABEL_KEYS[c] ? t(CHIP_LABEL_KEYS[c]) : ''))]
    .filter(Boolean).join(', ');
}

function actionHTML(row) {
  const host = escapeAttr(row.host);
  if (row.action === 'setup') {
    return `<button type="button" class="u-btn u-btn--primary spl-act" data-act="setup" data-host="${host}"`
      + ` aria-label="${escapeAttr(t('ui2.speakers.list.setUpLabel', { name: row.name }))}">`
      + `${escapeHtml(t('ui2.speakers.list.setUp'))}</button>`;
  }
  if (row.action === 'update') {
    return `<button type="button" class="u-btn u-btn--primary spl-act" data-act="update" data-host="${host}"`
      + ` aria-label="${escapeAttr(t('ui2.speakers.list.updateLabel', { name: row.name }))}">`
      + `${escapeHtml(t('ui2.speakers.list.update'))}</button>`;
  }
  if (row.action === 'updating') {
    return `<span class="spl-busy">${escapeHtml(t('ui2.speakers.list.updating', { name: row.name }))}</span>`;
  }
  return '';
}

// noteHTML is the explanation a card carries for a state that needs one: an
// offline speaker (what happened, what to do, and the search), a speaker
// starting up, a Bose original whose STR stopped.
function noteHTML(row) {
  if (row.offline) {
    const busy = row.search === 'busy';
    const what = row.offlineSince
      ? t('ui2.speakers.list.offlineSince', { ago: agoText(row.offlineSince) })
      : t('ui2.speakers.list.offlineNow');
    return '<div class="spl-note">'
      + `<p class="spl-note-text"><strong>${escapeHtml(what)}</strong> ${escapeHtml(t('ui2.speakers.list.offlineWhat'))}</p>`
      + `<button type="button" class="u-btn u-btn--secondary spl-act" data-act="search" data-host="${escapeAttr(row.host)}"`
      + `${busy ? ' aria-disabled="true" aria-busy="true"' : ''}`
      + ` aria-label="${escapeAttr(t(busy ? 'ui2.speakers.list.searching' : 'ui2.speakers.list.searchLabel', { name: row.name }))}">`
      + `${escapeHtml(t(busy ? 'ui2.speakers.list.searching' : 'ui2.speakers.list.search', { name: row.name }))}</button>`
      + (row.search === 'still'
        ? `<p class="spl-note-result">${escapeHtml(t('ui2.speakers.list.stillOffline', { name: row.name }))}</p>`
        : '')
      + '</div>';
  }
  if (row.startingUp) {
    return `<div class="spl-note"><p class="spl-note-text">${escapeHtml(t('ui2.speakers.list.startingUpNote'))}</p></div>`;
  }
  if (row.pill === 'strNotRunning') {
    return `<div class="spl-note"><p class="spl-note-text">${escapeHtml(t('ui2.speakers.list.strNotRunningNote'))}</p></div>`;
  }
  return '';
}

// halvesHTML: on a stereo pair's card, one button per half to its own page.
// attnHTML: what this speaker (and on a pair's card, either half) needs from
// you, the per-speaker side of the classic wedge and issue banners
// (attention.js), as a note on its card.
function attnHTML(row) {
  const hosts = [row.host, ...((row.halves || []).map((h) => h.host))].filter((h, i, a) => h && a.indexOf(h) === i);
  return attentionNoteHTML(hosts.flatMap((h) => attentionItems(h)));
}

function halvesHTML(row) {
  if (!row.halves || !row.halves.length) return '';
  return '<div class="spl-halves">'
    + row.halves.map((h) => `<button type="button" class="u-btn u-btn--secondary spl-act" data-act="half" data-host="${escapeAttr(h.host)}">`
      + `${escapeHtml(t('ui2.speakers.list.halfSettings', { name: h.name }))}</button>`).join('')
    + '</div>';
}

function cardHTML(row) {
  const stock = row.kind === 'stock';
  const cls = ['spl-item',
    row.current ? 'is-current' : '',
    row.offline ? 'is-offline' : '',
    stock ? 'is-stock' : '',
    row.kind === 'pair' ? 'is-pair' : ''].filter(Boolean).join(' ');
  const state = cardState(row);
  // A Bose original opens the setup, and its name says so (the sidebar's
  // own wording for the same row). An offline one cannot be set up: its card
  // searches again instead, the way the sidebar answers a pick of it. Every
  // other card is read from its words.
  const setUp = stock && !row.offline;
  const route = stock ? (row.offline ? 'search' : 'add') : 'speaker';
  const aria = setUp ? ` aria-label="${escapeAttr(t('ui2.side.rowLabelSetUp', { name: row.name, state }))}"` : '';
  const badges = listPillHTML(row.pill) + row.chips.map(chipHTML).join('');
  return `<li class="${cls}" data-host="${escapeAttr(row.host)}">`
    + '<div class="spl-card">'
    + `<button type="button" class="spl-open u-row${row.current ? ' is-selected' : ''}"`
    + ` data-host="${escapeAttr(row.host)}" data-route="${route}"${row.current ? ' aria-current="true"' : ''}${aria}>`
    + '<span class="spl-text">'
    + `<span class="spl-name">${escapeHtml(row.name)}</span>`
    + (row.meta ? `<span class="spl-meta">${escapeHtml(row.meta)}</span>` : '')
    + (row.group ? `<span class="spl-meta spl-group">${escapeHtml(row.group)}</span>` : '')
    + (row.now ? `<span class="spl-now">${escapeHtml(t('ui2.speakers.list.nowPlaying', { title: row.now }))}</span>` : '')
    + (row.updateLine ? `<span class="spl-meta spl-update">${escapeHtml(row.updateLine)}</span>` : '')
    + '</span>'
    + (badges ? `<span class="spl-badges">${badges}</span>` : '')
    + '</button>'
    + (row.action ? `<div class="spl-actions">${actionHTML(row)}</div>` : '')
    + '</div>'
    + halvesHTML(row)
    + noteHTML(row)
    + attnHTML(row)
    + '</li>';
}

function footHTML(model) {
  const lines = [];
  lines.push('<div class="spl-foot-row">'
    + `<span class="spl-foot-text">${escapeHtml(t('ui2.speakers.list.groupsText'))}</span>`
    + `<button type="button" class="u-btn u-btn--secondary spl-act" data-act="groups" aria-label="${escapeAttr(t('ui2.speakers.list.groupsLabel'))}">`
    + `${escapeHtml(t('ui2.speakers.list.groupsAct'))}</button></div>`);
  if (model.waiting > 0) {
    const label = t('ui2.speakers.list.waitingLabel', { count: model.waiting });
    lines.push('<div class="spl-foot-row">'
      + `<span class="spl-foot-text">${escapeHtml(t('ui2.speakers.list.updateAllText'))}</span>`
      + (model.updating
        ? `<span class="spl-busy">${escapeHtml(t('ui2.speakers.list.updateRunning'))}</span>`
        : `<button type="button" class="u-btn u-btn--secondary spl-act" data-act="updateAll" aria-label="${escapeAttr(label)}">`
          + `${escapeHtml(t('ui2.speakers.list.waiting', { count: model.waiting }))}</button>`)
      + '</div>');
  }
  lines.push('<div class="spl-foot-row">'
    + `<span class="spl-foot-text">${escapeHtml(t('ui2.speakers.list.addText'))}</span>`
    + `<button type="button" class="u-btn u-btn--primary spl-act" data-act="add">${escapeHtml(t('ui2.speakers.add'))}</button></div>`);
  return `<div class="spl-foot">${lines.join('')}</div>`;
}

export function speakerListHTML(model) {
  const m = Array.isArray(model) ? { rows: model, waiting: 0 } : (model || { rows: [], waiting: 0 });
  const body = m.rows.length
    ? `<ul class="spl-list" aria-labelledby="ui2SpkAreaTitle">${m.rows.map(cardHTML).join('')}</ul>`
    : `<p class="spl-empty u-card u-card--empty">${escapeHtml(t('ui2.side.empty'))}</p>`;
  return body + footHTML(m);
}

let lastSig = '';
let lastCtx = {};
const wired = new WeakSet();
// The offline cards' "Search again": host -> 'busy' while the search runs,
// 'still' when it came back without the speaker. Cleared when it is back.
const search = new Map();

const findBox = (host) => ((lastCtx.boxes || []).find((b) => b && b.host === host)) || null;

function repaint(root) {
  lastSig = '';
  renderSpeakerList(root, lastCtx);
}

async function searchAgain(root, host) {
  if (search.get(host) === 'busy') return;
  search.set(host, 'busy');
  repaint(root);
  try {
    if (typeof lastCtx.discoverBoxes === 'function') await lastCtx.discoverBoxes();
  } catch { /* the search reports its own errors */ }
  // Honest: "still not reachable" only from the record the search left.
  const box = findBox(host);
  if (box && box.offline) search.set(host, 'still');
  else search.delete(host);
  repaint(root);
  if (box && box.offline) announce(t('ui2.speakers.list.stillOffline', { name: (lastCtx.label && lastCtx.label(box)) || box.friendlyName || box.name || host }));
}

// The list's live region: one node outside the repainted markup, created
// empty when the list first paints, so a screen reader has registered it
// before the first result is written into it.
let liveRegion = null;
function ensureLiveRegion() {
  if (liveRegion && document.body.contains(liveRegion)) return liveRegion;
  liveRegion = document.createElement('div');
  liveRegion.id = 'ui2SplLive';
  liveRegion.className = 'u-sr-only';
  liveRegion.setAttribute('role', 'status');
  liveRegion.setAttribute('aria-live', 'polite');
  document.body.appendChild(liveRegion);
  return liveRegion;
}

function announce(text) {
  const region = ensureLiveRegion();
  // The same words twice in a row are a change too: clear first.
  region.textContent = '';
  setTimeout(() => { region.textContent = text; }, 50);
}

function onClick(root, e) {
  if (runAttentionAction(e.target)) return;
  const ctx = lastCtx;
  const act = e.target.closest('.spl-act');
  if (act) {
    if (act.getAttribute('aria-disabled') === 'true') return;
    const host = act.dataset.host || '';
    const open = typeof ctx.open === 'function' ? ctx.open : () => {};
    switch (act.dataset.act) {
      case 'groups': open('groups'); break;
      case 'add': open('add'); break;
      case 'setup': open('add', { host }); break;
      case 'half': open('speaker', { host }); break;
      case 'updateAll':
        if (typeof ctx.updateAll === 'function') ctx.updateAll();
        break;
      case 'update': {
        const box = findBox(host);
        if (!box) return;
        // The update paints its progress on the speaker's page, so open that
        // first (the speaker-update card does the same), then start it.
        open('speaker', { host });
        if (typeof ctx.doBoxUpdate === 'function') ctx.doBoxUpdate(box);
        break;
      }
      case 'search': searchAgain(root, host); break;
      default: break;
    }
    return;
  }
  const btn = e.target.closest('.spl-open');
  if (btn && btn.dataset.route === 'search') { searchAgain(root, btn.dataset.host); return; }
  if (btn && typeof ctx.open === 'function') ctx.open(btn.dataset.route, { host: btn.dataset.host });
}

// renderSpeakerList paints the list into root (#view-speakers). Called on
// every entry to the list and every sidebar repaint, so it repaints only when
// what it would draw changed. Delegated handler, wired once per root, so a
// repaint never loses it.
export function renderSpeakerList(root, ctx = {}) {
  if (!root) return;
  lastCtx = ctx;
  ensureLiveRegion();
  if (!wired.has(root)) {
    wired.add(root);
    root.addEventListener('click', (e) => onClick(root, e));
  }
  // A speaker that came back needs no search note any more.
  for (const h of [...search.keys()]) {
    const b = findBox(h);
    if (!b || !b.offline) search.delete(h);
  }
  const model = speakerListModel(ctx.boxes, { ...ctx, search });
  const sig = JSON.stringify([model, getLocale(), attentionSig()]);
  if (sig === lastSig && root.firstChild) return;
  lastSig = sig;
  const active = document.activeElement;
  const inside = !!active && root.contains(active) && !!active.dataset;
  const keepHost = inside ? active.dataset.host || '' : '';
  const keepAct = inside ? active.dataset.act || '' : '';
  root.innerHTML = speakerListHTML(model);
  if (inside) {
    const sel = keepAct ? `.spl-act[data-act="${keepAct}"]` : '.spl-open';
    const again = [...root.querySelectorAll(sel)].find((b) => (b.dataset.host || '') === keepHost)
      || [...root.querySelectorAll('.spl-open')].find((b) => b.dataset.host === keepHost);
    if (again) again.focus();
  }
}
