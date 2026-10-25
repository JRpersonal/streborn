// shell2/rooms.js: one row per speaker, for the UI 2.0 sidebar list.
//
// "Which speaker am I controlling" is the anchor of the whole window
// (docs/UI-2.0.md): keys, now playing and most settings belong to one speaker.
// The sidebar lists every speaker on the network with its live state, through
// the shared pill (speakers.js draws the list).
//
// Pure: data in, rows out. The flags come from speakertile.js, the same
// reading the Music tab's picker uses, so the two layouts never disagree.
// A live stereo pair collapses into ONE row: the two halves play as one, and
// choosing it selects the pair's master, as the picker's frame does.

import { pairMasterBox, pairMemberBoxes } from '../groups.js';
import { speakerTileState } from '../speakertile.js';
import { speakerChipKinds, speakerPillKind } from './pill.js';

const label = (ctx, b) => (typeof ctx.label === 'function' ? ctx.label(b) : '') || (b && (b.friendlyName || b.name || b.host)) || '';

// roomItems turns the box list into rows, in the list's own order.
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
    // The master by deviceID or IP (pairMasterBox): on a two-chip chassis the
    // firmware id is not the one discovery knows, and a deviceID-only match fell
    // back to the LEFT half, which is not always the master.
    const found = pairMasterBox(pair, list);
    const master = (found && members.find((b) => b.host === found.host)) || members[0];
    // The row selects the master. With the master offline, choosing it would
    // only say "offline" while the other half answers: the row then selects
    // the half that is there.
    const pick = !master.offline ? master : (members.find((b) => !b.offline) || master);
    const rec = { pair, members, master, pick };
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
      out.push({
        ...rowOf(rec.pick, tile, named || rec.members.map((m) => label(ctx, m)).join(' + '), ctx, rec.members.map((m) => m.host)),
        kind: 'pair',
      });
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
    kind: tile.stock ? 'stock' : 'str',
    offline: !!tile.offline,
    playing: !!tile.playing,
    // Half of a live pair, matched by deviceID or IP (speakertile.js). A half
    // whose partner has left the list is not collapsed into a pair row, and
    // still cannot join a group.
    inPair: !!tile.inStereoPair,
    model: box.model && box.model !== 'SoundTouch' ? box.model : '',
    pill: speakerPillKind(tile),
    chips: speakerChipKinds(tile),
    isCurrent: tile.isCurrent && !tile.stock,
    title: tile.offline && typeof ctx.offlineTitle === 'function' ? ctx.offlineTitle(box) : '',
  };
}
