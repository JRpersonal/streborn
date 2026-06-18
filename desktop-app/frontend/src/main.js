import './style.css';
import {
  DiscoverBoxes,
  RefreshKnownBoxes,
  GetPresets,
  SetPreset,
  DeletePreset,
  PlaySlot,
  PlayURL,
  VoteStation,
  RebootBox,
  SyncBoxPresets,
  BoxPresets,
  BoxSnapshot,
  RecallBoxPreset,
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
  DownloadUpdate,
  ApplyUpdate,
  RevealUpdateFile,
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
  GetResumeOnPowerOn,
  SetResumeOnPowerOn,
  GetAppFlag,
  SetAppFlag,
  RescuedSpeakerCount,
  GetWebhooks,
  SetWebhooks,
  SaveWebhookConfig,
  TestWebhook,
  StreamBitrate,
  StreamTitle,
  SpotifyBitrate,
  SpotifyNowPlaying,
  SaveSpotifyPreset,
  SaveLibraryPreset,
  RecentPlayed,
  SaveDiagnosticBundle,
  GetLogFilePath,
  InstallSTROnBox,
  RepairInstallViaSSH,
  RadioSearch,
  RadioTags,
  RadioLanguages,
  RadioClick,
  TrueFactoryReset,
  UninstallSTR,
  ProbeSetupAP,
  PushWLANToBox,
  ListMediaServers,
  BrowseLibrary,
  LogClientError,
  BrowserOpenURL,
  GetZoneState,
  EventsOn,
} from './api.js';

// Global frontend crash capture, registered as early as possible.
// A JavaScript error during startup does not reach str.log on its own,
// so a "flashes up and quits" leaves nothing to diagnose. Forward any
// uncaught error or rejected promise to the Go logger. Best-effort:
// the handlers never throw themselves.
(function installClientErrorHooks() {
  const seen = new Set();
  // Show the error ON SCREEN, persistently, so a user can screenshot it.
  // str.log is reset per launch, so an error that crashes/blanks the view is
  // otherwise lost on restart (the cause of #121 being un-diagnosable: the
  // saved diagnostic only ever held the startup lines). The banner makes the
  // real message + stack visible immediately, regardless of which view broke.
  const showBanner = (text) => {
    const add = () => {
      try {
        if (!document.body) return;
        let el = document.getElementById('__strErrBanner');
        if (!el) {
          el = document.createElement('div');
          el.id = '__strErrBanner';
          el.style.cssText = 'position:fixed;left:0;right:0;bottom:0;z-index:99999;max-height:42vh;overflow:auto;background:#3a0d0d;color:#ffd7d7;font:12px/1.45 monospace;padding:10px 38px 12px 12px;border-top:2px solid #c0392b;white-space:pre-wrap';
          const close = document.createElement('button');
          close.textContent = '×';
          close.style.cssText = 'position:absolute;top:4px;right:10px;background:transparent;color:#ffd7d7;border:0;font-size:20px;cursor:pointer';
          close.onclick = () => el.remove();
          el.appendChild(close);
          const body = document.createElement('div');
          body.id = '__strErrBannerBody';
          el.appendChild(body);
          document.body.appendChild(el);
        }
        const body = document.getElementById('__strErrBannerBody');
        body.textContent = (body.textContent ? body.textContent + '\n\n' : '') + text;
      } catch {}
    };
    if (typeof document !== 'undefined' && document.body) add();
    else if (typeof window !== 'undefined') window.addEventListener('DOMContentLoaded', add);
  };
  const report = (kind, detail) => {
    try { LogClientError(`${kind}: ${detail}`); } catch {}
    try { console.error(kind, detail); } catch {}
    const key = kind + ':' + String(detail).slice(0, 200);
    if (!seen.has(key)) { seen.add(key); showBanner(`STR ${kind}:\n${detail}`); }
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
  compareVerBuild,
  boxModelSupport,
  getBoxLabel,
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
  SPOTIFY_LOGO,
} from './logos.js';

// First view extracted out of this monolith into its own module (#135). The view
// pulls state/utils/i18n/api from the shared modules; only the slot-picker modal
// is main.js-local, injected below. New views should follow this pattern so this
// file stops growing.
import { renderRecent, initRecentView } from './views/recent.js';
import { renderMultiroom, initMultiroomView } from './views/multiroom.js';
import { renderSpotifyAlpha, initSpotifyView } from './views/spotify.js';
// Speaker Settings view (extracted from this monolith, same pattern as the views
// above). loadBoxSettings is the entry point switchView calls; langOptionsHtml /
// wireCombobox are reused by the Setup view below. throttledSetVolume /
// throttledSetBass are the shared volume-throttle instances the music view here
// reuses (the settings sliders and the music-view volume control share one
// throttle), so they are imported rather than redefined.
import {
  loadBoxSettings,
  langOptionsHtml,
  wireCombobox,
  initSettingsView,
  throttledSetVolume,
  throttledSetBass,
} from './views/settings.js';
// Library (DLNA MediaServer browse) view, extracted from this monolith, same
// pattern as the views above. openLibrary is the entry point switchView calls;
// showSlotPicker / formatDuration are main.js-local helpers it reuses, injected
// below.
import { openLibrary, initLibraryView } from './views/library.js';
// USB stick setup / install wizard view (extracted from this monolith, same
// pattern as the views above). renderSetupTargetPicker is the entry point
// switchView calls; refreshDrives + loadWifiProfiles are also called from
// switchView on Setup-tab activation. switchView / discoverBoxes / doBoxUpdate /
// getRoomNames are main.js-local helpers it reuses, injected below.
import {
  renderSetupTargetPicker,
  loadWifiProfiles,
  refreshDrives,
  initSetupView,
} from './views/setup.js';
// Inject the main.js-local helpers the views reuse so they behave exactly as
// before without reimplementing them. All hoisted function declarations, safe
// to pass here.
initRecentView({ showSlotPicker, playStation, openPick, toggleFav, isFav });
initMultiroomView({ boxNeedsUpdate, discoverBoxes });
initSpotifyView({
  switchView,
  // Live STR speaker list for the "sync Spotify login to all speakers" action.
  strBoxes: () => (state.boxes || [])
    .filter(b => b && b.kind !== 'stock' && b.deviceID && b.host)
    .map(b => ({ host: b.host, port: b.port, name: getBoxLabel(b) })),
});
initSettingsView({ switchView, updateFilterIndicators, discoverBoxes, renderBoxSelect, boxFetch, localizeLanguageName, doBoxUpdate, loadPresets, getRoomNames });
initLibraryView({ showSlotPicker, formatDuration });
initSetupView({ switchView, discoverBoxes, doBoxUpdate, getRoomNames });

// __nextLogoFallback walks a preset logo <img>'s data-fallbacks list (a
// pipe-separated set of candidate URLs) on each load error, swapping in the
// next candidate. The list always ends in a locally generated monogram data
// URI, which always loads, so a station whose favicon is missing or fails to
// load shows a clean letter tile instead of a broken-image icon (VRT
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

// Delegated logo-fallback: drive the data-fallbacks cascade from a single
// capture-phase 'error' listener instead of an inline onerror="" attribute on
// every <img>. Inline handlers require a CSP 'unsafe-inline' script-src, which
// we deliberately do NOT allow (see index.html CSP). 'error' does not bubble,
// so we listen in the capture phase, where it still reaches us. Only acts while
// data-fallbacks still has candidates, so it stops once the monogram loads.
window.addEventListener('error', (e) => {
  const img = e && e.target;
  if (img && img.tagName === 'IMG' && img.getAttribute && img.getAttribute('data-fallbacks')) {
    window.__nextLogoFallback(img);
  }
}, true);

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
    <button class="tab-btn" data-view="recent">${escapeHtml(t('nav.recent'))}</button>
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
  <div id="view-recent" class="view hidden"></div>
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
  $('view-recent').classList.toggle('hidden', view !== 'recent');
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
    // Re-evaluate the Favorites entry every time the music view is shown so a
    // stored favorites list always brings the button back (#Dieter: the button
    // was set once at init, before the WebView had restored localStorage, then
    // never re-checked, so it stayed hidden after a restart even though the
    // favorites were still saved).
    updateFavModeBtn();
  }
  if (view === 'settings') loadBoxSettings();
  if (view === 'library') openLibrary();
  if (view === 'recent') renderRecent();
  if (view === 'multiroom') renderMultiroom(true);
  if (view === 'spotify') renderSpotifyAlpha();
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
    // The banner itself only shows for a genuinely newer version (CheckAppUpdate
    // returns nothing otherwise), and the download button only when the manifest
    // carries a real download URL. The button is a primary button so it stands
    // out in the notice instead of reading as a faint secondary control.
    // In-app update (#71): download the matching asset, verify its SHA256, then
    // install. Linux/Windows self-replace and relaunch; macOS downloads+verifies
    // and opens the .dmg (Gatekeeper blocks an unsigned auto-replace). The button
    // always shows now: the asset URL + hash are resolved from the release
    // manifest in the backend, so it no longer depends on the manifest carrying a
    // downloadUrl. notesUrl / releases page stays as the manual fallback.
    const isMacOS = /Mac|iPhone|iPad|iPod/i.test(navigator.platform || navigator.userAgent || '');
    const installLabel = isMacOS ? t('banner.downloadUpdate') : t('banner.installNow');
    banner.innerHTML = `
      <div><b>${escapeHtml(t('banner.appUpdateAvail'))}</b> ${escapeHtml(m.version)} &middot; <a href="#" id="appUpdateNotes" class="footer-link">${escapeHtml(t('banner.whatsNew'))}</a></div>
      <button class="btn btn-primary app-update-btn" id="appUpdateBtn">${escapeHtml(installLabel)}</button>
    `;
    banner.classList.remove('hidden');
    const notesLink = $('appUpdateNotes');
    if (notesLink) notesLink.onclick = (e) => { e.preventDefault(); BrowserOpenURL(notesUrl); };
    const dl = $('appUpdateBtn');
    if (dl) dl.onclick = () => runAppUpdate(m.version, dl, installLabel, isMacOS, dlUrl || notesUrl);
  } catch (e) {
    try { console.warn('checkAppUpdate failed', e); } catch {}
  }
}

// runAppUpdate downloads + verifies the new version and installs it (#71). On
// Linux/Windows the backend replaces the running binary and relaunches, so the
// app quits mid-call and the code after ApplyUpdate only runs on macOS (assisted:
// the verified .dmg is opened for the user to drag into Applications). On any
// failure the button becomes a "download from the website" fallback so the user
// is never stuck.
async function runAppUpdate(version, btn, installLabel, isMacOS, fallbackUrl) {
  btn.disabled = true;
  const off = EventsOn('app:update:progress', (pct) => {
    btn.textContent = t('banner.downloadingPct', { pct });
  });
  try {
    btn.textContent = t('banner.downloadingPct', { pct: 0 });
    const path = await DownloadUpdate(version);
    btn.textContent = t('banner.installing');
    await ApplyUpdate(path);
    // Reached only on macOS (Linux/Windows relaunch+quit inside ApplyUpdate).
    if (isMacOS) {
      btn.disabled = false;
      btn.textContent = installLabel;
      showToast(t('banner.macDownloaded'));
    }
  } catch (e) {
    showError(t('banner.updateFailed', { err: String(e) }));
    btn.disabled = false;
    btn.textContent = t('banner.openWebsite');
    if (fallbackUrl) btn.onclick = () => BrowserOpenURL(fallbackUrl);
  } finally {
    if (typeof off === 'function') off();
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
    // The banner is a "remove the stick now that setup is done, otherwise SSH
    // stays open" reminder. As of the pre-1.0 hardening run.sh no longer
    // force-opens sshd on every boot; SSH is open only because a stick is in (the
    // stick opens sshd via its remote_services marker), and a stickless reboot
    // closes it. So sshOpen is now an accurate, self-clearing signal again, and
    // keying on it (not data.mounted) also covers the Portable, where the stick
    // is in but never auto-mounts so mounted=false (Jens, 2026-06-17). The old
    // mounted-based gate was a workaround from when sshd was always up (#11).
    // (Setup view and the OTA window are already excluded above.)
    const show = !!(data && data.sshOpen);
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
  try {
    const cc = state.searchCountry || '';
    // App-side: no box needed; query radio-browser directly.
    state.languages = await RadioLanguages(cc, cc ? 60 : 40) || [];
    renderLanguageOptions();
  } catch {}
}
$('searchLang').onchange    = () => {
  state.searchLang = $('searchLang').value;
  updateFilterIndicators();
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
    // Known-first refresh: re-probe the speakers we already have directly
    // (no mDNS), so their live values update within ~1 s, THEN run the full
    // discovery to catch new or moved speakers. Most refreshes just want the
    // current values of a known box, so this makes the button feel instant.
    if (hadBoxes) {
      try {
        const quick = await RefreshKnownBoxes();
        if (quick && quick.length) applyBoxList(quick);
      } catch {}
    }
    const list = await DiscoverBoxes(4);
    applyBoxList(list || []);
    // Auto retry: if a recently set up speaker has not yet re-announced its
    // new name via mDNS, search again every 4 s (driven by pendingNames).
    scheduleNextAutoRefresh();
  } catch (e) {
    if (!hadBoxes) $('boxSelect').textContent = t('common.error') + ': ' + e;
  } finally {
    const rb = $('refreshBtn');
    if (rb) rb.classList.remove('spinning');
  }
}

// applyBoxList folds a freshly probed box list into state + the UI. Shared by
// the known-first quick refresh and the full discovery so both render
// identically (current-box re-bind, speaker select, badges, setup picker).
function applyBoxList(list) {
  state.boxes = applyPendingNames(list || []);
  // Stable display order. mDNS returns boxes in a nondeterministic order that
  // varies between discovery cycles, so the speaker list visibly reshuffled
  // whenever discovery re-ran, most noticeably mid-OTA when the updating box
  // drops off and reappears (#105). Sort by name (then host, then deviceID) so
  // the order stays put across refreshes.
  state.boxes.sort((a, b) =>
    (a.friendlyName || a.name || a.host || '').toLowerCase()
      .localeCompare((b.friendlyName || b.name || b.host || '').toLowerCase())
    || (a.host || '').localeCompare(b.host || '')
    || (a.deviceID || '').localeCompare(b.deviceID || ''));
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
        // Same speaker (matched by deviceID), just a changed field: IP/port after
        // a reconnect, version after an OTA, or a rename. Its presets are
        // identical, so do NOT blank state.presets / the grid here, which flashed
        // the grid empty on a routine re-discovery. loadPresets refreshes them in
        // place (and now keeps them on a transient empty read).
        state.searchResults = [];
        state.nowLocation = '';
        state.nowPlayState = '';
        state.presetErrors = {};
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
  // Refresh the live multiroom zones so the music-tab group frames are current.
  // Debounced (not a tight loop) and best-effort; repaints the selector on result.
  refreshMusicZones();
  updateSettingsTabBadge();
  // Re-evaluate the Favorites entry on every box-list refresh. This is the first
  // point after boot where localStorage is reliably restored in the WebView, so
  // a favorites list saved in a previous session reliably brings the button back
  // even if the one-shot init call ran before storage was ready (#Dieter).
  updateFavModeBtn();
  // Setup-tab target picker reuses the same state.boxes feed.
  renderSetupTargetPicker();
  // Second world-map invite: once the user's whole supported SoundTouch set is
  // running STR (no stock box left to convert), celebrate the milestone again.
  maybeInviteWorldMapAllDone();
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

// refreshMusicZones fetches every STR speaker's live multiroom zone so the
// music-tab group frames are accurate, then repaints the selector. Debounced to
// at most once per 8s (NOT a tight loop) and shares state.zoneLive with the
// Multi-Room tab. Best-effort: on error the frames simply stay as they were.
let _musicZoneFetchAt = 0;
async function refreshMusicZones() {
  const strBoxes = (state.boxes || []).filter(b => b && b.kind !== 'stock' && b.deviceID && b.host);
  if (strBoxes.length < 2) { return; } // a zone needs at least two speakers
  const now = Date.now();
  if (state.zoneLiveBusy || now - _musicZoneFetchAt < 8000) return;
  _musicZoneFetchAt = now;
  state.zoneLiveBusy = true;
  try {
    const results = await Promise.allSettled(strBoxes.map(b => GetZoneState(b.host, b.port)));
    const map = {};
    results.forEach((r, i) => { map[strBoxes[i].deviceID] = (r.status === 'fulfilled' && r.value) ? r.value : null; });
    state.zoneLive = map;
  } catch { /* keep previous frames */ } finally {
    state.zoneLiveBusy = false;
  }
  renderBoxSelect();
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
  // Cluster speakers that share a live multiroom zone into a colored frame
  // (display only; grouping is done in the Multi-Room tab). Defensive: with no
  // live group of >=2 discovered speakers the selector renders exactly as before.
  const zlMap = state.zoneLive || {};
  const masterOf = (b) => {
    if (b.kind === 'stock') return '';
    const zl = zlMap[b.deviceID];
    return (zl && zl.master) ? zl.master.toUpperCase() : '';
  };
  const memberCount = {};
  state.boxes.forEach(b => { const m = masterOf(b); if (m) memberCount[m] = (memberCount[m] || 0) + 1; });
  // A box is a framed master only when its group actually renders a frame, i.e.
  // >=2 of its members are discovered here. This keeps the master star and the
  // frame in lock-step (never a lone star on an unframed pill).
  const isFramedMaster = (b) => {
    const m = masterOf(b);
    return !!m && m === (b.deviceID || '').toUpperCase() && memberCount[m] >= 2;
  };
  const pill = (b) => {
    const isStock = b.kind === 'stock';
    const groupMark = isFramedMaster(b)
      ? `<span class="box-group-master" title="${escapeAttr(t('multiroom.groupMasterTitle'))}">&#9733;</span>`
      : '';
    const active = state.currentBox && state.currentBox.host === b.host && !isStock ? ' active' : '';
    const stockCls = isStock ? ' stock' : '';
    const label = getBoxLabel(b);
    // Model (e.g. "SoundTouch 10") right next to the name so users
    // with several speakers can tell ST10 from ST20 at a glance.
    // Fall back gracefully when an older agent only advertises the
    // generic "SoundTouch".
    const model = b.model && b.model !== 'SoundTouch'
      ? `<span class="box-model" title="${escapeAttr(t('speaker.modelTitle'))}">${escapeHtml(b.model)}</span>`
      : '';
    if (isStock) {
      // A SoundTouch-speaking device that STR cannot run on (Lifestyle / CineMate
      // system, SoundTouch 300 soundbar, Wireless Link Adapter) still appears in
      // discovery. Flag it as not supported instead of inviting an install that
      // dead-ends in ssh255 (#unsupported-devices).
      const unsupported = boxModelSupport(b.model) === 'unsupported';
      const badge = unsupported
        ? `<span class="box-stock-badge box-unsupported-badge">${escapeHtml(t('speaker.unsupportedBadge'))}</span>`
        : `<span class="box-stock-badge">${escapeHtml(t('speaker.needsInstallBadge'))}</span>`;
      const tip = unsupported ? t('speaker.unsupportedBadgeTitle') : t('speaker.stockTooltip');
      return `<span class="box-btn${stockCls}${unsupported ? ' unsupported' : ''}" data-host="${b.host}" data-port="${b.port}" data-stock="1" role="button" tabindex="0" title="${escapeAttr(tip)}">${escapeHtml(label)}${model} <small>${b.host}</small>${badge}</span>`;
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
    return `<span class="box-btn${active}${updCls}" data-host="${b.host}" data-port="${b.port}" role="button" tabindex="0">${groupMark}${escapeHtml(label)}${model} <small>${b.host}</small>${ver}${updDot}<span class="box-edit" data-host="${b.host}" data-port="${b.port}" title="${escapeAttr(t('speaker.editTitle'))}">&#9881;</span></span>`;
  };
  const groups = Object.keys(memberCount).filter(m => memberCount[m] >= 2).sort();
  if (groups.length === 0) {
    sel.innerHTML = state.boxes.map(pill).join('');
  } else {
    const colorOf = {};
    groups.forEach((m, i) => { colorOf[m] = (i % 4) + 1; });
    let html = '';
    for (const m of groups) {
      const members = state.boxes.filter(b => masterOf(b) === m);
      // master first inside the frame
      members.sort((a, b) => (((b.deviceID || '').toUpperCase() === m ? 1 : 0) - ((a.deviceID || '').toUpperCase() === m ? 1 : 0)));
      html += `<div class="box-group box-group-c${colorOf[m]}">${members.map(pill).join('')}</div>`;
    }
    html += state.boxes.filter(b => { const mm = masterOf(b); return !(mm && memberCount[mm] >= 2); }).map(pill).join('');
    sel.innerHTML = html;
  }
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
        // Not a standalone SoundTouch speaker (Lifestyle / CineMate system,
        // SoundTouch 300 soundbar, Wireless Link Adapter): STR cannot run on it
        // and the install would dead-end in ssh255. Tell the user plainly instead
        // of sending them into the stick setup (#unsupported-devices).
        if (boxModelSupport(box.model) === 'unsupported') {
          await confirmWarn(
            t('speaker.unsupportedTitle'),
            t('speaker.unsupportedBody', { model: escapeHtml(box.model || 'SoundTouch') }),
            { icon: null, confirmLabel: t('common.close'), confirmClass: 'btn btn-primary' },
          );
          return;
        }
        // Stock speaker: not an error, this is the happy path. The user
        // found a Bose speaker they can revive with STR. Invite them to
        // the USB stick setup with a positive CTA (no warning triangle,
        // no red "proceed anyway") instead of a danger prompt.
        const label = getBoxLabel(box);
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
      // Deliberately do NOT seed the radio-search country filter from the
      // stick region. STR is a worldwide app; the radio search defaults to
      // all countries so a German-provisioned box does not silently hide
      // every non-German station. The country filter stays at the user's
      // own choice (persisted, issue #86) or "all countries" until they
      // pick one. The region still drives only the language default below.
      //
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
  // App-side: query radio-browser directly, no box needed.
  if (state.tags.length === 0) {
    try {
      state.tags = await RadioTags(24) || [];
      renderGenreChips();
    } catch {}
  }
  if (state.languages.length === 0) {
    try {
      state.languages = await RadioLanguages('', 40) || [];
      renderLanguageOptions();
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
  // Light the badge only when THIS app can actually upgrade the box, i.e. the
  // app (and its embedded agent) is NEWER than the box. When the box is newer
  // than the app, an OTA would push the app's older embedded agent and
  // DOWNGRADE the box, so that is an "update the app" situation, not a
  // speaker-update one (the #105 update-banner confusion). compareVerBuild
  // treats a missing box build (older agent that does not broadcast build=) as
  // older, so that case still flags.
  return compareVerBuild(appVer, appBuild, b.version, b.build) > 0;
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
  // The speaker-update banner moved out of the music view into Speaker
  // Settings (rendered prominently at the top by loadBoxSettings for the
  // settings-selected box). When that element is not present (music view),
  // this is a no-op so the old music-view callers never throw.
  if (!banner) return;
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
  const boxName = getBoxLabel(state.currentBox);
  // OTA running on THIS box: show an honest "updating" banner instead of the
  // stale "update available" one (Jens, 2026-06-17: the heading kept saying
  // "available" while the speaker was already restarting). The button stays
  // disabled; doBoxUpdate's 1 s ticker keeps the countdown text current. This
  // early return also stops a mid-OTA discovery refresh from re-rendering an
  // enabled "Update" button over the progress text.
  if (state.otaInProgress && state.otaTargetHost === state.currentBox.host) {
    banner.innerHTML = `
      <div class="update-msg">
        <b>${escapeHtml(t('update.inProgressTitle', { name: boxName }))}</b><br>
        <small class="muted">${escapeHtml(t('update.rebootNote'))}</small>
      </div>
      <button class="btn btn-primary btn-mini" id="boxUpdateBtn" disabled>${escapeHtml(t('update.uploading'))}</button>
    `;
    banner.classList.remove('hidden');
    return;
  }
  try {
    const v = await BoxAgentVersion(state.currentBox.host, state.currentBox.port);
    const boxVer = v.version || t('common.unknown');
    const boxBuild = v.build || '';
    const appVer = state.appInfo.version;
    const appBuild = state.appInfo.build || '';
    // Direction matters. The speaker update pushes THIS app's embedded agent, so
    // it only makes sense when the app is newer than the box. The old code fired
    // on any difference and so offered "Aktualisieren" even when the box was
    // newer than the app, which would have downgraded the box and confused the
    // user (#105: an old app v0.6.22 next to a box on v0.7.32).
    const cmp = compareVerBuild(appVer, appBuild, boxVer, boxBuild);
    if (cmp === 0) return;
    // When only the build stamp differs (same version string), show the build on
    // both sides so the line is not the confusing "v0.8.1 -> v0.8.1" (Jens,
    // 2026-06-17). A real release bumps the version, so production never hits the
    // same-version case; this is mainly dev builds.
    const sameVer = boxVer === appVer;
    const instDisp = sameVer && boxBuild ? `${boxVer} (Build ${boxBuild})` : boxVer;
    const nextDisp = sameVer && appBuild ? `${appVer} (Build ${appBuild})` : appVer;
    if (cmp > 0) {
      banner.innerHTML = `
        <div class="update-msg">
          <b>${escapeHtml(t('update.speakerUpdateAvailFor', { name: boxName }))}</b><br>
          <small>${escapeHtml(t('update.versionLine', { installed: instDisp, next: nextDisp }))}</small><br>
          <small class="muted">${escapeHtml(t('update.rebootNote'))}</small>
        </div>
        ${renderUpdateBtn()}
      `;
      banner.classList.remove('hidden');
      if (!otaElsewhere) $('boxUpdateBtn').onclick = doBoxUpdate;
    } else {
      // Box newer than the app: an OTA would downgrade it. Point the user at the
      // app update instead and do NOT show the "Aktualisieren" button.
      banner.innerHTML = `
        <div class="update-msg">
          <b>${escapeHtml(t('update.appBehindTitle', { name: boxName }))}</b><br>
          <small>${escapeHtml(t('update.appBehindLine', { boxVersion: boxVer, appVersion: appVer }))}</small>
        </div>
      `;
      banner.classList.remove('hidden');
    }
  } catch {
    // Live version fetch failed; fall back to the cached mDNS version, same
    // direction guard so a newer box never gets a downgrade offer.
    const cv = state.currentBox.version;
    if (cv && compareVerBuild(state.appInfo.version, '', cv, '') > 0) {
      banner.innerHTML = `
        <div class="update-msg">
          <b>${escapeHtml(t('update.speakerUpdateAvailFor', { name: boxName }))}</b><br>
          <small>${escapeHtml(t('update.versionLine', { installed: cv, next: state.appInfo.version }))}</small><br>
          <small class="muted">${escapeHtml(t('update.rebootNote'))}</small>
        </div>
        ${renderUpdateBtn()}
      `;
      banner.classList.remove('hidden');
      if (!otaElsewhere) $('boxUpdateBtn').onclick = doBoxUpdate;
    }
  }
}

async function doBoxUpdate(targetBox) {
  // The box to update is passed explicitly by the caller (Speaker Settings
  // passes state.settingsBox). Fall back to the music-tab box only when a
  // caller omits it. Earlier this always used state.currentBox, so updating a
  // speaker picked in Speaker Settings actually OTA'd whatever box the music tab
  // was on, re-flashing the wrong (already-updated) speaker every time (#105).
  targetBox = targetBox || state.currentBox;
  if (!targetBox) return;
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
    const r = await boxFetch(targetBox, '/api/stick/status');
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
    const s = await BoxSettings(targetBox.host, targetBox.port);
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
  state.otaTargetHost = targetBox.host;
  state.otaTargetName = getBoxLabel(targetBox);
  // Suppress the SSH "remove stick and reboot" banner for the whole
  // OTA window. The agent restarts mid-OTA and SSH is briefly open
  // during that restart; the banner's "Reboot now" button would
  // interrupt the agent exec and may leave the box half-flashed.
  state.otaInProgress = true;
  setStatus(t('update.uploading'));
  checkSshBanner();
  // Swap the banner heading to "updating" right away (the otaHere branch in
  // checkBoxUpdate), so it no longer reads "update available" while the OTA runs.
  checkBoxUpdate();
  const appBuild  = state.appInfo && state.appInfo.build;
  // Record what the box runs RIGHT NOW, before the push. The post-OTA success
  // signal is "the box is reachable AND no longer reports this pre-OTA build".
  // That is far more robust than the old exact-match-on-appBuild test: when the
  // app and the embedded agent carry slightly different build stamps the exact
  // match never fired, so the box came back fully updated but the poll ran to
  // its 6-minute ceiling and the button stayed greyed the whole time (Jens,
  // live 2026-06-17: "the box is long since rebooted and even shows the clock
  // again, but the app still says 'still answering old build, 3:35 left'").
  let preBuild = '', preVersion = '';
  try {
    const pv = await BoxAgentVersion(targetBox.host, targetBox.port);
    if (pv) { preBuild = pv.build || ''; preVersion = pv.version || ''; }
  } catch { /* box version unknown pre-OTA: fall back to the appBuild match */ }
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
    // Active poll: hit /api/agent/version until the box answers as updated. The
    // success signal is "box reachable AND its reported build/version is no
    // longer the pre-OTA one" (or, when we have it, an exact match to the app's
    // own build). The loop breaks the instant that happens, so the user never
    // waits past the real reconnect: the deadline below is only a give-up
    // ceiling, not a fixed wait. Poll every 2 s so the button frees within a
    // couple of seconds of the new agent answering, not up to 5 s later. The
    // ceiling is 6 minutes: a BCO box (Portable, ST20-spotty) can reboot TWICE
    // post-OTA (the OTA reboot, then a bootstrap-sync reboot when the new
    // binary's embedded run.sh differs from NAND — project_ota_only_replaces_binary),
    // each boot taking ~40 s to the agent plus ~85 s until the :17008 REDIRECT
    // makes it reachable, plus the box's slow BoseApp. As long as the box is
    // unreachable or still reports the pre-OTA build, the buttons stay locked.
    const deadlineMs = Date.now() + 360_000;
    const pollIntervalMs = 2_000;
    const renderStatus = () => {
      const remaining = formatRemaining(deadlineMs - Date.now());
      setStatus(t('update.waitingForSpeaker', { remaining }));
    };
    renderStatus();
    const tickHandle = setInterval(renderStatus, 1000);
    let confirmed = false;
    let confirmedVer = null;
    // updated() decides whether a version reading means the OTA landed. Prefer
    // an exact match to the app's build; otherwise any change away from the
    // pre-OTA build/version. The pre-OTA values must be non-empty for the
    // "changed" branch, else an unknown pre-OTA value would falsely confirm on
    // the OLD agent that is still answering during the brief pre-reboot window.
    const updated = (v) => {
      if (!v) return false;
      if (appBuild && v.build === appBuild) return true;
      if (preBuild && v.build && v.build !== preBuild) return true;
      if (preVersion && v.version && v.version !== preVersion) return true;
      return false;
    };
    try {
      while (Date.now() < deadlineMs) {
        await sleep(pollIntervalMs);
        try {
          const v = await BoxAgentVersion(targetBox.host, targetBox.port);
          if (updated(v)) {
            confirmed = true;
            confirmedVer = v;
            break;
          }
        } catch { /* box still unreachable mid-reboot; keep waiting */ }
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
    // glitch until the next clean discovery cycle (Jens 2026-06-01:
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

let loadedPresetsBoxKey = null;
async function loadPresets(retry = 0) {
  if (!state.currentBox) return;
  // Drop another box's box-native presets the moment we switch, so a Deezer tile
  // from the previous speaker never flashes on this one's empty slot. Kept across
  // same-box refreshes so the tiles don't flicker on every reload.
  const boxKey = state.currentBox.host + ':' + state.currentBox.port;
  if (boxKey !== loadedPresetsBoxKey) { state.boxPresets = []; state.boxSnapshot = null; loadedPresetsBoxKey = boxKey; }
  if (state.presets.length === 0) {
    $('presets').innerHTML = `<div class="muted small grid-loading">${escapeHtml(t('preset.loading'))}</div>`;
  }
  try {
    const fresh = await GetPresets(state.currentBox.host, state.currentBox.port) || [];
    // Guard against a transient empty result. The box can briefly return zero
    // presets while it is busy (switching source for a play) or its store is
    // reloading, even though presets.json is intact. Overwriting with [] made all
    // presets "vanish" from the grid after playing a radio, then reappear on the
    // next save (a display-only loss; the box never lost them). If we suddenly
    // read empty but currently have presets, retry once before trusting it and
    // keep the current presets meanwhile, so the grid never flashes empty. A
    // genuinely empty box (or an empty result that persists past the retry) is
    // still taken at face value.
    if (fresh.length === 0 && state.presets.length > 0 && retry < 1) {
      setTimeout(() => loadPresets(retry + 1), 1500);
      return;
    }
    state.presets = fresh;
    renderPresets();
    healPresetLogos();
    loadBoxPresets();
    loadBoxSnapshot();
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

// loadBoxPresets reads the box's OWN presets (including foreign sources like
// Deezer that STR did not set) so a slot STR does not manage can still be shown
// and recalled. Best-effort: on error or empty, the grid just shows no
// box-native tiles. The box reports these over gabbo; the agent serves the
// cached list, so this is one cheap app-side read, no box poll.
async function loadBoxPresets() {
  if (!state.currentBox) { state.boxPresets = []; return; }
  try {
    state.boxPresets = await BoxPresets(state.currentBox.host, state.currentBox.port) || [];
  } catch { state.boxPresets = []; return; }
  renderPresets();
  renderPresetLossNotice();
}

let loadedSnapshotBoxKey = null;

// loadBoxSnapshot reads the agent's pre-takeover snapshot of the box (presets +
// sources captured before STR took over the box's cloud endpoints). It is the
// only record of account-linked cloud sources (e.g. Deezer) that STR cannot
// carry over yet: once STR is on the box, the box's next account sync drops
// those sources and the presets bound to them. One cheap app-side read per box
// (the agent serves a cached NAND file); on error the notice simply never shows.
async function loadBoxSnapshot() {
  if (!state.currentBox) { state.boxSnapshot = null; renderPresetLossNotice(); return; }
  const boxKey = state.currentBox.host + ':' + state.currentBox.port;
  if (boxKey === loadedSnapshotBoxKey && state.boxSnapshot) { renderPresetLossNotice(); return; }
  loadedSnapshotBoxKey = boxKey;
  let snap = null;
  try { snap = await BoxSnapshot(state.currentBox.host, state.currentBox.port); } catch { snap = null; }
  if (snap && snap.captured === false) snap = null;
  if (snap) {
    const dev = snap.deviceID || boxKey;
    try { snap._dismissed = await GetAppFlag('box-loss-notice:' + dev); } catch { snap._dismissed = false; }
  }
  state.boxSnapshot = snap;
  renderPresetLossNotice();
}

// lostPresetsNow returns the snapshot's account-linked presets that are no
// longer present on the box (the slot is empty in both STR's presets and the
// box's own presets), i.e. the ones STR's takeover dropped. A Deezer preset that
// still shows as a box-native tile is intentionally NOT reported.
function lostPresetsNow() {
  const snap = state.boxSnapshot;
  if (!snap || !Array.isArray(snap.lostPresets)) return [];
  return snap.lostPresets.filter((lp) => {
    const live = state.presets.find((x) => x.slot === lp.slot) ||
      (state.boxPresets || []).find((x) => x.slot === lp.slot);
    return !live;
  });
}

// renderPresetLossNotice shows a dismissible banner above the preset grid when
// the box dropped account-linked presets STR cannot carry over (e.g. Deezer),
// listing the affected slots so the user knows what was there. Idempotent:
// re-creates or removes a single #preset-loss-notice element each call.
function renderPresetLossNotice() {
  const grid = $('presets');
  if (!grid || !grid.parentNode) return;
  let el = document.getElementById('preset-loss-notice');
  const snap = state.boxSnapshot;
  const lost = lostPresetsNow();
  const services = (snap && Array.isArray(snap.lostServices)) ? snap.lostServices : [];
  if (!snap || snap._dismissed || lost.length === 0 || services.length === 0) {
    if (el) el.remove();
    return;
  }
  if (!el) {
    el = document.createElement('div');
    el.id = 'preset-loss-notice';
    el.className = 'loss-notice';
    grid.parentNode.insertBefore(el, grid);
  }
  const svc = services.map((s) => boxSourceLabel(s)).join(', ');
  const slots = lost.map((lp) => `${lp.slot} (${lp.name || boxSourceLabel(lp.source)})`).join(', ');
  el.innerHTML =
    `<div class="loss-notice-body">` +
      `<strong>${escapeHtml(t('preset.lossTitle', { service: svc }))}</strong>` +
      `<div class="small">${escapeHtml(t('preset.lossBody', { service: svc, slots }))}</div>` +
    `</div>` +
    `<button type="button" class="loss-notice-dismiss" aria-label="${escapeAttr(t('preset.lossDismiss'))}">&times;</button>`;
  const btn = el.querySelector('.loss-notice-dismiss');
  if (btn) {
    btn.addEventListener('click', async () => {
      if (state.boxSnapshot) state.boxSnapshot._dismissed = true;
      const dev = (snap && snap.deviceID) ||
        (state.currentBox && (state.currentBox.host + ':' + state.currentBox.port));
      try { await SetAppFlag('box-loss-notice:' + dev); } catch { /* best-effort */ }
      el.remove();
    });
  }
}

// boxSourceLabel turns the box's raw source enum (DEEZER, LOCAL_INTERNET_RADIO,
// ...) into a friendly name for the tile badge.
function boxSourceLabel(source) {
  const s = String(source || '').toUpperCase();
  const map = {
    DEEZER: 'Deezer', SPOTIFY: 'Spotify', AMAZON: 'Amazon Music',
    TUNEIN: 'TuneIn', LOCAL_INTERNET_RADIO: 'Internet radio',
    INTERNET_RADIO: 'Internet radio', LOCAL_MUSIC: 'Library', STORED_MUSIC: 'Library',
    BLUETOOTH: 'Bluetooth', AIRPLAY: 'AirPlay',
  };
  if (map[s]) return map[s];
  // Unknown: title-case the first token (DEEZER_HIFI -> Deezer).
  const first = s.split('_')[0] || s;
  return first ? first.charAt(0) + first.slice(1).toLowerCase() : '';
}

// recallBoxPreset plays one of the box's own presets by pressing its hardware
// preset key (the box plays it through its own cached account, e.g. Deezer).
async function recallBoxPreset(slot) {
  if (!state.currentBox) return;
  try {
    await RecallBoxPreset(state.currentBox.host, state.currentBox.port, slot);
  } catch (err) {
    showError(t('preset.boxRecallFailed', { err: String((err && err.message) || err) }));
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
        const list = await RadioSearch({ q: p.name, limit: 12, order: 'votes', top: false }) || [];
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
        SetPreset(state.currentBox.host, state.currentBox.port, p.slot, p.name, p.stream_url, logo, p.bitrate || 0, p.homepage || '').catch(() => {});
      } catch {}
    }));
  } finally {
    healingInProgress = false;
    renderPresets();
  }
}

// ---------- Preset Render mit Long Press Support ----------

// BOX_LOOPBACK is the agent's own host:port as seen from the box (the agent runs
// on the box). It is the single source for the loopback URLs the frontend builds
// to optimistically reflect what the box is about to play; the Go side mirrors
// these in internal/boxurl. Keep the two in sync.
const BOX_LOOPBACK = 'http://127.0.0.1:8888';
const boxSpotifyDefaultUrl = () => `${BOX_LOOPBACK}/spotify/stream.ogg`;

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

// decodeProxyUrl unwraps a stream-proxy URL
// (http://<host>:8888/stream/raw?u=<base64url real URL>) back to the real
// upstream URL it wraps; returns the input unchanged otherwise. A preset MUST
// store the real station URL, never the proxy wrapper: since v0.7.16 ad-hoc radio
// plays through the proxy, so the box's now-playing location is the wrapper.
// Saving that made the box, on recall, ask the proxy to fetch its own loopback
// URL, which the agent's SSRF guard blocks, so nothing played (the ST20 "plays
// nothing" regression).
function decodeProxyUrl(loc) {
  if (!loc) return loc;
  try {
    const u = new URL(loc);
    if (u.pathname !== '/stream/raw') return loc;
    const enc = u.searchParams.get('u');
    if (!enc) return loc;
    const real = atob(enc.replace(/-/g, '+').replace(/_/g, '/'));
    if (/^https?:\/\//i.test(real)) return real;
  } catch { /* not a parseable proxy URL: fall through */ }
  return loc;
}

// spotifyURIFromContainer recovers the spotify: context URI from a box
// now-playing location of the form "/playback/container/<base64 spotify:...>"
// (STR writes this when it plays a Spotify selection; the agent encodes it
// URL-safe, see internal/webui legacySpotifyURI). Used as a save-time fallback
// when go-librespot's /spotify/info reports no context even though a real
// playlist is playing (#45). Returns "" when the location is not a container or
// does not decode to a spotify: URI.
function spotifyURIFromContainer(loc) {
  const marker = '/playback/container/';
  const i = (loc || '').indexOf(marker);
  if (i < 0) return '';
  let enc = loc.slice(i + marker.length);
  const j = enc.search(/[/?#]/);
  if (j >= 0) enc = enc.slice(0, j);
  try {
    const uri = atob(enc.replace(/-/g, '+').replace(/_/g, '/'));
    return uri.startsWith('spotify:') ? uri : '';
  } catch { return ''; }
}

// SPOTIFY_LOGO moved to logos.js (shared with the Recently-played view).

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
  // Remember the active Spotify slot from the per-slot /spotify/stream-<slot>.ogg
  // URL. A hardware next/prev advances go-librespot but can drop the slot from
  // the box's now-playing location, leaving only the generic "Spotify" name. We
  // keep the last known slot so the right tile stays lit. We must NOT fall back
  // to matching the preset NAME: a preset literally named "Spotify" (the generic
  // source name) would otherwise falsely light up, e.g. preset 1 lit up instead
  // of the playing preset 6 after pressing next on the remote.
  const spotifyPlaying = !!state.nowLocation && /\/spotify\/stream/.test(state.nowLocation);
  if (!spotifyPlaying) state.nowSpotifySlot = null;
  else if (activeSlot !== null) state.nowSpotifySlot = activeSlot;
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
    // A slot STR does not manage may still hold one of the box's own presets
    // (e.g. a Deezer playlist set on the speaker). Show it so the user sees and
    // can recall it, instead of a misleading "empty" tile.
    const bp = !p ? (state.boxPresets || []).find(x => x.slot === i) : null;
    const isActive = p && state.nowLocation && (
      p.stream_url === state.nowLocation ||
      (activeSlot !== null && p.slot === activeSlot) ||
      (activeStreamURL && p.stream_url === activeStreamURL) ||
      // Spotify: light the slot we recalled (remembered from the per-slot URL),
      // which survives a next/prev that drops the slot from the now-playing
      // location. Never match on the preset name (see nowSpotifySlot note above).
      (p.type === 'spotify' && spotifyPlaying && state.nowSpotifySlot != null && p.slot === state.nowSpotifySlot)
    );
    const hasErr = !!state.presetErrors[i];
    const div = document.createElement('div');
    div.className = 'preset' + (p || bp ? '' : ' empty') + (isActive ? ' playing' : '') + (hasErr ? ' error' : '') + (bp ? ' box-native' : '');
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
        SetPreset(state.currentBox.host, state.currentBox.port, p.slot, p.name, p.stream_url, state.nowIcon, p.bitrate || 0, p.homepage || '').catch(() => {});
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
              data-fallbacks="${escapeAttr(presetCandidates.slice(1).join('|'))}"/>`;
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
          SetPreset(state.currentBox.host, state.currentBox.port, p.slot, p.name, p.stream_url, p.art || '', state.nowBitrate, p.homepage || '').catch(() => {});
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
    } else if (bp) {
      // The box's own preset (a source STR does not manage, e.g. Deezer). Tap to
      // recall it via the hardware key; the box plays it through its own account.
      const bpActive = !!state.nowLocation && !!bp.location && state.nowLocation === bp.location;
      if (bpActive) div.classList.add('playing');
      const srcLabel = boxSourceLabel(bp.source);
      const logo =
        `<img class="preset-logo" src="${escapeAttr(monogramDataUri(bp.name || srcLabel || '?'))}"/>`;
      div.innerHTML = `
        <div class="preset-head"><span class="num">${escapeHtml(t('preset.key', { n: i }))}</span></div>
        <div class="preset-body">
          ${logo}
          <div class="preset-text">
            <div class="name">${escapeHtml(bp.name || srcLabel || t('preset.onSpeaker'))}</div>
            ${srcLabel ? `<div class="preset-source" title="${escapeAttr(srcLabel)}">${escapeHtml(t('preset.sourceBadge', { source: srcLabel }))}</div>` : ''}
            <div class="preset-box-hint">${escapeHtml(t('preset.boxNativeHint'))}</div>
          </div>
        </div>
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
    if (bp) {
      // Box-native preset: click recalls it; no long-press save so the user's
      // own (e.g. Deezer) preset can't be clobbered by STR's current station.
      attachPresetHandlers(div, i, bp, { onPlay: () => recallBoxPreset(i), allowSave: false });
    } else {
      attachPresetHandlers(div, i, p);
    }
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
// 1100 ms: a clearly deliberate hold, so a normal tap never saves by accident.
// VISUAL_HOLD_DELAY = 180 ms: only after this hold time do we show the
// scale(0.96) visual. A short click avoids the mini jiggle that a transition
// scale-down + scale-up would otherwise produce on the logo.
//
// opts.onPlay overrides the short-click action (box-native presets recall the
// box's hardware key instead of STR's play). opts.allowSave=false disables the
// long-press save so a box-native preset can't be overwritten by a hold.
const LONG_PRESS_MS = 1100;
const VISUAL_HOLD_DELAY = 180;
function attachPresetHandlers(el, slot, preset, opts = {}) {
  const onPlay = opts.onPlay || (() => play(slot));
  const allowSave = opts.allowSave !== false;
  let timer = null;
  let visualTimer = null;
  let armed = false;
  let firedLong = false;
  let startedAt = 0;
  const bar = el.querySelector('.long-press-bar');
  const animateBar = () => {
    if (!armed) return;
    const elapsed = Date.now() - startedAt;
    // The bar only represents the deliberate-hold window: it fills 0 -> 100%
    // between VISUAL_HOLD_DELAY and LONG_PRESS_MS, so it never shows for the
    // first 180 ms (a normal click). Starting it at elapsed/LONG_PRESS_MS made
    // a quick click flash the "save station" bar before mouseup.
    const pct = Math.min(100, Math.max(0,
      ((elapsed - VISUAL_HOLD_DELAY) / (LONG_PRESS_MS - VISUAL_HOLD_DELAY)) * 100));
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
    visualTimer = setTimeout(() => {
      if (!armed) return;
      el.classList.add('long-press');
      // Only start filling the save-progress bar once the hold is deliberate, so
      // a normal short click never flashes it.
      requestAnimationFrame(animateBar);
    }, VISUAL_HOLD_DELAY);
    if (allowSave) {
      timer = setTimeout(async () => {
        if (!armed) return;
        firedLong = true;
        await saveCurrentToSlot(slot);
        armed = false;
        el.classList.remove('long-press');
        if (bar) bar.style.width = '0%';
      }, LONG_PRESS_MS);
    }
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
    if (preset) onPlay();
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
  if (/\/spotify\/stream|\/playback\/container/.test(state.nowLocation)) {
    // We can only save a real, recallable Spotify preset when we know the
    // playlist/album/track context URI. state.nowSpotifyContext is only
    // refreshed by a throttled (>3s) background poll, so at save time it can
    // lag or be momentarily empty even while a real playlist is playing. That
    // made a legitimate save fail with a false "no replayable playlist" (Pierre,
    // #45, was on Premium playing a playlist). Re-read the live context from the
    // speaker now instead of trusting the cache.
    let ctxUri = state.nowSpotifyContext;
    let acct = state.nowSpotifyAccount || '';
    try {
      const np = await SpotifyNowPlaying(state.currentBox.host, state.currentBox.port);
      if (np) {
        if (np.context) ctxUri = np.context;
        if (np.account) acct = np.account;
      }
    } catch {}
    // Fallback: go-librespot's /spotify/info can report an empty context even
    // while a real playlist is playing (it depends on how playback was started).
    // The box's own now-playing still carries the URI STR wrote into its
    // /playback/container/<base64 spotify:...> location, so decode that before
    // giving up (Pierre, #45: Premium, a playlist was playing, the box location
    // held spotify:playlist:..., but np.context came back empty so the save
    // wrongly failed).
    if (!ctxUri) ctxUri = spotifyURIFromContainer(state.nowLocation);
    if (!ctxUri) {
      // Spotify is playing but the speaker reported no playlist/album/track
      // context. This is NOT the same as a non-replayable station: a real
      // station carries a (non-replayable) context that the agent rejects on
      // save below. An empty context almost always means an out-of-date speaker
      // agent that cannot capture the context yet (the app updates separately
      // from the on-box agent) or a will_play event the agent missed. Guide the
      // user to update the speaker and replay the playlist rather than falsely
      // claiming there is no playlist.
      showError(t('preset.spotifyContextUnknown'));
      return;
    }
    const sname = state.nowName || 'Spotify';
    try {
      await SaveSpotifyPreset(
        state.currentBox.host, state.currentBox.port,
        slot, sname, ctxUri, acct
      );
      showToast(t('preset.savedToKey', { n: slot, name: sname }));
      await loadPresets();
      return;
    } catch (err) {
      // The agent validates the context and returns 422 spotify-uri-unplayable
      // for a genuinely non-replayable selection (a Spotify radio/station). Show
      // the precise "no replayable playlist" message for that; a generic failure
      // otherwise.
      const msg = String(err);
      if (/spotify-uri-unplayable|replayable playlist/i.test(msg)) {
        showError(t('preset.spotifyNotSaveable'));
      } else {
        showError(t('preset.saveFailed', { err: msg }));
      }
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
          slot, src.name, src.stream_url, src.art || '', src.bitrate || 0, src.homepage || ''
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
      slot, name, decodeProxyUrl(state.nowLocation), state.nowIcon || '', state.nowBitrate || 0, ''
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
      ? boxSpotifyDefaultUrl()
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
    // Also repaint the now-playing status line from the optimistic state, not
    // just the preset tile: it reads purely from cached state, so without this
    // the "Stream is starting" label only appeared after PlaySlot returned and
    // the next refreshStatus ran, which on a cold soft recall lagged several
    // seconds behind the click. Now the status line tracks the tile instantly.
    renderNowPlayingBar();
  }
  try {
    await PlaySlot(state.currentBox.host, state.currentBox.port, slot);
    delete state.presetErrors[slot];
    if (wasIdle) reapplyDesiredVolume();
    refreshStatus();
    setTimeout(refreshStatus, 1500);
  } catch (e) {
    const errStr = String(e);
    state.nowPlayState = '';
    state.nowLocation = '';
    state.optimisticUntil = 0;
    state.presetErrors[slot] = friendlyPlayError(errStr);
    renderPresets();
    // The tile label is too small for the multi-step Spotify Connect how-to, so
    // also show it as a (localized) toast. Use the i18n help text, not the raw
    // English backend message, so non-English users get it in their language.
    if (errStr.toLowerCase().includes('spotify-not-logged-in')) {
      showToast(t('play.errSpotifyLoginHelp'));
    }
    setTimeout(() => refreshStatus(), 2000);
  }
}

// friendlyPlayError turns a technical error string into a short
// user-facing hint shown on the preset label.
function friendlyPlayError(s) {
  const l = String(s).toLowerCase();
  if (l.includes('box_not_ready')) return t('play.errBoxStarting');
  // Spotify recall refused because the speaker was never picked as the Spotify
  // Connect device (no go-librespot credential). Key off the stable backend code,
  // not the English message, so rewording the backend never breaks this (#45).
  if (l.includes('spotify-not-logged-in')) return t('play.errSpotifyLogin');
  if (l.includes('premium')) return t('play.errSpotifyPremium');
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
            SetPreset(box.host, box.port, p.slot, p.name, p.stream_url, p.art || '', br, p.homepage || '').catch(() => {});
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
  // Match the source case-insensitively: the firmware is not consistent about
  // casing across models, and AirPlay in particular can read as AIRPLAY,
  // AirPlay2, etc. depending on the speaker (#122).
  const srcU = src.toUpperCase();
  const isAirplay = srcU.includes('AIRPLAY');
  let stateLabel, stateClass;
  if (ps === 'PLAY_STATE') { stateLabel = t('status.playing'); stateClass = 'play'; }
  else if (ps === 'BUFFERING_STATE') { stateLabel = t('status.buffering'); stateClass = 'buf'; }
  else if (ps === 'PAUSE_STATE') { stateLabel = t('status.paused'); stateClass = 'idle'; }
  else if (srcU === 'STANDBY') { stateLabel = t('status.standby'); stateClass = 'idle'; }
  else { stateLabel = ''; stateClass = 'idle'; }
  if (srcU === 'AUX') { displayName = t('status.auxInput'); if (!stateLabel) { stateLabel = t('status.active'); stateClass = 'play'; } }
  else if (srcU === 'BLUETOOTH') { displayName = t('status.bluetooth'); if (!stateLabel) { stateLabel = t('status.active'); stateClass = 'play'; } }
  else if (isAirplay) { displayName = t('status.airplay'); if (!stateLabel) { stateLabel = t('status.active'); stateClass = 'play'; } }
  else if (srcU && srcU !== 'STANDBY' && srcU !== 'INVALID_SOURCE' && ps !== 'STOP_STATE' && !stateLabel && !displayName) {
    // The box has an active source STR does not specifically label (some models
    // report AirPlay/Spotify Connect/other inputs under a different name and
    // without a playStatus). Reflect it as active rather than letting it fall
    // through to a misleading "ready" while audio is actually playing. An
    // explicit STOP_STATE is excluded so a stopped box still reads as idle.
    stateLabel = t('status.active');
    stateClass = 'play';
  }
  const isStreamSrc = (ps === 'PLAY_STATE' || ps === 'BUFFERING_STATE' || ps === 'PAUSE_STATE') && srcU !== 'AUX' && srcU !== 'BLUETOOTH' && !isAirplay;
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
    const isSpotifyNow = /\/spotify\/stream|\/playback\/container/.test(newLoc);
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
      // First successful radio playback is the strongest moment to invite the
      // user to the community world map (their box is alive again). Radio only
      // (proxy /stream/, not Spotify/AUX/Bluetooth), fired once ever inside
      // maybeInviteWorldMap.
      if (/\/stream\//.test(loc) && !/\/spotify\//.test(loc)) {
        maybeInviteWorldMap();
      }
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

// ---------- Community world map invite ----------
//
// After the first successful radio playback, invite the user once to drop a pin
// on the st-reborn.de community world map ("your box is alive again"), the moment
// success feels real. Non-blocking: a dismissible banner with a button that opens
// the localized map URL in the EXTERNAL browser (not a webview, so the site's
// coarse-location + anti-spam work in a normal session). Fires once ever
// (localStorage flag). The app sends NO data and NO location: the website handles
// the pin (the user taps a coarse region) and its own anti-spam token.
const WORLD_MAP_FLAG = 'str.worldMapInvited';        // after the first radio play
const WORLD_MAP_ALL_FLAG = 'str.worldMapInvitedAll'; // when the whole set runs STR
// Session re-entry guard: the status poll fires every few seconds, so the async
// flag check below must not let a second poll open a second invite before the
// first has persisted the flag. Set synchronously on the first call.
let worldMapInviteHandled = false;
let worldMapAllHandled = false;

// worldMapURL builds the localized community-map deep link. English is the site
// root; the other locales live under /<locale>/. ?share opens the "set pin" form
// and scrolls to the map; src=app lets the site optionally attribute app pins
// (harmless if unused). Unknown params are ignored by the site.
function worldMapURL() {
  let loc = 'en';
  try { loc = getLocale() || 'en'; } catch { /* default en */ }
  const prefix = loc === 'en' ? '' : '/' + loc;
  return 'https://st-reborn.de' + prefix + '/?share&src=app#community';
}

// inviteWorldMapOnce shows the world-map invite at most once ever for the given
// flag. The durable Go-side flag survives app updates and reinstalls (unlike
// webview localStorage), so a one-time invite never reappears; localStorage is a
// fast secondary check. variant 'all' uses the "whole setup rescued" wording.
async function inviteWorldMapOnce(flag, variant) {
  let already = false;
  try { already = await GetAppFlag(flag); } catch { /* fall back to localStorage */ }
  if (!already) {
    try { already = localStorage.getItem(flag) === '1'; } catch {}
  }
  if (already) return;
  // Persist to BOTH stores before showing, so a crash right after still suppresses it.
  try { localStorage.setItem(flag, '1'); } catch {}
  try { await SetAppFlag(flag); } catch {}
  showWorldMapInvite(variant);
}

async function maybeInviteWorldMap() {
  if (worldMapInviteHandled) return; // synchronous guard against status-poll re-entry
  worldMapInviteHandled = true;
  await inviteWorldMapOnce(WORLD_MAP_FLAG, 'first');
}

// maybeInviteWorldMapAllDone fires the SECOND invite once every supported
// SoundTouch the app has discovered is running STR (no stock box left to
// convert) and there are at least two of them, i.e. the user has rescued their
// whole multi-speaker setup. Once ever. Single-box users already got the
// first-radio-play invite, so the >=2 guard keeps this as the distinct
// whole-setup milestone instead of a duplicate.
async function maybeInviteWorldMapAllDone() {
  if (worldMapAllHandled) return;
  const boxes = state.boxes || [];
  const strBoxes = boxes.filter(b => b && b.kind !== 'stock');
  const stockBoxes = boxes.filter(b => b && b.kind === 'stock');
  if (strBoxes.length < 2 || stockBoxes.length > 0) return;
  worldMapAllHandled = true; // latch only once the milestone is actually reached
  await inviteWorldMapOnce(WORLD_MAP_ALL_FLAG, 'all');
}

// worldMapPreviewSVG returns a small, stylized world-map thumbnail for the invite
// so the user instantly sees this is "the community pin map" before clicking
// through to the website. It is a self-contained inline SVG: NO network tile
// request and NO real pin data (which keeps the invite's "the app sends nothing"
// guarantee intact). Continents are simplified silhouettes; the pins are
// decorative, with one pulsing to suggest "add yours here".
function worldMapPreviewSVG() {
  const pin = (x, y) => `<circle cx="${x}" cy="${y}" r="2.4"/><circle cx="${x}" cy="${y}" r="0.9" fill="#fff"/>`;
  return `<svg viewBox="0 0 220 110" class="wmi-map-svg" role="img" aria-hidden="true" preserveAspectRatio="xMidYMid slice">`
    + `<defs><clipPath id="wmiClip"><rect x="0" y="0" width="220" height="110" rx="8"/></clipPath></defs>`
    + `<g clip-path="url(#wmiClip)">`
    + `<rect x="0" y="0" width="220" height="110" fill="#11202b"/>`
    + `<g stroke="#1f3a49" stroke-width="0.6">`
    + `<line x1="0" y1="27.5" x2="220" y2="27.5"/><line x1="0" y1="55" x2="220" y2="55"/><line x1="0" y1="82.5" x2="220" y2="82.5"/>`
    + `<line x1="55" y1="0" x2="55" y2="110"/><line x1="110" y1="0" x2="110" y2="110"/><line x1="165" y1="0" x2="165" y2="110"/></g>`
    + `<g fill="#2f6b4f">`
    + `<path d="M28,22 70,18 78,38 58,52 42,46 30,34 Z"/>`     // North America
    + `<path d="M62,58 78,60 82,78 70,98 60,84 Z"/>`           // South America
    + `<circle cx="88" cy="13" r="6"/>`                        // Greenland
    + `<path d="M104,27 126,25 124,41 108,43 Z"/>`             // Europe
    + `<path d="M110,46 134,46 136,72 122,92 112,68 Z"/>`      // Africa
    + `<path d="M128,22 192,20 196,44 158,52 132,44 Z"/>`      // Asia
    + `<path d="M150,52 162,52 156,66 Z"/>`                    // India
    + `<path d="M172,82 198,80 200,96 178,98 Z"/></g>`         // Australia
    + `<g fill="var(--brand,#e0531f)">`
    + pin(52, 34) + pin(72, 72) + pin(170, 36) + pin(186, 88) + pin(196, 30)
    + `<circle cx="112" cy="34" r="3" opacity="0.5">`          // "your pin", pulsing
    + `<animate attributeName="r" values="3;10;3" dur="2.2s" repeatCount="indefinite"/>`
    + `<animate attributeName="opacity" values="0.55;0;0.55" dur="2.2s" repeatCount="indefinite"/></circle>`
    + pin(112, 34)
    + `</g></g></svg>`;
}

function showWorldMapInvite(variant) {
  if (document.getElementById('worldMapInvite')) return;
  const headline = variant === 'all' ? t('worldMap.inviteTextAll') : t('worldMap.inviteText');
  const el = document.createElement('div');
  el.id = 'worldMapInvite';
  el.className = 'worldmap-invite';
  el.innerHTML =
    `<button class="wmi-close" id="wmiClose" aria-label="close">&times;</button>` +
    `<button class="wmi-map" id="wmiMap" title="${escapeAttr(t('worldMap.inviteBtn'))}" aria-label="${escapeAttr(t('worldMap.inviteBtn'))}">` +
      worldMapPreviewSVG() +
      `<span class="wmi-map-badge" aria-hidden="true">🎉</span>` +
    `</button>` +
    `<div class="wmi-body">` +
      `<div class="wmi-text">${escapeHtml(headline)}</div>` +
      `<div class="wmi-count hidden" id="wmiCount"></div>` +
      `<button class="btn btn-mini btn-primary wmi-share" id="wmiShare">${escapeHtml(t('worldMap.inviteBtn'))}</button>` +
    `</div>`;
  document.body.appendChild(el);
  requestAnimationFrame(() => el.classList.add('show'));
  // A short confetti burst for the celebration moment, removed after it plays.
  spawnConfetti(el);
  // Live "rescued worldwide" count, fetched server-side from the website's pin
  // API (graceful: the line stays hidden on 0 or any error). Motivates the user
  // to add their pin and push the counter higher.
  (async () => {
    try {
      const n = await RescuedSpeakerCount();
      if (n && n > 0) {
        const c = el.querySelector('#wmiCount');
        if (c) { c.textContent = t('worldMap.countLine', { n }); c.classList.remove('hidden'); }
      }
    } catch { /* no count, just the celebration */ }
  })();
  const close = () => { el.classList.remove('show'); setTimeout(() => el.remove(), 300); };
  const openMap = () => { try { BrowserOpenURL(worldMapURL()); } catch {} close(); };
  const shareBtn = el.querySelector('#wmiShare');
  if (shareBtn) shareBtn.onclick = openMap;
  // The thumbnail is the second, more obvious way in: clicking the map preview
  // opens the same community map on the website.
  const mapBtn = el.querySelector('#wmiMap');
  if (mapBtn) mapBtn.onclick = openMap;
  const closeBtn = el.querySelector('#wmiClose');
  if (closeBtn) closeBtn.onclick = close;
  // Auto-dismiss so it never lingers; the once-ever flag means it will not return.
  setTimeout(close, 20000);
}

// spawnConfetti drops a brief, CSS-animated emoji confetti burst above the invite
// for the celebration moment, then cleans itself up. Pure decoration, best-effort.
function spawnConfetti(anchor) {
  try {
    const burst = document.createElement('div');
    burst.className = 'wmi-confetti';
    const bits = ['🎉', '🎊', '✨', '🌍', '🔊', '🥳'];
    for (let i = 0; i < 14; i++) {
      const s = document.createElement('span');
      s.textContent = bits[i % bits.length];
      s.style.left = Math.round((i / 13) * 100) + '%';
      s.style.animationDelay = (i % 5) * 90 + 'ms';
      burst.appendChild(s);
    }
    anchor.appendChild(burst);
    setTimeout(() => burst.remove(), 2600);
  } catch { /* decoration only */ }
}

// Preview hook: force-show the world-map invite (bypassing the once-ever flag) so
// the celebration can be checked without re-triggering it. Ctrl+Shift+M = the
// first-radio-play invite; Ctrl+Shift+Alt+M = the "whole setup rescued" variant.
// Harmless if a user finds it; it only previews the invite.
try {
  document.addEventListener('keydown', (e) => {
    if (e.ctrlKey && e.shiftKey && (e.key === 'M' || e.key === 'm')) {
      e.preventDefault();
      showWorldMapInvite(e.altKey ? 'all' : 'first');
    }
  });
} catch { /* no preview hook */ }

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
    codec: s.codec, hls: s.hls || 0, tags: s.tags, votes: s.votes || 0, homepage: s.homepage,
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

function buildSearchOpts() {
  const isSearch = state.searchLastMode === 'search' && state.searchLastQuery;
  // For order=name we still fetch 4x the page size so the "Bose-compatible
  // only" filter still has enough left after the strip of HTTPS-only stations
  // like laut.fm. Sorting itself is done client-side in fetchSearchPage.
  const ord = state.searchOrder || 'votes';
  const limit = ord === 'name' ? PAGE_SIZE * 4 : PAGE_SIZE;
  // cc empty = all countries. top:true selects the vote-ordered top list (no
  // free-text query).
  return {
    q: isSearch ? state.searchLastQuery : '',
    cc: state.searchCountry || '',
    lang: state.searchLang || '',
    tag: state.searchTag || '',
    order: ord,
    limit: limit,
    offset: state.searchOffset,
    onlyok: !!state.searchOnlyOK,
    top: !isSearch,
  };
}

async function fetchSearchPage(append) {
  if (!append) {
    $('searchResults').innerHTML = `<div class="muted">${escapeHtml(t('search.loadingStations'))}</div>`;
    $('loadMoreRow').classList.add('hidden');
  }
  try {
    // Query radio-browser DIRECTLY from the app (reliable internet, real CPU)
    // instead of routing through the box agent — the box only ever needs the
    // final stream URL. This is the app-first direction and it removes the box
    // as a point of failure for search (the HTTP 502s in #121). The
    // radiobrowser client does its own multi-mirror failover, so no per-call
    // retry is needed here.
    const page = await RadioSearch(buildSearchOpts()) || [];
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

// isBoseCompatible estimates whether the speaker can reliably play the stream.
// Since stick-agent build 0132 every stream goes through /stream/raw (TLS is no
// longer a concern), and since v0.7.21 STR converts HLS playlists on the fly, so
// HLS stations play too. So:
//   - HLS streams (radio-browser hls=1, or an .m3u8 URL) play via STR's HLS
//     conversion, regardless of the segment codec
//   - MP3 / AAC / AAC+ / AACP / MPEG the box decodes directly
//   - Ogg / Opus / FLAC neither the box nor the proxy can decode
//   - an unknown codec ("UNKNOWN" or empty) is let through, the box tries it
// radio-browser reports the BBC HLS stations (Radio 2/4, which play since
// v0.7.21) as codec "UNKNOWN" with hls=1, and the old check dropped both: it
// only let an EMPTY codec through and knew nothing of HLS, so it treated the
// literal "UNKNOWN" string as incompatible and hid every now-playable HLS
// station when the filter was on (#124).
function isBoseCompatible(s) {
  const url = String(s.url_resolved || s.url || '');
  if (s.hls === 1 || s.hls === '1' || /\.m3u8(\?|#|$)/i.test(url)) return true;
  const codec = String(s.codec || '').toUpperCase();
  if (!codec || codec === 'UNKNOWN') return true; // let the speaker try
  return codec === 'MP3' || codec === 'AAC' || codec === 'AAC+' ||
    codec === 'AACP' || codec === 'MPEG';
}

// streamErrorMessage maps a stream-status reason to a clear, human, localized
// message. The raw HTTP code (403/503) means nothing to a user; the reason
// class does. Falls back to a generic "unreachable" line for unknown reasons.
function streamErrorMessage(reason) {
  switch (reason) {
    case 'blocked':     return t('search.streamBlocked');
    case 'gone':        return t('search.streamGone');
    case 'unavailable': return t('search.streamUnavailable');
    case 'hls':         return t('search.streamHls');
    default:            return t('search.streamUnreachable');
  }
}

// pollStreamFailure asks the agent whether the stream the box just started has
// failed upstream. Radio failures are asynchronous: the box accepts the UPnP
// URL instantly, then the 403/503 only surfaces when it pulls the bytes. We poll
// /api/stream-status for a few seconds; the moment a fresh failure for OUR url
// appears we return it, otherwise we assume the station is playing and return
// null. Best-effort: any fetch error just ends the poll (assume playing).
async function pollStreamFailure(box, url, windowMs = 6000) {
  const deadline = Date.now() + windowMs;
  while (Date.now() < deadline) {
    await new Promise(r => setTimeout(r, 800));
    if (state.nowLocation !== url) return null; // user moved on; stop watching
    let data;
    try {
      const r = await boxFetch(box, '/api/stream-status', {}, 4000);
      if (!r.ok) continue;
      data = await r.json();
    } catch { return null; }
    if (data && data.error && data.url === url) return data;
  }
  return null;
}

// findAlternativeStation looks for ANOTHER radio-browser entry of the same
// station than the ones already tried. Stations are frequently listed several
// times (different mirrors/CDNs); when one URL is geo-blocked or down, a sibling
// entry usually plays. We match by name (exact, then loose) and skip any URL on
// a host we already failed on, preferring entries radio-browser last checked OK.
async function findAlternativeStation(orig, triedHosts) {
  const wanted = (orig.name || '').toLowerCase().trim();
  if (!wanted) return null;
  let list;
  try {
    list = await RadioSearch({ q: orig.name, limit: 20, order: 'votes', top: false }) || [];
  } catch { return null; }
  const candidates = list.filter(s => {
    const u = s.url_resolved || s.url;
    if (!u) return false;
    const h = extractHost(u);
    if (!h || triedHosts.has(h)) return false;
    const n = (s.name || '').toLowerCase().trim();
    return n === wanted || n.includes(wanted) || wanted.includes(n);
  });
  if (candidates.length === 0) return null;
  // Prefer a station radio-browser last checked OK, then by votes (the search
  // already ordered by votes, so a stable partition keeps that secondary order).
  candidates.sort((a, b) => (b.lastcheckok ? 1 : 0) - (a.lastcheckok ? 1 : 0));
  return candidates[0];
}

// playStation plays a radio station and, when its stream fails upstream
// (403 geo-block, 503 down, dead URL), shows a clear reason and automatically
// retries with another radio-browser entry of the SAME station before giving
// up. This turns the most common "every station errors" frustration into a
// usually-silent recovery. Used by every radio play-now button.
async function playStation(s) {
  const box = state.currentBox;
  if (!box) return;
  const tried = new Set();
  let cur = s;
  for (let attempt = 0; attempt < 4; attempt++) {
    const url = cur.url_resolved || cur.url;
    const host = extractHost(url);
    if (host) tried.add(host);
    const chain = stationLogoChain(cur);
    state.nowPlayState = 'BUFFERING_STATE';
    state.nowLocation = url;
    state.nowName = s.name; // keep the user's chosen station name across retries
    state.nowIcon = chain;
    state.nowBitrate = cur.bitrate || 0;
    scheduleLiveBitrate();
    state.nowUUID = cur.stationuuid || '';
    renderPresets();

    let fail = null;
    try {
      await PlayURL(box.host, box.port, url, s.name, chain, cur.stationuuid || '', '', s.homepage || '');
      // Register the play with radio-browser, but ONLY for a real station UUID.
      // Recently-played cards reuse the stream URL as their identity (no UUID), so
      // a plain `if (stationuuid)` fired RadioClick with a URL, which 404s. Guard
      // on the UUID shape, and use .catch() (not try/catch) since the rejection is
      // async: an unhandled 404 promise surfaced as an error toast when playing a
      // recent radio card even though playback itself was fine.
      if (/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(cur.stationuuid || '')) {
        RadioClick(cur.stationuuid).catch(() => {});
      }
      // Refresh the now-playing bar promptly on the happy path; the upstream
      // verdict (success vs 403/503) arrives asynchronously, so poll for it.
      setTimeout(refreshStatus, 1200);
      fail = await pollStreamFailure(box, url);
    } catch (err) {
      // A synchronous failure (box refused the URI) reads as unreachable.
      fail = { reason: 'unreachable', status: 0 };
    }
    if (!fail) return; // playing fine

    const alt = await findAlternativeStation(s, tried);
    if (!alt) {
      state.nowPlayState = '';
      state.nowLocation = '';
      renderPresets();
      showToast(streamErrorMessage(fail.reason) + ' ' + t('search.allSourcesFailed'));
      return;
    }
    showToast(t('search.tryingAlternative', { name: s.name || '' }));
    cur = alt;
  }
  // Exhausted the retry budget without a working source.
  state.nowPlayState = '';
  state.nowLocation = '';
  renderPresets();
  showToast(t('search.allSourcesFailed'));
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
      // playStation handles the upstream-failure case (403/503/dead URL): it
      // shows a clear reason and auto-retries another radio-browser entry of the
      // same station before giving up, so a single blocked mirror no longer
      // looks like "every station errors".
      await playStation(s);
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
      await SetPreset(state.currentBox.host, state.currentBox.port, i, station.name, station.url_resolved || station.url, logo, station.bitrate || 0, station.homepage || '');
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
