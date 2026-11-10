// views/setup/appush.js: the panel under the factory-reset card that pushes
// Wi-Fi credentials straight to a speaker on its own setup network. Extracted
// from views/setup.js.

import { $, escapeHtml, escapeAttr, confirmWarn, showError } from '../../utils.js';
import { t } from '../../i18n/index.js';
import { ListWiFiProfiles, TryWiFiPassword, ProbeSetupAP, PushWLANToBox } from '../../api.js';
import { isMacOS } from './shared.js';

// renderSetupAPPushPanel populates the in-card panel that drives
// the direct WLAN-credentials push to a Bose setup-AP. Probes once
// for a reachable box at 192.168.1.1; on hit, shows the push form
// pre-filled from the saved Wi-Fi profile of whatever home network
// the user picked. On miss, shows the "switch your PC to the Bose
// SoundTouch Wi-Fi" guidance plus a retry button.
export async function renderSetupAPPushPanel() {
  const panel = $('setupAPPushPanel');
  if (!panel) return;

  panel.innerHTML = `<div class="setup-ap-push-loading muted small">${escapeHtml(t('setupAPPush.probing'))}</div>`;
  let probe;
  try {
    probe = await ProbeSetupAP();
  } catch {
    probe = null;
  }
  // One object with both fields. The comment that stood here said Wails
  // returns multi-value as an array, and it does not: a bound method with two
  // results loses the second unless it is an error, so the answer arrived as
  // the BoxInfo alone. BoxInfo has neither a found nor a box field, so both of
  // these read undefined and this panel believed nothing had been found, every
  // time it ran.
  const found = !!(probe && probe.found);
  const box = probe && probe.box;

  if (!found || !box) {
    panel.innerHTML = `
      <div class="setup-ap-push-not-found">
        <h4>${escapeHtml(t('setupAPPush.title'))}</h4>
        <div class="setup-ap-push-warn">${escapeHtml(t('setupAPPush.stickStillNeeded'))}</div>
        <p class="muted small">${escapeHtml(t('setupAPPush.instructionsBody'))}</p>
        <ol class="setup-ap-push-steps">
          <li>${escapeHtml(t('setupAPPush.step1'))}</li>
          <li>${escapeHtml(t('setupAPPush.step2'))}</li>
          <li>${escapeHtml(t('setupAPPush.step3'))}</li>
        </ol>
        <button class="btn" id="apPushRetry">${escapeHtml(t('setupAPPush.retryBtn'))}</button>
      </div>
    `;
    const retry = $('apPushRetry');
    if (retry) retry.onclick = () => renderSetupAPPushPanel().catch(() => {});
    return;
  }

  // We have a setup-AP. Pull the WiFi-profile list so the SSID
  // dropdown picks up the user's saved networks (most users tested
  // this with a single home Wi-Fi).
  let profiles = [];
  try { profiles = await ListWiFiProfiles() || []; } catch {}

  const ssidOpts = profiles.length
    ? profiles.map(p => `<option value="${escapeAttr(p.ssid)}">${escapeHtml(p.ssid)}</option>`).join('')
    : `<option value="">${escapeHtml(t('setupAPPush.noSavedProfile'))}</option>`;

  panel.innerHTML = `
    <div class="setup-ap-push-found">
      <h4>${escapeHtml(t('setupAPPush.foundTitle', { model: box.model || 'Bose SoundTouch' }))}</h4>
      <div class="setup-ap-push-warn">${escapeHtml(t('setupAPPush.stickStillNeeded'))}</div>
      <p class="muted small">${escapeHtml(t('setupAPPush.foundBody'))}</p>
      <div class="setup-ap-push-form">
        <label class="setup-ap-push-row">
          <span>${escapeHtml(t('setupAPPush.ssidLabel'))}</span>
          <select id="apPushSSID">${ssidOpts}</select>
        </label>
        <label class="setup-ap-push-row">
          <span>${escapeHtml(t('setupAPPush.passLabel'))}</span>
          <input type="password" id="apPushPass" placeholder="${escapeAttr(t('setupAPPush.passPlaceholder'))}" />
        </label>
        <label class="setup-ap-push-row">
          <span>${escapeHtml(t('setupAPPush.nameLabel'))}</span>
          <input type="text" id="apPushName" placeholder="${escapeAttr(t('setupAPPush.namePlaceholder'))}" />
        </label>
        <button class="btn btn-primary" id="apPushGo">${escapeHtml(t('setupAPPush.pushBtn'))}</button>
        <div id="apPushOutput" class="setup-ap-push-output"></div>
      </div>
    </div>
  `;

  // Auto-pull the saved password for whichever SSID is currently
  // picked, mirroring the existing box-wlan form behaviour.
  const ssidSel = $('apPushSSID');
  const passInp = $('apPushPass');
  const fillPassword = async () => {
    if (!ssidSel || !passInp) return;
    const v = ssidSel.value;
    if (!v) return;
    if (isMacOS) return; // macOS would prompt for Keychain admin
    try {
      const pw = await TryWiFiPassword(v);
      if (pw) passInp.value = pw;
    } catch {}
  };
  if (ssidSel) {
    ssidSel.onchange = fillPassword;
    fillPassword().catch(() => {});
  }

  const goBtn = $('apPushGo');
  if (goBtn) {
    goBtn.onclick = async () => {
      const ssid = ($('apPushSSID') || {}).value || '';
      const pass = ($('apPushPass') || {}).value || '';
      const name = ($('apPushName') || {}).value || '';
      if (!ssid) { showError(t('setupAPPush.ssidEmpty')); return; }
      // An empty password is right only for an open network. Anywhere else the
      // speaker cannot join, stays in setup mode and never shows up, which is
      // what a field report did on its first try. Ask, do not refuse.
      if (!pass) {
        const sendAnyway = await confirmWarn(
          t('setupAPPush.emptyPassTitle'),
          `<p>${escapeHtml(t('setupAPPush.emptyPassBody', { ssid }))}</p>`,
          { confirmLabel: t('setupAPPush.emptyPassSend'), confirmClass: 'btn btn-secondary' },
        );
        if (!sendAnyway) {
          const inp = $('apPushPass');
          if (inp) inp.focus();
          return;
        }
      }
      goBtn.disabled = true;
      const out = $('apPushOutput');
      if (out) out.innerHTML = `<div class="muted small">${escapeHtml(t('setupAPPush.pushing'))}</div>`;
      try {
        const result = await PushWLANToBox(box.host, ssid, pass, name);
        if (result && result.ok) {
          if (out) {
            const logHtml = (result.logTail || []).map(l => `<div>${escapeHtml(l)}</div>`).join('');
            out.innerHTML = `
              <div class="setup-ok">${escapeHtml(t('setupAPPush.successTitle'))}</div>
              <p>${escapeHtml(t('setupAPPush.successHint'))}</p>
              <details class="muted small">
                <summary>${escapeHtml(t('setupAPPush.detailsToggle'))}</summary>
                <div class="setup-ap-push-log">${logHtml}</div>
              </details>
            `;
          }
        } else {
          const msg = (result && result.message) || t('setupAPPush.unknownFailure');
          if (out) {
            const logHtml = ((result && result.logTail) || []).map(l => `<div>${escapeHtml(l)}</div>`).join('');
            out.innerHTML = `
              <div class="setup-warn">${escapeHtml(msg)}</div>
              <details class="muted small">
                <summary>${escapeHtml(t('setupAPPush.detailsToggle'))}</summary>
                <div class="setup-ap-push-log">${logHtml}</div>
              </details>
            `;
          }
          goBtn.disabled = false;
        }
      } catch (e) {
        if (out) out.innerHTML = `<div class="setup-warn">${escapeHtml(String(e))}</div>`;
        goBtn.disabled = false;
      }
    };
  }
}
