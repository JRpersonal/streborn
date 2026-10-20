// keyactions.js — pure helpers for the built-in actions a thumbs key of one
// speaker's remote can carry (the agent's /api/keyactions). The only action
// so far is the sleep timer: every press steps it 15 -> 30 -> 60 min -> off.
//
// Document shape: { keys: { thumbsUp: 'sleep', thumbsDown: 'sleep' } }
//
// A thumbs key carries a saved group (groupkeys.js) OR the sleep timer, never
// both; the agent refuses the second binding, and the views say so first.
//
// No DOM access, no imports from the views (the view-extraction trap).

export const SLEEP_KEY_IDS = ['thumbsUp', 'thumbsDown'];
export const ACTION_SLEEP = 'sleep';

// normalizeKeyActions turns whatever the agent (an older one answers 404, or
// nothing) answered into a well-formed document.
export function normalizeKeyActions(raw) {
  const keys = (raw && raw.keys && typeof raw.keys === 'object') ? raw.keys : {};
  const out = { keys: {} };
  for (const k of SLEEP_KEY_IDS) {
    if (String(keys[k] || '').trim().toLowerCase() === ACTION_SLEEP) out.keys[k] = ACTION_SLEEP;
  }
  return out;
}

// sleepOn reports whether key carries the sleep timer.
export function sleepOn(doc, key) {
  return !!(doc && doc.keys && doc.keys[key] === ACTION_SLEEP);
}

// withSleep returns a new document with the sleep timer on key switched on or
// off. Keys other than the thumbs keys are left as they are (never added).
export function withSleep(doc, key, on) {
  const keys = { ...((doc && doc.keys) || {}) };
  if (on && SLEEP_KEY_IDS.includes(key)) keys[key] = ACTION_SLEEP;
  else delete keys[key];
  return { keys };
}

// sleepBlockedBy returns the name of the saved group that sits on key (so the
// sleep timer cannot go there), or ''. groupBindings is the group-key
// document's bindings: key id -> template name.
export function sleepBlockedBy(groupBindings, key) {
  const name = groupBindings && groupBindings[key];
  return name ? String(name) : '';
}
