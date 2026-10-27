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
import { firmwareGuideMount, fwFlashStatus, fwGuidePlan, wireFirmwareGuides } from '../../fwguide.js';
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
//   host -> { phase, text, promise }
const flashes = new Map();

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
    // view: what the step knows. kase is checkmodel.js checkCase.
    const view = { box, fw: null, kase: '', ms: null, loading: true, reading: false, confirmed: false, flash: null, guide: null, guideFor: '', readBack: '' };
    let guideKey = '';
    let lastRows = '';
    let lastCallout = '';

    if (!container) { resolve({ ok: false, firmware: '', route: 'aborted' }); return; }

    container.innerHTML = `<section class="u-check" aria-labelledby="${id}T" data-check-host="${escapeHtml(host)}">`
      + `<h2 class="u-check-title" id="${id}T">${escapeHtml(t('ui2.setup.check.title', { name }))}</h2>`
      + '<div class="u-check-rowsbox"></div>'
      + '<div class="u-check-callout" role="status" aria-live="polite"></div>'
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
    const guideBox = root.querySelector('.u-check-guide');
    const recheckBtn = root.querySelector('[data-check="recheck"]');
    const skipBtn = root.querySelector('[data-check="skip"]');
    const risk = root.querySelector('.u-check-risk');

    const short = () => shortOf(view.fw);
    const finish = (ok, route) => {
      if (settled) return;
      settled = true;
      if (typeof offProgress === 'function') { try { offProgress(); } catch { /* gone */ } offProgress = null; }
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
        const key = view.kase === 'ladder' ? 'ui2.setup.check.ladderIntro' : 'ui2.setup.check.guideIntro';
        return `<p class="u-check-line">${escapeHtml(t(key, { name, version: v, required: LATEST_BOSE_FIRMWARE }))}</p>`
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
      // Only what changed is written: the callout is a polite live region,
      // and an unchanged line rewritten would be read out again.
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
      st.promise = Promise.resolve()
        .then(() => deps.flash(host, false))
        .then((res) => { st.phase = 'done'; st.text = ''; return res; },
          (err) => {
            st.phase = 'error';
            st.text = fwFlashStatus({ phase: 'error', error: String((err && err.message) || err || '') });
          });
      follow(st);
    };

    // follow shows one running update and confirms it when it ends.
    const follow = (st) => {
      showFlash(st);
      st.promise.then(() => {
        if (settled) return;
        showFlash(st);
        if (st.phase === 'done') read('flash');
      });
    };

    // Progress lines from the backend. A "done" for this speaker that is not
    // the step's own update (the ladder's last step, or Update now in the
    // other ways) is read back too, so the step notices the new version.
    offProgress = typeof deps.onProgress === 'function' ? deps.onProgress((p) => {
      if (!p || p.host !== host || settled) return;
      const st = flashes.get(host);
      if (p.phase === 'done') {
        if (!st || st.phase === 'error' || st.phase === 'done') read('recheck');
        return;
      }
      if (st && st.phase !== 'error' && st.phase !== 'done') {
        st.phase = p.phase || st.phase;
        st.text = fwFlashStatus(p);
        showFlash(st);
      }
    }) : null;

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

    // An update started earlier in this session is still running: show it
    // instead of offering it again.
    const earlier = flashes.get(host);
    read('start').then(() => {
      if (earlier && earlier.phase !== 'error' && earlier.phase !== 'done' && !settled) follow(earlier);
    });
  });
}
