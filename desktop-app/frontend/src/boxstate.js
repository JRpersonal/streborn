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
