// views/music/volume.js: the volume slider of the Music view with its +/- buttons,
// wheel, mute, the stereo-pair scope and the sync from the speaker.

import { BoxSettings, setBoxMute, SetBoxVolume } from '../../api.js';
import { sameBoxIdentity } from '../../groups.js';
import { t } from '../../i18n/index.js';
import { muteAfterPress, muteView } from '../../mutebutton.js';
import {
  createPairVolume,
  makePerHostThrottle,
  notePairRead,
  pairAnchored,
  pairLevelsFor,
  pairVolumeTarget,
} from '../../pairVolume.js';
import { state } from '../../state.js';
import { playerOnScreen } from '../../statusrefreshscope.js';
import { pairDisplayName } from '../../stereoNames.js';
import { $ } from '../../utils.js';
import { throttledSetVolume } from '../settings.js';

// Volume slider in the music tab. Uses SetBoxVolume, debounced so a
// drag does not fire a hundred API calls.
let musicVolBox = null;
// Filled by mountVolume once #view-box exists.
export let musicVolEl = null;
// Filled by mountVolume once #view-box exists.
export let musicVolValEl = null;

// Stereo pair volume (idea #1093, logic in pairVolume.js). While the selected
// speaker is half of a live pair, this slider moves BOTH halves and carries the
// pair's name, the way the phone remote's "Pair" scope does. Everything else
// (a single speaker, a member of an ordinary multiroom group) keeps the slider
// moving the selected speaker alone; a group has its own panel for the rest.
const pairVol = createPairVolume();
// Per-speaker throttle: the shared throttledSetVolume holds one pending call,
// and two halves fed through it in turn would lose one half's level.
export const pairSetVolume = makePerHostThrottle(SetBoxVolume);
state.desiredPairLevels = null;

export function currentPairTarget() {
  return pairVolumeTarget(state.currentBox, state.zoneLive, state.boxes);
}

// renderVolumeScope names the pair next to the slider while the slider acts on
// a pair, and puts the plain volume label back otherwise. The name is the one
// the speaker list frames the pair with, so the two always agree.
function renderVolumeScope(target = currentPairTarget()) {
  const lbl = $('musicVolumeScope');
  const wrap = musicVolEl ? musicVolEl.closest('.volume-control') : null;
  const name = target ? (pairDisplayName(target.pair, () => renderVolumeScope()) || t('multiroom.stereoHeading')) : '';
  if (lbl) {
    lbl.textContent = name;
    lbl.title = name ? t('controls.pairVolumeTitle', { name }) : '';
    lbl.classList.toggle('hidden', !target);
  }
  if (wrap) wrap.classList.toggle('is-pair', !!target);
  if (musicVolEl) {
    musicVolEl.setAttribute('aria-label', target ? t('controls.pairVolumeLabel', { name }) : t('controls.volume'));
    musicVolEl.title = target
      ? t('controls.pairVolumeTitle', { name }) + '\n' + t('controls.volumeWheelHint')
      : t('controls.volumeWheelHint');
  }
  return target;
}

// sendMusicVolume is the one write path of the music slider, its +/- buttons
// and its wheel. On a pair both halves shift by the same offset from the
// baseline the last read captured, so their balance stays as the user set it.
function sendMusicVolume(box, pct) {
  state.desiredVolume = pct;
  const target = currentPairTarget();
  if (target) {
    const levels = pairLevelsFor(pairVol, target.key, target.halves.map(b => b.host), pct);
    if (levels) {
      target.halves.forEach(b => {
        if (typeof levels[b.host] === 'number') pairSetVolume(b.host, b.port, levels[b.host]);
      });
      state.desiredPairLevels = { key: target.key, levels };
      return;
    }
    // The pair was named before its levels were read (pairLevelsFor says why
    // guessing is wrong). Move the selected speaker alone, as before, and read
    // both halves so the next move carries the pair.
    syncMusicTabVolumeFromBox();
  }
  state.desiredPairLevels = null;
  throttledSetVolume(box.host, box.port, pct);
}

// readMusicVolume reads what the slider should show for box: its own settings,
// and on a pair both halves' levels too. data is the selected speaker's
// settings (for mute), or null when it did not answer.
async function readMusicVolume(box) {
  const target = renderVolumeScope();
  if (!target) {
    return { target: null, data: await BoxSettings(box.host, box.port), levels: null };
  }
  const levels = {};
  let data = null;
  await Promise.all(target.halves.map(async (b) => {
    try {
      const d = await BoxSettings(b.host, b.port);
      if (sameBoxIdentity(b, box)) data = d;
      const v = d && d.volume && d.volume.actual;
      if (typeof v === 'number') levels[b.host] = v;
    } catch {}
  }));
  return { target, data, levels };
}

// musicVolumeLevel turns a read into the level the slider shows: the speaker's
// own, or for a pair the louder half (re-anchoring the pair baseline there, so
// the caller must only ask while the user is not touching the slider).
function musicVolumeLevel(r) {
  if (r.target) return notePairRead(pairVol, r.target.key, r.levels);
  const v = r.data && r.data.volume && r.data.volume.actual;
  return typeof v === 'number' ? v : null;
}
// Mute, which STR could not do at all until a user asked for it and pointed at
// this very symbol. It is the speaker's own MUTE key, not a volume pulled to
// zero and remembered here: the box's remote and its physical buttons set the
// same flag, so they and this icon always agree, and closing the app loses
// nothing that would have to be restored.
// Filled by mountVolume once #view-box exists.
let volMuteEl = null;
// muteUntil holds the 2 s status poll off for a moment after a press, exactly
// as the slider does. The speaker needs a round trip before it reports the new
// state, and a poll landing in that gap would flip the icon back, which reads
// as a button that does not work.
state.muteUntil = 0;
// renderMuteState draws what the SPEAKER says, not what we last clicked.
function renderMuteState(muted) {
  if (!volMuteEl) return;
  const v = muteView(muted);
  volMuteEl.innerHTML = v.glyph;
  volMuteEl.setAttribute('aria-pressed', v.pressed ? 'true' : 'false');
  const label = t(v.labelKey);
  volMuteEl.setAttribute('aria-label', label);
  volMuteEl.title = label;
  volMuteEl.classList.toggle('is-muted', v.pressed);
}
// Moving the volume while the speaker is muted means "I want to hear this".
//
// The firmware does not draw that conclusion. Measured on a speaker: mute, then
// PUT a volume, and it answers vol=6 muteenabled=true. So the slider moves, the
// number changes, and nothing comes out, which reads as an app that does not
// work rather than as a speaker that is doing exactly what it was told.
//
// Lifting it here rather than in the agent is deliberate. The agent cannot tell
// a person reaching for the slider from STR re-asserting a level after a wake,
// and the second one must not undo a mute somebody meant. At this end the
// gesture is the evidence.
function liftMuteForVolumeGesture() {
  const box = state.currentBox;
  if (!box || !volMuteEl) return;
  if (volMuteEl.getAttribute('aria-pressed') !== 'true') return;
  // Drawn first, which also disarms this for the rest of the drag: a slider
  // sends an event per pixel and this must fire once, not forty times.
  renderMuteState(false);
  state.muteUntil = Date.now() + 1500;
  setBoxMute(box, false).then((res) => {
    if (!sameBoxIdentity(state.currentBox, box)) return;
    renderMuteState(muteAfterPress(res, false));
  }).catch(() => {});
}

// Drag-busy + grace period so the 2 s periodic refresh in
// refreshStatus does not yank the thumb out from under the user
// while they are wischen. musicVolUntil is the timestamp at which
// auto-refresh is allowed to take over again after a release.
state.musicVolBusy = false;
state.musicVolUntil = 0;

// syncMusicTabVolumeFromBox refreshes the music-tab slider so
// hardware-button volume changes on the box (or any other client
// changing the volume out from under us) show up here within ~2 s.
// Called from refreshStatus on every poll. Cheap to call: BoxSettings
// caches well on the agent side.
export async function syncMusicTabVolumeFromBox() {
  const box = state.currentBox;
  if (!box || !musicVolEl) return;
  // Only while the slider is on screen: the Music tab, or the UI 2.0
  // persistent player in another view.
  if (!playerOnScreen({ view: state.view, persistentPlayer: !!state.ui2Shell, otaInProgress: !!state.otaInProgress })) return;
  if (state.musicVolBusy) return;
  if (Date.now() < (state.musicVolUntil || 0)) return;
  try {
    // On a stereo pair this reads both halves (readMusicVolume).
    const r = await readMusicVolume(box);
    const data = r.data;
    // Guard against a box switch while the request was in flight: a late
    // reply from the previous speaker would show ITS volume for the new one,
    // and the user's first slider touch would then send that stale level.
    if (!sameBoxIdentity(state.currentBox, box)) return;
    // Mute first, and on its own guard: it has to follow the speaker's own
    // remote and buttons, and it must still update on a speaker whose volume
    // the poll is holding off or whose level has not changed.
    if (data && Date.now() >= (state.muteUntil || 0)) {
      renderMuteState(data.volume && data.volume.muted);
    }
    // The user may have started dragging during the round trip; re-check the
    // drag guards so the reply cannot yank the thumb from under them.
    if (state.musicVolBusy || Date.now() < (state.musicVolUntil || 0)) return;
    const vol = musicVolumeLevel(r);
    if (typeof vol !== 'number') return;
    const current = parseInt(musicVolEl.value, 10);
    if (current !== vol) {
      musicVolEl.value = String(vol);
      if (musicVolValEl) musicVolValEl.textContent = String(vol);
    }
  } catch {}
}

// loadMusicTabVolume fetches the current volume on a tab switch so
// the slider position is in sync.
export async function loadMusicTabVolume() {
  const box = state.currentBox;
  if (!box || !musicVolEl) return;
  try {
    const r = await readMusicVolume(box);
    const data = r.data;
    // Guard against a box switch while the request was in flight: two quick
    // speaker clicks race their replies, and the slower (previous) speaker's
    // volume must not land on the newly selected one — the first slider
    // touch would send it there (a sudden loud jump).
    if (!sameBoxIdentity(state.currentBox, box)) return;
    const vol = musicVolumeLevel(r) || 0;
    musicVolEl.value = String(vol);
    if (musicVolValEl) musicVolValEl.textContent = String(vol);
    if (data) renderMuteState(data.volume && data.volume.muted);
  } catch {}
}

// syncVolumeScopeOnZoneLive is the zone-poll hook main.js registers: a pair
// formed or undone while its half is selected switches the music slider
// between pair and single speaker without waiting for a speaker click. When
// the pair shows up after the slider was filled (the zone poll lands after
// the speaker's own settings), read both halves straight away rather than at
// the next status poll, so the slider reads the pair before it is touched.
export function syncVolumeScopeOnZoneLive() {
  const target = renderVolumeScope();
  if (target && !pairAnchored(pairVol, target.key)) syncMusicTabVolumeFromBox();
}

// mountVolume attaches the handlers that used to be wired at module scope in
// main.js, once #view-box exists (called from mountMusicView).
export function mountVolume() {
  musicVolEl = $('musicVolume');
  musicVolValEl = $('musicVolumeVal');

  volMuteEl = $('volMute');

  if (volMuteEl) {
    volMuteEl.onclick = async () => {
      const box = state.currentBox;
      if (!box) return;
      // Optimistic: the speaker takes a moment to answer, and a button that
      // looks dead for half a second gets pressed a second time.
      const want = volMuteEl.getAttribute('aria-pressed') !== 'true';
      renderMuteState(want);
      state.muteUntil = Date.now() + 1500;
      volMuteEl.disabled = true;
      try {
        const res = await setBoxMute(box, want);
        // A speaker switch mid-flight would otherwise paint the old speaker's
        // answer onto the new one.
        if (!sameBoxIdentity(state.currentBox, box)) return;
        // The speaker has the last word. Our guess only filled the gap, and if
        // it could not be reached the icon must go back rather than lie.
        renderMuteState(muteAfterPress(res, want));
      } finally {
        volMuteEl.disabled = false;
      }
    };
  }

  if (musicVolEl) {
    // Live-update during drag: each input event throttles a
    // SetBoxVolume call so the user sees the level move on the
    // speaker WHILE they wipe, not only on release. The throttle
    // collapses bursts so the box's tiny HTTP server never has more
    // than one volume PUT in flight at a time.
    musicVolEl.oninput = () => {
      if (musicVolValEl) musicVolValEl.textContent = musicVolEl.value;
      const box = state.currentBox;
      if (!box) return;
      liftMuteForVolumeGesture();
      musicVolBox = box;
      sendMusicVolume(box, parseInt(musicVolEl.value, 10));
    };
    // Keyboard arrows fire only `change`, not `input`, so we still
    // dispatch on change as a safety net for that path.
    musicVolEl.onchange = () => {
      musicVolBox = state.currentBox;
      if (!musicVolBox) return;
      liftMuteForVolumeGesture();
      sendMusicVolume(musicVolBox, parseInt(musicVolEl.value, 10));
    };
    // pointerdown/up flag is the most reliable cross-device drag
    // signal. Keyboard arrows fire only `change`, which is already
    // wired above, so the busy flag is unnecessary there. Add a
    // ~1.2 s grace period after release so the network round-trip
    // to the box (and its own state update) does not race with us.
    const beginBusy = () => { state.musicVolBusy = true; };
    const endBusy = () => {
      state.musicVolBusy = false;
      state.musicVolUntil = Date.now() + 1200;
    };
    musicVolEl.addEventListener('pointerdown', beginBusy);
    musicVolEl.addEventListener('pointerup', endBusy);
    musicVolEl.addEventListener('pointercancel', endBusy);
    musicVolEl.addEventListener('pointerleave', () => {
      if (state.musicVolBusy) endBusy();
    });

    // Precise stepping: the +/- buttons and the mouse wheel both nudge the volume
    // by exactly one, sharing one clamped helper. stepVolume mirrors what an
    // oninput drag does (move the thumb, update the label, push to the box) and
    // holds off the periodic auto-refresh for the same grace period so it does not
    // snap the thumb back before the box has applied the change.
    const stepVolume = (delta) => {
      const box = state.currentBox;
      if (!box) return;
      const cur = parseInt(musicVolEl.value, 10) || 0;
      const next = Math.max(0, Math.min(100, cur + delta));
      if (next === cur) return;
      liftMuteForVolumeGesture();
      musicVolEl.value = String(next);
      if (musicVolValEl) musicVolValEl.textContent = String(next);
      musicVolBox = box;
      state.musicVolBusy = false;
      state.musicVolUntil = Date.now() + 1200;
      sendMusicVolume(box, next);
    };
    const volDown = $('volDown');
    const volUp = $('volUp');
    // Focusing the slider on a button press also arms the wheel gesture below, so
    // a user can click "+" once and then keep scrolling to fine-tune.
    if (volDown) volDown.onclick = () => { musicVolEl.focus(); stepVolume(-1); };
    if (volUp) volUp.onclick = () => { musicVolEl.focus(); stepVolume(1); };

    // Mouse wheel over the slider adjusts the volume, but ONLY while the slider is
    // focused (the user clicked it or used the +/- buttons). Passively scrolling
    // the page with the cursor merely passing over the slider must never change
    // the volume: that accidental blast is exactly what we are guarding against.
    // When not engaged we do not preventDefault, so the page scrolls as usual.
    // Rate-limited to one step per notch so a fast flick moves one at a time.
    let volWheelAt = 0;
    musicVolEl.addEventListener('wheel', (e) => {
      if (document.activeElement !== musicVolEl) return;
      e.preventDefault();
      const now = Date.now();
      if (now - volWheelAt < 60) return;
      volWheelAt = now;
      stepVolume(e.deltaY < 0 ? 1 : -1);
    }, { passive: false });
  }
}
