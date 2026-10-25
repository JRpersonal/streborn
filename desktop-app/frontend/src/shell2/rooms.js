// shell2/rooms.js: the room switcher at the top of the UI 2.0 sidebar.
//
// "Which speaker am I controlling" is the anchor of the whole window
// (docs/UI-2.0.md): keys, now playing and most settings belong to one speaker.
// The switcher shows that speaker with its live state, and its list shows
// every speaker on the network the same way, through the shared pill.
//
// Pure: data in, markup out. The flags come from speakertile.js, the same
// reading the Music tab's picker uses, so the two layouts never disagree.
// A live stereo pair collapses into ONE row: the two halves play as one, and
// choosing either selects the pair's master, as the picker's frame does.

import { t } from '../i18n/index.js';
import { escapeAttr, escapeHtml } from '../utils.js';
import { pairMemberBoxes } from '../groups.js';
import { speakerTileState } from '../speakertile.js';
import { chipHTML, pillLabel, speakerChipKinds, speakerPillKind, statusPillHTML } from './pill.js';

const label = (ctx, b) => (typeof ctx.label === 'function' ? ctx.label(b) : '') || (b && (b.friendlyName || b.name || b.host)) || '';

// roomItems turns the box list into switcher rows, in the list's own order.
//
//   ctx  everything speakerTileState reads, plus
//        label(b)          the display name (getBoxLabel)
//        offlineTitle(b)   "Offline, last seen ..." for an unreachable speaker
//        pairName(pair)    the stored name of a stereo pair, '' when none
export function roomItems(boxes, ctx = {}) {
  const list = (boxes || []).filter(Boolean);
  const pairOfHost = new Map();
  for (const pair of ctx.stereoPairs || []) {
    const members = pairMemberBoxes(pair, list).map((x) => x.box).filter(Boolean);
    if (members.length < 2) continue;
    const masterUp = String(pair.master || '').toUpperCase();
    const master = members.find((b) => String(b.deviceID || '').toUpperCase() === masterUp) || members[0];
    const rec = { pair, members, master };
    for (const b of members) pairOfHost.set(b.host, rec);
  }
  const out = [];
  const done = new Set();
  for (const b of list) {
    if (done.has(b.host)) continue;
    const rec = pairOfHost.get(b.host);
    if (rec) {
      for (const m of rec.members) done.add(m.host);
      const tiles = rec.members.map((m) => speakerTileState(m, ctx));
      const tile = {
        ...speakerTileState(rec.master, ctx),
        playing: tiles.some((x) => x.playing),
        isCurrent: tiles.some((x) => x.isCurrent),
        standby: tiles.some((x) => x.standby),
        needsUpdate: tiles.some((x) => x.needsUpdate),
        noInternet: tiles.some((x) => x.noInternet),
        offline: tiles.every((x) => x.offline),
        inStereoPair: true,
      };
      const named = typeof ctx.pairName === 'function' ? ctx.pairName(rec.pair) : '';
      out.push(rowOf(rec.master, tile, named || rec.members.map((m) => label(ctx, m)).join(' + '), ctx, rec.members.map((m) => m.host)));
      continue;
    }
    done.add(b.host);
    out.push(rowOf(b, speakerTileState(b, ctx), label(ctx, b), ctx, [b.host]));
  }
  return out;
}

function rowOf(box, tile, name, ctx, hosts) {
  return {
    host: box.host,
    port: box.port,
    hosts,
    name,
    model: box.model && box.model !== 'SoundTouch' ? box.model : '',
    pill: speakerPillKind(tile),
    chips: speakerChipKinds(tile),
    isCurrent: tile.isCurrent && !tile.stock,
    title: tile.offline && typeof ctx.offlineTitle === 'function' ? ctx.offlineTitle(box) : '',
  };
}

// roomButtonLabel is the switcher button's accessible name: which speaker it
// controls, in which state, and that pressing it changes the speaker.
export function roomButtonLabel(item) {
  if (!item) return t('ui2.rooms.none');
  const state = pillLabel(item.pill) || (item.chips || []).map((c) => t(c === 'update' ? 'speaker.updateChip' : 'speaker.noInternetChip')).join(', ');
  return t('ui2.rooms.switcherLabel', { name: item.name, state: state || '-' });
}

// roomButtonHTML is what the switcher button shows: the name and the pill.
export function roomButtonHTML(item) {
  if (!item) {
    return `<span class="ui2-room-text"><span class="ui2-room-name ui2-room-name--none">${escapeHtml(t('ui2.rooms.none'))}</span></span>`;
  }
  const pill = statusPillHTML(item.pill);
  const chips = (item.chips || []).map(chipHTML).join('');
  return `<span class="ui2-room-text"><span class="ui2-room-name">${escapeHtml(item.name)}</span>`
    + (pill || chips ? `<span class="ui2-room-badges">${pill}${chips}</span>` : '')
    + '</span>';
}

// roomSwitcherHTML renders the popover list: one row per speaker (a pair is
// one row), the current one pressed, and always the way to add a speaker.
export function roomSwitcherHTML({ items, searching } = {}) {
  const rows = (items || []).map((it) => {
    const pill = statusPillHTML(it.pill);
    const chips = (it.chips || []).map(chipHTML).join('');
    return `<button type="button" class="u-row ui2-room-row${it.isCurrent ? ' is-selected' : ''}" data-host="${escapeAttr(it.host)}" data-port="${escapeAttr(it.port)}" aria-pressed="${it.isCurrent ? 'true' : 'false'}"${it.title ? ` title="${escapeAttr(it.title)}"` : ''}>`
      + '<span class="ui2-room-row-text">'
      + `<span class="ui2-room-row-name">${escapeHtml(it.name)}</span>`
      + (it.model ? `<span class="ui2-room-row-meta">${escapeHtml(it.model)}</span>` : '')
      + '</span>'
      + (pill || chips ? `<span class="ui2-room-badges">${pill}${chips}</span>` : '')
      + '</button>';
  }).join('');
  const empty = rows ? '' : `<p class="ui2-room-empty">${escapeHtml(t(searching ? 'speaker.searching' : 'ui2.rooms.none'))}</p>`;
  return `<div class="ui2-room-list-rows">${rows}${empty}</div>`
    + `<button type="button" class="u-btn u-btn--secondary ui2-room-add" id="ui2RoomAdd">${escapeHtml(t('nav.setupStick'))}</button>`;
}
