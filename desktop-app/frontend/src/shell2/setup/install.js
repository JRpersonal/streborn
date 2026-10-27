// shell2/setup/install.js: step 3 of the UI 2.0 setup wizard, Install (phase
// 4, #1288).
//
// The install itself is the classic one, untouched (views/setup.js): the
// network install hero (#setupPrimaryAction: name, language, time zone, Wi-Fi,
// the Install button), its progress, waiting and failure screens
// (#setupResult), the USB stick section (#setupStickDetails) and the stick's
// "do this on the speaker now" panel (#setupAwaitResult). The wizard moves
// those nodes into this step and reads what they show; it never draws an
// install screen of its own and never calls the install binding.
//
// installOutcome reads the screens into one word, so the wizard can mark the
// step and move on:
//   ''                 nothing has happened yet
//   'running'          the install or its provisioning is in progress
//   'waiting'          the software is on the speaker, the speaker has not
//                      answered yet (the backend keeps looking)
//   'stick'            the stick panel waits for the speaker
//   'done'             installed and provisioned; Confirm checks the agent
//   'str-running'      the install guard (#1561) found STR already running:
//                      nothing was changed, not a failure
//   'update-finishing' the install guard found an update from this app still
//                      finishing: nothing was changed, not a failure
//   'failed'           the install did not reach its goal
//
// The markers are the classic screens' own: ids and classes setup.js renders.

import { t } from '../../i18n/index.js';
import { escapeHtml, escapeAttr } from '../../utils.js';

// installOutcome classifies the install screens. result is #setupResult,
// awaitPanel #setupAwaitResult. texts holds two localized lines that are the
// only marks of their screens: finishing (setup.installUpdateFinishing, the
// refusal has no button and no id of its own) and done (setup.installDone,
// which the SSH repair's success shows without the "how to play" block).
export function installOutcome(result, awaitPanel, texts = {}) {
  const same = (el, s) => !!s && el.textContent.trim() === String(s).trim();
  if (result) {
    if (result.querySelector('#installGoMusic')) return 'done';
    if (!result.querySelector('.setup-checklist')
      && [...result.querySelectorAll('.setup-ok')].some((el) => same(el, texts.done))) return 'done';
    // Both "already installed" screens carry the agent update button: the
    // guard's str-running refusal and the stick wait that found STR.
    if (result.querySelector('#setupUpdateAgentBtn')) return 'str-running';
    if (result.querySelector('#setupWaitPanel')) return 'waiting';
    const warn = [...result.querySelectorAll('.setup-warn')];
    if (warn.some((w) => same(w, texts.finishing))) return 'update-finishing';
    if (result.querySelector('.setup-err')) return 'failed';
    if (result.querySelector('.setup-checklist, .setup-pair-choices')) return 'running';
    if (result.textContent.trim()) return 'running';
  }
  if (awaitPanel && awaitPanel.querySelector('.setup-await-ready')) return 'stick';
  return '';
}

// outcomeStepState is what the stepper shows for the Install step.
export function outcomeStepState(outcome) {
  if (outcome === 'failed') return 'failed';
  if (outcome === 'done' || outcome === 'str-running') return 'done';
  return '';
}

// installBusy: an outcome during which leaving the step would leave a running
// install behind (the warning on leave, Back held).
export function installBusy(outcome) {
  return outcome === 'running' || outcome === 'waiting';
}

// installNoteHTML is the wizard's own line above the classic screens: what
// the outcome means for this speaker, said calmly where it is not a failure,
// with the way on. name is the speaker's name.
export function installNoteHTML(outcome, name) {
  const p = (key, cls = '') => `<p class="w2-note-text${cls}">${escapeHtml(t(key, { name }))}</p>`;
  const btn = (act, key, primary) => `<button type="button" class="u-btn ${primary ? 'u-btn--primary' : 'u-btn--secondary'} w2-act"`
    + ` data-act="${act}" aria-label="${escapeAttr(t(key + 'Label', { name }))}">${escapeHtml(t(key, { name }))}</button>`;
  switch (outcome) {
    case 'str-running':
      return '<div class="w2-note w2-note--calm" role="status">'
        + `<p class="w2-note-head"><span class="w2-ico" aria-hidden="true">i</span>${escapeHtml(t('ui2.setup.wizard.install.strRunningHead', { name }))}</p>`
        + p('ui2.setup.wizard.install.strRunningBody')
        + `<div class="w2-note-acts">${btn('confirm', 'ui2.setup.wizard.install.toConfirm', true)}</div>`
        + '</div>';
    case 'update-finishing':
      return '<div class="w2-note w2-note--calm" role="status">'
        + `<p class="w2-note-head"><span class="w2-ico" aria-hidden="true">i</span>${escapeHtml(t('ui2.setup.wizard.install.finishingHead', { name }))}</p>`
        + p('ui2.setup.wizard.install.finishingBody')
        + `<div class="w2-note-acts">${btn('confirm', 'ui2.setup.wizard.install.waitConfirm', true)}</div>`
        + '</div>';
    case 'failed':
      return '<div class="w2-note w2-note--failed" role="alert">'
        + `<p class="w2-note-head"><span class="w2-ico" aria-hidden="true">!</span>${escapeHtml(t('ui2.setup.wizard.install.failedHead', { name }))}</p>`
        + p('ui2.setup.wizard.install.failedBody')
        + '</div>';
    case 'waiting':
      return '<div class="w2-note" role="status">'
        + p('ui2.setup.wizard.install.waitingBody')
        + '</div>';
    case 'running':
      return '<div class="w2-note" role="status">'
        + p('ui2.setup.wizard.install.runningBody')
        + '</div>';
    case 'stick':
      return '<div class="w2-note" role="status">'
        + p('ui2.setup.wizard.install.stickBody')
        + '</div>';
    default:
      return '';
  }
}

// installIntroHTML is the step's lead before anything ran: which way it goes.
// route 'stick' (or a speaker that is not on the network yet) leads with the
// stick, 'network' with the install over the network.
export function installIntroHTML(route, name, missing) {
  if (missing) {
    return '<div class="w2-note w2-note--failed" role="status">'
      + `<p class="w2-note-head"><span class="w2-ico" aria-hidden="true">!</span>${escapeHtml(t('ui2.setup.wizard.install.missingHead', { name }))}</p>`
      + `<p class="w2-note-text">${escapeHtml(t('ui2.setup.wizard.install.missingBody'))}</p>`
      + '<div class="w2-note-acts"><button type="button" class="u-btn u-btn--secondary w2-act" data-act="search">'
      + `${escapeHtml(t('ui2.setup.wizard.searchAgain'))}</button></div>`
      + '</div>';
  }
  const key = route === 'stick' ? 'ui2.setup.wizard.install.introStick' : 'ui2.setup.wizard.install.introNetwork';
  return `<p class="w2-lead">${escapeHtml(t(key, { name }))}</p>`;
}
