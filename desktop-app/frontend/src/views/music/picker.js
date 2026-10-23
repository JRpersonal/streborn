// views/music/picker.js: the slot picker modal (#pickModal, "save on which key?"),
// shared with the Library and Recent views.

import { RadioVote, SetPreset } from '../../api.js';
import { t } from '../../i18n/index.js';
import { stationLogoChain } from '../../logos.js';
import { parsePresetConflict } from '../../presetmove.js';
import { state } from '../../state.js';
import { $, escapeHtml, showError, showToast } from '../../utils.js';
import { loadPresets } from './presets.js';
import { presetMoveOffer } from './save.js';
import { preferMp3SiblingForBox } from './search.js';

// showSlotPicker renders the shared 1-6 preset slot-picker modal. Callers pass
// the title, subtitle and an onPick(slot) that does the actual save; closing the
// modal, reloading the presets and surfacing errors are common to every use.
// box is the speaker the pick writes to. It defaults to the selected one, and a
// caller that can assign onto ANOTHER speaker (a Recently-played card from a
// different box) has to pass it, so a move offered after a refusal happens on
// the speaker the station is actually on.
export function showSlotPicker({ title, subtitle, onPick, box }) {
  $('pickTitle').textContent = title;
  $('pickSub').textContent = subtitle || '';
  const grid = $('pickGrid');
  grid.innerHTML = '';
  for (let i = 1; i <= 6; i++) {
    const p = state.presets.find(x => x.slot === i);
    const b = document.createElement('button');
    b.className = 'pick-slot' + (p ? ' has' : '');
    b.innerHTML = '<div class="ps-num">' + escapeHtml(t('preset.key', { n: i })) + '</div><div class="ps-name">' + (p ? escapeHtml(p.name) : escapeHtml(t('preset.pickEmpty'))) + '</div>';
    b.onclick = async () => {
      try {
        await onPick(i);
        closePick();
        await loadPresets();
      } catch (err) {
        // Assigning a station from a list meets the same already-on-another-key
        // refusal as the hold-to-save, and it is the path the two reporters were
        // on (#709/#925), so it gets the same offer to move.
        const conflict = parsePresetConflict(err);
        const target = box || state.currentBox;
        if (conflict && target) {
          closePick();
          presetMoveOffer(conflict, i, target);
          return;
        }
        showError(err);
      }
    };
    grid.appendChild(b);
  }
  $('pickModal').classList.remove('hidden');
}

export function openPick(station) {
  // Presets play on the box only, so the same MP3-over-AAC sibling preference
  // as playStation applies to an assignment from the search list (#252).
  station = preferMp3SiblingForBox(station, state.searchResults);
  showSlotPicker({
    title: t('preset.assignStationTitle'),
    subtitle: station.name + (station.bitrate ? ' (' + station.bitrate + ' kbit/s)' : ''),
    onPick: async (i) => {
      const logo = stationLogoChain(station);
      await SetPreset(state.currentBox.host, state.currentBox.port, i, station.name, station.url_resolved || station.url, logo, station.bitrate || 0, station.homepage || '', station.codec || '');
      if (station.stationuuid) {
        RadioVote(station.stationuuid).catch(() => {});
      }
      showToast(t('preset.savedToKey', { n: i, name: station.name }));
    },
  });
}
function closePick() { $('pickModal').classList.add('hidden'); }

// mountPicker attaches the handlers that used to be wired at module scope in
// main.js, once #view-box exists (called from mountMusicView).
export function mountPicker() {
  $('pickCancel').onclick = closePick;
}
