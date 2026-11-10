// update/failreport.js: what an update puts on screen about ONE speaker when
// it did not end plainly: the copyable failure report, the "has not come
// back" checklist, the SoundTouch 300 power-cycle notice and the short "this
// computer has no network" notice, and where each of them goes (the classic
// overlay host, or in the UI 2.0 preview that speaker's page or its row in the
// Update all window). Extracted from the main.js monolith; the pure helpers of
// the report text live in src/failreport.js.

import { BrowserOpenURL, SaveDiagnosticBundle, UpdateFailureReport } from '../api.js';
import { darkChecklistHtml, wireDarkChecklist } from '../darkafterupdate.js';
import { appendSavedBundlePath, failReportSaveHosts } from '../failreport.js';
import { t } from '../i18n/index.js';
import { openSpeakers } from '../shell2/speakers/index.js';
import { speakerReportSlot } from '../shell2/speakers/speaker.js';
import { state } from '../state.js';
import { $, escapeHtml, getBoxLabel, showError, showToast } from '../utils.js';
import { ustate } from './state.js';

// showSt300PowerCycleNotice puts the SoundTouch 300 power-cycle instruction on
// screen until the user dismisses it. After an install or agent OTA the 300
// always sits blinking and unreachable until it is unplugged once; a toast
// vanished before people read it and they thought the soundbar was dead
// (Jens 2026-08-30), so this one step gets a modal, reusing the failure-report
// overlay host.
export function showSt300PowerCycleNotice(box) {
  // The preview says it on that speaker's page (ui2ReportHost).
  const host = ui2ReportHost(box) || $('updateFailureReport');
  if (!host) { showToast(t('update.st300PowerCycle')); return; }
  host.innerHTML = `
    <div class="failreport-inner">
      <div class="failreport-title">${escapeHtml(t('update.st300NoticeTitle'))}</div>
      <p>${escapeHtml(t('update.st300PowerCycle'))}</p>
      <div class="failreport-actions">
        <button class="btn btn-primary btn-mini" id="st300NoticeClose">${escapeHtml(t('common.close'))}</button>
      </div>
    </div>`;
  host.classList.remove('hidden');
  focusUi2Report(host);
  const c = host.querySelector('#st300NoticeClose');
  if (c) c.onclick = () => { host.classList.add('hidden'); host.innerHTML = ''; };
}

// showDarkAfterUpdate puts the "has not come back" checklist on screen for a
// speaker whose update ended with nothing answering at its address. Same host
// as the failure report: the speaker's own page in the UI 2.0 preview, the
// overlay in the classic layout. It stays until the user closes it.
export function showDarkAfterUpdate(box) {
  const host = ui2ReportHost(box) || $('updateFailureReport');
  if (!host) { showToast(t('update.tookLongerToast')); return; }
  host.innerHTML = darkChecklistHtml({ name: getBoxLabel(box), box, context: 'update', framed: true });
  host.classList.remove('hidden');
  focusUi2Report(host);
  wireDarkChecklist(host, {
    openURL: (url) => { try { BrowserOpenURL(url); } catch {} },
    close: () => { host.classList.add('hidden'); host.innerHTML = ''; },
  });
}

// updateAllShowing: an Update all run is going, or its window is still open
// (it stays open after the run until the user closes it).
function updateAllShowing() {
  if (ustate.updateAllBusy) return true;
  const o = $('updateAllOverlay');
  return !!(o && !o.classList.contains('hidden'));
}

// ui2ReportHost is where the UI 2.0 preview shows an update outcome about one
// speaker (rule 3, errors where they happen). null in the classic layout, or
// when there is no such place, and the caller falls back to the classic
// overlay host #updateFailureReport.
//
// A single update: the report slot in that speaker's Overview, with the page
// opened on it. During Update all: that speaker's row in the Update all
// window, where the run is shown. Opening the page there would pick the
// speaker you control (the page always shows that one) and change the view
// behind the open window while the other speakers are still updating.
function ui2ReportHost(box) {
  if (!state.ui2Shell || !box || !box.host) return null;
  if (updateAllShowing()) return updateAllRowReport(box.host);
  try {
    if (!openSpeakers('speaker', { host: box.host, tab: 'overview' })) return null;
    return speakerReportSlot(box.host);
  } catch { return null; }
}

// updateAllRowReport is the report slot under one speaker's row in the Update
// all window, made on first use; null when the window has no row for it.
export function updateAllRowReport(host) {
  const list = $('uaList');
  if (!list) return null;
  const row = [...list.querySelectorAll('.ua-row')].find((r) => r.dataset.host === host);
  if (!row) return null;
  let slot = row.querySelector(':scope > .ua-report');
  if (!slot) {
    slot = document.createElement('div');
    slot.className = 'ua-report';
    row.appendChild(slot);
  }
  return slot;
}

// focusUi2Report makes the report's title a heading in the preview and puts
// the keyboard on it, so the report is announced where it appeared.
function focusUi2Report(host) {
  if (!host || host.id === 'updateFailureReport') return;
  const title = host.querySelector('.failreport-title');
  if (!title) return;
  title.setAttribute('role', 'heading');
  title.setAttribute('aria-level', '3');
  title.tabIndex = -1;
  // In the Update all window the keyboard moves only while that window is on
  // screen: one the user closed keeps running out of sight, and the report
  // waits in its row for the window to be opened again.
  const ua = host.closest('#updateAllOverlay');
  if (ua && ua.classList.contains('hidden')) return;
  // The update's own error dialog may still be open on top: the keyboard
  // stays in it, and moves to the report once the dialog is closed.
  const modal = $('errorModal');
  if (modal && !modal.classList.contains('hidden') && typeof MutationObserver !== 'undefined') {
    const mo = new MutationObserver(() => {
      if (!modal.classList.contains('hidden')) return;
      mo.disconnect();
      try { title.focus(); } catch {}
    });
    mo.observe(modal, { attributes: true, attributeFilter: ['class'] });
    return;
  }
  try { title.focus(); } catch {}
}

// showUpdateFailureReport offers the user a copyable account of an update that
// did not reach its goal, gathered while the evidence is still there.
//
// A failure the user cannot describe is a failure that gets reported as "it
// does not work", and by the time anyone asks for a diagnostic the speaker has
// usually been restarted and the trail is cold. Everything relevant is
// collected at the moment it happens, shown in full so the user can read it
// before sending, and copyable in one click.
export async function showUpdateFailureReport(box, phase, errMsg) {
  if (!box || !box.host) return;
  let report = '';
  try {
    report = await UpdateFailureReport(box.host, box.port, phase, errMsg,
      (state.appInfo && state.appInfo.version) || '');
  } catch { return; }
  if (!report) return;
  // The preview shows the report on that speaker's page (ui2ReportHost).
  const host = ui2ReportHost(box) || $('updateFailureReport');
  if (!host) return;
  host.innerHTML = `
    <div class="failreport-inner">
      <div class="failreport-title">${escapeHtml(t('update.reportTitle', { name: getBoxLabel(box) }))}</div>
      <p class="muted small">${escapeHtml(t('update.reportHelp'))}</p>
      <textarea class="failreport-text" id="failReportText" readonly rows="14">${escapeHtml(report)}</textarea>
      <div class="failreport-actions">
        <button class="btn btn-primary btn-mini" id="failReportCopy">${escapeHtml(t('update.reportCopy'))}</button>
        <button class="btn btn-mini" id="failReportSaveLogs">${escapeHtml(t('footer.saveLogs'))}</button>
        <button class="btn btn-mini" id="failReportMail">${escapeHtml(t('update.reportMail'))}</button>
        <button class="btn btn-secondary btn-mini" id="failReportClose">${escapeHtml(t('common.close'))}</button>
      </div>
      <p class="muted small">${escapeHtml(t('update.reportAttachHint'))}</p>
    </div>`;
  host.classList.remove('hidden');
  focusUi2Report(host);
  const ta = host.querySelector('#failReportText');
  // Read the textarea rather than the closure: saving the diagnostic bundle
  // rewrites the closing block to name the file that was just written, and the
  // copied text has to carry that path, not the version from before the save.
  const currentText = () => (ta && ta.value) || report;
  const copy = host.querySelector('#failReportCopy');
  if (copy) copy.onclick = async () => {
    try { await navigator.clipboard.writeText(currentText()); }
    catch { if (ta) { ta.select(); document.execCommand('copy'); } }
    copy.textContent = t('update.reportCopied');
  };
  // The update modal gets the same one-click bundle as the install screens.
  // A failed OTA is the other moment a user has no idea where the diagnostic
  // file would be, and the report itself now points at this button.
  const saveLogs = host.querySelector('#failReportSaveLogs');
  if (saveLogs) saveLogs.onclick = async () => {
    saveLogs.classList.add('working');
    try {
      const r = await SaveDiagnosticBundle(failReportSaveHosts(box, state.boxes), true);
      if (r && r.savePath) {
        showToast(t('footer.saveLogsDone', { path: r.savePath, size: Math.round((r.bytes || 0) / 1024) }));
        if (ta) ta.value = appendSavedBundlePath(currentText(), r.savePath);
      }
    } catch (e) { showError(String(e)); }
    finally { saveLogs.classList.remove('working'); }
  };
  const mail = host.querySelector('#failReportMail');
  if (mail) mail.onclick = () => {
    try {
      BrowserOpenURL('mailto:str@sichtbar-app.de?subject=' +
        encodeURIComponent('ST Reborn update failed') + '&body=' + encodeURIComponent(currentText()));
    } catch {}
  };
  const close = host.querySelector('#failReportClose');
  if (close) close.onclick = () => host.classList.add('hidden');
}

// showUpdateNoNetworkNotice is the short, dismissible version of the failure
// report, for the one cause that is not about the speaker at all.
//
// The full report exists so a real fault can be sent to me, and it earns its
// weight then. A computer that lost its network produces nothing worth reading
// in it: the speaker is untouched, nothing needs unplugging, and the whole
// answer is one sentence that the app already owns for the speaker list.
export function showUpdateNoNetworkNotice() {
  showError(t('settingsView.noNetworkTitle') + ' ' + t('settingsView.noNetworkHelp'));
}
