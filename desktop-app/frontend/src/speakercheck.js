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
//   off-network    nothing answers on this computer's own network, not even
//                  a ping
//   no-answer      nothing answers, but ping could not run or an address
//                  table entry is left over: the off-network steps without
//                  the "not even a ping" claim
//
// Two things about the speaker change the words: a Bose original STR was
// never installed on (an offline stock speaker that answers again) is invited
// to the setup, not told to install STR "again"; and a SoundTouch Portable,
// whose battery cannot be taken out, is restarted with AUX on its charger
// instead of being unplugged.
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
import { labelVia } from './boxes.js';

export const CHECK_VERDICTS = Object.freeze(['ok', 'str-silent', 'pc-offline', 'hung', 'other-network', 'off-network', 'no-answer']);

// The steps of each verdict, in the order the user should try them.
export const CHECK_STEP_KEYS = Object.freeze({
  'ok': [],
  'str-silent': ['speakerCheck.step.waitStart', 'speakerCheck.step.installAgain'],
  'pc-offline': ['speakerCheck.step.reconnectPC'],
  'hung': ['speakerCheck.step.unplugFirst', 'speakerCheck.step.refreshAfter'],
  'other-network': ['speakerCheck.step.sameWifi', 'speakerCheck.step.refreshOwnNetwork', 'speakerCheck.step.power', 'speakerCheck.step.unplugLast'],
  'off-network': ['speakerCheck.step.power', 'speakerCheck.step.boseApp', 'speakerCheck.step.newAddress', 'speakerCheck.step.unplugLast'],
  'no-answer': ['speakerCheck.step.power', 'speakerCheck.step.boseApp', 'speakerCheck.step.newAddress', 'speakerCheck.step.unplugLast'],
});

// The Portable runs on a battery that cannot be taken out: unplugging does not
// restart it. Its forced restart is AUX held on the charger.
const PORTABLE_STEPS = Object.freeze({
  'speakerCheck.step.unplugFirst': 'speakerCheck.step.restartPortableFirst',
  'speakerCheck.step.unplugLast': 'speakerCheck.step.restartPortableLast',
});

const LEAD_KEYS = Object.freeze({
  'ok': 'speakerCheck.lead.ok',
  'str-silent': 'speakerCheck.lead.strSilent',
  'pc-offline': 'speakerCheck.lead.pcOffline',
  'hung': 'speakerCheck.lead.hung',
  'other-network': 'speakerCheck.lead.otherNetwork',
  'off-network': 'speakerCheck.lead.offNetwork',
  'no-answer': 'speakerCheck.lead.noAnswer',
});

// Every key this module can show, for the i18n coverage test.
export function speakerCheckKeys() {
  return [...new Set([
    ...Object.values(LEAD_KEYS),
    ...Object.values(CHECK_STEP_KEYS).flat(),
    ...Object.values(PORTABLE_STEPS),
    'speakerCheck.lead.failed',
    'speakerCheck.lead.stockBack',
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

// isPortableSpeaker: a SoundTouch Portable (variant taigan), by model or
// variant, the way the setup's power-cycle advice tells it apart.
export function isPortableSpeaker(box) {
  if (!box) return false;
  const m = String(box.model || box.type || '').toLowerCase().replace(/[\s_]+/g, '');
  return m.includes('portable') || String(box.variant || '').toLowerCase() === 'taigan';
}

// neverHadSTR: a Bose original the list never saw running STR. Its "Bose
// firmware answers, STR does not" is simply a speaker that is back and can be
// set up, not one whose STR is gone.
const neverHadSTR = (box) => !!box && box.kind === 'stock' && !box.strNotRunning;

const backAsStock = (res, box) => known(res) && res.verdict === 'str-silent' && neverHadSTR(box);

// checkSteps is the list of step keys for a result, [] for none. box is the
// speaker that was checked (optional): it picks the Portable's restart and
// drops the "STR again" steps for a speaker STR was never on.
export function checkSteps(res, box) {
  if (!known(res) || backAsStock(res, box)) return [];
  const steps = CHECK_STEP_KEYS[res.verdict];
  return isPortableSpeaker(box) ? steps.map((k) => PORTABLE_STEPS[k] || k) : steps.slice();
}

// allowsInstallAgain: only a speaker whose Bose firmware answers while STR
// does not may be sent to the install. For every other verdict a new install
// cannot reach the speaker or is not what is wrong. (For a Bose original STR
// was never on, the same verdict leads to its first setup; the words differ,
// see checkLeadText.)
export function allowsInstallAgain(res) {
  return known(res) && res.verdict === 'str-silent';
}

// needsAttention: the check found something the user can do. A check that
// could not run found nothing, so it does not hold anyone back from writing;
// the mail still says it could not run (checkMailSection).
export function needsAttention(res) {
  return known(res) && res.verdict !== 'ok';
}

// checkLeadText is the sentence that says what the check found.
export function checkLeadText(res, name, t = defaultT, box = null) {
  const vars = { name: name || (res && res.host) || '', host: (res && res.host) || '', networks: (res && res.pcNetworks) || '' };
  if (!known(res)) return t('speakerCheck.lead.failed', vars);
  if (backAsStock(res, box)) return t('speakerCheck.lead.stockBack', vars);
  return t(LEAD_KEYS[res.verdict], vars);
}

// checkResultHTML is the result as a lead paragraph and a numbered list of
// steps. Escaped: names and addresses come from the speakers.
export function checkResultHTML(res, name, t = defaultT, box = null) {
  const steps = checkSteps(res, box);
  const lead = `<p class="spk-check-lead">${escapeHtml(checkLeadText(res, name, t, box))}</p>`;
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
  const name = (b) => labelVia(label, b);
  return `<p>${escapeHtml(t('speakerCheck.beforeReport'))}</p>`
    + found.map(({ box, res }) => `<div class="spk-check-block"><p><b>${escapeHtml(name(box))}</b></p>${checkResultHTML(res, name(box), t, box)}</div>`).join('');
}

// checkMailSection is the block the support mail carries: one line per speaker
// the check found something on, and one per check that could not run. In
// English like the rest of the mail body. A speaker that answers is left out,
// and so are the full facts: they are in the "speaker check" log line of the
// diagnostic bundle the mail asks for, and a mailto URL past about 2 KB is cut
// off or refused by some mail programs (desktop users are mostly on Windows).
export function checkMailSection(results) {
  const lines = (results || []).map(({ box, res }) => {
    if (needsAttention(res)) {
      return res.summary || `speaker check ${res.host || (box && box.host) || '?'}: ${res.verdict}`;
    }
    if (known(res)) return '';
    const host = (res && res.host) || (box && box.host) || '?';
    const why = res && res.error ? `: ${String(res.error).slice(0, 120)}` : '';
    return `speaker check ${host}: could not run${why}`;
  }).filter(Boolean);
  if (!lines.length) return '';
  return '--------------------------------\nSpeaker check (run before this mail)\n' + lines.join('\n') + '\n';
}

// supportMailBody is the body of the "Email support" mail (Speaker settings):
// where the diagnostic bundle was saved, and what the speaker check found.
// English on purpose: it is read by support.
export function supportMailBody(savePath, checks) {
  const checkBlock = checkMailSection(checks);
  return 'Hi,\n\nI need help with ST Reborn.\n\n'
    + '(Please describe the problem here.)\n\n'
    + '--------------------------------\n'
    + 'Diagnostic logs were saved to:\n' + savePath + '\n\n'
    + 'IMPORTANT: please attach that file to this email before sending.\n'
    + (checkBlock ? '\n' + checkBlock : '');
}
