// spotifyplaymode.js: the player's shuffle and repeat buttons while a Spotify
// playlist or album plays (#2058).
//
// The buttons are the music folder's own (#queueShuffleBtn, #queueRepeatBtn in
// #queueControls), in the classic player and in the UI 2.0 player's More. For
// Spotify they ask the speaker's agent for the step a remote key bound to
// shuffle or repeat does (POST /api/spotify/playmode), and show the state the
// agent's /spotify/info reports on the now-playing refresh the app already
// makes (playModes). Nothing here polls.
//
// An agent older than this sends no playModes, so the buttons never show for
// it; an agent that answers the press with "not available" is remembered per
// speaker and the buttons go away there too, rather than staying as dead ones.
//
// Pure: plain values in, plain values out, so vitest can read it without a
// DOM.

// spotifyPlayModesShown says whether the Spotify shuffle and repeat buttons
// belong in the player now:
//   location     the speaker's now-playing location
//   boxSpotify   the speaker plays its OWN Spotify (Connect picked on a phone),
//                which STR's engine does not drive
//   playModes    the last playModes the agent reported, or null/undefined
//   unsupported  the agent said it cannot do the press
//   queueActive  a music folder plays: the buttons are the folder's then
export function spotifyPlayModesShown({ location, boxSpotify, playModes, unsupported, queueActive } = {}) {
  if (queueActive || unsupported || boxSpotify) return false;
  if (!playModes || typeof playModes !== 'object') return false;
  return /\/spotify\/stream/.test(String(location || ''));
}

// buttonRepeatMode turns the engine's repeat word into the folder queue's, so
// the repeat button draws the same three states for both: off, the whole list
// ("all"), the one song ("one").
export function buttonRepeatMode(repeat) {
  if (repeat === 'context') return 'all';
  if (repeat === 'track') return 'one';
  return 'off';
}

// playModesFromReply is the playModes of a /spotify/info reply in the shape
// the player keeps, or null when the reply carries none (an older agent, an
// engine that did not answer).
export function playModesFromReply(np) {
  const pm = np && np.playModes;
  if (!pm || typeof pm !== 'object') return null;
  return { shuffle: !!pm.shuffle, repeat: typeof pm.repeat === 'string' ? pm.repeat : 'off' };
}

// mergePlayModeAnswer folds the answer of a press into what the player shows.
// The agent leaves a field out when it could not read it back; that field
// keeps what was known.
export function mergePlayModeAnswer(prev, answer) {
  const out = { shuffle: !!(prev && prev.shuffle), repeat: (prev && prev.repeat) || 'off' };
  if (answer && typeof answer === 'object') {
    if (typeof answer.shuffle === 'boolean') out.shuffle = answer.shuffle;
    if (typeof answer.repeat === 'string') out.repeat = answer.repeat;
  }
  return out;
}

// playModeUnsupported says whether a failed press means "this speaker cannot
// do it" (an agent older than the endpoint, or an app build without the
// binding), as opposed to a refusal worth showing.
export function playModeUnsupported(err) {
  const s = String((err && err.message) || err || '');
  return s.includes('spotify_playmode_unsupported') || s.includes('STR_MISSING_BINDING');
}
