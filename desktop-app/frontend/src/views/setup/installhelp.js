// views/setup/installhelp.js: what an install screen says under its headline.
// The help checklist per failure code, the firmware update guide for a speaker
// whose firmware is too old, the "has not come back" checklist and the
// power-cycle advice. Markup and wiring only; the flow that decides which of
// them a screen gets is install.js. Extracted from views/setup.js.

import { $, escapeHtml, confirmWarn, isSoundTouch300 } from '../../utils.js';
import { t } from '../../i18n/index.js';
import { firmwareGuideMount, firmwareGuideNeeded, wireFirmwareGuides } from '../../fwguide.js';
import { firmwareLadderDeps } from '../../fwladderdeps.js';
import {
  darkChecklistApplies,
  darkChecklistHtml,
  wireDarkChecklist,
} from '../../darkafterupdate.js';
import {
  GetFirmwareGuide,
  FlashBoseFirmware,
  EventsOn,
  BrowserOpenURL,
  ClipboardSetText,
} from '../../api.js';

// powerCycleAdviceHtml returns the post-install power-cycle advice as a ready-to-
// insert div, model-aware. A full power-cycle is what clears the coprocessor's
// undefined "light bar keeps sweeping" state that a normal restart leaves behind
// after an install (live-seen on ginger/SMSC ST300 and taigan). The battery
// Portable cannot be unplugged, so it gets the AUX button-combo variant (hold
// AUX, or hold AUX + volume-down); every mains-powered speaker keeps the
// unplug-from-the-wall advice.
export function powerCycleAdviceHtml(box) {
  const m = String((box && (box.model || box.type)) || '').toLowerCase().replace(/[\s_]+/g, '');
  const v = String((box && box.variant) || '').toLowerCase();
  // The SoundTouch 300 is not a "still stuck?" case: after an install or agent
  // OTA it ALWAYS sits in the alternating-yellow blink state, unreachable,
  // until it is unplugged once. As a muted tip people skipped it and thought
  // the soundbar was bricked (Michal 2026-07, Richard 2026-08-30), so for the
  // 300 this is a mandatory red step, not advice.
  if (isSoundTouch300(box)) {
    return `<div class="setup-warn setup-powercycle">${escapeHtml(t('update.st300PowerCycle'))}</div>`;
  }
  const isPortable = m.includes('portable') || v === 'taigan';
  const key = isPortable ? 'setup.powerCyclePortable' : 'setup.powerCycleAdvice';
  return `<div class="setup-powercycle muted small">${escapeHtml(t(key))}</div>`;
}

// INSTALL_HELP_STEPS maps a backend InstallResult.code (set in
// install_str.go) to the ordered list of concrete help steps to show under a
// failed install, so the user gets an actionable checklist instead of just the
// raw technical error. Each id resolves to a setup.help.<id> i18n key, so the
// guidance is localized in all bundles. The default covers the generic case
// and any future/unknown code.
const INSTALL_HELP_STEPS = {
  'install-timeout': ['freshBoot', 'wifi', 'stick', 'logs'],
  'install-error': ['freshBoot', 'wifi', 'logs'],
  'install-script-error': ['logs', 'freshBoot'],
  'ssh-handshake': ['freshBoot', 'wifi'],
  'ssh-probe': ['freshBoot', 'wifi', 'stick'],
  'stick-missing': ['st30Port', 'usbPicky', 'stickInserted', 'freshBoot', 'stick'],
  'agent-not-up': ['powerCycle', 'wifi', 'logs'],
  // The install ran, but the speaker answered nothing at any layer (no ping,
  // no ARP entry, no port) when the wait budget ran out: it is still
  // restarting or reconnecting to Wi-Fi, not proven failed (a SoundTouch 10
  // whose Wi-Fi came back minutes late, 2026-09-06). Wait and refresh first;
  // no power-cycle advice, that would interrupt a speaker that is likely
  // fine. The backend re-checks once by itself (install:late).
  'speaker-not-back': ['waitRefresh', 'wifi', 'logs'],
  // The speaker is running the firmware's OWN out-of-box setup: its Wi-Fi
  // light blinks fast and it leaves the network for minutes at a time (#873,
  // two independent SoundTouch 10 reports, 2026-09-06). STR is already on the
  // box and nothing here can end the state, so the checklist explains it and
  // says to wait. No power-cycle step: pulling the plug mid-setup is the one
  // action that can make it worse.
  'speaker-in-setup': ['setupPhase', 'setupWait', 'waitRefresh', 'logs'],
  'not-reachable': ['wifi', 'freshBoot'],
  'install-window-closed': ['freshBoot'],
  // The box answers UPnP on :8091 but not SSH / the Bose port / the STR agent:
  // it is on the network with a wedged control stack (the Portable "renderer up,
  // control crashed" state). A power-cycle clears the wedge; do NOT show the
  // 'wifi' step here (the default fallback does) - the box is already on Wi-Fi,
  // so re-onboarding advice would just mislead.
  'control-unresponsive': ['powerCycle', 'freshBoot', 'logs'],
  // Media read error: the speaker found install.sh but could not read it.
  // Usually a large stick force-formatted to FAT32 with a block size the
  // speaker can't read (the 64 GB case), or a faulty stick.
  'stick-io-error': ['reformatApp', 'usbPicky', 'smallerStick', 'differentStick', 'logs'],
  // The speaker's internal storage is full (ENOSPC during the NAND copy): the
  // stick is fine and reformatting would not help, so no stick advice here.
  // The result message names the cause; the report's '# nv usage' section
  // lists what occupies the space.
  'nand-full': ['logs'],
  // USB power dropout, not a faulty stick: the speaker's port could not keep the
  // stick powered under read load (dmesg VBUS_ERROR / error -110), so it
  // disconnected mid-install. The same stick installs fine on ST10/ST20, so the
  // remedy is a low-power stick or a powered hub, NOT "the stick is faulty".
  'stick-usb-power': ['usbPower', 'lowPowerStick', 'usbHub', 'logs'],
  // The speaker started but could not copy the agent binary off the stick into
  // its memory (run.sh stick->NAND copy hit an I/O error and there was no prior
  // NAND cache), so the agent never came up. A flaky/loose stick; the remedy is
  // a fresh stick firmly inserted, or the SSH repair that bypasses the stick.
  'stick-copy-failed': ['stickCopyFailed', 'stickInserted', 'differentStick', 'usbPicky', 'logs'],
  // The speaker's Bose firmware is far too old for the install to open it, so
  // the install stopped before touching it. The firmware guide renders above
  // this list; the one step is "update first, retrying only restarts it".
  'firmware-too-old': ['firmwareFirst', 'logs'],
  // The speaker answered, but named neither its model nor its firmware for the
  // whole wait: it is still starting up. The install stopped before the
  // unlock, so the one step is to wait and press Install again.
  'speaker-starting': ['waitRefresh', 'logs'],
  // The speaker restarted under the running network install (the SSH session
  // ended without an exit status). It comes back by itself, and pressing
  // Install again repeats the same restart (an ST20, twice, 2026-10-26), so
  // the steps say wait and send the logs, never retry, and name no stick.
  'repair-box-restarted': ['repairRestarted', 'netLogs'],
};

// INSTALL_HELP_STEPS_NET is the OTA/network-install counterpart of the map
// above. A network install has no USB stick, so stick-troubleshooting steps
// (reseat / reformat / low-power stick) are wrong and confusing here; instead we
// steer the user to the things that actually matter over the network: the LAN
// cable, the Wi-Fi, the speaker being on the network, and a retry once it has
// finished restarting. Used whenever the failed install came from the OTA path.
const INSTALL_HELP_STEPS_NET = {
  'install-timeout': ['netOnNetwork', 'netWifi', 'netCable', 'netRetry', 'netLogs'],
  'install-error': ['netOnNetwork', 'netWifi', 'netCable', 'netRetry', 'netLogs'],
  'install-script-error': ['netRetry', 'netLogs'],
  'ssh-handshake': ['netWifi', 'netCable', 'netRetry'],
  'ssh-probe': ['netOnNetwork', 'netWifi', 'netCable', 'netRetry'],
  'agent-not-up': ['netRetry', 'netWifi', 'netLogs'],
  'speaker-not-back': ['waitRefresh', 'netWifi', 'netLogs'],
  'speaker-in-setup': ['setupPhase', 'setupWait', 'waitRefresh', 'netLogs'],
  'not-reachable': ['netOnNetwork', 'netWifi', 'netCable', 'netRetry'],
  'install-window-closed': ['netRetry'],
  'control-unresponsive': ['netRetry', 'netLogs'],
  'stick-copy-failed': ['netRetry', 'netLogs'],
  'firmware-too-old': ['firmwareFirst', 'netLogs'],
  'speaker-starting': ['waitRefresh', 'netLogs'],
  'repair-box-restarted': ['repairRestarted', 'netLogs'],
};
const NET_HELP_DEFAULT = ['netOnNetwork', 'netWifi', 'netCable', 'netRetry', 'netLogs'];

// ST300_STUCK_CODES are the failure codes whose symptom on a SoundTouch 300 is
// its alternating-yellow blink state: the install ran, the soundbar rebooted
// into the blink, and every port on it is dead until somebody unplugs it once.
const ST300_STUCK_CODES = ['speaker-not-back', 'agent-not-up', 'install-timeout',
  'not-reachable', 'control-unresponsive'];

// outdatedFirmwareHtml is the firmware update guide, rendered where the owner
// of a still-stock speaker can actually reach it.
//
// The install-failure note told such an owner from 2026-08-10 that "the Firmware
// section in the speaker settings has the steps and the link". A stock speaker
// can never open it: the settings pane short-circuits a box with kind 'stock' to
// an empty state with a Setup button. So the steps live here, beside the
// failure, and they are the same steps the settings pane shows (fwguide.js).
//
// short comes from InstallResult.Firmware, and the model, moduleType and
// variant from the same InstallResult: install_str.go reads them off
// :8090/info before it touches the speaker, so they are available even when
// the speaker has since gone quiet. The box from discovery is the fallback for
// the model.
export function outdatedFirmwareHtml(box, short, result) {
  if (!firmwareGuideNeeded(short)) return '';
  const r = result || {};
  return `<div class="fw-update-banner" id="setupFwBanner">`
    + firmwareGuideMount({
      model: r.model || (box && (box.model || box.type)) || '',
      moduleType: r.moduleType || '',
      variant: r.variant || '',
      current: short,
      host: (box && box.host) || '',
    })
    + `</div>`;
}

// wireOutdatedFirmwareLinks wires the guide's buttons (Bose download, copy and
// open the update page) and looks up Bose's file for the speaker.
export function wireOutdatedFirmwareLinks() {
  wireFirmwareGuides(document, {
    getGuide: GetFirmwareGuide,
    openURL: BrowserOpenURL,
    copy: ClipboardSetText,
    flash: FlashBoseFirmware,
    onProgress: (cb) => EventsOn('firmware:progress', cb),
    confirm: confirmWarn,
    ladder: firmwareLadderDeps(),
  });
}

// installDarkChecklistHtml is the "has not come back" checklist for an
// install whose speaker answered nothing (darkafterupdate.js), unframed: the
// screen's own red headline already says the speaker did not come back.
// Empty for the SoundTouch 300 (darkChecklistApplies): its one step is the
// power cycle, and the checklist's Wi-Fi and factory reset lines are exactly
// the network steps installHelpHtml removes for that model.
export function installDarkChecklistHtml(box) {
  if (!darkChecklistApplies(box)) return '';
  return darkChecklistHtml({
    name: (box && (box.friendlyName || box.name || box.host)) || '',
    box: box || null,
    context: 'install',
    framed: false,
  });
}

// wireInstallDarkChecklist wires the checklist's firmware guide link.
export function wireInstallDarkChecklist() {
  wireDarkChecklist($('setupResult'), { openURL: BrowserOpenURL });
}

// installHelpHtml renders the localized help checklist for a failure code.
// isNetwork picks the OTA-appropriate step list (no USB-stick advice), and box
// is needed because on one model the code alone gives the wrong instructions.
export function installHelpHtml(code, isNetwork, box) {
  // A blinking SoundTouch 300 is not a network problem, and the checklist must
  // not suggest it is. The headline above it has said "unplug the soundbar"
  // since v0.9.84 and the red step is rendered beside this list on all three
  // screens, but the checklist still said check the speaker is on the network,
  // check the Wi-Fi, check the cable. The third 300 owner in a week
  // (2026-09-24) did exactly that, twice: factory-reset the soundbar and
  // re-entered his Wi-Fi in the Bose app, while the one action that would have
  // finished his install was a ten-second power interrupt. So for this model on
  // these codes the network steps are REMOVED, not merely demoted.
  if (isSoundTouch300(box) && ST300_STUCK_CODES.indexOf(code) >= 0) {
    const only = `<li>${escapeHtml(t('setup.help.' + (isNetwork ? 'netLogs' : 'logs')))}</li>`;
    return `<div class="setup-help"><b>${escapeHtml(t('setup.helpTitle'))}</b><ul>${only}</ul>`
      + `<p class="small">${escapeHtml(t('setup.helpLogsInstruction'))}</p></div>`;
  }
  const steps = isNetwork
    ? (INSTALL_HELP_STEPS_NET[code] || NET_HELP_DEFAULT)
    : (INSTALL_HELP_STEPS[code] || ['freshBoot', 'wifi', 'stick', 'logs']);
  const items = steps.map(s => `<li>${escapeHtml(t('setup.help.' + s))}</li>`).join('');
  // No save button here any more: the failure report block below the checklist
  // now carries it, and it is rendered on ALL THREE failure paths rather than
  // only this one. Two buttons doing the same thing on one screen is how the
  // other two paths ended up with none.
  return `<div class="setup-help"><b>${escapeHtml(t('setup.helpTitle'))}</b><ul>${items}</ul>`
    + `<p class="small">${escapeHtml(t('setup.helpLogsInstruction'))}</p></div>`;
}
