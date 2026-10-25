// testkit/desktop-mock.mjs: the in-page stand-in for the Go backend.
//
// The desktop UI talks to Go through window.go.main.App.* (the generated
// wailsjs/go/main/App.js calls window.go.main.App[name]) and to Wails through
// window.runtime. installDesktopMock replaces both before the app's own
// scripts run:
//
//   await page.addInitScript(installDesktopMock, { scenario: resolveScenario(sc), demo: DEMO });
//
// It runs INSIDE the page, so it is self-contained: it closes over nothing
// from this module, every fixture comes in through its one JSON argument, and
// it never evals (the app's CSP has no unsafe-eval).
//
// What a test reads back lives on window.__strMock:
//   calls       [{ name, args, t, seq }] every binding call, recorded when CALLED
//               (runtime calls are named 'runtime.BrowserOpenURL' and so on)
//   unmocked    binding names called with neither an override nor a model
//               (strict scenarios only)
//   pageErrors  window 'error' and 'unhandledrejection' messages
//   events      { eventName: number of registered listeners }
//   state       the models' state (play state, volume, presets, zones per host);
//               kept in sessionStorage over a location.reload(), not a new page
//   openCalls / maxOpen   { name: n } calls in flight now / at most at once
//   set(name, spec)  override a binding from now on (spec as in scenario.mjs)
//   emit(event, ...payload)  deliver a backend event to the app's listeners
//   resetCalls()     forget calls, unmocked and maxOpen (not the state)

// Every binding the model below answers on its own, without an override.
// src/testkit-bindings.test.js checks each name against
// wailsjs/go/main/App.d.ts and against the model itself, so a renamed Go
// method turns that test red instead of silently answering null.
export const MODELLED_BINDINGS = [
  'AppInfo', 'AppVersion', 'BoxAgentVersion', 'BoxInstallReachable', 'BoxSettings', 'BrowseLibrary',
  'CheckAppUpdate', 'CheckFirewall', 'CheckStereoBeforeInstall', 'ConsumeStableNameNotice', 'ConsumeWingetUpgradeFailure',
  'CurrentWiFi', 'DiscoverBoxes', 'DissolvePairBeforeInstall', 'DissolveZone', 'FormZone',
  'GetAirplayOpt', 'GetAppFlag', 'GetBoxLanguage', 'GetClockDisplay', 'GetClockFormat24',
  'GetDisplayTrack', 'GetGroupKeys', 'GetKeyActions', 'GetPresets', 'GetQueue', 'GetWebhooks',
  'GetZoneState', 'InstallSTROnBox', 'ListDrives', 'ListMediaServers', 'ListWiFiProfiles',
  'LogClientError', 'MovePreset', 'Next', 'Pause', 'PhoneQR', 'PlaySlot', 'PlayURL', 'Prev',
  'RadioClick', 'RadioLanguages', 'RadioSearch', 'RadioSearchDetailed', 'RadioTags', 'RadioVote',
  'RecentPlayed', 'RefreshKnownBoxes', 'ResolveStationLogo', 'Resume', 'RunSpeakerUpdate',
  'SaveFolderPreset', 'SaveLibraryPreset', 'SaveNativePreset', 'SaveQueuePreset', 'SaveSpotifyPreset',
  'SetAppFlag', 'SetAppLocale', 'SetBoxVolume', 'SetClockDisplay', 'SetDisplayClock', 'SetPreset',
  'SpotifyNowPlaying', 'Status', 'Stop', 'StickConfigs', 'StickVersion', 'StreamBitrate', 'StreamTitleInfo',
  'SuggestBoxLanguage', 'TrackPosition', 'TryWiFiPassword', 'WakeBox',
];

export function installDesktopMock({ scenario, demo }) {
  const sc = scenario;
  const clone = (v) => {
    if (v === undefined) return undefined;
    try { return JSON.parse(JSON.stringify(v)); } catch (e) { return String(v); }
  };

  // ---- storage seed --------------------------------------------------------
  // Written once per tab: a location.reload() keeps what the app (or the test)
  // changed since. Every access is guarded, so a test that makes storage throw
  // still gets a working mock.
  let ss = null;
  try { ss = window.sessionStorage; } catch (e) { ss = null; }
  let reloaded = false;
  try { reloaded = !!ss && ss.getItem('e2e.seeded') === '1'; } catch (e) { reloaded = false; }
  const put = (k, v) => { try { localStorage.setItem(k, v); } catch (e) {} };
  const drop = (k) => { try { localStorage.removeItem(k); } catch (e) {} };
  if (!reloaded) {
    const seed = sc.seed || {};
    if (seed.locale != null) put('locale', seed.locale);
    if (seed.cachedBoxes) {
      put('cachedBoxes', JSON.stringify(seed.cachedBoxes));
      if (sc.current) put('lastBoxDeviceID', sc.current.deviceID);
    } else {
      drop('cachedBoxes');
      drop('lastBoxDeviceID');
    }
    // The one-time celebration popups (world-map invite after the first radio
    // play or install) would cover the lower part of the window.
    put('str.worldMapInvited', '1');
    put('str.worldMapInvitedAll', '1');
    put('str.ui2Preview', seed.ui2 ? '1' : '0');
    if (seed.theme != null) put('a11yTheme', seed.theme); else drop('a11yTheme');
    if (seed.scale != null) put('a11yScale', String(seed.scale)); else drop('a11yScale');
    for (const [k, v] of Object.entries(seed.extraStorage || {})) put(k, String(v));
    try { if (ss) ss.setItem('e2e.seeded', '1'); } catch (e) {}
  }

  // ---- model state ---------------------------------------------------------
  const fresh = () => ({
    playing: clone(sc.playing) || {},   // host -> PLAY_STATE | STANDBY | PAUSE_STATE | STOP_STATE
    item: {},                           // host -> { name, location, art } of what plays
    volume: clone(sc.volume) || {},     // host -> 0..100
    presets: {},                        // host -> preset list (copied on first read)
    zones: clone(sc.zones) || {},       // host -> GetZoneState document
  });
  let state = fresh();
  if (reloaded) {
    try {
      const saved = ss && ss.getItem('e2e.mockState');
      if (saved) state = Object.assign(fresh(), JSON.parse(saved));
    } catch (e) {}
  }
  window.addEventListener('pagehide', () => {
    try { if (ss) ss.setItem('e2e.mockState', JSON.stringify(state)); } catch (e) {}
  });

  const appInfo = Object.assign({}, demo.appInfo, sc.appInfo || {});
  const boxes = sc.boxes || [];
  const boxByHost = (h) => boxes.find((b) => b.host === h) || (sc.current && sc.current.host === h ? sc.current : null);
  const presetsFor = (h) => {
    if (!state.presets[h]) state.presets[h] = clone(demo.presets);
    return state.presets[h];
  };
  const esc = (s) => String(s == null ? '' : s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  const fill = (tpl, vals) => tpl.replace(/%%(\w+)%%/g, (_, k) => (k === 'art' ? vals.art : esc(vals[k])));
  const statusFor = (h) => {
    const b = boxByHost(h);
    const deviceID = (b && b.deviceID) || demo.box.deviceID;
    const ps = state.playing[h] || 'STANDBY';
    if (ps === 'STANDBY') return fill(demo.statusTemplate.standby, { deviceID });
    const it = state.item[h] || {};
    const art = it.art === undefined ? demo.defaultArt : it.art;
    return fill(demo.statusTemplate.playing, {
      deviceID,
      source: it.source || 'INTERNET_RADIO',
      location: it.location || 'http://demobox.local:8888/stream/2',
      itemName: it.name || 'Best Of Rock.FM',
      art: art ? `<art artImageStatus="IMAGE_PRESENT">${art}</art>` : '',
      playState: ps,
    });
  };
  const settingsFor = (h) => {
    const s = clone(demo.settings);
    const b = boxByHost(h);
    if (b) {
      s.info.deviceID = b.deviceID;
      s.info.name = b.name;
      s.info.type = b.model;
    }
    if (typeof state.volume[h] === 'number') s.volume.actual = state.volume[h];
    return s;
  };
  // putPreset stores one slot the way the agent's PUT /api/presets/<slot>
  // does: the new preset replaces the old one whole, it is not merged.
  const putPreset = (h, slot, next) => {
    const list = presetsFor(h);
    const i = list.findIndex((x) => x.slot === slot);
    const p = { ...next, slot };
    if (i >= 0) list[i] = p; else { list.push(p); list.sort((x, y) => x.slot - y.slot); }
  };

  // ---- the model: one answer per binding (names in MODELLED_BINDINGS) ------
  const models = {
    AppInfo: () => clone(appInfo),
    AppVersion: () => appInfo.version,
    CheckAppUpdate: () => ({}), // nothing newer
    CheckFirewall: () => ({ supported: false, checked: false, blocked: false, publicNetwork: false }),
    // Offline speakers come back flagged, like the backend's roster.
    DiscoverBoxes: () => clone(boxes),
    RefreshKnownBoxes: () => clone(boxes),
    GetZoneState: ([h]) => clone(state.zones[h] || {}),
    // Group edits answer ok and change nothing: the UI 2.0 check reads the
    // call itself, and a moving zone document would race its assertions.
    FormZone: () => ({ ok: true }),
    DissolveZone: () => ({ ok: true }),
    WakeBox: () => ({}),
    // Durable "already seen" flags: true keeps the one-time invites away.
    GetAppFlag: () => true,
    SetAppFlag: () => undefined,
    BoxSettings: ([h]) => settingsFor(h),
    BoxAgentVersion: ([h]) => {
      const b = boxByHost(h);
      return { version: (b && b.version) || appInfo.version, build: (b && b.build) || appInfo.build };
    },
    PhoneQR: () => demo.phoneQR,
    GetPresets: ([h]) => clone(presetsFor(h)),
    // The argument orders are those of app_presets.go.
    SetPreset: ([h, , slot, name, url, art, bitrate, homepage, codec]) => {
      putPreset(h, slot, { name, stream_url: url, type: 'radio', art, bitrate, homepage, codec });
      return undefined;
    },
    MovePreset: ([h, , from, to]) => {
      const list = presetsFor(h);
      const a = list.find((x) => x.slot === from);
      const b = list.find((x) => x.slot === to);
      if (a) a.slot = to;
      if (b) b.slot = from;
      list.sort((x, y) => x.slot - y.slot);
      return undefined;
    },
    SaveLibraryPreset: ([h, , slot, name, url, art, bitrate, source, durationSec]) => {
      putPreset(h, slot, { name, stream_url: url, type: 'radio', art, bitrate, source, duration_sec: Math.max(0, durationSec || 0) });
      return undefined;
    },
    // The agent takes the station from the speaker's own now-playing.
    SaveNativePreset: ([h, , slot, name]) => {
      const it = state.item[h] || {};
      putPreset(h, slot, { name: name || it.name || '', stream_url: '', type: 'native', art: it.art });
      return undefined;
    },
    SaveSpotifyPreset: ([h, , slot, name, uri, account]) => {
      putPreset(h, slot, { name, stream_url: '', type: 'spotify', uri, account });
      return undefined;
    },
    // payloadJSON is the preset object the Library tab built, stored verbatim.
    SaveFolderPreset: ([h, , slot, payloadJSON]) => {
      let p = {};
      try { p = JSON.parse(payloadJSON) || {}; } catch (e) { p = {}; }
      putPreset(h, slot, { stream_url: '', ...p, type: p.type || 'queue' });
      return undefined;
    },
    SaveQueuePreset: () => '',
    RecentPlayed: () => clone(demo.recent),
    Status: ([h]) => statusFor(h),
    // Asked once, 2.5 s after boot (main.js), so a short test may or may not
    // meet it. Modelled as "no notice due", what an unmocked null meant.
    ConsumeStableNameNotice: () => false,
    StreamBitrate: () => 256,
    // The live ICY title poll (views/music/live.js) first reads 1.2 s after a
    // proxied station starts, so whether a test meets it depends on how long
    // it runs. Modelled as "no title yet", which is what the null of an
    // unmocked call meant before, so the strict drift guard no longer flips
    // with the machine's speed.
    StreamTitleInfo: () => ({ title: '', swapped: false }),
    PlaySlot: ([h, , slot]) => {
      const p = presetsFor(h).find((x) => x.slot === slot);
      state.playing[h] = 'PLAY_STATE';
      if (p) state.item[h] = { name: p.name, location: p.stream_url, art: p.art };
      return undefined;
    },
    PlayURL: ([h, , url, title, icon]) => {
      state.playing[h] = 'PLAY_STATE';
      state.item[h] = { name: title || url, location: url, art: icon || '' };
      return undefined;
    },
    Pause: ([h]) => { state.playing[h] = 'PAUSE_STATE'; return undefined; },
    Resume: ([h]) => { state.playing[h] = 'PLAY_STATE'; return undefined; },
    Stop: ([h]) => { state.playing[h] = 'STOP_STATE'; return undefined; },
    Next: () => undefined,
    Prev: () => undefined,
    SetBoxVolume: ([h, , pct]) => { state.volume[h] = pct; return undefined; },
    GetQueue: () => ({}), // no queue active
    TrackPosition: () => ({ positionSec: 0, durationSec: 0 }),
    SpotifyNowPlaying: () => ({ bitrate: 0, track: '', artist: '', cover: '', context: '', account: '', premiumRequired: false, audioKeyRefused: false, product: '' }),
    ConsumeWingetUpgradeFailure: () => ({ failed: false, exitCode: '', command: '' }),
    // The remote key map in Speaker settings: AUX snoozes a ringing alarm
    // (#1415), so the app-snooze shot shows the Z marker, and the thumbs keys
    // move the bass up and down (#1494), so app-basskey shows the B markers.
    GetKeyActions: () => ({ keys: { aux: 'snooze', thumbsUp: 'bassUp', thumbsDown: 'bassDown' } }),
    GetClockDisplay: () => 'true',
    SetClockDisplay: () => undefined,
    SetDisplayClock: () => undefined,
    GetClockFormat24: () => true,
    BoxInstallReachable: () => true,
    CheckStereoBeforeInstall: () => ({ known: false, paired: false, status: '', partnerIP: '', partnerOnline: false, partnerHasSTR: false }),
    DissolvePairBeforeInstall: () => ({ allClear: true, results: {} }),
    InstallSTROnBox: ([, model]) => ({ step: 'done', code: '', ok: true, message: '', log: '', firmware: '27.0.6', model: model || '', moduleType: '', variant: '' }),
    RunSpeakerUpdate: ([h, , , name]) => ({ host: h, name: name || '', engineOnly: false, outcome: 'done', version: {}, confirmed: true, verdict: '', engineDelivered: true, engineTooFull: false, quiet: false, unmet: '', attempts: 1, err: '' }),
    ListDrives: () => clone(demo.drives),
    StickVersion: () => '',
    StickConfigs: () => ({ wlanSSID: '', wlanPass: '', region: '', name: '', locale: '' }),
    ListWiFiProfiles: () => clone(demo.wifi),
    TryWiFiPassword: () => '',
    CurrentWiFi: () => 'HomeWiFi',
    SuggestBoxLanguage: () => 3,
    GetBoxLanguage: () => '3',
    GetAirplayOpt: () => ({ enabled: true }),
    GetWebhooks: () => ({ thumb: {}, buttons: {} }),
    GetGroupKeys: () => ({ bindings: {} }),
    // Readable display-track setting; onPanel hides the re-buffer warning.
    GetDisplayTrack: () => ({ enabled: false, mode: 'both', onPanel: true, supported: true }),
    ListMediaServers: () => clone(demo.mediaServers),
    BrowseLibrary: () => clone(demo.libraryPage),
    // Radio search runs app-side (Wails bindings, not /api/radio).
    RadioSearch: () => clone(demo.stations),
    RadioSearchDetailed: () => ({ stations: clone(demo.stations), relaxed: false }),
    RadioLanguages: () => clone(demo.languages),
    RadioTags: () => [],
    RadioClick: () => undefined,
    RadioVote: () => undefined,
    ResolveStationLogo: () => '',
    SetAppLocale: () => undefined,
    LogClientError: () => undefined,
  };

  // ---- recording and overrides ---------------------------------------------
  const overrides = Object.assign({}, sc.bindings || {});
  const counts = {};
  const mock = {
    calls: [], unmocked: [], pageErrors: [], events: {}, scenario: sc, state,
    openCalls: {}, maxOpen: {},
    models: Object.keys(models),
    set(name, spec) { overrides[name] = spec; counts[name] = 0; },
    emit(event, ...payload) { return dispatch(event, payload); },
    resetCalls() {
      mock.calls.length = 0;
      mock.unmocked.length = 0;
      for (const k of Object.keys(mock.maxOpen)) mock.maxOpen[k] = mock.openCalls[k] || 0;
    },
  };
  Object.defineProperty(mock, 'state', { get: () => state, enumerable: true });
  window.__strMock = mock;

  let seq = 0;
  const record = (name, args) => mock.calls.push({ name, args: clone(args), t: Date.now(), seq: seq++ });

  // pick resolves an override spec for the n-th call (0-based) of a binding;
  // null means "no override applies, use the model".
  const pick = (spec, args, n) => {
    if (!spec) return null;
    if (typeof spec.times === 'number' && n >= spec.times) return null;
    if (spec.byHost) {
      const sub = spec.byHost[args[0]];
      if (!sub) return ('value' in spec || 'error' in spec || spec.never || spec.sequence) ? spec : null;
      const s = pick(sub, args, n);
      return s && s.delayMs == null && spec.delayMs != null ? Object.assign({}, s, { delayMs: spec.delayMs }) : s;
    }
    if (Array.isArray(spec.sequence) && spec.sequence.length) {
      const s = spec.sequence[Math.min(n, spec.sequence.length - 1)];
      return s && s.delayMs == null && spec.delayMs != null ? Object.assign({}, s, { delayMs: spec.delayMs }) : s;
    }
    return spec;
  };

  const call = (name, args) => {
    record(name, args);
    const n = counts[name] || 0;
    counts[name] = n + 1;
    const spec = pick(overrides[name], args, n);
    const model = models[name];
    if (!spec && !model && sc.strict && !mock.unmocked.includes(name)) mock.unmocked.push(name);
    mock.openCalls[name] = (mock.openCalls[name] || 0) + 1;
    mock.maxOpen[name] = Math.max(mock.maxOpen[name] || 0, mock.openCalls[name]);
    const done = () => { mock.openCalls[name] -= 1; };
    if (spec && spec.never) return new Promise(() => {}); // stays open for good
    const p = new Promise((resolve, reject) => {
      const answer = () => {
        try {
          // Wails rejects with the Go error's text, a plain string.
          if (spec && 'error' in spec) reject(spec.error);
          else if (spec && 'value' in spec) resolve(clone(spec.value));
          else resolve(model ? model(args) : null);
        } catch (e) {
          reject(String(e && e.message ? e.message : e));
        }
      };
      if (spec && spec.delayMs) setTimeout(answer, spec.delayMs); else answer();
    });
    p.then(done, done);
    return p;
  };

  const IGNORED = new Set(['then', 'toJSON', 'constructor']);
  const App = new Proxy({}, {
    get(_t, p) {
      if (typeof p === 'symbol' || IGNORED.has(p)) return undefined;
      return (...args) => call(p, args);
    },
    has() { return true; },
  });
  window.go = { main: { App } };

  // ---- runtime: a working event registry -----------------------------------
  const listeners = {};
  const count = (name) => { mock.events[name] = (listeners[name] || []).length; };
  const off = (name, entry) => {
    listeners[name] = (listeners[name] || []).filter((e) => e !== entry);
    count(name);
  };
  function dispatch(name, payload) {
    let n = 0;
    for (const entry of (listeners[name] || []).slice()) {
      n++;
      try { entry.cb(...payload); } catch (e) { mock.pageErrors.push(`listener ${name}: ${e && e.message ? e.message : e}`); }
      if (entry.left > 0) {
        entry.left -= 1;
        if (entry.left === 0) off(name, entry);
      }
    }
    return n;
  }
  const noop = () => {};
  const rt = {
    EventsOnMultiple(name, cb, max) {
      const entry = { cb, left: typeof max === 'number' ? max : -1 };
      (listeners[name] = listeners[name] || []).push(entry);
      count(name);
      return () => off(name, entry);
    },
    EventsOn(name, cb) { return rt.EventsOnMultiple(name, cb, -1); },
    EventsOnce(name, cb) { return rt.EventsOnMultiple(name, cb, 1); },
    EventsOff(name, ...more) {
      for (const n of [name, ...more]) { delete listeners[n]; count(n); }
    },
    EventsOffAll() { for (const n of Object.keys(listeners)) { delete listeners[n]; count(n); } },
    EventsEmit(name, ...data) { record('runtime.EventsEmit', [name, ...data]); dispatch(name, data); },
    LogPrint: noop, LogTrace: noop, LogDebug: noop, LogInfo: noop, LogWarning: noop, LogError: noop, LogFatal: noop,
    BrowserOpenURL(url) { record('runtime.BrowserOpenURL', [url]); },
    WindowReload() { record('runtime.WindowReload', []); location.reload(); },
    WindowReloadApp() { record('runtime.WindowReloadApp', []); location.reload(); },
    Quit: noop, WindowSetTitle: noop, WindowShow: noop, WindowHide: noop,
    ClipboardSetText(text) { record('runtime.ClipboardSetText', [text]); return Promise.resolve(true); },
    ClipboardGetText: () => Promise.resolve(''),
    Environment: () => Promise.resolve({ buildType: 'production', platform: 'windows', arch: 'amd64' }),
  };
  window.runtime = new Proxy(rt, {
    get(t, p) {
      if (typeof p === 'symbol' || IGNORED.has(p)) return undefined;
      return p in t ? t[p] : noop;
    },
  });

  // ---- page errors ---------------------------------------------------------
  window.addEventListener('error', (e) => { mock.pageErrors.push(String((e && e.message) || e)); });
  window.addEventListener('unhandledrejection', (e) => {
    const r = e && e.reason;
    mock.pageErrors.push('unhandledrejection: ' + String(r && r.message ? r.message : r));
  });
}
