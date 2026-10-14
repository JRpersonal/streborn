// nointernet.js: "this speaker cannot reach the internet".
//
// The agent keeps a passive verdict on whether its speaker reaches the
// internet and reports it in /api/agent/version as internet:
// "ok" | "unreachable" | "unknown" (internetReason: dns, timeout, route,
// connect). Field case 2026-10-14: a router had cut five speakers off the
// internet while the LAN kept working, the app found and controlled every one
// of them, and the only symptom was a station that "stops after a few
// seconds". The owner rebooted speakers for two hours. The app reaching the
// speaker proves its Wi-Fi works, so the useful advice is the router.

// boxCannotReachInternet: the agent has positive evidence this speaker has no
// internet. A speaker the app cannot reach itself (offline tile) or a stock
// box is never flagged: the verdict would be stale or absent.
export function boxCannotReachInternet(box) {
  return !!(box && box.internet === 'unreachable' && !box.offline && box.kind !== 'stock');
}

// noInternetBoxes lists the speakers to name in the banner.
export function noInternetBoxes(boxes) {
  return (boxes || []).filter(boxCannotReachInternet);
}

// playFailedForNoInternet: a station that failed on this speaker failed
// because the speaker has no internet, not because of the station. Either the
// agent's stream-status said so ("offline"), or the failure was a plain
// network one ("unreachable") on a speaker whose verdict is already
// "unreachable". A clear 403/404/HLS answer from the station is never
// reworded: the station answered, so the internet works.
export function playFailedForNoInternet(fail, box) {
  if (!fail) return false;
  if (fail.reason === 'offline') return true;
  return fail.reason === 'unreachable' && boxCannotReachInternet(box);
}

// noInternetText is the one-sentence title plus the router hint, for a toast
// or a tooltip. t is the i18n lookup.
export function noInternetText(t) {
  return t('speaker.noInternetTitle') + ' ' + t('speaker.noInternetHint');
}
