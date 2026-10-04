// What the Multi-Room tab says after a stereo pairing that did not form.
//
// Pure, so the decision can be tested without the view. The agent answers 200
// with ok:false in three shapes: a partner whose agent was still starting
// (notReady), a pair it read back from both speakers and undid because only one
// side held it (reason partnerUnreachable / partnerDidNotStore, stereoverify.go),
// and a plain error text. The fleet run of 2026-10-04 showed the middle case
// reaching the user as a bare timeout, so the reason must win over any error
// text the agent also sent.
//
// Returns { cls, key, params } for t(key, params), or null when the pairing
// succeeded.
export function stereoFormMessage(res, nameForIP) {
  if (!res || res.ok !== false) return null;
  const notReady = Array.isArray(res.notReady) ? res.notReady : [];
  if (res.reason === 'partnerUnreachable' || res.reason === 'partnerDidNotStore') {
    const key = res.reason === 'partnerUnreachable' ? 'multiroom.pairPartnerUnreachable' : 'multiroom.pairPartnerDidNotStore';
    return { cls: 'setup-err', key, params: {} };
  }
  if (!res.error && notReady.length) {
    const names = notReady.map(ip => (nameForIP && nameForIP(ip)) || ip).join(', ');
    return { cls: 'setup-warn', key: 'multiroom.notReady', params: { names } };
  }
  return { cls: 'setup-err', key: 'multiroom.formFailed', params: { err: res.error || '' } };
}
