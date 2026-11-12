// Track progress bookkeeping for the Now Playing bar (#399, #845).
//
// Kept out of main.js so the rules below can be tested without a DOM. The
// caller owns one mutable record { sec, dur, at, key, resetAt } and feeds it
// speaker readings; this module only decides what a reading is allowed to do.

// How long after a track change the position is asked for on every 1 s render
// tick instead of only on the 5 s / 15 s status cadence. A fresh track reports
// its length a moment after it starts, and waiting for the next status tick
// for that is what made the bar appear up to 15 s late (#845). Capped, because
// radio never reports a length and must not keep the fast poll running.
export const FAST_POLL_WINDOW_MS = 15000;

// applyPositionReading folds one /api/position answer into the record.
// Returns true when the record changed.
//
// - A reading that could not be taken (pos < 0) changes nothing.
// - A reading taken for a track that has since been replaced (keyAtStart no
//   longer matches) changes nothing: it describes the previous track.
// - A zero length while a length is already known for the same track is a
//   momentary gap in the speaker's answer (Bose answers NOT_IMPLEMENTED for a
//   field it has no value for at that instant), not a track turning into radio.
//   The known length is kept, so the bar does not blink out for a poll.
// - A zero length right after a "track change" whose position simply continues
//   the previous track's clock is the same track: the speaker briefly reported
//   a different name or location for it (a metadata update mid-song) and then
//   answered without a length for a few seconds. The bar used to vanish for
//   those seconds while the elapsed clock kept counting (#1190), so the length
//   remembered by resetProgress is carried over instead.
// - The bar never runs backwards on its own; only a track change, which the
//   caller handles by resetting the record, may send it back to zero.
// - A jump the user asked for (beginSeek) is the one exception: the record
//   then shows the target at once, and readings are judged against it. One
//   near the target confirms the jump. One far from it is the speaker not
//   having jumped yet and is ignored for SEEK_SETTLE_MS; after that the
//   speaker's reading wins in either direction, so a jump that never happened
//   does not leave the bar on a position nothing plays at.
// - Whether the bar may be clicked (seekable) is the speaker's answer, never a
//   guess from the source or the track: only a reading can set it.
export function applyPositionReading(tp, reading, keyAtStart, now) {
  const pos = reading && typeof reading.positionSec === 'number' ? reading.positionSec : -1;
  let dur = reading && typeof reading.durationSec === 'number' ? reading.durationSec : 0;
  if (pos < 0) return false;
  if (keyAtStart !== undefined && keyAtStart !== tp.key) return false;
  tp.seekable = reading.seekable === true;
  if (tp.seekAt) {
    const expected = tp.sec + (now - tp.at) / 1000;
    const near = Math.abs(pos - expected) <= SEEK_NEAR_SEC;
    if (!near && now - tp.seekAt < SEEK_SETTLE_MS) return false;
    tp.seekAt = 0;
    tp.seekUndo = null;
    tp.sec = pos;
    if (dur > 0) tp.dur = dur;
    tp.at = now;
    return true;
  }
  if (dur <= 0 && tp.dur > 0 && pos <= tp.dur + 2) dur = tp.dur;
  if (dur <= 0 && tp.dur <= 0 && continuesPrevious(tp, pos, now)) dur = tp.prevDur;
  const drifted = tp.sec + (now - tp.at) / 1000;
  if (tp.at !== 0 && pos + 2 < drifted && dur === tp.dur) return false;
  tp.sec = pos;
  tp.dur = dur;
  tp.at = now;
  return true;
}

// CONTINUE_TOLERANCE_SEC is how far a reading may sit from where the previous
// track's clock would be now and still count as that same track playing on.
const CONTINUE_TOLERANCE_SEC = 3;

// continuesPrevious reports whether a position reading picks up exactly where
// the record's previous clock (saved by resetProgress) would be by now.
function continuesPrevious(tp, pos, now) {
  if (!(tp.prevDur > 0) || !tp.prevAt) return false;
  if (pos > tp.prevDur + 2) return false;
  const expected = tp.prevSec + (now - tp.prevAt) / 1000;
  return Math.abs(pos - expected) < CONTINUE_TOLERANCE_SEC;
}

// resetProgress starts the record over for a new track. It keeps the clock it
// replaces (prevSec / prevDur / prevAt) so applyPositionReading can tell a
// real new track from the same one under a briefly different name (#1190).
// A record that took no reading since its own reset holds no clock of its own,
// so the one before it is kept: the name flipping away and straight back is two
// resets in a row, and the second must not wipe what the first saved.
export function resetProgress(tp, key, now) {
  if (tp.at && tp.at !== tp.resetAt) {
    tp.prevSec = tp.sec;
    tp.prevDur = tp.dur;
    tp.prevAt = tp.at;
  } else if (!tp.at) {
    tp.prevSec = 0;
    tp.prevDur = 0;
    tp.prevAt = 0;
  }
  tp.sec = 0;
  tp.dur = 0;
  tp.at = now;
  tp.key = key || '';
  tp.resetAt = now;
  // A new track is not seekable until the speaker says so, and a jump asked
  // for in the old one is over.
  tp.seekable = false;
  tp.seekAt = 0;
  tp.seekUndo = null;
}

// wantsFastPoll says whether the 1 s render tick should also ask the speaker
// for the position: only while playing, only while the length is still
// unknown, and only for a short window after the track changed.
export function wantsFastPoll(tp, playing, now) {
  if (!playing || tp.dur > 0 || !tp.resetAt) return false;
  return now - tp.resetAt < FAST_POLL_WINDOW_MS;
}

// Jumping inside the track by clicking the bar (discussion #1207).

// How long after a jump a reading far from the target is taken for the
// speaker not having jumped yet, and how far "far" is.
export const SEEK_SETTLE_MS = 5000;
const SEEK_NEAR_SEC = 3;

// How far one press of an arrow key moves.
export const SEEK_KEY_STEP_SEC = 10;

// canSeek is the gate for every way of jumping: the speaker said this track
// is seekable, and its length is known. Radio, Spotify and a paused speaker
// never pass, because the agent answers seekable=false for them.
export function canSeek(tp) {
  return !!tp && tp.seekable === true && tp.dur > 0 && tp.at !== 0;
}

// shownSec is where the bar stands now: the last reading plus the time since,
// when the speaker is playing, capped at the track's length.
export function shownSec(tp, playing, now) {
  const drift = playing && tp.at ? (now - tp.at) / 1000 : 0;
  const sec = tp.sec + drift;
  return tp.dur > 0 ? Math.min(sec, tp.dur) : sec;
}

// seekTargetSec turns a pointer position on the bar into a whole second of
// the track. left and width are the bar's box; rtl mirrors it, because the
// bar fills from the right in a right-to-left language. A pointer outside the
// bar (a drag that left it) lands on the nearer end. Returns -1 when there is
// nothing to compute from.
export function seekTargetSec(clientX, left, width, dur, rtl) {
  if (!(width > 0) || !(dur > 0) || typeof clientX !== 'number' || Number.isNaN(clientX)) return -1;
  let frac = (clientX - left) / width;
  if (rtl) frac = 1 - frac;
  frac = Math.min(1, Math.max(0, frac));
  return Math.round(frac * dur);
}

// seekKeyTargetSec is the position an arrow key asks for, or -1 for any other
// key. Right and Up go forward, Left and Down back; right-to-left swaps Left
// and Right, as it does for every slider. Home goes to the start.
export function seekKeyTargetSec(tp, key, rtl, playing, now) {
  let step = 0;
  if (key === 'ArrowUp') step = 1;
  else if (key === 'ArrowDown') step = -1;
  else if (key === 'ArrowRight') step = rtl ? -1 : 1;
  else if (key === 'ArrowLeft') step = rtl ? 1 : -1;
  else if (key === 'Home') return 0;
  else return -1;
  const to = shownSec(tp, playing, now) + step * SEEK_KEY_STEP_SEC;
  return Math.round(Math.min(tp.dur, Math.max(0, to)));
}

// beginSeek shows the target at once, before the speaker has answered. It
// remembers the clock it replaces so failSeek can put it back.
export function beginSeek(tp, sec, now) {
  if (!tp.seekUndo) tp.seekUndo = { sec: tp.sec, at: tp.at };
  tp.sec = sec;
  tp.at = now;
  tp.seekAt = now;
}

// failSeek puts the clock back after the speaker refused the jump: the track
// played on from where it was.
export function failSeek(tp) {
  if (tp.seekUndo) {
    tp.sec = tp.seekUndo.sec;
    tp.at = tp.seekUndo.at;
  }
  tp.seekUndo = null;
  tp.seekAt = 0;
}
