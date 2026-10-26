// views/music/nowplaying.js: the transport actions, the DLNA queue controls, the
// now-playing bar, the balance line and the track progress bar.

import { GetQueue, Next, Pause, Prev, readBoxBalance, Resume, Stop, TrackPosition } from '../../api.js';
import { balanceSourceBox, inStereoPair, stereoPairsOf } from '../../groups.js';
import { t } from '../../i18n/index.js';
import { localIsAuxSocket } from '../../sourceinputs.js';
import { spotifySongText } from '../../spotifynowplaying.js';
import { state } from '../../state.js';
import { nowStateGlyph, nowStateLabel } from '../../stoppedstate.js';
import { titleSwapOffered } from '../../titleswap.js';
import { applyPositionReading, resetProgress } from '../../trackprogress.js';
import {
  $,
  balanceLabel,
  escapeHtml,
  libraryTrackMeta,
  presetRevChanged,
  proxiedRadioPlaying,
  queueReplyMerge,
  queueSlotCard,
  queueTrackMeta,
  showError,
  spotifyURIFromContainer,
} from '../../utils.js';
import { decodeProxyUrl, refreshPresetsIfChanged } from './presets.js';
import { applyTrackScroll, renderPresets } from './presettiles.js';
import { sourceListCache } from './sources.js';
import { mstate } from './state.js';
import { refreshStatus } from './status.js';
import { stoppedOrPaused } from './wiring.js';

export async function action(kind) {
  if (!state.currentBox) return;
  const fn = kind === 'pause' ? Pause
    : kind === 'resume' ? Resume
      : kind === 'next' ? Next
        : kind === 'prev' ? Prev
          : Stop;
  try { await fn(state.currentBox.host, state.currentBox.port); } catch (e) { showError(e); }
  // Skip lands on a new track fast; poll a touch sooner so the title updates.
  setTimeout(refreshStatus, (kind === 'next' || kind === 'prev') ? 600 : 1000);
}

// queueAction runs a queue binding for the current box, then refreshes the
// status (which pulls a fresh GetQueue) so the transport controls reflect the
// new position / toggle state promptly.
export async function queueAction(fn) {
  if (!state.currentBox) return;
  try { await fn(state.currentBox.host, state.currentBox.port); } catch (e) { showError(e); }
  setTimeout(refreshStatus, 600);
}

// queueKeyFields joins the queue fields that pick the lit key besides the card,
// so refreshQueue can tell when they changed.
function queueKeyFields(q) {
  return q ? [q.slot, q.card_slot, q.slot_checked].join('|') : '';
}

// refreshQueue pulls the box's current queue state and folds it into state.queue,
// then repaints the transport controls. Called from the status poll so it shares
// the existing cadence. Best-effort: a failed read just leaves the controls as is.
export async function refreshQueue() {
  const box = state.currentBox;
  if (!box) { state.queue = null; renderQueueControls(); return; }
  try {
    const q = await GetQueue(box.host, box.port);
    if (state.currentBox !== box) return; // box switched mid-fetch
    const before = queueSlotCard(state.queue);
    const beforeKeys = queueKeyFields(state.queue);
    // A reply that does not name the key just clicked yet (sent before the
    // agent started the queue) must not wipe the optimistic card (#978).
    const pending = Date.now() < (state.optimisticUntil || 0) ? state.optimisticQueueSlot : null;
    state.queue = queueReplyMerge(state.queue, q, pending);
    // A library-album key lights up from the queue card, which arrives here and
    // not with the status poll: repaint the keys when it changes, or the
    // highlight waited for an unrelated status change (#1190). Same for the
    // key a playing folder was just saved on (#1217), and for the agent's
    // checked card key: when a key was given another folder only card_slot
    // changes, card and slot stay as they were (#1525).
    const keysChanged = queueKeyFields(state.queue) !== beforeKeys;
    if ((queueSlotCard(state.queue) !== before || keysChanged) && state.presets.length > 0) renderPresets();
    // The agent's preset store was written (a save on this app, the phone, or
    // a hold on the speaker's own key): re-read the presets now instead of on
    // the slow recheck timer, which kept a held key unshown for up to 15 s
    // (#1217). The revision rides this reply, so it costs no request of its own.
    const rev = q ? q.preset_rev : undefined;
    if (presetRevChanged(state.presetRev, rev)) {
      mstate._lastPresetCheck = 0;
      refreshPresetsIfChanged();
    }
    if (typeof rev === 'number') state.presetRev = rev;
  } catch {
    // leave the last known queue state on screen
  }
  renderQueueControls();
}

// renderQueueControls shows/hides the queue transport controls and reflects the
// shuffle/repeat/position state. Purely DOM-driven from state.queue.
function renderQueueControls() {
  const wrap = $('queueControls');
  if (!wrap) return;
  const q = state.queue;
  const active = !!(q && q.active);
  wrap.classList.toggle('hidden', !active);
  if (!active) return;
  const shuffleBtn = $('queueShuffleBtn');
  if (shuffleBtn) shuffleBtn.classList.toggle('active', !!q.shuffle);
  const repeatBtn = $('queueRepeatBtn');
  if (repeatBtn) {
    const mode = q.repeat || 'off';
    repeatBtn.classList.toggle('active', mode !== 'off');
    // "Repeat one" gets a small superscript 1 so the two repeat modes are
    // distinguishable at a glance.
    repeatBtn.innerHTML = mode === 'one' ? '&#128257;¹' : '&#128257;';
  }
  const pos = $('queuePos');
  if (pos) {
    const items = q.items || [];
    const n = (typeof q.pos === 'number' && q.pos >= 0) ? q.pos + 1 : 0;
    // The running song's artist and album, as its Library row named them
    // (#1033); the title itself is already on the now-playing line.
    const meta = queueTrackMeta(q);
    const of = (n > 0 && items.length > 0) ? t('queue.trackOf', { n, total: items.length }) : '';
    pos.textContent = [of, meta].filter(Boolean).join(' · ');
  }
}

// resetNowPlaying drops the cached now-playing line so switching speakers (in
// particular tapping a different box / a group member) starts from a neutral
// placeholder instead of showing the previously selected box's track until the
// next status poll lands. Without this the marquee kept the previous speaker's
// station + track for 5-10 s after a switch, and in a group it stuck on the
// member's pre-group selection (#207).
export function resetNowPlaying() {
  state.nowLocation = '';
  state.nowName = '';
  state.nowTitle = '';
  state.nowSource = '';
  state.nowSourceAccount = '';
  state.nowPlayState = '';
  state.nowIcon = '';
  state.nowBitrate = 0;
  state.spotifyNow = null;
  state.spotifyRecall = null;
  state.spotifyNowGen = (state.spotifyNowGen || 0) + 1;
  state.optimisticUntil = 0;
  state.lastStatusHTML = '';
}

// renderNowPlayingBar paints the now-playing status line purely from cached
// state (no network), so it can be called both from the status poll and from
// the live-title poller the moment a track arrives, instead of the track only
// appearing on the next poll. Since 2026-08-23 this bar is the only place the
// running title is shown. Guarded on the rendered HTML so it does not restart
// the marquee animation when nothing changed.
// spotifyContextLabelKey names what is playing by what the URI says it is. The
// line used to say "Playlist" for everything, so an album played from the Spotify
// app was announced as a playlist (#709). Anything unrecognised keeps the old
// word rather than inventing one.
function spotifyContextLabelKey(uri) {
  const kind = String(uri || '').split(':')[1] || '';
  if (kind === 'album') return 'status.albumLabel';
  if (kind === 'artist') return 'status.artistLabel';
  if (kind === 'show' || kind === 'episode') return 'status.podcastLabel';
  return 'status.playlistLabel';
}

export function renderNowPlayingBar() {
  const bar = $('statusBar');
  if (!bar) return;
  const ps = state.nowPlayState || '';
  const src = state.nowSource || '';
  const loc = state.nowLocation || '';
  const name = state.nowName || '';
  // Transport button doubles as Play/Pause: when the box is paused it offers
  // Play (resume from the paused position, like the Bose remote), otherwise
  // Pause (#202).
  const ppBtn = $('pauseBtn');
  if (ppBtn) {
    ppBtn.innerHTML = stoppedOrPaused(ps)
      ? '&#9205; ' + escapeHtml(t('controls.play'))
      : '&#9208; ' + escapeHtml(t('controls.pause'));
  }
  // Track skip/previous: only for a Spotify playlist (the DLNA folder queue has
  // its own prev/next in #queueControls). The buttons flank Play/Pause and hide
  // for radio and single sources, which have nothing to skip.
  const isSpotify = /\/spotify\/stream/.test(loc);
  const nextBtn = $('trackNextBtn');
  const prevBtn = $('trackPrevBtn');
  if (nextBtn) nextBtn.classList.toggle('hidden', !isSpotify);
  if (prevBtn) prevBtn.classList.toggle('hidden', !isSpotify);
  let displayName = name;
  // Either Spotify: the one STR serves through its own proxy, or the one the
  // speaker serves itself after a phone picked it in Connect. Same line, two
  // sources for the song.
  // STR's own Spotify is drawn from ONE snapshot of one /spotify/info reply, so
  // song, artist and context always describe the same thing (#1077).
  const snap = state.spotifyNow;
  const song = state.nowBoxSpotify
    ? spotifySongText(state.nowBoxSpotify)
    : (/\/spotify\/stream/.test(loc) ? spotifySongText(snap) : '');
  if (song) {
    const spotifyCtx = snap ? snap.context : '';
    displayName = name ? `${t(spotifyContextLabelKey(spotifyCtx || spotifyURIFromContainer(loc)))}: "${name}" · ${song}` : song;
  } else if (proxiedRadioPlaying(loc) && state.nowTitle) {
    // The same predicate as the title poller above, and it has to be the same
    // one: fixing only the poll would fetch a title that this line then refused
    // to print (#922).
    displayName = name ? `${t('status.stationLabel')}: "${name}" · ${state.nowTitle}` : state.nowTitle;
  } else {
    // A single library track: its artist and album, as the Library row named
    // them, next to the title the speaker reports, the way the queue line
    // names a folder's running song (#1394).
    const libMeta = libraryTrackMeta(state.lastLibraryPlay, loc, decodeProxyUrl(loc));
    if (libMeta) displayName = name ? `${name} · ${libMeta}` : libMeta;
  }
  // The per-station "artist and title are the wrong way round" switch, only
  // while a proxied station plays a two-part title (titleswap.js).
  const swapRow = $('titleSwapRow');
  if (swapRow) {
    const offered = !song && titleSwapOffered(proxiedRadioPlaying(loc), state.nowTitle);
    swapRow.classList.toggle('hidden', !offered);
    const box = $('titleSwapToggle');
    if (box && offered) box.checked = !!state.nowTitleSwapped;
  }
  // Match the source case-insensitively: the firmware is not consistent about
  // casing across models, and AirPlay in particular can read as AIRPLAY,
  // AirPlay2, etc. depending on the speaker (#122).
  const srcU = src.toUpperCase();
  const isAirplay = srcU.includes('AIRPLAY');
  // An awake speaker on a selected station in STOP_STATE is "stopped", not a
  // label-less line that read as paused (#1526, stoppedstate.js).
  const label = nowStateLabel(ps, srcU);
  let stateLabel = label.key ? t(label.key) : '';
  let stateClass = label.cls;
  // LOCAL is what a Cinemate calls the very same analogue input, so it gets
  // the same line rather than falling through to the generic "some source is
  // active" case, which is why a speaker playing through it showed nothing
  // useful at all (#491).
  // `name || generic`, not the generic outright. The box names its own socket
  // in <itemName> and the app had already parsed and stored it one line above,
  // then overwrote it here, so a speaker with two or three inputs showed the
  // same "AUX input" for all of them while the phone remote printed the real
  // name (#274). The generic label stays as the FALLBACK: a CineMate reported
  // no name at all and fell through to "some source is active", which was
  // worse than a generic one (#491).
  // On a Wave, LOCAL is the console's own CD, radio or Aux, not a socket STR
  // can name, so it gets a neutral label instead of "AUX input" (#1322).
  const npBox = state.currentBox;
  const auxGeneric = (srcU === 'LOCAL' && !localIsAuxSocket(npBox ? sourceListCache.get(npBox.host) : null, npBox ? npBox.model : ''))
    ? t('status.ownSource') : t('status.auxInput');
  if (srcU === 'AUX' || srcU === 'LOCAL') { displayName = name || auxGeneric; if (!stateLabel) { stateLabel = t('status.active'); stateClass = 'play'; } }
  else if (srcU === 'BLUETOOTH') { displayName = t('status.bluetooth'); if (!stateLabel) { stateLabel = t('status.active'); stateClass = 'play'; } }
  else if (isAirplay) { displayName = t('status.airplay'); if (!stateLabel) { stateLabel = t('status.active'); stateClass = 'play'; } }
  else if (srcU && srcU !== 'STANDBY' && srcU !== 'INVALID_SOURCE' && ps !== 'STOP_STATE' && !stateLabel && !displayName) {
    // The box has an active source STR does not specifically label (some models
    // report AirPlay/Spotify Connect/other inputs under a different name and
    // without a playStatus). Reflect it as active rather than letting it fall
    // through to a misleading "ready" while audio is actually playing. An
    // explicit STOP_STATE is excluded so a stopped box still reads as idle.
    stateLabel = t('status.active');
    stateClass = 'play';
  }
  const isStreamSrc = (ps === 'PLAY_STATE' || ps === 'BUFFERING_STATE' || ps === 'PAUSE_STATE') && srcU !== 'AUX' && srcU !== 'BLUETOOTH' && !isAirplay;
  // The bitrate readout only means something for a measured live stream: radio
  // through the proxy (/stream/...) or Spotify. A direct library file is played
  // straight to the box and STR never measures its rate, so it must not show a
  // number (a leftover radio value) or a misleading "- kbit/s" (#310). Until
  // the rate is measured the bar shows nothing rather than a unit with no
  // number in front of it (#1077, Issue 17).
  const measuredStream = /\/stream\//.test(state.nowLocation || '') || /\/spotify\/stream/.test(state.nowLocation || '');
  const brLabel = (isStreamSrc && measuredStream && state.nowBitrate > 0) ? ` <small class="now-bitrate">${state.nowBitrate} kbit/s</small>` : '';
  bar.className = 'status-bar status-' + stateClass;
  // Glyph reflects the actual transport state so play vs pause is not conveyed
  // by colour alone (accessibility): play arrow normally, pause bars when
  // paused, a stop square when stopped (#1526).
  const stateGlyph = nowStateGlyph(ps, srcU);
  let statusHTML;
  if (displayName) {
    // displayName sits in a .track-inner so a too-long "Station: ... · track"
    // marquees inside .now. This bar is the full window wide, which is why it
    // is the one place that carries the running title (report 2026-08-23).
    statusHTML = `<span class="now"><span class="track-inner">${stateGlyph} ${escapeHtml(displayName)}</span></span>${stateLabel ? ' <small>' + escapeHtml(stateLabel) + '</small>' : ''}${brLabel}`;
  } else if (stateLabel) {
    statusHTML = `<span class="muted">${escapeHtml(stateLabel)}</span>`;
  } else if (state.currentBox && state.currentBox.offline) {
    // Not "stopped". A failed poll deliberately keeps the last known
    // now-playing rather than blanking the bar, and with nothing playing
    // that fell through to the stopped text (then "ready"), so a speaker that had gone off the
    // network was reported as an idle speaker waiting for input, for as long
    // as the app stayed open. The tile greys out but the status line said the
    // opposite (#165). Discovery already knows; this just stops contradicting it.
    statusHTML = `<span class="muted">${escapeHtml(t('status.unreachable'))}</span>`;
  } else {
    // Nothing playing is "stopped", the word the phone page uses too. It was
    // "ready" here and "Idle" there for the same speaker (#1034).
    statusHTML = `<span class="muted">${escapeHtml(t('status.stopped'))}</span>`;
  }
  // Only rewrite the DOM when the line changes, so the marquee animation is not
  // restarted on every poll (it would never get to scroll).
  // Written into .status-main, NOT into the bar itself: the elapsed time and
  // the progress bar are siblings inside the same bar now, and replacing the
  // bar's whole content would delete them on every status poll.
  if (statusHTML !== state.lastStatusHTML) {
    state.lastStatusHTML = statusHTML;
    const main = $('statusMain');
    if (main) main.innerHTML = statusHTML;
    requestAnimationFrame(() => applyTrackScroll('.status-bar .now'));
  }
}

// Stereo balance, read-only.
//
// Balance exists only while two speakers are paired, and only the MASTER of the
// pair reports it; ask the other one and it says it has none. The picker lists
// the two halves of a pair individually and marks neither as the master, so
// asking the selected speaker meant the balance simply vanished for whoever
// picked the other half, and the feature looked absent (reported 2026-08-06).
// balanceSourceBox therefore routes the question to the pair's master whichever
// half is selected. Range and centre come from the speaker (-7..+7, 0 centred
// on a SoundTouch 10) rather than from a constant, because a widely-copied
// community value of -50..+50 does not match what the firmware actually says.
//
// Shown, not settable yet. Every write ATTEMPT OVER HTTP hung and left the
// speaker's balance endpoint unresponsive until it was woken again. The
// WebSocket bus is reported to accept it (#70) and has never been tried,
// because STR's client there cannot send. Displaying it still earns its place, because a pair that was set
// off-centre in the old Bose app otherwise just sounds lopsided for no visible
// reason.
//
// Fetched on demand, never on the status poll: the speaker does not answer this
// while it is in deep standby, it simply hangs.
export async function refreshBalance() {
  const el = $('musicBalance');
  if (!el) return;
  const selected = state.currentBox;
  if (!selected || selected.kind === 'stock') { el.classList.add('hidden'); return; }
  // Resolve the pair that CONTAINS the selected box, not just the first: either
  // half of any pair must show that pair's balance (#70), which was only true
  // for the first pair while stereoPairOf returned it alone.
  const balPair = stereoPairsOf(state.zoneLive || {}).find(p => inStereoPair(selected, p)) || null;
  const box = balanceSourceBox(selected, balPair, state.boxes) || selected;
  if (box.kind === 'stock') { el.classList.add('hidden'); return; }
  const v = await readBoxBalance(box);
  if (v === null) { el.classList.add('hidden'); return; }
  el.textContent = balanceLabel(v);
  el.title = t('controls.balanceTitle');
  el.classList.remove('hidden');
}

// Track progress (#399). The speaker's own AVTransport clock is the source of
// truth, polled on the status cadence; between polls the bar advances locally
// so it moves smoothly instead of stepping every few seconds.
//
// Two rules keep it honest. It never runs backwards on its own: now_playing and
// the transport can disagree for a second around a track change, and a bar that
// jumps back reads as a bug even when the number is momentarily right. And a
// track change resets it hard, because that is the one moment when going back
// to zero is correct.
export const trackPos = { sec: 0, dur: 0, at: 0, key: '', resetAt: 0, polling: false };

function fmtClock(sec) {
  sec = Math.max(0, Math.floor(sec));
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return m + ':' + String(s).padStart(2, '0');
}

export function resetTrackProgress(key) {
  resetProgress(trackPos, key, Date.now());
  renderTrackProgress();
}

export function renderTrackProgress() {
  const wrap = $('trackProgress');
  if (!wrap) return;
  const playing = state.nowPlayState === 'PLAY_STATE' || state.nowPlayState === 'BUFFERING_STATE';
  if (!playing || trackPos.at === 0) { wrap.classList.add('hidden'); return; }
  // Interpolate forward from the last reading while the speaker is playing.
  const drift = state.nowPlayState === 'PLAY_STATE' ? (Date.now() - trackPos.at) / 1000 : 0;
  const shown = trackPos.sec + drift;
  wrap.classList.remove('hidden');
  $('trackElapsed').textContent = fmtClock(shown);
  const bar = $('trackBar');
  const fill = $('trackBarFill');
  if (trackPos.dur > 0) {
    // A track with a known length: show the bar and the total.
    bar.classList.remove('hidden');
    fill.style.width = Math.min(100, (shown / trackPos.dur) * 100).toFixed(1) + '%';
    $('trackTotal').textContent = fmtClock(trackPos.dur);
  } else {
    // Radio has no end. Elapsed time only, no bar to fill.
    bar.classList.add('hidden');
    $('trackTotal').textContent = '';
  }
}

export async function pollTrackPosition() {
  // No view gate here any more. The bar this feeds is on screen in every tab,
  // and refreshStatus decides what a tick is allowed to cost (#845).
  if (trackPos.polling || !state.currentBox) return;
  const playing = state.nowPlayState === 'PLAY_STATE' || state.nowPlayState === 'BUFFERING_STATE';
  if (!playing) { trackPos.at = 0; trackPos.resetAt = 0; renderTrackProgress(); return; }
  trackPos.polling = true;
  // A reading that lands after the track changed describes the old track.
  // refreshStatus fires this poll before its own status read, so around a
  // track change that is the normal order, not a rare race (#845).
  const keyAtStart = trackPos.key;
  try {
    // One object, read by field name. It used to be destructured as a pair,
    // which Wails cannot return: a bound method with two results keeps the
    // first and treats the second as an error, so the answer arrived as a
    // plain number and destructuring it threw on every single tick. The catch
    // below swallowed that, so the duration stayed 0, no bar was ever drawn,
    // and the elapsed clock was pure extrapolation rather than a reading.
    const p = await TrackPosition(state.currentBox.host, state.currentBox.port);
    // A failed read, a stale one, or a momentary zero length keeps the last
    // reading; see trackprogress.js. Hiding the bar on any of those is what
    // made it blink out for a poll during playback (#845).
    applyPositionReading(trackPos, p, keyAtStart, Date.now());
  } catch {
    // leave the last reading in place
  } finally {
    trackPos.polling = false;
    renderTrackProgress();
  }
}
