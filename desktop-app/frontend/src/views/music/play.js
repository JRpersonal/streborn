// views/music/play.js: play(): recalling a preset key on the selected speaker (or
// its group master), the volume re-apply after a wake, and the friendly error
// text.

import { PlaySlot } from '../../api.js';
import { parsePlayRejection, resolveBoxByRef } from '../../groups.js';
import { t } from '../../i18n/index.js';
import { isNativeServicePreset } from '../../nativeservice.js';
import { beginRecall } from '../../spotifynowplaying.js';
import { staleNoticeText } from '../../stalestereo.js';
import { state } from '../../state.js';
import { optimisticQueue, optimisticSpotifyLocation, showToast } from '../../utils.js';
import { escalationForCount, noteNotReady, noteWakeSucceeded } from '../../wakeescalation.js';
import { throttledSetVolume } from '../settings.js';
import { deps } from './deps.js';
import { effectivePlayTarget } from './group.js';
import { scheduleLiveBitrate, scheduleLiveTitle } from './live.js';
import { renderNowPlayingBar } from './nowplaying.js';
import { BOX_LOOPBACK } from './presets.js';
import { renderPresets } from './presettiles.js';
import { refreshStatus } from './status.js';
import { currentPairTarget, musicVolEl, musicVolValEl, pairSetVolume } from './volume.js';

// reapplyDesiredVolume re-sends the user's chosen volume after the box
// has woken from standby to play. Waking from standby resets the box to
// its own stored volume (often 30), which silently discards the level the
// user set while it was idle. We push the desired level again a couple of
// times across the wake window, reflect it in the slider right away, and
// hold off the slider-from-box sync for a few seconds so it cannot snap
// back to 30 in between. No-op until the user has actually set a volume.
function reapplyDesiredVolume() {
  const v = state.desiredVolume;
  const box = state.currentBox;
  if (v == null || !box) return;
  // Reflect the level in the slider right away and hold off the
  // slider-from-box sync. Send the volume early, while the box is still
  // buffering (before audio output), so the stream is not briefly audible
  // at the box's woken default (30). This is safe now that the agent
  // serializes box commands (boxCmdMu): a volume PUT can no longer race the
  // play and waits for it via the mutex instead of colliding. A second PUT
  // a bit later makes sure it sticks if the first landed before the box
  // had fully woken.
  state.musicVolUntil = Date.now() + 5000;
  if (musicVolEl) {
    musicVolEl.value = String(v);
    if (musicVolValEl) musicVolValEl.textContent = String(v);
  }
  // A pair woke as two speakers, so both get the level the pair slider last
  // gave them; one level for both would undo the balance between the halves.
  const pairLevels = state.desiredPairLevels;
  const apply = () => {
    if (state.currentBox !== box) return;
    const target = pairLevels && currentPairTarget();
    if (target && target.key === pairLevels.key) {
      target.halves.forEach(b => {
        if (typeof pairLevels.levels[b.host] === 'number') pairSetVolume(b.host, b.port, pairLevels.levels[b.host]);
      });
      return;
    }
    throttledSetVolume(box.host, box.port, v);
  };
  setTimeout(apply, 250);
  setTimeout(apply, 1500);
}

export async function play(slot) {
  // Was the box idle/standby before this play? Waking resets its volume,
  // so we re-apply the user's chosen level afterwards only in that case
  // (a normal preset switch while already playing keeps the live volume).
  const wasIdle = !state.nowPlayState || state.nowSource === 'STANDBY';
  // A preset recall supersedes any ad-hoc station the app started: drop the
  // record so a later long-press save goes back to trusting the box report.
  state.lastAppPlay = null;
  state.lastLibraryPlay = null;
  const p = state.presets.find(x => x.slot === slot);
  if (p) {
    // Optimistic UI: set BUFFERING_STATE immediately so the user
    // gets feedback. Sticky for 6 s. During that window refreshStatus
    // must not flip the preset back to grey when the speaker still
    // reports the old stream or an empty one.
    state.nowPlayState = 'BUFFERING_STATE';
    // A music-library album or folder key stores no stream URL, so the
    // optimistic location below is empty and nothing would name the key until
    // the agent's queue card arrived and the 6 s window ran out (#978): the key
    // lit about ten seconds after the click although the queue started within
    // a second. Name it through an optimistic queue card instead, and remember
    // what the speaker reported before the click so refreshStatus can end the
    // window as soon as the speaker reports something new.
    if (p.type === 'queue') {
      state.queue = optimisticQueue(state.queue, slot);
      state.optimisticQueueSlot = slot;
      state.optimisticQueuePrevLoc = state.nowLocation || '';
    } else {
      state.optimisticQueueSlot = null;
    }
    // Spotify presets carry no stream_url (they recall by URI), so without
    // this the optimistic location is empty: the tile would not light up and
    // the click feels ignored until the box confirms several seconds later.
    // Point it at the per-slot Spotify stream the box will report, so the
    // highlight and the "starting" label appear instantly on the CLICKED key.
    // The generic stream.ogg carried no slot, which kept the previously
    // playing key lit as "starting" until the optimistic window ran out
    // (#1077); the slot URL also lets the early release in refreshStatus match.
    state.nowLocation = p.type === 'spotify'
      ? optimisticSpotifyLocation(BOX_LOOPBACK, slot)
      : (isNativeServicePreset(p) && p.native ? (p.native.location || '') : (p.stream_url || ''));
    if (p.type === 'spotify') state.nowSpotifySlot = slot;
    state.nowName = p.name || '';
    state.nowIcon = p.art || '';
    state.nowBitrate = p.bitrate || 0;
    state.nowTitle = ''; // clear so the new station does not briefly show the old track
    // Same for a Spotify song: drop it, start a new generation so any
    // /spotify/info reply still in flight is ignored, remember which context
    // the click wants, and ask again at the next status poll instead of up to
    // three seconds later. Until a reply about THIS preset arrives the line
    // shows the preset name alone (#1077, see spotifynowplaying.js).
    state.spotifyRecall = p.type === 'spotify'
      ? beginRecall(p.uri, state.spotifyNow, Date.now())
      : null;
    state.spotifyNow = null;
    state.spotifyNowGen = (state.spotifyNowGen || 0) + 1;
    state.lastSpotifyNowFetch = 0;
    scheduleLiveBitrate();
    scheduleLiveTitle();
    state.nowUUID = '';
    state.optimisticUntil = Date.now() + 6000;
    delete state.presetErrors[slot];
    state.stuckBuf = null;
    renderPresets();
    // Also repaint the now-playing status line from the optimistic state, not
    // just the preset tile: it reads purely from cached state, so without this
    // the "Stream is starting" label only appeared after PlaySlot returned and
    // the next refreshStatus ran, which on a cold soft recall lagged several
    // seconds behind the click. Now the status line tracks the tile instantly.
    renderNowPlayingBar();
  }
  try {
    await PlaySlot(state.currentBox.host, state.currentBox.port, slot);
    // The speaker took the command, so it is awake: any run of wake failures
    // it had is over, and the next one starts counting from the beginning.
    noteWakeSucceeded(state.currentBox.host);
    delete state.presetErrors[slot];
    if (wasIdle) reapplyDesiredVolume();
    refreshStatus();
    setTimeout(refreshStatus, 1500);
  } catch (e0) {
    let e = e0;
    // Newer agents reject a recall sent to a grouped follower with a
    // structured 409 ({"error":"box-grouped","master":...}): retry ONCE on
    // the group master, which distributes the audio to the group. Older
    // agents send a raw SOAP string and take the error path unchanged.
    const rej = parsePlayRejection(e0);
    if (rej.grouped) {
      const mb = resolveBoxByRef(rej.master, state.boxes) || effectivePlayTarget();
      if (mb && state.currentBox && mb.host !== state.currentBox.host) {
        try {
          await PlaySlot(mb.host, mb.port, slot);
          noteWakeSucceeded(mb.host);
          delete state.presetErrors[slot];
          if (wasIdle) reapplyDesiredVolume();
          refreshStatus();
          setTimeout(refreshStatus, 1500);
          return;
        } catch (e2) {
          e = e2; // surface the master's error, not the follower's rejection
        }
      }
    }
    const errStr = String(e);
    state.nowPlayState = '';
    state.nowLocation = '';
    state.optimisticUntil = 0;
    state.presetErrors[slot] = friendlyPlayError(errStr, (state.currentBox && state.currentBox.host) || "");
    renderPresets();
    // The tile label is too small for the multi-step Spotify Connect how-to, so
    // also show it as a (localized) toast. Use the i18n help text, not the raw
    // English backend message, so non-English users get it in their language.
    if (errStr.toLowerCase().includes('spotify-not-logged-in')) {
      showToast(t('play.errSpotifyLoginHelp'));
    }
    // Half of an old stereo pair: the whole sentence, with where to fix it.
    // "Not ready" on every key, for an hour, is what this replaces (2026-10-04).
    const stale = deps.stereoStaleForError(errStr, (state.currentBox && state.currentBox.host) || '');
    if (stale) showToast(staleNoticeText(stale, t));
    setTimeout(() => refreshStatus(), 2000);
  }
}

// friendlyPlayError turns a technical error string into a short
// user-facing hint shown on the preset label.
//
// host is passed so a speaker that keeps refusing to wake gets a DIFFERENT
// sentence on the third attempt than on the first. One sentence for every
// attempt is a dead end: a reporter read the same line over eighty times
// (2026-09-29) and had nowhere to go from it.
function friendlyPlayError(s, host) {
  const l = String(s).toLowerCase();
  // Before box_not_ready: a speaker stuck as half of an old stereo pair is
  // "not ready" forever, and the escalation ladder below would send its owner to
  // pull the plug and mail logs, neither of which helps.
  if (deps.stereoStaleForError(s, host)) return t('play.errStereoStale');
  if (l.includes('box_not_ready')) return t(escalationForCount(noteNotReady(host)));
  // Spotify recall refused because the speaker was never picked as the Spotify
  // Connect device (no go-librespot credential). Key off the stable backend code,
  // not the English message, so rewording the backend never breaks this (#45).
  if (l.includes('spotify-not-logged-in')) return t('play.errSpotifyLogin');
  if (l.includes('premium')) return t('play.errSpotifyPremium');
  if (l.includes('no such host') || l.includes('lookup')) return t('play.errNoInternet');
  if (l.includes('timeout') || l.includes('deadline')) return t('play.errSpeakerTimeout');
  if (l.includes('refused')) return t('play.errSpeakerRefused');
  if (l.includes('402') || l.includes('no uri')) return t('play.errNotPlayable');
  if (l.includes('500')) return t('play.errServer500');
  if (l.includes('konnte nicht') || l.includes('could not')) {
    // Backend returns a localised 'detail' string on UPnP failures.
    return t('play.errUnreachable');
  }
  return t('play.errGeneric');
}
