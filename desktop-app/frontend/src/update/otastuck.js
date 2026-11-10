// update/otastuck.js: the OTA loop breaker (#381), extracted from the main.js
// monolith. Remembers per speaker how pushes of this app build ended, so a
// speaker an update cannot stick on is not offered the same push forever.
// noteOTAResult feeds it from a finished run; otaStuck is the verdict.

import { updateNoNetworkHere } from '../darkafterupdate.js';
import { state } from '../state.js';

// --- OTA loop breaker (#381) -------------------------------------------------
// A box whose reported version never changes after a push used to get the
// full push+reboot cycle re-offered forever (the banner re-armed on every
// discovery). Remember per box how pushes of THIS app build ended; after a
// hard "cannot help to retry" classification or two unconfirmed cycles, the
// banner switches to a diagnostic message and re-pushing needs an explicit
// user override. Keyed to the app build so a NEW app version resets the state.
function otaStuckKey(box) {
  return 'otaStuck:' + (box.deviceId || box.host);
}

function readOTAStuck(box) {
  try {
    const rec = JSON.parse(localStorage.getItem(otaStuckKey(box)) || 'null');
    if (!rec || rec.build !== (state.appInfo && state.appInfo.build)) return null;
    return rec;
  } catch { return null; }
}

function noteOTAFailure(box, cls) {
  try {
    const build = state.appInfo && state.appInfo.build;
    const cur = readOTAStuck(box) || { build, count: 0 };
    const rec = { build, count: (cur.count || 0) + 1, cls, at: Date.now() };
    localStorage.setItem(otaStuckKey(box), JSON.stringify(rec));
  } catch {}
}

function clearOTAStuck(box) {
  try { localStorage.removeItem(otaStuckKey(box)); } catch {}
}

// otaStuck returns the record when the box is in the give-up state: one
// "re-pushing the same bytes cannot help" verdict, or 2+ unconfirmed cycles.
export function otaStuck(box) {
  const rec = readOTAStuck(box);
  if (!rec) return null;
  if (rec.cls === 'landed-not-running' || rec.cls === 'swap-failed') return rec;
  if ((rec.count || 0) >= 2) return rec;
  return null;
}

// The update sequence itself lives in Go: desktop-app/otaorchestrate.go
// (RunSpeakerUpdate for one speaker, RunFleetUpdate for the house). It pushes
// the agent, polls the version, holds the stability window, classifies a miss,
// retries the one verdict a retry fixes, puts the Spotify engine back, and in
// a batch serialises the byte pushes through its upload gate. This file keeps
// what only a window can do: the pre-checks and their dialogs, the mapping of
// a phase to a sentence, and the rendering of the box:update:phase events.
//
// noteOTAResult feeds the loop breaker from a finished run: a confirmed agent
// clears the record, an unconfirmed one counts with the classifier's verdict.
// Only the result carries both, which is why this reads the result and not a
// phase event.
export function noteOTAResult(box, result) {
  if (!result) return;
  if (result.confirmed) { clearOTAStuck(box); return; }
  // The speaker changed its address mid-update: the update most likely
  // worked, so this is no evidence of an update that cannot stick.
  if (result.unmet === 'address-changed') return;
  // This computer had no network when the window ran out: the speaker was
  // never judged, so this is no evidence against it either.
  if (updateNoNetworkHere(result)) return;
  if (result.outcome === 'timeout' || result.outcome === 'agentGone' || result.outcome === 'boxInSetup') {
    noteOTAFailure(box, result.verdict || '');
  }
}
