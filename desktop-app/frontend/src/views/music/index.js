// views/music/index.js: the "Listen" (Music) view, extracted from the main.js
// monolith.
//
// The view is split by concern into the modules next to this file (see each
// header). This file holds the two entry points main.js calls, mountMusicView
// and initMusicView, and re-exports the Music functions the shell, the boot
// sequence and the other views still call. Nothing here touches the DOM at
// import time: main.js calls mountMusicView after the app skeleton exists, at
// the same point in the boot order where the #view-box markup used to be
// assigned inline (see views/settings.js for why a top-level DOM write in a
// module blanks the app).

import { $ } from '../../utils.js';
import { musicViewMarkup } from './markup.js';
import { mountWiring } from './wiring.js';
import { mountSources, purgeSourceCacheForSpeaker } from './sources.js';
import { mountVolume } from './volume.js';

export { initMusicView } from './deps.js';
export { refreshSourceButtons } from './sources.js';
export {
  currentPairTarget,
  loadMusicTabVolume,
  musicVolEl,
  musicVolValEl,
  pairSetVolume,
  syncVolumeScopeOnZoneLive,
} from './volume.js';
export {
  loadLanguagesForCountry,
  loadStickRegion,
  loadTaxonomy,
  localizeLanguageName,
  renderGenreChips,
} from './taxonomy.js';
export { refreshStatus } from './status.js';
export {
  pollTrackPosition,
  refreshBalance,
  renderNowPlayingBar,
  renderTrackProgress,
  resetNowPlaying,
  trackPos,
} from './nowplaying.js';
export { scheduleLiveBitrate, scheduleLiveTitle } from './live.js';
export { mstate } from './state.js';

// mountMusicView builds #view-box and attaches the static handlers, in the
// order the statements had in main.js: markup first, then each module's
// wiring. Called once by main.js right after the skeleton.
export function mountMusicView() {
  $('view-box').innerHTML = musicViewMarkup();
  mountWiring();
  mountSources();
  mountVolume();
}

// purgeMusicStateForSpeaker clears the per-speaker session maps the Music
// modules keep, for the onSpeakerPurge hook in main.js (Settings > Remove
// STR).
export function purgeMusicStateForSpeaker(host) {
  purgeSourceCacheForSpeaker(host);
}
