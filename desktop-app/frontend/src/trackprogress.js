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
export function applyPositionReading(tp, reading, keyAtStart, now) {
  const pos = reading && typeof reading.positionSec === 'number' ? reading.positionSec : -1;
  let dur = reading && typeof reading.durationSec === 'number' ? reading.durationSec : 0;
  if (pos < 0) return false;
  if (keyAtStart !== undefined && keyAtStart !== tp.key) return false;
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
}

// wantsFastPoll says whether the 1 s render tick should also ask the speaker
// for the position: only while playing, only while the length is still
// unknown, and only for a short window after the track changed.
export function wantsFastPoll(tp, playing, now) {
  if (!playing || tp.dur > 0 || !tp.resetAt) return false;
  return now - tp.resetAt < FAST_POLL_WINDOW_MS;
}
