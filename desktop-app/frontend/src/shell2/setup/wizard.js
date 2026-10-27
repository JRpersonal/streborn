// shell2/setup/wizard.js: the UI 2.0 setup wizard, Speakers > Add speaker
// (phase 4, #1288; docs/UI-2.0.md "What we add": the firmware check in the
// setup wizard).
//
// Four steps for exactly ONE speaker:
//   1 Find     choose the speaker (find.js)
//   2 Check    its firmware, brought to 27.0.6 first where needed (check.js,
//              built as its own part; see "The check" below)
//   3 Install  the classic network install, the USB stick as the fallback
//              (install.js reads the classic screens)
//   4 Confirm  done only when the speaker answers /api/agent/version with
//              STR (confirm.js); then a summary naming it
//
// The wizard is a frame, not a second setup. Like the shell (mount.js) it
// MOVES the classic setup view's own nodes into its steps: the target
// picker's onboarding part into Find, the network install hero, the result
// panel, the USB stick section and the stick's wait panel into Install, the
// result panel into Confirm once the install is through. Every handler, id
// lookup and running install of views/setup.js keeps working, and nothing is
// cloned. The classic layout never builds it: every entry checks
// state.ui2Shell, and nothing here touches the DOM at import.
//
// Where each control of the classic "Set up speaker" tab lives now is listed
// in SETUP_PLACES below (rule 4, nothing disappears).
//
// The check (check.js). The other part of phase 4 exports one function:
//   runCheck(container, ctx) -> Promise
//     container  the Check step's own element; the check renders into it
//     ctx        { box, label, signal, setBusy }
//                box     the chosen speaker (a discovery record, kind 'stock')
//                label   its name as the app shows it
//                signal  an AbortSignal, aborted when the user goes Back or
//                        starts over: stop and render nothing more
//                setBusy(true|false)  while true (a firmware update runs),
//                        Back is held and leaving the wizard warns
//   It resolves once the speaker is on 27.0.6, or the user chose to continue
//   with the USB stick fallback: { route: 'network' } or { route: 'stick' }
//   (checkRoute below also reads a bare 'stick'). A rejection is shown in
//   the step as an error with a retry. Without check.js in the build the step
//   says so and offers to go on (checkRunner returns null).

import { state } from '../../state.js';
import { t } from '../../i18n/index.js';
import { escapeHtml, escapeAttr, confirmWarn } from '../../utils.js';
import { BoxAgentVersion } from '../../api.js';
import { renderSetupTargetPicker, installRunActive } from '../../views/setup.js';
import { FACTORY, findModel, paintFind, selectedCandidate } from './find.js';
import { installBusy, installIntroHTML, installNoteHTML, installOutcome, outcomeStepState } from './install.js';
import { confirmHTML, confirmLiveText, confirmLoop, newStrHost, strSnapshot } from './confirm.js';
import './setup.css';

export const WIZARD_STEPS = Object.freeze(['find', 'check', 'install', 'confirm']);

export const STEP_LABEL_KEYS = Object.freeze({
  find: 'ui2.setup.wizard.step.find',
  check: 'ui2.setup.wizard.step.check',
  install: 'ui2.setup.wizard.step.install',
  confirm: 'ui2.setup.wizard.step.confirm',
});

// Every state a step can show. The words always show; the mark in front of
// them only repeats them, so the state is never told by colour alone.
export const STATE_LABEL_KEYS = Object.freeze({
  done: 'ui2.setup.wizard.state.done',
  current: 'ui2.setup.wizard.state.current',
  waiting: 'ui2.setup.wizard.state.waiting',
  failed: 'ui2.setup.wizard.state.failed',
  skipped: 'ui2.setup.wizard.state.skipped',
});

const STATE_MARK = Object.freeze({ done: '&#10003;', current: '&#9679;', waiting: '&#9675;', failed: '!', skipped: '&#8211;' });

// SETUP_PLACES: every control of the classic Set up speaker tab and its place
// in the wizard (docs/UI-2.0.md rule 4). The ids are the classic ones; the
// node is moved, not copied.
export const SETUP_PLACES = Object.freeze([
  { classic: 'Stock speaker cards (#setupTargetBody .setup-target-card[data-kind=stock])', place: 'find', how: 'one radio per Bose original; choosing clicks the classic card' },
  { classic: 'Limited model note (.setup-target-limited-note)', place: 'find', how: 'note under the row (setup.limitedNote)' },
  { classic: 'STR speaker cards, "already on STR" group (#setupDoneGroup)', place: 'find', how: 'listed as on STR with the way to its page, never as an install target' },
  { classic: 'Empty list hints (setup.targetEmpty*)', place: 'find', how: 'the empty state of the list' },
  { classic: 'New or factory-reset card (data-kind=factory-reset)', place: 'find', how: '"not on the network yet" radio; clicks the classic card' },
  { classic: 'Factory help, setup mode hint, Bose app buttons and QR codes, macOS hint', place: 'find', how: '#setupTargetSection moved below the list, shown with that choice' },
  { classic: 'Setup network push panel (#setupAPPushPanel)', place: 'find', how: 'inside the moved #setupTargetSection' },
  { classic: 'Network install hero: name, language, time zone, Wi-Fi, hidden network, show password, Install (#setupPrimaryAction)', place: 'install', how: 'moved' },
  { classic: '"Already runs STR" hero with Speaker settings (#setupStrSettings)', place: 'find', how: 'the STR rows open the speaker page' },
  { classic: 'Install progress, wait, failure, SSH repair, failure report, logs, firmware guide (#setupResult)', place: 'install', how: 'moved; to Confirm once the install is through' },
  { classic: 'Stereo pair question before the install (#setupResult)', place: 'install', how: 'moved with #setupResult' },
  { classic: 'Install guard refusals str-running and update-finishing (#setupResult)', place: 'install', how: 'moved, plus a calm note with the way to Confirm' },
  { classic: 'Update agent button (#setupUpdateAgentBtn)', place: 'install', how: 'moved with #setupResult' },
  { classic: 'How to play, power-cycle advice, pair after install, share row (#setupResult)', place: 'confirm', how: 'moved with #setupResult' },
  { classic: 'USB stick section: drives, refresh, format, name, region, language, Wi-Fi, prepare, skip to install (#setupStickDetails)', place: 'install', how: 'moved; opened when the stick is the route' },
  { classic: 'Stick wait panel: steps, status, speaker ready (#setupAwaitResult)', place: 'install', how: 'moved' },
]);

// ---- the check: the other part's module, when it is in the build ----------

const CHECK_MODULES = import.meta.glob('./check.js', { eager: true });

// checkRunner is the check's entry point, or null without check.js.
// check.js names it renderCheckStep and reports its state through onState;
// adaptCheckStep turns that into the runCheck shape described above.
export function checkRunner(mods = CHECK_MODULES) {
  const m = Object.values(mods || {})[0];
  if (!m) return null;
  if (typeof m.renderCheckStep === 'function') return adaptCheckStep(m.renderCheckStep);
  const fn = ['runCheck', 'renderCheck', 'mountCheck', 'default'].map((k) => m[k]).find((f) => typeof f === 'function');
  return fn || null;
}

// adaptCheckStep wraps check.js's renderCheckStep: a running firmware update
// (phase 'updating') holds Back and makes leaving warn, every other phase
// releases it.
export function adaptCheckStep(render) {
  return (container, ctx = {}) => {
    let busy = false;
    const onState = (s) => {
      if (typeof ctx.onPhase === 'function') ctx.onPhase((s && s.phase) || '');
      const b = !!s && s.phase === 'updating';
      if (b === busy) return;
      busy = b;
      if (typeof ctx.setBusy === 'function') ctx.setBusy(b);
    };
    // The step names the speaker exactly as the wizard does (rule 1).
    const box = { ...(ctx.box || {}) };
    if (ctx.label) box.name = ctx.label;
    return render(container, { box, signal: ctx.signal, onState });
  };
}

// checkAborted is true for an answer that only says the check was left.
export function checkAborted(result) {
  return !!result && typeof result === 'object' && result.ok === false && result.route === 'aborted';
}

// checkSkipped is true when the user went on without the firmware update
// (or without a readable version): the stepper says "skipped", not "done".
export function checkSkipped(result) {
  return !!result && typeof result === 'object' && (result.route === 'skip' || result.route === 'unknown');
}

// checkHolds is true when the check passed without a click of the user's
// (the firmware is confirmed): the wizard stays on Check and unlocks Install.
export function checkHolds(result) {
  return !!result && typeof result === 'object' && result.ok === true
    && !checkSkipped(result) && checkRoute(result) === 'network';
}

// lockHintKey says why Install is still locked during the check, from the
// check's phase (check.js onState).
export function lockHintKey(phase) {
  return ['needs-update', 'updating', 'failed'].includes(phase)
    ? 'ui2.setup.wizard.check.lockedUpdate'
    : 'ui2.setup.wizard.check.lockedCheck';
}

// checkRoute reads the check's answer: the stick, or the network install.
export function checkRoute(result) {
  if (result === 'stick') return 'stick';
  if (result && typeof result === 'object'
    && (result.route === 'stick' || result.stick === true || result.fallback === 'stick')) return 'stick';
  return 'network';
}

// ---- pure models -----------------------------------------------------------

// stepperModel: one entry per step. outcome holds what a step reached
// ('done', 'failed', 'skipped', or '' for nothing yet).
export function stepperModel(step, outcome = {}) {
  return WIZARD_STEPS.map((s, i) => {
    const o = outcome[s] || '';
    let st;
    if (s === step) st = o === 'failed' ? 'failed' : (o === 'done' ? 'done' : 'current');
    else st = o || 'waiting';
    return { step: s, n: i + 1, state: st, current: s === step };
  });
}

export function stepperHTML(model) {
  return model.map((m) => `<li class="w2-step is-${m.state}" data-step="${m.step}"${m.current ? ' aria-current="step"' : ''}>`
    + `<span class="w2-step-mark" aria-hidden="true">${STATE_MARK[m.state]}</span>`
    + '<span class="w2-step-text">'
    + `<span class="w2-step-n">${escapeHtml(t('ui2.setup.wizard.stepN', { n: m.n }))}</span>`
    + `<span class="w2-step-name">${escapeHtml(t(STEP_LABEL_KEYS[m.step]))}</span>`
    + `<span class="w2-step-state">${escapeHtml(t(STATE_LABEL_KEYS[m.state]))}</span>`
    + '</span></li>').join('');
}

// backTarget is where Back leads from a step, or '' where it is not safe:
// a firmware update or an install running, or an install that went through.
export function backTarget({ step, checkBusy, installKind, checkSkipped }) {
  if (step === 'check') return checkBusy ? '' : 'find';
  if (step === 'install') {
    if (installBusy(installKind) || installKind === 'done') return '';
    return checkSkipped ? 'find' : 'check';
  }
  return '';
}

// ---- runtime ---------------------------------------------------------------

const W = {
  root: null,
  view: null,
  ctx: {},
  step: 'find',
  outcome: {},
  target: null, // { kind: 'stock' | 'factory', host, name, model }
  route: 'network',
  installKind: '',
  // phase: the check's last state (check.js onState); ready: its answer once
  // the install may go ahead, until the user presses Install.
  check: { gen: 0, abort: null, busy: false, phase: '', ready: null },
  // running: the confirm loop is asking right now (it stops while the user
  // is elsewhere and starts again when the add route opens).
  confirm: { gen: 0, status: '', version: null, running: false },
  // The STR speakers known when the install started (confirm.js strSnapshot).
  strAtStart: { hosts: new Set(), ids: new Set() },
  sig: {},
  bypass: false,
};

const byId = (id) => (W.root && W.root.querySelector(`#${id}`)) || document.getElementById(id);
const boxFor = (host) => (state.boxes || []).find((b) => b && b.host === host) || null;
const label = (b) => (typeof W.ctx.label === 'function' ? W.ctx.label(b) : '') || (b && (b.friendlyName || b.name || b.host)) || '';
const strHosts = () => strSnapshot(state.boxes);

function targetName() {
  const tg = W.target;
  if (!tg) return '';
  const b = tg.host ? boxFor(tg.host) : null;
  return (b && label(b)) || tg.name || (tg.kind === 'factory' ? t('ui2.setup.wizard.newSpeaker') : tg.host || '');
}

function busyNow() {
  return installRunActive() || installBusy(W.installKind) || !!W.check.busy;
}

// wizardBusy: for the rest of the app (a leave elsewhere, tests).
export function wizardBusy() {
  return !!(state.ui2Shell && W.root) && busyNow();
}

// paintSetupWizard is the 'add' route's paint (shell2/speakers/index.js):
// entry true when the route was just opened, false on a refresh (discovery,
// status). Builds the wizard on first use.
export function paintSetupWizard(view, ctx = {}) {
  if (!state.ui2Shell || !view) return;
  W.ctx = ctx;
  // The setup view mounts its own DOM lazily; the wizard moves its nodes, so
  // they must exist first. renderSetupTargetPicker is idempotent.
  if (!document.getElementById('setupTargetBody')) renderSetupTargetPicker();
  if (!W.root || !view.contains(W.root)) mount(view);
  if (!W.root) return;
  if (ctx.entry) onEntry(ctx.host || '');
  paint();
}

function mount(view) {
  const nodes = {
    targetSection: document.getElementById('setupTargetSection'),
    primary: document.getElementById('setupPrimaryAction'),
    result: document.getElementById('setupResult'),
    stick: document.getElementById('setupStickDetails'),
    awaitPanel: document.getElementById('setupAwaitResult'),
  };
  const missing = Object.keys(nodes).filter((k) => !nodes[k]);
  if (missing.length) {
    try { console.warn('UI 2.0 setup wizard: not built, missing', missing.join(', ')); } catch (_) { /* no console */ }
    return;
  }
  const root = document.createElement('section');
  root.className = 'w2';
  root.id = 'ui2SetupWizard';
  root.setAttribute('aria-labelledby', 'w2Title');
  root.innerHTML = '<p class="w2-crumb" id="w2Crumb"></p>'
    + '<h2 class="w2-title" id="w2Title" tabindex="-1"></h2>'
    + '<ol class="w2-steps" id="w2Steps"></ol>'
    + '<div class="w2-panel" id="w2PanelFind" data-step="find">'
    + `<p class="w2-lead">${escapeHtml(t('ui2.setup.wizard.find.intro'))}</p>`
    + '<div id="w2FindList"></div>'
    + '<div class="w2-onboard" id="w2Onboard"></div>'
    + '</div>'
    + '<div class="w2-panel" id="w2PanelCheck" data-step="check" hidden><div id="w2CheckBody" class="w2-check"></div></div>'
    + '<div class="w2-panel" id="w2PanelInstall" data-step="install" hidden>'
    + '<div id="w2InstallIntro"></div><div id="w2InstallBody" class="w2-classic"><div id="w2InstallNote"></div></div>'
    + '</div>'
    + '<div class="w2-panel" id="w2PanelConfirm" data-step="confirm" hidden>'
    + '<div id="w2ConfirmBody"></div>'
    // Built once and only its text changes, so the result is read out.
    + '<p id="w2ConfirmLive" class="u-sr-only" role="status" aria-live="polite"></p>'
    + '<div id="w2ConfirmDetails" class="w2-classic"></div>'
    + '</div>'
    + '<div class="w2-foot" id="w2Foot"></div>';
  view.insertBefore(root, view.firstChild);
  W.root = root;
  W.view = view;
  root.querySelector('#w2Onboard').appendChild(nodes.targetSection);
  // The wizard's note on the outcome sits right above the classic result it
  // speaks about: errors where they happen.
  const body = root.querySelector('#w2InstallBody');
  const note = root.querySelector('#w2InstallNote');
  body.insertBefore(nodes.primary, note);
  for (const n of [nodes.result, nodes.stick, nodes.awaitPanel]) body.appendChild(n);
  view.classList.add('w2-on');
  W.strAtStart = strHosts();
  wire(root);
  observeInstall(nodes.result, nodes.awaitPanel);
  document.addEventListener('click', leaveGuard, true);
}

function wire(root) {
  // The stick's Prepare (the classic #setupGo) starts a stick install: the
  // speaker it brings in is one that is not on STR at this moment.
  root.addEventListener('click', (e) => {
    if (e.target && e.target.closest && e.target.closest('#setupGo')) W.strAtStart = strHosts();
  }, true);
  root.addEventListener('change', (e) => {
    const r = e.target.closest('.w2-radio');
    if (r && r.checked) choose(r.value);
  });
  root.addEventListener('click', (e) => {
    const open = e.target.closest('.w2-open[data-host]');
    if (open) {
      if (typeof W.ctx.open === 'function') W.ctx.open('speaker', { host: open.dataset.host });
      return;
    }
    const btn = e.target.closest('.w2-act[data-act]');
    if (!btn || !root.contains(btn)) return;
    if (btn.getAttribute('aria-disabled') === 'true') return;
    act(btn.dataset.act);
  });
}

function act(a) {
  switch (a) {
    case 'next': goCheck(); break;
    case 'back': {
      const to = backTarget({ step: W.step, checkBusy: W.check.busy, installKind: W.installKind, checkSkipped: W.outcome.check === 'skipped' });
      if (to === 'find') goFind();
      else if (to === 'check') goCheck();
      break;
    }
    case 'search': if (typeof W.ctx.discoverBoxes === 'function') W.ctx.discoverBoxes(); break;
    case 'stick':
      if (W.step === 'check') {
        if (W.check.busy) break;
        abortCheck();
      }
      goInstall('stick', true);
      break;
    case 'install':
      if (W.step === 'check' && W.check.ready) goInstall(checkRoute(W.check.ready), false);
      break;
    case 'confirm': goConfirm(); break;
    case 'recheck': startConfirm(); paint(); break;
    case 'backInstall': backToInstall(); break;
    case 'retryCheck': goCheck(); break;
    case 'another': reset(); paint(); focusTitle(); break;
    case 'openPage': openPage(); break;
    default: break;
  }
}

// choose takes a radio of Find through the classic picker's own card, so
// state.setupTarget, the install hero and the stick section follow exactly
// as a click in the classic layout would make them.
function choose(value) {
  if (W.step !== 'find') return;
  if (value === FACTORY) {
    W.target = { kind: 'factory', host: '', name: '', model: '' };
    clickCard('factory-reset', '');
  } else {
    const box = boxFor(value);
    if (!box || box.kind !== 'stock' || box.offline) return;
    W.target = { kind: 'stock', host: box.host, name: label(box), model: box.model || '' };
    clickCard('stock', box.host);
  }
  paint();
}

function clickCard(kind, host) {
  const sel = kind === 'factory-reset'
    ? '.setup-target-card[data-kind="factory-reset"]'
    : `.setup-target-card[data-kind="stock"][data-host="${host}"]`;
  const card = document.querySelector(sel);
  if (card) { card.click(); return; }
  // The picker has not drawn the card yet (a speaker that just arrived): the
  // same target the card would set, and the picker drawn from it.
  state.setupTarget = kind === 'factory-reset' ? { kind: 'factory-reset', box: null } : { kind: 'stock', box: boxFor(host) };
  renderSetupTargetPicker();
}

function onEntry(host) {
  if (W.step === 'confirm') {
    // A finished or failed Confirm, or another Bose original asked for (the
    // sidebar), starts over; a Confirm still waiting asks again.
    const other = !!host && !!W.target && host !== W.target.host;
    if (W.confirm.status === 'done' || W.confirm.status === 'failed' || other) reset();
    else if (W.confirm.status === 'waiting' && !W.confirm.running) startConfirm();
  }
  if (W.step !== 'find') return;
  if (host) {
    const box = boxFor(host);
    if (box && box.kind === 'stock' && !box.offline) {
      W.target = { kind: 'stock', host, name: label(box), model: box.model || '' };
      return;
    }
  }
  // A Bose original picked in the sidebar (pickBox sets the classic target and
  // opens the setup) replaces an earlier choice, the same as in the classic
  // picker; an STR speaker leaves the choice alone.
  syncFromSetupTarget();
}

// syncFromSetupTarget preselects in Find what the classic picker holds: a
// Bose original (from the sidebar, the list, or the picker's own default) or
// a chosen factory reset. An STR speaker is never an install target here.
function syncFromSetupTarget() {
  const st = state.setupTarget;
  if (!st) return;
  if (st.kind === 'factory-reset') W.target = { kind: 'factory', host: '', name: '', model: '' };
  else if (st.kind === 'stock' && st.box) {
    const box = boxFor(st.box.host);
    // Discovery decides: a speaker the picker still holds as stock may run
    // STR by now (the one just set up).
    if (box && box.kind === 'stock' && !box.offline) W.target = { kind: 'stock', host: box.host, name: label(box), model: box.model || '' };
  }
}

function reset() {
  abortCheck();
  W.confirm.gen++;
  W.step = 'find';
  W.outcome = {};
  W.target = null;
  W.route = 'network';
  W.installKind = '';
  W.confirm.status = '';
  W.confirm.version = null;
  W.confirm.running = false;
  W.strAtStart = strHosts();
  if (!installRunActive()) {
    const r = document.getElementById('setupResult');
    if (r) r.innerHTML = '';
    const a = document.getElementById('setupAwaitResult');
    if (a) a.innerHTML = '';
  }
  putResultIn('w2InstallBody');
  // Back to the picker's own choice (the classic default), shown as such.
  syncFromSetupTarget();
}

function goFind() {
  abortCheck();
  W.step = 'find';
  W.outcome = { };
  paint();
  focusTitle();
}

function goCheck() {
  const tg = W.target;
  const box = tg && tg.kind === 'stock' ? boxFor(tg.host) : null;
  if (!box || box.offline) return;
  W.step = 'check';
  W.outcome = { find: 'done' };
  W.installKind = '';
  abortCheck();
  paint();
  focusTitle();
  runCheckStep(box);
}

function abortCheck() {
  W.check.gen++;
  W.check.busy = false;
  W.check.phase = '';
  W.check.ready = null;
  if (W.check.abort) { try { W.check.abort.abort(); } catch (_) { /* already */ } }
  W.check.abort = null;
}

function runCheckStep(box) {
  abortCheck();
  const gen = W.check.gen;
  const ac = typeof AbortController === 'function' ? new AbortController() : null;
  W.check.abort = ac;
  const body = byId('w2CheckBody');
  if (!body) return;
  body.innerHTML = '';
  const name = label(box);
  const ctx = {
    box,
    label: name,
    signal: ac ? ac.signal : undefined,
    setBusy: (b) => {
      if (gen !== W.check.gen) return;
      W.check.busy = !!b;
      paint();
    },
    onPhase: (ph) => {
      if (gen !== W.check.gen || W.check.phase === ph) return;
      W.check.phase = ph;
      paint();
    },
  };
  const run = checkRunner();
  const p = run ? Promise.resolve().then(() => run(body, ctx)) : placeholderCheck(body, name);
  p.then((res) => {
    if (gen !== W.check.gen || W.step !== 'check') return;
    if (checkAborted(res)) return;
    W.check.busy = false;
    if (checkHolds(res)) {
      // The speaker passed on its own (27.0.6 read back): the rows stay in
      // view and Install unlocks in the footer, as in the design.
      W.check.ready = res;
      W.outcome = { ...W.outcome, check: 'done' };
      paint();
      return;
    }
    // A click inside the check settled it (continue without updating, the
    // placeholder): that click already was the way on.
    goInstall(checkRoute(res), checkSkipped(res));
  }, (err) => {
    if (gen !== W.check.gen || W.step !== 'check') return;
    W.check.busy = false;
    W.outcome.check = 'failed';
    const msg = String((err && err.message) || err || '');
    const note = document.createElement('div');
    note.className = 'w2-note w2-note--failed';
    note.setAttribute('role', 'alert');
    note.innerHTML = `<p class="w2-note-head"><span class="w2-ico" aria-hidden="true">!</span>${escapeHtml(t('ui2.setup.wizard.check.error', { name }))}</p>`
      + (msg ? `<p class="w2-note-text">${escapeHtml(msg)}</p>` : '')
      + '<div class="w2-note-acts"><button type="button" class="u-btn u-btn--primary w2-act" data-act="retryCheck"'
      + ` aria-label="${escapeAttr(t('ui2.setup.wizard.check.retryLabel', { name }))}">${escapeHtml(t('ui2.setup.wizard.check.retry'))}</button></div>`;
    body.appendChild(note);
    paint();
  });
}

// placeholderCheck stands in for check.js while the build has none: it says
// so plainly and lets the user go on to the install.
function placeholderCheck(body, name) {
  body.innerHTML = '<div class="w2-note" id="w2CheckPlaceholder" role="status">'
    + `<p class="w2-note-text">${escapeHtml(t('ui2.setup.wizard.check.placeholder', { name }))}</p>`
    + '<div class="w2-note-acts">'
    + `<button type="button" class="u-btn u-btn--primary" id="w2CheckSkip">${escapeHtml(t('ui2.setup.wizard.check.continue', { name }))}</button>`
    + '</div></div>';
  return new Promise((resolve) => {
    const b = body.querySelector('#w2CheckSkip');
    if (b) b.addEventListener('click', () => resolve({ route: 'network' }), { once: true });
  });
}

function goInstall(route, skipped) {
  W.route = route;
  W.outcome = { ...W.outcome, find: 'done', check: skipped ? 'skipped' : 'done', install: '' };
  W.step = 'install';
  W.installKind = '';
  if (!installRunActive()) {
    const r = document.getElementById('setupResult');
    if (r) r.innerHTML = '';
    W.strAtStart = strHosts();
  }
  putResultIn('w2InstallBody');
  applyTarget();
  const details = document.getElementById('setupStickDetails');
  if (details) details.open = route === 'stick' || (W.target && W.target.kind === 'factory');
  evaluateInstall();
  paint();
  focusTitle();
}

// applyTarget makes the classic picker hold the wizard's speaker before the
// install runs. Discovery can replace state.setupTarget when the chosen
// speaker drops off the network for a moment (a firmware update restarts
// it); the install hero must never show another speaker than the one named.
function applyTarget() {
  const tg = W.target;
  if (!tg || installRunActive()) return;
  if (tg.kind === 'stock') {
    const box = boxFor(tg.host);
    if (!box) return;
    const st = state.setupTarget;
    if (!st || st.kind !== 'stock' || !st.box || st.box.host !== tg.host) {
      state.setupTarget = { kind: 'stock', box };
      renderSetupTargetPicker();
    }
  } else if (tg.kind === 'factory') {
    const st = state.setupTarget;
    if (!st || st.kind !== 'factory-reset') clickCard('factory-reset', '');
  }
}

function putResultIn(id) {
  const r = document.getElementById('setupResult');
  const host = byId(id);
  if (!r || !host) return;
  if (id === 'w2InstallBody') {
    // Back to its place: after the hero and the wizard's note on it, before
    // the stick section.
    const note = byId('w2InstallNote');
    if (note && note.parentNode === host) {
      if (note.nextSibling !== r) host.insertBefore(r, note.nextSibling);
      return;
    }
  }
  if (r.parentNode !== host) host.appendChild(r);
}

let evalQueued = false;
function observeInstall(result, awaitPanel) {
  if (typeof MutationObserver === 'undefined') return;
  const obs = new MutationObserver(() => {
    if (evalQueued) return;
    evalQueued = true;
    Promise.resolve().then(() => { evalQueued = false; evaluateInstall(); });
  });
  for (const n of [result, awaitPanel]) obs.observe(n, { childList: true, subtree: true, characterData: true });
}

function evaluateInstall() {
  if (W.step !== 'install') return;
  const kind = installOutcome(
    document.getElementById('setupResult'),
    document.getElementById('setupAwaitResult'),
    { finishing: t('setup.installUpdateFinishing'), done: t('setup.installDone') },
  );
  if (kind === W.installKind) return;
  W.installKind = kind;
  if (kind === 'done') { goConfirm(); return; }
  W.outcome.install = outcomeStepState(kind);
  paint();
}

function goConfirm() {
  const kind = W.installKind;
  W.step = 'confirm';
  W.outcome = { ...W.outcome, find: 'done', install: (kind === 'done' || kind === 'str-running') ? 'done' : (W.outcome.install || ''), confirm: '' };
  putResultIn('w2ConfirmDetails');
  startConfirm();
  paint();
  focusTitle();
}

// backToInstall leaves a Confirm that got no answer for the Install step,
// where the network install and the USB stick section are again in reach.
function backToInstall() {
  if (W.step !== 'confirm' || installRunActive()) return;
  W.confirm.gen++;
  W.confirm.status = '';
  W.confirm.version = null;
  W.confirm.running = false;
  W.outcome.confirm = '';
  // A speaker that was only guessed from discovery (a stick install) is
  // guessed anew after the next install.
  if (W.target && W.target.kind === 'factory') W.target = { ...W.target, host: '', name: '', model: '' };
  goInstall(W.route, W.outcome.check === 'skipped');
  // The stick is the way past an install that got no answer.
  const details = document.getElementById('setupStickDetails');
  if (details) details.open = true;
}

// confirmWatched: the Confirm step is on screen. The loop asks the speaker
// only then; it is spare hardware, often in the middle of an agent update.
function confirmWatched() {
  return W.step === 'confirm' && state.view === 'setup' && !!W.root && W.root.isConnected !== false;
}

function startConfirm() {
  W.confirm.gen++;
  const gen = W.confirm.gen;
  W.confirm.status = 'waiting';
  W.confirm.version = null;
  W.confirm.running = true;
  W.outcome.confirm = '';
  const alive = () => gen === W.confirm.gen && confirmWatched();
  let asked = 0;
  const probe = async () => {
    let host = W.target && W.target.host;
    if (!host) {
      // A stick install: the speaker is the one that newly runs STR.
      host = newStrHost(state.boxes, W.strAtStart);
      if (host) {
        const b = boxFor(host);
        W.target = { ...(W.target || { kind: 'factory' }), host, name: b ? label(b) : '', model: (b && b.model) || '' };
        paint();
      } else {
        asked++;
        if (asked % 4 === 1 && typeof W.ctx.discoverBoxes === 'function') W.ctx.discoverBoxes();
        return null;
      }
    }
    return BoxAgentVersion(host, 0);
  };
  confirmLoop({ probe, alive }).then((r) => {
    if (gen === W.confirm.gen) W.confirm.running = false;
    // Stopped because the user left: status stays 'waiting', and opening
    // Add speaker again asks anew (onEntry).
    if (!alive()) return;
    if (r.ok) {
      W.confirm.status = 'done';
      W.confirm.version = r.version;
      W.outcome = { ...W.outcome, install: 'done', confirm: 'done' };
      // The speaker list (and the sidebar) learn it is on STR now.
      if (typeof W.ctx.discoverBoxes === 'function') W.ctx.discoverBoxes();
    } else {
      W.confirm.status = 'failed';
      W.outcome.confirm = 'failed';
    }
    paint();
  });
}

async function openPage() {
  const host = W.target && W.target.host;
  if (!host || typeof W.ctx.open !== 'function') return;
  const b = boxFor(host);
  // Discovery may still list it as a Bose original for a moment; its page
  // opens once the list knows it runs STR.
  if ((!b || b.kind === 'stock') && typeof W.ctx.discoverBoxes === 'function') {
    try { await W.ctx.discoverBoxes(); } catch (_) { /* the open below decides */ }
  }
  W.ctx.open('speaker', { host });
}

function focusTitle() {
  const h = byId('w2Title');
  if (h && typeof h.focus === 'function') { try { h.focus({ preventScroll: false }); } catch (_) { h.focus(); } }
}

// ---- paint -----------------------------------------------------------------

function setHTML(id, html) {
  const el = byId(id);
  if (!el || W.sig[id] === html) return;
  el.innerHTML = html;
  W.sig[id] = html;
}

function titleFor(name) {
  switch (W.step) {
    case 'check': return t('ui2.setup.wizard.title.check', { name });
    case 'install': return t('ui2.setup.wizard.title.install', { name });
    case 'confirm':
      return W.confirm.status === 'done'
        ? t('ui2.setup.wizard.title.done', { name })
        : t('ui2.setup.wizard.title.confirm', { name });
    default: return t('ui2.setup.wizard.title.find');
  }
}

function paint() {
  if (!W.root || !state.ui2Shell) return;
  const name = targetName();
  const tg = W.target;
  const crumbs = [t('ui2.speakers.add')];
  if (tg && W.step !== 'find') crumbs.push(name);
  if (tg && W.step !== 'find' && tg.model) crumbs.push(tg.model);
  setHTML('w2Crumb', escapeHtml(crumbs.join(' · ')));
  setHTML('w2Title', escapeHtml(titleFor(name || t('ui2.setup.wizard.newSpeaker'))));
  const steps = byId('w2Steps');
  if (steps) {
    const lbl = tg && W.step !== 'find' ? t('ui2.setup.wizard.stepsLabelFor', { name }) : t('ui2.setup.wizard.stepsLabel');
    if (steps.getAttribute('aria-label') !== lbl) steps.setAttribute('aria-label', lbl);
  }
  setHTML('w2Steps', stepperHTML(stepperModel(W.step, W.outcome)));
  W.root.querySelectorAll('.w2-panel[data-step]').forEach((p) => { p.hidden = p.dataset.step !== W.step; });
  W.root.dataset.step = W.step;

  if (W.step === 'find') paintFindStep();
  W.root.classList.toggle('is-onboarding', W.step === 'find' && !!tg && tg.kind === 'factory');

  let missing = false;
  if (W.step === 'install') {
    missing = !!tg && tg.kind === 'stock' && !boxFor(tg.host) && !installRunActive();
    if (!missing) applyTarget();
    setHTML('w2InstallIntro', W.installKind ? '' : installIntroHTML(W.route, name, missing));
    setHTML('w2InstallNote', installNoteHTML(W.installKind, name));
  }
  W.root.classList.toggle('is-missing', missing);
  // A refusal that is not a failure (setup.css tones the classic red down).
  W.root.classList.toggle('is-calm', W.step === 'install' && ['update-finishing', 'str-running'].includes(W.installKind));
  if (W.step === 'confirm') {
    const cm = { status: W.confirm.status, name, version: W.confirm.version, host: (tg && tg.host) || '' };
    setHTML('w2ConfirmBody', confirmHTML(cm));
    const live = byId('w2ConfirmLive');
    const say = confirmLiveText(cm);
    if (live && live.textContent !== say) live.textContent = say;
  }
  paintFoot(name);
}

function paintFindStep() {
  if (!W.target) syncFromSetupTarget();
  const selected = W.target ? (W.target.kind === 'factory' ? FACTORY : W.target.host) : '';
  const model = findModel(state.boxes || [], { label, selected });
  paintFind(byId('w2FindList'), model);
}

// footBtn: a footer button. A locked one keeps its focus (aria-disabled) and,
// with describedBy, carries the hint that says why it is locked.
function footBtn(act, text, { primary = false, disabled = false, aria = '', describedBy = '' } = {}) {
  return `<button type="button" class="u-btn ${primary ? 'u-btn--primary' : 'u-btn--secondary'} w2-act" data-act="${act}"`
    + (disabled ? ' aria-disabled="true"' : '')
    + (disabled && describedBy ? ` aria-describedby="${describedBy}"` : '')
    + (aria ? ` aria-label="${escapeAttr(aria)}"` : '')
    + `>${escapeHtml(text)}</button>`;
}

function paintFoot(name) {
  let left = '';
  let right = '';
  if (W.step === 'find') {
    left = footBtn('search', t('ui2.setup.wizard.searchAgain'));
    const tg = W.target;
    if (tg && tg.kind === 'factory') {
      left += footBtn('stick', t('ui2.setup.wizard.find.useStick'));
      right = `<span class="w2-hint">${escapeHtml(t('ui2.setup.wizard.find.factoryHint'))}</span>`;
    } else {
      const model = findModel(state.boxes || [], { label, selected: tg ? tg.host : '' });
      const c = selectedCandidate(model);
      right = (c ? '' : `<span class="w2-hint" id="w2NextHint">${escapeHtml(t('ui2.setup.wizard.find.chooseHint'))}</span>`)
        + footBtn('next', c ? t('ui2.setup.wizard.next', { name: c.name }) : t('ui2.setup.wizard.nextPlain'), { primary: true, disabled: !c, describedBy: 'w2NextHint' });
    }
  } else if (W.step === 'check' || W.step === 'install') {
    const to = backTarget({ step: W.step, checkBusy: W.check.busy, installKind: W.installKind, checkSkipped: W.outcome.check === 'skipped' });
    left = footBtn('back', t('ui2.setup.wizard.back'), { disabled: !to });
    // The USB stick stays the fallback during the check: it is the way past
    // a firmware the network install refuses.
    if (W.step === 'check' && !W.check.busy) left += footBtn('stick', t('ui2.setup.wizard.find.useStick'));
    if (W.step === 'check' && to) {
      const ready = !!W.check.ready;
      right = (ready ? '' : `<span class="w2-hint" id="w2InstallHint">${escapeHtml(t(lockHintKey(W.check.phase), { name }))}</span>`)
        + footBtn('install', t('ui2.setup.wizard.check.toInstall', { name }), { primary: true, disabled: !ready, describedBy: 'w2InstallHint' });
    }
    if (!to) right = `<span class="w2-hint">${escapeHtml(t(W.installKind === 'done' ? 'ui2.setup.wizard.backDone' : 'ui2.setup.wizard.backBusy', { name }))}</span>`;
  }
  // The footer is drawn anew when it changes; the keyboard stays on the
  // button it was on (a hint that changed, Install unlocking under it).
  const foot = byId('w2Foot');
  const ae = typeof document !== 'undefined' ? document.activeElement : null;
  const had = foot && ae && foot.contains(ae) && ae.dataset ? ae.dataset.act : '';
  setHTML('w2Foot', (left || right) ? `<div class="w2-foot-left">${left}</div><div class="w2-foot-right">${right}</div>` : '');
  if (had && foot && !foot.contains(document.activeElement)) {
    const b = foot.querySelector(`.w2-act[data-act="${had}"]`);
    if (b) b.focus();
    // Its button is gone (the stick while an update runs): the title keeps
    // the keyboard inside the wizard instead of the page.
    else focusTitle();
  }
}

// ---- leaving mid-install ---------------------------------------------------

// leaveGuard holds a click that would leave the wizard (a sidebar entry, a
// place of the Speakers row) while an install or a firmware update runs, and
// asks first. The install keeps running either way; the question is whether
// the user wants to stop watching it.
function leaveGuard(e) {
  if (W.bypass || !state.ui2Shell || !W.root || state.view !== 'setup') return;
  const el = e.target && e.target.closest ? e.target.closest('.ui2-nav-item[data-view], .spa-sub[data-route]') : null;
  if (!el || el.matches('.spa-sub[data-route="add"]')) return;
  if (!busyNow()) return;
  e.preventDefault();
  e.stopImmediatePropagation();
  askLeave().then((ok) => {
    if (!ok) return;
    W.bypass = true;
    try { el.click(); } finally { W.bypass = false; }
  });
}

function askLeave() {
  const name = targetName() || t('ui2.setup.wizard.newSpeaker');
  const key = W.step === 'check' ? 'ui2.setup.wizard.leave.bodyCheck' : 'ui2.setup.wizard.leave.body';
  return confirmWarn(
    t('ui2.setup.wizard.leave.title'),
    `<p>${escapeHtml(t(key, { name }))}</p>`,
    { confirmLabel: t('ui2.setup.wizard.leave.confirm'), confirmClass: 'btn btn-secondary' },
  ).then((ok) => !!ok, () => false);
}

// confirmLeaveWizard is for a change that reloads the window (the language
// and layout switches in main.js): while an install or a firmware update
// runs in the wizard it asks first, the same question as leaving it.
// Resolves true to go ahead. Outside the preview, or with nothing running,
// it is true at once and asks nothing.
export function confirmLeaveWizard() {
  if (!wizardBusy()) return Promise.resolve(true);
  return askLeave();
}
