// The setup wizard's firmware gate for the USB stick.
//
// The install over the network stops at "firmware-too-old" for a speaker whose
// Bose firmware is far behind 27.0.6, but the USB stick section beside it used
// to write STR's install files anyway. In a support case (2026-11) a SoundTouch
// 20 on 9.0.41 got the 14 install files on its stick two minutes after the
// install had said firmware-too-old; the owner then put Bose's Update.stu
// beside them for the firmware ladder, and the speaker never started that
// update. Now the stick's Prepare asks the backend first
// (StickInstallFirmwareGate) and, for such a speaker, shows the firmware
// update guide with its stick steps instead of writing anything.

import { t } from './i18n/index.js';
import { escapeHtml } from './utils.js';
import { firmwareGuideMount, firmwareGuideNeeded } from './fwguide.js';

// stickGateHost is the speaker the stick is being prepared for: the target
// picked in the wizard's first step. A factory-reset speaker the wizard does
// not know yet has none, and is not gated.
export function stickGateHost(target) {
  const box = target && target.box;
  return String((box && box.host) || '').trim();
}

// stickFirmwareGate asks the backend whether the stick may be written for
// host. Resolves to the gate when the speaker needs the firmware update
// first, otherwise to null. A missing binding (an older backend) or a failed
// call never blocks: the gate is a guard against a known dead end, not a
// requirement.
export async function stickFirmwareGate(gateFn, host) {
  if (!host || typeof gateFn !== 'function') return null;
  try {
    const g = await gateFn(host);
    return g && g.blocked ? g : null;
  } catch {
    return null;
  }
}

// stickFwTooOldHtml is what the wizard shows instead of preparing the stick:
// why nothing was written, then the firmware update guide (the step-by-step
// ladder where the speaker needs one), the same one the failed install shows.
export function stickFwTooOldHtml(gate, box) {
  if (!gate || !gate.blocked) return '';
  const firmware = String(gate.firmware || '').trim();
  const msg = `<div class="setup-err" data-stick-gate="firmware-too-old">${escapeHtml(
    t('setup.stickFwTooOld', { firmware: firmware || t('common.unknown') }))}</div>`;
  if (!firmwareGuideNeeded(firmware)) return msg;
  const b = box || {};
  return msg + '<div class="fw-update-banner" id="setupFwBanner">'
    + firmwareGuideMount({
      model: gate.model || b.model || b.type || '',
      moduleType: gate.moduleType || '',
      variant: gate.variant || '',
      current: firmware,
      host: b.host || '',
    })
    + '</div>';
}
