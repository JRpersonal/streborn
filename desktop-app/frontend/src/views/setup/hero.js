// views/setup/hero.js: the primary action above the stick wizard. For a stock
// speaker on the network that is the network-install card with its name,
// language, timezone and Wi-Fi fields and the Install button; for a speaker
// that already runs STR it is the way on to its settings. Also what keeps that
// button out of reach: the running-install latch and the update hold.
// Extracted from views/setup.js.

import { state } from '../../state.js';
import { $, escapeHtml, escapeAttr } from '../../utils.js';
import { t, getLocale } from '../../i18n/index.js';
import { langOptionsHtml } from '../settings.js';
import { ListWiFiProfiles, TryWiFiPassword, CurrentWiFi, SuggestBoxLanguage } from '../../api.js';
import { heldInstallNote } from '../../installhold.js';
import { deps } from './deps.js';
import { isMacOS, currentHeroHold } from './shared.js';
import { askAboutStereoPair } from './stereopair.js';
import { installWaitPanelLive, waitForBoxAfterSetup } from './install.js';

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

// syncHeroInstallHold shows or hides the "an update is still finishing" note
// on the network-install card and disables its button while the hold lasts.
// It updates the card in place: rebuilding it would wipe what the owner typed.
export function syncHeroInstallHold() {
  const hold = currentHeroHold();
  const note = $('setupHeroHold');
  if (note) {
    note.textContent = heldInstallNote(hold);
    note.hidden = !hold;
  }
  const btn = $('setupHeroInstall');
  if (btn) btn.disabled = networkInstallRunning || installWaitPanelLive || !!hold;
}

// networkInstallRunning guards against a second network install being started
// (a double-click on the hero button, or a hero re-render mid-install): it stays
// true while startNetworkInstall runs, disabling the button.
let networkInstallRunning = false;

// installRunActive tells main.js's periodic background refresh whether an
// install is running, so the refresh never probes a speaker mid-install.
export function installRunActive() { return networkInstallRunning || installWaitPanelLive; }

// renderPrimaryAction paints the OTA-first primary action above the (collapsed)
// USB-stick wizard. For a reachable stock box it shows the network-install hero
// whose button installs STR over the network with no stick; for a box that
// already runs STR it says so and offers the way on to that speaker's settings;
// for factory-reset / no target it clears the area. Called at the end of
// renderSetupTargetPicker, so it repaints on every target change.
//
// The stick <details> is never forced open here. It used to be opened for every
// non-stock target, which meant a first-time owner whose speaker was found on
// the network was shown the whole stick wizard before anything else, even
// though the stick is only the fallback path. It now stays where the user left
// it: closed until they open the "prepare a USB stick instead" summary.
export function renderPrimaryAction() {
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
    // Only (re)build when the target box actually changes. data-kind is part of
    // the check because the STR card below reuses the same .setup-hero styling,
    // and a speaker that was factory-reset comes back as a stock box under the
    // host its STR card still carries.
    const existingHero = host.querySelector('.setup-hero');
    if (existingHero
        && existingHero.getAttribute('data-kind') === 'stock'
        && existingHero.getAttribute('data-host') === b.host) {
      // Nothing to repaint, and deliberately no touch of the stick <details>:
      // this runs every few seconds, so collapsing it here would slam shut the
      // fallback that a failed install had just opened for the user. Only the
      // update hold may have changed, and that is updated in place.
      syncHeroInstallHold();
      return;
    }
    const model = b.model || 'SoundTouch';
    const curName = b.friendlyName || b.name || '';
    host.innerHTML =
      `<div class="setup-hero" data-kind="stock" data-host="${escapeAttr(b.host)}">` +
        // Up front, not after the click: while an update from this app is still
        // finishing on the speaker, the install is refused anyway.
        `<div class="setup-warn setup-hero-hold" id="setupHeroHold" role="status" hidden></div>` +
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
    syncHeroInstallHold();
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
  } else if (sel && sel.kind === 'str' && sel.box) {
    // The speaker already runs STR, so there is nothing to install. Without this
    // branch the whole area went blank and the only thing left on screen was the
    // stick wizard, which reads as "prepare a stick" to somebody who is in fact
    // already done. Say plainly that it is installed and hand them on to that
    // speaker's settings, where the name, Wi-Fi and language live.
    const b = sel.box;
    const existingHero = host.querySelector('.setup-hero');
    if (existingHero
        && existingHero.getAttribute('data-kind') === 'str'
        && existingHero.getAttribute('data-host') === b.host) {
      return;
    }
    host.innerHTML =
      `<div class="setup-hero" data-kind="str" data-host="${escapeAttr(b.host)}">` +
        `<h3>${escapeHtml(t('setup.targetCardKindSTR'))}</h3>` +
        `<p class="setup-hero-body">${escapeHtml(t('setup.awaitAlreadyStr'))}</p>` +
        `<button class="btn btn-primary" id="setupStrSettings">${escapeHtml(t('nav.speakerSettings'))}</button>` +
      `</div>`;
    const settingsBtn = $('setupStrSettings');
    if (settingsBtn) settingsBtn.onclick = () => {
      // Focus the speaker before switching: the Settings tab renders whatever
      // state.settingsBox holds, so without this it could open on a different
      // speaker than the one the card names.
      deps.speakerPicked(b);
      deps.switchView('settings');
    };
  } else {
    host.innerHTML = '';
    // Nothing to act on yet. If no speaker was found at all, the stick IS the
    // route: a speaker that is new or factory reset does not appear on the
    // network until it has been set up, so opening the stick section here is
    // guidance rather than clutter. Once a speaker HAS been found, the section
    // stays closed, because then the stick is the fallback and not the path.
    const foundAny = (state.boxes || []).some(b => b && b.host);
    if (details) details.open = !foundAny;
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
  // Ethernet cable is pulled. Empty SSID => skip the write (box stays wired).
  // An SSID with an EMPTY password used to be passed on here as an open
  // network and applied to the speaker with force:true; against a protected
  // home Wi-Fi that throws the speaker off the network right after the
  // successful OTA install (field case 2026-08-29: SSID prefilled, reading
  // the password failed silently). Without a password NOTHING is written,
  // and the note below says so plainly; a password that is too short still
  // skips the step instead of blocking the install.
  const ssid = ($('netWlanSsid') && $('netWlanSsid').value.trim()) || '';
  const pass = ($('netWlanPass') && $('netWlanPass').value) || '';
  // Hidden networks never show up in the speaker's site survey, so the flag
  // is passed through to the agent, which then skips its visibility preflight
  // and provisions with scan_ssid=1.
  const hidden = !!($('netWlanHidden') && $('netWlanHidden').checked);
  const wifiForBox = (ssid && pass.length >= 8) ? { ssid, pass, hidden } : null;
  let lead = `<div class="setup-ok">${escapeHtml(t('setup.netInstallOn', { name }))}</div>`;
  if (ssid && !pass) {
    lead += `<div class="setup-warn">${escapeHtml(t('setup.wlanNoPass'))}</div>`;
  }
  try {
    // Half of a stereo pair from the Bose-app days: ask before touching it, so
    // the install neither breaks the pair nor leaves the other half waiting for
    // a partner that has let go (see stereoinstall.js). A cancel returns through
    // the finally below, which hands the Install button back.
    if (await askAboutStereoPair(box) === 'cancel') return;
    await waitForBoxAfterSetup({ ssid: '', pass: '', html: lead, knownBox: box, wifiForBox, nameForBox, langForBox, tzForBox, format24ForBox });
  } finally {
    networkInstallRunning = false;
    // Not while the wait panel is up: the await above returns when that panel
    // is drawn, not when it resolves, so re-enabling here would hand the button
    // back under the one screen that exists to keep it out of reach.
    const b2 = $('setupHeroInstall');
    if (b2 && !installWaitPanelLive) b2.disabled = !!currentHeroHold();
  }
}
