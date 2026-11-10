// format.js: the small text formats several views share (a m:ss clock, "how
// long ago", a transfer rate). No DOM and no state, so it imports anywhere.

import { getLocale } from './i18n/index.js';

// formatClock renders whole seconds as m:ss ("3:07", "72:00"). A fraction is
// cut off and a negative value reads 0:00, so a caller that wants rounding
// (elapsed time rounds, a countdown rounds up) does it before the call.
export function formatClock(sec) {
  const total = Math.max(0, Math.floor(Number(sec) || 0));
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`;
}

// agoText says how long ago something was last seen, in the app's language,
// via Intl.RelativeTimeFormat, so the phrase localizes itself ("vor 5 Minuten"
// / "5 minutes ago") without per-language time strings. Never less than one
// minute; hours from one hour on, days from one day on.
export function agoText(sec, locale = getLocale()) {
  const s = Math.max(0, Number(sec) || 0);
  try {
    const rtf = new Intl.RelativeTimeFormat(locale || 'en', { numeric: 'auto' });
    if (s < 3600) return rtf.format(-Math.max(1, Math.round(s / 60)), 'minute');
    if (s < 86400) return rtf.format(-Math.round(s / 3600), 'hour');
    return rtf.format(-Math.round(s / 86400), 'day');
  } catch {
    return `${Math.max(1, Math.round(s / 60))} min`;
  }
}

// fmtRate turns a bytes/second number into a short human rate for the live
// download/upload throughput shown during an app update or a speaker update.
export function fmtRate(bps) {
  bps = Number(bps) || 0;
  if (bps >= 1024 * 1024) return (bps / 1048576).toFixed(1) + ' MB/s';
  if (bps >= 1024) return Math.round(bps / 1024) + ' KB/s';
  return Math.max(0, Math.round(bps)) + ' B/s';
}
