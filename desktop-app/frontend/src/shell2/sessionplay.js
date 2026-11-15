// shell2/sessionplay.js: Play/Pause on every playing row of the UI 2.0
// sidebar.
//
// With music on two speakers, pausing the one you do not control meant
// choosing it first, pausing it in the player, and choosing the first one
// again. Now each row that plays (a single speaker, the master of a group, a
// stereo pair) carries its own Play/Pause, which acts on that row's speaker
// through the Pause and Resume bindings and leaves the speaker you control as
// it was: the sidebar still has one place that chooses (the row's name).
//
// A group is paused through its master. A follower refuses direct transport
// control (the firmware answers 501, groups.js resolvePlayTarget, #70) and
// plays whatever its master plays, so a follower's row has no button of its
// own; the master's row says that it pauses the speakers playing along too.
//
// Pure: data in, data out. speakers.js puts the result on the row and draws
// it, mount.js sends the press and waits for the speaker to confirm it.

import { t } from '../i18n/index.js';
import { groupMemberBoxes, isFollower } from '../groups.js';

const NONE = Object.freeze({ kind: '', group: false });

// rowTransport decides the button a sidebar row gets.
//
//   row  a roomItems row: { host, hosts, kind, offline, playing }
//   ctx  boxes         the speaker list
//        zoneLive      the shared zone poll
//        currentBox    the speaker you control
//        nowPlayState  its play state from the status poll
//        nowSource     its source ('STANDBY' when asleep)
//        boxPaused     { deviceID: bool } from the every-speaker poll
//
// Returns { kind, group }: kind 'pause' while the row plays, 'play' while it
// is paused (the way back to the music), '' otherwise; group when pausing it
// also pauses speakers playing along. A stereo pair's other half does not make
// it a group: the pair is one row and plays as a whole.
export function rowTransport(row, ctx = {}) {
  if (!row || row.kind === 'stock' || row.offline || !row.host) return NONE;
  // A stereo pair plays through its master. With the master offline the row
  // picks the half that answers (rooms.js), which does not own the pair's
  // transport: no button rather than a press aimed at the wrong half.
  if (row.kind === 'pair' && row.pairMaster !== row.host) return NONE;
  const list = (ctx.boxes || []).filter(Boolean);
  const zl = ctx.zoneLive || {};
  const boxOf = (h) => list.find((b) => b.host === h) || null;
  const pick = boxOf(row.host);
  if (!pick || isFollower(pick.deviceID, zl)) return NONE;
  let kind = '';
  if (row.playing) kind = 'pause';
  else if ((row.hosts || [row.host]).some((h) => pausedNow(boxOf(h), ctx))) kind = 'play';
  if (!kind) return NONE;
  const hosts = new Set(row.hosts || [row.host]);
  const group = groupMemberBoxes(pick, zl, list).some((b) => !hosts.has(b.host));
  return { kind, group };
}

// pausedNow: the speaker you control is read from the status poll (no lag),
// every other one from the every-speaker poll. A paused speaker in standby is
// asleep, not paused.
function pausedNow(b, ctx) {
  if (!b) return false;
  const cur = ctx.currentBox;
  if (cur && cur.host === b.host && ctx.nowPlayState) {
    return ctx.nowPlayState === 'PAUSE_STATE' && ctx.nowSource !== 'STANDBY';
  }
  return !!(ctx.boxPaused && b.deviceID && ctx.boxPaused[b.deviceID]);
}

// rowTransportLabel names the button with the speaker (rule 1: the speaker is
// always named) and, on a group's master, with the speakers playing along.
// While a press waits for the speaker it says so: aria-busy alone is not
// announced on a button.
export function rowTransportLabel(row) {
  if (!row || !row.transport) return '';
  const name = row.name;
  if (row.transportPending) return t(row.transportPending === 'pause' ? 'ui2.side.pausing' : 'ui2.side.resuming', { name });
  if (row.transport === 'pause') return t(row.transportGroup ? 'ui2.side.pauseGroup' : 'ui2.side.pause', { name });
  return t(row.transportGroup ? 'ui2.side.playGroup' : 'ui2.side.play', { name });
}

// TRANSPORT_WITHIN_MS is how long a press waits for the speaker to report
// the change, counted from the moment the binding answered (two forced reads
// and a regular every-speaker round fit in it).
export const TRANSPORT_WITHIN_MS = 15000;

// TRANSPORT_SEND_MS is how long a press waits for the binding itself. Resume
// may take up to 15 s on its own (app_boxops.go resumeCallTimeout, #830: Play,
// up to 4 s watching, then Stop, Play, 2 s, Seek), so a slow but successful
// resume must not be called unconfirmed before it even answered. The binding
// always answers within its own deadline; the margin covers the round trip.
export const TRANSPORT_SEND_MS = 20000;

// transportVerdict decides a press (rule 2: sent is not arrived).
//
//   w    { want: 'pause' | 'resume', at, sent, sentAt }: sent once the
//        binding answered without an error, at sentAt
//   row  the row as the poll draws it now, or null when it left the list;
//        readAt is the start of the last every-speaker round that read the
//        row's speaker successfully (a failed read says nothing)
//
// 'confirmed' once the row shows the change: a resume when it plays again, a
// pause when it offers Play, or when a read that started after the binding
// answered finds it no longer playing. A failed read also leaves a row
// without its Pause, so that alone is no proof. 'gone' when the row left the
// list or went offline (nothing to confirm on; the row says offline itself),
// 'timeout' when the binding has not answered in TRANSPORT_SEND_MS or the
// speaker has not shown the change within ms after it answered, 'wait' until
// then.
export function transportVerdict(w, row, now, within = TRANSPORT_WITHIN_MS) {
  if (!w) return 'gone';
  if (!row || row.offline || row.kind === 'stock') return 'gone';
  if (!w.sent) return now - w.at > TRANSPORT_SEND_MS ? 'timeout' : 'wait';
  const since = typeof w.sentAt === 'number' ? w.sentAt : w.at;
  let shows;
  if (w.want === 'pause') {
    const freshRead = typeof row.readAt === 'number' && row.readAt >= since;
    shows = row.transport === 'play' || (row.transport !== 'pause' && freshRead);
  } else {
    shows = row.transport === 'pause';
  }
  if (shows) return 'confirmed';
  if (now - since > within) return 'timeout';
  return 'wait';
}
