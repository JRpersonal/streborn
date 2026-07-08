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

import { state } from '../state.js';
import {
  $,
  escapeHtml,
  escapeAttr,
  sleep,
  confirmWarn,
  showError,
  showToast,
  formatRemaining,
  boxModelSupport,
} from '../utils.js';
import { t, getLocale } from '../i18n/index.js';
import { COUNTRIES, optFlag } from '../localization.js';
// langOptionsHtml + wireCombobox are exported by the settings view (the Bose
// language dropdown and the name combobox are shared between Settings and Setup);
// reuse them rather than duplicating.
import { langOptionsHtml, wireCombobox } from './settings.js';
import {
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
  SetBoxName,
  SetBoxLanguage,
  SetClockDisplay,
  SuggestBoxLanguage,
  InstallSTROnBox,
  RepairInstallViaSSH,
  BoxInstallReachable,
  ProbeSetupAP,
  PushWLANToBox,
  GetBoxFirmware,
  DiscoverBoxes,
  SaveDiagnosticBundle,
  EventsOn,
} from '../api.js';

// macOS gates the System keychain behind an admin prompt for every
// `security find-generic-password -ga`. Auto-firing the password
// fetch at app startup pops the prompt every single launch (#88).
// We still want auto-fill on Windows (netsh -> no prompt for
// user-saved profiles) and Linux (nmcli -> no prompt). On macOS,
// the SSID gets auto-selected but the password field stays empty
// until the user clicks the explicit fill-from-keychain button.
// Re-derived locally (the same pure check main.js uses) so the view does not
// need an injected helper for it.
const isMacOS = /Mac OS X|Macintosh/.test(navigator.userAgent);

// Timezone helpers for the OTA setup card. detectTimeZone returns the real IANA
// zone of THIS computer (e.g. "Europe/Berlin", "America/New_York", "Asia/Tokyo")
// with no regional bias. detectClockFormat24 derives 12h vs 24h from the OS
// locale so we never force a format on the box. tzOptionsHtml builds the dropdown
// from the platform's full IANA zone list when the runtime exposes it, always
// including and pre-selecting the detected zone. On install we push the picked
// IANA zone via SetClockDisplay(host, true, zone, 0, format24): with a real zone
// set the box handles DST itself, so the offset MUST stay 0 (a non-zero offset on
// top would double-shift the clock).
function detectTimeZone() {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ''; } catch { return ''; }
}
function detectClockFormat24() {
  try {
    const hc = Intl.DateTimeFormat(undefined, { hour: 'numeric' }).resolvedOptions().hourCycle || '';
    if (hc === 'h23' || hc === 'h24') return true;
    if (hc === 'h11' || hc === 'h12') return false;
  } catch {}
  return true; // global-neutral default when the runtime does not report an hour cycle
}
// Worldwide fallback list, used only when Intl.supportedValuesOf is unavailable
// (older WebViews). Deliberately spread across every continent, no home bias.
const TZ_FALLBACK = [
  'UTC',
  'Europe/London', 'Europe/Berlin', 'Europe/Paris', 'Europe/Madrid', 'Europe/Rome',
  'Europe/Amsterdam', 'Europe/Warsaw', 'Europe/Kyiv', 'Europe/Moscow', 'Europe/Istanbul',
  'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
  'America/Sao_Paulo', 'America/Mexico_City', 'America/Toronto',
  'Africa/Cairo', 'Africa/Johannesburg', 'Africa/Lagos',
  'Asia/Dubai', 'Asia/Kolkata', 'Asia/Shanghai', 'Asia/Tokyo', 'Asia/Singapore', 'Asia/Seoul',
  'Australia/Sydney', 'Pacific/Auckland',
];
function tzOptionsHtml(selected) {
  let zones = [];
  try {
    if (typeof Intl.supportedValuesOf === 'function') zones = Intl.supportedValuesOf('timeZone') || [];
  } catch {}
  if (!zones.length) zones = TZ_FALLBACK.slice();
  if (selected && zones.indexOf(selected) === -1) zones = [selected, ...zones];
  return zones.map(z =>
    `<option value="${escapeAttr(z)}"${z === selected ? ' selected' : ''}>${escapeHtml(z)}</option>`
  ).join('');
}

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

// Injected main.js helpers (see initSetupView). These stay in main.js because
// they are shared across views; the setup code calls them as deps.<name>.
let deps = {
  switchView: () => {},
  discoverBoxes: async () => {},
  doBoxUpdate: async () => {},
  getRoomNames: () => [],
  // celebrateProvision(box): fire the community world-map pin invite right after a
  // box is successfully provisioned with STR (the most reliable "alive again"
  // moment). Wired in main.js; no-op default so the view never depends on it.
  celebrateProvision: () => {},
  // boxFetch(box, path, opts): self-healing fetch to the installed agent's HTTP
  // API. Used to PUT /api/box/wlan right after a network install so a
  // cable-installed speaker joins Wi-Fi. Wired in main.js.
  boxFetch: async () => ({ ok: false }),
};
export function initSetupView(d) {
  deps = { ...deps, ...d };
}

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
        <span>${t('setup.formatToggleLabel')}</span>
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

// targetIsST30 reports whether a discovered box is a SoundTouch 30. The ST30's
// USB port cannot reliably keep a higher-draw stick powered: under read load
// VBUS collapses and the stick disconnects mid-install (a power error, not a
// faulty stick, the same stick works on ST10/ST20). We warn in the picker so
// the user reaches for a low-power stick before preparing one. Matches the
// human label discovery emits ("SoundTouch 30") and the raw mDNS codes as a
// fallback.
function targetIsST30(box) {
  if (!box) return false;
  const m = String(box.model || '').toLowerCase().replace(/[\s_]+/g, '');
  return m === 'soundtouch30' || m === 'st30' || m === '30';
}

// powerCycleAdviceHtml returns the post-install power-cycle advice as a ready-to-
// insert div, model-aware. A full power-cycle is what clears the coprocessor's
// undefined "light bar keeps sweeping" state that a normal restart leaves behind
// after an install (live-seen on ginger/SMSC ST300 and taigan). The battery
// Portable cannot be unplugged, so it gets the AUX button-combo variant (hold
// AUX, or hold AUX + volume-down); every mains-powered speaker keeps the
// unplug-from-the-wall advice.
function powerCycleAdviceHtml(box) {
  const m = String((box && box.model) || '').toLowerCase().replace(/[\s_]+/g, '');
  const v = String((box && box.variant) || '').toLowerCase();
  const isPortable = m.includes('portable') || v === 'taigan';
  const key = isPortable ? 'setup.powerCyclePortable' : 'setup.powerCycleAdvice';
  return `<div class="setup-powercycle muted small">${escapeHtml(t(key))}</div>`;
}

// Unverified-hardware warning (#283).
//
// KNOWN_VARIANTS is the set of Bose chassis codenames (FirmwareInfo.Variant) STR
// has actually been verified on: rhino=ST10, taigan=Portable, spotty=ST20,
// ginger=ST300, mojo=ST30. Any other variant — e.g. 'lisa' (SA-4 / Wave
// SoundTouch / CineMate) or a codename we have never seen — has never been
// confirmed with STR, so the picker flags it as untested hardware.
//
// Per Jens' "no unsupported devices, only a warning" direction this NEVER blocks
// the install: the box still installs / heals normally, the user is just told STR
// has not been confirmed on their model so they proceed knowingly and report back.
// An empty/unknown variant counts as verified (compatibility-first: never cry
// wolf on a box we could not fingerprint).
const KNOWN_VARIANTS = new Set(['rhino', 'taigan', 'spotty', 'ginger', 'mojo']);
function isUnverifiedVariant(variant) {
  const v = String(variant || '').toLowerCase().trim();
  if (!v) return false;
  return !KNOWN_VARIANTS.has(v);
}

// Per-host cache of the box firmware Variant, used only for the unverified-
// hardware warning. GetBoxFirmware hits the speaker's :8090 and the picker
// re-renders on every discovery refresh (a few seconds apart), so the variant is
// fetched once per host and reused — never polled in a loop. undefined = not yet
// fetched; a string (possibly '') = fetched.
const setupVariantCache = {};
const unverifiedWarnId = (host) => 'setup-unverified-' + String(host || '');

// unverifiedWarnSlot emits a box card's unverified-hardware slot. If the variant
// is already cached AND unverified the warning is rendered inline (so it does not
// flash away on the next discovery re-render); otherwise an empty, hidden slot is
// emitted for paintUnverifiedWarning to fill once the async firmware lookup lands.
function unverifiedWarnSlot(host) {
  const id = escapeAttr(unverifiedWarnId(host));
  const cached = setupVariantCache[host];
  const show = cached !== undefined && isUnverifiedVariant(cached);
  const body = show ? escapeHtml(t('setup.unverifiedHardware')) : '';
  return `<div class="setup-target-st30-warn setup-target-unverified-warn" id="${id}"${show ? '' : ' hidden'}>${body}</div>`;
}

// paintUnverifiedWarning fills a box card's unverified slot after resolving the
// firmware Variant (cached after the first lookup). Kept out of the synchronous
// picker render so a slow or unreachable box never stalls the list; a failed or
// unreachable probe leaves the slot hidden and is retried on the next render.
async function paintUnverifiedWarning(host) {
  if (!host) return;
  let variant = setupVariantCache[host];
  if (variant === undefined) {
    try {
      const f = await GetBoxFirmware(host);
      if (f && f.reachable) {
        variant = String(f.variant || '').toLowerCase().trim();
        setupVariantCache[host] = variant; // cache only a real answer
      } else {
        variant = ''; // unreachable this pass: retry next render, do not cache
      }
    } catch { variant = ''; }
  }
  const el = $(unverifiedWarnId(host));
  if (!el) return;
  if (isUnverifiedVariant(variant)) {
    el.textContent = t('setup.unverifiedHardware');
    el.hidden = false;
  } else {
    el.textContent = '';
    el.hidden = true;
  }
}

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
    cards += cardHTML('stock', b.host, label,
      boxIdentLine(b, t('setup.targetCardKindStock')),
      t('setup.targetCardBadgeStock'), 'badge-warn');
    // Soundbars / home-cinema systems / adapters ('limited') install fine but
    // have no hardware preset buttons 1-6; say so honestly under the card.
    if (boxModelSupport(b.model) === 'limited') {
      cards += `<div class="setup-target-limited-note muted small">${escapeHtml(t('setup.limitedNote'))}</div>`;
    }
    // ST30 USB power warning, shown as soon as an ST30 is detected (not gated on
    // selection) so the user picks a low-power stick before preparing one.
    if (targetIsST30(b)) {
      cards += `<div class="setup-target-st30-warn">${escapeHtml(t('setup.st30StickPowerWarn'))}</div>`;
    }
    // Unverified-hardware slot (#283): filled async once the firmware Variant is
    // known. Does not block install; only tells the user STR is untested here.
    cards += unverifiedWarnSlot(b.host);
  }
  for (const b of strBoxes) {
    const label = b.friendlyName || b.name || b.host;
    cards += cardHTML('str', b.host, label,
      boxIdentLine(b, t('setup.targetCardKindSTR')),
      t('setup.targetCardBadgeSTR'), 'badge-ok');
    // The same ST30 USB power caveat applies to an update stick (the box reads
    // it on boot), so warn here too.
    if (targetIsST30(b)) {
      cards += `<div class="setup-target-st30-warn">${escapeHtml(t('setup.st30StickPowerWarn'))}</div>`;
    }
    cards += unverifiedWarnSlot(b.host);
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

  // Paint the OTA-first primary action for the current target: a network-install
  // hero for a reachable stock box, or the collapsed stick wizard otherwise.
  renderPrimaryAction();

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
      }
      renderSetupTargetPicker();
      updateSetupGoButtonLabel();
    };
  });

  // Async fill of every discovered box's unverified-hardware slot (#283). Runs
  // off the synchronous render so an unreachable box never stalls the picker; the
  // per-host variant is cached, so this does not re-probe the box on each refresh.
  for (const b of [...stockBoxes, ...strBoxes]) {
    paintUnverifiedWarning(b.host).catch(() => {});
  }

  // Async fill of the Setup-AP push panel (only present when
  // factory-reset is the current selection). Decoupled from the
  // picker render so a slow probe does not stall the page.
  if (sel && sel.kind === 'factory-reset') {
    renderSetupAPPushPanel().catch(err => {
      console.warn('setup-ap push panel render failed', err);
    });
  }
}

// renderSetupAPPushPanel populates the in-card panel that drives
// the direct WLAN-credentials push to a Bose setup-AP. Probes once
// for a reachable box at 192.168.1.1; on hit, shows the push form
// pre-filled from the saved Wi-Fi profile of whatever home network
// the user picked. On miss, shows the "switch your PC to the Bose
// SoundTouch Wi-Fi" guidance plus a retry button.
async function renderSetupAPPushPanel() {
  const panel = $('setupAPPushPanel');
  if (!panel) return;

  panel.innerHTML = `<div class="setup-ap-push-loading muted small">${escapeHtml(t('setupAPPush.probing'))}</div>`;
  let probe;
  try {
    probe = await ProbeSetupAP();
  } catch {
    probe = null;
  }
  // Wails returns multi-value as an array on the JS side; guard for
  // shape so we don't crash if the binding shape ever changes.
  const found = Array.isArray(probe) ? probe[1] : probe && probe.found;
  const box = Array.isArray(probe) ? probe[0] : probe && probe.box;

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

// networkInstallRunning guards against a second network install being started
// (a double-click on the hero button, or a hero re-render mid-install): it stays
// true while startNetworkInstall runs, disabling the button.
let networkInstallRunning = false;

// renderPrimaryAction paints the OTA-first primary action above the (collapsed)
// USB-stick wizard. For a reachable stock box it shows the network-install hero
// whose button installs STR over the network with no stick; for str /
// factory-reset / no target it clears the hero and opens the stick <details> so
// the wizard is the primary surface (str stick-update / brand-new-speaker
// territory). Called at the end of renderSetupTargetPicker, so it repaints on
// every target change.
function renderPrimaryAction() {
  const host = $('setupPrimaryAction');
  const details = $('setupStickDetails');
  if (!host) return;
  const sel = state.setupTarget;
  if (sel && sel.kind === 'stock' && sel.box) {
    const b = sel.box;
    // Do NOT rebuild the card if it already shows THIS box. renderPrimaryAction
    // runs on every discovery refresh (every few seconds); rebuilding would wipe
    // the user's typed/selected Wi-Fi, name and language mid-interaction and could
    // eat the install click (the button element gets replaced under the pointer).
    // Only (re)build when the target box actually changes.
    const existingHero = host.querySelector('.setup-hero');
    if (existingHero && existingHero.getAttribute('data-host') === b.host) {
      if (details) details.open = false;
      return;
    }
    const model = b.model || 'SoundTouch';
    const curName = b.friendlyName || b.name || '';
    host.innerHTML =
      `<div class="setup-hero" data-host="${escapeAttr(b.host)}">` +
        `<p class="setup-hero-body">${escapeHtml(t('setup.cardPitch', { model }))}</p>` +
        `<div class="setup-hero-field">` +
          `<label class="setup-hero-wifi-label" for="netBoxName">${escapeHtml(t('setup.cardNameLabel'))}</label>` +
          `<input type="text" id="netBoxName" autocomplete="off" value="${escapeAttr(curName)}" placeholder="${escapeAttr(t('setup.namePlaceholder'))}" />` +
        `</div>` +
        `<div class="setup-hero-field">` +
          `<label class="setup-hero-wifi-label" for="netBoxLang">${escapeHtml(t('setup.langLabel'))}</label>` +
          `<select id="netBoxLang"></select>` +
        `</div>` +
        `<div class="setup-hero-field">` +
          `<label class="setup-hero-wifi-label" for="netBoxTz">${escapeHtml(t('setup.tzLabel'))}</label>` +
          `<select id="netBoxTz"></select>` +
          `<div class="muted small">${escapeHtml(t('setup.tzHint'))}</div>` +
        `</div>` +
        `<div class="setup-hero-wifi">` +
          `<label class="setup-hero-wifi-label" for="netWlanSelect">${escapeHtml(t('setup.wifiFieldLabel'))}</label>` +
          `<div class="muted small">${escapeHtml(t('setup.wifiFieldHint'))}</div>` +
          `<div class="wlan-row">` +
            `<select id="netWlanSelect"><option value="">${escapeHtml(t('settingsView.wlanPickPlaceholder'))}</option></select>` +
            `<button type="button" class="btn btn-icon-sm" id="netWlanRefresh" title="${escapeAttr(t('setup.wlanRefreshTitle'))}">&#x21bb;</button>` +
          `</div>` +
          `<input type="text" id="netWlanSsid" autocomplete="off" placeholder="${escapeAttr(t('settingsView.wlanSsidPlaceholder'))}" />` +
          `<label style="display:block;margin:4px 0 0" title="${escapeAttr(t('settingsView.wlanHiddenHint'))}"><input type="checkbox" id="netWlanHidden" /> ${escapeHtml(t('settingsView.wlanHiddenToggle'))}</label>` +
          `<div class="wlan-row">` +
            `<input type="password" id="netWlanPass" placeholder="${escapeAttr(t('setup.wlanPassPlaceholder'))}" />` +
            `<button type="button" class="btn btn-icon-sm" id="netWlanShowPass" title="${escapeAttr(t('settingsView.wlanShowPass'))}">&#128065;</button>` +
          `</div>` +
        `</div>` +
        `<button class="btn btn-primary" id="setupHeroInstall"${networkInstallRunning ? ' disabled' : ''}>${escapeHtml(t('setup.netInstallBtn'))}</button>` +
      `</div>`;
    const btn = $('setupHeroInstall');
    if (btn) btn.onclick = () => startNetworkInstall(b);
    const showPass = $('netWlanShowPass');
    if (showPass) showPass.onclick = () => {
      const inp = $('netWlanPass');
      if (!inp) return;
      if (inp.type === 'password') { inp.type = 'text'; showPass.innerHTML = '&#128064;'; }
      else { inp.type = 'password'; showPass.innerHTML = '&#128065;'; }
    };
    const wsel = $('netWlanSelect');
    if (wsel) wsel.onchange = onCardWifiSelect;
    const wref = $('netWlanRefresh');
    if (wref) wref.onclick = () => loadCardWifi();
    loadCardWifi();
    const langSel = $('netBoxLang');
    if (langSel) {
      langSel.innerHTML = langOptionsHtml();
      SuggestBoxLanguage(getLocale(), '').then(id => { if (id && langSel) langSel.value = String(id); }).catch(() => {});
    }
    // Timezone dropdown: default to THIS computer's IANA zone (no regional bias).
    const tzSel = $('netBoxTz');
    if (tzSel) {
      const dz = detectTimeZone();
      tzSel.innerHTML = tzOptionsHtml(dz);
      if (dz) tzSel.value = dz;
    }
    if (details) details.open = false;
  } else {
    host.innerHTML = '';
    if (details) details.open = true;
  }
}

// prefillCardWifi fills the OTA card's Wi-Fi inputs from this PC's current Wi-Fi
// (SSID always; password too, except on macOS where reading the Keychain pops an
// admin prompt, #88). Best-effort: a binding error just leaves the fields blank.
async function loadCardWifi() {
  const sel = $('netWlanSelect');
  if (!sel) return;
  try {
    const profiles = await ListWiFiProfiles() || [];
    sel.innerHTML = `<option value="">${escapeHtml(t('settingsView.wlanPickPlaceholder'))}</option>` +
      profiles.map(p => `<option value="${escapeAttr(p.ssid)}">${escapeHtml(p.ssid)}</option>`).join('');
    try {
      const current = await CurrentWiFi();
      if (current && profiles.some(p => p.ssid === current)) {
        sel.value = current;
        await onCardWifiSelect();
      }
    } catch {}
  } catch {
    sel.innerHTML = `<option value="">${escapeHtml(t('setup.wlanListUnavailable'))}</option>`;
  }
}

// onCardWifiSelect copies the picked profile's SSID into the card's SSID field
// and auto-fills the password from the OS credential store (netsh/nmcli;
// skipped on macOS where reading the Keychain pops an admin prompt, #88).
async function onCardWifiSelect() {
  const sel = $('netWlanSelect');
  const v = sel ? sel.value : '';
  const ssidEl = $('netWlanSsid');
  if (ssidEl) ssidEl.value = v;
  if (!v || isMacOS) return;
  try {
    const pw = await TryWiFiPassword(v);
    const passEl = $('netWlanPass');
    if (pw && passEl) passEl.value = pw;
  } catch {}
}

// startNetworkInstall installs STR over the network on an already-reachable box:
// it pins the target and hands the known box straight to waitForBoxAfterSetup,
// whose knownBox path skips the 5-minute discovery wait and runs the same
// hardened install + progress + SSH-repair + play-how flow as the stick path,
// rendering into the shared #setupResult panel. No stick, no user reboot.
async function startNetworkInstall(box) {
  if (!box || networkInstallRunning) return;
  networkInstallRunning = true;
  const heroBtn = $('setupHeroInstall');
  if (heroBtn) heroBtn.disabled = true;
  state.setupTarget = { kind: 'stock', box };
  const name = box.friendlyName || box.name || box.host;
  const nameForBox = ($('netBoxName') && $('netBoxName').value.trim()) || '';
  const langForBox = parseInt(($('netBoxLang') && $('netBoxLang').value) || '0', 10) || 0;
  // Timezone to push after install. Empty => skip the clock write. The 12/24h
  // format is derived from this computer's locale so no extra control is needed.
  const tzForBox = ($('netBoxTz') && $('netBoxTz').value) || '';
  const format24ForBox = detectClockFormat24();
  // Wi-Fi to write onto the box after install so it stays reachable once the
  // Ethernet cable is pulled. Empty SSID => skip the write (box stays wired). A
  // non-empty SSID needs an open net (empty pass) or a >=8 char WPA passphrase
  // (the agent's handleBoxWLAN guard); a bad password does NOT block the install,
  // it just skips the Wi-Fi write, so the user is never stuck on a typo.
  const ssid = ($('netWlanSsid') && $('netWlanSsid').value.trim()) || '';
  const pass = ($('netWlanPass') && $('netWlanPass').value) || '';
  // Hidden networks never show up in the speaker's site survey, so the flag
  // is passed through to the agent, which then skips its visibility preflight
  // and provisions with scan_ssid=1.
  const hidden = !!($('netWlanHidden') && $('netWlanHidden').checked);
  const wifiForBox = (ssid && (pass === '' || pass.length >= 8)) ? { ssid, pass, hidden } : null;
  const lead = `<div class="setup-ok">${escapeHtml(t('setup.netInstallOn', { name }))}</div>`;
  try {
    await waitForBoxAfterSetup({ ssid: '', pass: '', html: lead, knownBox: box, wifiForBox, nameForBox, langForBox, tzForBox, format24ForBox });
  } finally {
    networkInstallRunning = false;
    const b2 = $('setupHeroInstall');
    if (b2) b2.disabled = false;
  }
}

function togglePasswordVisibility() {
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

async function onWifiSelect() {
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
  mountSetupShell(); // entry from switchView too; ensure the shell exists
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
  const notWritable = chk && chk.reason === 'not-writable';
  if (tooSmall || notWritable) {
    const gb = (chk.totalBytes / 1e9).toFixed(1);
    upd.innerHTML = `<b>${escapeHtml(
      tooSmall ? t('setup.stickTooSmall', { gb }) : t('setup.stickNotWritable')
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

async function doSetup() {
  const drive = state.drives[state.selectedDrive];
  if (!drive) return;
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
    if (chk && chk.reason === 'not-writable') {
      $('setupResult').innerHTML = `<div class="setup-err">${escapeHtml(t('setup.stickNotWritable'))}</div>`;
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
    if (ssid) {
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

// showAwaitBoxReadyPanel renders the "do this on the speaker now" instructions
// plus a confirm button that stays DISABLED while a background watcher works out
// what state the speaker is in. The button only unlocks once the speaker is
// found AND reachable for install; until then the watcher reports clearly what
// it sees, including the common "speaker booted without the stick" case, so
// users stop dropping out at this fragile step.
function showAwaitBoxReadyPanel({ ssid, pass, html }) {
  const setupResult = $('setupResult');
  if (!setupResult) return;
  setupResult.innerHTML = html +
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
  watchForSpeakerReady({ ssid, pass, html });
}

// watchForSpeakerReady is the idiot-proof background watcher for the final setup
// step. It polls every ~3s and classifies the speaker into one of several plain
// states so a non-technical user always knows what to do and the install button
// only unlocks when the speaker is genuinely ready. Key cases it tells apart:
//   - still searching / stalled (speaker not on the network yet),
//   - found but just booting (grace window, do not blame the stick yet),
//   - booted WITHOUT the stick (on the network, SSH off, firmware current): the
//     most common drop-out; reseat + power-cycle and it auto-recovers,
//   - firmware too old (on the network, SSH off, firmware outdated/unparsable):
//     update via the Bose app first; a "try anyway" escape never hard-blocks,
//   - ready (SSH open) / already STR / a factory-fresh setup network,
//   - wrong/multiple speakers (a pinned target that is not the one online).
// A separate 1s ticker keeps the countdown live between polls.
async function watchForSpeakerReady({ ssid, pass, html }) {
  const statusEl = $('setupAwaitStatus');
  const btn = $('setupSpeakerReady');
  if (!statusEl || !btn) return;

  const wantedHost = (state.setupTarget && state.setupTarget.box) ? state.setupTarget.box.host : '';
  const watcherStart = Date.now();
  const deadline = watcherStart + 6 * 60 * 1000;
  const GRACE_MS = 90 * 1000;
  const fwCache = {}; // host -> { at, info }; firmware does not change mid-watch
  let firstSeenOnNetwork = 0;
  let lastSeenState = '';
  let ready = false;
  let aborted = false; // a link (try-anyway / choose-different) took over

  const setStatus = (cls, txt, extraHtml) => {
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
  const arm = (label, onclick) => { liveSearchKey = null; btn.textContent = label; btn.disabled = false; btn.onclick = onclick; };
  const handoff = () => { btn.disabled = true; aborted = true; stopTicker(); waitForBoxAfterSetup({ ssid, pass, html }); };

  while (Date.now() < deadline && !ready && !aborted) {
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

    // Already runs STR.
    if (cand && cand.kind === 'str') {
      lastSeenState = 'str';
      setStatus('setup-ok', t('setup.awaitAlreadyStr'));
      arm(t('setup.awaitContinueBtn'), handoff);
      ready = true; break;
    }

    // On the home network (stock).
    if (cand && cand.host) {
      const f = await getFw(cand.host);
      const model = (f && f.model) || cand.model || 'SoundTouch';
      const fw = (f && f.short) || '';
      if (f) { if (!firstSeenOnNetwork) firstSeenOnNetwork = Date.now(); }
      else { firstSeenOnNetwork = 0; } // discovery listed it but :8090 blipped mid-reboot
      let sshOk = false;
      try { sshOk = await BoxInstallReachable(cand.host); } catch {}
      if (sshOk) {
        lastSeenState = 'ready';
        setStatus('setup-ok', t('setup.awaitFoundReady', { model }));
        arm(t('setup.awaitConfirmBtn'), handoff);
        ready = true; break;
      }
      if (!firstSeenOnNetwork || (Date.now() - firstSeenOnNetwork) < GRACE_MS) {
        liveSearchKey = null; lastSeenState = 'booting';
        setStatus('muted small', t('setup.awaitStillBooting'));
        await sleep(3000); continue;
      }
      // Grace elapsed, SSH still off: firmware too old vs booted without the stick.
      liveSearchKey = null;
      if (f && (f.outdated || !f.short)) {
        lastSeenState = 'firmware';
        setStatus('setup-warn', t('setup.awaitFirmwareTooOld', { model, fw: fw || '?' }),
          `<div><a href="#" id="setupTryAnyway">${escapeHtml(t('setup.awaitFirmwareTryAnyway'))}</a></div>`);
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
      if (ap && ap.host) {
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

  if (ready || aborted) { stopTicker(); return; }

  // Timeout: context-aware recovery based on whether we ever saw it on the network.
  stopTicker();
  const wasOnNetwork = lastSeenState === 'no-stick' || lastSeenState === 'firmware' || lastSeenState === 'booting';
  setStatus('setup-warn', t(wasOnNetwork ? 'setup.awaitTimeoutWasOnNetwork' : 'setup.awaitTimeoutNeverSeen'));
  btn.textContent = t('setup.awaitSearchAgain');
  btn.disabled = false;
  btn.onclick = () => { showAwaitBoxReadyPanel({ ssid, pass, html }); };
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
  'not-reachable': ['netOnNetwork', 'netWifi', 'netCable', 'netRetry'],
  'install-window-closed': ['netRetry'],
  'control-unresponsive': ['netRetry', 'netLogs'],
  'stick-copy-failed': ['netRetry', 'netLogs'],
};
const NET_HELP_DEFAULT = ['netOnNetwork', 'netWifi', 'netCable', 'netRetry', 'netLogs'];

// installHelpHtml renders the localized help checklist for a failure code.
// isNetwork picks the OTA-appropriate step list (no USB-stick advice).
function installHelpHtml(code, isNetwork) {
  const steps = isNetwork
    ? (INSTALL_HELP_STEPS_NET[code] || NET_HELP_DEFAULT)
    : (INSTALL_HELP_STEPS[code] || ['freshBoot', 'wifi', 'stick', 'logs']);
  const items = steps.map(s => `<li>${escapeHtml(t('setup.help.' + s))}</li>`).join('');
  return `<div class="setup-help"><b>${escapeHtml(t('setup.helpTitle'))}</b><ul>${items}</ul>`
    + `<p class="small">${escapeHtml(t('setup.helpLogsInstruction'))}</p>`
    + `<button class="btn btn-mini" id="installSaveLogs">${escapeHtml(t('footer.saveLogs'))}</button></div>`;
}

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
async function waitForBoxAfterSetup({ ssid, pass, html, knownBox, wifiForBox, nameForBox, langForBox, tzForBox, format24ForBox }) {
  const baseHtml = html;
  const setupResult = $('setupResult');
  if (!setupResult) return;
  const render = (extra) => { setupResult.innerHTML = baseHtml + extra; };

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
  let curPhase = 0, copyDetail = '', accessHint = '';
  const installStartMs = Date.now();
  const fmtMS = (ms) => { const s = Math.max(0, Math.round(ms / 1000)); return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0'); };
  const chkRow = (label, st, detail) => {
    const dot = st === 'done' ? '<span class="chk-ico chk-done">&#10003;</span>'
      : st === 'run' ? '<span class="chk-ico chk-spin"></span>'
      : '<span class="chk-ico chk-pend"></span>';
    return `<div class="chk-row chk-${st}">${dot}<span class="chk-lbl">${escapeHtml(label)}</span></div>` +
      (detail ? `<div class="chk-detail muted small">${detail}</div>` : '');
  };
  const renderChecklist = () => {
    let rows = '';
    PHASES.forEach((ph, i) => {
      const st = i < curPhase ? 'done' : i === curPhase ? 'run' : 'pend';
      const detail = (ph === 'copy' && st === 'run') ? copyDetail : (ph === 'access' && st === 'run') ? accessHint : '';
      rows += chkRow(phaseLabels[ph], st, detail);
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
        } else if (p.margeHits > 0) {
          accessHint = '';
        }
      }
    }
    renderChecklist();
  });
  let result;
  try {
    result = await InstallSTROnBox(foundBox.host, foundBox.model || foundBox.type || '');
  } catch (err) {
    clearInterval(timerHandle);
    if (offProgress) offProgress();
    render(`<div class="setup-err">${escapeHtml(t('setup.installFailed', { msg: String(err) }))}</div>`);
    return;
  }
  clearInterval(timerHandle);
  if (offProgress) offProgress();
  if (!result || !result.ok) {
    const msg = (result && result.message) || 'unknown';
    const help = installHelpHtml(result && result.code, !!knownBox);
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
    render(`<div class="setup-err">${escapeHtml(t('setup.installFailed', { msg }))}</div>` + help + repairBtn + powerCycleHint + log);
    // If the network path genuinely cannot proceed (no install window, box not
    // reachable, controls wedged), reveal the USB-stick fallback (relocated into
    // <details id="setupStickDetails">) so the user has an immediate next step.
    if (result && ['install-window-closed', 'not-reachable', 'control-unresponsive'].indexOf(result.code) >= 0) {
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
              + powerCycleAdviceHtml(foundBox));
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
          render(`<div class="setup-err">${escapeHtml(t('setup.repairSSHFailed', { msg: String(e) }))}</div>`);
        }
      };
    }
    const dlBtn = $('installSaveLogs');
    if (dlBtn) {
      dlBtn.onclick = async () => {
        dlBtn.classList.add('working');
        try {
          // Pass the box we just tried to install on first, plus any others, so
          // the bundle pulls its box-side setup.log / boot.log / agent.log /
          // dmesg over SSH (SSH is still open right after a failed install).
          // README + app.log are always written, so a file is produced even
          // with no stick in the PC and no reachable box.
          const hosts = [foundBox && foundBox.host, ...((state.boxes || []).map(b => b && b.host))].filter(Boolean);
          const r = await SaveDiagnosticBundle([...new Set(hosts)], true);
          if (r && r.savePath) showToast(t('footer.saveLogsDone', { path: r.savePath, size: Math.round((r.bytes || 0) / 1024) }));
        } catch (e) { showError(String(e)); }
        finally { dlBtn.classList.remove('working'); }
      };
    }
    return;
  }
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
  let unplugLine = '';
  let provisionFailed = false;
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
          const wr = await deps.boxFetch(agentBox, '/api/box/wlan', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ ssid: wifiForBox.ssid, password: wifiForBox.pass, hidden: !!wifiForBox.hidden }),
          });
          if (!wr || !wr.ok) return false;
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
      try { const r = await deps.boxFetch(agentBox, '/api/status', {}); if (r && r.ok) break; } catch {}
      await sleep(3000);
    }
    waitingAgent = false;
    renderProvision();

    // Apply each step in order with a short retry, reflecting the result live.
    for (const s of steps) {
      st[s.id] = 'run'; renderProvision();
      const res = await retryStep(s.run, 3, 2500);
      st[s.id] = res.ok ? 'done' : 'fail';
      if (!res.ok) provisionFailed = true;
      if (s.id === 'wifi') {
        if (res.ok) {
          const info = res.value || {};
          unplugLine = (info.mechanism === 'bco') ? t('setup.unplugSafeBco') : t('setup.unplugSafeWpa');
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
  render(`<div class="setup-ok">${escapeHtml(t('setup.installDone'))}</div>` +
         (unplugLine ? `<div class="setup-unplug">${escapeHtml(unplugLine)}</div>` : '') +
         (provisionFailed ? `<div class="setup-warn">${escapeHtml(t('setup.provisionSomeFailed'))}</div>` : '') +
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
         powerCycleAdviceHtml(foundBox));
  const goMusic = $('installGoMusic');
  if (goMusic) goMusic.onclick = () => deps.switchView('box');
  deps.discoverBoxes();
  // The box is alive again: invite the user to drop a pin on the community world
  // map. The most reliable moment to ask, and the one most users reach.
  try { deps.celebrateProvision(foundBox); } catch {}
}
