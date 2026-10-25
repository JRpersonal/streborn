// Shared by the screenshot harness (shoot.mjs) and the UI 2.0 check
// (ui2-check.mjs): the demo data, the in-page mock of the Wails bindings, the
// agent HTTP mock and the vite build/preview helpers.
//
// installMocks runs INSIDE the page (page.addInitScript), so it must stay
// self-contained: everything it reads comes in through its one argument.

import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
export const FRONTEND = path.resolve(__dirname, '..');                // desktop-app/frontend

// ---- demo data -------------------------------------------------------------
// A tiny inline SVG cover so screenshots have artwork without any network.
const cover = (letter, bg) =>
  'data:image/svg+xml;utf8,' +
  encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" width="160" height="160"><rect width="160" height="160" rx="14" fill="${bg}"/><text x="80" y="104" font-family="Segoe UI,Arial,sans-serif" font-size="78" font-weight="700" fill="#ffffff" text-anchor="middle">${letter}</text></svg>`,
  );

const BOX = {
  name: 'Living Room',
  host: 'demobox.local',
  port: 8888,
  deviceID: 'A1B2C3D4E5F6',
  friendlyName: 'Living Room',
  model: 'SoundTouch 20',
  version: 'v1.0.19',
  build: '2026-10-23',
  serialNumber: '',
  kind: 'STR',
  portVerified: true,
};

const PRESETS = [
  { slot: 1, name: 'BBC Radio 1', stream_url: 'http://demobox.local:8888/stream/1', type: 'radio', art: cover('B', '#c4302b'), bitrate: 128 },
  { slot: 2, name: 'Best Of Rock.FM', stream_url: 'http://demobox.local:8888/stream/2', type: 'radio', art: cover('R', '#1b2636'), bitrate: 256 },
  { slot: 3, name: 'NPR News', stream_url: 'http://demobox.local:8888/stream/3', type: 'radio', art: cover('N', '#2a6f97'), bitrate: 128 },
  { slot: 4, name: 'Jazz24', stream_url: 'http://demobox.local:8888/stream/4', type: 'radio', art: cover('J', '#7b2cbf'), bitrate: 320 },
  { slot: 5, name: 'Radio Paradise', stream_url: 'http://demobox.local:8888/stream/5', type: 'radio', art: cover('P', '#e07a00'), bitrate: 320 },
  { slot: 6, name: 'FIP', stream_url: 'http://demobox.local:8888/stream/6', type: 'radio', art: cover('F', '#d62246'), bitrate: 192 },
];

const SETTINGS = {
  info: { deviceID: 'A1B2C3D4E5F6', name: 'Living Room', type: 'SoundTouch 20' },
  volume: { actual: 32, muted: false },
  bass: { actual: 0, default: 0, min: -9, max: 9, available: true },
  network: { interfaces: [{ type: 'WIFI_INTERFACE', state: 'NETWORK_WIFI_CONNECTED', ssid: 'HomeWiFi', ipAddress: '192.0.2.50', signal: 'GOOD', frequencyKHz: 2412000 }] },
  sources: [{ source: 'AUX', status: 'READY' }, { source: 'BLUETOOTH', status: 'READY' }],
};

const STATUS_PLAYING =
  '<?xml version="1.0" encoding="UTF-8"?>' +
  '<nowPlaying deviceID="A1B2C3D4E5F6" source="INTERNET_RADIO">' +
  '<ContentItem source="INTERNET_RADIO" location="http://demobox.local:8888/stream/2" isPresetable="true">' +
  '<itemName>Best Of Rock.FM</itemName></ContentItem>' +
  '<art artImageStatus="IMAGE_PRESENT">' + cover('R', '#1b2636') + '</art>' +
  '<playStatus>PLAY_STATE</playStatus></nowPlaying>';

const STATUS_IDLE =
  '<?xml version="1.0" encoding="UTF-8"?>' +
  '<nowPlaying deviceID="A1B2C3D4E5F6" source="STANDBY">' +
  '<ContentItem source="STANDBY"></ContentItem><playStatus></playStatus></nowPlaying>';

const DRIVES = [
  { path: 'E:\\', label: 'USB DISK', totalBytes: 31043616768, freeBytes: 30500000000, filesystem: 'FAT32', removable: true, hasStick: false, description: 'Verbatim STORE N GO (29 GB)' },
];

const WIFI = [
  { ssid: 'HomeWiFi', hasPass: true, source: 'system' },
  { ssid: 'Guest-Net', hasPass: false, source: 'system' },
];

const APPINFO = {
  version: 'v1.0.19',
  build: '2026-10-23',
  author: 'Jens',
  githubUrl: 'https://github.com/JRpersonal/streborn',
  websiteUrl: 'https://st-reborn.de',
  donateUrl: 'https://github.com/sponsors/JRpersonal',
  donateSlogan: 'Support STR',
  updateManifestUrl: '',
};

const STATIONS = [
  { name: 'Best Of Rock.FM', url: 'http://example.com/rock', url_resolved: 'http://example.com/rock', stationuuid: 's1', countrycode: 'DE', bitrate: 256, votes: 4210, clicktrend: 12, clickcount: 90000, codec: 'MP3', tags: 'rock,alternative', favicon: cover('R', '#1b2636'), lastcheckok: 1 },
  { name: 'BBC Radio 1', url: 'http://example.com/bbc1', url_resolved: 'http://example.com/bbc1', stationuuid: 's2', countrycode: 'GB', bitrate: 128, votes: 3890, clicktrend: 8, clickcount: 120000, codec: 'MP3', tags: 'pop,charts', favicon: cover('B', '#c4302b'), lastcheckok: 1 },
  { name: 'Radio Paradise', url: 'http://example.com/rp', url_resolved: 'http://example.com/rp', stationuuid: 's3', countrycode: 'US', bitrate: 320, votes: 3550, clicktrend: 20, clickcount: 80000, codec: 'MP3', tags: 'eclectic,rock', favicon: cover('P', '#e07a00'), lastcheckok: 1 },
  { name: 'Jazz24', url: 'http://example.com/jazz24', url_resolved: 'http://example.com/jazz24', stationuuid: 's4', countrycode: 'US', bitrate: 320, votes: 2980, clicktrend: 5, clickcount: 60000, codec: 'AAC', tags: 'jazz', favicon: cover('J', '#7b2cbf'), lastcheckok: 1 },
  { name: 'FIP', url: 'http://example.com/fip', url_resolved: 'http://example.com/fip', stationuuid: 's5', countrycode: 'FR', bitrate: 192, votes: 2710, clicktrend: 9, clickcount: 55000, codec: 'MP3', tags: 'eclectic', favicon: cover('F', '#d62246'), lastcheckok: 1 },
  { name: 'NPR News', url: 'http://example.com/npr', url_resolved: 'http://example.com/npr', stationuuid: 's6', countrycode: 'US', bitrate: 128, votes: 2540, clicktrend: 3, clickcount: 70000, codec: 'MP3', tags: 'news,talk', favicon: cover('N', '#2a6f97'), lastcheckok: 1 },
  { name: 'KEXP', url: 'http://example.com/kexp', url_resolved: 'http://example.com/kexp', stationuuid: 's7', countrycode: 'US', bitrate: 256, votes: 2300, clicktrend: 7, clickcount: 50000, codec: 'MP3', tags: 'indie,alternative', favicon: cover('K', '#0a9396'), lastcheckok: 1 },
  { name: 'Classic FM', url: 'http://example.com/classic', url_resolved: 'http://example.com/classic', stationuuid: 's8', countrycode: 'GB', bitrate: 128, votes: 2100, clicktrend: 4, clickcount: 48000, codec: 'MP3', tags: 'classical', favicon: cover('C', '#5f0f40'), lastcheckok: 1 },
];

const LANGUAGES = [
  { name: 'english', stationcount: 12000 },
  { name: 'german', stationcount: 4200 },
  { name: 'french', stationcount: 2600 },
  { name: 'spanish', stationcount: 3100 },
  { name: 'japanese', stationcount: 900 },
  { name: 'ukrainian', stationcount: 300 },
  { name: 'dutch', stationcount: 700 },
];

const MEDIA_SERVERS = [
  { udn: 'uuid:demo-mediaserver', friendlyName: 'Living Room NAS', manufacturer: 'Demo', modelName: 'Media Server', iconURL: '', address: '192.0.2.60' },
];
const LIBRARY_PAGE = {
  containers: [
    { id: 'c1', title: 'Music', childCount: 6 },
    { id: 'c2', title: 'Pictures', childCount: 2 },
    { id: 'c3', title: 'Videos', childCount: 2 },
    { id: 'c4', title: 'Internet Radio', childCount: 0 },
    { id: 'c5', title: 'Podcasts', childCount: 0 },
    { id: 'c6', title: 'File Index', childCount: 1 },
  ],
  items: [],
  totalMatches: 6,
  returned: 6,
};

// A speaker still on Bose firmware: the setup views offer to install STR on it.
const STOCK_BOX = {
  name: 'Kitchen',
  host: '192.0.2.51',
  port: 8090,
  deviceID: 'F6E5D4C3B2A1',
  friendlyName: 'Kitchen',
  model: 'SoundTouch 10',
  version: '',
  build: '',
  serialNumber: '',
  kind: 'stock',
  portVerified: true,
};


// The simulated network of the UI 2.0 shots and checks (#1284): eleven
// speakers, so the sidebar list has to scroll. A group of two playing (the
// demo speaker leads, Dining room follows), a stereo pair, an offline speaker,
// a Bose original and six idle STR speakers. Placeholder addresses only.
const ui2Box = (n, id, name, model, extra = {}) => ({
  name, host: `192.0.2.${n}`, port: 8888, deviceID: id, friendlyName: name, model,
  version: 'v1.0.19', build: '2026-10-23', serialNumber: '', kind: 'STR', portVerified: true, ...extra,
});
const UI2_DINING = ui2Box(52, 'B2C3D4E5F6A1', 'Dining room', 'SoundTouch 10');
const UI2_TERRACE_L = ui2Box(53, 'C3D4E5F6A1B2', 'Terrace left', 'SoundTouch 10');
const UI2_TERRACE_R = ui2Box(54, 'D4E5F6A1B2C3', 'Terrace right', 'SoundTouch 10');
const UI2_NET = [
  BOX,
  UI2_DINING,
  UI2_TERRACE_L,
  UI2_TERRACE_R,
  ui2Box(55, 'E5F6A1B2C3D4', 'Garage', 'SoundTouch 10', { offline: true }),
  STOCK_BOX,
  ui2Box(56, 'F6A1B2C3D4E5', 'Office', 'SoundTouch 30'),
  ui2Box(57, 'A1B2C3D4E5F7', 'Bedroom', 'SoundTouch 10'),
  ui2Box(58, 'A1B2C3D4E5F8', 'Hall', 'SoundTouch 20'),
  ui2Box(59, 'A1B2C3D4E5F9', 'Bathroom', 'SoundTouch 10'),
  ui2Box(60, 'A1B2C3D4E5FA', 'Studio', 'SoundTouch Portable'),
];
const UI2_GROUP = {
  master: BOX.deviceID,
  senderIP: BOX.host,
  members: [{ deviceID: BOX.deviceID, ip: BOX.host }, { deviceID: UI2_DINING.deviceID, ip: UI2_DINING.host }],
};
const UI2_PAIR = {
  stereo: {
    id: 'pair-demo',
    masterDeviceID: UI2_TERRACE_L.deviceID,
    name: 'Terrace',
    members: [
      { deviceID: UI2_TERRACE_L.deviceID, ip: UI2_TERRACE_L.host, role: 'LEFT' },
      { deviceID: UI2_TERRACE_R.deviceID, ip: UI2_TERRACE_R.host, role: 'RIGHT' },
    ],
  },
};
// GetZoneState per speaker: the group on its two members, the pair document
// on both halves, an empty answer (no group) everywhere else.
const UI2_ZONES = {
  [BOX.host]: UI2_GROUP,
  [UI2_DINING.host]: UI2_GROUP,
  [UI2_TERRACE_L.host]: UI2_PAIR,
  [UI2_TERRACE_R.host]: UI2_PAIR,
};
const UI2_PLAYING = [BOX.host, UI2_DINING.host];

// "Recently played" history (newest first). Fictional artists only.
const RECENT = [
  { ts: '2026-10-23T18:42:00Z', source: 'radio', cardKey: 'r:rock', cardName: 'Best Of Rock.FM', cardArt: cover('R', '#1b2636'), cardURL: 'http://example.com/rock', track: 'The Demo Band - Morning Light' },
  { ts: '2026-10-23T18:38:00Z', source: 'radio', cardKey: 'r:rock', cardName: 'Best Of Rock.FM', cardArt: cover('R', '#1b2636'), cardURL: 'http://example.com/rock', track: 'Northern Lines - Paper Boats' },
  { ts: '2026-10-23T18:34:00Z', source: 'radio', cardKey: 'r:rock', cardName: 'Best Of Rock.FM', cardArt: cover('R', '#1b2636'), cardURL: 'http://example.com/rock', track: 'Sample & The Placeholders - Long Way Home' },
  { ts: '2026-10-23T17:55:00Z', source: 'radio', cardKey: 'r:jazz24', cardName: 'Jazz24', cardArt: cover('J', '#7b2cbf'), cardURL: 'http://example.com/jazz24', track: 'Quartet Example - Blue Hour' },
  { ts: '2026-10-23T17:49:00Z', source: 'radio', cardKey: 'r:jazz24', cardName: 'Jazz24', cardArt: cover('J', '#7b2cbf'), cardURL: 'http://example.com/jazz24', track: 'Ada Demo Trio - Late Train' },
  { ts: '2026-10-23T16:20:00Z', source: 'upnp', cardKey: 'u:album', cardName: 'Evening Sessions', cardArt: cover('E', '#2a9d8f'), cardURL: 'http://192.0.2.60:8200/MediaItems/1.flac', mime: 'audio/flac', track: 'Lorem Ipsum Ensemble - Opening' },
  { ts: '2026-10-23T08:05:00Z', source: 'radio', cardKey: 'r:npr', cardName: 'NPR News', cardArt: cover('N', '#2a6f97'), cardURL: 'http://example.com/npr', track: '' },
];

export const DEMO = { box: BOX, stockBox: STOCK_BOX, recent: RECENT, presets: PRESETS, settings: SETTINGS, statusPlaying: STATUS_PLAYING, statusIdle: STATUS_IDLE, drives: DRIVES, wifi: WIFI, appInfo: APPINFO, stations: STATIONS, languages: LANGUAGES, mediaServers: MEDIA_SERVERS, libraryPage: LIBRARY_PAGE, ui2Net: UI2_NET, ui2Zones: UI2_ZONES, ui2Playing: UI2_PLAYING };

// ---- the in-page mock (runs before the app's own scripts) ------------------
export function installMocks({ locale, view, demo }) {
  try {
    localStorage.setItem('locale', locale);
    localStorage.setItem('cachedBoxes', JSON.stringify(view.startsWith('app-ui2') ? demo.ui2Net : [demo.box]));
    localStorage.setItem('lastBoxDeviceID', demo.box.deviceID);
    localStorage.setItem('searchCountry', '');
    // One-time celebration popups (community world-map invite after the first
    // radio play / install) would cover the lower part of every shot: mark them
    // as already seen. GetAppFlag below answers true for the same reason.
    localStorage.setItem('str.worldMapInvited', '1');
    localStorage.setItem('str.worldMapInvitedAll', '1');
    if (view.startsWith('app-ui2')) {
      localStorage.setItem('str.ui2Preview', '1');
      localStorage.setItem('a11yTheme', view === 'app-ui2-light' ? 'light' : view === 'app-ui2-contrast' ? 'contrast' : 'dark');
      localStorage.setItem('a11yScale', view === 'app-ui2-xl' ? '3' : '1');
    }
  } catch (e) {}

  const P = (v) => Promise.resolve(v);
  // app-listen shows the lively now-playing state (matches the README hero);
  // every other view boots idle/standby.
  const status = view === 'app-listen' || view.startsWith('app-ui2') ? demo.statusPlaying : demo.statusIdle;
  const ui2 = view.startsWith('app-ui2');
  // What the UI 2.0 checks read back: every group edit the app sends.
  window.__ui2Calls = [];
  const record = (name, value) => (...args) => { window.__ui2Calls.push([name, ...args]); return P(value); };

  const App = {
    AppInfo: () => P(demo.appInfo),
    AppVersion: () => P(demo.appInfo.version),
    CheckAppUpdate: () => P({}),
    // The setup views also list a stock (not yet converted) speaker, so the
    // network-install card has a target to show.
    DiscoverBoxes: () => P(ui2 ? demo.ui2Net : (view.startsWith('app-install') || view.startsWith('app-stick') ? [demo.box, demo.stockBox] : [demo.box])),
    RefreshKnownBoxes: () => P(ui2 ? demo.ui2Net : null),
    GetZoneState: (host) => P(ui2 ? (demo.ui2Zones[host] || {}) : null),
    FormZone: record('FormZone', { ok: true }),
    DissolveZone: record('DissolveZone', { ok: true }),
    WakeBox: () => P({}),
    // Durable "already seen" flags: true keeps the one-time invites away.
    GetAppFlag: () => P(true),
    SetAppFlag: () => P(undefined),
    BoxSettings: () => P(demo.settings),
    BoxAgentVersion: () => P({ version: demo.appInfo.version, build: demo.appInfo.build }),
    // Phone-control QR card: a real demo QR (encodes http://demobox.local:8888/)
    // so the "Control this speaker on your phone" card renders in screenshots.
    PhoneQR: () => P('data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAPAAAADwAQMAAAAEm3vRAAAABlBMVEX///8AAABVwtN+AAABRklEQVR42uyXsbHDIBBEV0PgkBIoRaVJpakUSlBIwLB/7jCWma8ff3NmAw/ScwI6du8wNTX14XJUgRkPMgL1OY6Pk/xu+vYMcd0pz94C9uSxoegJyL4XMhnC+mlj4P5luLj8qN/bDK6FvDDL4q86HxA/refa960zjYebFvHUiHW/Dw1z2CUEOYeiq7gSS3ZdloyLtXwLJCR51JQ8YQD7iJXyMgFBce5C0ibWXkeORS6wYBb3foHHxfCM2Hh1PeXdcsfFehvFcNq+1YROGMCnWs+iGRk0VbqosYqT10JmbpYrK28BI9Q+td5Vyh87Rx4UN9VRkdz7Qh4XXzOwFqg24Pl3n2oPP/tzyZLXsXTt3LD4NSIn1M6O2RKu40Y4NkqqMH4Lloo+m+XSBK6FrFhWtILbiKzhL/uGu3Gm8fDU1NS/6ScAAP//khi29LPtOrwAAAAASUVORK5CYII='),
    GetPresets: () => P(demo.presets),
    RecentPlayed: () => P(demo.recent),
    // The UI 2.0 network: the group plays, every other speaker is idle.
    Status: (host) => P(ui2 ? (demo.ui2Playing.includes(host) ? demo.statusPlaying : demo.statusIdle) : status),
    StreamBitrate: () => P(256),
    ListDrives: () => P(demo.drives),
    StickVersion: () => P(''),
    StickConfigs: () => P({ wlanSSID: '', wlanPass: '', region: '', name: '', locale: '' }),
    ListWiFiProfiles: () => P(demo.wifi),
    TryWiFiPassword: () => P(''),
    CurrentWiFi: () => P('HomeWiFi'),
    SuggestBoxLanguage: () => P(3),
    GetBoxLanguage: () => P('3'),
    GetClockDisplay: () => P('true'),
    GetClockFormat24: () => P(true),
    GetAirplayOpt: () => P({ enabled: true }),
    // Readable display-track setting (a null answer paints "speaker did not
    // answer"); onPanel hides the re-buffer warning.
    // The remote key map in Speaker settings: AUX and thumbs down snooze a
    // ringing alarm (#1415), so app-snooze shows the Z markers.
    GetKeyActions: () => P({ keys: { aux: 'snooze', thumbsDown: 'snooze' } }),
    GetWebhooks: () => P({ thumb: {}, buttons: {} }),
    GetGroupKeys: () => P({ bindings: {} }),
    GetDisplayTrack: () => P({ enabled: false, mode: 'both', onPanel: true, supported: true }),
    ListMediaServers: () => P(demo.mediaServers),
    BrowseLibrary: () => P(demo.libraryPage),
    // Radio search moved app-side (Wails bindings, no longer /api/radio), so the
    // search view drives these instead of the routed HTTP endpoints below.
    RadioSearch: () => P(demo.stations),
    // The search view now prefers the detailed variant ({stations, relaxed}).
    RadioSearchDetailed: () => P({ stations: demo.stations, relaxed: false }),
    RadioLanguages: () => P(demo.languages),
    RadioTags: () => P([]),
    RadioClick: () => P(undefined),
    RadioVote: () => P(undefined),
    ResolveStationLogo: () => P(''),
    PlayURL: () => P(undefined),
    SetAppLocale: () => P(undefined),
    LogClientError: () => P(undefined),
  };
  // Any unmocked binding resolves to null so nothing throws.
  window.go = { main: { App: new Proxy(App, { get(t, p) { return p in t ? t[p] : () => Promise.resolve(null); } }) } };

  const rt = {
    EventsOn: () => () => {}, EventsOnce: () => () => {}, EventsOff: () => {}, EventsEmit: () => {},
    LogPrint: () => {}, LogTrace: () => {}, LogDebug: () => {}, LogInfo: () => {}, LogWarning: () => {}, LogError: () => {}, LogFatal: () => {},
    BrowserOpenURL: () => {}, WindowReload: () => {}, WindowReloadApp: () => {}, Quit: () => {},
    WindowSetTitle: () => {}, WindowShow: () => {}, WindowHide: () => {},
    ClipboardSetText: () => Promise.resolve(true), ClipboardGetText: () => Promise.resolve(''),
    Environment: () => Promise.resolve({ buildType: 'production', platform: 'windows', arch: 'amd64' }),
  };
  window.runtime = new Proxy(rt, { get(t, p) { return p in t ? t[p] : () => {}; } });
}

// ---- mock the agent HTTP endpoints the frontend fetch()es ------------------
export async function routeAgent(route) {
  const url = route.request().url();
  const json = (body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  if (url.includes('/api/radio/search') || url.includes('/api/radio/top')) return json(DEMO.stations);
  if (url.includes('/api/radio/languages')) return json(DEMO.languages);
  if (url.includes('/api/radio/tags')) return json([]);
  if (url.includes('/api/region')) return json({ countryCode: 'US', country: 'United States' });
  if (url.includes('/api/agent/version')) return json({ version: DEMO.appInfo.version, build: DEMO.appInfo.build });
  if (url.includes('/api/box/settings')) return json(DEMO.settings);
  if (url.includes('/api/stick/status')) return json({});
  if (url.includes('/api/debug/state')) return json({});
  return json([]); // safe default for any other /api/* call
}

// ---- build + vite preview server -------------------------------------------
// We build/serve with the parent frontend's own vite (resolved via npx in
// cwd=FRONTEND), so this harness's only dependency is playwright. That keeps
// playwright out of the app's package.json and therefore out of every CI /
// wails build (it would otherwise download ~130 MB of browsers on install).
export function buildFrontend() {
  const bin = process.platform === 'win32' ? 'npx.cmd' : 'npx';
  return new Promise((resolve, reject) => {
    const p = spawn(bin, ['vite', 'build'], { cwd: FRONTEND, stdio: 'ignore', shell: process.platform === 'win32' });
    p.on('exit', (code) => (code === 0 ? resolve() : reject(new Error('vite build exited ' + code))));
    p.on('error', reject);
  });
}

export function startServer(port) {
  const bin = process.platform === 'win32' ? 'npx.cmd' : 'npx';
  const srv = spawn(bin, ['vite', 'preview', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { cwd: FRONTEND, stdio: 'ignore', shell: process.platform === 'win32' });
  return srv;
}
export async function waitForServer(base) {
  for (let i = 0; i < 60; i++) {
    try { const r = await fetch(base); if (r.ok) return; } catch (e) {}
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error('vite preview did not come up on ' + base);
}


// stopServer takes the preview server down. On Windows it runs under a shell
// (see startServer), and killing the shell leaves vite listening on the port,
// where the next run then finds a stale build: take the whole tree down.
export function stopServer(server) {
  if (!server) return;
  if (process.platform === 'win32') spawn('taskkill', ['/pid', String(server.pid), '/T', '/F'], { stdio: 'ignore' });
  else server.kill();
}
