// api.js — single entry point for talking to the backend.
//
// Re-exports the Wails-generated bindings (so view modules do not
// need to know the wailsjs path) and adds a couple of HTTP helpers
// for the agent endpoints that are not yet wrapped on the Go side
// (status XML, radio search, etc.).

export {
  DiscoverBoxes,
  RefreshKnownBoxes,
  AddBoxByIP,
  GetPresets,
  SetPreset,
  DeletePreset,
  PlaySlot,
  PlayURL,
  StartQueue,
  QueueNext,
  QueuePrev,
  QueueShuffle,
  QueueRepeat,
  GetQueue,
  VoteStation,
  PhoneQR,
  RebootBox,
  SyncBoxPresets,
  BoxPresets,
  BoxSnapshot,
  RestoreBoxSnapshot,
  RecallBoxPreset,
  CopyPresetsAcrossBoxes,
  GetBoxFirmware,
  BoxInstallReachable,
  Pause,
  Resume,
  Stop,
  Next,
  Prev,
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
  EnsureSpotifyEngine,
  RecordOTAOutcome,
  ClassifyOTAResult,
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
  GetResumeOnPowerOn,
  SetResumeOnPowerOn,
  GetDisplayTrack,
  SetDisplayTrack,
  AnnounceExample,
  SendAnnounce,
  Translate,
  ResolveUpdateAsset,
  DownloadUpdate,
  ApplyUpdate,
  RevealUpdateFile,
  GetAppFlag,
  SetAppFlag,
  RescuedSpeakerCount,
  GetWebhooks,
  SetWebhooks,
  SaveWebhookConfig,
  TestWebhook,
  TestWebhookAction,
  StreamBitrate,
  StreamTitle,
  SpotifyBitrate,
  SpotifyNowPlaying,
  SaveSpotifyPreset,
  SaveLibraryPreset,
  SaveFolderPreset,
  RecentPlayed,
  ClearRecent,
  DeleteRecentCard,
  SaveDiagnosticBundle,
  GetLogFilePath,
  InstallSTROnBox,
  RepairInstallViaSSH,
  RadioSearch,
  RadioTags,
  RadioLanguages,
  RadioVote,
  RadioClick,
  TrueFactoryReset,
  UninstallSTR,
  ProbeSetupAP,
  PushWLANToBox,
  ListMediaServers,
  BrowseLibrary,
  AddMediaServerByURL,
  RemoveManualMediaServer,
  LogClientError,
  GetZoneState,
  FormZone,
  DissolveZone,
  SyncSpotifyLogin,
} from '../wailsjs/go/main/App';

export { BrowserOpenURL, EventsOn, EventsOff } from '../wailsjs/runtime/runtime';

// Optional bindings. The wailsjs bindings are regenerated only when the Go
// backend and the frontend are built together, so a frontend change that
// starts using a brand-new Go method must not name-import it above: the named
// re-export would fail to build against the older generated App module. The
// namespace import below always builds; the wrapper turns a missing binding
// into a rejected promise carrying MISSING_BINDING so callers can fall back
// to the older binding set instead of crashing.
import * as AppBindings from '../wailsjs/go/main/App';

const MISSING_BINDING = 'STR_MISSING_BINDING';

// isMissingBinding says whether a rejection came from calling an optional
// binding that this build's generated bindings do not have (as opposed to a
// real backend failure, which callers must surface, not silently fall back on).
export function isMissingBinding(err) {
  if (!err) return false;
  if (err.code === MISSING_BINDING) return true;
  return String(err.message || err).includes(MISSING_BINDING);
}

function callOptionalBinding(name, args) {
  const fn = AppBindings[name];
  if (typeof fn !== 'function') {
    const e = new Error(`${MISSING_BINDING}: ${name} is not available in this build`);
    e.code = MISSING_BINDING;
    return Promise.reject(e);
  }
  return fn(...args);
}

// RadioSearchDetailed is RadioSearch plus a relaxed flag: same opts object,
// returns {stations, relaxed} where relaxed=true means the backend had to
// drop the quality filters to find anything.
export function RadioSearchDetailed(opts) {
  return callOptionalBinding('RadioSearchDetailed', [opts]);
}

// RadioStationsByURL looks a pasted stream URL up in the radio-browser
// directory and returns the matching stations (possibly none).
export function RadioStationsByURL(streamURL) {
  return callOptionalBinding('RadioStationsByURL', [streamURL]);
}

// boxURL builds an absolute URL for an agent endpoint on a given box.
// Centralised so the host/port pattern is in one place and switching
// to HTTPS later only takes touching this helper.
export function boxURL(box, path) {
  if (!box) return '';
  return `http://${box.host}:${box.port}${path}`;
}
