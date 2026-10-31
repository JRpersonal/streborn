// The firmware ladder: the step-by-step update for a speaker whose firmware is
// too old to take Bose's last version in one jump.
//
// Why it exists. A SoundTouch 20 Series I on 5.x went 5.x -> 06.02.01 ->
// 07.00.37 by USB stick, while 06.02.01 -> 27.0.6 in one go stalled on an empty
// progress bar. Very old firmware needs the updates Bose once shipped in
// between, one after the other. Those in-between files are no longer on Bose's
// host; the Internet Archive keeps copies. The backend (app_fwladder.go,
// bosefw/ladder.go) knows the route and the fingerprint of every file, fetches a
// file only when the user clicks, checks it, and writes it to the user's USB
// stick. The last step is Bose's own file, through the normal guide's one-click
// update (fwguide.js).
//
// Screens, as in the design:
//   A  the plan: "Your speaker needs an update in N steps", the warning, Start
//   B  prepare the USB stick for one step
//   C  install it on the speaker, then "Done, check again"
//   D  the last step: the normal guide (one click over the network), with the
//      stick as the other route
// plus three small ones: the version picker (when the speaker cannot be read),
// the Bluetooth question (ST20/30 on 9 to 14 without moduleType), and the
// "no step-by-step path for this model yet" line.
//
// Phase 1 covers the SoundTouch 20, 30 and Portable on the older (scm)
// hardware only. Steps other than 06.02.01 and 07.00.37 (by stick) and 27.0.6
// (over the network) are untested on real speakers, and the screens say so.
//
// Rendering is pure (ladderView, ladderHtml and the helpers below are tested in
// fwladder.test.js without a DOM); wireFirmwareLadders does the clicking with
// the backend calls passed in.
import { t } from './i18n/index.js';
import { escapeHtml } from './utils.js';

export const LADDER_MODELS = ['soundtouch 20', 'soundtouch 30', 'soundtouch portable'];
export const FIRMWARE_DOCS_URL = 'https://github.com/JRpersonal/streborn/blob/main/docs/FIRMWARE-UPDATE.md';
// The ladder takes over below this major version. From 15 on, Bose's last file
// goes on directly and the normal guide runs unchanged.
export const LADDER_BELOW_MAJOR = 15;

// The version picker, for a speaker the app cannot read. Each choice stands
// for a version that gives the same steps as the whole range.
export const MANUAL_VERSIONS = [
  { id: '2', label: '2.x', version: '2.0.0' },
  { id: '5', label: '5.x', version: '5.0.0' },
  { id: '6', label: '6.x', version: '6.0.0' },
  { id: '7-13', labelKey: 'fwLadder.v7to13', version: '7.0.37' },
  { id: '14.0.15', label: '14.0.15', version: '14.0.15' },
  { id: '14.0.33', label: '14.0.33', version: '14.0.33' },
  { id: '15', labelKey: 'fwLadder.v15', version: '15.0.20' },
];
const MANUAL_MODELS = [
  { model: 'SoundTouch 20', label: 'SoundTouch 20' },
  { model: 'SoundTouch 30', label: 'SoundTouch 30' },
  { model: 'SoundTouch Portable', label: 'SoundTouch Portable' },
  { model: '', labelKey: 'fwLadder.manualOther' },
];

function major(v) {
  const m = String(v || '').trim().match(/^(\d+)\./);
  return m ? parseInt(m[1], 10) : null;
}

function tuple(v) {
  const m = String(v || '').trim().match(/^(\d+)(?:\.(\d+))?(?:\.(\d+))?/);
  if (!m) return null;
  return [parseInt(m[1], 10), parseInt(m[2] || '0', 10), parseInt(m[3] || '0', 10)];
}

// versionReached reports whether a speaker on `have` has finished the step to
// `target` (both as dotted versions, "7.0.37" or "07.00.37").
export function versionReached(have, target) {
  const a = tuple(have);
  const b = tuple(target);
  if (!a || !b) return false;
  for (let i = 0; i < 3; i++) {
    if (a[i] !== b[i]) return a[i] > b[i];
  }
  return true;
}

// sameVersion: both name the same major.minor.patch.
function sameVersion(a, b) {
  return versionReached(a, b) && versionReached(b, a);
}

function inFamily(model) {
  return LADDER_MODELS.includes(String(model || '').trim().toLowerCase());
}

// ladderApplies is the gate in firmwareGuideMount: the ladder replaces the
// single-step guide for a phase-1 model below 15.x. moduleType "sm2" is the
// newer hardware, which has no ladder yet.
export function ladderApplies(ident) {
  const id = ident || {};
  const mj = major(id.current);
  if (mj === null || mj >= LADDER_BELOW_MAJOR) return false;
  if (String(id.moduleType || '').trim().toLowerCase() === 'sm2') return false;
  return inFamily(id.model);
}

// noLadderYet: below 15.x but not a phase-1 model. Those keep the single-step
// guide plus one line saying there is no step-by-step path for them yet.
export function noLadderYet(ident) {
  const id = ident || {};
  const mj = major(id.current);
  return mj !== null && mj < LADDER_BELOW_MAJOR && !ladderApplies(id);
}

// isBatteryModel: the Portable runs on a battery that cannot be removed.
// Unplugging it does not switch it off, so it stays on its adapter and the
// power button does the switching.
export function isBatteryModel(model) {
  return String(model || '').trim().toLowerCase() === 'soundtouch portable';
}

// powerOffKey is the first instruction on screen C: unplug a mains speaker,
// switch a battery speaker off with its button while it stays plugged in.
export function powerOffKey(model) {
  return isBatteryModel(model) ? 'fwLadder.c1Battery' : 'fwLadder.c1';
}

// usbUpdateKey is the instruction that starts the USB stick update. 4 and
// Volume minus while plugging the power back in is confirmed on Series I and
// II SoundTouch 20s and reported by the community for the 10 and 30. The
// Portable cannot be started by plugging in, and the combination is not
// confirmed on it (design risk 4): it gets its own sentence with the power
// button, and usbUpdateUnconfirmed adds the "not confirmed" line. Every other
// model gets the generic sentence rather than invented buttons.
export function usbUpdateKey(model) {
  const m = String(model || '').trim().toLowerCase();
  if (isBatteryModel(m)) return 'fwLadder.c3Battery';
  if (['soundtouch 10', 'soundtouch 20', 'soundtouch 30'].includes(m)) {
    return 'fwLadder.c3FourVol';
  }
  return 'fwLadder.c3Generic';
}

// usbUpdateUnconfirmed: the button combination is not confirmed on this model,
// and screen C says so.
export function usbUpdateUnconfirmed(model) {
  return isBatteryModel(model);
}

// notStartedStartKey is the first point of the "the update did not start"
// checklist: the same button combination as screen C, stressed that the
// buttons are held before the power comes on and until the hand appears.
export function notStartedStartKey(model) {
  return isBatteryModel(model) ? 'fwLadder.nsKeysBattery' : 'fwLadder.nsKeys';
}

// portMaybeOtg: a SoundTouch 20 or 30 of the older hardware (moduleType scm,
// no Bluetooth button) may have a micro-USB port that needs an OTG adapter,
// like the 10 and the Portable. Reported once, not confirmed
// (docs/FIRMWARE-UPDATE.md, "The port"), so the hint says "may".
export function portMaybeOtg(model, moduleType) {
  const m = String(model || '').trim().toLowerCase();
  const mt = String(moduleType || '').trim().toLowerCase();
  return mt === 'scm' && (m === 'soundtouch 20' || m === 'soundtouch 30');
}

// fallbacksFor is the list of smaller steps the plan offers after stepId
// stalled. A step at or below the speaker's version would be a downgrade, so
// it never shows, whatever the plan was built from. A list left with nothing
// but the stalled step itself is no smaller route: empty.
export function fallbacksFor(plan, stepId) {
  const raw = plan && plan.fallbacks && plan.fallbacks[stepId];
  if (!Array.isArray(raw)) return [];
  const cur = plan.current;
  const fb = raw.filter(s => !(cur && versionReached(cur, s.version)));
  return fb.some(s => s.id !== stepId) ? fb : [];
}

// activeSteps is the list the user works through: the plan's steps, or, after
// a stall where the user chose smaller steps, the fallback sub-ladder followed
// by the rest of the plan.
export function activeSteps(plan, st) {
  const steps = (plan && Array.isArray(plan.steps)) ? plan.steps : [];
  const fbFor = st && st.fallbackFor;
  const fb = fbFor ? fallbacksFor(plan, fbFor) : [];
  if (!fb.length) return steps;
  const at = steps.findIndex(s => s.id === fbFor);
  if (at < 0) return steps;
  return [...fb, ...steps.slice(at + 1)];
}

// currentStep is the step on screen: the saved one while it is still ahead,
// otherwise the first one.
export function currentStep(plan, st) {
  const steps = activeSteps(plan, st);
  if (!steps.length) return null;
  const saved = st && st.stepId && steps.find(s => s.id === st.stepId);
  return saved || steps[0];
}

// ladderView decides which screen shows.
export function ladderView(plan, st) {
  if (!plan) return 'loading';
  const s = st || {};
  if (s.manual) return 'manual';
  switch (plan.status) {
    case 'needs-version': return 'manual';
    case 'needs-bluetooth': return 'bluetooth';
    case 'unsupported': return 'unsupported';
    case 'uptodate': return 'done';
    case 'final': return s.finalByStick ? (s.screen === 'C' ? 'C' : 'B') : 'D';
    case 'ladder': break;
    default: return 'error';
  }
  if (!s.started) return 'A';
  const step = currentStep(plan, s);
  if (!step) return 'done';
  if (step.source === 'bose' && !s.finalByStick) return 'D';
  return s.screen === 'C' ? 'C' : 'B';
}

// ladderClickCount is the design's click budget, without the speaker's own
// buttons: 2 on screen A, 3 per stick step (pick the stick, Prepare, Check
// again), 2 for the final one-click update.
export function ladderClickCount(plan) {
  const n = ((plan && plan.steps) || []).filter(s => s.source === 'archive').length;
  return 2 + n * 3 + 2;
}

// ladderMinutes is the honest range: 10 to 20 minutes per step on the speaker.
export function ladderMinutes(plan) {
  const n = ((plan && plan.steps) || []).length;
  return { min: n * 10, max: n * 20 };
}

// stickVerdict mirrors sticksetup.FirmwareStickProblem for the screen: the
// backend checks again before it writes anything. An Update.stu already on the
// stick is replaced by the write, so its space counts as free, as there.
//   check: the CheckStick answer; need: the file size in bytes
export function stickVerdict(check, need) {
  const c = check || {};
  const warnLarge = !!c.tooLarge;
  // warnOther: the stick holds files besides Update.stu. A hint only, never a
  // block: the Bose updater is most reliable on an otherwise empty stick.
  const warnOther = !!c.otherFiles;
  if (!c.ok) return { ok: false, problem: c.reason || 'not-writable', warnLarge, warnOther };
  if (c.clusterTooBig) return { ok: false, problem: 'cluster-too-big', warnLarge, warnOther };
  const free = (c.freeBytes || 0) + (c.existingFirmwareBytes || 0);
  if (free < (need || 0) + 10 * 1000 * 1000) return { ok: false, problem: 'no-space', warnLarge, warnOther };
  return { ok: true, problem: '', warnLarge, warnOther };
}

// formatBytes: "7.5 GB" / "512 MB".
export function formatBytes(n) {
  const b = Number(n) || 0;
  if (b >= 1e9) return `${(b / 1e9).toFixed(1)} GB`;
  return `${Math.round(b / 1e6)} MB`;
}

// ladderProgressText turns one "fwladder:progress" event into the line under
// the Prepare button.
export function ladderProgressText(p) {
  const e = p || {};
  const pct = typeof e.pct === 'number' && e.pct >= 0 ? `${e.pct} %` : '';
  switch (e.phase) {
    case 'verify': return t('fwLadder.pVerify');
    case 'download': return t('fwLadder.pDownload', { pct }).trim();
    case 'extract': return t('fwLadder.pExtract', { pct }).trim();
    case 'write': return t('fwLadder.pWrite', { pct }).trim();
    case 'eject': return t('fwLadder.pEject');
    case 'done': return t('fwLadder.pDone');
    default: return t('fwLadder.pStart');
  }
}

const STICK_PROBLEMS = ['gone', 'too-small', 'read-only', 'not-writable', 'macos-privacy', 'not-fat32', 'cluster-too-big', 'no-space'];

export function stickProblemText(problem) {
  const p = STICK_PROBLEMS.includes(problem) ? problem : 'not-writable';
  return t(`fwLadder.stick.${p}`);
}

// ladderErrorText explains a failed "Prepare stick" (FirmwareStickResult).
export function ladderErrorText(res) {
  const r = res || {};
  switch (r.code) {
    case 'fingerprint': return t('fwLadder.w7');
    case 'stick': return stickProblemText(r.detail);
    case 'download': return t('fwLadder.errDownload');
    case 'write': return t('fwLadder.errWrite');
    case 'cancelled': return t('fwLadder.errCancelled');
    case 'busy': return t('fwLadder.errBusy');
    case 'file-type': return t('fwLadder.errFileType');
    default: return t('fwLadder.errOther', { error: r.detail || r.code || '?' });
  }
}

// ---- persisted state, per speaker ----

const STORE_PREFIX = 'str.fwladder.';

// ladderKey: the speaker's own ID when known, else its address, else the model.
export function ladderKey(plan, ident) {
  const id = ident || {};
  return String((plan && plan.deviceId) || id.host || id.model || 'speaker').trim();
}

// ladderKeys: every key the state is saved under, the speaker's ID first. The
// state also goes under the address, so it is found again after a restart
// even when the speaker cannot be read then (no ID in the plan).
export function ladderKeys(plan, ident) {
  const id = ident || {};
  const keys = [ladderKey(plan, id), String(id.host || id.model || 'speaker').trim()];
  return keys.filter((k, i) => k && keys.indexOf(k) === i);
}

// loadLadderStateAny returns the first saved state among keys.
export function loadLadderStateAny(keys, storage) {
  for (const k of keys || []) {
    const v = loadLadderState(k, storage);
    if (v) return v;
  }
  return null;
}

// The part of the state worth keeping across an app restart or a speaker
// reboot. Progress, drives and results are always fresh.
export function persistable(st) {
  const s = st || {};
  return {
    started: !!s.started,
    ack: !!s.ack,
    stepId: s.stepId || '',
    screen: s.screen === 'C' ? 'C' : 'B',
    fallbackFor: s.fallbackFor || '',
    finalByStick: !!s.finalByStick,
  };
}

export function loadLadderState(key, storage) {
  try {
    const store = storage || (typeof localStorage !== 'undefined' ? localStorage : null);
    if (!store) return null;
    const raw = store.getItem(STORE_PREFIX + key);
    if (!raw) return null;
    const v = JSON.parse(raw);
    return v && typeof v === 'object' ? persistable(v) : null;
  } catch {
    return null;
  }
}

export function saveLadderState(key, st, storage) {
  try {
    const store = storage || (typeof localStorage !== 'undefined' ? localStorage : null);
    if (!store) return;
    store.setItem(STORE_PREFIX + key, JSON.stringify(persistable(st)));
  } catch {
    // Private window or blocked storage: the guide still works, it just
    // starts over after a restart.
  }
}

// resumeState merges a saved state into a fresh plan: a saved step that is no
// longer ahead (the speaker has moved on) falls back to the plan's first step.
export function resumeState(plan, saved) {
  if (!saved) return {};
  const st = { ...saved };
  const steps = activeSteps(plan, st);
  if (st.stepId && !steps.some(s => s.id === st.stepId)) {
    st.stepId = '';
    st.screen = 'B';
  }
  if (st.fallbackFor && !fallbacksFor(plan, st.fallbackFor).length) st.fallbackFor = '';
  return st;
}

// ---- rendering ----

function esc(s) { return escapeHtml(s); }

function badge(step) {
  const src = step.source === 'bose' ? t('fwLadder.badgeBose') : t('fwLadder.badgeArchive');
  const tested = step.liveVerified ? t('fwLadder.tested') : t('fwLadder.untested');
  return `<span class="fw-ladder-badge">${esc(src)}</span> `
    + `<span class="fw-ladder-badge ${step.liveVerified ? 'fw-ladder-tested' : 'fw-ladder-untested'}">${esc(tested)}</span>`;
}

function stepLabel(step) {
  return step.source === 'bose' ? t('fwLadder.stepBose', { version: step.version }) : step.version;
}

function screenA(plan, st) {
  const steps = plan.steps || [];
  const n = steps.length;
  const mins = ladderMinutes(plan);
  const list = steps.map(s => `<li><b>${esc(stepLabel(s))}</b> ${badge(s)}</li>`).join('');
  const now = plan.versionFrom === 'manual' ? 'fwLadder.nowManual' : 'fwLadder.now';
  return `<b>${esc(t('fwLadder.title', { n }))}</b>`
    + `<div>${esc(t(now, { version: plan.current || '?' }))}</div>`
    + `<ol class="fw-ladder-steps">${list}</ol>`
    + `<div class="muted small">${esc(t('fwLadder.time', { min: mins.min, max: mins.max }))}</div>`
    + (plan.oldStart ? `<div class="fw-ladder-note">${esc(t('fwLadder.oldStart'))}</div>` : '')
    + `<div class="fw-ladder-warn">${esc(t('fwLadder.w1', { n }))}</div>`
    + `<label class="fw-ladder-ack"><input type="checkbox" data-ladder="ack"${st.ack ? ' checked' : ''}> ${esc(t('fwLadder.w1Check'))}</label>`
    + `<div class="fw-guide-dl"><button type="button" class="btn btn-mini btn-primary" data-ladder="start"${st.ack ? '' : ' disabled'}>${esc(t('fwLadder.start'))}</button></div>`
    + `<div><a href="#" class="link small" data-ladder="manual">${esc(t('fwLadder.notFound'))}</a></div>`;
}

function justDoneHtml(st) {
  return st && st.justDone ? `<div class="small fw-ladder-ok">${esc(t('fwLadder.reached', { version: st.justDone }))}</div>` : '';
}

function stepHeader(plan, st, step) {
  const steps = activeSteps(plan, st);
  const k = steps.findIndex(s => s.id === step.id) + 1;
  return `<b>${esc(t('fwLadder.stepTitle', { k: k || 1, n: steps.length || 1, version: step.version }))}</b> ${badge(step)}`;
}

function driveOptions(st) {
  const drives = st.drives || [];
  if (!drives.length) return '';
  return drives.map(d => {
    const name = d.label ? `${d.label} (${d.path})` : d.path;
    const label = `${name}, ${formatBytes(d.totalBytes)}${d.filesystem ? `, ${d.filesystem}` : ''}`;
    return `<option value="${esc(d.path)}"${d.path === st.drive ? ' selected' : ''}>${esc(label)}</option>`;
  }).join('');
}

function stickStatusHtml(st, step) {
  if (!st.drive) return '';
  if (st.checking) return `<div class="muted small">${esc(t('fwLadder.checkingStick'))}</div>`;
  if (!st.check) return '';
  const v = stickVerdict(st.check, step.innerSize);
  let out = '';
  if (v.warnLarge) out += `<div class="fw-ladder-note small">${esc(t('fwLadder.w3Large'))}</div>`;
  if (v.warnOther) out += `<div class="fw-ladder-note small" data-ladder-note="other-files">${esc(t('fwLadder.otherFiles'))}</div>`;
  if (v.ok) return out + `<div class="small fw-ladder-ok">${esc(t('fwLadder.stickOk'))}</div>`;
  out += `<div class="small setup-err">${esc(stickProblemText(v.problem))}</div>`;
  if (v.problem === 'not-fat32' || v.problem === 'cluster-too-big') {
    out += `<div class="fw-guide-dl"><button type="button" class="btn btn-mini" data-ladder="format">${esc(t('fwLadder.format'))}</button></div>`;
  }
  return out;
}

function screenB(plan, st, step) {
  const v = st.check ? stickVerdict(st.check, step.innerSize) : { ok: false };
  const busy = !!st.busy;
  const drives = driveOptions(st);
  const picker = drives
    ? `<select data-ladder="drive" aria-label="${esc(t('fwLadder.drive'))}"><option value="">${esc(t('fwLadder.pickDrive'))}</option>${drives}</select>`
    : `<span class="muted small">${esc(st.drivesLoading ? t('fwLadder.lookingDrives') : t('fwLadder.noDrives'))}</span>`;
  let status = '';
  if (busy) status = esc(ladderProgressText(st.progress));
  else if (st.result && !st.result.ok) status = `<span class="setup-err">${esc(ladderErrorText(st.result))}</span>`;
  const archive = step.source === 'archive';
  return justDoneHtml(st) + stepHeader(plan, st, step)
    + `<div><b>${esc(t('fwLadder.prepTitle'))}</b></div>`
    + `<div class="muted small">${esc(t(archive ? 'fwLadder.prepIntro' : 'fwLadder.prepIntroBose'))}</div>`
    + `<div class="fw-guide-dl"><label class="small">${esc(t('fwLadder.drive'))}</label> ${picker} `
    + `<button type="button" class="btn btn-mini" data-ladder="refresh"${busy ? ' disabled' : ''}>${esc(t('fwLadder.refresh'))}</button></div>`
    + stickStatusHtml(st, step)
    + '<div class="fw-guide-dl">'
    + `<button type="button" class="btn btn-mini btn-primary" data-ladder="prepare"${v.ok && !busy ? '' : ' disabled'}>${esc(t('fwLadder.prepare'))}</button>`
    + (busy ? ` <button type="button" class="btn btn-mini" data-ladder="cancel">${esc(t('fwLadder.cancel'))}</button>` : '')
    + '</div>'
    + `<div class="fw-ladder-status small" aria-live="polite">${status}</div>`
    + `<div class="small"><a href="#" class="link" data-ladder="have-file">${esc(t('fwLadder.haveFile'))}</a>`
    + (archive ? ` · <a href="#" class="link" data-ladder="open-source">${esc(t('fwLadder.openArchive'))}</a>` : '')
    + '</div>';
}

// stalledHelp is what follows "the speaker still reports firmware X". An
// unchanged version has two causes with opposite remedies: the update ran and
// its bar stayed empty (a smaller in-between file is the documented way round),
// or the speaker never entered the USB update and booted normally (no hand on
// the display, back on the network within a minute or two). A smaller file
// cannot fix a start that never happened, so the guide asks first, and only
// the "it ran" answer offers smaller steps.
function stalledHelp(plan, st, step, model) {
  const started = st.recheck && st.recheck.started;
  if (started === true) {
    const fb = !st.fallbackFor && fallbacksFor(plan, step.id).length > 0;
    return `<div class="fw-ladder-warn">${esc(t('fwLadder.w6'))}</div>`
      + '<div class="fw-guide-dl">'
      + `<button type="button" class="btn btn-mini" data-ladder="retry">${esc(t('fwLadder.retry'))}</button>`
      + (fb ? ` <button type="button" class="btn btn-mini" data-ladder="smaller">${esc(t('fwLadder.smaller'))}</button>` : '')
      + '</div>';
  }
  if (started === false) {
    return `<div class="fw-ladder-warn" data-ladder-note="not-started">${esc(t('fwLadder.notStarted'))}`
      + '<ul class="fw-guide-steps">'
      + `<li>${esc(t(notStartedStartKey(model)))}</li>`
      + `<li>${esc(t('fwLadder.nsPort'))}</li>`
      + `<li>${esc(t('fwLadder.nsStick'))}</li>`
      + `<li>${esc(t('fwLadder.nsReset'))}</li>`
      + '</ul></div>'
      + '<div class="fw-guide-dl">'
      + `<button type="button" class="btn btn-mini" data-ladder="retry">${esc(t('fwLadder.retry'))}</button>`
      + '</div>';
  }
  return `<div><b>${esc(t('fwLadder.startedQ'))}</b></div>`
    + `<div class="muted small">${esc(t('fwLadder.startedWhy'))}</div>`
    + '<div class="fw-guide-dl">'
    + `<button type="button" class="btn btn-mini" data-ladder="started-yes">${esc(t('fwLadder.startedYes'))}</button> `
    + `<button type="button" class="btn btn-mini" data-ladder="started-no">${esc(t('fwLadder.startedNo'))}</button>`
    + '</div>';
}

function screenC(plan, st, step, ident) {
  const model = plan.model || (ident && ident.model) || '';
  let check = '';
  if (st.rechecking) check = `<div class="small">${esc(t('fwLadder.checking'))}</div>`;
  else if (st.recheck && st.recheck.state === 'same') {
    check = `<div class="small setup-err">${esc(t('fwLadder.stalled', { version: st.recheck.version || '?' }))}</div>`
      + stalledHelp(plan, st, step, model);
  } else if (st.recheck && st.recheck.state === 'unknown') {
    check = `<div class="small">${esc(t('fwLadder.notFoundAfter'))}</div>` + versionPicker('recheck-version');
  }
  return stepHeader(plan, st, step)
    + `<div><b>${esc(t('fwLadder.installTitle'))}</b></div>`
    + (st.ejectedByHand ? `<div class="small fw-ladder-note">${esc(t('fwLadder.ejectByHand'))}</div>` : '')
    + '<ol class="fw-guide-steps">'
    + `<li>${esc(t(powerOffKey(model)))}</li>`
    + `<li>${esc(t('fwLadder.c2'))}<div class="muted small">${esc(t('fwLadder.w4'))}`
    + (portMaybeOtg(model, plan.moduleType || (ident && ident.moduleType)) ? ` ${esc(t('fwLadder.w4Scm'))}` : '')
    + '</div></li>'
    + `<li>${esc(t(usbUpdateKey(model)))}`
    + (usbUpdateUnconfirmed(model) ? `<div class="muted small">${esc(t('fwLadder.c3Unconfirmed'))}</div>` : '')
    + '</li>'
    + `<li>${esc(t('fwLadder.c4'))}</li>`
    + `<li>${esc(t('fwLadder.c5'))}</li>`
    + '</ol>'
    + `<div class="fw-ladder-warn">${esc(t('fwLadder.w2'))}`
    + (isBatteryModel(model) ? ` <b>${esc(t('fwLadder.w2Battery'))}</b>` : '') + '</div>'
    + `<div class="muted small">${esc(t('fwLadder.w5'))}</div>`
    + `<div class="fw-guide-dl"><button type="button" class="btn btn-mini btn-primary" data-ladder="recheck"${st.rechecking ? ' disabled' : ''}>${esc(t('fwLadder.recheck'))}</button>`
    + ` <a href="#" class="link small" data-ladder="back-stick">${esc(t('fwLadder.backToStick'))}</a></div>`
    + check;
}

function versionPicker(act) {
  const opts = MANUAL_VERSIONS.map(v => `<option value="${esc(v.id)}">${esc(v.labelKey ? t(v.labelKey) : v.label)}</option>`).join('');
  return `<div class="fw-guide-dl"><select data-ladder-field="version" aria-label="${esc(t('fwLadder.manualVersion'))}">${opts}</select>`
    + ` <button type="button" class="btn btn-mini btn-primary" data-ladder="${act}">${esc(t('fwLadder.manualGo'))}</button></div>`;
}

function screenManual(plan, st, ident) {
  const model = String((plan && plan.model) || (ident && ident.model) || '').trim();
  const known = MANUAL_MODELS.some(m => m.model && m.model.toLowerCase() === model.toLowerCase());
  const models = MANUAL_MODELS.map((m) => {
    const sel = m.model ? m.model.toLowerCase() === model.toLowerCase() : !known;
    return `<option value="${esc(m.model)}"${sel ? ' selected' : ''}>${esc(m.labelKey ? t(m.labelKey) : m.label)}</option>`;
  }).join('');
  return `<b>${esc(t('fwLadder.manualTitle'))}</b>`
    + `<div class="muted small">${esc(t('fwLadder.manualHow'))}</div>`
    + `<div class="fw-guide-dl"><label class="small">${esc(t('fwLadder.manualModel'))}</label> `
    + `<select data-ladder-field="model" aria-label="${esc(t('fwLadder.manualModel'))}">${models}</select></div>`
    + `<div class="fw-guide-dl"><label class="small">${esc(t('fwLadder.manualVersion'))}</label></div>`
    + versionPicker('manual-go')
    + (st && st.manualError ? `<div class="small setup-err">${esc(st.manualError)}</div>` : '');
}

function screenBluetooth() {
  return `<b>${esc(t('fwLadder.btQuestion'))}</b>`
    + `<div class="muted small">${esc(t('fwLadder.btWhy'))}</div>`
    + '<div class="fw-guide-dl">'
    + `<button type="button" class="btn btn-mini" data-ladder="bt-yes">${esc(t('fwLadder.yes'))}</button> `
    + `<button type="button" class="btn btn-mini" data-ladder="bt-no">${esc(t('fwLadder.no'))}</button>`
    + '</div>';
}

// ladderHtml renders one screen.
//   ident: the mount's identity; plan: the backend's LadderPlan (or null);
//   st: the session state; guideMount(ident): the plain guide for screen D and
//   the unsupported case (passed in so this module does not import fwguide.js).
export function ladderHtml(ident, plan, st, guideMount) {
  const s = st || {};
  const view = ladderView(plan, s);
  const gm = typeof guideMount === 'function' ? guideMount : () => '';
  const plainIdent = { ...(ident || {}), current: (plan && plan.current) || (ident && ident.current) || '' };
  switch (view) {
    case 'loading':
      return `<div class="muted small">${esc(t('fwLadder.loading'))}</div>`;
    case 'manual':
      return screenManual(plan, s, ident);
    case 'bluetooth':
      return screenBluetooth();
    case 'unsupported':
      return gm({ ...plainIdent, noLadder: true });
    case 'error':
      return gm(plainIdent);
    case 'done':
      return `<div class="fw-ladder-ok">${esc(t(s.started ? 'fwLadder.doneAll' : 'fwLadder.upToDate'))}</div>`;
    case 'A':
      return screenA(plan, s);
    case 'D': {
      const fin = currentStep(plan, s) || {};
      const gid = { ...plainIdent, moduleType: plan.moduleType || (ident && ident.moduleType) || '' };
      return justDoneHtml(s) + (plan.status === 'ladder' ? stepHeader(plan, s, fin) : `<b>${esc(t('fwLadder.finalTitle'))}</b>`)
        + gm(gid)
        + `<div class="small"><a href="#" class="link" data-ladder="final-stick">${esc(t('fwLadder.finalStick'))}</a></div>`;
    }
    case 'B':
    case 'C': {
      const step = currentStep(plan, s);
      if (!step) return '';
      return view === 'B' ? screenB(plan, s, step) : screenC(plan, s, step, ident);
    }
    default:
      return '';
  }
}

// ---- mounting and wiring ----

function identKey(id) {
  return [id.model, id.moduleType, id.variant, id.current, id.host].map(v => String(v || '')).join('|');
}

// Session state per mount identity, so a redrawn screen keeps its place.
const sessions = new Map();

export function firmwareLadderMount(ident, guideMount) {
  const id = {
    model: (ident && ident.model) || '',
    moduleType: (ident && ident.moduleType) || '',
    variant: (ident && ident.variant) || '',
    current: (ident && ident.current) || '',
    context: (ident && ident.context) || '',
    host: (ident && ident.host) || '',
  };
  const sess = sessions.get(identKey(id));
  const body = ladderHtml(id, sess ? sess.plan : null, sess ? sess.st : {}, guideMount);
  return `<div class="fw-guide fw-ladder" data-fwladder="${esc(JSON.stringify(id))}">${body}</div>`;
}

// wireFirmwareLadders wires every ladder under root.
//   deps.ladder: { getLadder(host), getLadderManual(model, moduleType, version,
//     bluetooth), prepareStick(stepId, drive), prepareStickFromFile(stepId,
//     drive, path), pickFile(), cancelStick(), recheck(host), listDrives(),
//     checkStick(path), formatStick(path), onProgress(cb) -> off }
//   deps.openURL(url), deps.confirm(title, html, opts)
//   hooks.guideMount(ident) -> html, hooks.wireGuides(el)
export function wireFirmwareLadders(root, deps, hooks) {
  if (!root || !root.querySelectorAll || !deps || !deps.ladder) return;
  root.querySelectorAll('.fw-ladder[data-fwladder]').forEach((el) => {
    if (el.dataset.fwLadderWired) return;
    el.dataset.fwLadderWired = '1';
    let id;
    try { id = JSON.parse(el.dataset.fwladder); } catch { return; }
    const key = identKey(id);
    let sess = sessions.get(key);
    if (!sess) {
      sess = { plan: null, st: {}, loading: false };
      sessions.set(key, sess);
    }
    const ctl = new LadderController(el, id, sess, deps, hooks || {});
    el.addEventListener('click', e => ctl.onClick(e));
    el.addEventListener('change', e => ctl.onChange(e));
    ctl.render();
    if (!sess.plan && !sess.loading) ctl.load();
  });
}

const MANUAL_BY_ID = Object.fromEntries(MANUAL_VERSIONS.map(v => [v.id, v.version]));

class LadderController {
  constructor(el, id, sess, deps, hooks) {
    this.el = el;
    this.id = id;
    this.sess = sess;
    this.deps = deps;
    this.L = deps.ladder;
    this.hooks = hooks;
  }

  get st() { return this.sess.st; }

  set(patch) {
    Object.assign(this.sess.st, patch);
    if (this.sess.plan) {
      for (const k of ladderKeys(this.sess.plan, this.id)) saveLadderState(k, this.sess.st);
    }
    this.render();
  }

  render() {
    if (this.el.isConnected === false) return;
    const prevView = this.view;
    this.view = ladderView(this.sess.plan, this.st);
    this.el.innerHTML = ladderHtml(this.id, this.sess.plan, this.st, this.hooks.guideMount);
    if (this.hooks.wireGuides && (this.view === 'D' || this.view === 'unsupported' || this.view === 'error')) {
      this.hooks.wireGuides(this.el);
    }
    if (this.view === 'B' && prevView !== 'B' && !this.st.drives && !this.st.drivesLoading) this.refreshDrives();
  }

  async load(planner) {
    this.sess.loading = true;
    let plan = null;
    try {
      if (planner) {
        plan = await planner();
      } else {
        if (this.id.host) plan = await this.L.getLadder(this.id.host);
        if ((!plan || plan.status === 'needs-version') && this.id.current) {
          const deviceId = plan && plan.deviceId;
          plan = await this.L.getLadderManual(this.id.model, this.id.moduleType, this.id.current, '');
          if (plan && deviceId) plan.deviceId = deviceId;
          if (plan) plan.versionFrom = 'mount';
        }
      }
    } catch {
      plan = { status: 'error', steps: [] };
    }
    this.sess.loading = false;
    if (!plan) plan = { status: 'error', steps: [] };
    const prevDevice = this.sess.plan && this.sess.plan.deviceId;
    if (prevDevice && !plan.deviceId) plan.deviceId = prevDevice;
    this.sess.plan = plan;
    if (!planner) {
      this.sess.st = { ...resumeState(plan, loadLadderStateAny(ladderKeys(plan, this.id))) };
    } else {
      this.sess.st = { ...this.st, ...resumeState(plan, persistable(this.st)), manual: false };
    }
    this.render();
  }

  field(name) {
    const f = this.el.querySelector(`[data-ladder-field="${name}"]`);
    return f ? f.value : '';
  }

  onChange(e) {
    const a = e.target && e.target.dataset && e.target.dataset.ladder;
    if (a === 'ack') this.set({ ack: !!e.target.checked });
    else if (a === 'drive') this.pickDrive(e.target.value);
  }

  onClick(e) {
    const btn = e.target.closest && e.target.closest('[data-ladder]');
    if (!btn || !this.el.contains(btn)) return;
    const a = btn.dataset.ladder;
    if (a === 'ack' || a === 'drive') return;
    e.preventDefault();
    const plan = this.sess.plan;
    const model = (plan && plan.model) || this.id.model;
    switch (a) {
      case 'start': {
        const first = currentStep(plan, this.st);
        this.set({ started: true, stepId: first ? first.id : '', screen: 'B', result: null, recheck: null });
        break;
      }
      case 'manual': this.set({ manual: true }); break;
      case 'manual-go': {
        // "Another model" sends no model: the backend then plans the
        // unsupported case (or, from 15 on, the normal guide), never the
        // detected model's ladder.
        const m = this.field('model');
        const v = MANUAL_BY_ID[this.field('version')] || '';
        this.load(() => this.L.getLadderManual(m, '', v, ''));
        break;
      }
      case 'bt-yes':
      case 'bt-no':
        this.load(() => this.L.getLadderManual(model, '', (plan && plan.current) || this.id.current, a === 'bt-yes' ? 'yes' : 'no'));
        break;
      case 'refresh': this.refreshDrives(); break;
      case 'format': this.format(); break;
      case 'prepare': this.prepare(''); break;
      case 'have-file': this.haveFile(); break;
      case 'cancel': try { this.L.cancelStick(); } catch { /* nothing running */ } break;
      case 'open-source': {
        const s = currentStep(plan, this.st);
        if (s && s.url) { try { this.deps.openURL(s.url); } catch { /* no browser */ } }
        break;
      }
      case 'recheck': this.recheck(); break;
      case 'recheck-version': this.applyVersion(MANUAL_BY_ID[this.field('version')] || '', 'manual'); break;
      case 'started-yes':
      case 'started-no':
        // The answer lives with the recheck, so a new check asks again.
        if (this.st.recheck) this.set({ recheck: { ...this.st.recheck, started: a === 'started-yes' } });
        break;
      case 'retry': this.set({ screen: 'B', recheck: null, result: null, check: null, drives: null }); break;
      case 'smaller': {
        const s = currentStep(plan, this.st);
        const fb = s ? fallbacksFor(plan, s.id) : [];
        if (fb.length) {
          this.set({ fallbackFor: s.id, stepId: fb[0].id, screen: 'B', recheck: null, result: null, check: null, drives: null });
        }
        break;
      }
      case 'back-stick': this.set({ screen: 'B', recheck: null, result: null, drives: null }); break;
      case 'final-stick': {
        const steps = activeSteps(plan, this.st);
        const fin = steps.find(s => s.source === 'bose');
        this.set({ started: true, finalByStick: true, stepId: fin ? fin.id : this.st.stepId, screen: 'B', result: null, drives: null });
        break;
      }
      default: break;
    }
  }

  async refreshDrives() {
    this.st.drivesLoading = true;
    this.render();
    let drives = [];
    try { drives = (await this.L.listDrives()) || []; } catch { drives = []; }
    const keep = drives.some(d => d.path === this.st.drive) ? this.st.drive : (drives.length === 1 ? drives[0].path : '');
    Object.assign(this.st, { drives, drivesLoading: false, drive: keep, check: null });
    this.render();
    if (keep) this.pickDrive(keep);
  }

  async pickDrive(path) {
    Object.assign(this.st, { drive: path, check: null, checking: !!path, result: null });
    this.render();
    if (!path) return;
    let check = null;
    try { check = await this.L.checkStick(path); } catch { check = { ok: false, reason: 'gone' }; }
    if (this.st.drive !== path) return;
    Object.assign(this.st, { check, checking: false });
    this.render();
  }

  async format() {
    const d = (this.st.drives || []).find(x => x.path === this.st.drive);
    if (!d) return;
    if (this.deps.confirm) {
      const ok = await this.deps.confirm(t('fwLadder.w3Title'),
        `<p>${esc(t('fwLadder.w3', { label: d.label || d.path, size: formatBytes(d.totalBytes) }))}</p>`,
        { confirmLabel: t('fwLadder.format') });
      if (!ok) return;
    }
    Object.assign(this.st, { checking: true });
    this.render();
    try { await this.L.formatStick(d.path); } catch (err) {
      Object.assign(this.st, { checking: false, result: { ok: false, code: 'other', detail: String((err && err.message) || err || '') } });
      this.render();
      return;
    }
    await this.refreshDrives();
  }

  async haveFile() {
    let p = '';
    try { p = await this.L.pickFile(); } catch { p = ''; }
    if (p) this.prepare(p);
  }

  async prepare(localPath) {
    const step = currentStep(this.sess.plan, this.st);
    if (!step || !this.st.drive || this.st.busy) return;
    Object.assign(this.st, { busy: true, progress: { phase: 'start' }, result: null, justDone: '' });
    this.render();
    const off = this.L.onProgress ? this.L.onProgress((p) => {
      if (!p || p.stepId !== step.id || !this.st.busy) return;
      this.st.progress = p;
      const line = this.el.querySelector('.fw-ladder-status');
      if (line) line.textContent = ladderProgressText(p);
    }) : null;
    let res;
    try {
      res = localPath
        ? await this.L.prepareStickFromFile(step.id, this.st.drive, localPath)
        : await this.L.prepareStick(step.id, this.st.drive);
    } catch (err) {
      res = { ok: false, code: 'other', detail: String((err && err.message) || err || '') };
    } finally {
      if (typeof off === 'function') off();
    }
    this.st.busy = false;
    if (res && res.ok) {
      this.set({ screen: 'C', result: res, recheck: null, ejectedByHand: !res.ejected, drives: null, check: null });
    } else {
      this.set({ result: res || { ok: false, code: 'other' } });
    }
  }

  async recheck() {
    Object.assign(this.st, { rechecking: true, recheck: null });
    this.render();
    let r = null;
    try { r = await this.L.recheck(this.id.host || ''); } catch { r = null; }
    this.st.rechecking = false;
    if (!r || !r.found || !r.version) {
      this.set({ recheck: { state: 'unknown' } });
      return;
    }
    this.applyVersion(r.version, 'recheck');
  }

  // applyVersion moves the ladder on when the speaker reached the step, and
  // asks whether the USB update started when it did not (stalledHelp).
  async applyVersion(version, from) {
    const plan = this.sess.plan;
    const step = currentStep(plan, this.st);
    if (!step || !version) return;
    const model = plan.model || this.id.model;
    const moduleType = plan.moduleType || this.id.moduleType;
    if (!versionReached(version, step.version)) {
      // The plan may come from a version the user picked ("7 to 13"). Its
      // smaller steps would then start below where the speaker really is, so
      // the plan is built again from the version the speaker reports before
      // any smaller steps are offered.
      if (!sameVersion(version, plan.current)) {
        await this.load(() => this.L.getLadderManual(model, moduleType, version, ''));
        const now = currentStep(this.sess.plan, this.st);
        if (!now || now.id !== step.id) {
          // The new plan has no such step: go on with its own next one.
          this.set({ stepId: now ? now.id : '', screen: 'B', recheck: null, result: null, drives: null, check: null });
          return;
        }
      }
      this.set({ recheck: { state: 'same', version, from } });
      return;
    }
    await this.load(() => this.L.getLadderManual(model, moduleType, version, ''));
    const next = currentStep(this.sess.plan, { ...this.st, stepId: '' });
    this.set({ stepId: next ? next.id : '', screen: 'B', recheck: null, result: null, justDone: version, drives: null, check: null });
  }
}
