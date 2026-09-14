// The third answer to "this station is already on key N".
//
// The agent refuses a save whose station already sits on another key, and that
// refusal has to stay: the version that silently deleted the other key is what
// wiped preset after preset (#836). But a refusal on its own left the user
// nowhere. Discussion #709 read it as a bug; discussion #925 read it as "so I
// can only keep one Spotify playlist" and stopped at one. Both were asking for
// the same missing option, so the note now offers to MOVE the station to the key
// the user pressed. A move is an explicit act, which is why it is allowed where
// the silent delete is not.

// parsePresetConflict pulls the other key and the name of what sits on it out
// of the agent's 409, as it reaches the frontend: a rejected binding whose
// message carries the JSON body. Returns null for every other failure, so
// callers fall through to their normal error path.
export function parsePresetConflict(err) {
  const s = String((err && err.message) || err || '');
  if (!s.includes('already-on-slot')) return null;
  const slot = s.match(/"slot":\s*(\d+)/);
  if (!slot) return null;
  // The name is read with the escapes intact and unescaped afterwards, because
  // a station called Rock "Live" is what breaks a naive match (#925).
  const nm = (s.match(/"name":\s*"((?:[^"\\]|\\.)*)"/) || [])[1];
  return { slot: Number(slot[1]), name: nm ? nm.replace(/\\(.)/g, '$1') : '' };
}

// presetConflictNote is the refusal in words. It names WHAT the station
// collided with, because a note that names nothing reads as a refusal to have
// more than one of something, which is how #925 concluded he could only keep one
// playlist. This is also exactly what the user sees when he declines the move,
// so the plain refusal is unchanged.
export function presetConflictNote(conflict, t) {
  return conflict.name
    ? t('preset.alreadyOnKeyNamed', { name: conflict.name, n: conflict.slot })
    : t('preset.alreadyOnKey', { n: conflict.slot });
}

// offerPresetMove shows the refusal WITH the way out and performs the move when
// the user takes it. Everything it touches is injected, so the decision and the
// order of the steps can be tested without a DOM:
//
//   conflict  {slot, name} from parsePresetConflict
//   toSlot    the key the user was saving onto
//   t         translator
//   confirm   (title, body, opts) -> Promise<bool>, the app's warn modal
//   move      (from, to) -> Promise, the agent call
//   toast     (msg) -> void
//   fail      (msg) -> void, shown when the move itself fails
//   done      () -> Promise, repaint after a successful move
//
// Returns true when the station was moved, false when the user declined or the
// move failed. Declining leaves both keys exactly as they were.
export async function offerPresetMove({ conflict, toSlot, t, confirm, move, toast, fail, done }) {
  if (!conflict || !conflict.slot) return false;
  if (conflict.slot === toSlot) {
    // The agent never names the key being written as the collision (a same-key
    // re-save is skipped), so there is nothing to move here. Say the refusal and
    // offer nothing, because "move it to key 3" when it IS on key 3 is nonsense.
    toast(presetConflictNote(conflict, t));
    return false;
  }
  const ok = await confirm(t('preset.moveTitle'), presetConflictNote(conflict, t), {
    // No warning triangle and no red: nothing went wrong, the speaker is asking
    // which of two legitimate things the user meant.
    icon: null,
    calm: true,
    confirmLabel: t('preset.moveButton', { n: toSlot }),
    confirmClass: 'btn btn-primary',
  });
  if (!ok) {
    // Declining leaves exactly what a user saw before this option existed: the
    // plain refusal. Nothing was saved and nothing was moved.
    toast(presetConflictNote(conflict, t));
    return false;
  }
  try {
    await move(conflict.slot, toSlot);
  } catch (e) {
    fail(t('preset.moveFailed', { err: String((e && e.message) || e || '') }));
    return false;
  }
  toast(t('preset.movedToKey', { from: conflict.slot, n: toSlot }));
  if (done) await done();
  return true;
}
