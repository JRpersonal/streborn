import './style.css';
// UI 2.0 preview (#1285). Every rule in it is scoped under html.ui2, which is
// only set once the preview shell is mounted, so with the preview off it
// changes nothing.
import './shell2/shell2.css';
import { pinPromptKey } from './worldmapinvite.js';
import { noteCheckFailed, noteCheckSucceeded, shouldSayChecksAreFailing } from './updatecheckhealth.js';
import { sshBannerShow } from './sshbanner.js';
import { maybeShowStableNameNotice } from './stablenamenotice.js';
import { announceHighscores } from './arcadeshare.js';
import { openHighscorePopup } from './arcadepopup.js';
import { firewallBannerView, firewallUnblockToast, firewallSuspectGate } from './firewallbanner.js';
import { wingetInstallLabel, wingetOutcomeView, wingetFailureView } from './wingetupdate.js';
import { runConflictCleanup } from './conflictcleanup.js';
import { boxCannotReachInternet, noInternetBoxes, noInternetText } from './nointernet.js';
import { speakerTileState } from './speakertile.js';
import { wantsFastPoll } from './trackprogress.js';
import {
  DiscoverBoxes,
  RefreshKnownBoxes,
  AddBoxByIP,
  RebootBox,
  RemoveConflictingMod,
  RestoreSTRCloud,
  UpdateFailureReport,
  PendingUpdateIntent,
  RunSpeakerUpdate,
  RunFleetUpdate,
  SyncBoxPresets,
  GetBoxFirmware,
  BoxInstallReachable,
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
  BoxStoragePreflight,
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
  NewerCopyNextToThisOne,
  ConsumeStableNameNotice,
  DownloadUpdate,
  ApplyUpdate,
  UpdateViaWinget,
  ConsumeWingetUpgradeFailure,
  ClipboardSetText,
  RevealUpdateFile,
  ResolveStationLogo,
  BoxSettings,
  SetBoxName,
  SetBoxBass,
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
  RecentPlayed,
  SaveDiagnosticBundle,
  GetLogFilePath,
  InstallSTROnBox,
  RepairInstallViaSSH,
  TrueFactoryReset,
  UninstallSTR,
  ProbeSetupAP,
  PushWLANToBox,
  ListMediaServers,
  BrowseLibrary,
  LogClientError,
  BrowserOpenURL,
  DissolveZone,
  EventsOn,
  boxFetch,
  GetArcade,
  CheckFirewall,
  AllowThroughFirewall,
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
  // Wails' own callback registry drops in-flight call IDs when the webview
  // context is replaced underneath it - a zoom or text-size change, a resize, a
  // reload - while Go calls are still outstanding. What surfaces is
  // "Callback 'main.App.CheckAppUpdate-3018697214' not registered!!!" thrown
  // from wails/runtime.js. Nothing in the app is broken by it: the answer to a
  // call nobody is waiting for any more is discarded, and the next poll simply
  // asks again.
  //
  // It still goes to the log, because a burst of them means the webview is
  // being recycled far more than it should be, and that IS worth seeing in a
  // diagnostic. It must not raise the red overlay though. A user changed the
  // text size, got a wall of red he had never seen, and reported it as a fault
  // (#676, and the same thing in discussion #597 on macOS). An error banner
  // that cries wolf about internal plumbing teaches people to ignore it, which
  // costs us the one time it matters.
  const isFrameworkNoise = (detail) => {
    const d = String(detail || '');
    return /Callback\s+'[^']*'\s+not registered/i.test(d) ||
           (/wails\/runtime\.js/i.test(d) && /not registered/i.test(d));
  };
  const report = (kind, detail) => {
    try { LogClientError(`${kind}: ${detail}`); } catch {}
    try { console.error(kind, detail); } catch {}
    if (isFrameworkNoise(detail)) return;
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
} from './state.js';

import {
  $,
  escapeHtml,
  escapeAttr,
  debounce,
  formatRemaining,
  splitUpdateTargets,
  confirmWarn,
  closeWarn,
  showError,
  showToast,
  hideToast,
  compareVerBuild,
  getBoxLabel,
  dismissNotice,
  noticeDismissed,
  clearNoticeDismissal,
  stormBannerBoxes,
  holdRefusedBannerKey,
  STEREO_ICON,
  GROUP_ICON,
} from './utils.js';
import { boxRecordChange } from './boxstate.js';

// Group membership (who follows master X) and the shared zoneLive poll live
// in groups.js: ONE implementation for the selector frames, the group chips
// and the Multi-Room tab. masterOf is imported under a distinct name because
// renderBoxSelect keeps a local box-shaped wrapper of the same name.
import {
  masterOf as zoneMasterOf,
  applyOptimisticZone,
  fetchZoneLive,
  sameBoxIdentity,
  resolveBoxByRef,
  stereoPairsOf,
  masterBoxForKey,
  pairMemberBoxes,
  groupCount,
  groupCountSplit,
  onZoneLive,
  notifyZoneLive,
} from './groups.js';
import { pairDisplayName, healPairNames } from './stereoNames.js';
import { findStalePairs, confirmStalePairs, staleFindingFor } from './stalestereo.js';

// The already-on-another-key refusal and the offer to move the station live in
// presetmove.js, so vitest can drive the whole decision without a DOM.

// Pure decisions of the search flow (URL-paste detection, the synthetic
// play-this-URL card, the relaxed-filters hint) live in searchflow.js so
// vitest covers them without a DOM.

import {
  flagFromCC,
  flagSvg,
  localeFlagSvg,
} from './localization.js';

import {
  t,
  getLocale,
  setLocale,
  AVAILABLE_LOCALES,
} from './i18n/index.js';

// The Ideas category sorted by votes: one discussion per open backlog item.
const IDEAS_VOTE_URL = 'https://github.com/JRpersonal/streborn/discussions/categories/ideas?discussions_q=category%3AIdeas+sort%3Atop';

// LOCALE_FLAG_CC maps i18n locale codes to ISO-3166 alpha-2 country
// codes for flag emoji rendering. The "language flag" mapping is a UX
// convention: English uses the Union Jack rather than US for global
// audiences. Add new entries here when registering a new bundle.
// A locale whose code happens to look like a country code is the trap here:
// "ar" upper-cases to AR, which is Argentina. Locales with no country of their
// own are listed with an empty string so the fallback below cannot invent one;
// they are drawn by localeFlagSvg from a script mark instead.
const LOCALE_FLAG_CC = {
  ar: '',
  'zh-Hant': '',
  en: 'GB',
  de: 'DE',
  fr: 'FR',
  es: 'ES',
  ja: 'JP',
  uk: 'UA',
};

import {
  rootDomain,
  iconServicesFor,
  stationLogoCandidates,
  bestLogoForStation,
} from './logos.js';

// First view extracted out of this monolith into its own module (#135). The view
// pulls state/utils/i18n/api from the shared modules; only the slot-picker modal
// is main.js-local, injected below. New views should follow this pattern so this
// file stops growing.
import { renderRecent, initRecentView, repaintRecentFavStars, recentFollowsShellPick } from './views/recent.js';
import { shareModalHTML, openShareModal } from './share.js';
import { donateButtonsHTML, wireDonateButtons, donateFooterLinkHTML } from './donate.js';
import { renderMultiroom, initMultiroomView, stopMultiroomLive, resetMultiroomNotes } from './views/multiroom.js';
import { renderSpotifyAlpha, initSpotifyView } from './views/spotify.js';
import { initStreamSourceView } from './views/streamsource.js';
import { renderWishes, initWishesView } from './views/wishes.js';
import { appendSavedBundlePath, failReportSaveHosts } from './failreport.js';
// Which of a speaker's sources are line inputs a person can switch to, and what
// each button is called (sourceinputs.js, vitest-covered).
// Turning a refused preset transfer into a readable sentence is a pure
// decision (copyreport.js, vitest-covered).
// App-wide accessibility prefs (text size + theme). Applied to <html> before
// the skeleton renders so the first paint already reflects the chosen size and
// theme. The matching CSS lives in style.css (html.a11y-*).
import { applyA11y, getScale, setScale, getTheme, setTheme } from './a11y.js';
import { canKeepUi2Preview, getUi2Preview, setUi2Preview } from './uipreview.js';
import {
  mountShell2,
  shell2Mounted,
  shellListsSpeakers,
  syncShellNav,
  renderShellSpeakers,
  syncShellPlayer,
  mirrorShellBadges,
} from './shell2/mount.js';
import { playerOnScreen } from './statusrefreshscope.js';
import { watchTabs } from './tabfit.js';
applyA11y();
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
  throttledSetBass,
  openWebhookKeyMap,
  noNetworkHere,
} from './views/settings.js';
// Library (DLNA MediaServer browse) view, extracted from this monolith, same
// pattern as the views above. openLibrary is the entry point switchView calls;
// showSlotPicker / formatDuration are main.js-local helpers it reuses, injected
// below.
import { openLibrary, initLibraryView, libraryFollowSpeaker } from './views/library.js';
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
  installRunActive,
} from './views/setup.js';
import { onSpeakerPurge } from './speakerPurge.js';
// The Music view ("Listen": now playing, presets, volume, sources, search,
// favourites, Pandora, the group panel and the status poll), extracted from
// this monolith into src/views/music/. Same pattern as the views above:
// mountMusicView builds #view-box where the markup used to be assigned
// inline, initMusicView injects the main.js-local helpers the view calls,
// and the names below are the Music functions the shell, the boot sequence
// and the other views still call.
import {
  dissolveIncompleteMessage,
  effectivePlayTarget,
  groupEditPendingHosts,
  favStationsJSON,
  initMusicView,
  isFav,
  loadMusicTabVolume,
  loadPresets,
  loadStickRegion,
  loadTaxonomy,
  localizeLanguageName,
  mergeFavStations,
  mountMusicView,
  onFavRemovalsApplied,
  openPandoraPick,
  openPick,
  playStation,
  pollFavRemovalsOnCurrentBox,
  pollTrackPosition,
  purgeMusicStateForSpeaker,
  refreshBalance,
  refreshSourceButtons,
  refreshStatus,
  renderGroupControl,
  renderNowPlayingBar,
  renderTrackProgress,
  resetNowPlaying,
  showSlotPicker,
  syncFavRemovals,
  syncVolumeScopeOnZoneLive,
  toggleFav,
  toggleGroupMember,
  trackPos,
  updateFavModeBtn,
  updateFilterIndicators,
  updatePandoraModeBtn,
} from './views/music/index.js';
// Inject the main.js-local helpers the views reuse so they behave exactly as
// before without reimplementing them. All hoisted function declarations, safe
// to pass here.
initRecentView({ showSlotPicker, playStation, openPick, toggleFav, isFav, openPandoraPick, renderNowPlayingBar, refreshStatus, pollFavRemovals: pollFavRemovalsOnCurrentBox });
// A favourite removed on a phone takes its star off the Recently-played cards
// the moment the app learns of it, not on that view's next 30 s repaint (#1339).
onFavRemovalsApplied(() => { if (state.view === 'recent') repaintRecentFavStars(); });
// openSpeakerSettings selects a speaker and shows its settings, which is where
// its update lives. The Multi-Room tab needs it: a card that says a speaker
// must be updated first has to be able to take the owner there.
function openSpeakerSettings(box) {
  if (!box) return;
  try { selectBox(box); } catch { /* selection is best effort */ }
  state.settingsBox = box;
  switchView('settings');
}

initMultiroomView({ boxNeedsUpdate, discoverBoxes, selectBox, switchView, openSpeakerSettings, openWebhookKeyMap });
initSpotifyView({
  switchView,
  // Live STR speaker list for the "sync Spotify login to all speakers" action.
  strBoxes: () => (state.boxes || [])
    .filter(b => b && b.kind !== 'stock' && b.deviceID && b.host)
    .map(b => ({ host: b.host, port: b.port, name: getBoxLabel(b) })),
});
initSettingsView({ switchView, updateFilterIndicators, discoverBoxes, renderBoxSelect, localizeLanguageName, doBoxUpdate, updateAllBoxes, boxNeedsUpdate, loadPresets, getRoomNames, speakerPicked: speakerPickedInTab, favStationsJSON, mergeFavStations });
initLibraryView({ showSlotPicker, formatDuration, effectivePlayTarget, speakerPicked: speakerPickedInTab, renderNowPlayingBar, refreshStatus });
initSetupView({ switchView, discoverBoxes, doBoxUpdate, getRoomNames, celebrateProvision: inviteWorldMapAfterProvision, speakerPicked: speakerPickedInTab });
initStreamSourceView({
  playStation: (station, playback) => playStation({
    url: playback.streamURL,
    url_resolved: playback.streamURL,
    name: playback.title || station.name,
    stationuuid: '',
    homepage: '',
    codec: '',
    bitrate: 0,
  }, { streamSource: true }),
});
initWishesView();
// Music view deps: the shell helpers the Music modules call (see
// views/music/deps.js for what each one is used for).
initMusicView({ checkAppUpdate, checkSshBanner, discoverBoxes, maybeInviteWorldMap, refreshMusicZones, renderBoxSelect, speakerPickedInTab, stereoStaleForError, switchView });
// Session maps this file keeps per speaker, cleared when STR is removed from
// that speaker (Settings > Remove STR calls purgeSpeakerLocalState; see
// speakerPurge.js). Registered as a hook because speakerPurge.js cannot import
// this file. The maps are declared further down; the hook only runs on a
// purge, long after the module has finished evaluating. The Music view's own
// per-speaker maps are cleared by purgeMusicStateForSpeaker.
onSpeakerPurge(({ host }) => {
  if (host) {
    purgeMusicStateForSpeaker(host);
    worldMapKindSeen.delete(host);
    worldMapEverStr.delete(host);
    for (const key of [...supervisedIntent]) {
      if (key === host || key.startsWith(host + ':')) supervisedIntent.delete(key);
    }
  }
});

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
      <div class="app-a11y a11y-dd">
        <button type="button" class="a11y-dd-trigger" id="a11yTrigger" aria-haspopup="dialog" aria-expanded="false" aria-label="${escapeAttr(t('a11y.title'))}" title="${escapeAttr(t('a11y.title'))}"><span class="a11y-dd-icon" aria-hidden="true">Aa</span></button>
        <div class="a11y-dd-menu" id="a11yMenu" role="dialog" aria-label="${escapeAttr(t('a11y.title'))}" hidden>
          <div class="a11y-group">
            <div class="a11y-group-label" id="a11ySizeLabel">${escapeHtml(t('a11y.textSize'))}</div>
            <div class="a11y-seg" role="group" aria-labelledby="a11ySizeLabel">
              <button type="button" data-scale="1" aria-pressed="${getScale() === 1}">${escapeHtml(t('a11y.size.normal'))}</button>
              <button type="button" data-scale="2" aria-pressed="${getScale() === 2}">${escapeHtml(t('a11y.size.large'))}</button>
              <button type="button" data-scale="3" aria-pressed="${getScale() === 3}">${escapeHtml(t('a11y.size.xlarge'))}</button>
            </div>
          </div>
          <div class="a11y-group">
            <div class="a11y-group-label" id="a11yThemeLabel">${escapeHtml(t('a11y.theme'))}</div>
            <div class="a11y-seg" role="group" aria-labelledby="a11yThemeLabel">
              <button type="button" data-theme="dark" aria-pressed="${getTheme() === 'dark'}">${escapeHtml(t('a11y.theme.dark'))}</button>
              <button type="button" data-theme="light" aria-pressed="${getTheme() === 'light'}">${escapeHtml(t('a11y.theme.light'))}</button>
              <button type="button" data-theme="contrast" aria-pressed="${getTheme() === 'contrast'}">${escapeHtml(t('a11y.theme.contrast'))}</button>
            </div>
          </div>
          <div class="a11y-group">
            <div class="a11y-group-label" id="a11yLayoutLabel">${escapeHtml(t('ui2.preview.label'))}</div>
            <div class="a11y-seg" role="group" aria-labelledby="a11yLayoutLabel" aria-describedby="a11yLayoutHint">
              <button type="button" data-ui2="0" aria-pressed="${!getUi2Preview()}"${canKeepUi2Preview() ? '' : ' disabled'}>${escapeHtml(t('ui2.preview.current'))}</button>
              <button type="button" data-ui2="1" aria-pressed="${getUi2Preview()}"${canKeepUi2Preview() ? '' : ' disabled'}>${escapeHtml(t('ui2.preview.new'))}</button>
            </div>
            <p class="a11y-hint" id="a11yLayoutHint">${escapeHtml(t(canKeepUi2Preview() ? 'ui2.preview.hint' : 'ui2.preview.noStorage'))}</p>
          </div>
        </div>
      </div>
      <div class="app-locale locale-dd" role="group" aria-label="${escapeAttr(t('settings.language'))}">
        ${(() => {
          const cur = AVAILABLE_LOCALES.find(l => l.code === getLocale()) || AVAILABLE_LOCALES[0];
          const curCc = LOCALE_FLAG_CC[cur.code] ?? cur.code.toUpperCase();
          const trigger = `<button type="button" class="locale-dd-trigger" id="localeTrigger" aria-haspopup="listbox" aria-expanded="false" title="${escapeAttr(cur.label)}"><span class="locale-flag-emoji" aria-hidden="true">${localeFlagSvg(cur.code, curCc) || flagFromCC(curCc)}</span><span class="locale-flag-code">${escapeHtml(cur.code.toUpperCase())}</span><span class="locale-dd-caret" aria-hidden="true">&#9662;</span></button>`;
          const items = AVAILABLE_LOCALES.map(l => {
            const cc = LOCALE_FLAG_CC[l.code] ?? l.code.toUpperCase();
            const sel = l.code === getLocale();
            return `<li role="option" class="locale-dd-item${sel ? ' active' : ''}" data-locale="${escapeAttr(l.code)}" aria-selected="${sel ? 'true' : 'false'}"><span class="locale-flag-emoji" aria-hidden="true">${localeFlagSvg(l.code, cc) || flagFromCC(cc)}</span><span class="locale-dd-name">${escapeHtml(l.label)}</span></li>`;
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
    <button class="tab-btn" data-view="multiroom">${escapeHtml(t('nav.multiroom'))}<span class="tab-badge" id="multiroomTabBadge" hidden></span></button>
    <button class="tab-btn" data-view="spotify">${escapeHtml(t('nav.spotify'))}</button>
    <button class="tab-btn" data-view="wishes">${escapeHtml(t('nav.wishes'))}</button>
  </div>
  <div id="globalSecurityBanner" class="global-security-banner hidden">
    <span class="global-security-text">
      <b>${escapeHtml(t('banner.recommendation'))}</b> ${escapeHtml(t('banner.sshRecommend'))}
    </span>
    <button class="btn btn-mini" id="globalSecurityRebootBtn">${escapeHtml(t('speaker.reboot'))}</button>
    <button class="btn btn-secondary btn-mini" id="globalSecurityDismissBtn">${escapeHtml(t('speaker.issueDismiss'))}</button>
  </div>
  <div id="view-box" class="view"></div>
  <div id="view-library" class="view hidden"></div>
  <div id="view-recent" class="view hidden"></div>
  <div id="view-settings" class="view hidden"></div>
  <div id="view-setup" class="view hidden"></div>
  <div id="view-multiroom" class="view hidden"></div>
  <div id="view-spotify" class="view hidden"></div>
  <div id="view-wishes" class="view hidden"></div>

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

  <div class="modal hidden" id="donateModal">
    <div class="modal-content donate-modal">
      <h3>${escapeHtml(t('credits.donate'))}</h3>
      <p class="modal-sub" id="donateModalSlogan"></p>
      <div class="donate-modal-btns" id="donateModalBtns"></div>
      <button class="btn" id="donateClose">${escapeHtml(t('common.close'))}</button>
    </div>
  </div>

  ${shareModalHTML()}

  <div class="modal hidden" id="updateAllOverlay">
    <div class="modal-content ua-modal">
      <h3>${escapeHtml(t('updateAll.title'))}</h3>
      <p class="modal-sub" id="uaSummary"></p>
      <div id="uaList" class="ua-list"></div>
      <div class="warn-buttons">
        <!-- Never disabled. Closing only HIDES this window, it does not cancel
             the run, so disabling it protects nothing and can strand the user:
             the button was greyed while any row had no final outcome, and a
             speaker that drops off the Wi-Fi mid-update never gets one, so the
             window could not be dismissed at all. Reported 2026-08-23 by an
             owner whose speakers were dropping off a congested network. -->
        <button class="btn" id="uaClose">${escapeHtml(t('common.close'))}</button>
      </div>
    </div>
  </div>

  <!-- Shown only while a run is still going and the panel above is hidden.
       Closing that panel does not stop anything, but with nothing left on
       screen "hidden" and "finished" look identical, and the next thing an
       owner does is close the app. -->
  <button class="ua-mini hidden" id="uaMini" type="button"></button>

  <div id="toast" class="toast"></div>

  <footer class="app-footer" id="appFooter"></footer>
`;


// Tabs
document.querySelectorAll('.tab-btn').forEach(btn => {
  btn.onclick = () => switchView(btn.dataset.view);
});
// Keep every tab inside the column at any window width, language and text size.
watchTabs(document.querySelector('.tabs'));

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

// Accessibility menu (text size + theme) in the header. Mirrors the locale
// dropdown's open/close behaviour. Unlike the locale switch these apply
// instantly by toggling classes on <html>, so no page reload is needed.
(function wireA11yPicker() {
  const trigger = document.getElementById('a11yTrigger');
  const menu = document.getElementById('a11yMenu');
  if (!trigger || !menu) return;
  const close = () => { menu.hidden = true; trigger.setAttribute('aria-expanded', 'false'); };
  const open = () => { syncLayoutAvailable(); menu.hidden = false; trigger.setAttribute('aria-expanded', 'true'); };
  trigger.onclick = (e) => { e.stopPropagation(); if (menu.hidden) open(); else close(); };
  const syncPressed = (attr, val) => {
    menu.querySelectorAll(`button[${attr}]`).forEach(b => {
      b.setAttribute('aria-pressed', String(b.getAttribute(attr) === String(val)));
    });
  };
  menu.querySelectorAll('button[data-scale]').forEach(b => {
    b.onclick = () => { const n = Number(b.dataset.scale); setScale(n); syncPressed('data-scale', n); };
  });
  menu.querySelectorAll('button[data-theme]').forEach(b => {
    b.onclick = () => { setTheme(b.dataset.theme); syncPressed('data-theme', b.dataset.theme); };
  });
  // Layout: the current one or the UI 2.0 preview (#1285). The shell is built
  // once at boot, so a change reloads the window, the same as the language
  // switch; text size, theme and everything else stay as they are. Without
  // storage the reload would come back without the choice, so the switch is
  // drawn disabled with a hint, and a write that fails anyway never reloads.
  menu.querySelectorAll('button[data-ui2]').forEach(b => {
    b.onclick = () => {
      const on = b.dataset.ui2 === '1';
      if (on === getUi2Preview()) { syncPressed('data-ui2', b.dataset.ui2); return; }
      if (!setUi2Preview(on)) {
        setUi2Preview(!on);
        layoutWriteFailed = true;
        syncLayoutAvailable();
        return;
      }
      syncPressed('data-ui2', b.dataset.ui2);
      location.reload();
    };
  });
  document.addEventListener('click', (e) => { if (!e.target.closest('.a11y-dd')) close(); });
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
// Since the network install landed, every SoundTouch model runs STR - also the
// ones that never read a USB stick at boot (300, Wave, SA-4/SA-5, CineMate).
// Naming four models here was both wrong and discouraging for owners of the
// others, so the line now says what is true: all of them.
const SUPPORTED_LINE = {
  de: 'für alle SoundTouch Modelle',
  fr: 'pour tous les modèles SoundTouch',
  it: 'per tutti i modelli SoundTouch',
  es: 'para todos los modelos SoundTouch',
  nl: 'voor alle SoundTouch modellen',
  pt: 'para todos os modelos SoundTouch',
  ja: 'すべての SoundTouch モデルに対応',
  uk: 'для всіх моделей SoundTouch',
  pl: 'dla wszystkich modeli SoundTouch',
  lt: 'visiems SoundTouch modeliams',
  lv: 'visiem SoundTouch modeļiem',
  tr: 'tüm SoundTouch modelleri için',
  ar: 'لجميع طُرز SoundTouch',
  en: 'for every SoundTouch model',
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
  ar: 'واصِل استخدام مكبرات صوت Bose SoundTouch دون سحابة Bose.',
  en: 'Keep using your Bose SoundTouch speakers, without the Bose cloud.',
};

(function applyTagline() {
  const lang = getLocale();
  const tEl = $('appTagline');
  if (tEl) tEl.textContent = TAGLINES[lang] || TAGLINES.en;
  const sEl = $('appSupported');
  if (sEl) sEl.textContent = SUPPORTED_LINE[lang] || SUPPORTED_LINE.en;
})();

// --- New-feature discovery dots (Jens 2026-08-31) ------------------------
// A small blue dot on a nav tab points the user to a new STR-exclusive feature
// they have not opened yet; it clears the moment they open that tab. Per viewer
// (localStorage), so once found it stays gone. Add an entry when you ship a new
// exclusive feature and drop it a few releases later once it is no longer "new".
const NEW_FEATURES = [
  { id: '2026-08-permanent-group', view: 'multiroom' },
  { id: '2026-08-copy-presets', view: 'settings' },
  { id: '2026-08-recently-played', view: 'recent' },
];
const SEEN_FEATURES_KEY = 'str.seenFeatures';
function seenFeatureSet() {
  try { return new Set(JSON.parse(localStorage.getItem(SEEN_FEATURES_KEY) || '[]')); }
  catch { return new Set(); }
}
function markViewFeaturesSeen(view) {
  const seen = seenFeatureSet();
  let changed = false;
  for (const f of NEW_FEATURES) {
    if (f.view === view && !seen.has(f.id)) { seen.add(f.id); changed = true; }
  }
  if (changed) { try { localStorage.setItem(SEEN_FEATURES_KEY, JSON.stringify([...seen])); } catch {} }
}
// renderNavDots paints/removes the blue dot on each nav tab from the seen set,
// and on the matching entry of the UI 2.0 sidebar when the preview is on.
// Idempotent, so it is safe to call on every box refresh and every view switch.
function renderNavDots() {
  const seen = seenFeatureSet();
  document.querySelectorAll('.tab-btn, .ui2-nav-item').forEach(btn => {
    const want = NEW_FEATURES.some(f => f.view === btn.dataset.view && !seen.has(f.id));
    const dot = btn.querySelector('.nav-newdot');
    if (want && !dot) {
      const s = document.createElement('span');
      s.className = 'nav-newdot';
      s.setAttribute('aria-label', t('nav.newFeatureDot'));
      s.title = t('nav.newFeatureDot');
      btn.appendChild(s);
    } else if (!want && dot) {
      dot.remove();
    }
  });
}

function switchView(view) {
  const prevView = state.view;
  state.view = view;
  document.querySelectorAll('.tab-btn').forEach(b => {
    b.classList.toggle('active', b.dataset.view === view);
  });
  syncShellNav(view);
  // The UI 2.0 player's volume and transport follow the polling scope (hidden
  // in Setup). Coming back out of Setup into a view other than Music, the
  // slider reads the speaker once, so its first drag starts from the real
  // level and not from the one it showed before Setup.
  syncShellPlayer();
  if (state.ui2Shell && prevView === 'setup' && view !== 'box' && view !== 'setup' && state.currentBox) loadMusicTabVolume();
  // Opening a tab counts as finding its new exclusive features: clear their dots.
  markViewFeaturesSeen(view);
  renderNavDots();
  $('view-box').classList.toggle('hidden', view !== 'box');
  $('view-library').classList.toggle('hidden', view !== 'library');
  $('view-recent').classList.toggle('hidden', view !== 'recent');
  $('view-settings').classList.toggle('hidden', view !== 'settings');
  $('view-setup').classList.toggle('hidden', view !== 'setup');
  $('view-multiroom').classList.toggle('hidden', view !== 'multiroom');
  // The Multiroom view runs an active-only live poll (multiroom.js); stop it the
  // moment we leave so it never keeps polling boxes from behind another tab.
  // Its result notes go with it: an error note is meant to stay until the next
  // action, not to greet the user on every return to the tab (#882, the
  // "stereo pair cannot be grouped" note that survived every screen change).
  if (view !== 'multiroom') { stopMultiroomLive(); resetMultiroomNotes(); }
  $('view-spotify').classList.toggle('hidden', view !== 'spotify');
  $('view-wishes').classList.toggle('hidden', view !== 'wishes');
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
    // A finished install's status lines stayed in the panel for good: leave
    // the tab, come back, and the old "installing..." lines were still there
    // (shorty310, discussion #852). The panel is cleared on every entry unless
    // an install is actually running, whose live status must stay visible.
    if (!installRunActive()) { const r = $('setupResult'); if (r) r.innerHTML = ''; }
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
    updatePandoraModeBtn();
  }
  if (view === 'settings') loadBoxSettings();
  if (view === 'library') openLibrary();
  if (view === 'recent') renderRecent();
  if (view === 'multiroom') renderMultiroom(true);
  if (view === 'spotify') renderSpotifyAlpha();
  if (view === 'wishes') renderWishes();
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
  { name: 'Inter', by: 'The Inter Project Authors', license: 'OFL-1.1', url: 'https://github.com/rsms/inter', role: 'typeface of the track screen on the speaker display' },
  { name: 'Mozilla CA certificate store', by: 'curl project', license: 'MPL-2.0', url: 'https://curl.se/docs/caextract.html', role: 'public certificate authorities, so speakers built with an incomplete list can still reach stations' },
  { name: 'radio-browser.info', license: 'community service', url: 'https://www.radio-browser.info', role: 'radio station directory' },
  { name: 'DuckDuckGo icons', license: 'service', url: 'https://duckduckgo.com', role: 'station logos' },
];

// Projects that contributed KNOWLEDGE rather than code. STR ships none of their
// source, but several of its central mechanisms would not exist without their
// published findings, so they are credited in their own section. `donate` is
// filled in only where the project actually accepts support; most of them do
// not, which is worth seeing.
const RESEARCH_CREDITS = [
  {
    name: 'Bose-SoundTouch', by: 'gesellix',
    url: 'https://github.com/gesellix/Bose-SoundTouch',
    donate: 'https://github.com/sponsors/gesellix',
    role: 'Documented the account and service schema that lets a speaker register its sources again, which is what makes the preset buttons work without the Bose cloud.',
  },
  {
    name: 'SixBack', by: 'Dirk Tostmann',
    url: 'https://github.com/tostmann/SixBack',
    donate: 'https://paypal.me/busware',
    role: 'Showed how a speaker distinguishes a source it does not know from one it cannot log into, which is why STR can now tell those two failures apart.',
  },
  {
    name: 'soundcork', by: 'deborahgu',
    url: 'https://github.com/deborahgu/soundcork',
    role: 'Wrote down the Bose cloud API from real speaker traffic, the reference STR checks its own emulation against.',
  },
  {
    name: 'BoseSoundtouch', by: 'TimoGo',
    url: 'https://github.com/TimoGo/BoseSoundtouch',
    role: 'Documented the speaker command sequence for joining a Wi-Fi network, including the detail the official service manual leaves out.',
  },
  {
    name: 'Soundtouch-without-the-app', by: 'bosefirmware',
    url: 'https://github.com/bosefirmware/Soundtouch-without-the-app',
    role: 'Collected the firmware images and cloud-free operating notes STR uses to tell speaker generations apart.',
  },
  {
    name: 'bosesoundtouchapi', by: 'thlucas1',
    url: 'https://github.com/thlucas1/bosesoundtouchapi',
    role: 'The most complete public description of the speaker’s own control API.',
  },
  {
    name: 'libsoundtouch', by: 'CharlesBlonde',
    url: 'https://github.com/CharlesBlonde/libsoundtouch',
    role: 'The original community library for these speakers, and the first description of their live event stream.',
  },
  {
    name: 'opencloudtouch',
    url: 'https://github.com/opencloudtouch/opencloudtouch',
    donate: 'https://www.buymeacoffee.com/b49rjg5k6vj',
    role: 'A parallel effort to keep these speakers alive after the shutdown, and a useful cross-check on findings.',
  },
];

// showCredits opens the open-source credits dialog from the footer link.
function showCredits() {
  const modal = $('creditsModal');
  const body = $('creditsBody');
  if (!modal || !body) return;
  $('creditsTitle').textContent = t('credits.title');
  $('creditsIntro').textContent = t('credits.intro');
  const row = (c, extra) => `<div class="credit-row">`
    + `<div><a href="#" class="footer-link credit-name" data-url="${escapeAttr(c.url)}">${escapeHtml(c.name)}</a>`
    + (c.by ? ` <span class="credit-by">${escapeHtml(t('credits.by'))} ${escapeHtml(c.by)}</span>` : '')
    + extra + `</div>`
    + `<div class="credit-role">${escapeHtml(c.role)}</div></div>`;
  // STR's own license comes first. The NOTICE asks every work based on STR to
  // show this attribution where it shows its other credits, so STR shows it
  // here itself.
  const repo = 'https://github.com/JRpersonal/streborn/blob/main';
  const own = `<div class="credit-own">`
    + `<div><b>STR (SoundTouch Reborn)</b> <span class="credit-by">&copy; 2026 Jens Roggenfelder</span> <span class="credit-license">Apache-2.0</span></div>`
    + `<div class="credit-role">${escapeHtml(t('credits.ownLicense'))} `
    + `<a href="#" class="footer-link credit-name" data-url="${escapeAttr(repo + '/LICENSE')}">${escapeHtml(t('credits.licenseLink'))}</a> &middot; `
    + `<a href="#" class="footer-link credit-name" data-url="${escapeAttr(repo + '/NOTICE')}">NOTICE</a></div></div>`;
  body.innerHTML = own + OSS_CREDITS.map(c =>
    row(c, ` <span class="credit-license">${escapeHtml(c.license)}</span>`)
  ).join('')
    + `<h3 class="credit-section">${escapeHtml(t('credits.researchTitle'))}</h3>`
    + `<p class="credit-section-intro">${escapeHtml(t('credits.researchIntro'))}</p>`
    + RESEARCH_CREDITS.map(c => row(c, c.donate
      ? ` <a href="#" class="footer-link credit-name credit-donate" data-url="${escapeAttr(c.donate)}">${escapeHtml(t('credits.donate'))}</a>`
      : '')).join('');
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
    state.appInfo = { version: t('common.unknown'), build: '', author: '', githubUrl: '', websiteUrl: '', donateSlogan: '' };
  }
  const i = state.appInfo;
  const links = [];
  if (i.githubUrl)  links.push(`<a href="#" data-url="${escapeAttr(i.githubUrl)}" class="footer-link">GitHub</a>`);
  if (i.websiteUrl) links.push(`<a href="#" data-url="${escapeAttr(i.websiteUrl)}" class="footer-link">${escapeHtml(t('footer.website'))}</a>`);
  // Early in the row on purpose: the row wraps on a narrow window, and this is
  // the one entry whose whole point is to still be reachable there. The donate
  // rail is hidden below 62em, which with a larger text size is a fairly wide
  // window, so without this link somebody who WANTS to donate has nowhere to
  // click (reported 2026-09-09 by a user who had just written that he wanted to
  // buy a coffee).
  links.push(donateFooterLinkHTML());
  // Persistent way to reach the community pin map. The one-time celebration invite
  // auto-dismisses, so users who miss it had no way back and kept asking "where do
  // I add my pin?" (Helmut). This footer link is always available.
  links.push(`<a href="#" id="footerWorldMap" class="footer-link" title="${escapeAttr(t('worldMap.inviteBtn'))}">🌍 ${escapeHtml(t('footer.worldMap'))}</a>`);
  links.push(`<a href="#" id="footerSaveLogs" class="footer-link" title="${escapeAttr(t('footer.saveLogsHint'))}">${escapeHtml(t('footer.saveLogs'))}</a>`);
  links.push(`<a href="#" id="footerCredits" class="footer-link">${escapeHtml(t('footer.credits'))}</a>`);
  // Where to report something. Asked for on 2026-08-06: "maybe the ST Reborn app
  // should have some kind of Support/Help menu item which points the user to
  // github to log issues and send feedback". Until then the only route was
  // knowing the project is on GitHub and finding it there, which a user who
  // installed the app from the website has no reason to know.
  links.push(`<a href="#" id="footerReport" class="footer-link">${escapeHtml(t('footer.reportProblem'))}</a>`);
  // Where the open ideas are voted on. Each backlog item has its own discussion
  // in the Ideas category, sorted by upvotes, so the order of the backlog can
  // follow what people actually want rather than who wrote last.
  links.push(`<a href="#" id="footerVoteIdeas" class="footer-link">${escapeHtml(t('footer.voteIdeas'))}</a>`);
  // Recommend STR: the permanent, quiet way to the share buttons. Deliberately
  // not next to the donate buttons, sharing is not tied to donating.
  links.push(`<a href="#" id="footerShare" class="footer-link">${escapeHtml(t('share.menu'))}</a>`);
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
      <div class="footer-fine">${escapeHtml(t('footer.fine'))}</div>
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
  const donateLink = $('footerDonate');
  if (donateLink) donateLink.onclick = (e) => { e.preventDefault(); showDonate(); };
  // Straight to the issue list rather than the repository front page: a user
  // with a problem wants to see whether it is already reported and to write it
  // down, not to read a README.
  const reportLink = $('footerReport');
  if (reportLink) reportLink.onclick = (e) => { e.preventDefault(); BrowserOpenURL(`${repo}/issues`); };
  const voteLink = $('footerVoteIdeas');
  if (voteLink) voteLink.onclick = (e) => { e.preventDefault(); BrowserOpenURL(IDEAS_VOTE_URL); };
  const shareLink = $('footerShare');
  if (shareLink) shareLink.onclick = (e) => { e.preventDefault(); openShareModal(); };
  const worldMapLink = $('footerWorldMap');
  if (worldMapLink) worldMapLink.onclick = (e) => { e.preventDefault(); try { BrowserOpenURL(worldMapURL()); } catch {} };
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
  // The quiet version line with its "check for updates" link is on screen
  // from the start. It used to appear only once the first scheduled check
  // completed 8 s in, so the control materialized out of nowhere and at
  // least one user never spotted it at all.
  try { renderAppUpdateCheckLink($('appUpdateBanner'), ''); } catch {}
  // Defer the update check out of the critical startup path: the window
  // and discovery come up first, and the network call (a reported suspect
  // for a macOS start crash) only fires once the app is already running,
  // so even if it ever misbehaved it cannot abort startup. checkAppUpdate
  // is itself fully guarded (try/catch + Go-side recover).
  setTimeout(() => { try { checkAppUpdate(); } catch {} }, 8000);
  // Once after an update moved the app to STR-Windows.exe: Windows asks for
  // network access again for the new path, and a dismissed prompt leaves the
  // speaker list empty. Shown a moment after start so the firewall prompt and
  // the window are both up.
  setTimeout(() => { maybeShowStableNameNotice({ consume: ConsumeStableNameNotice, showToast, t }).catch(() => {}); }, 2500);
  // Whether Windows Firewall blocks the app. The backend checks once at start-up
  // and caches it; this reads that result (waiting for it if it is still
  // running). A no-op answer on macOS and Linux.
  setTimeout(() => { checkFirewallBanner(false); }, 3000);
  // An install that waits for the speaker's callback and gets none (setup.js)
  // is the firewall's other footprint: look again once.
  window.addEventListener('str:firewall-suspect', () => {
    firewallInstallSuspect = true;
    if (firewallSuspect('install')) checkFirewallBanner(true);
  });
  // A winget upgrade started by the previous run that failed: remembered before
  // the update check (8 s) so its banner carries the note and the command.
  ConsumeWingetUpgradeFailure().then((f) => {
    const v = wingetFailureView(f, t);
    if (v) { state.wingetFailure = v; showToast(v.text + ' ' + v.command, 20000); }
  }).catch(() => {});
  // Long-running apps re-check every 12 hours (#71): STR often stays open for
  // days on a media PC, and the startup-only check meant such installs never
  // learned about a new release (and its security fixes) until a restart. The
  // banner render is idempotent, so a repeat check just refreshes it.
  setInterval(() => { try { checkAppUpdate(); } catch {} }, 12 * 3600 * 1000);
  // Advance the track progress once a second between speaker polls, so the bar
  // moves like a clock instead of stepping whenever the status poll lands.
  // Right after a track change the length is not known yet, and the status
  // cadence (5 s, 15 s outside the speaker view) made the bar wait that long
  // to appear. For a short window the tick asks the speaker itself (#845).
  setInterval(() => {
    try {
      const playing = state.nowPlayState === 'PLAY_STATE' || state.nowPlayState === 'BUFFERING_STATE';
      if (wantsFastPoll(trackPos, playing, Date.now())) pollTrackPosition();
      renderTrackProgress();
    } catch {}
  }, 1000);
  // appInfo may have arrived after the first discovery completed; the
  // badge function defers until both are known. Re-render the box list
  // too so the per-speaker update dot (boxNeedsUpdate) appears once the
  // app version is finally known.
  updateSettingsTabBadge();
  if (state.boxes.length) renderBoxSelect();
  // Same race, and it was costing the user the better part of a minute right
  // after they had updated the app, which is exactly when the speakers are
  // behind and the prompt matters most.
  //
  // Both prompts need two facts: the app's version (this function, an await)
  // and the speakers' versions (a discovery pass). They were only ever
  // re-evaluated at the END of a discovery pass, so whenever appInfo lost that
  // race the card was skipped and nothing looked at it again until the NEXT
  // pass happened along. The delay was never a timer; it was a second sweep.
  // Now that both facts are in hand, ask immediately. Both are idempotent and
  // cheap, and both hide themselves when nothing is behind.
  if (state.boxes.length) {
    try { maybeShowSpeakerUpdateCard(); } catch {}
    try { checkBoxUpdate(); } catch {}
  }
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

  side.innerHTML = `
    <div class="donate-icon">&#9749;</div>
    <div class="donate-slogan">${escapeHtml(slogan)}</div>
    ${donateButtonsHTML()}
  `;

  wireDonateButtons(side);
}

// showDonate opens the same three buttons as a dialog, from the footer link.
//
// The rail they normally live in is an overlay that needs a wide window: it is
// display:none below 62em, and because that threshold is in em it moves out
// with the user's text size, so a narrow window or a large font hides it
// entirely. The CSS comment beside that rule promised "below that the footer
// donate link carries it" and there was no such link, which is how a user who
// had gone looking in order to actually pay reported the coffee button as
// missing (mail, 2026-09-09). The footer is always on screen, so this is the
// path that cannot disappear.
function showDonate() {
  const modal = $('donateModal');
  const btns = $('donateModalBtns');
  if (!modal || !btns) return;
  const i = state.appInfo || {};
  const slogan = $('donateModalSlogan');
  if (slogan) slogan.textContent = i.donateSlogan || t('footer.donateSlogan');
  btns.innerHTML = donateButtonsHTML();
  wireDonateButtons(btns);
  const close = () => modal.classList.add('hidden');
  const closeBtn = $('donateClose');
  if (closeBtn) closeBtn.onclick = close;
  modal.onclick = (e) => { if (e.target === modal) close(); };
  modal.classList.remove('hidden');
}

// renderAppUpdateCheckLink leaves a quiet way back to the update notice in the
// place the banner would occupy.
//
// Dismissing the notice used to be one-way. The version check ran 8 s after
// start and then every twelve hours, and a dismissal was remembered for that
// version, so somebody who clicked it away, or who went back to an older
// build, had nothing left to press and no way to be offered the update again
// (discussion #597: "How do I force it to check for updates?"). A manual
// check also ignores the stored dismissal, because a person pressing this has
// just answered the question the dismissal was standing in for.
function renderAppUpdateCheckLink(banner, note) {
  if (!banner) return;
  banner.classList.add('app-update-quiet');
  banner.classList.remove('hidden');
  // Name the version next to the link. On its own the link was a stray line
  // above everything else with nothing to explain why it was there; with the
  // installed version in front of it, the line says what it is about.
  const cur = (state.appInfo && state.appInfo.version) ? String(state.appInfo.version) : '';
  banner.innerHTML = (cur ? `<span class="app-update-quiet-ver">${escapeHtml(t('setup.versionLabel', { version: cur }))}</span>` : '')
    + `<a href="#" id="appUpdateCheckLink" class="footer-link">${escapeHtml(t('banner.checkForAppUpdate'))}</a>`
    + (note ? `<span class="app-update-quiet-note">${escapeHtml(note)}</span>` : '');
  const link = $('appUpdateCheckLink');
  if (link) link.onclick = (e) => {
    e.preventDefault();
    link.textContent = t('banner.checkingForAppUpdate');
    try { clearNoticeDismissal('appUpdate'); } catch {}
    checkAppUpdate(true);
  };
}

async function checkAppUpdate(manual) {
  // Entirely best-effort: an unreachable endpoint or a garbage payload
  // must never break the UI. Validate every field defensively and stay
  // silent on any failure.
  const banner = $('appUpdateBanner');
  try {
    const m = await CheckAppUpdate();
    // The question was reachable, whatever the answer. That is what the run
    // counts, not whether an update exists.
    noteCheckSucceeded();
    if (!m || typeof m !== 'object' || typeof m.version !== 'string' || !m.version) {
      // Nothing newer. Say so when the user asked; otherwise just leave the
      // way back on screen.
      renderAppUpdateCheckLink(banner, manual ? t('banner.appIsCurrent') : '');
      return;
    }
    if (!banner) return;
    // An app update outranks the speaker updates and hides their prompts (see
    // speakerUpdateCardMuted). Set before the dismissal check: the ORDER
    // holds either way, and a user who clicked the app notice away has not
    // stopped needing the app first.
    state.appUpdateVersion = m.version;
    // This check is async and often resolves after discovery already showed
    // the speaker card or the Speaker Settings banner. Re-evaluate both now so
    // they follow the app-first rule instead of sitting under the app notice
    // as a second update prompt (#1154).
    maybeShowSpeakerUpdateCard();
    checkBoxUpdate();
    if (noticeDismissed('appUpdate', m.version)) {
      renderAppUpdateCheckLink(banner, '');
      return;
    }
    banner.classList.remove('app-update-quiet');
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
    // /releases/latest always resolves to the newest PUBLISHED release, so it
    // never 404s on a draft/just-published tag (the "page not found" a user hit).
    const latestUrl = `${repo}/releases/latest`;
    const notesUrl = (typeof m.notesUrl === 'string' && /^https?:\/\//i.test(m.notesUrl))
      ? m.notesUrl
      : latestUrl;
    // The banner itself only shows for a genuinely newer version (CheckAppUpdate
    // returns nothing otherwise), and the download button only when the manifest
    // carries a real download URL. The button is a primary button so it stands
    // out in the notice instead of reading as a faint secondary control.
    // In-app update (#71): download the matching asset, verify its SHA256, then
    // install. Linux/Windows self-replace and relaunch; macOS downloads+verifies
    // and opens the .dmg (replacing a running .app bundle is unwritten work, not
    // a Gatekeeper problem: the build is notarized). The button
    // always shows now: the asset URL + hash are resolved from the release
    // manifest in the backend, so it no longer depends on the manifest carrying a
    // downloadUrl. notesUrl / releases page stays as the manual fallback.
    const isMacOS = /Mac|iPhone|iPad|iPod/i.test(navigator.platform || navigator.userAgent || '');
    // Label makes the target unambiguous: this updates THE APP itself, not the
    // speaker (a non-technical user clicked the speaker-update expecting the app
    // to update, and ended up with two .exe copies downloaded by hand).
    // A copy winget installed updates through winget (see wingetupdate.js), and
    // the button says so before it is pressed.
    const viaWinget = !!(state.appInfo && state.appInfo.installedViaWinget);
    const installLabel = viaWinget
      ? wingetInstallLabel(t)
      : (isMacOS ? t('banner.downloadAppUpdate') : t('banner.installAppNow'));
    banner.innerHTML = `
      <div class="app-update-text"><span class="app-update-icon" aria-hidden="true">&#8593;</span><span><b>${escapeHtml(t('banner.appUpdateAvail'))}</b> ${escapeHtml(m.version)} &middot; <a href="#" id="appUpdateNotes" class="footer-link">${escapeHtml(t('banner.whatsNew'))}</a></span></div>
      <button class="btn btn-primary app-update-btn" id="appUpdateBtn">${escapeHtml(installLabel)}</button>
      <button class="banner-close" id="appUpdateDismiss" aria-label="${escapeAttr(t('banner.dismiss'))}" title="${escapeAttr(t('banner.dismissTitle'))}">&times;</button>
    `;
    banner.classList.remove('hidden');
    // The winget upgrade the previous run started came back with an error:
    // say so above the button, with the command to run by hand.
    if (viaWinget && state.wingetFailure) {
      banner.insertAdjacentHTML('afterbegin', wingetNoteHTML(state.wingetFailure));
      wireWingetCopy(banner, state.wingetFailure.command);
    }
    // There is no Windows installer: the release is a bare exe (versioned
    // STR-Windows-vX.Y.Z.exe up to v1.0.1, STR-Windows.exe since), so updating
    // by hand means downloading a second file into the same folder and
    // nothing replaces anything. The Go side reads a sibling's version from
    // its Windows version resource, the old name being only the fallback. A reporter spent
    // three rounds of mail on that (2026-10-01), downloading the new version
    // again and again while starting the old one, until he deleted the old
    // file by hand. If the newer file is already sitting next to this one,
    // say so, because downloading it a fourth time will not help.
    //
    // The hint is added, and the Install button STAYS. It used to be hidden,
    // which made a file name the only thing standing between somebody and
    // their update: for files published up to v1.0.1 the name was the only
    // evidence about a sibling, a person may rename these files for their own
    // reasons (one does, to see the version on their desktop), and a file
    // called STR-Windows-v9.9.9.exe that is actually ancient would have left
    // the banner pointing at it with no way forward. The update check itself reads the version compiled into
    // the running build, so it is never fooled; only this hint can be, so this
    // hint may inform and must not block.
    NewerCopyNextToThisOne().then((file) => {
      if (!file) return;
      const line = document.createElement('div');
      line.className = 'app-update-text';
      line.textContent = t('banner.appUpdateAlreadyHere', { file });
      banner.insertBefore(line, banner.firstChild);
    }).catch(() => {});
    const notesLink = $('appUpdateNotes');
    if (notesLink) notesLink.onclick = (e) => { e.preventDefault(); BrowserOpenURL(notesUrl); };
    const dl = $('appUpdateBtn');
    if (dl) dl.onclick = viaWinget
      ? () => runWingetUpdate(m.version, dl, installLabel)
      : () => runAppUpdate(m.version, dl, installLabel, isMacOS, dlUrl || latestUrl);
    // Clicking it away is allowed and remembered, for THIS version only. The
    // next release is news again and says so; the user dismisses that one in
    // turn or installs it.
    const x = $('appUpdateDismiss');
    if (x) x.onclick = () => {
      dismissNotice('appUpdate', m.version);
      // Not hidden: the quiet link stays so the notice can be brought back.
      renderAppUpdateCheckLink(banner, '');
      // The speaker prompts stay muted: the app is still the thing to do first.
      // They return once the app is current.
      maybeShowSpeakerUpdateCard();
    };
  } catch (e) {
    try { console.warn('checkAppUpdate failed', e); } catch {}
    // A check that could not run must not swallow the control that triggers
    // it, and a person who pressed it is owed the failure: with the link
    // quietly resetting, a machine on which the request always fails (AV
    // proxy, firewall, DNS) looks exactly like "no update exists", and one
    // user pressed through several releases believing that.
    try { renderAppUpdateCheckLink(banner, manual ? t('banner.appCheckFailed') : ''); } catch {}
    // A silent automatic check that ALWAYS fails looks exactly like "you are up
    // to date", forever. One user sat on a June build until the end of
    // September, 142 releases behind, and nothing on his screen was ever about
    // the app itself. After a run long enough to rule out a bad afternoon, say
    // it once, quietly, with the way out beside it.
    if (!manual) {
      try {
        if (shouldSayChecksAreFailing(noteCheckFailed())) {
          showToast(t('banner.appCheckKeepsFailing'));
        }
      } catch { /* never let the notice break the startup */ }
    }
  }
}

// runAppUpdate downloads + verifies the new version and installs it (#71). On
// Linux/Windows the backend replaces the running binary and relaunches, so the
// app quits mid-call and the code after ApplyUpdate only runs on macOS (assisted:
// the verified .dmg is opened for the user to drag into Applications). On any
// failure the button becomes a "download from the website" fallback so the user
// is never stuck.
// downloadWasInterrupted separates "the connection went away" from a genuine
// failure of the update itself.
//
// Everything here is a transport fault: the machine lost its network, the
// connection was cut, the server never answered, or the download stopped short.
// None of them says anything is wrong with the app, the release or the file, so
// none of them earns an error the user is invited to copy and send on. A hash
// mismatch or a file that cannot be written is the opposite and is left to the
// full error path.
//
// Matching on the message rather than a typed error because these arrive from
// the Go side already flattened into a string.
export function downloadWasInterrupted(err) {
  const s = String(err || '').toLowerCase();
  return [
    'network is unreachable', 'unreachable network', // the machine has no route
    'no route to host', 'no such host', 'dns',        // gateway or resolver gone
    'connection reset', 'connection refused', 'broken pipe',
    'eof', 'unexpected eof',                          // cut mid-transfer
    'timeout', 'deadline exceeded', 'timed out',
    'canceled', 'cancelled', 'aborted',
    'i/o error', 'connection was aborted',
  ].some((m) => s.includes(m));
}

// fmtRate turns a bytes/second number into a short human rate for the live
// download/upload throughput shown during an app update or a speaker update.
function fmtRate(bps) {
  bps = Number(bps) || 0;
  if (bps >= 1024 * 1024) return (bps / 1048576).toFixed(1) + ' MB/s';
  if (bps >= 1024) return Math.round(bps / 1024) + ' KB/s';
  return Math.max(0, Math.round(bps)) + ' B/s';
}

// showMacHandoff turns the update banner into the standing instruction for the
// one step macOS leaves to the user: drag the new app out of the mounted .dmg.
//
// It replaces the banner rather than adding a second notice, because at this
// point the "install now" button has done everything it can and re-pressing it
// only downloads the same file again. The button here is short on purpose (Jens'
// rule: a button carries a few words, never a status message) and re-opens the
// downloaded file for anyone who closed the Finder window before reading it.
//
// Nothing dismisses this by itself. The banner is rebuilt from scratch by the
// next update check, which finds the app already current once the user has
// dragged it across, so the instruction disappears exactly when it is obsolete.
function showMacHandoff(path) {
  const banner = $('appUpdateBanner');
  if (!banner) { showToast(t('banner.macDownloaded')); return; }
  banner.innerHTML = `
    <div class="app-update-text"><span class="app-update-icon" aria-hidden="true">&#10003;</span><span><b>${escapeHtml(t('banner.macReadyTitle'))}</b> ${escapeHtml(t('banner.macDownloaded'))}</span></div>
    <button class="btn btn-primary app-update-btn" id="appUpdateReveal">${escapeHtml(t('banner.macShowFile'))}</button>
  `;
  banner.classList.remove('hidden');
  const rv = $('appUpdateReveal');
  if (rv && path) rv.onclick = () => { RevealUpdateFile(path).catch(() => {}); };
}

function showDebHandoff() {
  const banner = $('appUpdateBanner');
  if (!banner) { showToast(t('banner.debHandoff'), 12000); return; }
  banner.innerHTML = `
    <div class="app-update-text"><span class="app-update-icon" aria-hidden="true">&#10003;</span><span>${escapeHtml(t('banner.debHandoff'))}</span></div>
  `;
  banner.classList.remove('hidden');
}

// wingetNoteHTML renders a winget note for the update banner: the text and,
// when there is one, the command to run by hand with a Copy button.
function wingetNoteHTML(view) {
  const cmd = view.command
    ? `<code class="app-update-cmd">${escapeHtml(view.command)}</code><button class="btn app-update-copy" data-winget-copy>${escapeHtml(t('banner.wingetCopy'))}</button>`
    : '';
  return `<div class="app-update-text app-update-winget"><span>${escapeHtml(view.text)}</span>${cmd}</div>`;
}

function wireWingetCopy(root, command) {
  const b = root.querySelector('[data-winget-copy]');
  if (b && command) b.onclick = () => { ClipboardSetText(command).catch(() => {}); };
}

// runWingetUpdate is runAppUpdate for a copy winget installed. Nothing is
// downloaded here: the backend asks winget whether it offers the version,
// starts `winget upgrade` in a hidden helper and quits, and the helper starts
// the app again when winget is done. When it cannot, the banner shows the
// command to run by hand.
async function runWingetUpdate(version, btn, installLabel) {
  btn.disabled = true;
  btn.textContent = t('banner.connecting');
  try {
    const res = await UpdateViaWinget(version);
    const view = wingetOutcomeView(res, version, t);
    const banner = $('appUpdateBanner');
    if (!banner) { showToast(view.text, 15000); return; }
    banner.innerHTML = wingetNoteHTML(view);
    banner.classList.remove('hidden');
    wireWingetCopy(banner, view.command);
  } catch (e) {
    btn.disabled = false;
    btn.textContent = installLabel;
    showError(t('banner.updateFailed', { err: String(e) }));
  }
}

async function runAppUpdate(version, btn, installLabel, isMacOS, fallbackUrl) {
  btn.disabled = true;
  const off = EventsOn('app:update:progress', (p) => {
    const pct = (p && typeof p === 'object') ? p.pct : p;
    const rate = (p && typeof p === 'object' && p.bytesPerSec) ? ' (' + fmtRate(p.bytesPerSec) + ')' : '';
    btn.textContent = t('banner.downloadingPct', { pct }) + rate;
  });
  try {
    // Not "Downloading 0%". Nothing is downloading yet: the release
    // manifest still has to be looked up and the connection opened, which is
    // a minute of legitimate waiting on a slow link, and the first progress
    // event only fires once body bytes arrive. Showing 0% for all of it made
    // a reporter watch a frozen percentage and take a diagnostic (#935).
    btn.textContent = t('banner.connecting');
    const path = await DownloadUpdate(version);
    btn.textContent = t('banner.installing');
    await ApplyUpdate(path);
    // Reached only on macOS and for a Linux .deb copy (Linux/Windows otherwise
    // relaunch+quit inside ApplyUpdate).
    //
    // The last step is the user's: drag the app out of the mounted .dmg. Saying
    // that in a toast did not work, because ApplyUpdate opens the .dmg and
    // Finder's window comes up over ours, exactly where the toast sits, and the
    // toast is gone by the time the user looks back. Reported 2026-08-06 with a
    // screenshot showing the Finder window on top of it: "I feel that this
    // notice is easy to miss". So the instruction takes over the update banner
    // and stays there until the new version is actually running.
    if (isMacOS) {
      btn.disabled = false;
      btn.textContent = installLabel;
      showMacHandoff(path);
    } else if (state.appInfo && state.appInfo.linuxPackage === 'deb') {
      // A .deb copy: ApplyUpdate opened the package installer, which does the
      // rest. The user confirms there and starts the new version.
      showDebHandoff();
    }
  } catch (e) {
    btn.disabled = false;
    // A download the network cut off is not a fault anybody needs to report,
    // and it is not a reason to send the user to a web page either: the same
    // button, pressed again once the connection is back, is the whole fix.
    // Reported by shorty310 (#916) after pulling his Wi-Fi mid-download: he got
    // the copyable error modal, which reads as "send this to the developer",
    // and a button that had turned into "Get it from the downloads page".
    if (downloadWasInterrupted(e)) {
      btn.textContent = t('banner.retryAppUpdate');
      btn.onclick = () => runAppUpdate(version, btn, installLabel, isMacOS, fallbackUrl);
      showToast(t('banner.updateInterrupted'), 9000);
    } else {
      // A real failure: a hash that did not match, a file that could not be
      // written. That one IS worth showing in full and worth the web fallback.
      showError(t('banner.updateFailed', { err: String(e) }));
      btn.textContent = t('banner.getFromReleases');
      if (fallbackUrl) btn.onclick = () => BrowserOpenURL(fallbackUrl);
      // Reassure the non-technical user: the app replaces itself, so any .exe
      // they downloaded by hand earlier can simply be deleted (the
      // duplicate-copies confusion that prompted this).
      showToast(t('banner.manualHint'));
    }
  } finally {
    if (typeof off === 'function') off();
  }
}

// ---------- Music view ----------
// The #view-box markup and its static wiring used to be assigned right here;
// they now live in views/music/ and are mounted at this same point in the boot
// order (after the skeleton above, before the global security banner handlers
// below, which read elements the skeleton owns).
mountMusicView();
// UI 2.0 preview: the new shell is built around the views just mounted, only
// when the preview is switched on (Aa menu > Layout). Off, nothing here runs.
if (getUi2Preview()) safeMountShell2();

// safeMountShell2 builds the UI 2.0 preview shell (shell2/mount.js) and hands
// it what it needs from this file. A failure leaves the current layout as it
// was: the mount rolls itself back and never throws into the boot.
// syncLayoutPressed marks the Layout button of the Aa menu that matches the
// stored preview setting.
function syncLayoutPressed() {
  const on = getUi2Preview();
  document.querySelectorAll('#a11yMenu button[data-ui2]').forEach((b) => {
    b.setAttribute('aria-pressed', String((b.dataset.ui2 === '1') === on));
  });
}

// syncLayoutAvailable enables the Layout switch of the Aa menu only while the
// choice can be stored, and swaps its hint to say why when it cannot. Asked
// again on every menu open: a store the WebView restores late comes back. A
// store that reads but refused a write stays off for the session.
let layoutWriteFailed = false;
function syncLayoutAvailable() {
  const ok = !layoutWriteFailed && canKeepUi2Preview();
  document.querySelectorAll('#a11yMenu button[data-ui2]').forEach((b) => { b.disabled = !ok; });
  const hint = document.getElementById('a11yLayoutHint');
  if (hint) hint.textContent = t(ok ? 'ui2.preview.hint' : 'ui2.preview.noStorage');
}

function safeMountShell2() {
  try {
    const ok = mountShell2({
      switchView,
      pickBox: pickBoxFromShell,
      boxNeedsUpdate,
      boxCannotReachInternet,
      getBoxLabel,
      discoverBoxes,
      offlineTitle,
      pairName: (pair) => pairDisplayName(pair, renderBoxSelect),
      toggleGroupMember,
      groupEditPendingHosts,
    });
    if (!ok) return;
    // The Aa menu's Layout switch was drawn before a late mount (storage not
    // yet restored at boot): make it say what is on screen now.
    syncLayoutPressed();
    updateSettingsTabBadge();
    updateMultiroomTabBadge();
    renderNavDots();
  } catch (err) {
    try { console.warn('UI 2.0 preview: mount failed', err); } catch {}
  }
}

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
// Dismiss the SSH reminder for the current speaker. The user has seen the
// "remove the stick" hint and chooses not to be reminded again on this box
// (persisted per speaker, like the conflict/no-Wi-Fi banners). #381/#385.
const gsd = $('globalSecurityDismissBtn');
if (gsd) gsd.onclick = () => {
  const box = state.currentBox || (state.boxes && state.boxes[0]);
  if (box) { try { localStorage.setItem(warnDismissKey(box, 'ssh'), String(Date.now())); } catch {} }
  const gb = $('globalSecurityBanner');
  if (gb) gb.classList.add('hidden');
};

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
  // Everything below ends in exactly one write of the banner's visibility,
  // including the failure paths. A verdict left standing because the speaker
  // could not be asked is the bug this had: the banner says "this speaker", so
  // a stale yes becomes a false alarm about whichever speaker is selected now,
  // and it asks the user to reboot one for no reason (Jens, 2026-10-02: the Bad
  // speaker, whose agent answered running:false with port 22 refusing).
  let reachable = false;
  let data = null;
  try {
    const r = await boxFetch(box, '/api/stick/status');
    if (r.ok) {
      data = await r.json();
      reachable = true;
    }
  } catch {
    // Asleep, unreachable, or a reply that was not JSON. Nothing is known, so
    // nothing is claimed: reachable stays false and the banner comes down.
  }
  // A speaker switch while the request was in flight would otherwise paint the
  // PREVIOUS speaker's answer under the new speaker's name, the same race the
  // volume sync guards against.
  if (!sameBoxIdentity(state.currentBox, box)) return;
  // The banner is a "remove the stick now that setup is done, otherwise SSH
  // stays open" reminder. As of the pre-1.0 hardening run.sh no longer
  // force-opens sshd on every boot; SSH is open only because a stick is in (the
  // stick opens sshd via its remote_services marker), and a stickless reboot
  // closes it. So sshOpen is an accurate, self-clearing signal, and keying on it
  // (not data.mounted) also covers the Portable, where the stick is in but never
  // auto-mounts so mounted=false (Jens, 2026-06-17). The old mounted-based gate
  // was a workaround from when sshd was always up (#11). Setup view, the OTA
  // window, the persistent-marker case and the per-speaker dismissal are all
  // handled above or inside sshBannerShow, which carries their reasoning.
  const show = sshBannerShow({ reachable, status: data, dismissed: warnDismissed(box, 'ssh') });
  gb.classList.toggle('hidden', !show);
}

// emptyStateOnScreen reports whether the "no speaker found" card is the thing
// currently in the speaker panel. Its manual connect-by-IP field exists only
// inside that card, so its presence is the cheapest honest test.
function emptyStateOnScreen() {
  return !!document.getElementById('emptyIpInput');
}

async function discoverBoxes() {
  const hadBoxes = state.boxes.length > 0;
  if (!hadBoxes && !emptyStateOnScreen() && !manualIpInputBusy()) {
    // The FIRST search of a session, with nothing on screen yet: an explicit
    // message, so the user understands the app is doing something.
    //
    // Not while the manual connect-by-IP field is in use either: a repeat
    // sweep replacing the selector destroyed the input mid-typing, which made
    // the manual fallback unusable exactly when it is needed.
    $('boxSelect').textContent = t('speaker.searching');
  } else {
    // Every repeat, with speakers or without: the refresh icon spins and
    // whatever is on screen stays put.
    //
    // The empty case used to blank the card back to the one-line text on every
    // sweep, and a reporter watching an empty LAN saw the window toggle
    // between two screens ten times in about a minute (#935). Since the minute
    // timer began sweeping the empty list it would do that for as long as
    // nothing is found, which is the whole time somebody is waiting.
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
    if (firewallSpeakersFound !== (list || []).length > 0) {
      firewallSpeakersFound = (list || []).length > 0;
      renderFirewallBanner();
    }
    // An empty speaker list is what a firewall block looks like from in here.
    // Look at the firewall rules again, once per session, not on every sweep.
    if (!(list || []).length && firewallSuspect('empty')) checkFirewallBanner(true);
    // Recovery burst: if we had speakers and this cycle found NONE, the LAN most
    // likely re-IP'd every box at once (router restart, or a LAN<->Wi-Fi / band
    // switch). Re-sweep on a short burst so the list comes back on its own instead
    // of the user staring at an empty picker and hitting Refresh.
    updateRecoveryBurst(hadBoxes, (list || []).length);
    // Auto retry: if a recently set up speaker has not yet re-announced its
    // new name via mDNS, search again every 4 s (driven by pendingNames).
    scheduleNextAutoRefresh();
  } catch (e) {
    // Do NOT overwrite the picker with the raw exception. That took the retry
    // button and the connect-by-IP field away with it, so a blocked first
    // discovery left nothing to press. Repaint the picker and put the
    // exception underneath, folded away, where it helps a bug report without
    // being the whole screen.
    if (!hadBoxes) {
      try { renderBoxSelect(); } catch { /* keep whatever is on screen */ }
      const hint = $('boxHint');
      if (hint) {
        hint.innerHTML = `<p>${escapeHtml(t('speaker.choose'))}</p>`
          + `<details class="muted small"><summary>${escapeHtml(t('common.error'))}</summary>`
          + `<div>${escapeHtml(String(e))}</div></details>`;
        hint.classList.remove('hidden');
      }
    }
  } finally {
    const rb = $('refreshBtn');
    if (rb) rb.classList.remove('spinning');
    // Failed or not, the first sweep is over: from here an empty list counts.
    if (!firewallFirstSweepDone) {
      firewallFirstSweepDone = true;
      renderFirewallBanner();
    }
  }
}

// Periodic background refresh (2026-08-20): the app used to learn a speaker's
// new agent version only when the user pressed Refresh, so an app left open
// all day kept showing "needs update" for speakers that were long done. Every
// minute, re-probe the KNOWN speakers directly (no mDNS sweep, the same quick
// path the Refresh button uses first) and re-evaluate the update banner.
// Skipped while an OTA or an install runs so the probe never lands on a
// speaker mid-flash.
//
// The empty list gets a full sweep instead of the known-box probe, because
// there are no known boxes to probe. It used to be skipped altogether, on the
// grounds that the recovery burst owned the empty case, and the burst declines
// it: it only starts when speakers were there and vanished (`hadBoxesBefore`),
// deliberately, so an empty LAN is never swept every six seconds. Between the
// two, a session that never saw a speaker had nobody watching for one: start
// the app with the Wi-Fi off and the picker still said "No speaker found" long
// after the network was back, until Refresh was pressed by hand (#935).
//
// Once a minute is the right cadence for it. Somebody staring at an empty
// picker is waiting for exactly this, and it costs one sweep a minute for as
// long as the app has nothing to show, which is the same situation in which
// the app is doing nothing else at all.
setInterval(async () => {
  if (state.otaInProgress || installRunActive()) return;
  if (!state.boxes.length) {
    try {
      await discoverBoxes();
    } catch { /* the next tick retries */ }
    return;
  }
  try {
    const quick = await RefreshKnownBoxes();
    if (quick && quick.length) {
      applyBoxList(quick);
      checkBoxUpdate();
    }
  } catch { /* transient network trouble: the next tick retries */ }
}, 60000);

// While the music tab is on screen, keep its group frames and the group
// volume in step with what the speakers do on their own. A permanent group
// re-forms on the master's play without the app being told, so the members
// were already playing while the group volume only appeared after a manual
// refresh (Jens, 2026-09-06). Same cadence as the phone page, only while the
// window is visible, and the zone poll keeps its own 8 s debounce.
// The UI 2.0 preview lists every speaker in its sidebar on every view, so with
// the preview on the poll also runs off the Music tab, but only where the
// persistent player is on screen (not in Setup, not during a speaker update):
// the same cost as sitting on the Music tab, which is where the app opens.
setInterval(() => {
  if ((state.view !== 'box' && !shellListsSpeakers()) || document.visibilityState !== 'visible') return;
  refreshEverySpeaker();
}, 15000);

// refreshEverySpeaker is one round of the every-speaker poll (zones and who is
// playing), for the interval above. Never while
// a speaker update or an install runs: nothing should poll a speaker that is
// about to restart.
function refreshEverySpeaker() {
  if (state.otaInProgress || installRunActive() || !state.boxes.length) return;
  refreshMusicZones();
  refreshBoxPlaying();
}

// applyBoxList folds a freshly probed box list into state + the UI. Shared by
// the known-first quick refresh and the full discovery so both render
// identically (current-box re-bind, speaker select, badges, setup picker).
function applyBoxList(list) {
  state.boxes = applyPendingNames(list || []);
  // A round of a display game finished since the last look: tell the player
  // when it set a new highscore (arcadeshare.js).
  announceHighscores(state.boxes, {
    getArcade: GetArcade, popup: openHighscorePopup, storage: window.localStorage,
    nowSec: Math.floor(Date.now() / 1000),
  }).catch(() => {});
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
  // Speaker Settings shows whatever record it was handed when the gear was
  // clicked. Re-point it at this cycle's record for the same speaker, so a box
  // that turned stock (STR removed, agent gone) or changed IP is shown as it
  // is now, not as it was at click time.
  if (state.settingsBox && state.settingsBox.deviceID) {
    const freshSettings = state.boxes.find(b => b.deviceID === state.settingsBox.deviceID);
    if (freshSettings) state.settingsBox = freshSettings;
  }
  if (state.currentBox && state.currentBox.deviceID) {
    const fresh = state.boxes.find(b => b.deviceID === state.currentBox.deviceID);
    // A selected speaker whose record turned stock cannot be controlled any
    // more (STR was removed from it, or its agent is gone for good): treat it
    // like a speaker that left the list. Transient stock sightings never reach
    // here as stock (the cache promotes them), so this is a real change.
    if (fresh && fresh.kind !== 'stock') {
      // A port-only change (a speaker that answers on both :8888 and :17008)
      // just re-points the record: what plays did not change, so resetting the
      // now-playing state here made the view jump and the progress bar vanish
      // once a minute (#1190).
      const changed = boxRecordChange(state.currentBox, fresh) === 'changed';
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
      refreshSourceButtons();
      // Each discovery cycle re-checks the Pandora entry once the cached
      // answer has run out, so a speaker that was not ready (or did not
      // answer) at startup brings the entry back on its own (#1388).
      updatePandoraModeBtn(changed);
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
  // Mark which speakers are currently playing (small speaker icon on their tile).
  refreshBoxPlaying();
  updateSettingsTabBadge();
  // The group count depends on the box list too (a stored permanent group is
  // read off its master's record), so recount on every list refresh; the zone
  // poll above recounts again once its round lands.
  updateMultiroomTabBadge();
  // localStorage is reliably restored by this first box refresh (see below), so
  // paint the new-feature discovery dots here; a module-load call runs too early.
  // The UI 2.0 preview switch is read again for the same reason: a boot-time
  // read that found the storage not yet restored gets its second chance here.
  if (getUi2Preview() && !shell2Mounted()) safeMountShell2();
  renderNavDots();
  // Re-evaluate the Favorites entry on every box-list refresh. This is the first
  // point after boot where localStorage is reliably restored in the WebView, so
  // a favorites list saved in a previous session reliably brings the button back
  // even if the one-shot init call ran before storage was ready (#Dieter).
  updateFavModeBtn();
  // Favourites removed on a speaker's phone page come off this app's list too
  // (#1389), so the next star set here does not bring them back.
  syncFavRemovals();
  // Setup-tab target picker reuses the same state.boxes feed.
  renderSetupTargetPicker();
  // Per-speaker pin invite for stick installs: the USB stick provisions the
  // speaker autonomously on power-cycle, so unlike the network/SSH install
  // flows there is no in-app completion hook and celebrateProvision never
  // fired - users saw no pin prompt after their first stick-installed speaker
  // and only got the whole-set invite at the very end (2026-07-26). The
  // discovery feed knows the moment instead: a box this session saw as stock
  // reappearing as STR just got rescued.
  maybeInviteStickProvisioned();
  // Second world-map invite: once the user's whole supported SoundTouch set is
  // running STR (no stock box left to convert), celebrate the milestone again.
  maybeInviteWorldMapAllDone();
  // Speakers left behind on an old agent: the single most common support
  // theme (2026-07-27). Ask once per app start, right where the user works.
  maybeShowSpeakerUpdateCard();
  // Self-heal a mid-reboot misclassification (see updateStockReprobe).
  updateStockReprobe();
}

// The speaker-update prompt. Users update the APP (the website tells them to)
// and never learn that the speaker carries its own software: three independent
// "my speaker switches itself off" reports on the same day all turned out to be
// speakers left on an old agent, one of them proven by a screenshot showing app
// v0.9.21 next to speaker v0.9.17. The existing cues were a blue dot on the
// tile and a banner buried in Speaker Settings, and the screenshot proved both
// are missable: dots read as decoration, and a user who never opens that tab
// never sees the banner.
//
// So: ask ONCE PER APP START, in the music view where everyone works, naming
// the affected speakers and offering one button that updates all of them. Not
// a modal (that trains people to dismiss), not repeated within a session, and
// it disappears by itself as soon as the speakers are current, because it is
// driven purely by boxNeedsUpdate.
let speakerUpdateCardShown = false;
function maybeShowSpeakerUpdateCard() {
  const el = $('speakerUpdateCard');
  if (!el) return;
  if (!state.appInfo || !state.appInfo.version) return;
  // App first: while the app itself is out of date this card stays away, so
  // the user is never shown two update prompts and left to guess the order.
  if (speakerUpdateCardMuted()) { el.classList.add('hidden'); return; }
  const outdated = (state.boxes || []).filter(b => b && b.kind !== 'stock' && !b.offline && boxNeedsUpdate(b));

  // Nothing behind any more: TAKE THE CARD AWAY. This runs before the
  // once-per-start latch on purpose. The latch used to short-circuit the whole
  // function, so once the card had been shown there was no path that could ever
  // hide it again: the user updated every speaker, the update finished, and the
  // card still sat there asking them to do what they had just done (Jens,
  // 2026-08-05, right after updating all four of his).
  if (!outdated.length) { el.classList.add('hidden'); return; }

  // Some are still behind. If the card is already up, refresh the list rather
  // than leave a stale one naming speakers that are now current.
  const visible = !el.classList.contains('hidden');
  if (speakerUpdateCardShown && !visible) return; // asked once, they said later
  speakerUpdateCardShown = true;

  const list = outdated
    .map(b => `<li>${escapeHtml(getBoxLabel(b))} <span class="suc-ver">${escapeHtml(b.version || '')}</span></li>`)
    .join('');
  const btnLabel = outdated.length > 1
    ? t('speakerUpdate.btnAll', { n: outdated.length })
    : t('speakerUpdate.btnOne');
  el.innerHTML =
    `<div class="suc-body">` +
      `<div class="suc-title">${escapeHtml(t('speakerUpdate.title'))}</div>` +
      `<div class="suc-text">${escapeHtml(t('speakerUpdate.text', { version: state.appInfo.version }))}</div>` +
      `<ul class="suc-list">${list}</ul>` +
      `<div class="suc-actions">` +
        `<button class="btn btn-primary btn-mini" id="sucUpdate">${escapeHtml(btnLabel)}</button>` +
        `<button class="btn btn-mini" id="sucLater">${escapeHtml(t('speakerUpdate.later'))}</button>` +
      `</div>` +
    `</div>`;
  el.classList.remove('hidden');

  const hide = () => el.classList.add('hidden');
  const later = $('sucLater');
  if (later) later.onclick = hide;
  const go = $('sucUpdate');
  if (go) {
    go.onclick = async () => {
      // Do NOT hide first. The card used to disappear on the click and only
      // then start working; every path that then declined to start (an update
      // already running, the speakers turning out to be current, a pre-flight
      // the user cancelled) left the user staring at the tab they were on with
      // the prompt gone for the rest of the session, because the
      // once-per-start latch never brings it back. It hides when the update is
      // actually under way, and stays put otherwise (Jens, 2026-08-22).
      const label = go.textContent;
      const restore = () => { go.disabled = false; go.textContent = label; };
      // Re-resolve against the CURRENT box list. `outdated` is a snapshot from
      // render time, and a discovery pass landing between render and click
      // replaces every object in state.boxes: the stale copies can carry an
      // address the speaker has already left. This is the "shortly after the
      // app started, while the scan is still running" case.
      const wanted = new Set(outdated.map(b => b.host));
      const live = (state.boxes || []).filter(b => b && b.kind !== 'stock' && !b.offline && boxNeedsUpdate(b));
      const targets = live.filter(b => wanted.has(b.host));
      const pick = targets.length ? targets : live;
      if (!pick.length) {
        // Two different empties, and telling them apart matters. No speakers on
        // the list at all means the sweep is mid-cycle or the LAN dropped them:
        // keep the card up and say the speakers are not reachable. A populated
        // list with nothing behind means they really are current, and the card
        // has done its job.
        const anyBox = (state.boxes || []).some(b => b && b.kind !== 'stock');
        showToast(anyBox ? t('updateAll.noneToUpdate') : t('speakerUpdate.notReachable'));
        if (anyBox) hide();
        return;
      }
      go.disabled = true;
      go.textContent = t('speakerUpdate.working');
      try {
        if (pick.length === 1) {
          const box = pick[0];
          // Point Speaker Settings AT THE SPEAKER BEING UPDATED before
          // switching. doBoxUpdate only paints its progress while the user is
          // looking at the target box, so landing on whichever speaker the
          // settings tab happened to have selected ran the whole OTA with no
          // visible sign of it at all: the exact "nothing happens" report.
          state.settingsBox = box;
          showToast(t('speakerUpdate.starting', { name: getBoxLabel(box) }));
          switchView('settings');
          hide();
          await doBoxUpdate(box);
        } else {
          // updateAllBoxes probes every speaker (version, USB stick, Wi-Fi
          // quality) before it can ask, which is seconds of silence on a busy
          // LAN. It keeps its own "checking" toast up; the card stays on screen
          // in its working state until that flow commits or backs out.
          const started = await updateAllBoxes(hide);
          if (!started) restore();
          return;
        }
      } catch (e) { showError(e); restore(); return; }
      restore();
    };
  }
}

// Stock self-heal: a speaker captured mid-reboot classifies as "stock" (its
// Bose port answers before the STR agent is up) and, with no steady-state
// auto-refresh, stayed labelled "ready for STR" until a manual refresh even
// though the agent came back seconds later (live: an ST30 right after an OTA
// reboot). While ANY listed box reads as stock, gently re-probe the known
// addresses every 45s (direct probes only - no mDNS, no sweep); the cycle
// that reaches the agent re-labels the tile and, once no stock box is left,
// the timer stops. A genuinely stock speaker costs two tiny HTTP probes per
// cycle, which is negligible next to the regular status polling.
let _stockReprobeTimer = null;
function updateStockReprobe() {
  const anyStock = (state.boxes || []).some(b => b && b.kind === 'stock');
  if (!anyStock) {
    if (_stockReprobeTimer) { clearInterval(_stockReprobeTimer); _stockReprobeTimer = null; }
    return;
  }
  if (_stockReprobeTimer) return;
  _stockReprobeTimer = setInterval(async () => {
    try {
      const quick = await RefreshKnownBoxes();
      if (quick && quick.length) applyBoxList(quick);
    } catch { /* best-effort; next tick retries */ }
  }, 45000);
}

// Recovery burst: after a network event (router restart, LAN<->Wi-Fi, band switch)
// every speaker can come back on a new IP at once. Discovery then briefly returns
// nothing, and with no steady-state auto-refresh the list would stay empty until
// the user hits Refresh. When a cycle finds none but we had speakers, re-sweep
// every 6 s (up to ~1 min) until they reappear; a cycle that finds any box stops it.
let _recoveryInterval = null;
let _recoveryTicks = 0;
const RECOVERY_MAX_TICKS = 10;
function updateRecoveryBurst(hadBoxesBefore, foundNow) {
  if (foundNow > 0) {
    if (_recoveryInterval) { clearInterval(_recoveryInterval); _recoveryInterval = null; }
    _recoveryTicks = 0;
    return;
  }
  if (_recoveryInterval) return; // a burst is already running; let it continue
  if (!hadBoxesBefore) return;   // nothing was there to lose: don't sweep-spam an empty LAN
  _recoveryTicks = 0;
  _recoveryInterval = setInterval(() => {
    if (_recoveryTicks++ >= RECOVERY_MAX_TICKS) {
      clearInterval(_recoveryInterval); _recoveryInterval = null; _recoveryTicks = 0;
      return;
    }
    discoverBoxes(); // re-sweeps; a successful cycle calls updateRecoveryBurst and clears this
  }, 6000);
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

// refreshMusicZones fetches every STR speaker's live multiroom zone through
// the shared groups.js poll (ONE implementation with the Multi-Room tab) so
// the music-tab group frames are accurate, then repaints the selector.
// Debounced to at most one fetch per 8s (NOT a tight loop); force=true skips
// the debounce for the confirming fetch right after a group change, which
// the debounce used to swallow, leaving the optimistic state unconfirmed.
// Best-effort: a failed per-box poll keeps its last-good entry (groups.js).
async function refreshMusicZones(force) {
  const ran = await fetchZoneLive(state.boxes, { maxAgeMs: force ? 0 : 8000, minBoxes: 2 });
  if (!ran) return;
  renderBoxSelect();
  renderGroupControl(); // keep the group chips + slider in sync with the live zones
}

// refreshBoxPlaying fetches every STR speaker's now-playing so the music-tab
// selector can mark which speakers are currently playing (a small speaker icon
// on their tile). Debounced to at most once per 8s, same cadence and best-effort
// contract as refreshMusicZones. The currently-selected box is also marked live
// from state.nowPlayState in the pill renderer, so its icon never lags the poll.
let _boxPlayingFetchAt = 0;
async function refreshBoxPlaying() {
  // Offline tiles are excluded: every Status() against them just times out
  // and would delay the whole Promise.allSettled round for nothing.
  const strBoxes = (state.boxes || []).filter(b => b && b.kind !== 'stock' && b.deviceID && b.host && !b.offline);
  if (!strBoxes.length) return;
  const now = Date.now();
  if (state.boxPlayingBusy || now - _boxPlayingFetchAt < 8000) return;
  _boxPlayingFetchAt = now;
  state.boxPlayingBusy = true;
  try {
    const results = await Promise.allSettled(strBoxes.map(b => Status(b.host, b.port)));
    const map = {};
    results.forEach((r, i) => {
      let playing = false;
      if (r.status === 'fulfilled' && typeof r.value === 'string') {
        const xml = r.value;
        const ps = (xml.match(/playStatus>([^<]+)</) || [])[1] || '';
        const src = (xml.match(/nowPlaying[^>]*source="([^"]*)"/) || [])[1] || '';
        playing = (ps === 'PLAY_STATE' || ps === 'BUFFERING_STATE') && src !== 'STANDBY';
      }
      map[strBoxes[i].deviceID] = playing;
    });
    state.boxPlaying = map;
  } catch { /* keep previous marks */ } finally {
    state.boxPlayingBusy = false;
  }
  renderBoxSelect();
  renderShellSpeakers();
}

// checkWedgeBanner shows the pull-the-plug hint when any STR speaker reports
// the wedged-control state (transport accepted, never plays; only a
// power-cycle clears it - see the agent's wedge detection). Global banner so
// the hint is visible on every tab.
function checkWedgeBanner() {
  const el = $('boxWedgeBanner');
  if (!el) return;
  const wedged = (state.boxes || []).filter(b => b && b.boxHealth === 'wedged');
  if (!wedged.length) { el.classList.add('hidden'); return; }
  const names = wedged.map(b => getBoxLabel(b)).join(', ');
  el.innerHTML = `<div class="app-update-text"><span class="app-update-icon" aria-hidden="true">&#9888;</span><span><b>${escapeHtml(t('speaker.wedgedTitle'))}</b> ${escapeHtml(t('speaker.wedgedBanner', { name: names }))}</span></div>`;
  el.classList.remove('hidden');
}

// Windows Firewall banner (firewallbanner.js). firewallState holds the last
// backend answer so a re-render (language switch, speaker list repaint) needs
// no new check; firewallDismissed hides it for this session only, because the
// block is still there and the app should say so again on the next start.
let firewallState = null;
let firewallDismissed = false;
// Speakers found in the last discovery sweep, and an install that waited in
// vain for the speaker's callback: the banner only matters when the block
// shows (firewallbanner.js, #1215).
let firewallSpeakersFound = false;
let firewallInstallSuspect = false;
// False until the first discovery sweep returns, so the start-up check cannot
// read the empty list of a sweep still in progress as "nothing found".
let firewallFirstSweepDone = false;
let firewallBusy = false;
const firewallSuspect = firewallSuspectGate();

async function checkFirewallBanner(force) {
  try {
    firewallState = await CheckFirewall(force);
  } catch { return; /* older build without the binding, or no answer */ }
  renderFirewallBanner();
}

function renderFirewallBanner() {
  const el = $('firewallBanner');
  if (!el) return;
  const v = firewallBannerView(firewallState, t, {
    dismissed: firewallDismissed,
    speakersFound: firewallSpeakersFound,
    installSuspect: firewallInstallSuspect,
    discoveryPending: !firewallFirstSweepDone,
  });
  if (!v) { el.classList.add('hidden'); el.innerHTML = ''; return; }
  const btn = (b, id, cls) => `<button class="btn ${cls} app-update-btn" id="${id}"${firewallBusy ? ' disabled' : ''}>${escapeHtml(firewallBusy && cls === 'btn-primary' ? t('firewall.working') : b.label)}</button>`;
  el.innerHTML = `<div class="app-update-text"><span class="app-update-icon" aria-hidden="true">&#9888;</span><span><b>${escapeHtml(v.title)}</b> ${escapeHtml(v.text)}${v.note ? ' ' + escapeHtml(v.note) : ''}</span></div>`
    + `<div class="firewall-banner-actions">${btn(v.primary, 'firewallAllow', 'btn-primary')}${v.secondary ? btn(v.secondary, 'firewallAllowPrivate', 'btn-secondary') : ''}`
    + `<button class="banner-close" id="firewallDismiss" aria-label="${escapeAttr(t('banner.dismiss'))}" title="${escapeAttr(t('firewall.dismissTitle'))}">&times;</button></div>`;
  el.classList.remove('hidden');
  const allow = $('firewallAllow');
  if (allow) allow.onclick = () => allowThroughFirewall(v.primary.includePublic);
  const priv = $('firewallAllowPrivate');
  if (priv && v.secondary) priv.onclick = () => allowThroughFirewall(v.secondary.includePublic);
  const close = $('firewallDismiss');
  if (close) close.onclick = () => { firewallDismissed = true; renderFirewallBanner(); };
}

async function allowThroughFirewall(includePublic) {
  if (firewallBusy) return;
  firewallBusy = true;
  renderFirewallBanner();
  showToast(t('firewall.working'), 0);
  let res = null;
  try {
    res = await AllowThroughFirewall(includePublic);
  } catch (e) {
    res = { ok: false, error: String(e && e.message ? e.message : e) };
  } finally {
    firewallBusy = false;
  }
  if (res && res.status) firewallState = res.status;
  const toast = firewallUnblockToast(res, t);
  showToast(toast.text, toast.ok ? 6000 : 12000);
  renderFirewallBanner();
  // With the block gone, look for the speakers straight away rather than at
  // the next minute tick.
  if (toast.ok) { try { discoverBoxes(); } catch { /* the next sweep retries */ } }
}

// checkBoxIssueBanner is a heads-up when a speaker carries leftovers of a
// rival cloud-free SoundTouch tool (they can fight STR) or has no STR Wi-Fi
// backup saved. Global banner, #270. Dismissible per box and warning type:
// both are informational (a healthy speaker keeps playing either way), so the
// user must be able to acknowledge them once instead of being nagged forever
// (Jens, 2026-07-12).
function warnDismissKey(box, type) {
  return 'warnDismiss:' + (box.deviceId || box.host) + ':' + type;
}
function warnDismissed(box, type) {
  try { return !!localStorage.getItem(warnDismissKey(box, type)); } catch { return false; }
}
function checkBoxIssueBanner() {
  const el = $('boxIssueBanner');
  if (!el) return;
  const boxes = state.boxes || [];
  const conflict = boxes.filter(b => b && b.conflictingMod && !warnDismissed(b, 'conflict'));
  // A speaker asking a non-standard Bose cloud address (#986). NOT dismissible
  // and not folded into the conflict line: the marker files may be long gone
  // (they do not survive a factory reset, this does) and nothing plays on such a
  // speaker at all, so it is worth its own sentence naming the address.
  const foreignCloud = boxes.filter(b => b && b.foreignCloudURL && !b.conflictingMod);
  const noWifi = boxes.filter(b => b && b.wlanCredsMissing && !warnDismissed(b, 'nowifi'));
  // A speaker refusing essentially every preset recall (Bose error 1036). This
  // one is NOT dismissible: nothing the user presses will play until it clears,
  // and the remedy people find on their own, pulling the plug, resets the box
  // clock and poisons the next boot, while a soft restart clears it (#419
  // Finding 4). Saying so at the moment it happens is the whole point.
  // recallRefusal is the storm's quiet sibling (no 1036 ever fires, the box
  // just drops its source for every recall); same remedy, same banner.
  // A wedged speaker is excluded: it already has the "unplug it" banner, and
  // a restart notice beside it contradicted that (#1065).
  const storm = stormBannerBoxes(boxes);
  // A thumbs-key press the speaker refused. Not dismissible and worth its own
  // line: the press produced NOTHING visible before this, so two owners spent
  // days re-saving a group that was never the problem (2026-09-26). The
  // speaker's own reason is shown verbatim rather than paraphrased.
  const keyErr = boxes.filter(b => b && b.groupKeyError);
  // A long press the speaker made and STR could not keep. Same shape as the
  // key error: it happened, it was refused, and nothing visible said so, so
  // the key appeared to snap back to the old station on its own.
  const holdErr = boxes.filter(b => b && b.holdRefusedSource);
  // A speaker that cannot reach the internet while the LAN works (the agent's
  // passive verdict, nointernet.js). Not dismissible: every station fails on
  // it, and the fix is on the router, which only a sentence can point at.
  const noNet = noInternetBoxes(boxes);
  const msgs = [];
  if (noNet.length) {
    const names = noNet.map(b => getBoxLabel(b)).join(', ');
    msgs.push(`<b>${escapeHtml(t('speaker.noInternetBanner', { name: names }))}</b> ${escapeHtml(t('speaker.noInternetHint'))}`);
  }
  if (conflict.length) {
    const names = conflict.map(b => getBoxLabel(b)).join(', ');
    msgs.push(escapeHtml(t('speaker.conflictModBanner', { name: names, mod: conflict[0].conflictingMod })));
  }
  if (foreignCloud.length) {
    const names = foreignCloud.map(b => getBoxLabel(b)).join(', ');
    msgs.push(escapeHtml(t('speaker.foreignCloudBanner', {
      name: names, url: foreignCloud[0].foreignCloudURL,
    })));
  }
  if (noWifi.length) {
    const names = noWifi.map(b => getBoxLabel(b)).join(', ');
    msgs.push(escapeHtml(t('speaker.noWifiBanner', { name: names })));
  }
  if (storm.length) {
    const names = storm.map(b => getBoxLabel(b)).join(', ');
    msgs.push(escapeHtml(t('speaker.stormBanner', { name: names })));
  }
  if (keyErr.length) {
    const names = keyErr.map(b => getBoxLabel(b)).join(', ');
    msgs.push(escapeHtml(t('speaker.groupKeyErrorBanner', {
      name: names, reason: keyErr[0].groupKeyError,
    })));
  }
  if (holdErr.length) {
    const names = holdErr.map(b => getBoxLabel(b)).join(', ');
    msgs.push(escapeHtml(t(holdRefusedBannerKey(holdErr[0].holdRefusedSource), {
      name: names, slot: holdErr[0].holdRefusedSlot || '?',
    })));
  }
  if (!msgs.length) { el.classList.add('hidden'); return; }
  // When a speaker has no saved Wi-Fi, give the user a direct way to act on it
  // instead of just telling them to "set it up in the settings": the button
  // jumps straight to that speaker's settings, where the Wi-Fi section is.
  const wifiBtn = noWifi.length
    ? `<button class="btn btn-mini" id="boxIssueWifiBtn">${escapeHtml(t('speaker.wifiSaveBtn'))}</button>`
    : '';
  // A conflicting-mod (AfterTouch, OpenCloudTouch) leftover is removable in one
  // click, and this button does it right here. It used to only switch to the
  // speaker's settings page, which its label never said, so an owner pressed it
  // twice and saw nothing happen (#1083).
  const conflictBtn = conflict.length
    ? `<button class="btn btn-mini" id="boxIssueConflictBtn">${escapeHtml(t('speaker.conflictRemoveBtn'))}</button>`
    : '';
  // The soft restart, offered right where the problem is stated. The speaker
  // comes back in about a minute and the state is gone.
  const stormBtn = storm.length
    ? `<button class="btn btn-primary btn-mini" id="boxIssueStormBtn">${escapeHtml(t('speaker.stormRestartBtn'))}</button>`
    : '';
  // One press puts STR back on every hijacked speaker at once.
  //
  // The per-speaker Actions page cannot do this. Its repair rewrites the cloud
  // address FILE, and a rival app that sets the address at runtime leaves that
  // file untouched, so the repair healed nothing and said it had worked. The
  // cure is a restart, because the firmware reads the file once at boot.
  // Measured across five speakers on 2026-09-26 after the ST Remote Pro iOS app
  // was started; one reboot each brought every one back stock and paired.
  const cloudBtn = foreignCloud.length
    ? `<button class="btn btn-primary btn-mini" id="boxIssueCloudBtn">${escapeHtml(t('speaker.cloudRestoreBtn', { n: foreignCloud.length }))}</button>`
    : '';
  el.innerHTML = `
    <div class="app-update-text"><span class="app-update-icon" aria-hidden="true">&#9888;</span><span>${msgs.join('<br>')}</span></div>
    ${cloudBtn}
    ${stormBtn}
    ${conflictBtn}
    ${wifiBtn}
    ${(conflict.length || noWifi.length) ? `<button class="btn btn-secondary btn-mini" id="boxIssueDismissBtn">${escapeHtml(t('speaker.issueDismiss'))}</button>` : ''}`;
  const sb = $('boxIssueStormBtn');
  if (sb) sb.onclick = async () => {
    const box = storm[0];
    sb.disabled = true;
    showToast(t('speaker.stormRestarting', { name: getBoxLabel(box) }));
    try {
      await RebootBox(box.host, box.port);
    } catch (e) {
      showError(String(e));
    } finally {
      sb.disabled = false;
    }
  };
  const cloudB = $('boxIssueCloudBtn');
  if (cloudB) cloudB.onclick = () => runCloudRestore(foreignCloud, cloudB);
  const wb = $('boxIssueWifiBtn');
  // Settings opens straight on the Wi-Fi form for this speaker (wireWlanSwitch).
  if (wb) wb.onclick = () => { state.wlanStoreFor = noWifi[0].host; selectBox(noWifi[0]); switchView('settings'); };
  const cb = $('boxIssueConflictBtn');
  if (cb) cb.onclick = async () => {
    const label = cb.textContent;
    await runConflictCleanup(conflict[0], {
      t, confirmWarn, showToast, showError,
      removeConflictingMod: RemoveConflictingMod,
      rebootBox: RebootBox,
      rediscover: (afterMs) => {
        if (afterMs > 0) setTimeout(discoverBoxes, afterMs);
        else discoverBoxes();
      },
      setBusy: (busy) => {
        cb.disabled = busy;
        cb.textContent = busy ? t('settingsView.removeConflictRunning') : label;
      },
    });
  };
  const d = $('boxIssueDismissBtn');
  if (d) d.onclick = () => {
    const stamp = String(Date.now());
    conflict.forEach(b => { try { localStorage.setItem(warnDismissKey(b, 'conflict'), stamp); } catch {} });
    noWifi.forEach(b => { try { localStorage.setItem(warnDismissKey(b, 'nowifi'), stamp); } catch {} });
    el.classList.add('hidden');
  };
  el.classList.remove('hidden');
}

// runCloudRestore puts STR back on every speaker a rival app has taken over.
//
// The confirmation carries the warning that matters more than the restart: as
// long as the other app runs, it sets the address again, including right after
// the speaker comes back. That is not a theory, it is what happened on
// 2026-09-26: the hijack itself restarted all five speakers, and they came back
// still pointing at the other service because the app was still open. Restart
// them while it runs and the whole thing simply repeats.
async function runCloudRestore(boxes, btn) {
  if (!boxes || !boxes.length) return;
  const names = boxes.map(b => getBoxLabel(b)).join(', ');
  const ok = await confirmWarn(
    t('speaker.cloudRestoreTitle'),
    `<p>${escapeHtml(t('speaker.cloudRestoreBody', { name: names, n: boxes.length }))}</p>`
    + `<p><b>${escapeHtml(t('speaker.cloudRestoreAppWarning'))}</b></p>`,
  );
  if (!ok) return;
  btn.disabled = true;
  // The progress line names the speaker being worked on. One at a time, so a
  // fleet takes a few minutes and silence would read as a hang.
  const off = EventsOn('cloudrestore:progress', (p) => {
    if (!p) return;
    showToast(t('speaker.cloudRestoreProgress', {
      name: p.name || '', done: p.done || 0, total: p.total || boxes.length,
    }));
  });
  try {
    const results = await RestoreSTRCloud(boxes.map(b => ({
      host: b.host, port: b.port, name: getBoxLabel(b),
    })));
    const done = (results || []).filter(r => r && r.restored);
    const failed = (results || []).filter(r => r && !r.restored);
    if (!failed.length) {
      showToast(t('speaker.cloudRestoreDone', { n: done.length }));
    } else {
      // Name the speakers that are still wrong and why. A partial repair
      // reported as success is the mistake this whole path exists to undo.
      const detail = failed
        .map(r => `${r.name || r.host}: ${r.error || r.stillForeign || '?'}`)
        .join('; ');
      showError(t('speaker.cloudRestorePartial', {
        ok: done.length, bad: failed.length, detail,
      }));
    }
    discoverBoxes();
  } catch (e) {
    showError(String(e));
  } finally {
    try { off(); } catch {}
    btn.disabled = false;
  }
}

// manualIpInputBusy reports whether a manual connect-by-IP input is in use
// (focused or holding text). While one is, the speaker area must not be
// re-rendered: the recovery burst repaints every ~6s and a rebuild wiped the
// user's half-typed address and their focus. Both the empty state's field and
// the one behind the "add by IP" tile count.
function manualIpInputBusy() {
  return ['emptyIpInput', 'listIpInput'].some(id => {
    const el = document.getElementById(id);
    if (!el) return false;
    return document.activeElement === el || !!(el.value || '').trim();
  });
}

// wireManualIp connects one connect-by-IP input/button pair. Shared by the
// empty state and by the "add by IP" tile that sits at the end of a populated
// speaker list: on a routed network, discovery finds ONE speaker and the empty
// state disappears, which used to leave no way at all to add the second and
// third by address (#420).
function wireManualIp(inputId, btnId) {
  const ipInput = document.getElementById(inputId);
  const addIp = document.getElementById(btnId);
  if (!ipInput || !addIp) return;
  const tryAddIp = async () => {
    const ip = (ipInput.value || '').trim();
    if (!ip) { ipInput.focus(); return; }
    addIp.disabled = true;
    showToast(t('speaker.manualIpSearching', { ip }));
    try {
      await AddBoxByIP(ip);
      ipInput.value = ''; // so the next address can be typed straight away
      await discoverBoxes(); // RefreshKnownBoxes now includes the cached box
    } catch (e) {
      showError(t('speaker.manualIpNotFound', { ip }));
    } finally {
      addIp.disabled = false;
    }
  };
  addIp.onclick = tryAddIp;
  ipInput.onkeydown = (e) => { if (e.key === 'Enter') tryAddIp(); };
}

// offlineAgo renders "how long unseen" for an offline speaker tile via
// Intl.RelativeTimeFormat in the app locale, so the phrase localizes itself
// ("vor 5 Minuten" / "5 minutes ago") without per-language time strings.
function offlineAgo(sec) {
  try {
    const rtf = new Intl.RelativeTimeFormat(getLocale() || 'en', { numeric: 'auto' });
    if (sec < 3600) return rtf.format(-Math.max(1, Math.round(sec / 60)), 'minute');
    if (sec < 86400) return rtf.format(-Math.round(sec / 3600), 'hour');
    return rtf.format(-Math.round(sec / 86400), 'day');
  } catch { return Math.max(1, Math.round(sec / 60)) + ' min'; }
}
function offlineTitle(b) {
  return t('speaker.offlineTooltip', { ago: offlineAgo(b.offlineSinceSec || 0) });
}

// dissolveGroupFrame takes apart the group led by masterKey, from the x on its
// frame in the speaker picker. Same call the Multi-Room tab's x makes (the
// DELETE goes to the master and clears its stored group), then the reachable
// ex-followers are stopped so nothing keeps playing the group's stream alone.
async function dissolveGroupFrame(masterKey) {
  const mk = String(masterKey || '').toUpperCase();
  // The same resolver the frame's label uses. A plain deviceID lookup missed
  // whenever the app's record carries a different identity than the speaker's
  // own answer, and this handler then returned without a word: the x was drawn
  // and pressing it did nothing at all.
  const masterBox = masterBoxForKey(mk, state.zoneLive, state.boxes);
  if (!masterBox) {
    showToast(t('multiroom.dissolveIncomplete'));
    return;
  }
  const followers = state.boxes.filter(b => b !== masterBox && b.kind !== 'stock' && !b.offline &&
    String(((state.zoneLive || {})[b.deviceID] || {}).master || '').toUpperCase() === mk);
  try {
    // Trust the speaker's verdict, the same way views/multiroom.js already
    // does. A green tick on a group that is still playing is worse than no
    // message at all: it sends the user round the loop again, which is exactly
    // what nine refused dissolves in a row looked like from the outside
    // (Juergen, eleven speakers, 2026-09-14).
    const res = await DissolveZone(masterBox.host, masterBox.port);
    if (res && res.ok === false) {
      // The group is still there, so the optimistic empty zone must NOT be
      // applied: painting it empty is what made the frame vanish and come back.
      showToast(dissolveIncompleteMessage(res));
    } else {
      await Promise.allSettled(followers.map(b => Stop(b.host, b.port)));
      state.zoneLive = applyOptimisticZone(state.zoneLive, masterBox, []);
      notifyZoneLive();
      showToast(t(res && res.nothing ? 'multiroom.nothingToUngroup' : 'group.dissolvedToast'));
    }
  } catch (e) {
    showToast(t('multiroom.formFailed', { err: String((e && e.message) || e || '') }));
  }
  renderBoxSelect();
  renderGroupControl();
  setTimeout(() => refreshMusicZones(true), 1200);
}

function renderBoxSelect() {
  const sel = $('boxSelect');
  if (state.boxes.length === 0) {
    // The empty state is static markup; while the manual-IP field is in use
    // an identical rebuild would only destroy the input, so keep the DOM.
    if (manualIpInputBusy()) return;
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
        <div class="manual-ip">
          <div class="empty-state-text">${escapeHtml(t('speaker.manualIpHelp'))}</div>
          <div class="manual-ip-row">
            <input type="text" id="emptyIpInput" class="manual-ip-input" placeholder="${escapeAttr(t('speaker.manualIpPlaceholder'))}" inputmode="decimal" autocomplete="off" spellcheck="false">
            <button class="btn btn-mini" id="emptyAddIpBtn">${escapeHtml(t('speaker.manualIpButton'))}</button>
          </div>
        </div>
      </div>`;
    const go = document.getElementById('emptyGoSetup');
    if (go) go.onclick = () => switchView('setup');
    const rt = document.getElementById('emptyRetry');
    if (rt) rt.onclick = () => discoverBoxes();
    // Manual connect-by-IP: the fallback when discovery is blocked by the
    // network (AP isolation, a different subnet, a VPN, or a security suite).
    wireManualIp('emptyIpInput', 'emptyAddIpBtn');
    updateBoxUiVisibility();
  checkWedgeBanner();
  checkBoxIssueBanner();
  renderFirewallBanner();
    return;
  }
  // Cluster speakers that share a live multiroom zone into a colored frame
  // (display only; grouping is done in the Multi-Room tab). Defensive: with no
  // live group of >=2 discovered speakers the selector renders exactly as before.
  const zlMap = state.zoneLive || {};
  // Box-shaped wrapper around the shared groups.js helper (stock boxes are
  // never zone members, whatever their deviceID collides with).
  // A stereo pair is not a multiroom zone, and the speakers report it
  // separately, so the picker used to show the two halves as two unrelated
  // speakers. That is misleading twice over: they play as one, and pressing a
  // preset on the pair is refused by the firmware (#528) with nothing on screen
  // explaining why. Framing them like a group says what is going on.
  const livePairs = stereoPairsOf(zlMap);
  // Balance is read once when the selected speaker changes, never on the status
  // poll (the speaker hangs on that endpoint while it is asleep). Pairing and
  // unpairing happen without changing the selection, so without this the value
  // would stay stale, or stay hidden for a pair formed after the speaker was
  // picked. Keyed over ALL pairs, so a second pair forming after a speaker was
  // selected still re-reads once; an unchanged set re-reads nothing.
  const pairStamp = livePairs.map(p => `${p.id}|${p.master}`).sort().join(',');
  if (pairStamp !== state.lastBalancePair) {
    state.lastBalancePair = pairStamp;
    setTimeout(() => { refreshBalance().catch(() => {}); }, 0);
  }
  // Every live pair frames under its own key (its master box's discovered
  // deviceID). pairHostToKey routes any paired box to its frame; pairByKey
  // gives the frame its pair, so each pair shows its OWN stored name. Matched on
  // host, not deviceID: a two-chip chassis announces a different id over
  // discovery than the one the firmware puts in the pair.
  const pairHostToKey = new Map();
  const pairByKey = new Map();
  for (const p of livePairs) {
    const boxes = pairMemberBoxes(p, state.boxes).map(x => x.box).filter(Boolean);
    const masterBox = boxes.find(b =>
      String(b.deviceID || '').toUpperCase() === String(p.master || '').toUpperCase()) || boxes[0] || null;
    const key = masterBox ? String(masterBox.deviceID || '').toUpperCase() : '';
    if (!key) continue;
    pairByKey.set(key, p);
    for (const b of boxes) pairHostToKey.set(b.host, key);
  }
  const masterOf = (b) => {
    if (b.kind === 'stock') return '';
    const z = zoneMasterOf(b.deviceID, zlMap);
    if (z) return z;
    const k = pairHostToKey.get(b.host);
    if (k) return k;
    return '';
  };
  const memberCount = {};
  state.boxes.forEach(b => { const m = masterOf(b); if (m) memberCount[m] = (memberCount[m] || 0) + 1; });
  // Which discovered box leads a given master key. NOT a plain deviceID
  // lookup: the key comes from the speakers' own zone documents, so it names
  // the master by the identity the SPEAKER answers with, while the box record
  // here carries whatever the app resolved for it, and those two can be
  // different values for the same speaker (field bundle 2026-09-07: three
  // single-chip SoundTouch 10s where the box's own /info id WAS the zone master
  // and the app's record held the other MAC). The lookup then found nobody, so
  // the frame had no name and no x and no chip was starred. masterBoxForKey
  // asks by address and by group shape as well, and answers null rather than
  // guessing. Resolved once per key, because every pill asks again.
  const masterBoxCache = new Map();
  const masterBoxOfKey = (m) => {
    if (!masterBoxCache.has(m)) masterBoxCache.set(m, masterBoxForKey(m, zlMap, state.boxes));
    return masterBoxCache.get(m);
  };
  // A box is a framed master only when its group actually renders a frame, i.e.
  // >=2 of its members are discovered here. This keeps the master star and the
  // frame in lock-step (never a lone star on an unframed pill). Compared by
  // HOST for the same reason: the id is the field that can disagree.
  const isFramedMaster = (b) => {
    const m = masterOf(b);
    if (!m || memberCount[m] < 2) return false;
    const resolved = masterBoxOfKey(m);
    return !!resolved && resolved.host === b.host;
  };
  // The live flags of a tile (playing, offline, update, no internet) come from
  // speakertile.js, the same reading the UI 2.0 sidebar speaker list draws from, so
  // the two never disagree about a speaker.
  const tileCtx = {
    currentBox: state.currentBox,
    nowPlayState: state.nowPlayState,
    nowSource: state.nowSource,
    boxPlaying: state.boxPlaying,
    needsUpdate: boxNeedsUpdate,
    cannotReachInternet: boxCannotReachInternet,
    stereoPairs: livePairs,
  };
  const pill = (b) => {
    const tile = speakerTileState(b, tileCtx);
    const isStock = tile.stock;
    // Sticky offline tiles (2026-07-26): a speaker once sighted stays listed
    // when scans miss it (reboot, plug pulled), clearly greyed out with a
    // disconnect mark and a "last seen ..." tooltip, until it answers again.
    const off = tile.offline;
    const offCls = off ? ' offline' : '';
    const offTitle = off ? offlineTitle(b) : '';
    const offAttr = off ? ` data-offline="1" title="${escapeAttr(offTitle)}"` : '';
    const offMark = off
      ? `<span class="box-offline-mark" aria-label="${escapeAttr(offTitle)}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="12" height="12" aria-hidden="true"><line x1="1" y1="1" x2="23" y2="23"></line><path d="M16.72 11.06A10.94 10.94 0 0 1 19 12.55"></path><path d="M5 12.55a10.94 10.94 0 0 1 5.17-2.39"></path><path d="M10.71 5.05A16 16 0 0 1 22.58 9"></path><path d="M1.42 9a15.91 15.91 0 0 1 4.7-2.88"></path><path d="M8.53 16.11a6 6 0 0 1 6.95 0"></path><line x1="12" y1="20" x2="12.01" y2="20"></line></svg></span>`
      : '';
    // The main speaker of a group is named as such, not only starred: a lone
    // star did not read as "this one leads" (Jens, 2026-09-06).
    const groupMark = isFramedMaster(b)
      ? `<span class="box-group-master" title="${escapeAttr(t('multiroom.groupMasterTitle'))}">&#9733; ${escapeHtml(t('multiroom.mainBadge'))}</span>`
      : '';
    const active = tile.isCurrent && !isStock ? ' active' : '';
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
      // Every stock Bose speaker STR discovers is an install candidate: a box
      // reachable by IP installs over the network (stick-free), so we invite the
      // install for any model rather than blocking one. Soundbars / adapters
      // install too; their missing hardware preset buttons are noted in Setup.
      //
      // A box STR once ran on whose agent went silent for good while the stock
      // firmware still answers (uninstalled, NAND wiped, agent crashed) is a
      // stock box too, but says so: a grey "STR not running" instead of the
      // first-time "Ready for STR", so the owner knows this is a repair, not
      // a speaker that never had STR (2026-09-06 report: the card kept the
      // old version badge on a speaker STR had just been removed from).
      const gone = tile.strNotRunning;
      const badge = `<span class="box-stock-badge${gone ? ' box-stock-badge-gone' : ''}">${escapeHtml(t(gone ? 'speaker.strNotRunningBadge' : 'speaker.needsInstallBadge'))}</span>`;
      const stockTitle = off ? offTitle : t(gone ? 'speaker.strNotRunningTooltip' : 'speaker.stockTooltip');
      return `<span class="box-btn${stockCls}${offCls}" data-host="${b.host}" data-port="${b.port}" data-stock="1"${off ? ' data-offline="1"' : ''} role="button" tabindex="0" title="${escapeAttr(stockTitle)}">${offMark}${escapeHtml(label)}${model} <small>${b.host}</small>${badge}</span>`;
    }
    // An offline tile keeps showing the last CONFIRMED self-report, marked
    // stale: a fresh-looking version on an unreachable box is how #775's
    // falsified v-numbers stayed believable for twenty minutes.
    const ver = b.version ? `<span class="box-ver${b.offline ? ' box-ver-stale' : ''}" title="${escapeAttr(t(b.offline ? 'speaker.staleVersionTitle' : 'speaker.stickVersionTitle'))}">${escapeHtml(b.version)}</span>` : '';
    // Red dot when this speaker's agent is older than the app's embedded
    // one: a glanceable "update available" cue right on the speaker button
    // itself, in addition to the settings-tab badge and the music-tab
    // banner (#108).
    const showUpd = tile.needsUpdate;
    const updCls = showUpd ? ' needs-update' : '';
    // A WORD, not a dot: the blue dot that used to sit here reads as decoration
    // to non-technical users. A screenshot from a user whose speaker had been
    // three versions behind for days showed the dot in plain sight, twice, while
    // he was writing to ask why his speaker kept switching itself off
    // (2026-07-27). The tooltip spells out both versions.
    const updDot = showUpd
      ? `<span class="box-update-chip" role="button" tabindex="0" data-host="${b.host}" data-port="${b.port}" title="${escapeAttr(t('speaker.updateChipTitle', { box: b.version || '?', app: (state.appInfo && state.appInfo.version) || '?' }))}">${escapeHtml(t('speaker.updateChip'))}</span>`
      : '';
    // Small speaker icon on a tile whose speaker is currently playing, so the
    // playing speaker is obvious among several. The selected box is marked live
    // from nowPlayState (no poll lag); the others from the refreshBoxPlaying poll.
    const playingNow = tile.playing;
    const playMark = playingNow
      ? `<span class="box-playing" title="${escapeAttr(t('speaker.playingNow'))}" aria-label="${escapeAttr(t('speaker.playingNow'))}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="13" height="13" aria-hidden="true"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"></polygon><path d="M15.54 8.46a5 5 0 0 1 0 7.07"></path><path d="M19.07 4.93a7 7 0 0 1 0 14.14"></path></svg></span>`
      : '';
    // "No internet" right on the speaker's own tile, the router hint in its
    // tooltip: the tile is where an owner looks when a station will not play.
    const noNetChip = tile.noInternet
      ? `<span class="box-nointernet-chip" title="${escapeAttr(noInternetText(t))}">${escapeHtml(t('speaker.noInternetChip'))}</span>`
      : '';
    return `<span class="box-btn${active}${updCls}${playingNow ? ' playing-now' : ''}${offCls}" data-host="${b.host}" data-port="${b.port}"${offAttr} role="button" tabindex="0">${groupMark}${offMark}${playMark}${escapeHtml(label)}${model} <small>${b.host}</small>${ver}${updDot}${noNetChip}<span class="box-edit" data-host="${b.host}" data-port="${b.port}" title="${escapeAttr(t('speaker.editTitle'))}">&#9881;</span></span>`;
  };
  // "Add by IP" tile, always available once at least one speaker is listed.
  // On a routed network mDNS and the local /24 scan find only the speakers on
  // this subnet, and the empty state's address field is gone the moment the
  // first one shows up, so there was no path left to add the others (#420).
  // Collapsed to a bare plus until clicked, so it stays out of the way for
  // everyone whose speakers are simply found: this is a rescue path for a
  // network that hides them, not something most people ever need. It carries
  // its name as the accessible label and its explanation as the tooltip, so
  // nothing is lost by dropping the words next to the symbol.
  const addIpTile = () => `
    <span class="box-btn box-add-ip" id="addIpTile" role="button" tabindex="0" aria-label="${escapeAttr(t('speaker.addByIp'))}" title="${escapeAttr(t('speaker.manualIpHelp'))}">+</span>
    <span class="manual-ip-row box-add-ip-row hidden" id="addIpRow">
      <input type="text" id="listIpInput" class="manual-ip-input" placeholder="${escapeAttr(t('speaker.manualIpPlaceholder'))}" inputmode="decimal" autocomplete="off" spellcheck="false">
      <button class="btn btn-mini" id="listAddIpBtn">${escapeHtml(t('speaker.manualIpButton'))}</button>
    </span>`;
  const groups = Object.keys(memberCount).filter(m => memberCount[m] >= 2).sort();
  if (groups.length === 0) {
    sel.innerHTML = state.boxes.map(pill).join('') + addIpTile();
  } else {
    // Key the frame colour to the GROUP, not to its position in the list. With
    // (i % 4) + 1 a zone changed colour whenever another zone lost a member and
    // dropped out of the list, which is the one thing an identity colour must
    // not do.
    //
    // The hash is only a PREFERENCE, though. Taken raw it collides: with four
    // slots and four zones two of them share a colour more often than not, and
    // the old positional scheme at least guaranteed four distinct colours for
    // four groups. So hash first, then linear-probe into the next free slot, so
    // a zone keeps its colour across refreshes AND simultaneous zones stay
    // distinguishable up to the four the palette holds.
    const colorOf = {};
    const taken = new Set();
    for (const m of groups) {
      let h = 0;
      for (let i = 0; i < m.length; i++) h = (h * 31 + m.charCodeAt(i)) >>> 0;
      let slot = h % 4;
      for (let k = 0; k < 4 && taken.has(slot); k++) slot = (slot + 1) % 4;
      taken.add(slot);
      colorOf[m] = slot + 1;
    }
    let html = '';
    for (const m of groups) {
      const members = state.boxes.filter(b => masterOf(b) === m);
      // master first inside the frame
      members.sort((a, b) => (((b.deviceID || '').toUpperCase() === m ? 1 : 0) - ((a.deviceID || '').toUpperCase() === m ? 1 : 0)));
      // Name the group after its master speaker so it is obvious which zone the
      // frame is (the master leads the multiroom group). Through the same
      // resolver the star uses, so a frame can never be left nameless and
      // without its x by a key the box record does not carry (see
      // masterBoxOfKey).
      const masterBox = masterBoxOfKey(m);
      const groupName = masterBox ? getBoxLabel(masterBox) : '';
      // A stereo pair gets its own wording and its own mark. Reusing the
      // multiroom label would call two speakers acting as one channel pair a
      // "group led by X", which is not what it is.
      const framePair = pairByKey.get(m) || null;
      const isPair = !!framePair;
      const pairIcon = STEREO_ICON;
      const zoneIcon = GROUP_ICON;
      // A permanent group says so on its frame: it comes back by itself when
      // the main speaker plays, a temporary one ends with the next standby.
      // The main speaker's own zone answer carries the flag.
      const permanent = !isPair && masterBox && !!(((state.zoneLive || {})[masterBox.deviceID] || {}).permanent);
      const permBadge = permanent
        ? ` <span class="box-group-perm" title="${escapeAttr(t('speaker.permanentTitle'))}">&#128257; ${escapeHtml(t('speaker.permanentBadge'))}</span>`
        : '';
      const groupLabel = groupName
        ? `<span class="box-group-label" title="${escapeAttr(isPair ? t('speaker.stereoPairTitle') : t('speaker.groupLabelTitle', { name: groupName }))}">${isPair ? pairIcon : zoneIcon} ${escapeHtml(isPair ? (pairDisplayName(framePair, renderBoxSelect) || t('multiroom.stereoHeading')) : groupName)}${permBadge}</span>`
        : '';
      // The same x the Multi-Room tab has: take THIS group apart from where it
      // is shown. It was only on the other tab (Jens, 2026-09-06). A stereo
      // pair keeps its undo on the Multi-Room tab, where both halves are named.
      const xBtn = (!isPair && masterBox)
        ? `<button class="box-group-x" data-dissolve="${escapeAttr(m)}" title="${escapeAttr(t('multiroom.dissolveGroupTip'))}" aria-label="${escapeAttr(t('multiroom.dissolveGroupTip'))}">&times;</button>`
        : '';
      html += `<div class="box-group box-group-c${colorOf[m]}${xBtn ? ' has-x' : ''}">${xBtn}${groupLabel}${members.map(pill).join('')}</div>`;
    }
    html += state.boxes.filter(b => { const mm = masterOf(b); return !(mm && memberCount[mm] >= 2); }).map(pill).join('');
    sel.innerHTML = html + addIpTile();
    sel.querySelectorAll('.box-group-x').forEach(x => {
      x.onclick = (e) => { e.stopPropagation(); dissolveGroupFrame(x.dataset.dissolve); };
    });
  }
  const ipTile = document.getElementById('addIpTile');
  const ipRow = document.getElementById('addIpRow');
  if (ipTile && ipRow) {
    const openIpRow = () => {
      ipTile.classList.add('hidden');
      ipRow.classList.remove('hidden');
      const inp = document.getElementById('listIpInput');
      if (inp) inp.focus();
    };
    ipTile.onclick = openIpRow;
    ipTile.onkeydown = (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openIpRow(); } };
  }
  wireManualIp('listIpInput', 'listAddIpBtn');
  sel.querySelectorAll('.box-btn').forEach(btn => {
    // The "add by IP" tile shares the .box-btn look but is not a speaker.
    // Without this it got the speaker handler assigned over its own, and
    // since it carries no host the handler found nothing and returned in
    // silence: the tile simply did not react to a click (live 2026-07-29).
    if (btn.classList.contains('box-add-ip')) return;
    // A click takes the same path as the UI 2.0 sidebar speaker list (pickBox).
    btn.onclick = (e) => {
      // A click on the gear icon opens the settings view rather than
      // selecting the speaker.
      if (e.target.closest('.box-edit') || e.target.closest('.box-update-chip')) return;
      const host = btn.dataset.host;
      const port = parseInt(btn.dataset.port, 10);
      const box = state.boxes.find(b => b.host === host && b.port === port);
      // The tile as it was drawn decides, not the live list: a greyed tile
      // whose speaker has just come back (list replaced, picker not yet
      // redrawn) still explains itself and rediscovers, as it always did.
      if (btn.dataset.offline) { pickBox({ ...(box || {}), offline: true }, btn.title); return; }
      if (!box) return;
      pickBox(box);
    };
  });
  // Gear click: set settingsBox and switch the tab.
  // The "Update" chip TAKES YOU THERE, it does not start the update. It used
  // to fire the update on the spot, from a tab where nothing about updating is
  // shown: one click on a small word next to a speaker name and the speaker
  // restarts. Updating a speaker takes minutes and reboots it, so it should be
  // a decision made on the page that explains it, not a side effect of a click
  // meant to find out what the chip even means.
  sel.querySelectorAll('.box-update-chip').forEach(chip => {
    const open = (e) => {
      e.stopPropagation();
      e.preventDefault();
      const host = chip.dataset.host;
      const port = parseInt(chip.dataset.port, 10);
      const box = (state.boxes || []).find(b => b.host === host && b.port === port);
      if (!box) return;
      state.settingsBox = box;
      switchView('settings');
    };
    chip.onclick = open;
    chip.onkeydown = (e) => { if (e.key === 'Enter' || e.key === ' ') open(e); };
  });
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
  checkWedgeBanner();
  checkBoxIssueBanner();
  renderFirewallBanner();
}

// pickBox is what choosing a speaker does, wherever it is chosen: the Music
// tab's picker (click or keyboard) and the UI 2.0 sidebar speaker list.
//
// An offline speaker cannot be selected (every call would fail); choosing it
// explains itself and kicks off a fresh discovery, which is also the fastest
// way to bring a returned speaker back to life in the list. A stock speaker is
// an invitation into Setup. Anything else becomes the controlled speaker.
async function pickBox(box, titleHint) {
  if (!box) return;
  if (box.offline) {
    const title = titleHint || (box.host ? offlineTitle(box) : '');
    showToast(title || t('speaker.offlineTooltip', { ago: '' }));
    discoverBoxes();
    return;
  }
  if (box.kind === 'stock') {
    // Any stock Bose speaker is the happy path now: reachable boxes install
    // over the network (stick-free), so we invite every model into Setup with
    // a positive CTA instead of blocking soundbars / adapters. Setup notes the
    // hardware-preset-button caveat for 'limited' models.
    const label = getBoxLabel(box);
    const ok = await confirmWarn(
      t('speaker.stockConfirmTitle'),
      t(box.strNotRunning ? 'speaker.strNotRunningConfirmBody' : 'speaker.stockConfirmBody', { label: escapeHtml(label) }),
      { icon: null, confirmLabel: t('speaker.stockConfirmCta'), confirmClass: 'btn btn-primary' },
    );
    // Pin the clicked box as the setup target so Setup opens on the
    // network-install hero for THIS speaker (not an arbitrary one on a
    // multi-box LAN, #44). The install starts from the hero button, not here.
    if (ok) { state.setupTarget = { kind: 'stock', box }; switchView('setup'); }
    return;
  }
  selectBox(box);
}

// pickBoxFromShell is pickBox for the UI 2.0 sidebar speaker list. Unlike the Music
// tab's picker, the sidebar stays reachable while Speaker settings or Setup is
// open, so a pick there must bring the open view along: otherwise Settings
// would keep showing speaker A's form while its buttons act on B (they read
// state.settingsBox on click), and Setup would lose a chosen factory-reset
// target or the target of a running install without showing it.
async function pickBoxFromShell(box) {
  if (!box) return;
  const view = state.view;
  const prevSettings = state.settingsBox;
  const prevTarget = state.setupTarget;
  // A running install and a deliberately chosen factory reset keep their
  // target. Picking a stock speaker is itself a Setup choice (pickBox asks
  // first), so it may replace a factory reset, never a running install.
  const keepTarget = installRunActive()
    || (!!prevTarget && prevTarget.kind === 'factory-reset' && box.kind !== 'stock');
  await pickBox(box);
  if (keepTarget && state.setupTarget !== prevTarget) state.setupTarget = prevTarget;
  if (state.view !== view) return; // pickBox moved on (stock speaker into Setup)
  if (view === 'settings' && state.settingsBox !== prevSettings) {
    // The same reset the Settings picker's own change does.
    if (state.settingsReconnect && state.settingsReconnect.timer) clearTimeout(state.settingsReconnect.timer);
    state.settingsReconnect = null;
    loadBoxSettings();
  } else if (view === 'setup' && state.setupTarget !== prevTarget) {
    renderSetupTargetPicker();
  } else if (view === 'library') {
    // Its play and key actions name the speaker they act on.
    libraryFollowSpeaker();
  } else if (view === 'recent') {
    recentFollowsShellPick();
  }
}

// speakerPickedInTab keeps the tabs in lock-step in the other direction: a
// speaker picked in Settings or Setup becomes the music tab's active box too.
// Stock boxes cannot be driven from the music tab, so they only preselect the
// Settings/Setup pickers and leave the music selection alone.
function speakerPickedInTab(box) {
  if (!box) return;
  state.settingsBox = box;
  state.setupTarget = { kind: box.kind === 'stock' ? 'stock' : 'str', box };
  if (box.kind !== 'stock' && (!state.currentBox || state.currentBox.host !== box.host)) {
    selectBox(box);
  }
}

function selectBox(box) {
  // Clear the cached now-playing line when actually switching to a different
  // speaker, so the status bar does not show the previous box's track until the
  // first poll for the new box lands (#207). Re-selecting the same box must not
  // blank it on every call.
  const switched = !state.currentBox || state.currentBox.host !== (box && box.host);
  state.currentBox = box;
  // Stereo balance is per-speaker and only exists on the master of a pair, so
  // it is re-read whenever the selected speaker changes. Not on the status
  // poll: the speaker does not answer this one while it is asleep.
  setTimeout(() => { refreshBalance().catch(() => {}); }, 0);
  if (box && box.deviceID) saveLastBox(box.deviceID);
  // Tab-selection lock-step: the speaker picked here is preselected in the
  // Settings and Setup tabs too (their pickers re-render on every tab entry),
  // so switching tabs never lands on a different speaker than the one the
  // user was just controlling.
  if (box) {
    state.settingsBox = box;
    state.setupTarget = { kind: box.kind === 'stock' ? 'stock' : 'str', box };
  }
  state.presetErrors = {};
  if (switched) { resetNowPlaying(); renderNowPlayingBar(); }
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
  // The Pandora entry in Find stations belongs to this speaker's account.
  updatePandoraModeBtn();
  // Draw the input buttons this speaker actually has. A soundbar's TV and HDMI
  // sockets only appear once the speaker has listed them.
  refreshSourceButtons();
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
// speakerUpdateCardMuted reports whether the speaker-update CARD should stay
// away because the app itself is out of date.
//
// Both cards at once left people guessing which to act on first, and the wrong
// order is the harmful one: updating a speaker from an old app installs that
// old app's bundled speaker software, so the speaker is behind again the moment
// the app is updated. App first, speakers second.
//
// ONLY the card. The small "Update" next to a speaker and the mark on the
// Speaker Settings tab always stay: they report what state a speaker is IN, and
// a state indicator that comes and goes with an unrelated notice is worse than
// the ordering problem it was meant to solve. Hiding those was too broad.
function speakerUpdateCardMuted() {
  return !!state.appUpdateVersion;
}

function boxNeedsUpdate(b) {
  if (!b || b.kind === 'stock' || !b.version) return false;
  // Mid-update (post-OTA pin window): the agent restarts and cannot answer its
  // real version, so no update flag. This transient flag replaced the old
  // stamp of the APP version onto the record, which falsified the roster and
  // made a retry claim "already up to date" after an all-failed run (#775).
  if (b.otaPending) return false;
  const appVer   = state.appInfo && state.appInfo.version;
  const appBuild = state.appInfo && state.appInfo.build;
  if (!appVer) return false;
  // The binary itself, before any clock. A speaker that is already running the
  // exact agent this build carries is current, whatever the stamps say.
  //
  // v0.9.88 is why this comes first. Its release workflow took the build stamp
  // inside a three-leg matrix, so the app shipped stamped 2026-09-26-2023 while
  // the agent it embeds carried 2026-09-26-2022: one minute apart, same commit.
  // The comparison below then declared every speaker on earth out of date and
  // kept the badge lit no matter how often the user updated.
  // The binary the speaker RUNS, when it says. A speaker whose disk carries this
  // build but boots something older (#381) still needs help, so the on-disk hash
  // is only used when the agent is too old to report what it runs, where it is
  // still better than a stamp.
  const appSha = state.appInfo && state.appInfo.agentSha256;
  if (appSha && b.agentRunningSha256) {
    if (b.agentRunningSha256 === appSha) return false;
  } else if (appSha && b.agentBinarySha256 && b.agentBinarySha256 === appSha) {
    return false;
  }
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
  const n = state.boxes.filter(boxNeedsUpdate).length;
  btn.classList.toggle('has-update', n > 0);
  // The UI 2.0 sidebar shows how many (a no-op with the preview off).
  mirrorShellBadges({ settingsUpdates: n });
}

// updateMultiroomTabBadge shows how many groups exist right now (live zones
// plus stored permanent groups, see groupCount) as a small count on the
// Multi-Room tab, so a user sees at a glance that groups exist and shape which
// speakers play together, without opening the tab (Jens, 2026-09-07). Fed by
// the shared zone poll through onZoneLive, so it is right as soon as the first
// round lands and after every refresh path (music-tab poll, Multi-Room poll,
// optimistic group edits). Hidden at zero; no poll or timer of its own.
function updateMultiroomTabBadge() {
  const el = $('multiroomTabBadge');
  if (!el) return;
  const split = groupCountSplit(state.zoneLive, state.boxes);
  const n = split.total;
  el.hidden = n === 0;
  el.textContent = n ? String(n) : '';
  // A badge carries the colour of the state that produced it. A saved group
  // that is not currently formed is drawn as a dashed muted frame, so a count
  // made up only of those reads muted too; as soon as one group is actually
  // live the badge is brand again, matching the solid frames (Jens,
  // 2026-09-12: the blue count over a grey dashed frame did not read as the
  // same thing).
  el.classList.toggle('tab-badge-stored', n > 0 && split.live === 0);
  const tip = n ? t('nav.multiroomBadgeTitle', { n }) : '';
  el.title = tip;
  el.setAttribute('aria-label', tip);
  mirrorShellBadges({ groups: { n, stored: n > 0 && split.live === 0, tip } });
}
onZoneLive(updateMultiroomTabBadge);
// The UI 2.0 sidebar speaker list and the player read the same zone poll.
onZoneLive(() => renderShellSpeakers());
// A pair whose speakers lost the name STR stored for it (formed again with the
// field empty) gets it written back, so the Spotify picker and the phone remote
// match this app again (#1077, issue 5). Rides the same poll, no timer.
onZoneLive(() => { healPairNames(stereoPairsOf(state.zoneLive || {}), state.boxes).catch(() => {}); });
// A pair formed or undone while its half is selected switches the music slider
// between pair and single speaker without waiting for a speaker click.
// When the pair shows up after the slider was filled (the zone poll lands after
// the speaker's own settings), read both halves straight away rather than at
// the next status poll, so the slider reads the pair before it is touched.
onZoneLive(syncVolumeScopeOnZoneLive);

// A speaker stuck as half of a stereo pair that no longer exists (see
// stalestereo.js). Recomputed after every zone round from data the poll already
// has, no request of its own; the tracker makes a finding wait two rounds and
// ten seconds, so a pair being formed or dissolved never raises it.
const staleStereoTracker = new Map();
onZoneLive(() => {
  state.staleStereo = confirmStalePairs(findStalePairs(state.zoneLive || {}, state.boxes || []), staleStereoTracker);
});

// stereoStaleForError returns the stale-pair finding a failed play on host is
// explained by, or null: the agent said so itself (409 stereo-incomplete), or
// the app had already seen the speaker stuck in an old pair and the play ran
// into "not ready".
function stereoStaleForError(errStr, host) {
  const l = String(errStr || '').toLowerCase();
  const found = staleFindingFor(host, state.staleStereo);
  if (l.includes('stereo-incomplete')) {
    if (found) return found;
    const box = (state.boxes || []).find(b => b && b.host === host);
    // The agent's 409 names the partner (partnerIP) or at least the master.
    let ref = {};
    const m = String(errStr || '').match(/\{[^{}]*"error"\s*:\s*"stereo-incomplete"[\s\S]*\}/);
    if (m) { try { ref = JSON.parse(m[0]) || {}; } catch { ref = {}; } }
    const partner = resolveBoxByRef(ref.partnerIP || ref.partnerID || ref.master, state.boxes);
    return {
      holder: box || { host, name: host }, partner,
      partnerLabel: partner ? getBoxLabel(partner) : (ref.partnerIP || ''),
      reason: ref.reason === 'partner-gone' ? 'partner-gone' : 'partner-denies', pair: null,
    };
  }
  if (l.includes('box_not_ready') && found) return found;
  return null;
}

// foreignMod maps a leftover /mnt/nv directory name (as the agent reports it in
// foreignDirs / conflictingMod) to a human-readable name of the OTHER SoundTouch
// tool it belongs to, so the app can tell the user exactly what to remove to
// free NAND for the Spotify engine. Unknown names are shown verbatim.
const foreignMod = {
  aftertouch: 'AfterTouch',
  opentouchcloud: 'OpenTouchCloud',
  opentouch: 'OpenTouchCloud',
  bosman: 'Bosman',
  betterst: 'BetterST',
  sixback: 'SixBack',
  soundploy: 'SoundPloy',
};

// foreignSoftwareLabel turns a box version's foreignDirs/conflictingMod into a
// readable, de-duplicated list ("AfterTouch, OpenTouchCloud") for the
// "not enough space" message. Empty when the box carries no foreign leftovers.
function foreignSoftwareLabel(v) {
  const names = [];
  const push = (raw) => {
    if (!raw) return;
    String(raw).split(',').forEach((n) => {
      const key = n.trim().toLowerCase();
      if (!key) return;
      const label = foreignMod[key] || n.trim();
      if (!names.includes(label)) names.push(label);
    });
  };
  if (v) { push(v.conflictingMod); push(v.foreignDirs); }
  return names.join(', ');
}

// superviseUpdateIntent reports an update that never reached its goal. It does
// NOT act.
//
// Standing rule (Jens, 2026-07-29): a binary reaches a speaker only while an
// install or update is running, never from a background task. Anything that
// quietly pushed files onto speakers on its own is gone, because a speaker that
// rewrites itself when nobody asked is worse than one that is plainly out of
// date: the user cannot predict it, cannot see it, and cannot stop it.
//
// So all that is left of the old repair is telling the truth. The target was
// written down when the update started, and if the speaker never got there, the
// user is told once and decides.
const supervisedIntent = new Set();

async function superviseUpdateIntent(box) {
  if (!box || !box.host || box.kind === 'stock') return;
  if (state.otaInProgress) return;
  const key = box.host + ':' + box.port;
  if (supervisedIntent.has(key)) return;
  let pending;
  try { pending = await PendingUpdateIntent(box.host, box.port); } catch { return; }
  if (!pending || !pending.action) return;
  supervisedIntent.add(key);
  // One line, once per speaker per session, pointing at the button the user
  // would press anyway. No push, no reboot, no surprise. The engine case gets
  // its own words: "the update is unfinished" on a speaker whose version tile
  // looks current read as nonsense (field, 2026-08-29), when the truth was
  // simply a missing Spotify component the next update click delivers.
  const intentKey = pending.action === 'engine' ? 'update.engineMissing' : 'update.leftUnfinished';
  showToast(t(intentKey, { name: pending.name || getBoxLabel(box) }));
}

// checkBoxUpdate runs whenever a speaker is looked at. It used to render an
// update banner into #boxUpdateBanner as well, but that element left the music
// view when the speaker update moved into Speaker Settings (loadBoxSettings
// renders it there), so every path past the element lookup was dead code. What
// is left is the one thing that still matters here: the once-per-session hint
// for a speaker whose last update was left unfinished or lost its engine.
async function checkBoxUpdate() {
  if (!state.currentBox || !state.appInfo) return;
  superviseUpdateIntent(state.currentBox);
}

// --- OTA loop breaker (#381) -------------------------------------------------
// A box whose reported version never changes after a push used to get the
// full push+reboot cycle re-offered forever (the banner re-armed on every
// discovery). Remember per box how pushes of THIS app build ended; after a
// hard "cannot help to retry" classification or two unconfirmed cycles, the
// banner switches to a diagnostic message and re-pushing needs an explicit
// user override. Keyed to the app build so a NEW app version resets the state.
function otaStuckKey(box) {
  return 'otaStuck:' + (box.deviceId || box.host);
}
function readOTAStuck(box) {
  try {
    const rec = JSON.parse(localStorage.getItem(otaStuckKey(box)) || 'null');
    if (!rec || rec.build !== (state.appInfo && state.appInfo.build)) return null;
    return rec;
  } catch { return null; }
}
function noteOTAFailure(box, cls) {
  try {
    const build = state.appInfo && state.appInfo.build;
    const cur = readOTAStuck(box) || { build, count: 0 };
    const rec = { build, count: (cur.count || 0) + 1, cls, at: Date.now() };
    localStorage.setItem(otaStuckKey(box), JSON.stringify(rec));
  } catch {}
}
function clearOTAStuck(box) {
  try { localStorage.removeItem(otaStuckKey(box)); } catch {}
}
// otaStuck returns the record when the box is in the give-up state: one
// "re-pushing the same bytes cannot help" verdict, or 2+ unconfirmed cycles.
function otaStuck(box) {
  const rec = readOTAStuck(box);
  if (!rec) return null;
  if (rec.cls === 'landed-not-running' || rec.cls === 'swap-failed') return rec;
  if ((rec.count || 0) >= 2) return rec;
  return null;
}

// The update sequence itself lives in Go: desktop-app/otaorchestrate.go
// (RunSpeakerUpdate for one speaker, RunFleetUpdate for the house). It pushes
// the agent, polls the version, holds the stability window, classifies a miss,
// retries the one verdict a retry fixes, puts the Spotify engine back, and in
// a batch serialises the byte pushes through its upload gate. This file keeps
// what only a window can do: the pre-checks and their dialogs, the mapping of
// a phase to a sentence, and the rendering of the box:update:phase events.
//
// noteOTAResult feeds the loop breaker from a finished run: a confirmed agent
// clears the record, an unconfirmed one counts with the classifier's verdict.
// Only the result carries both, which is why this reads the result and not a
// phase event.
function noteOTAResult(box, result) {
  if (!result) return;
  if (result.confirmed) { clearOTAStuck(box); return; }
  if (result.outcome === 'timeout' || result.outcome === 'agentGone' || result.outcome === 'boxInSetup') {
    noteOTAFailure(box, result.verdict || '');
  }
}

// spotifyPhaseText words the engine step for the user. The row used to
// interpolate the speaker's raw "missing"/"present" report ("Spotify engine:
// missing, waiting for the target state"), English inside a German UI and a
// description of the speaker rather than of what is happening (Jens, fleet
// update 2026-09-06). Missing means the engine is about to be installed;
// present means it is being confirmed.
function spotifyPhaseText(d) {
  if (!d || !d.reachable) return t('updateAll.phase.spotifyUnreachable');
  return d.engine === 'present' ? t('updateAll.phase.spotifyChecking') : t('updateAll.phase.spotifyInstalling');
}

// showSt300PowerCycleNotice puts the SoundTouch 300 power-cycle instruction on
// screen until the user dismisses it. After an install or agent OTA the 300
// always sits blinking and unreachable until it is unplugged once; a toast
// vanished before people read it and they thought the soundbar was dead
// (Jens 2026-08-30), so this one step gets a modal, reusing the failure-report
// overlay host.
function showSt300PowerCycleNotice() {
  const host = $('updateFailureReport');
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
  const c = $('st300NoticeClose');
  if (c) c.onclick = () => { host.classList.add('hidden'); host.innerHTML = ''; };
}

// showUpdateFailureReport offers the user a copyable account of an update that
// did not reach its goal, gathered while the evidence is still there.
//
// A failure the user cannot describe is a failure that gets reported as "it
// does not work", and by the time anyone asks for a diagnostic the speaker has
// usually been restarted and the trail is cold. Everything relevant is
// collected at the moment it happens, shown in full so the user can read it
// before sending, and copyable in one click.
async function showUpdateFailureReport(box, phase, errMsg) {
  if (!box || !box.host) return;
  let report = '';
  try {
    report = await UpdateFailureReport(box.host, box.port, phase, errMsg,
      (state.appInfo && state.appInfo.version) || '');
  } catch { return; }
  if (!report) return;
  const host = $('updateFailureReport');
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
  const ta = $('failReportText');
  // Read the textarea rather than the closure: saving the diagnostic bundle
  // rewrites the closing block to name the file that was just written, and the
  // copied text has to carry that path, not the version from before the save.
  const currentText = () => (ta && ta.value) || report;
  const copy = $('failReportCopy');
  if (copy) copy.onclick = async () => {
    try { await navigator.clipboard.writeText(currentText()); }
    catch { if (ta) { ta.select(); document.execCommand('copy'); } }
    copy.textContent = t('update.reportCopied');
  };
  // The update modal gets the same one-click bundle as the install screens.
  // A failed OTA is the other moment a user has no idea where the diagnostic
  // file would be, and the report itself now points at this button.
  const saveLogs = $('failReportSaveLogs');
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
  const mail = $('failReportMail');
  if (mail) mail.onclick = () => {
    try {
      BrowserOpenURL('mailto:str@sichtbar-app.de?subject=' +
        encodeURIComponent('ST Reborn update failed') + '&body=' + encodeURIComponent(currentText()));
    } catch {}
  };
  const close = $('failReportClose');
  if (close) close.onclick = () => host.classList.add('hidden');
}

// BOX_BUSY_TOKEN is the marker the one-write-per-speaker guard puts on its
// refusal (desktop-app/boxbusy.go). Matched instead of the prose, which is free
// to be reworded and translated.
const BOX_BUSY_TOKEN = 'STR_BUSY:';

// Re-entrancy latch for the single-speaker update, set BEFORE the first await.
//
// state.otaInProgress is only raised after the stick gate and the Wi-Fi
// preflight, two network round-trips further down, so a second click inside
// that window sailed straight past the check at the top. Both runs then reached
// the backend, the second was correctly refused, and the REFUSED one ran the
// shared teardown and put a failure report on screen for the update that was
// running fine (Sascha, SoundTouch 30, 2026-09-14). The whole-house path
// learned this in August, see updateAllBusy; this one had not.
let boxUpdateBusy = false;

async function doBoxUpdate(targetBox) {
  // Both latches, not just this one. The whole-house run calls RunFleetUpdate
  // directly so it does not contend here, but it holds the single-box global
  // lock for the entire batch, and a single-speaker click during it would walk
  // into the same duplicate-start the latch exists to stop.
  if (boxUpdateBusy || updateAllBusy) {
    showToast(t('update.alreadyRunning', { name: state.otaTargetName || t('updateAll.batchLabel') }));
    return;
  }
  boxUpdateBusy = true;
  try {
    return await runSingleBoxUpdate(targetBox);
  } finally {
    boxUpdateBusy = false;
  }
}

async function runSingleBoxUpdate(targetBox) {
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
  // Say so out loud: returning silently made a second press look like a dead
  // button and invited a third (live, 2026-07-27).
  if (state.otaInProgress) {
    showToast(t('update.alreadyRunning', { name: state.otaTargetName || t('common.unknown') }));
    return;
  }

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
    // Only a real WIFI_INTERFACE reading gates the update: that class comes
    // from the firmware live. Coprocessor boxes (ETHERNET-presented Wi-Fi)
    // carry only the agent's last gabbo snapshot - a display hint, not a
    // current measurement, and it false-alarmed on a Portable sitting next
    // to the router (2026-07-12).
    const conn = ifs.find(i => i.type === 'WIFI_INTERFACE' && i.state === 'NETWORK_WIFI_CONNECTED');
    const sig = conn && conn.signal;
    if (sig === 'MARGINAL_SIGNAL' || sig === 'POOR_SIGNAL') {
      const ok = await confirmWarn(t('update.weakWifiTitle'), t('update.weakWifiBody'));
      if (!ok) return; // user chose to improve the signal first
    }
  } catch { /* signal unknown: do not block the update */ }

  // Storage pre-flight (v0.9.7, #381/#119): a tight NAND makes the update the
  // riskiest thing the app can do to a speaker — on rollout day two boxes went
  // down mid-update with 5-8 MB free while the app proceeded silently. The
  // user stays in control: show the real numbers and what will happen (more
  // than one restart, Spotify engine reinstalled after the restart) and let
  // them decide. An unknown free figure (older agent) never blocks.
  //
  // The verdict AND the numbers come from BoxStoragePreflight, i.e. from the
  // same Go gate the push itself goes through. This used to be computed here
  // in JavaScript and it disagreed with the Go side twice: it compared the raw
  // embedded agent size instead of the compression-credited need, so a user was
  // told his update needed 13.9 MB when the real need was about 10.7 MB
  // (screenshot, 2026-08-22), and it credited a present engine only when the
  // agent reported goLibrespotSizeBytes, so an older agent with a full engine
  // on the box got no reclaim credit at all. Nothing is derived here any more:
  // this renders what the gate says.
  try {
    const pf = await BoxStoragePreflight(targetBox.host, targetBox.port);
    if (pf && pf.tight) {
      const fmtMB = (n) => ((n || 0) / 1048576).toFixed(1);
      // Only one thing here is ever the user's to act on: other SoundTouch
      // software occupying the storage. Name it when it is there, because then
      // the warning carries an instruction instead of a decision they have no
      // basis to make. The label mapping stays here; the preflight carries the
      // agent's raw conflictingMod/foreignDirs fields through untouched.
      const foreign = foreignSoftwareLabel(pf);
      const ok = await confirmWarn(
        t('update.tightNandTitle'),
        foreign
          ? t('update.tightNandNamedBody', { freeMB: fmtMB(pf.freeBytes), needMB: fmtMB(pf.needBytes), software: foreign })
          : t('update.tightNandBody', { freeMB: fmtMB(pf.freeBytes), needMB: fmtMB(pf.needBytes) })
      );
      if (!ok) return; // user cancelled: no OTA, no reboot
    }
  } catch { /* headroom unknown: do not block the update */ }

  // Drive both update buttons together (banner up top + stick info section)
  const buttons = () => ['boxUpdateBtn', 'stickInfoUpdateBtn'].map(id => $(id)).filter(Boolean);
  // Mutate the DOM buttons only when the user is still LOOKING at
  // the box being updated. If they switched to another box,
  // checkBoxUpdate() has rendered a fresh button for that other
  // box — overwriting it with our progress text would lie about
  // what the other box is doing. The state.otaTargetHost guard
  // below in checkBoxUpdate() takes care of rendering the right
  // "Update running on <name>" label there.
  // "Looking at the target box" means the SETTINGS view (where the update
  // buttons and progress live since they moved out of the music view) or the
  // music selection. Comparing against state.currentBox alone was a leftover
  // from the music-view era: updating any speaker other than the currently
  // selected one silently suppressed every progress line, so the settings
  // page just sat there and users pressed Update again (live, 2026-07-27).
  const lookingAtTarget = () => {
    const t = state.otaTargetHost;
    if (!t) return false;
    return (state.settingsBox && state.settingsBox.host === t) ||
           (state.currentBox && state.currentBox.host === t);
  };
  // The progress text used to be written INTO the button. These messages are
  // whole sentences (up to 124 characters in German), so a btn-mini wrapped
  // them over several lines, grew to fit, and shoved the surrounding layout
  // around every time the phase changed. A button is a label; the status
  // belongs beside it. Both buttons now have an .op-status line under them
  // with a reserved height, so the panel stays still while the text changes.
  const statusLines = () => ['boxUpdateStatus', 'stickInfoUpdateStatus'].map(id => $(id)).filter(Boolean);
  const setStatus = (text) => {
    if (!lookingAtTarget()) return;
    // Idempotent: only touch the DOM when a value actually changes. The 1 s
    // countdown tick called this every second during the post-update wait, and
    // re-setting `disabled` each time re-triggered the button's CSS transition,
    // which read as a per-second flicker through the whole 2nd (Spotify) upload.
    const busy = t('update.runningBtn');
    buttons().forEach(b => {
      if (b.textContent !== busy) b.textContent = busy;
      if (!b.disabled) b.disabled = true;
    });
    statusLines().forEach(el => {
      if (el.textContent !== text) el.textContent = text;
      // The full sentence stays reachable on hover for anything the two
      // reserved lines cannot hold.
      if (el.title !== text) el.title = text;
    });
  };
  const reset = () => {
    if (!state.currentBox || state.currentBox.host !== state.otaTargetHost) return;
    buttons().forEach(b => { b.disabled = false; b.textContent = t('update.refreshBtnSpeaker'); });
    statusLines().forEach(el => { el.textContent = ''; el.title = ''; });
  };
  // Mark this box as the OTA target AND flip the global in-flight
  // flag BEFORE first setStatus() so checkBoxUpdate() and the
  // setStatus guard both see a consistent (target, in-flight)
  // pair at every point in this flow. Reset together in finally{}.
  //
  // What this speaker is supposed to end up running is written down inside
  // RunSpeakerUpdate, before any byte moves, and the backend reports itself
  // busy for the whole run so the window asks before closing. Neither is done
  // here any more; the Go side owns both for the headless harness too.
  state.otaTargetHost = targetBox.host;
  state.otaTargetName = getBoxLabel(targetBox);
  // Suppress the SSH "remove stick and reboot" banner for the whole
  // OTA window. The agent restarts mid-OTA and SSH is briefly open
  // during that restart; the banner's "Reboot now" button would
  // interrupt the agent exec and may leave the box half-flashed.
  state.otaInProgress = true;
  syncShellPlayer(); // UI 2.0: volume and transport follow the OTA gate
  setStatus(t('update.uploading'));
  // The post-reboot Spotify engine delivery streams its ~16 MB through this
  // same channel. It does NOT restart the speaker (a current agent swaps the
  // engine in place), so the progress line must not promise one.
  let engineStreaming = false;
  // Live upload progress + throughput while the ~10 MB agent streams to the box,
  // so a slow link shows movement instead of a frozen "Uploading...".
  const offBoxUp = EventsOn('box:update:progress', (p) => {
    if (!p || typeof p !== 'object' || p.pct == null || p.pct < 0) return;
    // At 100% the whole binary is on the box; it now writes NAND and reboots,
    // and its reply is usually lost in that reboot (BCO/taigan especially), so
    // the backend's UpdateBoxAgent call hangs another ~minute. Flip the label
    // to "restarting" the moment the upload completes instead of leaving the
    // user on "uploading" through the reboot the app cannot yet see.
    if (p.pct >= 100 && !engineStreaming) { setStatus(t('update.rebooting')); return; }
    const rate = p.bytesPerSec ? ' (' + fmtRate(p.bytesPerSec) + ')' : '';
    setStatus(t('update.uploadingPct', { pct: p.pct }) + rate);
  });
  checkSshBanner();
  // Swap the banner heading to "updating" right away (the otaHere branch in
  // checkBoxUpdate), so it no longer reads "update available" while the OTA runs.
  checkBoxUpdate();
  let boxWasTouched = false;
  // From here on the speaker itself is involved: anything that fails after the
  // transfer started may leave it mid-restart, which is when the power-cycle
  // advice below is genuinely useful. Everything BEFORE this point (no embedded
  // agent in this build, box unreachable, a gate the user cancelled) never
  // touched the speaker, and telling those users to pull the plug is both
  // pointless and alarming.
  boxWasTouched = true;
  // The phases arrive from the Go sequence, one event per change and at least
  // one per second while a countdown runs, so the remaining time counts down
  // smoothly with no timer of our own: it is formatted here from remainingMs.
  let uploadedToastShown = false;
  const offPhase = EventsOn('box:update:phase', (d) => {
    if (!d || typeof d !== 'object' || d.host !== targetBox.host) return;
    switch (d.phase) {
      case 'uploading':
        setStatus(withStep(t('update.uploading'), UPDATE_STEPS.send));
        break;
      case 'rebooting':
        // The binary is on the speaker now. Said once, at the moment it
        // becomes true, so the user knows the transfer is behind them: the
        // agent detaches, waits out TIME_WAIT on its listener ports (see
        // internal/webui handleAgentUpdate) and only then execs the new
        // binary, so the speaker is away for minutes with nothing to show.
        if (!uploadedToastShown) { uploadedToastShown = true; showToast(t('update.uploadedToast')); }
        setStatus(withStep(t('update.rebooting'), UPDATE_STEPS.restart));
        break;
      case 'verifying':
        setStatus(withStep(t('update.waitingForSpeaker', { remaining: formatRemaining(d.remainingMs) }), UPDATE_STEPS.restart));
        break;
      // The speaker IS back on the new version and is only being held for the
      // stability window before we believe it (a BCO box can reboot a second
      // time on its own). Saying "restarting" through that window reads as if
      // the app had not noticed the speaker was back.
      case 'settling':
        setStatus(withStep(t('updateAll.phase.settling', { remaining: formatRemaining(d.remainingMs) }), UPDATE_STEPS.confirm));
        break;
      case 'retrying':
        uploadedToastShown = false;
        showToast(t('update.retrying'));
        setStatus(t('update.retrying'));
        break;
      case 'confirmed':
        // Not "update done": the Spotify-engine half may still follow (the
        // grey Updating button stays for it), and calling the whole update
        // done here while the card visibly kept working confused a reporter
        // (#672). The single final "done" comes when the flow resolves.
        showToast(t('update.agentDoneToast'));
        break;
      case 'engineQueued': setStatus(withStep(t('updateAll.phase.engineQueued'), UPDATE_STEPS.engine)); break;
      case 'engineUploading':
        engineStreaming = true;
        setStatus(withStep(t('updateAll.phase.engineUploading'), UPDATE_STEPS.engine));
        break;
      case 'spotify':
        engineStreaming = false;
        setStatus(t('update.spotifyFinalStep', { remaining: formatRemaining(d.remainingMs) }));
        break;
    }
  });
  try {
    // One speaker and a whole house run the SAME sequence, in Go. This path
    // only differs in how it reports - a button, a status line and toasts
    // instead of the overlay's rows.
    const result = await RunSpeakerUpdate(targetBox.host, targetBox.port, targetBox.deviceID || '', getBoxLabel(targetBox));
    noteOTAResult(targetBox, result);
    const confirmedVer = (result && result.version) || null;
    const confirmed = !!confirmedVer;
    if (result && result.outcome === 'done') {
      // THE done moment: everything, engine half included, is finished. One
      // toast, here and only here (#672); the confirm phase above announced
      // only the speaker-software half.
      if (!result.quiet) showToast(result.engineDelivered ? t('update.spotifyDoneToast') : t('update.doneToast'));
    } else if (result && result.outcome === 'partial') {
      // Agent updated, Spotify engine outstanding.
      if (result.engineTooFull) {
        // Retrying cannot help, only freeing space can. Name the OTHER
        // SoundTouch software eating the NAND so the user knows what to remove.
        // With no name (nothing foreign reported, or no version to read) there
        // is nothing to point at, so that branch asks for the diagnostic logs
        // rather than sending the owner of a clean speaker hunting for add-ons
        // that are not there (user mail, 2026-08-22).
        const foreign = foreignSoftwareLabel(confirmedVer);
        showToast(foreign
          ? t('spotify.engineTooFullNamed', { software: foreign })
          : t('spotify.engineTooFull'));
      } else {
        // No silent retry: the speaker's own screen now carries a visible
        // "Install Spotify engine" action.
        showToast(t('spotify.engineDeferredVisible'));
      }
    } else if (result && result.outcome === 'boxInSetup') {
      // The speaker was busy with its own setup. Nothing is wrong with the
      // network and nothing needs pulling out of the wall.
      showToast(t('update.boxInSetupNote', { name: getBoxLabel(targetBox) }));
    } else if (result && result.outcome === 'agentGone') {
      // The speaker is up and answering its own Bose web API; only STR is not
      // running on it. That has one exact fix and the user can do it in ten
      // seconds, so say it plainly instead of "took longer than expected".
      showToast(t('update.agentGoneToast', { name: getBoxLabel(targetBox) }));
    } else {
      // Timed out. The Go sequence has already journaled the verdict,
      // classified why, retried the one class of failure a retry actually
      // fixes, and noteOTAResult fed the loop breaker, so all that is left
      // here is to say so.
      showToast(t('update.tookLongerToast'));
    }
    // The SoundTouch 300 drops into its blinking update-pending state after an
    // OTA and needs a manual power-cycle to finish; the agent cannot clear it, so
    // tell the user (#ST300 blink, Michal + Jens 2026-07-16). As a modal, not a
    // toast: it stays up until read (Jens 2026-08-30).
    if ((targetBox.model || '').includes('300')) showSt300PowerCycleNotice();
    // Refresh app state regardless of confirmation so the user sees current
    // truth (either updated or still in OTA). This is BEST-EFFORT and must never
    // surface as "Update failed": the box is typically still mid-reboot here, so
    // discoverBoxes / loadBoxSettings hit it while it is unreachable and can throw
    // "context deadline exceeded (... while reading body)". That throw used to land
    // in the outer catch and report a failed update even though the upload
    // succeeded and the version poll already decided the real outcome (reported by
    // the toast above). So swallow refresh errors here.
    try {
      await discoverBoxes();
      // Force the confirmed new version onto the box record(s) so the view
      // shows the updated version immediately instead of a stale "outdated"
      // glitch until the next clean discovery cycle (Jens 2026-06-01:
      // after OTA the screen kept the old version until a manual refresh).
      // This also overrides a discovery-stickiness cache entry that might
      // still carry the pre-OTA version for a box that just rebooted.
      if (confirmed) {
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
    } catch (refreshErr) {
      try { console.warn('post-update refresh failed (non-fatal, box likely still rebooting)', refreshErr); } catch {}
    }
    reset();
  } catch (e) {
    // A timeout-class rejection ("context deadline exceeded ... while reading
    // body", common on slow links or with an HTTP-inspecting security suite like
    // Norton) does NOT mean the OTA failed: the box may still be applying it and
    // rebooting. Show an actionable "still working" hint and let the user
    // re-check the version shortly, instead of a raw Go error toast that two
    // reporters hit while their speaker actually updated fine.
    const msg = String(e);
    if (msg.includes(BOX_BUSY_TOKEN)) {
      // The backend turned this start away because a write is already running
      // on this speaker. Nothing failed: the run that IS going is fine, and its
      // owner must not be handed an error report with a copyable Go error for
      // an update that finishes a minute later (Sascha, ST30, 2026-09-14).
      showToast(t('update.alreadyRunning', { name: getBoxLabel(targetBox) }));
    } else if (/deadline exceeded|client\.timeout|while reading body/i.test(msg)) {
      showToast(t('update.stillWorking'));
    } else {
      showError(boxWasTouched
        ? t('update.failed', { err: msg })
        : t('update.failedNoChange', { err: msg }));
      // The update could not put this speaker into the state it was meant to
      // reach, so hand the user everything needed to report it instead of
      // making them describe a failure they cannot see.
      if (noNetworkHere(msg)) {
        // The app already knows this: the same predicate greys the speaker
        // list out for it. The update path just never asked, so pulling the
        // router mid-update produced a full copyable fault report about a
        // speaker that was fine, and the reporter had to work out for
        // themselves that it was their own router (#963).
        showUpdateNoNetworkNotice();
      } else {
        showUpdateFailureReport(targetBox, 'update', msg);
      }
    }
    reset();
  } finally {
    if (typeof offPhase === 'function') offPhase();
    if (typeof offBoxUp === 'function') offBoxUp();
    // Always clear the OTA-in-flight gate so the SSH banner can
    // come back if it still applies, even if we threw mid-poll.
    state.otaInProgress = false;
    syncShellPlayer(); // UI 2.0: volume and transport follow the OTA gate
    state.otaTargetHost = null;
    state.otaTargetName = null;
    checkSshBanner();
    // Force a re-render of the current view's update button so any
    // other-box "Update running on …" placeholder is replaced with
    // the regular Update button immediately.
    checkBoxUpdate();
  }
}

// updateAllBoxes updates every eligible speaker in one click with a live
// per-box multi-progress overlay. Built for a household with several speakers,
// some on weak Wi-Fi: it asks ONCE up front, then hands the whole list to
// RunFleetUpdate, which starts every speaker at once and serialises only the
// byte pushes (so concurrent ~16 MB streams do not saturate a weak shared AP
// and trip the 60 s upload-stall watchdog), and never lets one slow/failed box
// block the rest. Calls the Go batch directly (not doBoxUpdate) so it does not
// contend on the single-box global lock, which it holds for the whole batch.
//
// Returns true when a batch was actually started, false when it backed out
// (already running, nothing to do, user cancelled). Callers that took a button
// or a card off screen for it need that answer to put it back. onStart fires
// the moment the batch is committed, for callers that want to get out of the
// way then rather than minutes later when the whole batch is done.
// Re-entrancy latch, set BEFORE the first await and cleared in the finally at
// the very bottom. state.otaInProgress is not enough: it is only raised AFTER
// the confirmation, and everything before it (a version read per speaker, a
// stick check and a Wi-Fi read per target) is seconds of network work during
// which a second click sails straight past that check. Two overlapping runs
// then queue two confirmations on a modal that keeps ONE resolver, so the
// second prompt arrived after the batch was already done and asked the user to
// update speakers they had just updated (Jens, 2026-08-22).
let updateAllBusy = false;
async function updateAllBoxes(onStart) {
  // Saying so out loud, not returning silently: a second press on a dead-looking
  // button is exactly what the single-box path already learned to answer.
  if (updateAllBusy || boxUpdateBusy || state.otaInProgress) {
    showToast(t('update.alreadyRunning', { name: state.otaTargetName || t('updateAll.batchLabel') }));
    return false;
  }
  updateAllBusy = true;
  try {
    return await runUpdateAllBoxes(onStart);
  } finally {
    updateAllBusy = false;
  }
}

// runUpdateAllBoxes is the body; updateAllBoxes above is the guard around it.
async function runUpdateAllBoxes(onStart) {
  // Offline speakers are excluded up front: probing them burns the pre-flight
  // budget, and counting them as "nothing to do" is how an all-offline fleet
  // produced "already up to date" (#775).
  const candidates = (state.boxes || []).filter(b => b && b.kind !== 'stock' && b.host && !b.offline && !otaStuck(b));
  // Everything from here to the confirmation is network work: one version read
  // per speaker, then a stick check and a Wi-Fi read per update target. On a
  // busy LAN, or right after start while discovery is still sweeping, that is
  // several seconds in which the click produced nothing at all on screen. Keep
  // a line up for the whole pre-flight so the app is visibly working.
  showToast(t('updateAll.checking'), 0);
  // Ask every speaker that is NOT behind whether it still has its Spotify
  // engine, so one that quietly lost it is repaired by this run instead of
  // being skipped (splitUpdateTargets carries the reasoning). One version read
  // per speaker; one in deep standby does not answer and is left alone, as a
  // read must.
  const rows = [];
  for (const b of candidates) {
    const needsUpdate = boxNeedsUpdate(b);
    let engineMissing = false;
    if (!needsUpdate) {
      try {
        const v = await BoxAgentVersion(b.host, b.port);
        engineMissing = !!(v && v.goLibrespot === 'missing');
      } catch { /* asleep or unreachable: leave it untouched */ }
    }
    rows.push({ box: b, needsUpdate, engineMissing });
  }
  const { updateTargets, engineTargets, targets } = splitUpdateTargets(rows);
  if (targets.length === 0) {
    hideToast();
    // "Already up to date" is only an honest claim when reachable speakers
    // were actually inspected; a fleet that is offline gets the truth (#775).
    const anyOnline = (state.boxes || []).some(b => b && b.kind !== 'stock' && !b.offline);
    showToast(anyOnline ? t('updateAll.noneToUpdate') : t('speakerUpdate.notReachable'));
    return false;
  }
  // Hosts that only need the engine put back, not a whole agent update.
  const engineOnly = new Set(engineTargets.map(b => b.host));

  // Pre-scan for sticks / weak Wi-Fi so the user is warned ONCE up front, not
  // once per speaker (the per-box prompts the single-box path shows).
  const notes = [];
  for (const b of updateTargets) {
    try {
      const r = await boxFetch(b, '/api/stick/status');
      if (r.ok) { const d = await r.json(); if (d && d.mounted) notes.push(t('updateAll.noteStick', { name: getBoxLabel(b) })); }
    } catch { /* stick status unknown: do not block */ }
    try {
      const s = await BoxSettings(b.host, b.port);
      const ifs = (s && s.network && s.network.interfaces) || [];
      // Firmware-reported Wi-Fi class only, same rule as doBoxUpdate: the
      // coprocessor boxes' agent-relayed snapshot is not a live measurement.
      const conn = ifs.find(i => i.type === 'WIFI_INTERFACE' && i.state === 'NETWORK_WIFI_CONNECTED');
      const sig = conn && conn.signal;
      if (sig === 'MARGINAL_SIGNAL' || sig === 'POOR_SIGNAL') notes.push(t('updateAll.noteWeakWifi', { name: getBoxLabel(b) }));
    } catch { /* signal unknown: do not block */ }
    try {
      // Storage, the one thing this pre-scan never looked at. The single-box
      // update has asked since #ST30 and stops to confirm when it is tight;
      // the batch wrote the same speakers without ever mentioning it, so the
      // owner of a tight box got a warning or no warning depending purely on
      // which button he pressed. Same Go gate, so the two paths cannot drift:
      // nothing is computed here, this renders what BoxStoragePreflight says.
      //
      // A note and not a confirm: the batch already asks once for the whole
      // run, and the update is the right thing to do anyway. Tight NAND costs
      // a re-delivered engine after the reboot, which the flow handles by
      // itself, so the user needs to KNOW rather than to decide.
      const pf = await BoxStoragePreflight(b.host, b.port);
      if (pf && pf.tight) {
        const mb = (n) => ((n || 0) / 1048576).toFixed(1);
        notes.push(t('updateAll.noteTightNand', {
          name: getBoxLabel(b), freeMB: mb(pf.freeBytes), needMB: mb(pf.needBytes),
        }));
      }
    } catch { /* headroom unknown (older agent): never block a batch */ }
  }
  hideToast();
  // The prompt used to be one plain string with "\n" separators pushed through
  // innerHTML, where HTML collapses newlines: four speakers plus two notes
  // arrived as a single unbroken paragraph under a red warning triangle and a
  // red "proceed anyway" button. Updating the speakers is the normal,
  // recommended thing to do, so it gets a calm heading, one short sentence per
  // group, the speakers as an actual list, and a primary "update now" button.
  // Names come from the speakers themselves and are escaped: they end up in
  // innerHTML.
  const nameList = (arr, cls) =>
    `<ul${cls ? ` class="${cls}"` : ''}>` +
    arr.map(x => `<li>${escapeHtml(typeof x === 'string' ? x : getBoxLabel(x))}</li>`).join('') +
    '</ul>';
  // Two groups with two different promises: the first restarts, the second
  // normally does not. Saying which is which up front is the difference between
  // a run the user can predict and one that surprises them.
  let body = '';
  if (updateTargets.length) {
    body += `<p>${escapeHtml(t('updateAll.confirmLead'))}</p>`
      + nameList(updateTargets);
  }
  if (engineTargets.length) {
    body += `<p>${escapeHtml(t('updateAll.confirmLeadEngine'))}</p>`
      + nameList(engineTargets);
  }
  if (notes.length) body += nameList(notes, 'warn-notes');
  const confirmTitle = updateTargets.length ? t('updateAll.confirmTitle') : t('updateAll.confirmTitleEngine');
  const confirmed = await confirmWarn(confirmTitle, body, {
    icon: null,
    calm: true,
    rich: true,
    confirmLabel: updateTargets.length ? t('updateAll.confirmBtn') : t('updateAll.confirmBtnEngine'),
    confirmClass: 'btn btn-primary',
  });
  if (!confirmed) return false;
  // The prompt can sit on screen for a while. Anything that started an OTA in
  // the meantime (the single-speaker button, a resumed update intent) owns the
  // speakers now, so do not push a second batch over it.
  if (state.otaInProgress) {
    showToast(t('update.alreadyRunning', { name: state.otaTargetName || t('updateAll.batchLabel') }));
    return false;
  }
  try { if (onStart) onStart(); } catch { /* caller's UI concern only */ }

  // Hold the single-box global lock for the whole batch: the single-speaker
  // buttons then render "update running" and the SSH "remove stick" banner stays
  // suppressed. The sentinel host makes every real box's button show that state.
  state.otaInProgress = true;
  syncShellPlayer(); // UI 2.0: volume and transport follow the OTA gate
  state.otaTargetHost = '__batch__';
  state.otaTargetName = t('updateAll.batchLabel');
  checkSshBanner();
  checkBoxUpdate();

  // Build one overlay row per box.
  const rowState = new Map(); // host -> { box, el, barFill, phaseEl, outcome }
  const listEl = $('uaList');
  if (listEl) listEl.innerHTML = '';
  for (const b of targets) {
    const row = document.createElement('div');
    row.className = 'ua-row';
    row.innerHTML = `
      <div class="ua-row-head">
        <span class="ua-name">${escapeHtml(getBoxLabel(b))}</span>
        <span class="ua-model">${escapeHtml(b.model || '')}</span>
        <span class="ua-status" data-role="phase">${escapeHtml(t('updateAll.phase.queued'))}</span>
      </div>
      <div class="ua-bar"><div class="ua-bar-fill" data-role="bar"></div></div>`;
    if (listEl) listEl.appendChild(row);
    rowState.set(b.host, { box: b, el: row, barFill: row.querySelector('[data-role=bar]'), phaseEl: row.querySelector('[data-role=phase]'), outcome: null });
  }
  const counts = () => {
    let done = 0, fail = 0, defer = 0, busy = 0;
    for (const r of rowState.values()) {
      if (r.outcome === 'done') done++;
      else if (r.outcome === 'failed') fail++;
      else if (r.outcome === 'agentGone') fail++;
      // A speaker in its own setup is deferred, not failed: it is very likely
      // updated and simply off the network while the firmware finishes.
      else if (r.outcome === 'partial' || r.outcome === 'timeout' || r.outcome === 'boxInSetup') defer++;
      else busy++;
    }
    return { done, fail, defer, busy };
  };
  const renderSummary = () => {
    const c = counts();
    const sum = $('uaSummary');
    if (sum) sum.textContent = t('updateAll.summary', { done: c.done, deferred: c.defer, failed: c.fail, total: targets.length });
  };
  // Row visuals, three live states and no marching bar anywhere: a row that
  // WAITS for its turn (queued behind another speaker) shows an empty track and
  // faint text; a row whose speaker is BUSY on its own (rebooting, settling,
  // the engine being confirmed) shows a still, dim fill that breathes slowly;
  // only a real transfer moves a bar, driven by its byte progress. Six rows of
  // sliding stripes competed with the one transfer that was actually running
  // and hid it (Jens, fleet update 2026-09-06).
  const setRow = (host, { phaseText, pct, barClass, wait, busy } = {}) => {
    const r = rowState.get(host); if (!r) return;
    if (phaseText != null && r.phaseEl) r.phaseEl.textContent = phaseText;
    if (pct != null && r.barFill) { r.barFill.style.width = Math.max(0, Math.min(100, pct)) + '%'; r.el.classList.remove('ua-wait', 'ua-busy'); }
    if (wait) { r.el.classList.remove('ua-busy'); r.el.classList.add('ua-wait'); }
    if (busy) { r.el.classList.remove('ua-wait'); r.el.classList.add('ua-busy'); }
    if (barClass) { r.el.classList.remove('ua-wait', 'ua-busy', 'ua-done', 'ua-failed', 'ua-defer'); r.el.classList.add(barClass); }
  };

  const overlay = $('updateAllOverlay');
  if (overlay) overlay.classList.remove('hidden');
  renderSummary();
  // Route the host-tagged live byte-progress to the right row's bar.
  const offProg = EventsOn('box:update:progress', (p) => {
    if (!p || typeof p !== 'object' || !p.host || p.pct == null || p.pct < 0) return;
    // An engine-only repair streams its bytes through this same channel, but a
    // current agent swaps the engine in place and does NOT restart. Its row
    // therefore only follows the bar and never claims a restart it will not make.
    if (engineOnly.has(p.host)) { setRow(p.host, { pct: p.pct }); return; }
    // At 100% the binary is on the box and it reboots; its reply is often lost
    // in that reboot, so the sequence's own 'rebooting' phase does not fire for
    // another ~minute. Flip the row to "restarting" on upload completion so it
    // does not sit at "uploading" through a reboot the app cannot yet see.
    if (p.pct >= 100) { setRow(p.host, { phaseText: withStep(t('updateAll.phase.rebooting'), UPDATE_STEPS.restart), busy: true }); return; }
    // The first byte is what turns a queued row into an uploading one: the
    // 'uploading' phase itself fires before the batch gate, while the row is
    // still waiting for another speaker's transfer to finish.
    setRow(p.host, { phaseText: withStep(t('updateAll.phase.uploading'), UPDATE_STEPS.send), pct: p.pct });
  });
  const mini = $('uaMini');
  // Reopening is the whole point of the strip, so it is a button, not a label.
  if (mini) mini.onclick = () => { mini.classList.add('hidden'); if (overlay) overlay.classList.remove('hidden'); };
  const showMiniIfStillRunning = () => {
    if (!mini) return;
    const c = counts();
    // c.fail and c.defer, not c.failed/c.deferred: counts() has never returned
    // those names, so the sum was NaN, `left <= 0` was false (every NaN
    // comparison is), and closing the panel mid-run put up a strip reading
    // "Still updating NaN speaker(s)" (reported with a bundle, 2026-09-15).
    //
    // targets.length, not rows.length: rows holds every speaker the run
    // INSPECTED, including the ones that turned out to need nothing. Counting
    // those made the strip claim more speakers were still going than were ever
    // being updated. renderSummary already uses targets.length for this reason.
    const left = targets.length - (c.done + c.fail + c.defer);
    if (left <= 0) { mini.classList.add('hidden'); return; }
    mini.textContent = t('updateAll.stillRunning', { n: left });
    mini.classList.remove('hidden');
  };
  if ($('uaClose')) {
    $('uaClose').onclick = () => {
      if (overlay) overlay.classList.add('hidden');
      showMiniIfStillRunning();
    };
  }

  // The batch writes software to speakers exactly like the single update,
  // so it carries the same guarantees: the window asks before closing for
  // the whole run, every speaker's target is written down before its turn,
  // and a speaker that does not get there produces the copyable report
  // instead of a red row nobody can act on. The sequence, the upload gate
  // that serialises only the byte pushes, and the honesty re-check all run in
  // Go (RunFleetUpdate); what follows renders its phases into the rows.

  let uaReportShown = false;
  // finishRow is a row's final word, from the terminal phase event or, if the
  // window missed it, from the returned row. The engine-only rows have their
  // own texts: nothing was written to those speakers but the engine, and a
  // current agent hot-swaps it, so they never claim a restart.
  const finishRow = (b, d) => {
    const outcome = (d && d.outcome) || 'failed';
    const err = String((d && d.err) || '');
    if (engineOnly.has(b.host)) {
      if (outcome === 'done') {
        setRow(b.host, { phaseText: t('updateAll.phase.engineDone'), pct: 100, barClass: 'ua-done' });
      } else if (outcome === 'partial' && d.unmet === 'no-embedded-engine') {
        // This app build carries no engine, so there was nothing to deliver.
        setRow(b.host, { phaseText: t('updateAll.phase.deferred'), pct: 100, barClass: 'ua-defer' });
      } else if (outcome === 'partial') {
        // Delivered, and gone again to a reboot right after: the same honesty
        // check the update path does, so the row does not go green on it.
        setRow(b.host, { phaseText: t('updateAll.phase.engineMissing'), pct: 100, barClass: 'ua-defer' });
      } else if (d.engineTooFull) {
        // A full NAND is not something to retry blindly, so it gets its own
        // row text instead of a bare "failed"; the speaker's own screen names
        // the other SoundTouch software eating the space.
        setRow(b.host, { phaseText: t('updateAll.phase.engineTooFull'), barClass: 'ua-failed' });
      } else {
        setRow(b.host, { phaseText: t('updateAll.phase.failed'), barClass: 'ua-failed' });
        if (!uaReportShown) {
          uaReportShown = true;
          // The engine repair row reaches the speaker the same way, so it
          // loses the network the same way (#963).
          if (noNetworkHere(err)) showUpdateNoNetworkNotice();
          else showUpdateFailureReport(b, 'update-all-engine', err);
        }
        try { console.warn('update all: engine repair failed', b.host, err); } catch {}
      }
    } else if (outcome === 'failed') {
      setRow(b.host, { phaseText: t('updateAll.phase.failed'), barClass: 'ua-failed' });
      // Same account of the failure the single update gives, for the
      // FIRST speaker that fails: a wall of reports would help nobody, and
      // the rest stay on record for the next time each is opened.
      //
      // Including the no-network case. The single-speaker button learned to
      // say "your own network went away" in one line instead of opening a
      // fault report about a speaker that was fine, and the batch did not,
      // although the batch is what the reporter ran: his report header reads
      // "failed at: update-all" (#963).
      if (!uaReportShown) {
        uaReportShown = true;
        if (noNetworkHere(err)) showUpdateNoNetworkNotice();
        else showUpdateFailureReport(b, 'update-all', err);
      }
      try { console.warn('update all: box failed', b.host, err); } catch {}
    } else if ((b.model || '').includes('300')) {
      // The SoundTouch 300 drops into its blinking update-pending state after an
      // OTA and is non-functional until it is unplugged; the agent cannot clear
      // it. Tell the user that on the row rather than a misleading plain "done".
      setRow(b.host, { phaseText: t('update.st300PowerCycle'), pct: 100, barClass: 'ua-defer' });
    } else if (outcome === 'done') setRow(b.host, { phaseText: t('updateAll.phase.done'), pct: 100, barClass: 'ua-done' });
    else if (outcome === 'partial') setRow(b.host, { phaseText: t('updateAll.phase.engineMissing'), pct: 100, barClass: 'ua-defer' });
    else if (outcome === 'agentGone') setRow(b.host, { phaseText: t('updateAll.phase.agentGone'), barClass: 'ua-failed' });
    else if (outcome === 'boxInSetup') setRow(b.host, { phaseText: t('updateAll.phase.boxInSetup'), pct: 100, barClass: 'ua-defer' });
    else setRow(b.host, { phaseText: t('updateAll.phase.timeout'), barClass: 'ua-defer' });
    const r = rowState.get(b.host); if (r) r.outcome = outcome;
    renderSummary();
  };
  const FINAL_PHASES = new Set(['done', 'partial', 'timeout', 'agentGone', 'boxInSetup', 'failed']);
  const offPhase = EventsOn('box:update:phase', (d) => {
    if (!d || typeof d !== 'object' || !d.host || !rowState.has(d.host)) return;
    const b = rowState.get(d.host).box;
    if (FINAL_PHASES.has(d.phase)) { if (!rowState.get(d.host).outcome) finishRow(b, d); return; }
    if (engineOnly.has(d.host)) {
      // Queued behind another speaker's push, then sending: both read as the
      // one thing happening to this speaker, the engine being put back.
      if (d.phase === 'engineQueued') setRow(d.host, { phaseText: t('updateAll.phase.engineOnly'), wait: true });
      else if (d.phase === 'engineUploading') setRow(d.host, { phaseText: t('updateAll.phase.engineOnly'), busy: true });
      return;
    }
    switch (d.phase) {
      // 'uploading' fires before the batch gate: the row keeps waiting
      // until its first progress byte arrives (the progress handler above).
      case 'uploading': setRow(d.host, { phaseText: t('updateAll.phase.queued'), wait: true }); break;
      case 'rebooting': setRow(d.host, { phaseText: t('updateAll.phase.rebooting'), busy: true }); break;
      case 'verifying': setRow(d.host, { phaseText: withStep(t('updateAll.phase.verifying', { remaining: formatRemaining(d.remainingMs) }), UPDATE_STEPS.restart), busy: true }); break;
      case 'retrying': setRow(d.host, { phaseText: t('updateAll.phase.retrying'), busy: true }); break;
      case 'settling': setRow(d.host, { phaseText: withStep(t('updateAll.phase.settling', { remaining: formatRemaining(d.remainingMs) }), UPDATE_STEPS.confirm), busy: true }); break;
      case 'engineQueued': setRow(d.host, { phaseText: t('updateAll.phase.engineQueued'), wait: true }); break;
      case 'engineUploading': setRow(d.host, { phaseText: t('updateAll.phase.engineUploading'), pct: 0 }); break;
      case 'spotify': setRow(d.host, { phaseText: spotifyPhaseText(d), busy: true }); break;
    }
  });

  // Every speaker starts at once and they queue only for the push itself, so
  // the restarts, which are the long part and need no network, all happen
  // together instead of two at a time (the upload gate in otaorchestrate.go).
  const fleetTargets = targets.map(b => ({
    host: b.host, port: b.port, deviceID: b.deviceID || '', name: getBoxLabel(b), engineOnly: engineOnly.has(b.host),
  }));
  let fleet = null;
  try {
    fleet = await RunFleetUpdate(fleetTargets);
  } catch (e) {
    const msg = String(e);
    if (msg.includes(BOX_BUSY_TOKEN)) {
      // The whole batch was turned away: another update owns the speakers.
      // Nothing failed on any of them, so no reports, just the word.
      showToast(t('update.alreadyRunning', { name: state.otaTargetName || t('updateAll.batchLabel') }));
      for (const r of rowState.values()) {
        if (r.outcome) continue;
        r.outcome = 'failed';
        setRow(r.box.host, { phaseText: t('updateAll.phase.failed'), barClass: 'ua-failed' });
      }
      renderSummary();
    } else {
      for (const r of rowState.values()) if (!r.outcome) finishRow(r.box, { outcome: 'failed', err: msg });
    }
  }
  // The rows are the record. A terminal event the window missed is filled in
  // from them, and the loop breaker is fed from them, because only the rows
  // carry the confirmation and the classifier's verdict.
  for (const row of (fleet && fleet.rows) || []) {
    const r = rowState.get(row.host);
    if (!r) continue;
    if (!r.outcome) finishRow(r.box, row);
    if (!engineOnly.has(row.host)) noteOTAResult(r.box, row);
  }

  // Batch done: release the global lock, refresh, summarize.
  offProg();
  offPhase();
  if (mini) mini.classList.add('hidden');
  state.otaInProgress = false;
  syncShellPlayer(); // UI 2.0: volume and transport follow the OTA gate
  state.otaTargetHost = null;
  state.otaTargetName = null;
  renderSummary();
  try { await discoverBoxes(); checkBoxUpdate(); if (state.view === 'settings') loadBoxSettings(); } catch { /* boxes still rebooting */ }
  checkSshBanner();
  const c = counts();
  // A batch with failures or unconfirmed boxes must not lead with "Done": the
  // #775 run had every update fail and still read like a success. Timeouts
  // (the version poll expired without the box confirming) are reported as
  // unconfirmed, not as deferred work.
  let unconfirmed = 0;
  for (const r of rowState.values()) if (r.outcome === 'timeout') unconfirmed++;
  if (c.fail > 0 || unconfirmed > 0) {
    showToast(t('updateAll.problemToast', { failed: c.fail, unconfirmed, done: c.done, deferred: c.defer - unconfirmed }));
  } else {
    showToast(t('updateAll.doneToast', { done: c.done, deferred: c.defer, failed: c.fail }));
  }
  // Any SoundTouch 300 that got this far is now blinking and unreachable until
  // someone unplugs it once. The row already says so, but the row scrolls away
  // with the panel, so the instruction also gets the stays-until-dismissed
  // treatment (Jens 2026-08-30).
  if (targets.some(b => (b.model || '').includes('300') && ((rowState.get(b.host) || {}).outcome !== 'failed'))) {
    showSt300PowerCycleNotice();
  }
  return true;
}

function updateBoxUiVisibility() {
  const hasBox = !!state.currentBox;
  const hasSTR = state.boxes.some(b => b.kind !== 'stock');
  const stockOnly = !hasSTR && (state.boxes || []).some(b => b && b.kind === 'stock');
  $('boxControls').classList.toggle('hidden', !hasBox);
  // UI 2.0 preview: the persistent player and the sidebar speaker list follow the
  // same selection (no-ops with the preview off). Here, not at the end of
  // renderBoxSelect, because both of its paths, the empty list included,
  // come through this function.
  syncShellPlayer(hasBox);
  renderShellSpeakers();
  const hint = $('boxHint');
  if (!hint) return;
  // Three states, not two. The third is somebody's FIRST time: their speaker is
  // on the network and still runs the Bose firmware, so there is nothing to
  // control yet. That used to leave the page blank with no hint of what to do,
  // which is the one moment a new user decides whether this product works.
  if (stockOnly && !hasBox) {
    hint.innerHTML = `<p><b>${escapeHtml(t('speaker.stockConfirmTitle'))}</b></p>`
      + `<p class="muted">${escapeHtml(t('speaker.stockTooltip'))}</p>`
      + `<button class="btn btn-primary" id="boxHintSetup">${escapeHtml(t('speaker.stockConfirmCta'))}</button>`;
    hint.classList.remove('hidden');
    const go = $('boxHintSetup');
    if (go) go.onclick = () => switchView('setup');
    return;
  }
  hint.innerHTML = `<p>${escapeHtml(t('speaker.choose'))}</p>`;
  hint.classList.toggle('hidden', !hasSTR || hasBox);
}

// showUpdateNoNetworkNotice is the short, dismissible version of the failure
// report, for the one cause that is not about the speaker at all.
//
// The full report exists so a real fault can be sent to me, and it earns its
// weight then. A computer that lost its network produces nothing worth reading
// in it: the speaker is untouched, nothing needs unplugging, and the whole
// answer is one sentence that the app already owns for the speaker list.
function showUpdateNoNetworkNotice() {
  showError(t('settingsView.noNetworkTitle') + ' ' + t('settingsView.noNetworkHelp'));
}

// step numbers the phases a speaker goes through, because the middle of the
// sequence looks exactly like the end of it.
//
// The order is: send the software, restart, confirm, then the Spotify engine.
// "Restarting" is roughly halfway, and it is the phase that sits on screen the
// longest, so a row reading "Rebooting (usually 2-4 min)" with a full bar reads
// as finished. It is not: the confirm and the ~16 MB engine delivery still have
// to happen, and a run interrupted there leaves a speaker on the new version
// with no Spotify engine, which is exactly what happened to one owner's
// speaker (#963).
const UPDATE_STEPS = { send: 1, restart: 2, confirm: 3, engine: 4 };

function withStep(text, n) {
  return text + t('updateAll.phase.step', { n });
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
const WORLD_MAP_PROVISIONED_PREFIX = 'str.worldMapProvisioned.'; // per box, right after a successful install
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
  // Show FIRST, then persist, and only if it really appeared. The old order
  // burned the flag before showing, so an invite that returned early (another
  // one still on screen, which is what a second speaker finishing its install 20
  // seconds later runs into) was recorded as seen and never came back for that
  // speaker. The re-entry the old order guarded against is already covered by
  // the synchronous worldMapInviteHandled latch and by the DOM check inside
  // showWorldMapInvite.
  if (!showWorldMapInvite(variant)) return;
  try { localStorage.setItem(flag, '1'); } catch {}
  try { await SetAppFlag(flag); } catch {}
}

async function maybeInviteWorldMap() {
  if (worldMapInviteHandled) return; // synchronous guard against status-poll re-entry
  worldMapInviteHandled = true;
  await inviteWorldMapOnce(WORLD_MAP_FLAG, 'first');
}

// inviteWorldMapAfterProvision celebrates a freshly provisioned box right after a
// successful install. This is the strongest and most reliable "your SoundTouch is
// alive again" moment, and the one nearly every user actually reaches: the old
// triggers only fired on the first internet-radio play through the proxy, or once
// the whole multi-box set ran STR, so users who play Spotify, AUX/Bluetooth, the
// media library, or just press the hardware buttons never saw the pin invite,
// which is exactly why so many ask how to drop a pin. Keyed per box (deviceID,
// else host), so converting another speaker celebrates and invites again, while
// re-running the installer on the same box does not nag. Called from the setup
// view via the injected celebrateProvision dep.
async function inviteWorldMapAfterProvision(box) {
  const id = (box && (box.deviceID || box.deviceId || box.id || box.mac || box.host)) || '';
  const flag = id ? (WORLD_MAP_PROVISIONED_PREFIX + id) : WORLD_MAP_FLAG;
  // The provision moment supersedes the first-playback invite, for this session
  // and forever, so the same box's "alive again" milestone is never doubled up.
  worldMapInviteHandled = true;
  try { localStorage.setItem(WORLD_MAP_FLAG, '1'); } catch {}
  try { await SetAppFlag(WORLD_MAP_FLAG); } catch {}
  await inviteWorldMapOnce(flag, 'first');
}

// maybeInviteStickProvisioned fires the per-speaker pin invite for speakers
// provisioned via the USB stick, where the app has no install-completion hook:
// the stick installs autonomously on power-cycle and the box simply reappears
// in discovery as STR. A stock -> str transition observed within this session
// is that completion signal. Two guards keep it honest: a box ever seen as STR
// earlier in the session never fires (an OTA/reboot briefly misclassifies a
// live STR box as stock when its Bose port answers before the agent is up, see
// updateStockReprobe), and the durable per-box flag inside
// inviteWorldMapAfterProvision suppresses repeats across sessions. Keyed by
// host: the deviceID only becomes visible once STR runs, so the host is the
// only identity stable across the transition.
const worldMapKindSeen = new Map(); // host -> last seen kind, this session
const worldMapEverStr = new Set();  // hosts ever seen as str, this session
function maybeInviteStickProvisioned() {
  for (const b of (state.boxes || [])) {
    if (!b || !b.host || !b.kind) continue;
    const prev = worldMapKindSeen.get(b.host);
    worldMapKindSeen.set(b.host, b.kind);
    const isStr = b.kind !== 'stock';
    if (prev === 'stock' && isStr && !worldMapEverStr.has(b.host)) {
      worldMapEverStr.add(b.host);
      // Freshly rescued via stick: celebrate like the in-app install paths do.
      inviteWorldMapAfterProvision(b);
      continue;
    }
    if (isStr) worldMapEverStr.add(b.host);
  }
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

// Returns true when the invite was actually put on screen. The caller burns the
// once-ever flag only on a true, because an invite that silently did not appear
// must not count as one the user has seen.
function showWorldMapInvite(variant) {
  if (document.getElementById('worldMapInvite')) return false;
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
      `<div class="wmi-count" id="wmiCount">${escapeHtml(t('worldMap.pinPrompt'))}</div>` +
      `<button class="btn btn-mini btn-primary wmi-share" id="wmiShare">${escapeHtml(t('worldMap.inviteBtn'))}</button>` +
    `</div>`;
  document.body.appendChild(el);
  requestAnimationFrame(() => el.classList.add('show'));
  // A short confetti burst for the celebration moment, removed after it plays.
  spawnConfetti(el);
  // Live "rescued worldwide" count, fetched server-side from the website's pin
  // API (graceful: the line stays hidden on 0 or any error). Motivates the user
  // to add their pin and push the counter higher.
  // The count is an upgrade to the prompt, never its precondition: the line is
  // already on screen asking for a pin, and a number simply makes it warmer.
  // Every failure here leaves the plain ask standing.
  (async () => {
    let n = 0;
    try { n = await RescuedSpeakerCount(); } catch { /* keep the plain ask */ }
    const line = pinPromptKey(n);
    const c = el.querySelector('#wmiCount');
    if (c) c.textContent = t(line.key, line.params);
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
  // Auto-dismiss so it never lingers. It will not return (once-ever flag), but the
  // persistent World map footer link is always there if the user wants back in, so
  // missing this window is no longer a dead end. A calmer 45 s gives time to react.
  setTimeout(close, 45000);
  return true;
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
    // The Pandora entry: selectBox asks for it, this cache path has to as
    // well, or the entry stays hidden until another view is opened (#1388).
    updatePandoraModeBtn();
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
//
// The 5 s cadence is the Music tab's alone. The UI 2.0 preview keeps the player
// on screen in every view, and the player off that tab polls at the slow
// cadence even while playing: the bar interpolates the track position between
// readings, and a second fast poller is exactly what the watchdog note above
// warns about.
const STATUS_FAST_MS = 5000;    // Music tab, playing
const STATUS_PLAYER_MS = 15000; // UI 2.0 player off the Music tab, playing
const STATUS_IDLE_MS = 15000;   // idle, standby, or nothing on screen
function nextStatusDelayMs() {
  if (!state.currentBox) return STATUS_IDLE_MS;
  const ps = state.nowPlayState;
  const playing = ps === 'PLAY_STATE' || ps === 'BUFFERING_STATE';
  if (state.view === 'box') return playing ? STATUS_FAST_MS : STATUS_IDLE_MS;
  if (playing && playerOnScreen({ view: state.view, persistentPlayer: !!state.ui2Shell, otaInProgress: !!state.otaInProgress })) {
    return STATUS_PLAYER_MS;
  }
  return STATUS_IDLE_MS;
}
(function statusPollLoop() {
  setTimeout(async () => {
    try { await refreshStatus(); } catch {}
    statusPollLoop();
  }, nextStatusDelayMs());
})();
