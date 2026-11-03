// The checklist for a speaker that does not come back after an update or an
// install.
//
// Why it exists. About ten owners wrote in over autumn 2026 with a speaker
// that went dark right when ST Reborn updated or installed it, and in those
// cases the update was most likely not the cause: a worn power supply or a
// failing amplifier gave out at the restart the update asked for (mails of
// 2026-09-30, 2026-10-20 and 2026-10-24, and #763). A few more were slow
// speakers (the BCO chassis, the small-NAND ones) that simply needed far
// longer than the six-minute verify window. What the app said in all of these
// was one toast, "taking longer than expected", and then nothing, so the
// owner was left with a dark box and no plan.
//
// So when a run ends with the speaker answering nothing at all, the app shows
// a staged checklist instead, in the order that costs the owner least:
//
//  1. wait: some speakers need up to half an hour after an update
//  2. power: unplug for ten seconds, another socket, another matching supply
//  3. lights up but is not here: the Bose app's "Add speaker" (still local)
//  4. stays dark: Bose's factory reset, then Bose's firmware by USB stick
//
// and closes with the honest sentence that past that point it is usually the
// hardware. No repair offer (maintainer policy since 2026-10-23): resets and
// the Bose firmware by USB stick are the way, and the stick steps are the ones
// the firmware guide already uses (fwladder.js, docs/FIRMWARE-UPDATE.md).
//
// Rendering is pure (darkChecklistSteps, darkChecklistHtml) so it is tested
// without a DOM; the callers in main.js, views/setup.js and the UI 2.0 setup
// wizard put the markup where their own failure would have gone.
import { t } from './i18n/index.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { FIRMWARE_DOCS_URL, isBatteryModel, usbUpdateKey, usbUpdateUnconfirmed } from './fwladder.js';

// The USB stick route of the firmware guide (docs/FIRMWARE-UPDATE.md,
// "5. Route C: USB stick"). Bose's own file is linked from there; STR never
// hosts one.
export const DARK_USB_DOCS_URL = FIRMWARE_DOCS_URL + '#5-route-c-usb-stick';

// The models whose factory reset is 1 and Volume minus, as the firmware
// guide already says for them (fwLadder.nsReset). Every other model gets the
// "as its manual describes" sentence rather than invented buttons.
const ONE_VOL_RESET_MODELS = ['soundtouch 10', 'soundtouch 20', 'soundtouch 30', 'soundtouch portable'];

function normModel(model) {
  return String(model || '').trim().toLowerCase();
}

// darkAfterUpdate is the gate for an update result (UpdateOutcome from
// RunSpeakerUpdate or one row of RunFleetUpdate): the upload was accepted, the
// verify window ran out, and the classifier found nothing answering at the
// speaker's address. Not a speaker that answers its own Bose API without STR
// (agentGone has its own one-step fix), not one in Bose's setup (boxInSetup:
// pulling the plug there is the wrong advice), and not one whose address
// another speaker now answers on (address-changed: it most likely worked).
export function darkAfterUpdate(result) {
  return !!result && !result.confirmed && result.outcome === 'timeout'
    && result.verdict === 'unreachable' && result.unmet !== 'address-changed';
}

// darkChecklistSteps is the checklist as data: one entry per stage, each with
// a heading key and the keys of its sentences, in order. model picks the
// battery wording (a Portable cannot be switched off by unplugging it) and the
// reset and USB sentences that are known for that model.
export function darkChecklistSteps(model) {
  const m = normModel(model);
  const battery = isBatteryModel(m);
  const dark = [
    ONE_VOL_RESET_MODELS.includes(m) ? 'darkCheck.resetKeys' : 'darkCheck.resetGeneric',
    battery ? 'darkCheck.usbBattery' : 'darkCheck.usb',
    'fwLadder.nsStick',
    usbUpdateKey(m),
  ];
  if (usbUpdateUnconfirmed(m)) dark.push('fwLadder.c3Unconfirmed');
  return [
    { id: 'wait', head: 'darkCheck.waitHead', lines: ['darkCheck.wait'] },
    { id: 'power', head: 'darkCheck.powerHead', lines: [battery ? 'darkCheck.powerBattery' : 'darkCheck.power'] },
    { id: 'wifi', head: 'darkCheck.wifiHead', lines: ['darkCheck.wifi'] },
    { id: 'dark', head: 'darkCheck.darkHead', lines: dark, link: DARK_USB_DOCS_URL },
  ];
}

// darkChecklistBody is the ordered list and the closing line, without any
// frame: the part every surface shares.
export function darkChecklistBody(model) {
  const items = darkChecklistSteps(model).map((s) => {
    const text = s.lines.map((k) => escapeHtml(t(k))).join(' ');
    const link = s.link
      ? ` <a href="#" class="link dark-check-link" data-darkopen="${escapeAttr(s.link)}">${escapeHtml(t('darkCheck.usbLink'))}</a>`
      : '';
    return `<li data-step="${s.id}"><b>${escapeHtml(t(s.head))}</b> ${text}${link}</li>`;
  }).join('');
  return `<ol class="dark-check-steps">${items}</ol>`
    + `<p class="muted small dark-check-hardware">${escapeHtml(t('darkCheck.hardware'))}</p>`;
}

// darkChecklistHtml renders the checklist for one speaker.
//   name     the speaker's name
//   model    its model string ("SoundTouch 10"), may be empty
//   context  'update' or 'install': which run it did not come back from
//   framed   true: the full panel with title and a Close button (the update
//            report host); false: a lead line and the list, to sit inside a
//            screen that already says the speaker did not come back
export function darkChecklistHtml({ name, model, context, framed } = {}) {
  const who = name || t('ui2.setup.wizard.newSpeaker');
  if (!framed) {
    return `<div class="setup-help dark-check" data-darkcheck="${context === 'install' ? 'install' : 'update'}">`
      + `<b>${escapeHtml(t('darkCheck.lead'))}</b>`
      + darkChecklistBody(model)
      + '</div>';
  }
  const intro = context === 'install' ? 'darkCheck.introInstall' : 'darkCheck.introUpdate';
  return `<div class="failreport-inner dark-check" data-darkcheck="${context === 'install' ? 'install' : 'update'}">`
    + `<div class="failreport-title">${escapeHtml(t('darkCheck.title', { name: who }))}</div>`
    + `<p class="muted small">${escapeHtml(t(intro, { name: who }))}</p>`
    + darkChecklistBody(model)
    + '<div class="failreport-actions">'
    + `<button class="btn btn-secondary btn-mini" data-darkclose="1">${escapeHtml(t('common.close'))}</button>`
    + '</div></div>';
}

// darkChecklistRowHtml is the fold-out under one speaker's row in the Update
// all window: a whole panel per speaker would bury the other rows.
export function darkChecklistRowHtml({ name, model } = {}) {
  return '<details class="dark-check dark-check-row" data-darkcheck="update">'
    + `<summary>${escapeHtml(t('darkCheck.rowSummary'))}</summary>`
    + `<p class="muted small">${escapeHtml(t('darkCheck.introUpdate', { name: name || '' }))}</p>`
    + darkChecklistBody(model)
    + '</details>';
}

// handleDarkChecklistClick answers a click inside a checklist: the firmware
// guide link opens in the browser, Close hides the host. Returns true when
// the click was the checklist's, so a caller with its own delegated handler
// can stop there.
export function handleDarkChecklistClick(e, { openURL, close } = {}) {
  const target = e && e.target;
  if (!target || typeof target.closest !== 'function') return false;
  const open = target.closest('[data-darkopen]');
  if (open) {
    if (typeof e.preventDefault === 'function') e.preventDefault();
    try { if (typeof openURL === 'function') openURL(open.dataset.darkopen); } catch { /* the browser's own problem */ }
    return true;
  }
  const shut = target.closest('[data-darkclose]');
  if (shut) {
    if (typeof e.preventDefault === 'function') e.preventDefault();
    if (typeof close === 'function') close();
    return true;
  }
  return false;
}

// wireDarkChecklist wires every checklist under root once.
export function wireDarkChecklist(root, deps = {}) {
  if (!root || typeof root.querySelectorAll !== 'function') return;
  const nodes = [...root.querySelectorAll('.dark-check')];
  if (root.classList && root.classList.contains('dark-check')) nodes.push(root);
  for (const el of nodes) {
    if (el.dataset.darkWired) continue;
    el.dataset.darkWired = '1';
    el.addEventListener('click', (e) => { handleDarkChecklistClick(e, deps); });
  }
}
