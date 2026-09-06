// speakerPurge.js — forget everything the frontend keeps about ONE speaker.
//
// After STR is removed from a speaker (Settings > Remove STR, Go side
// UninstallSTR + purgeSpeakerState) the speaker is a plain stock box again,
// but this side of the app kept remembering it as the STR speaker it used to
// be: the cached speaker list painted at the next launch, the "last selected"
// id, dismissed warnings, the OTA give-up record, the per-box world-map
// invite flag, and a handful of session maps (zone state, pending rename,
// group edits, source visibility, update supervision). Each of those made the
// speaker come back as something other than what it now is.
//
// purgeSpeakerLocalState is the one place that knows the whole list. The Go
// side has the same single place for its own records (uninstall_purge.go).
//
// main.js owns several of the session maps in its module scope and cannot be
// imported from here (it imports the views, which import this), so it
// registers a hook via onSpeakerPurge at load and clears them itself.
//
// Written defensively on purpose: a box object may carry no deviceID (a stock
// speaker never had one), localStorage may be missing or throw, and any state
// field may be absent. A purge must never throw into the uninstall handler.

import { loadCachedBoxes, saveCachedBoxes } from './state.js';
import { forgetPairNamesFor } from './stereoNames.js';

// Keys main.js writes per speaker (see warnDismissKey, otaStuckKey,
// WORLD_MAP_PROVISIONED_PREFIX and inviteWorldMapAfterProvision there).
const WARN_DISMISS_PREFIX = 'warnDismiss:';
const WARN_DISMISS_TYPES = ['ssh', 'conflict', 'nowifi'];
const OTA_STUCK_PREFIX = 'otaStuck:';
const WORLD_MAP_PROVISIONED_PREFIX = 'str.worldMapProvisioned.';
const LAST_BOX_KEY = 'lastBoxDeviceID';
const CACHED_BOXES_KEY = 'cachedBoxes';

const purgeHooks = new Set();

// onSpeakerPurge registers a function called with {host, deviceID, ids} on
// every purge, for module-scoped state this module cannot reach. Idempotent
// per function.
export function onSpeakerPurge(fn) {
  if (typeof fn === 'function') purgeHooks.add(fn);
}

function upper(v) {
  return String(v == null ? '' : v).trim().toUpperCase();
}

// speakerIdentity collects every spelling the app has keyed a record on for
// this box: host, deviceID (either casing), and the id/mac fallbacks the
// world-map flag uses. ids is de-duplicated and never contains ''.
export function speakerIdentity(box) {
  const b = box || {};
  const host = String(b.host || '').trim();
  const deviceID = String(b.deviceID || b.deviceId || '').trim();
  const ids = [];
  for (const v of [deviceID, b.deviceID, b.deviceId, b.id, b.mac, host]) {
    const s = String(v == null ? '' : v).trim();
    if (s && !ids.includes(s)) ids.push(s);
  }
  return { host, deviceID, ids };
}

// sameSpeaker reports whether another box record is this speaker: same host
// or same deviceID (case-insensitive). A box with neither is never a match.
function sameSpeaker(other, host, deviceID) {
  if (!other) return false;
  if (host && String(other.host || '').trim() === host) return true;
  const oid = upper(other.deviceID || other.deviceId);
  return !!(deviceID && oid && oid === upper(deviceID));
}

function lsGet(key) {
  try { return localStorage.getItem(key); } catch { return null; }
}
function lsRemove(key) {
  try { localStorage.removeItem(key); } catch {}
}

// lsKeys lists every stored key when the storage supports enumeration (the
// real one does; a minimal test double may not), else [].
function lsKeys() {
  const out = [];
  try {
    const n = Number(localStorage.length) || 0;
    for (let i = 0; i < n; i++) {
      const k = localStorage.key(i);
      if (typeof k === 'string') out.push(k);
    }
  } catch {}
  return out;
}

function purgeLocalStorage(host, deviceID, ids) {
  // The speaker list painted at the next launch.
  if (lsGet(CACHED_BOXES_KEY) != null) {
    try {
      const before = loadCachedBoxes();
      const after = before.filter(b => !sameSpeaker(b, host, deviceID));
      if (after.length !== before.length) saveCachedBoxes(after);
    } catch {}
  }
  // The remembered selection, only when it is this speaker.
  const last = lsGet(LAST_BOX_KEY);
  if (last && ids.some(id => upper(id) === upper(last))) lsRemove(LAST_BOX_KEY);
  // Per-speaker keys, under every id spelling. The known warn types are
  // removed by name so a storage without enumeration is still covered; the
  // scan below also catches a type added later.
  for (const id of ids) {
    for (const type of WARN_DISMISS_TYPES) lsRemove(WARN_DISMISS_PREFIX + id + ':' + type);
    lsRemove(OTA_STUCK_PREFIX + id);
    lsRemove(WORLD_MAP_PROVISIONED_PREFIX + id);
  }
  for (const key of lsKeys()) {
    for (const id of ids) {
      if (key.startsWith(WARN_DISMISS_PREFIX + id + ':')) lsRemove(key);
    }
  }
}

// deleteKeysMatching removes every own key of obj that equals one of ids,
// case-insensitively (zoneLive keys are deviceIDs as the box reported them,
// pendingNames keys are deviceIDs as discovery reported them).
function deleteKeysMatching(obj, ids) {
  if (!obj || typeof obj !== 'object') return;
  const wanted = ids.map(upper);
  for (const k of Object.keys(obj)) {
    if (wanted.includes(upper(k))) delete obj[k];
  }
}

function purgeState(state, host, deviceID, ids) {
  if (!state || typeof state !== 'object') return;
  // Per-speaker maps keyed by deviceID (or host).
  for (const name of ['pendingNames', 'zoneLive', 'zoneSlaves', 'zoneMaster', 'boxPlaying']) {
    deleteKeysMatching(state[name], ids);
  }
  // Selections that point at this speaker. state.boxes is left alone: the
  // list is discovery's, and the re-scan after the reboot repaints it.
  if (sameSpeaker(state.currentBox, host, deviceID)) state.currentBox = null;
  if (sameSpeaker(state.settingsBox, host, deviceID)) state.settingsBox = null;
  if (state.setupTarget && sameSpeaker(state.setupTarget.box, host, deviceID)) state.setupTarget = null;
  if (host && state.otaTargetHost === host) state.otaTargetHost = null;
  // recentBoxKey is a deviceID or "host:port".
  const rk = String(state.recentBoxKey || '');
  if (rk && ids.some(id => upper(rk) === upper(id) || rk.startsWith(id + ':'))) state.recentBoxKey = null;
  // The stereo-balance stamp names the pair's members.
  const bal = String(state.lastBalancePair || '');
  if (bal && deviceID && upper(bal).includes(upper(deviceID))) state.lastBalancePair = null;
}

// purgeSpeakerLocalState forgets one speaker everywhere on this side of the
// app: localStorage, the shared state object, the stereo-name cache, and
// whatever main.js registered via onSpeakerPurge. Returns the identity it
// worked with (useful in tests and logs). Never throws.
export function purgeSpeakerLocalState(box, state) {
  const identity = speakerIdentity(box);
  const { host, deviceID, ids } = identity;
  if (!ids.length) return identity;
  try { purgeLocalStorage(host, deviceID, ids); } catch {}
  try { purgeState(state, host, deviceID, ids); } catch {}
  try { forgetPairNamesFor(deviceID); } catch {}
  for (const fn of purgeHooks) {
    try { fn({ host, deviceID, ids }); } catch {}
  }
  return identity;
}
