// views/music/presets.js: loading the preset keys of the selected speaker, the
// change recheck, the lost-preset notice, logo healing and the transfer row.

import {
  BoxPresets,
  BoxSnapshot,
  CopyPresetsAcrossBoxes,
  GetAppFlag,
  GetPresets,
  RadioSearch,
  RecallBoxPreset,
  SetAppFlag,
  SetPreset,
} from '../../api.js';
import { presetCopyConflict } from '../../copyreport.js';
import { boxPresetRows, copyRowsNote, keyNum } from '../../presetrows.js';
import { t } from '../../i18n/index.js';
import { extractHost, stationLogoChain } from '../../logos.js';
import { isNativeServicePreset, nativeServiceLabel } from '../../nativeservice.js';
import { state } from '../../state.js';
import {
  $,
  appArtFromBoxArt,
  artCarriesBoxForm,
  confirmWarn,
  isLibraryPreset,
  escapeAttr,
  escapeHtml,
  friendlyErrorText,
  getBoxLabel,
  isLostStrKey,
  presetSignature,
  showError,
  showToast,
} from '../../utils.js';
import { renderPresets } from './presettiles.js';
import { mstate } from './state.js';
import { transferReport } from './ui2radio.js';

let loadedPresetsBoxKey = null;

// How often the preset list is re-read from the speaker. Deliberately slower
// than the status poll: this only has to catch a change made somewhere else,
// which is a human pressing save on their phone, and nothing here is worth
// another request per status tick.
const PRESET_RECHECK_MS = 15000;

// refreshPresetsIfChanged re-reads the presets and redraws only when they
// differ from what is on screen. Fired from the status poll, never awaited.
//
// It reads the agent's own preset store, not the Bose firmware, so it does not
// add to the :8090 load that keeps the status cadence deliberately slow.
export async function refreshPresetsIfChanged() {
  const box = state.currentBox;
  if (!box || state.view !== 'box') return;
  if (Date.now() - mstate._lastPresetCheck < PRESET_RECHECK_MS) return;
  mstate._lastPresetCheck = Date.now();
  let fresh;
  try {
    fresh = await GetPresets(box.host, box.port) || [];
  } catch {
    return; // a missed poll changes nothing; the next one tries again
  }
  if (state.currentBox !== box) return; // speaker switched while we were reading
  // Same transient-empty guard loadPresets uses: a busy speaker can answer
  // with zero presets while its store reloads, and taking that at face value
  // is what used to make the whole grid vanish.
  if (fresh.length === 0 && state.presets.length > 0) return;
  if (presetSignature(fresh) === presetSignature(state.presets)) return;
  state.presets = fresh;
  renderPresets();
  healPresetLogos();
  loadBoxPresets();
}

// isMeasurableStreamPreset reports whether a measured bitrate is worth storing
// on this key at all.
//
// It is worth it for a live radio stream, where the rate is a property of the
// broadcast and the directory often has none. It is meaningless for a file on a
// media server, whose rate is a property of the file and which the library
// already reads, and it is actively harmful there: persisting it runs the key
// through a radio-shaped write that drops the source field, so the next press
// went through the radio relay instead of fetching the file (#978). On one
// reporter's speaker that rewrote the same slot about twenty times in
// twenty-five minutes, which is flash wear for a number that means nothing.
export function isMeasurableStreamPreset(p) {
  if (!p) return false;
  if (p.type === 'spotify' || p.type === 'queue') return false;
  if (isNativeServicePreset(p)) return false;       // Pandora / iHeartRadio
  if (p.source) return false;                       // from a media server
  if (p.items && p.items.length) return false;      // a saved folder
  return true;
}

// setPresetIfUnchanged persists a METADATA self-heal (a measured bitrate, a
// healed logo) for a slot, but only after re-reading the store and proving the
// slot still holds the station the patch was computed for. The cached preset
// list can be up to PRESET_RECHECK_MS behind, and a fire-and-forget SetPreset
// built from that cache silently reverts a save another client made in
// between: the #758 reporter saved a station from the phone page and this app
// wrote the previous station back over it six seconds later, every time, for
// six rounds. A metadata write must never decide WHICH station a key holds,
// so on any mismatch it adopts the fresh list and walks away.
export async function setPresetIfUnchanged(box, cached, patch) {
  let fresh;
  try {
    fresh = await GetPresets(box.host, box.port) || [];
  } catch { return false; }
  if (state.currentBox !== box) return false;
  const live = fresh.find(x => x.slot === cached.slot);
  // A metadata heal must not be able to change WHAT a key is. SetPreset sends a
  // fixed type=radio and exactly seven fields, so writing it onto a key that
  // carries anything else silently flattens it: a media-library key lost the
  // source field that decides whether the track is fetched from the server or
  // pulled through the radio relay, and a folder key would lose its track list.
  // Guarded here rather than at each caller, because this is the only place
  // that has the LIVE preset in hand (#978).
  const flattenable = live && (live.type === 'spotify' || live.type === 'queue' ||
    isNativeServicePreset(live) || live.source || live.uri || (live.items && live.items.length));
  const held = live && !flattenable &&
    live.stream_url === cached.stream_url && live.name === cached.name;
  if (!held) {
    // The slot changed under us (or the read raced a save): show reality
    // instead of overwriting it. Same transient-empty guard the pollers use.
    if (fresh.length > 0 || state.presets.length === 0) {
      state.presets = fresh;
      mstate._lastPresetCheck = Date.now();
      renderPresets();
    }
    return false;
  }
  try {
    await SetPreset(box.host, box.port, live.slot, live.name, live.stream_url,
      patch.art !== undefined ? patch.art : (live.art || ''),
      patch.bitrate !== undefined ? patch.bitrate : (live.bitrate || 0),
      live.homepage || '', live.codec || '');
    return true;
  } catch { return false; }
}

export async function loadPresets(retry = 0) {
  if (!state.currentBox) return;
  // Drop another box's box-native presets the moment we switch, so a Deezer tile
  // from the previous speaker never flashes on this one's empty slot. Kept across
  // same-box refreshes so the tiles don't flicker on every reload.
  const boxKey = state.currentBox.host + ':' + state.currentBox.port;
  // The previous speaker's STORE tiles go too. They used to stay clickable on
  // the new speaker until its own read landed (1.5 s, longer when the read
  // threw), and a click in that window sent play(slot) to a speaker whose
  // store had nothing on that key: the "slot rejected ... preset not
  // configured" 404 in the #882 bundle. The transient-empty retry below still
  // applies to same-box refreshes, which this does not touch.
  if (boxKey !== loadedPresetsBoxKey) {
    state.boxPresets = []; state.boxSnapshot = null; state.presets = []; loadedPresetsBoxKey = boxKey;
  }
  if (state.presets.length === 0) {
    $('presets').innerHTML = `<div class="muted small grid-loading">${escapeHtml(t('preset.loading'))}</div>`;
  }
  try {
    const fresh = await GetPresets(state.currentBox.host, state.currentBox.port) || [];
    // Guard against a transient empty result. The box can briefly return zero
    // presets while it is busy (switching source for a play) or its store is
    // reloading, even though presets.json is intact. Overwriting with [] made all
    // presets "vanish" from the grid after playing a radio, then reappear on the
    // next save (a display-only loss; the box never lost them). If we suddenly
    // read empty but currently have presets, retry once before trusting it and
    // keep the current presets meanwhile, so the grid never flashes empty. A
    // genuinely empty box (or an empty result that persists past the retry) is
    // still taken at face value.
    if (fresh.length === 0 && state.presets.length > 0 && retry < 1) {
      setTimeout(() => loadPresets(retry + 1), 1500);
      return;
    }
    state.presets = fresh;
    // This IS a fresh read, so the background re-check starts its interval
    // from here instead of firing again right after a box switch or a save.
    mstate._lastPresetCheck = Date.now();
    renderPresets();
    healPresetLogos();
    loadBoxPresets();
    loadBoxSnapshot();
  } catch {
    if (retry < 1) {
      setTimeout(() => loadPresets(retry + 1), 1500);
      return;
    }
    if (state.presets.length > 0) {
      renderPresets();
    } else {
      $('presets').innerHTML = `<div class="muted small">${escapeHtml(t('preset.speakerUnreachable'))}</div>`;
    }
  }
}

// loadBoxPresets reads the box's OWN presets (including foreign sources like
// Deezer that STR did not set) so a slot STR does not manage can still be shown
// and recalled. Best-effort: on error or empty, the grid just shows no
// box-native tiles. The box reports these over gabbo; the agent serves the
// cached list, so this is one cheap app-side read, no box poll.
async function loadBoxPresets() {
  if (!state.currentBox) { state.boxPresets = []; return; }
  try {
    state.boxPresets = await BoxPresets(state.currentBox.host, state.currentBox.port) || [];
  } catch { state.boxPresets = []; return; }
  renderPresets();
  renderPresetLossNotice();
}

let loadedSnapshotBoxKey = null;

// loadBoxSnapshot reads the agent's pre-takeover snapshot of the box (presets +
// sources captured before STR took over the box's cloud endpoints). It is the
// only record of account-linked cloud sources (e.g. Deezer) that STR cannot
// carry over yet: once STR is on the box, the box's next account sync drops
// those sources and the presets bound to them. One cheap app-side read per box
// (the agent serves a cached NAND file); on error the notice simply never shows.
async function loadBoxSnapshot() {
  if (!state.currentBox) { state.boxSnapshot = null; renderPresetLossNotice(); return; }
  const boxKey = state.currentBox.host + ':' + state.currentBox.port;
  if (boxKey === loadedSnapshotBoxKey && state.boxSnapshot) { renderPresetLossNotice(); return; }
  loadedSnapshotBoxKey = boxKey;
  let snap = null;
  try { snap = await BoxSnapshot(state.currentBox.host, state.currentBox.port); } catch { snap = null; }
  if (snap && snap.captured === false) snap = null;
  if (snap) {
    const dev = snap.deviceID || boxKey;
    try { snap._dismissed = await GetAppFlag('box-loss-notice:' + dev); } catch { snap._dismissed = false; }
  }
  state.boxSnapshot = snap;
  renderPresetLossNotice();
}

// lostPresetsNow returns the snapshot's account-linked presets that are no
// longer present on the box (the slot is empty in both STR's presets and the
// box's own presets), i.e. the ones STR's takeover dropped. A Deezer preset that
// still shows as a box-native tile is intentionally NOT reported.
function lostPresetsNow() {
  const snap = state.boxSnapshot;
  if (!snap || !Array.isArray(snap.lostPresets)) return [];
  return snap.lostPresets.filter((lp) => {
    const live = state.presets.find((x) => x.slot === lp.slot) ||
      (state.boxPresets || []).find((x) => x.slot === lp.slot);
    return !live;
  });
}

// lostStrKey reports whether a speaker-side preset is a key STR itself wrote
// that the store no longer backs (#882). The decision is isLostStrKey in
// utils.js, where it can be tested: the agent's verdict when it sends one
// (it alone knows the webhook-only keys, #536, which look exactly like dead
// keys from the location), the location otherwise. Called only for slots the
// store has nothing on, so a true answer means a dead key.
export function lostStrKey(bp) {
  return isLostStrKey(bp);
}

// lostStrKeysNow lists the dead STR keys of the current speaker: speaker-side
// slots STR wrote whose store slot is empty.
function lostStrKeysNow() {
  return (state.boxPresets || []).filter((bp) =>
    lostStrKey(bp) && !state.presets.find((x) => x.slot === bp.slot));
}

// The dead-key banner is dismissed per speaker for this app run only. A
// persisted flag would hide it after the NEXT reinstall too, and the keys it
// names are gone from the grid anyway once they are copied back (or once the
// agent prunes them), so it stops showing by itself.
const lostKeysDismissed = new Set();

// renderPresetLossNotice shows a dismissible banner above the preset grid when
// the box dropped account-linked presets STR cannot carry over (e.g. Deezer),
// listing the affected slots so the user knows what was there. Since #882 it
// also covers dead STR keys: speaker-side slots STR wrote before a reinstall
// whose store slot is now empty, with the way to get them back. Idempotent:
// re-creates or removes a single #preset-loss-notice element each call.
function renderPresetLossNotice() {
  const grid = $('presets');
  if (!grid || !grid.parentNode) return;
  let el = document.getElementById('preset-loss-notice');
  const snap = state.boxSnapshot;
  const lost = lostPresetsNow();
  const services = (snap && Array.isArray(snap.lostServices)) ? snap.lostServices : [];
  const showSnapshot = !!snap && !snap._dismissed && lost.length > 0 && services.length > 0;
  const boxKey = state.currentBox ? (state.currentBox.host + ':' + state.currentBox.port) : '';
  const lostKeys = lostKeysDismissed.has(boxKey) ? [] : lostStrKeysNow();
  if (!showSnapshot && lostKeys.length === 0) {
    if (el) el.remove();
    return;
  }
  if (!el) {
    el = document.createElement('div');
    el.id = 'preset-loss-notice';
    el.className = 'loss-notice';
    grid.parentNode.insertBefore(el, grid);
  }
  let title, body;
  if (showSnapshot) {
    const svc = services.map((s) => boxSourceLabel(s)).join(', ');
    const slots = lost.map((lp) => `${lp.slot} (${lp.name || boxSourceLabel(lp.source)})`).join(', ');
    title = t('preset.lossTitle', { service: svc });
    body = t('preset.lossBody', { service: svc, slots });
  } else {
    const slots = lostKeys.map((bp) => `${bp.slot} (${bp.name || boxSourceLabel(bp.source)})`).join(', ');
    title = t('preset.lostKeysTitle', { n: lostKeys.length });
    body = t('preset.lostKeysBody', { slots });
  }
  el.innerHTML =
    `<div class="loss-notice-body">` +
      `<strong>${escapeHtml(title)}</strong>` +
      `<div class="small">${escapeHtml(body)}</div>` +
    `</div>` +
    `<button type="button" class="loss-notice-dismiss" aria-label="${escapeAttr(t('preset.lossDismiss'))}">&times;</button>`;
  const btn = el.querySelector('.loss-notice-dismiss');
  if (btn) {
    btn.addEventListener('click', async () => {
      if (showSnapshot) {
        if (state.boxSnapshot) state.boxSnapshot._dismissed = true;
        const dev = (snap && snap.deviceID) ||
          (state.currentBox && (state.currentBox.host + ':' + state.currentBox.port));
        try { await SetAppFlag('box-loss-notice:' + dev); } catch { /* best-effort */ }
      } else {
        lostKeysDismissed.add(boxKey);
      }
      el.remove();
    });
  }
}

// boxSourceLabel turns the box's raw source enum (DEEZER, LOCAL_INTERNET_RADIO,
// ...) into a friendly name for the tile badge.
export function boxSourceLabel(source) {
  const s = String(source || '').toUpperCase();
  const map = {
    DEEZER: 'Deezer', SPOTIFY: 'Spotify', AMAZON: 'Amazon Music',
    TUNEIN: 'TuneIn', LOCAL_INTERNET_RADIO: 'Internet radio',
    PANDORA: nativeServiceLabel('PANDORA'), IHEART: nativeServiceLabel('IHEART'),
    INTERNET_RADIO: 'Internet radio', LOCAL_MUSIC: 'Library', STORED_MUSIC: 'Library',
    BLUETOOTH: 'Bluetooth', AIRPLAY: 'AirPlay',
  };
  if (map[s]) return map[s];
  // Unknown: title-case the first token (DEEZER_HIFI -> Deezer).
  const first = s.split('_')[0] || s;
  return first ? first.charAt(0) + first.slice(1).toLowerCase() : '';
}

// recallBoxPreset plays one of the box's own presets by pressing its hardware
// preset key (the box plays it through its own cached account, e.g. Deezer).
//
// report(text, kind), when given (the UI 2.0 preview's key tile, ui2radio.js
// keyReport), takes the outcome instead of the error dialog, named with the
// key and the speaker. Whether the key then plays is the tile's own play
// state, which the status poll reads from the speaker, so a recall the
// speaker took only clears the "starting" line.
export async function recallBoxPreset(slot, report = null) {
  const box = state.currentBox;
  if (!box) return;
  const n = keyNum(slot, t);
  const speaker = getBoxLabel(box);
  if (report) report(t('ui2.feedback.keyStarting', { n, speaker }), 'busy');
  try {
    await RecallBoxPreset(box.host, box.port, slot);
    if (report) report(null);
  } catch (err) {
    const raw = String((err && err.message) || err);
    if (report) report(t('ui2.feedback.keyNotStarted', { n, speaker, reason: friendlyErrorText(raw) }), 'error');
    else showError(t('preset.boxRecallFailed', { err: raw }));
  }
}

// healPresetLogos searches radio-browser for the station name of any
// preset that has no logo (legacy presets from the pre-logo era or
// presets added via hardware) and adopts the favicon. Persists the
// result back to the stick so it also shows up on the speaker
// display.
let healingInProgress = false;
async function healPresetLogos() {
  if (healingInProgress) return;
  if (!state.currentBox) return;
  // "No logo" includes two shapes older saves left behind (#696 aftermath):
  // art that is ONLY box-local (the agent's /icon.png stand-in or a broken
  // art-proxy wrapper), which renders artless on every machine but this box,
  // and art still CARRYING any box-form candidate (see artCarriesBoxForm).
  // The latter must be re-looked-up, not merely unwrapped at render:
  // unwrapping yields the single URL the agent had picked as box-drawable,
  // and on the reporter's playing key that is a DuckDuckGo icon URL answering
  // 404 with the grey-chevron image body (probed 2026-08-24), which the
  // webview draws without firing onerror, so the tile's cascade ends on the
  // chevron anyway. Healing writes a full real chain once and the filter goes
  // false, so this adds no recurring work for healthy keys.
  // A Pandora or iHeartRadio key is no directory station: looking its name up
  // in the radio directory could only paint some unrelated station's logo.
  // Neither is a music-library key (a saved folder stores no preset-level art
  // at all when its tracks carry covers): its cover is the media server's, and
  // a directory hit for the folder's name would paint a radio logo over it
  // until the next reload (#1366).
  const missing = state.presets.filter(p =>
    p.name && (!appArtFromBoxArt(p.art) || artCarriesBoxForm(p.art)) &&
    !isNativeServicePreset(p) && !isLibraryPreset(p));
  if (missing.length === 0) return;
  healingInProgress = true;
  try {
    await Promise.all(missing.map(async (p) => {
      try {
        // Intentionally tolerant: NO onlyok filter (even a station
        // flagged broken usually still has a logo). The limit is
        // high enough to find an exact name match among several
        // stations sharing the same name.
        const list = await RadioSearch({ q: p.name, limit: 12, order: 'votes', top: false }) || [];
        const wanted = p.name.toLowerCase().trim();
        // 1) Exact name match.
        let pick = list.find(s => (s.name || '').toLowerCase().trim() === wanted);
        // 2) Substring match in either direction (e.g. "NDR2" vs
        //    "NDR 2").
        if (!pick) {
          pick = list.find(s => {
            const n = (s.name || '').toLowerCase().trim();
            return n && (n.includes(wanted) || wanted.includes(n));
          });
        }
        // 3) Same stream host implies the same station.
        if (!pick && p.stream_url) {
          const wantHost = extractHost(p.stream_url);
          if (wantHost) {
            pick = list.find(s => {
              return extractHost(s.url) === wantHost || extractHost(s.url_resolved) === wantHost;
            });
          }
        }
        if (!pick) return;
        const logo = stationLogoChain(pick);
        if (!logo) return;
        // Radio-only: SetPreset sends type=radio with no uri, so never persist
        // onto a Spotify preset or its URI is lost, nor onto a library key
        // (#1366). Guarded: the heal must not revert a slot another client
        // re-saved meanwhile (#758).
        if (p.type === 'spotify' || isLibraryPreset(p)) return;
        p.art = logo;
        await setPresetIfUnchanged(state.currentBox, p, { art: logo });
      } catch {}
    }));
  } finally {
    healingInProgress = false;
    renderPresets();
  }
}

// ---------- Preset Render mit Long Press Support ----------

// BOX_LOOPBACK is the agent's own host:port as seen from the box (the agent runs
// on the box). It is the single source for the loopback URLs the frontend builds
// to optimistically reflect what the box is about to play; the Go side mirrors
// these in internal/boxurl. Keep the two in sync.
export const BOX_LOOPBACK = 'http://127.0.0.1:8888';

// decodeProxyUrl unwraps a stream-proxy URL
// (http://<host>:8888/stream/raw?u=<base64url real URL>) back to the real
// upstream URL it wraps; returns the input unchanged otherwise. A preset MUST
// store the real station URL, never the proxy wrapper: since v0.7.16 ad-hoc radio
// plays through the proxy, so the box's now-playing location is the wrapper.
// Saving that made the box, on recall, ask the proxy to fetch its own loopback
// URL, which the agent's SSRF guard blocks, so nothing played (the ST20 "plays
// nothing" regression).
export function decodeProxyUrl(loc) {
  if (!loc) return loc;
  try {
    const u = new URL(loc);
    if (u.pathname !== '/stream/raw') return loc;
    const enc = u.searchParams.get('u');
    if (!enc) return loc;
    const real = atob(enc.replace(/-/g, '+').replace(/_/g, '/'));
    if (/^https?:\/\//i.test(real)) return real;
  } catch { /* not a parseable proxy URL: fall through */ }
  return loc;
}

// spotifyURIFromContainer lives in utils.js (shared with the preset-save
// URI decision, spotifySaveContextURI).

// SPOTIFY_LOGO lives in logos.js (shared with the Recently-played view); the
// preset grid draws Spotify keys through spotifyCoverImgTag from there.

// presetStateLabel returns the small state line shown on a preset tile: an
// error, or the now-playing state when this preset is the active one. Keeps the
// play-state -> CSS-class + i18n-key mapping in one place.
export function presetStateLabel(slot, isActive, hasErr) {
  if (hasErr) {
    return `<div class="preset-state state-err">&#9888; ${escapeHtml(state.presetErrors[slot])}</div>`;
  }
  if (!isActive) return '';
  const map = {
    PLAY_STATE: ['state-play', 'preset.statePlay'],
    BUFFERING_STATE: ['state-buf', 'preset.stateBuf'],
    PAUSE_STATE: ['state-pause', 'preset.statePause'],
  };
  const m = map[state.nowPlayState];
  return m ? `<div class="preset-state ${m[0]}">${escapeHtml(t(m[1]))}</div>` : '';
}

// Preset transfer (music view): one button that pushes THIS speaker's six
// presets onto another box. Clicking asks which target to send to (a picker of
// the other STR speakers, plus an "all" option when there is more than one),
// then reuses the same CopyPresetsAcrossBoxes backend + settingsView.copyPresets
// strings as the Expert transfer in the speaker settings. The source agent
// serves its live store, so only the target identity is chosen here.
function presetTransferTargets() {
  const box = state.currentBox;
  if (!box || box.kind === 'stock') return [];
  return (state.boxes || []).filter(b => b && b.kind !== 'stock' && b.host && b.host !== box.host);
}

export function updatePresetTransferRow() {
  const btn = $('presetTransferBtn');
  if (!btn) return;
  const targets = presetTransferTargets();
  const ok = targets.length > 0;
  btn.disabled = !ok;
  btn.title = ok ? t('settingsView.copyPresetsHeading') : t('presets.transferNoTargets');
  btn.setAttribute('aria-label', btn.title);
}

export function wirePresetTransferRow() {
  const btn = $('presetTransferBtn');
  if (!btn || btn.dataset.wired) return;
  btn.dataset.wired = '1';
  btn.onclick = async () => {
    const box = state.currentBox;
    if (!box || box.kind === 'stock') return;
    const targets = presetTransferTargets();
    if (!targets.length) { showToast(t('presets.transferNoTargets')); return; }
    const opts = targets
      .map(b => `<option value="${escapeAttr(b.host)}|${b.port || 0}">${escapeHtml(getBoxLabel(b))}</option>`)
      .join('');
    const allOpt = targets.length > 1
      ? `<option value="__ALL__">${escapeHtml(t('settingsView.copyPresetsAllTargets'))}</option>`
      : '';
    // A speaker with the second row of keys (#1110) copies both rows, and the
    // confirmation says that both rows on the target are replaced.
    const twoRows = boxPresetRows(box, state.presets) >= 2;
    const sourcePresets = state.presets.slice();
    const body = `<p>${escapeHtml(t(twoRows ? 'settingsView.copyPresetsHelpAll' : 'settingsView.copyPresetsHelp'))}</p>`
      + (twoRows ? `<p>${escapeHtml(t('presets.transferReplacesBoth'))}</p>` : '')
      + `<select id="presetXferTarget" class="preset-xfer-select" style="width:100%;box-sizing:border-box;">${opts}${allOpt}</select>`;
    const ok = await confirmWarn(t('settingsView.copyPresetsHeading'), body, {
      confirmLabel: t('presets.transferBtn'),
      confirmClass: 'btn btn-warning',
    });
    if (!ok) return;
    const sel = document.querySelector('#presetXferTarget');
    const val = sel ? sel.value : '';
    if (!val) return;
    btn.disabled = true;
    const origLabel = btn.textContent;
    btn.textContent = t('settingsView.copyPresetsBusyBtn');
    try {
      await runPresetTransfer({ box, targets, val, twoRows, sourcePresets, report: transferReport() });
    } finally {
      btn.textContent = origLabel;
      updatePresetTransferRow();
    }
  };
}

// runPresetTransfer copies the keys of box onto the chosen target (val is
// "host|port" or "__ALL__" for every target) and says how it went.
//
// report(text, kind), when given (the UI 2.0 preview: the line under the
// Transfer button, ui2radio.js transferReport), takes every message instead of
// the toasts and the error dialog, with the source and target speakers named.
// What it calls done is the target agents' own answer to the copy.
export async function runPresetTransfer({ box, targets, val, twoRows, sourcePresets, report = null }) {
  const source = getBoxLabel(box);
  const all = val === '__ALL__';
  const one = all ? null : targets.find(b => b.host === val.split('|')[0]);
  if (report) {
    report(t('ui2.feedback.transferBusy', { source, target: all ? t('ui2.feedback.allSpeakers') : (one ? getBoxLabel(one) : val.split('|')[0]) }), 'busy');
  } else {
    // Persistent progress toast: the copy also carries the Spotify login and
    // restarts the target's engine, which takes a few seconds over the LAN.
    // Without a visible "running" state the row looks idle and invites a
    // second press (same trap as the STR install button).
    showToast(t('settingsView.copyPresetsProgress'), 0);
  }
  let targetName = all ? t('ui2.feedback.allSpeakers') : '';
  try {
    if (all) {
      let done = 0;
      const failed = [];
      const row1Only = [];
      for (const tb of targets) {
        try {
          await CopyPresetsAcrossBoxes(box.host, box.port || 0, tb.host, tb.port || 0);
          done++;
          if (twoRows && copyRowsNote(sourcePresets, tb) === 'row1Only') row1Only.push(getBoxLabel(tb));
        } catch { failed.push(getBoxLabel(tb)); }
      }
      const rowMsg = row1Only.length ? ' ' + t('settingsView.copyPresetsRow1OnlyList', { names: row1Only.join(', ') }) : '';
      if (report) {
        if (failed.length) report(t('ui2.feedback.transferPartial', { done, total: targets.length, failed: failed.join(', '), source }) + rowMsg, done ? 'warn' : 'error');
        else report(t('ui2.feedback.transferAllDone', { n: targets.length, source }) + rowMsg, 'ok');
      } else if (failed.length) showError(t('settingsView.copyPresetsAllPartial', { done, total: targets.length, failed: failed.join(', ') }) + rowMsg);
      else showToast(t('settingsView.copyPresetsAllDone', { n: targets.length }) + rowMsg);
    } else {
      const [thost, tportRaw] = val.split('|');
      const tport = parseInt(tportRaw, 10) || 0;
      const target = targets.find(b => b.host === thost);
      targetName = target ? getBoxLabel(target) : thost;
      const n = await CopyPresetsAcrossBoxes(box.host, box.port || 0, thost, tport);
      const rowMsg = twoRows && copyRowsNote(sourcePresets, target) === 'row1Only'
        ? ' ' + t('settingsView.copyPresetsRow1Only', { target: targetName })
        : '';
      if (report) report(t('ui2.feedback.transferDone', { n, target: targetName, source }) + rowMsg, 'ok');
      else showToast(t('settingsView.copyPresetsDone', { n, target: targetName }) + rowMsg);
    }
  } catch (e) {
    // The target already holds one of the stations on a key this transfer
    // does not rewrite, so it refused the whole set and wrote nothing. That
    // used to surface as the agent's raw 409 JSON in an error dialog (#882).
    const conflict = presetCopyConflict(e);
    const conflictText = conflict ? t('settingsView.copyPresetsConflict', { name: conflict.name, n: keyNum(conflict.slot, t) }) : '';
    if (report) {
      const reason = conflictText || friendlyErrorText(String((e && e.message) || e));
      report(t('ui2.feedback.transferFailed', { source, target: targetName, reason }), 'error');
    } else if (conflict) showError(conflictText);
    else showError(e);
  }
}

// purgePresetStateForSpeaker forgets which speaker's presets and snapshot are
// on screen when STR is removed from it (the onSpeakerPurge hook in main.js).
export function purgePresetStateForSpeaker(host) {
  if (loadedPresetsBoxKey && loadedPresetsBoxKey.startsWith(host + ':')) loadedPresetsBoxKey = null;
  if (loadedSnapshotBoxKey && loadedSnapshotBoxKey.startsWith(host + ':')) loadedSnapshotBoxKey = null;
}
