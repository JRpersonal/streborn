// views/setup/stereopair.js: a stereo pair from the Bose-app days around an
// install: the question before it and the note after it. Extracted from
// views/setup.js.

import { $, escapeHtml } from '../../utils.js';
import { t } from '../../i18n/index.js';
import {
  CheckStereoBeforeInstall,
  DissolvePairBeforeInstall,
  RestorePairAfterInstall,
} from '../../api.js';
import { stereoInstallChoices, dissolveHosts, restoreMessage } from '../../stereoinstall.js';

// askAboutStereoPair reads the speaker's pair before an install and lets the
// user choose. Resolves to 'none' (no pair, install as usual), 'both' (keep the
// pair: install STR on this one now and on the partner next), 'only'/'dissolve'
// (the pair was dissolved first) or 'cancel'.
export async function askAboutStereoPair(box) {
  let check = null;
  try { check = await CheckStereoBeforeInstall(box.host); } catch { return 'none'; }
  const choice = stereoInstallChoices(check);
  if (!choice.show) return 'none';
  const setupResult = $('setupResult');
  if (!setupResult) return 'none';
  const pairName = (check.pair && check.pair.name) || t('setup.pairUnnamed');
  const partner = check.partnerIP || '';
  const buttons = choice.options.map((o, i) => {
    const cls = i === 0 ? 'btn btn-primary' : 'btn';
    const label = o === 'both' ? t('setup.pairKeepBoth') : o === 'only' ? t('setup.pairOnlyThis') : t('setup.pairDissolveThis');
    return `<button class="${cls}" data-pair-choice="${o}">${escapeHtml(label)}</button>`;
  }).join(' ');
  const body = choice.partnerKnown
    ? t('setup.pairFoundBody', { name: pairName, partner })
    : t('setup.pairPartnerGoneBody', { name: pairName });
  setupResult.innerHTML = `<div class="setup-warn"><strong>${escapeHtml(t('setup.pairFoundTitle'))}</strong>`
    + `<p>${escapeHtml(body)}</p><div class="setup-pair-choices">${buttons} `
    + `<button class="btn" data-pair-choice="cancel">${escapeHtml(t('setup.pairCancel'))}</button></div></div>`;
  const picked = await new Promise((resolve) => {
    setupResult.querySelectorAll('[data-pair-choice]').forEach((btn) => {
      btn.onclick = () => resolve(btn.getAttribute('data-pair-choice'));
    });
  });
  if (picked === 'cancel' || picked === 'both') {
    setupResult.innerHTML = '';
    return picked;
  }
  setupResult.innerHTML = `<div class="muted">${escapeHtml(t('setup.pairDissolving'))}</div>`;
  try {
    const res = await DissolvePairBeforeInstall(dissolveHosts(picked, box.host, check));
    if (!res || !res.ok) {
      setupResult.innerHTML = `<div class="setup-err">${escapeHtml(t('setup.pairDissolveFailed'))}</div>`;
      return 'cancel';
    }
  } catch (e) {
    setupResult.innerHTML = `<div class="setup-err">${escapeHtml(t('setup.pairDissolveFailed'))}</div>`;
    return 'cancel';
  }
  setupResult.innerHTML = '';
  return picked;
}

// showPairAfterInstall tells the user what became of a pair written down before
// the install: install the partner next, kept, formed again, or failed.
export async function showPairAfterInstall(box) {
  let res = null;
  try { res = await RestorePairAfterInstall(box.host); } catch { return; }
  const msg = restoreMessage(res);
  const slot = $('pairAfterInstall');
  if (!msg || !slot) return;
  slot.innerHTML = `<div class="${msg.cls}">${escapeHtml(t(msg.key, {
    name: (res && res.name) || t('setup.pairUnnamed'),
    partner: (res && res.partnerIP) || '',
  }))}</div>`;
}
