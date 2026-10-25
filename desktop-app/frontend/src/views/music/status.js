// views/music/status.js: refreshStatus, the status polling core of the Music view
// (now playing, volume sync, presets recheck, queue, the unreachable-speaker
// rediscovery), and the Spotify key gate it needs.

import { SpotifyNowPlaying, Status } from '../../api.js';
import { t } from '../../i18n/index.js';
import { sourceAccountFrom } from '../../nowsourceaccount.js';
import { acceptSpotifyReply, recallSettled, snapshotFromReply } from '../../spotifynowplaying.js';
import { state } from '../../state.js';
import { statusTickScope } from '../../statusrefreshscope.js';
import { renderShellRooms } from '../../shell2/mount.js';
import { nextStuckBuffering } from '../../stuckbuffering.js';
import {
  activeSlotFromLocation,
  appArtFromBoxArt,
  decodeXmlEntities,
  proxiedRadioPlaying,
  queueRecallConfirmed,
  shouldAdoptPresetArt,
  showError,
  showToast,
} from '../../utils.js';
import { deps } from './deps.js';
import { scheduleLiveBitrate, scheduleLiveTitle } from './live.js';
import {
  pollTrackPosition,
  refreshQueue,
  renderNowPlayingBar,
  resetTrackProgress,
  trackPos,
} from './nowplaying.js';
import { refreshPresetsIfChanged } from './presets.js';
import { renderPresets } from './presettiles.js';
import { highlightActiveSource } from './sources.js';
import { syncMusicTabVolumeFromBox } from './volume.js';

// Active-box health: how many consecutive status polls have failed, and when we
// last kicked a rediscovery because of it. See refreshStatus's catch: a box that
// goes unreachable for several polls has almost certainly changed IP under us.
let _statusFailCount = 0;
let _lastUnreachableRediscover = 0;

// spotifyKeyUsableFor answers one question for the built-in-Spotify notice:
// could a saved Spotify key on THIS speaker play anything at all? Only the
// speaker knows, so it is asked, once, and the answer cached per host.
//
// Returns true, false, or undefined while no answer has arrived yet. The caller
// shows the notice only on true, so a speaker that has not answered stays
// silent for a poll rather than repeating an instruction that already went out
// wrong twice (#973).
//
// canRecall absent means the agent predates the field, not "no". Reading it as
// no would hide the notice from everyone who has not updated their speaker.
function spotifyKeyUsableFor(box) {
  if (!box || !box.host) return undefined;
  const cache = state.spotifyKeyUsable || (state.spotifyKeyUsable = {});
  if (box.host in cache) return cache[box.host];
  if (state.spotifyKeyUsablePending === box.host) return undefined;
  state.spotifyKeyUsablePending = box.host;
  SpotifyNowPlaying(box.host, box.port)
    .then(np => {
      state.spotifyKeyUsablePending = '';
      if (!np) return;
      const canRecall = np.canRecall === null || np.canRecall === undefined ? true : !!np.canRecall;
      cache[box.host] = canRecall && !np.premiumRequired;
    })
    .catch(() => {
      state.spotifyKeyUsablePending = '';
    });
  return undefined;
}

export async function refreshStatus() {
  if (!state.currentBox) return;
  // The status bar is polled in EVERY tab. Today it lives in the Music tab
  // (#view-box); with the UI 2.0 preview on it sits in the persistent player
  // and is on screen everywhere. This function used to return immediately
  // outside the box view, so on the Library or Recently-played tab the bar sat
  // frozen. No play state, no track position, and the progress bar hid itself
  // because its last reading never refreshed, which is exactly where single
  // tracks get started from. Reported as "the desktop app does not show a
  // progress bar" while the phone remote did, and the phone has one view and
  // therefore never skipped a tick (#845).
  //
  // Outside the box view only what is on screen is refreshed: the bar, and the
  // volume and queue transport only while the UI 2.0 persistent player shows
  // them. The preset tiles belong to the Music tab alone, and this polls a
  // speaker whose NAND and CPU nobody can replace. The cadence out there stays
  // the slow one; the bar interpolates between readings once a second, so it
  // still moves like a clock.
  const scope = statusTickScope({
    hasBox: true,
    view: state.view,
    persistentPlayer: !!state.ui2Shell,
    otaInProgress: !!state.otaInProgress,
  });
  // Reflect hardware-button volume changes back into the slider.
  // Fired in parallel with the Status fetch so a slow Status call
  // does not delay the volume update. Cheap, drag-aware.
  if (scope.player) syncMusicTabVolumeFromBox();
  // Keep the queue transport controls in step with the box. Fired alongside the
  // Status fetch (not awaited) so it shares the poll cadence without delaying it.
  if (scope.player) refreshQueue();
  // Track position rides the same cadence, not awaited so a slow AVTransport
  // read cannot delay the status poll.
  pollTrackPosition();
  // This app is not the only thing that writes presets: the phone remote saves
  // to the same speaker, and until now the tiles here kept whatever they were
  // loaded with, so a key reassigned from the phone showed the old station
  // until the speaker was reselected.
  if (scope.controls) refreshPresetsIfChanged();
  try {
    const xml = await Status(state.currentBox.host, state.currentBox.port);
    _statusFailCount = 0; // the box answered: it is reachable at its current IP
    // The station name wins when the speaker (or the agent, #1170) reports one:
    // with the track shown on the speaker display, itemName carries the song.
    const name = decodeXmlEntities((xml.match(/<stationName>([^<]+)<\/stationName>/) || [])[1]
      || (xml.match(/<itemName>([^<]+)<\/itemName>/) || [])[1] || '');
    const src = (xml.match(/source="([^"]+)"/) || [])[1] || '';
    state.nowSource = src;
    // A speaker with more than one socket of the same kind (an SA-5 reports
    // three AUX inputs) says which one is playing only in the account, so the
    // input row needs it to light the right button (#274). It is not always in
    // the same place: UPnP puts it on the outer element AND on the ContentItem,
    // AUX only on the ContentItem, so reading the outer one alone found nothing
    // for exactly the speaker that needed it. See nowsourceaccount.js.
    state.nowSourceAccount = sourceAccountFrom(xml);
    // The speaker's OWN Spotify receiver names the song in the same response,
    // and that is not true of every source: on radio <track> merely repeats the
    // station, which is why the song has always had to come from STR's stream
    // proxy instead (#593). Confirmed on firmware 27.0.6 with the cloud gone
    // and a FREE Spotify account: <track>, <artist>, <album> and the cover are
    // all filled while <itemName> carries the playlist. So a phone-started
    // Connect session can show the song with no extra request, and STR's own
    // engine must not be asked about it, because it is not the one playing.
    state.nowBoxSpotify = null;
    if (src === 'SPOTIFY') {
      const bt = decodeXmlEntities((xml.match(/<track>([^<]*)<\/track>/) || [])[1] || '');
      if (bt) {
        state.nowBoxSpotify = {
          track: bt,
          artist: decodeXmlEntities((xml.match(/<artist>([^<]*)<\/artist>/) || [])[1] || ''),
          cover: (xml.match(/<art\b[^>]*>([^<]*)<\/art>/) || [])[1] || '',
        };
      }
    }

    // Piggy-back an SSH status check on the polling we are doing
    // anyway. Toggle the global banner so the user sees it on every
    // tab rather than only after entering the Settings tab.
    deps.checkSshBanner();
    const ps = (xml.match(/<playStatus>([^<]+)<\/playStatus>/) || [])[1] || '';

    // Native Bose Spotify receiver detection: source=SPOTIFY means a phone
    // connected to the speaker's built-in Spotify Connect, not STR's
    // go-librespot (STR's own playback is always source=UPNP via the stream
    // proxy). STR cannot recall a preset on the native receiver, so say so
    // once per episode and reset when the speaker leaves SPOTIFY again.
    //
    // Two conditions, both from the same report (#950). The notice opens with
    // "Playing on the speaker's built-in Spotify" and used to fire on a PAUSED
    // session, because the play state was read further down than the notice.
    // And it tells the user to press a saved Spotify key, which a free account
    // cannot create at all, so it sent a free-account user looking for a button
    // that does not exist. It now needs a speaker that is really playing and a
    // key that really exists, and it is remembered per speaker, not globally.
    // Two conditions were not enough. The reporter HAS a saved Spotify key
    // and his speaker WAS playing, so both gates passed and he still got a
    // notice telling him to press a key he cannot use (#973, on v0.9.83 which
    // already had the first fix). The key existing was never the question;
    // being able to use it is.
    //
    // The third attempt has to ask the SPEAKER, and it has to ask here.
    // state.spotifyPremiumRequired looked like the answer, but it is only ever
    // written while STR's own stream is the thing playing (isSpotifyNow, below),
    // and that is false exactly when this notice fires, because what is playing
    // is the box's OWN receiver. The flag was therefore always undefined at this
    // point. A free account is also only half of it: a speaker that was never
    // picked in Spotify at all cannot use the key either, and reports
    // premiumRequired=false because it has no account to judge.
    const spotifyKeySaved = (state.presets || []).some(p => p && p.type === 'spotify');
    const spotifyWarnKey = (state.currentBox && state.currentBox.host) || '';
    if (src === 'SPOTIFY' && ps === 'PLAY_STATE' && spotifyKeySaved) {
      // Silent until the speaker has answered. One poll's delay on a hint is
      // cheaper than a third wrong instruction to the same person.
      if (state.nativeSpotifyWarned !== spotifyWarnKey && spotifyKeyUsableFor(state.currentBox) === true) {
        state.nativeSpotifyWarned = spotifyWarnKey;
        showToast(t('play.nativeSpotifyHint'));
      }
    } else if (src !== 'SPOTIFY') {
      state.nativeSpotifyWarned = '';
      // Forget the verdict too: the user may go and tap the speaker in Spotify
      // precisely because the key did not work, and the app must notice.
      state.spotifyKeyUsable = {};
    }
    const loc = decodeXmlEntities((xml.match(/location="([^"]+)"/) || [])[1] || '');
    // Extract the art URL from the <art ...>URL</art> tag. Bose
    // emits it for stations with an image (for example after a
    // radio-search play). Without this refresh, state.nowIcon would
    // stay stuck from the previous soft click ("logo of the
    // previous station" bug).
    const artRaw = decodeXmlEntities((xml.match(/<art[^>]*>([^<]+)<\/art>/) || [])[1] || '');

    // Optimistic guard: after a user preset click we set nowLocation
    // straight to the desired stream. Until optimisticUntil expires,
    // refreshStatus must NOT overwrite location/name. Otherwise the
    // button flickers grey between click and the actual stream
    // start. Once the speaker confirms our location, release the
    // optimistic guard.
    const optimistic = Date.now() < (state.optimisticUntil || 0);
    if (optimistic && loc && loc === state.nowLocation) {
      state.optimisticUntil = 0;
    }
    // A library album or folder key has no location to compare: the recall is
    // confirmed once the queue names the clicked key and the speaker reports a
    // location other than the one it had before the click (#978).
    if (optimistic && queueRecallConfirmed(state.optimisticQueueSlot, state.queue, loc, state.optimisticQueuePrevLoc)) {
      state.optimisticUntil = 0;
      state.optimisticQueueSlot = null;
    }
    const newLoc = optimistic ? state.nowLocation : loc;
    const prevLoc = state.nowLocation;
    const newName = optimistic ? state.nowName : name;
    // The preset grid reads the play state and the location, never the track
    // name (a key shows what is SAVED on it). Rebuilding all six keys for a name
    // change re-created their markup mid-song, which made the window jump while
    // the speaker briefly reported a different name for the same track (#1190).
    const gridChanged = state.nowPlayState !== ps || state.nowLocation !== newLoc;
    // A different track means the progress must start over; anything else keeps
    // its reading so the bar does not stutter on an unrelated status change.
    const trackKey = newLoc + '|' + newName;
    if (trackKey !== trackPos.key) resetTrackProgress(trackKey);
    else if ((ps === 'PLAY_STATE' || ps === 'BUFFERING_STATE') && trackPos.at === 0) {
      // Playing again with no reading yet (the same track resumed after a
      // stop): open the fast-poll window so the bar does not wait a full
      // status interval either.
      trackPos.resetAt = Date.now();
    }
    // #810: the Recently-played view now refreshes now-playing itself (recent.js
    // syncCurrentNowPlaying), because refreshStatus returns early on any non-box
    // view, so an "if (state.view === 'recent')" branch here was unreachable.
    state.nowPlayState = ps;
    state.nowLocation = newLoc;
    state.nowName = newName;
    // Live Spotify track metadata for the now-playing line: poll the agent's
    // /spotify/info (throttled) while a Spotify stream is active so the desktop
    // shows the current song + artist, not just the playlist/preset name.
    // A container location is also what the speaker's own receiver reports, and
    // for that one the song came out of the status XML above. Asking STR's
    // engine then answers about a session it is not serving and blanks the line.
    const isSpotifyNow = /\/spotify\/stream|\/playback\/container/.test(newLoc) && !state.nowBoxSpotify;
    if (isSpotifyNow) {
      const npBox = state.currentBox;
      if (npBox && Date.now() - (state.lastSpotifyNowFetch || 0) > 3000) {
        state.lastSpotifyNowFetch = Date.now();
        const sentGen = state.spotifyNowGen || 0;
        SpotifyNowPlaying(npBox.host, npBox.port).then(np => {
          if (!np) return;
          // Only a reply about the preset clicked last may paint the line, and
          // only as a whole (#1077). A rejected reply leaves the previous
          // snapshot (none, right after a click) in place and the next status
          // poll asks again instead of waiting out the throttle. The account
          // and entitlement fields below are not about the song and are read
          // either way.
          let coverChanged = false;
          let contextChanged = false;
          if (acceptSpotifyReply(np, {
            sentGen, currentGen: state.spotifyNowGen || 0, recall: state.spotifyRecall, now: Date.now(),
          })) {
            const snap = snapshotFromReply(np);
            coverChanged = (state.spotifyNow ? state.spotifyNow.cover : '') !== snap.cover;
            // The context names the key to light when no key started the
            // playback (#1217): repaint the keys when it changes too.
            contextChanged = (state.spotifyNow ? state.spotifyNow.context : '') !== snap.context;
            state.spotifyNow = snap;
            if (recallSettled(snap)) state.spotifyRecall = null;
            renderNowPlayingBar();
          } else if (sentGen === (state.spotifyNowGen || 0)) {
            state.lastSpotifyNowFetch = 0;
          }
          // What the speaker has learned each account is CALLED, which is what
          // a preset tile and a Recently-played card draw. The account id never
          // goes on screen in any form, so an account with no remembered name
          // simply shows no line. Merged rather than replaced: two speakers can
          // each have met a different household account, and a payload without
          // the field (an older agent) must not wipe what another one told us.
          if (np.accountNames && typeof np.accountNames === 'object') {
            state.spotifyAccountNames = { ...(state.spotifyAccountNames || {}), ...np.accountNames };
          }
          // A free account cannot start a playlist from a preset key, so any
          // message that tells the user to press one is wrong for them (#973).
          state.spotifyPremiumRequired = !!np.premiumRequired;
          // Spotify refused the audio key for track after track. Without this
          // the playlist just races past in silence and the speaker looks
          // broken, when in fact Spotify is refusing this engine for this
          // account. Said once per episode: the condition clears itself on the
          // speaker after ten quiet minutes, and repeating it every three
          // seconds would be worse than the silence.
          if (np.audioKeyRefused) {
            if (!state.spotifyKeyRefusalShown) {
              state.spotifyKeyRefusalShown = true;
              showError(t('spotify.audioKeyRefused'));
            }
          } else {
            state.spotifyKeyRefusalShown = false;
          }
          // The now-playing line redraws every status poll, but the tile cover
          // only redraws on renderPresets. Re-render when the cover changes so
          // the preset logo tracks the song in step with the title instead of
          // lagging a track behind.
          if (coverChanged || contextChanged) renderPresets();
        }).catch(() => {});
      }
    } else {
      state.spotifyNow = null;
    }
    // The tile adopts state.nowIcon (and persists it) only while the active
    // preset still has no stored art. That render used to come for free from
    // the 12 s live-title rebuild; since the key stopped showing the running
    // title (2026-08-23) nothing repaints the grid between two status changes,
    // so a speaker that reports an empty <art> on the poll which first shows
    // the new location and fills it in a poll later would leave the key on its
    // fallback favicon for the rest of the song, with the real logo never
    // saved. False again the moment the art is stored, so this renders once
    // per station, not once per poll.
    let iconAdoptable = false;
    // Update state.nowIcon. Prefer the art tag from now_playing.
    // If that is empty AND we are playing through the stream proxy,
    // adopt the logo of the source preset. Bose UPnP items emitted
    // by hardware key presses carry no art tag, so we need this
    // fallback.
    if (!optimistic) {
      const slotFromProxy = activeSlotFromLocation(newLoc);
      const ap = slotFromProxy !== null ? state.presets.find(p => p.slot === slotFromProxy) : null;
      if (artRaw) {
        state.nowIcon = artRaw;
      } else if (ap) {
        state.nowIcon = ap.art || '';
      } else if (!newLoc) {
        state.nowIcon = '';
      }
      // Mirror of the grid's adopt gate in renderPresets: judge the UNWRAPPED
      // icon, because for a native play the box's <art> is the agent's own
      // loopback art-proxy URL and can leave nothing adoptable once unwrapped
      // (#696). Judging the raw value here while the grid persists the
      // unwrapped one would re-render for an adopt that never happens.
      iconAdoptable = shouldAdoptPresetArt(appArtFromBoxArt(state.nowIcon), ap);
      // Keep the now-playing bitrate in sync with the active preset
      // (hardware key press or app restart did not go through the play
      // path that sets it). Cleared when nothing is playing.
      if (/\/spotify\/stream/.test(newLoc)) {
        // Spotify stream: never inherit the previous radio station's bitrate.
        // Use the matching Spotify preset's stored (measured) rate, or 0 so the
        // live fetch below recomputes it from the actual stream.
        // A rate the live fetch already measured for this same stream stays:
        // resetting it on every poll left "- kbit/s" in the bar most of the
        // time (#1077, Issue 17).
        const sp = state.presets.find(p => p.type === 'spotify' && p.name === newName);
        if (!(prevLoc === newLoc && state.nowBitrate > 0)) {
          state.nowBitrate = (sp && sp.bitrate) ? sp.bitrate : 0;
        }
      } else if (ap && ap.bitrate) {
        state.nowBitrate = ap.bitrate;
      } else if (!newLoc ||
                 (!/\/stream\//.test(newLoc) && !/\/spotify\/stream/.test(newLoc))) {
        // Nothing playing, or a direct library file: a library track is played
        // straight to the box (not through the stream proxy), so there is no
        // measured live bitrate. Clear it instead of leaving a previous radio
        // station's value behind (#310).
        state.nowBitrate = 0;
      }
      // Still playing through the stream proxy but with no bitrate yet
      // (app restart, hardware key press, or a preset whose stored bitrate
      // is 0): kick the live fetch. It writes state.nowBitrate once the
      // agent has measured and then self-stops, so this does not re-trigger
      // on every poll once a value is in.
      if (!state.nowBitrate &&
          (ps === 'PLAY_STATE' || ps === 'BUFFERING_STATE') &&
          (activeSlotFromLocation(newLoc) !== null || /\/spotify\/stream/.test(newLoc))) {
        scheduleLiveBitrate();
      }
      // Keep the live radio track flowing into the now-playing bar for playback
      // STR did not itself start (hardware key, app restart). Self-guarded, so
      // calling it on every poll is safe; it no-ops while already polling.
      // Any proxied radio stream, not only a preset slot: a station started
      // from Find stations plays as /stream/raw and has no slot (#1190).
      if ((ps === 'PLAY_STATE' || ps === 'BUFFERING_STATE') &&
          proxiedRadioPlaying(newLoc)) {
        scheduleLiveTitle();
      }
    }

    // A key that stays BUFFERING for 30 s without ever playing gets an error
    // instead of "stream starting ..." forever (#1077: a stereo pair whose
    // follower could not decode the first frame left the lead speaker
    // buffering for good). The agent gives such a Spotify key one clean
    // retry; a PLAY_STATE below clears this error again.
    {
      const box = state.currentBox;
      const slot = state.activePresetSlot;
      const key = box && slot != null ? `${box.host}#${slot}` : null;
      const r = nextStuckBuffering(state.stuckBuf || null, key, ps, Date.now());
      state.stuckBuf = r.tracker;
      if (r.stuck && !state.presetErrors[slot]) {
        state.presetErrors[slot] = t('preset.errNoStart');
        renderPresets();
      }
    }

    // If the speaker is now playing successfully, clear the preset
    // error. The speaker's ContentItems run through the stream
    // proxy, so accept the slot match from /stream/<slot> too.
    if (ps === 'PLAY_STATE') {
      // Any successful playback is a valid "your box is alive again" moment, not
      // just internet radio. The old filter (proxy /stream/ and not /spotify/)
      // missed every Spotify, media-library, AUX/Bluetooth and hardware-button
      // user, which is why so many never saw the pin invite. maybeInviteWorldMap
      // is once-ever-guarded (synchronous flag + durable Go-side flag), so
      // broadening the trigger only widens reach, it cannot double-invite.
      deps.maybeInviteWorldMap();
      const slotFromProxy = activeSlotFromLocation(loc);
      const ap = state.presets.find(p =>
        p.stream_url === loc || (slotFromProxy !== null && p.slot === slotFromProxy)
      );
      if (ap && state.presetErrors[ap.slot]) {
        delete state.presetErrors[ap.slot];
      }
      // Spotify presets carry no stream_url and the location (/spotify/stream)
      // has no slot, so the match above never fires. When a Spotify stream is
      // confirmed playing, clear ALL Spotify preset errors so a stale
      // "speaker still starting" no longer sticks on the tile.
      if (/\/spotify\/stream/.test(loc)) {
        for (const p of state.presets) {
          if (p.type === 'spotify') delete state.presetErrors[p.slot];
        }
      }
    }

    if ((gridChanged || iconAdoptable) && state.presets.length > 0) {
      renderPresets();
    }

    // Now-playing status line. Rendered from cached state so the live-title
    // poller can refresh it the instant a track arrives, not only on the next
    // status poll. It is the only place the running title is shown.
    renderNowPlayingBar();
    // The UI 2.0 room switcher marks the controlled speaker playing or in
    // standby from this same reading (a no-op with the preview off).
    renderShellRooms();

    highlightActiveSource();
  } catch {
    // Transient status-fetch failure (a single poll timing out while the
    // box is briefly busy, e.g. BoseApp's :8090 under load). Keep the last
    // known now-playing on screen instead of blanking it to a dash, which
    // looked like the display flickering to "---" and back even though
    // nothing actually changed. The next successful poll refreshes it.
    _statusFailCount++;
    // Several consecutive failures mean the active box is genuinely unreachable,
    // most likely because its IP changed under us (a router restart re-leased the
    // whole LAN, or a LAN<->Wi-Fi / band switch). Kick a full rediscovery so the
    // /24 sweep finds it at its new address and applyBoxList re-binds it by
    // deviceID. Debounced so a box that is simply switched off does not sweep the
    // LAN every few seconds.
    if (_statusFailCount >= 4 && Date.now() - _lastUnreachableRediscover > 20000) {
      _lastUnreachableRediscover = Date.now();
      _statusFailCount = 0;
      deps.discoverBoxes();
    }
  }
}
