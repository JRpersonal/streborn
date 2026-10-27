// shell2/setup/confirm.js: step 4 of the UI 2.0 setup wizard, Confirm (phase
// 4, #1288).
//
// Honest feedback (docs/UI-2.0.md, rule 2): the wizard is done only when the
// speaker itself answers /api/agent/version with STR. Not when the install
// said ok, not when discovery lists it: when its agent reports a version.
// The probe is the app's own BoxAgentVersion on the agent ports (port 0
// tries :17008 and :8888, the same as the classic install's verify).
//
// confirmLoop and newStrHost are pure (vitest); confirmHTML is the markup.

import { t } from '../../i18n/index.js';
import { escapeHtml, escapeAttr } from '../../utils.js';

export const CONFIRM_POLL_MS = 3000;
export const CONFIRM_WINDOW_MS = 5 * 60 * 1000;

const realSleep = (ms) => new Promise((r) => setTimeout(r, ms));

// confirmLoop asks until the speaker answers with a version, the window runs
// out, or alive() turns false (the user left or started over).
//   probe()   -> BoxAgentVersion answer ({ version, build, ... }) or throws
//   onState({ attempt, remainingMs })   after every unanswered ask
// Resolves { ok: true, version } or { ok: false, reason: 'timeout' | 'stopped' }.
export async function confirmLoop({
  probe, onState, alive = () => true,
  sleep = realSleep, now = Date.now,
  pollMs = CONFIRM_POLL_MS, windowMs = CONFIRM_WINDOW_MS,
}) {
  const deadline = now() + windowMs;
  let attempt = 0;
  while (alive()) {
    attempt++;
    let v = null;
    try { v = await probe(); } catch { v = null; }
    if (!alive()) break;
    if (v && typeof v.version === 'string' && v.version.trim()) return { ok: true, version: v };
    const remainingMs = Math.max(0, deadline - now());
    if (onState) onState({ attempt, remainingMs });
    if (remainingMs <= 0) return { ok: false, reason: 'timeout' };
    await sleep(Math.min(pollMs, remainingMs));
  }
  return { ok: false, reason: 'stopped' };
}

// newStrHost finds the speaker a stick install brought in: an STR speaker
// discovery lists now that was not on STR when the wizard started. Only one
// such speaker is an answer; two new ones at once cannot be told apart.
export function newStrHost(boxes, strAtStart) {
  const before = strAtStart instanceof Set ? strAtStart : new Set(strAtStart || []);
  const fresh = (boxes || []).filter((b) => b && b.host && b.kind !== 'stock' && !b.offline && !before.has(b.host));
  const hosts = [...new Set(fresh.map((b) => b.host))];
  return hosts.length === 1 ? hosts[0] : '';
}

// confirmHTML is the step's body for one state of the check:
//   { status: 'waiting' | 'done' | 'failed', name, version, host }
export function confirmHTML(m) {
  const name = m.name || t('ui2.setup.wizard.newSpeaker');
  if (m.status === 'done') {
    const ver = (m.version && m.version.version) || '';
    return '<div class="w2-done" role="status">'
      + `<p class="w2-note-head"><span class="w2-ico w2-ico--ok" aria-hidden="true">&#10003;</span>${escapeHtml(t('ui2.setup.wizard.confirm.doneHead', { name }))}</p>`
      + `<p class="w2-note-text">${escapeHtml(t('ui2.setup.wizard.confirm.doneBody', { name, version: ver }))}</p>`
      + '<div class="w2-note-acts">'
      + `<button type="button" class="u-btn u-btn--primary w2-act" data-act="openPage" aria-label="${escapeAttr(t('ui2.setup.wizard.confirm.openPageLabel', { name }))}">`
      + `${escapeHtml(t('ui2.setup.wizard.confirm.openPage', { name }))}</button>`
      + '<button type="button" class="u-btn u-btn--secondary w2-act" data-act="another">'
      + `${escapeHtml(t('ui2.setup.wizard.confirm.another'))}</button>`
      + '</div></div>';
  }
  if (m.status === 'failed') {
    return '<div class="w2-note w2-note--failed" role="alert">'
      + `<p class="w2-note-head"><span class="w2-ico" aria-hidden="true">!</span>${escapeHtml(t('ui2.setup.wizard.confirm.failedHead', { name }))}</p>`
      + `<p class="w2-note-text">${escapeHtml(t('ui2.setup.wizard.confirm.failedBody'))}</p>`
      + '<div class="w2-note-acts">'
      + `<button type="button" class="u-btn u-btn--primary w2-act" data-act="recheck" aria-label="${escapeAttr(t('ui2.setup.wizard.confirm.recheckLabel', { name }))}">`
      + `${escapeHtml(t('ui2.setup.wizard.confirm.recheck'))}</button>`
      + '</div></div>';
  }
  return '<div class="w2-note" role="status" aria-busy="true">'
    + `<p class="w2-note-head"><span class="w2-spin" aria-hidden="true"></span>${escapeHtml(t(m.host ? 'ui2.setup.wizard.confirm.waitingHead' : 'ui2.setup.wizard.confirm.lookingHead', { name }))}</p>`
    + `<p class="w2-note-text">${escapeHtml(t('ui2.setup.wizard.confirm.waitingBody'))}</p>`
    + '</div>';
}
