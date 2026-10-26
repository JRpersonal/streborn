// views/music/save.js: saving what is playing (or a native service) onto a preset
// key, with the already-on-another-key offer.

import {
  LogSpotifySaveGate,
  MovePreset,
  RadioVote,
  SaveLibraryPreset,
  SaveNativePreset,
  SavePlayingPreset,
  SaveQueuePreset,
  SaveSpotifyPreset,
  SetPreset,
  SpotifyNowPlaying,
} from '../../api.js';
import { t } from '../../i18n/index.js';
import { nativeServiceLabel, nativeServiceSaveable } from '../../nativeservice.js';
import { offerPresetMove, parsePresetConflict, presetConflictNote } from '../../presetmove.js';
import { state } from '../../state.js';
import {
  activeSlotFromLocation,
  appArtFromBoxArt,
  confirmWarn,
  escapeHtml,
  libraryPlayMatches,
  orionStationPayload,
  queueSaveFallsThrough,
  savePlayingFallsThrough,
  savePresetCase,
  showError,
  showToast,
  spotifySaveContextURI,
} from '../../utils.js';
import { decodeProxyUrl, loadPresets } from './presets.js';
import { refreshStatus } from './status.js';

// APP_PLAY_FRESH_MS is how long the app trusts its own record of an ad-hoc
// station it started (state.lastAppPlay) over the box-reported now-playing
// when saving to a key. Short on purpose: it only needs to cover the
// play-then-save gesture and the wake window in which an agent-side resume
// could have raced the play (#252); after that, whatever the box reports IS
// what the user hears, so the box report wins again.
const APP_PLAY_FRESH_MS = 2 * 60 * 1000;

// showPresetSaveError turns a failed preset save into the right message. The
// agent refuses (409 already-on-slot) a save whose station already sits on
// another key rather than silently deleting that key (#836); that is a question,
// not an error, so it becomes the note naming the collision plus the offer to
// move the station onto the key the user pressed (#709/#925). slot is that key;
// without one there is nothing to move to and the plain note is all that is left.
function showPresetSaveError(err, slot) {
  const conflict = parsePresetConflict(err);
  if (conflict) {
    if (slot >= 1 && slot <= 6 && state.currentBox) {
      presetMoveOffer(conflict, slot);
    } else {
      showToast(presetConflictNote(conflict, t));
    }
    return;
  }
  showError(t('preset.saveFailed', { err: String(err) }));
}

// presetMoveOffer hands offerPresetMove the app's own modal, agent call and grid
// repaint. The decision itself stays in presetmove.js where it is unit-tested.
export function presetMoveOffer(conflict, slot, onBox) {
  const box = onBox || state.currentBox;
  return offerPresetMove({
    conflict,
    toSlot: slot,
    t,
    // confirmWarn writes the body as HTML and a station name comes from the
    // directory, so it is escaped here.
    confirm: (title, body, opts) => confirmWarn(title, escapeHtml(body), opts),
    move: (from, to) => MovePreset(box.host, box.port, from, to),
    toast: showToast,
    fail: showError,
    done: loadPresets,
  });
}

// voteForWhatPlays counts a save of what plays as a vote for the station on
// radio-browser, when the station came from there.
function voteForWhatPlays() {
  if (state.nowUUID) {
    RadioVote(state.nowUUID).catch(() => {});
  }
}

// saveNativeToSlot keeps the Pandora or iHeartRadio station the speaker is
// playing on a key. The app sends only the name: the agent reads the exact
// item from the speaker's own now-playing.
async function saveNativeToSlot(slot) {
  const nname = state.nowName || nativeServiceLabel(state.nowSource);
  try {
    await SaveNativePreset(state.currentBox.host, state.currentBox.port, slot, nname);
    showToast(t('preset.savedToKey', { n: slot, name: nname }));
    await loadPresets();
  } catch (err) {
    showPresetSaveError(err, slot);
  }
}

// saveCurrentToSlot saves the currently playing station onto the
// given slot (overwrites whatever was there before). Uses the
// now_playing data state.nowLocation + state.nowName plus the last
// known logo.
// The Spotify "plan could not be checked" hint is shown once per app run. A
// Premium account whose token cannot read the plan stays unknown for good, and
// repeating the hint on every save would nag exactly those users.
let spotifyPlanHintShown = false;

export async function saveCurrentToSlot(slot) {
  // Refresh from the speaker first. On a hardware key press,
  // state.nowLocation / nowName often lag behind (the boxws event
  // arrives late). Without the refresh we would save "Station" as
  // the name or the previous station.
  try { await refreshStatus(); } catch {}
  if (!state.nowLocation) {
    showToast(t('preset.noCurrentStation'));
    return;
  }

  // Pandora or iHeartRadio playing: the speaker's own client plays it and the
  // item lives only on the speaker, so the agent reads it from there and keeps
  // it on the key as the speaker's own item. No stream URL is involved.
  if (nativeServiceSaveable(state.nowSource)) {
    await saveNativeToSlot(slot);
    return;
  }

  // Which save path applies is a pure decision (savePresetCase in utils.js,
  // unit-tested): spotify / app-play / copy-slot / direct.
  const sourceSlot = activeSlotFromLocation(state.nowLocation);
  // A fresh app play is authoritative regardless of what the box reports right
  // now (native-switch lag / wake-resume race, #836). savePresetCase decides;
  // the 'app-play' branch below saves state.lastAppPlay with its real logo chain.
  // A single library track the app started from the Library, still playing:
  // saved with its media server like the Library star button does (#1065).
  const libraryMatch = libraryPlayMatches(state.lastLibraryPlay, state.nowLocation, decodeProxyUrl(state.nowLocation));
  let saveCase = savePresetCase(state.nowLocation, sourceSlot, state.lastAppPlay, Date.now(), APP_PLAY_FRESH_MS, state.queue, libraryMatch);

  // Case folder: a music-library folder plays as the speaker's queue. The
  // speaker only reports the current track, so the agent builds the FOLDER
  // preset from its live queue, the same one the Library star button stores
  // (#1030). When the agent says no folder plays after all (the queue ended a
  // moment ago), or it is too old to know the call, the save falls through to
  // the paths below as if no queue were known.
  if (saveCase === 'queue') {
    try {
      const fname = await SaveQueuePreset(state.currentBox.host, state.currentBox.port, slot);
      showToast(t('library.folderPresetSaved', { n: slot, name: fname || state.nowName || '' }));
      await loadPresets();
      return;
    } catch (err) {
      if (!queueSaveFallsThrough(err)) {
        showPresetSaveError(err, slot);
        return;
      }
      saveCase = savePresetCase(state.nowLocation, sourceSlot, state.lastAppPlay, Date.now(), APP_PLAY_FRESH_MS, null, libraryMatch);
    }
  }

  // Case Spotify: the speaker is playing a Spotify playlist. Save a REAL
  // Spotify preset (type=spotify with the playlist URI), not a radio link to
  // the raw stream. The latter showed the album cover instead of the Spotify
  // logo and could not recall/shuffle the playlist. Needs the current context
  // (playlist URI) from /spotify/info.
  if (saveCase === 'spotify') {
    // We can only save a real, recallable Spotify preset when we know the
    // playlist/album/track context URI. The state.spotifyNow context is only
    // refreshed by a throttled (>3s) background poll, so at save time it can
    // lag or be momentarily empty even while a real playlist is playing. That
    // made a legitimate save fail with a false "no replayable playlist" (Pierre,
    // #45, was on Premium playing a playlist). Re-read the live context from the
    // speaker now instead of trusting the cache.
    // The URI itself is decided after the engine read below
    // (spotifySaveContextURI): the engine's context only counts while the
    // speaker is playing STR's engine stream.
    let engineCtx = state.spotifyNow ? state.spotifyNow.context : '';
    let acct = (state.spotifyNow && state.spotifyNow.account) || '';
    // Whether a key on THIS speaker could play at all. The same read already
    // happens here, so asking costs nothing. undefined means the speaker's
    // agent predates the field, and "cannot tell" must never become a warning.
    let canRecall;
    // And whether the account behind it can do an on-demand recall at all. A free
    // Spotify account cannot, so the key stores fine and then never plays: that is
    // the entitlement half of #976, which the login check alone never covered.
    let premiumRequired;
    // Whether the speaker could find out the plan at all. premiumRequired false
    // means EITHER Premium OR an unanswered question (Spotify answering 429 is
    // the common one, #1314), and only np.product tells them apart. undefined =
    // the speaker was not readable, which stays silent like every other gate.
    let planKnown;
    let gateRead = 'unknown';
    try {
      const np = await SpotifyNowPlaying(state.currentBox.host, state.currentBox.port);
      if (np) {
        gateRead = 'yes';
        if (np.context) engineCtx = np.context;
        if (np.account) acct = np.account;
        if (np.canRecall !== null && np.canRecall !== undefined) canRecall = !!np.canRecall;
        if (np.premiumRequired !== null && np.premiumRequired !== undefined) premiumRequired = !!np.premiumRequired;
        planKnown = !!np.product;
      }
    } catch { gateRead = 'no'; }
    // Fallback: go-librespot's /spotify/info can report an empty context even
    // while a real playlist is playing (it depends on how playback was started).
    // The box's own now-playing still carries the URI STR wrote into its
    // /playback/container/<base64 spotify:...> location, so decode that before
    // giving up (Pierre, #45: Premium, a playlist was playing, the box location
    // held spotify:playlist:..., but np.context came back empty so the save
    // wrongly failed).
    //
    // And the reverse (#1077 issue 15): on the speaker's OWN Spotify entry
    // (source SPOTIFY, same /playback/container location form) STR's engine is
    // not what plays, yet it can still hold an older, paused context. Taking
    // that saved the wrong album under the playing song's name, the duplicate
    // guard refused it as "already on key 6", and "Move" would have taken it
    // off key 6. So the speaker's own location decides there, and the engine
    // answer is used only for the account and the recall gates.
    const ctxUri = spotifySaveContextURI(state.nowSource, state.nowLocation, engineCtx);
    if (!ctxUri) {
      // Spotify is playing but the speaker reported no playlist/album/track
      // context. This is NOT the same as a non-replayable station: a real
      // station carries a (non-replayable) context that the agent rejects on
      // save below. An empty context almost always means an out-of-date speaker
      // agent that cannot capture the context yet (the app updates separately
      // from the on-box agent) or a will_play event the agent missed. Guide the
      // user to update the speaker and replay the playlist rather than falsely
      // claiming there is no playlist.
      showError(t('preset.spotifyContextUnknown'));
      return;
    }
    const sname = state.nowName || 'Spotify';
    try {
      await SaveSpotifyPreset(
        state.currentBox.host, state.currentBox.port,
        slot, sname, ctxUri, acct
      );
      showToast(t('preset.savedToKey', { n: slot, name: sname }));
      // The speaker already knows this key will be refused when it is pressed:
      // both recall paths gate on the same question, and only the SAVE side had
      // no gate, so the app reported success for a key that cannot play (#976).
      // Said after the save, not instead of it: the key becomes good the moment
      // the speaker is picked in Spotify once, so refusing to store it would
      // throw away work the user will want.
      // Same order the speaker applies when the key is pressed (recallgate.go):
      // picked-once first, then the plan. Both stay silent when the speaker could
      // not be asked, because "cannot tell" must never become a warning.
      let notice = 'none';
      if (canRecall === false) {
        notice = 'needs-login';
        showToast(t('preset.spotifyKeyNeedsLogin'), 12000);
      } else if (premiumRequired === true) {
        notice = 'needs-premium';
        showToast(t('preset.spotifyKeyNeedsPremium'), 12000);
      } else if (canRecall === true && premiumRequired === false && planKnown === false && !spotifyPlanHintShown) {
        spotifyPlanHintShown = true;
        // The speaker answered but could not read the plan. Not a warning, a
        // hint: the key may well be fine, but a free account would make it
        // silent and nothing else would say so.
        notice = 'plan-unknown';
        showToast(t('preset.spotifyKeyPlanUnknown'), 10000);
      }
      // Which notice was shown, and what it was decided from. Without this a
      // report of "two different notices for one long press" cannot be read out
      // of a bundle at all (#976).
      // An older build has no such binding, and logging must never be the reason
      // a save reports a failure, so the rejection is swallowed.
      try {
        LogSpotifySaveGate(state.currentBox.host, slot,
          canRecall === undefined ? 'unknown' : (canRecall ? 'yes' : 'no'),
          premiumRequired === undefined ? 'unknown' : (premiumRequired ? 'yes' : 'no'),
          gateRead === 'yes' ? notice : notice + ' (speaker not readable)')?.catch(() => {});
      } catch {}
      await loadPresets();
      return;
    } catch (err) {
      // The agent validates the context and returns 422 spotify-uri-unplayable
      // for a genuinely non-replayable selection (a Spotify radio/station). Show
      // the precise "no replayable playlist" message for that; a generic failure
      // otherwise.
      const msg = String(err);
      if (/spotify-uri-unplayable|replayable playlist/i.test(msg)) {
        showError(t('preset.spotifyNotSaveable'));
      } else {
        showPresetSaveError(err, slot);
      }
      return;
    }
  }

  // Case library track: the speaker plays the single music-library track the
  // app started from the Library. Saved through SaveLibraryPreset with the media
  // server, exactly what the Library star button stores, so the key shows its
  // "from <server>" line and plays straight from the server. The 'direct' path
  // below stored it as a plain stream and lost the server (#1065).
  if (saveCase === 'library') {
    const lib = state.lastLibraryPlay;
    const lname = lib.name || state.nowName || '(track)';
    try {
      await SaveLibraryPreset(
        state.currentBox.host, state.currentBox.port,
        slot, lname, lib.url, lib.art || '', 0, lib.source || '', lib.durationSec || 0
      );
      showToast(t('preset.savedToKey', { n: slot, name: lname }));
      await loadPresets();
    } catch (err) {
      showPresetSaveError(err, slot);
    }
    return;
  }

  // Case A: speaker is playing a proxy item
  // (location = /stream/<sourceSlot>). That happens when the
  // station was triggered via a hardware key or by selecting
  // another soft slot. In that case we copy the source preset
  // directly onto the target slot: name, URL, art logo one to one.
  // That bypasses state.nowIcon / state.nowName completely; on
  // hardware press both often still hold the previous station.
  // Same-slot saves (sourceSlot === slot) must take this path too: falling
  // through to Case B would store the box-visible /stream/<n> proxy URL and
  // permanently clobber the preset's origin URL (#252).
  if (saveCase === 'app-play' || saveCase === 'copy-slot') {
    // The app itself started an ad-hoc station moments ago, yet the box
    // reports a /stream/<slot> location: on a speaker that was asleep, an
    // agent-side wake resume racing the play can have put the PREVIOUS
    // preset back on (#252). Copying that preset here saved the OLD station
    // onto the key while the user's chosen one silently vanished. The app
    // knows exactly which station the user picked, so save its own record;
    // the plain copy below remains for the true hardware-key case, where the
    // app has no record of the play.
    const app = state.lastAppPlay;
    if (saveCase === 'app-play') {
      const aname = app.name || t('preset.placeholderSender');
      try {
        await SetPreset(
          state.currentBox.host, state.currentBox.port,
          slot, aname, app.url, app.icon || '', app.bitrate || 0, app.homepage || '', app.codec || ''
        );
        showToast(t('preset.savedToKey', { n: slot, name: aname }));
        await loadPresets();
        if (app.uuid) {
          RadioVote(app.uuid).catch(() => {});
        }
      } catch (err) {
        showPresetSaveError(err, slot);
      }
      return;
    }
    const src = state.presets.find(p => p.slot === sourceSlot);
    if (src && src.stream_url) {
      try {
        await SetPreset(
          state.currentBox.host, state.currentBox.port,
          slot, src.name, src.stream_url, src.art || '', src.bitrate || 0, src.homepage || '', src.codec || ''
        );
        showToast(t('preset.copiedToKey', { n: slot, name: src.name }));
        await loadPresets();
        return;
      } catch (err) {
        showPresetSaveError(err, slot);
        return;
      }
    }
    // No stored preset to copy from (empty source slot) and no fresh app
    // play record: the only URL available is the box's own /stream/<n>
    // proxy location, which must never be saved as a preset.
    showError(t('preset.saveFailed', { err: 'source preset empty' }));
    return;
  }

  // Case station: a stream the app did not just start, or started longer ago
  // than APP_PLAY_FRESH_MS. The speaker agent keeps its own record of every
  // play with the whole logo chain, the homepage and the codec, and knows
  // which form of the stream the speaker reports; the now-playing below holds
  // one logo guess and neither of the others. Rebuilding the station from it
  // stored a directory's site icon in place of the station's logo, and the
  // phone page saved the same station differently (#1467). So the agent saves
  // it, through the same call as the phone page's hold. Only an agent or an
  // app build without that call falls back to the app's own save below.
  try {
    const sname = await SavePlayingPreset(state.currentBox.host, state.currentBox.port, slot);
    const shown = sname || state.nowName || t('preset.placeholderSender');
    showToast(t('preset.savedToKey', { n: slot, name: shown }));
    await loadPresets();
    voteForWhatPlays();
    return;
  } catch (err) {
    if (!savePlayingFallsThrough(err)) {
      showPresetSaveError(err, slot);
      return;
    }
  }

  // Case B: speaker is playing a stream that does NOT go through
  // our proxy (for example a station started directly via the radio
  // search). Use state.nowLocation / nowName / nowIcon as before.
  //
  // Unless it is a NATIVE radio preset, where the speaker fetches the stream
  // itself and its location is the ORION descriptor rather than a URL. Saving
  // that verbatim posts "/station?data=..." as the stream, which the speaker
  // rightly refuses:
  //
  //   status 422 {"code":"stream-url-invalid","error":"This selection can't be
  //   saved as a preset (no playable stream)."}
  //
  // Reported on #608, and it hit every hold-to-save on a speaker whose presets
  // had migrated to the native form: the "+" button kept working because that
  // path uses the app's own record of the station instead of the speaker's.
  // The descriptor carries the name, the logo and the stream URL, so unwrap it
  // and save what is actually playing. slotFromOrionStation already handles
  // the case where that stream URL is a per-slot proxy; this is the rest of
  // them, above all the /stream/raw form.
  const orion = orionStationPayload(state.nowLocation);
  if (orion && orion.streamUrl) {
    const oname = orion.name || state.nowName || t('preset.placeholderSender');
    // The descriptor's imageUrl is built for the SPEAKER, not for us: it is
    // the agent's loopback art proxy (or its /icon.png stand-in), which only
    // resolves on the box itself. Persisting it verbatim was the #696 bug:
    // every hold-saved key held art that is dead from the user's machine, the
    // tile's <img> fell through to the DuckDuckGo stream-host icon, and DDG's
    // 404 body is the grey chevron the webview renders without firing onerror
    // (see app_library.go). Unwrap to the real image URL; when that leaves
    // nothing (stand-in logo), try the now-playing icon the same way.
    const oart = appArtFromBoxArt(orion.imageUrl) || appArtFromBoxArt(state.nowIcon);
    try {
      await SetPreset(
        state.currentBox.host, state.currentBox.port,
        slot, oname, decodeProxyUrl(orion.streamUrl), oart,
        state.nowBitrate || 0, '', ''
      );
      showToast(t('preset.savedToKey', { n: slot, name: oname }));
      await loadPresets();
    } catch (err) {
      showPresetSaveError(err, slot);
    }
    return;
  }

  const name = state.nowName || t('preset.placeholderSender');
  // The codec is only known when this stream is the one the app itself
  // started (radio-browser reported it at play time); the box does not
  // report one.
  const appRec = state.lastAppPlay;
  const codec = (appRec && appRec.url === decodeProxyUrl(state.nowLocation)) ? (appRec.codec || '') : '';
  try {
    // state.nowIcon carries the box's <art> tag, which for anything the agent
    // wrapped is its loopback art-proxy URL: unwrap so the store keeps the
    // real image URL, never a URL only the box can fetch (#696, same poison
    // shape as the orion branch above).
    await SetPreset(
      state.currentBox.host, state.currentBox.port,
      slot, name, decodeProxyUrl(state.nowLocation), appArtFromBoxArt(state.nowIcon), state.nowBitrate || 0, '', codec
    );
    showToast(t('preset.savedToKey', { n: slot, name }));
    await loadPresets();
    voteForWhatPlays();
  } catch (err) {
    showPresetSaveError(err, slot);
  }
}
