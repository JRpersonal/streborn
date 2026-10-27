// keyactions.js — pure helpers for the built-in actions a key of one
// speaker's remote can carry (the agent's /api/keyactions):
//   - 'sleep' steps the sleep timer 15 -> 30 -> 60 min -> off (thumbs keys);
//   - 'snooze' snoozes a ringing alarm and does nothing otherwise (thumbs
//     keys and AUX; AUX always switches the input too, which is the silence
//     a ringing alarm needs, so the sleep timer cannot go there);
//   - 'bassUp' / 'bassDown' move the speaker's bass one step per press
//     (discussion #1494; thumbs keys only, for the same reason as the timer).
//
// Document shape: { keys: { thumbsUp: 'sleep', thumbsDown: 'snooze', aux: 'snooze' } }
//
// A thumbs key carries a saved group (groupkeys.js) OR a key action, never
// both; the agent refuses the second binding, and the views say so first.
//
// No DOM access, no imports from the views (the view-extraction trap).

export const SLEEP_KEY_IDS = ['thumbsUp', 'thumbsDown'];
export const SNOOZE_KEY_IDS = ['thumbsUp', 'thumbsDown', 'aux'];
// Every key that can carry some action.
export const KEY_ACTION_KEY_IDS = SNOOZE_KEY_IDS;
export const ACTION_SLEEP = 'sleep';
export const ACTION_SNOOZE = 'snooze';
export const ACTION_BASS_UP = 'bassUp';
export const ACTION_BASS_DOWN = 'bassDown';
// Every known action by its lower-cased name, to its canonical spelling (the
// agent's keyactions normalises the same way).
const CANONICAL = {
  sleep: ACTION_SLEEP, snooze: ACTION_SNOOZE, bassup: ACTION_BASS_UP, bassdown: ACTION_BASS_DOWN,
};

// isBassAction reports whether a is one of the two bass steps.
export function isBassAction(a) {
  return a === ACTION_BASS_UP || a === ACTION_BASS_DOWN;
}

// actionAllowed reports whether action may go on key (the agent's
// keyactions.AllowedOn).
export function actionAllowed(action, key) {
  if (action === ACTION_SLEEP || isBassAction(action)) return SLEEP_KEY_IDS.includes(key);
  if (action === ACTION_SNOOZE) return SNOOZE_KEY_IDS.includes(key);
  return false;
}

// normalizeKeyActions turns whatever the agent (an older one answers 404, or
// nothing) answered into a well-formed document.
export function normalizeKeyActions(raw) {
  const keys = (raw && raw.keys && typeof raw.keys === 'object') ? raw.keys : {};
  const out = { keys: {} };
  for (const k of KEY_ACTION_KEY_IDS) {
    const a = CANONICAL[String(keys[k] || '').trim().toLowerCase()] || '';
    if (actionAllowed(a, k)) out.keys[k] = a;
  }
  return out;
}

// actionOn returns the action key carries, '' for none.
export function actionOn(doc, key) {
  const a = doc && doc.keys && doc.keys[key];
  return a === ACTION_SLEEP || a === ACTION_SNOOZE || isBassAction(a) ? a : '';
}

// bassOn reports whether key carries a bass step.
export function bassOn(doc, key) {
  return isBassAction(actionOn(doc, key));
}

// sleepOn reports whether key carries the sleep timer.
export function sleepOn(doc, key) {
  return actionOn(doc, key) === ACTION_SLEEP;
}

// snoozeOn reports whether key carries snooze.
export function snoozeOn(doc, key) {
  return actionOn(doc, key) === ACTION_SNOOZE;
}

// withAction returns a new document with key carrying action ('' for none).
// An action the key cannot carry leaves the key without one.
export function withAction(doc, key, action) {
  const keys = { ...((doc && doc.keys) || {}) };
  if (action && actionAllowed(action, key)) keys[key] = action;
  else delete keys[key];
  return { keys };
}

// withSleep returns a new document with the sleep timer on key switched on or
// off. Keys other than the thumbs keys are left as they are (never added).
export function withSleep(doc, key, on) {
  return withAction(doc, key, on ? ACTION_SLEEP : '');
}

// sleepBlockedBy returns the name of the saved group that sits on key (so no
// key action can go there), or ''. groupBindings is the group-key document's
// bindings: key id -> template name.
export function sleepBlockedBy(groupBindings, key) {
  const name = groupBindings && groupBindings[key];
  return name ? String(name) : '';
}
