// wificopy.js: STR keeps its own copy of a speaker's Wi-Fi by itself.
//
// A coprocessor speaker (scm SoundTouch 20/30, a Portable that keeps no
// profile) reports wlanCredsMissing when STR has no copy of its Wi-Fi. That
// copy is what the speaker's offline rescue uses after a power cut. The app
// used to show a banner with a "Save Wi-Fi" button for it, which read like a
// required action on a speaker that worked fine, and its button ran the full
// Wi-Fi change, which restarts these speakers (maintainer's own SoundTouch 20,
// 2026-11-15).
//
// Now the app keeps the copy itself: KeepSpeakerWiFiCopy (desktop-app
// wificopy.go) stores the computer's current Wi-Fi with its saved password,
// store-only, when the speaker is in the subnet of that Wi-Fi. That runs at the
// end of an install and whenever the app sees a speaker without a copy, at most
// once per WIFI_COPY_RETRY_MS per speaker. Only when it cannot (macOS, no saved
// password, the computer on a cable, an agent too old, the speaker naming
// another network) does the speaker's Network settings show a quiet note with a
// button. No banner, no attention item.
//
// Pure helpers first (tested in wificopy.test.js); the runner takes its
// binding as an argument so the tests need no backend.

import { state } from './state.js';

// How long a speaker is left alone after an attempt that did not store a copy.
// The usual reasons (no password on this computer, the computer on a cable)
// do not change by the minute, and every attempt reads the saved password.
export const WIFI_COPY_RETRY_MS = 30 * 60 * 1000;

const attempts = () => {
  if (!state.wifiCopy) state.wifiCopy = {};
  return state.wifiCopy;
};

// wifiCopyCandidates: the speakers the app should try to keep a copy for now.
// A speaker without a copy, answering, not tried within the retry window and
// not being tried right now.
export function wifiCopyCandidates(boxes, tried = attempts(), now = Date.now()) {
  return (boxes || []).filter((b) => {
    if (!b || !b.host || !b.wlanCredsMissing || b.offline || b.unreachable) return false;
    const a = tried[b.host];
    if (!a) return true;
    if (a.pending) return false;
    if (a.stored) return false;
    return now - (a.at || 0) >= WIFI_COPY_RETRY_MS;
  });
}

// noteWifiCopyResult records an outcome for a host.
export function noteWifiCopyResult(host, res, now = Date.now(), tried = attempts()) {
  const r = res || {};
  tried[host] = { at: now, stored: !!r.stored, reason: r.reason || '', note: r.note !== false && !r.stored, pending: false };
  return tried[host];
}

// wifiCopyNoteShows: whether the speaker's Network settings show the note.
// Only for a speaker without a copy, once the app has tried and could not keep
// one (or the owner's own try failed); not while an attempt is running, and
// not after a stored copy, which the next discovery confirms.
export function wifiCopyNoteShows(box, tried = attempts()) {
  if (!box || !box.wlanCredsMissing) return false;
  const a = tried[box.host];
  if (!a) return false;
  if (a.pending || a.stored) return false;
  return a.note !== false;
}

// keepWifiCopy runs one attempt for a box through keep (the binding), records
// it, and clears the box's flag on a stored copy so the note never shows for
// it. Never throws: a binding missing from an older build counts as "cannot".
export async function keepWifiCopy(box, trigger, keep, tried = attempts()) {
  if (!box || !box.host) return null;
  tried[box.host] = { ...(tried[box.host] || {}), pending: true, at: Date.now() };
  let res;
  try {
    res = await keep(box.host, box.port || 0, trigger);
  } catch {
    res = { stored: false, reason: 'binding-missing', note: true };
  }
  const rec = noteWifiCopyResult(box.host, res, Date.now(), tried);
  if (rec.stored) {
    for (const b of state.boxes || []) {
      if (b && b.host === box.host) b.wlanCredsMissing = false;
    }
    box.wlanCredsMissing = false;
  }
  return rec;
}

// paintWifiCopyNote shows or hides the note in the Network settings on screen
// (views/settings/network.js renders it hidden, with the speaker's host), so a
// round that finishes while the page is open needs no full redraw.
export function paintWifiCopyNote(doc = (typeof document !== 'undefined' ? document : null)) {
  const el = doc && doc.getElementById ? doc.getElementById('wlanCopyNote') : null;
  if (!el) return;
  const host = el.getAttribute('data-host') || '';
  const box = (state.boxes || []).find((b) => b && b.host === host)
    || (state.settingsBox && state.settingsBox.host === host ? state.settingsBox : null);
  el.hidden = !wifiCopyNoteShows(box);
}

// keepWifiCopies tries every candidate, one speaker at a time (each attempt
// reads the computer's Wi-Fi and its saved password). onDone runs once after
// the round when anything changed, so a settings page on screen can redraw.
export async function keepWifiCopies(boxes, keep, onDone) {
  const todo = wifiCopyCandidates(boxes);
  if (!todo.length) return 0;
  for (const b of todo) attempts()[b.host] = { ...(attempts()[b.host] || {}), pending: true, at: Date.now() };
  for (const b of todo) await keepWifiCopy(b, 'seen', keep);
  if (typeof onDone === 'function') {
    try { onDone(); } catch { /* a redraw must not break the round */ }
  }
  return todo.length;
}
