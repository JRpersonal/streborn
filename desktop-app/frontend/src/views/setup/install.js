// views/setup/install.js: the install itself. waitForBoxAfterSetup finds the
// speaker (or takes the one the network-install card hands it), runs the
// install with its live checklist, and ends on one of the result screens: done
// with the provisioning steps, still waiting for the speaker, or failed with
// the help checklist and the failure report. Both routes into an install end
// here, the network-install card (hero.js) and the stick wait (await.js).
//
// installWaitPanelLive is exported as a plain `let`: this module is the only
// one that writes it (renderInstallWaiting) and the others read the live
// binding, so it needs no state object. Extracted from views/setup.js.

import { state } from '../../state.js';
import { formatClock } from '../../format.js';
import {
  $,
  escapeHtml,
  sleep,
  showError,
  showToast,
  formatRemaining,
  isSoundTouch300,
  neverReadsStick,
} from '../../utils.js';
import { t } from '../../i18n/index.js';
import { busyRefusalText } from '../../update/shared.js';
import { darkChecklistApplies } from '../../darkafterupdate.js';
import { appendSavedBundlePath, failReportSaveHosts } from '../../failreport.js';
import {
  SetBoxName,
  SetBoxLanguage,
  SetClockDisplay,
  InstallSTROnBox,
  BoxAgentVersion,
  EnsureSpotifyEngine,
  UpdateFailureReport,
  SetOTARunning,
  RecordUpdateIntent,
  RepairInstallViaSSH,
  ProbeSetupAP,
  DiscoverBoxes,
  SaveDiagnosticBundle,
  EventsOn,
  boxFetch,
} from '../../api.js';
import { takeShareOffer, wireShareOffer } from '../../share.js';
import { verifyInstalledLoop, waitForStableAgent } from '../../agentsettle.js';
import { deps } from './deps.js';
import { currentHeroHold } from './shared.js';
import {
  powerCycleAdviceHtml,
  outdatedFirmwareHtml,
  wireOutdatedFirmwareLinks,
  installDarkChecklistHtml,
  wireInstallDarkChecklist,
  installHelpHtml,
} from './installhelp.js';
import { showPairAfterInstall } from './stereopair.js';

// retryStep runs an async op up to `attempts` times with a short pause between
// tries. Resolves to { ok, value }: a thrown error OR an explicit `false` return
// counts as a failure and is retried; any other resolved value counts as success
// and is returned. Used for post-install provisioning, where the box has just
// restarted its agent and the first call or two often lands before it answers.
async function retryStep(fn, attempts, delayMs) {
  let last = { ok: false, value: undefined };
  for (let i = 0; i < attempts; i++) {
    try {
      const v = await fn();
      if (v !== false) return { ok: true, value: v };
      last = { ok: false, value: v };
    } catch (e) { last = { ok: false, value: e }; }
    if (i < attempts - 1) await sleep(delayMs);
  }
  return last;
}

// installWaitPanelLive is true while the "the speaker has not answered yet"
// panel is on screen and its backend watcher is still running.
//
// It exists because startNetworkInstall's await returns as soon as that panel is
// DRAWN, not when it resolves: renderInstallWaiting wires two listeners and
// returns, so the finally clause cleared the guard while the install was still
// in flight. Two things followed, both reported from the field. main.js's
// periodic refresh empties #setupResult whenever installRunActive() is false, so
// switching to another tab and back wiped the panel, and the next event then
// found its marker gone and dropped both listeners: the install:late rescue,
// which is the whole reason the watcher keeps looking, could never render. And
// the hero Install button was re-enabled under a panel whose entire purpose is
// to stop a second install, so a SoundTouch 300 owner looking at "unplug it
// once" could press Install again on a soundbar that was blinking with every
// port dead. One did, twice, in the week of 2026-09-24.
export let installWaitPanelLive = false;

// waitForBoxAfterSetup runs after the user has confirmed the stick
// is in the speaker and the speaker has booted. It covers three
// end-user paths in priority order:
//   1. Box is already on the home LAN (had Wi-Fi before) -> just
//      proceed to step 3 once mDNS or the active probe surfaces it.
//   2. Box is brand-new / factory-reset and is broadcasting a Bose
//      setup-AP -> auto cold-bootstrap it onto the home Wi-Fi using
//      the credentials the user already entered for the stick.
//   3. Once a stock box is reachable, run the in-app STR installer
//      via SSH so the user does not need the PowerShell wizard.
//
// Credentials are kept only in this closure and never persisted.
export async function waitForBoxAfterSetup({ ssid, pass, html, knownBox, wifiForBox, nameForBox, langForBox, tzForBox, format24ForBox }) {
  // baseHtml is the "Installing STR over the network on X..." progress lead,
  // prepended to every render so the user keeps the context while it works. It
  // must be cleared once the install finishes, or the "Installing..." line sits
  // stuck above the "STR is installed and running" result (#852).
  let baseHtml = html;
  const setupResult = $('setupResult');
  if (!setupResult) return;
  const render = (extra) => { setupResult.innerHTML = baseHtml + extra; };

  // The await panel deliberately renders BELOW the stick wizard, because a user
  // looking at the button they just pressed never scrolls up. The install then
  // renders here, ABOVE it, so from this moment the progress was off screen
  // while the now-finished "do this on the speaker now" steps stayed on it. Take
  // the old panel down and bring the user to where the work actually is.
  const awaitPanel = $('setupAwaitResult');
  if (awaitPanel && awaitPanel !== setupResult) awaitPanel.innerHTML = '';
  try { setupResult.scrollIntoView({ behavior: 'smooth', block: 'start' }); } catch { /* older webview */ }

  // 5 minutes max. Computed up front so progressLine + tick share
  // the same deadline.
  const deadline = Date.now() + 5 * 60 * 1000;

  function progressLine() {
    const remaining = formatRemaining(deadline - Date.now());
    return `<div class="muted small">${escapeHtml(t('setup.waitingForBox', { remaining }))}</div>`;
  }

  // Smooth 1 s countdown so the value ticks predictably between
  // polling cycles. The earlier "elapsed seconds, rendered once per
  // poll" pattern made the number jump 3-6 seconds at a time and
  // sometimes appeared frozen during a slow Discover call.
  let tickHandle = null;
  const startTicker = () => {
    if (tickHandle) return;
    render(progressLine());
    tickHandle = setInterval(() => render(progressLine()), 1000);
  };
  const stopTicker = () => {
    if (tickHandle) { clearInterval(tickHandle); tickHandle = null; }
  };

  // knownBox short-circuits the 5-minute discovery wait: an OTA/network
  // install (startNetworkInstall) already has a reachable box in hand, so we
  // skip straight to the install with no ticker and no polling.
  let foundBox = knownBox || null;

  // Honour the target the user picked in Step 0 if any. Without
  // this the loop would lock onto an arbitrary speaker on a LAN
  // with multiple Bose units — exactly the failure mode in
  // #44 where the install ran against the wrong speaker.
  //
  // Three cases:
  //   - target.kind === 'stock' / 'str' with a host  → only accept
  //     a discovered box whose host matches. mDNS may briefly drop
  //     the target during reboot; the loop keeps trying for 5 min.
  //   - target.kind === 'factory-reset'              → ignore the
  //     LAN entirely on the first pass; jump straight to setup-AP
  //     scan + cold-bootstrap. Once bootstrap finishes the result's
  //     boxIP is treated as the target host from then on.
  //   - target null (legacy, can happen if Step 0 was skipped)     → fall
  //     back to "first stock/STR box that shows up on the LAN" so
  //     the old behaviour is preserved on edge paths.
  const target = state.setupTarget;
  const isFactory = target && target.kind === 'factory-reset';
  const wantedHost = (target && target.box) ? target.box.host : '';

  // Up to 5 minutes to find a reachable box via mDNS discovery on the
  // home LAN. The cold-bootstrap path (PC joins the speaker's setup
  // network, pushes Wi-Fi credentials over the air) was removed in
  // 2026-05 because (a) it required the Windows location permission
  // on Win11 24H2 — Microsoft ties any `netsh wlan show networks`
  // call to that, and a music app asking for location is a real
  // trust hit — and (b) the stick-based install does the same
  // provisioning without ever needing the PC to leave the home Wi-Fi.
  // Anyone with a stock or factory-reset speaker now writes the stick
  // here, inserts it, power-cycles the speaker; the speaker joins
  // home Wi-Fi from the stick's wlan.conf on its own.
  if (!foundBox) startTicker();
  while (Date.now() < deadline && !foundBox) {
    try {
      const list = await DiscoverBoxes(4);
      if (wantedHost) {
        foundBox = (list || []).find(b => b && b.host === wantedHost);
      } else {
        foundBox = (list || []).find(b => b && b.host && (b.kind === 'stock' || b.kind === 'str'));
      }
      if (foundBox) break;
    } catch {}

    // Setup-AP fallback. A factory-fresh wireless-only speaker
    // (SoundTouch Portable, variant "taigan") cannot join home Wi-Fi
    // until the stick install has run once. mDNS never sees it on
    // the home LAN because it is on its own setup-AP subnet at
    // 192.168.1.1. When the user temporarily joins their laptop to
    // "Bose SoundTouch Wi-Fi Network" the probe lights up and we
    // feed the synthetic stock BoxInfo straight into the existing
    // install path. The probe is a single TCP dial — no Wi-Fi scan,
    // no location permission. Skipped when the user pinned a
    // specific home-LAN target in Step 0.
    if (!foundBox && !wantedHost) {
      try {
        const ap = await ProbeSetupAP();
        if (ap && ap.host) {
          foundBox = ap;
          break;
        }
      } catch {}
    }

    await sleep(3000);
  }
  stopTicker();

  if (!foundBox) {
    render(`<div class="setup-warn">${escapeHtml(t('setup.waitForBoxTimeout'))}</div>`);
    return;
  }

  if (foundBox.kind === 'str') {
    // Show "already runs STR" PLUS an inline "Update agent" CTA so
    // users in this state are not stuck. Without the button the
    // setup view is a dead end: it told the user STR is installed,
    // but the only way to actually push a newer embedded agent to
    // the box was a separate OTA banner elsewhere in the UI that
    // multiple testers missed. Observed live in #60: tester wrote
    // a fresh v0.5.9 stick to upgrade a v0.5.5 box, the setup
    // screen said "Nothing to install" and they had no way to
    // proceed. The button calls the existing doBoxUpdate() flow
    // which uploads the embedded ARM binary over /api/agent/update
    // and waits for the box to come back on the new build.
    state.currentBox = foundBox;
    render(`<div class="setup-ok">${escapeHtml(t('setup.alreadyInstalled', { ip: foundBox.host }))}</div>` +
           `<div class="setup-ok-actions">` +
             `<button id="setupUpdateAgentBtn" class="btn">${escapeHtml(t('setup.alreadyInstalledUpdateBtn'))}</button>` +
             `<div class="muted small">${escapeHtml(t('setup.alreadyInstalledUpdateHint'))}</div>` +
           `</div>`);
    const btn = $('setupUpdateAgentBtn');
    if (btn) {
      btn.onclick = () => { deps.doBoxUpdate(); };
    }
    deps.discoverBoxes();
    return;
  }

  // Stock box (LAN-found or cold-bootstrapped): run installer.
  const installBase = `<div class="setup-ok">${escapeHtml(t('setup.boxFoundOnLAN', { ip: foundBox.host }))}</div>`;
  const runningLine = `<div class="muted small">${escapeHtml(t('setup.installRunning'))}</div>`;
  // Live install checklist: one row per backend phase plus an independent JS
  // mm:ss timer, so the long silent stretches (the 3-5 min :17000 unlock and the
  // up-to-240s agent wait) always read as alive instead of a frozen "installing"
  // line. Phases are emitted by install_str.go (access/copy/restart/wait); the
  // settle event folds into the copy row as a sub-detail, and a firewall hint
  // appears if the factory-reset bootstrap gets 0 callbacks (margeHits) - the
  // exact silent failure the maintainer hit.
  void installBase; void runningLine;
  const PHASES = ['access', 'copy', 'restart', 'wait'];
  const phaseLabels = {
    access: t('setup.phase.access'), copy: t('setup.phase.copy'),
    restart: t('setup.phase.restart'), wait: t('setup.phase.wait'),
  };
  // Rough per-step durations from real installs (maintainer OTA journal, live
  // Portable/ST300 runs, the code's own wait budgets), so the long silent
  // stretches read as expected rather than stuck.
  const phaseEtas = {
    access: t('setup.phaseEta.access'), copy: t('setup.phaseEta.copy'),
    restart: t('setup.phaseEta.restart'), wait: t('setup.phaseEta.wait'),
  };
  let curPhase = 0, copyDetail = '', accessHint = '';
  const installStartMs = Date.now();
  const fmtMS = (ms) => formatClock(Math.round(ms / 1000));
  const chkRow = (label, st, detail, eta) => {
    const dot = st === 'done' ? '<span class="chk-ico chk-done">&#10003;</span>'
      : st === 'run' ? '<span class="chk-ico chk-spin"></span>'
      : '<span class="chk-ico chk-pend"></span>';
    const etaHtml = eta ? `<span class="chk-eta muted small">${escapeHtml(eta)}</span>` : '';
    return `<div class="chk-row chk-${st}">${dot}<span class="chk-lbl">${escapeHtml(label)}</span>${etaHtml}</div>` +
      (detail ? `<div class="chk-detail muted small">${detail}</div>` : '');
  };
  const renderChecklist = () => {
    let rows = '';
    PHASES.forEach((ph, i) => {
      const st = i < curPhase ? 'done' : i === curPhase ? 'run' : 'pend';
      const detail = (ph === 'copy' && st === 'run') ? copyDetail : (ph === 'access' && st === 'run') ? accessHint : '';
      // The estimate shows while a step is pending or running; once done the
      // real time is history and the suffix disappears.
      rows += chkRow(phaseLabels[ph], st, detail, st === 'done' ? '' : phaseEtas[ph]);
    });
    render(`<div class="setup-checklist"><div class="chk-head"><span>${escapeHtml(t('setup.installRunning'))}</span>` +
      `<span class="chk-timer">${fmtMS(Date.now() - installStartMs)}</span></div>${rows}` +
      `<div class="chk-reassure muted small">${escapeHtml(t('setup.cardReassure'))}</div></div>`);
  };
  const timerHandle = setInterval(renderChecklist, 1000);
  renderChecklist();
  const offProgress = EventsOn('install:progress', (p) => {
    if (!p) return;
    if (p.phase === 'settle') {
      if (p.busy) {
        const load = (typeof p.load === 'number') ? p.load.toFixed(2) : '?';
        const secs = Math.max(0, Math.round((p.remainingMs || 0) / 1000));
        copyDetail = escapeHtml(t('setup.installBoxBusy', { load, secs }));
        if (curPhase < 1) curPhase = 1; // settle maps onto the copy row
      } else {
        copyDetail = '';
      }
    } else {
      const idx = PHASES.indexOf(p.phase);
      if (idx > curPhase) curPhase = idx; // mark-earlier-done, never go backwards
      if (p.phase === 'access') {
        if (typeof p.margeHits === 'number' && p.margeHits === 0 && (p.elapsedMs || 0) > 60000) {
          accessHint = escapeHtml(t('setup.installFirewallHint'));
          // Let main.js re-read the Windows Firewall rules once, so a real
          // block gets its banner with the one-click fix.
          try { window.dispatchEvent(new CustomEvent('str:firewall-suspect')); } catch { /* no window in tests */ }
        } else if (p.margeHits > 0) {
          accessHint = '';
        }
      }
    }
    renderChecklist();
  });
// installFailureReportHtml renders the shell of the copyable account of an
// install that did not reach its goal, the same one the update flow shows. A
// user should never have to describe a failure they cannot see.
//
// The shell is SYNCHRONOUS and the text arrives afterwards (fillFailReport).
// Gathering the facts now costs a couple of seconds of probes, and holding the
// whole failure screen blank for those seconds, on a screen the user is
// already staring at after a failure, reads as a second hang.
//
// The Save-diagnostics button lives here rather than only in the help
// checklist because all three ways an install can end up here need it: the
// thrown-error path and the verify path used to render this block with no
// button at all, and the report's own closing line then pointed at a file the
// user had no way to produce.
function installFailureReportHtml() {
  return `<div class="failreport-inline">`
    + `<p class="muted small">${escapeHtml(t('update.reportHelp'))}</p>`
    + `<div class="muted small" id="setupFailPending">${escapeHtml(t('update.reportCollecting'))}</div>`
    + `<div class="setup-tips hidden" id="setupFailTips"></div>`
    + `<textarea class="failreport-text hidden" id="setupFailReport" readonly rows="14"></textarea>`
    + `<div class="failreport-actions">`
    + `<button class="btn btn-mini" id="setupFailCopy" disabled>${escapeHtml(t('update.reportCopy'))}</button> `
    + `<button class="btn btn-mini" id="setupFailSaveLogs">${escapeHtml(t('footer.saveLogs'))}</button>`
    + `</div>`
    + `<p class="muted small">${escapeHtml(t('update.reportAttachHint'))}</p>`
    + `</div>`;
}

// fillFailReport asks the backend for the report and drops it into the shell.
// Best effort throughout: a report that cannot be produced leaves the save
// button, which is the half that still works without the speaker.
async function fillFailReport(box, phase, errMsg) {
  const ta = document.getElementById('setupFailReport');
  const pending = document.getElementById('setupFailPending');
  const copy = document.getElementById('setupFailCopy');
  if (!ta) return;
  let report = '';
  if (box && box.host) {
    try {
      report = await UpdateFailureReport(box.host, box.port || 0, phase, errMsg, '');
    } catch { report = ''; }
  }
  // The screen may have moved on (a retry, the SSH repair) while the probes
  // ran; do not resurrect a block that is no longer in the document.
  if (!ta.isConnected) return;
  if (pending) pending.classList.add('hidden');
  if (!report) return;
  ta.value = report;
  ta.classList.remove('hidden');
  if (copy) copy.disabled = false;
  // The report's own verdict, lifted out of the text and shown where the
  // user is looking. Before, the steps sat at the bottom of a long report the
  // user was told to mail, so the first thing many did was mail it (Jens,
  // 2026-09-06: "ein paar Schritte, die er selbst testen kann").
  const tips = document.getElementById('setupFailTips');
  const m = report.match(/\nwhat to try\n-+\n([\s\S]*?)\n\nPlease send this text/);
  if (tips && m) {
    const paras = m[1].split(/\n\n+/).map(p => p.trim()).filter(Boolean);
    tips.innerHTML = `<b>${escapeHtml(t('update.whatToTry'))}</b>` + paras.map(p => `<p>${escapeHtml(p)}</p>`).join('');
    tips.classList.remove('hidden');
  }
}

// wireInstallFailureReport makes the copy and save buttons work after the HTML
// landed. Takes the speaker so the bundle pulls its box-side logs when SSH is
// still open, which it usually is right after a failed install.
function wireInstallFailureReport(box) {
  const btn = document.getElementById('setupFailCopy');
  const ta = document.getElementById('setupFailReport');
  if (btn && ta) {
    btn.onclick = async () => {
      try { await navigator.clipboard.writeText(ta.value); }
      catch { ta.select(); document.execCommand('copy'); }
      btn.textContent = t('update.reportCopied');
    };
  }
  const save = document.getElementById('setupFailSaveLogs');
  if (save) save.onclick = () => saveFailReportBundle(save, box, ta);
}

// saveFailReportBundle writes the full diagnostic bundle from a failure screen
// and then rewrites the report so it names the file that now exists.
//
// A failed install is exactly when a user has no idea where to find that file,
// and the previous closing line ("together with the diagnostic file if you
// were able to save one") was a wish, not a path. README + app.log are always
// written, so a speaker that never became reachable still produces a bundle
// carrying everything the APP knows.
async function saveFailReportBundle(btn, box, ta) {
  btn.classList.add('working');
  try {
    const r = await SaveDiagnosticBundle(failReportSaveHosts(box, state.boxes), true);
    if (r && r.savePath) {
      showToast(t('footer.saveLogsDone', { path: r.savePath, size: Math.round((r.bytes || 0) / 1024) }));
      if (ta && ta.value) ta.value = appendSavedBundlePath(ta.value, r.savePath);
    }
  } catch (e) { showError(String(e)); }
  finally { btn.classList.remove('working'); }
}

// verifyInstalledState holds the install open until the speaker really is
// there. Same contract as the update: the speaker's own report decides, the
// state is re-checked throughout, and the Spotify engine is delivered inside
// this flow because nothing is allowed to deliver it afterwards in the
// background. The engine goes out through the update's own gate (agent on the
// build this app carries, then settled); the loop lives in agentsettle.js so
// it can be tested without a speaker.
function verifyInstalledState(box, onState) {
  const probe = () => BoxAgentVersion(box.host, box.port || 0);
  return verifyInstalledLoop({
    probe,
    ensureEngine: () => EnsureSpotifyEngine(box.host, box.port || 0),
    waitStable: (deadlineMs) => waitForStableAgent(probe, deadlineMs),
    appSha: (state.appInfo && state.appInfo.agentSha256) || '',
    onState,
  });
}

  let result;
  // A first install writes software to a speaker just like an update does,
  // so it plays by the same rules: the window asks before closing while this
  // runs, and the flow does not consider itself finished until the speaker is
  // really in the state it was supposed to reach.
  // Write the target down before anything is copied, so an install cut short
  // by a closed window is still known about the next time this speaker is
  // opened, instead of looking like a speaker that was never touched.
  try {
    RecordUpdateIntent(foundBox.host, foundBox.port || 0,
      (state.appInfo && state.appInfo.version) || '', foundBox.deviceID || '',
      foundBox.friendlyName || foundBox.name || foundBox.host, true);
  } catch {}
  // A speaker that is not answering at all cannot be installed onto, and the
  // failure it produces blames the wrong thing. The install ends in "this is
  // usually a firewall or antivirus", which is provably false whenever the app
  // is talking to another speaker at the same moment over the very same
  // network path. A user with three working speakers and one silent one then
  // goes hunting through his antivirus settings for nothing (field 2026-08-05:
  // "an der vierten beisse ich mir die Zaehne aus", two attempts a minute
  // apart, every port on that speaker dead while the others answered fine).
  //
  // So say what is actually true before starting: this speaker is not
  // answering, and here is why we know it is not your PC.
  const othersLive = (state.boxes || []).some(b =>
    b && b.host && b.host !== foundBox.host && !b.offline);
  if (foundBox.offline && othersLive) {
    clearInterval(timerHandle);
    if (offProgress) offProgress();
    render(`<div class="setup-err">${escapeHtml(t('setup.installOfflineOthersLive'))}</div>`);
    return;
  }

  try { SetOTARunning(true); } catch {}
  try {
    result = await InstallSTROnBox(foundBox.host, foundBox.model || foundBox.type || '');
  } catch (err) {
    clearInterval(timerHandle);
    if (offProgress) offProgress();
    try { SetOTARunning(false); } catch {}
    const busyMsg = busyRefusalText(err, foundBox.friendlyName || foundBox.name || foundBox.host); // '' unless the write guard refused the start
    render(`<div class="setup-err">${escapeHtml(busyMsg || t('setup.installFailed', { msg: String(err) }))}</div>`
      + (busyMsg ? '' : installFailureReportHtml()));
    wireInstallFailureReport(foundBox);
    fillFailReport(foundBox, 'install', String(err));
    return;
  }
  clearInterval(timerHandle);
  if (offProgress) offProgress();
  // The install guard (install_guard.go) stopped the install before it touched
  // the speaker: an update from this app is still finishing on it, or STR is
  // already running there. Neither is a failure, so neither gets the failure
  // screen. That screen offers the SSH repair, the power-cycle block, the
  // failure report and, for some codes, the USB stick section, and every one of
  // those is the wrong next step for a speaker whose update simply needs a few
  // more minutes (field case 2026-10-21: a slow but successful update, then a
  // stick install on top of it, then a dark speaker).
  if (result && !result.ok && (result.code === 'update-finishing' || result.code === 'str-running')) {
    try { SetOTARunning(false); } catch {}
    baseHtml = '';
    if (result.code === 'str-running') {
      state.currentBox = foundBox;
      render(`<div class="setup-ok">${escapeHtml(t('setup.alreadyInstalled', { ip: foundBox.host }))}</div>`
        + `<div class="muted small">${escapeHtml(t('setup.installRefusedRemoveStick'))}</div>`
        + `<div class="setup-ok-actions">`
        + `<button id="setupUpdateAgentBtn" class="btn">${escapeHtml(t('setup.alreadyInstalledUpdateBtn'))}</button>`
        + `<div class="muted small">${escapeHtml(t('setup.alreadyInstalledUpdateHint'))}</div>`
        + `</div>`);
      const upd = $('setupUpdateAgentBtn');
      if (upd) upd.onclick = () => { deps.doBoxUpdate(); };
    } else {
      render(`<div class="setup-warn">${escapeHtml(t('setup.installUpdateFinishing'))}</div>`
        + `<div class="muted small">${escapeHtml(t('setup.installRefusedRemoveStick'))}</div>`);
    }
    deps.discoverBoxes();
    return;
  }
  if (!result || !result.ok) {
    const msg = (result && result.message) || 'unknown';
    const help = installHelpHtml(result && result.code, !!knownBox, foundBox);
    const log = (result && result.log)
      ? `<details class="setup-log"><summary>${escapeHtml(t('setup.installLogToggle'))}</summary><pre>${escapeHtml(result.log)}</pre></details>`
      : '';
    // SSH repair fallback (F): offer it when the install failed in a way the
    // SSH-copy-to-NAND path can rescue (an unreadable/faulty stick, an install
    // script error, or a timeout) and SSH was reachable enough to even start.
    // It bypasses the stick by staging the embedded files on NAND over SSH.
    // stick-usb-power is included: the SSH repair stages the embedded files onto
    // NAND over Wi-Fi and never reads the stick, so it sidesteps the dead USB
    // port entirely, the strongest one-click recovery for the power case.
    const repairCodes = ['stick-io-error', 'stick-usb-power', 'install-error', 'install-timeout', 'install-script-error', 'stick-copy-failed'];
    const canRepair = result && repairCodes.indexOf(result.code) >= 0;
    const repairBtn = canRepair
      ? `<div class="setup-repair" style="margin-top:12px">`
        + `<button class="btn btn-primary btn-mini" id="installRepairSSH">${escapeHtml(t('setup.repairSSHBtn'))}</button>`
        + ` <span class="muted small">${escapeHtml(t('setup.repairSSHHint'))}</span></div>`
      : '';
    // Coprocessor power-cycle advice: a soft reboot does not reset the box's
    // coprocessor, so a sweeping light bar / dead playback often only clears with
    // a full mains power-cycle (live-proven). Shown on every failure screen.
    const powerCycleHint = powerCycleAdviceHtml(foundBox);
    try { SetOTARunning(false); } catch {}
    // Neither "speaker-not-back" nor "speaker-in-setup" is a proven failure.
    // The install ran; the speaker was either not back on the network when
    // the wait ran out, or busy with the firmware's own out-of-box setup,
    // which takes it off the network for minutes at a time (#873). The
    // backend keeps looking in both cases, so this screen is a WAIT, not a
    // verdict: no repair button, no power-cycle block, and above all no
    // failure report and no "press Install again", which is precisely what
    // produced a second and worse failure on one reporter's speaker.
    const inSetup = !!(result && result.code === 'speaker-in-setup');
    const waiting = inSetup || !!(result && result.code === 'speaker-not-back');
    if (waiting) {
      renderInstallWaiting(inSetup, msg, help, log,
        outdatedFirmwareHtml(foundBox, result && result.firmware, result));
      return;
    }
    const headline = t('setup.installFailed', { msg });
    // The firmware route, right here, because a stock speaker cannot open the
    // settings section the message points at (see outdatedFirmwareHtml).
    const fwBlock = outdatedFirmwareHtml(foundBox, result && result.firmware, result);
    render(`<div class="setup-err">${escapeHtml(headline)}</div>`
      + fwBlock + help + repairBtn + powerCycleHint + installFailureReportHtml() + log);
    wireOutdatedFirmwareLinks();
    wireInstallFailureReport(foundBox);
    fillFailReport(foundBox, 'install:' + ((result && result.code) || 'unknown'), msg);
    // If the network path genuinely cannot proceed (no install window, box not
    // reachable, controls wedged), reveal the USB-stick fallback (relocated into
    // <details id="setupStickDetails">) so the user has an immediate next step.
    // Not on a model that never reads a stick at boot: the section would offer
    // a route that speaker cannot take (a Wave owner wrote a stick for nothing,
    // 2026-10-22).
    if (result && ['install-window-closed', 'not-reachable', 'control-unresponsive'].indexOf(result.code) >= 0
        && !neverReadsStick(foundBox, result)) {
      const stickDetails = $('setupStickDetails');
      if (stickDetails) stickDetails.open = true;
    }
    const repEl = $('installRepairSSH');
    if (repEl) {
      repEl.onclick = async () => {
        repEl.disabled = true;
        render(`<div class="muted">${escapeHtml(t('setup.repairSSHRunning'))}</div>`);
        try {
          const rr = await RepairInstallViaSSH(foundBox.host, foundBox.model || foundBox.type || '');
          if (rr && rr.ok) {
            render(`<div class="setup-ok">${escapeHtml(t('setup.installDone'))}</div>`
              + `<div class="muted small">${escapeHtml(t('setup.installDoneHint'))}</div>`
              + powerCycleAdviceHtml(foundBox)
              + takeShareOffer());
            wireShareOffer();
            deps.discoverBoxes();
            try { deps.celebrateProvision(foundBox); } catch {}
          } else {
            const m2 = (rr && rr.message) || 'unknown';
            const log2 = (rr && rr.log)
              ? `<details class="setup-log"><summary>${escapeHtml(t('setup.installLogToggle'))}</summary><pre>${escapeHtml(rr.log)}</pre></details>`
              : '';
            render(`<div class="setup-err">${escapeHtml(t('setup.repairSSHFailed', { msg: m2 }))}</div>` + log2);
          }
        } catch (e) {
          render(`<div class="setup-err">${escapeHtml(busyRefusalText(e, foundBox.friendlyName || foundBox.name || foundBox.host) || t('setup.repairSSHFailed', { msg: String(e) }))}</div>`);
        }
      };
    }
    return;
  }
  // renderInstallWaiting is the screen for an install that is not finished and
  // not failed: the software is on the speaker, the speaker is not answering
  // yet, and the backend is still looking (installwait.go's watcher).
  //
  // What it deliberately does NOT show: a failure report, a repair button, or
  // any invitation to install again. Reporter A read a checklist telling him to
  // press Install, pressed it, and got a second and much worse failure out of a
  // speaker that was working the whole time.
  //
  // The ONE exception is the SoundTouch 300, and it is not a judgement call.
  // That soundbar always ends an install in the alternating-yellow blink state
  // with every port dead, and it does not come back until it is unplugged once.
  // On that speaker "we are still looking, check your Wi-Fi" is a wait with no
  // end: the third 300 owner in a week (2026-09-24) reset his soundbar and
  // re-entered his Wi-Fi in the Bose app twice over, because the one instruction
  // that would have finished the install was on a screen he never reached. So a
  // 300 gets the mandatory power-cycle step here, above the checklist. Nothing
  // else about this screen changes, and no other model sees it.
  //
  // It is NOT a screen with no way out, either. It carries the two actions the
  // help text under it already promises: save the diagnostic logs (the
  // checklist says "use the button below", and there has to be a button), and
  // stop waiting, which goes straight to the give-up screen instead of leaving
  // the user with a quarter of an hour of spinner and no control.
  function renderInstallWaiting(inSetup, msg, help, log, fwBlock) {
    let offLate = null, offWaiting = null;
    // The install is still in flight for as long as this panel is: the backend
    // watcher is looking, and a late answer still has somewhere to land.
    installWaitPanelLive = true;
    const heroNow = $('setupHeroInstall');
    if (heroNow) heroNow.disabled = true;
    const stopAll = () => {
      for (const off of [offLate, offWaiting]) { if (off) { try { off(); } catch {} } }
      offLate = offWaiting = null;
      installWaitPanelLive = false;
      const hero = $('setupHeroInstall');
      if (hero) hero.disabled = !!currentHeroHold();
    };
    // The panel owns a marker element, so a screen the user has navigated away
    // from is never overwritten by a late event.
    const mine = () => !!setupResult.querySelector('#setupWaitPanel');
    // forUs decides whether an event still concerns this panel, and UNSUBSCRIBES
    // when it does not. Without that the two listeners outlive the screen: the
    // single look this replaced dropped its own listener on a six-minute timer,
    // and a bare "return when the panel is gone" would have leaked both of them
    // for the life of the app, once per install the user starts.
    const forUs = (p) => {
      if (!p || p.host !== foundBox.host) return false;
      if (!mine()) { stopAll(); return false; }
      return true;
    };
    // Belt and braces for the case where no further event ever arrives (the app
    // context died with the watcher): longer than either ceiling, so it can
    // never cut a live wait short.
    setTimeout(stopAll, 20 * 60 * 1000);
    let remainingMs = 0;
    // The "Installing STR over the network on X..." lead is stale the moment
    // this panel goes up, and leaving it there under a wait message is the
    // stuck-progress-line trap of #852 all over again.
    baseHtml = '';
    // giveUp is the end of the wait, from the watcher's ceiling OR from the
    // user pressing the button. One render, so both routes tell the same story.
    const giveUp = () => {
      baseHtml = '';
      // A speaker that answered nothing for the whole wait gets the staged
      // checklist in place of the generic one (darkafterupdate.js): give it
      // time, the power supply, the Bose app, the resets and the Bose
      // firmware by stick. Several of these turned out to be a power supply
      // that gave out at the restart. The setup phase keeps its own list:
      // pulling the plug on a speaker mid-setup is the wrong advice there.
      // So does the SoundTouch 300: its logs-only list and the red
      // power-cycle step are the whole answer (installHelpHtml).
      const steps = (inSetup || !darkChecklistApplies(foundBox))
        ? help + powerCycleAdviceHtml(foundBox)
        : installDarkChecklistHtml(foundBox);
      render(`<div class="setup-err">${escapeHtml(t('setup.installWaitGaveUp'))}</div>`
        + (fwBlock || '') + steps + installFailureReportHtml() + (log || ''));
      wireOutdatedFirmwareLinks();
      wireInstallDarkChecklist();
      wireInstallFailureReport(foundBox);
      fillFailReport(foundBox, 'install:' + (inSetup ? 'speaker-in-setup' : 'speaker-not-back'), msg);
    };
    const draw = () => {
      const head = inSetup ? t('setup.installBoxInSetup') : t('setup.installUnconfirmed', { msg });
      const wait = remainingMs > 0
        ? `<div class="muted small">${escapeHtml(t('setup.installStillWaiting', { remaining: formatRemaining(remainingMs) }))}</div>`
        : '';
      // The 300's power-cycle step comes before the "still waiting" line and
      // the checklist, because on that model it IS the next step.
      const st300 = isSoundTouch300(foundBox) ? powerCycleAdviceHtml(foundBox) : '';
      render(`<div id="setupWaitPanel" class="setup-warn">${escapeHtml(head)}</div>`
        + (inSetup ? `<div class="muted small">${escapeHtml(t('setup.installInSetupPhase'))}</div>` : '')
        + st300 + (fwBlock || '') + wait + help
        + `<div class="failreport-actions" style="margin-top:12px">`
        + `<button class="btn btn-mini" id="setupWaitSaveLogs">${escapeHtml(t('footer.saveLogs'))}</button> `
        + `<button class="btn btn-mini" id="setupWaitStop">${escapeHtml(t('setup.installWaitStopBtn'))}</button>`
        + `</div>`);
      wireOutdatedFirmwareLinks();
      const save = $('setupWaitSaveLogs');
      if (save) save.onclick = () => saveFailReportBundle(save, foundBox, null);
      const stop = $('setupWaitStop');
      if (stop) stop.onclick = () => { stopAll(); giveUp(); };
    };
    draw();
    offWaiting = EventsOn('install:waiting', (p) => {
      if (!forUs(p)) return;
      if (p.inSetup) inSetup = true;
      remainingMs = p.remainingMs || 0;
      draw();
    });
    offLate = EventsOn('install:late', async (p) => {
      if (!forUs(p)) return;
      stopAll();
      if (p.ok) {
        // The speaker answered after all. Carry on with the REST of the
        // install: until now this rendered "it worked" and stopped, so the
        // name, language, timezone and Wi-Fi the user had typed were never
        // written to a speaker the app had just rescued.
        //
        // The banner rides along as baseHtml, which finishInstall clears when
        // it puts up the final result, so the news is on screen for the whole
        // provisioning run and gone once it is old.
        baseHtml = `<div class="setup-ok">${escapeHtml(t(inSetup ? 'setup.installLateSetupOk' : 'setup.installLateOk'))}</div>`;
        await finishInstall();
        return;
      }
      // The watcher reached its ceiling. Now, and only now, this is a failure
      // report worth filling in.
      giveUp();
    });
  }

  // finishInstall is everything that happens AFTER the install itself
  // succeeded: the verify, the name/language/timezone/Wi-Fi provisioning, and
  // the "how to play" panel.
  //
  // It is a function rather than straight-line code because there are now TWO
  // ways to arrive here. The obvious one is an install that returned ok. The
  // other is a speaker that was silent when the wait budget ran out and
  // answered a few minutes later (install:late): that path used to render "the
  // install succeeded after all" and call discoverBoxes(), and stop. Everything
  // the user had typed into the wizard - the name, the language, the timezone
  // and the Wi-Fi - was never written to that speaker, and nothing said so.
  async function finishInstall() {
    // After a successful install, spell out HOW to play. Users repeatedly went
    // back to the Bose app, saw "playback not possible" (dead Bose cloud) and
    // assumed STR was broken (HP Baehr, 2026-06-12). The recurring expectation
    // gap is that playback moved from the Bose app to the STR presets + the
    // speaker's own buttons 1-6, so say it plainly and offer a jump to the tab.
    // Network install (knownBox) succeeded. The box has just restarted its agent,
    // so it is often NOT ready to accept settings for another few seconds. On the
    // real ST300 the name/language/timezone/Wi-Fi silently failed to take because
    // provisioning ran against a still-rebooting box. So: wait for the agent + box
    // to answer again, then apply name -> language -> timezone -> Wi-Fi IN ORDER,
    // each with a short retry, and surface per-step success/failure in a live
    // checklist. All best-effort: a step that cannot be applied never blocks the
    // otherwise-successful install, and the user can set it later in Speaker
    // settings. The Wi-Fi write keeps the box reachable after the Ethernet cable is
    // pulled (agent persists creds to NAND, then wpa live-switch or BCO reboot).
    // The install reported success. That is not the same as the speaker being
    // in the state it was supposed to reach, and nothing will fix it later in
    // the background, so the flow stays here until the speaker itself says so.
    // Verify against the AGENT, not the stock box record. foundBox still carries
    // the pre-install Bose port :8090 that probeStock stamped, and
    // candidatePorts(host, 8090) yields [8090, 8888]: it can never reach :17008,
    // so on a BCO/whitelisted chassis (Portable taigan, ST20 scm) a fully
    // successful install was reported as "The installation did not complete".
    // Worse, :8090 ANSWERS, with a 404, so the failure report's closing probe
    // printed "NOT REACHABLE (status 404)" and blamed the speaker. A field report
    // on 2026-08-22 carried exactly that, down to the "speaker : <ip>:8090" line.
    // port 0 gives candidatePorts [17008, 8888], which is both agent ports. The
    // same trap, with the same fix, is documented on agentBox thirty lines below.
    const agentPortBox = { ...foundBox, port: 0 };
    const installed = await verifyInstalledState(agentPortBox, (st) => {
      render(`<div class="muted">${escapeHtml(!st.reachable
        ? t('updateAll.phase.spotifyUnreachable')
        : (st.engine === 'present' ? t('updateAll.phase.spotifyChecking') : t('updateAll.phase.spotifyInstalling')))}</div>`);
    });
    try { SetOTARunning(false); } catch {}
    if (!installed.ok) {
      // The verify ends this way only when the agent never answered once in
      // the whole window (verifyInstalledLoop): the speaker is dark, so the
      // same staged checklist as the give-up screen goes under the headline.
      // The SoundTouch 300 gets its red power-cycle step instead.
      render(`<div class="setup-err">${escapeHtml(t('setup.installIncomplete'))}</div>`
        + (darkChecklistApplies(foundBox) ? installDarkChecklistHtml(foundBox) : powerCycleAdviceHtml(foundBox))
        + installFailureReportHtml());
      wireInstallDarkChecklist();
      wireInstallFailureReport(agentPortBox);
      fillFailReport(agentPortBox, 'install:verify', installed.reason || '');
      return;
    }
    // Installed, but without the Spotify engine. The speaker works; only Spotify
    // is missing, and running the speaker update once delivers it (nothing does
    // that in the background, by design). Said here as a note, because calling
    // this a failed install sends people hunting for a fault they do not have.
    const engineNote = installed.engineMissing
      ? `<div class="setup-warn">${escapeHtml(t('setup.installedEngineMissing'))}</div>`
      : '';

    let unplugLine = '';
    let provisionFailed = false;
    let failReasons = {};
    if (knownBox) {
      // After install the box is an STR agent, reachable on :17008 (BCO REDIRECT)
      // or :8888 (sm2 direct), NOT the pre-install stock :8090 that foundBox still
      // carries (probeStock stamped it). Provision + probe readiness against the
      // agent port: port 0 => candidatePorts(host, 0) tries 17008 then 8888, so
      // name / Wi-Fi / status never hit the dead-for-this-purpose Bose :8090 (which
      // answers /api/* with a 404 that boxDo/boxFetch would wrongly accept as a
      // reachable response, silently failing name+Wi-Fi and burning the readiness
      // gate's full 90 s). Language + timezone ARE Bose :8090 endpoints and keep
      // using foundBox.host directly (no port), so they are unaffected.
      const agentBox = { ...foundBox, port: 0 };
      // Ordered steps, built only from what the user actually requested.
      const steps = [];
      if (nameForBox) {
        steps.push({ id: 'name', label: t('setup.provisionName'),
          run: () => SetBoxName(agentBox.host, agentBox.port, nameForBox) });
      }
      if (langForBox > 0) {
        steps.push({ id: 'lang', label: t('setup.provisionLang'),
          run: () => SetBoxLanguage(foundBox.host, langForBox) });
      }
      if (tzForBox) {
        // Real IANA zone => the box derives the offset incl. DST itself, so the
        // offset argument MUST stay 0 (a non-zero value would double-shift).
        steps.push({ id: 'tz', label: t('setup.provisionTz'),
          run: () => SetClockDisplay(foundBox.host, true, tzForBox, 0, !!format24ForBox) });
      }
      if (wifiForBox && wifiForBox.ssid) {
        steps.push({ id: 'wifi', label: t('setup.provisionWifi'),
          run: async () => {
            // force:true - this is the FIRST setup and the user just typed this
            // network in. The visibility preflight refused a cable-connected
            // ST30 whose site survey came back empty (live 2026-07-09,
            // "WLAN switch refused ... visible=[]"), silently breaking the
            // Wi-Fi save; on a first install the cable stays in anyway, so the
            // strand-protection the preflight exists for does not apply.
            const wr = await boxFetch(agentBox, '/api/box/wlan', {
              method: 'PUT',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ ssid: wifiForBox.ssid, password: wifiForBox.pass, hidden: !!wifiForBox.hidden, force: true }),
            });
            if (!wr || !wr.ok) {
              let reason = wr ? 'HTTP ' + wr.status : t('setup.provisionNoAnswer');
              try {
                const body = await wr.text();
                try {
                  const j = JSON.parse(body);
                  if (j && j.error) reason = j.error;
                  else if (body) reason = body.slice(0, 160);
                } catch { if (body) reason = body.slice(0, 160); }
              } catch {}
              throw new Error(reason);
            }
            let info = {};
            try { info = await wr.json(); } catch {}
            return info || {};
          } });
      }

      // Live per-step checklist (reuses the .chk-* row styling plus a red-cross
      // fail variant). A leading "waiting for the speaker" row covers the reboot
      // gap before the first step runs.
      const st = {}; steps.forEach(s => { st[s.id] = 'pend'; });
      let waitingAgent = true;
      const chkRowLike = (rowState, label) => {
        const ico = rowState === 'done' ? '<span class="chk-ico chk-done">&#10003;</span>'
          : rowState === 'fail' ? '<span class="chk-ico chk-fail">&#10007;</span>'
          : rowState === 'run' ? '<span class="chk-ico chk-spin"></span>'
          : '<span class="chk-ico chk-pend"></span>';
        return `<div class="chk-row chk-${rowState}">${ico}<span class="chk-lbl">${escapeHtml(label)}</span></div>`;
      };
      const renderProvision = () => {
        const head = `<div class="chk-head"><span>${escapeHtml(t('setup.provisionTitle'))}</span></div>`;
        const agentRow = chkRowLike(waitingAgent ? 'run' : 'done', t('setup.provisionWaitAgent'));
        const rows = steps.map(s => chkRowLike(st[s.id], s.label)).join('');
        render(`<div class="setup-ok">${escapeHtml(t('setup.installDone'))}</div>` +
          `<div class="setup-checklist setup-provision">${head}${agentRow}${rows}</div>`);
      };
      renderProvision();

      // Wait (up to ~90s) for the agent + box :8090 to answer via /api/status
      // (self-healing fetch through the agent). If it never answers we still try
      // the steps below; the per-step retries give more chances.
      const readyDeadline = Date.now() + 90 * 1000;
      while (Date.now() < readyDeadline) {
        try { const r = await boxFetch(agentBox, '/api/status', {}); if (r && r.ok) break; } catch {}
        await sleep(3000);
      }
      waitingAgent = false;
      renderProvision();

      // Apply each step in order with a short retry, reflecting the result live.
      for (const s of steps) {
        st[s.id] = 'run'; renderProvision();
        const res = await retryStep(s.run, 3, 2500);
        st[s.id] = res.ok ? 'done' : 'fail';
        if (!res.ok) {
          provisionFailed = true;
          // Keep the REAL reason: "some settings could not be applied" without
          // saying which and why sent the maintainer log-diving (2026-07-09).
          failReasons[s.id] = { label: s.label, reason: String((res.value && res.value.message) || res.value || t('setup.provisionNoAnswer')) };
        }
        if (s.id === 'wifi') {
          if (res.ok) {
            const info = res.value || {};
            // A cabled wpa speaker only stored the network (#1307): its radio
            // stays off until the cable is out and the speaker restarts.
            unplugLine = info.wiredUplink ? t('setup.unplugWiredWpa')
              : (info.mechanism === 'bco') ? t('setup.unplugSafeBco') : t('setup.unplugSafeWpa');
          } else {
            unplugLine = t('setup.wifiWriteFailed');
          }
        }
        renderProvision();
      }
      if (!(wifiForBox && wifiForBox.ssid)) {
        unplugLine = t('setup.unplugNoWifi');
      }
    }
    const failDetails = Object.values(failReasons)
      .map(f => `<div class="setup-warn-detail muted small">${escapeHtml(f.label)}: ${escapeHtml(f.reason)}</div>`).join('');
    // Install is finished: drop the "Installing..." progress lead so the result
    // stands on its own instead of sitting under a stuck progress line (#852).
    baseHtml = '';
    render(`<div class="setup-ok">${escapeHtml(t('setup.installDone'))}</div>` +
           `<div id="pairAfterInstall"></div>` +
           engineNote +
           (unplugLine ? `<div class="setup-unplug">${escapeHtml(unplugLine)}</div>` : '') +
           (provisionFailed ? `<div class="setup-warn">${escapeHtml(t('setup.provisionSomeFailed'))}${failDetails}</div>` : '') +
           `<div class="muted small">${escapeHtml(t('setup.installDoneHint'))}</div>` +
           `<div class="setup-playhow">` +
             `<h3>${escapeHtml(t('setup.playHowTitle'))}</h3>` +
             `<ol>` +
               `<li>${escapeHtml(t('setup.playHowStep1'))}</li>` +
               `<li>${escapeHtml(t('setup.playHowStep2'))}</li>` +
             `</ol>` +
             `<p class="muted small">${escapeHtml(t('setup.playHowBoseApp'))}</p>` +
             `<button class="btn btn-primary" id="installGoMusic">${escapeHtml(t('setup.playHowGoBtn'))}</button>` +
           `</div>` +
           powerCycleAdviceHtml(foundBox) +
           // Quiet share row, once per computer, below the actual result.
           takeShareOffer());
    wireShareOffer();
    const goMusic = $('installGoMusic');
    if (goMusic) goMusic.onclick = () => deps.switchView('box');
    showPairAfterInstall(foundBox);
    deps.discoverBoxes();
    // The box is alive again: invite the user to drop a pin on the community world
    // map. The most reliable moment to ask, and the one most users reach.
    try { deps.celebrateProvision(foundBox); } catch {}
  }


  await finishInstall();
}
