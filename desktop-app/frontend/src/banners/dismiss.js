// banners/dismiss.js: the per-speaker, per-warning dismissal the banners keep
// in localStorage ("warnDismiss:<speaker>:<type>"). Shared by the SSH reminder,
// the speaker issue banner and the UI 2.0 attention notes. Extracted from the
// main.js monolith.

export function warnDismissKey(box, type) {
  return 'warnDismiss:' + (box.deviceId || box.host) + ':' + type;
}

export function warnDismissed(box, type) {
  try { return !!localStorage.getItem(warnDismissKey(box, type)); } catch { return false; }
}
