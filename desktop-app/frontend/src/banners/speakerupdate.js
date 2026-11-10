// banners/speakerupdate.js: the speaker update card (#speakerUpdateCard), the
// once-per-start prompt that names the speakers left on an older agent and
// starts their update. Extracted from the main.js monolith; main.js-local
// helpers arrive through deps.js.

import { isStr, strBoxes } from '../boxes.js';
import { anyInLiveGroup } from '../groups.js';
import { t } from '../i18n/index.js';
import { state } from '../state.js';
import { updateAllBoxes } from '../update/all.js';
import { doBoxUpdate } from '../update/single.js';
import { $, escapeHtml, getBoxLabel, showError, showToast } from '../utils.js';
import { deps } from './deps.js';

// The speaker-update prompt. Users update the APP (the website tells them to)
// and never learn that the speaker carries its own software: three independent
// "my speaker switches itself off" reports on the same day all turned out to be
// speakers left on an old agent, one of them proven by a screenshot showing app
// v0.9.21 next to speaker v0.9.17. The existing cues were a blue dot on the
// tile and a banner buried in Speaker Settings, and the screenshot proved both
// are missable: dots read as decoration, and a user who never opens that tab
// never sees the banner.
//
// So: ask ONCE PER APP START, in the music view where everyone works, naming
// the affected speakers and offering one button that updates all of them. Not
// a modal (that trains people to dismiss), not repeated within a session, and
// it disappears by itself as soon as the speakers are current, because it is
// driven purely by boxNeedsUpdate.
let speakerUpdateCardShown = false;
export function maybeShowSpeakerUpdateCard() {
  const el = $('speakerUpdateCard');
  if (!el) return;
  if (!state.appInfo || !state.appInfo.version) return;
  // App first: while the app itself is out of date this card stays away, so
  // the user is never shown two update prompts and left to guess the order.
  if (deps.speakerUpdateCardMuted()) { el.classList.add('hidden'); return; }
  const outdated = strBoxes(state.boxes).filter(b => !b.offline && deps.boxNeedsUpdate(b));

  // Nothing behind any more: TAKE THE CARD AWAY. This runs before the
  // once-per-start latch on purpose. The latch used to short-circuit the whole
  // function, so once the card had been shown there was no path that could ever
  // hide it again: the user updated every speaker, the update finished, and the
  // card still sat there asking them to do what they had just done (Jens,
  // 2026-08-05, right after updating all four of his).
  if (!outdated.length) { el.classList.add('hidden'); return; }

  // Some are still behind. If the card is already up, refresh the list rather
  // than leave a stale one naming speakers that are now current.
  const visible = !el.classList.contains('hidden');
  if (speakerUpdateCardShown && !visible) return; // asked once, they said later
  speakerUpdateCardShown = true;

  const list = outdated
    .map(b => `<li>${escapeHtml(getBoxLabel(b))} <span class="suc-ver">${escapeHtml(b.version || '')}</span></li>`)
    .join('');
  const btnLabel = outdated.length > 1
    ? t('speakerUpdate.btnAll', { n: outdated.length })
    : t('speakerUpdate.btnOne');
  // UI 2.0 preview: one speaker behind is named in the title (rule 1, the
  // speaker is always named). The classic layout keeps its title.
  const sucTitle = state.ui2Shell && outdated.length === 1
    ? t('ui2.speakerUpdate.titleOne', { name: getBoxLabel(outdated[0]) })
    : t('speakerUpdate.title');
  if (state.ui2Shell) el.setAttribute('aria-label', sucTitle);
  el.innerHTML =
    `<div class="suc-body">` +
      `<div class="suc-title">${escapeHtml(sucTitle)}</div>` +
      `<div class="suc-text">${escapeHtml(t('speakerUpdate.text', { version: state.appInfo.version }))}</div>` +
      `<ul class="suc-list">${list}</ul>` +
      // Several speakers at once restart one after the other; any of them in
      // a live group leaves it (#1339). One speaker goes through doBoxUpdate,
      // whose own banner says it for that speaker.
      (outdated.length > 1 && anyInLiveGroup(outdated, state.zoneLive)
        ? `<div class="suc-text" id="sucGroupNote">${escapeHtml(t('updateAll.groupNote'))}</div>` : '') +
      `<div class="suc-actions">` +
        `<button class="btn btn-primary btn-mini" id="sucUpdate">${escapeHtml(btnLabel)}</button>` +
        `<button class="btn btn-mini" id="sucLater">${escapeHtml(t('speakerUpdate.later'))}</button>` +
      `</div>` +
    `</div>`;
  el.classList.remove('hidden');

  const hide = () => el.classList.add('hidden');
  const later = $('sucLater');
  if (later) later.onclick = hide;
  const go = $('sucUpdate');
  if (go) {
    go.onclick = async () => {
      // Do NOT hide first. The card used to disappear on the click and only
      // then start working; every path that then declined to start (an update
      // already running, the speakers turning out to be current, a pre-flight
      // the user cancelled) left the user staring at the tab they were on with
      // the prompt gone for the rest of the session, because the
      // once-per-start latch never brings it back. It hides when the update is
      // actually under way, and stays put otherwise (Jens, 2026-08-22).
      const label = go.textContent;
      const restore = () => { go.disabled = false; go.textContent = label; };
      // Re-resolve against the CURRENT box list. `outdated` is a snapshot from
      // render time, and a discovery pass landing between render and click
      // replaces every object in state.boxes: the stale copies can carry an
      // address the speaker has already left. This is the "shortly after the
      // app started, while the scan is still running" case.
      const wanted = new Set(outdated.map(b => b.host));
      const live = strBoxes(state.boxes).filter(b => !b.offline && deps.boxNeedsUpdate(b));
      const targets = live.filter(b => wanted.has(b.host));
      const pick = targets.length ? targets : live;
      if (!pick.length) {
        // Two different empties, and telling them apart matters. No speakers on
        // the list at all means the sweep is mid-cycle or the LAN dropped them:
        // keep the card up and say the speakers are not reachable. A populated
        // list with nothing behind means they really are current, and the card
        // has done its job.
        const anyBox = (state.boxes || []).some(isStr);
        showToast(anyBox ? t('updateAll.noneToUpdate') : t('speakerUpdate.notReachable'));
        if (anyBox) hide();
        return;
      }
      go.disabled = true;
      go.textContent = t('speakerUpdate.working');
      try {
        if (pick.length === 1) {
          const box = pick[0];
          // Point Speaker Settings AT THE SPEAKER BEING UPDATED before
          // switching. doBoxUpdate only paints its progress while the user is
          // looking at the target box, so landing on whichever speaker the
          // settings tab happened to have selected ran the whole OTA with no
          // visible sign of it at all: the exact "nothing happens" report.
          state.settingsBox = box;
          showToast(t('speakerUpdate.starting', { name: getBoxLabel(box) }));
          deps.switchView('settings');
          hide();
          await doBoxUpdate(box);
        } else {
          // updateAllBoxes probes every speaker (version, USB stick, Wi-Fi
          // quality) before it can ask, which is seconds of silence on a busy
          // LAN. It keeps its own "checking" toast up; the card stays on screen
          // in its working state until that flow commits or backs out.
          const started = await updateAllBoxes(hide);
          if (!started) restore();
          return;
        }
      } catch (e) { showError(e); restore(); return; }
      restore();
    };
  }
}
