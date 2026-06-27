// views/settings.js — the "Speaker settings" view.
//
// Extracted from the main.js monolith, same pattern as views/recent.js and
// views/multiroom.js: the module pulls shared things (state, utils, i18n, api,
// localization) from their own modules and receives the few main.js-local
// helpers it needs (switchView, discoverBoxes, boxFetch, ...) via
// initSettingsView, so it never imports back into main.js (which would create a
// cycle). New views should follow this pattern so main.js stops growing.
//
// Entry point: loadBoxSettings() is called from main.js's switchView when the
// settings tab is opened. The top-level setup below (the #view-settings shell
// and the box-switcher wiring) runs once when this module is first imported,
// exactly as it did when it lived at module scope in main.js.

import { state, saveSearchCountry } from '../state.js';
import {
  $,
  escapeHtml,
  escapeAttr,
  sleep,
  debounce,
  confirmWarn,
  showError,
  showToast,
  compareVerBuild,
} from '../utils.js';
import { t, tLookup } from '../i18n/index.js';
import { COUNTRIES, optFlag } from '../localization.js';
import {
  BoxSettings,
  BoxAgentVersion,
  PhoneQR,
  RebootBox,
  SetPreset,
  SyncBoxPresets,
  RestoreBoxSnapshot,
  SaveDiagnosticBundle,
  BrowserOpenURL,
  TrueFactoryReset,
  UninstallSTR,
  GetBoxLanguage,
  SetBoxLanguage,
  GetClockFormat24,
  GetClockDisplay,
  SetClockDisplay,
  GetResumeOnPowerOn,
  SetResumeOnPowerOn,
  GetDisplayTrack,
  SetDisplayTrack,
  AnnounceExample,
  SendAnnounce,
  Translate,
  GetAirplayOpt,
  SetAirplayOpt,
  GetWebhooks,
  SaveWebhookConfig,
  TestWebhook,
  TestWebhookAction,
  CopyPresetsAcrossBoxes,
  SetBoxName,
  SetBoxVolume,
  SetBoxBass,
  ListWiFiProfiles,
  TryWiFiPassword,
} from '../api.js';

// isMacOS is re-derived locally (the same pure check main.js uses) so the WLAN
// switch UI can skip the System-keychain admin prompt on macOS (#88) without
// expanding the injected-helper surface.
const isMacOS = /Mac OS X|Macintosh/.test(navigator.userAgent);

// Injected main.js helpers (see initSettingsView). These stay in main.js because
// they are shared across views; the settings code calls them as deps.<name>.
let deps = {
  switchView: () => {},
  updateFilterIndicators: () => {},
  discoverBoxes: async () => {},
  renderBoxSelect: () => {},
  boxFetch: async () => ({ ok: false }),
  localizeLanguageName: (n) => n,
  doBoxUpdate: async () => {},
  loadPresets: async () => {},
  getRoomNames: () => [],
};
export function initSettingsView(d) {
  deps = { ...deps, ...d };
}

// mountSettingsShell builds the settings view's static shell (box switcher + body
// container) and wires the box-switcher controls. Run LAZILY on first open, NOT
// at module import: this module is imported at the top of main.js, before the
// #view-settings container is created further down, so touching the DOM at import
// time threw and blanked the whole app. loadBoxSettings calls this once first.
let settingsShellMounted = false;
function mountSettingsShell() {
  if (settingsShellMounted) return;
  const root = $('view-settings');
  if (!root) return;
  settingsShellMounted = true;
  root.innerHTML = `
    <h2>${escapeHtml(t('settingsView.title'))}</h2>
    <div class="settings-box-switcher">
      <span class="muted small">${escapeHtml(t('settingsView.forSpeaker'))}</span>
      <select id="settingsBoxSelect"></select>
      <button class="btn-icon" id="settingsRefreshBtn" title="${escapeAttr(t('settingsView.refreshListTitle'))}"><span class="refresh-icon">&#x21bb;</span></button>
    </div>
    <div id="settingsBody">
      <div class="muted">${escapeHtml(t('settingsView.selectFirst'))}</div>
    </div>
  `;
  $('settingsBoxSelect').onchange = () => {
    const id = $('settingsBoxSelect').value;
    const box = state.boxes.find(b => b.deviceID === id);
    if (box) {
      state.settingsBox = box;
      loadBoxSettings();
    }
  };
  $('settingsRefreshBtn').onclick = async () => {
    const rb = $('settingsRefreshBtn');
    rb.disabled = true;
    rb.classList.add('spinning'); // visible feedback while refreshing, like the topbar refresh
    try {
      await deps.discoverBoxes();
      renderSettingsBoxSelect();
      loadBoxSettings();
    } finally {
      rb.classList.remove('spinning');
      rb.disabled = false;
    }
  };
}

// uidSuffixFor returns the last 4 characters of the device ID as a
// suffix used to disambiguate friendly names (for example "FFD8").
function uidSuffixFor(box) {
  const id = (box && box.deviceID) || '';
  return id.slice(-4).toUpperCase();
}

// ensureWithUID always appends the speaker's UID suffix so the user
// can see at a glance that an identifier is attached. Prevents name
// collisions on the network and is useful for support too.
function ensureWithUID(desired, ownBox) {
  const trimmed = (desired || '').trim();
  if (!trimmed) return trimmed;
  const suffix = uidSuffixFor(ownBox);
  if (!suffix) return trimmed;
  if (trimmed.toUpperCase().endsWith(suffix)) return trimmed;
  // #133: the ID suffix exists only to disambiguate multiple speakers that
  // would otherwise share a name. The name is display-only (discovery matches
  // on IP/DeviceID), so append the suffix ONLY when another known speaker would
  // collide with this exact name. A single speaker, or a unique name, keeps the
  // clean name the user chose.
  const ownId = (ownBox && ownBox.deviceID) || '';
  const want = trimmed.toUpperCase();
  const collides = (state.boxes || []).some(b => {
    if (!b || b.deviceID === ownId) return false;
    const other = (b.friendlyName || b.name || '').trim();
    if (!other) return false;
    const otherBase = other.replace(/\s+[0-9A-Fa-f]{4}$/, '').trim().toUpperCase();
    return otherBase === want || other.toUpperCase() === want;
  });
  return collides ? `${trimmed} ${suffix}` : trimmed;
}

function renderSettingsBoxSelect() {
  const sel = $('settingsBoxSelect');
  if (!state.boxes.length) {
    sel.innerHTML = `<option value="">${escapeHtml(t('settingsView.noSpeakerFound'))}</option>`;
    return;
  }
  const target = state.settingsBox || state.currentBox || state.boxes[0];
  if (target) state.settingsBox = target;
  sel.innerHTML = state.boxes.map(b => {
    const label = b.friendlyName || b.name || b.host;
    // Append the box model so the dropdown can distinguish two
    // boxes that happen to carry the same Bose-default friendly
    // name. Older agents that only broadcast generic "SoundTouch"
    // get no extra annotation — would just be noise.
    const modelSuffix = b.model && b.model !== 'SoundTouch' ? ` · ${b.model}` : '';
    return `<option value="${escapeAttr(b.deviceID)}">${escapeHtml(label + modelSuffix)} (${escapeHtml(b.host)})</option>`;
  }).join('');
  if (state.settingsBox) sel.value = state.settingsBox.deviceID;
}

// Bose sysLanguage enum, fully resolved 2026-06-01 (see
// project_bose_language_enum memory): id -> { endonym, key }. The
// endonym is the language's own name in its own script, so a speaker
// who cannot read the app's UI language (a Cyrillic/CJK/Greek/Thai
// reader looking at an English UI) still recognises and can pick their
// box language. key is the lowercased English name used to localise a
// secondary label via localizeLanguageName. 0 is the unset/factory
// sentinel (a no-op for the OOB gate) and 14 is undefined, so neither is
// offered as a real choice; 0 is only labelled when a box still reports
// it as its current value.
const BOSE_LANGS = {
  1: { endonym: 'Dansk', key: 'danish' },
  2: { endonym: 'Deutsch', key: 'german' },
  3: { endonym: 'English', key: 'english' },
  4: { endonym: 'Español', key: 'spanish' },
  5: { endonym: 'Français', key: 'french' },
  6: { endonym: 'Italiano', key: 'italian' },
  7: { endonym: 'Nederlands', key: 'dutch' },
  8: { endonym: 'Svenska', key: 'swedish' },
  9: { endonym: '日本語', key: 'japanese' },
  10: { endonym: '简体中文', key: 'chinese' },
  11: { endonym: '繁體中文', key: 'chinese' },
  12: { endonym: '한국어', key: 'korean' },
  13: { endonym: 'ไทย', key: 'thai' },
  15: { endonym: 'Čeština', key: 'czech' },
  16: { endonym: 'Suomi', key: 'finnish' },
  17: { endonym: 'Ελληνικά', key: 'greek' },
  18: { endonym: 'Norsk', key: 'norwegian' },
  19: { endonym: 'Polski', key: 'polish' },
  20: { endonym: 'Português', key: 'portuguese' },
  21: { endonym: 'Română', key: 'romanian' },
  22: { endonym: 'Русский', key: 'russian' },
  23: { endonym: 'Slovenščina', key: 'slovenian' },
  24: { endonym: 'Türkçe', key: 'turkish' },
  25: { endonym: 'Magyar', key: 'hungarian' },
};
const BOSE_LANG_IDS = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25];

// boseLangLabel returns "<endonym> (<localised name>)" for a sysLanguage
// id, e.g. "日本語 (Japanese)". The endonym lets a native speaker pick
// regardless of the app UI language; the parenthetical helps the current
// UI reader. The suffix is dropped when it would just repeat the endonym.
function boseLangLabel(id) {
  const n = Number(id);
  const e = BOSE_LANGS[n];
  if (!e) return n === 0 ? t('settingsView.langNoValue') : t('settingsView.langUnknown');
  const localized = deps.localizeLanguageName(e.key);
  return localized && localized.toLowerCase() !== e.endonym.toLowerCase()
    ? `${e.endonym} (${localized})`
    : e.endonym;
}

export function langOptionsHtml() {
  // Sort by the localised name (a Latin string in the current UI
  // language) so the order is predictable for the majority; the
  // native-script endonym stands out for speakers scanning for theirs.
  return BOSE_LANG_IDS
    .map((id) => ({ id, label: boseLangLabel(id), sort: deps.localizeLanguageName(BOSE_LANGS[id].key) }))
    .sort((a, b) => a.sort.localeCompare(b.sort))
    .map((o) => `<option value="${o.id}">${escapeHtml(o.label)}</option>`)
    .join('');
}

export async function loadBoxSettings() {
  mountSettingsShell(); // build the shell on first open (see note above)
  renderSettingsBoxSelect();
  const body = $('settingsBody');
  if (!state.settingsBox) {
    body.innerHTML = `
      <div class="empty-state">
        <div class="empty-state-title">${escapeHtml(t('settingsView.noSpeakerFoundTitle'))}</div>
        <div class="empty-state-text">
          ${escapeHtml(t('settingsView.noSpeakerFoundHelp'))}
        </div>
        <button class="btn btn-primary btn-mini" id="settingsGoSetup">${escapeHtml(t('speaker.goSetup'))}</button>
      </div>`;
    const go = document.getElementById('settingsGoSetup');
    if (go) go.onclick = () => deps.switchView('setup');
    return;
  }
  // Stock Bose speakers do not run the STR agent, so /api/box/settings
  // (an STR endpoint on port 8888) would hit Bose's RomPager web
  // server and return a 404 with a confusing HTML error. Render a
  // clear "install STR first" panel instead.
  if (state.settingsBox && state.settingsBox.kind === 'stock') {
    body.innerHTML = `
      <div class="empty-state">
        <div class="empty-state-title">${escapeHtml(t('settingsView.stockBoxTitle'))}</div>
        <div class="empty-state-text">
          ${escapeHtml(t('settingsView.stockBoxHelp', { name: state.settingsBox.friendlyName || state.settingsBox.name || state.settingsBox.host }))}
        </div>
        <button class="btn btn-primary btn-mini" id="settingsGoSetup">${escapeHtml(t('speaker.goSetup'))}</button>
      </div>`;
    const go = document.getElementById('settingsGoSetup');
    if (go) go.onclick = () => deps.switchView('setup');
    return;
  }
  // If content is already rendered, do not overwrite it. The user
  // should keep seeing the previous values while we fetch fresh data.
  // Otherwise show a hint.
  const hasContent = body.querySelector('.settings-section');
  if (!hasContent) {
    body.innerHTML = `<div class="muted">${escapeHtml(t('settingsView.loadingData'))}</div>`;
  }

  let lastErr = null;
  // Retry loop: on "connection refused" / "timeout" retry up to two
  // times. A rename briefly restarts the speaker's webserver, which
  // is an expected transient.
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      const s = await BoxSettings(state.settingsBox.host, state.settingsBox.port);
      // The agent returns HTTP 200 with an all-empty payload when the
      // box's Bose app (:8090) did not answer within the read timeout
      // (it is still starting or briefly hung). That is not data; treat
      // it like a transient so the user gets a clear "speaker busy"
      // banner instead of a panel full of blank fields.
      if (isEmptySettings(s)) {
        lastErr = new Error('box_settings_empty');
        if (attempt < 2) { await sleep(1500); continue; }
        break;
      }
      // Erfolg: Reconnect Counter + Timer aufloesen
      if (state.settingsReconnect && state.settingsReconnect.timer) {
        clearTimeout(state.settingsReconnect.timer);
      }
      state.settingsReconnect = null;
      renderBoxSettings(s, state.settingsBox);
      return;
    } catch (e) {
      lastErr = e;
      if (attempt < 2 && isTransientBoxError(e)) {
        await sleep(1500);
        continue;
      }
      break;
    }
  }

  // Persistent reconnect banner instead of recurring toasts. Counts
  // down the remaining attempts, gives up after 10 and then shows a
  // clear instruction (power cycle after a failed OTA update).
  const friendly = friendlySettingsError(lastErr);
  // While an OTA is in flight on THIS box, a failed fetch is the EXPECTED reboot
  // (the box restarts once, or twice on a tight NAND while it activates the
  // Spotify engine), NOT a dead agent. Never escalate to the "agent died, unplug
  // the speaker" panel during an update: it told users to power-cycle a speaker
  // that was only restarting mid-OTA (#197). Show a calm "updating, restarting"
  // banner and keep polling until the box comes back.
  const otaHere = state.otaInProgress && state.otaTargetHost === state.settingsBox.host;
  state.settingsReconnect = state.settingsReconnect || { attempts: 0, max: 10 };
  state.settingsReconnect.attempts++;
  const remaining = state.settingsReconnect.max - state.settingsReconnect.attempts;

  if (otaHere || remaining > 0) {
    const bannerHtml = otaHere
      ? `
      <div class="reconnect-banner">
        <div>
          <b>${escapeHtml(t('update.inProgressTitle', { name: state.settingsBox.name || '' }))}</b>
          <small>${escapeHtml(t('update.rebootNote'))}</small>
        </div>
      </div>`
      : `
      <div class="reconnect-banner">
        <div>
          <b>${escapeHtml(t('settingsView.speakerUnreachable'))}</b>
          <small>${escapeHtml(friendly)}</small>
          <small>${escapeHtml(t('settingsView.retryIn', { remaining }))}</small>
        </div>
      </div>`;
    if (hasContent) {
      // Keep the existing rendering, insert the banner at the top.
      let existing = body.querySelector('.reconnect-banner');
      if (!existing) {
        body.insertAdjacentHTML('afterbegin', bannerHtml);
      } else {
        existing.outerHTML = bannerHtml;
      }
    } else {
      body.innerHTML = bannerHtml;
    }
    if (state.settingsReconnect.timer) clearTimeout(state.settingsReconnect.timer);
    state.settingsReconnect.timer = setTimeout(loadBoxSettings, 4000);
  } else {
    // After 10 failed attempts: give up and show clear instructions.
    state.settingsReconnect = null;
    body.innerHTML = `
      <div class="empty-state">
        <div class="empty-state-title">${escapeHtml(t('settingsView.speakerDeadTitle'))}</div>
        <div class="empty-state-text">
          ${escapeHtml(t('settingsView.speakerDeadHelp1'))}
          <br><br>
          ${t('settingsView.speakerDeadHelp2')}
        </div>
        <div class="empty-state-buttons">
          <button class="btn btn-mini" id="settingsRetry">${escapeHtml(t('common.retry'))}</button>
          <button class="btn btn-primary btn-mini" id="settingsBackToBoxes">${escapeHtml(t('settingsView.backToSpeakerList'))}</button>
        </div>
      </div>`;
    const r = document.getElementById('settingsRetry');
    if (r) r.onclick = () => { state.settingsReconnect = null; loadBoxSettings(); };
    const b2 = document.getElementById('settingsBackToBoxes');
    if (b2) b2.onclick = () => { state.settingsReconnect = null; deps.switchView('box'); };
  }
}

function isTransientBoxError(err) {
  const s = String(err || '').toLowerCase();
  return s.includes('refused') || s.includes('timeout') ||
         s.includes('deadline') || s.includes('reset') ||
         s.includes('eof') || s.includes('no route');
}

function friendlySettingsError(err) {
  const s = String(err || '');
  if (/box_settings_empty/.test(s)) return t('settingsView.errBoseBusy');
  if (/refused/i.test(s)) return t('settingsView.errRefused');
  if (/timeout|deadline/i.test(s)) return t('settingsView.errTimeout');
  if (/no such host|no route/i.test(s)) return t('settingsView.errNoRoute');
  return t('settingsView.errGeneric', { err: s });
}

// isEmptySettings reports whether the agent returned a 200 but the box's
// Bose app (:8090) supplied nothing within the read timeout: no device
// info, no network interfaces, no sources. That means the speaker's Bose
// side is still starting or briefly hung, not that there is real data.
function isEmptySettings(s) {
  if (!s) return true;
  const info = s.info || {};
  const hasInfo = !!(info.deviceID || info.name || info.type);
  const hasNet = !!(s.network && Array.isArray(s.network.interfaces) && s.network.interfaces.length);
  const hasSources = Array.isArray(s.sources) && s.sources.length > 0;
  return !hasInfo && !hasNet && !hasSources;
}


// groupSettingsSections folds the long flat settings list into a few collapsible
// accordions (#declutter): the Status block stays pinned at the top, everything
// else is bucketed into Basics (open) / Sound & display / Network / Status & info
// / Actions / Advanced (collapsed). It MOVES the existing section nodes (ids and
// listeners intact), so no rewiring is needed, and re-runs cleanly on each render
// because the template emits no .settings-group of its own.
function groupSettingsSections() {
  const body = $('settingsBody');
  if (!body || body.querySelector('.settings-group')) return;
  const GROUPS = [
    { key: 'basics', label: t('settingsView.groupBasics'), open: true },
    { key: 'sound', label: t('settingsView.groupSound'), open: false },
    { key: 'network', label: t('settingsView.groupNetwork'), open: false },
    { key: 'info', label: t('settingsView.groupInfo'), open: false },
    { key: 'actions', label: t('settingsView.groupActions'), open: false },
    { key: 'advanced', label: t('settingsView.groupAdvanced'), open: false },
  ];
  // Section -> group by stable id first, then by heading text (the same t() the
  // template rendered, so it stays locale-safe). Expert blocks all go Advanced.
  const byId = {
    resumeOnPowerSection: 'sound',
    displayTrackSection: 'sound',
    airplayOptSection: 'sound',
  };
  const byHeading = {
    [t('settingsView.nameHeading')]: 'basics',
    [t('controls.volume')]: 'basics',
    [t('settingsView.bassHeading')]: 'basics',
    [t('settingsView.clockHeading')]: 'sound',
    [t('settingsView.wlanHeading')]: 'network',
    [t('settingsView.langHeading')]: 'network',
    [t('settingsView.regionHeading')]: 'network',
    [t('settingsView.sourcesHeading')]: 'info',
    [t('settingsView.actionsHeading')]: 'actions',
    [t('settingsView.speakerInfoHeading')]: 'info',
  };
  const buckets = {};
  GROUPS.forEach(g => { buckets[g.key] = []; });
  Array.from(body.querySelectorAll('.settings-section')).forEach(sec => {
    // Status and the phone-control feature card stay pinned and visible at the
    // top (the phone remote is a headline feature, not buried in a group).
    if (sec.id === 'stickInfoSection' || sec.id === 'phoneCardSection') return;
    let g;
    if (sec.classList.contains('settings-expert')) g = 'advanced';
    else if (sec.id && byId[sec.id]) g = byId[sec.id];
    else {
      const h = sec.querySelector('h3');
      g = (h && byHeading[h.textContent.trim()]) || 'info';
    }
    buckets[g].push(sec);
  });
  const frag = document.createDocumentFragment();
  GROUPS.forEach(g => {
    const items = buckets[g.key];
    if (!items.length) return;
    const det = document.createElement('details');
    det.className = 'settings-group';
    if (g.open) det.open = true;
    const sum = document.createElement('summary');
    sum.className = 'settings-group-summary';
    sum.textContent = g.label;
    det.appendChild(sum);
    items.forEach(it => det.appendChild(it)); // moves the node; ids/listeners intact
    frag.appendChild(det);
  });
  body.appendChild(frag);
}

function renderBoxSettings(s, box) {
  const info = s.info || {};
  const vol = s.volume || {};
  const bass = s.bass || {};
  const net = s.network || {};
  const sources = rollupSources(s.sources || []);
  // Series-II boxes expose Wi-Fi as a WIFI_INTERFACE in
  // NETWORK_WIFI_CONNECTED. BCO boxes (Portable/taigan, ST20-spotty)
  // drive Wi-Fi through a coprocessor exposed as eth0, so /networkInfo
  // reports an ETHERNET_INTERFACE in NETWORK_ETHERNET_CONNECTED with the
  // real LAN IP but no ssid/signal. Accept either, otherwise a fully
  // connected BCO box is mislabelled "not connected to Wi-Fi" (ssid /
  // signal then render as "-" since the coprocessor does not expose them).
  const wifi = (net.interfaces || []).find(i =>
    (i.type === 'WIFI_INTERFACE' && i.state === 'NETWORK_WIFI_CONNECTED') ||
    (i.state === 'NETWORK_ETHERNET_CONNECTED' && i.ipAddress));
  const signalLabel = {
    'EXCELLENT_SIGNAL': t('signal.excellent'),
    'GOOD_SIGNAL': t('signal.good'),
    'MARGINAL_SIGNAL': t('signal.marginal'),
    'POOR_SIGNAL': t('signal.poor'),
    'NO_SIGNAL': t('signal.none'),
  };
  // BCO boxes (scm ST20, Portable) expose Wi-Fi as an ethernet coprocessor
  // and report no signal class. Show an honest note instead of a bare "-"
  // so it does not read like a missing/failed reading (issue #90).
  const isCoprocessorWifi = wifi && wifi.type !== 'WIFI_INTERFACE';
  const signalText = signalLabel[wifi && wifi.signal] || (wifi && wifi.signal) ||
    (isCoprocessorWifi ? t('signal.notReported') : '-');
  const uid = uidSuffixFor(box);

  $('settingsBody').innerHTML = `
    <div class="settings-section" id="stickInfoSection">
      <h3>${escapeHtml(t('settingsView.statusHeading'))}</h3>
      <div id="stickInfoBody"><span class="muted small">${escapeHtml(t('common.loading'))}</span></div>
    </div>
    <div class="settings-section">
      <h3>${escapeHtml(t('settingsView.nameHeading'))}</h3>
      <div class="setting-row">
        <div class="combobox" id="boxNameCombo">
          <input type="text" id="boxNameInput" autocomplete="off"
                 value="${escapeAttr(info.name || '')}"
                 placeholder="${escapeAttr(t('settingsView.namePlaceholder'))}" />
          <button type="button" class="combo-toggle" id="boxNameToggle" title="${escapeAttr(t('settingsView.nameShowList'))}">&#9662;</button>
          <ul class="combo-list hidden" id="boxNameList"></ul>
        </div>
        <button class="btn btn-mini" id="boxNameSave">${escapeHtml(t('common.save'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.nameHelp', { uid: uid || '----' }))}</small>
    </div>

    <div class="settings-section">
      <h3>${escapeHtml(t('controls.volume'))}</h3>
      <div class="setting-row">
        <input type="range" id="boxVolume" min="0" max="100" value="${vol.actual || 0}" />
        <span class="setting-value" id="boxVolumeVal">${vol.actual || 0}</span>
      </div>
      ${vol.muted ? `<small class="muted small">${escapeHtml(t('settingsView.muted'))}</small>` : ''}
    </div>

    <div class="settings-section">
      <h3>${escapeHtml(t('settingsView.bassHeading'))}</h3>
      <div class="setting-row">
        <input type="range" id="boxBass"
               min="${(bass.min || 0) - (bass.default || 0)}"
               max="${(bass.max || 0) - (bass.default || 0)}"
               step="1"
               value="${(bass.actual || 0) - (bass.default || 0)}"
               ${bass.available ? '' : 'disabled'} />
        <span class="setting-value" id="boxBassVal">${formatRel((bass.actual || 0) - (bass.default || 0))}</span>
        <button class="btn btn-mini" id="boxBassReset" title="${escapeAttr(t('settingsView.bassResetTitle'))}">${escapeHtml(t('settingsView.bassResetBtn'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.bassHelp'))}</small>
    </div>

    <div class="settings-section">
      <h3>${escapeHtml(t('settingsView.wlanHeading'))}</h3>
      ${wifi ? `
        <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.wlanSsid'))}</span><span class="kv-val">${escapeHtml(wifi.ssid || '-')}</span></div>
        <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.wlanIp'))}</span><span class="kv-val">${escapeHtml(wifi.ipAddress || '-')}</span></div>
        <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.wlanSignal'))}</span><span class="kv-val">${escapeHtml(signalText)}</span></div>
        ${wifi.frequencyKHz ? `<div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.wlanFrequency'))}</span><span class="kv-val">${(wifi.frequencyKHz/1000).toFixed(0)} MHz</span></div>` : ''}
      ` : `<div class="muted small">${escapeHtml(t('settingsView.wlanNotConnected'))}</div>`}
      <small class="muted small" style="display:block;margin-top:8px">${escapeHtml(t('settingsView.wlanSwitchHint'))}</small>
      <button class="btn" id="wlanSwitchToggle" style="margin-top:8px">${escapeHtml(t('settingsView.wlanSwitchToggle'))}</button>
      <div id="wlanSwitchForm" class="hidden" style="margin-top:8px">
        <div class="wlan-row">
          <select id="boxWlanSelect"><option value="">${escapeHtml(t('settingsView.wlanPickPlaceholder'))}</option></select>
          <button class="btn btn-icon-sm" id="boxWlanRefresh" title="${escapeAttr(t('settingsView.wlanRefreshTitle'))}">&#x21bb;</button>
        </div>
        <input type="text" id="boxWlanSSID" placeholder="${escapeAttr(t('settingsView.wlanSsidPlaceholder'))}" />
        <div class="wlan-row">
          <input type="password" id="boxWlanPass" placeholder="${escapeAttr(t('settingsView.wlanPassPlaceholder'))}" />
          <button class="btn btn-icon-sm" id="boxWlanShowPass" title="${escapeAttr(t('settingsView.wlanShowPass'))}">&#128065;</button>
        </div>
        <button class="btn btn-danger btn-mini" id="boxWlanSave">${escapeHtml(t('settingsView.wlanSaveBtn'))}</button>
        <small class="muted small">${escapeHtml(t('settingsView.wlanWarn'))}</small>
      </div>
    </div>

    <div class="settings-section">
      <h3>${escapeHtml(t('settingsView.langHeading'))}</h3>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.langCurrent'))}</span><span class="kv-val" id="boxLangCurrent">${escapeHtml(t('common.loading'))}</span></div>
      <div class="setting-row">
        <select id="boxLangSelect">${langOptionsHtml()}</select>
        <button class="btn btn-mini" id="boxLangSave">${escapeHtml(t('common.save'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.langHelp'))}</small>
    </div>

    <div class="settings-section">
      <h3>${escapeHtml(t('settingsView.clockHeading'))}</h3>
      <div class="setting-row">
        <select id="boxClockFormat">
          <option value="24">${escapeHtml(t('settingsView.clock24h'))}</option>
          <option value="12">${escapeHtml(t('settingsView.clock12h'))}</option>
        </select>
        <button class="btn btn-mini toggle-btn" id="boxClockOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="boxClockOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.clockHelp'))}</small>
    </div>

    <div class="settings-section" id="resumeOnPowerSection">
      <h3>${escapeHtml(t('settingsView.resumeOnPowerHeading'))}</h3>
      <div class="setting-row">
        <button class="btn btn-mini toggle-btn" id="resumeOnPowerOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="resumeOnPowerOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.resumeOnPowerHelp'))}</small>
    </div>

    <div class="settings-section" id="displayTrackSection">
      <h3>${escapeHtml(t('settingsView.displayTrackHeading'))}</h3>
      <div class="setting-row">
        <button class="btn btn-mini toggle-btn" id="displayTrackOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="displayTrackOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <div class="setting-row hidden" id="displayTrackModeRow">
        <span class="muted small" style="margin-right:6px">${escapeHtml(t('settingsView.displayTrackModeLabel'))}</span>
        <button class="btn btn-mini toggle-btn" id="displayTrackModeArtist">${escapeHtml(t('settingsView.displayTrackModeArtist'))}</button>
        <button class="btn btn-mini toggle-btn" id="displayTrackModeTitle">${escapeHtml(t('settingsView.displayTrackModeTitle'))}</button>
        <button class="btn btn-mini toggle-btn" id="displayTrackModeBoth">${escapeHtml(t('settingsView.displayTrackModeBoth'))}</button>
      </div>
      <div class="format-warn">
        <div class="warn-icon-inline">&#9888;</div>
        <div><b>${escapeHtml(t('settingsView.displayTrackWarn'))}</b></div>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.displayTrackHelp'))}</small>
    </div>

    <div class="settings-section hidden" id="airplayOptSection">
      <h3>${escapeHtml(t('settingsView.airplayOptHeading'))}</h3>
      <div class="setting-row">
        <button class="btn btn-mini toggle-btn" id="airplayOptOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="airplayOptOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.airplayOptHelp'))}</small>
      <small class="muted small" style="display:block;margin-top:6px">${escapeHtml(t('settingsView.airplayOptRecommend'))}</small>
    </div>

    <details class="settings-section settings-expert" id="announceSection">
      <summary class="settings-expert-summary">${escapeHtml(t('settingsView.announceHeading'))} <span class="expert-badge">${escapeHtml(t('settingsView.expertBadge'))}</span></summary>
      <small class="muted small expert-intro">${escapeHtml(t('settingsView.announceHelp'))}</small>
      <div class="setting-row" style="margin-top:8px">
        <input type="text" id="announceText" class="text-input" maxlength="200" value="${escapeAttr(t('settingsView.announceDefault'))}" placeholder="${escapeAttr(t('settingsView.announcePlaceholder'))}" style="flex:1" />
        <select id="announceLang" style="flex:0 0 130px;" title="${escapeAttr(t('settingsView.announceLangLabel'))}">
          <option value="de">Deutsch</option>
          <option value="en">English</option>
          <option value="fr">Français</option>
          <option value="es">Español</option>
          <option value="it">Italiano</option>
          <option value="nl">Nederlands</option>
          <option value="pl">Polski</option>
          <option value="pt">Português</option>
          <option value="tr">Türkçe</option>
          <option value="uk">Українська</option>
          <option value="ja">日本語</option>
        </select>
        <button class="btn btn-mini" id="announceTranslate" title="${escapeAttr(t('settingsView.announceTranslateTitle'))}">${escapeHtml(t('settingsView.announceTranslate'))}</button>
        <button class="btn btn-mini" id="announceSend">${escapeHtml(t('settingsView.announceSend'))}</button>
      </div>
      <div class="setting-row" style="align-items:center;gap:10px;margin-top:8px">
        <label class="muted small" for="announceVolume" style="flex:0 0 auto">${escapeHtml(t('settingsView.announceVolumeLabel'))}</label>
        <input type="range" id="announceVolume" min="1" max="100" value="25" style="flex:1" />
        <span class="muted small" id="announceVolumeVal" style="flex:0 0 2.5em;text-align:right">25</span>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.announceCharHint'))}</small>
      <small class="muted small" style="display:block;margin-top:6px">&#9432; ${escapeHtml(t('settingsView.announcePrivacy'))}</small>
      <details style="margin-top:10px">
        <summary class="muted small" style="cursor:pointer">${escapeHtml(t('settingsView.announceExpert'))}</summary>
        <small class="muted small" style="display:block;margin:6px 0">${escapeHtml(t('settingsView.announceExpertHelp'))}</small>
        <div class="setting-row">
          <code id="announceCurl" class="announce-curl"></code>
          <button class="btn btn-mini" id="announceCopy">${escapeHtml(t('settingsView.announceCopy'))}</button>
        </div>
      </details>
    </details>

    <details class="settings-section settings-expert">
      <summary class="settings-expert-summary">${escapeHtml(t('settingsView.webhookHeading'))} <span class="expert-badge">${escapeHtml(t('settingsView.expertBadge'))}</span></summary>
      <small class="muted small expert-intro">${escapeHtml(t('settingsView.webhookHelp'))}</small>
      <div class="setting-row">
        <select id="webhookTarget" style="flex:1;">
          <option value="thumb">${escapeHtml(t('settingsView.webhookKeyThumb'))}</option>
          <option value="preset1">${escapeHtml(t('preset.key', { n: 1 }))}</option>
          <option value="preset2">${escapeHtml(t('preset.key', { n: 2 }))}</option>
          <option value="preset3">${escapeHtml(t('preset.key', { n: 3 }))}</option>
          <option value="preset4">${escapeHtml(t('preset.key', { n: 4 }))}</option>
          <option value="preset5">${escapeHtml(t('preset.key', { n: 5 }))}</option>
          <option value="preset6">${escapeHtml(t('preset.key', { n: 6 }))}</option>
          <option value="aux">AUX</option>
          <option value="power">Power</option>
        </select>
        <select id="webhookMode" style="flex:0 0 170px;">
          <option value="additional">${escapeHtml(t('settingsView.webhookModeAdditional'))}</option>
          <option value="replace">${escapeHtml(t('settingsView.webhookModeReplace'))}</option>
        </select>
      </div>
      <small class="muted small" id="webhookModeNote"></small>
      <div class="setting-row">
        <select id="webhookType" style="flex:0 0 200px;">
          <option value="http">${escapeHtml(t('settingsView.webhookTypeHttp'))}</option>
          <option value="wol">${escapeHtml(t('settingsView.webhookTypeWol'))}</option>
          <option value="udp">${escapeHtml(t('settingsView.webhookTypeUdp'))}</option>
        </select>
      </div>
      <div class="setting-row" id="webhookHttpRow">
        <input type="text" id="webhookUrl" autocomplete="off" placeholder="${escapeAttr(t('settingsView.webhookUrlPlaceholder'))}" />
        <select id="webhookMethod" style="flex:0 0 90px;">
          <option value="GET">GET</option>
          <option value="POST">POST</option>
        </select>
      </div>
      <div class="setting-row" id="webhookBodyRow">
        <input type="text" id="webhookBody" autocomplete="off" placeholder="${escapeAttr(t('settingsView.webhookBodyPlaceholder'))}" />
      </div>
      <div class="setting-row hidden" id="webhookWolRow">
        <input type="text" id="webhookMac" autocomplete="off" placeholder="AA:BB:CC:DD:EE:FF" />
      </div>
      <div class="setting-row hidden" id="webhookUdpRow">
        <input type="text" id="webhookUdpHost" autocomplete="off" placeholder="${escapeAttr(t('settingsView.webhookUdpHostPlaceholder'))}" />
        <input type="text" id="webhookUdpPort" autocomplete="off" placeholder="Port" style="flex:0 0 80px;" />
        <select id="webhookUdpEnc" style="flex:0 0 110px;">
          <option value="text">text</option>
          <option value="hex">hex</option>
          <option value="base64">base64</option>
        </select>
      </div>
      <div class="setting-row hidden" id="webhookUdpPayloadRow">
        <input type="text" id="webhookUdpPayload" autocomplete="off" placeholder="${escapeAttr(t('settingsView.webhookUdpPayloadPlaceholder'))}" />
      </div>
      <div class="setting-row">
        <button class="btn btn-mini toggle-btn" id="webhookOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="webhookOff">${escapeHtml(t('settingsView.clockOff'))}</button>
        <span style="flex:1;"></span>
        <button class="btn btn-mini" id="webhookTestBtn">${escapeHtml(t('settingsView.webhookTestBtn'))}</button>
        <button class="btn btn-mini btn-primary" id="webhookSaveBtn">${escapeHtml(t('common.save'))}</button>
      </div>
    </details>

    ${(() => {
      // Box-to-box preset copy (Expert): source is THIS speaker (the one whose
      // settings are open), the user picks a target to push keys 1-6 onto. Only
      // rendered when at least one other STR speaker exists, so a single-speaker
      // user never sees it.
      const src = state.settingsBox;
      const targets = (state.boxes || []).filter(b => b.kind !== 'stock' && src && b.host !== src.host);
      if (!targets.length) return '';
      const opts = targets.map(b => {
        const lbl = (b.friendlyName || b.name || b.host) + (b.model && b.model !== 'SoundTouch' ? ' (' + b.model + ')' : '');
        return `<option value="${escapeAttr(b.host)}|${b.port || 0}">${escapeHtml(lbl)}</option>`;
      }).join('');
      // With more than one other speaker, offer "apply to all" so a multi-box
      // user gets the same 1-6 on every speaker in one go (Gerald, 4 boxes).
      const allOpt = targets.length > 1
        ? `<option value="__ALL__">${escapeHtml(t('settingsView.copyPresetsAllTargets'))}</option>`
        : '';
      return `<details class="settings-section settings-expert">
      <summary class="settings-expert-summary">${escapeHtml(t('settingsView.copyPresetsHeading'))} <span class="expert-badge">${escapeHtml(t('settingsView.expertBadge'))}</span></summary>
      <small class="muted small expert-intro">${escapeHtml(t('settingsView.copyPresetsHelp'))}</small>
      <div class="setting-row">
        <select id="copyPresetTarget" style="flex:1;">${allOpt}${opts}</select>
        <button class="btn btn-mini btn-warning" id="copyPresetBtn">${escapeHtml(t('settingsView.copyPresetsBtn'))}</button>
      </div>
    </details>`;
    })()}

    <details class="settings-section settings-expert">
      <summary class="settings-expert-summary">${escapeHtml(t('settingsView.urlPresetHeading'))} <span class="expert-badge">${escapeHtml(t('settingsView.expertBadge'))}</span></summary>
      <small class="muted small expert-intro">${escapeHtml(t('settingsView.urlPresetHelp'))}</small>
      <div class="setting-row">
        <select id="urlPresetSlot" style="flex:0 0 130px;">
          ${[1, 2, 3, 4, 5, 6].map(n => `<option value="${n}">${escapeHtml(t('preset.key', { n }))}</option>`).join('')}
        </select>
        <input type="text" id="urlPresetName" autocomplete="off" placeholder="${escapeAttr(t('settingsView.urlPresetNamePlaceholder'))}" style="flex:1;" />
      </div>
      <div class="setting-row">
        <input type="text" id="urlPresetUrl" autocomplete="off" placeholder="${escapeAttr(t('settingsView.urlPresetUrlPlaceholder'))}" />
        <button class="btn btn-mini btn-primary" id="urlPresetSaveBtn">${escapeHtml(t('common.save'))}</button>
      </div>
    </details>

    <div class="settings-section">
      <h3>${escapeHtml(t('settingsView.sourcesHeading'))}</h3>
      <div class="sources-grid">
        ${sources.map(src => {
          const su = (src.source || '').toUpperCase();
          const ready = src.status === 'READY';
          // Sources STR revives even though the box reports them UNAVAILABLE (the
          // Bose cloud is gone): the box plays STR's stream over UPnP, Spotify runs
          // through STR's go-librespot, and the Library plays from a DLNA server
          // via the app. Showing those as "inactive" was misleading; mark them
          // "via STR". QPlay and the like stay inactive (STR does not revive them).
          const viaSTR = !ready && (su === 'UPNP' || su === 'SPOTIFY' || su.startsWith('STORED_MUSIC') || su === 'LOCAL_MUSIC');
          // AirPlay and Bluetooth are local firmware features that work without
          // the Bose cloud. The box's /sources READY flag is unreliable for them
          // post-cloud (it tracked the original Bose-account setup), so a working
          // AirPlay can report not-READY and was shown as "inactive" (#200). Label
          // these "available" instead, with the hint below for the nuance.
          const localFw = !ready && (su === 'AIRPLAY' || su === 'BLUETOOTH');
          const cls = ready ? 'src-ok' : (viaSTR ? 'src-ok src-via-str' : (localFw ? 'src-ok' : 'src-unav'));
          const label = sourceLabel(src.source);
          const statusLabel = ready ? t('settingsView.sourceActive') : (viaSTR ? t('settingsView.sourceViaSTR') : (localFw ? t('settingsView.sourceAvailable') : t('settingsView.sourceInactive')));
          return `<div class="source-pill ${cls}" title="${escapeAttr(sourceHint(src.source) || src.sourceAccount || '')}">${escapeHtml(label)} <small>${escapeHtml(statusLabel)}</small></div>`;
        }).join('')}
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.spotifyHint'))}</small>
      ${sources.some(x => x.source === 'AIRPLAY' && x.status !== 'READY') ? `<small class="muted small">${escapeHtml(t('settingsView.airplayHint'))}</small>` : ''}
    </div>

    <div class="settings-section">
      <h3>${escapeHtml(t('settingsView.regionHeading'))}</h3>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.regionCurrent'))}</span><span class="kv-val" id="currentAppRegion">${escapeHtml(t('common.loading'))}</span></div>
      <div class="setting-row">
        <select id="appRegionSelect"></select>
        <button class="btn btn-mini" id="appRegionSave">${escapeHtml(t('common.save'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.regionHelp'))}</small>
    </div>
    <div class="settings-section">
      <h3>${escapeHtml(t('settingsView.actionsHeading'))}</h3>
      <div class="actions-grid">
        <button class="btn btn-mini" id="boxSyncPresetsBtn">${escapeHtml(t('settingsView.syncHardwareKeys'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.syncHardwareKeysHelp'))}</p>
        <button class="btn btn-mini" id="boxSaveLogsBtn">${escapeHtml(t('footer.saveLogs'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.saveLogsHelp'))}</p>
        <button class="btn btn-mini" id="boxEmailSupportBtn">${escapeHtml(t('settingsView.emailSupport'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.emailSupportHelp'))}</p>
        <button class="btn btn-mini btn-warning" id="boxRebootBtn">${escapeHtml(t('speaker.reboot'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.rebootHelp'))}</p>
        <hr class="actions-divider" />
        <button class="btn btn-mini btn-danger" id="boxTrueFactoryResetBtn">${escapeHtml(t('settingsView.trueFactoryResetBtn'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.trueFactoryResetHelpShort'))}</p>
        <button class="btn btn-mini btn-danger" id="boxRemoveSTRBtn">${escapeHtml(t('settingsView.removeSTRBtn'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.removeSTRHelp'))}</p>
      </div>
    </div>
    <details class="settings-section settings-expert">
      <summary class="settings-expert-summary">${escapeHtml(t('settingsView.restoreHeading'))} <span class="exp-badge">${escapeHtml(t('settingsView.experimentalBadge'))}</span></summary>
      <p class="muted small">${escapeHtml(t('settingsView.restoreHelp'))}</p>
      <label class="muted small" for="boxRestoreXml">${escapeHtml(t('settingsView.restoreXmlLabel'))}</label>
      <textarea id="boxRestoreXml" rows="5" placeholder="${escapeAttr(t('settingsView.restoreImportPlaceholder'))}" style="width:100%;margin-top:4px"></textarea>
      <div class="setting-row" style="margin-top:6px">
        <button class="btn btn-mini" id="boxRestoreBtn">${escapeHtml(t('settingsView.restoreBtn'))}</button>
      </div>
      <div id="boxRestoreResult" class="muted small" style="margin-top:8px"></div>
    </details>
    <div class="settings-section">
      <h3>${escapeHtml(t('settingsView.speakerInfoHeading'))}</h3>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.modelLabel'))}</span><span class="kv-val">${escapeHtml(info.type || '-')}</span></div>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.firmwareLabel'))}</span>
        <span class="kv-val">${fwStatusInline(info)}</span>
      </div>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.deviceIdLabel'))}</span><span class="kv-val small muted">${escapeHtml(info.deviceID || '-')}</span></div>
      ${fwUpdateHint(info)}
    </div>
    <div class="settings-section phone-feature" id="phoneCardSection">
      <h3><span class="phone-feature-icon">&#128241;</span> ${escapeHtml(t('settingsView.phoneHeading'))}</h3>
      <small class="muted small">${escapeHtml(t('settingsView.phoneHelp'))}</small>
      <div class="phone-card">
        <img id="phoneQrImg" class="phone-qr" alt="QR" />
        <div class="phone-url-row">
          <code id="phoneUrl" class="phone-url"></code>
          <button class="btn btn-mini" id="phoneUrlCopy">${escapeHtml(t('common.copy'))}</button>
        </div>
      </div>
    </div>
  `;

  // Phone control: build this speaker's web-remote URL from its reachable
  // host:port (probeSTR records the right port: 8888 direct or 17008 redirect)
  // and render a locally generated QR (no external service). Grouped into the
  // Status & info accordion by groupSettingsSections below.
  (async () => {
    const urlEl = $('phoneUrl');
    if (!urlEl || !box || !box.host) return;
    const url = `http://${box.host}:${box.port || 8888}/`;
    urlEl.textContent = url;
    const copyBtn = $('phoneUrlCopy');
    if (copyBtn) {
      copyBtn.onclick = async () => {
        try {
          await navigator.clipboard.writeText(url);
          copyBtn.textContent = t('common.copied');
          setTimeout(() => { copyBtn.textContent = t('common.copy'); }, 1500);
        } catch { /* clipboard blocked: the URL is still selectable */ }
      };
    }
    try {
      const data = await PhoneQR(url);
      const img = $('phoneQrImg');
      if (img && data) img.src = data;
    } catch (e) { try { console.warn('phone QR failed', e); } catch {} }
  })();

  groupSettingsSections();

  // Name combobox: input + dropdown list, free-typeable + filterable.
  wireCombobox('boxNameInput', 'boxNameToggle', 'boxNameList', deps.getRoomNames());

  // Wire the WLAN switch UI: collapsible form, PC-known SSIDs in
  // a dropdown with auto-password prefill, Save sends
  // PUT /api/box/wlan.
  wireWlanSwitch(box);

  // The "Update" button next to the firmware version scrolls down
  // to the update banner.
  const fwBtn = $('fwUpdateBtn');
  if (fwBtn) {
    fwBtn.onclick = () => {
      const banner = $('fwUpdateBanner');
      if (banner) banner.scrollIntoView({ behavior: 'smooth', block: 'center' });
    };
  }
  // Outdated-firmware banner links: open the Bose support guide / USB download
  // directory in the user's browser (Wails BrowserOpenURL) instead of leaving
  // them as plain text the user has to retype (Jens, 2026-06-27).
  for (const id of ['fwGuideLink', 'fwUsbLink']) {
    const el = $(id);
    if (el) el.onclick = (e) => { e.preventDefault(); try { BrowserOpenURL(el.dataset.url); } catch {} };
  }

  // Status block: software version + USB stick mount.
  (async () => {
    const body = $('stickInfoBody');
    if (!body) return;
    const app = state.appInfo || {};

    // Software version, three tiers:
    //   - Version + build match -> up to date (green)
    //   - Version matches, build differs -> update available (yellow)
    //   - Version differs -> outdated (red)
    let softwareLine = `<span class="muted small">${escapeHtml(t('common.unknown'))}</span>`;
    let softwareBtn = '';
    try {
      const v = await BoxAgentVersion(box.host, box.port);
      const boxVer = v.version || '?';
      const boxBuild = v.build || '';
      const appVer = app.version || '?';
      const appBuild = app.build || '';
      // Direction-aware (see compareVerBuild): only offer the OTA when the app
      // is newer than the box. When the box is newer than the app, an OTA would
      // downgrade it, so show "update the app" with no button (#105).
      const cmp = compareVerBuild(appVer, appBuild, boxVer, boxBuild);
      const otaBtn = () => (state.otaInProgress && state.otaTargetHost && state.otaTargetHost !== box.host)
        ? `<button class="btn btn-mini btn-primary" id="stickInfoUpdateBtn" disabled>${escapeHtml(t('update.otherBoxRunning', { name: state.otaTargetName || '...' }))}</button>`
        : `<button class="btn btn-mini btn-primary" id="stickInfoUpdateBtn">${escapeHtml(t('update.refreshBtn'))}</button>`;
      if (cmp === 0) {
        const buildSuffix = boxBuild ? ` (Build ${escapeHtml(boxBuild)})` : '';
        softwareLine = `<span class="fw-ok">&#10003; ${escapeHtml(t('settingsView.swCurrent'))}</span> <span class="muted small">${escapeHtml(boxVer)}${buildSuffix}</span>`;
      } else if (cmp > 0) {
        // When only the build stamp differs (same version string), show the
        // build on both sides so the line is not the confusing "v0.8.1 -> v0.8.1"
        // (Jens, 2026-06-17). A real release bumps the version, so production
        // never hits the same-version case; this is mainly dev builds.
        const sameVer = boxVer === appVer;
        const instDisp = sameVer && boxBuild ? `${boxVer} (Build ${boxBuild})` : boxVer;
        const nextDisp = sameVer && appBuild ? `${appVer} (Build ${appBuild})` : appVer;
        softwareLine = `<span class="fw-pending">${escapeHtml(t('settingsView.swUpdateAvail'))}</span> <span class="muted small">${escapeHtml(t('update.versionLine', { installed: instDisp, next: nextDisp }))}</span>`;
        softwareBtn = otaBtn();
      } else {
        softwareLine = `<span class="fw-pending">${escapeHtml(t('update.appBehindShort', { appVersion: appVer }))}</span> <span class="muted small">${escapeHtml(boxVer)}</span>`;
      }
    } catch {}

    // USB stick mount status. Try /api/stick/status first (newer
    // agent); fall back to /api/debug/state.stick_listing for older
    // agent versions.
    let stickLine = `<span class="muted small">${escapeHtml(t('common.unknown'))}</span>`;
    let sshOpen = false;
    try {
      const r = await deps.boxFetch(box, '/api/stick/status');
      const ct = r.headers.get('content-type') || '';
      if (r.ok && ct.includes('json')) {
        const data = await r.json();
        sshOpen = !!data.sshOpen;
        // Trust the agent's mounted flag (v0.7.33+ stickReallyMounted reports it
        // only for a real stick, not the leftover empty mountpoint, #105). Do NOT
        // also require data.version: the agent can report mounted without a
        // version, and requiring it wrongly showed an inserted stick as removed
        // (#105 follow-up).
        if (data.mounted) {
          stickLine = `<span class="fw-ok">&#10003; ${escapeHtml(t('settingsView.stickDetected'))}</span>` + (data.version ? ` <span class="muted small">${escapeHtml(data.version)}</span>` : '');
        } else if (sshOpen) {
          // Stick not mounted but SSH is open. On STR that only happens because a
          // stick was in at boot (it opens sshd via the remote_services marker;
          // pulling it out does not close sshd until the next reboot). Some boxes
          // (the Portable) never auto-mount the stick, so mounted=false even with
          // the stick physically in. Report it honestly as "still inserted"
          // instead of flatly "removed", which contradicted the remove-the-stick
          // recommendation right below it (Jens, 2026-06-17).
          stickLine = `<span class="fw-warn">${escapeHtml(t('settingsView.stickStillInserted'))}</span>`;
        } else {
          // No stick and SSH closed: the secure steady state after a clean install.
          stickLine = `<span class="muted small">${escapeHtml(t('settingsView.stickRemoved'))}</span>`;
        }
      } else {
        // Fallback: debug/state listing for older agents.
        const rd = await deps.boxFetch(box, '/api/debug/state');
        if (rd.ok && (rd.headers.get('content-type') || '').includes('json')) {
          const d = await rd.json();
          const listing = d.stick_listing;
          if (Array.isArray(listing) && listing.length > 0 && !String(listing[0]).startsWith('ERR')) {
            stickLine = `<span class="fw-ok">&#10003; ${escapeHtml(t('settingsView.stickDetected'))}</span>`;
          } else {
            stickLine = `<span class="muted small">${escapeHtml(t('settingsView.stickRemoved'))}</span>`;
          }
        }
      }
    } catch {}

    // Show the warning banner only once the stick is no longer
    // mounted — while a stick is in the box, the agent is either
    // doing initial install or applying an update, SSH is expected
    // to be open in that window, and the banner is just noise.
    // Also suppress while an OTA update is in flight: the agent is
    // mid-restart and SSH state is transient; the banner's "Reboot
    // now" button would interrupt the update.
    // In Speaker Settings the detailed recommendation below (securityWarn) is the
    // richer version of the same warning, so hide the global top banner here to
    // avoid showing it twice. checkSshBanner drives the top banner in the other
    // views.
    const gb = $('globalSecurityBanner');
    if (gb) gb.classList.add('hidden');

    // Show the remove-the-stick + restart recommendation whenever SSH is open.
    // As of the pre-1.0 hardening (run.sh no longer force-opens sshd every boot),
    // SSH being open means a stick was in at boot (the stick is what opens sshd
    // via its remote_services marker) and a stickless reboot closes it again, so
    // sshOpen is now a self-clearing, accurate signal. This also fixes the case
    // where the stick is in but not mounted (the Portable): the old
    // `sshOpen && !stickMounted` happened to work there, but `mounted` boxes
    // showed nothing. Suppressed during the OTA window (the agent restarts and
    // the reboot button would interrupt it).
    const securityWarn = (sshOpen && !state.otaInProgress) ? `
      <div class="security-warn">
        <div class="security-warn-title">${escapeHtml(t('banner.recommendationShort'))}</div>
        <div class="security-warn-text">
          ${escapeHtml(t('banner.sshRecommend'))}
        </div>
        <button class="btn btn-mini" id="securityRebootBtn">${escapeHtml(t('speaker.rebootNow'))}</button>
      </div>` : '';

    // Prominent update banner at the very TOP of Speaker Settings whenever an
    // update is available (softwareBtn is only set then). Moved here from the
    // music view so normal users see it where they manage the speaker and do
    // not mis-click it while just listening. The update button lives in the
    // banner now; the software kv-row below keeps the version status text.
    const updateBanner = softwareBtn ? `
      <div class="update-banner" style="margin-bottom:14px">
        <div class="update-msg"><b>${escapeHtml(t('update.speakerUpdateAvailFor', { name: box.friendlyName || box.name || box.host }))}</b><br>
          <small class="muted">${escapeHtml(t('update.rebootNote'))}</small></div>
        ${softwareBtn}
      </div>` : '';
    body.innerHTML = updateBanner + `
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.softwareLabel'))}</span>
        <span class="kv-val">${softwareLine}</span></div>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.usbStickLabel'))}</span>
        <span class="kv-val">${stickLine}</span></div>
      ${securityWarn}
    `;
    const ub = $('stickInfoUpdateBtn');
    if (ub) {
      // The stick-in confirmation now lives inside doBoxUpdate (the single
      // OTA chokepoint), so both this button and the music-tab banner are
      // gated identically and Cancel always aborts before anything starts.
      // Pass the settings-selected box explicitly: every other handler here
      // targets settingsBox, and doBoxUpdate defaulting to currentBox was
      // OTA-ing the music-tab box instead of the one shown here (#105).
      ub.onclick = () => deps.doBoxUpdate(box);
    }
    const sb = $('securityRebootBtn');
    if (sb) sb.onclick = async () => {
      const ok = await confirmWarn(
        t('speaker.rebootConfirmTitle'),
        t('speaker.rebootConfirmDetailedBody')
      );
      if (!ok) return;
      try {
        await RebootBox(box.host, box.port);
        showToast(t('speaker.rebootingToast'));
        setTimeout(deps.discoverBoxes, 35000);
      } catch (e) { showError(e); }
    };
  })();

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

  // Experimental: restore account-linked cloud presets (e.g. Deezer) the box
  // dropped, from STR's snapshot or a saved presets XML the user pastes. Writes
  // them back onto their slots and re-advertises the source; the box usually
  // needs a reboot to re-sync, so we offer one when the agent recommends it.
  async function doCloudRestore(xml) {
    const out = $('boxRestoreResult');
    if (out) out.textContent = t('settingsView.restoreRunning');
    try {
      const r = await RestoreBoxSnapshot(box.host, box.port, xml || '');
      const restored = (r && r.restored) || [];
      const services = (r && r.services) || [];
      const unavailable = (r && r.unavailable) || [];
      const expired = (r && r.expired) || [];
      const parsed = (r && typeof r.parsed === 'number') ? r.parsed : null;
      if (!restored.length && !services.length && !expired.length) {
        // Distinguish "could not read any buttons from the paste" (parsed 0,
        // usually the wrong text) from "read buttons but none are account-bound"
        // (parsed > 0, nothing to restore here), so the guidance is precise.
        if (out) {
          out.textContent = (parsed === 0)
            ? t('settingsView.restoreNoneParsed')
            : (parsed > 0 ? t('settingsView.restoreNoneCloud', { count: parsed }) : t('settingsView.restoreNone'));
        }
        return;
      }
      if (out) {
        const parts = [];
        if (restored.length) {
          // If a written source still reads unavailable, its saved login has
          // likely expired: say so honestly rather than implying a clean success.
          parts.push(unavailable.length
            ? t('settingsView.restoreUnavailable', { slots: restored.join(', '), services: services.join(', '), unavailable: unavailable.join(', ') })
            : t('settingsView.restoreDone', { slots: restored.join(', '), services: services.join(', ') }));
        }
        if (expired.length) {
          // Services whose saved login on the speaker already expired with the Bose
          // cloud: STR did NOT write those buttons (the speaker drops them on its
          // own within seconds) and a reboot cannot bring the login back.
          parts.push(t('settingsView.restoreExpired', { services: expired.join(', ') }));
        }
        out.textContent = parts.join('\n');
      }
      if (r && r.rebootRecommended) {
        const ok = await confirmWarn(t('speaker.rebootConfirmTitle'), t('settingsView.restoreRebootBody'));
        if (ok) {
          try {
            await RebootBox(box.host, box.port);
            showToast(t('speaker.rebootingToast'));
            setTimeout(deps.discoverBoxes, 35000);
          } catch (e) { showError(e); }
        }
      }
    } catch (e) {
      if (out) out.textContent = '';
      showError(e);
    }
  }
  const restoreBtn = $('boxRestoreBtn');
  if (restoreBtn) restoreBtn.onclick = () => {
    const ta = $('boxRestoreXml');
    doCloudRestore(ta ? ta.value : '');
  };

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
            BrowserOpenURL('mailto:str@sichtbar-app.de?subject=' +
              encodeURIComponent(subject) + '&body=' + encodeURIComponent(body));
          } catch {}
          showToast(t('settingsView.emailSupportToast', { path: res.savePath }));
        }
      } catch (e) { showError(e); }
      emailSupportBtn.disabled = false;
    };
  }

  // Speaker reboot button.
  const rebootBtn = $('boxRebootBtn');
  if (rebootBtn) {
    rebootBtn.onclick = async () => {
      const ok = await confirmWarn(
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

  // True factory reset: wipes Bose's persistence files via SSH so
  // the speaker truly returns to OOB. Required for the Bose iOS app
  // to be able to re-onboard the speaker — Bose's hardware reset
  // (Preset 1 + Vol-) leaves NetworkProfiles.xml intact, the
  // speaker auto-rejoins its last WiFi, and the iOS app's WAC
  // discovery sees an "already configured" speaker and gives up.
  const tfrBtn = $('boxTrueFactoryResetBtn');
  if (tfrBtn) {
    tfrBtn.onclick = async () => {
      const ok = await confirmWarn(
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
          // when the iOS app brings it back onto JJ3.
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
      const ok = await confirmWarn(
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
          // Speaker reboots into vanilla Bose OOB; it will be off the LAN
          // for a while. Re-scan in 60 s.
          setTimeout(deps.discoverBoxes, 60000);
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

  // Language (BETA): GET /language on :8090 of the box, parse the
  // sysLanguage integer, show in the label and pre-select the
  // dropdown. POST on Save. Errors surface via showError; the
  // dropdown stays editable so users can keep trying.
  const langCurrent = $('boxLangCurrent');
  const langSel = $('boxLangSelect');
  const langSave = $('boxLangSave');
  const boseHost = box.host;
  // Clock + language go through the Go backend (GetBoxLanguage etc.):
  // the box's :8090 sends no CORS headers, so a direct frontend fetch
  // with a text/xml POST failed with "Failed to fetch" (CORS preflight
  // the box never answers). Server-side has no CORS and reaches :8090
  // even on Series-I/BCO boxes. See app.go boseGet/bosePostXML.
  if (langCurrent && langSel) {
    (async () => {
      try {
        const v = await GetBoxLanguage(boseHost);
        langCurrent.textContent = v ? boseLangLabel(v) : t('settingsView.langNoValue');
        if (v) langSel.value = v;
      } catch {
        langCurrent.textContent = t('settingsView.langUnreachable');
      }
    })();
  }
  if (langSave) {
    langSave.onclick = async () => {
      const v = langSel.value;
      try {
        await SetBoxLanguage(boseHost, parseInt(v, 10) || 0);
        showToast(t('settingsView.langSavedToast', { v: boseLangLabel(v) }));
        langCurrent.textContent = boseLangLabel(v);
      } catch (e) { showError(e); }
    };
  }

  // Clock display (BETA): GET /clockDisplay to see current state,
  // POST to toggle. The endpoint is undocumented in the public Bose
  // Web API PDF and may not exist on every model. On 404 / 500 we
  // surface "not supported" rather than burying the failure.
  const clockOn = $('boxClockOn');
  const clockOff = $('boxClockOff');
  const clockFormat = $('boxClockFormat');
  // Reflect the current on/off state by highlighting the active button
  // instead of a separate status line. null = unknown (neither lit).
  const paintClock = (enabled) => {
    if (clockOn) clockOn.classList.toggle('active', enabled === true);
    if (clockOff) clockOff.classList.toggle('active', enabled === false);
  };
  // Preselect the box's current 12/24h format in the dropdown.
  if (clockFormat) {
    GetClockFormat24(boseHost).then(is24 => { clockFormat.value = is24 ? '24' : '12'; }).catch(() => {});
  }
  // refreshClock reads the current /clockDisplay state. Previously
  // any non-200 / fetch failure surfaced "not supported on this
  // model", but live-verified 2026-05-30 on a SoundTouch Portable
  // (taigan): the GET path returns 200 most of the time but the
  // box sometimes responds slowly or briefly drops the request
  // during BoseApp restart, and that one missed GET painted the
  // settings panel as "permanently unsupported" even though POST
  // toggles work fine. Don't draw conclusions from a single GET:
  // unknown means unknown, not unsupported.
  let clockEnabled = false; // tracked so a format change re-sends with the right on/off state
  const refreshClock = async () => {
    try {
      const s = await GetClockDisplay(boseHost);
      clockEnabled = (s === 'true');
      paintClock(s === 'true' ? true : (s === 'false' ? false : null));
    } catch {
      paintClock(null);
    }
  };
  refreshClock();
  const postClock = async (enable) => {
    try {
      // Real IANA time zone (e.g. "Europe/Berlin"), like the Bose iOS
      // app sets, so the speaker handles DST itself. userOffsetMinute is
      // sent too as a correct-now fallback: minutes EAST of UTC, i.e.
      // the negated JS getTimezoneOffset().
      let tz = '';
      try { tz = Intl.DateTimeFormat().resolvedOptions().timeZone || ''; } catch {}
      const offsetMin = -new Date().getTimezoneOffset();
      const fmt24 = (clockFormat ? clockFormat.value : '24') === '24';
      await SetClockDisplay(boseHost, enable, tz, offsetMin, fmt24);
      showToast(t('settingsView.clockSavedToast', { v: enable ? 'on' : 'off' }));
      await refreshClock();
    } catch (e) { showError(e); }
  };
  if (clockOn) clockOn.onclick = () => { paintClock(true); postClock(true); };
  if (clockOff) clockOff.onclick = () => { paintClock(false); postClock(false); };
  // Send the 12/24h format to the box immediately on dropdown change,
  // keeping the current on/off state (no need to click "On" again).
  if (clockFormat) clockFormat.onchange = () => postClock(clockEnabled);

  // Resume last station on power-on (all models, default on). GET reports the
  // current state; toggling applies live on the box (no reboot). The next real
  // power press either brings the last station back, like Bose did, or stays
  // silent. A self-wake (zone/stereo pair) never resumes regardless.
  const ropOn = $('resumeOnPowerOn');
  const ropOff = $('resumeOnPowerOff');
  const paintResumeOnPower = (enabled) => {
    if (ropOn) ropOn.classList.toggle('active', enabled === true);
    if (ropOff) ropOff.classList.toggle('active', enabled === false);
  };
  if (ropOn && ropOff) {
    (async () => {
      try {
        const r = await GetResumeOnPowerOn(box.host, box.port);
        // Default on: treat anything other than an explicit false as enabled.
        paintResumeOnPower(r && r.enabled === false ? false : true);
      } catch { paintResumeOnPower(true); }
    })();
    const setROP = async (enabled) => {
      paintResumeOnPower(enabled);
      try {
        await SetResumeOnPowerOn(box.host, box.port, enabled);
        showToast(t('settingsView.resumeOnPowerSavedToast'));
      } catch (e) { showError(e); }
    };
    ropOn.onclick = () => setROP(true);
    ropOff.onclick = () => setROP(false);
  }

  // Show the live radio track on the speaker display (opt-in, default off).
  // Enabling it makes the box re-buffer (a brief audio dropout) on each text
  // change, so turning it ON asks for confirmation first, and a sub-row lets the
  // user pick what to show: artist, title, or both.
  const dtOn = $('displayTrackOn');
  const dtOff = $('displayTrackOff');
  const dtModeRow = $('displayTrackModeRow');
  const dtModeBtns = { artist: $('displayTrackModeArtist'), title: $('displayTrackModeTitle'), both: $('displayTrackModeBoth') };
  let dtMode = 'both';
  const paintDisplayTrack = (enabled) => {
    if (dtOn) dtOn.classList.toggle('active', enabled === true);
    if (dtOff) dtOff.classList.toggle('active', enabled === false);
    if (dtModeRow) dtModeRow.classList.toggle('hidden', enabled !== true);
  };
  const paintDtMode = () => {
    for (const [m, b] of Object.entries(dtModeBtns)) { if (b) b.classList.toggle('active', m === dtMode); }
  };
  if (dtOn && dtOff) {
    (async () => {
      try {
        const r = await GetDisplayTrack(box.host, box.port);
        if (r && (r.mode === 'artist' || r.mode === 'title' || r.mode === 'both')) dtMode = r.mode;
        paintDisplayTrack(r && r.enabled === true);
        paintDtMode();
      } catch { paintDisplayTrack(false); }
    })();
    const save = async (enabled) => {
      try {
        await SetDisplayTrack(box.host, box.port, enabled, dtMode);
        showToast(t('settingsView.displayTrackSavedToast'));
      } catch (e) { showError(e); }
    };
    dtOn.onclick = async () => {
      // Confirm before enabling: it interrupts the audio on every text change.
      const ok = await confirmWarn(t('settingsView.displayTrackConfirmTitle'), t('settingsView.displayTrackConfirmBody'));
      if (!ok) return;
      paintDisplayTrack(true);
      paintDtMode();
      save(true);
    };
    dtOff.onclick = () => { paintDisplayTrack(false); save(false); };
    for (const [m, b] of Object.entries(dtModeBtns)) {
      if (b) b.onclick = () => { dtMode = m; paintDtMode(); save(true); };
    }
  }

  // Announcements (#125, beta): a quick test field plus a copy-paste curl command
  // for power users (Home Assistant / scripts). The command is built on the Go
  // side so it carries the agent port that actually answers this box.
  const annText = $('announceText');
  const annSend = $('announceSend');
  const annLang = $('announceLang');
  const annCurl = $('announceCurl');
  const annCopy = $('announceCopy');
  const annVol = $('announceVolume');
  const annVolVal = $('announceVolumeVal');
  const annTranslate = $('announceTranslate');
  if (annVol && annVolVal) {
    annVolVal.textContent = annVol.value;
    annVol.oninput = () => { annVolVal.textContent = annVol.value; };
  }
  // Default the TTS voice to the app's UI language when it is one of the offered
  // voices, so e.g. a German user gets a German voice without having to pick.
  if (annLang) {
    const ui = (document.documentElement.lang || '').slice(0, 2);
    if (ui && [...annLang.options].some(o => o.value === ui)) annLang.value = ui;
  }
  if (annCurl) {
    (async () => {
      try { annCurl.textContent = await AnnounceExample(box.host, box.port); } catch { /* leave blank */ }
    })();
  }
  if (annSend) {
    annSend.onclick = async () => {
      const text = ((annText && annText.value) || '').trim();
      if (!text) return;
      annSend.disabled = true;
      try {
        const vol = annVol ? (parseInt(annVol.value, 10) || 0) : 0;
        await SendAnnounce(box.host, box.port, text, (annLang && annLang.value) || '', vol);
        showToast(t('settingsView.announceSentToast'));
      } catch (e) { showError(e); }
      annSend.disabled = false;
    };
  }
  if (annTranslate) {
    // Translate the field text into the selected voice language (keyless Google
    // Translate, app-side), then drop it back in the field so the user can
    // review before sending. Same language picker doubles as the TTS voice.
    annTranslate.onclick = async () => {
      const text = ((annText && annText.value) || '').trim();
      if (!text) return;
      annTranslate.disabled = true;
      try {
        const translated = await Translate(text, (annLang && annLang.value) || 'en');
        if (translated && annText) annText.value = translated;
      } catch (e) { showError(e); }
      annTranslate.disabled = false;
    };
  }
  if (annText) {
    annText.onkeydown = (e) => { if (e.key === 'Enter' && annSend) annSend.click(); };
  }
  if (annCopy && annCurl) {
    annCopy.onclick = async () => {
      try {
        await navigator.clipboard.writeText(annCurl.textContent || '');
        showToast(t('settingsView.announceCopiedToast'));
      } catch { /* clipboard blocked */ }
    };
  }

  // AirPlay optimization (BCO speakers only). GET reports supported +
  // current state; the section stays hidden on non-BCO models. Toggling
  // reboots the speaker to apply it (BoseApp reads BCOResetTimerEnabled
  // at boot, same as the Bose app), so confirm first.
  const aoSection = $('airplayOptSection');
  const aoOn = $('airplayOptOn');
  const aoOff = $('airplayOptOff');
  // Same active-highlight pattern as the clock toggle: the lit button
  // is the state, no separate status line. null = unknown (neither lit).
  const paintAirplay = (enabled) => {
    if (aoOn) aoOn.classList.toggle('active', enabled === true);
    if (aoOff) aoOff.classList.toggle('active', enabled === false);
  };
  if (aoSection && aoOn && aoOff) {
    (async () => {
      try {
        const r = await GetAirplayOpt(box.host, box.port);
        if (r && r.supported) {
          aoSection.classList.remove('hidden');
          paintAirplay(r.enabled === true ? true : (r.enabled === false ? false : null));
        }
      } catch { /* leave the section hidden on error */ }
    })();
    const setAO = async (enabled) => {
      const ok = await confirmWarn(
        t('settingsView.airplayOptConfirmTitle'),
        t('settingsView.airplayOptConfirmBody'),
      );
      if (!ok) return;
      paintAirplay(enabled);
      try {
        await SetAirplayOpt(box.host, box.port, enabled);
        showToast(t('settingsView.airplayOptSavedToast'));
        setTimeout(deps.discoverBoxes, 60000);
      } catch (e) { showError(e); }
    };
    aoOn.onclick = () => setAO(true);
    aoOff.onclick = () => setAO(false);
  }

  // Smart-home / webhook: the remote thumbs keys (up and down are
  // indistinguishable on this firmware, so one shared toggle trigger) fire a
  // user-defined HTTP request the agent persists on the box.
  const whUrl = $('webhookUrl');
  const whMethod = $('webhookMethod');
  const whBody = $('webhookBody');
  const whBodyRow = $('webhookBodyRow');
  const whOn = $('webhookOn');
  const whOff = $('webhookOff');
  const whTest = $('webhookTestBtn');
  const whSave = $('webhookSaveBtn');
  const whTarget = $('webhookTarget');
  const whMode = $('webhookMode');
  const whModeNote = $('webhookModeNote');
  const whType = $('webhookType');
  const whHttpRow = $('webhookHttpRow');
  const whWolRow = $('webhookWolRow');
  const whUdpRow = $('webhookUdpRow');
  const whUdpPayloadRow = $('webhookUdpPayloadRow');
  const whMac = $('webhookMac');
  const whUdpHost = $('webhookUdpHost');
  const whUdpPort = $('webhookUdpPort');
  const whUdpEnc = $('webhookUdpEnc');
  const whUdpPayload = $('webhookUdpPayload');
  if (whUrl && whOn && whOff && whSave && whTarget) {
    let whEnabled = false;
    // Full config held locally; each target's edits are captured into it before
    // switching target or saving, then the WHOLE config is PUT (a partial PUT
    // would wipe the other keys). buttons keys: preset1..preset6, aux, power.
    let cfg = { thumb: {}, buttons: {} };
    let prevTarget = whTarget.value || 'thumb';
    const presetIds = new Set(['preset1', 'preset2', 'preset3', 'preset4', 'preset5', 'preset6']);
    const isModeTarget = (tg) => presetIds.has(tg); // only presets support replace
    const actionOf = (tg) => tg === 'thumb' ? (cfg.thumb || {}) : ((cfg.buttons && cfg.buttons[tg]) || {});
    const paintWh = (en) => {
      whEnabled = en === true;
      whOn.classList.toggle('active', whEnabled === true);
      whOff.classList.toggle('active', whEnabled === false);
    };
    const syncBodyRow = () => {
      // A request body is only sent for non-GET methods.
      if (whBodyRow) whBodyRow.style.display = (whMethod.value === 'GET') ? 'none' : '';
    };
    // Show only the fields the selected transport needs (http | wol | udp). The
    // Test button works for all three (it fires the actual packet/request).
    const syncType = () => {
      const ty = (whType && whType.value) || 'http';
      if (whHttpRow) whHttpRow.classList.toggle('hidden', ty !== 'http');
      if (whBodyRow) whBodyRow.classList.toggle('hidden', ty !== 'http');
      if (whWolRow) whWolRow.classList.toggle('hidden', ty !== 'wol');
      if (whUdpRow) whUdpRow.classList.toggle('hidden', ty !== 'udp');
      if (whUdpPayloadRow) whUdpPayloadRow.classList.toggle('hidden', ty !== 'udp');
      if (ty === 'http') syncBodyRow();
    };
    const loadInto = (tg) => {
      const a = actionOf(tg);
      if (whType) whType.value = a.type || 'http';
      whUrl.value = a.url || '';
      whMethod.value = a.method || 'GET';
      whBody.value = a.body || '';
      if (whMac) whMac.value = a.mac || '';
      if (whUdpHost) whUdpHost.value = a.host || '';
      if (whUdpPort) whUdpPort.value = a.port ? String(a.port) : '';
      if (whUdpEnc) whUdpEnc.value = a.payload_enc || 'text';
      if (whUdpPayload) whUdpPayload.value = a.payload || '';
      paintWh(a.enabled === true);
      whMode.value = a.mode === 'replace' ? 'replace' : 'additional';
      whMode.style.display = isModeTarget(tg) ? '' : 'none';
      whModeNote.textContent = isModeTarget(tg)
        ? t('settingsView.webhookModePresetNote')
        : (tg === 'thumb' ? '' : t('settingsView.webhookModeAuxPowerNote'));
      syncType();
    };
    const captureInto = (tg) => {
      const ty = (whType && whType.value) || 'http';
      const a = { enabled: whEnabled === true, type: ty };
      if (ty === 'wol') {
        a.mac = (whMac && whMac.value.trim()) || '';
      } else if (ty === 'udp') {
        a.host = (whUdpHost && whUdpHost.value.trim()) || '';
        a.port = parseInt((whUdpPort && whUdpPort.value.trim()) || '0', 10) || 0;
        a.payload = (whUdpPayload && whUdpPayload.value) || '';
        a.payload_enc = (whUdpEnc && whUdpEnc.value) || 'text';
      } else {
        a.method = whMethod.value;
        a.url = whUrl.value.trim();
        a.body = whBody.value.trim();
        a.content_type = '';
      }
      if (tg === 'thumb') {
        cfg.thumb = a;
      } else {
        if (!cfg.buttons) cfg.buttons = {};
        if (isModeTarget(tg)) a.mode = whMode.value;
        cfg.buttons[tg] = a;
      }
    };
    (async () => {
      try {
        const w = await GetWebhooks(box.host, box.port);
        cfg = { thumb: (w && w.thumb) || {}, buttons: (w && w.buttons) || {} };
      } catch { cfg = { thumb: {}, buttons: {} }; }
      loadInto(prevTarget);
    })();
    whTarget.onchange = () => { captureInto(prevTarget); prevTarget = whTarget.value; loadInto(whTarget.value); };
    whOn.onclick = () => paintWh(true);
    whOff.onclick = () => paintWh(false);
    whMethod.onchange = syncBodyRow;
    if (whType) whType.onchange = syncType;
    whSave.onclick = async () => {
      const tg = whTarget.value;
      const ty = (whType && whType.value) || 'http';
      if (whEnabled) {
        if (ty === 'http' && !whUrl.value.trim()) { showError(t('settingsView.webhookUrlRequired')); return; }
        if (ty === 'wol' && !(whMac && whMac.value.trim())) { showError(t('settingsView.webhookMacRequired')); return; }
        if (ty === 'udp' && (!(whUdpHost && whUdpHost.value.trim()) || !(whUdpPort && parseInt(whUdpPort.value, 10) > 0))) { showError(t('settingsView.webhookUdpRequired')); return; }
      }
      captureInto(tg);
      try {
        await SaveWebhookConfig(box.host, box.port, cfg);
        showToast(t('settingsView.webhookSavedToast'));
      } catch (e) { showError(e); }
    };
    whTest.onclick = async () => {
      // Build the action by type and fire it once, the same for http/udp/wol.
      const ty = (whType && whType.value) || 'http';
      const a = { enabled: true, type: ty };
      if (ty === 'wol') {
        if (!(whMac && whMac.value.trim())) { showError(t('settingsView.webhookMacRequired')); return; }
        a.mac = whMac.value.trim();
      } else if (ty === 'udp') {
        if (!(whUdpHost && whUdpHost.value.trim()) || !(whUdpPort && parseInt(whUdpPort.value, 10) > 0)) { showError(t('settingsView.webhookUdpRequired')); return; }
        a.host = whUdpHost.value.trim();
        a.port = parseInt(whUdpPort.value, 10);
        a.payload = (whUdpPayload && whUdpPayload.value) || '';
        a.payload_enc = (whUdpEnc && whUdpEnc.value) || 'text';
      } else {
        if (!whUrl.value.trim()) { showError(t('settingsView.webhookUrlRequired')); return; }
        a.method = whMethod.value;
        a.url = whUrl.value.trim();
        a.body = whBody.value.trim();
        a.content_type = '';
      }
      whTest.disabled = true;
      try {
        const r = await TestWebhookAction(box.host, box.port, JSON.stringify(a));
        showToast(t('settingsView.webhookTestOk', { status: (r && r.status) || '' }));
      } catch (e) {
        showError(t('settingsView.webhookTestFailed', { err: String(e) }));
      } finally {
        whTest.disabled = false;
      }
    };
  }

  // Box-to-box preset copy (Expert): copy keys 1-6 from this speaker (the
  // settings source) onto a chosen target speaker, behind a warning confirm
  // because it overwrites the target's presets.
  const copyBtn = $('copyPresetBtn');
  if (copyBtn) {
    copyBtn.onclick = async () => {
      const sel = $('copyPresetTarget');
      if (!sel || !sel.value) return;
      // "Apply to all": copy this box's 1-6 onto every other STR speaker.
      if (sel.value === '__ALL__') {
        const all = (state.boxes || []).filter(b => b.kind !== 'stock' && b.host !== box.host);
        const ok = await confirmWarn(
          t('settingsView.copyPresetsConfirmTitle'),
          t('settingsView.copyPresetsConfirmBody', { target: t('settingsView.copyPresetsAllTargets') })
        );
        if (!ok) return;
        copyBtn.disabled = true;
        let done = 0;
        const failed = [];
        for (const tb of all) {
          try {
            await CopyPresetsAcrossBoxes(box.host, box.port, tb.host, tb.port || 0);
            done++;
            if (state.currentBox && state.currentBox.host === tb.host) await deps.loadPresets();
          } catch { failed.push(tb.friendlyName || tb.name || tb.host); }
        }
        copyBtn.disabled = false;
        if (failed.length) showError(t('settingsView.copyPresetsAllPartial', { done, total: all.length, failed: failed.join(', ') }));
        else showToast(t('settingsView.copyPresetsAllDone', { n: all.length }));
        return;
      }
      const [thost, tportRaw] = sel.value.split('|');
      const tport = parseInt(tportRaw, 10) || 0;
      const target = (state.boxes || []).find(b => b.host === thost);
      const targetName = target ? (target.friendlyName || target.name || target.host) : thost;
      const ok = await confirmWarn(
        t('settingsView.copyPresetsConfirmTitle'),
        t('settingsView.copyPresetsConfirmBody', { target: targetName })
      );
      if (!ok) return;
      copyBtn.disabled = true;
      try {
        const n = await CopyPresetsAcrossBoxes(box.host, box.port, thost, tport);
        showToast(t('settingsView.copyPresetsDone', { n, target: targetName }));
        if (state.currentBox && state.currentBox.host === thost) await deps.loadPresets();
      } catch (e) { showError(e); }
      copyBtn.disabled = false;
    };
  }

  // Expert: put a custom audio URL on a preset. Stored as a normal radio preset,
  // so the box plays the URL over UPnP when the key (hardware or app) is pressed,
  // using STR's robust play path (DIDL metadata, HTTP handling) rather than a raw
  // UPnP push. Replaces the current source on press (no auto-resume).
  const urlPresetBtn = $('urlPresetSaveBtn');
  if (urlPresetBtn) {
    urlPresetBtn.onclick = async () => {
      const slot = parseInt(($('urlPresetSlot') || {}).value, 10);
      const url = (($('urlPresetUrl') || {}).value || '').trim();
      const name = (($('urlPresetName') || {}).value || '').trim() || t('settingsView.urlPresetDefaultName');
      if (!slot || !/^https?:\/\/\S+/i.test(url)) { showError(t('settingsView.urlPresetInvalid')); return; }
      urlPresetBtn.disabled = true;
      try {
        await SetPreset(box.host, box.port, slot, name, url, '', 0, '');
        showToast(t('settingsView.urlPresetSaved', { n: slot }));
        if (state.currentBox && state.currentBox.host === box.host) await deps.loadPresets();
      } catch (e) { showError(e); }
      urlPresetBtn.disabled = false;
    };
  }

  // App Region dropdown fuellen + aktuelle Region selektieren
  const regSel = $('appRegionSelect');
  if (regSel) {
    regSel.innerHTML = COUNTRIES.filter(c => c.cc).map(c =>
      `<option value="${c.cc}">${optFlag(c.cc)}${escapeHtml(c.name)}</option>`
    ).join('');
    const updateCurrentDisplay = (cc) => {
      const el = $('currentAppRegion');
      if (!el) return;
      const c = COUNTRIES.find(x => x.cc === cc);
      el.innerHTML = c
        ? `${optFlag(cc)}${escapeHtml(c.name)} (${escapeHtml(cc)})`
        : escapeHtml(cc || t('common.unknown'));
    };
    deps.boxFetch(box, '/api/region').then(r => r.ok ? r.json() : null).then(data => {
      if (data && data.country) {
        regSel.value = data.country;
        updateCurrentDisplay(data.country);
      } else {
        const el = $('currentAppRegion'); if (el) el.textContent = t('settingsView.langUnreachable');
      }
    }).catch(() => { const el = $('currentAppRegion'); if (el) el.textContent = t('settingsView.langUnreachable'); });
    $('appRegionSave').onclick = async () => {
      const cc = regSel.value;
      try {
        const r = await deps.boxFetch(box, '/api/region', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ country: cc }),
        });
        if (!r.ok) throw new Error('HTTP ' + r.status);
        const data = await r.json();
        state.searchCountry = data.country;
        state.searchLang = data.language;
        saveSearchCountry(state.searchCountry);
        const cs = $('searchCountry');
        if (cs) cs.value = data.country;
        deps.updateFilterIndicators();
        updateCurrentDisplay(data.country);
        showToast(t('settingsView.regionSavedToast', { name: (COUNTRIES.find(c => c.cc === cc) || {}).name || cc }));
      } catch (e) { showError(e); }
    };
  }

  // Wire handlers. All operations explicitly target settingsBox
  // (not currentBox).
  $('boxNameSave').onclick = async () => {
    const desired = $('boxNameInput').value.trim();
    if (!desired) return;
    const finalName = ensureWithUID(desired, box);
    try {
      await SetBoxName(box.host, box.port, finalName);
      showToast(t('settingsView.speakerNameSavedToast', { name: finalName }));
      $('boxNameInput').value = finalName;
      // Update locally: both the selected speaker and the matching
      // entry in the global speakers list so every dropdown is
      // consistent immediately. NO discoverBoxes or loadBoxSettings
      // afterwards because that would flicker and could overwrite a
      // slider input the user just made.
      box.friendlyName = finalName;
      const idx = state.boxes.findIndex(b => b.deviceID === box.deviceID);
      if (idx >= 0) state.boxes[idx] = { ...state.boxes[idx], friendlyName: finalName };
      // 90 s pending name: survives an mDNS refresh during which
      // the stick still announces the old name.
      state.pendingNames[box.deviceID] = { name: finalName, until: Date.now() + 90000 };
      renderSettingsBoxSelect();
      deps.renderBoxSelect();
    } catch (e) { showError(e); }
  };
  $('boxVolume').oninput = () => {
    $('boxVolumeVal').textContent = $('boxVolume').value;
    throttledSetVolume(box.host, box.port, parseInt($('boxVolume').value, 10));
  };
  $('boxVolume').onchange = () => {
    throttledSetVolume(box.host, box.port, parseInt($('boxVolume').value, 10));
  };
  $('boxBass').oninput = () => {
    $('boxBassVal').textContent = formatRel($('boxBass').value);
    const rel = parseInt($('boxBass').value, 10);
    throttledSetBass(box.host, box.port, rel + (bass.default || 0));
  };
  $('boxBass').onchange = () => {
    const rel = parseInt($('boxBass').value, 10);
    throttledSetBass(box.host, box.port, rel + (bass.default || 0));
  };
  $('boxBassReset').onclick = async () => {
    $('boxBass').value = 0;
    $('boxBassVal').textContent = formatRel(0);
    try {
      await SetBoxBass(box.host, box.port, bass.default || 0);
    } catch (e) { showError(e); }
  };
}

// wireWlanSwitch wires the WLAN switch UI in the Settings tab.
// Lists PC-known WLANs in a dropdown (no manual typing), prefills
// the password, and on Save sends PUT /api/box/wlan.
function wireWlanSwitch(box) {
  const toggle = $('wlanSwitchToggle');
  const form = $('wlanSwitchForm');
  if (!toggle || !form) return;
  toggle.onclick = () => {
    form.classList.toggle('hidden');
    if (!form.classList.contains('hidden')) {
      loadBoxWlanList();
    }
  };
  async function loadBoxWlanList() {
    const sel = $('boxWlanSelect');
    const rb = $('boxWlanRefresh');
    if (rb) rb.classList.add('spinning'); // visible feedback: the button spins while loading
    try {
      const profiles = await ListWiFiProfiles() || [];
      sel.innerHTML = `<option value="">${escapeHtml(t('settingsView.wlanPickPlaceholder'))}</option>` +
        profiles.map(p => `<option value="${escapeAttr(p.ssid)}">${escapeHtml(p.ssid)}</option>`).join('');
      showToast(t('settingsView.wlanListRefreshed', { n: profiles.length }));
    } catch {
      sel.innerHTML = `<option value="">${escapeHtml(t('setup.wlanListUnavailable'))}</option>`;
    } finally {
      if (rb) rb.classList.remove('spinning');
    }
  }
  $('boxWlanRefresh').onclick = loadBoxWlanList;
  $('boxWlanSelect').onchange = async () => {
    const v = $('boxWlanSelect').value;
    if (!v) return;
    $('boxWlanSSID').value = v;
    if (isMacOS) return; // #88: don't trigger System-keychain admin prompt
    try {
      const pw = await TryWiFiPassword(v);
      if (pw) $('boxWlanPass').value = pw;
    } catch {}
  };
  $('boxWlanShowPass').onclick = () => {
    const i = $('boxWlanPass');
    const b = $('boxWlanShowPass');
    if (i.type === 'password') { i.type = 'text'; b.innerHTML = '&#128064;'; }
    else { i.type = 'password'; b.innerHTML = '&#128065;'; }
  };
  $('boxWlanSave').onclick = async () => {
    const ssid = $('boxWlanSSID').value.trim();
    const pass = $('boxWlanPass').value;
    if (!ssid) { showError(t('settingsView.wlanSsidEmpty')); return; }
    const ok = await confirmWarn(
      t('settingsView.wlanSwitchConfirmTitle'),
      t('settingsView.wlanConfirmBody', { ssid: escapeHtml(ssid) })
    );
    if (!ok) return;
    try {
      const r = await deps.boxFetch(box, '/api/box/wlan', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ssid, password: pass }),
      });
      if (!r.ok) {
        const body = await r.text();
        throw new Error('HTTP ' + r.status + ': ' + body);
      }
      // The agent applies the switch in the background and the box leaves the
      // current network, so we rediscover it rather than wait. BCO speakers
      // (Portable) reboot to apply, which takes a few minutes; wpa speakers
      // switch live and fall back to their old network if the new one fails.
      let info = {};
      try { info = await r.json(); } catch {}
      $('boxWlanPass').value = '';
      form.classList.add('hidden');
      showToast(
        info.mechanism === 'bco'
          ? t('settingsView.wlanRebootingToast')
          : t('settingsView.wlanSwitchedToast'),
        7000,
      );
      // The speaker gets a new IP (or reboots). Retrigger discovery; a longer
      // delay for the BCO reboot so the rediscover lands after it is back up.
      setTimeout(deps.discoverBoxes, info.mechanism === 'bco' ? 90000 : 12000);
    } catch (e) { showError(e); }
  };
}

// wireCombobox binds to an <input> + <button toggle> + <ul list>
// trio. Filters while typing, opens on toggle click, selects on
// item click.
export function wireCombobox(inputId, toggleId, listId, options) {
  const input = document.getElementById(inputId);
  const toggle = document.getElementById(toggleId);
  const list = document.getElementById(listId);
  if (!input || !toggle || !list) return;

  function render(filter) {
    const q = (filter || '').toLowerCase().trim();
    const matches = options.filter(o => !q || o.toLowerCase().includes(q));
    if (matches.length === 0) {
      list.innerHTML = `<li class="combo-empty">${escapeHtml(t('combobox.noSuggestions'))}</li>`;
      return;
    }
    list.innerHTML = matches.map(o => `<li data-value="${escapeAttr(o)}">${escapeHtml(o)}</li>`).join('');
    list.querySelectorAll('li[data-value]').forEach(li => {
      li.onmousedown = (e) => {
        // Use mousedown rather than click so the handler fires
        // before the input loses focus.
        e.preventDefault();
        input.value = li.dataset.value;
        list.classList.add('hidden');
      };
    });
  }

  // When opening the dropdown show ALL options rather than
  // filtering by the current input. Otherwise the suggestions are
  // gone whenever the current name does not match a room (e.g.
  // "Bose SoundTouch 02FFD8"). Filtering only kicks in on typing.
  let userIsTyping = false;
  const showAll = () => { render(''); list.classList.remove('hidden'); };
  const showFiltered = () => { render(input.value); list.classList.remove('hidden'); };
  const hide = () => list.classList.add('hidden');

  input.addEventListener('focus', () => {
    if (userIsTyping) showFiltered(); else showAll();
  });
  input.addEventListener('input', () => {
    userIsTyping = true;
    showFiltered();
  });
  input.addEventListener('blur', () => {
    setTimeout(hide, 150);
    userIsTyping = false;
  });
  toggle.addEventListener('mousedown', (e) => {
    e.preventDefault();
    if (list.classList.contains('hidden')) {
      input.focus();
      showAll();
    } else {
      hide();
    }
  });
}

// formatRel renders a signed relative value: 0, +3, -2.
function formatRel(v) {
  const n = parseInt(v, 10);
  if (isNaN(n) || n === 0) return '0';
  return n > 0 ? '+' + n : String(n);
}

// Last known firmware major.minor.patch per SoundTouch model. Bose
// shipped the final wave in 2022; nothing more arrived after the
// cloud shutdown. Source: support.bose.com.
const LATEST_FW = {
  'SoundTouch 10': '27.0.6',
  'SoundTouch 20': '27.0.6',
  'SoundTouch 30': '27.0.6',
  'SoundTouch Portable': '27.0.6',
};

// Bose's own SoundTouch firmware-update support article. The update steps are
// the same across the 10/20/30/Portable, so one link serves every model; swap
// in a per-model article later if needed. Shown as a clickable link in the
// outdated-firmware banner (Jens, 2026-06-27: link the Bose support article).
const BOSE_FW_SUPPORT_URL = 'https://support.bose.com/s/article/soundtouch-20-iii-updating-the-software-or-firmware-of-your-product?language=en_US';
// The Bose USB firmware download directory referenced in step 4, made clickable
// so the user does not have to retype it.
const BOSE_FW_USB_URL = 'https://downloads.bose.com/ced/soundtouch/soundtouch_usb/';

// fwVersionTuple extracts the first 3 numbers from
// "27.0.6.46330.5043500" for comparison. Returns null on an unknown
// format.
function fwVersionTuple(v) {
  if (!v) return null;
  const m = v.match(/^(\d+)\.(\d+)\.(\d+)/);
  if (!m) return null;
  return [parseInt(m[1]), parseInt(m[2]), parseInt(m[3])];
}

function isFwOutdated(info) {
  const want = LATEST_FW[info.type || ''];
  const have = fwVersionTuple(info.version || '');
  const wantT = fwVersionTuple(want || '');
  if (!have || !wantT) return false;
  for (let i = 0; i < 3; i++) {
    if (have[i] < wantT[i]) return true;
    if (have[i] > wantT[i]) return false;
  }
  return false;
}

// fwStatusInline renders the firmware row.
//   Up to date: green text + checkmark
//   Outdated:  red text + an Update button next to it
function fwStatusInline(info) {
  const v = escapeHtml(info.version || '-');
  if (!info.version) return v;
  if (isFwOutdated(info)) {
    return `<span class="fw-old">${v}</span> <button class="btn btn-mini btn-danger" id="fwUpdateBtn">${escapeHtml(t('fw.updateBtn'))}</button>`;
  }
  return `<span class="fw-ok">&#10003; ${v}</span>`;
}

function fwUpdateHint(info) {
  const want = LATEST_FW[info.type || ''];
  if (!isFwOutdated(info)) {
    return `<small class="muted small">${escapeHtml(t('fw.uptodate', { version: want || '27.0.6' }))}</small>`;
  }
  return `
    <div class="fw-update-banner" id="fwUpdateBanner">
      <b>${escapeHtml(t('fw.outdatedTitle'))}</b>
      <div>${t('fw.outdatedIntro', { version: `<b>${escapeHtml(want || '27.0.6')}</b>` })}</div>
      <div class="fw-update-howto">
        <b>${escapeHtml(t('fw.howToHeader'))}</b>
        <ol>
          <li>${escapeHtml(t('fw.step1'))}</li>
          <li>${escapeHtml(t('fw.step2'))}</li>
          <li>${escapeHtml(t('fw.step3'))}</li>
          <li>${t('fw.step4')} <a href="#" class="link" id="fwUsbLink" data-url="${escapeHtml(BOSE_FW_USB_URL)}">downloads.bose.com/ced/soundtouch/soundtouch_usb/</a></li>
        </ol>
        <p><a href="#" class="btn btn-mini" id="fwGuideLink" data-url="${escapeHtml(BOSE_FW_SUPPORT_URL)}">${escapeHtml(t('fw.boseGuideLink'))}</a></p>
        <small class="muted small">${escapeHtml(t('fw.hint'))}</small>
      </div>
    </div>`;
}

// Source labels and hints are resolved via the i18n bundle. Keys are
// `source.label.<UPPER>` and `source.hint.<UPPER>`. Falls back to the
// raw API enum value when no translation is registered.
function sourceLabel(key) {
  return tLookup('source.label', key) || key;
}
function sourceHint(key) {
  return tLookup('source.hint', key) || '';
}

// rollupSources removes NOTIFICATION (internal), collapses multiple
// entries of the same source type into a single pill and prefers
// READY over UNAVAILABLE. The result is then sorted with READY
// first.
function rollupSources(raw) {
  const grouped = {};
  for (const src of raw) {
    if (!src || !src.source) continue;
    if (src.source === 'NOTIFICATION') continue;
    // Alexa on the SoundTouch ran entirely through the Bose/Amazon cloud, which
    // is gone, so the box still advertises ALEXA as READY but it cannot work.
    // Showing it as "active" only confuses users (Discussion #170), so hide the
    // dead source like NOTIFICATION rather than imply it is usable.
    if (src.source === 'ALEXA') continue;
    const existing = grouped[src.source];
    if (!existing || (src.status === 'READY' && existing.status !== 'READY')) {
      grouped[src.source] = src;
    }
  }
  return Object.values(grouped).sort((a, b) => {
    if (a.status === 'READY' && b.status !== 'READY') return -1;
    if (a.status !== 'READY' && b.status === 'READY') return 1;
    return sourceLabel(a.source).localeCompare(sourceLabel(b.source));
  });
}

// Volume update plumbing. The slider has to behave like the
// hardware: the level changes WHILE the user drags, not only on
// release. A naive "fire on every input event" floods the box with
// PUTs (60+ events per drag-second), each of which holds a request
// slot on the Bose firmware's tiny HTTP server and blocks the next.
// Symptom on a stock-Bose-on-:17008 box: every PUT times out at the
// 6 s httpClient cap and the user sees a wall of red error toasts.
//
// makeOneInFlightThrottle gives us "fire the first immediately,
// drop intermediate calls, replay the most recent args after the
// current call finishes". Net effect during a 1 s drag: 3 to 5
// actual PUTs with the very last value being whatever the user
// released on, regardless of how fast they wiped.
function makeOneInFlightThrottle(fn) {
  let inFlight = false;
  let pending = null;
  const fire = async (args) => {
    inFlight = true;
    try {
      await fn(...args);
    } catch (e) {
      // Quiet during drag: showError would stack-up toasts. The
      // periodic status poll recovers the visible value anyway.
      console.warn('throttled box update failed', e);
    } finally {
      inFlight = false;
      if (pending) {
        const next = pending;
        pending = null;
        fire(next);
      }
    }
  };
  return (...args) => {
    if (inFlight) { pending = args; return; }
    fire(args);
  };
}
// throttledSetVolume/throttledSetBass are the app-wide volume plumbing: the
// settings sliders use them here, and the music view imports them from this
// module (the volume controls there share the exact same throttle). Exported so
// main.js's music view can reuse the single shared instance.
export const throttledSetVolume = makeOneInFlightThrottle(SetBoxVolume);
export const throttledSetBass = makeOneInFlightThrottle(SetBoxBass);

const debouncedSetVolume = debounce(async (box) => {
  try {
    await SetBoxVolume(box.host, box.port, parseInt($('boxVolume').value, 10));
  } catch (e) { showError(e); }
}, 200);
const debouncedSetBass = debounce(async (box, defaultBass) => {
  try {
    // The slider holds a relative value (0 = factory default). The
    // speaker expects the absolute value, so add the default offset
    // back in.
    const rel = parseInt($('boxBass').value, 10);
    await SetBoxBass(box.host, box.port, rel + (defaultBass || 0));
  } catch (e) { showError(e); }
}, 200);
