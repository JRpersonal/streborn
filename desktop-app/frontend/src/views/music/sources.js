// views/music/sources.js: the source/input row of the Music view (AUX, TV, Bluetooth,
// the power toggle) and the per-speaker source list cache behind it.

import { BoxSettings, SelectBoxSource, SetBoxPower } from '../../api.js';
import { t } from '../../i18n/index.js';
import { inputButtons, isActiveInput, sourceRowKey, speakerIsOn } from '../../sourceinputs.js';
import { state } from '../../state.js';
import { $, escapeAttr, escapeHtml, showError, showToast } from '../../utils.js';
import { refreshStatus } from './status.js';

async function selectSource(btn) {
  const box = state.currentBox;
  if (!box) { showToast(t('speaker.noneSelected')); return; }
  // The name that goes to the speaker is the name the speaker itself reported,
  // which is why a CineMate's analogue input is switched with LOCAL and an
  // SA-5's second line input needs the account alongside it: without the
  // account its three AUX sockets are indistinguishable (#491, #274).
  const src = btn.dataset.source;
  const account = btn.dataset.sourceAccount || '';
  const shown = btn.dataset.sourceLabel || src;
  btn.disabled = true;
  try {
    await SelectBoxSource(box.host, box.port, src, account);
    showToast(t('toast.source', { src: shown }));
    setTimeout(refreshStatus, 800);
  } catch (e) {
    // A button is only drawn for an input the speaker listed, but if the box
    // refuses it anyway (1005 UNKNOWN_SOURCE_ERROR, relayed by the agent as
    // source_unavailable) show a clear message instead of the raw box error.
    if (String(e).includes('source_unavailable')) {
      showToast(t('toast.sourceUnavailable', { src: shown }));
      btn.classList.add('hidden');
    } else {
      showError(e);
    }
  } finally {
    btn.disabled = false;
  }
}

// refreshSourceButtons draws one button per input the selected speaker reports,
// and is run after every selectBox() AND after every discovery refresh so the
// row tracks model detection that lands later (Bose stock /info enrichment).
//
// Two layers. The speaker's own source list is the answer, and until it arrives
// the row holds the classic AUX plus Bluetooth pair, minus what the model name
// rules out. The list then replaces them, which is what brings a soundbar's TV,
// CBL-Sat, BD-DVD and Game sockets in (#385, #491, #577), splits an SA-5's three
// line inputs (#274), and still catches the ST20 variants that ship without
// Bluetooth and used to answer a BT /select with 1005 UNKNOWN_SOURCE_ERROR
// (#102). The row is rebuilt on every box switch, so a button hidden after a
// 1005 rejection on one speaker comes back on a speaker that has the input
// (#417: it stayed hidden for every box until an app restart).
//
// sourceListCache remembers each speaker's list. The startup pair is drawn
// first on every refresh, and for a speaker whose list had already dropped a
// button that made the button blink in and out every minute (Jens,
// 2026-09-06), so a list already read wins from the first frame.
export const sourceListCache = new Map();
export async function refreshSourceButtons() {
  const row = $('sourceInputs');
  if (!row || !state.currentBox) return;
  const box = state.currentBox;
  renderSourceButtons(inputButtons(sourceListCache.get(box.host), box.model || ''), box.host);
  try {
    const settings = await BoxSettings(box.host, box.port);
    // Guard against a box switch while the request was in flight.
    if (state.currentBox !== box) return;
    const sources = (settings && settings.sources) || [];
    // Only trust a non-empty list. An empty one means the speaker did not
    // answer /sources, and the startup pair is the better guess than no inputs
    // at all on a box that has them.
    if (Array.isArray(sources) && sources.length) {
      sourceListCache.set(box.host, sources);
      renderSourceButtons(inputButtons(sources, box.model || ''), box.host);
    }
  } catch {
    // Keep what is on screen on any error.
  }
}

// The Bluetooth glyph, so that input keeps the icon it has always had in this
// row while every other input is drawn from the name the speaker gives it.
const BLUETOOTH_ICON = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="16" height="16" aria-hidden="true"><polyline points="6.5 6.5 17.5 17.5 12 23 12 1 17.5 6.5 6.5 17.5"></polyline></svg>';

let sourceRowDrawn = '';

function renderSourceButtons(buttons, host) {
  const row = $('sourceInputs');
  if (!row) return;
  const key = sourceRowKey(host, buttons);
  if (key === sourceRowDrawn) {
    highlightActiveSource();
    return;
  }
  sourceRowDrawn = key;
  row.innerHTML = buttons.map((b) => {
    const title = escapeAttr(t('controls.inputTitle', { input: b.label }));
    const data = `data-source="${escapeAttr(b.source)}" data-source-account="${escapeAttr(b.sourceAccount)}" data-source-label="${escapeAttr(b.label)}"`;
    if (b.bluetooth) {
      return `<button class="btn btn-source btn-source-icon" ${data} aria-label="${escapeAttr(b.label)}" title="${title}">${BLUETOOTH_ICON}</button>`;
    }
    return `<button class="btn btn-source" ${data} title="${title}">${escapeHtml(b.label)}</button>`;
  }).join('');
  highlightActiveSource();
}

// highlightActiveSource marks the input the speaker is playing from. Called
// after every status poll and again after a rebuild of the row, because the
// buttons are replaced now and would otherwise come back unlit while the
// speaker is still on that input.
export function highlightActiveSource() {
  document.querySelectorAll('.btn-source').forEach((b) => {
    const button = { source: b.dataset.source, sourceAccount: b.dataset.sourceAccount };
    b.classList.toggle('active', isActiveInput(button, state.nowSource, state.nowSourceAccount));
  });
  applyPowerButton();
}

// The power toggle at the end of the row (#1644). It used to be a STANDBY input
// button, so the green "selected input" style lit it exactly while the speaker
// slept, the opposite of the phone remote's power button, and a click on a
// sleeping speaker sent standby again instead of waking it. It is now the
// remote's toggle: lit while the speaker is on (speakerIsOn), off sends it to
// standby, on wakes it and resumes the last station.
//
// powerHold keeps the clicked target on the button while the agent call runs
// and for a few seconds after it. Waking a speaker takes several seconds, and a
// status poll in that window still reads STANDBY and would flip the button back
// before the wake lands; going to standby, the speaker reports its old source
// for a moment. It is tied to the speaker it was clicked for.
let powerHold = null;
const POWER_HOLD_AFTER_MS = 3500;

function powerShown() {
  const box = state.currentBox;
  if (powerHold && box && powerHold.host === box.host && Date.now() < powerHold.until) return powerHold.on;
  return speakerIsOn(state.nowPlayState, state.nowSource);
}

export function applyPowerButton() {
  const btn = $('powerBtn');
  if (!btn) return;
  const on = powerShown();
  btn.classList.toggle('on', on);
  btn.setAttribute('aria-pressed', String(on));
}

async function togglePower(btn) {
  const box = state.currentBox;
  if (!box) { showToast(t('speaker.noneSelected')); return; }
  const target = !powerShown();
  powerHold = { host: box.host, on: target, until: Infinity };
  applyPowerButton();
  btn.disabled = true;
  try {
    await SetBoxPower(box.host, box.port, target);
    powerHold.until = Date.now() + POWER_HOLD_AFTER_MS;
  } catch (e) {
    powerHold = null;
    showError(e);
  } finally {
    btn.disabled = false;
    applyPowerButton();
  }
  // The same follow-up reads as the phone remote; the last one lands after the
  // hold has run out, so from there the button shows what the speaker reports.
  setTimeout(refreshStatus, 1500);
  setTimeout(refreshStatus, 4000);
}

// purgeSourceCacheForSpeaker forgets the cached source list of a speaker STR
// was removed from (the onSpeakerPurge hook in main.js).
export function purgeSourceCacheForSpeaker(host) {
  sourceListCache.delete(host);
}

// mountSources attaches the handlers that used to be wired at module scope in
// main.js, once #view-box exists (called from mountMusicView).
export function mountSources() {
  // The input row in the music tab, routed through the agent's /api/box/source
  // endpoint via the SelectBoxSource binding.
  //
  // Delegated from the row because the input buttons are built per speaker now: a
  // soundbar's TV and HDMI sockets only exist once that speaker has reported them,
  // so there is no fixed set of buttons left to bind at startup.
  $('sourceButtons').addEventListener('click', (ev) => {
    const btn = ev.target.closest('.btn-source');
    if (btn) selectSource(btn);
  });
  // The power toggle goes to the agent's /api/box/power, the endpoint behind the
  // phone remote's power button.
  const power = $('powerBtn');
  if (power) power.addEventListener('click', () => togglePower(power));
}
