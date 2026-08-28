// utils.js — small, view-independent helper functions.
//
// Everything here is either pure (formatNumber, escapeHtml, debounce)
// or depends only on the DOM skeleton that main.js renders on boot
// (showError, showToast, confirmWarn). No coupling to the state
// object on purpose, so utils can be imported anywhere without
// circular-import risk.

import { t } from './i18n/index.js';

export const $ = (id) => document.getElementById(id);

export function escapeHtml(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => (
    { '&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;' }[c]
  ));
}

export function escapeAttr(s) { return escapeHtml(s); }

// getBoxLabel returns a speaker's display name: its friendly name, else the agent
// name (the backend always fills this with a "str-<ip>" fallback), else the host.
// One place for the label that several views repeated inline.
export function getBoxLabel(b) { return (b && (b.friendlyName || b.name || b.host)) || ''; }

// compareVerBuild compares two (version, build) pairs and returns -1 if A<B,
// 1 if A>B, 0 if equal. version is "vMAJOR.MINOR.PATCH" (a leading "v" and any
// "-N-gHASH" dev suffix are ignored); build is a sortable "YYYY-MM-DD-HHMM"
// stamp used as the tie-breaker. This tells whether the desktop app can upgrade
// a speaker agent (app newer -> offer the OTA) or whether the app itself is
// behind the speaker (app older -> an OTA would DOWNGRADE the box, so offer an
// app update instead). The old logic only checked "differs" and so offered a
// downgrade when the speaker was newer than the app (#105 update-banner UX).
export function compareVerBuild(aVer, aBuild, bVer, bBuild) {
  const parse = (v) => String(v || '').replace(/^v/, '').split('-')[0].split('.').map(n => parseInt(n, 10) || 0);
  const pa = parse(aVer), pb = parse(bVer);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const da = pa[i] || 0, db = pb[i] || 0;
    if (da !== db) return da < db ? -1 : 1;
  }
  const sa = String(aBuild || ''), sb = String(bBuild || '');
  if (sa === sb) return 0;
  return sa < sb ? -1 : 1;
}

// boxModelSupport classifies a Bose /info <type> model string for STR.
// Every SoundTouch-speaking device STR can reach on the LAN is an install
// candidate now: a box reachable by IP installs over the network (stick-free
// via the :17000 unlock + RepairInstallViaSSH), so no model is blocked. The
// classification only frames the install:
//   'supported' - a standalone SoundTouch speaker with the hardware preset
//                 buttons 1-6 STR was built around (10, 20, 30, Portable, Wave)
//   'limited'   - a SoundTouch-speaking device STR still installs on over the
//                 network (soundbar / home-cinema system / Wireless Link Adapter)
//                 but which has no hardware preset buttons 1-6. Installable;
//                 callers add a short note that those buttons will not work.
//   'unknown'   - type absent or unrecognised: treat like 'supported'
//                 (compatibility-first). An odd type string on a real speaker
//                 must still be installable.
// Nothing here blocks an install any more; the old 'unsupported' verdict (which
// dead-ended installs before the stick-free :17000 network path existed) is gone.
export function boxModelSupport(model) {
  const m = String(model || '').toLowerCase().trim();
  if (!m || m === 'soundtouch') return 'unknown';
  // Soundbar / home-cinema / adapter families first: these can themselves
  // contain the word "soundtouch", so they take precedence over the speaker
  // whitelist below. STR installs on them over the network; they just lack the
  // hardware preset buttons, hence 'limited' rather than a hard block.
  if (/\blifestyle\b/.test(m) || /\bcinemate\b/.test(m) || /\bacoustimass\b/.test(m)
      || /soundtouch\s*300\b/.test(m) || /\bsoundbar\b/.test(m)
      || /wireless\s*link\s*adapter/.test(m) || /\badapter\b/.test(m)) {
    return 'limited';
  }
  // Standalone speakers with hardware preset buttons. Match the model number as
  // a whole token so "SoundTouch 30" does not also catch the "SoundTouch 300".
  if (/soundtouch\s*(10|20|30)\b/.test(m) || /\bportable\b/.test(m) || /\bwave\b/.test(m)) {
    return 'supported';
  }
  return 'unknown';
}

// decodeXmlEntities decodes the five named XML entity sequences plus
// numeric character references that the Bose /now_playing XML
// occasionally emits. Without this, "Bryan Adams &amp; Tina Turner"
// would be stored verbatim as a preset name and double-escaped at
// the next render.
export function decodeXmlEntities(s) {
  if (!s) return s;
  return String(s)
    .replace(/&amp;/g, '&')
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&#(\d+);/g, (_, n) => String.fromCharCode(parseInt(n, 10)))
    .replace(/&#x([0-9a-f]+);/gi, (_, h) => String.fromCharCode(parseInt(h, 16)));
}

export function formatNumber(n) {
  if (!n || isNaN(n)) return '0';
  return Number(n).toLocaleString('de-DE');
}

export function debounce(fn, ms) {
  let h = null;
  return (...args) => {
    if (h) clearTimeout(h);
    h = setTimeout(() => { h = null; fn(...args); }, ms);
  };
}

export function sleep(ms) {
  return new Promise(r => setTimeout(r, ms));
}

// savePresetCase is the pure decision behind saveCurrentToSlot: which save
// path applies to the currently playing location.
//   'spotify'   playing via the Spotify engine — save a real Spotify preset.
//   'app-play'  a proxy slot is playing but the app itself started an ad-hoc
//               station within freshMs — trust the app's own record (an
//               agent-side wake resume racing the play can leave the box
//               briefly reporting the PREVIOUS preset, #252).
//   'copy-slot' a proxy slot is playing (hardware key / other soft slot) —
//               copy the source preset one to one.
//   'direct'    a non-proxy stream — save the box-reported now-playing.
// sourceSlot is activeSlotFromLocation(nowLocation), passed in so the caller
// computes it once; null means "not a proxy location".
// nowStreamUrl is the real upstream URL the box report resolves to (the
// caller unwraps the ORION descriptor and the /stream/raw proxy; '' when
// unknown). It exists because a NATIVE ad-hoc play has no slot: the box
// reports the descriptor, sourceSlot is null, and this function used to fall
// through to 'direct' even when the app itself had started the station
// seconds earlier. 'direct' then persisted the descriptor's box-loopback
// art-proxy URL as the key's artwork, which is dead from the user's machine,
// so every hold-saved key lost its logo to the grey-chevron fallback (#696,
// six ST10 keys, MacBook). Unlike the proxy branch above, the app record is
// trusted here ONLY when it matches what the box actually plays: there is no
// wake-resume race to excuse a mismatch (#252's reason does not apply), so a
// station someone started elsewhere still saves from the box report.
export function savePresetCase(nowLocation, sourceSlot, lastAppPlay, nowMs, freshMs, nowStreamUrl) {
  if (/\/spotify\/stream|\/playback\/container/.test(nowLocation || '')) return 'spotify';
  const fresh = !!(lastAppPlay && lastAppPlay.url && nowMs - lastAppPlay.at < freshMs);
  if (sourceSlot !== null && sourceSlot !== undefined) {
    if (fresh) return 'app-play';
    return 'copy-slot';
  }
  if (fresh && nowStreamUrl && lastAppPlay.url === nowStreamUrl) return 'app-play';
  return 'direct';
}

// formatRemaining turns a remaining-ms value into a "m:ss" string for
// countdown UI. Negative or zero inputs return "0:00". Used by the
// stick-install and OTA-wait flows where the previous implementation
// showed elapsed seconds that jumped 3-6s at a time (the value was
// updated once per polling cycle, which is uneven), confusing users
// who could not tell how much wait was left.
// splitUpdateTargets decides who takes part in an "update all" run.
//
// A speaker that is behind gets the full update. A speaker that is already
// current is NOT dropped when its Spotify engine is missing: the engine is the
// other half of what an update delivers, and a speaker that lost it (tight NAND
// freed for the agent, or an update cut short at the restart) used to be left
// out of the whole-house run entirely, so the only way back was to open that one
// speaker and press its own button (Jens, 2026-07-29). The two groups are kept
// apart because they promise different things: the first restarts, the second
// normally does not.
//
// rows: [{ box, needsUpdate, engineMissing }]. A speaker that could not be asked
// (asleep, unreachable) carries engineMissing false and is simply left alone.
export function splitUpdateTargets(rows) {
  const updateTargets = [];
  const engineTargets = [];
  for (const r of rows || []) {
    if (!r || !r.box) continue;
    if (r.needsUpdate) updateTargets.push(r.box);
    else if (r.engineMissing) engineTargets.push(r.box);
  }
  return { updateTargets, engineTargets, targets: updateTargets.concat(engineTargets) };
}

// encodeURIStrict is encodeURIComponent plus the six characters JS deliberately
// leaves alone: ! ' ( ) * ~
//
// Wails' own URL validator rejects a URL containing any of ; | ` $ \ < > * { }
// [ ] ( ) ~ ! space tab CR LF before it ever reaches the OS handler, and it
// runs on BrowserOpenURL. So the "Email support" button, whose body says
// "(Please describe the problem here.)", was refused on every Windows machine
// with "Invalid URL shell metacharacters not allowed": one plain parenthesis.
// The mail window never opened and the user had to write it by hand, which is
// visible in a field log from 2026-08-22, on the line right after the bundle
// was written.
//
// Percent-encoding those six is valid everywhere a percent-encoded octet is,
// so mail clients receive the same text.
export function encodeURIStrict(s) {
  return encodeURIComponent(s).replace(/[!'()*~]/g,
    (c) => '%' + c.charCodeAt(0).toString(16).toUpperCase());
}

export function formatRemaining(ms) {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${String(s).padStart(2, '0')}`;
}

// ---------- Modals ----------
//
// The three modals (warn, error, toast) live in the DOM skeleton in
// main.js. These helpers bind to the well-known IDs the first time
// they are used so the wiring lives alongside the helpers that use
// it — no need for main.js to remember to wire anything.

let warnResolve = null;
let warnWired = false;
// Default label/class of the confirm button, captured from the static
// modal HTML the first time we wire it. The modal is reused across very
// different prompts (destructive warnings AND happy-path invitations),
// so every confirmWarn call resets the button to these defaults unless
// the caller overrides them, otherwise a positive prompt's styling would
// leak into the next destructive one.
let warnConfirmDefaults = null;
function wireWarn() {
  if (warnWired) return;
  warnWired = true;
  const cancel = $('warnCancel');
  const confirm = $('warnConfirm');
  if (confirm) warnConfirmDefaults = { label: confirm.textContent, cls: confirm.className };
  // Clear the resolver as it fires: a stray second click on the same button
  // must not resolve an already-settled promise, and the displacement path
  // above relies on warnResolve being null once a prompt has been answered.
  const settle = (v) => { const r = warnResolve; warnResolve = null; closeWarn(); if (r) r(v); };
  if (cancel)  cancel.onclick  = () => settle(false);
  if (confirm) confirm.onclick = () => settle(true);
}

// confirmWarn(title, bodyHtml, opts?)
//   opts.icon         HTML for the title icon; pass null for no icon (use
//                     for happy-path prompts so there is no warning
//                     triangle). Defaults to the warning triangle.
//   opts.confirmLabel text for the confirm button (default: the static
//                     localized "Proceed anyway").
//   opts.confirmClass class for the confirm button (default: btn-danger).
//                     Pass 'btn btn-primary' for an encouraging CTA.
//   opts.calm         true drops the red alarm colour from the title. The modal
//                     doubles as the confirmation for ROUTINE actions (updating
//                     the speakers), and a red-on-triangle "warning" for the
//                     normal, recommended thing to do reads as "something is
//                     wrong here" and stops users from doing it.
export function confirmWarn(title, bodyHtml, opts) {
  opts = opts || {};
  wireWarn();
  const m = $('warnModal');
  if (!m) return Promise.resolve(false);
  const t = m.querySelector('.warn-title');
  if (t) {
    const icon = (opts.icon === null) ? ''
      : '<span class="warn-icon">' + (opts.icon || '&#9888;') + '</span> ';
    t.innerHTML = icon + escapeHtml(title);
    // Reset on every call: the modal is shared, so a calm prompt must not
    // leave the next destructive one without its alarm colour.
    t.classList.toggle('warn-title-calm', !!opts.calm);
  }
  const body = $('warnBody');
  if (body) body.classList.toggle('warn-body-rich', !!opts.rich);
  const confirm = $('warnConfirm');
  if (confirm && warnConfirmDefaults) {
    confirm.textContent = opts.confirmLabel || warnConfirmDefaults.label;
    confirm.className = opts.confirmClass || warnConfirmDefaults.cls;
  }
  $('warnBody').innerHTML = bodyHtml;
  m.classList.remove('hidden');
  // One modal, one resolver. A second prompt raised while the first is still
  // open used to overwrite the resolver, so the first promise never settled and
  // its caller waited forever with a lock held. Settle the displaced one as
  // "cancelled": its prompt is off screen, so the user never answered it.
  const displaced = warnResolve;
  warnResolve = null;
  if (displaced) { try { displaced(false); } catch { /* caller's problem */ } }
  return new Promise(res => { warnResolve = res; });
}

export function closeWarn() {
  const m = $('warnModal');
  if (m) m.classList.add('hidden');
}

let errorWired = false;
function wireError() {
  if (errorWired) return;
  errorWired = true;
  const close = $('errorClose');
  const copy  = $('errorCopy');
  if (close) close.onclick = () => $('errorModal').classList.add('hidden');
  if (copy)  copy.onclick  = async () => {
    const txt = $('errorText').value;
    try {
      await navigator.clipboard.writeText(txt);
      copy.textContent = t('modal.copied');
      setTimeout(() => { copy.textContent = t('modal.copy'); }, 1500);
    } catch {
      $('errorText').select();
      document.execCommand('copy');
    }
  };
}

export function showError(msg) {
  wireError();
  const m = $('errorModal');
  if (!m) return;
  let text = String(msg || t('modal.unknownError'));
  // The radio-directory outage error carries a stable marker followed by raw
  // mirror detail (which can include the bare last-resort IP mirror, e.g.
  // https://91.98.4.78 - alarming-looking to users, field 2026-07-26). Show
  // the friendly localized message instead; the raw detail stays in app.log.
  if (text.includes('radio directory unreachable')) {
    text = t('radio.mirrorsDown');
  }
  $('errorText').value = text;
  m.classList.remove('hidden');
}

let toastTimer = null;
// showToast displays msg for ms milliseconds. ms <= 0 keeps it up until the
// next showToast call replaces it - used for "in progress" states so a slow
// background action (e.g. a preset+login transfer over the LAN) does not look
// idle and invite a second button press.
export function showToast(msg, ms = -1) {
  const t = $('toast');
  if (!t) return;
  t.textContent = msg;
  t.classList.add('show');
  if (toastTimer) { clearTimeout(toastTimer); toastTimer = null; }
  // ms 0 = sticky (the caller hides it later, e.g. the copy-presets progress).
  if (ms === 0) return;
  // Default: scale the display time with the amount of text. The old fixed
  // 2.2 s was too short to read a two-line message (discussion #406); bounded
  // so short toasts stay snappy and long ones do not linger forever. An
  // explicit positive ms still wins.
  if (ms < 0) {
    const words = String(msg).trim().split(/\s+/).length;
    ms = Math.min(8000, Math.max(2600, 1200 + words * 320));
  }
  toastTimer = setTimeout(() => t.classList.remove('show'), ms);
}

// hideToast takes a sticky toast (showToast(msg, 0)) back down. Used by the
// long pre-flight checks that keep a "working on it" line up while they run.
export function hideToast() {
  const t = $('toast');
  if (toastTimer) { clearTimeout(toastTimer); toastTimer = null; }
  if (t) t.classList.remove('show');
}

// ---- Dismissible notices ----
//
// A dismissal covers EXACTLY the version it was made for. Clicking "version X
// is out" away means "not this one, not now"; the next version is a new piece
// of news and gets to say so, and the user clicks that one away in turn or
// installs it.
//
// This used to swallow the next version too, so a user who dismissed once
// stopped hearing about the release after it as well and only saw the notice
// again two versions later. That reads as "the update notice never comes back"
// (Jens, 2026-08-12), which is exactly what a release notice must not do.
//
// The comparison is a plain string equality on the version, never arithmetic,
// so 0.9.34 -> 0.10.0 needs no special case: it is simply a different version.

function dismissKey(kind) { return 'str.dismissed.' + kind; }

// localStorage is not always there (private mode, and the test runner has no
// DOM on purpose). Fall back to memory: dismissals then last for the session
// rather than across restarts, which is far better than throwing.
const dismissMem = new Map();
function storeGet(k) {
  try {
    if (typeof localStorage !== 'undefined') return localStorage.getItem(k);
  } catch { /* fall through to memory */ }
  return dismissMem.has(k) ? dismissMem.get(k) : null;
}
function storeSet(k, v) {
  try {
    if (typeof localStorage !== 'undefined') { localStorage.setItem(k, v); return; }
  } catch { /* fall through to memory */ }
  dismissMem.set(k, v);
}
function storeDel(k) {
  try {
    if (typeof localStorage !== 'undefined') { localStorage.removeItem(k); return; }
  } catch { /* fall through to memory */ }
  dismissMem.delete(k);
}

function readDismissal(kind) {
  try {
    const raw = storeGet(dismissKey(kind));
    if (!raw) return null;
    const d = JSON.parse(raw);
    if (!d || typeof d.version !== 'string' || !d.version) return null;
    return { version: d.version };
  } catch { return null; }
}

// dismissNotice records that the user clicked this notice away at `version`.
export function dismissNotice(kind, version) {
  if (!version) return;
  storeSet(dismissKey(kind), JSON.stringify({ version: String(version) }));
}

// noticeDismissed reports whether the notice for `version` is currently
// dismissed, which is true only for the exact version that was clicked away.
// Any other version clears the record on the way past, so nothing stale is left
// behind for a version that will never be offered again.
export function noticeDismissed(kind, version) {
  const d = readDismissal(kind);
  if (!d || !version) return false;
  if (String(version) === d.version) return true;
  storeDel(dismissKey(kind));
  return false;
}

// clearNoticeDismissal forgets a dismissal, e.g. once the user acts on the
// notice anyway.
export function clearNoticeDismissal(kind) {
  storeDel(dismissKey(kind));
}

// activeSlotFromLocation extracts the slot number from a stream proxy
// URL like http://127.0.0.1:8888/stream/3. Since build 2335 the
// speaker's content items always run through the proxy, so the older
// direct-URL comparison no longer matches. The slot match keeps the
// green "playing" highlight stable even when the real CDN URL
// rotates its tokens.
export function activeSlotFromLocation(loc) {
  if (!loc) return null;
  // Spotify presets point the box at the per-slot /spotify/stream-<slot>.ogg
  // (both the hardware and the soft recall), so the slot is in the URL: prefer
  // it so the right Spotify tile lights up even when several presets share the
  // generic "Spotify" now-playing name.
  const sp = loc.match(/\/spotify\/stream-(\d+)\.ogg/);
  if (sp) return parseInt(sp[1], 10);
  const m = loc.match(/\/stream\/(\d+)(?:[/?#]|$)/);
  if (m) return parseInt(m[1], 10);
  // A NATIVE radio preset does not carry the proxy URL in its location at all:
  // the speaker plays it itself and the content item is the ORION descriptor
  // /station?data=<base64url JSON>, with the proxy URL inside the payload. The
  // plain match above therefore stopped finding anything the moment presets
  // migrated to the native form, so no tile lit up and the app looked like it
  // had lost track of what was playing (reported in discussion #555:
  // "die Sender werden nicht mehr als abgespielt angezeigt", with the speaker
  // reporting source=LOCAL_INTERNET_RADIO, PLAY_STATE and the station name).
  return slotFromOrionStation(loc);
}

// slotFromOrionStation digs the slot out of an ORION station descriptor, or
// returns null when the location is not one. Failure is silent on purpose: this
// runs on every status poll and a malformed payload must cost a highlight, not
// throw inside the render.
export function slotFromOrionStation(loc) {
  const payload = orionStationPayload(loc);
  const url = payload && payload.streamUrl;
  if (!url) return null;
  const m = String(url).match(/\/stream\/(\d+)(?:[/?#]|$)/);
  return m ? parseInt(m[1], 10) : null;
}

// orionStationPayload decodes the ORION station descriptor the speaker reports
// while it plays a native radio preset, or returns null for any other location.
//
// The descriptor is the ONLY record of what is playing in that case: the
// speaker fetches the stream itself, so its now-playing location is the
// envelope rather than a URL anything can play. Everything needed to save the
// station again is inside it, which is what makes saving from a native preset
// possible at all: the display name, the logo, and the stream URL (itself one
// of STR's proxy forms, which decodeProxyUrl unwraps).
//
// Failure is silent on purpose: this runs on every status poll, and a
// malformed payload must cost a highlight, not throw inside the render.
export function orionStationPayload(loc) {
  const d = (loc || '').match(/[?&]data=([A-Za-z0-9\-_=+/]+)/);
  if (!d) return null;
  try {
    // The agent writes unpadded base64url; older builds wrote the standard
    // alphabet. Accept both, like the agent's own decoder does.
    let b64 = d[1].replace(/-/g, '+').replace(/_/g, '/');
    while (b64.length % 4) b64 += '=';
    const payload = JSON.parse(atob(b64));
    return (payload && typeof payload === 'object') ? payload : null;
  } catch {
    return null;
  }
}

// nativeSlotStale decides whether a preset tile the active-slot number points
// at should still light up while the speaker plays a NATIVE radio descriptor.
// The speaker keeps playing the station it recalled even after the box's own
// preset list is re-synced (edited from the Bose remote / ST Remote), so a slot
// can come to hold a DIFFERENT station than the one still coming out of the
// speaker. Matching by slot number alone then lights the freshly-changed tile
// even though the audio is another station (#758: "preset key should not stay
// selected ... when the key's new station no longer matches what is playing").
//
// It suppresses ONLY when the playing station's identity is positively known
// (its name or its stream URL) AND that identity does not match the preset's
// current content. When the descriptor yields no usable identity we cannot
// judge and keep the historical behavior (leave the tile lit), so a valid
// native play never goes dark.
export function nativeSlotStale({ presetName, presetUrl, playingName, playingUrl }) {
  const haveIdentity = !!playingUrl || !!playingName;
  if (!haveIdentity) return false;
  if (playingUrl && presetUrl && presetUrl === playingUrl) return false;
  if (playingName && presetName && presetName === playingName) return false;
  return true;
}

// balanceLabel renders a stereo-balance reading (-7..+7, 0 = centred) as the
// localized label the three balance displays share.
export function balanceLabel(v) {
  return v === 0
    ? t('controls.balanceCentre')
    : (v < 0 ? t('controls.balanceLeft', { n: Math.abs(v) })
             : t('controls.balanceRight', { n: v }));
}

// bassControlsDisabled is the ONE gate every bass control in the settings view
// reads: the slider, the "default" reset button next to it, and the reset
// handler itself.
//
// bass.available is boxapi's reading of the speaker's own /bassCapabilities
// (bassAvailable), and it is false BOTH when the speaker says it has no
// adjustable bass and when that capabilities read did not come back at all.
// The two are indistinguishable from here, which is why nothing downstream may
// claim more than "not reported". Either way there is no bass level for STR to
// set: the range collapses to 0..0 and a POST of the default can only be a
// no-op. The slider has always been greyed out on that reading; the reset
// button was not, so it stayed clickable next to a dead slider. A Lifestyle 650
// owner is looking at exactly that pair (mail with photo, 2026-08-22: slider
// greyed at 0..0, "Standard" button live), on a system whose bass sits in the
// separate bass module.
//
// What the mail does NOT establish is what the speaker answers to that POST,
// so do not write "it returns 502" into this file: the agent maps every
// SetBass failure to 502, but nobody has seen this speaker's reply. The gate
// stands on the capability reading alone.
//
// Keeping the decision here rather than inline in the template is what makes it
// testable: the settings view renders through innerHTML and the vitest
// environment is deliberately DOM-free, so a shared helper is the only place
// the gate can be pinned down.
export function bassControlsDisabled(bass) {
  return !(bass && bass.available);
}

// bassSliderProps is the ONE mapping between the speaker's bass reading and
// the settings slider. The slider is RELATIVE to the speaker's default
// (0 = default), which is why min/max/value all subtract it; the change
// handlers add it back before posting.
//
// step exists because of the capability-gated tone-controls bass route
// (home-theater systems; the greyed-slider Lifestyle 650 mail of 2026-08-22
// is the field case): that route imposes a write granularity that can exceed
// 1, and a slider stepping by 1 would post values the firmware is free to
// refuse. The agent maps that route's default to 0, so relative equals
// absolute there. Classic boxes report no step and keep the historic step
// of 1.
//
// Shared helper rather than inline template math for the same reason as
// bassControlsDisabled above: the settings view renders through innerHTML
// and the vitest environment is deliberately DOM-free, so this is the only
// place the mapping can be pinned by a test.
export function bassSliderProps(bass) {
  const b = bass || {};
  const def = b.default || 0;
  return {
    min: (b.min || 0) - def,
    max: (b.max || 0) - def,
    step: b.step > 1 ? b.step : 1,
    value: (b.actual || 0) - def,
  };
}

// shouldAdoptPresetArt answers one question the preset grid and the status poll
// have to agree on: may the logo of the station playing right now be written
// onto the key it was recalled from? Yes only while that key has no logo of its
// own, so the tile takes the speaker's <art> once and then keeps the value it
// saved, and never for a Spotify key (those draw the Spotify logo on purpose,
// and SetPreset is radio-only, so writing art there would clobber the URI).
//
// It matters that this is a shared gate: the grid uses it to decide what to
// draw, and refreshStatus uses it to decide whether a redraw is worth it at
// all. Until 2026-08-23 the grid was rebuilt every 12 s by the live-title
// poller and a late <art> tag was picked up by accident; the key stopped
// showing the running title that day (duplicate ticker, user report), the
// rebuild went with it, and a logo arriving one poll after the location had
// nothing left to trigger it.
//
// Extracted rather than written inline for the same reason as
// bassControlsDisabled: renderPresets builds its markup through innerHTML and
// the vitest environment is deliberately DOM-free, so a helper is the only
// place this rule can be pinned down by a test.
export function shouldAdoptPresetArt(icon, preset) {
  return !!icon && !!preset && !preset.art && preset.type !== 'spotify';
}

// appArtFromBoxArt turns art the way the BOX carries it into art the app may
// persist or draw. It is the artwork mirror of decodeProxyUrl (main.js): the
// preset store must hold the station's real image URL, never one of the
// agent's own wrappers.
//
// Two box-only shapes exist, both built for the SPEAKER's display by
// internal/webui/lir.go (the speaker fetches its artwork itself and cannot do
// https, so the agent wraps the image in its loopback art proxy):
//   http://127.0.0.1:8888/art?u=<base64url real URL>   the wrapped station art
//   http://127.0.0.1:8888/icon.png                     STR's stand-in logo
// Both reach the app through the ORION descriptor's imageUrl and through the
// box's now-playing <art> tag. Persisted app-side they are poison: 127.0.0.1
// is not the agent on the user's machine, so the tile's <img> errors over to
// the DuckDuckGo stream-host icon, whose 404 answer is a grey-chevron image
// body the webview renders without firing onerror (see app_library.go). That
// is exactly how six hold-saved keys on an ST10 lost their artwork while the
// one station whose stream host has a real DDG icon, WDCB 90.9, kept it
// (#696).
//
// So, per candidate of the pipe-separated chain: an /art?u= wrapper (any
// host: the LAN form dies with the next DHCP lease, and the store rule is the
// origin URL, same as for streams) is unwrapped back to the real image URL; a
// box-loopback URL that is not a wrapper (above all the /icon.png stand-in,
// which is STR's logo, not the station's) is dropped; everything else,
// including data: URIs and unparsable values, passes through untouched.
// boxArtKind classifies one parsed art candidate: 'wrapper' for the agent's
// /art?u= form, 'local' for any other box-loopback URL (above all the
// /icon.png stand-in), null for everything else, non-URLs included, which are
// not the box's shapes. Shared by appArtFromBoxArt (which rewrites) and
// artCarriesBoxForm (which only detects), so the two can never disagree on
// what counts as box-form.
function boxArtKind(u) {
  if (!u || (u.protocol !== 'http:' && u.protocol !== 'https:')) return null;
  if (u.pathname === '/art' && u.searchParams.has('u')) return 'wrapper';
  if (u.hostname === '127.0.0.1' || u.hostname === 'localhost' || u.hostname === '[::1]' || u.hostname === '::1') {
    return 'local';
  }
  return null;
}

export function appArtFromBoxArt(art) {
  const out = [];
  for (const c of String(art || '').split('|')) {
    const cand = c.trim();
    if (!cand) continue;
    let u = null;
    try { u = new URL(cand); } catch { /* not a URL: keep as-is below */ }
    const kind = boxArtKind(u);
    if (kind === 'wrapper') {
      try {
        // Unpadded base64url, like the agent writes it; pad and swap the
        // alphabet the same way orionStationPayload does.
        let b64 = u.searchParams.get('u').replace(/-/g, '+').replace(/_/g, '/');
        while (b64.length % 4) b64 += '=';
        const real = atob(b64);
        if (/^https?:\/\//i.test(real)) { if (!out.includes(real)) out.push(real); }
      } catch { /* undecodable wrapper: certainly not station art, drop */ }
      continue;
    }
    if (kind === 'local') {
      continue; // box-local, unreachable from the app and not the station's art
    }
    if (!out.includes(cand)) out.push(cand);
  }
  return out.join('|');
}

// artCarriesBoxForm reports whether a stored art chain holds ANY box-form
// candidate (the agent's /art?u= wrapper or a box-loopback URL). A key saved
// that way needs a real logo re-lookup, not only the render-time unwrap:
// unwrapping gives back the ONE URL the agent had picked as box-drawable, and
// for the #696 reporter's playing key that is a DuckDuckGo icon URL which
// answers 404 (probed 2026-08-24), whose body is the grey-chevron image the
// webview draws without firing onerror, so the tile's fallback cascade ends
// right there. Only healPresetLogos can put a full working chain back.
export function artCarriesBoxForm(art) {
  for (const c of String(art || '').split('|')) {
    const cand = c.trim();
    if (!cand) continue;
    let u = null;
    try { u = new URL(cand); } catch { continue; }
    if (boxArtKind(u) !== null) return true;
  }
  return false;
}
