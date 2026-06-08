import './style.css';
import {
  DiscoverBoxes,
  GetPresets,
  SetPreset,
  DeletePreset,
  PlaySlot,
  PlayURL,
  VoteStation,
  RebootBox,
  SyncBoxPresets,
  CopyPresetsAcrossBoxes,
  GetBoxFirmware,
  BoxInstallReachable,
  Pause,
  Stop,
  Status,
  ListDrives,
  WriteStickFiles,
  FormatStick,
  StickVersion,
  CheckStick,
  StickConfigs,
  AppInfo,
  EjectDrive,
  BoxAgentVersion,
  UpdateBoxAgent,
  WriteWLANConfig,
  WriteRegionConfig,
  WriteNameConfig,
  WriteLangConfig,
  SetAppLocale,
  SuggestBoxLanguage,
  ListWiFiProfiles,
  TryWiFiPassword,
  CurrentWiFi,
  CheckAppUpdate,
  ResolveStationLogo,
  BoxSettings,
  SetBoxName,
  SetBoxVolume,
  SetBoxBass,
  SelectBoxSource,
  GetClockDisplay,
  SetClockDisplay,
  GetClockFormat24,
  GetBoxLanguage,
  SetBoxLanguage,
  GetAirplayOpt,
  SetAirplayOpt,
  GetWebhooks,
  SetWebhooks,
  TestWebhook,
  StreamBitrate,
  StreamTitle,
  SpotifyBitrate,
  SpotifyNowPlaying,
  SaveSpotifyPreset,
  SaveLibraryPreset,
  SaveDiagnosticBundle,
  GetLogFilePath,
  InstallSTROnBox,
  TrueFactoryReset,
  UninstallSTR,
  ProbeSetupAP,
  PushWLANToBox,
  ListMediaServers,
  BrowseLibrary,
  LogClientError,
  BrowserOpenURL,
  EventsOn,
  GetZoneState,
  FormZone,
  DissolveZone,
} from './api.js';

// Global frontend crash capture, registered as early as possible.
// A JavaScript error during startup does not reach str.log on its own,
// so a "flashes up and quits" leaves nothing to diagnose. Forward any
// uncaught error or rejected promise to the Go logger. Best-effort:
// the handlers never throw themselves.
(function installClientErrorHooks() {
  const report = (kind, detail) => {
    try { LogClientError(`${kind}: ${detail}`); } catch {}
    try { console.error(kind, detail); } catch {}
  };
  try {
    window.addEventListener('error', (e) => {
      const stack = e && e.error && e.error.stack ? '\n' + e.error.stack : '';
      report('window.onerror', `${(e && e.message) || ''} @ ${(e && e.filename) || ''}:${(e && e.lineno) || ''}${stack}`);
    });
    window.addEventListener('unhandledrejection', (e) => {
      const r = e && e.reason;
      report('unhandledrejection', (r && r.stack) ? r.stack : String(r));
    });
  } catch {}
})();

import {
  state,
  loadLastBox,
  saveLastBox,
  loadCachedBoxes,
  saveCachedBoxes,
  saveSearchCountry,
} from './state.js';

import {
  $,
  escapeHtml,
  escapeAttr,
  decodeXmlEntities,
  formatNumber,
  debounce,
  sleep,
  formatRemaining,
  confirmWarn,
  closeWarn,
  showError,
  showToast,
} from './utils.js';

import {
  COUNTRIES,
  ORDERS,
  GENRE_CORE,
  GENRE_BY_COUNTRY,
  translateCountry,
  canonGenre,
  translateGenre,
  translateTags,
  flagFromCC,
  flagSvg,
  optFlag,
} from './localization.js';

import {
  t,
  tLookup,
  getLocale,
  setLocale,
  AVAILABLE_LOCALES,
} from './i18n/index.js';

// LOCALE_FLAG_CC maps i18n locale codes to ISO-3166 alpha-2 country
// codes for flag emoji rendering. The "language flag" mapping is a UX
// convention: English uses the Union Jack rather than US for global
// audiences. Add new entries here when registering a new bundle.
const LOCALE_FLAG_CC = {
  en: 'GB',
  de: 'DE',
  fr: 'FR',
  es: 'ES',
  ja: 'JP',
  uk: 'UA',
};

// LOCALE_TO_RADIO_LANG maps the app UI locale to radio-browser's English
// language name, so the radio language filter can default to the chosen app
// language (e.g. a Dutch UI defaults the filter to Dutch stations) rather
// than to the stick region's language or a last-used value.
const LOCALE_TO_RADIO_LANG = {
  en: 'english',
  de: 'german',
  fr: 'french',
  es: 'spanish',
  ja: 'japanese',
  uk: 'ukrainian',
  nl: 'dutch',
  pl: 'polish',
  lt: 'lithuanian',
  lv: 'latvian',
  tr: 'turkish',
};

import {
  extractHost,
  rootDomain,
  iconServicesFor,
  stationLogoCandidates,
  logoImgTag,
  bestLogoForStation,
  stationLogoChain,
  monogramDataUri,
} from './logos.js';

// __nextLogoFallback walks a preset logo <img>'s data-fallbacks list (a
// pipe-separated set of candidate URLs) on each load error, swapping in the
// next candidate. The list always ends in a locally generated monogram data
// URI, which always loads, so a station whose favicon is missing or fails to
// load shows a clean letter tile instead of a broken-image icon (Brecht: VRT
// stations showed broken icons because this handler was referenced in onerror
// but never defined, so the cascade threw and the broken image stuck).
window.__nextLogoFallback = function (img) {
  try {
    const fb = (img.getAttribute('data-fallbacks') || '').split('|').filter(Boolean);
    if (fb.length) {
      const next = fb.shift();
      img.setAttribute('data-fallbacks', fb.join('|'));
      img.src = next;
      return;
    }
  } catch {}
  // Chain exhausted (or attribute unreadable): stop so onerror cannot loop.
  img.onerror = null;
};

// ---------- Station logo hydration ----------
// logoImgTag renders a tile with the local monogram as the immediate
// src. Here we upgrade each such tile to a real logo asynchronously:
// the Go backend (ResolveStationLogo) validates the station's own HTTPS
// favicon and then DuckDuckGo by HTTP status, returning a real URL or ""
// (keep the monogram). Resolution runs in Go because DuckDuckGo serves
// its "no icon" 404 as a grey chevron that the webview would otherwise
// display. A MutationObserver catches every tile any view renders, so no
// render site needs to call this explicitly. Results are cached in Go.
function hydrateLogo(img) {
  if (!img || img.dataset.logoResolved) return;
  img.dataset.logoResolved = '1';
  const hosts = (img.dataset.logoHosts || '').split('|').filter(Boolean);
  const fav = img.dataset.logoFav || '';
  const brand = img.dataset.logoBrand || '';
  if (!fav && !brand && hosts.length === 0) return; // nothing to resolve, monogram stays
  ResolveStationLogo(fav, brand, hosts).then((url) => {
    if (typeof url === 'string' && url) {
      const mono = img.dataset.logoMono || img.src;
      img.onerror = () => { img.onerror = null; img.src = mono; };
      img.src = url;
    }
  }).catch(() => {});
}

(function setupLogoHydration() {
  const scan = (root) => {
    if (root.nodeType !== 1) return;
    if (root.matches && root.matches('img[data-logo-hosts]')) hydrateLogo(root);
    if (root.querySelectorAll) root.querySelectorAll('img[data-logo-hosts]').forEach(hydrateLogo);
  };
  const obs = new MutationObserver((muts) => {
    for (const m of muts) for (const n of m.addedNodes) scan(n);
  });
  obs.observe(document.body, { childList: true, subtree: true });
  // Catch any tiles already present before the observer attached.
  scan(document.body);
})();

// ---------- DOM Skeleton ----------

document.querySelector('#app').innerHTML = `
  <header class="app-header">
    <div class="app-header-row">
      <span class="app-logo" aria-hidden="true">
        <svg viewBox="0 0 64 64" fill="none">
          <g fill="currentColor">
            <circle cx="22" cy="14" r="3"/><circle cx="32" cy="14" r="3"/><circle cx="42" cy="14" r="3"/>
            <circle cx="22" cy="26" r="3"/><circle cx="32" cy="26" r="3"/><circle cx="42" cy="26" r="3"/>
          </g>
          <path d="M 4 44 L 22 44" stroke="currentColor" stroke-width="3" stroke-linecap="round"/>
          <path d="M 22 44 L 26 48 L 30 32 L 34 54 L 38 40 L 42 44" stroke="#cc0000" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"/>
          <path d="M 42 44 L 60 44" stroke="currentColor" stroke-width="3" stroke-linecap="round"/>
        </svg>
      </span>
      <div class="app-brand">ST <span class="app-brand-accent">Reborn</span></div>
      <div class="app-locale locale-dd" role="group" aria-label="${escapeAttr(t('settings.language'))}">
        ${(() => {
          const cur = AVAILABLE_LOCALES.find(l => l.code === getLocale()) || AVAILABLE_LOCALES[0];
          const curCc = LOCALE_FLAG_CC[cur.code] || cur.code.toUpperCase();
          const trigger = `<button type="button" class="locale-dd-trigger" id="localeTrigger" aria-haspopup="listbox" aria-expanded="false" title="${escapeAttr(cur.label)}"><span class="locale-flag-emoji" aria-hidden="true">${flagSvg(curCc) || flagFromCC(curCc)}</span><span class="locale-flag-code">${escapeHtml(cur.code.toUpperCase())}</span><span class="locale-dd-caret" aria-hidden="true">&#9662;</span></button>`;
          const items = AVAILABLE_LOCALES.map(l => {
            const cc = LOCALE_FLAG_CC[l.code] || l.code.toUpperCase();
            const sel = l.code === getLocale();
            return `<li role="option" class="locale-dd-item${sel ? ' active' : ''}" data-locale="${escapeAttr(l.code)}" aria-selected="${sel ? 'true' : 'false'}"><span class="locale-flag-emoji" aria-hidden="true">${flagSvg(cc) || flagFromCC(cc)}</span><span class="locale-dd-name">${escapeHtml(l.label)}</span></li>`;
          }).join('');
          return trigger + `<ul class="locale-dd-menu" id="localeMenu" role="listbox" hidden>${items}</ul>`;
        })()}
      </div>
    </div>
    <div class="app-tagline" id="appTagline"></div>
    <div class="app-supported" id="appSupported"></div>
  </header>
  <div class="tabs">
    <button class="tab-btn active" data-view="box">${escapeHtml(t('nav.music'))}</button>
    <button class="tab-btn" data-view="library">${escapeHtml(t('nav.library'))}</button>
    <button class="tab-btn" data-view="settings">${escapeHtml(t('nav.speakerSettings'))}</button>
    <button class="tab-btn" data-view="setup">${escapeHtml(t('nav.setupStick'))}</button>
    <button class="tab-btn" data-view="multiroom">${escapeHtml(t('nav.multiroom'))}<span class="beta-pill alpha-pill">${escapeHtml(t('common.alpha'))}</span></button>
    <button class="tab-btn" data-view="spotify">${escapeHtml(t('nav.spotify'))}<span class="beta-pill">${escapeHtml(t('common.beta'))}</span></button>
  </div>
  <div id="globalSecurityBanner" class="global-security-banner hidden">
    <span class="global-security-text">
      <b>${escapeHtml(t('banner.recommendation'))}</b> ${escapeHtml(t('banner.sshRecommend'))}
    </span>
    <button class="btn btn-mini" id="globalSecurityRebootBtn">${escapeHtml(t('speaker.reboot'))}</button>
  </div>
  <div id="view-box" class="view"></div>
  <div id="view-library" class="view hidden"></div>
  <div id="view-settings" class="view hidden"></div>
  <div id="view-setup" class="view hidden"></div>
  <div id="view-multiroom" class="view hidden"></div>
  <div id="view-spotify" class="view hidden"></div>

  <div class="modal hidden" id="pickModal">
    <div class="modal-content">
      <h3 id="pickTitle">${escapeHtml(t('preset.assignTitle'))}</h3>
      <p class="modal-sub" id="pickSub"></p>
      <div class="pick-grid" id="pickGrid"></div>
      <button class="btn btn-secondary" id="pickCancel">${escapeHtml(t('common.cancel'))}</button>
    </div>
  </div>

  <div class="modal hidden" id="warnModal">
    <div class="modal-content">
      <h3 class="warn-title"><span class="warn-icon">&#9888;</span> ${escapeHtml(t('modal.warnTitle'))}</h3>
      <div id="warnBody"></div>
      <div class="warn-buttons">
        <button class="btn btn-secondary" id="warnCancel">${escapeHtml(t('common.cancel'))}</button>
        <button class="btn btn-danger" id="warnConfirm">${escapeHtml(t('modal.proceed'))}</button>
      </div>
    </div>
  </div>

  <div class="modal hidden" id="errorModal">
    <div class="modal-content">
      <h3 class="warn-title"><span class="warn-icon">&#9888;</span> ${escapeHtml(t('modal.errorTitle'))}</h3>
      <textarea id="errorText" class="error-text" readonly></textarea>
      <div class="warn-buttons">
        <button class="btn btn-secondary" id="errorCopy">${escapeHtml(t('modal.copy'))}</button>
        <button class="btn" id="errorClose">${escapeHtml(t('common.close'))}</button>
      </div>
    </div>
  </div>

  <div class="modal hidden" id="creditsModal">
    <div class="modal-content">
      <h3 id="creditsTitle">${escapeHtml(t('credits.title'))}</h3>
      <p class="modal-sub" id="creditsIntro">${escapeHtml(t('credits.intro'))}</p>
      <div id="creditsBody" class="credits-list"></div>
      <button class="btn" id="creditsClose">${escapeHtml(t('common.close'))}</button>
    </div>
  </div>

  <div id="toast" class="toast"></div>

  <footer class="app-footer" id="appFooter"></footer>
`;


// Tabs
document.querySelectorAll('.tab-btn').forEach(btn => {
  btn.onclick = () => switchView(btn.dataset.view);
});

// Language picker in the header. Switching locale reloads the page so
// the full UI re-renders against the new bundle. Reload is heavy but
// keeps the rendering path simple: no piecemeal rerender of 3000
// lines of view code.
(function wireLocalePicker() {
  const trigger = document.getElementById('localeTrigger');
  const menu = document.getElementById('localeMenu');
  if (!trigger || !menu) return;
  const close = () => { menu.hidden = true; trigger.setAttribute('aria-expanded', 'false'); };
  const open = () => { menu.hidden = false; trigger.setAttribute('aria-expanded', 'true'); };
  trigger.onclick = (e) => { e.stopPropagation(); if (menu.hidden) open(); else close(); };
  menu.querySelectorAll('.locale-dd-item').forEach(item => {
    item.onclick = () => {
      const code = item.dataset.locale;
      if (code && code !== getLocale() && setLocale(code)) {
        location.reload();
      } else {
        close();
      }
    };
  });
  // Close on outside click or Escape.
  document.addEventListener('click', (e) => { if (!e.target.closest('.locale-dd')) close(); });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') close(); });
})();

// Tell the Go backend which UI language is active, so server-side
// provisioning (the Setup-AP push) sets the speaker's display language
// to the user's language instead of a hardcoded default. This runs on
// every load — including after a locale switch, since the picker above
// reloads the page — so the backend always has the current locale.
// Best-effort: a binding error must never block UI startup.
SetAppLocale(getLocale()).catch(() => {});

// Tagline and supported-models line follow the active locale, falling
// back to English. Native-speaker translations live inline here for
// the languages we have native-speaker copy for. New languages added
// to i18n/bundles fall back to English until a maintainer adds
// localized prose here.
const SUPPORTED_LINE = {
  de: 'für SoundTouch 10, 20, 30 und Portable',
  fr: 'pour SoundTouch 10, 20, 30 et Portable',
  it: 'per SoundTouch 10, 20, 30 e Portable',
  es: 'para SoundTouch 10, 20, 30 y Portable',
  nl: 'voor SoundTouch 10, 20, 30 en Portable',
  pt: 'para SoundTouch 10, 20, 30 e Portable',
  ja: 'SoundTouch 10、20、30、Portable に対応',
  uk: 'для SoundTouch 10, 20, 30 і Portable',
  pl: 'dla SoundTouch 10, 20, 30 i Portable',
  lt: 'skirta SoundTouch 10, 20, 30 ir Portable',
  lv: 'SoundTouch 10, 20, 30 un Portable modeļiem',
  tr: 'SoundTouch 10, 20, 30 ve Portable için',
  en: 'for SoundTouch 10, 20, 30 and Portable',
};

const TAGLINES = {
  de: 'Bose SoundTouch Lautsprecher ohne Bose Cloud weiter nutzen.',
  fr: 'Continue d\'utiliser tes enceintes Bose SoundTouch sans le cloud Bose.',
  it: 'Continua a usare gli altoparlanti Bose SoundTouch senza il cloud di Bose.',
  es: 'Sigue usando tus altavoces Bose SoundTouch sin la nube de Bose.',
  nl: 'Blijf je Bose SoundTouch speakers gebruiken, zonder de Bose cloud.',
  pt: 'Continua a usar os teus altifalantes Bose SoundTouch sem a cloud Bose.',
  ja: 'Bose SoundTouch スピーカーを Bose クラウドなしで使い続けられます。',
  uk: 'Користуйтеся колонками Bose SoundTouch і далі, без хмари Bose.',
  pl: 'Korzystaj dalej z głośników Bose SoundTouch, bez chmury Bose.',
  lt: 'Toliau naudokitės savo Bose SoundTouch garsiakalbiais be Bose debesies.',
  lv: 'Turpiniet lietot savus Bose SoundTouch skaļruņus bez Bose mākoņa.',
  tr: 'Bose SoundTouch hoparlörlerinizi Bose bulutu olmadan kullanmaya devam edin.',
  en: 'Keep using your Bose SoundTouch speakers, without the Bose cloud.',
};

(function applyTagline() {
  const lang = getLocale();
  const tEl = $('appTagline');
  if (tEl) tEl.textContent = TAGLINES[lang] || TAGLINES.en;
  const sEl = $('appSupported');
  if (sEl) sEl.textContent = SUPPORTED_LINE[lang] || SUPPORTED_LINE.en;
})();

function switchView(view) {
  state.view = view;
  document.querySelectorAll('.tab-btn').forEach(b => {
    b.classList.toggle('active', b.dataset.view === view);
  });
  $('view-box').classList.toggle('hidden', view !== 'box');
  $('view-library').classList.toggle('hidden', view !== 'library');
  $('view-settings').classList.toggle('hidden', view !== 'settings');
  $('view-setup').classList.toggle('hidden', view !== 'setup');
  $('view-multiroom').classList.toggle('hidden', view !== 'multiroom');
  $('view-spotify').classList.toggle('hidden', view !== 'spotify');
  // Global SSH banner: the Setup tab has no speaker context, so hide
  // the banner there unconditionally. Otherwise let checkSshBanner
  // decide.
  if (view === 'setup') {
    const gb = $('globalSecurityBanner');
    if (gb) gb.classList.add('hidden');
  } else {
    checkSshBanner();
  }
  if (view === 'setup') {
    refreshDrives();
    // Re-render the target picker on every entry into the Setup
    // tab. The list may have changed (newly powered speaker,
    // freshly installed STR) and the user just opened the tab to
    // start a prep flow — make sure they see the right targets.
    renderSetupTargetPicker();
    // Lazy-load saved WiFi profiles (#88 followup). v0.5.16
    // gated the macOS keychain auto-prompt that fired on app start;
    // v0.5.17 defers the lookup entirely to Setup-tab activation so
    // Windows (netsh wlan show profiles) and Linux (nmcli) also do
    // not run the OS call for users who only use Music or Settings.
    // Idempotent: re-runs on every Setup-tab open so a refreshed
    // OS profile list is picked up too.
    if (typeof loadWifiProfiles === 'function') loadWifiProfiles();
  }
  if (view === 'box') {
    // Refresh the mDNS list on every switch to the music view so a
    // recently renamed speaker or a speaker that went offline does
    // not linger. discoverBoxes is async and non-blocking.
    discoverBoxes();
    refreshStatus();
    loadMusicTabVolume();
  }
  if (view === 'settings') loadBoxSettings();
  if (view === 'library') openLibrary();
  if (view === 'multiroom') renderMultiroom();
  if (view === 'spotify') renderSpotifyAlpha();
}

// Multi-room / SoundTouch zone (#70, BETA). Blind beta: drives the native Bose
// /setZone via each speaker's agent, persists the group so it auto-reforms, and
// asks multi-speaker testers to send logs. The master speaker plays the source
// and the firmware streams it to the others. Re-rendered on every view switch
// and after each action so the live group reflects reality.
function renderMultiroom() {
  const root = $('view-multiroom');
  if (!root) return;
  const strBoxes = (state.boxes || []).filter(b => b && b.kind !== 'stock' && b.host);
  const beta =
    `<div class="setup-help" style="margin-bottom:14px">` +
    `<b>${escapeHtml(t('multiroom.heading'))} <span class="beta-pill alpha-pill">${escapeHtml(t('common.beta'))}</span></b>` +
    `<div class="muted small" style="margin-top:6px">${escapeHtml(t('multiroom.betaNote'))}</div></div>`;

  if (strBoxes.length < 2) {
    root.innerHTML = beta + `<div class="muted">${escapeHtml(t('multiroom.needTwo'))}</div>`;
    return;
  }

  if (!state.zoneMaster || !strBoxes.some(b => b.deviceID === state.zoneMaster)) {
    state.zoneMaster = strBoxes[0].deviceID;
  }
  const master = state.zoneMaster;
  const label = (b) => b.name || b.friendlyName || b.host;
  const masterOpts = strBoxes
    .map(b => `<option value="${escapeAttr(b.deviceID)}" ${b.deviceID === master ? 'selected' : ''}>${escapeHtml(label(b))}</option>`)
    .join('');
  const slaveRows = strBoxes
    .filter(b => b.deviceID !== master)
    .map(b => `<label class="zone-slave"><input type="checkbox" class="zoneSlave" value="${escapeAttr(b.deviceID)}"> ${escapeHtml(label(b))} <span class="muted small">${escapeHtml(b.model || '')}</span></label>`)
    .join('');

  root.innerHTML = beta +
    `<div class="zone-form">
      <label class="zone-field"><span>${escapeHtml(t('multiroom.masterLabel'))}</span>
        <select id="zoneMaster">${masterOpts}</select></label>
      <div class="zone-field"><span>${escapeHtml(t('multiroom.slavesLabel'))}</span>
        <div id="zoneSlaves">${slaveRows}</div></div>
      <input id="zoneName" type="text" placeholder="${escapeAttr(t('multiroom.groupNamePh'))}" />
      <div class="zone-actions">
        <button id="zoneCreate" class="btn">${escapeHtml(t('multiroom.createBtn'))}</button>
        <button id="zoneUngroup" class="btn btn-mini">${escapeHtml(t('multiroom.ungroupBtn'))}</button>
      </div>
      <div id="zoneResult"></div>
      <div id="zoneCurrent" class="muted small" style="margin-top:10px"></div>
    </div>`;

  $('zoneMaster').onchange = (e) => { state.zoneMaster = e.target.value; renderMultiroom(); };
  $('zoneCreate').onclick = () => doFormZone(strBoxes);
  $('zoneUngroup').onclick = () => doDissolveZone(strBoxes);
  refreshZoneCurrent(strBoxes);
}

async function doFormZone(strBoxes) {
  const master = strBoxes.find(b => b.deviceID === state.zoneMaster);
  if (!master) return;
  const slaveIds = Array.from(document.querySelectorAll('.zoneSlave:checked')).map(el => el.value);
  if (!slaveIds.length) {
    $('zoneResult').innerHTML = `<div class="setup-warn">${escapeHtml(t('multiroom.needTwo'))}</div>`;
    return;
  }
  const slaves = slaveIds.map(id => {
    const b = strBoxes.find(x => x.deviceID === id);
    return { deviceID: id, ip: b ? b.host : '' };
  });
  const name = ($('zoneName').value || '').trim();
  $('zoneResult').innerHTML = `<div class="muted">${escapeHtml(t('common.loading'))}</div>`;
  try {
    await FormZone(master.host, master.port, {
      master: { deviceID: master.deviceID, ip: master.host },
      slaves, name, stereo: false,
    });
    $('zoneResult').innerHTML = `<div class="setup-ok">${escapeHtml(t('multiroom.formed'))}</div>`;
    refreshZoneCurrent(strBoxes);
  } catch (e) {
    $('zoneResult').innerHTML = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(e) }))}</div>`;
  }
}

async function doDissolveZone(strBoxes) {
  const master = strBoxes.find(b => b.deviceID === state.zoneMaster);
  if (!master) return;
  try {
    await DissolveZone(master.host, master.port);
    $('zoneResult').innerHTML = `<div class="setup-ok">${escapeHtml(t('multiroom.noZone'))}</div>`;
    refreshZoneCurrent(strBoxes);
  } catch (e) {
    $('zoneResult').innerHTML = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(e) }))}</div>`;
  }
}

async function refreshZoneCurrent(strBoxes) {
  const master = strBoxes.find(b => b.deviceID === state.zoneMaster);
  const el = $('zoneCurrent');
  if (!master || !el) return;
  try {
    const z = await GetZoneState(master.host, master.port);
    if (z && z.master) {
      const names = (z.members || []).map(m => {
        const b = strBoxes.find(x => (x.deviceID || '').toUpperCase() === (m.deviceID || '').toUpperCase());
        return b ? label0(b) : (m.ip || m.deviceID);
      });
      el.innerHTML = `<b>${escapeHtml(t('multiroom.currentZone'))}:</b> ` +
        escapeHtml(label0(master) + (names.length ? ' + ' + names.join(', ') : ''));
    } else {
      el.textContent = t('multiroom.noZone');
    }
  } catch { el.textContent = ''; }
}

function label0(b) { return b.name || b.friendlyName || b.host; }

// Alpha-stage placeholder for Spotify Connect / streaming integration
// (#78). Same pattern as renderMultiroomAlpha.
function renderSpotifyAlpha() {
  const root = $('view-spotify');
  if (!root || root.dataset.rendered === '1') return;
  root.dataset.rendered = '1';
  root.innerHTML = `
    <div class="alpha-stage">
      <h2>${escapeHtml(t('spotify.heading'))} <span class="beta-pill">${escapeHtml(t('common.beta'))}</span></h2>
      <p>${escapeHtml(t('spotify.nativeIntro'))}</p>
      <ol class="alpha-checklist">
        <li>${escapeHtml(t('spotify.nativeStep1'))}</li>
        <li>${escapeHtml(t('spotify.nativeStep2'))}</li>
        <li>${escapeHtml(t('spotify.nativeStep3'))}</li>
      </ol>
      <p class="muted small">${escapeHtml(t('spotify.versionNote'))} <a href="#" id="spotifyUpdateLink">${escapeHtml(t('spotify.updateLink'))}</a></p>
      <h3>${escapeHtml(t('spotify.worksTitle'))}</h3>
      <ul class="spotify-status">
        <li>${escapeHtml(t('spotify.works1'))}</li>
        <li>${escapeHtml(t('spotify.works2'))}</li>
        <li>${escapeHtml(t('spotify.works3'))}</li>
        <li>${escapeHtml(t('spotify.works4'))}</li>
      </ul>
      <h3>${escapeHtml(t('spotify.limitsTitle'))}</h3>
      <ul class="spotify-status">
        <li>${escapeHtml(t('spotify.limit1'))}</li>
        <li>${escapeHtml(t('spotify.limit2'))}</li>
        <li>${escapeHtml(t('spotify.limit3'))}</li>
      </ul>
      <p class="muted small">${escapeHtml(t('spotify.nativeNote'))}</p>
      <p>${escapeHtml(t('spotify.feedbackNote'))} <a href="#" id="spotifyIssueLink">${escapeHtml(t('spotify.issueLink'))}</a></p>
    </div>
  `;
  const upd = $('spotifyUpdateLink');
  if (upd) upd.onclick = (e) => { e.preventDefault(); switchView('settings'); };
  const link = $('spotifyIssueLink');
  if (link) link.onclick = (e) => {
    e.preventDefault();
    try { BrowserOpenURL('https://github.com/JRpersonal/streborn/issues/78'); } catch {}
  };
}

// ---------- Footer ----------

// withAppReferrer appends UTM parameters to URLs pointing at the
// project's own website so the site's visitor analytics can attribute
// traffic that originated in the desktop app. Vendor URLs (GitHub,
// PayPal, Ko-fi, GitHub Sponsors) are returned unchanged because
// their analytics do not respect UTM tags and a few of them refuse
// query strings on canonical paths.
function withAppReferrer(url, campaign) {
  try {
    const u = new URL(url);
    if (!/(^|\.)st-reborn\.de$/i.test(u.hostname)) return url;
    if (!u.searchParams.has('utm_source'))   u.searchParams.set('utm_source',   'st-reborn-app');
    if (!u.searchParams.has('utm_medium'))   u.searchParams.set('utm_medium',   'desktop');
    if (!u.searchParams.has('utm_campaign')) u.searchParams.set('utm_campaign', campaign || 'app');
    const ver = (state.appInfo && state.appInfo.version) || '';
    if (ver && !u.searchParams.has('utm_content')) u.searchParams.set('utm_content', ver);
    return u.toString();
  } catch {
    return url;
  }
}

// OSS STR bundles, links, or builds on. Listed in full regardless of license
// (it does not hurt to be generous), with the bundled GPL-3.0 go-librespot
// first since that one is a licensing obligation, not just a courtesy.
const OSS_CREDITS = [
  { name: 'go-librespot', by: 'devgianlu', license: 'GPL-3.0', url: 'https://github.com/devgianlu/go-librespot', role: 'Spotify Connect client (bundled as a separate binary)' },
  { name: 'Wails', license: 'MIT', url: 'https://wails.io', role: 'desktop app framework' },
  { name: 'gorilla/websocket', license: 'BSD-2-Clause', url: 'https://github.com/gorilla/websocket', role: 'Bose gabbo WebSocket client' },
  { name: 'grandcat/zeroconf', license: 'MIT', url: 'https://github.com/grandcat/zeroconf', role: 'mDNS discovery' },
  { name: 'golang.org/x/sys', license: 'BSD-3-Clause', url: 'https://pkg.go.dev/golang.org/x/sys', role: 'low-level system calls' },
  { name: 'Go', license: 'BSD-3-Clause', url: 'https://go.dev', role: 'language and toolchain' },
  { name: 'Vite', license: 'MIT', url: 'https://vitejs.dev', role: 'frontend build tool' },
  { name: 'Octicons', by: 'GitHub', license: 'MIT', url: 'https://github.com/primer/octicons', role: 'interface icons' },
  { name: 'radio-browser.info', license: 'community service', url: 'https://www.radio-browser.info', role: 'radio station directory' },
  { name: 'DuckDuckGo icons', license: 'service', url: 'https://duckduckgo.com', role: 'station logos' },
];

// showCredits opens the open-source credits dialog from the footer link.
function showCredits() {
  const modal = $('creditsModal');
  const body = $('creditsBody');
  if (!modal || !body) return;
  $('creditsTitle').textContent = t('credits.title');
  $('creditsIntro').textContent = t('credits.intro');
  body.innerHTML = OSS_CREDITS.map(c => {
    const by = c.by ? ` <span class="credit-by">${escapeHtml(t('credits.by'))} ${escapeHtml(c.by)}</span>` : '';
    return `<div class="credit-row">`
      + `<div><a href="#" class="footer-link credit-name" data-url="${escapeAttr(c.url)}">${escapeHtml(c.name)}</a>${by}`
      + ` <span class="credit-license">${escapeHtml(c.license)}</span></div>`
      + `<div class="credit-role">${escapeHtml(c.role)}</div></div>`;
  }).join('');
  body.querySelectorAll('.credit-name[data-url]').forEach(a => {
    a.onclick = (e) => { e.preventDefault(); BrowserOpenURL(a.dataset.url); };
  });
  const close = () => modal.classList.add('hidden');
  $('creditsClose').onclick = close;
  modal.onclick = (e) => { if (e.target === modal) close(); };
  modal.classList.remove('hidden');
}

async function renderFooter() {
  try {
    state.appInfo = await AppInfo();
  } catch {
    state.appInfo = { version: t('common.unknown'), build: '', author: '', githubUrl: '', donateUrl: '', websiteUrl: '', donateSlogan: '' };
  }
  const i = state.appInfo;
  const links = [];
  if (i.githubUrl)  links.push(`<a href="#" data-url="${escapeAttr(i.githubUrl)}" class="footer-link">GitHub</a>`);
  if (i.websiteUrl) links.push(`<a href="#" data-url="${escapeAttr(i.websiteUrl)}" class="footer-link">${escapeHtml(t('footer.website'))}</a>`);
  links.push(`<a href="#" id="footerSaveLogs" class="footer-link" title="${escapeAttr(t('footer.saveLogsHint'))}">${escapeHtml(t('footer.saveLogs'))}</a>`);
  links.push(`<a href="#" id="footerCredits" class="footer-link">${escapeHtml(t('footer.credits'))}</a>`);
  const buildStr = i.build && i.build !== 'dev' ? ` <span class="build-stamp">(Build ${escapeHtml(i.build)})</span>` : '';
  // Clicking the version opens the release notes. For a clean tagged
  // build that is the matching GitHub release page (which carries the
  // generated "What's changed" notes); for a dev build (version like
  // v0.6.21-3-gabc-dirty) there is no tag page, so fall back to the
  // releases list.
  const repo = (i.githubUrl || 'https://github.com/JRpersonal/streborn').replace(/\/+$/, '');
  const isTag = /^v\d+\.\d+\.\d+$/.test(i.version || '');
  const releaseNotesUrl = isTag ? `${repo}/releases/tag/${i.version}` : `${repo}/releases`;
  $('appFooter').innerHTML = `
    <div class="footer-left">
      ST Reborn &middot; Version <a href="#" id="appVersionLink" class="footer-link" title="${escapeAttr(t('banner.whatsNew'))}"><b>${escapeHtml(i.version)}</b></a>${buildStr}${i.author ? ' &middot; ' + escapeHtml(i.author) : ''}
      <div class="footer-fine">Independent open source project, donation funded, MIT license.</div>
    </div>
    <div class="footer-right">${links.join(' &middot; ')}</div>
  `;
  $('appFooter').querySelectorAll('.footer-link[data-url]').forEach(a => {
    a.onclick = (e) => { e.preventDefault(); BrowserOpenURL(withAppReferrer(a.dataset.url, 'footer')); };
  });
  const verLink = $('appVersionLink');
  if (verLink) verLink.onclick = (e) => { e.preventDefault(); BrowserOpenURL(releaseNotesUrl); };
  const creditsLink = $('footerCredits');
  if (creditsLink) creditsLink.onclick = (e) => { e.preventDefault(); showCredits(); };
  const saveLogsBtn = $('footerSaveLogs');
  if (saveLogsBtn) {
    saveLogsBtn.onclick = async (e) => {
      e.preventDefault();
      saveLogsBtn.classList.add('working');
      try {
        const hosts = (state.boxes || []).map(b => b && b.host).filter(Boolean);
        const res = await SaveDiagnosticBundle(hosts, true);
        if (res && res.savePath) {
          showToast(t('footer.saveLogsDone', { path: res.savePath, size: Math.round((res.bytes || 0) / 1024) }));
        }
        // If user cancelled the dialog, savePath comes back empty —
        // no toast on cancel.
      } catch (err) {
        showError(String(err));
      } finally {
        saveLogsBtn.classList.remove('working');
      }
    };
  }
  renderDonateSidebar();
  // Defer the update check out of the critical startup path: the window
  // and discovery come up first, and the network call (a reported suspect
  // for a macOS start crash) only fires once the app is already running,
  // so even if it ever misbehaved it cannot abort startup. checkAppUpdate
  // is itself fully guarded (try/catch + Go-side recover).
  setTimeout(() => { try { checkAppUpdate(); } catch {} }, 8000);
  // appInfo may have arrived after the first discovery completed; the
  // badge function defers until both are known. Re-render the box list
  // too so the per-speaker update dot (boxNeedsUpdate) appears once the
  // app version is finally known.
  updateSettingsTabBadge();
  if (state.boxes.length) renderBoxSelect();
}

// Donate sidebar — three branded buttons that open in the system
// browser via Wails. Brand colours and assets follow each provider's
// guidelines:
//   * GitHub Sponsors: white background, #bf3989 (Mona Pink) border
//     and Octicons heart-fill SVG (MIT licensed)
//   * PayPal: #FFC439 yellow background, two-tone "PayPal" wordmark
//     in #003087 + #009CDE per the official color system
//   * Ko-fi: #FF5E5B coral background, coffee-cup mark + white text
//
// Links are baked in rather than fetched from appInfo because each
// provider has its own canonical URL — keeping them inline removes a
// round trip and means the sidebar renders even before AppInfo loads.
function renderDonateSidebar() {
  const side = $('donateSide');
  if (!side) return;
  const i = state.appInfo || {};
  const slogan = i.donateSlogan || t('footer.donateSlogan');

  // Octicons heart-fill-16, MIT licensed (https://github.com/primer/octicons).
  const heartSvg = `<svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path fill="currentColor" d="m8 14.25.345.666a.75.75 0 0 1-.69 0l-.008-.004-.018-.01a7.152 7.152 0 0 1-.31-.17 22.055 22.055 0 0 1-3.434-2.414C2.045 10.731 0 8.35 0 5.5 0 2.836 2.086 1 4.25 1 5.797 1 7.153 1.802 8 3.02 8.847 1.802 10.203 1 11.75 1 13.914 1 16 2.836 16 5.5c0 2.85-2.045 5.231-3.885 6.818a22.066 22.066 0 0 1-3.744 2.584l-.018.01-.005.003h-.002Z"/></svg>`;
  // Ko-fi has no single canonical inline mark; this is a compact
  // coffee-cup glyph (taken from Simple Icons / Ko-fi brand kit
  // composition) that recognisable at 14px.
  const coffeeSvg = `<svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true"><path fill="currentColor" d="M20.216 6.415C19.964 5.43 19.066 4.78 18.057 4.78H5.943c-1.009 0-1.907.65-2.159 1.635C2.987 9.085 3 12.34 4.97 14.605c1.236 1.42 3.116 2.13 5.59 2.13 1.85 0 3.62-.404 4.97-1.137A6.43 6.43 0 0 0 19.046 14H19.5a3.5 3.5 0 1 0 0-7h-.07a4.8 4.8 0 0 0-.214-.585zM19 9.5h.5a1.5 1.5 0 0 1 0 3H19a4.21 4.21 0 0 0 .003-.123V9.535c0-.012-.002-.023-.003-.035zM7.5 19h11a.5.5 0 0 1 0 1h-11a.5.5 0 0 1 0-1z"/></svg>`;

  side.innerHTML = `
    <div class="donate-icon">&#9749;</div>
    <div class="donate-slogan">${escapeHtml(slogan)}</div>
    <button class="donate-btn donate-gh" id="donateGhBtn" type="button" title="GitHub Sponsors">
      <span class="donate-btn-icon">${heartSvg}</span>
      <span class="donate-btn-label">Sponsor</span>
    </button>
    <button class="donate-btn donate-paypal" id="donatePayPalBtn" type="button" title="PayPal">
      <span class="donate-paypal-wordmark"><span class="pay">Pay</span><span class="pal">Pal</span></span>
    </button>
    <button class="donate-btn donate-kofi" id="donateKofiBtn" type="button" title="Ko-fi">
      <span class="donate-btn-icon">${coffeeSvg}</span>
      <span class="donate-btn-label">Ko-fi</span>
    </button>
  `;

  const wire = (id, url) => {
    const b = $(id);
    if (b) b.onclick = () => BrowserOpenURL(url);
  };
  wire('donateGhBtn',     'https://github.com/sponsors/JRpersonal');
  wire('donatePayPalBtn', 'https://paypal.me/JR31337');
  wire('donateKofiBtn',   'https://ko-fi.com/streborn');
}

async function checkAppUpdate() {
  // Entirely best-effort: an unreachable endpoint or a garbage payload
  // must never break the UI. Validate every field defensively and stay
  // silent on any failure.
  try {
    const m = await CheckAppUpdate();
    if (!m || typeof m !== 'object' || typeof m.version !== 'string' || !m.version) return;
    const banner = $('appUpdateBanner');
    if (!banner) return;
    // Keep the banner discreet: version, a single "What's new" LINK to
    // the release notes (not the notes inline, which took too much space
    // and does not interest every user), and the download button.
    // Only treat a real http(s) URL as a download link; anything else is
    // ignored so we never hand junk to the system browser.
    const dlUrl = (typeof m.downloadUrl === 'string' && /^https?:\/\//i.test(m.downloadUrl)) ? m.downloadUrl : '';
    // Link target for the notes: an explicit notesUrl from the server if
    // present, else the matching GitHub release page for the new version,
    // else the releases list.
    const repo = ((state.appInfo && state.appInfo.githubUrl) || 'https://github.com/JRpersonal/streborn').replace(/\/+$/, '');
    const notesUrl = (typeof m.notesUrl === 'string' && /^https?:\/\//i.test(m.notesUrl))
      ? m.notesUrl
      : (/^v\d+\.\d+\.\d+$/.test(m.version) ? `${repo}/releases/tag/${m.version}` : `${repo}/releases`);
    banner.innerHTML = `
      <div><b>${escapeHtml(t('banner.appUpdateAvail'))}</b> ${escapeHtml(m.version)} &middot; <a href="#" id="appUpdateNotes" class="footer-link">${escapeHtml(t('banner.whatsNew'))}</a></div>
      ${dlUrl ? `<button class="btn btn-mini" id="appUpdateBtn">${escapeHtml(t('banner.download'))}</button>` : ''}
    `;
    banner.classList.remove('hidden');
    const notesLink = $('appUpdateNotes');
    if (notesLink) notesLink.onclick = (e) => { e.preventDefault(); BrowserOpenURL(notesUrl); };
    const dl = $('appUpdateBtn');
    if (dl && dlUrl) dl.onclick = () => BrowserOpenURL(dlUrl);
  } catch (e) {
    try { console.warn('checkAppUpdate failed', e); } catch {}
  }
}

// ---------- Box steuern View ----------

$('view-box').innerHTML = `
  <div class="topbar">
    <div class="topbar-head">
      <div class="topbar-title">${escapeHtml(t('topbar.title'))}</div>
      <button class="btn-icon" id="refreshBtn" title="${escapeAttr(t('topbar.refreshTitle'))}"><span class="refresh-icon">&#x21bb;</span></button>
    </div>
    <div class="box-select" id="boxSelect">${escapeHtml(t('speaker.searching'))}</div>
  </div>
  <div id="boxHint" class="box-hint hidden">
    <p>${escapeHtml(t('speaker.choose'))}</p>
  </div>
  <div id="boxControls" class="hidden">
    <div id="boxUpdateBanner" class="update-banner hidden"></div>
    <div class="status-bar" id="statusBar"></div>
    <div class="controls">
      <button class="btn" id="pauseBtn">&#9208; ${escapeHtml(t('controls.pause'))}</button>
      <button class="btn" id="stopBtn">&#9209; ${escapeHtml(t('controls.stop'))}</button>
      <div class="source-buttons">
        <button class="btn btn-source" data-source="AUX" title="${escapeAttr(t('controls.auxTitle'))}">AUX</button>
        <button class="btn btn-source btn-source-icon" data-source="BLUETOOTH" title="${escapeAttr(t('controls.bluetoothTitle'))}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="16" height="16"><polyline points="6.5 6.5 17.5 17.5 12 23 12 1 17.5 6.5 6.5 17.5"></polyline></svg></button>
        <button class="btn btn-source btn-source-icon" data-source="STANDBY" title="${escapeAttr(t('controls.standbyTitle'))}">&#9211;</button>
      </div>
      <div class="volume-control">
        <span class="vol-icon" title="${escapeAttr(t('controls.volume'))}">&#128266;</span>
        <input type="range" id="musicVolume" min="0" max="100" step="1" />
        <span class="vol-val" id="musicVolumeVal">--</span>
      </div>
    </div>
    <div class="grid" id="presets"></div>
    <div class="search">
      <h3>${escapeHtml(t('search.heading'))} <small>(${escapeHtml(t('search.headingSub'))})</small></h3>
      <div class="search-input-row">
        <input type="text" id="searchQ" placeholder="${escapeAttr(t('search.placeholder'))}" />
        <button class="btn" id="searchBtn">${escapeHtml(t('search.btn'))}</button>
        <button class="btn btn-mini" id="topBtn">${escapeHtml(t('search.topBtn'))}</button>
        <button class="btn btn-mini hidden" id="favModeBtn" title="${escapeAttr(t('search.favBtnTitle'))}">${escapeHtml(t('search.favBtn'))}</button>
      </div>
      <div class="search-filters">
        <label>${escapeHtml(t('search.countryLabel'))}:
          <select id="searchCountry"></select>
        </label>
        <label>${escapeHtml(t('search.languageLabel'))}:
          <select id="searchLang"><option value="">${escapeHtml(t('search.allLanguages'))}</option></select>
        </label>
        <label>${escapeHtml(t('search.orderLabel'))}:
          <select id="searchOrder"></select>
        </label>
        <label>${escapeHtml(t('search.bitrateLabel'))}:
          <select id="searchBitrate">
            <option value="0">${escapeHtml(t('search.bitrateAny'))}</option>
            <option value="64">&ge; 64 kbit/s</option>
            <option value="96">&ge; 96 kbit/s</option>
            <option value="128">&ge; 128 kbit/s</option>
            <option value="192">&ge; 192 kbit/s</option>
            <option value="256">&ge; 256 kbit/s</option>
            <option value="320">&ge; 320 kbit/s</option>
          </select>
        </label>
        <label><input type="checkbox" id="searchOnlyOK" checked /> ${escapeHtml(t('search.onlyOK'))}</label>
        <label><input type="checkbox" id="searchOnlyBose" checked /> ${escapeHtml(t('search.onlyBose'))}</label>
      </div>
      <div class="genre-chips" id="genreChips"></div>
      <div class="search-count muted small hidden" id="searchCount"></div>
      <div class="search-results" id="searchResults"></div>
      <div class="load-more-row hidden" id="loadMoreRow">
        <button class="btn btn-mini" id="loadMoreBtn">${escapeHtml(t('search.loadMore'))}</button>
      </div>
      <a href="#" class="search-addhint muted small" id="addStationHint">${escapeHtml(t('search.addStationHint'))}</a>
    </div>
  </div>
`;

// Filter Dropdowns befuellen
$('searchCountry').innerHTML = COUNTRIES.map(c =>
  `<option value="${c.cc}">${optFlag(c.cc)}${escapeHtml(c.name)}</option>`
).join('');
$('searchOrder').innerHTML = ORDERS.map(o =>
  `<option value="${o.v}">${escapeHtml(o.label)}</option>`
).join('');
$('searchCountry').value = state.searchCountry;
$('searchOrder').value = state.searchOrder;
$('searchOnlyOK').checked = state.searchOnlyOK;
$('searchOnlyBose').checked = state.searchOnlyBose;

$('refreshBtn').onclick = discoverBoxes;

// Globaler Security Reboot Knopf (im Top Banner)
const gsb = $('globalSecurityRebootBtn');
if (gsb) gsb.onclick = async () => {
  const box = state.currentBox || (state.boxes && state.boxes[0]);
  if (!box) { showToast(t('speaker.noneSelected')); return; }
  const ok = await confirmWarn(
    t('speaker.rebootConfirmTitle'),
    t('speaker.rebootConfirmBody')
  );
  if (!ok) return;
  try {
    await RebootBox(box.host, box.port);
    showToast(t('speaker.rebootingToast'));
    setTimeout(discoverBoxes, 35000);
  } catch (e) { showError(e); }
};
$('pauseBtn').onclick = () => action('pause');
$('stopBtn').onclick = () => action('stop');

// Source Buttons (AUX / Bluetooth / Standby) im Musik-Hoeren Tab —
// rufen das neue /api/box/source Endpoint via SelectBoxSource Binding.
document.querySelectorAll('.btn-source').forEach(btn => {
  btn.onclick = async () => {
    const box = state.currentBox;
    if (!box) { showToast(t('speaker.noneSelected')); return; }
    const src = btn.dataset.source;
    btn.disabled = true;
    try {
      await SelectBoxSource(box.host, box.port, src);
      showToast(t('toast.source', { src }));
      setTimeout(refreshStatus, 800);
    } catch (e) {
      // The button is normally hidden on hardware that lacks the
      // source, but if the box reports it unavailable anyway (1005
      // UNKNOWN_SOURCE_ERROR, relayed by the agent as source_unavailable)
      // show a clear message instead of the raw box error.
      if (String(e).includes('source_unavailable')) {
        showToast(t('toast.sourceUnavailable', { src }));
        btn.classList.add('hidden');
      } else {
        showError(e);
      }
    } finally {
      btn.disabled = false;
    }
  };
});

// Volume slider in the music tab. Uses SetBoxVolume, debounced so a
// drag does not fire a hundred API calls.
let musicVolTimer = null;
let musicVolBox = null;
const musicVolEl = $('musicVolume');
const musicVolValEl = $('musicVolumeVal');
// Drag-busy + grace period so the 2 s periodic refresh in
// refreshStatus does not yank the thumb out from under the user
// while they are wischen. musicVolUntil is the timestamp at which
// auto-refresh is allowed to take over again after a release.
state.musicVolBusy = false;
state.musicVolUntil = 0;
if (musicVolEl) {
  // Live-update during drag: each input event throttles a
  // SetBoxVolume call so the user sees the level move on the
  // speaker WHILE they wipe, not only on release. The throttle
  // collapses bursts so the box's tiny HTTP server never has more
  // than one volume PUT in flight at a time.
  musicVolEl.oninput = () => {
    if (musicVolValEl) musicVolValEl.textContent = musicVolEl.value;
    const box = state.currentBox;
    if (!box) return;
    musicVolBox = box;
    state.desiredVolume = parseInt(musicVolEl.value, 10);
    throttledSetVolume(box.host, box.port, state.desiredVolume);
  };
  // Keyboard arrows fire only `change`, not `input`, so we still
  // dispatch on change as a safety net for that path.
  musicVolEl.onchange = () => {
    musicVolBox = state.currentBox;
    if (!musicVolBox) return;
    state.desiredVolume = parseInt(musicVolEl.value, 10);
    throttledSetVolume(musicVolBox.host, musicVolBox.port, state.desiredVolume);
  };
  // pointerdown/up flag is the most reliable cross-device drag
  // signal. Keyboard arrows fire only `change`, which is already
  // wired above, so the busy flag is unnecessary there. Add a
  // ~1.2 s grace period after release so the network round-trip
  // to the box (and its own state update) does not race with us.
  const beginBusy = () => { state.musicVolBusy = true; };
  const endBusy = () => {
    state.musicVolBusy = false;
    state.musicVolUntil = Date.now() + 1200;
  };
  musicVolEl.addEventListener('pointerdown', beginBusy);
  musicVolEl.addEventListener('pointerup', endBusy);
  musicVolEl.addEventListener('pointercancel', endBusy);
  musicVolEl.addEventListener('pointerleave', () => {
    if (state.musicVolBusy) endBusy();
  });
}

// syncMusicTabVolumeFromBox refreshes the music-tab slider so
// hardware-button volume changes on the box (or any other client
// changing the volume out from under us) show up here within ~2 s.
// Called from refreshStatus on every poll. Cheap to call: BoxSettings
// caches well on the agent side.
async function syncMusicTabVolumeFromBox() {
  const box = state.currentBox;
  if (!box || !musicVolEl) return;
  if (state.view !== 'box') return;
  if (state.musicVolBusy) return;
  if (Date.now() < (state.musicVolUntil || 0)) return;
  try {
    const data = await BoxSettings(box.host, box.port);
    const vol = (data && data.volume && data.volume.actual);
    if (typeof vol !== 'number') return;
    const current = parseInt(musicVolEl.value, 10);
    if (current !== vol) {
      musicVolEl.value = String(vol);
      if (musicVolValEl) musicVolValEl.textContent = String(vol);
    }
  } catch {}
}

// checkSshBanner queries /api/stick/status to find out whether SSH is
// open on the current speaker and toggles the global top banner
// accordingly. Called on every refreshStatus + discoverBoxes so the
// warning shows up before the user even visits the Settings tab.
async function checkSshBanner() {
  const gb = $('globalSecurityBanner');
  if (!gb) return;
  const box = state.currentBox;
  // The Setup tab has no current speaker context, so the banner would
  // be free-floating and just noise. Otherwise check sshOpen status.
  if (!box || state.view === 'setup') { gb.classList.add('hidden'); return; }
  // OTA window: agent restarts, SSH may flap, and the banner's
  // "Reboot now" button would interrupt the agent exec. Suppress
  // until doBoxUpdate clears the flag (finally{} guaranteed).
  if (state.otaInProgress) { gb.classList.add('hidden'); return; }
  try {
    const r = await boxFetch(box, '/api/stick/status');
    if (!r.ok) return;
    const data = await r.json();
    // Only warn once the stick is no longer mounted on the box. While
    // mounted, the agent is mid-setup or mid-update — SSH is expected
    // to be open, the user cannot act on the warning yet, and the
    // banner is just noise. After the stick is removed (or the next
    // setup phase has unmounted it), the SSH state is meaningful.
    const show = !!(data && data.sshOpen && !data.mounted);
    gb.classList.toggle('hidden', !show);
  } catch {}
}

// loadMusicTabVolume fetches the current volume on a tab switch so
// the slider position is in sync.
async function loadMusicTabVolume() {
  const box = state.currentBox;
  if (!box || !musicVolEl) return;
  try {
    const data = await BoxSettings(box.host, box.port);
    const vol = (data && data.volume && data.volume.actual) || 0;
    musicVolEl.value = String(vol);
    if (musicVolValEl) musicVolValEl.textContent = String(vol);
  } catch {}
}
$('searchBtn').onclick = () => doSearch();
$('topBtn').onclick = () => doTop();
$('favModeBtn').onclick = () => loadFavorites();
updateFavModeBtn();
// Discreet pointer for the few users who want a station that radio-browser.info
// does not list yet: they can add it there and it shows up here after a while.
{ const ah = $('addStationHint'); if (ah) ah.onclick = (e) => { e.preventDefault(); try { BrowserOpenURL('https://www.radio-browser.info/'); } catch {} }; }
$('loadMoreBtn').onclick = () => loadMore();
$('searchQ').onkeydown = (e) => { if (e.key === 'Enter') doSearch(); };
$('searchQ').oninput = () => {
  $('searchQ').classList.toggle('has-query', !!$('searchQ').value.trim());
};
$('searchCountry').onchange = () => {
  state.searchCountry = $('searchCountry').value;
  // A country change resets the language to "all". Otherwise a
  // country/language mismatch filter would empty the results.
  state.searchLang = '';
  const ls = $('searchLang');
  if (ls) ls.value = '';
  updateFilterIndicators();
  try { localStorage.setItem('userTouchedRegion', '1'); } catch {}
  saveSearchCountry(state.searchCountry);
  // Reload the language list scoped to the selected country so the
  // counts reflect stations in THIS country, not the global pool.
  state.languages = [];
  loadLanguagesForCountry();
  // Country-boost pills depend on the selected country — re-render
  // so the highlighted row matches. Collapse the "More" expansion
  // because the previous tail may no longer apply.
  state.showMoreGenres = false;
  renderGenreChips();
  doRefilter();
};

async function loadLanguagesForCountry() {
  if (!state.currentBox) return;
  try {
    const cc = state.searchCountry || '';
    const path = cc
      ? `/api/radio/languages?country=${encodeURIComponent(cc)}&limit=60`
      : `/api/radio/languages?limit=40`;
    const r = await boxFetch(state.currentBox, path);
    if (r.ok) {
      state.languages = await r.json() || [];
      renderLanguageOptions();
    }
  } catch {}
}
$('searchLang').onchange    = () => {
  state.searchLang = $('searchLang').value;
  updateFilterIndicators();
  try { localStorage.setItem('userTouchedRegion', '1'); } catch {}
  doRefilter();
};

// updateFilterIndicators setzt die has-filter CSS Klasse auf jene Filter
// Dropdowns die einen anderen Wert als "alle" haben. Damit erkennt der
// User sofort wo aktiv gefiltert wird.
function updateFilterIndicators() {
  const cc = $('searchCountry');
  const lang = $('searchLang');
  if (cc) cc.classList.toggle('has-filter', !!cc.value);
  if (lang) lang.classList.toggle('has-filter', !!lang.value);
}
updateFilterIndicators();
$('searchOrder').onchange   = () => { state.searchOrder   = $('searchOrder').value;   doRefilter(); };
$('searchOnlyOK').onchange  = () => { state.searchOnlyOK  = $('searchOnlyOK').checked; doRefilter(); };
$('searchOnlyBose').onchange = () => { state.searchOnlyBose = $('searchOnlyBose').checked; renderSearchResults(); };
$('searchBitrate').onchange = () => { state.searchMinBitrate = parseInt($('searchBitrate').value, 10) || 0; renderSearchResults(); };
$('pickCancel').onclick = closePick;

// doRefilter re-runs the last action (Top or Search) with the new
// filters but keeps the existing query string.
function doRefilter() {
  state.searchOffset = 0;
  if (state.searchLastMode === 'search' && state.searchLastQuery) {
    doSearch();
  } else {
    doTop();
  }
}

async function discoverBoxes() {
  const hadBoxes = state.boxes.length > 0;
  if (!hadBoxes) {
    // First search: explicit message so the user understands the
    // app is doing something.
    $('boxSelect').textContent = t('speaker.searching');
  } else {
    // Background refresh: the refresh icon spins, the existing list
    // stays visible.
    const rb = $('refreshBtn');
    if (rb) rb.classList.add('spinning');
  }
  try {
    const list = await DiscoverBoxes(4);
    state.boxes = applyPendingNames(list || []);
    saveCachedBoxes(state.boxes);
    if (state.currentBox && state.currentBox.deviceID) {
      const fresh = state.boxes.find(b => b.deviceID === state.currentBox.deviceID);
      if (fresh) {
        const changed = fresh.host !== state.currentBox.host
                     || fresh.port !== state.currentBox.port
                     || fresh.version !== state.currentBox.version
                     || fresh.friendlyName !== state.currentBox.friendlyName;
        state.currentBox = fresh;
        if (changed) {
          state.presets = [];
          state.searchResults = [];
          state.nowLocation = '';
          state.nowPlayState = '';
          state.presetErrors = {};
          $('presets').innerHTML = '';
          $('searchResults').innerHTML = '';
          loadPresets();
          refreshStatus();
          checkBoxUpdate();
        }
        updateSourceButtonVisibility();
      } else {
        state.currentBox = null;
        state.presets = [];
        $('presets').innerHTML = '';
      }
    }
    renderBoxSelect();
    updateSettingsTabBadge();
    // Setup-tab target picker reuses the same state.boxes feed.
    // Re-render so newly arrived speakers appear as choices without
    // making the user leave and re-enter the Setup tab.
    renderSetupTargetPicker();
    // Auto retry: if a recently set up speaker has not yet
    // re-announced its new name via mDNS, search again every 4 s for
    // up to 90 s. Driven by pendingNames.
    scheduleNextAutoRefresh();
  } catch (e) {
    if (!hadBoxes) $('boxSelect').textContent = t('common.error') + ': ' + e;
  } finally {
    const rb = $('refreshBtn');
    if (rb) rb.classList.remove('spinning');
  }
}

let _autoRefreshTimer = null;
function scheduleNextAutoRefresh() {
  if (_autoRefreshTimer) clearTimeout(_autoRefreshTimer);
  const now = Date.now();
  const stillPending = Object.keys(state.pendingNames).some(
    id => state.pendingNames[id].until > now
  );
  if (!stillPending) return; // everything already converged
  _autoRefreshTimer = setTimeout(() => {
    _autoRefreshTimer = null;
    discoverBoxes();
  }, 4000);
}

// applyPendingNames overrides the friendlyName from mDNS with our
// locally stored value while the stick has not yet re-announced.
// Entries expire at state.pendingNames[id].until.
function applyPendingNames(list) {
  const now = Date.now();
  // Drop expired entries.
  for (const id of Object.keys(state.pendingNames)) {
    if (now > state.pendingNames[id].until) delete state.pendingNames[id];
  }
  // If the stick is already reporting the new name, clear the pending
  // entry.
  return list.map(b => {
    const p = state.pendingNames[b.deviceID];
    if (!p) return b;
    if ((b.friendlyName || '') === p.name) {
      delete state.pendingNames[b.deviceID];
      return b;
    }
    return { ...b, friendlyName: p.name };
  });
}

function renderBoxSelect() {
  const sel = $('boxSelect');
  if (state.boxes.length === 0) {
    sel.innerHTML = `
      <div class="empty-state">
        <div class="empty-state-title">${escapeHtml(t('speaker.emptyTitle'))}</div>
        <div class="empty-state-text">
          ${escapeHtml(t('speaker.emptyHelp1'))}
          <br><br>
          ${escapeHtml(t('speaker.emptyHelp2'))}
        </div>
        <div class="empty-state-buttons">
          <button class="btn btn-mini" id="emptyRetry">${escapeHtml(t('speaker.retry'))}</button>
          <button class="btn btn-primary btn-mini" id="emptyGoSetup">${escapeHtml(t('speaker.goSetup'))}</button>
        </div>
      </div>`;
    const go = document.getElementById('emptyGoSetup');
    if (go) go.onclick = () => switchView('setup');
    const rt = document.getElementById('emptyRetry');
    if (rt) rt.onclick = () => discoverBoxes();
    updateBoxUiVisibility();
    return;
  }
  sel.innerHTML = state.boxes.map(b => {
    const isStock = b.kind === 'stock';
    const active = state.currentBox && state.currentBox.host === b.host && !isStock ? ' active' : '';
    const stockCls = isStock ? ' stock' : '';
    const label = b.friendlyName || b.name || b.host;
    // Model (e.g. "SoundTouch 10") right next to the name so users
    // with several speakers can tell ST10 from ST20 at a glance.
    // Fall back gracefully when an older agent only advertises the
    // generic "SoundTouch".
    const model = b.model && b.model !== 'SoundTouch'
      ? `<span class="box-model" title="${escapeAttr(t('speaker.modelTitle'))}">${escapeHtml(b.model)}</span>`
      : '';
    if (isStock) {
      return `<span class="box-btn${stockCls}" data-host="${b.host}" data-port="${b.port}" data-stock="1" role="button" tabindex="0" title="${escapeAttr(t('speaker.stockTooltip'))}">${escapeHtml(label)}${model} <small>${b.host}</small><span class="box-stock-badge">${escapeHtml(t('speaker.needsInstallBadge'))}</span></span>`;
    }
    const ver = b.version ? `<span class="box-ver" title="${escapeAttr(t('speaker.stickVersionTitle'))}">${escapeHtml(b.version)}</span>` : '';
    // Red dot when this speaker's agent is older than the app's embedded
    // one: a glanceable "update available" cue right on the speaker button
    // itself, in addition to the settings-tab badge and the music-tab
    // banner (#108).
    const updCls = boxNeedsUpdate(b) ? ' needs-update' : '';
    const updDot = boxNeedsUpdate(b)
      ? `<span class="box-update-dot" title="${escapeAttr(t('speaker.updateBadgeTitle'))}" aria-label="${escapeAttr(t('speaker.updateBadgeTitle'))}"></span>`
      : '';
    return `<span class="box-btn${active}${updCls}" data-host="${b.host}" data-port="${b.port}" role="button" tabindex="0">${escapeHtml(label)}${model} <small>${b.host}</small>${ver}${updDot}<span class="box-edit" data-host="${b.host}" data-port="${b.port}" title="${escapeAttr(t('speaker.editTitle'))}">&#9881;</span></span>`;
  }).join('');
  sel.querySelectorAll('.box-btn').forEach(btn => {
    btn.onclick = async (e) => {
      // A click on the gear icon opens the settings view rather than
      // selecting the speaker.
      if (e.target.closest('.box-edit')) return;
      const host = btn.dataset.host;
      const port = parseInt(btn.dataset.port, 10);
      const box = state.boxes.find(b => b.host === host && b.port === port);
      if (!box) return;
      if (box.kind === 'stock') {
        // Stock speaker: not an error, this is the happy path. The user
        // found a Bose speaker they can revive with STR. Invite them to
        // the USB stick setup with a positive CTA (no warning triangle,
        // no red "proceed anyway") instead of a danger prompt.
        const label = box.friendlyName || box.name || box.host;
        const ok = await confirmWarn(
          t('speaker.stockConfirmTitle'),
          t('speaker.stockConfirmBody', { label: escapeHtml(label) }),
          { icon: null, confirmLabel: t('speaker.stockConfirmCta'), confirmClass: 'btn btn-primary' },
        );
        if (ok) switchView('setup');
        return;
      }
      selectBox(box);
    };
  });
  // Gear click: set settingsBox and switch the tab.
  sel.querySelectorAll('.box-edit').forEach(icon => {
    icon.onclick = (e) => {
      e.stopPropagation();
      const host = icon.dataset.host;
      const port = parseInt(icon.dataset.port, 10);
      const box = state.boxes.find(b => b.host === host && b.port === port);
      if (!box) return;
      state.settingsBox = box;
      switchView('settings');
    };
  });
  if (!state.currentBox) {
    // Auto-select only STR speakers. Stock speakers cannot be
    // controlled and would put the music tab into a permanent
    // "loading" state.
    const strBoxes = state.boxes.filter(b => b.kind !== 'stock');
    const lastID = loadLastBox();
    let target = lastID ? strBoxes.find(b => b.deviceID === lastID) : null;
    if (!target && strBoxes.length === 1) target = strBoxes[0];
    if (target) selectBox(target);
  }
  updateBoxUiVisibility();
}

function selectBox(box) {
  state.currentBox = box;
  if (box && box.deviceID) saveLastBox(box.deviceID);
  state.presetErrors = {};
  renderBoxSelect();
  loadPresets();
  refreshStatus();
  checkBoxUpdate();
  loadTaxonomy();
  // Pull the current volume so the music-tab slider does not start
  // at 0 — otherwise the first touch yanks it to whatever value the
  // slider was last left at. The tab-switch path in switchView()
  // also calls this, but a tab switch is not always involved (the
  // box-select buttons can fire without leaving the music view).
  loadMusicTabVolume();
  // Fetch the stick's region and use it as a default for radio search.
  // Do not overwrite a country the user has already picked manually.
  loadStickRegion();
  // Some models do not have Bluetooth hardware. Hide the source
  // button for those instead of letting the user click it and hit
  // the box's 1005 UNKNOWN_SOURCE_ERROR.
  updateSourceButtonVisibility();
}

// updateSourceButtonVisibility hides source buttons for hardware that
// the currently-selected box does not have. Run after every selectBox()
// AND after every discovery refresh so the visibility tracks model
// detection that lands later (Bose stock /info enrichment).
//
// Two layers: a model-name heuristic gives an immediate answer (the
// SoundTouch Portable has no Bluetooth), then the box's actual /sources
// list refines it. The list is authoritative and model-agnostic, so it
// also catches ST20 hardware variants that ship without Bluetooth (see
// issue #102, where the box answered a BT /select with 1005
// UNKNOWN_SOURCE_ERROR). AUX and STANDBY exist on every model.
async function updateSourceButtonVisibility() {
  const btBtn = document.querySelector('.btn-source[data-source="BLUETOOTH"]');
  if (!btBtn || !state.currentBox) return;
  const model = (state.currentBox.model) || '';
  // Immediate heuristic so the button is correct before the async
  // source list arrives.
  btBtn.classList.toggle('hidden', /portable/i.test(model));
  const box = state.currentBox;
  try {
    const settings = await BoxSettings(box.host, box.port);
    // Guard against a box switch while the request was in flight.
    if (state.currentBox !== box) return;
    const sources = (settings && settings.sources) || [];
    // Only trust a non-empty list; an empty one means the box did not
    // answer /sources and we keep the heuristic result.
    if (Array.isArray(sources) && sources.length) {
      const hasBT = sources.some(s => (s.source || '').toUpperCase() === 'BLUETOOTH');
      btBtn.classList.toggle('hidden', !hasBT);
    }
  } catch {
    // Keep the heuristic result on any error.
  }
}

// boxFetch is a self-healing fetch for the agent's plain-HTTP endpoints
// (region, radio search/tags/languages, stick status). Unlike the Go
// bindings it cannot reuse boxDo, so it replicates the same resilience in
// JS: a hard timeout, so a flaky port can never hang the UI forever (the
// "region keeps loading" bug on BCO boxes), plus a :8888 <-> :17008
// failover for BCO speakers where only one of the two answers. The first
// reachable port is remembered on the box so later calls go straight to it.
async function boxFetch(box, path, opts = {}, timeoutMs = 8000) {
  if (!box) throw new Error('no box');
  const ports = [...new Set([box.port, 17008, 8888].filter(Boolean))];
  let lastErr;
  for (const p of ports) {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), timeoutMs);
    try {
      const r = await fetch(`http://${box.host}:${p}${path}`, { ...opts, signal: ctrl.signal });
      clearTimeout(timer);
      if (p !== box.port) box.port = p; // remember the reachable port
      return r;
    } catch (e) {
      clearTimeout(timer);
      lastErr = e;
    }
  }
  throw lastErr || new Error('box unreachable');
}

let regionLoaded = false;
async function loadStickRegion() {
  if (regionLoaded || !state.currentBox) return;
  try {
    const r = await boxFetch(state.currentBox, '/api/region');
    if (!r.ok) return;
    const data = await r.json();
    if (data && data.country) {
      // Only set defaults if the user has not touched the region.
      const userTouched = (() => { try { return !!localStorage.getItem('userTouchedRegion'); } catch { return false; }})();
      if (!userTouched) {
        state.searchCountry = data.country;
        const cs = $('searchCountry');
        if (cs) cs.value = data.country;
      }
      // Default the language filter to the APP language, not the stick
      // region's language and not a last-used value. Only when the app
      // locale has no obvious radio-browser language do we fall back to the
      // region's language.
      if (!state.searchLang) {
        state.searchLang = LOCALE_TO_RADIO_LANG[getLocale()] || data.language || '';
      }
      updateFilterIndicators();
      // Re-render the language dropdown so the locale-based default is
      // injected and selected even when this resolves after loadTaxonomy
      // (the two run concurrently on box selection).
      renderLanguageOptions();
      regionLoaded = true;
    }
  } catch {}
}

// loadTaxonomy fetches the genre tag list and the language list from
// the stick once, then renders the genre chips and the language
// dropdown.
async function loadTaxonomy() {
  if (!state.currentBox) return;
  if (state.tags.length === 0) {
    try {
      const r = await boxFetch(state.currentBox, '/api/radio/tags?limit=24');
      if (r.ok) {
        state.tags = await r.json() || [];
        renderGenreChips();
      }
    } catch {}
  }
  if (state.languages.length === 0) {
    try {
      const r = await boxFetch(state.currentBox, '/api/radio/languages?limit=40');
      if (r.ok) {
        state.languages = await r.json() || [];
        renderLanguageOptions();
      }
    } catch {}
  }
}

// Tracks whether the user has clicked "Mehr Genres" to expand the
// long tail of auto-fetched tags. Resets on every fresh page load.
state.showMoreGenres = false;

function renderGenreChips() {
  const wrap = $('genreChips');
  if (!wrap) return;

  // 1. Aggregate the live counts from radio-browser so each chip can
  //    show "N Sender" in its tooltip. State.tags may be empty on
  //    first paint — that's fine, core chips still render with 0.
  const liveCounts = {};
  for (const t of state.tags) {
    const canon = canonGenre(t.name);
    if (!canon) continue;
    liveCounts[canon] = (liveCounts[canon] || 0) + (t.stationcount || 0);
  }

  // 2. Country-boost pills (max 2). state.searchCountry is the user's
  //    selected country in the search filter — it falls back to the
  //    stick's region.txt if the user hasn't manually picked one.
  const cc = (state.searchCountry || '').toUpperCase();
  const boost = GENRE_BY_COUNTRY[cc] || [];

  const chipHtml = (canon, label, count, extraClass) => {
    const active = state.searchTag === canon ? ' active' : '';
    const cls = ['chip', active.trim(), extraClass || ''].filter(Boolean).join(' ');
    const title = count > 0 ? t('search.nStations', { n: formatNumber(count) }) : '';
    return `<button class="${cls}" data-tag="${escapeAttr(canon)}" title="${escapeAttr(title)}">${escapeHtml(label)}</button>`;
  };
  const labelFor = (canon) => translateGenre(canon) || canon.replace(/\b\w/g, c => c.toUpperCase());

  const seen = new Set();
  const parts = [];

  parts.push('<button class="chip' + (!state.searchTag ? ' active' : '') + '" data-tag="">' + escapeHtml(t('search.allGenres')) + '</button>');

  for (const canon of boost) {
    if (!canon || seen.has(canon)) continue;
    seen.add(canon);
    parts.push(chipHtml(canon, labelFor(canon), liveCounts[canon] || 0, 'chip--boost'));
  }
  for (const canon of GENRE_CORE) {
    if (seen.has(canon)) continue;
    seen.add(canon);
    parts.push(chipHtml(canon, labelFor(canon), liveCounts[canon] || 0));
  }

  // 3. Long tail: tags from state.tags that the user might recognise
  //    but we did not promote into the core set. Shown only when the
  //    user expands via "Mehr Genres".
  const tail = Object.keys(liveCounts)
    .filter(canon => !seen.has(canon) && liveCounts[canon] > 0)
    .map(canon => ({ canon, count: liveCounts[canon] }))
    .sort((a, b) => b.count - a.count)
    .slice(0, 24);

  if (state.showMoreGenres) {
    for (const t of tail) {
      seen.add(t.canon);
      parts.push(chipHtml(t.canon, labelFor(t.canon), t.count));
    }
  }

  // 4. Toggle button at the end. Hidden when there is no tail to
  //    reveal, or when the currently selected tag is in the tail (so
  //    the user does not lose their selection by collapsing).
  const showSelectedInTail = state.searchTag && !seen.has(state.searchTag);
  if (tail.length > 0 || state.showMoreGenres) {
    const label = state.showMoreGenres ? t('search.fewerGenres') : t('search.moreGenres', { n: tail.length });
    parts.push(`<button class="chip chip--more" id="genreMoreToggle">${escapeHtml(label)}</button>`);
  }
  if (showSelectedInTail) {
    // Selection points to a tag the tail dropdown is hiding — force
    // expand so the user sees their own filter.
    state.showMoreGenres = true;
  }

  wrap.innerHTML = parts.join('');
  wrap.querySelectorAll('.chip').forEach(btn => {
    btn.onclick = () => {
      if (btn.id === 'genreMoreToggle') {
        state.showMoreGenres = !state.showMoreGenres;
        renderGenreChips();
        return;
      }
      state.searchTag = btn.dataset.tag || '';
      renderGenreChips();
      doRefilter();
    };
  });
}

// localizeLanguageName looks up the display name for a language emitted
// by radio-browser.info. The API hands us lowercased English names
// ("german", "english", ...). The i18n bundle holds the per-locale
// translation under `lang.<name>`. Unknown languages fall back to a
// capitalised version of the raw API value.
// radio-browser hands us lowercase English language names; map the
// common ones to ISO 639 codes so Intl.DisplayNames can localize them
// into the active app language (works for every locale, no per-language
// tables).
const LANG_NAME_TO_CODE = {
  german: 'de', english: 'en', french: 'fr', spanish: 'es', italian: 'it',
  dutch: 'nl', portuguese: 'pt', russian: 'ru', polish: 'pl', turkish: 'tr',
  arabic: 'ar', japanese: 'ja', chinese: 'zh', mandarin: 'zh', cantonese: 'yue',
  swedish: 'sv', norwegian: 'nb', danish: 'da', finnish: 'fi', czech: 'cs',
  hungarian: 'hu', romanian: 'ro', greek: 'el', ukrainian: 'uk', bulgarian: 'bg',
  croatian: 'hr', serbian: 'sr', slovak: 'sk', slovenian: 'sl', estonian: 'et',
  latvian: 'lv', lithuanian: 'lt', irish: 'ga', welsh: 'cy', catalan: 'ca',
  galician: 'gl', basque: 'eu', icelandic: 'is', hindi: 'hi', thai: 'th',
  vietnamese: 'vi', korean: 'ko', indonesian: 'id', malay: 'ms', persian: 'fa',
  hebrew: 'he', bengali: 'bn', tamil: 'ta', urdu: 'ur', maltese: 'mt',
};
let _langDN = null;
let _langDNLocale = null;
// localizeLanguageName localizes a radio-browser language name to the
// active app language via Intl.DisplayNames (per-locale cached), falling
// back to the i18n lang table, then a capitalized form of the raw name.
function localizeLanguageName(name) {
  if (!name) return '';
  // Localize to the active app language via Intl.DisplayNames, exactly like
  // regionName does for countries (the Wails webview is Chromium and honours
  // the locale argument, as the localized country dropdown proves). So a
  // French UI shows French language names, Dutch shows Dutch, and so on,
  // for every app language without hand-translated tables. Cached per locale.
  const code = LANG_NAME_TO_CODE[name.toLowerCase().trim()];
  if (code) {
    try {
      const loc = getLocale();
      if (_langDNLocale !== loc) {
        _langDN = new Intl.DisplayNames([loc], { type: 'language' });
        _langDNLocale = loc;
      }
      const n = _langDN.of(code);
      if (n && n.toLowerCase() !== code) return n;
    } catch (_) {
      // Intl unavailable / bad code: fall through to the table.
    }
  }
  // Names not in the code map (e.g. "american english"), or if Intl failed:
  // the i18n lang table (selected language for de/en, else English), then the
  // raw radio-browser name title-cased.
  const translated = tLookup('lang', name.toLowerCase().trim());
  if (translated) return translated;
  return name.replace(/\b\w/g, (c) => c.toUpperCase());
}

function renderLanguageOptions() {
  const sel = $('searchLang');
  if (!sel) return;
  const sorted = (state.languages || [])
    .filter((l) => l.name)
    .map((l) => ({ name: l.name, stationcount: l.stationcount, label: localizeLanguageName(l.name) }));
  // Ensure the language matching the app UI locale is always selectable,
  // even when radio-browser's top-N-by-count list omits it (smaller
  // languages like Lithuanian or Latvian fall outside the limit).
  // Without this the locale-based pre-selection (LOCALE_TO_RADIO_LANG)
  // cannot apply, because the matching <option> would not exist.
  const want = state.searchLang || LOCALE_TO_RADIO_LANG[getLocale()] || '';
  if (want && !sorted.some((l) => l.name.toLowerCase() === want.toLowerCase())) {
    sorted.push({ name: want, stationcount: null, label: localizeLanguageName(want) });
  }
  // Sort alphabetically by the localized display name, consistent with
  // the country dropdown. The API returns languages by station count.
  sorted.sort((a, b) => a.label.localeCompare(b.label));
  const opts = [`<option value="">${escapeHtml(t('search.allLanguages'))}</option>`];
  for (const l of sorted) {
    const count = (l.stationcount == null) ? '' : ` (${l.stationcount})`;
    opts.push(`<option value="${escapeAttr(l.name)}">${escapeHtml(l.label)}${count}</option>`);
  }
  sel.innerHTML = opts.join('');
  sel.value = state.searchLang;
}

// updateSettingsTabBadge shows a small blue dot on the speaker
// settings tab whenever at least one discovered speaker reports a
// version or build stamp different from the desktop app's own. The
// dot signals: there is work to do in this tab, namely OTA-update
// at least one speaker.
//
// Compared against BOTH version and build because two local dev
// builds often share the same `git describe` version but carry
// distinct build stamps. Without the build check the badge would
// silently agree while the speaker-settings status line is
// screaming "update available".
//
// Version + build data comes from the mDNS TXT record so no extra
// HTTP call is needed. The badge updates as the speaker list
// refreshes.
// boxNeedsUpdate decides whether a single discovered speaker is running
// an agent older than the desktop app's embedded one. A box is flagged
// when its version OR build stamp differs from the app's. Two local dev
// builds often share the same `git describe` version but carry distinct
// build stamps, so both halves matter (see updateSettingsTabBadge).
//
// Returns false for stock boxes (no agent yet — that is a "needs install"
// case, handled separately) and when the app version is not yet known.
function boxNeedsUpdate(b) {
  if (!b || b.kind === 'stock' || !b.version) return false;
  const appVer   = state.appInfo && state.appInfo.version;
  const appBuild = state.appInfo && state.appInfo.build;
  if (!appVer) return false;
  const verDiffers   = b.version !== appVer;
  // Three build-related cases to flag as drift:
  //   - both sides populated and different
  //   - we have a build, box has none (older agent that does not
  //     yet broadcast `build=` in mDNS — guaranteed pre-update)
  const buildDiffers = appBuild && b.build && b.build !== appBuild;
  const buildMissing = appBuild && !b.build;
  return verDiffers || buildDiffers || buildMissing;
}

function updateSettingsTabBadge() {
  const btn = document.querySelector('.tab-btn[data-view="settings"]');
  if (!btn) return;
  const needsUpdate = state.boxes.some(boxNeedsUpdate);
  btn.classList.toggle('has-update', needsUpdate);
}

async function checkBoxUpdate() {
  if (!state.currentBox || !state.appInfo) return;
  const banner = $('boxUpdateBanner');
  banner.classList.add('hidden');
  // If an OTA is in flight on a DIFFERENT box, the update button on
  // the currently-viewed box must be locked. We still need the
  // banner to be visible so the user has a clear reason for the
  // disabled state. The version-mismatch check runs first so we
  // know whether to show the banner at all; the OTA gate then
  // decides what to put inside it.
  const otaElsewhere = state.otaInProgress && state.otaTargetHost && state.otaTargetHost !== state.currentBox.host;
  const renderUpdateBtn = () => {
    if (otaElsewhere) {
      return `<button class="btn btn-primary btn-mini" id="boxUpdateBtn" disabled>${escapeHtml(t('update.otherBoxRunning', { name: state.otaTargetName || '...' }))}</button>`;
    }
    return `<button class="btn btn-primary btn-mini" id="boxUpdateBtn">${escapeHtml(t('update.refreshBtn'))}</button>`;
  };
  try {
    const v = await BoxAgentVersion(state.currentBox.host, state.currentBox.port);
    const boxVer = v.version || t('common.unknown');
    const boxBuild = v.build || '';
    const appVer = state.appInfo.version;
    const appBuild = state.appInfo.build || '';
    // Show the banner on any version OR build difference. Stamp-only
    // drift used to be ignored as "not alarming enough", but in
    // practice it is exactly the case the speaker-settings status
    // line already flags as an update — keeping the music-tab banner
    // silent in that situation produced confusing inconsistency
    // across the two tabs.
    const sameVer   = boxVer === appVer;
    const sameBuild = boxBuild === appBuild;
    if (sameVer && sameBuild) return;
    const boxLabel = boxBuild ? `${boxVer} (Build ${boxBuild})` : boxVer;
    const appLabel = appBuild ? `${appVer} (Build ${appBuild})` : appVer;
    banner.innerHTML = `
      <div class="update-msg">
        <b>${escapeHtml(t('update.speakerUpdateAvail'))}</b><br>
        <small>${escapeHtml(t('update.speakerAppLine', { box: boxLabel, app: appLabel }))}</small><br>
        <small class="muted">${escapeHtml(t('update.rebootNote'))}</small>
      </div>
      ${renderUpdateBtn()}
    `;
    banner.classList.remove('hidden');
    if (!otaElsewhere) $('boxUpdateBtn').onclick = doBoxUpdate;
  } catch {
    if (state.currentBox.version && state.currentBox.version !== state.appInfo.version) {
      banner.innerHTML = `
        <div class="update-msg">
          <b>${escapeHtml(t('update.speakerUpdateAvail'))}</b><br>
          <small>${escapeHtml(t('update.speakerRunningOld', { boxVersion: state.currentBox.version, appVersion: state.appInfo.version }))}</small><br>
          <small class="muted">${escapeHtml(t('update.rebootNote'))}</small>
        </div>
        ${renderUpdateBtn()}
      `;
      banner.classList.remove('hidden');
      if (!otaElsewhere) $('boxUpdateBtn').onclick = doBoxUpdate;
    }
  }
}

async function doBoxUpdate() {
  if (!state.currentBox) return;
  // Hard-lock: while an OTA is in flight on ANY box, refuse to start
  // a second one. The UI also renders the button disabled in that
  // case via checkBoxUpdate(), but the redundant check here guards
  // against races where the user clicked through a stale render.
  if (state.otaInProgress) return;

  // Stick gate, checked at the single OTA chokepoint so EVERY caller
  // (the music-tab banner button and the stick-info button) is covered.
  // A USB stick still in the speaker means rc.local re-copies the
  // stick's (older) version on the next boot and undoes the OTA; the OTA
  // also reboots the box. So if a stick is mounted, ask first and let
  // Cancel abort cleanly BEFORE anything starts. Earlier this gate only
  // sat on the stick-info button, so the banner button started the OTA
  // (and the reboot) with no confirmation.
  try {
    const r = await boxFetch(state.currentBox, '/api/stick/status');
    if (r.ok) {
      const data = await r.json();
      if (data && data.mounted) {
        const ok = await confirmWarn(t('update.stickInTitle'), t('update.stickInBody'));
        if (!ok) return; // user cancelled: no OTA, no reboot
      }
    }
  } catch { /* status unknown: do not block the update */ }

  // Wi-Fi pre-flight: a weak link can drop the OTA upload mid-transfer and
  // leave the speaker half-updated, then rebooting. If the box reports a
  // marginal/poor Wi-Fi signal, warn first so the user can move the speaker or
  // router closer before committing. Ethernet/coprocessor boxes report no
  // signal class and are never blocked; an unknown reading never blocks.
  try {
    const s = await BoxSettings(state.currentBox.host, state.currentBox.port);
    const ifs = (s && s.network && s.network.interfaces) || [];
    const conn = ifs.find(i =>
      (i.type === 'WIFI_INTERFACE' && i.state === 'NETWORK_WIFI_CONNECTED') ||
      (i.state === 'NETWORK_ETHERNET_CONNECTED' && i.ipAddress));
    const sig = conn && conn.signal;
    if (sig === 'MARGINAL_SIGNAL' || sig === 'POOR_SIGNAL') {
      const ok = await confirmWarn(t('update.weakWifiTitle'), t('update.weakWifiBody'));
      if (!ok) return; // user chose to improve the signal first
    }
  } catch { /* signal unknown: do not block the update */ }

  // Drive both update buttons together (banner up top + stick info section)
  const buttons = () => ['boxUpdateBtn', 'stickInfoUpdateBtn'].map(id => $(id)).filter(Boolean);
  // Mutate the DOM buttons only when the user is still LOOKING at
  // the box being updated. If they switched to another box,
  // checkBoxUpdate() has rendered a fresh button for that other
  // box — overwriting it with our progress text would lie about
  // what the other box is doing. The state.otaTargetHost guard
  // below in checkBoxUpdate() takes care of rendering the right
  // "Update running on <name>" label there.
  const setStatus = (text) => {
    if (!state.currentBox || state.currentBox.host !== state.otaTargetHost) return;
    buttons().forEach(b => { b.textContent = text; b.disabled = true; });
  };
  const reset = () => {
    if (!state.currentBox || state.currentBox.host !== state.otaTargetHost) return;
    buttons().forEach(b => { b.disabled = false; b.textContent = t('update.refreshBtn'); });
  };
  // Mark this box as the OTA target AND flip the global in-flight
  // flag BEFORE first setStatus() so checkBoxUpdate() and the
  // setStatus guard both see a consistent (target, in-flight)
  // pair at every point in this flow. Reset together in finally{}.
  state.otaTargetHost = state.currentBox.host;
  state.otaTargetName = state.currentBox.friendlyName || state.currentBox.name || state.currentBox.host;
  // Suppress the SSH "remove stick and reboot" banner for the whole
  // OTA window. The agent restarts mid-OTA and SSH is briefly open
  // during that restart; the banner's "Reboot now" button would
  // interrupt the agent exec and may leave the box half-flashed.
  state.otaInProgress = true;
  setStatus(t('update.uploading'));
  checkSshBanner();
  const targetBox = state.currentBox;
  const appBuild  = state.appInfo && state.appInfo.build;
  try {
    await UpdateBoxAgent(targetBox.host, targetBox.port);
    // Agent has accepted the binary. It will detach, sleep ~70 s
    // (TIME_WAIT for listener ports — see internal/webui handleAgentUpdate),
    // then exec the new binary. During that whole window the box is
    // unreachable; the previous UI would re-enable the button after a
    // fixed 13 s timer, which invited the user to click again while
    // the agent was still mid-restart.
    showToast(t('update.uploadedToast'));
    setStatus(t('update.rebooting'));
    // Active poll: hit /api/agent/version until the box answers with
    // a build matching the app's appBuild. That is the success signal:
    // box back online AND running the new binary. The loop breaks the
    // instant that happens, so the user never waits past the real boot
    // — the deadline below is only a give-up ceiling, not a fixed wait.
    // Poll every 5 s. The ceiling is 6 minutes: a BCO box (Portable,
    // ST20-spotty) can reboot TWICE post-OTA (the OTA reboot, then a
    // bootstrap-sync reboot when the new binary's embedded run.sh differs
    // from NAND — project_ota_only_replaces_binary), each boot taking
    // ~40 s to the agent plus ~85 s until the :17008 REDIRECT makes it
    // reachable, plus the box's slow BoseApp. 3 minutes was too short
    // (live 2026-06-01: the box came back correctly on the new build but
    // only AFTER the window expired, so the button wrongly flipped back
    // to "Update"). As long as the build is wrong or the box is
    // unreachable, the buttons stay locked.
    const deadlineMs = Date.now() + 360_000;
    const pollIntervalMs = 5_000;
    // Phase state shared between the 1 s display ticker and the 5 s
    // polling loop: "answered-with-old-build" vs "not-answering-yet".
    // The previous code updated only after each 5 s poll, so the
    // visible counter jumped 5+ seconds at a time and looked frozen
    // mid-poll. Now the ticker re-renders every second, with the
    // current phase text picked from this variable.
    let lastPhase = 'waitingForSpeaker';
    const renderStatus = () => {
      const remaining = formatRemaining(deadlineMs - Date.now());
      const key = lastPhase === 'oldBuild' ? 'update.oldBuildWait' : 'update.waitingForSpeaker';
      setStatus(t(key, { remaining }));
    };
    renderStatus();
    const tickHandle = setInterval(renderStatus, 1000);
    let confirmed = false;
    let confirmedVer = null;
    try {
      while (Date.now() < deadlineMs) {
        await sleep(pollIntervalMs);
        try {
          const v = await BoxAgentVersion(targetBox.host, targetBox.port);
          if (v && v.build && (!appBuild || v.build === appBuild)) {
            confirmed = true;
            confirmedVer = v;
            break;
          }
          lastPhase = 'oldBuild';
        } catch {
          lastPhase = 'waitingForSpeaker';
        }
        renderStatus();
      }
    } finally {
      clearInterval(tickHandle);
    }
    if (confirmed) {
      showToast(t('update.doneToast'));
    } else {
      showToast(t('update.tookLongerToast'));
    }
    // Refresh app state regardless of confirmation so the user sees
    // current truth (either updated or still in OTA).
    await discoverBoxes();
    // Force the confirmed new version onto the box record(s) so the view
    // shows the updated version immediately instead of a stale "outdated"
    // glitch until the next clean discovery cycle (deqw + Jens 2026-06-01:
    // after OTA the screen kept the old version until a manual refresh).
    // This also overrides a discovery-stickiness cache entry that might
    // still carry the pre-OTA version for a box that just rebooted.
    if (confirmed && confirmedVer) {
      const patchVer = (b) => {
        if (b && b.host === targetBox.host) {
          if (confirmedVer.version) b.version = confirmedVer.version;
          if (confirmedVer.build) b.build = confirmedVer.build;
        }
      };
      patchVer(state.currentBox);
      if (Array.isArray(state.boxes)) state.boxes.forEach(patchVer);
    }
    checkBoxUpdate();
    if (state.view === 'settings') loadBoxSettings();
    reset();
  } catch (e) {
    showError(t('update.failed', { err: String(e) }));
    reset();
  } finally {
    // Always clear the OTA-in-flight gate so the SSH banner can
    // come back if it still applies, even if we threw mid-poll.
    state.otaInProgress = false;
    state.otaTargetHost = null;
    state.otaTargetName = null;
    checkSshBanner();
    // Force a re-render of the current view's update button so any
    // other-box "Update running on …" placeholder is replaced with
    // the regular Update button immediately.
    checkBoxUpdate();
  }
}

function updateBoxUiVisibility() {
  const hasBox = !!state.currentBox;
  const hasSTR = state.boxes.some(b => b.kind !== 'stock');
  // Show controls only when a selected STR speaker exists. Show the
  // "pick a speaker" hint when STR speakers exist but none is
  // selected; stock-only LAN scenarios fall through to the empty
  // state rendered by renderBoxSelect (the badge speaks for itself).
  $('boxControls').classList.toggle('hidden', !hasBox);
  $('boxHint').classList.toggle('hidden', !hasSTR || hasBox);
}

async function loadPresets(retry = 0) {
  if (!state.currentBox) return;
  if (state.presets.length === 0) {
    $('presets').innerHTML = `<div class="muted small grid-loading">${escapeHtml(t('preset.loading'))}</div>`;
  }
  try {
    state.presets = await GetPresets(state.currentBox.host, state.currentBox.port) || [];
    renderPresets();
    healPresetLogos();
  } catch {
    if (retry < 1) {
      setTimeout(() => loadPresets(retry + 1), 1500);
      return;
    }
    if (state.presets.length > 0) {
      renderPresets();
    } else {
      $('presets').innerHTML = `<div class="muted small">${escapeHtml(t('preset.speakerUnreachable'))}</div>`;
    }
  }
}

// healPresetLogos searches radio-browser for the station name of any
// preset that has no logo (legacy presets from the pre-logo era or
// presets added via hardware) and adopts the favicon. Persists the
// result back to the stick so it also shows up on the speaker
// display.
let healingInProgress = false;
async function healPresetLogos() {
  if (healingInProgress) return;
  if (!state.currentBox) return;
  const missing = state.presets.filter(p => !p.art && p.name);
  if (missing.length === 0) return;
  healingInProgress = true;
  try {
    await Promise.all(missing.map(async (p) => {
      try {
        // Intentionally tolerant: NO onlyok filter (even a station
        // flagged broken usually still has a logo). The limit is
        // high enough to find an exact name match among several
        // stations sharing the same name.
        const params = new URLSearchParams({ q: p.name, limit: '12', order: 'votes' });
        const r = await boxFetch(state.currentBox, `/api/radio/search?${params}`);
        if (!r.ok) return;
        const list = await r.json() || [];
        const wanted = p.name.toLowerCase().trim();
        // 1) Exact name match.
        let pick = list.find(s => (s.name || '').toLowerCase().trim() === wanted);
        // 2) Substring match in either direction (e.g. "NDR2" vs
        //    "NDR 2").
        if (!pick) {
          pick = list.find(s => {
            const n = (s.name || '').toLowerCase().trim();
            return n && (n.includes(wanted) || wanted.includes(n));
          });
        }
        // 3) Same stream host implies the same station.
        if (!pick && p.stream_url) {
          const wantHost = extractHost(p.stream_url);
          if (wantHost) {
            pick = list.find(s => {
              return extractHost(s.url) === wantHost || extractHost(s.url_resolved) === wantHost;
            });
          }
        }
        if (!pick) return;
        const logo = stationLogoChain(pick);
        if (!logo) return;
        // Radio-only: SetPreset sends type=radio with no uri, so never persist
        // onto a Spotify preset or its URI is lost.
        if (p.type === 'spotify') return;
        p.art = logo;
        SetPreset(state.currentBox.host, state.currentBox.port, p.slot, p.name, p.stream_url, logo, p.bitrate || 0).catch(() => {});
      } catch {}
    }));
  } finally {
    healingInProgress = false;
    renderPresets();
  }
}

// ---------- Preset Render mit Long Press Support ----------

// activeSlotFromLocation extracts the slot number from a stream proxy
// URL like http://127.0.0.1:8888/stream/3. Since build 2335 the
// speaker's content items always run through the proxy, so the older
// direct-URL comparison no longer matches. The slot match keeps the
// green "playing" highlight stable even when the real CDN URL
// rotates its tokens.
function activeSlotFromLocation(loc) {
  if (!loc) return null;
  // Spotify presets point the box at the per-slot /spotify/stream-<slot>.ogg
  // (both the hardware and the soft recall), so the slot is in the URL: prefer
  // it so the right Spotify tile lights up even when several presets share the
  // generic "Spotify" now-playing name.
  const sp = loc.match(/\/spotify\/stream-(\d+)\.ogg/);
  if (sp) return parseInt(sp[1], 10);
  const m = loc.match(/\/stream\/(\d+)(?:[/?#]|$)/);
  return m ? parseInt(m[1], 10) : null;
}

// Spotify glyph (green circle + three arcs) shown as the logo on Spotify
// preset tiles so they are instantly recognisable as a Spotify playlist.
// Inline SVG data URI: no bundled asset, no network fetch.
const SPOTIFY_LOGO = "data:image/svg+xml,%3Csvg%20xmlns='http://www.w3.org/2000/svg'%20viewBox='0%200%20168%20168'%3E%3Ccircle%20cx='84'%20cy='84'%20r='84'%20fill='%231ED760'/%3E%3Cpath%20fill='none'%20stroke='%23000'%20stroke-width='13'%20stroke-linecap='round'%20d='M37%2099c30-9%2065-7%2092%209M35%2075c34-10%2076-7%20105%2011M33%2050c38-11%2086-7%20118%2012'/%3E%3C/svg%3E";

// presetStateLabel returns the small state line shown on a preset tile: an
// error, or the now-playing state when this preset is the active one. Keeps the
// play-state -> CSS-class + i18n-key mapping in one place.
function presetStateLabel(slot, isActive, hasErr) {
  if (hasErr) {
    return `<div class="preset-state state-err">&#9888; ${escapeHtml(state.presetErrors[slot])}</div>`;
  }
  if (!isActive) return '';
  const map = {
    PLAY_STATE: ['state-play', 'preset.statePlay'],
    BUFFERING_STATE: ['state-buf', 'preset.stateBuf'],
    PAUSE_STATE: ['state-pause', 'preset.statePause'],
  };
  const m = map[state.nowPlayState];
  return m ? `<div class="preset-state ${m[0]}">${escapeHtml(t(m[1]))}</div>` : '';
}

function renderPresets() {
  const grid = $('presets');
  grid.innerHTML = '';
  const activeSlot = activeSlotFromLocation(state.nowLocation);
  // If the speaker is playing through the stream proxy, resolve the
  // real stream URL of the source slot. That lets us mark sibling
  // slots with the same station as active too. Otherwise only the
  // single slot named in /stream/<n> would light up.
  let activeStreamURL = null;
  if (activeSlot !== null) {
    const ap = state.presets.find(x => x.slot === activeSlot);
    if (ap) activeStreamURL = ap.stream_url;
  }
  for (let i = 1; i <= 6; i++) {
    const p = state.presets.find(x => x.slot === i);
    const isActive = p && state.nowLocation && (
      p.stream_url === state.nowLocation ||
      (activeSlot !== null && p.slot === activeSlot) ||
      (activeStreamURL && p.stream_url === activeStreamURL) ||
      // Spotify presets stream through /spotify/stream.ogg, which carries no
      // slot number, so the slot match above never fires. The preset name is
      // pushed as the now-playing title, so match on that to light up the
      // right Spotify tile.
      (p.type === 'spotify' && /\/spotify\/stream/.test(state.nowLocation) && p.name === state.nowName)
    );
    const hasErr = !!state.presetErrors[i];
    const div = document.createElement('div');
    div.className = 'preset' + (p ? '' : ' empty') + (isActive ? ' playing' : '') + (hasErr ? ' error' : '');
    div.dataset.slot = i;
    if (p) {
      const stateLabel = presetStateLabel(i, isActive, hasErr);
      const hint = state.nowLocation && !isActive
        ? `<div class="preset-hint">${escapeHtml(t('preset.longPressHint'))}</div>`
        : '';
      // Preset logo fallback chain:
      //   1. p.art candidates (pipe-separated if present).
      //   2. state.nowIcon ONLY when p.art is empty and the preset
      //      is currently active. Otherwise a logo from the actively
      //      playing station could leak onto an inactive preset
      //      button whose p.art is broken and falls through.
      //   3. DDG / Google service for stream host and its root
      //      domain.
      const presetCandidates = [];
      const addCands = (val) => {
        if (!val) return;
        for (const c of String(val).split('|')) {
          const t = c.trim();
          if (t && !presetCandidates.includes(t)) presetCandidates.push(t);
        }
      };
      if (p.type === 'spotify') {
        // Show the Spotify logo so the tile is instantly recognisable as a
        // Spotify playlist; the account name is shown small under the title.
        // (Chosen over the album/playlist cover, which changes or lags.)
        addCands(SPOTIFY_LOGO);
      } else if (p.art) {
        addCands(p.art);
      } else if (isActive && state.nowIcon) {
        addCands(state.nowIcon);
        // Auto-persist so the preset has its logo on the next load.
        p.art = state.nowIcon;
        SetPreset(state.currentBox.host, state.currentBox.port, p.slot, p.name, p.stream_url, state.nowIcon, p.bitrate || 0).catch(() => {});
      }
      const streamHost = extractHost(p.stream_url);
      const hostsToTry = [];
      if (streamHost) hostsToTry.push(streamHost);
      const streamRoot = rootDomain(streamHost);
      if (streamRoot && streamRoot !== streamHost) hostsToTry.push(streamRoot);
      for (const h of hostsToTry) {
        for (const svc of iconServicesFor(h)) {
          if (!presetCandidates.includes(svc)) presetCandidates.push(svc);
        }
      }
      // Terminal fallback: a locally generated monogram (data URI, always
      // loads), appended last so a station with a missing or broken logo ends
      // on a clean letter tile instead of a broken-image icon. Spotify keeps
      // its own logo as the first candidate; the monogram only shows if even
      // that fails.
      presetCandidates.push(monogramDataUri(p.name));
      const logo =
        `<img class="preset-logo" src="${escapeAttr(presetCandidates[0])}"
              data-fallbacks="${escapeAttr(presetCandidates.slice(1).join('|'))}"
              onerror="window.__nextLogoFallback(this)"/>`;
      // The active tile mirrors the live now-playing bitrate even when the
      // stored preset bitrate is still 0 (preset saved before the bitrate
      // feature, or radio-browser had none). Persist it so the tile keeps
      // the value after a reload and the other clients see it too.
      let tileBitrate = p.bitrate || 0;
      if (isActive && state.nowBitrate > 0) {
        tileBitrate = state.nowBitrate;
        // Persist the corrected bitrate, but NEVER for Spotify presets:
        // SetPreset is radio-only and would overwrite the Spotify URI.
        if ((p.bitrate || 0) !== state.nowBitrate && p.type !== 'spotify') {
          p.bitrate = state.nowBitrate;
          SetPreset(state.currentBox.host, state.currentBox.port, p.slot, p.name, p.stream_url, p.art || '', state.nowBitrate).catch(() => {});
        }
      }
      div.innerHTML = `
        <div class="preset-head"><span class="num">${escapeHtml(t('preset.key', { n: i }))}</span><span class="del" data-slot="${i}" title="${escapeAttr(t('preset.deleteTitle'))}">&times;</span></div>
        <div class="preset-body">
          ${logo}
          <div class="preset-text">
            <div class="name">${escapeHtml(p.name || t('preset.key', { n: i }))}</div>
            ${p.type === 'spotify' && p.account ? `<div class="preset-account">${escapeHtml(p.account)}</div>` : ''}
            ${p.source ? `<div class="preset-source" title="${escapeAttr(p.source)}">${escapeHtml(t('preset.sourceBadge', { source: p.source }))}</div>` : ''}
            ${isActive && state.nowTitle && p.type !== 'spotify' ? `<div class="preset-track" title="${escapeAttr(state.nowTitle)}"><span class="track-inner">${escapeHtml(state.nowTitle)}</span></div>` : ''}
            <div class="preset-bitrate">${tileBitrate ? tileBitrate + ' kbit/s' : '- kbit/s'}</div>
            ${stateLabel}
          </div>
        </div>
        ${hint}
        <div class="long-press-bar" id="lp-bar-${i}"></div>
      `;
    } else {
      const hint = state.nowLocation
        ? `<div class="preset-hint">${escapeHtml(t('preset.longPressHint'))}</div>`
        : `<div class="url">${escapeHtml(t('preset.searchHint'))}</div>`;
      div.innerHTML = `
        <div class="num">${escapeHtml(t('preset.key', { n: i }))}</div>
        <div class="name">${escapeHtml(t('preset.empty'))}</div>
        ${hint}
        <div class="long-press-bar" id="lp-bar-${i}"></div>
      `;
    }
    attachPresetHandlers(div, i, p);
    grid.appendChild(div);
  }
  grid.querySelectorAll('.del').forEach(el => {
    el.onclick = async (e) => {
      e.stopPropagation();
      const slot = parseInt(el.dataset.slot, 10);
      const p = state.presets.find(x => x.slot === slot);
      const senderName = p && p.name ? escapeHtml(p.name) : t('preset.placeholderSender');
      const ok = await confirmWarn(
        t('preset.confirmClearTitle'),
        t('preset.confirmClearBody', { n: slot, name: senderName })
      );
      if (!ok) return;
      try {
        await DeletePreset(state.currentBox.host, state.currentBox.port, slot);
        loadPresets();
      } catch (err) { showError(err); }
    };
  });
  // Marquee any now-playing track line that overflows its tile, so the full
  // "Artist - Title" is readable without hovering. Deferred to the next frame
  // so the layout (scrollWidth/clientWidth) is settled after the innerHTML
  // rebuild above.
  requestAnimationFrame(() => applyTrackScroll('.preset-track'));
}

// applyTrackScroll turns an overflowing .preset-track into a gentle marquee:
// it pauses at the start, scrolls left until the end is visible, pauses, then
// jumps back to the start and repeats. Lines that fit are left static. Only
// the active tile carries a track line, so this measures one element.
function applyTrackScroll(selector = '.preset-track, .status-bar .now') {
  document.querySelectorAll(selector).forEach(box => {
    const inner = box.querySelector('.track-inner');
    if (!inner) return;
    inner.classList.remove('scrolling');
    inner.style.removeProperty('--track-scroll');
    inner.style.removeProperty('--track-dur');
    const overflow = inner.scrollWidth - box.clientWidth;
    if (overflow > 4) {
      // Brisk ~100 px/s scroll plus the built-in pauses, floored so a
      // slightly-too-long line still scrolls slowly enough to read.
      const dur = Math.max(3, Math.round(overflow / 75 + 1.5));
      inner.style.setProperty('--track-scroll', overflow + 'px');
      inner.style.setProperty('--track-dur', dur + 's');
      inner.classList.add('scrolling');
    }
  });
}

// attachPresetHandlers wires click (short = play) and long press
// (hold = save the current station to this slot). LONG_PRESS_MS =
// 800 ms. VISUAL_HOLD_DELAY = 180 ms: only after this hold time do
// we show the scale(0.96) visual. A short click avoids the mini
// jiggle that a transition scale-down + scale-up would otherwise
// produce on the logo.
const LONG_PRESS_MS = 800;
const VISUAL_HOLD_DELAY = 180;
function attachPresetHandlers(el, slot, preset) {
  let timer = null;
  let visualTimer = null;
  let armed = false;
  let firedLong = false;
  let startedAt = 0;
  const bar = el.querySelector('.long-press-bar');
  const animateBar = () => {
    if (!armed) return;
    const elapsed = Date.now() - startedAt;
    const pct = Math.min(100, (elapsed / LONG_PRESS_MS) * 100);
    if (bar) bar.style.width = pct + '%';
    if (armed) requestAnimationFrame(animateBar);
  };
  const start = (e) => {
    if (e.button !== undefined && e.button !== 0) return; // left click only
    // A click on the X icon is not a preset click.
    if (e.target.classList && e.target.classList.contains('del')) return;
    armed = true; // we start the hold
    firedLong = false; // true once long press fires
    startedAt = Date.now();
    requestAnimationFrame(animateBar);
    visualTimer = setTimeout(() => {
      if (armed) el.classList.add('long-press');
    }, VISUAL_HOLD_DELAY);
    timer = setTimeout(async () => {
      if (!armed) return;
      firedLong = true;
      await saveCurrentToSlot(slot);
      armed = false;
      el.classList.remove('long-press');
      if (bar) bar.style.width = '0%';
    }, LONG_PRESS_MS);
  };
  const cancel = () => {
    if (!armed) return;
    armed = false;
    if (timer) { clearTimeout(timer); timer = null; }
    if (visualTimer) { clearTimeout(visualTimer); visualTimer = null; }
    el.classList.remove('long-press');
    if (bar) bar.style.width = '0%';
  };
  const finish = (e) => {
    if (e.target.classList && e.target.classList.contains('del')) return;
    const wasArmed = armed;
    cancel();
    if (!wasArmed) return;
    if (firedLong) return;
    if (preset) play(slot);
  };
  el.addEventListener('mousedown', start);
  el.addEventListener('mouseup', finish);
  el.addEventListener('mouseleave', cancel);
  el.addEventListener('touchstart', (e) => { start(e); }, { passive: true });
  el.addEventListener('touchend', (e) => { finish(e); });
  el.addEventListener('touchcancel', cancel);
}

// saveCurrentToSlot saves the currently playing station onto the
// given slot (overwrites whatever was there before). Uses the
// now_playing data state.nowLocation + state.nowName plus the last
// known logo.
async function saveCurrentToSlot(slot) {
  // Refresh from the speaker first. On a hardware key press,
  // state.nowLocation / nowName often lag behind (the boxws event
  // arrives late). Without the refresh we would save "Station" as
  // the name or the previous station.
  try { await refreshStatus(); } catch {}
  if (!state.nowLocation) {
    showToast(t('preset.noCurrentStation'));
    return;
  }

  // Case Spotify: the speaker is playing a Spotify playlist. Save a REAL
  // Spotify preset (type=spotify with the playlist URI), not a radio link to
  // the raw stream. The latter showed the album cover instead of the Spotify
  // logo and could not recall/shuffle the playlist. Needs the current context
  // (playlist URI) from /spotify/info.
  if (/\/spotify\/stream/.test(state.nowLocation) && state.nowSpotifyContext) {
    const sname = state.nowName || 'Spotify';
    try {
      await SaveSpotifyPreset(
        state.currentBox.host, state.currentBox.port,
        slot, sname, state.nowSpotifyContext, state.nowSpotifyAccount || ''
      );
      showToast(t('preset.savedToKey', { n: slot, name: sname }));
      await loadPresets();
      return;
    } catch (err) {
      showError(t('preset.saveFailed', { err: String(err) }));
      return;
    }
  }

  // Case A: speaker is playing a proxy item
  // (location = /stream/<sourceSlot>). That happens when the
  // station was triggered via a hardware key or by selecting
  // another soft slot. In that case we copy the source preset
  // directly onto the target slot: name, URL, art logo one to one.
  // That bypasses state.nowIcon / state.nowName completely; on
  // hardware press both often still hold the previous station.
  const sourceSlot = activeSlotFromLocation(state.nowLocation);
  if (sourceSlot !== null && sourceSlot !== slot) {
    const src = state.presets.find(p => p.slot === sourceSlot);
    if (src && src.stream_url) {
      try {
        await SetPreset(
          state.currentBox.host, state.currentBox.port,
          slot, src.name, src.stream_url, src.art || '', src.bitrate || 0
        );
        showToast(t('preset.copiedToKey', { n: slot, name: src.name }));
        await loadPresets();
        return;
      } catch (err) {
        showError(t('preset.saveFailed', { err: String(err) }));
        return;
      }
    }
  }

  // Case B: speaker is playing a stream that does NOT go through
  // our proxy (for example a station started directly via the radio
  // search). Use state.nowLocation / nowName / nowIcon as before.
  const name = state.nowName || t('preset.placeholderSender');
  try {
    await SetPreset(
      state.currentBox.host, state.currentBox.port,
      slot, name, state.nowLocation, state.nowIcon || '', state.nowBitrate || 0
    );
    showToast(t('preset.savedToKey', { n: slot, name }));
    await loadPresets();
    if (state.nowUUID) {
      VoteStation(state.currentBox.host, state.currentBox.port, state.nowUUID).catch(() => {});
    }
  } catch (err) {
    showError(t('preset.saveFailed', { err: String(err) }));
  }
}

// reapplyDesiredVolume re-sends the user's chosen volume after the box
// has woken from standby to play. Waking from standby resets the box to
// its own stored volume (often 30), which silently discards the level the
// user set while it was idle. We push the desired level again a couple of
// times across the wake window, reflect it in the slider right away, and
// hold off the slider-from-box sync for a few seconds so it cannot snap
// back to 30 in between. No-op until the user has actually set a volume.
function reapplyDesiredVolume() {
  const v = state.desiredVolume;
  const box = state.currentBox;
  if (v == null || !box) return;
  // Reflect the level in the slider right away and hold off the
  // slider-from-box sync. Send the volume early, while the box is still
  // buffering (before audio output), so the stream is not briefly audible
  // at the box's woken default (30). This is safe now that the agent
  // serializes box commands (boxCmdMu): a volume PUT can no longer race the
  // play and waits for it via the mutex instead of colliding. A second PUT
  // a bit later makes sure it sticks if the first landed before the box
  // had fully woken.
  state.musicVolUntil = Date.now() + 5000;
  if (musicVolEl) {
    musicVolEl.value = String(v);
    if (musicVolValEl) musicVolValEl.textContent = String(v);
  }
  const apply = () => { if (state.currentBox === box) throttledSetVolume(box.host, box.port, v); };
  setTimeout(apply, 250);
  setTimeout(apply, 1500);
}

async function play(slot) {
  // Was the box idle/standby before this play? Waking resets its volume,
  // so we re-apply the user's chosen level afterwards only in that case
  // (a normal preset switch while already playing keeps the live volume).
  const wasIdle = !state.nowPlayState || state.nowSource === 'STANDBY';
  const p = state.presets.find(x => x.slot === slot);
  if (p) {
    // Optimistic UI: set BUFFERING_STATE immediately so the user
    // gets feedback. Sticky for 6 s. During that window refreshStatus
    // must not flip the preset back to grey when the speaker still
    // reports the old stream or an empty one.
    state.nowPlayState = 'BUFFERING_STATE';
    // Spotify presets carry no stream_url (they recall by URI), so without
    // this the optimistic location is empty: the tile would not light up and
    // the click feels ignored until the box confirms several seconds later.
    // Point it at the Spotify stream the box will report, so the highlight and
    // the "starting" label appear instantly on click.
    state.nowLocation = p.type === 'spotify'
      ? 'http://127.0.0.1:8888/spotify/stream.ogg'
      : (p.stream_url || '');
    state.nowName = p.name || '';
    state.nowIcon = p.art || '';
    state.nowBitrate = p.bitrate || 0;
    state.nowTitle = ''; // clear so the new station does not briefly show the old track
    scheduleLiveBitrate();
    scheduleLiveTitle();
    state.nowUUID = '';
    state.optimisticUntil = Date.now() + 6000;
    delete state.presetErrors[slot];
    renderPresets();
  }
  try {
    await PlaySlot(state.currentBox.host, state.currentBox.port, slot);
    delete state.presetErrors[slot];
    if (wasIdle) reapplyDesiredVolume();
    refreshStatus();
    setTimeout(refreshStatus, 1500);
  } catch (e) {
    state.nowPlayState = '';
    state.nowLocation = '';
    state.optimisticUntil = 0;
    state.presetErrors[slot] = friendlyPlayError(String(e));
    renderPresets();
    setTimeout(() => refreshStatus(), 2000);
  }
}

// friendlyPlayError turns a technical error string into a short
// user-facing hint shown on the preset label.
function friendlyPlayError(s) {
  const l = String(s).toLowerCase();
  if (l.includes('box_not_ready')) return t('play.errBoxStarting');
  if (l.includes('no such host') || l.includes('lookup')) return t('play.errNoInternet');
  if (l.includes('timeout') || l.includes('deadline')) return t('play.errSpeakerTimeout');
  if (l.includes('refused')) return t('play.errSpeakerRefused');
  if (l.includes('402') || l.includes('no uri')) return t('play.errNotPlayable');
  if (l.includes('500')) return t('play.errServer500');
  if (l.includes('konnte nicht') || l.includes('could not')) {
    // Backend returns a localised 'detail' string on UPnP failures.
    return t('play.errUnreachable');
  }
  return t('play.errGeneric');
}

// scheduleLiveBitrate fetches the agent's detected stream bitrate after a
// station starts and reflects it into now-playing + the active preset tile.
// Two delayed reads, not a timer: the first catches an icy-br value (known
// instantly), the second catches the throughput estimate, which the agent
// only has after it has skipped the buffer-fill and measured ~6 s of
// steady-state playback (~10 s in). Routed through the StreamBitrate Go
// binding so it self-heals across :8888 / :17008 like every other box call
// (a raw fetch pinned to box.port silently failed on BCO speakers).
let liveBitrateTimer = null;
function scheduleLiveBitrate() {
  if (liveBitrateTimer) { clearTimeout(liveBitrateTimer); liveBitrateTimer = null; }
  const box = state.currentBox;
  if (!box) return;
  // The agent only has a throughput bitrate ~10 s after the stream itself
  // starts, and the stream start lags the play click by a few seconds
  // (wake + UPnP). A fixed delay therefore races the measurement and can
  // read 0 forever. So retry every 4 s until a value appears, then stop;
  // bounded to ~32 s so it never polls indefinitely. icy-br stations
  // resolve on the first attempt. Routed through the StreamBitrate Go
  // binding so it self-heals across :8888 / :17008.
  let tries = 0;
  const attempt = async () => {
    liveBitrateTimer = null;
    if (state.currentBox !== box) return;
    tries++;
    // Spotify streams report their measured rate through a separate agent
    // endpoint and carry no slot in the location, so resolve the preset by
    // name (the now-playing title is the preset name).
    const isSpotify = /\/spotify\/stream/.test(state.nowLocation || '');
    let br = 0;
    try {
      br = ((isSpotify ? await SpotifyBitrate(box.host, box.port)
                       : await StreamBitrate(box.host, box.port)) | 0);
    } catch {}
    if (br > 0) {
      if (br !== state.nowBitrate) {
        state.nowBitrate = br;
        // status bar repaints on its own 1 s tick; setting nowBitrate is enough.
        // Correct the active preset's stored bitrate to the real value
        // (radio-browser's catalogue number is often missing or wrong; a
        // Spotify preset never had one).
        const p = isSpotify
          ? state.presets.find(x => x.type === 'spotify' && x.name === state.nowName)
          : (() => { const s = activeSlotFromLocation(state.nowLocation); return s !== null ? state.presets.find(x => x.slot === s) : null; })();
        if (p && p.bitrate !== br) {
          p.bitrate = br;
          // Persist for radio only. SetPreset is radio-only (type=radio, no
          // uri), so persisting a Spotify preset would wipe its URI. The
          // Spotify rate stays live via state.nowBitrate + /spotify/info.
          if (!isSpotify) {
            SetPreset(box.host, box.port, p.slot, p.name, p.stream_url, p.art || '', br).catch(() => {});
          }
        }
        renderPresets();
      }
      return; // got it
    }
    if (tries < 11) liveBitrateTimer = setTimeout(attempt, 3000);
  };
  // First attempt soon: a station measured earlier this session is cached
  // agent-side and answers instantly. Fresh stations return 0 here and the
  // retries (every 3 s, ~33 s total) pick up the value once the agent's
  // ~10 s throughput window completes.
  liveBitrateTimer = setTimeout(attempt, 2000);
}

// scheduleLiveTitle polls the agent's live ICY StreamTitle for the radio
// station currently playing and reflects it into the active preset tile as the
// now-playing track. Unlike the bitrate (stable, read once) the title changes
// per song, so this re-polls every 12 s while a proxied radio stream is the
// active source. It stops when the speaker changes or playback stops; Spotify
// is skipped (it shows its own track via /spotify/info).
// liveTitleActive guards a single running poll loop: scheduleLiveTitle may be
// called from the play handler AND from every refreshStatus tick, so without
// this each call would reset the timer and it would never fire. The loop
// clears the flag when it stops (speaker change or playback stop), so the next
// play restarts it.
let liveTitleActive = false;
function scheduleLiveTitle() {
  if (liveTitleActive) return;
  const box = state.currentBox;
  if (!box) return;
  liveTitleActive = true;
  const tick = async () => {
    if (state.currentBox !== box) { liveTitleActive = false; return; }   // speaker changed
    const loc = state.nowLocation || '';
    if (loc === '') { liveTitleActive = false; return; }                 // playback stopped
    const isRadio = /\/stream\//.test(loc) && !/\/spotify\/stream/.test(loc);
    if (isRadio) {
      let title = '';
      try { title = (await StreamTitle(box.host, box.port)) || ''; } catch {}
      if (state.currentBox !== box) { liveTitleActive = false; return; }
      if (title !== state.nowTitle) {
        state.nowTitle = title;
        renderPresets();
        renderNowPlayingBar(); // keep the status line in sync with the tile
      }
    }
    // Poll fast (every 2 s) while there is no title yet, so a just-started
    // station or a fresh preset switch shows its track within a couple of
    // seconds instead of waiting a full cycle; relax to 12 s once a title is
    // showing. An empty title (station between songs / no metadata) keeps the
    // fast cadence so it re-acquires quickly.
    setTimeout(tick, state.nowTitle ? 12000 : 2000);
  };
  // First read soon: the station emits its first metadata block a moment after
  // the stream starts.
  setTimeout(tick, 1200);
}

async function action(kind) {
  if (!state.currentBox) return;
  const fn = kind === 'pause' ? Pause : Stop;
  try { await fn(state.currentBox.host, state.currentBox.port); } catch (e) { showError(e); }
  setTimeout(refreshStatus, 1000);
}

// renderNowPlayingBar paints the now-playing status line purely from cached
// state (no network), so it can be called both from the status poll and from
// the live-title poller the moment a track arrives, keeping the status line in
// sync with the preset tile. Guarded on the rendered HTML so it does not
// restart the marquee animation when nothing changed.
function renderNowPlayingBar() {
  const bar = $('statusBar');
  if (!bar) return;
  const ps = state.nowPlayState || '';
  const src = state.nowSource || '';
  const loc = state.nowLocation || '';
  const name = state.nowName || '';
  let displayName = name;
  if (/\/spotify\/stream/.test(loc) && state.nowSpotifyTrack) {
    const song = state.nowSpotifyArtist
      ? `${state.nowSpotifyArtist} - ${state.nowSpotifyTrack}`
      : state.nowSpotifyTrack;
    displayName = name ? `${t('status.playlistLabel')}: "${name}" · ${song}` : song;
  } else if (/\/stream\//.test(loc) && !/\/spotify\/stream/.test(loc) && state.nowTitle) {
    displayName = name ? `${t('status.stationLabel')}: "${name}" · ${state.nowTitle}` : state.nowTitle;
  }
  let stateLabel, stateClass;
  if (ps === 'PLAY_STATE') { stateLabel = t('status.playing'); stateClass = 'play'; }
  else if (ps === 'BUFFERING_STATE') { stateLabel = t('status.buffering'); stateClass = 'buf'; }
  else if (ps === 'PAUSE_STATE') { stateLabel = t('status.paused'); stateClass = 'idle'; }
  else if (src === 'STANDBY') { stateLabel = t('status.standby'); stateClass = 'idle'; }
  else { stateLabel = ''; stateClass = 'idle'; }
  if (src === 'AUX') { displayName = t('status.auxInput'); if (!stateLabel) stateLabel = t('status.active'); }
  else if (src === 'BLUETOOTH') { displayName = t('status.bluetooth'); if (!stateLabel) stateLabel = t('status.active'); }
  const isStreamSrc = (ps === 'PLAY_STATE' || ps === 'BUFFERING_STATE' || ps === 'PAUSE_STATE') && src !== 'AUX' && src !== 'BLUETOOTH';
  const brLabel = isStreamSrc ? ` <small class="now-bitrate">${state.nowBitrate ? state.nowBitrate + ' kbit/s' : '- kbit/s'}</small>` : '';
  bar.className = 'status-bar status-' + stateClass;
  let statusHTML;
  if (displayName) {
    // displayName sits in a .track-inner so a too-long "Station: ... · track"
    // marquees inside .now, exactly like the preset tiles.
    statusHTML = `<span class="now"><span class="track-inner">&#9654; ${escapeHtml(displayName)}</span></span>${stateLabel ? ' <small>' + escapeHtml(stateLabel) + '</small>' : ''}${brLabel}`;
  } else if (stateLabel) {
    statusHTML = `<span class="muted">${escapeHtml(stateLabel)}</span>`;
  } else {
    statusHTML = `<span class="muted">${escapeHtml(t('status.ready'))}</span>`;
  }
  // Only rewrite the DOM when the line changes, so the marquee animation is not
  // restarted on every poll (it would never get to scroll).
  if (statusHTML !== state.lastStatusHTML) {
    state.lastStatusHTML = statusHTML;
    bar.innerHTML = statusHTML;
    requestAnimationFrame(() => applyTrackScroll('.status-bar .now'));
  }
}

async function refreshStatus() {
  if (!state.currentBox || state.view !== 'box') return;
  // Reflect hardware-button volume changes back into the slider.
  // Fired in parallel with the Status fetch so a slow Status call
  // does not delay the volume update. Cheap, drag-aware.
  syncMusicTabVolumeFromBox();
  try {
    const xml = await Status(state.currentBox.host, state.currentBox.port);
    const name = decodeXmlEntities((xml.match(/<itemName>([^<]+)<\/itemName>/) || [])[1] || '');
    const src = (xml.match(/source="([^"]+)"/) || [])[1] || '';
    state.nowSource = src;

    // Piggy-back an SSH status check on the polling we are doing
    // anyway. Toggle the global banner so the user sees it on every
    // tab rather than only after entering the Settings tab.
    checkSshBanner();
    const ps = (xml.match(/<playStatus>([^<]+)<\/playStatus>/) || [])[1] || '';
    const loc = decodeXmlEntities((xml.match(/location="([^"]+)"/) || [])[1] || '');
    // Extract the art URL from the <art ...>URL</art> tag. Bose
    // emits it for stations with an image (for example after a
    // radio-search play). Without this refresh, state.nowIcon would
    // stay stuck from the previous soft click ("logo of the
    // previous station" bug).
    const artRaw = decodeXmlEntities((xml.match(/<art[^>]*>([^<]+)<\/art>/) || [])[1] || '');

    // Optimistic guard: after a user preset click we set nowLocation
    // straight to the desired stream. Until optimisticUntil expires,
    // refreshStatus must NOT overwrite location/name. Otherwise the
    // button flickers grey between click and the actual stream
    // start. Once the speaker confirms our location, release the
    // optimistic guard.
    const optimistic = Date.now() < (state.optimisticUntil || 0);
    if (optimistic && loc && loc === state.nowLocation) {
      state.optimisticUntil = 0;
    }
    const newLoc = optimistic ? state.nowLocation : loc;
    const newName = optimistic ? state.nowName : name;
    const stateChanged = state.nowPlayState !== ps || state.nowLocation !== newLoc || state.nowName !== newName;
    state.nowPlayState = ps;
    state.nowLocation = newLoc;
    state.nowName = newName;
    // Live Spotify track metadata for the now-playing line: poll the agent's
    // /spotify/info (throttled) while a Spotify stream is active so the desktop
    // shows the current song + artist, not just the playlist/preset name.
    const isSpotifyNow = /\/spotify\/stream/.test(newLoc);
    if (isSpotifyNow) {
      const npBox = state.currentBox;
      if (npBox && Date.now() - (state.lastSpotifyNowFetch || 0) > 3000) {
        state.lastSpotifyNowFetch = Date.now();
        SpotifyNowPlaying(npBox.host, npBox.port).then(np => {
          if (!np) return;
          const coverChanged = state.nowSpotifyCover !== (np.cover || '');
          state.nowSpotifyTrack = np.track || '';
          state.nowSpotifyArtist = np.artist || '';
          state.nowSpotifyCover = np.cover || '';
          state.nowSpotifyContext = np.context || '';
          state.nowSpotifyAccount = np.account || '';
          // The now-playing line redraws every status poll, but the tile cover
          // only redraws on renderPresets. Re-render when the cover changes so
          // the preset logo tracks the song in step with the title instead of
          // lagging a track behind.
          if (coverChanged) renderPresets();
        }).catch(() => {});
      }
    } else {
      state.nowSpotifyTrack = '';
      state.nowSpotifyArtist = '';
      state.nowSpotifyCover = '';
    }
    // Update state.nowIcon. Prefer the art tag from now_playing.
    // If that is empty AND we are playing through the stream proxy,
    // adopt the logo of the source preset. Bose UPnP items emitted
    // by hardware key presses carry no art tag, so we need this
    // fallback.
    if (!optimistic) {
      const slotFromProxy = activeSlotFromLocation(newLoc);
      const ap = slotFromProxy !== null ? state.presets.find(p => p.slot === slotFromProxy) : null;
      if (artRaw) {
        state.nowIcon = artRaw;
      } else if (ap) {
        state.nowIcon = ap.art || '';
      } else if (!newLoc) {
        state.nowIcon = '';
      }
      // Keep the now-playing bitrate in sync with the active preset
      // (hardware key press or app restart did not go through the play
      // path that sets it). Cleared when nothing is playing.
      if (/\/spotify\/stream/.test(newLoc)) {
        // Spotify stream: never inherit the previous radio station's bitrate.
        // Use the matching Spotify preset's stored (measured) rate, or 0 so the
        // live fetch below recomputes it from the actual stream.
        const sp = state.presets.find(p => p.type === 'spotify' && p.name === newName);
        state.nowBitrate = (sp && sp.bitrate) ? sp.bitrate : 0;
      } else if (ap && ap.bitrate) {
        state.nowBitrate = ap.bitrate;
      } else if (!newLoc) {
        state.nowBitrate = 0;
      }
      // Still playing through the stream proxy but with no bitrate yet
      // (app restart, hardware key press, or a preset whose stored bitrate
      // is 0): kick the live fetch. It writes state.nowBitrate once the
      // agent has measured and then self-stops, so this does not re-trigger
      // on every poll once a value is in.
      if (!state.nowBitrate &&
          (ps === 'PLAY_STATE' || ps === 'BUFFERING_STATE') &&
          (activeSlotFromLocation(newLoc) !== null || /\/spotify\/stream/.test(newLoc))) {
        scheduleLiveBitrate();
      }
      // Keep the live radio track flowing into the active tile for playback
      // STR did not itself start (hardware key, app restart). Self-guarded, so
      // calling it on every poll is safe; it no-ops while already polling.
      if ((ps === 'PLAY_STATE' || ps === 'BUFFERING_STATE') &&
          activeSlotFromLocation(newLoc) !== null) {
        scheduleLiveTitle();
      }
    }

    // If the speaker is now playing successfully, clear the preset
    // error. The speaker's ContentItems run through the stream
    // proxy, so accept the slot match from /stream/<slot> too.
    if (ps === 'PLAY_STATE') {
      const slotFromProxy = activeSlotFromLocation(loc);
      const ap = state.presets.find(p =>
        p.stream_url === loc || (slotFromProxy !== null && p.slot === slotFromProxy)
      );
      if (ap && state.presetErrors[ap.slot]) {
        delete state.presetErrors[ap.slot];
      }
      // Spotify presets carry no stream_url and the location (/spotify/stream)
      // has no slot, so the match above never fires. When a Spotify stream is
      // confirmed playing, clear ALL Spotify preset errors so a stale
      // "speaker still starting" no longer sticks on the tile.
      if (/\/spotify\/stream/.test(loc)) {
        for (const p of state.presets) {
          if (p.type === 'spotify') delete state.presetErrors[p.slot];
        }
      }
    }

    if (stateChanged && state.presets.length > 0) {
      renderPresets();
    }

    // Now-playing status line. Rendered from cached state so the live-title
    // poller can refresh it the instant a track arrives (in sync with the
    // preset tile), not only on the next status poll.
    renderNowPlayingBar();

    // Source buttons: highlight the active source in green.
    document.querySelectorAll('.btn-source').forEach(b => {
      const s = b.dataset.source;
      const active = (s === 'AUX' && src === 'AUX') ||
                     (s === 'BLUETOOTH' && src === 'BLUETOOTH') ||
                     (s === 'STANDBY' && src === 'STANDBY');
      b.classList.toggle('active', active);
    });
  } catch {
    // Transient status-fetch failure (a single poll timing out while the
    // box is briefly busy, e.g. BoseApp's :8090 under load). Keep the last
    // known now-playing on screen instead of blanking it to a dash, which
    // looked like the display flickering to "---" and back even though
    // nothing actually changed. The next successful poll refreshes it.
  }
}

// ---------- Search ----------

const PAGE_SIZE = 30;

// Radio favorites: a per-machine list of starred stations kept in
// localStorage (no agent change, no preset-schema change). It stores only
// the minimal Station fields renderSearchResults needs, so a favorite renders
// through the exact same row path as a search result and inherits play, the
// pick -> assign-to-key modal, and the long-press-to-tile fast path for free.
const FAV_KEY = 'str.favStations';

function loadFavStore() {
  try { return JSON.parse(localStorage.getItem(FAV_KEY)) || []; } catch { return []; }
}
function saveFavStore(arr) {
  try { localStorage.setItem(FAV_KEY, JSON.stringify(arr)); } catch {}
}
function favMinimal(s) {
  return {
    stationuuid: s.stationuuid, name: s.name, url: s.url, url_resolved: s.url_resolved,
    bitrate: s.bitrate || 0, country: s.country, countrycode: s.countrycode,
    codec: s.codec, tags: s.tags, votes: s.votes || 0, homepage: s.homepage,
    favicon: s.favicon, lastcheckok: s.lastcheckok,
  };
}
function favId(s) { return s && (s.stationuuid || (s.name + '|' + (s.url || ''))); }
function isFav(s) {
  const id = favId(s);
  return !!id && loadFavStore().some(x => favId(x) === id);
}
function toggleFav(s) {
  const id = favId(s);
  if (!id) return false;
  const arr = loadFavStore();
  const idx = arr.findIndex(x => favId(x) === id);
  let nowFav;
  if (idx >= 0) { arr.splice(idx, 1); nowFav = false; }
  else { arr.push(favMinimal(s)); nowFav = true; }
  saveFavStore(arr);
  updateFavModeBtn();
  return nowFav;
}
// updateFavModeBtn shows the "Favorites" mode entry next to Top/Search only
// once at least one station is starred, so a user who never uses favorites
// sees the unchanged toggle (the "appears only after the first star" design).
function updateFavModeBtn() {
  const b = $('favModeBtn');
  if (!b) return;
  b.classList.toggle('hidden', loadFavStore().length === 0);
}
// loadFavorites renders the saved stations through the normal search-result
// path. No server fetch and no load-more: the list is exactly the store.
function loadFavorites() {
  if (!state.currentBox) { showError(t('search.errSelectSpeaker')); return; }
  state.searchLastMode = 'favorites';
  state.searchResults = loadFavStore();
  const lm = $('loadMoreRow');
  if (lm) lm.classList.add('hidden');
  renderSearchResults();
}

async function doSearch() {
  if (!state.currentBox) { showError(t('search.errSelectSpeaker')); return; }
  const q = $('searchQ').value.trim();
  state.searchLastQuery = q;
  state.searchLastMode = q ? 'search' : 'top';
  state.searchOffset = 0;
  if (!q) { return doTop(); }
  await fetchSearchPage(false);
}

async function doTop() {
  if (!state.currentBox) { showError(t('search.errSelectSpeaker')); return; }
  state.searchLastMode = 'top';
  state.searchOffset = 0;
  await fetchSearchPage(false);
}

async function loadMore() {
  state.searchOffset += PAGE_SIZE;
  await fetchSearchPage(true);
}

function buildSearchURL() {
  const isSearch = state.searchLastMode === 'search' && state.searchLastQuery;
  // Server-side sort: when the user wants name order, the server
  // must deliver in that order, otherwise "A" lands on page 50.
  // For order=name we still fetch 4x the page size so the
  // "Bose-compatible only" filter still has enough left after the
  // strip of HTTPS-only stations like laut.fm.
  const ord = state.searchOrder || 'votes';
  const limit = ord === 'name' ? PAGE_SIZE * 4 : PAGE_SIZE;
  const params = new URLSearchParams({
    limit: String(limit),
    offset: String(state.searchOffset),
    order: ord,
  });
  // Country: an empty string means "all countries". We send the
  // filter as an explicit empty value (cc=) rather than omitting it
  // entirely, so the server can distinguish "filter not set" from
  // "user wants no filter". Otherwise the older server variant
  // silently defaults to DE.
  params.set('cc', state.searchCountry || '');
  if (state.searchLang)    params.set('lang', state.searchLang);
  if (state.searchTag)     params.set('tag', state.searchTag);
  if (state.searchOnlyOK)  params.set('onlyok', '1');
  if (isSearch) {
    params.set('q', state.searchLastQuery);
    return `/api/radio/search?${params.toString()}`;
  }
  return `/api/radio/top?${params.toString()}`;
}

async function fetchSearchPage(append) {
  const url = buildSearchURL();
  if (!append) {
    $('searchResults').innerHTML = `<div class="muted">${escapeHtml(t('search.loadingStations'))}</div>`;
    $('loadMoreRow').classList.add('hidden');
  }
  try {
    const r = await boxFetch(state.currentBox, url);
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const page = await r.json() || [];
    if (append) {
      state.searchResults = state.searchResults.concat(page);
    } else {
      state.searchResults = page;
    }
    // Dedup by UUID (paginate + local sort can produce duplicates).
    const seen = new Set();
    state.searchResults = state.searchResults.filter(s => {
      const id = s.stationuuid || (s.name + '|' + s.url);
      if (seen.has(id)) return false;
      seen.add(id);
      return true;
    });
    // Local sort. The server always returns order=votes so that the
    // set of stations stays consistent across all sort options.
    const ord = state.searchOrder || 'votes';
    state.searchResults.sort((a, b) => {
      switch (ord) {
        case 'name': {
          const ca = cleanForSort(a.name);
          const cb = cleanForSort(b.name);
          return ca.localeCompare(cb, 'de', { sensitivity: 'base' });
        }
        case 'clickcount':
          return (b.clickcount || 0) - (a.clickcount || 0);
        case 'clicktrend':
          return (b.clicktrend || 0) - (a.clicktrend || 0);
        case 'bitrate':
          return (b.bitrate || 0) - (a.bitrate || 0);
        case 'votes':
        default:
          return (b.votes || 0) - (a.votes || 0);
      }
    });
    renderSearchResults();
    $('loadMoreRow').classList.toggle('hidden', page.length < PAGE_SIZE);
  } catch (e) {
    $('searchResults').innerHTML = `<div class="muted">${escapeHtml(t('common.error'))}: ${escapeHtml(e.message)}</div>`;
    $('loadMoreRow').classList.add('hidden');
  }
}

// cleanForSort strips leading non-alphanumeric characters (tab,
// space, dash, dot, asterisk, ...) so that "  ABC" and "ABC" sort
// alike. Robust against webview versions without Unicode property
// escapes: matches the classic ASCII range plus selected extended
// blocks (Latin diacritics, Cyrillic, etc.).
function cleanForSort(name) {
  const raw = (name || '').toString();
  // Strip leading non-alphanumeric characters. Accept A-Z, 0-9 and
  // selected Unicode ranges (German diacritics, Cyrillic, etc.).
  const stripped = raw.replace(/^[^A-Za-z0-9À-ɏͰ-ӿ]+/, '');
  // If nothing remains after the strip (the name was symbols only),
  // fall back to the raw name. Otherwise empty-string stations would
  // all clump together at the top.
  return (stripped || raw).toLowerCase().trim();
}

// isBoseCompatible estimates whether the speaker can reliably play
// the stream. Since stick-agent build 0132 every stream goes through
// /stream/raw, which removes the TLS concerns. We only check the
// codec now:
//   - MP3 / AAC / AACP / MPEG work
//   - Ogg / Opus / FLAC the Bose player cannot decode
// Conservative: when the codec is unknown we let the station
// through.
function isBoseCompatible(s) {
  const codec = String(s.codec || '').toUpperCase();
  if (!codec) return true; // unknown - let the speaker try
  return codec === 'MP3' || codec === 'AAC' || codec === 'AACP' || codec === 'MPEG';
}

function renderSearchResults() {
  const res = $('searchResults');
  // Optional client-side Bose compatibility filter: drop HTTPS
  // streams and exotic codecs so the user does not hit 502 errors
  // on play.
  const totalRaw = (state.searchResults || []).length;
  let list = state.searchResults || [];
  if (state.searchOnlyBose) {
    list = list.filter(isBoseCompatible);
  }
  // Minimum-bitrate filter. Stations with no reported bitrate (very
  // common on radio-browser) are kept, not hidden, so a quality filter
  // does not wipe out most results; only stations with a known bitrate
  // below the threshold are dropped.
  const minBr = state.searchMinBitrate || 0;
  if (minBr) {
    list = list.filter(s => !s.bitrate || s.bitrate >= minBr);
  }
  // Update the counter row. radio-browser does not return a grand
  // total on a filtered search, so we show "X shown" plus a hint
  // that more can arrive via "load more".
  const cnt = $('searchCount');
  if (cnt) {
    if (list.length === 0) {
      cnt.classList.add('hidden');
    } else {
      const moreHint = totalRaw >= PAGE_SIZE ? ' ' + t('search.moreHint') : '';
      const filterHint = state.searchOnlyBose && list.length < totalRaw
        ? ' ' + t('search.filterHiddenCount', { n: totalRaw - list.length })
        : '';
      cnt.innerHTML = t('search.shownCount', { n: `<b>${list.length}</b>` }) + filterHint + moreHint;
      cnt.classList.remove('hidden');
    }
  }
  if (list.length === 0) {
    res.innerHTML = '<div class="muted">' + escapeHtml(
      state.searchLastMode === 'favorites'
        ? t('search.favEmpty')
        : state.searchOnlyBose && (state.searchResults || []).length > 0
          ? t('search.noBoseStations')
          : t('search.noStationsFound')
    ) + '</div>';
    return;
  }
  res.innerHTML = list.map((s, i) => {
    const flag = flagFromCC(s.countrycode);
    const okClass = s.lastcheckok ? 'ok' : 'bad';
    const webUrl = (typeof s.homepage === 'string' && /^https?:\/\//i.test(s.homepage)) ? s.homepage : '';
    const okTitle = s.lastcheckok ? t('search.checkOk') : t('search.checkBad');
    let trend = '';
    if (s.clicktrend > 0) trend = `<span class="result-trend" title="${escapeAttr(t('search.trendUp', { n: s.clicktrend }))}">&#9650;</span>`;
    else if (s.clicktrend < 0) trend = `<span class="result-trend up-down" title="${escapeAttr(t('search.trendDown', { n: s.clicktrend }))}">&#9660;</span>`;

    const countryDe = translateCountry(s.country);
    const tagChips = translateTags(s.tags).slice(0, 4).map(tag => `<span class="tag-pill">${escapeHtml(tag)}</span>`).join('');

    const metaBits = [];
    if (countryDe) metaBits.push(escapeHtml(countryDe));
    // Always show a bitrate cell. Many radio-browser stations report no
    // bitrate (e.g. "Sunshine Live - Die 90er"); show "- kbit/s" rather
    // than hiding the field so the column stays consistent.
    metaBits.push(s.bitrate ? `${s.bitrate} kbit/s` : '- kbit/s');
    if (s.votes)   metaBits.push(t('search.votes', { n: formatNumber(s.votes) }));

    const logo = `
      <div class="result-logo">
        ${logoImgTag(s, 'fav')}
        ${flag ? `<span class="fav-flag" title="${escapeAttr(s.country || '')}">${flag}</span>` : ''}
      </div>`;
    return `
      <div class="result-row" data-i="${i}">
        ${logo}
        <div class="result-text">
          <div class="result-name">
            <span class="result-online-dot ${okClass}" title="${escapeAttr(okTitle)}"></span>
            <span class="result-name-text">${escapeHtml(s.name || t('search.unnamed'))}</span>
            ${trend}
          </div>
          <div class="result-meta">${metaBits.join(' &middot; ')}${webUrl ? ` &middot; <a href="#" class="result-site" data-i="${i}" title="${escapeAttr(t('search.openWebsite'))}">${escapeHtml(t('footer.website'))}</a>` : ''}</div>
          ${tagChips ? `<div class="result-tag-chips">${tagChips}</div>` : ''}
        </div>
        <div class="result-actions">
          <button class="btn btn-mini play-now" data-i="${i}" title="${escapeAttr(t('search.playNow'))}">&#9654;</button>
          <button class="btn btn-mini pick" data-i="${i}" title="${escapeAttr(t('search.assignToKey'))}">&#10133;</button>
          <button class="btn btn-mini fav-toggle${isFav(s) ? ' is-fav' : ''}" data-i="${i}" title="${escapeAttr(isFav(s) ? t('search.removeFav') : t('search.addFav'))}">${isFav(s) ? '&#9733;' : '&#9734;'}</button>
        </div>
      </div>
    `;
  }).join('');
  res.querySelectorAll('.play-now').forEach(btn => {
    btn.onclick = async (e) => {
      e.stopPropagation();
      const s = list[parseInt(btn.dataset.i, 10)];
      const url = s.url_resolved || s.url;
      const chain = stationLogoChain(s);
      state.nowPlayState = 'BUFFERING_STATE';
      state.nowLocation = url;
      state.nowName = s.name;
      state.nowIcon = chain;
      state.nowBitrate = s.bitrate || 0;
      scheduleLiveBitrate();
      state.nowUUID = s.stationuuid || '';
      renderPresets();
      try {
        await PlayURL(state.currentBox.host, state.currentBox.port, url, s.name, chain, s.stationuuid || '');
        setTimeout(refreshStatus, 1200);
      } catch (err) {
        state.nowPlayState = '';
        state.nowLocation = '';
        showError(err);
      }
    };
  });
  res.querySelectorAll('.pick').forEach(btn => {
    btn.onclick = (e) => { e.stopPropagation(); openPick(list[parseInt(btn.dataset.i, 10)]); };
  });
  res.querySelectorAll('.fav-toggle').forEach(btn => {
    btn.onclick = (e) => {
      e.stopPropagation();
      const s = list[parseInt(btn.dataset.i, 10)];
      const nowFav = toggleFav(s);
      if (state.searchLastMode === 'favorites' && !nowFav) {
        // Unstarred while viewing the favorites list: drop it from the view.
        state.searchResults = loadFavStore();
        renderSearchResults();
        return;
      }
      btn.classList.toggle('is-fav', nowFav);
      btn.innerHTML = nowFav ? '&#9733;' : '&#9734;';
      btn.title = nowFav ? t('search.removeFav') : t('search.addFav');
    };
  });
  res.querySelectorAll('.result-site').forEach(link => {
    link.onclick = (e) => {
      e.preventDefault();
      e.stopPropagation();
      const s = list[parseInt(link.dataset.i, 10)];
      if (s && typeof s.homepage === 'string' && /^https?:\/\//i.test(s.homepage)) BrowserOpenURL(s.homepage);
    };
  });
}

// showSlotPicker renders the shared 1-6 preset slot-picker modal. Callers pass
// the title, subtitle and an onPick(slot) that does the actual save; closing the
// modal, reloading the presets and surfacing errors are common to every use.
function showSlotPicker({ title, subtitle, onPick }) {
  $('pickTitle').textContent = title;
  $('pickSub').textContent = subtitle || '';
  const grid = $('pickGrid');
  grid.innerHTML = '';
  for (let i = 1; i <= 6; i++) {
    const p = state.presets.find(x => x.slot === i);
    const b = document.createElement('button');
    b.className = 'pick-slot' + (p ? ' has' : '');
    b.innerHTML = '<div class="ps-num">' + escapeHtml(t('preset.key', { n: i })) + '</div><div class="ps-name">' + (p ? escapeHtml(p.name) : escapeHtml(t('preset.pickEmpty'))) + '</div>';
    b.onclick = async () => {
      try {
        await onPick(i);
        closePick();
        await loadPresets();
      } catch (err) { showError(err); }
    };
    grid.appendChild(b);
  }
  $('pickModal').classList.remove('hidden');
}

function openPick(station) {
  showSlotPicker({
    title: t('preset.assignStationTitle'),
    subtitle: station.name + (station.bitrate ? ' (' + station.bitrate + ' kbit/s)' : ''),
    onPick: async (i) => {
      const logo = stationLogoChain(station);
      await SetPreset(state.currentBox.host, state.currentBox.port, i, station.name, station.url_resolved || station.url, logo, station.bitrate || 0);
      if (station.stationuuid) {
        VoteStation(state.currentBox.host, state.currentBox.port, station.stationuuid).catch(() => {});
      }
      showToast(t('preset.savedToKey', { n: i, name: station.name }));
    },
  });
}
function closePick() { $('pickModal').classList.add('hidden'); }

// ---------- Box Einstellungen View ----------

// ROOM_NAMES_BY_LOCALE contains the common room suggestions for the
// friendly-name combobox. Recomputed via getRoomNames() so a locale
// switch reflects on the next render. Falls back to English for any
// locale we have not localised.
const ROOM_NAMES_BY_LOCALE = {
  de: [
    'Wohnzimmer', 'Schlafzimmer', 'Küche', 'Esszimmer',
    'Bad', 'Arbeitszimmer', 'Büro', 'Kinderzimmer',
    'Gästezimmer', 'Flur', 'Diele', 'Eingang',
    'Garten', 'Terrasse', 'Balkon', 'Werkstatt',
    'Hobbyraum', 'Keller', 'Dachboden', 'Garage',
  ],
  fr: [
    'Salon', 'Chambre', 'Cuisine', 'Salle à manger',
    'Salle de bain', 'Bureau', 'Espace de travail', 'Chambre d\'enfant',
    'Chambre d\'amis', 'Couloir', 'Entrée',
    'Jardin', 'Terrasse', 'Balcon', 'Atelier',
    'Salle de loisirs', 'Sous-sol', 'Grenier', 'Garage',
  ],
  es: [
    'Salón', 'Dormitorio', 'Cocina', 'Comedor',
    'Baño', 'Estudio', 'Oficina', 'Habitación infantil',
    'Habitación de invitados', 'Pasillo', 'Entrada',
    'Jardín', 'Patio', 'Balcón', 'Taller',
    'Sala de ocio', 'Sótano', 'Ático', 'Garaje',
  ],
  ja: [
    'リビング', '寝室', 'キッチン', 'ダイニング',
    'バスルーム', '書斎', 'オフィス', '子供部屋',
    'ゲストルーム', '廊下', '玄関',
    '庭', 'テラス', 'バルコニー', '作業部屋',
    '趣味の部屋', '地下室', '屋根裏', 'ガレージ',
  ],
  uk: [
    'Вітальня', 'Спальня', 'Кухня', 'Їдальня',
    'Ванна', 'Кабінет', 'Офіс', 'Дитяча',
    'Кімната для гостей', 'Коридор', 'Передпокій',
    'Сад', 'Тераса', 'Балкон', 'Майстерня',
    'Кімната для хобі', 'Підвал', 'Горище', 'Гараж',
  ],
  nl: [
    'Woonkamer', 'Slaapkamer', 'Keuken', 'Eetkamer',
    'Badkamer', 'Studeerkamer', 'Kantoor', 'Kinderkamer',
    'Logeerkamer', 'Gang', 'Hal',
    'Tuin', 'Terras', 'Balkon', 'Werkplaats',
    'Hobbykamer', 'Kelder', 'Zolder', 'Garage',
  ],
  pl: [
    'Salon', 'Sypialnia', 'Kuchnia', 'Jadalnia',
    'Łazienka', 'Gabinet', 'Biuro', 'Pokój dziecięcy',
    'Pokój gościnny', 'Korytarz', 'Przedpokój',
    'Ogród', 'Taras', 'Balkon', 'Warsztat',
    'Pokój hobby', 'Piwnica', 'Strych', 'Garaż',
  ],
  lt: [
    'Svetainė', 'Miegamasis', 'Virtuvė', 'Valgomasis',
    'Vonia', 'Darbo kambarys', 'Biuras', 'Vaikų kambarys',
    'Svečių kambarys', 'Koridorius', 'Prieškambaris',
    'Sodas', 'Terasa', 'Balkonas', 'Dirbtuvė',
    'Pomėgių kambarys', 'Rūsys', 'Palėpė', 'Garažas',
  ],
  lv: [
    'Viesistaba', 'Guļamistaba', 'Virtuve', 'Ēdamistaba',
    'Vannasistaba', 'Kabinets', 'Birojs', 'Bērnu istaba',
    'Viesu istaba', 'Gaitenis', 'Priekštelpa',
    'Dārzs', 'Terase', 'Balkons', 'Darbnīca',
    'Hobiju istaba', 'Pagrabs', 'Bēniņi', 'Garāža',
  ],
  tr: [
    'Oturma Odası', 'Yatak Odası', 'Mutfak', 'Yemek Odası',
    'Banyo', 'Çalışma Odası', 'Ofis', 'Çocuk Odası',
    'Misafir Odası', 'Koridor', 'Giriş',
    'Bahçe', 'Teras', 'Balkon', 'Atölye',
    'Hobi Odası', 'Bodrum', 'Çatı Katı', 'Garaj',
  ],
  en: [
    'Living Room', 'Bedroom', 'Kitchen', 'Dining Room',
    'Bathroom', 'Study', 'Office', 'Kid\'s Room',
    'Guest Room', 'Hallway', 'Entrance',
    'Garden', 'Patio', 'Balcony', 'Workshop',
    'Hobby Room', 'Basement', 'Attic', 'Garage',
  ],
};
function getRoomNames() {
  return ROOM_NAMES_BY_LOCALE[getLocale()] || ROOM_NAMES_BY_LOCALE.en;
}

$('view-settings').innerHTML = `
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
  $('settingsRefreshBtn').disabled = true;
  await discoverBoxes();
  renderSettingsBoxSelect();
  loadBoxSettings();
  $('settingsRefreshBtn').disabled = false;
};

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
  return `${trimmed} ${suffix}`;
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
  const localized = localizeLanguageName(e.key);
  return localized && localized.toLowerCase() !== e.endonym.toLowerCase()
    ? `${e.endonym} (${localized})`
    : e.endonym;
}

function langOptionsHtml() {
  // Sort by the localised name (a Latin string in the current UI
  // language) so the order is predictable for the majority; the
  // native-script endonym stands out for speakers scanning for theirs.
  return BOSE_LANG_IDS
    .map((id) => ({ id, label: boseLangLabel(id), sort: localizeLanguageName(BOSE_LANGS[id].key) }))
    .sort((a, b) => a.sort.localeCompare(b.sort))
    .map((o) => `<option value="${o.id}">${escapeHtml(o.label)}</option>`)
    .join('');
}

async function loadBoxSettings() {
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
    if (go) go.onclick = () => switchView('setup');
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
    if (go) go.onclick = () => switchView('setup');
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
  state.settingsReconnect = state.settingsReconnect || { attempts: 0, max: 10 };
  state.settingsReconnect.attempts++;
  const remaining = state.settingsReconnect.max - state.settingsReconnect.attempts;

  if (remaining > 0) {
    const bannerHtml = `
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
    if (b2) b2.onclick = () => { state.settingsReconnect = null; switchView('box'); };
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
      <button class="btn btn-mini" id="wlanSwitchToggle" style="margin-top:8px">${escapeHtml(t('settingsView.wlanSwitchToggle'))}</button>
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

    <div class="settings-section hidden" id="airplayOptSection">
      <h3>${escapeHtml(t('settingsView.airplayOptHeading'))}</h3>
      <div class="setting-row">
        <button class="btn btn-mini toggle-btn" id="airplayOptOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="airplayOptOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.airplayOptHelp'))}</small>
    </div>

    <details class="settings-section settings-expert">
      <summary class="settings-expert-summary">${escapeHtml(t('settingsView.webhookHeading'))} <span class="expert-badge">${escapeHtml(t('settingsView.expertBadge'))}</span></summary>
      <small class="muted small expert-intro">${escapeHtml(t('settingsView.webhookHelp'))}</small>
      <div class="setting-row">
        <input type="text" id="webhookUrl" autocomplete="off" placeholder="${escapeAttr(t('settingsView.webhookUrlPlaceholder'))}" />
        <select id="webhookMethod" style="flex:0 0 90px;">
          <option value="GET">GET</option>
          <option value="POST">POST</option>
        </select>
      </div>
      <div class="setting-row" id="webhookBodyRow">
        <input type="text" id="webhookBody" autocomplete="off" placeholder="${escapeAttr(t('settingsView.webhookBodyPlaceholder'))}" />
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
      return `<details class="settings-section settings-expert">
      <summary class="settings-expert-summary">${escapeHtml(t('settingsView.copyPresetsHeading'))} <span class="expert-badge">${escapeHtml(t('settingsView.expertBadge'))}</span></summary>
      <small class="muted small expert-intro">${escapeHtml(t('settingsView.copyPresetsHelp'))}</small>
      <div class="setting-row">
        <select id="copyPresetTarget" style="flex:1;">${opts}</select>
        <button class="btn btn-mini btn-warning" id="copyPresetBtn">${escapeHtml(t('settingsView.copyPresetsBtn'))}</button>
      </div>
    </details>`;
    })()}

    <div class="settings-section">
      <h3>${escapeHtml(t('settingsView.sourcesHeading'))}</h3>
      <div class="sources-grid">
        ${sources.map(src => {
          const cls = src.status === 'READY' ? 'src-ok' : 'src-unav';
          const label = sourceLabel(src.source);
          const statusLabel = src.status === 'READY' ? t('settingsView.sourceActive') : t('settingsView.sourceInactive');
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
    <div class="settings-section" id="stickInfoSection">
      <h3>${escapeHtml(t('settingsView.statusHeading'))}</h3>
      <div id="stickInfoBody"><span class="muted small">${escapeHtml(t('common.loading'))}</span></div>
    </div>
    <div class="settings-section">
      <h3>${escapeHtml(t('settingsView.actionsHeading'))}</h3>
      <div class="actions-grid">
        <button class="btn btn-mini" id="boxSyncPresetsBtn">${escapeHtml(t('settingsView.syncHardwareKeys'))}</button>
        <p class="muted small">${escapeHtml(t('settingsView.syncHardwareKeysHelp'))}</p>
        <button class="btn btn-mini" id="boxSaveLogsBtn">${escapeHtml(t('settingsView.saveLogs'))}</button>
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
    <div class="settings-section">
      <h3>${escapeHtml(t('settingsView.speakerInfoHeading'))}</h3>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.modelLabel'))}</span><span class="kv-val">${escapeHtml(info.type || '-')}</span></div>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.firmwareLabel'))}</span>
        <span class="kv-val">${fwStatusInline(info)}</span>
      </div>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.deviceIdLabel'))}</span><span class="kv-val small muted">${escapeHtml(info.deviceID || '-')}</span></div>
      ${fwUpdateHint(info)}
    </div>
  `;

  // Name combobox: input + dropdown list, free-typeable + filterable.
  wireCombobox('boxNameInput', 'boxNameToggle', 'boxNameList', getRoomNames());

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
      const sameVer = boxVer === appVer;
      const sameBuild = boxBuild === appBuild;
      if (sameVer && sameBuild) {
        const buildSuffix = boxBuild ? ` (Build ${escapeHtml(boxBuild)})` : '';
        softwareLine = `<span class="fw-ok">&#10003; ${escapeHtml(t('settingsView.swCurrent'))}</span> <span class="muted small">${escapeHtml(boxVer)}${buildSuffix}</span>`;
      } else if (sameVer && !sameBuild) {
        softwareLine = `<span class="fw-pending">${escapeHtml(t('settingsView.swUpdateAvail'))}</span> <span class="muted small">${escapeHtml(boxBuild)} &rarr; ${escapeHtml(appBuild)}</span>`;
        if (state.otaInProgress && state.otaTargetHost && state.otaTargetHost !== box.host) {
          softwareBtn = `<button class="btn btn-mini btn-primary" id="stickInfoUpdateBtn" disabled>${escapeHtml(t('update.otherBoxRunning', { name: state.otaTargetName || '...' }))}</button>`;
        } else {
          softwareBtn = `<button class="btn btn-mini btn-primary" id="stickInfoUpdateBtn">${escapeHtml(t('update.refreshBtn'))}</button>`;
        }
      } else {
        softwareLine = `<span class="fw-old">${escapeHtml(t('settingsView.swOutdated'))}</span> <span class="muted small">${escapeHtml(boxVer)} &rarr; ${escapeHtml(appVer)}</span>`;
        if (state.otaInProgress && state.otaTargetHost && state.otaTargetHost !== box.host) {
          softwareBtn = `<button class="btn btn-mini btn-primary" id="stickInfoUpdateBtn" disabled>${escapeHtml(t('update.otherBoxRunning', { name: state.otaTargetName || '...' }))}</button>`;
        } else {
          softwareBtn = `<button class="btn btn-mini btn-primary" id="stickInfoUpdateBtn">${escapeHtml(t('update.refreshBtn'))}</button>`;
        }
      }
    } catch {}

    // USB stick mount status. Try /api/stick/status first (newer
    // agent); fall back to /api/debug/state.stick_listing for older
    // agent versions.
    let stickLine = `<span class="muted small">${escapeHtml(t('common.unknown'))}</span>`;
    let stickMounted = false;
    let sshOpen = false;
    try {
      const r = await boxFetch(box, '/api/stick/status');
      const ct = r.headers.get('content-type') || '';
      if (r.ok && ct.includes('json')) {
        const data = await r.json();
        if (data.mounted) {
          stickMounted = true;
          stickLine = `<span class="fw-ok">&#10003; ${escapeHtml(t('settingsView.stickDetected'))}</span>` + (data.version ? ` <span class="muted small">${escapeHtml(data.version)}</span>` : '');
        } else {
          // After a clean install the stick is pulled, so "not mounted" is
          // the expected steady state. Show it informationally, not as an
          // error in signal-red.
          stickLine = `<span class="muted small">${escapeHtml(t('settingsView.stickRemoved'))}</span>`;
        }
        sshOpen = !!data.sshOpen;
      } else {
        // Fallback: debug/state listing for older agents.
        const rd = await boxFetch(box, '/api/debug/state');
        if (rd.ok && (rd.headers.get('content-type') || '').includes('json')) {
          const d = await rd.json();
          const listing = d.stick_listing;
          if (Array.isArray(listing) && listing.length > 0 && !String(listing[0]).startsWith('ERR')) {
            stickMounted = true;
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
    const gb = $('globalSecurityBanner');
    if (gb) {
      const show = sshOpen && !stickMounted && !state.otaInProgress;
      gb.classList.toggle('hidden', !show);
    }

    // Same guard as the global banner above: never show the "reboot now"
    // recommendation while the stick is still mounted (initial install or
    // an update is applying) or an OTA is in flight. Rebooting then would
    // interrupt the install/update. Only after it completes and the stick
    // is removed is the SSH state meaningful and the reboot safe.
    const securityWarn = (sshOpen && !stickMounted && !state.otaInProgress) ? `
      <div class="security-warn">
        <div class="security-warn-title">${escapeHtml(t('banner.recommendationShort'))}</div>
        <div class="security-warn-text">
          ${escapeHtml(t('banner.sshRecommend'))}
        </div>
        <button class="btn btn-mini" id="securityRebootBtn">${escapeHtml(t('speaker.rebootNow'))}</button>
      </div>` : '';

    body.innerHTML = `
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.softwareLabel'))}</span>
        <span class="kv-val">${softwareLine} ${softwareBtn}</span></div>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.usbStickLabel'))}</span>
        <span class="kv-val">${stickLine}</span></div>
      ${securityWarn}
    `;
    const ub = $('stickInfoUpdateBtn');
    if (ub) {
      // The stick-in confirmation now lives inside doBoxUpdate (the single
      // OTA chokepoint), so both this button and the music-tab banner are
      // gated identically and Cancel always aborts before anything starts.
      ub.onclick = doBoxUpdate;
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
        setTimeout(discoverBoxes, 35000);
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
        setTimeout(discoverBoxes, 35000);
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
          setTimeout(discoverBoxes, 60000);
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
          setTimeout(discoverBoxes, 60000);
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
        setTimeout(discoverBoxes, 60000);
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
  if (whUrl && whOn && whOff && whSave) {
    let whEnabled = false;
    const paintWh = (en) => {
      whEnabled = en === true;
      whOn.classList.toggle('active', whEnabled === true);
      whOff.classList.toggle('active', whEnabled === false);
    };
    const syncBodyRow = () => {
      // A request body is only sent for non-GET methods.
      if (whBodyRow) whBodyRow.style.display = (whMethod.value === 'GET') ? 'none' : '';
    };
    (async () => {
      try {
        const w = await GetWebhooks(box.host, box.port);
        const th = (w && w.thumb) || {};
        if (th.url) whUrl.value = th.url;
        if (th.method) whMethod.value = th.method;
        if (th.body) whBody.value = th.body;
        paintWh(th.enabled === true);
      } catch { paintWh(false); }
      syncBodyRow();
    })();
    whOn.onclick = () => paintWh(true);
    whOff.onclick = () => paintWh(false);
    whMethod.onchange = syncBodyRow;
    whSave.onclick = async () => {
      const url = whUrl.value.trim();
      if (whEnabled && !url) { showError(t('settingsView.webhookUrlRequired')); return; }
      try {
        await SetWebhooks(box.host, box.port, whEnabled, whMethod.value, url, whBody.value.trim(), '');
        showToast(t('settingsView.webhookSavedToast'));
      } catch (e) { showError(e); }
    };
    whTest.onclick = async () => {
      const url = whUrl.value.trim();
      if (!url) { showError(t('settingsView.webhookUrlRequired')); return; }
      whTest.disabled = true;
      try {
        const r = await TestWebhook(box.host, box.port, whMethod.value, url, whBody.value.trim(), '');
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
        if (state.currentBox && state.currentBox.host === thost) await loadPresets();
      } catch (e) { showError(e); }
      copyBtn.disabled = false;
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
    boxFetch(box, '/api/region').then(r => r.ok ? r.json() : null).then(data => {
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
        const r = await boxFetch(box, '/api/region', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ country: cc }),
        });
        if (!r.ok) throw new Error('HTTP ' + r.status);
        const data = await r.json();
        try { localStorage.removeItem('userTouchedRegion'); } catch {}
        state.searchCountry = data.country;
        state.searchLang = data.language;
        saveSearchCountry(state.searchCountry);
        const cs = $('searchCountry');
        if (cs) cs.value = data.country;
        updateFilterIndicators();
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
      renderBoxSelect();
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
      const r = await boxFetch(box, '/api/box/wlan', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ssid, password: pass }),
      });
      if (!r.ok) {
        const body = await r.text();
        throw new Error('HTTP ' + r.status + ': ' + body);
      }
      $('boxWlanPass').value = '';
      form.classList.add('hidden');
      showToast(t('settingsView.wlanSwitchedToast'));
      // The speaker likely gets a new IP via DHCP. Retrigger
      // discovery in 10 s.
      setTimeout(discoverBoxes, 10000);
    } catch (e) { showError(e); }
  };
}

// wireCombobox binds to an <input> + <button toggle> + <ul list>
// trio. Filters while typing, opens on toggle click, selects on
// item click.
function wireCombobox(inputId, toggleId, listId, options) {
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
          <li>${t('fw.step4')} <span class="kv-val">downloads.bose.com/ced/soundtouch/soundtouch_usb/</span></li>
        </ol>
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
const throttledSetVolume = makeOneInFlightThrottle(SetBoxVolume);
const throttledSetBass = makeOneInFlightThrottle(SetBoxBass);

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


// ---------- Setup View ----------

$('view-setup').innerHTML = `
  <h2>${escapeHtml(t('setup.heading'))}</h2>
  <p class="setup-intro">${escapeHtml(t('setup.intro'))}</p>
  <div class="setup-stay-on-wifi-note">${escapeHtml(t('setup.stayOnHomeWifi'))}</div>
  <div class="setup-section setup-target-section" id="setupTargetSection">
    <h3>${escapeHtml(t('setup.targetHeading'))}</h3>
    <p class="muted small">${escapeHtml(t('setup.targetIntro'))}</p>
    <div id="setupTargetBody"></div>
  </div>
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
    <div id="setupResult" class="setup-result"></div>
  </div>
`;

// Wire the setup-tab name combobox with the same helper used in
// Settings.
wireCombobox('setupName', 'setupNameToggle', 'setupNameList', getRoomNames());

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
$('setupGo').onclick = doSetup;
$('wlanRefresh').onclick = loadWifiProfiles;
$('wlanSelect').onchange = onWifiSelect;
$('wlanShowPass').onclick = togglePasswordVisibility;

// ---------- Setup target picker ----------
//
// Pierre's failure mode (#44): when more than one stock Bose speaker
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

function renderSetupTargetPicker() {
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
    // A box already on Wi-Fi (the common case) just needs STR added via
    // the stick: no reset, the existing Wi-Fi stays. Reassure here so
    // users do not reach for a factory reset they do not need.
    if (isSelected('stock', b.host)) {
      cards += `<div class="setup-target-factory-help muted small">${escapeHtml(t('setup.stockKeepsWifi'))}</div>`;
    }
  }
  for (const b of strBoxes) {
    const label = b.friendlyName || b.name || b.host;
    cards += cardHTML('str', b.host, label,
      boxIdentLine(b, t('setup.targetCardKindSTR')),
      t('setup.targetCardBadgeSTR'), 'badge-ok');
  }
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

  // Locked pill summarising the current choice, rendered below the
  // cards. Hidden when no target is selected (very rare; the
  // default fallbacks above usually populate something).
  let pill = '';
  if (sel) {
    let targetLabel;
    if (sel.kind === 'factory-reset') {
      targetLabel = t('setup.targetFactoryLabel');
    } else {
      const b = sel.box;
      const friendly = b.friendlyName || b.name || b.host;
      const idTail = [];
      if (b.model) idTail.push(b.model);
      if (b.serialNumber) idTail.push(`SN ${b.serialNumber}`);
      idTail.push(b.host);
      targetLabel = `${friendly} (${idTail.join(' · ')})`;
    }
    pill = `<div class="setup-target-pill"><span class="muted small">${escapeHtml(t('setup.targetPickedFor'))}</span> ` +
           `<b>${escapeHtml(targetLabel)}</b></div>`;
  }

  body.innerHTML = `<div class="setup-target-cards" role="radiogroup" aria-label="${escapeAttr(t('setup.targetHeading'))}">${cards}</div>${pill}`;

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
// concrete thing Pierre (#44) asked for.
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

// macOS gates the System keychain behind an admin prompt for every
// `security find-generic-password -ga`. Auto-firing the password
// fetch at app startup pops the prompt every single launch (#88).
// We still want auto-fill on Windows (netsh -> no prompt for
// user-saved profiles) and Linux (nmcli -> no prompt). On macOS,
// the SSID gets auto-selected but the password field stays empty
// until the user clicks the explicit fill-from-keychain button.
const isMacOS = /Mac OS X|Macintosh/.test(navigator.userAgent);

async function loadWifiProfiles() {
  const sel = $('wlanSelect');
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

async function refreshDrives(clearResult) {
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
  if (!state.drives.length) {
    $('drivesList').innerHTML = `<div class="muted">${escapeHtml(t('setup.noSticksFound'))}</div>`;
    $('setupGo').disabled = true;
    $('updateInfo').classList.add('hidden');
    $('formatWarn').classList.add('hidden');
    return;
  }
  $('drivesList').innerHTML = state.drives.map((d, i) => {
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
      upd.innerHTML = (same
        ? `<b>${escapeHtml(t('setup.stickCurrent'))}</b> <small>${escapeHtml(t('setup.versionLabel', { version: fromShort }))}</small>`
        : `<b>${escapeHtml(t('setup.stickUpdateAvail'))}</b> <small>${escapeHtml(fromShort)} &rarr; ${escapeHtml(toFull)}</small>`)
        + ` <div class="muted small" style="margin-top:6px">${escapeHtml(t('setup.alreadyConfigured'))}</div>`;
      upd.classList.remove('hidden');
    } catch {
      upd.classList.add('hidden');
    }
    warn.classList.add('hidden');
    btn.textContent = t('setup.goBtnUpdate');
  } else {
    upd.classList.add('hidden');
    warn.classList.remove('hidden');
    btn.textContent = t('setup.goBtn');
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
  const wantFormat = $('setupFormat') && $('setupFormat').checked;
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
        $('setupResult').innerHTML = `<div class="setup-err">${escapeHtml(t('setup.formatFailed', { err: String(fErr) }))}</div>`;
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
    discoverBoxes();
    // Show a confirmation panel first. We do NOT start the discovery
    // + auto-install loop yet: the user just clicked "GO", the stick
    // is still in the laptop, the speaker has not been touched.
    // Starting the loop now would race the user and almost always
    // probe a stale stock state (box up from a previous session,
    // stick not yet inserted) which fails install.sh.
    showAwaitBoxReadyPanel({ ssid, pass, html });
  } catch (e) {
    $('setupResult').innerHTML = `<div class="setup-err">${escapeHtml(t('common.error'))}: ${escapeHtml(String(e))}</div>`;
  }
  $('setupGo').disabled = false;
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
  'stick-missing': ['stickInserted', 'freshBoot', 'stick'],
  'agent-not-up': ['powerCycle', 'wifi', 'logs'],
  'not-reachable': ['wifi', 'freshBoot'],
  'install-window-closed': ['freshBoot'],
  // Media read error: the speaker found install.sh but could not read it.
  // Usually a large stick force-formatted to FAT32 with a block size the
  // speaker can't read (the 64 GB case), or a faulty stick.
  'stick-io-error': ['reformatApp', 'smallerStick', 'differentStick', 'logs'],
};

// installHelpHtml renders the localized help checklist for a failure code.
function installHelpHtml(code) {
  const steps = INSTALL_HELP_STEPS[code] || ['freshBoot', 'wifi', 'stick', 'logs'];
  const items = steps.map(s => `<li>${escapeHtml(t('setup.help.' + s))}</li>`).join('');
  return `<div class="setup-help"><b>${escapeHtml(t('setup.helpTitle'))}</b><ul>${items}</ul></div>`;
}

// Credentials are kept only in this closure and never persisted.
async function waitForBoxAfterSetup({ ssid, pass, html }) {
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

  let foundBox = null;

  // Honour the target the user picked in Step 0 if any. Without
  // this the loop would lock onto an arbitrary speaker on a LAN
  // with multiple Bose units — exactly Pierre's failure mode in
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
  startTicker();
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
      btn.onclick = () => { doBoxUpdate(); };
    }
    discoverBoxes();
    return;
  }

  // Stock box (LAN-found or cold-bootstrapped): run installer.
  const installBase = `<div class="setup-ok">${escapeHtml(t('setup.boxFoundOnLAN', { ip: foundBox.host }))}</div>`;
  const runningLine = `<div class="muted small">${escapeHtml(t('setup.installRunning'))}</div>`;
  render(installBase + runningLine);
  // Live load-settle feedback: while the backend waits for the box's boot-time
  // CPU storm to subside before launching install.sh, it pushes install:progress
  // events. Show "reachable but still finishing boot" with a settle countdown,
  // so the wait reads as deliberate rather than a hung install. Flip back to the
  // running line once the box reports calm (busy=false).
  const offProgress = EventsOn('install:progress', (p) => {
    if (p && p.phase === 'settle' && p.busy) {
      const load = (typeof p.load === 'number') ? p.load.toFixed(2) : '?';
      const secs = Math.max(0, Math.round((p.remainingMs || 0) / 1000));
      render(installBase + `<div class="muted small">${escapeHtml(t('setup.installBoxBusy', { load, secs }))}</div>`);
    } else {
      render(installBase + runningLine);
    }
  });
  let result;
  try {
    result = await InstallSTROnBox(foundBox.host, foundBox.model || foundBox.type || '');
  } catch (err) {
    if (offProgress) offProgress();
    render(`<div class="setup-err">${escapeHtml(t('setup.installFailed', { msg: String(err) }))}</div>`);
    return;
  }
  if (offProgress) offProgress();
  if (!result || !result.ok) {
    const msg = (result && result.message) || 'unknown';
    const help = installHelpHtml(result && result.code);
    const log = (result && result.log)
      ? `<details class="setup-log"><summary>${escapeHtml(t('setup.installLogToggle'))}</summary><pre>${escapeHtml(result.log)}</pre></details>`
      : '';
    render(`<div class="setup-err">${escapeHtml(t('setup.installFailed', { msg }))}</div>` + help + log);
    return;
  }
  render(`<div class="setup-ok">${escapeHtml(t('setup.installDone'))}</div>` +
         `<div class="muted small">${escapeHtml(t('setup.installDoneHint'))}</div>`);
  discoverBoxes();
}


// ---------- Library (DLNA MediaServer browse, BETA) ----------
//
// Per-session state. servers is the discovered MediaServer list,
// keyed by UDN. currentUDN is the one the user picked. stack is the
// folder navigation breadcrumb, where each entry is {id, title}.
const libState = {
  servers: [],
  currentUDN: '',
  stack: [{ id: '0', title: '' }],
  page: null,
  loading: false,
};

async function openLibrary() {
  renderLibrary();
  if (libState.servers.length === 0) {
    await loadMediaServers();
  } else if (libState.currentUDN) {
    await libraryBrowseCurrent();
  }
}

async function loadMediaServers() {
  libState.loading = true;
  renderLibrary();
  try {
    const list = await ListMediaServers(3);
    libState.servers = list || [];
    if (libState.servers.length === 1) {
      libState.currentUDN = libState.servers[0].udn;
      libState.stack = [{ id: '0', title: libState.servers[0].friendlyName || '' }];
      await libraryBrowseCurrent();
      return;
    }
    libState.currentUDN = '';
    libState.page = null;
  } catch (e) {
    showError(`ListMediaServers: ${e}`);
  } finally {
    libState.loading = false;
    renderLibrary();
  }
}

async function libraryPickServer(udn) {
  const srv = libState.servers.find(s => s.udn === udn);
  if (!srv) return;
  libState.currentUDN = udn;
  libState.stack = [{ id: '0', title: srv.friendlyName || '' }];
  await libraryBrowseCurrent();
}

async function libraryBrowseCurrent() {
  if (!libState.currentUDN) return;
  const top = libState.stack[libState.stack.length - 1];
  libState.loading = true;
  renderLibrary();
  try {
    libState.page = await BrowseLibrary(libState.currentUDN, top.id, 0, 100);
  } catch (e) {
    showError(`BrowseLibrary: ${e}`);
    libState.page = null;
  } finally {
    libState.loading = false;
    renderLibrary();
  }
}

async function libraryEnter(container) {
  libState.stack.push({ id: container.id, title: container.title });
  await libraryBrowseCurrent();
}

async function libraryGoTo(depth) {
  // Truncate breadcrumb to the clicked depth.
  if (depth < 0 || depth >= libState.stack.length) return;
  libState.stack = libState.stack.slice(0, depth + 1);
  await libraryBrowseCurrent();
}

async function libraryPlay(item) {
  if (!state.currentBox) {
    showToast(t('library.toastNoBox') || t('common.pickBox'));
    return;
  }
  if (!item.streamURL) {
    showError(t('library.errorNoURL'));
    return;
  }
  try {
    await PlayURL(state.currentBox.host, state.currentBox.port,
      item.streamURL, item.title || '', item.albumArtURL || '', '');
    showToast(t('library.toastPlaying') + ': ' + (item.title || ''));
  } catch (e) {
    showError(`PlayURL: ${e}`);
  }
}

function librarySaveAsPreset(item) {
  if (!state.currentBox) {
    showToast(t('library.toastNoBox') || t('common.pickBox'));
    return;
  }
  if (!item.streamURL) {
    showError(t('library.errorNoURL'));
    return;
  }
  // The media server this track came from, stored on the preset so the tile can
  // show a small "from <server>" badge.
  const srv = libState.servers.find(s => s.udn === libState.currentUDN);
  const source = (srv && (srv.friendlyName || srv.address)) || '';
  showSlotPicker({
    title: t('library.assignTitle'),
    subtitle: [item.artist, item.title].filter(Boolean).join(' — ') || item.title || '',
    onPick: async (i) => {
      await SaveLibraryPreset(state.currentBox.host, state.currentBox.port, i,
        item.title || '(track)', item.streamURL, item.albumArtURL || '', 0, source);
      showToast(t('preset.savedToKey', { n: i, name: item.title || '(track)' }));
    },
  });
}

function renderLibrary() {
  const el = $('view-library');
  if (!el) return;
  const intro = `
    <div class="library-header">
      <h2>${escapeHtml(t('library.title'))}</h2>
      <p class="library-sub">${escapeHtml(t('library.subtitle'))}</p>
    </div>`;

  if (libState.loading) {
    el.innerHTML = intro + `<p class="library-loading">${escapeHtml(t('library.loading'))}</p>`;
    return;
  }

  // Server picker section.
  let serverPicker = '';
  if (libState.servers.length === 0) {
    serverPicker = `
      <div class="library-empty">
        <p>${escapeHtml(t('library.noServers'))}</p>
        <button class="btn" id="libRefreshBtn">${escapeHtml(t('library.refresh'))}</button>
      </div>`;
  } else {
    const opts = libState.servers.map(s => {
      const sel = s.udn === libState.currentUDN ? ' selected' : '';
      const sub = s.modelName ? ` (${escapeHtml(s.modelName)})` : '';
      return `<option value="${escapeAttr(s.udn)}"${sel}>${escapeHtml(s.friendlyName || s.address)}${sub}</option>`;
    }).join('');
    serverPicker = `
      <div class="library-server-row">
        <label class="library-label">${escapeHtml(t('library.server'))}</label>
        <select class="library-select" id="libServerSelect">${opts}</select>
        <button class="btn btn-mini" id="libRefreshBtn" title="${escapeAttr(t('library.refresh'))}">&#8634;</button>
      </div>`;
  }

  // Breadcrumb + folder/track listing.
  let body = '';
  if (libState.currentUDN && libState.page) {
    const crumbs = libState.stack.map((s, i) => {
      const lbl = s.title || t('library.root');
      const isLast = i === libState.stack.length - 1;
      return isLast
        ? `<span class="library-crumb-active">${escapeHtml(lbl)}</span>`
        : `<a href="#" class="library-crumb" data-depth="${i}">${escapeHtml(lbl)}</a>`;
    }).join(' <span class="library-crumb-sep">&rsaquo;</span> ');

    const containers = (libState.page.containers || []).map(c => `
      <li class="library-row library-row-folder" data-cid="${escapeAttr(c.id)}">
        <span class="library-icon">&#128194;</span>
        <span class="library-title">${escapeHtml(c.title)}</span>
        ${c.childCount > 0 ? `<span class="library-meta">${c.childCount}</span>` : ''}
      </li>`).join('');

    const items = (libState.page.items || []).map(it => {
      const meta = [it.artist, it.album].filter(Boolean).join(' — ');
      const dur = it.durationSec > 0 ? ` <span class="library-duration">${formatDuration(it.durationSec)}</span>` : '';
      return `
        <li class="library-row library-row-track" data-iid="${escapeAttr(it.id)}">
          <span class="library-icon">&#9835;</span>
          <span class="library-title">
            <span class="library-track-title">${escapeHtml(it.title)}</span>
            ${meta ? `<span class="library-track-meta">${escapeHtml(meta)}</span>` : ''}
          </span>
          ${dur}
          <span class="library-actions">
            <button class="btn btn-mini lib-play-btn" data-iid="${escapeAttr(it.id)}" title="${escapeAttr(t('library.play'))}">${escapeHtml(t('library.play'))}</button>
            <button class="btn btn-mini btn-secondary lib-preset-btn" data-iid="${escapeAttr(it.id)}" title="${escapeAttr(t('library.saveAsPreset'))}">${escapeHtml(t('library.saveAsPreset'))}</button>
          </span>
        </li>`;
    }).join('');

    const empty = (!containers && !items) ? `<p class="library-empty-folder">${escapeHtml(t('library.emptyFolder'))}</p>` : '';

    body = `
      <div class="library-crumbs">${crumbs}</div>
      <ul class="library-list">${containers}${items}</ul>
      ${empty}`;
  } else if (libState.servers.length > 0 && !libState.currentUDN) {
    body = `<p class="library-pick-server">${escapeHtml(t('library.pickServer'))}</p>`;
  }

  el.innerHTML = intro + serverPicker + body;

  // Wire interactions.
  const sel = $('libServerSelect');
  if (sel) sel.onchange = () => libraryPickServer(sel.value);
  const ref = $('libRefreshBtn');
  if (ref) ref.onclick = () => loadMediaServers();

  el.querySelectorAll('.library-crumb').forEach(a => {
    a.onclick = (e) => { e.preventDefault(); libraryGoTo(parseInt(a.dataset.depth, 10)); };
  });
  el.querySelectorAll('.library-row-folder').forEach(row => {
    row.onclick = () => {
      const id = row.dataset.cid;
      const c = (libState.page.containers || []).find(x => x.id === id);
      if (c) libraryEnter(c);
    };
  });
  el.querySelectorAll('.lib-play-btn').forEach(btn => {
    btn.onclick = (e) => {
      e.stopPropagation();
      const it = (libState.page.items || []).find(x => x.id === btn.dataset.iid);
      if (it) libraryPlay(it);
    };
  });
  el.querySelectorAll('.lib-preset-btn').forEach(btn => {
    btn.onclick = (e) => {
      e.stopPropagation();
      const it = (libState.page.items || []).find(x => x.id === btn.dataset.iid);
      if (it) librarySaveAsPreset(it);
    };
  });
}

function formatDuration(sec) {
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}:${s.toString().padStart(2, '0')}`;
}

renderFooter();

// Prefill from the cache first so the UI shows the last selected
// speaker immediately. discoverBoxes refreshes the real list in the
// background within a few seconds.
(function bootFromCache() {
 // Wrapped end to end: this runs synchronously at module load and only
 // when a cached speaker exists in localStorage. A throw here would abort
 // the rest of the bootstrap (discoverBoxes + the refreshStatus timer
 // below never run) and leave the window blank, which presents to the
 // user as the app flashing up and quitting. Never let prefill-render do
 // that: on any failure, log and fall through to live discovery.
 try {
  const cached = loadCachedBoxes();
  if (cached.length === 0) return;
  state.boxes = cached;
  const lastID = loadLastBox();
  const target = lastID ? cached.find(b => b.deviceID === lastID) : null;
  if (target) {
    state.currentBox = target;
    renderBoxSelect();
    loadPresets();
    refreshStatus();
    loadTaxonomy();
    loadStickRegion();
    // Also fire the OTA check on boot. Without this, an app that
    // boots while speaker version === app version skips
    // checkBoxUpdate (it only fires from discoverBoxes on
    // `changed=true`), and the music-tab banner would never reflect
    // a build-stamp mismatch even though the speaker-settings tab
    // surfaces it independently.
    checkBoxUpdate();
  } else {
    renderBoxSelect();
  }
 } catch (e) {
  try { console.warn('bootFromCache failed, falling back to live discovery', e); } catch {}
 }
})();

try { discoverBoxes(); } catch (e) { try { console.warn('discoverBoxes failed', e); } catch {} }
// loadWifiProfiles() no longer fires at app start. Defers the OS
// WiFi profile lookup to Setup-tab activation (see switchView).
// Was the cause of the macOS keychain prompt on every launch (#88)
// even after the v0.5.16 isMacOS gate, and is also redundant on
// Windows / Linux for users who never visit the Setup tab.
// Adaptive status poll. A fixed 2 s interval meant ~30 now_playing
// requests/min at the speaker. On BCO speakers (Portable, ST20-spotty) the
// Bose firmware app cannot sustain that: its memory and the system load
// climb steadily until a firmware watchdog reboots the box about every
// 25 minutes (confirmed live 2026-06-02 on a Portable: with the desktop app
// killed the memAvailable freefall stopped cold and load fell from 5 to 1.5,
// while gabbo + autopair kept running). So poll moderately while audio is
// actually playing (metadata/volume move) and slowly when idle or in
// standby (nothing changes). Every user action still fires its own
// immediate refreshStatus, so feedback stays snappy regardless of cadence.
function nextStatusDelayMs() {
  if (state.view !== 'box' || !state.currentBox) return 15000;
  const ps = state.nowPlayState;
  if (ps === 'PLAY_STATE' || ps === 'BUFFERING_STATE') return 5000;
  return 15000;
}
(function statusPollLoop() {
  setTimeout(async () => {
    try { await refreshStatus(); } catch {}
    statusPollLoop();
  }, nextStatusDelayMs());
})();
