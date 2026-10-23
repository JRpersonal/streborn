// Screenshot harness for the phone remote: the mobile web page every STR
// speaker serves on http://<speaker>:8888/ (internal/webui/assets/index.html).
//
// The page is self-contained (inline CSS and JS), so this harness needs no
// server and no speaker: Playwright intercepts every request to a fake origin,
// answers "/" with the embedded index.html straight from the repo, and answers
// every /api/* call (and the radio-browser.info station search the page does
// from the phone) with deterministic demo data. Nothing leaves the machine and
// no real speaker is ever contacted.
//
// Output: docs/screenshots/phone/<lang>/<view>.png
//
// Run: npm run shoot-phone   (SHOOT_LANGS=en,de and SHOOT_VIEWS=phone-play to narrow)

import { chromium } from 'playwright';
import { mkdir, readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(__dirname, '../../..');
const ASSETS = path.join(REPO, 'internal/webui/assets');
const OUT = path.join(REPO, 'docs/screenshots/phone');
// A placeholder origin. It is never resolved: every request to it is answered
// by the route handler below.
const ORIGIN = 'http://demobox.local:8888';

const ALL_LANGS = ['en', 'de'];
const LANGS = (process.env.SHOOT_LANGS && process.env.SHOOT_LANGS.trim())
  ? process.env.SHOOT_LANGS.split(',').map((s) => s.trim()).filter(Boolean)
  : ALL_LANGS;
const ALL_VIEWS = ['phone-play', 'phone-alarm', 'phone-browse', 'phone-speakers'];
const VIEWS = (process.env.SHOOT_VIEWS && process.env.SHOOT_VIEWS.trim())
  ? process.env.SHOOT_VIEWS.split(',').map((s) => s.trim()).filter(Boolean)
  : ALL_VIEWS;

// ---- demo data -------------------------------------------------------------
// Cover art: the page only shows http(s) art (data: URIs are dropped by its
// art filter), so the presets point at a fake art host that the route
// handler answers with a small generated SVG.
const ART_HOST = 'https://art.example';
const coverSVG = (letter, bg) =>
  `<svg xmlns="http://www.w3.org/2000/svg" width="160" height="160"><rect width="160" height="160" rx="14" fill="${bg}"/><text x="80" y="104" font-family="Segoe UI,Arial,sans-serif" font-size="78" font-weight="700" fill="#ffffff" text-anchor="middle">${letter}</text></svg>`;
const COVERS = {
  bbc: ['B', '#c4302b'], rock: ['R', '#1b2636'], npr: ['N', '#2a6f97'], jazz: ['J', '#7b2cbf'],
  rp: ['P', '#e07a00'], fip: ['F', '#d62246'], kexp: ['K', '#0a9396'], classic: ['C', '#5f0f40'],
};
const art = (k) => `${ART_HOST}/${k}.svg`;

const PRESETS = [
  { slot: 1, name: 'BBC Radio 1', type: 'radio', stream_url: 'http://example.com/bbc1', art: art('bbc') },
  { slot: 2, name: 'Best Of Rock.FM', type: 'radio', stream_url: 'http://example.com/rock', art: art('rock') },
  { slot: 3, name: 'NPR News', type: 'radio', stream_url: 'http://example.com/npr', art: art('npr') },
  { slot: 4, name: 'Jazz24', type: 'radio', stream_url: 'http://example.com/jazz24', art: art('jazz') },
  { slot: 5, name: 'Radio Paradise', type: 'radio', stream_url: 'http://example.com/rp', art: art('rp') },
  { slot: 6, name: 'FIP', type: 'radio', stream_url: 'http://example.com/fip', art: art('fip') },
];

const STATUS_PLAYING =
  '<?xml version="1.0" encoding="UTF-8"?>' +
  '<nowPlaying deviceID="A1B2C3D4E5F6" source="LOCAL_INTERNET_RADIO">' +
  '<ContentItem source="LOCAL_INTERNET_RADIO" location="http://example.com/rock" isPresetable="true">' +
  '<itemName>Best Of Rock.FM</itemName></ContentItem>' +
  '<playStatus>PLAY_STATE</playStatus></nowPlaying>';

const SETTINGS = {
  info: { deviceID: 'A1B2C3D4E5F6', name: 'Living Room', type: 'SoundTouch 20' },
  volume: { actual: 32, muted: false },
  bass: { actual: -2, default: 0, min: -9, max: 0, available: true },
  network: { interfaces: [{ type: 'WIFI', state: 'NETWORK_WIFI_CONNECTED', ssid: 'HomeWiFi', ipAddress: '192.0.2.50', signal: 'GOOD' }] },
  sources: [{ source: 'AUX', status: 'READY' }, { source: 'BLUETOOTH', status: 'READY' }],
};

const VERSION = { version: 'v1.0.19', build: '2026-10-23', boxHealth: 'ok', internet: 'ok' };

// Days are 0 = Sunday .. 6 = Saturday (internal/alarm).
const ALARMS = {
  zone: 'Europe/Berlin',
  alarms: [
    { id: 'a1', enabled: true, hour: 6, minute: 45, days: [1, 2, 3, 4, 5], slot: 3, volume: 18, snooze: 9 },
    { id: 'a2', enabled: false, hour: 9, minute: 0, days: [0, 6], slot: 5, volume: 15 },
  ],
  status: { clockTrusted: true, zoneResolved: true, nextFire: '2026-10-26T05:45:00Z' },
};

const RECENT = [ // oldest first, the way the agent's ring answers
  { ts: '2026-10-23T08:05:00Z', source: 'radio', cardKey: 'r:npr', cardName: 'NPR News', cardURL: 'http://example.com/npr', track: '' },
  { ts: '2026-10-23T17:49:00Z', source: 'radio', cardKey: 'r:jazz24', cardName: 'Jazz24', cardURL: 'http://example.com/jazz24', track: 'Ada Demo Trio - Late Train' },
  { ts: '2026-10-23T18:42:00Z', source: 'radio', cardKey: 'r:rock', cardName: 'Best Of Rock.FM', cardURL: 'http://example.com/rock', track: 'The Demo Band - Morning Light' },
];

const FAVORITES = [
  { stationuuid: 's7', name: 'KEXP', url: 'http://example.com/kexp', favicon: art('kexp'), countrycode: 'US', bitrate: 256 },
  { stationuuid: 's8', name: 'Classic FM', url: 'http://example.com/classic', favicon: art('classic'), countrycode: 'GB', bitrate: 128 },
];

// radio-browser.info answer for the Browse tab search (asked from the phone).
const STATIONS = [
  ['Best Of Rock.FM', 'rock', 'DE', 256, 'rock,alternative'],
  ['Rock Antenne Demo', 'rock', 'DE', 128, 'rock,classic rock'],
  ['KEXP', 'kexp', 'US', 256, 'indie,alternative'],
  ['Radio Paradise', 'rp', 'US', 320, 'eclectic,rock'],
  ['Classic Rock Example', 'classic', 'GB', 192, 'classic rock'],
].map(([name, k, cc, bitrate, tags], i) => ({
  stationuuid: 'demo-' + i, name, url: 'http://example.com/' + k + i, url_resolved: 'http://example.com/' + k + i,
  favicon: art(k), countrycode: cc, bitrate, tags, codec: 'MP3', lastcheckok: 1, votes: 4000 - i * 300, clickcount: 9000 - i * 500,
}));

const PEERS = [
  { name: 'Kitchen', ip: '192.0.2.51', deviceID: 'B1B2C3D4E5F6', url: 'http://192.0.2.51:8888/', reachable: true },
  { name: 'Bedroom', ip: '192.0.2.52', deviceID: 'C1B2C3D4E5F6', url: 'http://192.0.2.52:8888/', reachable: true },
  { name: 'Office', ip: '192.0.2.53', deviceID: 'D1B2C3D4E5F6', url: 'http://192.0.2.53:8888/', reachable: true },
];

// Only the Speakers shot plays as a group (this speaker leads, Kitchen
// follows); every other shot is a single speaker.
const GROUP_MEMBERS = [
  { name: 'Living Room', ip: '192.0.2.50', deviceID: 'A1B2C3D4E5F6', isSelf: true, isMaster: true, volume: 32 },
  { name: 'Kitchen', ip: '192.0.2.51', deviceID: 'B1B2C3D4E5F6', volume: 24 },
];
const zoneDoc = (grouped) => (grouped
  ? { members: GROUP_MEMBERS }
  : { members: [], remembered: [{ name: 'Kitchen', ip: '192.0.2.51' }, { name: 'Bedroom', ip: '192.0.2.52' }] });
const zoneVolume = (grouped) => (grouped
  ? { grouped: true, members: GROUP_MEMBERS, loudest: 32, average: 28 }
  : { grouped: false });
let currentView = '';

const LIB_SERVERS = [{ udn: 'uuid:demo-mediaserver', id: 'uuid:demo-mediaserver', friendlyName: 'Living Room NAS', address: '192.0.2.60' }];

// ---- request handling ------------------------------------------------------
let indexHTML = '';
let iconPNG = null;
const unmocked = new Set();

async function handle(route) {
  const req = route.request();
  const u = new URL(req.url());
  const json = (body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

  if (u.origin === ART_HOST) {
    const k = path.basename(u.pathname, '.svg');
    const [letter, bg] = COVERS[k] || ['?', '#555'];
    return route.fulfill({ status: 200, contentType: 'image/svg+xml', body: coverSVG(letter, bg) });
  }
  if (/radio-browser\.info$/.test(u.hostname)) return json(STATIONS);
  if (u.origin !== ORIGIN) return route.abort(); // nothing else may leave the browser

  const p = u.pathname;
  if (p === '/' || p === '/index.html') {
    // Same substitution the agent does (status_index.go): the speaker's name
    // becomes the page title.
    return route.fulfill({ status: 200, contentType: 'text/html; charset=utf-8', body: indexHTML.replace('<title>ST Reborn</title>', '<title>Living Room</title>') });
  }
  if (p === '/icon.png' || p === '/icon-large.png') return route.fulfill({ status: 200, contentType: 'image/png', body: iconPNG });
  if (p === '/manifest.webmanifest') return json({ name: 'Living Room', icons: [] });
  if (p === '/share.json') return route.fulfill({ status: 404, body: '' }); // share card stays hidden

  if (p === '/api/status') {
    return route.fulfill({ status: 200, contentType: 'application/xml', headers: { 'X-STR-Playing-Key': '2' }, body: STATUS_PLAYING });
  }
  if (p === '/api/stream/title') return json({ title: 'The Demo Band - Morning Light' });
  if (p === '/api/presets') return json(PRESETS);
  if (p === '/api/box/settings') return json(SETTINGS);
  if (p === '/api/agent/version') return json(VERSION);
  if (p === '/api/alarms') return json(ALARMS);
  if (p === '/api/recent') return json(RECENT);
  if (p === '/api/favorites') return json(FAVORITES);
  if (p === '/api/peers') return json(PEERS);
  if (p === '/api/box/zone') return json(zoneDoc(currentView === 'phone-speakers'));
  if (p === '/api/box/zone/volume') return json(zoneVolume(currentView === 'phone-speakers'));
  if (p === '/api/box/sleep') return json({ active: false, minutes: 0 });
  if (p === '/api/queue') return json({ active: false });
  if (p === '/api/position') return json({});
  if (p === '/api/library/servers') return json(LIB_SERVERS);
  if (p === '/api/box/mediaservers') return json([]);
  if (p === '/spotify/info') return json({});
  unmocked.add(req.method() + ' ' + p);
  return json({});
}

// ---- per-view driving ------------------------------------------------------
async function drive(page, view) {
  const tab = async (name) => {
    await page.click('#tab' + name);
    await page.waitForSelector('#panel' + name + ':not([hidden])', { timeout: 9000 });
  };
  const scrollTo = (sel, offset = 0) => page.evaluate(([s, o]) => {
    const e = document.querySelector(s);
    if (e) window.scrollTo(0, e.getBoundingClientRect().top + window.scrollY - o);
  }, [sel, offset]);

  await page.waitForSelector('#presets .preset.has-art', { timeout: 9000 });
  if (view === 'phone-play') {
    await page.evaluate(() => window.scrollTo(0, 0));
  } else if (view === 'phone-alarm') {
    await page.waitForSelector('#alarmList button', { timeout: 9000 });
    await scrollTo('#lblSleep', 70);
  } else if (view === 'phone-browse') {
    await tab('Browse');
    await page.fill('#q', 'rock');
    await page.click('#btnSearch');
    await page.waitForSelector('#results .station, #results button', { timeout: 9000 });
    await page.evaluate(() => window.scrollTo(0, 0));
  } else if (view === 'phone-speakers') {
    await tab('Speakers');
    await page.waitForSelector('#peers .peer', { timeout: 9000 });
    await page.waitForSelector('#groupCard:not([style*="none"]) #members', { timeout: 9000 });
    // The peer list painted before the group was known; repaint it once (the
    // page itself does so every 20 s) so a member no longer offers "+".
    await page.evaluate(() => (typeof loadPeers === 'function' ? loadPeers() : null));
    await page.evaluate(() => window.scrollTo(0, 0));
  }
  await page.waitForTimeout(600); // let art and transitions settle
}

// ---- main ------------------------------------------------------------------
indexHTML = await readFile(path.join(ASSETS, 'index.html'), 'utf8');
iconPNG = await readFile(path.join(ASSETS, 'icon.png'));

const browser = await chromium.launch({ headless: true });
let ok = 0, fail = 0;
try {
  for (const lang of LANGS) {
    await mkdir(path.join(OUT, lang), { recursive: true });
    const ctx = await browser.newContext({
      viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true,
      locale: lang, timezoneId: 'Europe/Berlin',
      userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1',
    });
    await ctx.addInitScript((l) => {
      try {
        localStorage.setItem('str.remote.lang', l);
        localStorage.setItem('coachSeen', '1'); // the first-visit hint bubble
      } catch (e) {}
    }, lang);
    await ctx.route('**/*', handle);
    for (const view of VIEWS) {
      currentView = view;
      const page = await ctx.newPage();
      page.on('pageerror', (e) => console.warn(`  [${lang}/${view}] pageerror:`, e.message));
      try {
        await page.goto(ORIGIN + '/', { waitUntil: 'domcontentloaded' });
        await drive(page, view);
        await page.screenshot({ path: path.join(OUT, lang, view + '.png') });
        console.log(`ok  ${lang}/${view}`);
        ok++;
      } catch (e) {
        console.error(`FAIL ${lang}/${view}: ${e.message}`);
        fail++;
      }
      await page.close();
    }
    await ctx.close();
  }
} finally {
  await browser.close();
}
if (unmocked.size) console.log('\nUnmocked calls (answered {}):\n  ' + [...unmocked].sort().join('\n  '));
console.log(`\nDone: ${ok} ok, ${fail} failed -> ${OUT}`);
if (fail) process.exitCode = 1;
