// shell2/setup/check.js: step 2 of the UI 2.0 setup wizard, the check (#1288,
// docs/UI-2.0.md "Firmware check in the setup wizard", the design in
// docs/design/ui-2.0/setup.png).
//
// CONTRACT with the wizard (wizard.js):
//
//   renderCheckStep(container, ctx) -> Promise<{ ok, firmware, route }>
//
//   container  an element the wizard owns; the step fills it and keeps it
//              filled after the promise settles, so the rows stay readable.
//   ctx.box    the speaker the find step chose: { host, name | friendlyName,
//              model }. Required; the step names it in every line.
//   ctx.signal optional AbortSignal: the wizard went Back or away. The step
//              then stops listening and settles { ok: false, route: 'aborted' }.
//              A firmware update already sent to the speaker cannot be called
//              back; it goes on, and a later call for the same speaker shows
//              its progress again.
//   ctx.onState optional ({ phase, firmware, kase }) => void, on every change,
//              so the wizard's footer can say why Install is still locked.
//              phase: checking, unreachable, needs-update, updating, failed, ok.
//   ctx.deps   optional backend calls (tests); the Wails bindings by default.
//
//   The promise settles once, when the install may go ahead:
//     ok: true   route 'current'  already on 27.0.6 or newer
//                route 'route-a'  the one-click update ran and the speaker
//                                 reported 27.0.6 back
//                route 'ladder'   the stick ladder ran and the speaker reported
//                                 27.0.6 back
//                route 'guide'    updated another way (update page, stick) and
//                                 the speaker reported 27.0.6 back
//                route 'skip'     "Continue without updating" (15.x to 27.0.5
//                                 only, where the install over the network
//                                 still starts)
//                route 'unknown'  the version could not be read and the user
//                                 continued
//     ok: false  route 'aborted'  ctx.signal fired first
//   firmware is the short version the speaker last reported ('' if none).
//   "Done" is never assumed from a finished upload: only GetBoxFirmware
//   reading the new version back confirms an update.
//
// What it reuses rather than repeats: the firmware guide and the stick ladder
// are the classic modules (fwguide.js firmwareGuideMount and
// wireFirmwareGuides, which mount fwladder.js for a first generation
// SoundTouch 20/30/Portable below 15), rendered inside the step with the same
// backend calls as the classic setup screen. The one-click update is the same
// FlashBoseFirmware the guide's "Update now" calls, with the guide's own
// confirmation and progress words (fwFlashStatus); the step adds the read-back
// and names the speaker. In the "Other ways" block the guide's own Update now
// is hidden by CSS (check.css), so one speaker never gets two update buttons.
//
// Only the wizard imports this module, and it touches the DOM only inside
// renderCheckStep (never at module scope: views load before main.js builds
// the #view-* containers).

import { t } from '../../i18n/index.js';
import { confirmWarn, escapeHtml } from '../../utils.js';
import {
  BrowserOpenURL,
  ClipboardSetText,
  EventsOn,
  FlashBoseFirmware,
  GetBoxFirmware,
  GetFirmwareGuide,
} from '../../api.js';
import { LATEST_BOSE_FIRMWARE } from '../../firmware.js';
import {
  firmwareGuideMount, fwFlashStatus, fwGuideFlash, fwGuidePlan, watchFwGuideFlash, wireFirmwareGuides,
} from '../../fwguide.js';
import { isBatteryModel } from '../../fwladder.js';
import { firmwareLadderDeps } from '../../fwladderdeps.js';
import {
  STICK_ROUTE_URL,
  canSkip,
  checkCase,
  checkRows,
  firmwareConfirmed,
  identOf,
  rowsHtml,
  shortOf,
  speakerLabel,
  stepPhase,
} from './checkmodel.js';
import './check.css';

// A one-click update per speaker, for this app session: leaving the step does
// not stop the speaker's update, and coming back picks the progress up.
//   host -> { phase, text, promise, external, timer, wake }
// external: an update the step did not start itself (a guide's or the
// ladder's own "Update now", or one still running from before a reload of the
// window). It has no promise; its progress lines and the backend's "done"
// drive it, and a long silence ends it with a fresh read of the speaker.
const flashes = new Map();

// The backend sends nothing while the speaker installs and restarts (up to
// twelve minutes, app_fwflash.go) and no line at all for a failed update, so
// an update the step only watches counts as over after this much silence.
export const EXTERNAL_FLASH_QUIET_MS = 15 * 60 * 1000;

// A running update outlives a reload of the window (the language or layout
// switch): the Go call goes on, the page's memory does not. The host is kept
// in sessionStorage while it runs, so the step after the reload holds the
// wizard instead of offering the same update a second time. Without storage
// (a private window) only the progress lines can tell.
const MARK_KEY = 'str.ui2.fwFlashRunning';

function readMarks() {
  try {
    const raw = typeof sessionStorage !== 'undefined' ? sessionStorage.getItem(MARK_KEY) : null;
    const m = raw ? JSON.parse(raw) : {};
    return m && typeof m === 'object' ? m : {};
  } catch { return {}; }
}

function writeMarks(m) {
  try {
    if (typeof sessionStorage === 'undefined') return;
    if (Object.keys(m).length) sessionStorage.setItem(MARK_KEY, JSON.stringify(m));
    else sessionStorage.removeItem(MARK_KEY);
  } catch { /* no storage: the progress lines still tell */ }
}

export function markFlash(host, phase, nowMs = Date.now()) {
  if (!host) return;
  const m = readMarks();
  m[host] = { at: nowMs, phase: phase || 'start' };
  writeMarks(m);
}

export function clearFlashMark(host) {
  const m = readMarks();
  if (!host || !(host in m)) return;
  delete m[host];
  writeMarks(m);
}

// flashMark is the stored running update for host, or null when none is
// stored or it went quiet too long ago.
export function flashMark(host, nowMs = Date.now()) {
  const e = host ? readMarks()[host] : null;
  if (!e || typeof e.at !== 'number') return null;
  if (nowMs - e.at > EXTERNAL_FLASH_QUIET_MS) { clearFlashMark(host); return null; }
  return { phase: String(e.phase || 'start') };
}

const runningFlash = (st) => !!st && !!st.phase && st.phase !== 'error' && st.phase !== 'done';

// endFlash closes an update entry and forgets its mark.
function endFlash(host, st, phase, text) {
  if (!st) return;
  st.phase = phase;
  st.text = text || '';
  if (st.timer) { clearTimeout(st.timer); st.timer = null; }
  clearFlashMark(host);
}

// trackExternal records (or refreshes) an update the step did not start and
// arms its silence timer. Returns the entry.
function trackExternal(host, phase, text) {
  let st = flashes.get(host);
  if (!runningFlash(st)) {
    st = { phase: phase || 'start', text: text || '', promise: null, external: true, timer: null, wake: null };
    flashes.set(host, st);
  } else {
    st.phase = phase || st.phase;
    if (text) st.text = text;
  }
  markFlash(host, st.phase);
  if (st.external) {
    if (st.timer) clearTimeout(st.timer);
    st.timer = setTimeout(() => {
      st.timer = null;
      if (!runningFlash(st)) return;
      endFlash(host, st, 'done', '');
      if (typeof st.wake === 'function') st.wake();
    }, EXTERNAL_FLASH_QUIET_MS);
  }
  return st;
}

export function checkDeps(name) {
  return {
    getFirmware: GetBoxFirmware,
    getGuide: GetFirmwareGuide,
    flash: FlashBoseFirmware,
    onProgress: (cb) => EventsOn('firmware:progress', cb),
    openURL: BrowserOpenURL,
    copy: ClipboardSetText,
    // The guide's confirmations, titled with the speaker (rule 1).
    confirm: (title, body, opts) => confirmWarn(
      name ? t('ui2.speakers.speaker.confirmTitle', { name, action: title }) : title, body, opts),
    ladder: firmwareLadderDeps(),
  };
}

function now() {
  return (typeof performance !== 'undefined' && performance.now) ? performance.now() : Date.now();
}

let seq = 0;

export function renderCheckStep(container, ctx) {
  const c = ctx || {};
  const box = c.box || {};
  const host = String(box.host || '').trim();
  const name = speakerLabel(box);
  const deps = { ...checkDeps(name), ...(c.deps || {}) };
  const id = `uCheck${++seq}`;

  return new Promise((resolve) => {
    let settled = false;
    let offProgress = null;
    let offGuide = null;
    // view: what the step knows. kase is checkmodel.js checkCase.
    const view = { box, fw: null, kase: '', ms: null, loading: true, reading: false, confirmed: false, flash: null, guide: null, guideFor: '', readBack: '' };
    let guideKey = '';
    let lastRows = '';
    let lastCallout = '';

    if (!container) { resolve({ ok: false, firmware: '', route: 'aborted' }); return; }

    container.innerHTML = `<section class="u-check" aria-labelledby="${id}T" data-check-host="${escapeHtml(host)}">`
      + `<h2 class="u-check-title" id="${id}T">${escapeHtml(t('ui2.setup.check.title', { name }))}</h2>`
      + '<div class="u-check-rowsbox"></div>'
      + '<div class="u-check-callout"></div>'
      // The one line a screen reader hears: a phase changed, not every
      // percent of a download (the callout holds a whole block).
      + '<p class="u-sr-only u-check-live" role="status" aria-live="polite"></p>'
      + '<div class="u-check-guide"></div>'
      + '<div class="u-check-actions">'
      + `<button type="button" class="u-btn u-btn--secondary" data-check="recheck" hidden>${escapeHtml(t('ui2.setup.check.recheck', { name }))}</button>`
      + `<button type="button" class="u-btn u-btn--secondary" data-check="skip" hidden aria-describedby="${id}R">${escapeHtml(t('ui2.setup.check.skip', { name }))}</button>`
      + `<p class="u-check-risk" id="${id}R" hidden></p>`
      + '</div>'
      + '</section>';
    const root = container.querySelector('.u-check');
    const rowsBox = root.querySelector('.u-check-rowsbox');
    const callout = root.querySelector('.u-check-callout');
    const live = root.querySelector('.u-check-live');
    const guideBox = root.querySelector('.u-check-guide');
    const recheckBtn = root.querySelector('[data-check="recheck"]');
    const skipBtn = root.querySelector('[data-check="skip"]');
    const risk = root.querySelector('.u-check-risk');

    const short = () => shortOf(view.fw);
    const finish = (ok, route) => {
      if (settled) return;
      settled = true;
      if (typeof offProgress === 'function') { try { offProgress(); } catch { /* gone */ } offProgress = null; }
      if (typeof offGuide === 'function') { try { offGuide(); } catch { /* gone */ } offGuide = null; }
      resolve({ ok, firmware: short(), route });
    };
    const report = () => {
      if (typeof c.onState !== 'function') return;
      try { c.onState({ phase: stepPhase(view), firmware: short(), kase: view.kase }); } catch { /* the wizard's problem */ }
    };

    // ---- rendering ----

    const calloutHtml = () => {
      const v = short();
      if (view.loading) return `<p class="u-check-line">${escapeHtml(t('ui2.setup.check.reading', { name }))}</p>`;
      switch (view.kase) {
        case 'unreachable':
          return `<p class="u-check-line u-check-line--err">${escapeHtml(t('ui2.setup.check.reachNo', { name, host }))}</p>`;
        case 'unknown':
          return `<p class="u-check-line">${escapeHtml(t('ui2.setup.check.unknownBody', { name }))}</p>`;
        case 'current':
          return `<p class="u-check-line u-check-line--ok">${escapeHtml(t('ui2.setup.check.currentBody', { name, version: v }))}</p>`;
        default:
          break;
      }
      if (view.confirmed) {
        return `<p class="u-check-line u-check-line--ok">${escapeHtml(t('ui2.setup.check.updateDone', { name, version: v }))}</p>`;
      }
      if (view.kase !== 'routeA') {
        // One neutral line for the guide and the ladder alike: the ladder can
        // still turn into the single-step guide (its Bluetooth question), and
        // the line above must never promise what the guide below denies.
        return `<p class="u-check-line">${escapeHtml(t('ui2.setup.check.guideIntro', { name, version: v, required: LATEST_BOSE_FIRMWARE }))}</p>`
          + `<p class="u-check-line u-check-muted">${escapeHtml(t('ui2.setup.check.noSkip', { name }))}</p>`;
      }
      // routeA: the one-click update is the main action.
      const plan = view.guide ? fwGuidePlan(identOf(view.fw, box), view.guide) : null;
      const fl = view.flash;
      const running = fl && fl.phase !== 'error' && fl.phase !== 'done';
      let out = '<div class="u-check-routea">'
        + `<h3 class="u-check-routea-title">${escapeHtml(t('ui2.setup.check.routeATitle', { required: LATEST_BOSE_FIRMWARE }))}</h3>`
        + `<p class="u-check-line">${escapeHtml(t('ui2.setup.check.routeABody', { name }))}</p>`;
      if (!plan) {
        out += `<p class="u-check-line u-check-muted">${escapeHtml(t('ui2.setup.check.lookingUp', { name }))}</p>`;
      } else if (!plan.auto) {
        out += `<p class="u-check-line">${escapeHtml(t('ui2.setup.check.noAuto', { name }))}</p>`;
      } else {
        out += '<div class="u-check-routea-act">'
          // aria-disabled, not disabled, while the update runs: the button
          // keeps the keyboard focus through every progress line.
          + `<button type="button" class="u-btn u-btn--primary" data-check="flash"${running ? ' aria-disabled="true"' : ''}>`
          + `${escapeHtml(t(fl && fl.phase === 'error' ? 'ui2.setup.check.retry' : 'ui2.setup.check.routeABtn', { name }))}</button>`
          + '</div>';
      }
      if (fl && fl.text) {
        out += `<p class="u-check-line u-check-progress${fl.phase === 'error' ? ' u-check-line--err' : ''}" data-check-progress="${escapeHtml(fl.phase)}">${escapeHtml(fl.text)}</p>`;
      }
      if (view.readBack) {
        out += `<p class="u-check-line u-check-line--err">${escapeHtml(view.readBack)}</p>`;
      }
      return out + '</div>';
    };

    // liveText is the short line the live region says for the step's state.
    // Progress counts by phase only, so a download is not read out per percent.
    const liveText = () => {
      const v = short();
      if (view.loading) return t('ui2.setup.check.reading', { name });
      if (view.kase === 'unreachable') return t('ui2.setup.check.reachNo', { name, host });
      if (view.kase === 'current') return t('ui2.setup.check.currentBody', { name, version: v });
      if (view.confirmed) return t('ui2.setup.check.updateDone', { name, version: v });
      if (view.readBack) return view.readBack;
      const fl = view.flash;
      if (fl && fl.phase === 'error') return fl.text || '';
      if (runningFlash(fl)) return fwFlashStatus({ phase: fl.phase });
      if (view.kase === 'unknown') return t('ui2.setup.check.unknownBody', { name });
      if (view.kase === 'routeA') return t('ui2.setup.check.routeATitle', { required: LATEST_BOSE_FIRMWARE });
      return t('ui2.setup.check.guideIntro', { name, version: v, required: LATEST_BOSE_FIRMWARE });
    };

    // The guide or the ladder renders once per case and speaker: they keep
    // their own state (a stick picked, a step reached) and redrawing them on
    // every progress line would throw it away.
    const renderGuide = () => {
      const want = view.loading || view.confirmed || !['routeA', 'ladder', 'guide'].includes(view.kase)
        ? ''
        : `${view.kase}|${short()}|${view.kase === 'routeA' && view.guide ? fwGuidePlan(identOf(view.fw, box), view.guide).auto : ''}`;
      if (want === guideKey) return;
      guideKey = want;
      if (!want) { guideBox.innerHTML = ''; return; }
      const mount = firmwareGuideMount(identOf(view.fw, box));
      if (view.kase === 'routeA') {
        const plan = view.guide ? fwGuidePlan(identOf(view.fw, box), view.guide) : null;
        const other = `<p class="u-check-line"><a href="#" class="u-check-link" data-check-open="${escapeHtml(STICK_ROUTE_URL)}">${escapeHtml(t('ui2.setup.check.stickRoute'))}</a></p>`;
        // Without a one-click update the steps by hand ARE the way, so they
        // show open; with it they are the fallback, folded.
        guideBox.innerHTML = `<details class="u-check-other"${plan && !plan.auto ? ' open' : ''}>`
          + `<summary class="u-check-other-sum">${escapeHtml(t('ui2.setup.check.otherWays'))}</summary>`
          + `<div class="u-check-other-body">${mount}${other}</div></details>`;
      } else {
        guideBox.innerHTML = `<div class="u-check-fw">${mount}</div>`;
      }
      wireFirmwareGuides(guideBox, deps);
    };

    const render = () => {
      // Only what changed is written: a redrawn button loses the keyboard
      // focus, and the live line is read out on every change.
      const rows = view.loading
        ? rowsHtml([{ id: 'reach', titleKey: 'ui2.setup.check.reach', detail: t('ui2.setup.check.reading', { name }), status: 'wait' }])
        : rowsHtml(checkRows(view));
      if (rows !== lastRows) { rowsBox.innerHTML = rows; lastRows = rows; }
      const said = calloutHtml();
      if (said !== lastCallout) {
        const focused = callout.contains(document.activeElement) && document.activeElement.dataset.check;
        callout.innerHTML = said;
        lastCallout = said;
        // The update button is redrawn with its new label (Try again): keep
        // the keyboard where it was.
        if (focused) { const b = callout.querySelector(`[data-check="${focused}"]`); if (b) b.focus(); }
      }
      renderGuide();
      const say = liveText();
      if (live.textContent !== say) live.textContent = say;
      const fl = view.flash;
      const running = fl && fl.phase !== 'error' && fl.phase !== 'done';
      recheckBtn.hidden = view.loading || view.kase === 'current' || view.confirmed;
      recheckBtn.disabled = !!running || !!view.reading;
      const skippable = !view.loading && !view.confirmed && canSkip(view.kase) && !running && !settled;
      skipBtn.hidden = !skippable;
      risk.hidden = !skippable;
      risk.textContent = !skippable ? '' : (view.kase === 'unknown'
        ? t('ui2.setup.check.skipRiskUnknown', { name })
        : t('ui2.setup.check.skipRisk', { name, version: short(), required: LATEST_BOSE_FIRMWARE }));
      root.dataset.case = view.loading ? 'loading' : view.kase;
      root.dataset.phase = stepPhase(view);
      report();
    };

    // ---- reading the speaker ----

    const lookUpGuide = () => {
      const ident = identOf(view.fw, box);
      const key = `${ident.model}|${ident.moduleType}|${ident.variant}|${ident.current}`;
      if (view.guideFor === key) return;
      view.guideFor = key;
      view.guide = null;
      Promise.resolve()
        .then(() => deps.getGuide(ident.model, ident.moduleType, ident.variant, ident.current))
        .then((g) => { if (view.guideFor === key) { view.guide = g || { files: [] }; render(); } })
        .catch(() => { if (view.guideFor === key) { view.guide = { files: [] }; render(); } });
    };

    // read asks the speaker for its firmware. from: 'start', 'recheck' or
    // 'flash' (after an update, to confirm it).
    const read = async (from) => {
      view.loading = from === 'start';
      view.reading = true;
      render();
      const t0 = now();
      let fw = null;
      try { fw = await deps.getFirmware(host); } catch { fw = null; }
      if (settled && from !== 'flash') return;
      view.loading = false;
      view.reading = false;
      view.ms = now() - t0;
      const before = view.kase;
      if (fw && fw.reachable) view.fw = fw;
      else if (from === 'start' || !view.fw) view.fw = fw;
      const reached = firmwareConfirmed(fw);
      if (reached) {
        // On 27.0.6 now: whatever update was running is through.
        const st = flashes.get(host);
        if (st && st.external && runningFlash(st)) endFlash(host, st, 'done', '');
        if (runningFlash(view.flash)) view.flash = st ? { phase: st.phase, text: st.text } : null;
        clearFlashMark(host);
      }
      if (from === 'start') {
        view.kase = checkCase(fw, box);
      } else if (!fw || !fw.reachable) {
        // Gone quiet after a check or an update: say so where it happened.
        if (from === 'flash') view.readBack = t('ui2.setup.check.updateNotBack', { name });
        else view.kase = 'unreachable';
      } else if (reached && ['routeA', 'ladder', 'guide'].includes(before)) {
        view.confirmed = true;
        view.readBack = '';
      } else {
        view.kase = checkCase(fw, box);
        if (from === 'flash') {
          view.readBack = t('ui2.setup.check.updateNotReached', { name, version: shortOf(fw) || '?', required: LATEST_BOSE_FIRMWARE });
        }
      }
      if (view.kase === 'routeA' && !view.confirmed) lookUpGuide();
      render();
      if (view.kase === 'current' && !view.confirmed) finish(true, 'current');
      else if (view.confirmed) {
        const fl = flashes.get(host);
        const route = (from === 'flash' || (fl && fl.phase === 'done')) ? 'route-a' : (before === 'ladder' ? 'ladder' : 'guide');
        finish(true, route);
      }
    };

    // ---- the one-click update ----

    const showFlash = (st) => {
      view.flash = st ? { phase: st.phase, text: st.text } : null;
      render();
    };

    const startFlash = async () => {
      const running = flashes.get(host);
      if (running && running.phase !== 'error' && running.phase !== 'done') return;
      if (deps.confirm) {
        const body = `<p>${escapeHtml(t('fwGuide.autoConfirmBody'))}</p>`
          + (isBatteryModel(identOf(view.fw, box).model) ? `<p><b>${escapeHtml(t('fwGuide.autoConfirmBattery'))}</b></p>` : '');
        let ok = false;
        try {
          ok = await deps.confirm(t('fwGuide.autoConfirmTitle'), body,
            { calm: true, confirmLabel: t('fwGuide.autoBtn'), confirmClass: 'btn btn-primary' });
        } catch { ok = false; }
        if (!ok) return;
      }
      view.readBack = '';
      const st = { phase: 'start', text: fwFlashStatus({ phase: 'start' }), promise: null };
      flashes.set(host, st);
      markFlash(host, 'start');
      st.promise = Promise.resolve()
        .then(() => deps.flash(host, false))
        .then((res) => { endFlash(host, st, 'done', ''); return res; },
          (err) => {
            endFlash(host, st, 'error', fwFlashStatus({ phase: 'error', error: String((err && err.message) || err || '') }));
          });
      follow(st);
    };

    // follow shows one running update and confirms it when it ends. An
    // update the step only watches (no promise) ends by its "done" line or
    // its silence, which wakes the step for a fresh read.
    const follow = (st) => {
      showFlash(st);
      if (!st.promise) {
        st.wake = () => { if (!settled) { showFlash(st); read('recheck'); } };
        return;
      }
      st.promise.then(() => {
        if (settled) return;
        showFlash(st);
        if (st.phase === 'done') read('flash');
      });
    };

    // Progress lines from the backend. An update for this speaker that is
    // not the step's own (a guide's or the ladder's own Update now, or one
    // from before a reload) is tracked too: it holds the step (phase
    // 'updating': Back, the USB stick and leaving wait) exactly like the
    // step's own, and its "done" is read back so the step sees the version.
    offProgress = typeof deps.onProgress === 'function' ? deps.onProgress((p) => {
      if (!p || p.host !== host || settled) return;
      const st = flashes.get(host);
      if (p.phase === 'done') {
        if (runningFlash(st) && !st.external) return; // its own promise confirms it
        if (runningFlash(st)) { endFlash(host, st, 'done', ''); showFlash(st); }
        read('recheck');
        return;
      }
      if (runningFlash(st) && !st.external) {
        st.phase = p.phase || st.phase;
        st.text = fwFlashStatus(p);
        markFlash(host, st.phase);
        showFlash(st);
        return;
      }
      follow(trackExternal(host, p.phase, fwFlashStatus(p)));
    }) : null;

    // The guide's own update (fwguide.js keeps it): its start, its lines and
    // its end, which also tells a failed update.
    offGuide = watchFwGuideFlash((h, g) => {
      if (h !== host || settled || !g) return;
      const st = flashes.get(host);
      if (g.phase === 'done' || g.phase === 'error') {
        if (runningFlash(st) && st.external) { endFlash(host, st, g.phase, g.phase === 'error' ? g.text : ''); showFlash(st); }
        if (g.phase === 'done') read('recheck');
        return;
      }
      if (runningFlash(st) && !st.external) return;
      follow(trackExternal(host, g.phase, g.text));
    });

    // ---- clicks ----

    root.addEventListener('click', (e) => {
      const open = e.target.closest && e.target.closest('[data-check-open]');
      if (open && root.contains(open)) {
        e.preventDefault();
        try { deps.openURL(open.dataset.checkOpen); } catch { /* no browser */ }
        return;
      }
      const b = e.target.closest && e.target.closest('[data-check]');
      if (!b || !root.contains(b) || b.disabled || b.getAttribute('aria-disabled') === 'true') return;
      switch (b.dataset.check) {
        case 'recheck': read('recheck'); break;
        case 'flash': startFlash(); break;
        case 'skip':
          finish(true, view.kase === 'unknown' ? 'unknown' : 'skip');
          render();
          break;
        default: break;
      }
    });

    if (c.signal) {
      if (c.signal.aborted) { finish(false, 'aborted'); return; }
      c.signal.addEventListener('abort', () => finish(false, 'aborted'), { once: true });
    }

    // An update started earlier is still running: show it instead of
    // offering it again. Earlier in this session (the step's own or the
    // guide's), or before a reload of the window (the stored mark).
    let earlier = flashes.get(host);
    if (!runningFlash(earlier)) {
      const g = fwGuideFlash(host);
      const mk = flashMark(host);
      if (runningFlash(g)) earlier = trackExternal(host, g.phase, g.text);
      else if (mk) earlier = trackExternal(host, mk.phase, fwFlashStatus({ phase: mk.phase }));
    }
    // Held from the first paint: the read below may find the speaker
    // restarting, and the update must not be offered meanwhile.
    if (runningFlash(earlier)) view.flash = { phase: earlier.phase, text: earlier.text };
    read('start').then(() => {
      if (runningFlash(earlier) && !settled) follow(earlier);
    });
  });
}
