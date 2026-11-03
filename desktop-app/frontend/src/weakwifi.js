// weakwifi.js: "playback dropped out while the Wi-Fi signal at this speaker
// was weak".
//
// About fourteen owners wrote in during October 2026 about music that kept
// dropping out, and the cause was poor reception at the speaker every time.
// The app graded the signal only before an update and during setup, so
// nothing tied a dropout to it. The agent now reads the speaker's live Wi-Fi
// grade whenever playback drops out (a station breaking off mid-stream, the
// speaker letting go of a Spotify stream) and, when the grade is MARGINAL or
// POOR, reports it for an hour in /api/agent/version as weakWifiDropout, with
// weakWifiDropoutAgeSec (internal/webui/weakwifidropout.go).
//
// The hint is informational: the speaker plays, just not reliably. So it can
// be dismissed per speaker, and a dismissal holds for a day rather than until
// the next dropout, which on a speaker with poor reception would be minutes.

import { boxCannotReachInternet } from './nointernet.js';

// The two grades the update pre-flight also calls weak (main.js).
const WEAK = new Set(['MARGINAL_SIGNAL', 'POOR_SIGNAL']);

// WEAK_WIFI_DISMISS_MS is how long "Dismiss" keeps the hint away.
export const WEAK_WIFI_DISMISS_MS = 24 * 60 * 60 * 1000;

// boxHasWeakWifiDropout: the agent saw a recent dropout on a weak signal. A
// speaker the app cannot reach (offline tile) or a stock box never carries a
// fresh marker. A speaker without internet is left to its own, stronger hint:
// there the station cannot play at all, whatever the signal.
export function boxHasWeakWifiDropout(box) {
  return !!(box && WEAK.has(box.weakWifiDropout) && !box.offline && box.kind !== 'stock'
    && box.boxHealth !== 'wedged' && !boxCannotReachInternet(box));
}

// weakWifiDismissActive: a dismissal stamp (ms, from localStorage; 0 or NaN
// when none) still holds at now.
export function weakWifiDismissActive(stamp, now = Date.now()) {
  const at = Number(stamp) || 0;
  return at > 0 && now - at < WEAK_WIFI_DISMISS_MS;
}

// weakWifiDropoutBoxes lists the speakers to name. dismissedAt(box) returns
// that speaker's dismissal stamp.
export function weakWifiDropoutBoxes(boxes, dismissedAt = () => 0, now = Date.now()) {
  return (boxes || []).filter(b => boxHasWeakWifiDropout(b) && !weakWifiDismissActive(dismissedAt(b), now));
}
