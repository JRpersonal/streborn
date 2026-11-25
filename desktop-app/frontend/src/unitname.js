// unitname.js: the name a speaker goes by on the key tiles and in what a key
// reports.
//
// Half of a live stereo pair is not a speaker of its own to the user: the
// sidebar, the player and the Spotify screen list the pair once, under its
// name with "(stereo)" (#1587, #2101). The keys of a pair live on its left
// half, so everything that names "the speaker" of a key (the heading over the
// keys, the hold card, the line a save, a rename, a clear or a recall writes
// into the key, the key picker) names the pair the same way, never the half
// the app happens to address ("Key 2 of Kitchen pair (stereo) is empty now",
// not "Key 2 of Kitchen left").

import { pairUnitName } from './groups.js';
import { t } from './i18n/index.js';
import { state } from './state.js';
import { pairDisplayName } from './stereoNames.js';
import { getBoxLabel } from './utils.js';

// speakerUnitName names box as the user knows it: a pair half by the pair's
// name with "(stereo)", any other speaker by its own name ('' for none).
// onLate repaints the caller once a pair name that was still being looked up
// lands; until then the pair is named by its halves ("Left + Right").
export function speakerUnitName(box, onLate) {
  if (!box) return '';
  const pair = pairUnitName(box, state.zoneLive, state.boxes, (p) => pairDisplayName(p, onLate), getBoxLabel);
  return pair ? t('ui2.side.pairName', { name: pair }) : getBoxLabel(box);
}
