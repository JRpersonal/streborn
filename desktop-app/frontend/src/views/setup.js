// views/setup.js — the "USB stick setup" / install wizard view.
//
// Extracted from the main.js monolith, same pattern as views/settings.js and
// views/recent.js: the module pulls shared things (state, utils, i18n, api,
// localization) from their own modules and receives the few main.js-local
// helpers it needs (switchView, discoverBoxes, doBoxUpdate, getRoomNames) via
// initSetupView, so it never imports back into main.js (which would create a
// cycle). New views should follow this pattern so main.js stops growing.
//
// Entry point: renderSetupTargetPicker() is called from main.js's switchView
// when the Setup tab is opened (and again on every discoverBoxes refresh so
// newly arrived speakers appear in the target picker). It mounts the view's
// static shell on first call (mountSetupShell), then paints the picker.
//
// CRITICAL — lazy mount: this module is imported at the TOP of main.js, which
// runs BEFORE main.js's body creates the #view-setup container. Touching the DOM
// at module-eval time would throw and blank the whole app (a previous extraction
// did exactly that). So NOTHING here touches the DOM at module scope: the
// #view-setup shell HTML and all its control wiring live inside mountSetupShell,
// guarded to run once, and renderSetupTargetPicker calls it first.
//
// The view is split by topic under views/setup/. This file keeps the shell
// mount (below) and re-exports the entry points, so main.js and the UI 2.0
// wizard keep importing them from here.

import { $, escapeHtml, escapeAttr } from '../utils.js';
import { t, getLocale } from '../i18n/index.js';
import { COUNTRIES, optFlag } from '../localization.js';
// langOptionsHtml + wireCombobox are exported by the settings view (the Bose
// language dropdown and the name combobox are shared between Settings and Setup);
// reuse them rather than duplicating.
import { langOptionsHtml, wireCombobox } from './settings.js';
import { SuggestBoxLanguage } from '../api.js';
import { deps, initSetupView } from './setup/deps.js';
import { showAwaitBoxReadyPanel } from './setup/await.js';
import {
  togglePasswordVisibility,
  loadWifiProfiles,
  onWifiSelect,
  refreshDrives,
  doSetup,
} from './setup/stick.js';

// The entry points other files import from here (main.js, views/boxpicker.js,
// shell2/setup/wizard.js). They live in the topic modules now.
export { loadWifiProfiles, refreshDrives, initSetupView };
export { installRunActive } from './setup/hero.js';
export { renderSetupTargetPicker } from './setup/picker.js';

// mountSetupShell stays in this file, and the picker and the stick wizard both
// call it; they cannot import this file (it imports them), so it is handed to
// them the same way main.js hands its helpers in. No DOM is touched here.
initSetupView({ mountSetupShell });

// mountSetupShell builds the setup view's static shell (#view-setup) and wires
// its controls. Run LAZILY on first open, NOT at module import: this module is
// imported at the top of main.js, before the #view-setup container is created
// further down, so touching the DOM at import time threw and blanked the whole
// app. renderSetupTargetPicker (the entry point) calls this once first.
let setupShellMounted = false;
function mountSetupShell() {
  if (setupShellMounted) return;
  const root = $('view-setup');
  if (!root) return;
  setupShellMounted = true;
  root.innerHTML = `
    <h2>${escapeHtml(t('setup.heading'))}</h2>
    <div class="setup-section setup-target-section" id="setupTargetSection">
      <h3>${escapeHtml(t('setup.targetHeading'))}</h3>
      <div id="setupTargetBody"></div>
    </div>
    <div id="setupPrimaryAction" class="setup-primary-action"></div>
    <div id="setupResult" class="setup-result"></div>
    <details class="setup-stick-details" id="setupStickDetails">
      <summary class="setup-stick-summary">
        <span class="setup-stick-summary-label">${escapeHtml(t('setup.useStickInstead'))}</span>
        <span class="muted small setup-stick-summary-hint">${escapeHtml(t('setup.useStickInsteadHint'))}</span>
      </summary>
    <div class="setup-section">
      <div class="setup-section-head">
        <h3>${escapeHtml(t('setup.step1Heading'))}</h3>
        <button class="btn btn-mini" id="drivesRefresh">${escapeHtml(t('setup.searchAgain'))}</button>
      </div>
      <div id="drivesList">${escapeHtml(t('setup.searchingSticks'))}</div>
      <label class="format-toggle">
        <input type="checkbox" id="setupFormat" />
        <span>${t('setup.formatToggleLabel')}${/win/i.test(navigator.platform || '') ? ' ' + t('setup.formatToggleWinHint') : ''}</span>
      </label>
    </div>
    <div class="setup-section" id="nameSection">
      <h3>${t('setup.step2Heading')}</h3>
      <p class="muted small">${escapeHtml(t('setup.step2Help'))}</p>
      <div class="combobox" id="setupNameCombo">
        <input type="text" id="setupName" autocomplete="off" placeholder="${escapeAttr(t('setup.namePlaceholder'))}" />
        <button type="button" class="combo-toggle" id="setupNameToggle">&#9662;</button>
        <ul class="combo-list hidden" id="setupNameList"></ul>
      </div>
    </div>
    <div class="setup-section" id="regionSection">
      <h3>${escapeHtml(t('setup.step3Heading'))}</h3>
      <p class="muted small">${escapeHtml(t('setup.step3Help'))}</p>
      <select id="setupRegion"></select>
      <label class="setup-sublabel" for="setupLang">${escapeHtml(t('setup.langLabel'))}</label>
      <select id="setupLang"></select>
      <p class="muted small">${escapeHtml(t('setup.langHelp'))}</p>
    </div>
    <div class="setup-section" id="wlanSection">
      <h3>${t('setup.step4Heading')}</h3>
      <p class="muted small">${escapeHtml(t('setup.step4Help'))}</p>
      <div class="wlan-row">
        <select id="wlanSelect">
          <option value="">${escapeHtml(t('settingsView.wlanPickPlaceholder'))}</option>
        </select>
        <button class="btn btn-icon-sm" id="wlanRefresh" title="${escapeAttr(t('setup.wlanRefreshTitle'))}">&#x21bb;</button>
      </div>
      <input type="text" id="wlanSsid" placeholder="${escapeAttr(t('settingsView.wlanSsidPlaceholder'))}" />
      <div class="wlan-row">
        <input type="password" id="wlanPass" placeholder="${escapeAttr(t('setup.wlanPassPlaceholder'))}" />
        <button class="btn btn-icon-sm" id="wlanShowPass" title="${escapeAttr(t('settingsView.wlanShowPass'))}">&#128065;</button>
      </div>
    </div>
    <div class="setup-section">
      <h3>${escapeHtml(t('setup.step5Heading'))}</h3>
      <div id="updateInfo" class="update-info hidden"></div>
      <div id="formatWarn" class="format-warn hidden">
        <div class="warn-icon-inline">&#9888;</div>
        <div>${t('setup.formatWarnBody')}</div>
      </div>
      <button class="btn btn-primary" id="setupGo" disabled>${escapeHtml(t('setup.goBtn'))}</button>
    </div>
    <div class="setup-section setup-skip-section" id="setupSkipSection">
      <button class="btn" id="setupSkipToInstall">${escapeHtml(t('setup.skipToInstallBtn'))}</button>
      <p class="muted small">${escapeHtml(t('setup.skipToInstallHint'))}</p>
    </div>
    </details>
    <div id="setupAwaitResult" class="setup-result"></div>
  `;

  // Wire the setup-tab name combobox with the same helper used in
  // Settings.
  wireCombobox('setupName', 'setupNameToggle', 'setupNameList', deps.getRoomNames());

  // Region dropdown in the setup wizard reuses the same countries as
  // the radio filter. Default Germany.
  (function fillSetupRegion() {
    const sel = $('setupRegion');
    if (!sel) return;
    sel.innerHTML = COUNTRIES.filter(c => c.cc).map(c =>
      `<option value="${c.cc}">${optFlag(c.cc)}${escapeHtml(c.name)}</option>`
    ).join('');
    const saved = (() => { try { return localStorage.getItem('setupRegion'); } catch { return null; }})();
    sel.value = saved || 'DE';
  })();

  // Language dropdown in the wizard: the 25 box languages as native
  // endonyms (so any speaker recognises theirs regardless of the app UI
  // language), pre-selected intelligently from the chosen country via
  // SuggestBoxLanguage (country primary, deliberate app language as
  // override, English floor). The user can override; once they do, a
  // later country change no longer moves the selection out from under them.
  (function fillSetupLang() {
    const sel = $('setupLang');
    const region = $('setupRegion');
    if (!sel) return;
    sel.innerHTML = langOptionsHtml();
    let userPicked = false;
    const preselect = async () => {
      if (userPicked) return;
      try {
        const cc = region ? region.value : '';
        const id = await SuggestBoxLanguage(getLocale(), cc);
        if (id) sel.value = String(id);
      } catch { /* leave the dropdown default on a binding error */ }
    };
    sel.addEventListener('change', () => { userPicked = true; });
    if (region) region.addEventListener('change', preselect);
    preselect();
  })();

  $('drivesRefresh').onclick = () => refreshDrives(true);
  // Skip-to-install: the user already prepared a stick and plugged it into the
  // speaker, so jump straight to "wait for the speaker, then install STR over
  // the network", bypassing the PC stick-prepare steps entirely. ssid/pass are
  // blank here (the stick already carries the WLAN config); the await panel just
  // waits for the box to appear and runs the install.
  $('setupSkipToInstall').onclick = () => {
    showAwaitBoxReadyPanel({ ssid: '', pass: '', html: `<div class="setup-ok">${escapeHtml(t('setup.skipToInstallBtn'))}</div>` });
  };
  $('setupGo').onclick = doSetup;
  $('wlanRefresh').onclick = async () => {
    const rb = $('wlanRefresh');
    rb.classList.add('spinning'); // consistent spin feedback like the other refresh buttons
    try { await loadWifiProfiles(); } finally { rb.classList.remove('spinning'); }
  };
  $('wlanSelect').onchange = onWifiSelect;
  $('wlanShowPass').onclick = togglePasswordVisibility;
}
