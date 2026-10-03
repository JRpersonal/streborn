// Where the start-volume marker sits on the volume bar, and what a drag means.
//
// Two functions that must be each other's inverse. If they are not, the marker
// shows one level and saves another, and both halves look right on their own:
// the marker lands where you dropped it, the speaker gets a number, and nobody
// notices until a speaker wakes up at the wrong volume days later.
//
// THUMB_PX is why this is not just a percentage. A range input's thumb CENTRE
// travels from thumb/2 to width - thumb/2, never from 0 to width, so a marker
// placed at a naive left:<pct>% agrees with the handle in the middle of the
// track and is half a handle out at both ends, which is exactly where somebody
// checks whether it is accurate.
export const THUMB_PX = 16;

// markLeft turns a level into the pixel offset of the marker's CENTRE inside a
// track of the given width.
export function markLeft(trackWidth, pct) {
  const travel = Math.max(0, (Number(trackWidth) || 0) - THUMB_PX);
  const p = Math.max(0, Math.min(100, Number(pct) || 0));
  return THUMB_PX / 2 + (p / 100) * travel;
}

// pctFromOffset is the inverse: an x inside the track, measured from its left
// edge, becomes a level.
//
// The floor is 1 rather than 0 because the speaker stores 0 as "off". A marker
// dragged to the far left would otherwise switch the feature off while still
// sitting there looking switched on, and the checkbox beside it would disagree
// with the thing it controls.
export function pctFromOffset(trackWidth, offsetX) {
  const travel = Math.max(1, (Number(trackWidth) || 0) - THUMB_PX);
  const x = (Number(offsetX) || 0) - THUMB_PX / 2;
  return Math.max(1, Math.min(100, Math.round((x / travel) * 100)));
}
