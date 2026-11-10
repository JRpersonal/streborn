// update/shared.js: the constants the two update runners (single.js, all.js)
// both read: the busy marker of the one-write-per-speaker guard and the step
// numbering of the phases. Extracted from the main.js monolith.

import { t } from '../i18n/index.js';

// BOX_BUSY_TOKEN is the marker the one-write-per-speaker guard puts on its
// refusal (desktop-app/boxbusy.go). Matched instead of the prose, which is free
// to be reworded and translated.
export const BOX_BUSY_TOKEN = 'STR_BUSY:';

// busyRefusalText is the sentence for a start the guard turned away, or '' when
// err is anything else. Every operation that takes the guard (update, install,
// repair, removal, firmware flash) can be refused by it, and the refusal
// arrives as a rejected call or as a result's message depending on the
// operation, so err is an Error, a string or a message field. name is the
// speaker's label.
export function busyRefusalText(err, name) {
  const msg = String((err && err.message) || err || '');
  if (!msg.includes(BOX_BUSY_TOKEN)) return '';
  return t('update.alreadyRunning', { name: name || t('common.unknown') });
}

// busyRefusalOr is the text to show for a failure: the refusal sentence when
// the guard turned the start away, else the error as it came.
export function busyRefusalOr(err, name) {
  return busyRefusalText(err, name) || String(err);
}

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
