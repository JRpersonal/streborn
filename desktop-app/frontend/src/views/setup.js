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

import { state } from '../state.js';
import { $, escapeHtml, escapeAttr, boxModelSupport, modelNoteKey } from '../utils.js';
import { t, getLocale } from '../i18n/index.js';
import { COUNTRIES, optFlag } from '../localization.js';
// langOptionsHtml + wireCombobox are exported by the settings view (the Bose
// language dropdown and the name combobox are shared between Settings and Setup);
// reuse them rather than duplicating.
import { langOptionsHtml, wireCombobox } from './settings.js';
import { SuggestBoxLanguage, InstallHolds, BrowserOpenURL, PhoneQR } from '../api.js';
import { holdsByHost, holdFor, sameHeldHosts, stockCardText } from '../installhold.js';
import { deps, initSetupView } from './setup/deps.js';
import { showAwaitBoxReadyPanel } from './setup/await.js';
import {
  togglePasswordVisibility,
  loadWifiProfiles,
  onWifiSelect,
  refreshDrives,
  doSetup,
} from './setup/stick.js';
import { renderSetupAPPushPanel } from './setup/appush.js';
import { syncHeroInstallHold, renderPrimaryAction } from './setup/hero.js';

// The entry points other files import from here (main.js, views/boxpicker.js,
// shell2/setup/wizard.js). They live in the topic modules now.
export { loadWifiProfiles, refreshDrives, initSetupView };
export { installRunActive } from './setup/hero.js';

// mountSetupShell stays in this file, and the picker and the stick wizard both
// call it; they cannot import this file (it imports them), so it is handed to
// them the same way main.js hands its helpers in. No DOM is touched here.
initSetupView({ mountSetupShell });

// Official Bose SoundTouch app store listings (verified live 2026-07-09). The
// app's local Wi-Fi setup still works after the cloud shutdown, so it is the
// recommended way to get a factory-reset speaker onto the LAN before STR's
// stick-free network install takes over.
const BOSE_APP_ANDROID_URL = 'https://play.google.com/store/apps/details?id=com.bose.soundtouch';
const BOSE_APP_IOS_URL = 'https://apps.apple.com/app/bose-soundtouch/id708379313';

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

// ---------- Setup target picker ----------
//
// The failure mode in #44: when more than one stock Bose speaker
// is on the LAN, the install-after-prep step picked an arbitrary
// one — the user had no way to see *which* speaker the wizard
// would target until install.sh ran against the wrong box. Picker
// also covers brand-new / factory-reset speakers that are not yet
// on the network at all (cold-bootstrap target).
//
// Three target kinds:
//   - "str"           — speaker already runs STR. Stick prep = update.
//   - "stock"         — speaker on LAN, factory firmware. Install via SSH.
//   - "factory-reset" — no box yet on the LAN, cold-bootstrap from
//                       its Bose setup-AP after the stick boots.
//
// state.setupTarget holds the active choice. renderSetupTargetPicker
// is called whenever discoverBoxes() refreshes state.boxes so newly
// arrived speakers appear in the picker, but the user's chosen
// target is preserved unless the chosen speaker actually drops off
// the LAN (brief mDNS gaps would otherwise yank the choice).

export function renderSetupTargetPicker() {
  mountSetupShell(); // build the shell on first open (see note above)
  const body = $('setupTargetBody');
  if (!body) return;

  // Pull the candidate list from state. dedup by host so a speaker
  // that announced twice (mDNS + active-probe fallback) only shows
  // up once. Sort: stock first (most common bootstrap target),
  // then STR, alphabetically inside each kind.
  const seen = new Set();
  const stockBoxes = [];
  const strBoxes = [];
  for (const b of (state.boxes || [])) {
    if (!b || !b.host) continue;
    if (seen.has(b.host)) continue;
    seen.add(b.host);
    // Every stock Bose speaker is an install target now, including soundbars /
    // home-cinema systems / adapters ('limited'): a box reachable by IP installs
    // over the network, so none is filtered out. The card adds a hardware-preset-
    // button caveat for 'limited' models below.
    if (b.kind === 'stock') stockBoxes.push(b);
    else if (b.kind === 'str') strBoxes.push(b);
  }
  const byName = (a, b) => (a.friendlyName || a.name || '').localeCompare(b.friendlyName || b.name || '');
  stockBoxes.sort(byName);
  strBoxes.sort(byName);

  // If the previously chosen STR/stock box has vanished from the
  // LAN, drop the cached target so the picker UI does not show a
  // dead row as "selected". Factory-reset target is preserved
  // regardless (the box is by definition not on the LAN).
  if (state.setupTarget && state.setupTarget.box) {
    const stillThere = state.setupTarget.kind === 'factory-reset'
      || (state.boxes || []).some(b => b && b.host === state.setupTarget.box.host);
    if (!stillThere) state.setupTarget = null;
  }

  // Default selection: prefer the box currently focused in the
  // Music tab (most common path: user picked a speaker, then went
  // to Setup). If that does not match a candidate (or is null),
  // fall back to the first stock box, then the first STR box,
  // then factory-reset.
  if (!state.setupTarget) {
    const cur = state.currentBox;
    if (cur && (cur.kind === 'stock' || cur.kind === 'str')) {
      const match = [...stockBoxes, ...strBoxes].find(b => b.host === cur.host);
      if (match) state.setupTarget = { kind: match.kind, box: match };
    }
    if (!state.setupTarget && stockBoxes.length > 0) {
      state.setupTarget = { kind: 'stock', box: stockBoxes[0] };
    }
    if (!state.setupTarget && strBoxes.length > 0) {
      state.setupTarget = { kind: 'str', box: strBoxes[0] };
    }
    // We deliberately do NOT auto-select factory-reset as the
    // default — that path triggers a different post-prep flow
    // (cold-bootstrap), so the user should pick it consciously.
  }

  const sel = state.setupTarget;
  const isSelected = (kind, host) => sel
    && sel.kind === kind
    && ((kind === 'factory-reset' && !host) || (sel.box && sel.box.host === host));

  // Each target is a single-choice radio. The radio dot + role="radio"
  // make it obvious these cards are mutually-exclusive selections (and
  // not just info rows), which is the whole point of the picker.
  const cardHTML = (kind, host, label, sublabel, badge, badgeClass = '') => {
    const selected = isSelected(kind, host);
    const cls = `setup-target-card${selected ? ' selected' : ''}`;
    return `<button type="button" role="radio" aria-checked="${selected ? 'true' : 'false'}" class="${cls}" data-kind="${kind}" data-host="${escapeAttr(host || '')}">` +
      `<div class="stc-row1"><span class="stc-left"><span class="stc-radio" aria-hidden="true"></span>` +
      `<span class="stc-label">${escapeHtml(label)}</span></span>` +
      `<span class="stc-badge ${badgeClass}">${escapeHtml(badge)}</span></div>` +
      (sublabel ? `<div class="stc-sublabel">${escapeHtml(sublabel)}</div>` : '') +
      `</button>`;
  };

  let cards = '';
  if (stockBoxes.length === 0 && strBoxes.length === 0) {
    cards += `<div class="muted small setup-target-empty">${escapeHtml(t('setup.targetEmpty'))}</div>`;
    // The single most common reason for an empty list: the speaker is not on
    // any Wi-Fi yet. That first step belongs to the ORIGINAL Bose app (its
    // local setup still works after the shutdown), and nobody can know that
    // unless the app says it right here. A field mail (2026-08-30) burned ten
    // stick attempts across four speakers that were never on the network.
    cards += `<div class="setup-warn setup-target-empty-hint">${escapeHtml(t('setup.targetEmptyBoseApp'))}</div>`;
    // ... and the way back INTO the Bose app for a speaker that still holds a
    // dead Wi-Fi (router replaced): the hardware combo that puts it into its
    // setup network. Verified live: key 1 + volume-down clears pairing and
    // presets, opens the setup AP for a few minutes, and leaves the STR
    // install untouched (reset-layers, 2026-05-24).
    cards += `<div class="muted small setup-target-empty-reset">${escapeHtml(t('setup.targetEmptySetupMode'))}</div>`;
  }
  // boxIdentLine builds the sublabel pieces (model, serial, host)
  // that help users distinguish two or three identical speakers on
  // the same LAN. Each piece is skipped when empty so we never
  // render dangling separators. Serial is shown in full because the
  // Bose-printed sticker on the bottom of the speaker is what users
  // will compare against; truncating to "last 6" would defeat the
  // point.
  const boxIdentLine = (b, kindLabel) => {
    const parts = [];
    if (b.model) parts.push(b.model);
    parts.push(kindLabel);
    if (b.serialNumber) parts.push(`SN ${b.serialNumber}`);
    parts.push(b.host);
    return parts.join(' · ');
  };
  for (const b of stockBoxes) {
    const label = b.friendlyName || b.name || b.host;
    // "first install" is informational, not a warning: the amber badge-warn
    // chip drew a contrast complaint twice (#692, #833) and reads as an alarm
    // for a normal stock speaker. The neutral .stc-badge base (--c-text on
    // --c-surface-3) is high-contrast in every theme and is what the #833 fix
    // intended before it landed on the wrong element.
    //
    // A speaker an update from this app is still finishing on looks stock to
    // discovery (its agent is not answering yet), but it is not a first
    // install, and saying so is what invites the very click the install guard
    // refuses (field case 2026-10-30). Its card says what is going on instead.
    const card = stockCardText(holdFor(state.installHolds, b.host));
    cards += cardHTML('stock', b.host, label,
      boxIdentLine(b, card.kind),
      card.badge, '');
    // What is known about this model, said under the card BEFORE the install
    // (docs/MODELS.md via boxModelSupport): no hardware preset buttons 1-6 on
    // soundbars, home-cinema systems and adapters, not confirmed yet on a model
    // MODELS.md has no row for, and no way at all on a non-SoundTouch device.
    const modelNote = modelNoteKey(b.model || b.type);
    if (modelNote) {
      cards += `<div class="setup-target-limited-note setup-target-model-note--${boxModelSupport(b.model || b.type)} muted small">${escapeHtml(t(modelNote))}</div>`;
    }
    // Still set up for another cloud replacement (AfterTouch, OpenCloudTouch):
    // its /info cloud address points at a server on the home network. Said
    // before the install, so a former AfterTouch user knows the switch is
    // expected and what the install does about it (field mail 2026-11-03).
    if (b.rivalCloudURL) {
      cards += `<div class="setup-target-rival-note muted small">${escapeHtml(t('setup.rivalCloudNote', { url: b.rivalCloudURL }))}</div>`;
    }
  }
  // Speakers that already run STR fold away. A household that has done this
  // once has four cards saying "done" and one saying "to do", all the same
  // size and the done ones first, so the eye lands on the wrong thing. They
  // are one click away, not hidden.
  //
  // Open when one of them IS the selection, or the picker would hide the
  // thing it is a picker for. state.setupDoneOpen remembers a manual toggle,
  // because this function re-renders on every click and the group would
  // otherwise snap shut under the hand that opened it.
  if (strBoxes.length > 0) {
    const selIsDone = sel && sel.kind === 'str' && sel.box
      && strBoxes.some(b => b.host === sel.box.host);
    const open = selIsDone || state.setupDoneOpen === true;
    let inner = '';
    for (const b of strBoxes) {
      const label = b.friendlyName || b.name || b.host;
      inner += cardHTML('str', b.host, label,
        boxIdentLine(b, t('setup.targetCardKindSTR')),
        t('setup.targetCardBadgeSTR'), 'badge-ok');
    }
    cards += `<details class="setup-done-group" id="setupDoneGroup"${open ? ' open' : ''}>`
      + `<summary>${escapeHtml(t('setup.targetDoneGroup', { n: strBoxes.length }))}</summary>`
      + inner
      + `</details>`;
  }
  // No "unsupported" list any more: every discovered stock box is an install
  // target above, so there is nothing non-selectable to render here.
  // Factory-reset card is always shown. Append the macOS hint
  // inline only if we're on macOS, otherwise just the standard help.
  const isMac = (typeof navigator !== 'undefined' &&
                 /Mac|iPhone|iPad|iPod/i.test(navigator.platform || navigator.userAgent || ''));
  cards += cardHTML('factory-reset', '', t('setup.targetCardKindFactory'), '', t('setup.targetCardBadgeFactory'));
  if (isSelected('factory-reset', '')) {
    cards += `<div class="setup-target-factory-help muted small">${escapeHtml(t('setup.targetCardFactoryHelp'))}</div>`;
    cards += `<div class="setup-target-factory-help muted small">${escapeHtml(t('setup.targetEmptySetupMode'))}</div>`;
    // Wi-Fi onboarding happens in the official Bose SoundTouch app (its local
    // setup still works after the cloud shutdown); once the speaker is on the
    // LAN, STR's network install takes over and no stick is needed. The app has
    // to go onto the user's PHONE, so each store link gets a small QR code
    // (painted async via PhoneQR after the innerHTML render) next to a plain
    // button for whoever wants the link on this machine.
    cards += `<div class="setup-factory-boseapp-links">
      <div class="boseapp-store">
        <img id="boseAppAndroidQr" class="boseapp-qr" alt="QR: Google Play" />
        <button type="button" class="btn" id="boseAppAndroidBtn">${escapeHtml(t('setup.boseAppAndroid'))}</button>
      </div>
      <div class="boseapp-store">
        <img id="boseAppIosQr" class="boseapp-qr" alt="QR: App Store" />
        <button type="button" class="btn" id="boseAppIosBtn">${escapeHtml(t('setup.boseAppIos'))}</button>
      </div>
    </div>`;
    if (isMac) {
      cards += `<div class="setup-target-factory-mac">${escapeHtml(t('setup.targetCardFactoryMacHint'))}</div>`;
    }
    // Setup-AP-Push panel: when the user is currently joined to a
    // Bose setup-AP (PC IP 192.168.1.100, box at 192.168.1.1), we
    // offer a direct WLAN credentials push that skips the stick
    // entirely. Live-verified 2026-05-30 on a factory-reset taigan
    // Portable. Renders into the placeholder div after the picker
    // commits, so the async ProbeSetupAP does not block the picker.
    cards += `<div id="setupAPPushPanel" class="setup-ap-push-panel"></div>`;
  }

  body.innerHTML = `<div class="setup-target-cards" role="radiogroup" aria-label="${escapeAttr(t('setup.targetHeading'))}">${cards}</div>`;

  // Store links for the Bose SoundTouch app (Wi-Fi onboarding). Wired after the
  // innerHTML render; stopPropagation so a tap never doubles as a card select.
  const wireStore = (id, url) => {
    const b = body.querySelector('#' + id);
    if (b) b.onclick = (e) => { e.stopPropagation(); BrowserOpenURL(url); };
  };
  wireStore('boseAppAndroidBtn', BOSE_APP_ANDROID_URL);
  wireStore('boseAppIosBtn', BOSE_APP_IOS_URL);
  // Small scannable QR per store link (the Bose app belongs on the phone, not
  // on this PC). Locally generated (PhoneQR, no external service); on failure
  // the QR img is dropped and the buttons remain.
  const paintStoreQr = async (id, url) => {
    const img = body.querySelector('#' + id);
    if (!img) return;
    try {
      img.src = await PhoneQR(url);
      img.classList.add('ready');
    } catch { img.remove(); }
  };
  paintStoreQr('boseAppAndroidQr', BOSE_APP_ANDROID_URL).catch(() => {});
  paintStoreQr('boseAppIosQr', BOSE_APP_IOS_URL).catch(() => {});

  // Paint the OTA-first primary action for the current target: a network-install
  // hero for a reachable stock box, or the collapsed stick wizard otherwise.
  renderPrimaryAction();

  // A manual open or close has to outlive the next render, which happens on
  // every card click.
  const doneGroup = $('setupDoneGroup');
  if (doneGroup) doneGroup.ontoggle = () => { state.setupDoneOpen = doneGroup.open; };

  body.querySelectorAll('.setup-target-card').forEach(el => {
    el.onclick = () => {
      const kind = el.getAttribute('data-kind');
      const host = el.getAttribute('data-host');
      if (kind === 'factory-reset') {
        state.setupTarget = { kind: 'factory-reset', box: null };
      } else {
        const list = kind === 'stock' ? stockBoxes : strBoxes;
        const box = list.find(b => b.host === host);
        if (!box) return;
        state.setupTarget = { kind, box };
        // Lock-step with the music tab and the Settings picker.
        if (deps.speakerPicked) deps.speakerPicked(box);
      }
      renderSetupTargetPicker();
      updateSetupGoButtonLabel();
    };
  });

  // Async fill of the Setup-AP push panel (only present when
  // factory-reset is the current selection). Decoupled from the
  // picker render so a slow probe does not stall the page.
  if (sel && sel.kind === 'factory-reset') {
    renderSetupAPPushPanel().catch(err => {
      console.warn('setup-ap push panel render failed', err);
    });
  }

  refreshInstallHolds(stockBoxes.map(b => b.host));
}

// installHoldsInFlight keeps refreshInstallHolds to one question at a time:
// the picker re-renders on every discovery refresh and on every click.
let installHoldsInFlight = false;

// refreshInstallHolds asks the backend which of the listed stock speakers an
// update from this app is still finishing on (install_guard.go, the same hold
// the install guard enforces) and repaints the picker when a speaker entered
// or left that hold. The answer costs no network I/O on the Go side. A failed
// call keeps the last answer: it is a hint in front of the guard, never the
// guard itself.
async function refreshInstallHolds(hosts) {
  if (installHoldsInFlight) return;
  installHoldsInFlight = true;
  let next;
  try {
    next = holdsByHost(hosts.length ? await InstallHolds(hosts) : []);
  } catch {
    return;
  } finally {
    installHoldsInFlight = false;
  }
  const changed = !sameHeldHosts(state.installHolds, next);
  state.installHolds = next;
  if (changed) {
    renderSetupTargetPicker();
    deps.installHoldsChanged();
  } else {
    syncHeroInstallHold();
  }
}

// updateSetupGoButtonLabel keeps the "Prepare" button text in sync
// with the chosen target. Used so users see "Prepare USB stick for
// Living Room ST20" rather than the generic label, which makes the
// connection between Step 0 and Step 5 explicit and was the
// concrete thing #44 asked for.
function updateSetupGoButtonLabel() {
  const btn = $('setupGo');
  if (!btn) return;
  const sel = state.setupTarget;
  if (sel && sel.kind === 'factory-reset') {
    btn.textContent = t('setup.goBtnFactory');
    return;
  }
  // For str/stock targets, fall back to whatever refreshDrives()
  // computed last (it sets the text to one of goBtn /
  // goBtnFormatPrepare / goBtnUpdate based on stick state). Do
  // nothing here — refreshDrives ran already during drive pick
  // and the existing label is correct for the picked stick.
}
