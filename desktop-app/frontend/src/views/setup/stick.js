// views/setup/stick.js: the USB stick wizard, the fallback route. The Wi-Fi
// fields, the drive list with its readiness panels, and the two ways on from
// there: prepare the stick (doSetup) or continue with one that is already
// prepared. Its markup is part of the view's shell and is mounted by
// mountSetupShell in views/setup.js. Extracted from views/setup.js.

import { state } from '../../state.js';
import { $, escapeHtml, escapeAttr, sleep, confirmWarn } from '../../utils.js';
import { t, getLocale } from '../../i18n/index.js';
import { stickGateHost, stickFirmwareGate, stickFwTooOldHtml } from '../../stickfwgate.js';
import {
  StickInstallFirmwareGate,
  ListDrives,
  WriteStickFiles,
  FormatStick,
  StickVersion,
  CheckStick,
  StickConfigs,
  EjectDrive,
  WriteWLANConfig,
  WriteRegionConfig,
  WriteNameConfig,
  WriteLangConfig,
  ListWiFiProfiles,
  TryWiFiPassword,
  CurrentWiFi,
} from '../../api.js';
import { deps } from './deps.js';
import { isMacOS } from './shared.js';
import { wireOutdatedFirmwareLinks } from './installhelp.js';
import { showAwaitBoxReadyPanel } from './await.js';

export function togglePasswordVisibility() {
  const input = $('wlanPass');
  const btn = $('wlanShowPass');
  if (input.type === 'password') {
    input.type = 'text';
    btn.innerHTML = '&#128064;';
    btn.title = t('setup.hidePassword');
  } else {
    input.type = 'password';
    btn.innerHTML = '&#128065;';
    btn.title = t('settingsView.wlanShowPass');
  }
}

export async function loadWifiProfiles() {
  const sel = $('wlanSelect');
  if (!sel) return; // shell not mounted yet
  try {
    const profiles = await ListWiFiProfiles() || [];
    sel.innerHTML = `<option value="">${escapeHtml(t('settingsView.wlanPickPlaceholder'))}</option>` +
      profiles.map(p => `<option value="${escapeAttr(p.ssid)}">${escapeHtml(p.ssid)}</option>`).join('');
    try {
      const current = await CurrentWiFi();
      if (current && profiles.some(p => p.ssid === current)) {
        sel.value = current;
        if (isMacOS) {
          $('wlanSsid').value = current;
        } else {
          onWifiSelect();
        }
      }
    } catch {}
  } catch {
    sel.innerHTML = `<option value="">${escapeHtml(t('setup.wlanListUnavailable'))}</option>`;
  }
}

export async function onWifiSelect() {
  const v = $('wlanSelect').value;
  if (!v) return;
  $('wlanSsid').value = v;
  if (isMacOS) return;
  try {
    const pw = await TryWiFiPassword(v);
    if (pw) $('wlanPass').value = pw;
  } catch {}
}

async function prefillWizardFromStick(path) {
  try {
    const c = await StickConfigs(path);
    if (!c) return;
    if (c.region) {
      const r = $('setupRegion');
      if (r) r.value = c.region;
    }
    if (c.name) {
      const n = $('setupName');
      if (n && !n.value) n.value = c.name;
    }
    if (c.wlanSSID) {
      const s = $('wlanSsid');
      if (s && !s.value) s.value = c.wlanSSID;
    }
    if (c.wlanPass) {
      const p = $('wlanPass');
      if (p && !p.value) p.value = c.wlanPass;
    }
  } catch {}
}

export async function refreshDrives(clearResult) {
  deps.mountSetupShell(); // entry from switchView too; ensure the shell exists
  state.selectedDrive = null;
  // Manual "Neu suchen" click clears the just-ejected guard so the
  // wizard returns to its normal listing behaviour.
  if (clearResult) {
    state.justEjectedPath = null;
  }
  try {
    state.drives = await ListDrives() || [];
    // Filter out a stick we just ejected: Windows reports the
    // dismounted volume for a few seconds with totalBytes=0 and an
    // empty filesystem, which the wizard otherwise reads as "unknown
    // format, must be reformatted" — confusing right after a
    // successful prepare. If the same path comes back with a valid
    // FAT32 mount (user pulled and re-inserted), allow it again.
    if (state.justEjectedPath) {
      state.drives = state.drives.filter(d => {
        if (d.path !== state.justEjectedPath) return true;
        const looksMounted = d.totalBytes > 0 && (d.filesystem || '').toUpperCase() === 'FAT32';
        if (looksMounted) {
          state.justEjectedPath = null;
          return true;
        }
        return false;
      });
    }
    // Only clear the success message when the user actively
    // requested a re-search (button click). Auto refreshes after
    // setup should keep the success message visible.
    if (clearResult) {
      const res = $('setupResult');
      if (res) res.innerHTML = '';
    }
    renderDrives();
  } catch (e) {
    $('drivesList').textContent = t('common.error') + ': ' + e;
  }
}

async function renderDrives() {
  // Persistent session re-entry: after the first successful stick prep this
  // session, let the user jump straight to installing on a speaker (over the
  // network) without writing the stick again, e.g. provisioning several boxes
  // or retrying. Shown even when no stick is currently in the PC (it is now in
  // the box).
  const prepBanner = state.sessionPrep
    ? `<div class="setup-help" style="margin-bottom:8px"><span class="muted small">${escapeHtml(t('setup.sessionPrepHint'))}</span> <button class="btn btn-mini" id="setupSkipPrep">${escapeHtml(t('setup.sessionPrepBtn'))}</button></div>`
    : '';
  const bindSkip = () => {
    const sk = $('setupSkipPrep');
    if (sk) sk.onclick = () => {
      const p = state.sessionPrep || {};
      showAwaitBoxReadyPanel({ ssid: p.ssid || '', pass: p.pass || '', html: `<div class="setup-ok">${escapeHtml(t('setup.sessionPrepGo'))}</div>` });
    };
  };
  if (!state.drives.length) {
    $('drivesList').innerHTML = prepBanner + `<div class="muted">${escapeHtml(t('setup.noSticksFound'))}</div>`;
    $('setupGo').disabled = true;
    $('updateInfo').classList.add('hidden');
    $('formatWarn').classList.add('hidden');
    bindSkip();
    return;
  }
  $('drivesList').innerHTML = prepBanner + state.drives.map((d, i) => {
    const gb = (d.totalBytes / (1024*1024*1024)).toFixed(1);
    const fs = (d.filesystem || '').toUpperCase();
    const isFat32 = fs === 'FAT32';
    const has = d.hasStick ? '<span class="badge">STR</span>' : '';
    const fsBadge = !isFat32 ? ` <span class="badge badge-warn">${escapeHtml(fs || t('common.unknown'))} – ${escapeHtml(t('setup.needsFormat'))}</span>` : '';
    const active = state.selectedDrive === i ? ' active' : '';
    return `<div class="drive-row${active}" data-i="${i}">
      <div class="drive-info">
        <div class="drive-path"><b>${escapeHtml(d.path)}</b> ${has}${fsBadge}</div>
        <div class="drive-meta">${escapeHtml(d.label || '')} &middot; ${gb} GB &middot; ${escapeHtml(d.filesystem)}</div>
      </div>
    </div>`;
  }).join('');
  bindSkip();
  document.querySelectorAll('.drive-row').forEach(el => {
    el.onclick = async () => {
      state.selectedDrive = parseInt(el.dataset.i, 10);
      await renderDrives();
      await updateDrivePanels();
    };
  });
  if (state.selectedDrive == null && state.drives.length === 1) {
    state.selectedDrive = 0;
    await renderDrives();
    await updateDrivePanels();
  } else {
    await updateDrivePanels();
  }
}

async function updateDrivePanels() {
  const drive = state.drives[state.selectedDrive];
  const btn = $('setupGo');
  const upd = $('updateInfo');
  const warn = $('formatWarn');
  // Default the "the stick is already in the speaker" shortcut to VISIBLE on every
  // panel update; only the prepared-stick-in-the-PC branch below hides it (there
  // "Continue" is the one install path). This guarantees the shortcut stays
  // available for the common "I prepared a stick in an earlier session and put it
  // straight into the box" flow, and is never left stale-hidden after a stick is
  // removed from the PC drive (#i).
  { const skip = $('setupSkipSection'); if (skip) skip.classList.remove('hidden'); }
  if (!drive) {
    btn.disabled = true;
    btn.textContent = t('setup.goBtn');
    upd.classList.add('hidden');
    warn.classList.add('hidden');
    return;
  }
  btn.disabled = false;
  // If the stick still carries unconsumed setup configs (the user has
  // not yet plugged the stick into the speaker), prefill the wizard
  // fields from those configs.
  prefillWizardFromStick(drive.path);
  const isFat32 = (drive.filesystem || '').toUpperCase() === 'FAT32';

  // Technical readiness check first: a stick that is too small or write-
  // protected cannot be fixed by formatting, so block here with a clear
  // message and a disabled button instead of letting the user run into a
  // cryptic failure during write or install. FAT32 itself is handled below
  // (it is fixable by the offered format step). The size floor only applies
  // to fresh sticks: a stick that already carries STR clearly works, so don't
  // block updating a smaller, already-provisioned one.
  let chk = null;
  try { chk = await CheckStick(drive.path); } catch {}
  const tooSmall = chk && chk.reason === 'too-small' && !drive.hasStick;
  // 'read-only' is the DIAGNOSED case of the macOS read-only mount (a dirty
  // FAT32 after the stick was pulled without ejecting, #775) and gets
  // instructions instead of the helpless "maybe write-protected".
  const readOnly = chk && chk.reason === 'read-only';
  // 'macos-privacy': macOS refuses the app access to removable volumes (TCC
  // consent denied or never granted). Every stick fails identically then, so
  // the message has to point at System Settings, not at another stick.
  const macPrivacy = chk && chk.reason === 'macos-privacy';
  const notWritable = chk && chk.reason === 'not-writable';
  if (tooSmall || readOnly || macPrivacy || notWritable) {
    const gb = (chk.totalBytes / 1e9).toFixed(1);
    upd.innerHTML = `<b>${escapeHtml(
      tooSmall ? t('setup.stickTooSmall', { gb })
        : readOnly ? t('setup.stickReadOnly')
        : macPrivacy ? t('setup.stickMacPrivacy')
        : t('setup.stickNotWritable')
    )}</b>`;
    upd.classList.remove('hidden');
    warn.classList.add('hidden');
    btn.disabled = true;
    btn.textContent = t('setup.goBtn');
    return;
  }

  // Large stick already formatted FAT32: Windows cannot format >32 GB as
  // FAT32, so it was done with a third-party tool, which for big sticks
  // usually picks a 64 KB cluster size the speaker's old kernel cannot read
  // (the I/O error reading install.sh, #119). Our own formatter caps clusters
  // at a size the speaker reads, so steer the user to reformat in-app. Fresh
  // sticks only; an existing STR stick is left alone.
  const LARGE_FAT32_BYTES = 34e9; // larger than any real 32 GB stick
  if (isFat32 && !drive.hasStick && (drive.totalBytes || 0) > LARGE_FAT32_BYTES) {
    const cb = $('setupFormat');
    if (cb && !cb.checked) cb.checked = true;
    upd.innerHTML = `<b>${escapeHtml(t('setup.stickLargeFat32'))}</b>`;
    upd.classList.remove('hidden');
    warn.classList.add('hidden');
    btn.disabled = false;
    btn.textContent = t('setup.goBtnFormatPrepare');
    return;
  }

  // If the stick is not FAT32, auto-enable the format checkbox and
  // show the visual warning. hasStick is only meaningful for FAT32
  // anyway because the speaker reads nothing else.
  if (!isFat32) {
    const cb = $('setupFormat');
    if (cb && !cb.checked) cb.checked = true;
    upd.innerHTML =
      `<b>${escapeHtml(t('setup.stickFsLine', { fs: drive.filesystem || t('common.unknown') }))}</b> ` +
      `<div class="muted small" style="margin-top:6px">${escapeHtml(t('setup.stickFsHelp'))}</div>`;
    upd.classList.remove('hidden');
    warn.classList.add('hidden');
    btn.textContent = t('setup.goBtnFormatPrepare');
    return;
  }

  // Small, already-FAT32 stick: no reformat needed. Clear any leftover format
  // tick from a previous exFAT/large-stick selection so prepare does not invoke
  // the (blockable) elevated formatter for nothing.
  {
    const cb = $('setupFormat');
    if (cb && cb.checked && (drive.totalBytes || 0) <= 34e9) cb.checked = false;
  }

  if (drive.hasStick) {
    try {
      const fromFull = (await StickVersion(drive.path) || '').trim();
      const appVer = state.appInfo ? state.appInfo.version : '';
      const appBld = state.appInfo ? (state.appInfo.build || '') : '';
      const toFull = appBld && appBld !== 'dev' ? `${appVer}+${appBld}` : appVer;
      // version.txt is either "1.0.0" or "1.0.0+2026-05-15-2202".
      // Strict comparison: same version + newer build still counts
      // as an update.
      const same = fromFull === toFull;
      const fromShort = fromFull || t('common.unknown');
      // Lead with the "already configured, just continue" message and the
      // continue button. The version / update-available line goes last and
      // muted: shown too prominently at the top it pulls normal users into
      // clicking "update" mid-setup instead of continuing to the speaker.
      // The "Update USB stick" button (setupGo, label goBtnUpdate) re-writes the
      // stick unconditionally, even when the version matches. Spell that out so a
      // user who wants to refresh run.sh/agent on an already-current stick (e.g.
      // after an STR update) sees that it is offered, not just "already done".
      const verLine = (same
        ? `<b>${escapeHtml(t('setup.stickCurrent'))}</b> <small>${escapeHtml(t('setup.versionLabel', { version: fromShort }))}</small>`
        : `<b>${escapeHtml(t('setup.stickUpdateAvail'))}</b> <small>${escapeHtml(fromShort)} &rarr; ${escapeHtml(toFull)}</small>`)
        + `<br><span class="muted small">${escapeHtml(t('setup.stickRefreshHint'))}</span>`;
      upd.innerHTML =
        `<div>${escapeHtml(t('setup.alreadyConfigured'))}</div>`
        + `<div style="margin-top:10px"><button class="btn btn-mini" id="setupContinue">${escapeHtml(t('setup.continueBtn'))}</button>`
        + ` <span class="muted small">${escapeHtml(t('setup.continueHint'))}</span></div>`
        + `<div class="muted small" style="margin-top:12px">${verLine}</div>`;
      upd.classList.remove('hidden');
      const contBtn = $('setupContinue');
      if (contBtn) contBtn.onclick = doContinueWithStick;
    } catch {
      upd.classList.add('hidden');
    }
    warn.classList.add('hidden');
    btn.textContent = t('setup.goBtnUpdate');
    // A prepared stick is in the PC, so "Continue" (eject + install over the
    // network) is the one install path. Hide the bottom "stick is already in the
    // speaker" shortcut here: showing both made it look like two buttons that do
    // the same OTA deploy (#i). The shortcut stays for the no-stick-in-PC case.
    const skip = $('setupSkipSection');
    if (skip) skip.classList.add('hidden');
  } else {
    upd.classList.add('hidden');
    warn.classList.remove('hidden');
    btn.textContent = t('setup.goBtn');
    const skip = $('setupSkipSection');
    if (skip) skip.classList.remove('hidden');
  }
}

export async function doSetup() {
  const drive = state.drives[state.selectedDrive];
  if (!drive) return;
  // Firmware first: a speaker the install over the network stopped at
  // "firmware-too-old" (or that reports such a firmware now) gets the firmware
  // update guide, not STR's install files. Those files on the stick were what
  // Bose's Update.stu later sat beside when the speaker never started its
  // update (support case 2026-11). Checked before anything touches the stick.
  const fwGate = await stickFirmwareGate(StickInstallFirmwareGate, stickGateHost(state.setupTarget));
  if (fwGate) {
    const result = $('setupResult');
    result.innerHTML = stickFwTooOldHtml(fwGate, state.setupTarget && state.setupTarget.box);
    wireOutdatedFirmwareLinks();
    try { result.scrollIntoView({ behavior: 'smooth', block: 'start' }); } catch { /* older webview */ }
    return;
  }
  // Hard technical gate: a too-small or write-protected stick cannot be
  // rescued by formatting, so refuse before doing anything (even if the
  // format checkbox is ticked). This is the same verdict the panel shows;
  // re-checked here so a stick swapped after selection cannot slip through.
  try {
    const chk = await CheckStick(drive.path);
    if (chk && chk.reason === 'too-small' && !drive.hasStick) {
      $('setupResult').innerHTML = `<div class="setup-err">${escapeHtml(t('setup.stickTooSmall', { gb: (chk.totalBytes / 1e9).toFixed(1) }))}</div>`;
      return;
    }
    if (chk && (chk.reason === 'not-writable' || chk.reason === 'read-only' || chk.reason === 'macos-privacy')) {
      $('setupResult').innerHTML = `<div class="setup-err">${escapeHtml(t(
        chk.reason === 'read-only' ? 'setup.stickReadOnly'
          : chk.reason === 'macos-privacy' ? 'setup.stickMacPrivacy'
          : 'setup.stickNotWritable'))}</div>`;
      return;
    }
  } catch {}
  const isFat32 = (drive.filesystem || '').toUpperCase() === 'FAT32';
  const formatChecked = $('setupFormat') && $('setupFormat').checked;
  // Re-check the stick: a small, writable, already-FAT32 stick (CheckStick
  // reason '') needs no reformat, so skip the elevated formatter even if a stale
  // tick survived from a prior exFAT/NTFS selection. This both avoids a needless
  // erase and dodges Smart App Control / WDAC blocking the unsigned format
  // helper. Large FAT32 sticks (>34 GB) are excluded so the 64 KB-cluster trap
  // (#119) stays fixed: those still get reformatted to a speaker-readable layout.
  let alreadyOk = false;
  try {
    const c = await CheckStick(drive.path);
    alreadyOk = c && c.ok === true && (c.reason || '') === '';
  } catch {}
  const LARGE_FAT32 = 34e9;
  const wantFormat = formatChecked && !(isFat32 && alreadyOk && (drive.totalBytes || 0) <= LARGE_FAT32);
  // The speaker only reads FAT32. If the stick is NTFS/exFAT and
  // the user has NOT enabled the format option, writing on top is
  // pointless. Block and explain.
  if (!isFat32 && !wantFormat) {
    $('setupResult').innerHTML =
      `<div class="setup-err"><b>${escapeHtml(t('setup.stickFsLine', { fs: drive.filesystem || t('common.unknown') }))}</b> ` +
      t('setup.errFat32Required') + `</div>`;
    return;
  }
  if (!drive.hasStick && !wantFormat) {
    const ok = await confirmWarn(
      t('setup.eraseConfirmTitle'),
      t('setup.eraseConfirmBody', { path: escapeHtml(drive.path) })
    );
    if (!ok) return;
  }
  $('setupGo').disabled = true;
  $('setupResult').innerHTML = wantFormat
    ? `<div class="muted">${escapeHtml(t('setup.formattingShort'))}</div>`
    : `<div class="muted">${escapeHtml(t('setup.preparing'))}</div>`;
  try {
    let formatLine = '';
    if (wantFormat) {
      try {
        $('setupResult').innerHTML = `<div class="muted">${escapeHtml(t('setup.formattingLong'))}</div>`;
        await FormatStick(drive.path);
        formatLine = `<div class="setup-ok">${escapeHtml(t('setup.formatOk'))}</div>`;
        $('setupResult').innerHTML = formatLine + `<div class="muted">${escapeHtml(t('setup.preparing'))}</div>`;
        await sleep(1500);
        state.drives = await ListDrives() || [];
        const fresh = state.drives.find(d => d.path === drive.path);
        if (!fresh) {
          $('setupResult').innerHTML = formatLine + `<div class="setup-warn">${escapeHtml(t('setup.stickGoneAfterFormat', { path: drive.path }))}</div>`;
          $('setupGo').disabled = false;
          refreshDrives();
          return;
        }
      } catch (fErr) {
        const es = String(fErr);
        // The signed Format-Volume fallback was also blocked by the OS security
        // policy: tell the user how to format the stick by hand instead of
        // showing a raw error they cannot act on.
        const blocked = es.includes('format-blocked-manual');
        $('setupResult').innerHTML = `<div class="setup-err">${escapeHtml(
          blocked ? t('setup.formatBlockedManual') : t('setup.formatFailed', { err: es })
        )}</div>`;
        $('setupGo').disabled = false;
        return;
      }
    }
    const written = await WriteStickFiles(drive.path);
    let html = formatLine + `<div class="setup-ok">${escapeHtml(t('setup.stickPrepared', { n: written.length }))}</div>`;
    const region = $('setupRegion').value || 'DE';
    try {
      await WriteRegionConfig(drive.path, region);
      try { localStorage.setItem('setupRegion', region); } catch {}
      html += `<div class="setup-ok">${escapeHtml(t('setup.regionSaved', { region }))}</div>`;
    } catch (regErr) {
      html += `<div class="setup-warn">${escapeHtml(t('setup.regionFailed', { err: String(regErr) }))}</div>`;
    }
    // Box display language: the user's explicit pick from the wizard
    // dropdown (pre-filled from the country). locale + country travel
    // along so the Go side can re-derive if the value is somehow
    // invalid. Best-effort and silent.
    try {
      const langSel = $('setupLang');
      const sysLang = langSel ? (parseInt(langSel.value, 10) || 0) : 0;
      await WriteLangConfig(drive.path, getLocale(), region, sysLang);
    } catch {}
    const boxName = $('setupName').value.trim();
    if (boxName) {
      try {
        await WriteNameConfig(drive.path, boxName);
        html += `<div class="setup-ok">${escapeHtml(t('setup.nameSaved', { name: boxName }))}</div>`;
      } catch (nErr) {
        html += `<div class="setup-warn">${escapeHtml(t('setup.nameFailed', { err: String(nErr) }))}</div>`;
      }
    }
    const ssid = $('wlanSsid').value.trim();
    const pass = $('wlanPass').value;
    if (ssid && !pass) {
      // An SSID without a password would switch the speaker to an OPEN
      // network and throw it off a protected home Wi-Fi (field case
      // 2026-08-29: install succeeded, speaker no longer on the Wi-Fi
      // afterwards). The SSID is prefilled and reading the password can
      // fail, so the empty field is a realistic state. Do not write, and
      // say clearly why; the Go backend refuses the same case as well, and
      // run.sh on the speaker ignores old sticks with an empty password.
      html += `<div class="setup-warn">${escapeHtml(t('setup.wlanNoPass'))}</div>`;
    } else if (ssid) {
      try {
        await WriteWLANConfig(drive.path, ssid, pass);
        html += `<div class="setup-ok">${escapeHtml(t('setup.wlanSaved', { ssid }))}</div>`;
        $('wlanPass').value = '';
      } catch (wlanErr) {
        html += `<div class="setup-warn">${escapeHtml(t('setup.wlanFailed', { err: String(wlanErr) }))}</div>`;
      }
    }
    try {
      $('setupResult').innerHTML = html + `<div class="muted small">${escapeHtml(t('setup.ejecting'))}</div>`;
      await EjectDrive(drive.path);
      html += `<p>${t('setup.ejectedBody')}</p>`;
      // Remember the path we just ejected. Windows keeps reporting the
      // dismounted volume for a few seconds with totalBytes=0 and an
      // empty filesystem string. refreshDrives would then auto-select
      // it and updateDrivePanels would render the misleading "Stick ist
      // unbekannt formatiert" warning right under the success message.
      // Hide it until the user either pulls + re-inserts (path becomes
      // valid again) or clicks "Neu suchen" explicitly.
      state.justEjectedPath = drive.path;
    } catch (ejErr) {
      html += `<p class="setup-warn">${t('setup.ejectFailed', { err: escapeHtml(String(ejErr)) })}</p>`;
    }
    $('setupResult').innerHTML = html;
    state.selectedDrive = null;
    state.currentBox = null;
    state.presets = [];
    refreshDrives();
    deps.discoverBoxes();
    // Show a confirmation panel first. We do NOT start the discovery
    // + auto-install loop yet: the user just clicked "GO", the stick
    // is still in the laptop, the speaker has not been touched.
    // Starting the loop now would race the user and almost always
    // probe a stale stock state (box up from a previous session,
    // stick not yet inserted) which fails install.sh.
    // Remember this prep for the rest of the session so the user can re-enter
    // the install flow for another speaker (or a retry) without writing the
    // stick again. Surfaced as a persistent button in renderDrives.
    state.sessionPrep = { ssid, pass };
    showAwaitBoxReadyPanel({ ssid, pass, html });
  } catch (e) {
    $('setupResult').innerHTML = `<div class="setup-err">${escapeHtml(t('common.error'))}: ${escapeHtml(String(e))}</div>`;
  }
  $('setupGo').disabled = false;
}

// doContinueWithStick reuses an already-prepared STR stick: it skips the full
// rewrite/format and jumps straight to the "insert in the speaker and install"
// step. A user with an ST30 had to re-write the whole stick every time the box
// was not found mid-setup, just to get back to this step. This mirrors doSetup's
// tail (eject + show the await/install panel) without touching the stick.
async function doContinueWithStick() {
  const drive = state.drives[state.selectedDrive];
  if (!drive) return;
  const ssid = $('wlanSsid').value.trim();
  const pass = $('wlanPass').value;
  let html = `<div class="setup-ok">${escapeHtml(t('setup.continueUsingStick'))}</div>`;
  try {
    $('setupResult').innerHTML = html + `<div class="muted small">${escapeHtml(t('setup.ejecting'))}</div>`;
    await EjectDrive(drive.path);
    html += `<p>${t('setup.ejectedBody')}</p>`;
    state.justEjectedPath = drive.path;
  } catch (ejErr) {
    html += `<p class="setup-warn">${t('setup.ejectFailed', { err: escapeHtml(String(ejErr)) })}</p>`;
  }
  $('setupResult').innerHTML = html;
  state.selectedDrive = null;
  state.currentBox = null;
  state.presets = [];
  refreshDrives();
  deps.discoverBoxes();
  showAwaitBoxReadyPanel({ ssid, pass, html });
}
