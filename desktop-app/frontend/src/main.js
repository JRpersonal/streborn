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
  Pause,
  Stop,
  Status,
  ListDrives,
  WriteStickFiles,
  FormatStick,
  StickVersion,
  StickConfigs,
  AppInfo,
  EjectDrive,
  BoxAgentVersion,
  UpdateBoxAgent,
  WriteWLANConfig,
  WriteRegionConfig,
  WriteNameConfig,
  ListWiFiProfiles,
  TryWiFiPassword,
  CurrentWiFi,
  CheckAppUpdate,
  BoxSettings,
  SetBoxName,
  SetBoxVolume,
  SetBoxBass,
  SelectBoxSource,
  SaveDiagnosticBundle,
  GetLogFilePath,
  BrowserOpenURL,
} from './api.js';

import {
  state,
  loadLastBox,
  saveLastBox,
  loadCachedBoxes,
  saveCachedBoxes,
} from './state.js';

import {
  $,
  escapeHtml,
  escapeAttr,
  decodeXmlEntities,
  formatNumber,
  debounce,
  sleep,
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
};

import {
  extractHost,
  rootDomain,
  iconServicesFor,
  stationLogoCandidates,
  logoImgTag,
  bestLogoForStation,
  stationLogoChain,
} from './logos.js';

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
      <div class="app-locale" role="group" aria-label="${escapeAttr(t('settings.language'))}">
        ${AVAILABLE_LOCALES.map(l => {
          const cc = LOCALE_FLAG_CC[l.code] || l.code.toUpperCase();
          const active = l.code === getLocale() ? ' active' : '';
          return `<button type="button" class="locale-flag${active}" data-locale="${escapeAttr(l.code)}" title="${escapeAttr(l.label)}" aria-label="${escapeAttr(l.label)}" aria-pressed="${l.code === getLocale() ? 'true' : 'false'}"><span class="locale-flag-emoji" aria-hidden="true">${flagFromCC(cc)}</span><span class="locale-flag-code">${escapeHtml(l.code.toUpperCase())}</span></button>`;
        }).join('')}
      </div>
    </div>
    <div class="app-tagline" id="appTagline"></div>
    <div class="app-supported" id="appSupported"></div>
  </header>
  <div class="tabs">
    <button class="tab-btn active" data-view="box">${escapeHtml(t('nav.music'))}</button>
    <button class="tab-btn" data-view="settings">${escapeHtml(t('nav.speakerSettings'))}</button>
    <button class="tab-btn" data-view="setup">${escapeHtml(t('nav.setupStick'))}</button>
  </div>
  <div id="appUpdateBanner" class="app-update-banner hidden"></div>
  <div id="globalSecurityBanner" class="global-security-banner hidden">
    <span class="global-security-text">
      <b>${escapeHtml(t('banner.recommendation'))}</b> ${escapeHtml(t('banner.sshRecommend'))}
    </span>
    <button class="btn btn-mini" id="globalSecurityRebootBtn">${escapeHtml(t('speaker.reboot'))}</button>
  </div>
  <div id="view-box" class="view"></div>
  <div id="view-settings" class="view hidden"></div>
  <div id="view-setup" class="view hidden"></div>

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
  document.querySelectorAll('.locale-flag').forEach(btn => {
    btn.onclick = () => {
      const code = btn.dataset.locale;
      if (code && code !== getLocale() && setLocale(code)) {
        location.reload();
      }
    };
  });
})();

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
  en: 'for SoundTouch 10, 20, 30 and Portable',
};

const TAGLINES = {
  de: 'Bose SoundTouch Lautsprecher ohne Bose Cloud weiter nutzen.',
  fr: 'Continue d\'utiliser tes enceintes Bose SoundTouch sans le cloud Bose.',
  it: 'Continua a usare gli altoparlanti Bose SoundTouch senza il cloud di Bose.',
  es: 'Sigue usando tus altavoces Bose SoundTouch sin la nube de Bose.',
  nl: 'Blijf je Bose SoundTouch speakers gebruiken, zonder de Bose cloud.',
  pt: 'Continua a usar os teus altifalantes Bose SoundTouch sem a cloud Bose.',
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
  $('view-settings').classList.toggle('hidden', view !== 'settings');
  $('view-setup').classList.toggle('hidden', view !== 'setup');
  // Global SSH banner: the Setup tab has no speaker context, so hide
  // the banner there unconditionally. Otherwise let checkSshBanner
  // decide.
  if (view === 'setup') {
    const gb = $('globalSecurityBanner');
    if (gb) gb.classList.add('hidden');
  } else {
    checkSshBanner();
  }
  if (view === 'setup') refreshDrives();
  if (view === 'box') {
    // Refresh the mDNS list on every switch to the music view so a
    // recently renamed speaker or a speaker that went offline does
    // not linger. discoverBoxes is async and non-blocking.
    discoverBoxes();
    refreshStatus();
    loadMusicTabVolume();
  }
  if (view === 'settings') loadBoxSettings();
}

// ---------- Footer ----------

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
  const buildStr = i.build && i.build !== 'dev' ? ` <span class="build-stamp">(Build ${escapeHtml(i.build)})</span>` : '';
  $('appFooter').innerHTML = `
    <div class="footer-left">
      ST Reborn &middot; Version <b>${escapeHtml(i.version)}</b>${buildStr}${i.author ? ' &middot; ' + escapeHtml(i.author) : ''}
      <div class="footer-fine">Independent open source project, donation funded, MIT license.</div>
    </div>
    <div class="footer-right">${links.join(' &middot; ')}</div>
  `;
  $('appFooter').querySelectorAll('.footer-link[data-url]').forEach(a => {
    a.onclick = (e) => { e.preventDefault(); BrowserOpenURL(a.dataset.url); };
  });
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
  checkAppUpdate();
  // appInfo may have arrived after the first discovery completed; the
  // badge function defers until both are known.
  updateSettingsTabBadge();
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
  try {
    const m = await CheckAppUpdate();
    if (!m || !m.version) return;
    const banner = $('appUpdateBanner');
    banner.innerHTML = `
      <div><b>${escapeHtml(t('banner.appUpdateAvail'))}</b> ${escapeHtml(m.version)} <small>${escapeHtml(m.notes || '')}</small></div>
      ${m.downloadUrl ? `<button class="btn btn-mini" id="appUpdateBtn">${escapeHtml(t('banner.download'))}</button>` : ''}
    `;
    banner.classList.remove('hidden');
    const dl = $('appUpdateBtn');
    if (dl) dl.onclick = () => BrowserOpenURL(m.downloadUrl);
  } catch {
    // Stille
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
        <label><input type="checkbox" id="searchOnlyOK" checked /> ${escapeHtml(t('search.onlyOK'))}</label>
        <label><input type="checkbox" id="searchOnlyBose" checked /> ${escapeHtml(t('search.onlyBose'))}</label>
      </div>
      <div class="genre-chips" id="genreChips"></div>
      <div class="search-count muted small hidden" id="searchCount"></div>
      <div class="search-results" id="searchResults"></div>
      <div class="load-more-row hidden" id="loadMoreRow">
        <button class="btn btn-mini" id="loadMoreBtn">${escapeHtml(t('search.loadMore'))}</button>
      </div>
    </div>
  </div>
`;

// Filter Dropdowns befuellen
$('searchCountry').innerHTML = COUNTRIES.map(c =>
  `<option value="${c.cc}">${flagFromCC(c.cc)} ${escapeHtml(c.name)}</option>`
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
      showError(e);
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
  musicVolEl.oninput = () => {
    if (musicVolValEl) musicVolValEl.textContent = musicVolEl.value;
  };
  musicVolEl.onchange = () => {
    musicVolBox = state.currentBox;
    if (!musicVolBox) return;
    if (musicVolTimer) clearTimeout(musicVolTimer);
    musicVolTimer = setTimeout(() => {
      SetBoxVolume(musicVolBox.host, musicVolBox.port,
        parseInt(musicVolEl.value, 10)).catch(showError);
    }, 200);
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
  try {
    const r = await fetch(`http://${box.host}:${box.port}/api/stick/status`);
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
    const url = cc
      ? `http://${state.currentBox.host}:${state.currentBox.port}/api/radio/languages?country=${encodeURIComponent(cc)}&limit=60`
      : `http://${state.currentBox.host}:${state.currentBox.port}/api/radio/languages?limit=40`;
    const r = await fetch(url);
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
      } else {
        state.currentBox = null;
        state.presets = [];
        $('presets').innerHTML = '';
      }
    }
    renderBoxSelect();
    updateSettingsTabBadge();
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
    return `<span class="box-btn${active}" data-host="${b.host}" data-port="${b.port}" role="button" tabindex="0">${escapeHtml(label)}${model} <small>${b.host}</small>${ver}<span class="box-edit" data-host="${b.host}" data-port="${b.port}" title="${escapeAttr(t('speaker.editTitle'))}">&#9881;</span></span>`;
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
        // Stock speaker: the desktop app cannot control it directly.
        // Offer to jump to the USB stick setup flow.
        const label = box.friendlyName || box.name || box.host;
        const ok = await confirmWarn(
          t('speaker.stockConfirmTitle'),
          t('speaker.stockConfirmBody', { label: escapeHtml(label) }),
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
}

let regionLoaded = false;
async function loadStickRegion() {
  if (regionLoaded || !state.currentBox) return;
  try {
    const r = await fetch(`http://${state.currentBox.host}:${state.currentBox.port}/api/region`);
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
      if (data.language && !state.searchLang) {
        state.searchLang = data.language;
      }
      updateFilterIndicators();
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
      const r = await fetch(`http://${state.currentBox.host}:${state.currentBox.port}/api/radio/tags?limit=24`);
      if (r.ok) {
        state.tags = await r.json() || [];
        renderGenreChips();
      }
    } catch {}
  }
  if (state.languages.length === 0) {
    try {
      const r = await fetch(`http://${state.currentBox.host}:${state.currentBox.port}/api/radio/languages?limit=40`);
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
function localizeLanguageName(name) {
  if (!name) return '';
  const translated = tLookup('lang', name);
  if (translated) return translated;
  return name.charAt(0).toUpperCase() + name.slice(1);
}

function renderLanguageOptions() {
  const sel = $('searchLang');
  if (!sel || !state.languages.length) return;
  const opts = [`<option value="">${escapeHtml(t('search.allLanguages'))}</option>`];
  for (const l of state.languages) {
    if (!l.name) continue;
    const label = localizeLanguageName(l.name);
    opts.push(`<option value="${escapeAttr(l.name)}">${escapeHtml(label)} (${l.stationcount})</option>`);
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
function updateSettingsTabBadge() {
  const btn = document.querySelector('.tab-btn[data-view="settings"]');
  if (!btn) return;
  const appVer   = state.appInfo && state.appInfo.version;
  const appBuild = state.appInfo && state.appInfo.build;
  let needsUpdate = false;
  if (appVer) {
    for (const b of state.boxes) {
      if (!b || !b.version) continue;
      const verDiffers   = b.version !== appVer;
      // Three build-related cases to flag as drift:
      //   - both sides populated and different
      //   - we have a build, box has none (older agent that does not
      //     yet broadcast `build=` in mDNS — guaranteed pre-update)
      const buildDiffers = appBuild && b.build && b.build !== appBuild;
      const buildMissing = appBuild && !b.build;
      if (verDiffers || buildDiffers || buildMissing) {
        needsUpdate = true;
        break;
      }
    }
  }
  btn.classList.toggle('has-update', needsUpdate);
}

async function checkBoxUpdate() {
  if (!state.currentBox || !state.appInfo) return;
  const banner = $('boxUpdateBanner');
  banner.classList.add('hidden');
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
        <small>${escapeHtml(t('update.speakerAppLine', { box: boxLabel, app: appLabel }))}</small>
      </div>
      <button class="btn btn-primary btn-mini" id="boxUpdateBtn">${escapeHtml(t('update.refreshBtn'))}</button>
    `;
    banner.classList.remove('hidden');
    $('boxUpdateBtn').onclick = doBoxUpdate;
  } catch {
    if (state.currentBox.version && state.currentBox.version !== state.appInfo.version) {
      banner.innerHTML = `
        <div class="update-msg">
          <b>${escapeHtml(t('update.speakerUpdateAvail'))}</b><br>
          <small>${escapeHtml(t('update.speakerRunningOld', { boxVersion: state.currentBox.version, appVersion: state.appInfo.version }))}</small>
        </div>
        <button class="btn btn-primary btn-mini" id="boxUpdateBtn">${escapeHtml(t('update.refreshBtn'))}</button>
      `;
      banner.classList.remove('hidden');
      $('boxUpdateBtn').onclick = doBoxUpdate;
    }
  }
}

async function doBoxUpdate() {
  if (!state.currentBox) return;
  // Drive both update buttons together (banner up top + stick info section)
  const buttons = () => ['boxUpdateBtn', 'stickInfoUpdateBtn'].map(id => $(id)).filter(Boolean);
  const setStatus = (text) => buttons().forEach(b => { b.textContent = text; b.disabled = true; });
  const reset = () => buttons().forEach(b => { b.disabled = false; b.textContent = t('update.refreshBtn'); });
  setStatus(t('update.uploading'));
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
    // a build matching the app's appBuild. Up to 3 minutes, 5 s
    // between attempts. As long as the build is wrong or the box
    // is unreachable, the buttons stay locked.
    const startMs = Date.now();
    const deadlineMs = startMs + 180_000;
    let confirmed = false;
    while (Date.now() < deadlineMs) {
      await sleep(5_000);
      try {
        const v = await BoxAgentVersion(targetBox.host, targetBox.port);
        if (v && v.build && (!appBuild || v.build === appBuild)) {
          confirmed = true;
          break;
        }
        const waited = Math.round((Date.now() - startMs) / 1000);
        setStatus(t('update.oldBuildWait', { sec: waited }));
      } catch {
        const waited = Math.round((Date.now() - startMs) / 1000);
        setStatus(t('update.waitingForSpeaker', { sec: waited }));
      }
    }
    if (confirmed) {
      showToast(t('update.doneToast'));
    } else {
      showToast(t('update.tookLongerToast'));
    }
    // Refresh app state regardless of confirmation so the user sees
    // current truth (either updated or still in OTA).
    await discoverBoxes();
    checkBoxUpdate();
    if (state.view === 'settings') loadBoxSettings();
    reset();
  } catch (e) {
    showError(t('update.failed', { err: String(e) }));
    reset();
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
        const r = await fetch(`http://${state.currentBox.host}:${state.currentBox.port}/api/radio/search?${params}`);
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
        p.art = logo;
        SetPreset(state.currentBox.host, state.currentBox.port, p.slot, p.name, p.stream_url, logo).catch(() => {});
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
  const m = loc.match(/\/stream\/(\d+)(?:[/?#]|$)/);
  return m ? parseInt(m[1], 10) : null;
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
      (activeStreamURL && p.stream_url === activeStreamURL)
    );
    const hasErr = !!state.presetErrors[i];
    const div = document.createElement('div');
    div.className = 'preset' + (p ? '' : ' empty') + (isActive ? ' playing' : '') + (hasErr ? ' error' : '');
    div.dataset.slot = i;
    if (p) {
      let stateLabel = '';
      if (hasErr) {
        stateLabel = `<div class="preset-state state-err">&#9888; ${escapeHtml(state.presetErrors[i])}</div>`;
      } else if (isActive) {
        const ps = state.nowPlayState;
        if (ps === 'PLAY_STATE') {
          stateLabel = `<div class="preset-state state-play">${escapeHtml(t('preset.statePlay'))}</div>`;
        } else if (ps === 'BUFFERING_STATE') {
          stateLabel = `<div class="preset-state state-buf">${escapeHtml(t('preset.stateBuf'))}</div>`;
        } else if (ps === 'PAUSE_STATE') {
          stateLabel = `<div class="preset-state state-pause">${escapeHtml(t('preset.statePause'))}</div>`;
        }
      }
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
      if (p.art) {
        addCands(p.art);
      } else if (isActive && state.nowIcon) {
        addCands(state.nowIcon);
        // Auto-persist so the preset has its logo on the next load.
        p.art = state.nowIcon;
        SetPreset(state.currentBox.host, state.currentBox.port, p.slot, p.name, p.stream_url, state.nowIcon).catch(() => {});
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
      const logo = presetCandidates.length === 0 ? '' :
        `<img class="preset-logo" src="${escapeAttr(presetCandidates[0])}"
              data-fallbacks="${escapeAttr(presetCandidates.slice(1).join('|'))}"
              onerror="window.__nextLogoFallback(this)"/>`;
      div.innerHTML = `
        <div class="preset-head"><span class="num">${escapeHtml(t('preset.key', { n: i }))}</span><span class="del" data-slot="${i}" title="${escapeAttr(t('preset.deleteTitle'))}">&times;</span></div>
        <div class="preset-body">
          ${logo}
          <div class="preset-text">
            <div class="name">${escapeHtml(p.name || t('preset.key', { n: i }))}</div>
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
          slot, src.name, src.stream_url, src.art || ''
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
      slot, name, state.nowLocation, state.nowIcon || ''
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

async function play(slot) {
  const p = state.presets.find(x => x.slot === slot);
  if (p) {
    // Optimistic UI: set BUFFERING_STATE immediately so the user
    // gets feedback. Sticky for 6 s. During that window refreshStatus
    // must not flip the preset back to grey when the speaker still
    // reports the old stream or an empty one.
    state.nowPlayState = 'BUFFERING_STATE';
    state.nowLocation = p.stream_url || '';
    state.nowName = p.name || '';
    state.nowIcon = p.art || '';
    state.nowUUID = '';
    state.optimisticUntil = Date.now() + 6000;
    delete state.presetErrors[slot];
    renderPresets();
  }
  try {
    await PlaySlot(state.currentBox.host, state.currentBox.port, slot);
    delete state.presetErrors[slot];
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

async function action(kind) {
  if (!state.currentBox) return;
  const fn = kind === 'pause' ? Pause : Stop;
  try { await fn(state.currentBox.host, state.currentBox.port); } catch (e) { showError(e); }
  setTimeout(refreshStatus, 1000);
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
    // Update state.nowIcon. Prefer the art tag from now_playing.
    // If that is empty AND we are playing through the stream proxy,
    // adopt the logo of the source preset. Bose UPnP items emitted
    // by hardware key presses carry no art tag, so we need this
    // fallback.
    if (!optimistic) {
      const slotFromProxy = activeSlotFromLocation(newLoc);
      if (artRaw) {
        state.nowIcon = artRaw;
      } else if (slotFromProxy !== null) {
        const ap = state.presets.find(p => p.slot === slotFromProxy);
        state.nowIcon = (ap && ap.art) || '';
      } else if (!newLoc) {
        state.nowIcon = '';
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
    }

    if (stateChanged && state.presets.length > 0) {
      renderPresets();
    }

    let stateLabel;
    let stateClass;
    let displayName = name;
    if (ps === 'PLAY_STATE') { stateLabel = t('status.playing'); stateClass = 'play'; }
    else if (ps === 'BUFFERING_STATE') { stateLabel = t('status.buffering'); stateClass = 'buf'; }
    else if (ps === 'PAUSE_STATE') { stateLabel = t('status.paused'); stateClass = 'idle'; }
    else if (src === 'STANDBY') { stateLabel = t('status.standby'); stateClass = 'idle'; }
    else { stateLabel = ''; stateClass = 'idle'; }
    // Source-specific labels: when AUX or BT is the active source,
    // show "AUX input" or "Bluetooth" instead of an empty name.
    if (src === 'AUX') {
      displayName = t('status.auxInput');
      if (!stateLabel) stateLabel = t('status.active');
    } else if (src === 'BLUETOOTH') {
      displayName = t('status.bluetooth');
      if (!stateLabel) stateLabel = t('status.active');
    }

    $('statusBar').className = 'status-bar status-' + stateClass;
    if (displayName) {
      $('statusBar').innerHTML = `<span class="now">&#9654; ${escapeHtml(displayName)}</span>${stateLabel ? ' <small>' + escapeHtml(stateLabel) + '</small>' : ''}`;
    } else if (stateLabel) {
      $('statusBar').innerHTML = `<span class="muted">${escapeHtml(stateLabel)}</span>`;
    } else {
      $('statusBar').innerHTML = `<span class="muted">${escapeHtml(t('status.ready'))}</span>`;
    }

    // Source buttons: highlight the active source in green.
    document.querySelectorAll('.btn-source').forEach(b => {
      const s = b.dataset.source;
      const active = (s === 'AUX' && src === 'AUX') ||
                     (s === 'BLUETOOTH' && src === 'BLUETOOTH') ||
                     (s === 'STANDBY' && src === 'STANDBY');
      b.classList.toggle('active', active);
    });
  } catch {
    $('statusBar').textContent = '—';
  }
}

// ---------- Search ----------

const PAGE_SIZE = 30;

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
    return `http://${state.currentBox.host}:${state.currentBox.port}/api/radio/search?${params.toString()}`;
  }
  return `http://${state.currentBox.host}:${state.currentBox.port}/api/radio/top?${params.toString()}`;
}

async function fetchSearchPage(append) {
  const url = buildSearchURL();
  if (!append) {
    $('searchResults').innerHTML = `<div class="muted">${escapeHtml(t('search.loadingStations'))}</div>`;
    $('loadMoreRow').classList.add('hidden');
  }
  try {
    const r = await fetch(url);
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
      state.searchOnlyBose && (state.searchResults || []).length > 0
        ? t('search.noBoseStations')
        : t('search.noStationsFound')
    ) + '</div>';
    return;
  }
  res.innerHTML = list.map((s, i) => {
    const flag = flagFromCC(s.countrycode);
    const okClass = s.lastcheckok ? 'ok' : 'bad';
    const okTitle = s.lastcheckok ? t('search.checkOk') : t('search.checkBad');
    let trend = '';
    if (s.clicktrend > 0) trend = `<span class="result-trend" title="${escapeAttr(t('search.trendUp', { n: s.clicktrend }))}">&#9650;</span>`;
    else if (s.clicktrend < 0) trend = `<span class="result-trend up-down" title="${escapeAttr(t('search.trendDown', { n: s.clicktrend }))}">&#9660;</span>`;

    const countryDe = translateCountry(s.country);
    const tagChips = translateTags(s.tags).slice(0, 4).map(tag => `<span class="tag-pill">${escapeHtml(tag)}</span>`).join('');

    const metaBits = [];
    if (countryDe) metaBits.push(escapeHtml(countryDe));
    if (s.bitrate) metaBits.push(`${s.bitrate} kbit/s`);
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
          <div class="result-meta">${metaBits.join(' &middot; ')}</div>
          ${tagChips ? `<div class="result-tag-chips">${tagChips}</div>` : ''}
        </div>
        <div class="result-actions">
          <button class="btn btn-mini play-now" data-i="${i}" title="${escapeAttr(t('search.playNow'))}">&#9654;</button>
          <button class="btn btn-mini pick" data-i="${i}" title="${escapeAttr(t('search.assignToKey'))}">&#10133;</button>
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
}

function openPick(station) {
  $('pickTitle').textContent = t('preset.assignStationTitle');
  $('pickSub').textContent = station.name + (station.bitrate ? ' (' + station.bitrate + ' kbit/s)' : '');
  const grid = $('pickGrid');
  grid.innerHTML = '';
  for (let i = 1; i <= 6; i++) {
    const p = state.presets.find(x => x.slot === i);
    const b = document.createElement('button');
    b.className = 'pick-slot' + (p ? ' has' : '');
    b.innerHTML = '<div class="ps-num">' + escapeHtml(t('preset.key', { n: i })) + '</div><div class="ps-name">' + (p ? escapeHtml(p.name) : escapeHtml(t('preset.pickEmpty'))) + '</div>';
    b.onclick = async () => {
      try {
        const logo = stationLogoChain(station);
        await SetPreset(state.currentBox.host, state.currentBox.port, i, station.name, station.url_resolved || station.url, logo);
        closePick();
        await loadPresets();
        if (station.stationuuid) {
          VoteStation(state.currentBox.host, state.currentBox.port, station.stationuuid).catch(() => {});
        }
        showToast(t('preset.savedToKey', { n: i, name: station.name }));
      } catch (err) { showError(err); }
    };
    grid.appendChild(b);
  }
  $('pickModal').classList.remove('hidden');
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
  if (/refused/i.test(s)) return t('settingsView.errRefused');
  if (/timeout|deadline/i.test(s)) return t('settingsView.errTimeout');
  if (/no such host|no route/i.test(s)) return t('settingsView.errNoRoute');
  return t('settingsView.errGeneric', { err: s });
}


function renderBoxSettings(s, box) {
  const info = s.info || {};
  const vol = s.volume || {};
  const bass = s.bass || {};
  const net = s.network || {};
  const sources = rollupSources(s.sources || []);
  const wifi = (net.interfaces || []).find(i => i.type === 'WIFI_INTERFACE' && i.state === 'NETWORK_WIFI_CONNECTED');
  const signalLabel = {
    'GOOD_SIGNAL': t('signal.good'),
    'MARGINAL_SIGNAL': t('signal.marginal'),
    'POOR_SIGNAL': t('signal.poor'),
    'NO_SIGNAL': t('signal.none'),
  };
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
        <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.wlanSignal'))}</span><span class="kv-val">${escapeHtml(signalLabel[wifi.signal] || wifi.signal || '-')}</span></div>
        <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.wlanFrequency'))}</span><span class="kv-val">${wifi.frequencyKHz ? (wifi.frequencyKHz/1000).toFixed(0) + ' MHz' : '-'}</span></div>
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
      <p class="muted small">${escapeHtml(t('settingsView.actionsHelp'))}</p>
      <div class="setting-row">
        <button class="btn btn-mini" id="boxSyncPresetsBtn">${escapeHtml(t('settingsView.syncHardwareKeys'))}</button>
        <button class="btn btn-mini btn-danger" id="boxRebootBtn">${escapeHtml(t('speaker.reboot'))}</button>
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
        softwareBtn = `<button class="btn btn-mini btn-primary" id="stickInfoUpdateBtn">${escapeHtml(t('update.refreshBtn'))}</button>`;
      } else {
        softwareLine = `<span class="fw-old">${escapeHtml(t('settingsView.swOutdated'))}</span> <span class="muted small">${escapeHtml(boxVer)} &rarr; ${escapeHtml(appVer)}</span>`;
        softwareBtn = `<button class="btn btn-mini btn-primary" id="stickInfoUpdateBtn">${escapeHtml(t('update.refreshBtn'))}</button>`;
      }
    } catch {}

    // USB stick mount status. Try /api/stick/status first (newer
    // agent); fall back to /api/debug/state.stick_listing for older
    // agent versions.
    let stickLine = `<span class="muted small">${escapeHtml(t('common.unknown'))}</span>`;
    let stickMounted = false;
    let sshOpen = false;
    try {
      const r = await fetch(`http://${box.host}:${box.port}/api/stick/status`);
      const ct = r.headers.get('content-type') || '';
      if (r.ok && ct.includes('json')) {
        const data = await r.json();
        if (data.mounted) {
          stickMounted = true;
          stickLine = `<span class="fw-ok">&#10003; ${escapeHtml(t('settingsView.stickDetected'))}</span>` + (data.version ? ` <span class="muted small">${escapeHtml(data.version)}</span>` : '');
        } else {
          stickLine = `<span class="fw-old">${escapeHtml(t('settingsView.stickNotDetected'))}</span>`;
        }
        sshOpen = !!data.sshOpen;
      } else {
        // Fallback: debug/state listing for older agents.
        const rd = await fetch(`http://${box.host}:${box.port}/api/debug/state`);
        if (rd.ok && (rd.headers.get('content-type') || '').includes('json')) {
          const d = await rd.json();
          const listing = d.stick_listing;
          if (Array.isArray(listing) && listing.length > 0 && !String(listing[0]).startsWith('ERR')) {
            stickMounted = true;
            stickLine = `<span class="fw-ok">&#10003; ${escapeHtml(t('settingsView.stickDetected'))}</span>`;
          } else {
            stickLine = `<span class="fw-old">${escapeHtml(t('settingsView.stickNotDetected'))}</span>`;
          }
        }
      }
    } catch {}

    // Show the warning banner only once the stick is no longer
    // mounted — while a stick is in the box, the agent is either
    // doing initial install or applying an update, SSH is expected
    // to be open in that window, and the banner is just noise.
    const gb = $('globalSecurityBanner');
    if (gb) {
      const show = sshOpen && !stickMounted;
      gb.classList.toggle('hidden', !show);
    }

    const securityWarn = sshOpen ? `
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
    if (ub) ub.onclick = doBoxUpdate;
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

  // App Region dropdown fuellen + aktuelle Region selektieren
  const regSel = $('appRegionSelect');
  if (regSel) {
    regSel.innerHTML = COUNTRIES.filter(c => c.cc).map(c =>
      `<option value="${c.cc}">${flagFromCC(c.cc)} ${escapeHtml(c.name)}</option>`
    ).join('');
    const updateCurrentDisplay = (cc) => {
      const el = $('currentAppRegion');
      if (!el) return;
      const c = COUNTRIES.find(x => x.cc === cc);
      el.innerHTML = c
        ? `${flagFromCC(cc)} ${escapeHtml(c.name)} (${escapeHtml(cc)})`
        : escapeHtml(cc || t('common.unknown'));
    };
    fetch(`http://${box.host}:${box.port}/api/region`).then(r => r.ok ? r.json() : null).then(data => {
      if (data && data.country) {
        regSel.value = data.country;
        updateCurrentDisplay(data.country);
      }
    }).catch(() => {});
    $('appRegionSave').onclick = async () => {
      const cc = regSel.value;
      try {
        const r = await fetch(`http://${box.host}:${box.port}/api/region`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ country: cc }),
        });
        if (!r.ok) throw new Error('HTTP ' + r.status);
        const data = await r.json();
        try { localStorage.removeItem('userTouchedRegion'); } catch {}
        state.searchCountry = data.country;
        state.searchLang = data.language;
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
  $('boxVolume').oninput = () => { $('boxVolumeVal').textContent = $('boxVolume').value; };
  $('boxVolume').onchange = () => debouncedSetVolume(box);
  $('boxBass').oninput = () => { $('boxBassVal').textContent = formatRel($('boxBass').value); };
  $('boxBass').onchange = () => debouncedSetBass(box, bass.default || 0);
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
    try {
      const profiles = await ListWiFiProfiles() || [];
      sel.innerHTML = `<option value="">${escapeHtml(t('settingsView.wlanPickPlaceholder'))}</option>` +
        profiles.map(p => `<option value="${escapeAttr(p.ssid)}">${escapeHtml(p.ssid)}</option>`).join('');
    } catch {
      sel.innerHTML = `<option value="">${escapeHtml(t('setup.wlanListUnavailable'))}</option>`;
    }
  }
  $('boxWlanRefresh').onclick = loadBoxWlanList;
  $('boxWlanSelect').onchange = async () => {
    const v = $('boxWlanSelect').value;
    if (!v) return;
    $('boxWlanSSID').value = v;
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
      const r = await fetch(`http://${box.host}:${box.port}/api/box/wlan`, {
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
    `<option value="${c.cc}">${flagFromCC(c.cc)} ${escapeHtml(c.name)}</option>`
  ).join('');
  const saved = (() => { try { return localStorage.getItem('setupRegion'); } catch { return null; }})();
  sel.value = saved || 'DE';
})();

$('drivesRefresh').onclick = () => refreshDrives(true);
$('setupGo').onclick = doSetup;
$('wlanRefresh').onclick = loadWifiProfiles;
$('wlanSelect').onchange = onWifiSelect;
$('wlanShowPass').onclick = togglePasswordVisibility;

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
        onWifiSelect();
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
      html += `<p class="setup-warn">${t('setup.ejectedWarn')}</p>`;
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
  } catch (e) {
    $('setupResult').innerHTML = `<div class="setup-err">${escapeHtml(t('common.error'))}: ${escapeHtml(String(e))}</div>`;
  }
  $('setupGo').disabled = false;
}


renderFooter();

// Prefill from the cache first so the UI shows the last selected
// speaker immediately. discoverBoxes refreshes the real list in the
// background within a few seconds.
(function bootFromCache() {
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
})();

discoverBoxes();
loadWifiProfiles();
setInterval(refreshStatus, 2000);
