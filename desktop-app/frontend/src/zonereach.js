// zonereach.js: what the Multi-room editors do and say about a speaker the
// app cannot reach (#1741).
//
// Field report: ten minutes into a Wi-Fi outage (router switched off) three
// SoundTouch 10s were still offered for a group, "Create group" could be
// pressed, and the answer was "Could not change the group: Error:
// box_not_ready: master". "Create stereo pair" said the same. Two things were
// wrong: the editor offered speakers the app had already failed to reach, and
// the refusal reached the user as an internal code.
//
// Pure, so the decisions can be tested without the view; views/multiroom.js
// paints them.
import { boxUnreachable } from './groups.js';

// NOT_REACHABLE matches the failures that mean "this speaker is not on the
// network right now": the app's own readiness gate (FormZone answers
// box_not_ready when the main speaker's agent did not answer its probe) and
// the operating system's connect errors. A plain read timeout is left out on
// purpose: the speaker answered the connect, and a slow group form has been
// seen to go on and succeed after one, so "not reachable" would be wrong.
const NOT_REACHABLE = /box_not_ready|no route to host|network is unreachable|host is down|connection refused|dial tcp[^\n]*(timeout|i\/o)/i;

export function isNotReachableError(err) {
  const s = String((err && err.message) || err || '');
  return NOT_REACHABLE.test(s);
}

// groupCreateBlock says why "Create group" cannot be used right now, or null
// when it can. Only reachability is judged here: too few speakers at all, or a
// stereo pair in the way, have their own notes. master is the starred box (or
// null), zoneBoxes the speakers the editor shows, nameOf(box) a display name.
export function groupCreateBlock(master, zoneBoxes, nameOf) {
  const boxes = zoneBoxes || [];
  if (!boxes.some(boxUnreachable)) return null;
  const reachable = boxes.filter((b) => !boxUnreachable(b));
  if (reachable.length < 2) return { key: 'multiroom.groupNeedsReachable', params: {} };
  if (master && boxUnreachable(master)) {
    return { key: 'multiroom.mainNotReachable', params: { name: nameOf ? nameOf(master) : '' } };
  }
  return null;
}

// zoneFormFailure is the note for a group create whose call failed. A speaker
// that is not reachable is said in plain words; anything else keeps the old
// note with the error text, which is what a bug report needs.
export function zoneFormFailure(err, masterName) {
  if (isNotReachableError(err)) {
    return { key: 'multiroom.groupNotReachable', params: { names: masterName || '' } };
  }
  return { key: 'multiroom.formFailed', params: { err: String(err) } };
}

// stereoReachBlock names the picked halves the app already knows are not
// reachable, so the pairing is not sent at all; null when both answer.
export function stereoReachBlock(left, right, nameOf) {
  const off = [left, right].filter(boxUnreachable);
  if (!off.length) return null;
  return { key: 'multiroom.stereoNotReachable', params: { names: off.map((b) => (nameOf ? nameOf(b) : '')).join(', ') } };
}

// stereoFormFailure is the note for a stereo pairing whose call failed. The
// pairing is sent to the left speaker, so box_not_ready: master names that
// one; a connect error names both, since it does not say which one is out.
export function stereoFormFailure(err, leftName, rightName) {
  if (isNotReachableError(err)) {
    const s = String((err && err.message) || err || '');
    const names = /box_not_ready:\s*master/i.test(s)
      ? (leftName || '')
      : [leftName, rightName].filter(Boolean).join(', ');
    return { key: 'multiroom.stereoNotReachable', params: { names } };
  }
  return { key: 'multiroom.formFailed', params: { err: String(err) } };
}
