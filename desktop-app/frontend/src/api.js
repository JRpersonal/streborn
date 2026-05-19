// api.js — single entry point for talking to the backend.
//
// Re-exports the Wails-generated bindings (so view modules do not
// need to know the wailsjs path) and adds a couple of HTTP helpers
// for the agent endpoints that are not yet wrapped on the Go side
// (status XML, radio search, etc.).

export {
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
  InstallSTROnBox,
  ScanForSetupAPs,
  BootstrapBoxOnSetupAP,
  ListMediaServers,
  BrowseLibrary,
} from '../wailsjs/go/main/App';

export { BrowserOpenURL } from '../wailsjs/runtime/runtime';

// boxURL builds an absolute URL for an agent endpoint on a given box.
// Centralised so the host/port pattern is in one place and switching
// to HTTPS later only takes touching this helper.
export function boxURL(box, path) {
  if (!box) return '';
  return `http://${box.host}:${box.port}${path}`;
}
