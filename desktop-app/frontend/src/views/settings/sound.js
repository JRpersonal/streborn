// views/settings/sound.js: the Sound & display group of Speaker settings. The
// clock display, resume on power-on, what the speaker's display shows (the
// track, its clock, STR's messages, splash and notices), the hidden games and
// the AirPlay optimisation.

import { $, escapeHtml, escapeAttr, showError, showToast } from '../../utils.js';
import { t } from '../../i18n/index.js';
import { displayTrackState, displayTrackOnPanel, displayTrackOffered, displayMessagesState, displaySplashState, displayNoticesState, arcadeState } from '../../boxstate.js';
// The On/Off pair every switchable speaker setting is drawn with: one paint,
// one read that hides the section, one write that repaints the old state when
// the speaker refuses it (togglesection.js, vitest-covered).
import { wireToggleSection } from '../../togglesection.js';
import { copyScreenshot, scoreText } from '../../arcadeshare.js';
import {
  GetClockFormat24,
  GetClockDisplay,
  SetClockDisplay,
  GetResumeOnPowerOn,
  SetResumeOnPowerOn,
  GetDisplayTrack,
  SetDisplayTrack,
  GetDisplayClock,
  SetDisplayClock,
  GetDisplayMessages,
  SetDisplayMessages,
  GetDisplaySplash,
  SetDisplaySplash,
  GetDisplayNotices,
  SetDisplayNotices,
  GetArcade,
  OpenArcadeThread,
  SaveArcadeScreenshot,
  ClipboardSetText,
  GetAirplayOpt,
  SetAirplayOpt,
} from '../../api.js';
import { deps, toggleDeps, withButtonPending, confirmFor } from './shared.js';

export function renderSoundSections() {
  return {
    group: 'sound',
    html: `
    <div data-ui2-sec="clock" class="settings-section" id="clockSection">
      <h3>${escapeHtml(t('settingsView.clockHeading'))}</h3>
      <div class="setting-row" id="clockControlsRow">
        <select id="boxClockFormat">
          <option value="24">${escapeHtml(t('settingsView.clock24h'))}</option>
          <option value="12">${escapeHtml(t('settingsView.clock12h'))}</option>
        </select>
        <button class="btn btn-mini toggle-btn" id="boxClockOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="boxClockOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <div class="setting-row hidden" id="clockNotSupportedRow">
        <span class="muted small">${escapeHtml(t('settingsView.clockNotSupported'))}</span>
      </div>
      <small class="muted small" id="clockHelp">${escapeHtml(t('settingsView.clockHelp'))}</small>
    </div>

    <div data-ui2-sec="resumeOnPower" class="settings-section" id="resumeOnPowerSection">
      <h3>${escapeHtml(t('settingsView.resumeOnPowerHeading'))}</h3>
      <div class="setting-row">
        <button class="btn btn-mini toggle-btn" id="resumeOnPowerOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="resumeOnPowerOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.resumeOnPowerHelp'))}</small>
    </div>

    <div data-ui2-sec="displayTrack" class="settings-section" id="displayTrackSection">
      <h3>${escapeHtml(t('settingsView.displayTrackHeading'))}</h3>
      <div class="setting-row">
        <button class="btn btn-mini toggle-btn" id="displayTrackOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="displayTrackOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <div class="setting-row hidden" id="displayTrackModeRow">
        <span class="muted small" style="margin-right:6px">${escapeHtml(t('settingsView.displayTrackModeLabel'))}</span>
        <button class="btn btn-mini toggle-btn" id="displayTrackModeArtist">${escapeHtml(t('settingsView.displayTrackModeArtist'))}</button>
        <button class="btn btn-mini toggle-btn" id="displayTrackModeTitle">${escapeHtml(t('settingsView.displayTrackModeTitle'))}</button>
        <button class="btn btn-mini toggle-btn" id="displayTrackModeBoth">${escapeHtml(t('settingsView.displayTrackModeBoth'))}</button>
      </div>
      <div class="setting-row hidden" id="displayClockRow">
        <span class="muted small" style="margin-right:6px">${escapeHtml(t('settingsView.displayClockLabel'))}</span>
        <button class="btn btn-mini toggle-btn" id="displayClockOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="displayClockOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <div class="format-warn" id="displayTrackWarn">
        <div class="warn-icon-inline">&#9888;</div>
        <div><b>${escapeHtml(t('settingsView.displayTrackWarn'))}</b></div>
      </div>
      <small class="muted small" id="displayTrackHelp">${escapeHtml(t('settingsView.displayTrackHelp'))}</small>
      <small class="muted small hidden" id="displayTrackHelpPanel">${escapeHtml(t('settingsView.displayTrackHelpPanel'))}</small>
      <small class="muted small hidden" id="displayClockHelp">${escapeHtml(t('settingsView.displayClockHelp'))}</small>
      <small class="fw-warn small hidden" id="displayTrackUnknown">${escapeHtml(t('settingsView.displayTrackUnknown'))}</small>
    </div>

    <div data-ui2-sec="displayMsg" class="settings-section hidden" id="displayMsgSection">
      <h3>${escapeHtml(t('settingsView.displayMsgHeading'))}</h3>
      <div class="setting-row">
        <button class="btn btn-mini toggle-btn" id="displayMsgOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="displayMsgOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.displayMsgHelp'))}</small>
      <small class="muted small hidden" id="displayMsgLast" style="display:block;margin-top:6px"></small>
    </div>

    <div data-ui2-sec="displaySplash" class="settings-section hidden" id="displaySplashSection">
      <h3>${escapeHtml(t('settingsView.displaySplashHeading'))}</h3>
      <div class="setting-row">
        <button class="btn btn-mini toggle-btn" id="displaySplashOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="displaySplashOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.displaySplashHelp'))}</small>
    </div>

    <div data-ui2-sec="displayNotices" class="settings-section hidden" id="displayNoticesSection">
      <h3>${escapeHtml(t('settingsView.displayNoticesHeading'))}</h3>
      <div class="setting-row">
        <button class="btn btn-mini toggle-btn" id="displayNoticesOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="displayNoticesOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.displayNoticesHelp'))}</small>
    </div>

    <div data-ui2-sec="arcade" class="settings-section hidden" id="arcadeSection">
      <h3>${escapeHtml(t('settingsView.blockfallHeading'))}</h3>
      <div id="arcadeGames"></div>
      <div class="setting-row">
        <button class="btn btn-mini" id="arcadeOpenThread">${escapeHtml(t('settingsView.blockfallOpenThread'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.blockfallHelp'))}</small>
    </div>

    <div data-ui2-sec="airplay" class="settings-section hidden" id="airplayOptSection">
      <h3>${escapeHtml(t('settingsView.airplayOptHeading'))}</h3>
      <div class="setting-row">
        <button class="btn btn-mini toggle-btn" id="airplayOptOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="airplayOptOff">${escapeHtml(t('settingsView.clockOff'))}</button>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.airplayOptHelp'))}</small>
      <small class="muted small" style="display:block;margin-top:6px">${escapeHtml(t('settingsView.airplayOptRecommend'))}</small>
    </div>
  `,
  };
}

export function wireSoundSections(ctx) {
  const { box } = ctx;
  // The clock goes through the Go backend (GetClockDisplay etc.) by host: the
  // box's :8090 sends no CORS headers, see the language section in basics.js.
  const boseHost = box.host;

  // Clock display (BETA): GET /clockDisplay to see current state,
  // POST to toggle. The endpoint is undocumented in the public Bose
  // Web API PDF and may not exist on every model. On 404 / 500 we
  // surface "not supported" rather than burying the failure.
  const clockOn = $('boxClockOn');
  const clockOff = $('boxClockOff');
  const clockFormat = $('boxClockFormat');
  // The SoundTouch 10 (rhino) has no display at all, so a clock can never be
  // shown on it regardless of what /clockDisplay reports. Gate by model
  // identity (reliable) rather than the GET probe below (flaky): swap the
  // controls for a plain "not supported" note, which is exactly what the help
  // text promises, and skip the GET/POST wiring entirely. Fixes #299. Same
  // \b10\b model test as the stereo-pair gate in multiroom.js.
  const noClockDisplay = /\b10\b/.test(box.model || '');
  if (noClockDisplay) {
    const ctlRow = $('clockControlsRow');
    const clockHelp = $('clockHelp');
    const nsRow = $('clockNotSupportedRow');
    if (ctlRow) ctlRow.classList.add('hidden');
    if (clockHelp) clockHelp.classList.add('hidden');
    if (nsRow) nsRow.classList.remove('hidden');
  } else {
    // Reflect the current on/off state by highlighting the active button
    // instead of a separate status line. null = unknown (neither lit).
    const paintClock = (enabled) => {
      if (clockOn) clockOn.classList.toggle('active', enabled === true);
      if (clockOff) clockOff.classList.toggle('active', enabled === false);
    };
    // Preselect the box's current 12/24h format in the dropdown. lastFormat
    // follows it so a format change that cannot be applied can be put back
    // instead of leaving the dropdown claiming something the speaker never got.
    let lastFormat = clockFormat ? clockFormat.value : '24';
    // The preselect must not overwrite a choice the user has already made. That
    // read goes to the speaker and can take seconds, and the dropdown shows the
    // static default meanwhile, so somebody who picks 12h while it is in flight
    // had their pick, and lastFormat with it, reset to whatever the speaker
    // answered. The format they never chose was then posted, and the dropdown
    // snapped back with nothing said.
    let userTouchedFormat = false;
    if (clockFormat) {
      clockFormat.addEventListener('input', () => { userTouchedFormat = true; });
      GetClockFormat24(boseHost).then(is24 => {
        if (userTouchedFormat) return;
        clockFormat.value = is24 ? '24' : '12';
        lastFormat = clockFormat.value;
      }).catch(() => {});
    }
    // refreshClock reads the current /clockDisplay state. Previously
    // any non-200 / fetch failure surfaced "not supported on this
    // model", but live-verified 2026-05-30 on a SoundTouch Portable
    // (taigan): the GET path returns 200 most of the time but the
    // box sometimes responds slowly or briefly drops the request
    // during BoseApp restart, and that one missed GET painted the
    // settings panel as "permanently unsupported" even though POST
    // toggles work fine. Don't draw conclusions from a single GET:
    // unknown means unknown, not unsupported.
    // Tri-state, and the third state is the point: true, false, or null for "the
    // speaker did not say". This used to be a plain boolean initialised to false,
    // which made "unknown" and "off" the same thing on the one path that writes
    // without the user choosing an on/off (see the format dropdown below).
    let clockState = null;
    const refreshClock = async () => {
      try {
        const s = await GetClockDisplay(boseHost);
        clockState = s === 'true' ? true : (s === 'false' ? false : null);
        paintClock(clockState);
      } catch {
        clockState = null;
        paintClock(null);
      }
    };
    refreshClock();
    const postClock = async (enable) => {
      try {
        // Real IANA time zone (e.g. "Europe/Berlin"), like the Bose iOS
        // app sets, so the speaker handles DST itself. userOffsetMinute is
        // sent too as a correct-now fallback: minutes EAST of UTC, i.e.
        // the negated JS getTimezoneOffset().
        let tz = '';
        try { tz = Intl.DateTimeFormat().resolvedOptions().timeZone || ''; } catch {}
        const offsetMin = -new Date().getTimezoneOffset();
        const fmt24 = (clockFormat ? clockFormat.value : '24') === '24';
        await SetClockDisplay(boseHost, enable, tz, offsetMin, fmt24);
        showToast(t('settingsView.clockSavedToast', { v: enable ? 'on' : 'off' }));
        await refreshClock();
        return true;
      } catch (e) { showError(e); return false; }
    };
    // The highlight moves before the speaker has answered, so the panel feels
    // immediate. It has to move BACK when the write fails, or the panel claims a
    // state the speaker refused: a failed write behind a dismissed error modal
    // left "On" lit on a clock that was still off, and the next thing that
    // happened was a bug report about the clock.
    const pressClock = (btn, enable) => {
      const before = clockState;
      paintClock(enable);
      withButtonPending(btn, async () => {
        const ok = await postClock(enable);
        if (!ok) paintClock(before);
      });
    };
    if (clockOn) clockOn.onclick = () => pressClock(clockOn, true);
    if (clockOff) clockOff.onclick = () => pressClock(clockOff, false);
    // Send the 12/24h format to the box immediately on dropdown change, keeping
    // the current on/off state (no need to click "On" again).
    //
    // The Bose body carries userEnable and timeFormat in ONE write, so the format
    // cannot be changed without asserting on or off. Until now the state came
    // from a boolean that started false and was only ever set by a SUCCESSFUL
    // GET, so a speaker whose /clockDisplay read failed, which is the documented
    // reason the unknown state exists at all (the box drops the request during a
    // BoseApp restart), was sent userEnable="false": a user who picked 24h
    // switched his clock display off and nothing said so. The state is now
    // re-read at the moment of the change, and an unknown answer stops the write
    // instead of guessing the destructive half of it.
    if (clockFormat) {
      clockFormat.onchange = async () => {
        await refreshClock();
        if (clockState === null) {
          clockFormat.value = lastFormat;
          showError(t('settingsView.clockFormatUnknownState'));
          return;
        }
        lastFormat = clockFormat.value;
        await postClock(clockState);
      };
    }
  }

  // Resume last station on power-on (all models, default on). GET reports the
  // current state; toggling applies live on the box (no reboot). The next real
  // power press either brings the last station back, like Bose did, or stays
  // silent. A self-wake (zone/stereo pair) never resumes regardless.
  wireToggleSection({
    sectionId: 'resumeOnPowerSection', onId: 'resumeOnPowerOn', offId: 'resumeOnPowerOff',
    get: () => GetResumeOnPowerOn(box.host, box.port),
    // Default on: treat anything other than an explicit false as enabled.
    stateFn: (r) => ({ show: true, enabled: r && r.enabled === false ? false : true }),
    onGetError: { show: true, enabled: true },
    set: (enabled) => SetResumeOnPowerOn(box.host, box.port, enabled),
    toastKey: 'settingsView.resumeOnPowerSavedToast',
  }, toggleDeps);

  // Show the live radio track on the speaker display (opt-in, default off).
  // Where the speaker's panel is STR's to draw on (onPanel), STR draws the
  // track itself and the music never stops, so there is no warning. Elsewhere
  // enabling it makes the box re-buffer (a brief audio dropout) on each text
  // change, so there turning it ON asks for confirmation first. A sub-row lets
  // the user pick what to show: artist, title, or both.
  const dtModeRow = $('displayTrackModeRow');
  const dtModeBtns = { artist: $('displayTrackModeArtist'), title: $('displayTrackModeTitle'), both: $('displayTrackModeBoth') };
  let dtMode = 'both';
  const dtUnknown = $('displayTrackUnknown');
  const dtWarn = $('displayTrackWarn');
  const dtHelp = $('displayTrackHelp');
  const dtHelpPanel = $('displayTrackHelpPanel');
  // Until the speaker says otherwise, assume the re-buffer path: the warning
  // stays and the confirm runs (the safe side for older agents).
  let dtOnPanel = false;
  const paintDtPath = () => {
    if (dtWarn) dtWarn.classList.toggle('hidden', dtOnPanel);
    if (dtHelp) dtHelp.classList.toggle('hidden', dtOnPanel);
    if (dtHelpPanel) dtHelpPanel.classList.toggle('hidden', !dtOnPanel);
  };
  const paintDtMode = () => {
    for (const [m, b] of Object.entries(dtModeBtns)) { if (b) b.classList.toggle('active', m === dtMode); }
  };
  // enabled is true, false, or null for "could not be read". A failed read used
  // to paint Off, so a speaker that was restarting showed the setting off while
  // it was on and pushing titles (#1083). Unknown lights neither button.
  wireToggleSection({
    sectionId: 'displayTrackSection', onId: 'displayTrackOn', offId: 'displayTrackOff',
    get: () => GetDisplayTrack(box.host, box.port),
    stateFn: (r) => ({ show: displayTrackOffered(r), enabled: displayTrackState(r), answer: r }),
    onGetError: { show: true, enabled: null },
    onLoaded: (st) => {
      const r = st.answer;
      if (!st.show) return;
      if (r && (r.mode === 'artist' || r.mode === 'title' || r.mode === 'both')) dtMode = r.mode;
      dtOnPanel = displayTrackOnPanel(r);
      paintDtPath();
      paintDtMode();
    },
    onPaint: (enabled) => {
      if (dtModeRow) dtModeRow.classList.toggle('hidden', enabled !== true);
      if (dtUnknown) dtUnknown.classList.toggle('hidden', enabled !== null);
      if (enabled === true) paintDtMode();
    },
    // Confirm before enabling where it interrupts the audio on every text
    // change; on a panel STR draws on there is nothing to warn about.
    confirm: async (enabled) => {
      if (!enabled || dtOnPanel) return true;
      return confirmFor(box, t('settingsView.displayTrackConfirmTitle'), t('settingsView.displayTrackConfirmBody'));
    },
    set: (enabled) => SetDisplayTrack(box.host, box.port, enabled, dtMode),
    toastKey: 'settingsView.displayTrackSavedToast',
  }, toggleDeps);
  // The mode buttons write the same setting with the track left on; a refused
  // write puts the previous mode back, the same rule as the On/Off pair.
  const saveDtMode = async (m) => {
    const before = dtMode;
    dtMode = m;
    paintDtMode();
    try {
      await SetDisplayTrack(box.host, box.port, true, dtMode);
      showToast(t('settingsView.displayTrackSavedToast'));
    } catch (e) {
      dtMode = before;
      paintDtMode();
      showError(e);
    }
  };
  for (const [m, b] of Object.entries(dtModeBtns)) {
    if (b) b.onclick = () => saveDtMode(m);
  }

  // The time on the speaker display during playback. Independent of the track
  // setting (with the track it sits in the top line, without it the time is
  // large), and only offered where STR draws on the speaker's panel itself:
  // an older agent or a speaker without one answers no or nothing, and the
  // row stays hidden.
  // The "section" here is the one row inside the track section; an older
  // agent (no clock setting) leaves it hidden.
  wireToggleSection({
    sectionId: 'displayClockRow', onId: 'displayClockOn', offId: 'displayClockOff',
    get: () => GetDisplayClock(box.host, box.port),
    stateFn: (r) => ({
      show: !!r && r.supported === true,
      enabled: r && typeof r.enabled === 'boolean' ? r.enabled : null,
    }),
    onLoaded: (st) => {
      const dcHelp = $('displayClockHelp');
      if (st.show && dcHelp) dcHelp.classList.remove('hidden');
    },
    set: (enabled) => SetDisplayClock(box.host, box.port, enabled),
    toastKey: 'settingsView.displayTrackSavedToast',
  }, toggleDeps);

  // Short message on the speaker display when a key cannot play (default on).
  // Only shown for a speaker that has a display; elsewhere the setting does
  // nothing, so the section stays hidden.
  wireToggleSection({
    sectionId: 'displayMsgSection', onId: 'displayMsgOn', offId: 'displayMsgOff',
    get: () => GetDisplayMessages(box.host, box.port),
    stateFn: displayMessagesState,
    onLoaded: (st) => {
      const dmLast = $('displayMsgLast');
      if (dmLast) {
        dmLast.textContent = st.last ? t('settingsView.displayMsgLast', { text: st.last }) : '';
        dmLast.classList.toggle('hidden', !st.last);
      }
    },
    set: (enabled) => SetDisplayMessages(box.host, box.port, enabled),
    toastKey: 'settingsView.displayMsgSavedToast',
  }, toggleDeps);

  // STR logo animation on the speaker display after an install or update, at
  // start-up and while an update lands (default on). Only shown where the agent
  // can draw on the panel (the Portable); elsewhere the section stays hidden.
  wireToggleSection({
    sectionId: 'displaySplashSection', onId: 'displaySplashOn', offId: 'displaySplashOff',
    get: () => GetDisplaySplash(box.host, box.port),
    stateFn: displaySplashState,
    set: (enabled) => SetDisplaySplash(box.host, box.port, enabled),
    toastKey: 'settingsView.displaySplashSavedToast',
  }, toggleDeps);

  // STR's short notices on the speaker display (a saved key, an empty key,
  // keys restored after a reboot; default on). Same rule as the splash: only
  // where the agent can draw on the panel, hidden elsewhere and on an agent
  // or app build that does not know the setting yet.
  wireToggleSection({
    sectionId: 'displayNoticesSection', onId: 'displayNoticesOn', offId: 'displayNoticesOff',
    get: () => GetDisplayNotices(box.host, box.port),
    stateFn: displayNoticesState,
    set: (enabled) => SetDisplayNotices(box.host, box.port, enabled),
    toastKey: 'settingsView.displayNoticesSavedToast',
  }, toggleDeps);

  // The hidden games on the speaker display: per game the score, the last
  // screen, and buttons to copy or save them for the GitHub thread. Hidden
  // until a game was played on this speaker, and a game shows up only once it
  // was played, so the app never gives one away.
  const arcSection = $('arcadeSection');
  const arcGames = $('arcadeGames');
  const arcOpen = $('arcadeOpenThread');
  if (arcSection && arcGames && arcOpen) {
    let arc = { show: false, games: [] };
    (async () => {
      try {
        arc = arcadeState(await GetArcade(box.host, box.port));
        arcSection.classList.toggle('hidden', !arc.show);
        arcGames.innerHTML = arc.games.map((g) => {
          const count = g.id === 'starguard' ? 'settingsView.blockfallScoreWaves' : 'settingsView.blockfallScore';
          return `<div class="arcade-game" data-game="${escapeAttr(g.id)}" style="margin:8px 0 12px">
            <h4 style="margin:0 0 4px">${escapeHtml(g.title)}</h4>
            <p style="margin:0 0 6px">${escapeHtml(t(count, { best: g.best, last: g.last, rows: g.rows }))}</p>
            ${g.screenshot ? `<img src="${escapeAttr(g.screenshot)}" alt="${escapeAttr(t('settingsView.blockfallScreenAlt'))}" style="display:block;max-width:100%;width:384px;image-rendering:pixelated;margin:6px 0">` : ''}
            <div class="setting-row">
              ${g.screenshot ? `<button class="btn btn-mini" data-arcade="copyShot">${escapeHtml(t('settingsView.blockfallCopyShot'))}</button>
              <button class="btn btn-mini" data-arcade="saveShot">${escapeHtml(t('settingsView.blockfallSaveShot'))}</button>` : ''}
              <button class="btn btn-mini" data-arcade="copyScore">${escapeHtml(t('settingsView.blockfallCopyScore'))}</button>
            </div>
          </div>`;
        }).join('');
      } catch { arcSection.classList.add('hidden'); }
    })();
    const saveShot = async (id) => {
      try {
        const path = await SaveArcadeScreenshot(box.host, box.port, id);
        if (path) showToast(t('settingsView.blockfallShotSavedToast', { path }));
      } catch (e) { showError(e); }
    };
    // The clipboard write starts first thing in the click; where the webview
    // cannot hold an image, the save dialog opens instead.
    arcGames.onclick = async (ev) => {
      const btn = ev.target.closest('[data-arcade]');
      const game = arc.games.find((g) => g.id === btn?.closest('[data-game]')?.dataset.game);
      if (!btn || !game) return;
      switch (btn.dataset.arcade) {
        case 'copyShot':
          if (await copyScreenshot(game.screenshot)) showToast(t('settingsView.blockfallShotCopiedToast'));
          else await saveShot(game.id);
          break;
        case 'saveShot':
          await saveShot(game.id);
          break;
        case 'copyScore':
          try {
            await ClipboardSetText(scoreText(game, box.model || ''));
            showToast(t('settingsView.blockfallScoreCopiedToast'));
          } catch (e) { showError(e); }
          break;
      }
    };
    arcOpen.onclick = () => OpenArcadeThread();
  }

  // AirPlay optimization (BCO speakers only). GET reports supported +
  // current state; the section stays hidden on non-BCO models. Toggling
  // reboots the speaker to apply it (BoseApp reads BCOResetTimerEnabled
  // at boot, same as the Bose app), so confirm first.
  // Same active-highlight pattern as the clock toggle: the lit button
  // is the state, no separate status line. null = unknown (neither lit).
  wireToggleSection({
    sectionId: 'airplayOptSection', onId: 'airplayOptOn', offId: 'airplayOptOff',
    get: () => GetAirplayOpt(box.host, box.port),
    stateFn: (r) => ({
      show: !!(r && r.supported),
      enabled: r && r.enabled === true ? true : (r && r.enabled === false ? false : null),
    }),
    confirm: () => confirmFor(box, 
      t('settingsView.airplayOptConfirmTitle'),
      t('settingsView.airplayOptConfirmBody'),
    ),
    set: (enabled) => SetAirplayOpt(box.host, box.port, enabled),
    afterSet: () => { setTimeout(deps.discoverBoxes, 60000); },
    toastKey: 'settingsView.airplayOptSavedToast',
  }, toggleDeps);
}
