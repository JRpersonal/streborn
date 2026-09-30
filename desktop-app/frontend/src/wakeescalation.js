// What to tell somebody whose speaker will not wake, on the second, third and
// fourth try.
//
// The first message was "still starting, try again in a moment", which is the
// one harmful thing: the speaker's power signal is a TOGGLE, so pressing again
// at a half-woken box switches it back off. That wording is gone.
//
// But the replacement was still a single sentence for every attempt, and a
// sentence that is right the first time is a dead end the fifth. A reporter
// pressed over eighty times across four log generations and read the same line
// every time (2026-09-29). Jens asked for the obvious missing piece: at some
// point STR should stop asking for patience and suggest pulling the plug.
//
// Deliberately NOT a factory reset. That wipes the presets and the Wi-Fi
// credentials, and no software should steer somebody there for a speaker that
// is merely asleep. A power cycle costs ten seconds and loses nothing.

// notReadyCounts holds consecutive failures per speaker, this session only. A
// count that survived a restart would greet somebody with "pull the plug" for a
// speaker that has been fine since yesterday.
const notReadyCounts = new Map();

// noteNotReady records one failed attempt for a speaker and returns how many
// have now failed in a row. An unknown host still counts: one shared bucket is
// better than losing the escalation entirely.
export function noteNotReady(host) {
  const key = String(host || '(unknown)');
  const n = (notReadyCounts.get(key) || 0) + 1;
  notReadyCounts.set(key, n);
  return n;
}

// noteWakeSucceeded clears the run for a speaker. Anything that proves the
// speaker is awake and taking commands resets it, not only a successful preset:
// the next failure after a good play is a first failure, not a fifth.
export function noteWakeSucceeded(host) {
  notReadyCounts.delete(String(host || '(unknown)'));
}

// forgetAllWakeFailures exists for the tests and for a full speaker re-scan.
export function forgetAllWakeFailures() {
  notReadyCounts.clear();
}

// escalationForCount picks the message for the nth consecutive failure.
//
//   1-2  ask for patience, and warn against pressing again
//   3-4  suggest the power cycle, which is what actually clears a speaker
//        wedged in standby
//   5+   say plainly that STR cannot get it out of standby, and point at the
//        diagnostic, because at that point more advice is noise and what is
//        needed is a log
//
// Returning a key rather than a string keeps this testable without the i18n
// bundles and keeps every language in one place.
export function escalationForCount(n) {
  if (!Number.isFinite(n) || n < 1) return 'play.errBoxStarting';
  if (n <= 2) return 'play.errBoxStarting';
  if (n <= 4) return 'play.errBoxStuckTryPower';
  return 'play.errBoxStuckSendLog';
}
