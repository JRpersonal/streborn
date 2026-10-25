// shell2/speakers.js: every speaker in the UI 2.0 sidebar (#1284).
//
// DrDBanner's sidebar, adopted on #1284 on 14 October in place of the room
// switcher and the Output popover of the first draft. One list answers both
// questions of the window:
//
//   - which speaker you control: one click on its row;
//   - who plays along: "+" on an idle speaker adds it to the group of the
//     speaker you control, "-" on a member takes it out again.
//
// The group you control is pinned on top, the rest scrolls on its own, so with
// eight or more speakers the navigation below never scrolls away. A stereo pair
// is one row ("Living room (stereo)"), because it only plays as a whole.
//
// Pure: data in, markup out. The rows come from rooms.js (speakertile.js
// flags), the membership from groups.js: groupMembersOf is the very list the
// group edit acts on, and groupCandidates the set the Music view's chips
// offer, so a "+" never shows the opposite of what its click does. The click
// itself goes to toggleGroupMember (views/music/group.js), the one group edit
// path; nothing here forms or dissolves a zone.

import { t } from '../i18n/index.js';
import { escapeAttr, escapeHtml } from '../utils.js';
import { groupCandidates, groupMemberBoxes, groupMembersOf, inAnyStereoPair, masterOf, resolvePlayTarget } from '../groups.js';
import { roomItems } from './rooms.js';
import { CHIP_LABEL_KEYS, chipHTML, pillLabel, statusPillHTML } from './pill.js';

const has = (o, k) => !!o && !!k && Object.prototype.hasOwnProperty.call(o, k);

// sidebarModel sorts the rows into the pinned group and the others.
//
//   ctx  everything roomItems reads, plus
//        zoneLive    the shared zone poll (state.zoneLive)
//        pending     Set of hosts whose group edit waits or runs
//        searching   discovery has not answered yet
//
// The pinned group is the group of the speaker you control: its master (the
// speaker itself, or the master it follows) and the master's members. That is
// the group a "+" or "-" edits, and it is pinned whether or not it plays yet,
// so a group can be put together before pressing Play.
export function sidebarModel(boxes, ctx = {}) {
  const list = (boxes || []).filter(Boolean);
  const zl = ctx.zoneLive || {};
  const rows = roomItems(list, ctx);
  const sel = ctx.currentBox ? (list.find((b) => b.host === ctx.currentBox.host) || ctx.currentBox) : null;
  const strSel = sel && sel.kind !== 'stock' ? sel : null;
  const master = strSel ? (resolvePlayTarget(strSel, zl, list) || strSel) : null;
  const members = master ? groupMembersOf(master, zl, list) : [];
  const memberHosts = new Set(members.map((m) => m.ip).filter(Boolean));
  // The members the player shows (group volume, Levels): the same list, so
  // the heading and "+ n more" never count a speaker the player leaves out.
  const memberBoxes = master ? groupMemberBoxes(master, zl, list) : [];
  const groupHosts = new Set(master ? [master.host, ...memberHosts] : []);
  // A stereo pair plays on its own (#792): while you control one, nothing can
  // be added to it.
  const pairAlone = !!master && inAnyStereoPair(master, zl);
  const groupable = !!master && !pairAlone;
  const candidates = new Set(groupable ? groupCandidates(master, zl, list).map((b) => b.host) : []);
  const playing = ctx.boxPlaying || {};
  const pending = ctx.pending || new Set();
  const boxOf = (host) => list.find((b) => b.host === host);

  const decorate = (r) => {
    const pinned = r.hosts.some((h) => groupHosts.has(h));
    const isMaster = !!master && r.hosts.includes(master.host);
    let action = '';
    if (groupable && r.kind === 'str' && !r.inPair && candidates.has(r.host)) {
      if (pinned && !isMaster && memberHosts.has(r.host)) {
        action = 'remove';
      } else if (!pinned && !r.offline && !r.playing) {
        // "+" only on a speaker known to be free: in no zone of its own or of
        // another master, and with its play state read at least once. Before
        // the first every-speaker poll every row looks idle, and forming a zone with
        // a speaker that follows another master pulls it out of that group.
        const b = boxOf(r.host);
        const known = !!b && has(zl, b.deviceID) && has(playing, b.deviceID);
        if (known && masterOf(b.deviceID, zl) === '') action = 'add';
      }
    }
    const row = {
      ...r,
      pinned,
      isMaster,
      action,
      pending: r.hosts.some((h) => pending.has(h)),
    };
    if (r.kind === 'pair') {
      // The name already says it is a pair, so the pill does not repeat it.
      row.name = t('ui2.side.pairName', { name: r.name });
      if (row.pill === 'stereoPair') row.pill = '';
    }
    return row;
  };

  const all = rows.map(decorate);
  const pinnedRows = all.filter((r) => r.pinned);
  const masterRow = pinnedRows.find((r) => r.isMaster);
  const pinned = masterRow ? [masterRow, ...pinnedRows.filter((r) => r !== masterRow)] : pinnedRows;
  // "Playing together" only while the group plays: a group is pinned whether
  // or not it plays, and a stopped one is just a group.
  const grouped = memberBoxes.length > 0;
  const headingKey = !grouped ? 'ui2.side.controlling'
    : (pinned.some((r) => r.playing) ? 'ui2.side.together' : 'ui2.side.group');
  return {
    pinned,
    // In the list's own order, never re-sorted: rows do not jump under the
    // pointer when a speaker starts or stops playing.
    others: all.filter((r) => !r.pinned),
    headingKey,
    groupable,
    pairAlone,
    searching: !!ctx.searching,
    master,
    memberCount: memberBoxes.length,
  };
}

// rowState is everything a row shows as words: the pill and every chip.
function rowState(row) {
  return [pillLabel(row.pill), ...(row.chips || []).map((c) => (CHIP_LABEL_KEYS[c] ? t(CHIP_LABEL_KEYS[c]) : ''))]
    .filter(Boolean).join(', ');
}

// speakerRowLabel is the accessible name of a row's pick button: the name and
// the state it shows, so status is never told by colour alone. A Bose
// original also says that choosing it opens the setup.
export function speakerRowLabel(row) {
  if (!row) return '';
  const state = rowState(row);
  if (row.kind === 'stock') return t('ui2.side.rowLabelSetUp', { name: row.name, state });
  return state ? t('ui2.side.rowLabel', { name: row.name, state }) : row.name;
}

// actionLabel names the "+" or "-" with the speaker it acts on. While the edit
// waits or runs it says so: aria-busy alone is not announced on a button.
export function actionLabel(row) {
  if (!row || !row.action) return '';
  if (row.action === 'add') return t(row.pending ? 'ui2.side.adding' : 'ui2.side.add', { name: row.name });
  return t(row.pending ? 'ui2.side.removing' : 'ui2.side.remove', { name: row.name });
}

// tabbableHost picks the one pick button of the list that takes Tab: the one
// that had focus, else the controlled speaker, else the first row.
function tabbableHost(rows, focusHost) {
  if (focusHost && rows.some((r) => r.host === focusHost)) return focusHost;
  const cur = rows.find((r) => r.isCurrent);
  return cur ? cur.host : (rows[0] ? rows[0].host : '');
}

function rowHTML(row, tabHost) {
  const tab = row.host === tabHost ? '0' : '-1';
  const cls = ['ui2-spk-row',
    row.isCurrent ? 'is-current' : '',
    row.offline ? 'is-offline' : '',
    row.kind === 'stock' ? 'is-stock' : '',
    row.kind === 'pair' ? 'is-pair' : '',
    row.pinned ? 'is-member' : ''].filter(Boolean).join(' ');
  const pill = statusPillHTML(row.pill);
  const chips = (row.chips || []).map(chipHTML).join('');
  const pick = `<button type="button" class="ui2-spk-pick" data-host="${escapeAttr(row.host)}" data-port="${escapeAttr(row.port)}" tabindex="${tab}"`
    + (row.isCurrent ? ' aria-current="true"' : '')
    + ` aria-label="${escapeAttr(speakerRowLabel(row))}"`
    + (row.title ? ` title="${escapeAttr(row.title)}"` : '')
    + '>'
    + `<span class="ui2-spk-name">${escapeHtml(row.name)}</span>`
    + (pill || chips ? `<span class="ui2-room-badges">${pill}${chips}</span>` : '')
    + '</button>';
  let act = '';
  if (row.action) {
    const name = actionLabel(row);
    act = `<button type="button" class="u-btn u-btn--icon ui2-spk-act${row.pending ? ' pending' : ''}" data-group-toggle data-act="${row.action}"`
      + ` data-host="${escapeAttr(row.host)}" data-port="${escapeAttr(row.port)}" tabindex="${tab}"`
      + ` aria-label="${escapeAttr(name)}" title="${escapeAttr(name)}"${row.pending ? ' aria-busy="true"' : ''}>`
      + `<span aria-hidden="true">${row.action === 'add' ? '+' : '&minus;'}</span></button>`;
  }
  return `<li class="${cls}">${pick}${act}</li>`;
}

// sidebarHTML renders both lists: the pinned group with its heading, and the
// other speakers, which scroll on their own. One pick button takes Tab (plus
// the action next to it); the arrow keys move between rows (rovingIndex).
export function sidebarHTML(model, opts = {}) {
  const m = model || { pinned: [], others: [] };
  const rows = [...(m.pinned || []), ...(m.others || [])];
  const tabHost = tabbableHost(rows, opts.focusHost);
  let pinned = '';
  if ((m.pinned || []).length) {
    pinned = '<div class="ui2-spk-pinned" role="group" aria-labelledby="ui2SpkNowHead">'
      + `<div class="ui2-spk-subhead" id="ui2SpkNowHead">${escapeHtml(t(m.headingKey || 'ui2.side.controlling'))}</div>`
      + `<ul class="ui2-spk-list">${m.pinned.map((r) => rowHTML(r, tabHost)).join('')}</ul>`
      + (m.pairAlone ? `<p class="ui2-spk-note">${escapeHtml(t('ui2.side.pairAlone'))}</p>` : '')
      + '</div>';
  }
  const others = (m.others || []).map((r) => rowHTML(r, tabHost)).join('');
  const empty = rows.length ? ''
    : `<p class="ui2-spk-empty">${escapeHtml(t(m.searching ? 'speaker.searching' : 'ui2.side.empty'))}</p>`;
  const scroll = '<div class="ui2-spk-scroll">'
    + (others ? `<ul class="ui2-spk-list" aria-label="${escapeAttr(t('ui2.side.others'))}">${others}</ul>` : '')
    + empty
    + '</div>';
  return pinned + scroll;
}

// rovingIndex is where an arrow key moves in a list of n rows from index at:
// Down and Up one step (stopping at the ends), Home and End to the ends. -1
// for any other key, which the list leaves alone.
export function rovingIndex(key, at, n) {
  if (!(n > 0)) return -1;
  const i = at < 0 ? -1 : Math.min(at, n - 1);
  if (key === 'ArrowDown') return i < 0 ? 0 : Math.min(n - 1, i + 1);
  if (key === 'ArrowUp') return i < 0 ? n - 1 : Math.max(0, i - 1);
  if (key === 'Home') return 0;
  if (key === 'End') return n - 1;
  return -1;
}
