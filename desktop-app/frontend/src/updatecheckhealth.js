// Telling somebody that STR has not managed to look for updates in a while.
//
// The manual check already reports its own failure: press the link, get an
// answer. The automatic one at startup stays silent, deliberately, because a
// single failed request is not worth a banner.
//
// But a machine where the request ALWAYS fails, behind an AV proxy, a firewall
// or a broken resolver, looks exactly like "you are up to date" forever. One
// user ran a version from 19 June until 29 September, 142 releases behind, with
// a green "up-to-date" mark on his screen the whole time. That mark was not
// lying: it is about the SPEAKER relative to the installed app, and both were
// equally old. Nothing anywhere was about the app itself.
//
// So: count the consecutive failures, and once there have been enough to rule
// out a bad afternoon, say so once. Quietly, with the way out next to it.

const KEY = 'str.updateCheckFailures';

// SPEAK_AFTER is how many consecutive failed checks earn a word. Three startups
// is comfortably past a flaky router and nowhere near three months.
export const SPEAK_AFTER = 3;

function read() {
  try {
    const n = parseInt(localStorage.getItem(KEY) || '0', 10);
    return Number.isFinite(n) && n > 0 ? n : 0;
  } catch {
    // Private windows and locked-down profiles throw here. Without storage the
    // count cannot survive a restart, so the notice simply never fires, which
    // is the right way to fail: silence, not a false alarm.
    return 0;
  }
}

function write(n) {
  try {
    localStorage.setItem(KEY, String(n));
  } catch { /* see read() */ }
}

// noteCheckFailed records one failed check and returns the new run length.
export function noteCheckFailed() {
  const n = read() + 1;
  write(n);
  return n;
}

// noteCheckSucceeded ends the run. Any answer at all counts, including "you are
// current": the point is whether STR could REACH the question, not what the
// answer was.
export function noteCheckSucceeded() {
  try {
    localStorage.removeItem(KEY);
  } catch { /* see read() */ }
}

// shouldSayChecksAreFailing reports whether the run has gone on long enough to
// be worth a word. True exactly ONCE per threshold crossing, so the notice does
// not reappear on every single startup afterwards and turn into wallpaper.
export function shouldSayChecksAreFailing(n) {
  return Number.isFinite(n) && n === SPEAK_AFTER;
}

// updateCheckFailureRun exposes the current run for tests and for a diagnostic.
export function updateCheckFailureRun() {
  return read();
}
