// Pure readers of a discovered speaker record's STR state, shared by the views
// and unit-tested without a DOM. The record shape is BoxInfo from the Go side
// (desktop-app/app_discovery.go).

// answersWithoutSTR reports whether the speaker's Bose firmware answered while
// the STR agent on it did not: a record discovery already degraded to "STR not
// running", a plain stock record, or this refresh's transient strSilent marker
// (stock :8090 answered, agent port silent, box still counted as STR). False
// for a record that is missing or offline, which is the genuine "nothing
// answers, unplug it" case. Speaker Settings uses this to pick the honest
// ending for its reconnect loop (2026-09-06 report: a stock speaker that was
// answering fine got the "agent died, unplug the speaker" panel).
export function answersWithoutSTR(rec) {
  if (!rec || rec.offline) return false;
  return !!(rec.strSilent || rec.strNotRunning || rec.kind === 'stock');
}

// displayTrackState reads the agent's /api/box/display-track answer: true or
// false when the speaker said, null when it did not. A failed read used to come
// out as false, so a speaker that was restarting showed "Off" while it was on
// and pushing titles to its display (#1083).
export function displayTrackState(r) {
  if (!r || typeof r.enabled !== 'boolean') return null;
  return r.enabled;
}

// displayMessagesState reads the agent's /api/box/display-messages answer for
// the "short message on the speaker display" setting. show is true only when
// the speaker said it has a display (the setting does nothing elsewhere);
// enabled is true/false, or null when unreadable (same rule as the display
// track, #1083); last is the text of the last message shown, or ''.
export function displayMessagesState(r) {
  if (!r || typeof r !== 'object') return { show: false, enabled: null, last: '' };
  const last = r.lastShown && typeof r.lastShown.text === 'string' ? r.lastShown.text : '';
  return {
    show: r.hasDisplay === true,
    enabled: typeof r.enabled === 'boolean' ? r.enabled : null,
    last,
  };
}
