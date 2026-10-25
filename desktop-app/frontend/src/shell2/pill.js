// shell2/pill.js: the one status pill of UI 2.0 (docs/UI-2.0.md, "STR's real
// speaker states in one status pill used everywhere").
//
// A speaker shows at most ONE state pill and, independently of it, the chips
// for "Update" and "No internet": a playing speaker can be due an update, and
// hiding either fact behind the other would be wrong. The pill always carries
// its words; the coloured dot next to them is decoration (aria-hidden), so the
// state is never told by colour alone.
//
// Pure: markup strings only, so vitest covers the precedence without a DOM.

import { t } from '../i18n/index.js';
import { escapeHtml } from '../utils.js';

// Every state the pill knows. startingUp and installFailed are components
// only in phase 1: nothing in the app derives them yet (the install flow keeps
// its own wording until the setup wizard of phase 4).
export const PILL_KINDS = Object.freeze([
  'playing', 'standby', 'strNotRunning', 'boseOriginal',
  'notReachable', 'stereoPair', 'startingUp', 'installFailed',
]);

// The words on each pill. Existing keys are reused where the app already says
// the same thing, so the two layouts name a state identically.
export const PILL_LABEL_KEYS = Object.freeze({
  playing: 'ui2.pill.playing',
  standby: 'ui2.pill.standby',
  strNotRunning: 'speaker.strNotRunningBadge',
  boseOriginal: 'speaker.needsInstallBadge',
  notReachable: 'ui2.pill.offline',
  stereoPair: 'ui2.pill.stereoPair',
  startingUp: 'ui2.pill.startingUp',
  installFailed: 'ui2.pill.installFailed',
});

// The colour family of each pill, a token name from shell2.css (--u-<tone>).
const PILL_TONE = Object.freeze({
  playing: 'ok',
  standby: 'muted',
  strNotRunning: 'warn',
  boseOriginal: 'accent',
  notReachable: 'danger',
  stereoPair: 'accent',
  startingUp: 'muted',
  installFailed: 'danger',
});

export const CHIP_LABEL_KEYS = Object.freeze({
  update: 'speaker.updateChip',
  noInternet: 'speaker.noInternetChip',
});

// speakerPillKind picks the one state a speaker tile shows, from the flags of
// speakertile.js. An unreachable speaker says so before anything else (every
// other fact about it is stale); a stock speaker is either a first install or
// a repair; then what the speaker is doing. '' means no pill.
export function speakerPillKind(tile) {
  if (!tile) return '';
  if (tile.offline) return 'notReachable';
  if (tile.stock) return tile.strNotRunning ? 'strNotRunning' : 'boseOriginal';
  if (tile.playing) return 'playing';
  if (tile.isCurrent && tile.standby) return 'standby';
  if (tile.inStereoPair) return 'stereoPair';
  return '';
}

// speakerChipKinds lists the chips shown next to the pill, in a fixed order.
export function speakerChipKinds(tile) {
  if (!tile) return [];
  const out = [];
  if (tile.needsUpdate) out.push('update');
  if (tile.noInternet) out.push('noInternet');
  return out;
}

// pillLabel is the pill's words as plain text, for an accessible name that
// has to say the state too (a sidebar row).
export function pillLabel(kind) {
  const key = PILL_LABEL_KEYS[kind];
  return key ? t(key) : '';
}

export function statusPillHTML(kind) {
  if (!PILL_LABEL_KEYS[kind]) return '';
  return `<span class="u-pill u-pill--${PILL_TONE[kind]}" data-kind="${kind}">`
    + '<span class="u-pill-dot" aria-hidden="true"></span>'
    + `<span class="u-pill-text">${escapeHtml(pillLabel(kind))}</span></span>`;
}

export function chipHTML(kind) {
  const key = CHIP_LABEL_KEYS[kind];
  if (!key) return '';
  return `<span class="u-chip u-chip--${kind === 'update' ? 'action' : 'warn'}" data-chip="${kind}">${escapeHtml(t(key))}</span>`;
}
