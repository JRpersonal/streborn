// playbutton.js: what the one Play/Pause button does on its next press
// (discussion #1920).
//
// On the original Bose remote the Play key starts a speaker with what it
// played last. STR's button knew two things: pause what plays, and carry on a
// paused or stopped stream. On a speaker in standby, or awake with nothing
// loaded, it read "Pause" and sent a pause to a transport that had nothing to
// pause, so a press did nothing at all. That state is now the third answer:
// wake the speaker and bring back what it played last, which is what the
// power button's "on" already does (the agent's /api/box/power).
//
// The phone remote keeps the same rule in its own page (playButtonAction in
// internal/webui/assets/index.html); playbutton.test.js holds the two against
// each other.
//
// Pure: strings in, a string out, so vitest reads it without a DOM.

const PLAYING = new Set(['PLAY_STATE', 'BUFFERING_STATE']);
const HELD = new Set(['PAUSE_STATE', 'STOP_STATE']);
// What a speaker reports as its source while nothing is loaded: standby, or
// awake after its selection could not be restored.
const NOTHING_LOADED = new Set(['STANDBY', 'INVALID_SOURCE']);

// playButtonAction names the press for a play state and a source, both as the
// speaker reports them:
//
//   'pause'   something plays or is starting
//   'wake'    standby or nothing loaded: wake the speaker and resume the last
//             item it played
//   'resume'  paused or stopped on a real source: carry on where it was
//
// A speaker nothing is known about yet (no play state and no source, the
// moment before its first answer) keeps 'pause', the button's first state in
// the markup: the app does not offer to start a speaker it has not heard from.
export function playButtonAction(playState, nowSource) {
  const ps = String(playState || '');
  const src = String(nowSource || '').toUpperCase();
  if (PLAYING.has(ps)) return 'pause';
  if (NOTHING_LOADED.has(src)) return 'wake';
  if (HELD.has(ps)) return 'resume';
  return 'pause';
}

// playButtonOffersPlay says whether the button shows Play (true) or Pause.
export function playButtonOffersPlay(playState, nowSource) {
  return playButtonAction(playState, nowSource) !== 'pause';
}
