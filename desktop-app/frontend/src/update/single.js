// update/single.js: the single-speaker update. doBoxUpdate is the guard the
// buttons call, runSingleBoxUpdate the body: the pre-checks and their dialogs,
// then RunSpeakerUpdate and the rendering of its phases into the button, the
// status line and toasts. Extracted from the main.js monolith; main.js-local
// helpers arrive through deps.js.

import { BoxSettings, BoxStoragePreflight, EventsOn, RunSpeakerUpdate, boxFetch } from '../api.js';
import { darkAfterUpdate, darkChecklistApplies, updateNoNetworkHere } from '../darkafterupdate.js';
import { t } from '../i18n/index.js';
import { syncShellPlayer } from '../shell2/mount.js';
import { state } from '../state.js';
import { setUpdateRunning, updateRunningNoticeHTML } from '../updaterunning.js';
import { $, confirmWarn, formatRemaining, getBoxLabel, showError, showToast } from '../utils.js';
import { loadBoxSettings, noNetworkHere } from '../views/settings.js';
import { deps } from './deps.js';
import {
  showDarkAfterUpdate,
  showSt300PowerCycleNotice,
  showUpdateFailureReport,
  showUpdateNoNetworkNotice,
} from './failreport.js';
import { noteOTAResult } from './otastuck.js';
import { BOX_BUSY_TOKEN, UPDATE_STEPS, withStep } from './shared.js';
import { ustate } from './state.js';

// foreignMod maps a leftover /mnt/nv directory name (as the agent reports it in
// foreignDirs / conflictingMod) to a human-readable name of the OTHER SoundTouch
// tool it belongs to, so the app can tell the user exactly what to remove to
// free NAND for the Spotify engine. Unknown names are shown verbatim.
const foreignMod = {
  aftertouch: 'AfterTouch',
  opentouchcloud: 'OpenTouchCloud',
  opentouch: 'OpenTouchCloud',
  bosman: 'Bosman',
  betterst: 'BetterST',
  sixback: 'SixBack',
  soundploy: 'SoundPloy',
};

// foreignSoftwareLabel turns a box version's foreignDirs/conflictingMod into a
// readable, de-duplicated list ("AfterTouch, OpenTouchCloud") for the
// "not enough space" message. Empty when the box carries no foreign leftovers.
function foreignSoftwareLabel(v) {
  const names = [];
  const push = (raw) => {
    if (!raw) return;
    String(raw).split(',').forEach((n) => {
      const key = n.trim().toLowerCase();
      if (!key) return;
      const label = foreignMod[key] || n.trim();
      if (!names.includes(label)) names.push(label);
    });
  };
  if (v) { push(v.conflictingMod); push(v.foreignDirs); }
  return names.join(', ');
}

// dropRunningNotices removes the "update running" notices over the progress
// lines once the update is over (#1764).
function dropRunningNotices() {
  document.querySelectorAll('.update-running').forEach((n) => n.remove());
}

export async function doBoxUpdate(targetBox) {
  // Both latches, not just this one. The whole-house run calls RunFleetUpdate
  // directly so it does not contend here, but it holds the single-box global
  // lock for the entire batch, and a single-speaker click during it would walk
  // into the same duplicate-start the latch exists to stop.
  if (ustate.boxUpdateBusy || ustate.updateAllBusy) {
    showToast(t('update.alreadyRunning', { name: state.otaTargetName || t('updateAll.batchLabel') }));
    return;
  }
  ustate.boxUpdateBusy = true;
  try {
    return await runSingleBoxUpdate(targetBox);
  } finally {
    ustate.boxUpdateBusy = false;
  }
}

async function runSingleBoxUpdate(targetBox) {
  // The box to update is passed explicitly by the caller (Speaker Settings
  // passes state.settingsBox). Fall back to the music-tab box only when a
  // caller omits it. Earlier this always used state.currentBox, so updating a
  // speaker picked in Speaker Settings actually OTA'd whatever box the music tab
  // was on, re-flashing the wrong (already-updated) speaker every time (#105).
  targetBox = targetBox || state.currentBox;
  if (!targetBox) return;
  // Hard-lock: while an OTA is in flight on ANY box, refuse to start
  // a second one. The UI also renders the button disabled in that
  // case via checkBoxUpdate(), but the redundant check here guards
  // against races where the user clicked through a stale render.
  // Say so out loud: returning silently made a second press look like a dead
  // button and invited a third (live, 2026-07-27).
  if (state.otaInProgress) {
    showToast(t('update.alreadyRunning', { name: state.otaTargetName || t('common.unknown') }));
    return;
  }

  // Stick gate, checked at the single OTA chokepoint so EVERY caller
  // (the music-tab banner button and the stick-info button) is covered.
  // A USB stick still in the speaker means rc.local re-copies the
  // stick's (older) version on the next boot and undoes the OTA; the OTA
  // also reboots the box. So if a stick is mounted, ask first and let
  // Cancel abort cleanly BEFORE anything starts. Earlier this gate only
  // sat on the stick-info button, so the banner button started the OTA
  // (and the reboot) with no confirmation.
  try {
    const r = await boxFetch(targetBox, '/api/stick/status');
    if (r.ok) {
      const data = await r.json();
      if (data && data.mounted) {
        const ok = await confirmWarn(t('update.stickInTitle'), t('update.stickInBody'));
        if (!ok) return; // user cancelled: no OTA, no reboot
      }
    }
  } catch { /* status unknown: do not block the update */ }

  // Wi-Fi pre-flight: a weak link can drop the OTA upload mid-transfer and
  // leave the speaker half-updated, then rebooting. If the box reports a
  // marginal/poor Wi-Fi signal, warn first so the user can move the speaker or
  // router closer before committing. Ethernet/coprocessor boxes report no
  // signal class and are never blocked; an unknown reading never blocks.
  try {
    const s = await BoxSettings(targetBox.host, targetBox.port);
    const ifs = (s && s.network && s.network.interfaces) || [];
    // Only a real WIFI_INTERFACE reading gates the update: that class comes
    // from the firmware live. Coprocessor boxes (ETHERNET-presented Wi-Fi)
    // carry only the agent's last gabbo snapshot - a display hint, not a
    // current measurement, and it false-alarmed on a Portable sitting next
    // to the router (2026-07-12).
    const conn = ifs.find(i => i.type === 'WIFI_INTERFACE' && i.state === 'NETWORK_WIFI_CONNECTED');
    const sig = conn && conn.signal;
    if (sig === 'MARGINAL_SIGNAL' || sig === 'POOR_SIGNAL') {
      const ok = await confirmWarn(t('update.weakWifiTitle'), t('update.weakWifiBody'));
      if (!ok) return; // user chose to improve the signal first
    }
  } catch { /* signal unknown: do not block the update */ }

  // Storage pre-flight (v0.9.7, #381/#119): a tight NAND makes the update the
  // riskiest thing the app can do to a speaker — on rollout day two boxes went
  // down mid-update with 5-8 MB free while the app proceeded silently. The
  // user stays in control: show the real numbers and what will happen (more
  // than one restart, Spotify engine reinstalled after the restart) and let
  // them decide. An unknown free figure (older agent) never blocks.
  //
  // The verdict AND the numbers come from BoxStoragePreflight, i.e. from the
  // same Go gate the push itself goes through. This used to be computed here
  // in JavaScript and it disagreed with the Go side twice: it compared the raw
  // embedded agent size instead of the compression-credited need, so a user was
  // told his update needed 13.9 MB when the real need was about 10.7 MB
  // (screenshot, 2026-08-22), and it credited a present engine only when the
  // agent reported goLibrespotSizeBytes, so an older agent with a full engine
  // on the box got no reclaim credit at all. Nothing is derived here any more:
  // this renders what the gate says.
  try {
    const pf = await BoxStoragePreflight(targetBox.host, targetBox.port);
    if (pf && pf.tight) {
      const fmtMB = (n) => ((n || 0) / 1048576).toFixed(1);
      // Only one thing here is ever the user's to act on: other SoundTouch
      // software occupying the storage. Name it when it is there, because then
      // the warning carries an instruction instead of a decision they have no
      // basis to make. The label mapping stays here; the preflight carries the
      // agent's raw conflictingMod/foreignDirs fields through untouched.
      const foreign = foreignSoftwareLabel(pf);
      const ok = await confirmWarn(
        t('update.tightNandTitle'),
        foreign
          ? t('update.tightNandNamedBody', { freeMB: fmtMB(pf.freeBytes), needMB: fmtMB(pf.needBytes), software: foreign })
          : t('update.tightNandBody', { freeMB: fmtMB(pf.freeBytes), needMB: fmtMB(pf.needBytes) })
      );
      if (!ok) return; // user cancelled: no OTA, no reboot
    }
  } catch { /* headroom unknown: do not block the update */ }

  // Drive both update buttons together (banner up top + stick info section)
  const buttons = () => ['boxUpdateBtn', 'stickInfoUpdateBtn'].map(id => $(id)).filter(Boolean);
  // Mutate the DOM buttons only when the user is still LOOKING at
  // the box being updated. If they switched to another box,
  // checkBoxUpdate() has rendered a fresh button for that other
  // box — overwriting it with our progress text would lie about
  // what the other box is doing. The state.otaTargetHost guard
  // below in checkBoxUpdate() takes care of rendering the right
  // "Update running on <name>" label there.
  // "Looking at the target box" means the SETTINGS view (where the update
  // buttons and progress live since they moved out of the music view) or the
  // music selection. Comparing against state.currentBox alone was a leftover
  // from the music-view era: updating any speaker other than the currently
  // selected one silently suppressed every progress line, so the settings
  // page just sat there and users pressed Update again (live, 2026-07-27).
  const lookingAtTarget = () => {
    const t = state.otaTargetHost;
    if (!t) return false;
    return (state.settingsBox && state.settingsBox.host === t) ||
           (state.currentBox && state.currentBox.host === t);
  };
  // The progress text used to be written INTO the button. These messages are
  // whole sentences (up to 124 characters in German), so a btn-mini wrapped
  // them over several lines, grew to fit, and shoved the surrounding layout
  // around every time the phase changed. A button is a label; the status
  // belongs beside it. Both buttons now have an .op-status line under them
  // with a reserved height, so the panel stays still while the text changes.
  const statusLines = () => ['boxUpdateStatus', 'stickInfoUpdateStatus'].map(id => $(id)).filter(Boolean);
  const setStatus = (text) => {
    if (!lookingAtTarget()) return;
    // Idempotent: only touch the DOM when a value actually changes. The 1 s
    // countdown tick called this every second during the post-update wait, and
    // re-setting `disabled` each time re-triggered the button's CSS transition,
    // which read as a per-second flicker through the whole 2nd (Spotify) upload.
    const busy = t('update.runningBtn');
    buttons().forEach(b => {
      if (b.textContent !== busy) b.textContent = busy;
      if (!b.disabled) b.disabled = true;
    });
    statusLines().forEach(el => {
      // Over the progress line, the notice that the speaker restarts and is
      // not to be used meanwhile (#1764). The settings card draws it too when
      // it renders during an update; this adds it to a card drawn before.
      const prev = el.previousElementSibling;
      if (!prev || !prev.classList.contains('update-running')) el.insertAdjacentHTML('beforebegin', updateRunningNoticeHTML());
      if (el.textContent !== text) el.textContent = text;
      // The full sentence stays reachable on hover for anything the two
      // reserved lines cannot hold.
      if (el.title !== text) el.title = text;
    });
  };
  const reset = () => {
    if (!state.currentBox || state.currentBox.host !== state.otaTargetHost) return;
    buttons().forEach(b => { b.disabled = false; b.textContent = t('update.refreshBtnSpeaker'); });
    statusLines().forEach(el => { el.textContent = ''; el.title = ''; });
    dropRunningNotices();
  };
  // Mark this box as the OTA target AND flip the global in-flight
  // flag BEFORE first setStatus() so checkBoxUpdate() and the
  // setStatus guard both see a consistent (target, in-flight)
  // pair at every point in this flow. Reset together in finally{}.
  //
  // What this speaker is supposed to end up running is written down inside
  // RunSpeakerUpdate, before any byte moves, and the backend reports itself
  // busy for the whole run so the window asks before closing. Neither is done
  // here any more; the Go side owns both for the headless harness too.
  state.otaTargetHost = targetBox.host;
  state.otaTargetName = getBoxLabel(targetBox);
  setUpdateRunning(targetBox.host, true);
  // Suppress the SSH "remove stick and reboot" banner for the whole
  // OTA window. The agent restarts mid-OTA and SSH is briefly open
  // during that restart; the banner's "Reboot now" button would
  // interrupt the agent exec and may leave the box half-flashed.
  state.otaInProgress = true;
  syncShellPlayer(); // UI 2.0: volume and transport follow the OTA gate
  setStatus(t('update.uploading'));
  // The post-reboot Spotify engine delivery streams its ~16 MB through this
  // same channel. It does NOT restart the speaker (a current agent swaps the
  // engine in place), so the progress line must not promise one.
  let engineStreaming = false;
  // Live upload progress + throughput while the ~10 MB agent streams to the box,
  // so a slow link shows movement instead of a frozen "Uploading...".
  const offBoxUp = EventsOn('box:update:progress', (p) => {
    if (!p || typeof p !== 'object' || p.pct == null || p.pct < 0) return;
    // At 100% the whole binary is on the box; it now writes NAND and reboots,
    // and its reply is usually lost in that reboot (BCO/taigan especially), so
    // the backend's UpdateBoxAgent call hangs another ~minute. Flip the label
    // to "restarting" the moment the upload completes instead of leaving the
    // user on "uploading" through the reboot the app cannot yet see.
    if (p.pct >= 100 && !engineStreaming) { setStatus(t('update.rebooting')); return; }
    const rate = p.bytesPerSec ? ' (' + deps.fmtRate(p.bytesPerSec) + ')' : '';
    setStatus(t('update.uploadingPct', { pct: p.pct }) + rate);
  });
  deps.checkSshBanner();
  // Swap the banner heading to "updating" right away (the otaHere branch in
  // checkBoxUpdate), so it no longer reads "update available" while the OTA runs.
  deps.checkBoxUpdate();
  let boxWasTouched = false;
  // From here on the speaker itself is involved: anything that fails after the
  // transfer started may leave it mid-restart, which is when the power-cycle
  // advice below is genuinely useful. Everything BEFORE this point (no embedded
  // agent in this build, box unreachable, a gate the user cancelled) never
  // touched the speaker, and telling those users to pull the plug is both
  // pointless and alarming.
  boxWasTouched = true;
  // The phases arrive from the Go sequence, one event per change and at least
  // one per second while a countdown runs, so the remaining time counts down
  // smoothly with no timer of our own: it is formatted here from remainingMs.
  let uploadedToastShown = false;
  const offPhase = EventsOn('box:update:phase', (d) => {
    if (!d || typeof d !== 'object' || d.host !== targetBox.host) return;
    switch (d.phase) {
      case 'uploading':
        setStatus(withStep(t('update.uploading'), UPDATE_STEPS.send));
        break;
      case 'rebooting':
        // The binary is on the speaker now. Said once, at the moment it
        // becomes true, so the user knows the transfer is behind them: the
        // agent detaches, waits out TIME_WAIT on its listener ports (see
        // internal/webui handleAgentUpdate) and only then execs the new
        // binary, so the speaker is away for minutes with nothing to show.
        if (!uploadedToastShown) { uploadedToastShown = true; showToast(t('update.uploadedToast')); }
        setStatus(withStep(t('update.rebooting'), UPDATE_STEPS.restart));
        break;
      case 'verifying':
        setStatus(withStep(t('update.waitingForSpeaker', { remaining: formatRemaining(d.remainingMs) }), UPDATE_STEPS.restart));
        break;
      // The speaker IS back on the new version and is only being held for the
      // stability window before we believe it (a BCO box can reboot a second
      // time on its own). Saying "restarting" through that window reads as if
      // the app had not noticed the speaker was back.
      case 'settling':
        setStatus(withStep(t('updateAll.phase.settling', { remaining: formatRemaining(d.remainingMs) }), UPDATE_STEPS.confirm));
        break;
      case 'retrying':
        uploadedToastShown = false;
        showToast(t('update.retrying'));
        setStatus(t('update.retrying'));
        break;
      case 'confirmed':
        // Not "update done": the Spotify-engine half may still follow (the
        // grey Updating button stays for it), and calling the whole update
        // done here while the card visibly kept working confused a reporter
        // (#672). The single final "done" comes when the flow resolves.
        showToast(t('update.agentDoneToast'));
        break;
      case 'engineQueued': setStatus(withStep(t('updateAll.phase.engineQueued'), UPDATE_STEPS.engine)); break;
      case 'engineUploading':
        engineStreaming = true;
        setStatus(withStep(t('updateAll.phase.engineUploading'), UPDATE_STEPS.engine));
        break;
      case 'spotify':
        engineStreaming = false;
        setStatus(t('update.spotifyFinalStep', { remaining: formatRemaining(d.remainingMs) }));
        break;
    }
  });
  try {
    // One speaker and a whole house run the SAME sequence, in Go. This path
    // only differs in how it reports - a button, a status line and toasts
    // instead of the overlay's rows.
    const result = await RunSpeakerUpdate(targetBox.host, targetBox.port, targetBox.deviceID || '', getBoxLabel(targetBox));
    noteOTAResult(targetBox, result);
    const confirmedVer = (result && result.version) || null;
    const confirmed = !!confirmedVer;
    if (result && result.outcome === 'done') {
      // THE done moment: everything, engine half included, is finished. One
      // toast, here and only here (#672); the confirm phase above announced
      // only the speaker-software half.
      if (!result.quiet) showToast(result.engineDelivered ? t('update.spotifyDoneToast') : t('update.doneToast'));
    } else if (result && result.outcome === 'partial') {
      // Agent updated, Spotify engine outstanding.
      if (result.engineTooFull) {
        // Retrying cannot help, only freeing space can. Name the OTHER
        // SoundTouch software eating the NAND so the user knows what to remove.
        // With no name (nothing foreign reported, or no version to read) there
        // is nothing to point at, so that branch asks for the diagnostic logs
        // rather than sending the owner of a clean speaker hunting for add-ons
        // that are not there (user mail, 2026-08-22).
        const foreign = foreignSoftwareLabel(confirmedVer);
        showToast(foreign
          ? t('spotify.engineTooFullNamed', { software: foreign })
          : t('spotify.engineTooFull'));
      } else {
        // No silent retry: the speaker's own screen now carries a visible
        // "Install Spotify engine" action.
        showToast(t('spotify.engineDeferredVisible'));
      }
    } else if (result && result.outcome === 'boxInSetup') {
      // The speaker was busy with its own setup. Nothing is wrong with the
      // network and nothing needs pulling out of the wall.
      showToast(t('update.boxInSetupNote', { name: getBoxLabel(targetBox) }));
    } else if (result && result.outcome === 'agentGone') {
      // The speaker is up and answering its own Bose web API; only STR is not
      // running on it. That has one exact fix and the user can do it in ten
      // seconds, so say it plainly instead of "took longer than expected".
      showToast(t('update.agentGoneToast', { name: getBoxLabel(targetBox) }));
    } else if (result && result.unmet === 'address-changed') {
      // Another speaker answers at this one's old address: the router gave
      // this speaker a new one while it restarted. "Check power / network"
      // would send the owner looking for a fault that is not there.
      showToast(t('update.addressChangedToast', { name: getBoxLabel(targetBox) }));
    } else if (updateNoNetworkHere(result)) {
      // The verify window ran out while this computer had no network at all
      // (#963): the speaker was never judged, and the power supply and reset
      // advice of the dark checklist would be wrong for it.
      showUpdateNoNetworkNotice();
    } else if (darkAfterUpdate(result) && darkChecklistApplies(targetBox)) {
      // Nothing answers at the speaker's address at all. A toast saying it
      // takes longer left owners of a speaker whose power supply gave out at
      // the restart with a dark box and no plan, so this gets the staged
      // checklist (darkafterupdate.js). The SoundTouch 300 is left to its own
      // power-cycle notice below, which is the one step it needs.
      showDarkAfterUpdate(targetBox);
    } else {
      // Timed out. The Go sequence has already journaled the verdict,
      // classified why, retried the one class of failure a retry actually
      // fixes, and noteOTAResult fed the loop breaker, so all that is left
      // here is to say so.
      showToast(t('update.tookLongerToast'));
    }
    // The SoundTouch 300 drops into its blinking update-pending state after an
    // OTA and needs a manual power-cycle to finish; the agent cannot clear it, so
    // tell the user (#ST300 blink, Michal + Jens 2026-07-16). As a modal, not a
    // toast: it stays up until read (Jens 2026-08-30).
    if ((targetBox.model || '').includes('300')) showSt300PowerCycleNotice(targetBox);
    // Refresh app state regardless of confirmation so the user sees current
    // truth (either updated or still in OTA). This is BEST-EFFORT and must never
    // surface as "Update failed": the box is typically still mid-reboot here, so
    // discoverBoxes / loadBoxSettings hit it while it is unreachable and can throw
    // "context deadline exceeded (... while reading body)". That throw used to land
    // in the outer catch and report a failed update even though the upload
    // succeeded and the version poll already decided the real outcome (reported by
    // the toast above). So swallow refresh errors here.
    try {
      await deps.discoverBoxes();
      // Force the confirmed new version onto the box record(s) so the view
      // shows the updated version immediately instead of a stale "outdated"
      // glitch until the next clean discovery cycle (Jens 2026-06-01:
      // after OTA the screen kept the old version until a manual refresh).
      // This also overrides a discovery-stickiness cache entry that might
      // still carry the pre-OTA version for a box that just rebooted.
      if (confirmed) {
        // Only onto the record of the speaker that answered. A speaker that
        // got a new address while it restarted is confirmed at that new
        // address, and its old one may belong to another speaker by now:
        // patching that record would show the other speaker as up to date.
        const ids = (o) => [o && o.deviceID, o && o.boxDeviceID]
          .filter(Boolean).map((s) => String(s).toLowerCase());
        const answered = ids(confirmedVer);
        const sameSpeaker = (b) => {
          const have = ids(b);
          return !answered.length || !have.length || have.some((s) => answered.includes(s));
        };
        const patchVer = (b) => {
          if (b && b.host === targetBox.host && sameSpeaker(b)) {
            if (confirmedVer.version) b.version = confirmedVer.version;
            if (confirmedVer.build) b.build = confirmedVer.build;
          }
        };
        patchVer(state.currentBox);
        if (Array.isArray(state.boxes)) state.boxes.forEach(patchVer);
      }
      deps.checkBoxUpdate();
      if (state.view === 'settings') loadBoxSettings();
    } catch (refreshErr) {
      try { console.warn('post-update refresh failed (non-fatal, box likely still rebooting)', refreshErr); } catch {}
    }
    reset();
  } catch (e) {
    // A timeout-class rejection ("context deadline exceeded ... while reading
    // body", common on slow links or with an HTTP-inspecting security suite like
    // Norton) does NOT mean the OTA failed: the box may still be applying it and
    // rebooting. Show an actionable "still working" hint and let the user
    // re-check the version shortly, instead of a raw Go error toast that two
    // reporters hit while their speaker actually updated fine.
    const msg = String(e);
    if (msg.includes(BOX_BUSY_TOKEN)) {
      // The backend turned this start away because a write is already running
      // on this speaker. Nothing failed: the run that IS going is fine, and its
      // owner must not be handed an error report with a copyable Go error for
      // an update that finishes a minute later (Sascha, ST30, 2026-09-14).
      showToast(t('update.alreadyRunning', { name: getBoxLabel(targetBox) }));
    } else if (/deadline exceeded|client\.timeout|while reading body/i.test(msg)) {
      showToast(t('update.stillWorking'));
    } else {
      showError(boxWasTouched
        ? t('update.failed', { err: msg })
        : t('update.failedNoChange', { err: msg }));
      // The update could not put this speaker into the state it was meant to
      // reach, so hand the user everything needed to report it instead of
      // making them describe a failure they cannot see.
      if (noNetworkHere(msg)) {
        // The app already knows this: the same predicate greys the speaker
        // list out for it. The update path just never asked, so pulling the
        // router mid-update produced a full copyable fault report about a
        // speaker that was fine, and the reporter had to work out for
        // themselves that it was their own router (#963).
        showUpdateNoNetworkNotice();
      } else {
        showUpdateFailureReport(targetBox, 'update', msg);
      }
    }
    reset();
  } finally {
    if (typeof offPhase === 'function') offPhase();
    if (typeof offBoxUp === 'function') offBoxUp();
    // Always clear the OTA-in-flight gate so the SSH banner can
    // come back if it still applies, even if we threw mid-poll.
    state.otaInProgress = false;
    syncShellPlayer(); // UI 2.0: volume and transport follow the OTA gate
    setUpdateRunning(state.otaTargetHost, false);
    dropRunningNotices();
    state.otaTargetHost = null;
    state.otaTargetName = null;
    deps.checkSshBanner();
    // Force a re-render of the current view's update button so any
    // other-box "Update running on …" placeholder is replaced with
    // the regular Update button immediately.
    deps.checkBoxUpdate();
  }
}
