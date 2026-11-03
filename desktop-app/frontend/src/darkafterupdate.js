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
// and closes with the plain sentence that past that point the hardware is the
// most likely cause. Not "a software update cannot fix that": STR survives a
// Bose factory reset, and the slow small-NAND ST20 that is still dark after
// almost two hours is not a power supply. No repair offer (maintainer policy
// since 2026-10-23): resets and the Bose firmware by USB stick are the way,
// and the stick steps are the ones the firmware guide already uses
// (fwladder.js, docs/FIRMWARE-UPDATE.md), port and OTG adapter, the "let go"
// and the "never unplug it while it updates" warnings included.
//
// Two kinds of speaker are handled apart:
//  - The SoundTouch 300 gets no checklist at all (darkChecklistApplies). After
//    every install and update it blinks, unreachable, until it is unplugged
//    once, and its own power-cycle step is the whole answer. A 300 owner who
//    was shown the network steps factory-reset the soundbar and re-entered his
//    Wi-Fi twice over (2026-09-24), which is why installHelpHtml removes them
//    for that model, and this checklist must not bring them back.
//  - A model that never reads a USB stick at boot (Wave, SA-4/SA-5, CineMate,
//    Lifestyle, the Wireless Link Adapter: neverReadsStick) gets the factory
//    reset without the stick steps, which it cannot do.
//
// And one run is not a dark speaker at all: when every speaker of an Update
// all run went silent at once, or the system said this computer has no
// network, it is this computer that lost the connection (#963), and the
// power supply and factory reset advice would be wrong for every one of them
// (darkBatchPlan, updateNoNetworkHere).
//
// Rendering is pure (darkChecklistSteps, darkChecklistHtml) so it is tested
// without a DOM; the callers in main.js, views/setup.js and the UI 2.0 setup
// wizard put the markup where their own failure would have gone.
import { t } from './i18n/index.js';
import { escapeHtml, escapeAttr, isSoundTouch300, neverReadsStick } from './utils.js';
import { FIRMWARE_DOCS_URL, isBatteryModel, usbUpdateKey, usbUpdateUnconfirmed } from './fwladder.js';

// The USB stick route of the firmware guide (docs/FIRMWARE-UPDATE.md,
// "5. Route C: USB stick"), and the guide's list of Bose's own files per model
// and chassis ("9. Bose's files for 27.0.6, per model"): the two generations
// of one model take different files, so the stick step points there for the
// file. STR never hosts one.
export const DARK_USB_DOCS_URL = FIRMWARE_DOCS_URL + '#5-route-c-usb-stick';
export const DARK_FILES_DOCS_URL = FIRMWARE_DOCS_URL + '#9-boses-files-for-2706-per-model';

// The models whose factory reset is 1 and Volume minus, as the firmware
// guide already says for them (fwLadder.nsReset). Every other model gets the
// "as its manual describes" sentence rather than invented buttons.
const ONE_VOL_RESET_MODELS = ['soundtouch 10', 'soundtouch 20', 'soundtouch 30', 'soundtouch portable'];

// asRecord takes a box record ({ model, type, variant }) or a bare model
// string, so every helper here reads the same fields isSoundTouch300 and
// neverReadsStick read (a stock speaker often carries only `type`).
function asRecord(box) {
  if (typeof box === 'string') return { model: box };
  return box || {};
}

function modelOf(box) {
  const r = asRecord(box);
  return String(r.model || r.type || '').trim().toLowerCase();
}

// darkChecklistApplies is the one per-model gate every caller uses: every
// speaker but the SoundTouch 300, which keeps its own power-cycle step (see
// the header). An unknown speaker gets the checklist.
export function darkChecklistApplies(box) {
  return !isSoundTouch300(asRecord(box));
}

// darkStickRoute: the speaker can take Bose's firmware from a USB stick at
// boot, so the "stays dark" stage may tell its owner to do that.
export function darkStickRoute(box) {
  return !neverReadsStick(asRecord(box));
}

// darkAfterUpdate is the gate for an update result (UpdateOutcome from
// RunSpeakerUpdate or one row of RunFleetUpdate): the upload was accepted, the
// verify window ran out, and the classifier found nothing answering at the
// speaker's address. Not a speaker that answers its own Bose API without STR
// (agentGone has its own one-step fix), not one in Bose's setup (boxInSetup:
// pulling the plug there is the wrong advice), not one whose address another
// speaker now answers on (address-changed: it most likely worked), and not a
// run where this computer had no network (no-network-here: the speaker was
// never judged).
export function darkAfterUpdate(result) {
  return !!result && !result.confirmed && result.outcome === 'timeout'
    && result.verdict === 'unreachable' && result.unmet !== 'address-changed';
}

// updateNoNetworkHere: the verify window ran out while this computer had no
// network at all (ClassifyOTAResult's "no-network-here", the ENETUNREACH
// family). The speaker is untouched and nothing needs unplugging.
export function updateNoNetworkHere(result) {
  return !!result && !result.confirmed && result.verdict === 'no-network-here';
}

// darkBatchPlan decides what an Update all run shows once its rows are back.
//   entries: [{ host, result, box, engineOnly }]
// Returns { notice, checklist }: notice is 'noNetwork' (this computer said it
// has no network), 'allDark' (two or more speakers, and every one of them went
// silent at once: far more likely this computer's connection than that many
// power supplies at the same minute) or ''; checklist is the hosts whose row
// gets the staged checklist folded out, empty whenever there is a notice.
// Engine-only rows were never restarted and the 300 has its own step, so
// neither counts toward "every speaker".
export function darkBatchPlan(entries) {
  const list = (entries || []).filter((e) => e && !e.engineOnly);
  if (list.some((e) => updateNoNetworkHere(e.result))) return { notice: 'noNetwork', checklist: [] };
  const judged = list.filter((e) => darkChecklistApplies(e.box));
  const dark = judged.filter((e) => darkAfterUpdate(e.result));
  if (judged.length >= 2 && dark.length === judged.length) return { notice: 'allDark', checklist: [] };
  return { notice: '', checklist: dark.map((e) => e.host) };
}

// darkChecklistSteps is the checklist as data: one entry per stage, each with
// a heading key and the keys of its sentences, in order, and the guide links
// a stage carries. box (a record or a model string) picks the battery wording
// (a Portable cannot be switched off by unplugging it), the reset and USB
// sentences that are known for that model, and leaves the stick out for a
// model that never reads one.
export function darkChecklistSteps(box) {
  const m = modelOf(box);
  const battery = isBatteryModel(m);
  const dark = [ONE_VOL_RESET_MODELS.includes(m) ? 'darkCheck.resetKeys' : 'darkCheck.resetGeneric'];
  let links = [];
  if (darkStickRoute(box)) {
    // The same steps and warnings as the firmware guide's screen C and its
    // "did not start" list: which port (micro-USB needs an OTG adapter on the
    // 10 and the Portable), the stick, the start, let go, and never unplug it
    // while it updates, the one dangerous moment of the whole route.
    dark.push(battery ? 'darkCheck.usbBattery' : 'darkCheck.usb', 'fwLadder.nsPort', 'fwLadder.nsStick', usbUpdateKey(m));
    if (usbUpdateUnconfirmed(m)) dark.push('fwLadder.c3Unconfirmed');
    dark.push('fwLadder.c4', 'fwLadder.w2');
    if (battery) dark.push('fwLadder.w2Battery');
    links = [
      { url: DARK_USB_DOCS_URL, key: 'darkCheck.usbLink' },
      { url: DARK_FILES_DOCS_URL, key: 'darkCheck.filesLink' },
    ];
  }
  return [
    { id: 'wait', head: 'darkCheck.waitHead', lines: ['darkCheck.wait'], links: [] },
    { id: 'power', head: 'darkCheck.powerHead', lines: [battery ? 'darkCheck.powerBattery' : 'darkCheck.power'], links: [] },
    { id: 'wifi', head: 'darkCheck.wifiHead', lines: ['darkCheck.wifi'], links: [] },
    { id: 'dark', head: 'darkCheck.darkHead', lines: dark, links },
  ];
}

// darkChecklistBody is the ordered list and the closing line, without any
// frame: the part every surface shares.
export function darkChecklistBody(box) {
  const items = darkChecklistSteps(box).map((s) => {
    const text = s.lines.map((k) => escapeHtml(t(k))).join(' ');
    const links = s.links.map((l) =>
      ` <a href="#" class="link dark-check-link" data-darkopen="${escapeAttr(l.url)}">${escapeHtml(t(l.key))}</a>`).join('');
    return `<li data-step="${s.id}"><b>${escapeHtml(t(s.head))}</b> ${text}${links}</li>`;
  }).join('');
  return `<ol class="dark-check-steps">${items}</ol>`
    + `<p class="muted small dark-check-hardware">${escapeHtml(t('darkCheck.hardware'))}</p>`;
}

// darkChecklistHtml renders the checklist for one speaker.
//   name     the speaker's name
//   box      its record ({ model, type, variant }), may be missing
//   model    its model string ("SoundTouch 10") when there is no record
//   context  'update' or 'install': which run it did not come back from
//   framed   true: the full panel with title and a Close button (the update
//            report host); false: a lead line and the list, to sit inside a
//            screen that already says the speaker did not come back
// The callers gate on darkChecklistApplies; this renders what it is given.
export function darkChecklistHtml({ name, box, model, context, framed } = {}) {
  const who = name || t('ui2.setup.wizard.newSpeaker');
  const rec = box || { model: model || '' };
  if (!framed) {
    return `<div class="setup-help dark-check" data-darkcheck="${context === 'install' ? 'install' : 'update'}">`
      + `<b>${escapeHtml(t('darkCheck.lead'))}</b>`
      + darkChecklistBody(rec)
      + '</div>';
  }
  const intro = context === 'install' ? 'darkCheck.introInstall' : 'darkCheck.introUpdate';
  return `<div class="failreport-inner dark-check" data-darkcheck="${context === 'install' ? 'install' : 'update'}">`
    + `<div class="failreport-title">${escapeHtml(t('darkCheck.title', { name: who }))}</div>`
    + `<p class="muted small">${escapeHtml(t(intro, { name: who }))}</p>`
    + darkChecklistBody(rec)
    + '<div class="failreport-actions">'
    + `<button class="btn btn-secondary btn-mini" data-darkclose="1">${escapeHtml(t('common.close'))}</button>`
    + '</div></div>';
}

// darkChecklistRowHtml is the fold-out under one speaker's row in the Update
// all window: a whole panel per speaker would bury the other rows.
export function darkChecklistRowHtml({ name, box, model } = {}) {
  return '<details class="dark-check dark-check-row" data-darkcheck="update">'
    + `<summary>${escapeHtml(t('darkCheck.rowSummary'))}</summary>`
    + `<p class="muted small">${escapeHtml(t('darkCheck.introUpdate', { name: name || '' }))}</p>`
    + darkChecklistBody(box || { model: model || '' })
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
