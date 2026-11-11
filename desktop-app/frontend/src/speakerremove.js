// speakerremove.js: taking a speaker that no longer answers off the list by
// hand (#1852).
//
// A speaker that got a new address replaces its old entry by itself once it
// is found at the new one. Across a routed network nothing finds it there, and
// the entry at the old address stayed, greyed out, for a day, with nothing the
// owner could do about it. "Remove from list" is the way out, in the classic
// speaker picker (views/boxpicker.js) and in the UI 2.0 speakers list
// (shell2/speakers/list.js).
//
// It forgets the ADDRESS only (Go: RemoveSpeakerFromList in speaker_remove.go,
// which also refuses a speaker that answers or is being updated). Nothing is
// changed on a speaker, and what belongs to the speaker rather than to the
// address stays: its favourites, groups, pair names, dismissed warnings. The
// same speaker is usually still there, at another address. That is the
// difference to speakerPurge.js, which forgets a speaker STR was removed from.
//
// Pure: no DOM, no state import, no i18n import. Everything arrives as an
// argument, so vitest covers it without the Wails runtime.

// canRemoveFromList is the gate of the action: only an entry the app counts
// as not reachable (the greyed-out, "offline" one) offers it. A speaker that
// answers would only be found again, and one that is silent because an update
// from this app is restarting it must keep its entry.
//
//   ctx.updateRunning(host)  an update from this app runs on it right now
//   ctx.installHolds         { host: hold } of speakers an update is still
//                            finishing on (installhold.js)
export function canRemoveFromList(box, ctx = {}) {
  const b = box || {};
  if (!b.host || !b.offline) return false;
  if (b.otaPending) return false;
  if (typeof ctx.updateRunning === 'function' && ctx.updateRunning(b.host)) return false;
  const holds = ctx.installHolds || {};
  if (Object.prototype.hasOwnProperty.call(holds, b.host) && holds[b.host]) return false;
  return true;
}

// withoutEntry is the speaker list without the entry at host. The match is by
// address on purpose: the same speaker at another address stays.
export function withoutEntry(boxes, host) {
  return (boxes || []).filter((b) => b && b.host !== host);
}

// dropEntrySelections clears the selections that point at the removed address
// and that the list refresh cannot re-point by deviceID: a selected speaker
// without an id, the speaker Settings shows, the Setup target.
export function dropEntrySelections(state, host) {
  if (!state || !host) return;
  const at = (b) => !!b && b.host === host;
  if (at(state.currentBox) && !state.currentBox.deviceID) state.currentBox = null;
  if (at(state.settingsBox)) state.settingsBox = null;
  if (state.setupTarget && at(state.setupTarget.box)) state.setupTarget = null;
}

// removeEntry asks the backend to forget the entry and brings the list in
// line with its answer. Never throws; resolves { status, error? } with status
// 'removed', 'reachable' (the address answers after all: the list is searched
// again instead), 'updating' or 'failed'.
//
//   env.remove(host)     the RemoveSpeakerFromList binding
//   env.boxes()          the current speaker list
//   env.applyList(list)  main.js applyBoxList (repaints, rewrites the cache)
//   env.state            the shared state, for the selections
//   env.discover()       main.js discoverBoxes
export async function removeEntry(box, env = {}) {
  const host = box && box.host;
  if (!host || typeof env.remove !== 'function') return { status: 'failed', error: '' };
  let status = '';
  try {
    status = await env.remove(host);
  } catch (e) {
    return { status: 'failed', error: String((e && e.message) || e || '') };
  }
  if (status === 'removed') {
    const list = withoutEntry(typeof env.boxes === 'function' ? env.boxes() : [], host);
    dropEntrySelections(env.state, host);
    if (typeof env.applyList === 'function') env.applyList(list);
    // After the list was applied: it re-points the Settings speaker by
    // deviceID only when it finds one, and leaves the old record otherwise.
    dropEntrySelections(env.state, host);
    return { status };
  }
  if (status === 'reachable' && typeof env.discover === 'function') {
    try { await env.discover(); } catch { /* the search reports its own errors */ }
  }
  if (status === 'reachable' || status === 'updating') return { status };
  return { status: 'failed', error: String(status || '') };
}

// removeOutcomeText words what removeEntry answered, for the toast of the
// classic layout and the card of the UI 2.0 one.
export function removeOutcomeText(res, name, t) {
  const status = (res && res.status) || 'failed';
  if (status === 'removed') return t('speaker.removedFromList', { name });
  if (status === 'reachable') return t('speaker.removeAnswers', { name });
  if (status === 'updating') return t('speaker.removeUpdating', { name });
  return t('speaker.removeFailed', { name, err: (res && res.error) || '' });
}
