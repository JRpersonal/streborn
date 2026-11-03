// speakercheck.js: the "speaker not answering" self-check, frontend side.
//
// The Go side (desktop-app/speakercheck.go, CheckSpeaker) probes one speaker
// the way the failure report does and returns ONE verdict:
//
//   ok             STR answers (the list has not caught up yet)
//   str-silent     the Bose firmware answers, STR does not: the only verdict
//                  that keeps "install STR again" on offer
//   pc-offline     this computer has no network address
//   hung           something is at the address, the Bose firmware does not
//                  answer: a restart is the fix
//   other-network  nothing answers, and the address is in another network
//   off-network    nothing answers on this computer's own network
//
// This module turns that verdict into the words and steps both layouts show
// (classic: a dialog from the speaker list; UI 2.0: inside the speaker's
// card), and into the block the support mail carries. Pure apart from the
// runner, which takes the binding as an argument so tests need no backend.
//
// Rate limit: nothing here polls. A check runs on a click only, at most
// MAX_CHECKS speakers at once, and the Go side reuses a result for a few
// seconds and joins a check that is already running.

import { t as defaultT } from './i18n/index.js';
import { escapeHtml } from './utils.js';

export const CHECK_VERDICTS = Object.freeze(['ok', 'str-silent', 'pc-offline', 'hung', 'other-network', 'off-network']);

// The steps of each verdict, in the order the user should try them.
export const CHECK_STEP_KEYS = Object.freeze({
  'ok': [],
  'str-silent': ['speakerCheck.step.waitStart', 'speakerCheck.step.installAgain'],
  'pc-offline': ['speakerCheck.step.reconnectPC'],
  'hung': ['speakerCheck.step.unplugFirst', 'speakerCheck.step.refreshAfter'],
  'other-network': ['speakerCheck.step.sameWifi', 'speakerCheck.step.refreshOwnNetwork', 'speakerCheck.step.power', 'speakerCheck.step.unplugLast'],
  'off-network': ['speakerCheck.step.power', 'speakerCheck.step.boseApp', 'speakerCheck.step.newAddress', 'speakerCheck.step.unplugLast'],
});

const LEAD_KEYS = Object.freeze({
  'ok': 'speakerCheck.lead.ok',
  'str-silent': 'speakerCheck.lead.strSilent',
  'pc-offline': 'speakerCheck.lead.pcOffline',
  'hung': 'speakerCheck.lead.hung',
  'other-network': 'speakerCheck.lead.otherNetwork',
  'off-network': 'speakerCheck.lead.offNetwork',
});

// Every key this module can show, for the i18n coverage test.
export function speakerCheckKeys() {
  return [...new Set([
    ...Object.values(LEAD_KEYS),
    ...Object.values(CHECK_STEP_KEYS).flat(),
    'speakerCheck.lead.failed',
    'speakerCheck.check',
    'speakerCheck.checkLabel',
    'speakerCheck.checking',
    'speakerCheck.title',
    'speakerCheck.installAgain',
    'speakerCheck.refresh',
    'speakerCheck.beforeReportTitle',
    'speakerCheck.beforeReport',
    'speakerCheck.mailAnyway',
    'speakerCheck.reportAnyway',
  ])].sort();
}

// MAX_CHECKS caps how many speakers one click checks: the selected one plus
// the failing ones, never the whole house.
export const MAX_CHECKS = 4;

const known = (res) => !!res && CHECK_VERDICTS.includes(res.verdict);

// checkSteps is the list of step keys for a result, [] for none.
export function checkSteps(res) {
  return known(res) ? CHECK_STEP_KEYS[res.verdict].slice() : [];
}

// allowsInstallAgain: only a speaker whose Bose firmware answers while STR
// does not may be sent to the install. For every other verdict a new install
// cannot reach the speaker or is not what is wrong.
export function allowsInstallAgain(res) {
  return known(res) && res.verdict === 'str-silent';
}

// needsAttention: the result names something the user can do.
export function needsAttention(res) {
  return !!res && (!known(res) || res.verdict !== 'ok');
}

// checkLeadText is the sentence that says what the check found.
export function checkLeadText(res, name, t = defaultT) {
  const vars = { name: name || (res && res.host) || '', host: (res && res.host) || '', networks: (res && res.pcNetworks) || '' };
  if (!known(res)) return t('speakerCheck.lead.failed', vars);
  return t(LEAD_KEYS[res.verdict], vars);
}

// checkResultHTML is the result as a lead paragraph and a numbered list of
// steps. Escaped: names and addresses come from the speakers.
export function checkResultHTML(res, name, t = defaultT) {
  const steps = checkSteps(res);
  const lead = `<p class="spk-check-lead">${escapeHtml(checkLeadText(res, name, t))}</p>`;
  if (!steps.length) return lead;
  return lead + '<ol class="spk-check-steps">'
    + steps.map((k) => `<li>${escapeHtml(t(k))}</li>`).join('')
    + '</ol>';
}

// failingSpeakers are the speakers the list itself already shows as not
// answering: offline tiles, and those whose STR stopped answering.
export function failingSpeakers(boxes) {
  return (boxes || []).filter((b) => b && b.host && (b.offline || b.strNotRunning));
}

// checkTargets is who a report checks first: the selected speaker (when
// there is one), then the failing ones, each address once, at most MAX_CHECKS.
export function checkTargets(selected, boxes) {
  const out = [];
  const add = (b) => {
    if (b && b.host && !out.some((x) => x.host === b.host)) out.push(b);
  };
  add(selected);
  failingSpeakers(boxes).forEach(add);
  return out.slice(0, MAX_CHECKS);
}

// runSpeakerChecks checks every target in parallel through the binding and
// pairs each result with its speaker. A check that throws comes back as a
// result without a verdict, so the caller can still go on to the mail.
export async function runSpeakerChecks(checkFn, targets) {
  const list = (targets || []).filter((b) => b && b.host).slice(0, MAX_CHECKS);
  return Promise.all(list.map(async (box) => {
    let res = null;
    try { res = await checkFn(box.host); } catch (e) { res = { host: box.host, verdict: '', error: String((e && e.message) || e) }; }
    return { box, res: res || { host: box.host, verdict: '' } };
  }));
}

// precheckHTML lists what the checks found, one block per speaker that needs
// something, for the dialog before a report or a mail.
export function precheckHTML(found, label, t = defaultT) {
  const name = (b) => (typeof label === 'function' ? label(b) : '') || (b && (b.friendlyName || b.name || b.host)) || '';
  return `<p>${escapeHtml(t('speakerCheck.beforeReport'))}</p>`
    + found.map(({ box, res }) => `<div class="spk-check-block"><p><b>${escapeHtml(name(box))}</b></p>${checkResultHTML(res, name(box), t)}</div>`).join('');
}

// checkMailSection is the block the support mail carries: each speaker's own
// fact block from the Go side, in English like the rest of the mail body.
export function checkMailSection(results) {
  const blocks = (results || []).map(({ box, res }) => {
    if (res && res.report) return res.report;
    const host = (res && res.host) || (box && box.host) || '?';
    return `speaker check ${host}\nthe check could not run${res && res.error ? `: ${res.error}` : ''}`;
  }).filter(Boolean);
  if (!blocks.length) return '';
  return '--------------------------------\nSpeaker check (run before this mail)\n\n' + blocks.join('\n\n') + '\n';
}
