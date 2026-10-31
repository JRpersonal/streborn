// installhold.js: what the app says about a speaker an update from this app
// is still finishing on.
//
// The backend's install guard (install_guard.go, recentOTAHold) refuses an
// install on such a speaker, because a second writer on a speaker whose new
// agent is still starting up is what left one dark for a week (2026-10-21).
// The guard is the backstop. This module is the part in front of the button:
// InstallHolds reports the held speakers, and the setup picker, the network
// install card, Speaker settings and the speaker list read it from here so a
// held speaker is never presented as "Bose stock firmware, first install".
//
// Field case 2026-10-30 (SoundTouch 20 Series I): the update was accepted,
// the speaker was slow to come back, discovery degraded it to "STR not
// running", and the picker showed it as stock firmware with a "first install"
// badge and "Install STR now" ready. The guard refused the click; the picker
// had already offered it.
//
// Pure: strings and lookups only, so vitest covers it without a DOM or the
// Wails runtime. The record shape is InstallHold from the Go side
// ({ host, sinceSec, remainingSec }).

import { t } from './i18n/index.js';

// holdsByHost turns InstallHolds' list into { host: hold }. Anything that is
// not a held speaker with a host is dropped, so a failed or odd answer reads
// as "nothing held" and never blocks a genuine first install.
export function holdsByHost(list) {
  const out = {};
  for (const h of Array.isArray(list) ? list : []) {
    if (h && typeof h.host === 'string' && h.host) out[h.host] = h;
  }
  return out;
}

// holdFor is the hold on host, or null.
export function holdFor(holds, host) {
  if (!holds || !host) return null;
  return Object.prototype.hasOwnProperty.call(holds, host) ? holds[host] : null;
}

// sameHeldHosts reports whether two hold maps hold the same speakers. The
// remaining time changes on every call; only a speaker entering or leaving
// the hold is worth a re-render.
export function sameHeldHosts(a, b) {
  const ka = Object.keys(a || {}).sort();
  const kb = Object.keys(b || {}).sort();
  return ka.length === kb.length && ka.every((k, i) => k === kb[i]);
}

// holdMinutes is the hold's remaining time in whole minutes for a sentence
// that says "about N minutes": never below two, so no language has to word
// "about 1 minutes" or "about 0 minutes".
export function holdMinutes(hold) {
  const sec = hold && Number.isFinite(hold.remainingSec) ? hold.remainingSec : 0;
  return Math.max(2, Math.ceil(sec / 60));
}

// stockCardText is the kind line and badge of a stock speaker's card in the
// setup picker: "Bose stock firmware" / "first install", or, while an update
// from this app is still finishing on it, what is really going on. Both keep
// the neutral badge style: the amber one drew contrast complaints (#692, #833).
export function stockCardText(hold) {
  if (hold) {
    return {
      kind: t('setup.targetCardKindUpdating'),
      badge: t('setup.targetCardBadgeUpdating'),
    };
  }
  return {
    kind: t('setup.targetCardKindStock'),
    badge: t('setup.targetCardBadgeStock'),
  };
}

// heldInstallNote is the explanation shown above the disabled "Install STR
// now" button of a held speaker: the guard's own refusal, said before the
// click instead of after it.
export function heldInstallNote(hold) {
  return hold ? t('setup.installHeldNote', { min: holdMinutes(hold) }) : '';
}
