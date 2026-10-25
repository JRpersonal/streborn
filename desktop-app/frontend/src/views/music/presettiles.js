// views/music/presettiles.js: renderPresets, the six preset tiles with their rename,
// long-press and drag handling.

import { DeletePreset, RenamePreset } from '../../api.js';
import { t } from '../../i18n/index.js';
import { logoImgTag, monogramDataUri, spotifyCoverImgTag } from '../../logos.js';
import { isNativeServicePreset, nativeServiceActive, nativeServiceBadge } from '../../nativeservice.js';
import { spotifyAccountName } from '../../spotifyaccountlabel.js';
import { nativeSpotifySlot, spotifySlotForContext, spotifySlotStillHolds } from '../../spotifynowplaying.js';
import { state } from '../../state.js';
import {
  $,
  activeSlotFromLocation,
  appArtFromBoxArt,
  confirmWarn,
  escapeAttr,
  escapeHtml,
  isKeyChrome,
  nativeSlotStale,
  orionStationPayload,
  presetBitrateLine,
  presetOwnArt,
  queueSlotActive,
  shouldAdoptPresetArt,
  showError,
  showToast,
} from '../../utils.js';
import { renderGroupControl } from './group.js';
import { play } from './play.js';
import {
  boxSourceLabel,
  decodeProxyUrl,
  isMeasurableStreamPreset,
  loadPresets,
  lostStrKey,
  presetStateLabel,
  recallBoxPreset,
  setPresetIfUnchanged,
  updatePresetTransferRow,
  wirePresetTransferRow,
} from './presets.js';
import { saveCurrentToSlot } from './save.js';
import { keyFocusMark, restoreKeyFocus, syncUi2Keys } from './ui2radio.js';

export function renderPresets() {
  const grid = $('presets');
  // The preview keeps the keyboard on the key it was on across the rebuild.
  const keyFocus = keyFocusMark();
  grid.innerHTML = '';
  // The transfer row lives right under the grid and follows the selection.
  wirePresetTransferRow();
  updatePresetTransferRow();
  renderGroupControl();
  const activeSlot = activeSlotFromLocation(state.nowLocation);
  // Remember the active Spotify slot from the per-slot /spotify/stream-<slot>.ogg
  // URL. A hardware next/prev advances go-librespot but can drop the slot from
  // the box's now-playing location, leaving only the generic "Spotify" name. We
  // keep the last known slot so the right tile stays lit. We must NOT fall back
  // to matching the preset NAME: a preset literally named "Spotify" (the generic
  // source name) would otherwise falsely light up, e.g. preset 1 lit up instead
  // of the playing preset 6 after pressing next on the remote.
  const spotifyPlaying = !!state.nowLocation && /\/spotify\/stream/.test(state.nowLocation);
  if (!spotifyPlaying) state.nowSpotifySlot = null;
  else if (activeSlot !== null) state.nowSpotifySlot = activeSlot;
  else if (state.nowSpotifySlot != null) {
    // The Spotify app took over the "(STR)" speaker: same generic location, but
    // the engine now plays a context the key does not hold (#1077, issue 14).
    const held = state.presets.find(x => x.slot === state.nowSpotifySlot);
    if (held && held.type === 'spotify' && !spotifySlotStillHolds(held.uri, state.spotifyNow)) {
      state.nowSpotifySlot = null;
    }
  }
  // Playback no key started (the Spotify app, then a hold that saved it on a
  // key) never gets a per-slot URL: light the key that holds the playing
  // context, by URI only (#1217). The stale check above drops it again once
  // the engine moves to something else.
  if (spotifyPlaying && state.nowSpotifySlot == null) {
    state.nowSpotifySlot = spotifySlotForContext(state.presets, state.spotifyNow);
  }
  // The speaker's own Spotify client (the "(L+R)" entry in the Spotify app)
  // plays a context it names in its location; the key holding that context
  // lights up, e.g. right after a hold saved it (#1077 issue 18).
  const nativeSpotifyKey = spotifyPlaying ? null : nativeSpotifySlot(state.presets, state.nowLocation);
  // If the speaker is playing through the stream proxy, resolve the
  // real stream URL of the source slot. That lets us mark sibling
  // slots with the same station as active too. Otherwise only the
  // single slot named in /stream/<n> would light up.
  // The station the speaker is ACTUALLY playing, decoded from the native ORION
  // descriptor (null for Spotify or a bare /stream/<n> proxy). While the box's
  // own preset list is re-synced under a stale recall, this is the only reliable
  // identity of the live audio, so a slot whose stored station has since changed
  // can be told apart from the one still coming out of the speaker (#758).
  const orionNow = orionStationPayload(state.nowLocation);
  const playingURL = orionNow && orionNow.streamUrl ? decodeProxyUrl(orionNow.streamUrl) : '';
  const playingName = orionNow && typeof orionNow.name === 'string' ? orionNow.name : '';
  let activeStreamURL = null;
  if (activeSlot !== null) {
    const ap = state.presets.find(x => x.slot === activeSlot);
    if (ap) activeStreamURL = ap.stream_url;
  } else if (playingURL) {
    // A native preset whose descriptor carries the RAW proxy form rather than a
    // per-slot one: the slot lookup finds nothing, and the location is the
    // descriptor rather than a URL, so neither of the matches above can fire
    // and no tile lit up while the speaker was plainly playing one of them.
    // The real station URL is inside the descriptor, and that does match what
    // the key holds.
    activeStreamURL = playingURL;
  }
  // The lit tile, for the stuck-buffering check in refreshStatus (#1077).
  state.activePresetSlot = null;
  for (let i = 1; i <= 6; i++) {
    const p = state.presets.find(x => x.slot === i);
    // A slot STR does not manage may still hold one of the box's own presets
    // (e.g. a Deezer playlist set on the speaker). Show it so the user sees and
    // can recall it, instead of a misleading "empty" tile.
    const bp = !p ? (state.boxPresets || []).find(x => x.slot === i) : null;
    // The queue match is checked outside the location gate below: right after
    // a click on a library album or folder key the optimistic location is
    // empty (the key stores no stream URL) and only the optimistic queue card
    // names the key (#978). Outside that window it still needs a location, so
    // a queue left active on a speaker that went quiet lights nothing.
    const queueLit = (!!state.nowLocation || Date.now() < (state.optimisticUntil || 0))
      && queueSlotActive(p, state.queue, state.nowLocation);
    const baseActive = p && (queueLit || (state.nowLocation && (
      p.stream_url === state.nowLocation ||
      (activeSlot !== null && p.slot === activeSlot) ||
      (activeStreamURL && p.stream_url === activeStreamURL) ||
      // Spotify: light the slot we recalled (remembered from the per-slot URL),
      // which survives a next/prev that drops the slot from the now-playing
      // location. Never match on the preset name (see nowSpotifySlot note above).
      (p.type === 'spotify' && spotifyPlaying && state.nowSpotifySlot != null && p.slot === state.nowSpotifySlot) ||
      (p.type === 'spotify' && nativeSpotifyKey != null && p.slot === nativeSpotifyKey) ||
      // Pandora / iHeartRadio: the speaker reports the service's own station
      // reference, which is exactly what the key stores.
      nativeServiceActive(p, state.nowLocation)
      // A music-library album or folder: the key stores no stream URL and the
      // box plays one track URL after another, so only the agent's queue card
      // names the key (#1190). Matched by queueLit above.
    )));
    // While a native descriptor is playing, drop a slot whose stored station no
    // longer matches the live audio: a preset list re-synced from the box remote
    // must not leave the freshly-changed tile lit as "playing" (#758). Only bites
    // when the speaker plays native radio (orionNow set) and the identity is
    // known and differs; otherwise baseActive stands unchanged.
    const isActive = baseActive && !(orionNow && nativeSlotStale({
      presetName: p && p.name,
      presetUrl: p && p.stream_url,
      playingName,
      playingUrl: playingURL,
    }));
    if (isActive && p && state.activePresetSlot === null) state.activePresetSlot = i;
    const hasErr = !!state.presetErrors[i];
    const div = document.createElement('div');
    div.className = 'preset' + (p || bp ? '' : ' empty') + (isActive ? ' playing' : '') + (hasErr ? ' error' : '') + (bp ? ' box-native' : '');
    div.dataset.slot = i;
    if (p) {
      const stateLabel = presetStateLabel(i, isActive, hasErr);
      const hint = state.nowLocation && !isActive
        ? `<div class="preset-hint">${escapeHtml(t('preset.longPressHint'))}</div>`
        : '';
      // Preset logo fallback chain:
      //   1. p.art candidates (pipe-separated if present).
      //   2. state.nowIcon ONLY when p.art is empty and the preset
      //      is currently active. Otherwise a logo from the actively
      //      playing station could leak onto an inactive preset
      //      button whose p.art is broken and falls through.
      //   3. DDG / Google service for stream host and its root
      //      domain.
      const presetCandidates = [];
      const addCands = (val) => {
        if (!val) return;
        for (const c of String(val).split('|')) {
          const t = c.trim();
          if (t && !presetCandidates.includes(t)) presetCandidates.push(t);
        }
      };
      // For a native play the box's <art> tag is the agent's own loopback
      // art-proxy URL (built for the speaker's display); adopting THAT would
      // persist a URL that is dead from this machine and hand the tile to the
      // DDG grey-chevron fallback, the exact poisoning the hold-save had
      // (#696). Unwrap first, and gate + persist on the unwrapped value.
      const adoptIcon = appArtFromBoxArt(state.nowIcon);
      if (p.type === 'spotify') {
        // Show the playlist or album cover STORED on the key (the agent fills
        // p.art from Spotify's oEmbed thumbnail when the key is saved, #1312).
        // That image is fixed at save time, so it neither changes nor lags
        // with the track playing, which is why the live now-playing art is
        // still never adopted here. Unwrapped like any stored art; with no
        // cover (a key saved before the fill, or oEmbed failed) or one that
        // does not load, spotifyCoverImgTag keeps the Spotify glyph.
        addCands(appArtFromBoxArt(p.art));
      } else if (presetOwnArt(p)) {
        // Same unwrap on the STORED art: keys hold-saved by v0.9.56 and older
        // already carry the box-loopback wrapper (the reporter's six keys in
        // the #696 bundle do), so unwrapping at render gives them back their
        // real image URL without rewriting the store. A library album or
        // folder key has no art of its own and shows its first track's cover,
        // the media server's albumArtURI (#1301).
        addCands(appArtFromBoxArt(presetOwnArt(p)));
      } else if (isActive && shouldAdoptPresetArt(adoptIcon, p)) {
        // Same gate refreshStatus uses to decide whether a late-arriving logo
        // is worth a redraw, so the two cannot drift apart.
        addCands(adoptIcon);
        // Auto-persist so the preset has its logo on the next load. Guarded:
        // skipped when the slot changed on the box since this list was loaded,
        // so the adopt never reverts a save from another client (#758).
        p.art = adoptIcon;
        setPresetIfUnchanged(state.currentBox, p, { art: adoptIcon });
      }
      // Render through the same Go-validated hydration the search and
      // Recently-played tiles use (logoImgTag + ResolveStationLogo): the raw
      // data-fallbacks cascade cannot reject DuckDuckGo's grey "no icon"
      // chevron, which the icon service serves as a real image on its 404, so
      // a station with no logo anywhere wore the chevron on its key while the
      // search row for the same station correctly fell back to the letter
      // tile (#696, EPIC CLASSICAL). A Spotify key validates its stored cover
      // the same way, with the Spotify glyph instead of the monogram as the
      // immediate src and fallback (spotifyCoverImgTag). The first DIRECT art candidate is
      // handed over as the favicon to validate; icon-service entries stored in
      // older chains are dropped here because the resolver rederives them from
      // the hosts, status-checked.
      let logo;
      if (p.type === 'spotify') {
        logo = spotifyCoverImgTag(presetCandidates[0], 'preset-logo');
      } else {
        const directArt = presetCandidates.find(
          (c) => /^https?:\/\//i.test(c) && !c.startsWith('https://icons.duckduckgo.com/')
        ) || '';
        logo = logoImgTag({
          name: p.name,
          favicon: directArt,
          homepage: p.homepage || '',
          url: p.stream_url || '',
          url_resolved: p.stream_url || '',
        }, 'preset-logo');
      }
      // The active tile mirrors the live now-playing bitrate even when the
      // stored preset bitrate is still 0 (preset saved before the bitrate
      // feature, or radio-browser had none). Persist it so the tile keeps
      // the value after a reload and the other clients see it too.
      let tileBitrate = p.bitrate || 0;
      // The "from" badge: a media server's name, or for a Pandora or
      // iHeartRadio key the service (the bitrate line is left out there, the
      // speaker's own client plays it and STR measures nothing).
      const tileBadge = p.source || nativeServiceBadge(p);
      if (isActive && state.nowBitrate > 0) {
        tileBitrate = state.nowBitrate;
        // Persist the corrected bitrate, but NEVER for Spotify presets:
        // SetPreset is radio-only and would overwrite the Spotify URI.
        if ((p.bitrate || 0) !== state.nowBitrate && isMeasurableStreamPreset(p)) {
          p.bitrate = state.nowBitrate;
          // Guarded: never rewrite a slot another client changed meanwhile (#758).
          setPresetIfUnchanged(state.currentBox, p, { bitrate: state.nowBitrate });
        }
      }
      // No bitrate line on a native Pandora / iHeartRadio key, nor on a media-server
      // key that has no rate (presetBitrateLine, #1065).
      const bitrateLine = isNativeServicePreset(p) ? '' : presetBitrateLine(p, tileBitrate);
      // A key shows what is SAVED on it: a short, static name, plus the markers
      // that say it is the one playing (the green .playing tile and the
      // Playing/Buffering/Paused line from presetStateLabel). It deliberately
      // carries NO live track line. Until 2026-08-23 the active key also
      // rendered state.nowTitle and marqueed it, but a tile is one third of the
      // window minus a 32px logo, so practically every "Artist - Title"
      // overflowed and scrolled, while the now-playing bar a few centimetres
      // above scrolled the identical string ("the app looks busy ... why
      // display the same information in two places? The song title/artist
      // almost always scrolls in the preset key area because the space is not
      // wide enough for the string", user report 2026-08-23). The running title
      // now lives only in that bar, which is the full window wide.
      div.innerHTML = `
        <div class="preset-head"><span class="num">${escapeHtml(t('preset.key', { n: i }))}</span><span class="preset-acts"><span class="ren" data-slot="${i}" title="${escapeAttr(t('preset.renameTitle'))}" aria-label="${escapeAttr(t('preset.renameTitle'))}" role="button"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="13" height="13" aria-hidden="true"><path d="M20.59 13.41l-7.17 7.17a2 2 0 0 1-2.83 0L2 12V2h10l8.59 8.59a2 2 0 0 1 0 2.82z"></path><line x1="7" y1="7" x2="7.01" y2="7"></line></svg></span><span class="del" data-slot="${i}" title="${escapeAttr(t('preset.deleteTitle'))}">&times;</span></span></div>
        <div class="preset-body">
          ${logo}
          <div class="preset-text">
            <div class="name">${escapeHtml(p.name || t('preset.key', { n: i }))}</div>
            ${p.type === 'spotify' && spotifyAccountName(p.account, state.spotifyAccountNames) ? `<div class="preset-account">${escapeHtml(spotifyAccountName(p.account, state.spotifyAccountNames))}</div>` : ''}
            ${tileBadge ? `<div class="preset-source" title="${escapeAttr(tileBadge)}">${escapeHtml(t('preset.sourceBadge', { source: tileBadge }))}</div>` : ''}
            ${bitrateLine ? `<div class="preset-bitrate">${bitrateLine}</div>` : ''}
            ${stateLabel}
          </div>
        </div>
        ${hint}
        <div class="long-press-bar" id="lp-bar-${i}"></div>
      `;
    } else if (bp && lostStrKey(bp)) {
      // A key STR itself wrote that the store no longer backs: the firmware
      // kept it across a removal and reinstall of STR, the stream behind it
      // went with the store, and a press plays nothing. It used to render as
      // the box-native tile below, i.e. as a playable speaker-side preset,
      // which is how the desktop showed six stations on a speaker whose store
      // had been empty for a day (#882). Shown and named, not offered: the
      // hint says where the key comes back from, the banner above the grid
      // says it for all of them.
      div.classList.add('lost');
      const srcLabel = boxSourceLabel(bp.source);
      const logo =
        `<img class="preset-logo" alt="" src="${escapeAttr(monogramDataUri(bp.name || srcLabel || '?'))}"/>`;
      div.innerHTML = `
        <div class="preset-head"><span class="num">${escapeHtml(t('preset.key', { n: i }))}</span></div>
        <div class="preset-body">
          ${logo}
          <div class="preset-text">
            <div class="name">${escapeHtml(bp.name || srcLabel || t('preset.onSpeaker'))}</div>
            ${srcLabel ? `<div class="preset-source" title="${escapeAttr(srcLabel)}">${escapeHtml(t('preset.sourceBadge', { source: srcLabel }))}</div>` : ''}
            <div class="preset-box-hint">${escapeHtml(t('preset.lostOnSpeaker'))}</div>
          </div>
        </div>
        <div class="long-press-bar" id="lp-bar-${i}"></div>
      `;
    } else if (bp) {
      // The box's own preset (a source STR does not manage, e.g. Deezer). Tap to
      // recall it via the hardware key; the box plays it through its own account.
      const bpActive = !!state.nowLocation && !!bp.location && state.nowLocation === bp.location;
      if (bpActive) div.classList.add('playing');
      const srcLabel = boxSourceLabel(bp.source);
      const logo =
        `<img class="preset-logo" alt="" src="${escapeAttr(monogramDataUri(bp.name || srcLabel || '?'))}"/>`;
      div.innerHTML = `
        <div class="preset-head"><span class="num">${escapeHtml(t('preset.key', { n: i }))}</span></div>
        <div class="preset-body">
          ${logo}
          <div class="preset-text">
            <div class="name">${escapeHtml(bp.name || srcLabel || t('preset.onSpeaker'))}</div>
            ${srcLabel ? `<div class="preset-source" title="${escapeAttr(srcLabel)}">${escapeHtml(t('preset.sourceBadge', { source: srcLabel }))}</div>` : ''}
            <div class="preset-box-hint">${escapeHtml(t('preset.boxNativeHint'))}</div>
          </div>
        </div>
      `;
    } else {
      const hint = state.nowLocation
        ? `<div class="preset-hint">${escapeHtml(t('preset.longPressHint'))}</div>`
        : `<div class="url">${escapeHtml(t('preset.searchHint'))}</div>`;
      div.innerHTML = `
        <div class="num">${escapeHtml(t('preset.key', { n: i }))}</div>
        <div class="name">${escapeHtml(t('preset.empty'))}</div>
        ${hint}
        <div class="long-press-bar" id="lp-bar-${i}"></div>
      `;
    }
    if (bp && lostStrKey(bp)) {
      // A dead STR key: a tap explains, it never sends a recall the speaker
      // cannot fulfil. The long-press save stays allowed, it is one of the
      // two ways the key comes back (the other is the copy the toast names).
      attachPresetHandlers(div, i, bp, { onPlay: () => showToast(t('preset.lostOnSpeakerToast')) });
    } else if (bp) {
      // Box-native preset: click recalls it; no long-press save so the user's
      // own (e.g. Deezer) preset can't be clobbered by STR's current station.
      attachPresetHandlers(div, i, bp, { onPlay: () => recallBoxPreset(i), allowSave: false });
    } else {
      attachPresetHandlers(div, i, p);
    }
    grid.appendChild(div);
  }
  grid.querySelectorAll('.ren').forEach(el => {
    el.onclick = (e) => {
      e.stopPropagation();
      renamePresetKey(parseInt(el.dataset.slot, 10));
    };
  });
  grid.querySelectorAll('.del').forEach(el => {
    el.onclick = async (e) => {
      e.stopPropagation();
      const slot = parseInt(el.dataset.slot, 10);
      const p = state.presets.find(x => x.slot === slot);
      const senderName = p && p.name ? escapeHtml(p.name) : t('preset.placeholderSender');
      const ok = await confirmWarn(
        t('preset.confirmClearTitle'),
        t('preset.confirmClearBody', { n: slot, name: senderName })
      );
      if (!ok) return;
      try {
        await DeletePreset(state.currentBox.host, state.currentBox.port, slot);
        loadPresets();
      } catch (err) { showError(err); }
    };
  });
  // No marquee pass here: nothing in a tile scrolls any more (see the tile
  // template above). The saved name wraps and the badge lines clip, so a
  // rebuilt grid needs no measuring frame.
  syncUi2Keys();
  restoreKeyFocus(keyFocus);
}

// PRESET_NAME_MAX mirrors maxPresetNameRunes in the agent, so the field stops
// where the speaker's own preset entry does and a typed name is never handed
// back shortened.
const PRESET_NAME_MAX = 64;

// renamePresetKey lets the user call a key whatever they want (#812). Station
// names arrive from the radio directory shouted in capitals, misspelled, or long
// enough to fill the whole tile, and the key is the user's own. Only the name is
// written: the station, its logo, a Spotify preset's playlist and account all
// stay exactly as they are.
async function renamePresetKey(slot) {
  if (!state.currentBox) return;
  const p = state.presets.find(x => x.slot === slot);
  if (!p) return;
  const current = p.name || '';
  const answer = confirmWarn(
    t('preset.renameHeading'),
    `<p>${escapeHtml(t('preset.renameBody', { n: slot }))}</p>`
    + `<input id="presetRenameInput" class="rename-input" type="text" maxlength="${PRESET_NAME_MAX}" />`,
    {
      icon: null,
      calm: true,
      rich: true,
      confirmLabel: t('common.save'),
      confirmClass: 'btn btn-primary',
    },
  );
  // Filled and focused only once the modal is on screen, and the value is set
  // from JS so an apostrophe or a quote in a station name cannot break out of
  // the markup.
  const input = $('presetRenameInput');
  if (input) {
    input.value = current;
    input.focus();
    input.select();
    // Enter is what a person presses after typing a name. Without this the
    // modal's two buttons are the only way out of the field.
    input.onkeydown = (e) => {
      if (e.key !== 'Enter') return;
      e.preventDefault();
      const confirmBtn = $('warnConfirm');
      if (confirmBtn) confirmBtn.click();
    };
  }
  if (!await answer) return;
  const name = (input ? input.value : '').trim();
  if (!name || name === current) return;
  try {
    await RenamePreset(state.currentBox.host, state.currentBox.port, slot, name);
  } catch (err) {
    showError(err);
    return;
  }
  showToast(t('preset.renamedKey', { n: slot, name }));
  loadPresets();
}

// applyTrackScroll turns an overflowing line into a gentle marquee: it pauses
// at the start, scrolls left until the end is visible, pauses, then jumps back
// to the start and repeats. Lines that fit are left static. Since 2026-08-23
// the now-playing bar above the preset grid is the only marquee in the app;
// the preset keys stay still (user report, same date), so this measures one
// element.
export function applyTrackScroll(selector = '.status-bar .now') {
  document.querySelectorAll(selector).forEach(box => {
    const inner = box.querySelector('.track-inner');
    if (!inner) return;
    inner.classList.remove('scrolling');
    inner.style.removeProperty('--track-scroll');
    inner.style.removeProperty('--track-dur');
    const overflow = inner.scrollWidth - box.clientWidth;
    if (overflow > 4) {
      // Brisk ~100 px/s scroll plus the built-in pauses, floored so a
      // slightly-too-long line still scrolls slowly enough to read.
      const dur = Math.max(3, Math.round(overflow / 75 + 1.5));
      inner.style.setProperty('--track-scroll', overflow + 'px');
      inner.style.setProperty('--track-dur', dur + 's');
      inner.classList.add('scrolling');
    }
  });
}

// attachPresetHandlers wires click (short = play) and long press
// (hold = save the current station to this slot). LONG_PRESS_MS =
// 1100 ms: a clearly deliberate hold, so a normal tap never saves by accident.
// VISUAL_HOLD_DELAY = 180 ms: only after this hold time do we show the
// scale(0.96) visual. A short click avoids the mini jiggle that a transition
// scale-down + scale-up would otherwise produce on the logo.
//
// opts.onPlay overrides the short-click action (box-native presets recall the
// box's hardware key instead of STR's play). opts.allowSave=false disables the
// long-press save so a box-native preset can't be overwritten by a hold.
const LONG_PRESS_MS = 1100;
const VISUAL_HOLD_DELAY = 180;
// isKeyChrome reports whether an event landed on one of the small icons in a
// key's header (clear, rename) rather than on the key itself. Those icons sit
// INSIDE the element that carries the play click and the hold-to-save, so
// without this a tap on the pencil would also start the station, and holding it
// would save over the key the user only wanted to rename. Lives in utils.js.

function attachPresetHandlers(el, slot, preset, opts = {}) {
  const onPlay = opts.onPlay || (() => play(slot));
  const allowSave = opts.allowSave !== false;
  let timer = null;
  let visualTimer = null;
  let armed = false;
  let firedLong = false;
  let startedAt = 0;
  const bar = el.querySelector('.long-press-bar');
  const animateBar = () => {
    if (!armed) return;
    const elapsed = Date.now() - startedAt;
    // The bar only represents the deliberate-hold window: it fills 0 -> 100%
    // between VISUAL_HOLD_DELAY and LONG_PRESS_MS, so it never shows for the
    // first 180 ms (a normal click). Starting it at elapsed/LONG_PRESS_MS made
    // a quick click flash the "save station" bar before mouseup.
    const pct = Math.min(100, Math.max(0,
      ((elapsed - VISUAL_HOLD_DELAY) / (LONG_PRESS_MS - VISUAL_HOLD_DELAY)) * 100));
    if (bar) bar.style.width = pct + '%';
    if (armed) requestAnimationFrame(animateBar);
  };
  const start = (e) => {
    if (e.button !== undefined && e.button !== 0) return; // left click only
    // A press on one of the icons in the key's header is not a press on the key.
    if (isKeyChrome(e.target)) return;
    armed = true; // we start the hold
    firedLong = false; // true once long press fires
    startedAt = Date.now();
    visualTimer = setTimeout(() => {
      if (!armed) return;
      el.classList.add('long-press');
      // Only start filling the save-progress bar once the hold is deliberate, so
      // a normal short click never flashes it.
      requestAnimationFrame(animateBar);
    }, VISUAL_HOLD_DELAY);
    if (allowSave) {
      timer = setTimeout(async () => {
        if (!armed) return;
        firedLong = true;
        await saveCurrentToSlot(slot);
        armed = false;
        el.classList.remove('long-press');
        if (bar) bar.style.width = '0%';
      }, LONG_PRESS_MS);
    }
  };
  const cancel = () => {
    if (!armed) return;
    armed = false;
    if (timer) { clearTimeout(timer); timer = null; }
    if (visualTimer) { clearTimeout(visualTimer); visualTimer = null; }
    el.classList.remove('long-press');
    if (bar) bar.style.width = '0%';
  };
  const finish = (e) => {
    if (isKeyChrome(e.target)) return;
    const wasArmed = armed;
    cancel();
    if (!wasArmed) return;
    if (firedLong) return;
    if (preset) onPlay();
  };
  el.addEventListener('mousedown', start);
  el.addEventListener('mouseup', finish);
  el.addEventListener('mouseleave', cancel);
  el.addEventListener('touchstart', (e) => { start(e); }, { passive: true });
  el.addEventListener('touchend', (e) => { finish(e); });
  el.addEventListener('touchcancel', cancel);
}
