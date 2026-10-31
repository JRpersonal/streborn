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

// strSnapshot is what newStrHost compares against: the STR speakers
// discovery lists right now, by host and by device ID. The wizard takes it
// when the install starts (the Install step, the stick's Prepare), not when
// it was first opened, so a speaker that turned up in between is known.
export function strSnapshot(boxes) {
  const hosts = new Set();
  const ids = new Set();
  for (const b of boxes || []) {
    if (!b || !b.host || b.kind === 'stock') continue;
    hosts.add(b.host);
    if (b.deviceID) ids.add(String(b.deviceID).toUpperCase());
  }
  return { hosts, ids };
}

// newStrHost finds the speaker a stick install brought in: an STR speaker
// discovery lists now that was not on STR in the snapshot (strSnapshot, or a
// plain Set of hosts). A known speaker under a new address (DHCP) is not new:
// its device ID gives it away. Only one such speaker is an answer; two new
// ones at once cannot be told apart.
export function newStrHost(boxes, snapshot) {
  const snap = snapshot && snapshot.hosts instanceof Set
    ? snapshot
    : { hosts: snapshot instanceof Set ? snapshot : new Set(snapshot || []), ids: new Set() };
  const fresh = (boxes || []).filter((b) => b && b.host && b.kind !== 'stock' && !b.offline
    && !snap.hosts.has(b.host) && !(b.deviceID && snap.ids.has(String(b.deviceID).toUpperCase())));
  const hosts = [...new Set(fresh.map((b) => b.host))];
  return hosts.length === 1 ? hosts[0] : '';
}

// confirmLiveText is what the step's live region says for a state: the
// region exists from the start and only its text changes, so a screen reader
// hears the result. A failure is an alert of its own (confirmHTML).
export function confirmLiveText(m) {
  const name = m.name || t('ui2.setup.wizard.newSpeaker');
  if (m.status === 'done') {
    const ver = (m.version && m.version.version) || '';
    return `${t('ui2.setup.wizard.confirm.doneHead', { name })}. ${t('ui2.setup.wizard.confirm.doneBody', { name, version: ver })}`;
  }
  if (m.status === 'waiting') {
    return t(m.host ? 'ui2.setup.wizard.confirm.waitingHead' : 'ui2.setup.wizard.confirm.lookingHead', { name });
  }
  return '';
}

// confirmHTML is the step's body for one state of the check:
//   { status: 'waiting' | 'done' | 'failed', name, version, host }
export function confirmHTML(m) {
  const name = m.name || t('ui2.setup.wizard.newSpeaker');
  if (m.status === 'done') {
    const ver = (m.version && m.version.version) || '';
    return '<div class="w2-done">'
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
      // The way out (rule 4): back to the install with the USB stick
      // section, or start over with another speaker.
      + `<button type="button" class="u-btn u-btn--secondary w2-act" data-act="backInstall" aria-label="${escapeAttr(t('ui2.setup.wizard.confirm.backInstallLabel', { name }))}">`
      + `${escapeHtml(t('ui2.setup.wizard.confirm.backInstall'))}</button>`
      + '<button type="button" class="u-btn u-btn--secondary w2-act" data-act="another">'
      + `${escapeHtml(t('ui2.setup.wizard.confirm.another'))}</button>`
      + '</div></div>';
  }
  return '<div class="w2-note">'
    + `<p class="w2-note-head"><span class="w2-spin" aria-hidden="true"></span>${escapeHtml(t(m.host ? 'ui2.setup.wizard.confirm.waitingHead' : 'ui2.setup.wizard.confirm.lookingHead', { name }))}</p>`
    + `<p class="w2-note-text">${escapeHtml(t('ui2.setup.wizard.confirm.waitingBody'))}</p>`
    + '</div>';
}
