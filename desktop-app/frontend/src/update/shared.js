// update/shared.js: the constants the two update runners (single.js, all.js)
// both read: the busy marker of the one-write-per-speaker guard and the step
// numbering of the phases. Extracted from the main.js monolith.

import { t } from '../i18n/index.js';

// BOX_BUSY_TOKEN is the marker the one-write-per-speaker guard puts on its
// refusal (desktop-app/boxbusy.go). Matched instead of the prose, which is free
// to be reworded and translated.
export const BOX_BUSY_TOKEN = 'STR_BUSY:';

// step numbers the phases a speaker goes through, because the middle of the
// sequence looks exactly like the end of it.
//
// The order is: send the software, restart, confirm, then the Spotify engine.
// "Restarting" is roughly halfway, and it is the phase that sits on screen the
// longest, so a row reading "Rebooting (usually 2-4 min)" with a full bar reads
// as finished. It is not: the confirm and the ~16 MB engine delivery still have
// to happen, and a run interrupted there leaves a speaker on the new version
// with no Spotify engine, which is exactly what happened to one owner's
// speaker (#963).
export const UPDATE_STEPS = { send: 1, restart: 2, confirm: 3, engine: 4 };

export function withStep(text, n) {
  return text + t('updateAll.phase.step', { n });
}
