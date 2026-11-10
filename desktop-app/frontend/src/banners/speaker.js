// banners/speaker.js: the two global banners about what a speaker needs: the
// wedge banner (#boxWedgeBanner) and the speaker issue banner (#boxIssueBanner)
// with its buttons, plus what both feed: the per-speaker issue list of the
// UI 2.0 preview (noteSpeakerIssues) and the cloud restore run. Extracted from
// the main.js monolith; main.js-local helpers arrive through deps.js.

import { EventsOn, RebootBox, RemoveConflictingMod, RepairSpeakerName, RestoreSTRCloud } from '../api.js';
import { runConflictCleanup } from '../conflictcleanup.js';
import { groupKeyErrorBanners } from '../groupkeys.js';
import { t } from '../i18n/index.js';
import { noInternetBoxes } from '../nointernet.js';
import { localizeStillRestarting, restarts } from '../restartwait.js';
import { speakerIssuesFrom } from '../shell2/attention.js';
import { renderShellSpeakers } from '../shell2/mount.js';
import { unsafeNameBoxes } from '../speakername.js';
import { state } from '../state.js';
import {
  $,
  confirmWarn,
  escapeHtml,
  getBoxLabel,
  holdRefusedBannerKey,
  showError,
  showToast,
  stormBannerBoxes,
} from '../utils.js';
import { renderBoxSelect, selectBox } from '../views/boxpicker.js';
import { weakWifiDropoutBoxes } from '../weakwifi.js';
import { deps } from './deps.js';
import { warnDismissKey, warnDismissed } from './dismiss.js';

// checkWedgeBanner shows the pull-the-plug hint when any STR speaker reports
// the wedged-control state (transport accepted, never plays; only a
// power-cycle clears it - see the agent's wedge detection). Global banner so
// the hint is visible on every tab.
export function checkWedgeBanner() {
  const el = $('boxWedgeBanner');
  if (!el) return;
  const wedged = (state.boxes || []).filter(b => b && b.boxHealth === 'wedged');
  noteSpeakerIssues({ wedged });
  if (!wedged.length) { el.classList.add('hidden'); return; }
  const names = wedged.map(b => getBoxLabel(b)).join(', ');
  el.innerHTML = `<div class="app-update-text"><span class="app-update-icon" aria-hidden="true">&#9888;</span><span><b>${escapeHtml(t('speaker.wedgedTitle'))}</b> ${escapeHtml(t('speaker.wedgedBanner', { name: names }))}</span></div>`;
  el.classList.remove('hidden');
}

// noteRestartRequested records a restart the speaker accepted, and repaints
// the warning once the wait can last no longer, so a speaker that never shows
// as gone in a refresh still gets its button back.
export function noteRestartRequested(host) {
  restarts.requested(host);
  setTimeout(checkBoxIssueBanner, restarts.remainingMs(host) + 1000);
}

// checkBoxIssueBanner is a heads-up when a speaker carries leftovers of a
// rival cloud-free SoundTouch tool (they can fight STR) or has no STR Wi-Fi
// backup saved. Global banner, #270. Dismissible per box and warning type:
// both are informational (a healthy speaker keeps playing either way), so the
// user must be able to acknowledge them once instead of being nagged forever
// (Jens, 2026-07-12).
export function checkBoxIssueBanner() {
  const el = $('boxIssueBanner');
  if (!el) return;
  const boxes = state.boxes || [];
  // Every refresh lands here: it is where a speaker the app restarted is seen
  // going away and coming back, which ends its "Restarting…" button.
  restarts.observe(boxes);
  const conflict = boxes.filter(b => b && b.conflictingMod && !warnDismissed(b, 'conflict'));
  // A speaker asking a non-standard Bose cloud address (#986). NOT dismissible
  // and not folded into the conflict line: the marker files may be long gone
  // (they do not survive a factory reset, this does) and nothing plays on such a
  // speaker at all, so it is worth its own sentence naming the address.
  const foreignCloud = boxes.filter(b => b && b.foreignCloudURL && !b.conflictingMod);
  const noWifi = boxes.filter(b => b && b.wlanCredsMissing && !warnDismissed(b, 'nowifi'));
  // A speaker refusing essentially every preset recall (Bose error 1036). This
  // one is NOT dismissible: nothing the user presses will play until it clears,
  // and the remedy people find on their own, pulling the plug, resets the box
  // clock and poisons the next boot, while a soft restart clears it (#419
  // Finding 4). Saying so at the moment it happens is the whole point.
  // recallRefusal is the storm's quiet sibling (no 1036 ever fires, the box
  // just drops its source for every recall); same remedy, same banner.
  // A wedged speaker is excluded: it already has the "unplug it" banner, and
  // a restart notice beside it contradicted that (#1065).
  const storm = stormBannerBoxes(boxes);
  // A thumbs-key press the speaker refused. Not dismissible and worth its own
  // line: the press produced NOTHING visible before this, so two owners spent
  // days re-saving a group that was never the problem (2026-09-26). The
  // speaker's own reason is shown verbatim rather than paraphrased.
  const keyErr = boxes.filter(b => b && b.groupKeyError);
  // A long press the speaker made and STR could not keep. Same shape as the
  // key error: it happened, it was refused, and nothing visible said so, so
  // the key appeared to snap back to the old station on its own.
  const holdErr = boxes.filter(b => b && b.holdRefusedSource);
  // A folder key whose saved file addresses the media server no longer has
  // (a USB stick swapped on a router's media server: the server answers,
  // every old path is 404). The agent names the key until it is saved again,
  // which is the only cure, so the banner says exactly that.
  const staleKey = boxes.filter(b => b && b.staleFolderKeySlot);
  // A speaker that cannot reach the internet while the LAN works (the agent's
  // passive verdict, nointernet.js). Not dismissible: every station fails on
  // it, and the fix is on the router, which only a sentence can point at.
  const noNet = noInternetBoxes(boxes);
  // Playback dropped out recently while the Wi-Fi signal at the speaker was
  // weak (weakwifi.js). Informational, so dismissible per speaker; the
  // dismissal holds for a day, because on a speaker with poor reception the
  // next dropout is never far away.
  const weakWifi = weakWifiDropoutBoxes(boxes, (b) => {
    try { return localStorage.getItem(warnDismissKey(b, 'weakwifi')); } catch { return 0; }
  });
  const msgs = [];
  if (noNet.length) {
    const names = noNet.map(b => getBoxLabel(b)).join(', ');
    msgs.push(`<b>${escapeHtml(t('speaker.noInternetBanner', { name: names }))}</b> ${escapeHtml(t('speaker.noInternetHint'))}`);
  }
  if (conflict.length) {
    const names = conflict.map(b => getBoxLabel(b)).join(', ');
    msgs.push(escapeHtml(t('speaker.conflictModBanner', { name: names, mod: conflict[0].conflictingMod })));
  }
  if (foreignCloud.length) {
    const names = foreignCloud.map(b => getBoxLabel(b)).join(', ');
    msgs.push(escapeHtml(t('speaker.foreignCloudBanner', {
      name: names, url: foreignCloud[0].foreignCloudURL,
    })));
  }
  if (noWifi.length) {
    const names = noWifi.map(b => getBoxLabel(b)).join(', ');
    msgs.push(escapeHtml(t('speaker.noWifiBanner', { name: names })));
  }
  if (storm.length) {
    const names = storm.map(b => getBoxLabel(b)).join(', ');
    msgs.push(escapeHtml(t('speaker.stormBanner', { name: names })));
  }
  if (keyErr.length) {
    // A refusal the user can act on reads as a sentence in their language,
    // one per kind; the agent's raw reason (English, technical) is the line
    // under it, visible and selectable for a support mail, not hover-only.
    for (const line of groupKeyErrorBanners(keyErr, getBoxLabel)) {
      const text = escapeHtml(t(line.key, { name: line.names, reason: line.reason }));
      msgs.push(line.key === 'speaker.groupKeyErrorBanner'
        ? text
        : `${text}<br><small class="muted">${escapeHtml(line.reasons.join(' | '))}</small>`);
    }
  }
  if (holdErr.length) {
    const names = holdErr.map(b => getBoxLabel(b)).join(', ');
    msgs.push(escapeHtml(t(holdRefusedBannerKey(holdErr[0].holdRefusedSource), {
      name: names, slot: holdErr[0].holdRefusedSlot || '?',
    })));
  }
  if (staleKey.length) {
    const names = staleKey.map(b => getBoxLabel(b)).join(', ');
    msgs.push(escapeHtml(t('speaker.staleFolderKeyBanner', {
      name: names, slot: staleKey[0].staleFolderKeySlot,
    })));
  }
  if (weakWifi.length) {
    const names = weakWifi.map(b => getBoxLabel(b)).join(', ');
    msgs.push(escapeHtml(t('speaker.weakWifiDropoutBanner', { name: names })));
  }
  // A speaker name its firmware cannot survive ("&", "<", ">" or invalid
  // UTF-8, #1582): the UPnP renderer does not come up, so Spotify and the
  // music library cannot play while radio still does, and nothing else on
  // screen says why. Not dismissible; the fix is one tap.
  const badName = unsafeNameBoxes(boxes);
  noteSpeakerIssues({ noNet, conflict, foreignCloud, noWifi, storm, keyErr, holdErr, staleKey, weakWifi, badName });
  if (badName.length) {
    const names = badName.map(b => getBoxLabel(b)).join(', ');
    msgs.push(`<b>${escapeHtml(t('speaker.badNameBanner', { name: names }))}</b> ${escapeHtml(t('speaker.badNameHint'))}`);
  }
  if (!msgs.length) { el.classList.add('hidden'); return; }
  const badNameBtn = badName.length
    ? `<button class="btn btn-primary btn-mini" id="boxIssueBadNameBtn">${escapeHtml(t('speaker.badNameFixBtn'))}</button>`
    : '';
  // When a speaker has no saved Wi-Fi, give the user a direct way to act on it
  // instead of just telling them to "set it up in the settings": the button
  // jumps straight to that speaker's settings, where the Wi-Fi section is.
  const wifiBtn = noWifi.length
    ? `<button class="btn btn-mini" id="boxIssueWifiBtn">${escapeHtml(t('speaker.wifiSaveBtn'))}</button>`
    : '';
  // A conflicting-mod (AfterTouch, OpenCloudTouch) leftover is removable in one
  // click, and this button does it right here. It used to only switch to the
  // speaker's settings page, which its label never said, so an owner pressed it
  // twice and saw nothing happen (#1083).
  const conflictBtn = conflict.length
    ? `<button class="btn btn-mini" id="boxIssueConflictBtn">${escapeHtml(t('speaker.conflictRemoveBtn'))}</button>`
    : '';
  // The soft restart, offered right where the problem is stated. The speaker
  // comes back in about a minute and the state is gone.
  // Until a restart the app asked for has visibly happened, the button says
  // "Restarting…" and stays off: the warning outlives the request by a
  // minute or more, and a second press landed on a speaker mid-boot (#1768).
  const stormRestarting = storm.length && restarts.isPending(storm[0].host);
  const stormBtn = storm.length
    ? `<button class="btn btn-primary btn-mini" id="boxIssueStormBtn"${stormRestarting ? ' disabled' : ''}>${escapeHtml(t(stormRestarting ? 'speaker.restartingBtn' : 'speaker.stormRestartBtn'))}</button>`
    : '';
  // One press puts STR back on every hijacked speaker at once.
  //
  // The per-speaker Actions page cannot do this. Its repair rewrites the cloud
  // address FILE, and a rival app that sets the address at runtime leaves that
  // file untouched, so the repair healed nothing and said it had worked. The
  // cure is a restart, because the firmware reads the file once at boot.
  // Measured across five speakers on 2026-09-26 after the ST Remote Pro iOS app
  // was started; one reboot each brought every one back stock and paired.
  const cloudBtn = foreignCloud.length
    ? `<button class="btn btn-primary btn-mini" id="boxIssueCloudBtn">${escapeHtml(t('speaker.cloudRestoreBtn', { n: foreignCloud.length }))}</button>`
    : '';
  el.innerHTML = `
    <div class="app-update-text"><span class="app-update-icon" aria-hidden="true">&#9888;</span><span>${msgs.join('<br>')}</span></div>
    ${badNameBtn}
    ${cloudBtn}
    ${stormBtn}
    ${conflictBtn}
    ${wifiBtn}
    ${(conflict.length || noWifi.length || weakWifi.length) ? `<button class="btn btn-secondary btn-mini" id="boxIssueDismissBtn">${escapeHtml(t('speaker.issueDismiss'))}</button>` : ''}`;
  const sb = $('boxIssueStormBtn');
  if (sb) sb.onclick = async () => {
    const box = storm[0];
    if (restarts.isPending(box.host)) {
      showToast(t('speaker.stillRestarting', { name: getBoxLabel(box) }));
      return;
    }
    sb.disabled = true;
    showToast(t('speaker.stormRestarting', { name: getBoxLabel(box) }));
    try {
      await RebootBox(box.host, box.port);
      noteRestartRequested(box.host);
      sb.textContent = t('speaker.restartingBtn');
    } catch (e) {
      sb.disabled = false;
      showError(localizeStillRestarting(String(e), t('speaker.stillRestarting', { name: getBoxLabel(box) })));
    }
  };
  const bnb = $('boxIssueBadNameBtn');
  if (bnb) bnb.onclick = async () => {
    bnb.disabled = true;
    try {
      for (const box of badName) {
        const name = await RepairSpeakerName(box.host, box.port, box.friendlyName || '');
        showToast(t('speaker.badNameFixing', { name: name || getBoxLabel(box) }));
        box.friendlyName = name || box.friendlyName;
        box.speakerNameUnsafe = false;
        const idx = (state.boxes || []).findIndex(b => b && b.deviceID === box.deviceID);
        if (idx >= 0) state.boxes[idx] = { ...state.boxes[idx], friendlyName: box.friendlyName, speakerNameUnsafe: false };
      }
      checkBoxIssueBanner();
      renderBoxSelect();
    } catch (e) {
      showError(String(e));
    } finally {
      bnb.disabled = false;
    }
  };
  const cloudB = $('boxIssueCloudBtn');
  if (cloudB) cloudB.onclick = () => runCloudRestore(foreignCloud, cloudB);
  const wb = $('boxIssueWifiBtn');
  // Settings opens straight on the Wi-Fi form for this speaker (wireWlanSwitch).
  if (wb) wb.onclick = () => { state.wlanStoreFor = noWifi[0].host; selectBox(noWifi[0]); deps.switchView('settings'); };
  const cb = $('boxIssueConflictBtn');
  if (cb) cb.onclick = async () => {
    const label = cb.textContent;
    await runConflictCleanup(conflict[0], {
      t, confirmWarn, showToast, showError,
      removeConflictingMod: RemoveConflictingMod,
      rebootBox: RebootBox,
      rediscover: (afterMs) => {
        if (afterMs > 0) setTimeout(deps.discoverBoxes, afterMs);
        else deps.discoverBoxes();
      },
      setBusy: (busy) => {
        cb.disabled = busy;
        cb.textContent = busy ? t('settingsView.removeConflictRunning') : label;
      },
    });
  };
  const d = $('boxIssueDismissBtn');
  if (d) d.onclick = () => {
    const stamp = String(Date.now());
    conflict.forEach(b => { try { localStorage.setItem(warnDismissKey(b, 'conflict'), stamp); } catch {} });
    noWifi.forEach(b => { try { localStorage.setItem(warnDismissKey(b, 'nowifi'), stamp); } catch {} });
    weakWifi.forEach(b => { try { localStorage.setItem(warnDismissKey(b, 'weakwifi'), stamp); } catch {} });
    el.classList.add('hidden');
  };
  el.classList.remove('hidden');
}

// noteSpeakerIssues keeps the per-speaker side of the two banners above in
// state.speakerIssues (shell2/attention.js), passively: the classic banners
// render exactly as before, and the UI 2.0 preview shows the same words and
// buttons on that speaker's card, page and sidebar row instead. part carries
// the lists one of the two banner functions just worked out.
// Kept on state, not in module variables: a banner check that runs while this
// module is still being evaluated must not hit an uninitialised binding.
function noteSpeakerIssues(part) {
  state.speakerIssueParts = { ...(state.speakerIssueParts || {}), ...part };
  state.speakerIssues = speakerIssuesFrom(state.speakerIssueParts, getBoxLabel);
  const sig = JSON.stringify(state.speakerIssues);
  if (sig === state.speakerIssuesSig) return;
  state.speakerIssuesSig = sig;
  if (state.ui2Shell) renderShellSpeakers(true);
}

// runCloudRestore puts STR back on every speaker a rival app has taken over.
//
// The confirmation carries the warning that matters more than the restart: as
// long as the other app runs, it sets the address again, including right after
// the speaker comes back. That is not a theory, it is what happened on
// 2026-09-26: the hijack itself restarted all five speakers, and they came back
// still pointing at the other service because the app was still open. Restart
// them while it runs and the whole thing simply repeats.
export async function runCloudRestore(boxes, btn) {
  if (!boxes || !boxes.length) return;
  const names = boxes.map(b => getBoxLabel(b)).join(', ');
  const ok = await confirmWarn(
    t('speaker.cloudRestoreTitle'),
    `<p>${escapeHtml(t('speaker.cloudRestoreBody', { name: names, n: boxes.length }))}</p>`
    + `<p><b>${escapeHtml(t('speaker.cloudRestoreAppWarning'))}</b></p>`,
  );
  if (!ok) return;
  btn.disabled = true;
  // The progress line names the speaker being worked on. One at a time, so a
  // fleet takes a few minutes and silence would read as a hang.
  const off = EventsOn('cloudrestore:progress', (p) => {
    if (!p) return;
    showToast(t('speaker.cloudRestoreProgress', {
      name: p.name || '', done: p.done || 0, total: p.total || boxes.length,
    }));
  });
  try {
    const results = await RestoreSTRCloud(boxes.map(b => ({
      host: b.host, port: b.port, name: getBoxLabel(b),
    })));
    const done = (results || []).filter(r => r && r.restored);
    const failed = (results || []).filter(r => r && !r.restored);
    if (!failed.length) {
      showToast(t('speaker.cloudRestoreDone', { n: done.length }));
    } else {
      // Name the speakers that are still wrong and why. A partial repair
      // reported as success is the mistake this whole path exists to undo.
      const detail = failed
        .map(r => `${r.name || r.host}: ${r.error || r.stillForeign || '?'}`)
        .join('; ');
      showError(t('speaker.cloudRestorePartial', {
        ok: done.length, bad: failed.length, detail,
      }));
    }
    deps.discoverBoxes();
  } catch (e) {
    showError(String(e));
  } finally {
    try { off(); } catch {}
    btn.disabled = false;
  }
}
