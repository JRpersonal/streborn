// views/music/live.js: the live bitrate and live title polls that follow a play.

import { SpotifyBitrate, StreamBitrate, StreamTitleInfo } from '../../api.js';
import { state } from '../../state.js';
import {
  activeSlotFromLocation,
  nativeSlotStale,
  orionStationPayload,
  proxiedRadioPlaying,
} from '../../utils.js';
import { deps } from './deps.js';
import { renderNowPlayingBar } from './nowplaying.js';

// scheduleLiveBitrate fetches the agent's detected stream bitrate after a
// station starts and reflects it into now-playing + the active preset tile.
// Two delayed reads, not a timer: the first catches an icy-br value (known
// instantly), the second catches the throughput estimate, which the agent
// only has after it has skipped the buffer-fill and measured ~6 s of
// steady-state playback (~10 s in). Routed through the StreamBitrate Go
// binding so it self-heals across :8888 / :17008 like every other box call
// (a raw fetch pinned to box.port silently failed on BCO speakers).
let liveBitrateTimer = null;
export function scheduleLiveBitrate() {
  if (liveBitrateTimer) { clearTimeout(liveBitrateTimer); liveBitrateTimer = null; }
  const box = state.currentBox;
  if (!box) return;
  // The agent only has a throughput bitrate ~10 s after the stream itself
  // starts, and the stream start lags the play click by a few seconds
  // (wake + UPnP). A fixed delay therefore races the measurement and can
  // read 0 forever. So retry every 4 s until a value appears, then stop;
  // bounded to ~32 s so it never polls indefinitely. icy-br stations
  // resolve on the first attempt. Routed through the StreamBitrate Go
  // binding so it self-heals across :8888 / :17008.
  let tries = 0;
  const attempt = async () => {
    liveBitrateTimer = null;
    if (state.currentBox !== box) return;
    tries++;
    // Spotify streams report their measured rate through a separate agent
    // endpoint and carry no slot in the location, so resolve the preset by
    // name (the now-playing title is the preset name).
    const isSpotify = /\/spotify\/stream/.test(state.nowLocation || '');
    let br = 0;
    try {
      br = ((isSpotify ? await SpotifyBitrate(box.host, box.port)
                       : await StreamBitrate(box.host, box.port)) | 0);
    } catch {}
    if (br > 0) {
      if (br !== state.nowBitrate) {
        state.nowBitrate = br;
        // status bar repaints on its own 1 s tick; setting nowBitrate is enough.
        // Correct the active preset's stored bitrate to the real value
        // (radio-browser's catalogue number is often missing or wrong; a
        // Spotify preset never had one).
        const p = isSpotify
          ? state.presets.find(x => x.type === 'spotify' && x.name === state.nowName)
          : (() => { const s = activeSlotFromLocation(state.nowLocation); return s !== null ? state.presets.find(x => x.slot === s) : null; })();
        // The slot lookup goes by NUMBER, so after a sync replaced the slot's
        // station the rate measured from the still-playing OLD stream must not
        // be stamped onto the NEW station (#758: a Jazz-stream reading written
        // onto the freshly-saved key). Same identity check the tile uses.
        const orionNow = orionStationPayload(state.nowLocation);
        const stale = !isSpotify && p && orionNow && nativeSlotStale({
          presetName: p.name,
          presetUrl: p.stream_url,
          playingName: typeof orionNow.name === 'string' ? orionNow.name : '',
          playingUrl: orionNow.streamUrl ? deps.decodeProxyUrl(orionNow.streamUrl) : '',
        });
        if (p && !stale && p.bitrate !== br && deps.isMeasurableStreamPreset(p)) {
          p.bitrate = br;
          // Persist for radio only. SetPreset is radio-only (type=radio, no
          // uri), so persisting a Spotify preset would wipe its URI. The
          // Spotify rate stays live via state.nowBitrate + /spotify/info.
          // Guarded: the write is skipped when the slot changed on the box
          // since this list was loaded (#758).
          if (!isSpotify) {
            deps.setPresetIfUnchanged(box, p, { bitrate: br });
          }
        }
        deps.renderPresets();
      }
      return; // got it
    }
    if (tries < 11) liveBitrateTimer = setTimeout(attempt, 3000);
  };
  // First attempt soon: a station measured earlier this session is cached
  // agent-side and answers instantly. Fresh stations return 0 here and the
  // retries (every 3 s, ~33 s total) pick up the value once the agent's
  // ~10 s throughput window completes.
  liveBitrateTimer = setTimeout(attempt, 2000);
}

// scheduleLiveTitle polls the agent's live ICY StreamTitle for the radio
// station currently playing and reflects it into the now-playing bar above the
// preset grid. Unlike the bitrate (stable, read once) the title changes
// per song, so this re-polls every 12 s while a proxied radio stream is the
// active source. It stops when the speaker changes or playback stops; Spotify
// is skipped (it shows its own track via /spotify/info).
// liveTitleActive guards a single running poll loop: scheduleLiveTitle may be
// called from the play handler AND from every refreshStatus tick, so without
// this each call would reset the timer and it would never fire. The loop
// clears the flag when it stops (speaker change or playback stop), so the next
// play restarts it.
let liveTitleActive = false;
export function scheduleLiveTitle() {
  if (liveTitleActive) return;
  const box = state.currentBox;
  if (!box) return;
  liveTitleActive = true;
  const tick = async () => {
    if (state.currentBox !== box) { liveTitleActive = false; return; }   // speaker changed
    const loc = state.nowLocation || '';
    if (loc === '') { liveTitleActive = false; return; }                 // playback stopped
    // Anything the stream proxy is serving, which is where a live title comes
    // from. This used to ask for a preset SLOT instead, which is a narrower
    // question: a station started from Find stations has no slot, so the poll
    // never ran and the Play tab showed no artist or track while the speaker's
    // own page showed both (#922). The predicate still covers the ORION
    // descriptor a native preset carries (#597/#555) and still says no to a
    // native station pointing straight at a CDN, where there is no title to get.
    const isRadio = proxiedRadioPlaying(loc);
    if (isRadio) {
      let title = '';
      let swapped = false;
      try {
        const info = await StreamTitleInfo(box.host, box.port);
        title = (info && info.title) || '';
        swapped = !!(info && info.swapped);
      } catch {}
      if (state.currentBox !== box) { liveTitleActive = false; return; }
      if (title !== state.nowTitle || swapped !== !!state.nowTitleSwapped) {
        state.nowTitle = title;
        state.nowTitleSwapped = swapped;
        // Only the status line. The preset grid is NOT rebuilt here any more:
        // since the keys stopped showing the live title (2026-08-23) a rebuild
        // would re-create six <img class="preset-logo"> every 12 s for a value
        // no tile displays. The tile's own inputs have their own triggers: the
        // highlight from gridChanged in refreshStatus, the bitrate from
        // scheduleLiveBitrate.
        renderNowPlayingBar();
      }
    }
    // Poll fast (every 2 s) while there is no title yet, so a just-started
    // station or a fresh preset switch shows its track within a couple of
    // seconds instead of waiting a full cycle; relax to 12 s once a title is
    // showing. An empty title (station between songs / no metadata) keeps the
    // fast cadence so it re-acquires quickly.
    setTimeout(tick, state.nowTitle ? 12000 : 2000);
  };
  // First read soon: the station emits its first metadata block a moment after
  // the stream starts.
  setTimeout(tick, 1200);
}
