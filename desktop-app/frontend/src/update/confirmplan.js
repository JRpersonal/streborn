// update/confirmplan.js: how much Update all still has to ask once its
// pre-scan is done. Pure: no DOM, no state, nothing at import.
//
// The speaker update card already asked. It names every speaker it is about to
// update, says that each restarts and how long it takes, and its button is the
// owner's yes. The run used to follow that yes with a second window listing
// the same speakers and asking again, which one owner rightly called a
// confirmation of the confirmation (#1954). That window still has a job when
// the pre-scan found something the card could not know: a stick in a speaker,
// weak Wi-Fi, tight storage, a speaker that may stay dark after the restart,
// or speakers the card never named (a current one that only needs its Spotify
// engine back). Those are worth one stop, the same stop the single-speaker
// update makes for each of them. Without any of that the yes already given is
// the yes, and the run starts straight into its progress window.
//
//   'full'  ask with the whole list, as before. Every caller that did not name
//           the speakers itself (the Update all button in Speaker Settings),
//           and a card whose list no longer matches what the run would update.
//   'notes' ask, but only about what the card did not say: the notes and the
//           engine-only speakers. The speakers the owner already agreed to are
//           not listed a second time.
//   'none'  do not ask again.
export function updateAllConfirmPlan({ named, updateHosts, engineCount, warnings, groupNoted, groupNoteShown } = {}) {
  if (!Array.isArray(named)) return 'full';
  const want = new Set(named);
  const hosts = Array.isArray(updateHosts) ? updateHosts : [];
  // The same speakers, no more and no fewer. One that dropped out or turned up
  // between the card and the click means the card's list is not what the
  // owner agreed to, so the whole list is shown again.
  if (hosts.length !== want.size || hosts.some((h) => !want.has(h))) return 'full';
  if ((engineCount || 0) > 0 || (warnings || 0) > 0) return 'notes';
  // The group note is the one note the card can show itself. Already said
  // there, it is no reason to stop; not said there, it is.
  if (groupNoted && !groupNoteShown) return 'notes';
  return 'none';
}
