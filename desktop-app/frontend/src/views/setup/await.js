// views/setup/await.js: the wait between a prepared stick and the install.
// The "do this on the speaker now" panel and the watcher that works out what
// state the speaker is in and only then unlocks the install. Extracted from
// views/setup.js.

import { state } from '../../state.js';
import { $, escapeHtml, sleep, formatRemaining } from '../../utils.js';
import { t } from '../../i18n/index.js';
import { firmwareGuideMount, firmwareGuideNeeded } from '../../fwguide.js';
import { BoxInstallReachable, ProbeSetupAP, GetBoxFirmware, DiscoverBoxes } from '../../api.js';
import { deps } from './deps.js';
import { wireOutdatedFirmwareLinks } from './installhelp.js';
import { waitForBoxAfterSetup } from './install.js';

// showAwaitBoxReadyPanel renders the "do this on the speaker now" instructions
// plus a confirm button that stays DISABLED while a background watcher works out
// what state the speaker is in. The button only unlocks once the speaker is
// found AND reachable for install; until then the watcher reports clearly what
// it sees, including the common "speaker booted without the stick" case, so
// users stop dropping out at this fragile step.
export function showAwaitBoxReadyPanel({ ssid, pass, html }) {
  // Render the "do this on the speaker now" instructions at the VERY END,
  // below the stick wizard and the "prepare USB stick" button (setupAwaitResult
  // sits after the </details>), not in setupResult which is ABOVE the button.
  // Otherwise the user, looking at the button they just pressed, never scrolls
  // up to see the next steps and drops out here.
  const target = $('setupAwaitResult') || $('setupResult');
  if (!target) return;
  const oldResult = $('setupResult');
  if (oldResult && target !== oldResult) oldResult.innerHTML = '';
  target.innerHTML = html +
    `<div class="setup-section setup-await-ready">` +
    `<div class="setup-ok"><b>${escapeHtml(t('setup.awaitBoxReadyTitle'))}</b></div>` +
    `<ol class="setup-await-steps">` +
    `<li>${escapeHtml(t('setup.awaitStep1'))}</li>` +
    `<li>${escapeHtml(t('setup.awaitStep2'))}</li>` +
    `<li>${escapeHtml(t('setup.awaitStep3'))}</li>` +
    `</ol>` +
    `<div class="muted small">${escapeHtml(t('setup.awaitPrecondition'))}</div>` +
    `<div class="setup-await-status" id="setupAwaitStatus"></div>` +
    `<button class="btn btn-primary" id="setupSpeakerReady" disabled>${escapeHtml(t('setup.awaitConfirmWaiting'))}</button>` +
    `</div>`;
  try { target.scrollIntoView({ behavior: 'smooth', block: 'start' }); } catch { /* older webview */ }
  // Retiring any watcher already running is not optional. This panel is reached
  // from five places, including a link inside the watcher itself, and the loop
  // only ends on ready, abort or its own six-minute deadline. Two of them alive
  // at once share one status line and one confirm button: they overwrite each
  // other's text, and the older one can unlock the button for a speaker the
  // newer one has already moved on from.
  watchGeneration++;
  watchForSpeakerReady({ ssid, pass, html, generation: watchGeneration });
}

// watchGeneration retires an earlier watcher when a new panel takes over. Only
// the newest one owns the status line and the confirm button.
let watchGeneration = 0;

// watchForSpeakerReady is the idiot-proof background watcher for the final setup
// step. It polls every ~3s and classifies the speaker into one of several plain
// states so a non-technical user always knows what to do and the install button
// only unlocks when the speaker is genuinely ready. Key cases it tells apart:
//   - still searching / stalled (speaker not on the network yet),
//   - found but just booting (grace window, do not blame the stick yet),
//   - on the network but silent (listed by discovery, :8090 never answered past
//     the grace): nothing can be said about its firmware, so say that instead of
//     repeating "still booting" for six minutes,
//   - booted WITHOUT the stick (on the network, SSH off, firmware current): the
//     most common drop-out; reseat + power-cycle and it auto-recovers,
//   - firmware too old (on the network, SSH off, firmware outdated/unparsable):
//     update via the Bose app first; a "try anyway" escape never hard-blocks,
//   - ready (SSH open) / already STR / a factory-fresh setup network,
//   - wrong/multiple speakers (a pinned target that is not the one online).
// A separate 1s ticker keeps the countdown live between polls.
async function watchForSpeakerReady({ ssid, pass, html, generation }) {
  const statusEl = $('setupAwaitStatus');
  const btn = $('setupSpeakerReady');
  if (!statusEl || !btn) return;
  // mine() is false the moment a newer panel has taken over, and every write to
  // the shared status line and the shared button goes through it.
  const mine = () => generation === watchGeneration;

  const wantedHost = (state.setupTarget && state.setupTarget.box) ? state.setupTarget.box.host : '';
  const watcherStart = Date.now();
  const deadline = watcherStart + 6 * 60 * 1000;
  const GRACE_MS = 90 * 1000;
  const fwCache = {}; // host -> { at, info }; firmware does not change mid-watch
  // TWO timestamps, and the difference between them is the whole point.
  //
  // firstSeenOnNetwork is when discovery first listed the speaker. It is never
  // reset while it keeps being listed, so the grace below actually elapses.
  //
  // lastAnsweredBosePort is when its :8090 last answered, which separates a
  // speaker restarting mid-watch from one that has never spoken at all. Only one
  // variable used to exist, it held the second meaning, and it was set back to 0
  // on every :8090 miss. A speaker that is listed but never answers therefore restarted
  // the 90-second grace 120 times over the six-minute budget and read "it is
  // still booting" the whole way, which is the reassuring message, while the two
  // states that carry an instruction (firmware too old, booted without the
  // stick) were reachable only by a speaker that WAS answering. The longer it
  // was broken, the calmer the screen got.
  let firstSeenOnNetwork = 0;
  let lastAnsweredBosePort = 0;
  let lastSeenState = '';
  let ready = false;
  let aborted = false; // a link (try-anyway / choose-different) took over

  const setStatus = (cls, txt, extraHtml) => {
    if (!mine()) return;
    statusEl.innerHTML = `<span class="${cls}">${escapeHtml(txt)}</span>` + (extraHtml || '');
  };

  // The 1s ticker only refreshes the countdown while we are in a searching
  // state (liveSearchKey set); other states show a steady message and issue no
  // probes from the ticker.
  let liveSearchKey = null;
  let ticker = setInterval(() => {
    if (!liveSearchKey) return;
    setStatus('muted small', t(liveSearchKey, { remaining: formatRemaining(deadline - Date.now()) }));
  }, 1000);
  const stopTicker = () => { if (ticker) { clearInterval(ticker); ticker = null; } };

  const getFw = async (host) => {
    const c = fwCache[host];
    if (c && (Date.now() - c.at) < 15000) return c.info;
    try { const f = await GetBoxFirmware(host); if (f && f.reachable) { fwCache[host] = { at: Date.now(), info: f }; return f; } } catch {}
    return null;
  };
  const arm = (label, onclick) => {
    if (!mine()) return;
    liveSearchKey = null; btn.textContent = label; btn.disabled = false; btn.onclick = onclick;
  };
  const handoff = () => {
    btn.disabled = true; aborted = true; stopTicker();
    // The stick is in the speaker and the wizard that wrote it is over. Folding
    // that section away puts the install panel, which sits above it, back within
    // a screen of where the user is looking. A failure that genuinely needs the
    // stick re-opens the section itself.
    const stickDetails = $('setupStickDetails');
    if (stickDetails) stickDetails.open = false;
    waitForBoxAfterSetup({ ssid, pass, html });
  };

  while (Date.now() < deadline && !ready && !aborted && mine()) {
    let list = [];
    try { list = (await DiscoverBoxes(4)) || []; } catch {}
    // No target pinned: match only an STR-FREE (stock) speaker, the one we are
    // here to install. An already-STR speaker on the network is NOT the target
    // of a fresh stick setup, so it is ignored and we keep waiting for the new
    // speaker, instead of misleadingly reporting "already installed" against it.
    // (The already-STR state still fires when the user explicitly picked an STR
    // speaker as the target, i.e. wantedHost matches it.)
    const cand = wantedHost
      ? list.find(b => b && b.host === wantedHost)
      : list.find(b => b && b.host && b.kind === 'stock');

    // Wrong/multiple speakers: a target was pinned but is not online while other
    // speakers are. Never lock onto a different unit.
    if (wantedHost && !cand && list.some(b => b && b.host && b.host !== wantedHost && (b.kind === 'stock' || b.kind === 'str'))) {
      liveSearchKey = null; lastSeenState = 'wrong';
      setStatus('setup-warn', t('setup.awaitWrongSpeaker'),
        `<div><a href="#" id="setupChooseDifferent">${escapeHtml(t('setup.awaitChooseDifferent'))}</a></div>`);
      const cd = $('setupChooseDifferent');
      if (cd) cd.onclick = (e) => { e.preventDefault(); aborted = true; stopTicker(); state.setupTarget = null; showAwaitBoxReadyPanel({ ssid, pass, html }); };
      await sleep(3000); continue;
    }

    // Already runs STR. Continue must NOT re-enter the stick wait: the
    // message promises "you'll be offered an update", and the wait loop it
    // used to hand off into ignores STR speakers by design, so the button
    // greyed out and the screen sat unchanged through a silent five-minute
    // poll (field report + log, 2026-08-30). Hand over to the speaker view,
    // where the update offer actually lives.
    if (cand && cand.kind === 'str') {
      lastSeenState = 'str';
      setStatus('setup-ok', t('setup.awaitAlreadyStr'));
      arm(t('setup.awaitContinueBtn'), () => {
        aborted = true;
        stopTicker();
        state.currentBox = cand;
        deps.switchView('box');
      });
      ready = true; break;
    }

    // On the home network (stock).
    if (cand && cand.host) {
      const f = await getFw(cand.host);
      const model = (f && f.model) || cand.model || 'SoundTouch';
      const fw = (f && f.short) || '';
      // Discovery has it, so it is on the network; that clock starts now and
      // does not restart when :8090 blips mid-reboot.
      if (!firstSeenOnNetwork) firstSeenOnNetwork = Date.now();
      if (f) lastAnsweredBosePort = Date.now();
      let sshOk = false;
      try { sshOk = await BoxInstallReachable(cand.host); } catch {}
      if (sshOk) {
        lastSeenState = 'ready';
        setStatus('setup-ok', t('setup.awaitFoundReady', { model }));
        arm(t('setup.awaitConfirmBtn'), handoff);
        ready = true; break;
      }
      if ((Date.now() - firstSeenOnNetwork) < GRACE_MS) {
        liveSearchKey = null; lastSeenState = 'booting';
        setStatus('muted small', t('setup.awaitStillBooting'));
        await sleep(3000); continue;
      }
      // Grace elapsed, SSH still off. Three cases, and the third one used to be
      // invisible: the speaker is listed but its Bose port never answered, so
      // nothing can be said about its firmware and "still booting" is no longer
      // an honest reading after a minute and a half.
      liveSearchKey = null;
      if (!f) {
        // It answered a moment ago and has gone quiet: that is a restart, and
        // the reassuring message is the right one for another grace window.
        if (lastAnsweredBosePort && (Date.now() - lastAnsweredBosePort) < GRACE_MS) {
          liveSearchKey = null; lastSeenState = 'booting';
          setStatus('muted small', t('setup.awaitStillBooting'));
          await sleep(3000); continue;
        }
        lastSeenState = 'silent';
        setStatus('setup-warn', t('setup.awaitOnNetworkSilent', { model }));
        await sleep(3000); continue;
      }
      if (f && (f.outdated || !f.short)) {
        // The status is re-set every pass of this loop; the guide is only
        // rendered when it is not already on screen, so a user halfway through
        // copying the update page address does not see it redrawn under them.
        const guideUp = lastSeenState === 'firmware' && statusEl.querySelector('.fw-guide');
        lastSeenState = 'firmware';
        if (!guideUp) {
          const guide = firmwareGuideNeeded(fw)
            ? `<div class="fw-update-banner">${firmwareGuideMount({
              model, moduleType: f.moduleType, variant: f.variant, current: f.firmware || fw, host: cand.host,
            })}</div>`
            : '';
          setStatus('setup-warn', t('setup.awaitFirmwareTooOld', { model, fw: fw || '?' }),
            guide + `<div><a href="#" id="setupTryAnyway">${escapeHtml(t('setup.awaitFirmwareTryAnyway'))}</a></div>`);
          wireOutdatedFirmwareLinks();
        }
        const ta = $('setupTryAnyway');
        if (ta) ta.onclick = (e) => { e.preventDefault(); handoff(); };
      } else {
        lastSeenState = 'no-stick';
        setStatus('setup-warn', t('setup.awaitBootedWithoutStick', { model }));
      }
      await sleep(3000); continue;
    }

    // Factory-fresh wireless-only speaker on its own setup network.
    if (!wantedHost) {
      let ap = null;
      try { ap = await ProbeSetupAP(); } catch {}
      if (ap && ap.found && ap.box && ap.box.host) {
        lastSeenState = 'setup-ap';
        setStatus('setup-ok', t('setup.awaitSetupNetwork'));
        arm(t('setup.awaitSetupNetworkBtn'), handoff);
        ready = true; break;
      }
    }

    // Nothing on the network yet.
    liveSearchKey = (Date.now() - watcherStart) < 25000 ? 'setup.awaitSearching' : 'setup.awaitSearchingStalled';
    setStatus('muted small', t(liveSearchKey, { remaining: formatRemaining(deadline - Date.now()) }));
    await sleep(3000);
  }

  if (ready || aborted || !mine()) { stopTicker(); return; }

  // Timeout: context-aware recovery based on whether we ever saw it on the network.
  stopTicker();
  const wasOnNetwork = lastSeenState === 'no-stick' || lastSeenState === 'firmware'
    || lastSeenState === 'booting' || lastSeenState === 'silent';
  setStatus('setup-warn', t(wasOnNetwork ? 'setup.awaitTimeoutWasOnNetwork' : 'setup.awaitTimeoutNeverSeen'));
  btn.textContent = t('setup.awaitSearchAgain');
  btn.disabled = false;
  btn.onclick = () => { showAwaitBoxReadyPanel({ ssid, pass, html }); };
}
