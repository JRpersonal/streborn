// speakertile.js: what one speaker's tile says about it, as plain flags.
//
// Two places draw a speaker with its live state: the speaker picker on the
// Music tab (renderBoxSelect in main.js) and the sidebar speaker list of the UI 2.0
// preview (shell2/rooms.js). They must never disagree about whether a speaker
// is playing, offline or due an update, so both read the flags from here and
// only the markup differs.
//
// Pure: everything it needs comes in through ctx, so vitest covers it without
// a DOM, the Wails runtime or main.js.
//
//   ctx.currentBox          the speaker the Music tab controls (or null)
//   ctx.nowPlayState        that speaker's last play state from the status poll
//   ctx.nowSource           that speaker's last source ('STANDBY' when asleep)
//   ctx.boxPlaying          { deviceID: bool } from the every-speaker poll
//   ctx.needsUpdate(b)      boxNeedsUpdate from main.js
//   ctx.cannotReachInternet(b)  boxCannotReachInternet from nointernet.js
//   ctx.stereoPairs         live pairs (groups.js stereoPairsOf)

import { inStereoPair } from './groups.js';

function isPlayState(ps) {
  return ps === 'PLAY_STATE' || ps === 'BUFFERING_STATE';
}

export function speakerTileState(b, ctx = {}) {
  const box = b || {};
  const stock = box.kind === 'stock';
  const offline = !!box.offline;
  const cur = ctx.currentBox;
  const isCurrent = !!(cur && cur.host === box.host);
  // The selected box is marked live from nowPlayState (no poll lag); the others
  // from the refreshBoxPlaying poll. Exactly the rule behind the picker's small
  // speaker icon on a tile whose speaker is playing.
  const playing = !!((ctx.boxPlaying && ctx.boxPlaying[box.deviceID])
    || (isCurrent && isPlayState(ctx.nowPlayState)));
  const standby = isCurrent && !playing
    && (ctx.nowSource === 'STANDBY' || ctx.nowPlayState === 'STANDBY_STATE');
  const pairs = ctx.stereoPairs || [];
  return {
    offline,
    stock,
    // A box STR once ran on whose agent went silent while the stock firmware
    // still answers: a repair, not a first install.
    strNotRunning: stock && !!box.strNotRunning,
    playing,
    // Stock speakers have no agent to update and no agent to ask about the
    // internet, so both chips are STR-only.
    needsUpdate: !stock && typeof ctx.needsUpdate === 'function' && !!ctx.needsUpdate(box),
    noInternet: !stock && typeof ctx.cannotReachInternet === 'function' && !!ctx.cannotReachInternet(box),
    isCurrent,
    standby,
    inStereoPair: !stock && pairs.some((p) => inStereoPair(box, p)),
  };
}
