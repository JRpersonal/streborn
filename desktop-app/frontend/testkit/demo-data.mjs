// testkit/demo-data.mjs: the deterministic demo data behind every mocked run
// of the frontend: the screenshot harness (screenshots/shoot.mjs and
// shoot-phone.mjs), the UI 2.0 check (screenshots/ui2-check.mjs) and the
// Playwright suite (e2e/).
//
// Plain data, no dependencies, safe to import from Node and from a test. The
// in-page mocks (desktop-mock.mjs) receive it as JSON through
// page.addInitScript, so everything in DEMO must stay serialisable.
//
// Placeholder identifiers only (RFC 5737 addresses, made-up device IDs,
// fictional artists): nothing in here may ever come from a real speaker.

// ---- desktop app -----------------------------------------------------------
// A tiny inline SVG cover so screenshots have artwork without any network.
export const cover = (letter, bg) =>
  'data:image/svg+xml;utf8,' +
  encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" width="160" height="160"><rect width="160" height="160" rx="14" fill="${bg}"/><text x="80" y="104" font-family="Segoe UI,Arial,sans-serif" font-size="78" font-weight="700" fill="#ffffff" text-anchor="middle">${letter}</text></svg>`,
  );

export const BOX = {
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

export const PRESETS = [
  { slot: 1, name: 'BBC Radio 1', stream_url: 'http://demobox.local:8888/stream/1', type: 'radio', art: cover('B', '#c4302b'), bitrate: 128 },
  { slot: 2, name: 'Best Of Rock.FM', stream_url: 'http://demobox.local:8888/stream/2', type: 'radio', art: cover('R', '#1b2636'), bitrate: 256 },
  { slot: 3, name: 'NPR News', stream_url: 'http://demobox.local:8888/stream/3', type: 'radio', art: cover('N', '#2a6f97'), bitrate: 128 },
  { slot: 4, name: 'Jazz24', stream_url: 'http://demobox.local:8888/stream/4', type: 'radio', art: cover('J', '#7b2cbf'), bitrate: 320 },
  { slot: 5, name: 'Radio Paradise', stream_url: 'http://demobox.local:8888/stream/5', type: 'radio', art: cover('P', '#e07a00'), bitrate: 320 },
  { slot: 6, name: 'FIP', stream_url: 'http://demobox.local:8888/stream/6', type: 'radio', art: cover('F', '#d62246'), bitrate: 192 },
];

export const SETTINGS = {
  info: { deviceID: 'A1B2C3D4E5F6', name: 'Living Room', type: 'SoundTouch 20' },
  volume: { actual: 32, muted: false },
  bass: { actual: 0, default: 0, min: -9, max: 9, available: true },
  network: { interfaces: [{ type: 'WIFI_INTERFACE', state: 'NETWORK_WIFI_CONNECTED', ssid: 'HomeWiFi', ipAddress: '192.0.2.50', signal: 'GOOD', frequencyKHz: 2412000 }] },
  sources: [{ source: 'AUX', status: 'READY' }, { source: 'BLUETOOTH', status: 'READY' }],
};

const xmlEscape = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

// statusXml builds the speaker's /now_playing answer the way the Status
// binding hands it to the app. Any playState other than STANDBY gives the
// "a station is on" shape (PLAY_STATE, PAUSE_STATE, STOP_STATE, BUFFERING_STATE).
// The in-page mock cannot call this function (the app's CSP forbids eval), so
// it fills STATUS_TEMPLATE below instead, which is built from this same code.
export function statusXml({
  deviceID = BOX.deviceID,
  source = 'INTERNET_RADIO',
  playState = 'PLAY_STATE',
  itemName = 'Best Of Rock.FM',
  location = 'http://demobox.local:8888/stream/2',
  art = cover('R', '#1b2636'),
} = {}) {
  if (playState === 'STANDBY' || source === 'STANDBY') {
    return '<?xml version="1.0" encoding="UTF-8"?>' +
      `<nowPlaying deviceID="${xmlEscape(deviceID)}" source="STANDBY">` +
      '<ContentItem source="STANDBY"></ContentItem><playStatus></playStatus></nowPlaying>';
  }
  return '<?xml version="1.0" encoding="UTF-8"?>' +
    `<nowPlaying deviceID="${xmlEscape(deviceID)}" source="${xmlEscape(source)}">` +
    `<ContentItem source="${xmlEscape(source)}" location="${xmlEscape(location)}" isPresetable="true">` +
    `<itemName>${xmlEscape(itemName)}</itemName></ContentItem>` +
    (art ? `<art artImageStatus="IMAGE_PRESENT">${art}</art>` : '') +
    `<playStatus>${xmlEscape(playState)}</playStatus></nowPlaying>`;
}

// Templates for the in-page mock: statusXml with marker tokens the page
// replaces (already escaped) per speaker.
const tok = (k) => `%%${k}%%`;
export const STATUS_TEMPLATE = {
  playing: '<?xml version="1.0" encoding="UTF-8"?>' +
    `<nowPlaying deviceID="${tok('deviceID')}" source="${tok('source')}">` +
    `<ContentItem source="${tok('source')}" location="${tok('location')}" isPresetable="true">` +
    `<itemName>${tok('itemName')}</itemName></ContentItem>` +
    `${tok('art')}<playStatus>${tok('playState')}</playStatus></nowPlaying>`,
  standby: statusXml({ deviceID: tok('deviceID'), playState: 'STANDBY' }),
};

export const STATUS_PLAYING = statusXml();
export const STATUS_IDLE = statusXml({ playState: 'STANDBY' });

export const DRIVES = [
  { path: 'E:\\', label: 'USB DISK', totalBytes: 31043616768, freeBytes: 30500000000, filesystem: 'FAT32', removable: true, hasStick: false, description: 'Verbatim STORE N GO (29 GB)' },
];

export const WIFI = [
  { ssid: 'HomeWiFi', hasPass: true, source: 'system' },
  { ssid: 'Guest-Net', hasPass: false, source: 'system' },
];

export const APPINFO = {
  version: 'v1.0.19',
  build: '2026-10-23',
  author: 'Jens',
  githubUrl: 'https://github.com/JRpersonal/streborn',
  websiteUrl: 'https://st-reborn.de',
  donateUrl: 'https://github.com/sponsors/JRpersonal',
  donateSlogan: 'Support STR',
  updateManifestUrl: '',
};

export const STATIONS = [
  { name: 'Best Of Rock.FM', url: 'http://example.com/rock', url_resolved: 'http://example.com/rock', stationuuid: 's1', countrycode: 'DE', bitrate: 256, votes: 4210, clicktrend: 12, clickcount: 90000, codec: 'MP3', tags: 'rock,alternative', favicon: cover('R', '#1b2636'), lastcheckok: 1 },
  { name: 'BBC Radio 1', url: 'http://example.com/bbc1', url_resolved: 'http://example.com/bbc1', stationuuid: 's2', countrycode: 'GB', bitrate: 128, votes: 3890, clicktrend: 8, clickcount: 120000, codec: 'MP3', tags: 'pop,charts', favicon: cover('B', '#c4302b'), lastcheckok: 1 },
  { name: 'Radio Paradise', url: 'http://example.com/rp', url_resolved: 'http://example.com/rp', stationuuid: 's3', countrycode: 'US', bitrate: 320, votes: 3550, clicktrend: 20, clickcount: 80000, codec: 'MP3', tags: 'eclectic,rock', favicon: cover('P', '#e07a00'), lastcheckok: 1 },
  { name: 'Jazz24', url: 'http://example.com/jazz24', url_resolved: 'http://example.com/jazz24', stationuuid: 's4', countrycode: 'US', bitrate: 320, votes: 2980, clicktrend: 5, clickcount: 60000, codec: 'AAC', tags: 'jazz', favicon: cover('J', '#7b2cbf'), lastcheckok: 1 },
  { name: 'FIP', url: 'http://example.com/fip', url_resolved: 'http://example.com/fip', stationuuid: 's5', countrycode: 'FR', bitrate: 192, votes: 2710, clicktrend: 9, clickcount: 55000, codec: 'MP3', tags: 'eclectic', favicon: cover('F', '#d62246'), lastcheckok: 1 },
  { name: 'NPR News', url: 'http://example.com/npr', url_resolved: 'http://example.com/npr', stationuuid: 's6', countrycode: 'US', bitrate: 128, votes: 2540, clicktrend: 3, clickcount: 70000, codec: 'MP3', tags: 'news,talk', favicon: cover('N', '#2a6f97'), lastcheckok: 1 },
  { name: 'KEXP', url: 'http://example.com/kexp', url_resolved: 'http://example.com/kexp', stationuuid: 's7', countrycode: 'US', bitrate: 256, votes: 2300, clicktrend: 7, clickcount: 50000, codec: 'MP3', tags: 'indie,alternative', favicon: cover('K', '#0a9396'), lastcheckok: 1 },
  { name: 'Classic FM', url: 'http://example.com/classic', url_resolved: 'http://example.com/classic', stationuuid: 's8', countrycode: 'GB', bitrate: 128, votes: 2100, clicktrend: 4, clickcount: 48000, codec: 'MP3', tags: 'classical', favicon: cover('C', '#5f0f40'), lastcheckok: 1 },
];

export const LANGUAGES = [
  { name: 'english', stationcount: 12000 },
  { name: 'german', stationcount: 4200 },
  { name: 'french', stationcount: 2600 },
  { name: 'spanish', stationcount: 3100 },
  { name: 'japanese', stationcount: 900 },
  { name: 'ukrainian', stationcount: 300 },
  { name: 'dutch', stationcount: 700 },
];

export const MEDIA_SERVERS = [
  { udn: 'uuid:demo-mediaserver', friendlyName: 'Living Room NAS', manufacturer: 'Demo', modelName: 'Media Server', iconURL: '', address: '192.0.2.60' },
];
export const LIBRARY_PAGE = {
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
export const STOCK_BOX = {
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

// "Recently played" history (newest first). Fictional artists only.
export const RECENT = [
  { ts: '2026-10-23T18:42:00Z', source: 'radio', cardKey: 'r:rock', cardName: 'Best Of Rock.FM', cardArt: cover('R', '#1b2636'), cardURL: 'http://example.com/rock', track: 'The Demo Band - Morning Light' },
  { ts: '2026-10-23T18:38:00Z', source: 'radio', cardKey: 'r:rock', cardName: 'Best Of Rock.FM', cardArt: cover('R', '#1b2636'), cardURL: 'http://example.com/rock', track: 'Northern Lines - Paper Boats' },
  { ts: '2026-10-23T18:34:00Z', source: 'radio', cardKey: 'r:rock', cardName: 'Best Of Rock.FM', cardArt: cover('R', '#1b2636'), cardURL: 'http://example.com/rock', track: 'Sample & The Placeholders - Long Way Home' },
  { ts: '2026-10-23T17:55:00Z', source: 'radio', cardKey: 'r:jazz24', cardName: 'Jazz24', cardArt: cover('J', '#7b2cbf'), cardURL: 'http://example.com/jazz24', track: 'Quartet Example - Blue Hour' },
  { ts: '2026-10-23T17:49:00Z', source: 'radio', cardKey: 'r:jazz24', cardName: 'Jazz24', cardArt: cover('J', '#7b2cbf'), cardURL: 'http://example.com/jazz24', track: 'Ada Demo Trio - Late Train' },
  { ts: '2026-10-23T16:20:00Z', source: 'upnp', cardKey: 'u:album', cardName: 'Evening Sessions', cardArt: cover('E', '#2a9d8f'), cardURL: 'http://192.0.2.60:8200/MediaItems/1.flac', mime: 'audio/flac', track: 'Lorem Ipsum Ensemble - Opening' },
  { ts: '2026-10-23T08:05:00Z', source: 'radio', cardKey: 'r:npr', cardName: 'NPR News', cardArt: cover('N', '#2a6f97'), cardURL: 'http://example.com/npr', track: '' },
];

// Phone-control QR card: a real demo QR (encodes http://demobox.local:8888/)
// so the "Control this speaker on your phone" card renders.
export const PHONE_QR = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAPAAAADwAQMAAAAEm3vRAAAABlBMVEX///8AAABVwtN+AAABRklEQVR42uyXsbHDIBBEV0PgkBIoRaVJpakUSlBIwLB/7jCWma8ff3NmAw/ScwI6du8wNTX14XJUgRkPMgL1OY6Pk/xu+vYMcd0pz94C9uSxoegJyL4XMhnC+mlj4P5luLj8qN/bDK6FvDDL4q86HxA/refa960zjYebFvHUiHW/Dw1z2CUEOYeiq7gSS3ZdloyLtXwLJCR51JQ8YQD7iJXyMgFBce5C0ibWXkeORS6wYBb3foHHxfCM2Hh1PeXdcsfFehvFcNq+1YROGMCnWs+iGRk0VbqosYqT10JmbpYrK28BI9Q+td5Vyh87Rx4UN9VRkdz7Qh4XXzOwFqg24Pl3n2oPP/tzyZLXsXTt3LD4NSIn1M6O2RKu40Y4NkqqMH4Lloo+m+XSBK6FrFhWtILbiKzhL/uGu3Gm8fDU1NS/6ScAAP//khi29LPtOrwAAAAASUVORK5CYII=';

// ---- the e2e roster --------------------------------------------------------
// The speakers a scenario names by key (scenario.mjs). livingRoom is the
// current speaker; the others each carry one state worth testing.
const strBox = (n, id, name, model, extra = {}) => ({
  name, host: `192.0.2.${n}`, port: 8888, deviceID: id, friendlyName: name, model,
  version: 'v1.0.19', build: '2026-10-23', serialNumber: '', kind: 'STR', portVerified: true, ...extra,
});
export const ROSTER = {
  livingRoom: BOX,
  // Its own settings (name Bedroom, volume 18), idle in STANDBY by default.
  bedroom: strBox(52, 'B7C8D9E0F1A2', 'Bedroom', 'SoundTouch 10', { volume: 18 }),
  // Offline: in the list, greyed, cannot be chosen as a target.
  office: strBox(53, 'C7D8E9F0A1B2', 'Office', 'SoundTouch 30', { offline: true, offlineSinceSec: 600 }),
  // Still on Bose firmware: offers the install.
  kitchen: STOCK_BOX,
  // An old agent: boxNeedsUpdate (main.js) is true for it.
  oldAgent: strBox(54, 'D7E8F9A0B1C2', 'Hallway', 'SoundTouch 20', { version: 'v1.0.10', build: '2026-09-30' }),
};

// ---- the UI 2.0 network (screenshots and ui2-check) ------------------------
// Eleven speakers, so the sidebar list has to scroll (#1284). A group of two
// playing (the demo speaker leads, Dining room follows), a stereo pair, an
// offline speaker, a Bose original and six idle STR speakers.
const UI2_DINING = strBox(52, 'B2C3D4E5F6A1', 'Dining room', 'SoundTouch 10');
const UI2_TERRACE_L = strBox(53, 'C3D4E5F6A1B2', 'Terrace left', 'SoundTouch 10');
const UI2_TERRACE_R = strBox(54, 'D4E5F6A1B2C3', 'Terrace right', 'SoundTouch 10');
export const UI2_NET = [
  BOX,
  UI2_DINING,
  UI2_TERRACE_L,
  UI2_TERRACE_R,
  strBox(55, 'E5F6A1B2C3D4', 'Garage', 'SoundTouch 10', { offline: true }),
  STOCK_BOX,
  strBox(56, 'F6A1B2C3D4E5', 'Office', 'SoundTouch 30'),
  strBox(57, 'A1B2C3D4E5F7', 'Bedroom', 'SoundTouch 10'),
  strBox(58, 'A1B2C3D4E5F8', 'Hall', 'SoundTouch 20'),
  strBox(59, 'A1B2C3D4E5F9', 'Bathroom', 'SoundTouch 10'),
  strBox(60, 'A1B2C3D4E5FA', 'Studio', 'SoundTouch Portable'),
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
export const UI2_ZONES = {
  [BOX.host]: UI2_GROUP,
  [UI2_DINING.host]: UI2_GROUP,
  [UI2_TERRACE_L.host]: UI2_PAIR,
  [UI2_TERRACE_R.host]: UI2_PAIR,
};
export const UI2_PLAYING = [BOX.host, UI2_DINING.host];

// ---- phone remote ----------------------------------------------------------
// The page only shows http(s) art (its art filter drops data: URIs), so the
// presets point at a fake art host that phone-mock.mjs answers with an SVG.
const ART_HOST = 'https://art.example';
const coverSVG = (letter, bg) =>
  `<svg xmlns="http://www.w3.org/2000/svg" width="160" height="160"><rect width="160" height="160" rx="14" fill="${bg}"/><text x="80" y="104" font-family="Segoe UI,Arial,sans-serif" font-size="78" font-weight="700" fill="#ffffff" text-anchor="middle">${letter}</text></svg>`;
const art = (k) => `${ART_HOST}/${k}.svg`;
const PHONE_GROUP_MEMBERS = [
  { name: 'Living Room', ip: '192.0.2.50', deviceID: 'A1B2C3D4E5F6', isSelf: true, isMaster: true, volume: 32 },
  { name: 'Kitchen', ip: '192.0.2.51', deviceID: 'B1B2C3D4E5F6', volume: 24 },
];

export const PHONE = {
  ORIGIN: 'http://demobox.local:8888', // a placeholder origin, never resolved
  ART_HOST,
  coverSVG,
  COVERS: {
    bbc: ['B', '#c4302b'], rock: ['R', '#1b2636'], npr: ['N', '#2a6f97'], jazz: ['J', '#7b2cbf'],
    rp: ['P', '#e07a00'], fip: ['F', '#d62246'], kexp: ['K', '#0a9396'], classic: ['C', '#5f0f40'],
  },
  USER_AGENT: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1',
  // The title the agent writes into the page (status_index.go).
  TITLE: 'Living Room',
  PRESETS: [
    { slot: 1, name: 'BBC Radio 1', type: 'radio', stream_url: 'http://example.com/bbc1', art: art('bbc') },
    { slot: 2, name: 'Best Of Rock.FM', type: 'radio', stream_url: 'http://example.com/rock', art: art('rock') },
    { slot: 3, name: 'NPR News', type: 'radio', stream_url: 'http://example.com/npr', art: art('npr') },
    { slot: 4, name: 'Jazz24', type: 'radio', stream_url: 'http://example.com/jazz24', art: art('jazz') },
    { slot: 5, name: 'Radio Paradise', type: 'radio', stream_url: 'http://example.com/rp', art: art('rp') },
    { slot: 6, name: 'FIP', type: 'radio', stream_url: 'http://example.com/fip', art: art('fip') },
  ],
  STATUS_PLAYING:
    '<?xml version="1.0" encoding="UTF-8"?>' +
    '<nowPlaying deviceID="A1B2C3D4E5F6" source="LOCAL_INTERNET_RADIO">' +
    '<ContentItem source="LOCAL_INTERNET_RADIO" location="http://example.com/rock" isPresetable="true">' +
    '<itemName>Best Of Rock.FM</itemName></ContentItem>' +
    '<playStatus>PLAY_STATE</playStatus></nowPlaying>',
  SETTINGS: {
    info: { deviceID: 'A1B2C3D4E5F6', name: 'Living Room', type: 'SoundTouch 20' },
    volume: { actual: 32, muted: false },
    bass: { actual: -2, default: 0, min: -9, max: 0, available: true },
    network: { interfaces: [{ type: 'WIFI', state: 'NETWORK_WIFI_CONNECTED', ssid: 'HomeWiFi', ipAddress: '192.0.2.50', signal: 'GOOD' }] },
    sources: [{ source: 'AUX', status: 'READY' }, { source: 'BLUETOOTH', status: 'READY' }],
  },
  VERSION: { version: 'v1.0.19', build: '2026-10-23', boxHealth: 'ok', internet: 'ok' },
  // Days are 0 = Sunday .. 6 = Saturday (internal/alarm).
  ALARMS: {
    zone: 'Europe/Berlin',
    alarms: [
      { id: 'a1', enabled: true, hour: 6, minute: 45, days: [1, 2, 3, 4, 5], slot: 3, volume: 18, snooze: 9 },
      { id: 'a2', enabled: false, hour: 9, minute: 0, days: [0, 6], slot: 5, volume: 15 },
    ],
    status: { clockTrusted: true, zoneResolved: true, nextFire: '2026-10-26T05:45:00Z' },
  },
  RECENT: [ // oldest first, the way the agent's ring answers
    { ts: '2026-10-23T08:05:00Z', source: 'radio', cardKey: 'r:npr', cardName: 'NPR News', cardURL: 'http://example.com/npr', track: '' },
    { ts: '2026-10-23T17:49:00Z', source: 'radio', cardKey: 'r:jazz24', cardName: 'Jazz24', cardURL: 'http://example.com/jazz24', track: 'Ada Demo Trio - Late Train' },
    { ts: '2026-10-23T18:42:00Z', source: 'radio', cardKey: 'r:rock', cardName: 'Best Of Rock.FM', cardURL: 'http://example.com/rock', track: 'The Demo Band - Morning Light' },
  ],
  FAVORITES: [
    { stationuuid: 's7', name: 'KEXP', url: 'http://example.com/kexp', favicon: art('kexp'), countrycode: 'US', bitrate: 256 },
    { stationuuid: 's8', name: 'Classic FM', url: 'http://example.com/classic', favicon: art('classic'), countrycode: 'GB', bitrate: 128 },
  ],
  // radio-browser.info answer for the Browse tab search (asked from the phone).
  STATIONS: [
    ['Best Of Rock.FM', 'rock', 'DE', 256, 'rock,alternative'],
    ['Rock Antenne Demo', 'rock', 'DE', 128, 'rock,classic rock'],
    ['KEXP', 'kexp', 'US', 256, 'indie,alternative'],
    ['Radio Paradise', 'rp', 'US', 320, 'eclectic,rock'],
    ['Classic Rock Example', 'classic', 'GB', 192, 'classic rock'],
  ].map(([name, k, cc, bitrate, tags], i) => ({
    stationuuid: 'demo-' + i, name, url: 'http://example.com/' + k + i, url_resolved: 'http://example.com/' + k + i,
    favicon: art(k), countrycode: cc, bitrate, tags, codec: 'MP3', lastcheckok: 1, votes: 4000 - i * 300, clickcount: 9000 - i * 500,
  })),
  PEERS: [
    { name: 'Kitchen', ip: '192.0.2.51', deviceID: 'B1B2C3D4E5F6', url: 'http://192.0.2.51:8888/', reachable: true },
    { name: 'Bedroom', ip: '192.0.2.52', deviceID: 'C1B2C3D4E5F6', url: 'http://192.0.2.52:8888/', reachable: true },
    { name: 'Office', ip: '192.0.2.53', deviceID: 'D1B2C3D4E5F6', url: 'http://192.0.2.53:8888/', reachable: true },
  ],
  // A group plays only when the handler is told so (this speaker leads,
  // Kitchen follows); otherwise the speaker plays alone.
  GROUP_MEMBERS: PHONE_GROUP_MEMBERS,
  zoneDoc: (grouped) => (grouped
    ? { members: PHONE_GROUP_MEMBERS }
    : { members: [], remembered: [{ name: 'Kitchen', ip: '192.0.2.51' }, { name: 'Bedroom', ip: '192.0.2.52' }] }),
  zoneVolume: (grouped) => (grouped
    ? { grouped: true, members: PHONE_GROUP_MEMBERS, loudest: 32, average: 28 }
    : { grouped: false }),
  LIB_SERVERS: [{ udn: 'uuid:demo-mediaserver', id: 'uuid:demo-mediaserver', friendlyName: 'Living Room NAS', address: '192.0.2.60' }],
};

// ---- the bundle the in-page desktop mock receives --------------------------
export const DEMO = {
  box: BOX, stockBox: STOCK_BOX, roster: ROSTER, recent: RECENT, presets: PRESETS, settings: SETTINGS,
  statusPlaying: STATUS_PLAYING, statusIdle: STATUS_IDLE, statusTemplate: STATUS_TEMPLATE, defaultArt: cover('R', '#1b2636'),
  drives: DRIVES, wifi: WIFI, appInfo: APPINFO, stations: STATIONS, languages: LANGUAGES,
  mediaServers: MEDIA_SERVERS, libraryPage: LIBRARY_PAGE, phoneQR: PHONE_QR,
  ui2Net: UI2_NET, ui2Zones: UI2_ZONES, ui2Playing: UI2_PLAYING,
};
