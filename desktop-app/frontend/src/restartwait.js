// restartwait.js remembers which speakers the app has just asked to restart,
// so the restart button stays "Restarting…" until the speaker is back instead
// of offering itself again while the speaker is still booting.
//
// Learned from #1768: the sign-in warning's restart button re-enabled the
// moment RebootBox returned, the warning stayed up (the speaker had not gone
// down yet), and 79 s later a second press hit a speaker still in the middle
// of its boot. Both of its ports refused, and the app answered with advice to
// check the firewall and antivirus, for a speaker that was doing exactly what
// it had been asked to.
//
// A restart counts as pending from the moment the speaker took the request
// until either the speaker has been seen again after it (it was missing from
// a refresh and then answered one), or RESTART_WAIT_MS has passed. The window
// matches the Go side's (restartWindow in app_restarting.go), which swaps the
// firewall advice for "still restarting" on a failed connection inside it.
//
// Pure apart from the module-level tracker the app uses; tests build their
// own with a fake clock.

export const RESTART_WAIT_MS = 4 * 60 * 1000;

export function createRestartTracker(now = () => Date.now()) {
  // host -> { at, sawDown }
  const pending = new Map();
  const expired = (r) => now() - r.at >= RESTART_WAIT_MS;
  return {
    // requested records that host accepted a restart request just now.
    requested(host) {
      if (host) pending.set(host, { at: now(), sawDown: false });
    },
    // observe takes one refresh's speaker list. A speaker missing from the
    // refresh (unreachable or offline) is going through its restart; the
    // first refresh it answers again after that ends the wait.
    observe(boxes) {
      for (const b of boxes || []) {
        if (!b || !pending.has(b.host)) continue;
        const r = pending.get(b.host);
        if (b.unreachable || b.offline) r.sawDown = true;
        else if (r.sawDown) pending.delete(b.host);
      }
    },
    // isPending reports whether host is still inside its restart.
    isPending(host) {
      const r = pending.get(host);
      if (!r) return false;
      if (expired(r)) {
        pending.delete(host);
        return false;
      }
      return true;
    },
    // remainingMs is how long the wait for host can still last at most, 0
    // when nothing is pending: the caller re-renders then.
    remainingMs(host) {
      const r = pending.get(host);
      if (!r || expired(r)) return 0;
      return RESTART_WAIT_MS - (now() - r.at);
    },
    // sig changes whenever the set of pending speakers does, for the views
    // that repaint only when their signature changes.
    sig() {
      return [...pending.keys()].filter((h) => this.isPending(h)).sort().join(',');
    },
  };
}

export const restarts = createRestartTracker();

// The Go side's stillRestartingAdvice, word for word: an error from inside the
// window ends on it, in English like every advice paragraph the app sends up.
export const STILL_RESTARTING_EN = 'This speaker is still restarting, give it a few minutes.';

// localizeStillRestarting swaps that paragraph for the user's own language,
// where the restart buttons show an error; any other text is left as it is.
export function localizeStillRestarting(msg, localized) {
  const s = String(msg == null ? '' : msg);
  return localized && s.includes(STILL_RESTARTING_EN) ? s.replace(STILL_RESTARTING_EN, localized) : s;
}
