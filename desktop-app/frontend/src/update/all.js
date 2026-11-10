// update/all.js: Update all. updateAllBoxes is the guard the buttons call,
// runUpdateAllBoxes the body: the pre-scan and the one confirmation, then
// RunFleetUpdate and the rendering of its phases into the rows of the Update
// all window. Extracted from the main.js monolith; main.js-local helpers
// arrive through deps.js.

import {
  BoxAgentVersion,
  BoxSettings,
  BoxStoragePreflight,
  BrowserOpenURL,
  EventsOn,
  RunFleetUpdate,
  boxFetch,
} from '../api.js';
import { strBoxes } from '../boxes.js';
import { darkBatchPlan, darkChecklistRowHtml, wireDarkChecklist } from '../darkafterupdate.js';
import { anyInLiveGroup } from '../groups.js';
import { t } from '../i18n/index.js';
import { syncShellPlayer } from '../shell2/mount.js';
import { state } from '../state.js';
import { clearUpdateRunning, setUpdateRunning } from '../updaterunning.js';
import {
  $,
  confirmWarn,
  escapeHtml,
  formatRemaining,
  getBoxLabel,
  hideToast,
  showError,
  showToast,
  splitUpdateTargets,
} from '../utils.js';
import { loadBoxSettings, noNetworkHere } from '../views/settings.js';
import { deps } from './deps.js';
import {
  showSt300PowerCycleNotice,
  showUpdateFailureReport,
  showUpdateNoNetworkNotice,
  updateAllRowReport,
} from './failreport.js';
import { noteOTAResult, otaStuck } from './otastuck.js';
import { BOX_BUSY_TOKEN, UPDATE_STEPS, withStep } from './shared.js';
import { ustate } from './state.js';

// spotifyPhaseText words the engine step for the user. The row used to
// interpolate the speaker's raw "missing"/"present" report ("Spotify engine:
// missing, waiting for the target state"), English inside a German UI and a
// description of the speaker rather than of what is happening (Jens, fleet
// update 2026-09-06). Missing means the engine is about to be installed;
// present means it is being confirmed.
function spotifyPhaseText(d) {
  if (!d || !d.reachable) return t('updateAll.phase.spotifyUnreachable');
  return d.engine === 'present' ? t('updateAll.phase.spotifyChecking') : t('updateAll.phase.spotifyInstalling');
}

// updateAllBoxes updates every eligible speaker in one click with a live
// per-box multi-progress overlay. Built for a household with several speakers,
// some on weak Wi-Fi: it asks ONCE up front, then hands the whole list to
// RunFleetUpdate, which starts every speaker at once and serialises only the
// byte pushes (so concurrent ~16 MB streams do not saturate a weak shared AP
// and trip the 60 s upload-stall watchdog), and never lets one slow/failed box
// block the rest. Calls the Go batch directly (not doBoxUpdate) so it does not
// contend on the single-box global lock, which it holds for the whole batch.
//
// Returns true when a batch was actually started, false when it backed out
// (already running, nothing to do, user cancelled). Callers that took a button
// or a card off screen for it need that answer to put it back. onStart fires
// the moment the batch is committed, for callers that want to get out of the
// way then rather than minutes later when the whole batch is done.
// Re-entrancy latch, set BEFORE the first await and cleared in the finally at
// the very bottom. state.otaInProgress is not enough: it is only raised AFTER
// the confirmation, and everything before it (a version read per speaker, a
// stick check and a Wi-Fi read per target) is seconds of network work during
// which a second click sails straight past that check. Two overlapping runs
// then queue two confirmations on a modal that keeps ONE resolver, so the
// second prompt arrived after the batch was already done and asked the user to
// update speakers they had just updated (Jens, 2026-08-22).
export async function updateAllBoxes(onStart) {
  // Saying so out loud, not returning silently: a second press on a dead-looking
  // button is exactly what the single-box path already learned to answer.
  if (ustate.updateAllBusy || ustate.boxUpdateBusy || state.otaInProgress) {
    showToast(t('update.alreadyRunning', { name: state.otaTargetName || t('updateAll.batchLabel') }));
    return false;
  }
  ustate.updateAllBusy = true;
  try {
    return await runUpdateAllBoxes(onStart);
  } finally {
    ustate.updateAllBusy = false;
  }
}

// runUpdateAllBoxes is the body; updateAllBoxes above is the guard around it.
async function runUpdateAllBoxes(onStart) {
  // Offline speakers are excluded up front: probing them burns the pre-flight
  // budget, and counting them as "nothing to do" is how an all-offline fleet
  // produced "already up to date" (#775).
  const candidates = strBoxes(state.boxes).filter(b => b.host && !b.offline && !otaStuck(b));
  // Everything from here to the confirmation is network work: one version read
  // per speaker, then a stick check and a Wi-Fi read per update target. On a
  // busy LAN, or right after start while discovery is still sweeping, that is
  // several seconds in which the click produced nothing at all on screen. Keep
  // a line up for the whole pre-flight so the app is visibly working.
  showToast(t('updateAll.checking'), 0);
  // Ask every speaker that is NOT behind whether it still has its Spotify
  // engine, so one that quietly lost it is repaired by this run instead of
  // being skipped (splitUpdateTargets carries the reasoning). One version read
  // per speaker; one in deep standby does not answer and is left alone, as a
  // read must.
  const rows = [];
  for (const b of candidates) {
    const needsUpdate = deps.boxNeedsUpdate(b);
    let engineMissing = false;
    if (!needsUpdate) {
      try {
        const v = await BoxAgentVersion(b.host, b.port);
        engineMissing = !!(v && v.goLibrespot === 'missing');
      } catch { /* asleep or unreachable: leave it untouched */ }
    }
    rows.push({ box: b, needsUpdate, engineMissing });
  }
  const { updateTargets, engineTargets, targets } = splitUpdateTargets(rows);
  if (targets.length === 0) {
    hideToast();
    // "Already up to date" is only an honest claim when reachable speakers
    // were actually inspected; a fleet that is offline gets the truth (#775).
    const anyOnline = strBoxes(state.boxes).some(b => !b.offline);
    showToast(anyOnline ? t('updateAll.noneToUpdate') : t('speakerUpdate.notReachable'));
    return false;
  }
  // Hosts that only need the engine put back, not a whole agent update.
  const engineOnly = new Set(engineTargets.map(b => b.host));

  // Pre-scan for sticks / weak Wi-Fi so the user is warned ONCE up front, not
  // once per speaker (the per-box prompts the single-box path shows).
  const notes = [];
  for (const b of updateTargets) {
    try {
      const r = await boxFetch(b, '/api/stick/status');
      if (r.ok) { const d = await r.json(); if (d && d.mounted) notes.push(t('updateAll.noteStick', { name: getBoxLabel(b) })); }
    } catch { /* stick status unknown: do not block */ }
    try {
      const s = await BoxSettings(b.host, b.port);
      const ifs = (s && s.network && s.network.interfaces) || [];
      // Firmware-reported Wi-Fi class only, same rule as doBoxUpdate: the
      // coprocessor boxes' agent-relayed snapshot is not a live measurement.
      const conn = ifs.find(i => i.type === 'WIFI_INTERFACE' && i.state === 'NETWORK_WIFI_CONNECTED');
      const sig = conn && conn.signal;
      if (sig === 'MARGINAL_SIGNAL' || sig === 'POOR_SIGNAL') notes.push(t('updateAll.noteWeakWifi', { name: getBoxLabel(b) }));
    } catch { /* signal unknown: do not block */ }
    try {
      // Storage, the one thing this pre-scan never looked at. The single-box
      // update has asked since #ST30 and stops to confirm when it is tight;
      // the batch wrote the same speakers without ever mentioning it, so the
      // owner of a tight box got a warning or no warning depending purely on
      // which button he pressed. Same Go gate, so the two paths cannot drift:
      // nothing is computed here, this renders what BoxStoragePreflight says.
      //
      // A note and not a confirm: the batch already asks once for the whole
      // run, and the update is the right thing to do anyway. Tight NAND costs
      // a re-delivered engine after the reboot, which the flow handles by
      // itself, so the user needs to KNOW rather than to decide.
      const pf = await BoxStoragePreflight(b.host, b.port);
      if (pf && pf.tight) {
        const mb = (n) => ((n || 0) / 1048576).toFixed(1);
        notes.push(t('updateAll.noteTightNand', {
          name: getBoxLabel(b), freeMB: mb(pf.freeBytes), needMB: mb(pf.needBytes),
        }));
      }
    } catch { /* headroom unknown (older agent): never block a batch */ }
  }
  // A grouped speaker leaves its group while it restarts, in a run just as in
  // a single update (#1339): said once, before the run starts.
  if (anyInLiveGroup(updateTargets, state.zoneLive)) notes.push(t('updateAll.groupNote'));
  hideToast();
  // The prompt used to be one plain string with "\n" separators pushed through
  // innerHTML, where HTML collapses newlines: four speakers plus two notes
  // arrived as a single unbroken paragraph under a red warning triangle and a
  // red "proceed anyway" button. Updating the speakers is the normal,
  // recommended thing to do, so it gets a calm heading, one short sentence per
  // group, the speakers as an actual list, and a primary "update now" button.
  // Names come from the speakers themselves and are escaped: they end up in
  // innerHTML.
  const nameList = (arr, cls) =>
    `<ul${cls ? ` class="${cls}"` : ''}>` +
    arr.map(x => `<li>${escapeHtml(typeof x === 'string' ? x : getBoxLabel(x))}</li>`).join('') +
    '</ul>';
  // Two groups with two different promises: the first restarts, the second
  // normally does not. Saying which is which up front is the difference between
  // a run the user can predict and one that surprises them.
  let body = '';
  if (updateTargets.length) {
    body += `<p>${escapeHtml(t('updateAll.confirmLead'))}</p>`
      + nameList(updateTargets);
  }
  if (engineTargets.length) {
    body += `<p>${escapeHtml(t('updateAll.confirmLeadEngine'))}</p>`
      + nameList(engineTargets);
  }
  if (notes.length) body += nameList(notes, 'warn-notes');
  const confirmTitle = updateTargets.length ? t('updateAll.confirmTitle') : t('updateAll.confirmTitleEngine');
  const confirmed = await confirmWarn(confirmTitle, body, {
    icon: null,
    calm: true,
    rich: true,
    confirmLabel: updateTargets.length ? t('updateAll.confirmBtn') : t('updateAll.confirmBtnEngine'),
    confirmClass: 'btn btn-primary',
  });
  if (!confirmed) return false;
  // The prompt can sit on screen for a while. Anything that started an OTA in
  // the meantime (the single-speaker button, a resumed update intent) owns the
  // speakers now, so do not push a second batch over it.
  if (state.otaInProgress) {
    showToast(t('update.alreadyRunning', { name: state.otaTargetName || t('updateAll.batchLabel') }));
    return false;
  }
  try { if (onStart) onStart(); } catch { /* caller's UI concern only */ }

  // Hold the single-box global lock for the whole batch: the single-speaker
  // buttons then render "update running" and the SSH "remove stick" banner stays
  // suppressed. The sentinel host makes every real box's button show that state.
  state.otaInProgress = true;
  syncShellPlayer(); // UI 2.0: volume and transport follow the OTA gate
  state.otaTargetHost = '__batch__';
  state.otaTargetName = t('updateAll.batchLabel');
  deps.checkSshBanner();
  deps.checkBoxUpdate();

  // Build one overlay row per box.
  const rowState = new Map(); // host -> { box, el, barFill, phaseEl, outcome }
  const listEl = $('uaList');
  if (listEl) listEl.innerHTML = '';
  for (const b of targets) {
    const row = document.createElement('div');
    row.className = 'ua-row';
    row.dataset.host = b.host;
    row.innerHTML = `
      <div class="ua-row-head">
        <span class="ua-name">${escapeHtml(getBoxLabel(b))}</span>
        <span class="ua-model">${escapeHtml(b.model || '')}</span>
        <span class="ua-status" data-role="phase">${escapeHtml(t('updateAll.phase.queued'))}</span>
      </div>
      <div class="ua-bar"><div class="ua-bar-fill" data-role="bar"></div></div>`;
    if (listEl) listEl.appendChild(row);
    rowState.set(b.host, { box: b, el: row, barFill: row.querySelector('[data-role=bar]'), phaseEl: row.querySelector('[data-role=phase]'), outcome: null });
  }
  const counts = () => {
    let done = 0, fail = 0, defer = 0, busy = 0;
    for (const r of rowState.values()) {
      if (r.outcome === 'done') done++;
      else if (r.outcome === 'failed') fail++;
      else if (r.outcome === 'agentGone') fail++;
      // A speaker in its own setup is deferred, not failed: it is very likely
      // updated and simply off the network while the firmware finishes.
      else if (r.outcome === 'partial' || r.outcome === 'timeout' || r.outcome === 'boxInSetup') defer++;
      else busy++;
    }
    return { done, fail, defer, busy };
  };
  const renderSummary = () => {
    const c = counts();
    const sum = $('uaSummary');
    if (sum) sum.textContent = t('updateAll.summary', { done: c.done, deferred: c.defer, failed: c.fail, total: targets.length });
  };
  // Row visuals, three live states and no marching bar anywhere: a row that
  // WAITS for its turn (queued behind another speaker) shows an empty track and
  // faint text; a row whose speaker is BUSY on its own (rebooting, settling,
  // the engine being confirmed) shows a still, dim fill that breathes slowly;
  // only a real transfer moves a bar, driven by its byte progress. Six rows of
  // sliding stripes competed with the one transfer that was actually running
  // and hid it (Jens, fleet update 2026-09-06).
  const setRow = (host, { phaseText, pct, barClass, wait, busy } = {}) => {
    const r = rowState.get(host); if (!r) return;
    if (phaseText != null && r.phaseEl) r.phaseEl.textContent = phaseText;
    if (pct != null && r.barFill) { r.barFill.style.width = Math.max(0, Math.min(100, pct)) + '%'; r.el.classList.remove('ua-wait', 'ua-busy'); }
    if (wait) { r.el.classList.remove('ua-busy'); r.el.classList.add('ua-wait'); }
    if (busy) { r.el.classList.remove('ua-wait'); r.el.classList.add('ua-busy'); }
    if (barClass) { r.el.classList.remove('ua-wait', 'ua-busy', 'ua-done', 'ua-failed', 'ua-defer'); r.el.classList.add(barClass); }
  };

  const overlay = $('updateAllOverlay');
  if (overlay) overlay.classList.remove('hidden');
  renderSummary();
  // Route the host-tagged live byte-progress to the right row's bar.
  const offProg = EventsOn('box:update:progress', (p) => {
    if (!p || typeof p !== 'object' || !p.host || p.pct == null || p.pct < 0) return;
    // An engine-only repair streams its bytes through this same channel, but a
    // current agent swaps the engine in place and does NOT restart. Its row
    // therefore only follows the bar and never claims a restart it will not make.
    if (engineOnly.has(p.host)) { setRow(p.host, { pct: p.pct }); return; }
    // Its first byte is where the update starts touching this speaker: from
    // here until its row has an outcome it is marked as updating (#1764).
    setUpdateRunning(p.host, true);
    // At 100% the binary is on the box and it reboots; its reply is often lost
    // in that reboot, so the sequence's own 'rebooting' phase does not fire for
    // another ~minute. Flip the row to "restarting" on upload completion so it
    // does not sit at "uploading" through a reboot the app cannot yet see.
    if (p.pct >= 100) { setRow(p.host, { phaseText: withStep(t('updateAll.phase.rebooting'), UPDATE_STEPS.restart), busy: true }); return; }
    // The first byte is what turns a queued row into an uploading one: the
    // 'uploading' phase itself fires before the batch gate, while the row is
    // still waiting for another speaker's transfer to finish.
    setRow(p.host, { phaseText: withStep(t('updateAll.phase.uploading'), UPDATE_STEPS.send), pct: p.pct });
  });
  const mini = $('uaMini');
  // Reopening is the whole point of the strip, so it is a button, not a label.
  if (mini) mini.onclick = () => { mini.classList.add('hidden'); if (overlay) overlay.classList.remove('hidden'); };
  const showMiniIfStillRunning = () => {
    if (!mini) return;
    const c = counts();
    // c.fail and c.defer, not c.failed/c.deferred: counts() has never returned
    // those names, so the sum was NaN, `left <= 0` was false (every NaN
    // comparison is), and closing the panel mid-run put up a strip reading
    // "Still updating NaN speaker(s)" (reported with a bundle, 2026-09-15).
    //
    // targets.length, not rows.length: rows holds every speaker the run
    // INSPECTED, including the ones that turned out to need nothing. Counting
    // those made the strip claim more speakers were still going than were ever
    // being updated. renderSummary already uses targets.length for this reason.
    const left = targets.length - (c.done + c.fail + c.defer);
    if (left <= 0) { mini.classList.add('hidden'); return; }
    mini.textContent = t('updateAll.stillRunning', { n: left });
    mini.classList.remove('hidden');
  };
  if ($('uaClose')) {
    $('uaClose').onclick = () => {
      if (overlay) overlay.classList.add('hidden');
      showMiniIfStillRunning();
    };
  }

  // The batch writes software to speakers exactly like the single update,
  // so it carries the same guarantees: the window asks before closing for
  // the whole run, every speaker's target is written down before its turn,
  // and a speaker that does not get there produces the copyable report
  // instead of a red row nobody can act on. The sequence, the upload gate
  // that serialises only the byte pushes, and the honesty re-check all run in
  // Go (RunFleetUpdate); what follows renders its phases into the rows.

  let uaReportShown = false;
  // finishRow is a row's final word, from the terminal phase event or, if the
  // window missed it, from the returned row. The engine-only rows have their
  // own texts: nothing was written to those speakers but the engine, and a
  // current agent hot-swaps it, so they never claim a restart.
  const finishRow = (b, d) => {
    const outcome = (d && d.outcome) || 'failed';
    const err = String((d && d.err) || '');
    if (engineOnly.has(b.host)) {
      if (outcome === 'done') {
        setRow(b.host, { phaseText: t('updateAll.phase.engineDone'), pct: 100, barClass: 'ua-done' });
      } else if (outcome === 'partial' && d.unmet === 'no-embedded-engine') {
        // This app build carries no engine, so there was nothing to deliver.
        setRow(b.host, { phaseText: t('updateAll.phase.deferred'), pct: 100, barClass: 'ua-defer' });
      } else if (outcome === 'partial') {
        // Delivered, and gone again to a reboot right after: the same honesty
        // check the update path does, so the row does not go green on it.
        setRow(b.host, { phaseText: t('updateAll.phase.engineMissing'), pct: 100, barClass: 'ua-defer' });
      } else if (d.engineTooFull) {
        // A full NAND is not something to retry blindly, so it gets its own
        // row text instead of a bare "failed"; the speaker's own screen names
        // the other SoundTouch software eating the space.
        setRow(b.host, { phaseText: t('updateAll.phase.engineTooFull'), barClass: 'ua-failed' });
      } else {
        setRow(b.host, { phaseText: t('updateAll.phase.failed'), barClass: 'ua-failed' });
        if (!uaReportShown) {
          uaReportShown = true;
          // The engine repair row reaches the speaker the same way, so it
          // loses the network the same way (#963).
          if (noNetworkHere(err)) showUpdateNoNetworkNotice();
          else showUpdateFailureReport(b, 'update-all-engine', err);
        }
        try { console.warn('update all: engine repair failed', b.host, err); } catch {}
      }
    } else if (outcome === 'failed') {
      setRow(b.host, { phaseText: t('updateAll.phase.failed'), barClass: 'ua-failed' });
      // Same account of the failure the single update gives, for the
      // FIRST speaker that fails: a wall of reports would help nobody, and
      // the rest stay on record for the next time each is opened.
      //
      // Including the no-network case. The single-speaker button learned to
      // say "your own network went away" in one line instead of opening a
      // fault report about a speaker that was fine, and the batch did not,
      // although the batch is what the reporter ran: his report header reads
      // "failed at: update-all" (#963).
      if (!uaReportShown) {
        uaReportShown = true;
        if (noNetworkHere(err)) showUpdateNoNetworkNotice();
        else showUpdateFailureReport(b, 'update-all', err);
      }
      try { console.warn('update all: box failed', b.host, err); } catch {}
    } else if ((b.model || '').includes('300')) {
      // The SoundTouch 300 drops into its blinking update-pending state after an
      // OTA and is non-functional until it is unplugged; the agent cannot clear
      // it. Tell the user that on the row rather than a misleading plain "done".
      setRow(b.host, { phaseText: t('update.st300PowerCycle'), pct: 100, barClass: 'ua-defer' });
    } else if (outcome === 'done') setRow(b.host, { phaseText: t('updateAll.phase.done'), pct: 100, barClass: 'ua-done' });
    else if (outcome === 'partial') setRow(b.host, { phaseText: t('updateAll.phase.engineMissing'), pct: 100, barClass: 'ua-defer' });
    else if (outcome === 'agentGone') setRow(b.host, { phaseText: t('updateAll.phase.agentGone'), barClass: 'ua-failed' });
    else if (outcome === 'boxInSetup') setRow(b.host, { phaseText: t('updateAll.phase.boxInSetup'), pct: 100, barClass: 'ua-defer' });
    else if (d.unmet === 'address-changed') setRow(b.host, { phaseText: t('updateAll.phase.addressChanged'), pct: 100, barClass: 'ua-defer' });
    else setRow(b.host, { phaseText: t('updateAll.phase.timeout'), barClass: 'ua-defer' });
    const r = rowState.get(b.host); if (r) r.outcome = outcome;
    setUpdateRunning(b.host, false);
    renderSummary();
  };
  const FINAL_PHASES = new Set(['done', 'partial', 'timeout', 'agentGone', 'boxInSetup', 'failed']);
  const offPhase = EventsOn('box:update:phase', (d) => {
    if (!d || typeof d !== 'object' || !d.host || !rowState.has(d.host)) return;
    const b = rowState.get(d.host).box;
    if (FINAL_PHASES.has(d.phase)) { if (!rowState.get(d.host).outcome) finishRow(b, d); return; }
    if (engineOnly.has(d.host)) {
      // Queued behind another speaker's push, then sending: both read as the
      // one thing happening to this speaker, the engine being put back.
      if (d.phase === 'engineQueued') setRow(d.host, { phaseText: t('updateAll.phase.engineOnly'), wait: true });
      else if (d.phase === 'engineUploading') setRow(d.host, { phaseText: t('updateAll.phase.engineOnly'), busy: true });
      return;
    }
    // A speaker that restarts or is being confirmed is in its update even if
    // its progress event was missed (#1764).
    if (['rebooting', 'verifying', 'retrying', 'settling', 'spotify'].includes(d.phase)) setUpdateRunning(d.host, true);
    switch (d.phase) {
      // 'uploading' fires before the batch gate: the row keeps waiting
      // until its first progress byte arrives (the progress handler above).
      case 'uploading': setRow(d.host, { phaseText: t('updateAll.phase.queued'), wait: true }); break;
      case 'rebooting': setRow(d.host, { phaseText: t('updateAll.phase.rebooting'), busy: true }); break;
      case 'verifying': setRow(d.host, { phaseText: withStep(t('updateAll.phase.verifying', { remaining: formatRemaining(d.remainingMs) }), UPDATE_STEPS.restart), busy: true }); break;
      case 'retrying': setRow(d.host, { phaseText: t('updateAll.phase.retrying'), busy: true }); break;
      case 'settling': setRow(d.host, { phaseText: withStep(t('updateAll.phase.settling', { remaining: formatRemaining(d.remainingMs) }), UPDATE_STEPS.confirm), busy: true }); break;
      case 'engineQueued': setRow(d.host, { phaseText: t('updateAll.phase.engineQueued'), wait: true }); break;
      case 'engineUploading': setRow(d.host, { phaseText: t('updateAll.phase.engineUploading'), pct: 0 }); break;
      case 'spotify': setRow(d.host, { phaseText: spotifyPhaseText(d), busy: true }); break;
    }
  });

  // Every speaker starts at once and they queue only for the push itself, so
  // the restarts, which are the long part and need no network, all happen
  // together instead of two at a time (the upload gate in otaorchestrate.go).
  const fleetTargets = targets.map(b => ({
    host: b.host, port: b.port, deviceID: b.deviceID || '', name: getBoxLabel(b), engineOnly: engineOnly.has(b.host),
  }));
  let fleet = null;
  try {
    fleet = await RunFleetUpdate(fleetTargets);
  } catch (e) {
    const msg = String(e);
    if (msg.includes(BOX_BUSY_TOKEN)) {
      // The whole batch was turned away: another update owns the speakers.
      // Nothing failed on any of them, so no reports, just the word.
      showToast(t('update.alreadyRunning', { name: state.otaTargetName || t('updateAll.batchLabel') }));
      for (const r of rowState.values()) {
        if (r.outcome) continue;
        r.outcome = 'failed';
        setRow(r.box.host, { phaseText: t('updateAll.phase.failed'), barClass: 'ua-failed' });
      }
      renderSummary();
    } else {
      for (const r of rowState.values()) if (!r.outcome) finishRow(r.box, { outcome: 'failed', err: msg });
    }
  }
  // The rows are the record. A terminal event the window missed is filled in
  // from them, and the loop breaker is fed from them, because only the rows
  // carry the confirmation and the classifier's verdict.
  const darkEntries = [];
  for (const row of (fleet && fleet.rows) || []) {
    const r = rowState.get(row.host);
    if (!r) continue;
    if (!r.outcome) finishRow(r.box, row);
    if (!engineOnly.has(row.host)) noteOTAResult(r.box, row);
    darkEntries.push({ host: row.host, result: row, box: r.box, engineOnly: engineOnly.has(row.host) });
  }
  // A speaker that answers nothing at all after its restart gets the staged
  // checklist folded out under its own row. Only the returned rows carry the
  // classifier's verdict, the phase events do not. The 300 keeps its own
  // power-cycle row text and notice. When this computer had no network, or
  // every speaker of the run went silent at once, it is this computer's
  // connection, not a row of dead power supplies: one notice instead of a
  // checklist on every row (darkBatchPlan).
  const darkPlan = darkBatchPlan(darkEntries);
  for (const host of darkPlan.checklist) {
    const r = rowState.get(host);
    const slot = r && updateAllRowReport(host);
    if (slot) {
      slot.innerHTML = darkChecklistRowHtml({ name: getBoxLabel(r.box), box: r.box });
      wireDarkChecklist(slot, { openURL: (url) => { try { BrowserOpenURL(url); } catch {} } });
    }
  }

  // Batch done: release the global lock, refresh, summarize.
  offProg();
  offPhase();
  if (mini) mini.classList.add('hidden');
  state.otaInProgress = false;
  syncShellPlayer(); // UI 2.0: volume and transport follow the OTA gate
  clearUpdateRunning();
  state.otaTargetHost = null;
  state.otaTargetName = null;
  renderSummary();
  try { await deps.discoverBoxes(); deps.checkBoxUpdate(); if (state.view === 'settings') loadBoxSettings(); } catch { /* boxes still rebooting */ }
  deps.checkSshBanner();
  const c = counts();
  // A batch with failures or unconfirmed boxes must not lead with "Done": the
  // #775 run had every update fail and still read like a success. Timeouts
  // (the version poll expired without the box confirming) are reported as
  // unconfirmed, not as deferred work.
  let unconfirmed = 0;
  for (const r of rowState.values()) if (r.outcome === 'timeout') unconfirmed++;
  if (c.fail > 0 || unconfirmed > 0) {
    showToast(t('updateAll.problemToast', { failed: c.fail, unconfirmed, done: c.done, deferred: c.defer - unconfirmed }));
  } else {
    showToast(t('updateAll.doneToast', { done: c.done, deferred: c.defer, failed: c.fail }));
  }
  if (darkPlan.notice === 'noNetwork') showUpdateNoNetworkNotice();
  else if (darkPlan.notice === 'allDark') showError(t('darkCheck.allDark'));
  // Any SoundTouch 300 that got this far is now blinking and unreachable until
  // someone unplugs it once. The row already says so, but the row scrolls away
  // with the panel, so the instruction also gets the stays-until-dismissed
  // treatment (Jens 2026-08-30).
  if (targets.some(b => (b.model || '').includes('300') && ((rowState.get(b.host) || {}).outcome !== 'failed'))) {
    showSt300PowerCycleNotice(targets.find(b => (b.model || '').includes('300') && ((rowState.get(b.host) || {}).outcome !== 'failed')));
  }
  return true;
}
