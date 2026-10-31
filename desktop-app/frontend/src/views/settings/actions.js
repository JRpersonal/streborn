// views/settings/actions.js: the Actions group of Speaker settings. Find the
// speaker, sync the hardware keys, save the logs, email support, reboot, the
// rival-tool cleanup, the true factory reset, removing STR, and backup and
// restore of favorites and presets.

import { state } from '../../state.js';
import { $, escapeHtml, escapeAttr, showError, showToast, encodeURIStrict } from '../../utils.js';
import { t } from '../../i18n/index.js';
import { purgeSpeakerLocalState } from '../../speakerPurge.js';
import { identifyOutcome } from '../../boxstate.js';
import { runConflictCleanup } from '../../conflictcleanup.js';
import {
  RebootBox,
  SyncBoxPresets,
  ExportBackup,
  ImportBackup,
  SaveDiagnosticBundle,
  BrowserOpenURL,
  TrueFactoryReset,
  UninstallSTR,
  RemoveConflictingMod,
  IdentifyBox,
} from '../../api.js';
import { deps, helpBlock, renderSTRRemovedPanel, confirmFor } from './shared.js';

export function renderActionSections(ctx) {
  const { box } = ctx;
  return {
    group: 'actions',
    html: `
    <div data-ui2-sec="actions" class="settings-section">
      <h3>${escapeHtml(t('settingsView.actionsHeading'))}</h3>
      <div class="actions-grid">
        <button class="btn btn-mini" id="boxIdentifyBtn">${escapeHtml(t('settingsView.identifyBtn'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.identifyHelp'))}</p>
        <button class="btn btn-mini" id="boxSyncPresetsBtn">${escapeHtml(t('settingsView.syncHardwareKeys'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.syncHardwareKeysHelp'))}</p>
        <button class="btn btn-mini" id="boxSaveLogsBtn">${escapeHtml(t('footer.saveLogs'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.saveLogsHelp'))}</p>
        <button class="btn btn-mini" id="boxEmailSupportBtn">${escapeHtml(t('settingsView.emailSupport'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.emailSupportHelp'))}</p>
        <button class="btn btn-mini btn-warning" id="boxRebootBtn">${escapeHtml(t('speaker.reboot'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.rebootHelp'))}</p>
        ${box && box.conflictingMod ? `<button class="btn btn-mini btn-warning" id="boxRemoveConflictBtn">${escapeHtml(t('settingsView.removeConflictBtn', { mod: box.conflictingMod }))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.removeConflictHelp', { mod: box.conflictingMod }))}</p>` : ''}
        ${box && !box.conflictingMod && box.foreignCloudURL ? `<button class="btn btn-mini btn-warning" id="boxRemoveConflictBtn">${escapeHtml(t('settingsView.healCloudURLBtn'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.healCloudURLHelp', { url: box.foreignCloudURL }))}</p>` : ''}
        <hr class="actions-divider" />
        <button class="btn btn-mini btn-danger" id="boxTrueFactoryResetBtn">${escapeHtml(t('settingsView.trueFactoryResetBtn'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.trueFactoryResetHelpShort'))}</p>
        <button class="btn btn-mini btn-danger" id="boxRemoveSTRBtn">${escapeHtml(t('settingsView.removeSTRBtn'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.removeSTRHelp'))}</p>
      </div>
    </div>

    <div data-ui2-sec="backup" class="settings-section" id="backupSection">
      <h3>${escapeHtml(t('settingsView.backupHeading'))}<span class="str-badge" title="${escapeAttr(t('common.strOnlyHint'))}">${escapeHtml(t('common.strOnly'))}</span></h3>
      ${helpBlock(t('settingsView.backupHelp'), 'p', 'muted small')}
      <div class="setting-row">
        <button class="btn btn-mini" id="backupExportBtn">${escapeHtml(t('settingsView.backupExportBtn'))}</button>
        <button class="btn btn-mini" id="backupImportBtn">${escapeHtml(t('settingsView.backupImportBtn'))}</button>
      </div>
      <div id="backupResult" class="muted small" style="margin-top:8px"></div>
    </div>
  `,
  };
}

export function wireActionSections(ctx) {
  const { box } = ctx;

  // Backup and restore (#778): favorites + every speaker's presets into one
  // JSON file, and back. The heavy lifting is Go-side (ExportBackup /
  // ImportBackup own the dialogs, the preset reads and the restore writes);
  // this wiring only contributes the favorites, which live in the frontend's
  // localStorage, and renders the outcome.
  const bkExport = $('backupExportBtn');
  if (bkExport) {
    bkExport.onclick = async () => {
      const out = $('backupResult');
      bkExport.disabled = true;
      if (out) out.innerHTML = `<div class="muted small">${escapeHtml(t('common.loading'))}</div>`;
      try {
        const favs = deps.favStationsJSON ? deps.favStationsJSON() : '[]';
        const r = await ExportBackup(favs);
        if (out) {
          if (r && r.canceled) { out.innerHTML = ''; }
          else {
            out.innerHTML = `<div class="setup-ok">${escapeHtml(t('settingsView.backupExportDone', {
              favs: (r && r.favorites) || 0, speakers: (r && r.speakers) || 0, presets: (r && r.presets) || 0,
            }))}</div>` + ((r && r.skipped && r.skipped.length)
              ? `<div class="setup-warn">${escapeHtml(t('settingsView.backupNotReached', { names: r.skipped.join(', ') }))}</div>` : '');
          }
        }
      } catch (e) {
        if (out) out.innerHTML = `<div class="setup-err">${escapeHtml(String(e))}</div>`;
      }
      bkExport.disabled = false;
    };
  }
  const bkImport = $('backupImportBtn');
  if (bkImport) {
    bkImport.onclick = async () => {
      const out = $('backupResult');
      bkImport.disabled = true;
      if (out) out.innerHTML = `<div class="muted small">${escapeHtml(t('common.loading'))}</div>`;
      try {
        const r = await ImportBackup();
        if (r && r.canceled) { if (out) out.innerHTML = ''; }
        else {
          const merged = (deps.mergeFavStations && r && typeof r.favorites === 'string' && r.favorites)
            ? deps.mergeFavStations(r.favorites) : { added: 0 };
          if (out) {
            out.innerHTML = `<div class="setup-ok">${escapeHtml(t('settingsView.backupImportDone', {
              favs: merged.added || 0, presets: (r && r.presets) || 0, speakers: (r && r.speakers) || 0,
            }))}</div>` + ((r && r.missed && r.missed.length)
              ? `<div class="setup-warn">${escapeHtml(t('settingsView.backupNotReached', { names: r.missed.join(', ') }))}</div>` : '');
          }
          deps.loadPresets && deps.loadPresets();
        }
      } catch (e) {
        if (out) out.innerHTML = `<div class="setup-err">${escapeHtml(String(e))}</div>`;
      }
      bkImport.disabled = false;
    };
  }

  // Hardware key sync handler.
  const syncBtn = $('boxSyncPresetsBtn');
  if (syncBtn) {
    syncBtn.onclick = async () => {
      syncBtn.disabled = true;
      syncBtn.textContent = t('settingsView.syncing');
      try {
        const r = await SyncBoxPresets(box.host, box.port);
        const synced = r && r.synced != null ? r.synced : 0;
        showToast(t('settingsView.syncDoneToast', { n: synced }));
      } catch (e) { showError(e); }
      syncBtn.disabled = false;
      syncBtn.textContent = t('settingsView.syncHardwareKeys');
    };
  }

  // Save diagnostic logs, the same bundle the footer link saves. Surfaced in
  // Speaker Settings too because the install-timeout message points users here,
  // and the footer link is easy to miss at the very bottom of the window (#114).
  const saveLogsSettingsBtn = $('boxSaveLogsBtn');
  if (saveLogsSettingsBtn) {
    saveLogsSettingsBtn.onclick = async () => {
      saveLogsSettingsBtn.disabled = true;
      try {
        const hosts = (state.boxes || []).map(b => b && b.host).filter(Boolean);
        const res = await SaveDiagnosticBundle(hosts, true);
        if (res && res.savePath) {
          showToast(t('footer.saveLogsDone', { path: res.savePath, size: Math.round((res.bytes || 0) / 1024) }));
        }
      } catch (e) { showError(e); }
      saveLogsSettingsBtn.disabled = false;
    };
  }

  // Email support: save the diagnostic bundle, then open a pre-filled email to
  // STR support with the saved file path so the user only has to attach it.
  // mailto cannot attach a file itself (browser/OS limitation), so the body and
  // the toast both point at the saved zip. Body kept in English (it is a support
  // ticket); the button, help and toast are localized.
  const emailSupportBtn = $('boxEmailSupportBtn');
  if (emailSupportBtn) {
    emailSupportBtn.onclick = async () => {
      emailSupportBtn.disabled = true;
      try {
        const hosts = (state.boxes || []).map(b => b && b.host).filter(Boolean);
        const res = await SaveDiagnosticBundle(hosts, true);
        if (res && res.savePath) {
          const subject = 'ST Reborn support request';
          const body =
            'Hi,\n\nI need help with ST Reborn.\n\n' +
            '(Please describe the problem here.)\n\n' +
            '--------------------------------\n' +
            'Diagnostic logs were saved to:\n' + res.savePath + '\n\n' +
            'IMPORTANT: please attach that file to this email before sending.\n';
          try {
            // encodeURIStrict, not encodeURIComponent: Wails refuses a URL
            // carrying a bare parenthesis, and this body has one.
            BrowserOpenURL('mailto:str@sichtbar-app.de?subject=' +
              encodeURIStrict(subject) + '&body=' + encodeURIStrict(body));
          } catch {}
          showToast(t('settingsView.emailSupportToast', { path: res.savePath }));
        }
      } catch (e) { showError(e); }
      emailSupportBtn.disabled = false;
    };
  }

  // Find this speaker: HELLO and its name on its display for a few seconds.
  const identifyBtn = $('boxIdentifyBtn');
  if (identifyBtn) {
    identifyBtn.onclick = async () => {
      identifyBtn.disabled = true;
      try {
        const outcome = identifyOutcome(await IdentifyBox(box.host, box.port));
        const msg = {
          'shown': 'settingsView.identifyShownToast',
          'no-display': 'settingsView.identifyNoDisplayToast',
          'old-agent': 'settingsView.identifyOldAgentToast',
          'busy': 'settingsView.identifyBusyToast',
        }[outcome];
        if (msg) showToast(t(msg));
        else showError(t('settingsView.identifyFailed'));
      } catch (e) { showError(e); }
      // The notice holds for 5 s; a second press meanwhile would only queue.
      setTimeout(() => { identifyBtn.disabled = false; }, 3000);
    };
  }

  // Speaker reboot button.
  const rebootBtn = $('boxRebootBtn');
  if (rebootBtn) {
    rebootBtn.onclick = async () => {
      const ok = await confirmFor(box, 
        t('speaker.reboot'),
        t('speaker.rebootGenericBody')
      );
      if (!ok) return;
      try {
        await RebootBox(box.host, box.port);
        showToast(t('speaker.rebootingToast'));
        // The speaker is gone for ~30 s, then trigger discovery again.
        setTimeout(deps.discoverBoxes, 35000);
      } catch (e) { showError(e); }
    };
  }

  // Remove the leftovers of a rival cloud-free SoundTouch tool (AfterTouch,
  // OpenCloudTouch) that clash with STR, and put the box's Bose cloud address
  // back to the standard one. One click, no SSH; the box-issue banner points
  // here. Rendered when the box carries such leftovers (box.conflictingMod) or
  // asks a non-standard cloud address (box.foreignCloudURL, #986), which is the
  // half that survives both a file cleanup and a factory reset.
  const rmConflictBtn = $('boxRemoveConflictBtn');
  if (rmConflictBtn) {
    rmConflictBtn.onclick = async () => {
      const mod = (box && box.conflictingMod) || '';
      const cloudOnly = !mod && !!(box && box.foreignCloudURL);
      const idleLabel = cloudOnly
        ? t('settingsView.healCloudURLBtn')
        : t('settingsView.removeConflictBtn', { mod: mod || 'AfterTouch' });
      // The flow itself lives in conflictcleanup.js, shared with the banner's
      // button (#1083).
      await runConflictCleanup(box, {
        t, confirmWarn: (title, body, o) => confirmFor(box, title, body, o), showToast, showError,
        removeConflictingMod: RemoveConflictingMod,
        rebootBox: RebootBox,
        rediscover: (afterMs) => {
          if (!deps.discoverBoxes) return;
          if (afterMs > 0) setTimeout(deps.discoverBoxes, afterMs);
          else deps.discoverBoxes();
        },
        setBusy: (busy) => {
          rmConflictBtn.disabled = busy;
          rmConflictBtn.textContent = busy ? t('settingsView.removeConflictRunning') : idleLabel;
        },
      });
    };
  }

  // True factory reset: wipes Bose's persistence files via SSH so
  // the speaker truly returns to OOB. Required for the Bose iOS app
  // to be able to re-onboard the speaker — Bose's hardware reset
  // (Preset 1 + Vol-) leaves NetworkProfiles.xml intact, the
  // speaker auto-rejoins its last WiFi, and the iOS app's WAC
  // discovery sees an "already configured" speaker and gives up.
  const tfrBtn = $('boxTrueFactoryResetBtn');
  if (tfrBtn) {
    tfrBtn.onclick = async () => {
      const ok = await confirmFor(box, 
        t('settingsView.trueFactoryResetConfirmTitle'),
        t('settingsView.trueFactoryResetConfirmBody', { name: box.friendlyName || box.name || box.host })
      );
      if (!ok) return;
      tfrBtn.disabled = true;
      tfrBtn.textContent = t('settingsView.trueFactoryResetRunning');
      try {
        const r = await TrueFactoryReset(box.host);
        if (r && r.ok) {
          showToast(t('settingsView.trueFactoryResetDoneToast', { n: (r.wipedFiles || []).length }));
          // Speaker reboots into OOB, will not be on the LAN for a
          // while. Trigger discovery in 60 s so the UI catches it
          // when the iOS app brings it back onto <home Wi-Fi>.
          setTimeout(deps.discoverBoxes, 60000);
        } else {
          showError(t('settingsView.trueFactoryResetFailed', { msg: (r && r.message) || '?' }));
        }
      } catch (e) {
        showError(e);
      } finally {
        tfrBtn.disabled = false;
        tfrBtn.textContent = t('settingsView.trueFactoryResetBtn');
      }
    };
  }

  // Remove STR entirely and return the speaker to vanilla Bose. Unlike
  // True Factory Reset (which keeps STR installed for re-onboarding),
  // this uninstalls STR. The backend refuses while the USB stick is
  // still inserted (Bose would reinstall STR from it on the next boot),
  // so we warn about that up front and surface the stick-present case.
  const rmBtn = $('boxRemoveSTRBtn');
  if (rmBtn) {
    rmBtn.onclick = async () => {
      const ok = await confirmFor(box, 
        t('settingsView.removeSTRConfirmTitle'),
        t('settingsView.removeSTRConfirmBody', { name: box.friendlyName || box.name || box.host })
      );
      if (!ok) return;
      rmBtn.disabled = true;
      rmBtn.textContent = t('settingsView.removeSTRRunning');
      try {
        const r = await UninstallSTR(box.host);
        if (r && r.ok) {
          showToast(t('settingsView.removeSTRDoneToast', { n: (r.removedFiles || []).length }));
          purgeSpeakerLocalState(box, state); // forget every local record of this speaker (speakerPurge.js)
          // The speaker is a stock Bose box again, by design. Do not let the
          // reconnect loop chase an agent that is gone on purpose (it ended in
          // "agent died, unplug the speaker", 2026-09-06 report): stop any
          // pending retry, show what happened, and hand Speaker Settings a
          // record that already says stock so a re-render lands on the same
          // panel until the box list carries the backend's rewritten record.
          if (state.settingsReconnect && state.settingsReconnect.timer) clearTimeout(state.settingsReconnect.timer);
          state.settingsReconnect = null;
          state.strRemovedHost = box.host;
          state.settingsBox = { ...box, kind: 'stock', version: '', build: '', port: 8090, portVerified: false, strSilent: false };
          renderSTRRemovedPanel($('settingsBody'), state.settingsBox);
          // The backend rewrote the cached record as stock; refresh the list
          // now so the Listen to music card drops the version badge at once,
          // and re-scan in 60 s once the box is back from its reboot.
          deps.discoverBoxes();
          setTimeout(deps.discoverBoxes, 60000);
          return;
        } else if (r && r.stickPresent) {
          showError(t('settingsView.removeSTRStickPresent'));
        } else {
          showError(t('settingsView.removeSTRFailed', { msg: (r && r.message) || '?' }));
        }
      } catch (e) {
        showError(e);
      } finally {
        rmBtn.disabled = false;
        rmBtn.textContent = t('settingsView.removeSTRBtn');
      }
    };
  }
}
