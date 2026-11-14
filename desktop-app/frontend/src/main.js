import './style.css';
// UI 2.0 preview (#1285). Every rule in it is scoped under html.ui2, which is
// only set once the preview shell is mounted, so with the preview off it
// changes nothing.
import './shell2/shell2.css';
import { pinPromptKey, worldMapURL } from './worldmapinvite.js';
import { maybeShowStableNameNotice } from './stablenamenotice.js';
import { announceHighscores } from './arcadeshare.js';
import { openHighscorePopup } from './arcadepopup.js';
import { firewallBannerView, firewallUnblockToast, firewallSuspectGate } from './firewallbanner.js';
import { wingetFailureView } from './wingetupdate.js';
import { boxCannotReachInternet } from './nointernet.js';
import { onUpdateRunningChange } from './updaterunning.js';
import { checkTargets, failingSpeakers, needsAttention, precheckHTML, runSpeakerChecks } from './speakercheck.js';
import { wantsFastPoll } from './trackprogress.js';
import {
  DiscoverBoxes,
  RefreshKnownBoxes,
  AddBoxByIP,
  RemoveSpeakerFromList,
  RebootBox,
  CheckSpeaker,
  PendingUpdateIntent,
  SyncBoxPresets,
  GetBoxFirmware,
  BoxInstallReachable,
  Status,
  ListDrives,
  WriteStickFiles,
  FormatStick,
  StickVersion,
  CheckStick,
  StickConfigs,
  AppInfo,
  EjectDrive,
  WriteWLANConfig,
  WriteRegionConfig,
  WriteNameConfig,
  WriteLangConfig,
  SetAppLocale,
  SuggestBoxLanguage,
  ListWiFiProfiles,
  TryWiFiPassword,
  CurrentWiFi,
  ConsumeStableNameNotice,
  ConsumeWingetUpgradeFailure,
  ResolveStationLogo,
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
  loadCachedBoxes,
  saveCachedBoxes,
} from './state.js';

import {
  $,
  escapeHtml,
  escapeAttr,
  debounce,
  confirmWarn,
  closeWarn,
  showError,
  showToast,
  compareVerBuild,
  getBoxLabel,
  clearNoticeDismissal,
} from './utils.js';
import { isPlaying, strBoxes } from './boxes.js';
import { boxRecordChange } from './boxstate.js';

// Group membership (who follows master X) and the shared zoneLive poll live
// in groups.js: ONE implementation for the selector frames, the group chips
// and the Multi-Room tab. masterOf is imported under a distinct name because
// renderBoxSelect keeps a local box-shaped wrapper of the same name.
import {
  fetchZoneLive,
  resolveBoxByRef,
  stereoPairsOf,
  groupCount,
  groupCountSplit,
  onZoneLive,
  anyInLiveGroup,
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
import { renderRecent, initRecentView, repaintRecentFavStars } from './views/recent.js';
import { shareModalHTML, openShareModal } from './share.js';
import { donateButtonsHTML, wireDonateButtons, donateFooterLinkHTML } from './donate.js';
import { renderMultiroom, initMultiroomView, stopMultiroomLive, resetMultiroomNotes } from './views/multiroom.js';
import { renderSpotifyAlpha, initSpotifyView } from './views/spotify.js';
import { initStreamSourceView } from './views/streamsource.js';
// VOTE_URL: the Ideas category sorted by votes, one discussion per open
// backlog item (the footer's "Vote on what comes next").
import { renderWishes, initWishesView, VOTE_URL } from './views/wishes.js';
import { releaseNotesURL, repoURL } from './shell2/help/about.js';
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
} from './views/settings.js';
// Library (DLNA MediaServer browse) view, extracted from this monolith, same
// pattern as the views above. openLibrary is the entry point switchView calls;
// showSlotPicker is a main.js-local helper it reuses, injected below.
import { openLibrary, initLibraryView, libraryNowPlaying } from './views/library.js';
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
import { confirmLeaveWizard, wizardBusy } from './shell2/setup/wizard.js';
// UI 2.0 phase 5: what one speaker needs is said on that speaker (the wedge
// and box issue banners, the update failure report); preview only.
import { setSpeakerAttentionRunner, speakerAttention } from './shell2/attention.js';
import { onSpeakerPurge } from './speakerPurge.js';
import { removeEntry } from './speakerremove.js';
// The Music view ("Listen": now playing, presets, volume, sources, search,
// favourites, Pandora, the group panel and the status poll), extracted from
// this monolith into src/views/music/. Same pattern as the views above:
// mountMusicView builds #view-box where the markup used to be assigned
// inline, initMusicView injects the main.js-local helpers the view calls,
// and the names below are the Music functions the shell, the boot sequence
// and the other views still call.
import {
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
  presetMoveOffer,
  pollTrackPosition,
  purgeMusicStateForSpeaker,
  refreshSourceButtons,
  refreshStatus,
  renderGroupControl,
  renderNowPlayingBar,
  renderTrackProgress,
  showSlotPicker,
  syncFavRemovals,
  syncSearchModeButtons,
  syncVolumeScopeOnZoneLive,
  toggleFav,
  toggleGroupMember,
  trackPos,
  updateFavModeBtn,
  updateFilterIndicators,
  updatePandoraModeBtn,
} from './views/music/index.js';
// The speaker update (one speaker and Update all), extracted from this monolith
// into src/update/. Same pattern again: initUpdateFlow injects the main.js-local
// helpers the runners call (see update/deps.js).
// The app's own update (check, notice, install paths), extracted from this
// monolith. initAppUpdate injects the one main.js-local helper it calls.
import { checkAppUpdate, initAppUpdate, renderAppUpdateCheckLink } from './appupdate.js';
// The global banners (SSH reminder, wedge, speaker issues, the speaker update
// card) and the UI 2.0 attention runner, extracted from this monolith into
// src/banners/. initBanners injects the main.js-local helpers they call (see
// banners/deps.js). The Windows Firewall banner stays here: its state is
// written by discoverBoxes and renderFooter.
import { initBanners } from './banners/deps.js';
import { warnDismissKey } from './banners/dismiss.js';
import { checkSshBanner } from './banners/ssh.js';
import { checkBoxIssueBanner, checkWedgeBanner } from './banners/speaker.js';
import { runSpeakerAttention } from './banners/attention.js';
import { maybeShowSpeakerUpdateCard } from './banners/speakerupdate.js';
// The speaker picker (the tiles in #boxSelect, pickBox, selectBox and the
// speaker check dialog), extracted from this monolith. Same pattern again:
// initBoxPicker injects the main.js-local helpers it calls.
import {
  initBoxPicker,
  manualIpInputBusy,
  offlineTitle,
  pickBoxFromShell,
  renderBoxSelect,
  selectBox,
  speakerPickedInTab,
} from './views/boxpicker.js';
import { initUpdateFlow } from './update/deps.js';
import { doBoxUpdate } from './update/single.js';
import { updateAllBoxes } from './update/all.js';
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
  strBoxes: () => strBoxes(state.boxes)
    .filter(b => b.deviceID && b.host)
    .map(b => ({ host: b.host, port: b.port, name: getBoxLabel(b) })),
});
initSettingsView({ switchView, updateFilterIndicators, discoverBoxes, renderBoxSelect, localizeLanguageName, doBoxUpdate, updateAllBoxes, boxNeedsUpdate, loadPresets, getRoomNames, speakerPicked: speakerPickedInTab, favStationsJSON, mergeFavStations });
initLibraryView({ showSlotPicker, effectivePlayTarget, speakerPicked: speakerPickedInTab, renderNowPlayingBar, refreshStatus, presetMoveOffer });
initSetupView({
  switchView, discoverBoxes, doBoxUpdate, getRoomNames, celebrateProvision: inviteWorldMapAfterProvision, speakerPicked: speakerPickedInTab,
  // The speaker list reads the same holds (speakertile.js); repaint it when a
  // speaker enters or leaves one.
  installHoldsChanged: () => { renderBoxSelect(); renderShellSpeakers(); },
});
// A speaker an update starts or stops running on is marked or unmarked in
// both speaker lists at once (updaterunning.js, #1764).
onUpdateRunningChange(() => { renderBoxSelect(); renderShellSpeakers(); });
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
initMusicView({ checkAppUpdate, checkSshBanner, discoverBoxes, libraryNowPlaying, maybeInviteWorldMap, refreshMusicZones, renderBoxSelect, speakerPickedInTab, stereoStaleForError, switchView });
// App update and banner deps (see appupdate.js and banners/deps.js).
initAppUpdate({ checkBoxUpdate });
initBanners({ boxNeedsUpdate, discoverBoxes, speakerUpdateCardMuted, switchView });
// Speaker picker deps (see views/boxpicker.js for what each one is used for).
initBoxPicker({ boxNeedsUpdate, checkBoxIssueBanner, checkBoxUpdate, checkWedgeBanner, discoverBoxes, refreshMusicZones, removeSpeakerEntry, renderFirewallBanner, switchView, updateBoxUiVisibility });
// Speaker update deps (see update/deps.js for what each one is used for).
initUpdateFlow({ boxNeedsUpdate, checkBoxUpdate, checkSshBanner, discoverBoxes });
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
  // The answer can be a short list: the station's own logo that the check in
  // Go could not confirm, then the fallback it found (#1898). The window tries
  // them in order and ends on the monogram.
  ResolveStationLogo(fav, brand, hosts).then((url) => {
    if (typeof url !== 'string' || !url) return;
    const chain = url.split('|').map((c) => c.trim()).filter(Boolean);
    if (chain.length === 0) return;
    const mono = img.dataset.logoMono || img.src;
    let next = 1;
    img.onerror = () => {
      if (next < chain.length) { img.src = chain[next++]; return; }
      img.onerror = null;
      img.src = mono;
    };
    img.src = chain[0];
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
      <div class="pick-rows hidden" id="pickRows"></div>
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
      <p class="modal-sub credits-trademark" id="creditsTrademark">${escapeHtml(t('about.trademark'))}</p>
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
      // The UI 2.0 setup wizard with an install or a firmware update running
      // asks first: the reload would stop it being followed on screen. With
      // nothing running (and always in the classic layout) nothing changes.
      if (code && code !== getLocale() && wizardBusy()) {
        close();
        confirmLeaveWizard().then((ok) => { if (ok && setLocale(code)) location.reload(); });
        return;
      }
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
      // Same question as the language switch while the wizard works.
      if (wizardBusy()) {
        close();
        confirmLeaveWizard().then((ok) => { if (ok) switchLayout(b, on); });
        return;
      }
      switchLayout(b, on);
    };
  });
  function switchLayout(b, on) {
    if (!setUi2Preview(on)) {
      setUi2Preview(!on);
      layoutWriteFailed = true;
      syncLayoutAvailable();
      return;
    }
    syncPressed('data-ui2', b.dataset.ui2);
    location.reload();
  }
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
// the languages we have native-speaker copy for. Every language with a
// bundle in i18n/bundles has a line here (localetables.test.js fails when
// one is missing); `it` and `pt` are kept for the day their bundles arrive.
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
  'zh-Hant': '適用於所有 SoundTouch 型號',
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
  'zh-Hant': '不靠 Bose 雲端，繼續使用你的 Bose SoundTouch 喇叭。',
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
    // A UI 2.0 area entry (Speakers) stands for several views (data-views).
    const views = (btn.dataset.views || btn.dataset.view || '').split(' ');
    const want = NEW_FEATURES.some(f => views.includes(f.view) && !seen.has(f.id));
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
  // The UI 2.0 Speakers list is a view of the preview shell only
  // (shell2/speakers); without the shell the node does not exist.
  const speakersView = $('view-speakers');
  if (speakersView) speakersView.classList.toggle('hidden', view !== 'speakers');
  // Help and info is a preview view too (shell2/help); classic has no node.
  const helpView = $('view-help'); if (helpView) helpView.classList.toggle('hidden', view !== 'help');
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
  // CLAUDE.md asks for the trademark notice in the app's About dialog; the
  // credits dialog is the classic layout's About.
  $('creditsTrademark').textContent = t('about.trademark');
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
  // releases list (shell2/help/about.js, shared with Help and info).
  const repo = repoURL(i.githubUrl);
  const releaseNotesUrl = releaseNotesURL(i);
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
  // The speakers the list already shows as not answering are checked first
  // (reportProblemWithCheck): most reports about them are a speaker off the
  // network, which the check explains with the steps.
  if (reportLink) reportLink.onclick = (e) => { e.preventDefault(); reportProblemWithCheck(`${repo}/issues`); };
  const voteLink = $('footerVoteIdeas');
  if (voteLink) voteLink.onclick = (e) => { e.preventDefault(); BrowserOpenURL(VOTE_URL); };
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
      const playing = isPlaying(state.nowPlayState);
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
      // The Speakers area (shell2/speakers): opening one speaker's page picks
      // it the way the classic Settings dropdown does (lock-step).
      speakerPicked: speakerPickedInTab,
      // Speakers > list: the classic single-speaker and update-all paths.
      doBoxUpdate,
      updateAllBoxes,
      // Phase 5 (states): where the search stands (state.discovery, set in
      // discoverBoxes) and Connect by address, the classic AddBoxByIP plus a
      // search, so the speaker it finds joins the list.
      discovery: () => state.discovery,
      addBoxByIp: async (ip) => {
        await AddBoxByIP(ip);
        await discoverBoxes();
      },
      // Phase 5: the issues of one speaker (shell2/attention.js), for the
      // sidebar chip; filled where the wedge and issue banners render.
      speakerAttention,
      // Help and info (shell2/help): the credits dialog, and a manual app
      // update check like the version line's own link (which also forgets a
      // dismissed notice); the result shows where it always does.
      showCredits,
      // The speaker check (speakercheck.js): the list's Check action and the
      // check in front of "Report a problem".
      checkSpeaker: CheckSpeaker,
      // "Remove from list" on a speaker that is not reachable (#1852).
      removeSpeaker: removeSpeakerEntry,
      reportProblem: reportProblemWithCheck,
      checkAppUpdate: (manual) => {
        try { clearNoticeDismissal('appUpdate'); } catch {}
        return checkAppUpdate(manual);
      },
      withAppReferrer,
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

// emptyStateOnScreen reports whether the "no speaker found" card is the thing
// currently in the speaker panel. Its manual connect-by-IP field exists only
// inside that card, so its presence is the cheapest honest test.
function emptyStateOnScreen() {
  return !!document.getElementById('emptyIpInput');
}

async function discoverBoxes() {
  const hadBoxes = state.boxes.length > 0;
  // UI 2.0 states (shell2/state.js): where the search stands. A repeat keeps
  // the answer of the last finished search in `last`, so the empty list does
  // not toggle between two screens on every sweep. Read by the preview only.
  state.discovery = { phase: 'searching', error: '', at: Date.now(), last: state.discovery ? (state.discovery.phase === 'searching' ? state.discovery.last : state.discovery) : null };
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
    state.discovery = { phase: 'done', error: '', at: Date.now(), found: (list || []).length };
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
    state.discovery = { phase: 'failed', error: String(e), at: Date.now() };
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
        state.searchListOpen = false;
        state.nowLocation = '';
        state.nowPlayState = '';
        state.presetErrors = {};
        state.presetServerOff = {};
        $('searchResults').innerHTML = '';
        syncSearchModeButtons();
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

// removeSpeakerEntry takes the entry of a speaker that is not reachable off
// the list (speakerremove.js, #1852): the classic picker's x on a greyed tile
// and the UI 2.0 list's "Remove from list" both end here. The list is applied
// the way a search result is, so the tiles, the cached list painted at the
// next start and every view that reads state.boxes follow.
function removeSpeakerEntry(box) {
  return removeEntry(box, {
    remove: RemoveSpeakerFromList,
    boxes: () => state.boxes,
    applyList: applyBoxList,
    state,
    discover: discoverBoxes,
  });
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
  const polled = strBoxes(state.boxes).filter(b => b.deviceID && b.host && !b.offline);
  if (!polled.length) return;
  const now = Date.now();
  if (state.boxPlayingBusy || now - _boxPlayingFetchAt < 8000) return;
  _boxPlayingFetchAt = now;
  state.boxPlayingBusy = true;
  try {
    const results = await Promise.allSettled(polled.map(b => Status(b.host, b.port)));
    const map = {};
    results.forEach((r, i) => {
      let playing = false;
      if (r.status === 'fulfilled' && typeof r.value === 'string') {
        const xml = r.value;
        const ps = (xml.match(/playStatus>([^<]+)</) || [])[1] || '';
        const src = (xml.match(/nowPlaying[^>]*source="([^"]*)"/) || [])[1] || '';
        playing = isPlaying(ps) && src !== 'STANDBY';
      }
      map[polled[i].deviceID] = playing;
    });
    state.boxPlaying = map;
  } catch { /* keep previous marks */ } finally {
    state.boxPlayingBusy = false;
  }
  renderBoxSelect();
  renderShellSpeakers();
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

setSpeakerAttentionRunner(runSpeakerAttention);

// reportProblemWithCheck is "Report a problem" (classic footer, UI 2.0 Help):
// when the list shows speakers that are not answering, they are checked first
// and what the check found is shown with its steps. Reporting stays possible
// from the same dialog. Nothing is checked when every speaker answers.
async function reportProblemWithCheck(url) {
  const failing = checkTargets(null, failingSpeakers(state.boxes));
  if (failing.length) {
    showToast(t('speakerCheck.checking', { name: failing.map(getBoxLabel).join(', ') }));
    const found = (await runSpeakerChecks(CheckSpeaker, failing)).filter((r) => needsAttention(r.res));
    if (found.length) {
      const go = await confirmWarn(t('speakerCheck.beforeReportTitle'), precheckHTML(found, getBoxLabel), {
        icon: null,
        calm: true,
        rich: true,
        confirmLabel: t('speakerCheck.reportAnyway'),
        confirmClass: 'btn btn-secondary',
        cancelLabel: t('common.close'),
      });
      if (!go) return;
    }
  }
  BrowserOpenURL(url);
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
// The speaker update card usually paints before the first zone round lands.
// Redraw it once its group note would change, and only then: a redraw resets
// its button, which may be mid-click (#1339).
onZoneLive(() => {
  const el = $('speakerUpdateCard');
  if (!el || el.classList.contains('hidden')) return;
  const outdated = strBoxes(state.boxes).filter(b => !b.offline && boxNeedsUpdate(b));
  const want = outdated.length > 1 && anyInLiveGroup(outdated, state.zoneLive);
  if (want !== !!$('sucGroupNote')) maybeShowSpeakerUpdateCard();
});

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

// worldMapURL (worldmapinvite.js) builds the localized community-map deep link.

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
  const strCount = strBoxes(boxes).length;
  const stockBoxes = boxes.filter(b => b && b.kind === 'stock');
  if (strCount < 2 || stockBoxes.length > 0) return;
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

// ---------- Speaker settings view ----------

// ROOM_NAMES_BY_LOCALE contains the common room suggestions for the
// friendly-name combobox. Recomputed via getRoomNames() so a locale
// switch reflects on the next render. Every language with a bundle has a
// list (localetables.test.js); anything else falls back to English.
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
  ar: [
    'غرفة المعيشة', 'غرفة النوم', 'المطبخ', 'غرفة الطعام',
    'الحمّام', 'غرفة الدراسة', 'المكتب', 'غرفة الأطفال',
    'غرفة الضيوف', 'الممر', 'المدخل',
    'الحديقة', 'الفناء', 'الشرفة', 'الورشة',
    'غرفة الهوايات', 'القبو', 'العلّية', 'المرآب',
  ],
  'zh-Hant': [
    '客廳', '臥室', '廚房', '餐廳',
    '浴室', '書房', '辦公室', '兒童房',
    '客房', '走廊', '玄關',
    '花園', '露臺', '陽台', '工作室',
    '休閒室', '地下室', '閣樓', '車庫',
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
  const playing = isPlaying(state.nowPlayState);
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
