// testkit/scenario.mjs: what a mocked run of the desktop app looks like.
//
// A scenario is plain data (it crosses into the page as JSON): which speakers
// exist, which one is current, what plays where, what localStorage holds at
// boot, and per-binding or per-route overrides. defaultScenario() is the e2e
// default; the screenshot harness builds its views as patches on top of it.
//
// Binding override spec (scenario.bindings[Name], or __strMock.set at runtime):
//   exactly one of { value } | { error } | { never: true } | { sequence: [spec, ...] }
//   or none of them, which means "the model's answer" (useful with delayMs).
//   Optional: delayMs (page clock, so a fake clock must advance it),
//             times (the override applies to the first n calls, then the model),
//             byHost { '192.0.2.52': spec } (picked by the call's first argument;
//                    a host not listed falls through to the model).
// Route override (scenario.routes['/api/path']): { status, body } | { abort: true }.

import { ROSTER } from './demo-data.mjs';

export function defaultScenario() {
  return {
    // What localStorage holds at boot. theme / scale / locale null = not written.
    // cachedBoxes: true (the roster below), false (nothing cached, no
    // lastBoxDeviceID) or an array of roster keys / box objects.
    seed: { locale: 'en', ui2: false, theme: 'dark', scale: '1', cachedBoxes: true, extraStorage: {} },
    // Roster keys (demo-data.mjs ROSTER) or whole box objects.
    boxes: ['livingRoom', 'bedroom', 'office', 'kitchen'],
    // Fields merged into a roster box, e.g. { livingRoom: { internet: 'unreachable' } }.
    boxPatches: {},
    current: 'livingRoom',
    // Play state per roster key or host. Anything not listed is STANDBY.
    playing: { livingRoom: 'PLAY_STATE', bedroom: 'STANDBY' },
    // GetZoneState answer per roster key or host. Anything not listed is {}.
    zones: {},
    // Starting preset store per roster key or host, replacing the demo's.
    presets: {},
    appInfo: { version: 'v1.0.19', build: '2026-10-23' },
    bindings: {},
    routes: {},
    banners: { appUpdate: false, firewall: false, speakerUpdate: false, noInternet: false, security: false },
    // Strict: every binding call with neither an override nor a model is
    // recorded in __strMock.unmocked (the drift guard reads it).
    strict: false,
  };
}

const isPlainObject = (v) => v !== null && typeof v === 'object' && !Array.isArray(v);

// mergeScenario deep-merges patch into base without touching either. Arrays
// are replaced, not concatenated, and an explicit null replaces too.
export function mergeScenario(base, patch) {
  if (!isPlainObject(patch)) return patch === undefined ? clone(base) : clone(patch);
  const out = isPlainObject(base) ? clone(base) : {};
  for (const [k, v] of Object.entries(patch)) {
    if (v === undefined) continue;
    out[k] = isPlainObject(v) && isPlainObject(out[k]) ? mergeScenario(out[k], v) : clone(v);
  }
  return out;
}

function clone(v) {
  return v === undefined ? undefined : JSON.parse(JSON.stringify(v));
}

// bannerOverrides is the ONE place that knows what makes each top-of-window
// notice appear. It returns a scenario patch; resolveScenario applies it under
// the scenario's own bindings and routes, so a test can still override any
// piece of it. The triggers, read from the code:
//
// - appUpdate: checkAppUpdate (main.js) shows #appUpdateBanner for any
//   CheckAppUpdate answer that carries a version string (the backend only
//   answers one when it is newer). It runs 8 s after boot. An app update also
//   MUTES the speaker-update card (speakerUpdateCardMuted, "app first").
// - firewall: firewallBannerView (firewallbanner.js) needs a CheckFirewall
//   answer with supported, checked and blocked all true, AND either no speaker
//   found by the last sweep or an install waiting for its callback (#1215).
//   So this patch also makes DiscoverBoxes find nobody. The check runs 3 s
//   after boot. It can therefore never show together with a banner that
//   needs a speaker in the list.
// - speakerUpdate: maybeShowSpeakerUpdateCard (main.js) shows
//   #speakerUpdateCard for every non-stock, online speaker boxNeedsUpdate
//   flags; the patch adds the oldAgent roster speaker (v1.0.10).
// - noInternet: checkBoxIssueBanner (main.js, #boxIssueBanner) names every
//   speaker whose record says internet: 'unreachable' (nointernet.js); the
//   patch marks the current speaker that way.
// - security: checkSshBanner (main.js, #globalSecurityBanner) asks the
//   current speaker's /api/stick/status and shows the banner when it answers
//   sshOpen: true without sshPersistent (sshbanner.js). Never on Setup.
export function bannerOverrides(banners = {}, scenario = defaultScenario()) {
  const patch = { bindings: {}, routes: {}, boxPatches: {} };
  if (banners.appUpdate) {
    patch.bindings.CheckAppUpdate = { value: { version: 'v1.0.99', notesUrl: '', downloadUrl: '' } };
  }
  if (banners.firewall) {
    patch.bindings.CheckFirewall = { value: { supported: true, checked: true, blocked: true, blockRules: ['ST Reborn (block)'], publicNetwork: false } };
    patch.bindings.DiscoverBoxes = { value: [] };
  }
  if (banners.speakerUpdate) {
    const boxes = scenario.boxes || [];
    if (!boxes.includes('oldAgent')) patch.boxes = [...boxes, 'oldAgent'];
  }
  if (banners.noInternet) {
    patch.boxPatches[typeof scenario.current === 'string' ? scenario.current : 'livingRoom'] = { internet: 'unreachable', internetReason: 'dns' };
  }
  if (banners.security) {
    patch.routes['/api/stick/status'] = { status: 200, body: { mounted: true, sshOpen: true, sshPersistent: false } };
  }
  return patch;
}

const hostOf = (ref, roster) => (typeof ref === 'string' ? (roster[ref] ? roster[ref].host : ref) : ref && ref.host);

// resolveScenario turns a scenario into what the in-page mock and the route
// handler read: box objects instead of roster keys, play states and zones per
// host, banner patches applied. Idempotent: a resolved scenario passes through.
export function resolveScenario(sc, roster = ROSTER) {
  if (sc && sc.resolved) return sc;
  const user = mergeScenario(defaultScenario(), sc || {});
  const patch = bannerOverrides(user.banners, user);
  const s = patch.boxes ? mergeScenario(user, { boxes: patch.boxes }) : user;
  // A binding or route spec is replaced whole, never merged: the scenario's
  // own { value: {} } must not inherit fields from a banner's answer.
  s.bindings = { ...patch.bindings, ...user.bindings };
  s.routes = { ...patch.routes, ...user.routes };
  s.boxPatches = mergeScenario(patch.boxPatches, user.boxPatches);
  const toBox = (ref) => {
    const base = typeof ref === 'string' ? roster[ref] : ref;
    if (!base) throw new Error(`scenario names an unknown speaker: ${JSON.stringify(ref)}`);
    const key = typeof ref === 'string' ? ref : null;
    const extra = (key && s.boxPatches[key]) || s.boxPatches[base.host] || {};
    return { ...clone(base), ...clone(extra) };
  };
  const boxes = (s.boxes || []).map(toBox);
  const current = s.current == null ? null : (boxes.find((b) => b.host === hostOf(s.current, roster)) || toBox(s.current));
  const byHost = (m) => Object.fromEntries(Object.entries(m || {}).map(([k, v]) => [hostOf(k, roster), v]));
  let cached = null;
  if (s.seed.cachedBoxes === true) cached = boxes;
  else if (Array.isArray(s.seed.cachedBoxes)) cached = s.seed.cachedBoxes.map(toBox);
  const volume = {};
  for (const b of [...boxes, ...(cached || [])]) if (typeof b.volume === 'number') volume[b.host] = b.volume;
  return {
    resolved: true,
    seed: { ...s.seed, cachedBoxes: cached },
    boxes,
    current,
    playing: byHost(s.playing),
    zones: byHost(s.zones),
    presets: byHost(s.presets),
    volume,
    appInfo: s.appInfo,
    bindings: s.bindings,
    routes: s.routes,
    banners: s.banners,
    strict: !!s.strict,
  };
}
