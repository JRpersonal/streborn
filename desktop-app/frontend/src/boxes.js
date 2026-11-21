// boxes.js: the questions every view asks about a speaker record, in one place.
//
// Pure and import-free on purpose: no DOM, no state, no i18n, so any module
// (and any vitest file) can import it without a circular-import risk. Each
// helper replaces a one-liner that used to be repeated per file.

// up normalises an identifier for comparison. Device IDs arrive in mixed case
// (zone answers upper, discovery as the speaker reports them), so every
// comparison goes through it.
export const up = (s) => String(s || '').toUpperCase();

// isStr: a speaker record that runs STR, as opposed to a Bose original
// (kind 'stock') the app can only offer an install to. Tolerates a missing
// record.
export const isStr = (b) => !!b && b.kind !== 'stock';

// strBoxes filters a speaker list down to the speakers that run STR. Tolerates
// a missing list and holes in it.
export function strBoxes(boxes) {
  return (boxes || []).filter(isStr);
}

// labelOf returns a speaker's display name: its friendly name, else the agent
// name (the backend always fills this with a "str-<ip>" fallback), else the
// host. '' for a missing record.
export function labelOf(b) {
  return (b && (b.friendlyName || b.name || b.host)) || '';
}

// labelVia names a speaker through an injected label function (the views hand
// one down so a stereo pair or a pending rename is named their way) and falls
// back to labelOf when there is none or it has no answer.
export function labelVia(fn, b) {
  return (typeof fn === 'function' ? fn(b) : '') || labelOf(b);
}

// isPlaying: the speaker reports sound coming, or about to (buffering). Takes
// the playStatus string of now_playing; a paused or stopped speaker is false.
export function isPlaying(playState) {
  return playState === 'PLAY_STATE' || playState === 'BUFFERING_STATE';
}

// POWER_CYCLE_HEALTH are the agent's boxHealth values that only a power cycle
// clears: "wedged" (the speaker accepts playback and never plays) and
// "firmware-unresponsive" (the speaker's own Bose software stopped answering
// while STR runs on). Both get the "unplug it" banner.
export const POWER_CYCLE_HEALTH = Object.freeze(['wedged', 'firmware-unresponsive']);

// needsPowerCycle: the speaker reports one of POWER_CYCLE_HEALTH.
export function needsPowerCycle(box) {
  return !!box && POWER_CYCLE_HEALTH.includes(box.boxHealth);
}
