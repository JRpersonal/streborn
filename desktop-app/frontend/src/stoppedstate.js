// stoppedstate.js: how the desktop draws a speaker that is awake, has a source
// selected and reports STOP_STATE (discussion #1526).
//
// Forming a pair or group out of standby wakes the master, the firmware
// resumes its last station and the quiet wake stops it again. The master is
// then on, on that station, and stopped: nothing plays. The app had no picture
// for that state. It drew the pause bars in the green "now playing" colour, no
// state word, the song title from the brief resume and a lit preset key, so a
// stopped speaker read as "paused on key 1". It is "stopped", the word the
// phone remote already uses, never "standby": a group master really is on and
// cannot be put to standby without dissolving the group.

// nowStateLabel picks the state word (an i18n key, '' for none) and the bar's
// class for a play state and an upper-cased source. An explicit STOP_STATE on a
// real source is "stopped"; with no source (standby, nothing selected) the
// older branches stand.
export function nowStateLabel(ps, srcU) {
  if (ps === 'PLAY_STATE') return { key: 'status.playing', cls: 'play' };
  if (ps === 'BUFFERING_STATE') return { key: 'status.buffering', cls: 'buf' };
  if (ps === 'PAUSE_STATE') return { key: 'status.paused', cls: 'idle' };
  if (ps === 'STOP_STATE' && srcU && srcU !== 'STANDBY' && srcU !== 'INVALID_SOURCE') {
    return { key: 'status.stopped', cls: 'idle' };
  }
  if (srcU === 'STANDBY') return { key: 'status.standby', cls: 'idle' };
  return { key: '', cls: 'idle' };
}

// nowStateGlyph is the transport glyph in front of the now-playing line, so the
// state is not told by colour alone: a stop square for STOP_STATE, pause bars
// for PAUSE_STATE only, the play arrow otherwise.
export function nowStateGlyph(ps) {
  if (ps === 'STOP_STATE') return '&#9209;';
  if (ps === 'PAUSE_STATE') return '&#9208;';
  return '&#9205;';
}

// keepsLiveTitle says whether a play state may keep the cached live ICY title.
// Playing, starting and paused keep it. Any other REPORTED state (stopped,
// nothing selected) drops it: the agent already wipes its own copy a few
// seconds after the stream ends, and the app must not keep printing a song from
// a resume that lasted two seconds. An empty state is "not reported" and
// changes nothing.
export function keepsLiveTitle(ps) {
  if (!ps) return true;
  return ps === 'PLAY_STATE' || ps === 'BUFFERING_STATE' || ps === 'PAUSE_STATE';
}

// presetMayLight says whether the preset grid may light the key whose station
// is selected. A stopped speaker still reports the station's location, but the
// key is not playing. A key click's optimistic window still lights it at once,
// before the speaker has left STOP_STATE.
export function presetMayLight(ps, optimisticUntil, now) {
  return ps !== 'STOP_STATE' || now < (optimisticUntil || 0);
}
