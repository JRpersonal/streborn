// uipreview.js: the switch for the UI 2.0 preview (#1285, docs/UI-2.0.md).
//
// The new layout ships next to the current one, OFF by default, and is turned
// on from the "Aa" menu. Like the text size and the theme it is a per-viewer
// chrome preference, so it lives in localStorage. Only the exact value '1'
// reads as on: a missing key, a cleared store or anything unexpected keeps the
// layout everybody knows.
//
// No top-level access to localStorage, document or navigator: vitest imports
// this module in a plain Node environment, and a WebView that refuses storage
// (a private window, blocked site data) must not stop the app from booting.
// When storage throws, the choice is kept in memory for this session only.

const KEY = 'str.ui2Preview';
let memory = null;

function store() {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage;
  } catch (_) {
    return null;
  }
}

// getUi2Preview reports whether the UI 2.0 preview is switched on.
export function getUi2Preview() {
  const s = store();
  if (s) {
    try {
      return s.getItem(KEY) === '1';
    } catch (_) { /* fall through to the in-memory value */ }
  }
  return memory === '1';
}

// setUi2Preview stores the choice. The caller reloads the window afterwards,
// the same as the language switch: the shell is built once at boot.
export function setUi2Preview(on) {
  const v = on ? '1' : '0';
  memory = v;
  const s = store();
  if (!s) return;
  try { s.setItem(KEY, v); } catch (_) { /* kept in memory only */ }
}
