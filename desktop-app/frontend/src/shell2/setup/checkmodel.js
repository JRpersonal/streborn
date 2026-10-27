// shell2/setup/checkmodel.js: the decisions of the setup wizard's check step
// (check.js), as data and markup strings. Pure: no DOM, no backend, so vitest
// covers every firmware case without either.
//
// The firmware facts are not decided here. They come from the modules the
// classic layout uses for the same speaker:
//   firmware.js   LATEST_BOSE_FIRMWARE, the last version Bose shipped (27.0.6)
//   fwladder.js   versionReached, ladderApplies (the first generation
//                 SoundTouch 20/30/Portable below 15 get the stick ladder),
//                 LADDER_BELOW_MAJOR (from 15 on, Bose's last file goes on in
//                 one step), FIRMWARE_DOCS_URL
// and the install gate mirrors unlockMinFirmwareMajor in install_str.go: below
// 15 the install over the network refuses to start, so the step offers no
// "continue without updating" there.

import { t } from '../../i18n/index.js';
import { escapeHtml } from '../../utils.js';
import { LATEST_BOSE_FIRMWARE } from '../../firmware.js';
import { FIRMWARE_DOCS_URL, LADDER_BELOW_MAJOR, ladderApplies, versionReached } from '../../fwladder.js';

// Route C (the USB stick) in docs/FIRMWARE-UPDATE.md: the fallback the step
// names next to the one-click update.
export const STICK_ROUTE_URL = `${FIRMWARE_DOCS_URL}#5-route-c-usb-stick`;

// The cases of the step, one per firmware situation:
//   unreachable  the speaker's :8090/info did not answer
//   unknown      it answered without a readable version
//   current      27.0.6 or newer: nothing to do
//   routeA       15.x up to 27.0.5: the one-click update over the network
//   ladder       below 15 on a first generation SoundTouch 20/30/Portable
//   guide        below 15 on any other model: the single-step guide
export const CHECK_CASES = Object.freeze(['unreachable', 'unknown', 'current', 'routeA', 'ladder', 'guide']);

function majorOf(v) {
  const m = String(v || '').trim().match(/^(\d+)\./);
  return m ? parseInt(m[1], 10) : null;
}

// shortOf is the dotted version the step shows ("27.0.6" from
// "27.0.6.46330.5043500").
export function shortOf(fw) {
  const f = fw || {};
  const raw = String(f.short || f.firmware || '').trim();
  const m = raw.match(/^(\d+\.\d+\.\d+)/);
  return m ? m[1] : '';
}

// identOf is the speaker identity the firmware guide and the ladder take
// (fwguide.js firmwareGuideMount), from GetBoxFirmware's answer and the box
// the wizard chose.
export function identOf(fw, box) {
  const f = fw || {};
  const b = box || {};
  return {
    model: f.model || b.model || b.type || '',
    moduleType: f.moduleType || '',
    variant: f.variant || '',
    current: f.firmware || f.short || '',
    host: b.host || '',
  };
}

// checkCase picks the case for one GetBoxFirmware answer (null: no answer).
export function checkCase(fw, box) {
  if (!fw || !fw.reachable) return 'unreachable';
  const short = shortOf(fw);
  if (!short) return 'unknown';
  if (versionReached(short, LATEST_BOSE_FIRMWARE)) return 'current';
  const mj = majorOf(short);
  if (mj !== null && mj >= LADDER_BELOW_MAJOR) return 'routeA';
  return ladderApplies(identOf(fw, box)) ? 'ladder' : 'guide';
}

// canSkip: where the classic flow lets the install go ahead on this firmware.
// The install over the network refuses below 15 (install_str.go,
// "firmware-too-old"), so only an unreadable version (no evidence is no
// reason to block, as there) and 15.x up to 27.0.5 may continue without the
// update.
export function canSkip(kase) {
  return kase === 'routeA' || kase === 'unknown';
}

// firmwareConfirmed is the one test for "done": the version the speaker
// reports back has reached Bose's last one.
export function firmwareConfirmed(fw) {
  return !!(fw && fw.reachable) && versionReached(shortOf(fw), LATEST_BOSE_FIRMWARE);
}

// speakerLabel is the name every line of the step uses for the speaker.
export function speakerLabel(box) {
  const b = box || {};
  return String(b.name || b.friendlyName || b.host || '').trim();
}

// hardwareKey names the chassis where the speaker says which one it is
// (moduleType, docs/MODEL-VARIANTS.md); anything else shows the model alone.
export function hardwareKey(moduleType) {
  const m = String(moduleType || '').trim().toLowerCase();
  if (m === 'sm2') return 'ui2.setup.check.hwNewer';
  if (m === 'scm') return 'ui2.setup.check.hwOlder';
  return '';
}

// The status word and tone of each row state. The tone is a token name
// (--u-<tone>); the words always carry the state, and a mark beside them
// repeats it in shape, so nothing rests on colour alone.
const ROW_STATUS = Object.freeze({
  ok: { key: 'ui2.setup.check.stOk', tone: 'ok', mark: '✓' },
  warn: { key: 'ui2.setup.check.stNeedsUpdate', tone: 'warn', mark: '!' },
  busy: { key: 'ui2.setup.check.stUpdating', tone: 'accent', mark: '…' },
  wait: { key: 'ui2.setup.check.stChecking', tone: 'muted', mark: '…' },
  error: { key: 'ui2.setup.check.stError', tone: 'danger', mark: '×' },
  unknown: { key: 'ui2.setup.check.stUnknown', tone: 'warn', mark: '?' },
});

export function rowStatus(kind) {
  return ROW_STATUS[kind] || ROW_STATUS.wait;
}

// checkRows is the list on top of the step: reachable, model, firmware.
//   view: { kase, fw, box, ms, flash: { phase }, confirmed, readBack }
export function checkRows(view) {
  const v = view || {};
  const fw = v.fw || {};
  const name = speakerLabel(v.box);
  const host = (v.box && v.box.host) || '';
  const rows = [];
  if (v.kase === 'unreachable') {
    rows.push({ id: 'reach', titleKey: 'ui2.setup.check.reach', detail: t('ui2.setup.check.reachNo', { name, host }), status: 'error' });
    return rows;
  }
  rows.push({
    id: 'reach',
    titleKey: 'ui2.setup.check.reach',
    detail: typeof v.ms === 'number' ? t('ui2.setup.check.reachMs', { ms: Math.max(1, Math.round(v.ms)) }) : t('ui2.setup.check.reachYes'),
    status: 'ok',
  });
  const model = fw.model || (v.box && v.box.model) || '';
  const hw = hardwareKey(fw.moduleType);
  rows.push({
    id: 'model',
    titleKey: 'ui2.setup.check.model',
    detail: model ? (hw ? t('ui2.setup.check.modelHw', { model, hardware: t(hw) }) : model) : t('ui2.setup.check.modelUnknown'),
    status: model ? 'ok' : 'unknown',
  });
  const short = shortOf(fw);
  let detail;
  let status;
  if (v.kase === 'unknown') {
    detail = t('ui2.setup.check.fwUnknown', { name });
    status = 'unknown';
  } else if (v.confirmed || v.kase === 'current') {
    detail = v.confirmed
      ? t('ui2.setup.check.fwConfirmed', { name, version: short })
      : t('ui2.setup.check.fwCurrent', { version: short });
    status = 'ok';
  } else if (v.flash && v.flash.phase && v.flash.phase !== 'error' && v.flash.phase !== 'done') {
    detail = t('ui2.setup.check.fwUpdating', { name, version: short, required: LATEST_BOSE_FIRMWARE });
    status = 'busy';
  } else {
    detail = t('ui2.setup.check.fwOld', { version: short, required: LATEST_BOSE_FIRMWARE });
    status = 'warn';
  }
  rows.push({ id: 'firmware', titleKey: 'ui2.setup.check.firmware', detail, status });
  return rows;
}

export function rowsHtml(rows) {
  return '<ul class="u-check-rows">' + (rows || []).map((r) => {
    const st = rowStatus(r.status);
    return `<li class="u-check-row" data-row="${escapeHtml(r.id)}" data-status="${escapeHtml(r.status)}">`
      + `<span class="u-check-mark u-check-mark--${st.tone}" aria-hidden="true">${st.mark}</span>`
      + '<span class="u-check-row-text">'
      + `<span class="u-check-row-title">${escapeHtml(t(r.titleKey))}</span>`
      + `<span class="u-check-row-detail">${escapeHtml(r.detail)}</span>`
      + '</span>'
      + `<span class="u-check-row-status u-check-row-status--${st.tone}">${escapeHtml(t(st.key))}</span>`
      + '</li>';
  }).join('') + '</ul>';
}

// The step's state for the wizard (ctx.onState), so its footer can say why
// Install is still locked: checking, unreachable, needs-update, updating,
// failed, ok.
export function stepPhase(view) {
  const v = view || {};
  if (v.loading) return 'checking';
  if (v.kase === 'unreachable') return 'unreachable';
  if (v.confirmed || v.kase === 'current') return 'ok';
  if (v.flash && v.flash.phase === 'error') return 'failed';
  if (v.flash && v.flash.phase && v.flash.phase !== 'done') return 'updating';
  return 'needs-update';
}
