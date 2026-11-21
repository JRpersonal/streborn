// uipreview.js: the switch between the UI 2.0 layout and the classic one
// (#1285, docs/UI-2.0.md).
//
// UI 2.0 is the default since phase 6 (#1290). The classic layout stays
// selectable from the "Aa" menu for one or two releases and is then removed.
// Like the text size and the theme it is a per-viewer chrome preference, so it
// lives in localStorage. Only the exact value '0' reads as classic: that is
// what a person who picked it (in the preview days or since) has stored, and
// their choice stands. A missing key, a cleared store or anything unexpected
// gets the default.
//
// No top-level access to localStorage, document or navigator: vitest imports
// this module in a plain Node environment, and a WebView that refuses storage
// (a private window, blocked site data) must not stop the app from booting.
// When storage throws, the choice is kept in memory for this session only, and
// the Aa menu offers no switch (canKeepUi2Preview): the reload a switch needs
// would come back without it.

const KEY = 'str.ui2Preview';
let memory = null;

function store() {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage;
  } catch (_) {
    return null;
  }
}

// getUi2Preview reports whether the UI 2.0 layout is on (the default).
export function getUi2Preview() {
  const s = store();
  if (s) {
    try {
      return s.getItem(KEY) !== '0';
    } catch (_) { /* fall through to the in-memory value */ }
  }
  return memory !== '0';
}

// canKeepUi2Preview reports whether a choice survives the reload the switch
// does. Read-only on purpose: the menu asks at boot, possibly before the
// WebView has restored the store, and a probe write then could clobber it.
export function canKeepUi2Preview() {
  const s = store();
  if (!s) return false;
  try {
    s.getItem(KEY);
    return true;
  } catch (_) {
    return false;
  }
}

// setUi2Preview stores the choice and reports whether it reached the store.
// The caller reloads the window afterwards, the same as the language switch:
// the shell is built once at boot. On false it must not reload, the choice
// would be gone again.
export function setUi2Preview(on) {
  const v = on ? '1' : '0';
  memory = v;
  const s = store();
  if (!s) return false;
  try {
    s.setItem(KEY, v);
    return true;
  } catch (_) {
    return false; // kept in memory only
  }
}
