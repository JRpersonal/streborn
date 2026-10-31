// views/settings/basics.js: the Basics group of Speaker settings. The name,
// the volume (with the stereo balance and the start level on the same bar),
// the bass and the speaker's own language.

import { state } from '../../state.js';
import {
  $,
  escapeHtml,
  escapeAttr,
  debounce,
  showError,
  showToast,
  balanceLabel,
  balanceStateLabel,
  bassControlsDisabled,
  bassSliderProps,
} from '../../utils.js';
import { t } from '../../i18n/index.js';
import { markLeft, pctFromOffset } from '../../startvolmark.js';
import { balanceSourceBox } from '../../groups.js';
import {
  SetBoxName,
  SetBoxVolume,
  SetBoxBass,
  GetBoxLanguage,
  SetBoxLanguage,
  GetStartVolume,
  SetStartVolume,
  readBoxBalanceInfo,
  writeBoxBalance,
} from '../../api.js';
import { deps, withButtonPending, wireCombobox, formatRel } from './shared.js';
import { sanitizeSpeakerName } from '../../speakername.js';

// uidSuffixFor returns the last 4 characters of the device ID as a
// suffix used to disambiguate friendly names (for example "FFD8").
function uidSuffixFor(box) {
  const id = (box && box.deviceID) || '';
  return id.slice(-4).toUpperCase();
}

// ensureWithUID always appends the speaker's UID suffix so the user
// can see at a glance that an identifier is attached. Prevents name
// collisions on the network and is useful for support too.
function ensureWithUID(desired, ownBox) {
  const trimmed = (desired || '').trim();
  if (!trimmed) return trimmed;
  const suffix = uidSuffixFor(ownBox);
  if (!suffix) return trimmed;
  if (trimmed.toUpperCase().endsWith(suffix)) return trimmed;
  // #133: the ID suffix exists only to disambiguate multiple speakers that
  // would otherwise share a name. The name is display-only (discovery matches
  // on IP/DeviceID), so append the suffix ONLY when another known speaker would
  // collide with this exact name. A single speaker, or a unique name, keeps the
  // clean name the user chose.
  const ownId = (ownBox && ownBox.deviceID) || '';
  const want = trimmed.toUpperCase();
  const collides = (state.boxes || []).some(b => {
    if (!b || b.deviceID === ownId) return false;
    const other = (b.friendlyName || b.name || '').trim();
    if (!other) return false;
    const otherBase = other.replace(/\s+[0-9A-Fa-f]{4}$/, '').trim().toUpperCase();
    return otherBase === want || other.toUpperCase() === want;
  });
  return collides ? `${trimmed} ${suffix}` : trimmed;
}

export function renderBasicsSections(ctx) {
  const { info, vol, bass, box, langOptionsHtml } = ctx;
  // Range, step and position of the bass slider all come from the shared
  // mapping: tone-controls speakers report a step above 1 that the slider
  // must honor (see bassSliderProps in utils.js).
  const bassProps = bassSliderProps(bass);
  const uid = uidSuffixFor(box);
  return {
    group: 'basics',
    html: `
    <div data-ui2-sec="name" class="settings-section">
      <h3>${escapeHtml(t('settingsView.nameHeading'))}</h3>
      <div class="setting-row">
        <div class="combobox" id="boxNameCombo">
          <input type="text" id="boxNameInput" autocomplete="off"
                 value="${escapeAttr(info.name || '')}"
                 placeholder="${escapeAttr(t('settingsView.namePlaceholder'))}" />
          <button type="button" class="combo-toggle" id="boxNameToggle" title="${escapeAttr(t('settingsView.nameShowList'))}">&#9662;</button>
          <ul class="combo-list hidden" id="boxNameList"></ul>
        </div>
        <button class="btn btn-mini" id="boxNameSave">${escapeHtml(t('common.save'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.nameHelp', { uid: uid || '----' }))}</small>
    </div>

    <div data-ui2-sec="volume" class="settings-section">
      <h3>${escapeHtml(t('controls.volume'))}</h3>
      <div class="setting-row">
        <div class="vol-with-start" id="boxVolumeWrap">
          <input type="range" id="boxVolume" min="0" max="100" value="${vol.actual || 0}" />
          <div class="startvol-mark hidden" id="boxStartVolMark" role="slider" tabindex="0"
               aria-label="${escapeAttr(t('settingsView.startVolLabel'))}"
               aria-valuemin="0" aria-valuemax="100" aria-valuenow="0"></div>
        </div>
        <span class="setting-value" id="boxVolumeVal">${vol.actual || 0}</span>
      </div>
      <h3 id="boxBalanceHead" hidden>${escapeHtml(t('controls.balanceHead'))}</h3>
      <div class="setting-row" id="boxBalanceRow" hidden>
        <input type="range" id="boxBalanceSlider" min="-7" max="7" step="1" value="0" hidden />
        <span class="setting-value" id="boxBalance"></span>
        <button class="btn btn-mini" id="boxBalanceCentre" hidden>${escapeHtml(t('controls.balanceCentreBtn'))}</button>
      </div>
      <div class="setting-row" id="boxStartVolRow" hidden>
        <label class="startvol-toggle">
          <input type="checkbox" id="boxStartVolOn" />
          <span class="startvol-chip" aria-hidden="true"></span>
          <span>${escapeHtml(t('settingsView.startVolLabel'))}</span>
        </label>
        <span class="setting-value" id="boxStartVolVal"></span>
      </div>
      <div class="setting-row" id="boxStartVolHelpRow" hidden>
        <small class="muted small">${escapeHtml(t('settingsView.startVolHelp'))}</small>
      </div>
      <div class="setting-row" id="boxBalanceNoteRow" hidden>
        <small class="muted small" id="boxBalanceNote"></small>
      </div>
      ${vol.muted ? `<small class="muted small">${escapeHtml(t('settingsView.muted'))}</small>` : ''}
    </div>

    <div data-ui2-sec="bass" class="settings-section">
      <h3>${escapeHtml(t('settingsView.bassHeading'))}</h3>
      <div class="setting-row">
        <input type="range" id="boxBass"
               min="${bassProps.min}"
               max="${bassProps.max}"
               step="${bassProps.step}"
               value="${bassProps.value}"
               ${bassControlsDisabled(bass) ? 'disabled' : ''} />
        <span class="setting-value" id="boxBassVal">${formatRel(bassProps.value)}</span>
        <button class="btn btn-mini" id="boxBassReset" title="${escapeAttr(t('settingsView.bassResetTitle'))}" ${bassControlsDisabled(bass) ? 'disabled' : ''}>${escapeHtml(t('settingsView.bassResetBtn'))}</button>
      </div>
      ${bassControlsDisabled(bass)
        // Say WHY the row is dead. Greying the controls without a word is what
        // put a Lifestyle 650 owner in the inbox on 2026-08-22: his mail (with
        // a photo of this exact row) reported the slider and the Bass button
        // greyed out and unusable. He could see the state, not the reason.
        // bassHelp explains how to USE the slider, so it is replaced rather
        // than joined here: instructions for a control that cannot move are
        // the wrong text to put under it.
        //
        // The wording says "does not report", not "has no bass hardware".
        // bassAvailable=false is also what a /bassCapabilities read that never
        // came back looks like from here (see bassControlsDisabled), and on
        // his system the bass genuinely lives in a separate module, so the
        // line must not claim to know which of those it is.
        ? `<small class="muted small">${escapeHtml(t('settingsView.bassUnavailable'))}</small>`
        : `<small class="muted small">${escapeHtml(t('settingsView.bassHelp'))}</small>`}
    </div>

    <div data-ui2-sec="language" class="settings-section">
      <h3>${escapeHtml(t('settingsView.langHeading'))}</h3>
      <div class="kv-row"><span class="kv-key">${escapeHtml(t('settingsView.langCurrent'))}</span><span class="kv-val" id="boxLangCurrent">${escapeHtml(t('common.loading'))}</span></div>
      <div class="setting-row">
        <select id="boxLangSelect">${langOptionsHtml()}</select>
        <button class="btn btn-mini" id="boxLangSave">${escapeHtml(t('common.save'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.langHelp'))}</small>
    </div>
  `,
  };
}

export function wireBasicsSections(ctx) {
  const { box, bass, boseLangLabel, renderSettingsBoxSelect } = ctx;

  // Name combobox: input + dropdown list, free-typeable + filterable.
  wireCombobox('boxNameInput', 'boxNameToggle', 'boxNameList', deps.getRoomNames());

  // Language (BETA): GET /language on :8090 of the box, parse the
  // sysLanguage integer, show in the label and pre-select the
  // dropdown. POST on Save. Errors surface via showError; the
  // dropdown stays editable so users can keep trying.
  const langCurrent = $('boxLangCurrent');
  const langSel = $('boxLangSelect');
  const langSave = $('boxLangSave');
  const boseHost = box.host;
  // Clock + language go through the Go backend (GetBoxLanguage etc.):
  // the box's :8090 sends no CORS headers, so a direct frontend fetch
  // with a text/xml POST failed with "Failed to fetch" (CORS preflight
  // the box never answers). Server-side has no CORS and reaches :8090
  // even on Series-I/BCO boxes. See app.go boseGet/bosePostXML.
  if (langCurrent && langSel) {
    (async () => {
      try {
        const v = await GetBoxLanguage(boseHost);
        langCurrent.textContent = v ? boseLangLabel(v) : t('settingsView.langNoValue');
        if (v) langSel.value = v;
      } catch {
        langCurrent.textContent = t('settingsView.langUnreachable');
      }
    })();
  }
  if (langSave) {
    langSave.onclick = () => withButtonPending(langSave, async () => {
      const v = langSel.value;
      try {
        await SetBoxLanguage(boseHost, parseInt(v, 10) || 0);
        showToast(t('settingsView.langSavedToast', { v: boseLangLabel(v) }));
        langCurrent.textContent = boseLangLabel(v);
      } catch (e) { showError(e); }
    });
  }

  // Wire handlers. All operations explicitly target settingsBox
  // (not currentBox).
  $('boxNameSave').onclick = () => withButtonPending($('boxNameSave'), async () => {
    const typed = $('boxNameInput').value.trim();
    // The characters the speaker's firmware cannot survive (#1582) are
    // replaced before saving, the same way the agent stores them, so the field
    // shows the name the speaker really got.
    const desired = sanitizeSpeakerName(typed);
    if (!desired) return;
    const finalName = ensureWithUID(desired, box);
    try {
      await SetBoxName(box.host, box.port, finalName);
      showToast(desired !== typed
        ? t('settingsView.speakerNameAdjustedToast', { name: finalName })
        : t('settingsView.speakerNameSavedToast', { name: finalName }));
      $('boxNameInput').value = finalName;
      // Update locally: both the selected speaker and the matching
      // entry in the global speakers list so every dropdown is
      // consistent immediately. NO discoverBoxes or loadBoxSettings
      // afterwards because that would flicker and could overwrite a
      // slider input the user just made.
      box.friendlyName = finalName;
      const idx = state.boxes.findIndex(b => b.deviceID === box.deviceID);
      if (idx >= 0) state.boxes[idx] = { ...state.boxes[idx], friendlyName: finalName };
      // 90 s pending name: survives an mDNS refresh during which
      // the stick still announces the old name.
      state.pendingNames[box.deviceID] = { name: finalName, until: Date.now() + 90000 };
      renderSettingsBoxSelect();
      deps.renderBoxSelect();
    } catch (e) { showError(e); }
  });
  $('boxVolume').oninput = () => {
    $('boxVolumeVal').textContent = $('boxVolume').value;
    throttledSetVolume(box.host, box.port, parseInt($('boxVolume').value, 10));
  };
  $('boxVolume').onchange = () => {
    throttledSetVolume(box.host, box.port, parseInt($('boxVolume').value, 10));
  };
  $('boxBass').oninput = () => {
    $('boxBassVal').textContent = formatRel($('boxBass').value);
    const rel = parseInt($('boxBass').value, 10);
    throttledSetBass(box.host, box.port, rel + (bass.default || 0));
  };
  $('boxBass').onchange = () => {
    const rel = parseInt($('boxBass').value, 10);
    throttledSetBass(box.host, box.port, rel + (bass.default || 0));
  };
  $('boxBassReset').onclick = async () => {
    // Second half of the same gate as the rendered disabled attribute. On a
    // speaker that does not report an adjustable bass the range collapses to
    // 0..0, so this button could only ever post the default back at a level
    // the speaker says it does not have. It was ungated until 2026-08-23 and
    // sat live next to a greyed slider on a Lifestyle 650 (mail with photo,
    // 2026-08-22). Repeated here rather than trusted to the attribute alone:
    // the disabled attribute is markup, and a click that reaches this handler
    // from a stale render must not get through either.
    if (bassControlsDisabled(bass)) return;
    $('boxBass').value = 0;
    $('boxBassVal').textContent = formatRel(0);
    try {
      await SetBoxBass(box.host, box.port, bass.default || 0);
    } catch (e) { showError(e); }
  };
}

// Volume update plumbing. The slider has to behave like the
// hardware: the level changes WHILE the user drags, not only on
// release. A naive "fire on every input event" floods the box with
// PUTs (60+ events per drag-second), each of which holds a request
// slot on the Bose firmware's tiny HTTP server and blocks the next.
// Symptom on a stock-Bose-on-:17008 box: every PUT times out at the
// 6 s httpClient cap and the user sees a wall of red error toasts.
//
// makeOneInFlightThrottle gives us "fire the first immediately,
// drop intermediate calls, replay the most recent args after the
// current call finishes". Net effect during a 1 s drag: 3 to 5
// actual PUTs with the very last value being whatever the user
// released on, regardless of how fast they wiped.
function makeOneInFlightThrottle(fn) {
  let inFlight = false;
  let pending = null;
  const fire = async (args) => {
    inFlight = true;
    try {
      await fn(...args);
    } catch (e) {
      // Quiet during drag: showError would stack-up toasts. The
      // periodic status poll recovers the visible value anyway.
      console.warn('throttled box update failed', e);
    } finally {
      inFlight = false;
      if (pending) {
        const next = pending;
        pending = null;
        fire(next);
      }
    }
  };
  return (...args) => {
    if (inFlight) { pending = args; return; }
    fire(args);
  };
}
// throttledSetVolume/throttledSetBass are the app-wide volume plumbing: the
// settings sliders use them here, and the music view imports them from this
// module (the volume controls there share the exact same throttle). Exported so
// main.js's music view can reuse the single shared instance.
export const throttledSetVolume = makeOneInFlightThrottle(SetBoxVolume);
export const throttledSetBass = makeOneInFlightThrottle(SetBoxBass);

const debouncedSetVolume = debounce(async (box) => {
  try {
    await SetBoxVolume(box.host, box.port, parseInt($('boxVolume').value, 10));
  } catch (e) { showError(e); }
}, 200);
const debouncedSetBass = debounce(async (box, defaultBass) => {
  try {
    // The slider holds a relative value (0 = factory default). The
    // speaker expects the absolute value, so add the default offset
    // back in.
    const rel = parseInt($('boxBass').value, 10);
    await SetBoxBass(box.host, box.port, rel + (defaultBass || 0));
  } catch (e) { showError(e); }
}, 200);

// The stereo balance, shown where people look for it, and settable since
// v0.9.79.
//
// It has been on the Play page next to the volume since v0.9.35, and the owner
// who asked for it went to Speaker settings twice and reported it missing, on
// the very version that added it (#70, 2026-08-08). A feature nobody can find
// is not shipped. A second owner then found it and reported the other half:
// "steht neben dem Lautstaerkeregler und hat auch keinen Effekt" (2026-08-09).
// He was right, and it was read-only for a year because every write over the
// firmware's HTTP API hung the endpoint. It is written on the speaker's own
// WebSocket now (gesellix, #70), so the read-out is a control.
//
// Which speaker: always the pair's MASTER, whichever half is selected. Only the
// master reports a balance and only the master takes the write.
export async function refreshBoxBalanceRow(box, pair, boxes) {
  const row = document.getElementById('boxBalanceRow');
  const el = document.getElementById('boxBalance');
  if (!row || !el) return;
  // The heading and the row are one thing: a "Balance" heading over a hidden
  // slider would be a label for nothing.
  const head = document.getElementById('boxBalanceHead');
  const show = (on) => { row.hidden = !on; if (head) head.hidden = !on; };
  const slider = document.getElementById('boxBalanceSlider');
  const centre = document.getElementById('boxBalanceCentre');
  const src = balanceSourceBox(box, pair, boxes) || box;
  if (!src || src.kind === 'stock') { show(false); return; }
  const b = await readBoxBalanceInfo(src);
  if (!b) { show(false); setBalanceNote(''); return; }

  el.textContent = balanceStateLabel(b.actual);
  el.title = t(b.settable ? 'controls.balanceSetTitle' : 'controls.balanceTitle');
  show(true);
  setBalanceNote('');
  if (!slider || !centre) return;

  // An older agent on the other end reports no socket, so there is nothing to
  // drag: leave the read-out exactly as it was rather than offering a control
  // that cannot work.
  if (!b.settable) { slider.hidden = true; centre.hidden = true; return; }

  // Bounds from the speaker, never a constant (see readBoxBalanceInfo).
  slider.min = String(b.min);
  slider.max = String(b.max);
  slider.value = String(b.actual);
  slider.title = el.title;
  slider.hidden = false;
  centre.hidden = false;
  centre.title = t('controls.balanceCentreTitle');

  // While dragging, only the label moves. The write goes out on release: the
  // value travels over a WebSocket to the speaker and is then read back to
  // confirm, and firing that per pixel of slider travel would put a queue of
  // writes on the speaker's own bus for one gesture.
  slider.oninput = () => { el.textContent = balanceStateLabel(parseInt(slider.value, 10)); };
  slider.onchange = () => applyBalance(src, parseInt(slider.value, 10));
  centre.onclick = () => {
    slider.value = String(b.default || 0);
    el.textContent = balanceStateLabel(b.default || 0);
    applyBalance(src, b.default || 0);
  };
}

// The start level: what the speaker returns to when it wakes and plays again.
//
// Asked for by an owner whose Portable and ST20 do not keep their level over a
// power cycle, so every morning began at whatever the firmware had kept. Mine
// do keep it, which is why I first told him it was not a problem. He was right.
//
// Off by default (0), because a speaker that quietly changes its own volume is
// worse than one that forgets. It applies ONLY to the automatic resume after a
// rest; a level set while the music plays belongs to the user.
// The start volume, shown on the slider it is about.
//
// It used to be a second range input in its own row, running 0 to 100 like the
// one above it, with 0 standing for "off". Two identical sliders, two numbers,
// and nothing saying which was the level now and which was the level later.
//
// So: a checkbox for whether it applies at all, and when it does, a marker on
// the real volume bar that can be dragged. One bar, two things on it, and the
// difference between them is visible instead of described.
export async function refreshStartVolume(box) {
  const row = document.getElementById('boxStartVolRow');
  const helpRow = document.getElementById('boxStartVolHelpRow');
  const check = document.getElementById('boxStartVolOn');
  const label = document.getElementById('boxStartVolVal');
  const mark = document.getElementById('boxStartVolMark');
  const slider = document.getElementById('boxVolume');
  if (!row || !check || !label || !mark || !slider) return;
  row.hidden = true;
  if (helpRow) helpRow.hidden = true;
  mark.classList.add('hidden');
  if (!box || box.kind === 'stock') return;
  let data;
  try {
    data = await GetStartVolume(box.host, box.port);
  } catch (e) {
    // An older agent has no such route; the row simply is not there.
    return;
  }
  if (!data || data.supported !== true) return;

  // 0 is how the speaker stores "off", and that stays its business. Up here the
  // checkbox says it instead, so the number never has to mean two things.
  let vol = Number(data.volume) || 0;
  let saved = vol;

  const place = () => {
    if (vol <= 0) return;
    mark.style.left = markLeft(slider.clientWidth || 0, vol) + 'px';
    mark.setAttribute('aria-valuenow', String(vol));
  };
  const paint = () => {
    const on = vol > 0;
    check.checked = on;
    label.textContent = on ? String(vol) : t('settingsView.startVolOff');
    mark.classList.toggle('hidden', !on);
    place();
  };

  const save = async (next) => {
    try {
      await SetStartVolume(box.host, box.port, next);
      saved = next;
    } catch (e) {
      showError(e);
      vol = saved;
      paint();
    }
  };

  check.onchange = () => {
    if (check.checked) {
      // A stored level comes back as it was. With nothing stored the mark
      // appears at 20, which is a quiet level to wake up at and, just as
      // importantly, somewhere the person can SEE it. Spawning it at the
      // current volume put it directly under the native handle, which is the
      // one spot on the bar where a new marker is both hard to notice and
      // hard to grab. Zero is not an option either: it is invisible at the
      // far left and it is the value that means off.
      vol = saved > 0 ? saved : 20;
    } else {
      vol = 0;
    }
    paint();
    save(vol);
  };

  // Dragging. Pointer events rather than mouse, so a touch screen and a pen
  // work without a second code path, and the capture means the marker keeps
  // following the finger after it leaves the few pixels the marker occupies.
  let dragging = false;
  mark.onpointerdown = (e) => {
    if (vol <= 0) return;
    dragging = true;
    mark.setPointerCapture(e.pointerId);
    e.preventDefault();
  };
  mark.onpointermove = (e) => {
    if (!dragging) return;
    const r = slider.getBoundingClientRect();
    vol = pctFromOffset(r.width, e.clientX - r.left);
    label.textContent = String(vol);
    place();
  };
  const endDrag = (e) => {
    if (!dragging) return;
    dragging = false;
    try { mark.releasePointerCapture(e.pointerId); } catch { /* already gone */ }
    if (vol !== saved) save(vol);
  };
  mark.onpointerup = endDrag;
  mark.onpointercancel = endDrag;

  // The marker is a slider to anything that reads the page, so it answers the
  // keys a slider answers. Without this it would be a control that only a mouse
  // can reach, which is the sort of thing that gets shipped and then reported.
  mark.onkeydown = (e) => {
    if (vol <= 0) return;
    let next = vol;
    if (e.key === 'ArrowRight' || e.key === 'ArrowUp') next = Math.min(100, vol + 1);
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowDown') next = Math.max(1, vol - 1);
    else if (e.key === 'PageUp') next = Math.min(100, vol + 10);
    else if (e.key === 'PageDown') next = Math.max(1, vol - 10);
    else if (e.key === 'Home') next = 1;
    else if (e.key === 'End') next = 100;
    else return;
    e.preventDefault();
    vol = next;
    label.textContent = String(vol);
    place();
    save(vol);
  };

  // The track is only as wide as the panel, and the panel resizes with the
  // window and with sections opening above it. A marker placed once would then
  // point at the wrong level while looking perfectly deliberate.
  if (typeof ResizeObserver === 'function') {
    if (mark._ro) mark._ro.disconnect();
    mark._ro = new ResizeObserver(() => place());
    mark._ro.observe(slider);
  }

  paint();
  row.hidden = false;
  if (helpRow) helpRow.hidden = false;
}

// applyBalance sends one balance write and says what came back.
//
// Three outcomes, and they are deliberately three rather than two. A write that
// was refused is a failure. A write the speaker accepted but has not reported
// back is NOT a failure: the value goes out on a bus that acknowledges nothing,
// the agent reads it back on a budget of a couple of seconds, and a slow
// speaker missing that window would otherwise be shown to the user as a broken
// control. Silence is reserved for the case where it plainly worked.
async function applyBalance(src, target) {
  const res = await writeBoxBalance(src, target);
  if (!res || res.ok !== true) {
    setBalanceNote(t('controls.balanceFailed'), true);
    return;
  }
  const slider = document.getElementById('boxBalanceSlider');
  const el = document.getElementById('boxBalance');
  // The agent clamps to the speaker's own range, so the value that landed can
  // differ from the one that was asked for. Show what the speaker has.
  if (Number.isFinite(Number(res.target))) {
    if (slider) slider.value = String(res.target);
    if (el) el.textContent = balanceLabel(Number(res.target));
  }
  setBalanceNote(res.verified === false ? t('controls.balanceUnverified') : '');
}

function setBalanceNote(text, warn = false) {
  const row = document.getElementById('boxBalanceNoteRow');
  const el = document.getElementById('boxBalanceNote');
  if (!row || !el) return;
  el.textContent = text || '';
  el.classList.toggle('setup-warn', !!warn && !!text);
  row.hidden = !text;
}
