// What the setup's Wi-Fi step did, read from the agent's answer to
// PUT /api/box/wlan. Kept apart from the install view so the mapping can be
// tested without a DOM.

// wifiStepOutcome names the result for the app log:
//   'unchanged'  the speaker already was on that network with that password,
//                the agent left the running connection alone
//   'stored'     a cabled speaker only stored the network (#1307)
//   'changed'    the speaker is switching live, or restarting to apply
// An agent that predates the "unchanged" answer reports every accepted
// request as a change, which is what it then really is.
export function wifiStepOutcome(info) {
  const i = info || {};
  if (i.unchanged === true || i.status === 'unchanged') return 'unchanged';
  if (i.wiredUplink) return 'stored';
  return 'changed';
}

// wifiStepRediscoverDelays says when to search the network again after the
// step, in milliseconds from now. Only a real change needs it: the agent
// answers BEFORE it switches, so the search the install runs right away can
// still see the speaker on its old address, and the once-a-minute refresh
// only re-probes addresses the app already knows. A live switch settles
// within seconds, but the speaker re-announces itself only once its new lease
// has held (up to 45 s), hence the second, later search; a speaker that
// restarts to apply needs the long wait.
export function wifiStepRediscoverDelays(info) {
  if (wifiStepOutcome(info) !== 'changed') return [];
  return (info && info.mechanism === 'bco') ? [90000, 150000] : [12000, 75000];
}

// wifiStepLogDetail is the short machine-readable tail of the log line. Never
// the network name or the password: the name goes to the app as its own
// argument and is turned into a tag there.
export function wifiStepLogDetail(info) {
  const i = info || {};
  return `status=${i.status || '?'} mechanism=${i.mechanism || '?'}`;
}
