// a11y.js — app-wide accessibility preferences: text size and theme.
//
// Both are UI chrome preferences (like the locale), not per-speaker, so
// they live in localStorage and are applied as classes on <html>. The
// matching token overrides and zoom rules are in style.css
// (html.a11y-light / html.a11y-contrast / html.a11y-scale-*).
//
// applyA11y() must run as early as possible (top of main.js, before the
// app skeleton is rendered) so the chosen theme/size is in effect on the
// first paint with no flash.

const SCALE_KEY = 'a11yScale'; // '1' normal, '2' large, '3' extra large
const THEME_KEY = 'a11yTheme'; // 'dark' | 'light' | 'contrast'

const SCALE_CLASS = { 1: '', 2: 'a11y-scale-l', 3: 'a11y-scale-xl' };
const THEME_CLASS = { dark: '', light: 'a11y-light', contrast: 'a11y-contrast' };

const ALL_CLASSES = [
  'a11y-scale-l', 'a11y-scale-xl', 'a11y-light', 'a11y-contrast',
];

// detectOSTheme picks a sensible theme from the OS accessibility settings
// for users who have never made an explicit choice. "More contrast" wins
// over a light preference; otherwise we honour the light/dark preference,
// defaulting to the app's native dark.
function detectOSTheme() {
  try {
    if (window.matchMedia('(prefers-contrast: more)').matches) return 'contrast';
    if (window.matchMedia('(prefers-color-scheme: light)').matches) return 'light';
  } catch (_) {
    // matchMedia unavailable (old WebView / test): fall through.
  }
  return 'dark';
}

// getScale returns 1, 2 or 3. Invalid/missing values fall back to 1.
export function getScale() {
  try {
    const n = Number(localStorage.getItem(SCALE_KEY));
    if (n === 2 || n === 3) return n;
  } catch (_) { /* ignore */ }
  return 1;
}

// getTheme returns the EFFECTIVE theme: the user's stored choice if any,
// otherwise the OS-derived default. The UI active-state reads this too, so
// the seeded default shows as selected until the user changes it.
export function getTheme() {
  try {
    const v = localStorage.getItem(THEME_KEY);
    if (v === 'dark' || v === 'light' || v === 'contrast') return v;
  } catch (_) { /* ignore */ }
  return detectOSTheme();
}

// applyA11y reflects the current scale + theme onto <html>. Idempotent.
export function applyA11y() {
  try {
    const el = document.documentElement;
    el.classList.remove(...ALL_CLASSES);
    const sc = SCALE_CLASS[getScale()];
    if (sc) el.classList.add(sc);
    const th = THEME_CLASS[getTheme()];
    if (th) el.classList.add(th);
  } catch (_) {
    // no document (test): nothing to apply.
  }
}

export function setScale(n) {
  try { localStorage.setItem(SCALE_KEY, String(n)); } catch (_) { /* ignore */ }
  applyA11y();
}

export function setTheme(theme) {
  try { localStorage.setItem(THEME_KEY, theme); } catch (_) { /* ignore */ }
  applyA11y();
}
