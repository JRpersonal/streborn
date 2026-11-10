// banners/attention.js: what a button of a speaker's attention note does in
// the UI 2.0 preview (shell2/attention.js): the classic issue banner's own
// handlers, for that one speaker. main.js registers runSpeakerAttention with
// setSpeakerAttentionRunner. Extracted from the main.js monolith.

import { RebootBox, RemoveConflictingMod, RepairSpeakerName } from '../api.js';
import { runConflictCleanup } from '../conflictcleanup.js';
import { t } from '../i18n/index.js';
import { localizeStillRestarting, restarts } from '../restartwait.js';
import { openSpeakers } from '../shell2/speakers/index.js';
import { sectionReport } from '../shell2/state.js';
import { state } from '../state.js';
import { confirmWarn, getBoxLabel, showError, showToast } from '../utils.js';
import { renderBoxSelect, selectBox } from '../views/boxpicker.js';
import { deps } from './deps.js';
import { warnDismissKey } from './dismiss.js';
import { checkBoxIssueBanner, noteRestartRequested, runCloudRestore } from './speaker.js';

// runSpeakerAttention is what a button of a speaker's attention note does in
// the preview: the classic banner's own handler, for that one speaker.
export async function runSpeakerAttention(host, act, btn, dismissKind) {
  const box = (state.boxes || []).find(b => b && b.host === host);
  if (!box) return;
  switch (act) {
    case 'cloudRestore':
      await runCloudRestore([box], btn);
      break;
    case 'removeConflict': {
      const label = btn.textContent;
      await runConflictCleanup(box, {
        t, confirmWarn, showToast, showError,
        removeConflictingMod: RemoveConflictingMod,
        rebootBox: RebootBox,
        rediscover: (afterMs) => {
          if (afterMs > 0) setTimeout(deps.discoverBoxes, afterMs);
          else deps.discoverBoxes();
        },
        setBusy: (busy) => {
          btn.disabled = busy;
          btn.textContent = busy ? t('settingsView.removeConflictRunning') : label;
        },
      });
      break;
    }
    case 'saveWifi':
      state.wlanStoreFor = box.host;
      // The preview's speaker page shows one tab at a time: open it on
      // Network, where the Wi-Fi form is (wireWlanSwitch opens it there).
      if (state.ui2Shell && openSpeakers('speaker', { host: box.host, tab: 'network' })) break;
      selectBox(box);
      deps.switchView('settings');
      break;
    case 'dismiss':
      try { localStorage.setItem(warnDismissKey(box, dismissKind), String(Date.now())); } catch {}
      checkBoxIssueBanner();
      break;
    case 'stormRestart':
      // In the attention item the button sits in (sectionReport; the classic
      // toast and dialog without the preview): "Restarting" only once the
      // speaker took the restart, a refusal right there.
      if (restarts.isPending(box.host)) {
        sectionReport(btn, { tone: 'busy', text: t('speaker.stillRestarting', { name: getBoxLabel(box) }) });
        break;
      }
      btn.disabled = true;
      try {
        await RebootBox(box.host, box.port);
        noteRestartRequested(box.host);
        btn.textContent = t('speaker.restartingBtn');
        sectionReport(btn, { tone: 'busy', text: t('speaker.stormRestarting', { name: getBoxLabel(box) }) });
      } catch (e) {
        // Off until the speaker is back on success; on a failure the
        // button is the way to try again.
        btn.disabled = false;
        sectionReport(btn, { tone: 'error', text: localizeStillRestarting(String(e), t('speaker.stillRestarting', { name: getBoxLabel(box) })) });
      }
      break;
    case 'badNameFix':
      btn.disabled = true;
      try {
        const name = await RepairSpeakerName(box.host, box.port, box.friendlyName || '');
        // A success takes the item away (the name is fixed): its word goes
        // where the classic one went.
        showToast(t('speaker.badNameFixing', { name: name || getBoxLabel(box) }));
        const idx = (state.boxes || []).findIndex(b => b && b.deviceID === box.deviceID);
        if (idx >= 0) state.boxes[idx] = { ...state.boxes[idx], friendlyName: name || box.friendlyName, speakerNameUnsafe: false };
        checkBoxIssueBanner();
        renderBoxSelect();
      } catch (e) { sectionReport(btn, { tone: 'error', text: String(e) }); } finally { btn.disabled = false; }
      break;
    default: break;
  }
}
