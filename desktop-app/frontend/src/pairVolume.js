// One volume control for a stereo pair (idea #1093).
//
// A live pair is two speakers playing one programme, left and right. The music
// view's slider used to move only the half that happened to be selected, so
// turning the pair down meant selecting each half in turn and matching them by
// ear, and the group panel deliberately leaves pair halves out (a pair is not a
// group the user can add to or take apart there). The phone remote already
// offers a "Pair" scope that moves both; this is the desktop's version of it.
//
// While a pair half is selected the main slider acts on the pair:
//   - it READS the louder half, like the phone's pair scope and the group
//     slider, because that is how loud the room actually is. Halves at 40 and
//     36 read 40, not 38.
//   - it WRITES both halves by the same offset from a baseline captured at the
//     last read, the way setGroupVolume does for a group (#401). Halves that
//     are deliberately set apart keep that spacing, and because the offset is
//     measured from the baseline rather than from the last write, a half that
//     hit 0 or 100 on the way gets its spacing back when the slider returns.
//
// Stereo balance is NOT touched: it is a read-out (see multiroom.js), and the
// relative levels of the two halves are left exactly as the user set them.
//
// Pure apart from the throttle's call into the supplied function, so the logic
// is covered by pairvolume.test.js without a DOM or bindings.

import { stereoPairsOf, pairMemberBoxes, inStereoPair, stereoPairKey } from './groups.js';

// pairVolumeTarget returns {pair, key, halves} when the selected speaker is half
// of a live stereo pair whose two halves are both discovered, otherwise null.
// halves are in LEFT/RIGHT order. A pair with only one half on the network gets
// no pair control: the slider then moves the one speaker that is there, which
// is what it did before, rather than pretending to move two.
export function pairVolumeTarget(selected, zoneLive, boxes) {
  if (!selected || selected.kind === 'stock') return null;
  const pair = stereoPairsOf(zoneLive || {}).find(p => inStereoPair(selected, p)) || null;
  if (!pair) return null;
  const seen = new Set();
  const halves = [];
  for (const { box } of pairMemberBoxes(pair, boxes)) {
    if (!box || box.kind === 'stock' || !box.host || seen.has(box.host)) continue;
    seen.add(box.host);
    halves.push(box);
  }
  if (halves.length < 2) return null;
  const key = stereoPairKey(pair) || halves.map(b => b.host).sort().join('+');
  return { pair, key, halves };
}

// pairShownLevel is the level the pair slider shows: the louder half. null when
// neither half answered.
export function pairShownLevel(levels) {
  const vals = Object.values(levels || {}).filter(v => typeof v === 'number' && v >= 0);
  return vals.length ? Math.max(...vals) : null;
}

const clamp = (v) => Math.max(0, Math.min(100, Math.round(v)));

// createPairVolume holds the baseline the pair slider offsets from.
//   key    the pair the baseline belongs to; a different pair starts over
//   levels the last known level per host (read or written)
//   base   the levels captured at the anchor
//   at     the slider position the baseline was captured at, or null
export function createPairVolume() {
  return { key: '', levels: {}, base: {}, at: null };
}

function capture(st, anchor) {
  st.base = { ...st.levels };
  st.at = anchor;
}

// notePairRead records levels read from the speakers and re-anchors the
// baseline at the louder half. Returns the level the slider should show, or
// null when nothing usable was read. The caller must not call this while the
// user is touching the slider: re-anchoring mid-drag measures the next move
// against levels our own writes already changed.
export function notePairRead(st, key, levels) {
  if (st.key !== key) {
    st.key = key;
    st.levels = {};
    st.at = null;
    st.base = {};
  }
  for (const [host, v] of Object.entries(levels || {})) {
    if (typeof v === 'number' && v >= 0) st.levels[host] = v;
  }
  const shown = pairShownLevel(st.levels);
  if (shown !== null) capture(st, shown);
  return shown;
}

// pairAnchored reports whether the pair slider has a baseline for this pair,
// that is whether the halves' levels have been read since the pair appeared.
export function pairAnchored(st, key) {
  return st.key === key && st.at !== null;
}

// pairLevelsFor moves the pair to slider position pct and returns the level
// each host should get, {host: level}. Both halves shift by pct minus the
// anchor.
//
// Returns null while the pair has no baseline. Moving then would have to guess
// the halves' levels, and the obvious guess (both at the slider position) is
// exactly what wipes out a balance the user set: in a headless run a pair at
// 34/28 stepped down to 27/27 on the first press, because the zone poll had
// named the pair before anything had read its levels. The caller moves the
// selected speaker alone in that case and asks for a read.
//
// A half that did not answer the last read is left where it is and is not in
// the result, the way the agent's group step leaves a silent member alone.
export function pairLevelsFor(st, key, hosts, pct) {
  if (!pairAnchored(st, key)) return null;
  const delta = pct - st.at;
  const out = {};
  for (const h of hosts) {
    if (typeof st.base[h] !== 'number') continue;
    out[h] = clamp(st.base[h] + delta);
    st.levels[h] = out[h];
  }
  return out;
}

// makePerHostThrottle is the one-in-flight throttle the music slider uses, kept
// per speaker. The shared app-wide throttle holds ONE pending call, so feeding
// it two halves in turn would drop whichever half was queued first and leave it
// behind. Here each host collapses its own burst to its latest level, and the
// two halves never wait for each other.
export function makePerHostThrottle(fn) {
  const slots = new Map();
  const fire = async (host, args) => {
    const slot = slots.get(host);
    slot.inFlight = true;
    try {
      await fn(...args);
    } catch (e) {
      console.warn('pair volume update failed', e);
    } finally {
      slot.inFlight = false;
      if (slot.pending) {
        const next = slot.pending;
        slot.pending = null;
        fire(host, next);
      }
    }
  };
  return (...args) => {
    const host = args[0];
    if (!slots.has(host)) slots.set(host, { inFlight: false, pending: null });
    const slot = slots.get(host);
    if (slot.inFlight) { slot.pending = args; return; }
    fire(host, args);
  };
}
