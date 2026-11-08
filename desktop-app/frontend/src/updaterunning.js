// updaterunning.js: the speakers an update from this app is running on right
// now, and the one way the app says so on them (discussion #1764).
//
// While an update runs, the speaker uploads, writes and restarts, sometimes
// twice. The app said so in a small grey line in Speaker Settings, easy to
// miss, so people kept pressing the remote meanwhile. Now every place that
// draws that speaker (the classic speaker picker, the UI 2.0 sidebar, the
// speakers list, the speaker page, the update card in Speaker Settings) marks
// it: bold, in the accent colour, with an activity indicator and the words
// "Update running, the speaker restarts shortly. Please don't use it until
// then."
//
// main.js marks a speaker from the moment its update touches it (a single
// update: at the start; a batch: from its first byte or restart) until its
// row has an outcome, and clears every mark when the run ends. The sets are
// tiny and change a handful of times per update, so every change repaints
// the speaker lists (onUpdateRunningChange).

import { t } from './i18n/index.js';
import { escapeHtml } from './utils.js';

const hosts = new Set();
const listeners = new Set();

function changed() {
  for (const fn of listeners) {
    try { fn(); } catch { /* one list's repaint must not stop the others */ }
  }
}

// setUpdateRunning marks (on) or unmarks host. A call that changes nothing
// repaints nothing.
export function setUpdateRunning(host, on) {
  if (!host) return;
  if (on ? hosts.has(host) : !hosts.has(host)) return;
  if (on) hosts.add(host); else hosts.delete(host);
  changed();
}

// clearUpdateRunning unmarks every speaker (the run is over).
export function clearUpdateRunning() {
  if (!hosts.size) return;
  hosts.clear();
  changed();
}

export function isUpdateRunning(host) {
  return !!host && hosts.has(host);
}

// onUpdateRunningChange registers fn, called after every change. Returns the
// unsubscribe function.
export function onUpdateRunningChange(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

// updateRunningNoticeHTML is the notice over a running update's progress
// line: the activity indicator and the sentence, bold in the accent colour.
// role=status, so a screen reader hears it once when it appears.
export function updateRunningNoticeHTML() {
  return '<div class="update-running" role="status">'
    + '<span class="update-running-spin" aria-hidden="true"></span>'
    + `<span class="update-running-text">${escapeHtml(t('speaker.updateRunningHint'))}</span></div>`;
}

// updateRunningChipHTML is the mark on a speaker tile of the classic speaker
// picker: the indicator and "Update running", with the whole sentence as
// its tooltip.
export function updateRunningChipHTML() {
  const hint = t('speaker.updateRunningHint');
  return `<span class="box-updating" title="${escapeHtml(hint)}">`
    + '<span class="update-running-spin" aria-hidden="true"></span>'
    + `${escapeHtml(t('speaker.updateRunningBadge'))}</span>`;
}
