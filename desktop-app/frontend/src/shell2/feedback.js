// shell2/feedback.js: the verdict lines of the UI 2.0 preview's music views
// (#1286 phase 5, docs/UI-2.0.md rule 2: done only when the speaker
// confirmed it).
//
// A play or a key save passes through up to three honest states, and every
// line names the speaker (rule 1):
//   - starting       the request is on its way ("Starting on Kitchen...");
//   - sent           the speaker took the request but does not play it yet
//                    ("Sent to Kitchen. It has not started yet.");
//   - playing        the speaker itself reports that it plays it;
//   - key confirmed  the preset list read back from the speaker holds it.
//
// Pure: strings in, { kind, text } out. No DOM, nothing at module scope but
// constants, so the view modules (and vitest) can import it anywhere.

import { t } from '../i18n/index.js';

// startingVerdict is the line while a play is on its way to the speaker.
export function startingVerdict(speaker) {
  return { kind: 'busy', text: t('ui2.radio.starting', { speaker }) };
}

// sentVerdict is the line of a play the speaker accepted but has not
// started (yet). A warning, not an error: it may still start.
export function sentVerdict(speaker) {
  return { kind: 'warn', text: t('ui2.feedback.sentNotStarted', { speaker }) };
}

// playVerdict is the line once the speaker answered: "Playing" only for a
// play state the speaker reported itself (confirmed), anything else is
// "sent, not started".
export function playVerdict(speaker, confirmed) {
  return confirmed
    ? { kind: 'ok', text: t('ui2.radio.playing', { speaker }) }
    : sentVerdict(speaker);
}

// verdictFromPlayState maps the answer of views/library.js waitForPlayState
// ('playing', 'rejected', 'timeout', 'gone') to the line. 'gone' (no speaker
// selected any more) has nothing left to say: null.
export function verdictFromPlayState(speaker, verdict) {
  if (verdict === 'gone') return null;
  return playVerdict(speaker, verdict === 'playing');
}

// keyHolds reports whether the preset list read back from the speaker holds
// something on slot, and, when a name is given, that name (case and outer
// spaces aside: the agent stores what it was sent, a list may trim it).
export function keyHolds(presets, slot, name = '') {
  const p = (presets || []).find((x) => x && x.slot === slot);
  if (!p) return false;
  const want = String(name || '').trim().toLowerCase();
  if (!want) return true;
  return String(p.name || '').trim().toLowerCase() === want;
}

// keySavingVerdict is the line while a key is written.
export function keySavingVerdict(n, speaker) {
  return { kind: 'busy', text: t('ui2.radio.saving', { n, speaker }) };
}

// keyVerdict is the line after a key was written: confirmed only when the
// read-back holds it (keyHolds), otherwise the speaker did not confirm.
export function keyVerdict(n, speaker, confirmed) {
  return confirmed
    ? { kind: 'ok', text: t('ui2.radio.saved', { n, speaker }) }
    : { kind: 'warn', text: t('ui2.radio.notConfirmed', { n, speaker }) };
}

// keyFailedVerdict is a key change the speaker refused or never got, with
// the reason in the app's words.
export function keyFailedVerdict(n, speaker, reason, kind = 'error') {
  return { kind, text: t('ui2.radio.saveFailed', { n, speaker, error: reason || t('modal.unknownError') }) };
}
