// agentsettle.js holds the two questions both the install and the update ask
// before they hand a speaker the ~16 MB Spotify engine: is the agent the app
// pushed actually the one running, and has it settled enough to take a large
// write. It used to live in main.js, where only the update could reach it, so
// the install pushed the engine the moment the speaker reported it missing and
// treated the first "no space" answer as final (#994).
//
// Everything here takes its I/O as arguments, so the decisions can be tested
// without a speaker.
import { sleep as realSleep } from './utils.js';

// The engine push refused for lack of room. The agent says "insufficient
// nand", the transport says "no space" or carries the 507 status.
export const SPACE_REFUSAL = /insufficient nand|no space|507/i;

// speakerReachedTarget answers the only question that matters at the end of an
// install or an update: is this speaker actually where it was meant to be.
//
// It exists because judging on one half is how a run lies. A speaker whose
// Spotify engine survived the reboot reports the engine present within seconds,
// while its agent is still being replaced, and anything that stops looking at
// that moment declares success on the old software. The mirror case is just as
// real: the agent lands and the engine is still missing. Both halves, always,
// and the caller is told WHICH half is outstanding so it can say so rather than
// showing a spinner with no explanation.
//
// Learned from a fleet run on 2026-07-29 where a Portable passed on the old
// build and only finished minutes later. Users hit the same on slow speakers.
//
// appSha is the hash of the agent binary this app carries (appInfo.agentSha256).
export function speakerReachedTarget(live, preVersion, wantEngine, appSha) {
  if (!live) return { done: false, missing: 'unreachable' };
  // The binary the speaker is RUNNING first, then the version string.
  //
  // "the version string moved" cannot be satisfied by a push of the SAME
  // version, and such a push is not harmless: a speaker that has the Spotify
  // engine is by definition NAND-tight, so the agent drops the engine to make
  // the update fit. Judging by the string then returns before the engine is
  // put back, and a working Spotify install is destroyed by a re-push that was
  // meant to change nothing. Four of six speakers in one household ended up
  // that way after the v0.9.88 stamp skew invited re-push after re-push.
  const agentDone = (!!appSha && live.agentRunningSha256 === appSha) ||
    (!!live.version && (!preVersion || live.version !== preVersion));
  const engineDone = !wantEngine || live.goLibrespot === 'present';
  if (!agentDone) return { done: false, missing: 'agent' };
  if (!engineDone) return { done: false, missing: 'engine' };
  return { done: true, missing: '' };
}

// installAgentOnTarget is the install's version of the agent half. A first
// install has no previous version to compare against, so speakerReachedTarget
// alone accepts any agent that answers, including an older STR agent still
// running on a speaker that is being reinstalled. When both sides know the
// binary hash, the hash decides; without it (an agent too old to report one,
// or a dev build with no embedded agent) the old "any version" rule stands.
export function installAgentOnTarget(live, appSha) {
  if (!live || !live.version) return false;
  if (appSha && live.agentRunningSha256) return live.agentRunningSha256 === appSha;
  return speakerReachedTarget(live, '', false, appSha).done;
}

// waitForStableAgent waits (bounded by deadlineMs) until the box's agent
// answers again and KEEPS answering for stableMs, so the next engine attempt
// streams at a settled box instead of one mid-reboot. probe() is one
// BoxAgentVersion call for that speaker.
//
// Agents that report their box uptime (uptimeSec, v0.9.20+) get two extra
// gates, learned from the #466 bundles where the FIRST post-confirm 16 MB push
// reliably died with a connection reset ~107 s in while a retry minutes later
// sailed through in ~15 s: the box must be past the reboot-prone post-OTA
// settling window (uptime >= minUptimeSec), and an uptime DROP between two
// probes is a reboot that plain reachability polling misses entirely (the box
// can be back up before the next probe) - it resets the stability clock.
// Older agents without uptimeSec keep the reachability-only behavior.
export async function waitForStableAgent(probe, deadlineMs, stableMs = 30_000, minUptimeSec = 150,
  { sleep = realSleep, now = Date.now } = {}) {
  let up = 0;
  let lastUptime = -1;
  while (now() < deadlineMs) {
    await sleep(3_000);
    try {
      const v = await probe();
      const uptime = v && v.uptimeSec ? parseInt(v.uptimeSec, 10) : NaN;
      if (!Number.isNaN(uptime)) {
        if (uptime < lastUptime) up = 0; // rebooted between probes
        lastUptime = uptime;
        if (uptime < minUptimeSec) continue; // still in the settling window
      }
      if (!up) up = now();
      if (now() - up >= stableMs) return true;
    } catch { up = 0; lastUptime = -1; }
  }
  return false;
}

// verifyInstalledLoop holds an install open until the speaker really is there,
// and delivers the Spotify engine inside that window because nothing is allowed
// to deliver it afterwards in the background.
//
// deps: probe() -> BoxAgentVersion answer, ensureEngine() -> EnsureSpotifyEngine
// result, waitStable(deadlineMs) -> waitForStableAgent bound to the speaker,
// appSha, onState, and sleep/now for tests.
//
// The engine is pushed only at an agent that is the build this app carries,
// and it goes out at once: an install that fits pays no extra wait. A space
// refusal is not final, though. A freshly installed agent can still be tidying
// up behind itself when the first push lands, so only then does the loop wait
// for the agent to settle (the update's own gate) and ask once more. A second
// refusal, or the install window running out, reports the speaker as installed
// without Spotify.
export async function verifyInstalledLoop({
  probe, ensureEngine, waitStable, appSha = '', onState,
  sleep = realSleep, now = Date.now,
  windowMs = 600_000, liveWindowMs = 300_000, hardMs = 1_800_000,
}) {
  // Ten minutes is the budget for a speaker that comes back promptly. It is not
  // the budget for one that does not: a donor's SoundTouch 20 took just over
  // twenty minutes to return after its update, the window closed at ten, and
  // everything the app said after that was about a speaker it had stopped
  // listening to. So the clock is extended each time the speaker proves it is
  // still working on it, up to a hard ceiling.
  const hardStop = now() + hardMs;
  let deadline = now() + windowMs;
  let attempt = 0;
  let lastLive = null;
  let spaceRefusals = 0;
  let lastRefusal = '';
  while (now() < deadline && now() < hardStop) {
    attempt++;
    let live = null;
    try { live = await probe(); } catch {}
    if (live && live.version) {
      lastLive = live;
      // Alive and answering: whatever is left to do (the engine) is worth a
      // fresh window rather than the remains of the old one.
      deadline = Math.min(hardStop, now() + liveWindowMs);
    }
    if (onState) onState({ attempt, reachable: !!live, remainingMs: deadline - now(),
      version: (live && live.version) || '', engine: (live && live.goLibrespot) || 'unknown' });
    // Both halves, never one: a speaker can report the engine present while
    // its agent is still being written, and stopping there declares success
    // on software that is not installed yet (fleet run 2026-07-29). A first
    // install has no previous version to compare against, so any reported
    // version means the agent is up.
    if (live && live.goLibrespot === 'present' && live.version) return { ok: true, version: live };
    if (live && live.goLibrespot === 'missing' && installAgentOnTarget(live, appSha)) {
      try {
        const r = await ensureEngine();
        // Nothing to deliver in this build: the speaker is as finished as it
        // can get, so do not wait out the window for an impossibility.
        if (r && /no embedded engine/i.test(r)) return { ok: true, version: live };
      } catch (e) {
        const m = String((e && e.message) || e || '');
        if (SPACE_REFUSAL.test(m)) {
          spaceRefusals++;
          lastRefusal = m;
          // Twice, the second time from a settled agent on the right build:
          // too full to ever fit, only freeing space can help. Still an
          // INSTALLED speaker, so it is reported as one, with the engine
          // named as the part that is missing.
          if (spaceRefusals >= 2) {
            return { ok: true, version: live, engineMissing: true, engineReason: m };
          }
          // First refusal: let the agent settle, then the next pass asks
          // again. The wait does the polling, so this pass sleeps no further.
          // A wait that runs out is the window ending.
          if (!(await waitStable(Math.min(deadline, hardStop)))) break;
          continue;
        }
      }
    }
    await sleep(Math.min(20_000, 3_000 * attempt));
  }
  // The window closed. Whether that is a failure depends entirely on what the
  // speaker last said about itself.
  //
  // A donor's SoundTouch 20 was reported as a failed installation while STR was
  // running on it perfectly: the agent had come up on the new version, but it
  // had dropped the Spotify engine to make room for its own update, so the
  // "engine present" condition never became true and the whole install timed
  // out (2026-08-11). He was told to send in logs for a speaker that was
  // already working. Spotify is one optional component of an install; it
  // cannot be the thing that decides whether the install happened.
  if (lastLive && lastLive.version) {
    return { ok: true, version: lastLive, engineMissing: true,
      engineReason: lastRefusal || 'engine not delivered inside the install window' };
  }
  return { ok: false, reason: 'timeout waiting for the speaker to reach the installed state' };
}
