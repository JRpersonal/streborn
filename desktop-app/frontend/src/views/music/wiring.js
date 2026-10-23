// views/music/wiring.js: the static transport handlers of the Music view (refresh,
// pause/stop, track and queue buttons, the title-swap toggle, the stream
// source fold). Attached once by mountWiring.

import { QueueNext, QueuePrev, QueueRepeat, QueueShuffle, SetStreamTitleSwap } from '../../api.js';
import { state } from '../../state.js';
import { $, clearNoticeDismissal, showError } from '../../utils.js';
import { renderStreamSource } from '../streamsource.js';
import { deps } from './deps.js';

// stoppedOrPaused reports whether the transport should offer Play. INVALID_SOURCE
// is deliberately excluded: nothing is loaded there, so Play would promise
// something that cannot happen.
export function stoppedOrPaused(ps) { return ps === 'PAUSE_STATE' || ps === 'STOP_STATE'; }

// mountWiring attaches the handlers that used to be wired at module scope in
// main.js, once #view-box exists (called from mountMusicView).
export function mountWiring() {
  $('streamSourceDetails').addEventListener('toggle', () => {
    if ($('streamSourceDetails').open) renderStreamSource();
  });

  // The refresh button also re-asks whether a newer app exists, and forgets any
  // earlier dismissal while doing so. Until now the app version was only checked
  // 8 s after start and then every 12 hours, so a user who had put the notice
  // away, or who moved back to an older build, had no way to be offered the
  // update again and pressing refresh appeared to do nothing at all (discussion
  // #597: "How do I force it to check for updates?"). Somebody pressing refresh
  // is asking, so a stored "not now" must not answer for them.
  $('refreshBtn').onclick = () => {
    try { clearNoticeDismissal('appUpdate'); deps.checkAppUpdate(); } catch {}
    deps.discoverBoxes();
  };

  // A STOPPED stream resumes too. Only a paused one counted, so after Stop the
  // button still read Pause and sent pause to a transport that had already
  // stopped: nothing happened and there was no way back to the music. Same fault,
  // same fix as the phone remote.
  $('pauseBtn').onclick = () => deps.action(stoppedOrPaused(state.nowPlayState) ? 'resume' : 'pause');
  // "Artist and title are the wrong way round for this station": the agent keeps
  // the flag per station and answers with the corrected live title, so the line
  // above flips at once instead of with the next song.
  $('titleSwapToggle').onchange = async (e) => {
    const box = state.currentBox;
    if (!box) return;
    const want = !!e.target.checked;
    try {
      const title = await SetStreamTitleSwap(box.host, box.port, want);
      if (state.currentBox !== box) return;
      state.nowTitleSwapped = want;
      if (title) state.nowTitle = title;
    } catch (err) {
      e.target.checked = !want;
      showError(err);
    }
    deps.renderNowPlayingBar();
  };

  $('stopBtn').onclick = () => deps.action('stop');
  // Track skip for a Spotify playlist (the DLNA folder queue has its own controls
  // in #queueControls). The agent's /api/next//api/prev are source-aware.
  $('trackNextBtn').onclick = () => deps.action('next');
  $('trackPrevBtn').onclick = () => deps.action('prev');

  // Queue transport controls (DLNA folder play queue). Each fires its action and
  // then a quick GetQueue refresh so the indicator and toggle states catch up
  // before the next status poll.
  $('queuePrevBtn').onclick = () => deps.queueAction(QueuePrev);
  $('queueNextBtn').onclick = () => deps.queueAction(QueueNext);
  $('queueShuffleBtn').onclick = () => {
    const on = !(state.queue && state.queue.shuffle);
    deps.queueAction((h, p) => QueueShuffle(h, p, on));
  };
  $('queueRepeatBtn').onclick = () => {
    const cur = (state.queue && state.queue.repeat) || 'off';
    const nextMode = cur === 'off' ? 'all' : cur === 'all' ? 'one' : 'off';
    deps.queueAction((h, p) => QueueRepeat(h, p, nextMode));
  };
}
