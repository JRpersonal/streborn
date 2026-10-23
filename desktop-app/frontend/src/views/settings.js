// views/settings.js — the "Speaker settings" view.
//
// Extracted from the main.js monolith, same pattern as views/recent.js and
// views/multiroom.js: the module pulls shared things (state, utils, i18n, api,
// localization) from their own modules and receives the few main.js-local
// helpers it needs (switchView, discoverBoxes, ...) via
// initSettingsView, so it never imports back into main.js (which would create a
// cycle). New views should follow this pattern so main.js stops growing.
//
// The sections themselves live in one module per accordion group under
// views/settings/ (basics, sound, network, info, actions, advanced, plus the
// pinned status and phone sections in status.js). Each exports a render
// function that returns its markup and the group it belongs in, and a wire
// function that attaches the handlers once the markup is on the page. This
// file keeps the speaker dropdown, the Bose language table, the load/retry
// loop and the orchestration in renderBoxSettings.
//
// Entry point: loadBoxSettings() is called from main.js's switchView when the
// settings tab is opened. The top-level setup below (the #view-settings shell
// and the box-switcher wiring) runs once when this module is first imported,
// exactly as it did when it lived at module scope in main.js.

import { state } from '../state.js';
import {
  $,
  escapeHtml,
  escapeAttr,
  sleep,
} from '../utils.js';
import { t } from '../i18n/index.js';
import { stereoPairsOf, inStereoPair } from '../groups.js';
import { answersWithoutSTR } from '../boxstate.js';
import { networkView } from '../netview.js';
import {
  BoxSettings,
  RefreshKnownBoxes,
} from '../api.js';
import { deps, initSettingsDeps, renderSTRRemovedPanel, openInstallFor, settingsBoxLabel } from './settings/shared.js';
import { renderStatusSection, renderPhoneSection, wireStatusSections } from './settings/status.js';
import { renderBasicsSections, wireBasicsSections, refreshBoxBalanceRow, refreshStartVolume } from './settings/basics.js';
import { renderSoundSections, wireSoundSections } from './settings/sound.js';
import { renderNetworkSections, wireNetworkSections } from './settings/network.js';
import { renderInfoSections, wireInfoSections, refreshForeignInfluence, refreshBoxLevelRows } from './settings/info.js';
import { renderActionSections, wireActionSections } from './settings/actions.js';
import { renderAdvancedSections, wireAdvancedSections } from './settings/advanced.js';

// What the rest of the app imports from this view, kept here so main.js and
// setup.js need no path into the group modules: the shared volume throttle
// (the music view's volume control shares it), the name combobox, and the
// Multi-Room tab's way to the remote key map.
export { throttledSetVolume, throttledSetBass } from './settings/basics.js';
export { wireCombobox } from './settings/shared.js';
export { openWebhookKeyMap } from './settings/advanced.js';

// Injected main.js helpers: shared with the group modules through
// settings/shared.js, so the settings code everywhere calls them as deps.<name>.
export function initSettingsView(d) {
  initSettingsDeps(d);
}

// mountSettingsShell builds the settings view's static shell (box switcher + body
// container) and wires the box-switcher controls. Run LAZILY on first open, NOT
// at module import: this module is imported at the top of main.js, before the
// #view-settings container is created further down, so touching the DOM at import
// time threw and blanked the whole app. loadBoxSettings calls this once first.
let settingsShellMounted = false;
function mountSettingsShell() {
  if (settingsShellMounted) return;
  const root = $('view-settings');
  if (!root) return;
  settingsShellMounted = true;
  root.innerHTML = `
    <h2>${escapeHtml(t('settingsView.title'))}</h2>
    <div class="settings-box-switcher">
      <span class="muted small">${escapeHtml(t('settingsView.forSpeaker'))}</span>
      <select id="settingsBoxSelect"></select>
      <button class="btn-icon" id="settingsRefreshBtn" title="${escapeAttr(t('settingsView.refreshListTitle'))}"><span class="refresh-icon">&#x21bb;</span></button>
    </div>
    <div id="settingsBody">
      <div class="muted">${escapeHtml(t('settingsView.selectFirst'))}</div>
    </div>
  `;
  $('settingsBoxSelect').onchange = () => {
    const id = $('settingsBoxSelect').value;
    const box = state.boxes.find(b => b.deviceID === id);
    if (box) {
      state.settingsBox = box;
      // A switch starts the new speaker's story from zero: the previous
      // speaker's reconnect countdown (and its pending retry timer) must not
      // keep running against the one just picked.
      if (state.settingsReconnect && state.settingsReconnect.timer) {
        clearTimeout(state.settingsReconnect.timer);
      }
      state.settingsReconnect = null;
      loadBoxSettings();
      // Lock-step with the music tab (and the Setup target picker).
      if (deps.speakerPicked) deps.speakerPicked(box);
    }
  };
  $('settingsRefreshBtn').onclick = async () => {
    const rb = $('settingsRefreshBtn');
    rb.disabled = true;
    rb.classList.add('spinning'); // visible feedback while refreshing, like the topbar refresh
    try {
      await deps.discoverBoxes();
      renderSettingsBoxSelect();
      loadBoxSettings();
    } finally {
      rb.classList.remove('spinning');
      rb.disabled = false;
    }
  };
}

function renderSettingsBoxSelect() {
  const sel = $('settingsBoxSelect');
  if (!state.boxes.length) {
    sel.innerHTML = `<option value="">${escapeHtml(t('settingsView.noSpeakerFound'))}</option>`;
    return;
  }
  const target = state.settingsBox || state.currentBox || state.boxes[0];
  if (target) state.settingsBox = target;
  sel.innerHTML = state.boxes.map(b => {
    const label = b.friendlyName || b.name || b.host;
    // Append the box model so the dropdown can distinguish two
    // boxes that happen to carry the same Bose-default friendly
    // name. Older agents that only broadcast generic "SoundTouch"
    // get no extra annotation — would just be noise.
    const modelSuffix = b.model && b.model !== 'SoundTouch' ? ` · ${b.model}` : '';
    return `<option value="${escapeAttr(b.deviceID)}">${escapeHtml(label + modelSuffix)} (${escapeHtml(b.host)})</option>`;
  }).join('');
  if (state.settingsBox) sel.value = state.settingsBox.deviceID;
}

// Bose sysLanguage enum, fully resolved 2026-06-01 (see
// project_bose_language_enum memory): id -> { endonym, key }. The
// endonym is the language's own name in its own script, so a speaker
// who cannot read the app's UI language (a Cyrillic/CJK/Greek/Thai
// reader looking at an English UI) still recognises and can pick their
// box language. key is the lowercased English name used to localise a
// secondary label via localizeLanguageName. 0 is the unset/factory
// sentinel (a no-op for the OOB gate) and 14 is undefined, so neither is
// offered as a real choice; 0 is only labelled when a box still reports
// it as its current value.
const BOSE_LANGS = {
  1: { endonym: 'Dansk', key: 'danish' },
  2: { endonym: 'Deutsch', key: 'german' },
  3: { endonym: 'English', key: 'english' },
  4: { endonym: 'Español', key: 'spanish' },
  5: { endonym: 'Français', key: 'french' },
  6: { endonym: 'Italiano', key: 'italian' },
  7: { endonym: 'Nederlands', key: 'dutch' },
  8: { endonym: 'Svenska', key: 'swedish' },
  9: { endonym: '日本語', key: 'japanese' },
  10: { endonym: '简体中文', key: 'chinese' },
  11: { endonym: '繁體中文', key: 'chinese' },
  12: { endonym: '한국어', key: 'korean' },
  13: { endonym: 'ไทย', key: 'thai' },
  15: { endonym: 'Čeština', key: 'czech' },
  16: { endonym: 'Suomi', key: 'finnish' },
  17: { endonym: 'Ελληνικά', key: 'greek' },
  18: { endonym: 'Norsk', key: 'norwegian' },
  19: { endonym: 'Polski', key: 'polish' },
  20: { endonym: 'Português', key: 'portuguese' },
  21: { endonym: 'Română', key: 'romanian' },
  22: { endonym: 'Русский', key: 'russian' },
  23: { endonym: 'Slovenščina', key: 'slovenian' },
  24: { endonym: 'Türkçe', key: 'turkish' },
  25: { endonym: 'Magyar', key: 'hungarian' },
};
const BOSE_LANG_IDS = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25];

// boseLangLabel returns "<endonym> (<localised name>)" for a sysLanguage
// id, e.g. "日本語 (Japanese)". The endonym lets a native speaker pick
// regardless of the app UI language; the parenthetical helps the current
// UI reader. The suffix is dropped when it would just repeat the endonym.
function boseLangLabel(id) {
  const n = Number(id);
  const e = BOSE_LANGS[n];
  if (!e) return n === 0 ? t('settingsView.langNoValue') : t('settingsView.langUnknown');
  const localized = deps.localizeLanguageName(e.key);
  return localized && localized.toLowerCase() !== e.endonym.toLowerCase()
    ? `${e.endonym} (${localized})`
    : e.endonym;
}

export function langOptionsHtml() {
  // Sort by the localised name (a Latin string in the current UI
  // language) so the order is predictable for the majority; the
  // native-script endonym stands out for speakers scanning for theirs.
  return BOSE_LANG_IDS
    .map((id) => ({ id, label: boseLangLabel(id), sort: deps.localizeLanguageName(BOSE_LANGS[id].key) }))
    .sort((a, b) => a.sort.localeCompare(b.sort))
    .map((o) => `<option value="${o.id}">${escapeHtml(o.label)}</option>`)
    .join('');
}

// renderSTRNotRunningPanel is the honest ending for a speaker that answers its
// Bose firmware while the STR agent on it does not answer: the box is up, so
// "unplug it" is wrong advice; what it needs is STR installed again (a repair
// over the network). Used both for a record discovery already degraded to
// "STR not running" and for the reconnect loop's give-up when the box still
// answers on its Bose port.
function renderSTRNotRunningPanel(body, box, withRetry) {
  body.innerHTML = `
    <div class="empty-state">
      <div class="empty-state-title">${escapeHtml(t('settingsView.strNotRunningTitle'))}</div>
      <div class="empty-state-text">
        ${escapeHtml(t('settingsView.strNotRunningHelp', { name: settingsBoxLabel(box) }))}
      </div>
      <div class="empty-state-buttons">
        <button class="btn btn-primary btn-mini" id="settingsInstallAgain">${escapeHtml(t('settingsView.repairSTRBtn'))}</button>
        ${withRetry ? `<button class="btn btn-mini" id="settingsRetry">${escapeHtml(t('common.retry'))}</button>` : ''}
        <button class="btn btn-mini" id="settingsBackToBoxes">${escapeHtml(t('settingsView.backToSpeakerList'))}</button>
      </div>
    </div>`;
  const again = document.getElementById('settingsInstallAgain');
  if (again) again.onclick = () => openInstallFor(box);
  const r = document.getElementById('settingsRetry');
  if (r) r.onclick = () => { state.settingsReconnect = null; loadBoxSettings(); };
  const back = document.getElementById('settingsBackToBoxes');
  if (back) back.onclick = () => { state.settingsReconnect = null; deps.switchView('box'); };
}

// boseAnswersWithoutSTR asks the backend for a fresh direct probe of the
// selected speaker and reports whether its Bose firmware answered while the
// STR agent did not: a degraded "STR not running" record, a plain stock
// record, or this cycle's transient strSilent marker. False when nothing
// answered (or the probe itself failed), which is the genuine "dead" case.
async function boseAnswersWithoutSTR(box) {
  try {
    const fresh = await RefreshKnownBoxes();
    return answersWithoutSTR((fresh || []).find(b => b && b.host === box.host));
  } catch {
    return false;
  }
}

export async function loadBoxSettings() {
  mountSettingsShell(); // build the shell on first open (see note above)
  renderSettingsBoxSelect();
  const body = $('settingsBody');
  if (!state.settingsBox) {
    body.innerHTML = `
      <div class="empty-state">
        <div class="empty-state-title">${escapeHtml(t('settingsView.noSpeakerFoundTitle'))}</div>
        <div class="empty-state-text">
          ${escapeHtml(t('settingsView.noSpeakerFoundHelp'))}
        </div>
        <button class="btn btn-primary btn-mini" id="settingsGoSetup">${escapeHtml(t('speaker.goSetup'))}</button>
      </div>`;
    const go = document.getElementById('settingsGoSetup');
    if (go) go.onclick = () => deps.switchView('setup');
    return;
  }
  // Stock Bose speakers do not run the STR agent, so /api/box/settings
  // (an STR endpoint on port 8888) would hit Bose's RomPager web
  // server and return a 404 with a confusing HTML error. Render a
  // clear "install STR first" panel instead.
  //
  // Two cousins of the plain stock box come first. STR was just removed from
  // this speaker by the user (strRemovedHost): the box is stock by design, say
  // so instead of "STR not installed yet". And a box discovery degraded to
  // "STR not running" (answers its Bose firmware, agent gone for good): the
  // fix is a reinstall, not a power cycle. Both clear once the agent answers
  // again (an STR record with a version behind it).
  if (state.strRemovedHost && state.settingsBox.host === state.strRemovedHost) {
    if (state.settingsBox.kind === 'str' && state.settingsBox.version && !state.settingsBox.strSilent) {
      state.strRemovedHost = null;
    } else {
      renderSTRRemovedPanel(body, state.settingsBox);
      return;
    }
  }
  if (state.settingsBox && state.settingsBox.kind === 'stock' && state.settingsBox.strNotRunning) {
    renderSTRNotRunningPanel(body, state.settingsBox, false);
    return;
  }
  if (state.settingsBox && state.settingsBox.kind === 'stock') {
    body.innerHTML = `
      <div class="empty-state">
        <div class="empty-state-title">${escapeHtml(t('settingsView.stockBoxTitle'))}</div>
        <div class="empty-state-text">
          ${escapeHtml(t('settingsView.stockBoxHelp', { name: state.settingsBox.friendlyName || state.settingsBox.name || state.settingsBox.host }))}
        </div>
        <button class="btn btn-primary btn-mini" id="settingsGoSetup">${escapeHtml(t('speaker.goSetup'))}</button>
      </div>`;
    const go = document.getElementById('settingsGoSetup');
    if (go) go.onclick = () => deps.switchView('setup');
    return;
  }
  // The pane may still show ANOTHER speaker's settings: the user just
  // switched in the dropdown. Keeping those visible while the new fetch runs
  // made an unreachable or slow speaker look fully configured, every value
  // on screen belonged to the previous box and nothing said so (field,
  // 2026-08-29). A switch therefore blanks the pane down to the loading hint
  // until the new speaker has actually answered; only a re-fetch of the SAME
  // speaker keeps its current values on screen.
  if (body.dataset.renderedBoxId && body.dataset.renderedBoxId !== state.settingsBox.deviceID) {
    delete body.dataset.renderedBoxId;
    body.innerHTML = '';
  }
  // If this speaker's content is already rendered, do not overwrite it. The
  // user should keep seeing the previous values while we fetch fresh data.
  // Otherwise show a hint.
  const hasContent = body.querySelector('.settings-section');
  if (!hasContent) {
    body.innerHTML = `<div class="muted">${escapeHtml(t('settingsView.loadingData'))}</div>`;
  }

  let lastErr = null;
  // Retry loop: on "connection refused" / "timeout" retry up to two
  // times. A rename briefly restarts the speaker's webserver, which
  // is an expected transient.
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      const s = await BoxSettings(state.settingsBox.host, state.settingsBox.port);
      // The agent returns HTTP 200 with an all-empty payload when the
      // box's Bose app (:8090) did not answer within the read timeout
      // (it is still starting or briefly hung). That is not data; treat
      // it like a transient so the user gets a clear "speaker busy"
      // banner instead of a panel full of blank fields.
      if (isEmptySettings(s)) {
        lastErr = new Error('box_settings_empty');
        if (attempt < 2) { await sleep(1500); continue; }
        break;
      }
      // Erfolg: Reconnect Counter + Timer aufloesen
      if (state.settingsReconnect && state.settingsReconnect.timer) {
        clearTimeout(state.settingsReconnect.timer);
      }
      state.settingsReconnect = null;
      renderBoxSettings(s, state.settingsBox);
      // Stamp whose settings the pane now shows, so the next dropdown
      // switch can tell "this speaker's values" from "the previous one's".
      body.dataset.renderedBoxId = state.settingsBox.deviceID;
      // Read-only, and only present on a stereo pair, so it is filled after the
      // markup exists rather than being part of it.
      refreshBoxBalanceRow(state.settingsBox,
        stereoPairsOf(state.zoneLive || {}).find(p => inStereoPair(state.settingsBox, p)) || null, state.boxes)
        .catch(() => {});
      // Same deal: only a home theater system has these, and whether it does
      // is a capability probe on the speaker, not something /api/box/settings
      // carries. The section stays hidden everywhere else.
      refreshBoxLevelRows(state.settingsBox).catch(() => {});
      // What other tools have done to this speaker. Also filled after the
      // markup exists, because it is a separate read and the common answer is
      // "nothing", which hides the whole section.
      refreshForeignInfluence(state.settingsBox).catch(() => {});
      refreshStartVolume(state.settingsBox).catch(() => {});
      return;
    } catch (e) {
      lastErr = e;
      if (attempt < 2 && isTransientBoxError(e)) {
        await sleep(1500);
        continue;
      }
      break;
    }
  }

  // Persistent reconnect banner instead of recurring toasts. Counts
  // down the remaining attempts, gives up after 10 and then shows a
  // clear instruction (power cycle after a failed OTA update).
  const friendly = friendlySettingsError(lastErr);
  // While an OTA is in flight on THIS box, a failed fetch is the EXPECTED reboot
  // (the box restarts once, or twice on a tight NAND while it activates the
  // Spotify engine), NOT a dead agent. Never escalate to the "agent died, unplug
  // the speaker" panel during an update: it told users to power-cycle a speaker
  // that was only restarting mid-OTA (#197). Show a calm "updating, restarting"
  // banner and keep polling until the box comes back.
  const otaHere = state.otaInProgress && state.otaTargetHost === state.settingsBox.host;
  state.settingsReconnect = state.settingsReconnect || { attempts: 0, max: 10 };
  state.settingsReconnect.attempts++;
  const remaining = state.settingsReconnect.max - state.settingsReconnect.attempts;

  if (otaHere || remaining > 0) {
    const bannerHtml = otaHere
      ? `
      <div class="reconnect-banner">
        <div>
          <b>${escapeHtml(t('update.inProgressTitle', { name: state.settingsBox.name || '' }))}</b>
          <small>${escapeHtml(t('update.rebootNote'))}</small>
        </div>
      </div>`
      : `
      <div class="reconnect-banner">
        <div>
          <b>${escapeHtml(noNetworkHere(lastErr) ? t('settingsView.noNetworkTitle') : t('settingsView.speakerUnreachable'))}</b>
          <small>${escapeHtml(friendly)}</small>
          <small>${escapeHtml(t('settingsView.retryIn', { remaining }))}</small>
        </div>
      </div>`;
    if (hasContent) {
      // Keep the existing rendering, insert the banner at the top.
      let existing = body.querySelector('.reconnect-banner');
      if (!existing) {
        body.insertAdjacentHTML('afterbegin', bannerHtml);
      } else {
        existing.outerHTML = bannerHtml;
      }
    } else {
      body.innerHTML = bannerHtml;
    }
    if (state.settingsReconnect.timer) clearTimeout(state.settingsReconnect.timer);
    state.settingsReconnect.timer = setTimeout(loadBoxSettings, 4000);
  } else {
    // After 10 failed attempts: give up and show clear instructions.
    state.settingsReconnect = null;
    // Which instructions depends on whether the speaker answers at all. A box
    // whose Bose firmware answers while the agent does not (STR removed out
    // of band, agent gone) is not dead, and "unplug it" would not help: what
    // it needs is STR installed again. Only a speaker that answers nothing
    // gets the power-cycle advice (2026-09-06 report: the dead-speaker panel
    // on a stock speaker that was answering fine).
    // This computer has no network at all. Nothing here is about the speaker,
    // so none of the speaker advice applies, and above all nothing should be
    // unplugged. Keep re-checking: while the machine has no route the attempt
    // fails inside the socket layer and never reaches a speaker, so this costs
    // the speakers nothing, and the moment the network is back the panel
    // replaces itself with the real settings. That is what the reporter asked for:
    // "when wifi was restored, shouldn't the notice be updated to reflect that
    // the speakers all came back online?"
    if (noNetworkHere(lastErr)) {
      body.innerHTML = `
      <div class="empty-state">
        <div class="empty-state-title">${escapeHtml(t('settingsView.noNetworkTitle'))}</div>
        <div class="empty-state-text">${escapeHtml(t('settingsView.noNetworkHelp'))}</div>
        <div class="empty-state-buttons">
          <button class="btn btn-mini" id="settingsRetry">${escapeHtml(t('common.retry'))}</button>
        </div>
      </div>`;
      const rn = document.getElementById('settingsRetry');
      if (rn) rn.onclick = () => { state.settingsReconnect = null; loadBoxSettings(); };
      state.settingsReconnect = { attempts: 0, max: 10, timer: setTimeout(loadBoxSettings, 5000) };
      return;
    }
    if (await boseAnswersWithoutSTR(state.settingsBox)) {
      renderSTRNotRunningPanel(body, state.settingsBox, true);
      return;
    }
    body.innerHTML = `
      <div class="empty-state">
        <div class="empty-state-title">${escapeHtml(t('settingsView.speakerDeadTitle'))}</div>
        <div class="empty-state-text">
          ${escapeHtml(t('settingsView.speakerDeadHelp1'))}
          <br><br>
          ${t('settingsView.speakerDeadHelp2')}
        </div>
        <div class="empty-state-buttons">
          <button class="btn btn-mini" id="settingsRetry">${escapeHtml(t('common.retry'))}</button>
          <button class="btn btn-primary btn-mini" id="settingsBackToBoxes">${escapeHtml(t('settingsView.backToSpeakerList'))}</button>
        </div>
      </div>`;
    const r = document.getElementById('settingsRetry');
    if (r) r.onclick = () => { state.settingsReconnect = null; loadBoxSettings(); };
    const b2 = document.getElementById('settingsBackToBoxes');
    if (b2) b2.onclick = () => { state.settingsReconnect = null; deps.switchView('box'); };
  }
}

// noNetworkHere is the ENETUNREACH family: this computer has no route to the
// speaker's network, so the request never left the machine. It is the one
// unreachable-speaker cause that is provably NOT the speaker: a firewall cannot
// produce it and neither can a speaker, and it fails identically for every
// device at once. A user pulled their Mac's Wi-Fi mid-update on 2026-09-10
// and this panel told her the agent on the speaker had died and to unplug it.
//
// The matching pair lives in Go as noNetworkHere (desktop-app/app_transport.go),
// which attaches the honest paragraph; this is the same test on the text that
// reaches the view. "no route to host" is deliberately NOT included: that one
// means a route exists and the host did not answer, which usually IS a speaker
// that is switched off.
export function noNetworkHere(err) {
  const s = String(err || '').toLowerCase();
  return s.includes('network is unreachable') || s.includes('unreachable network');
}

function isTransientBoxError(err) {
  const s = String(err || '').toLowerCase();
  return s.includes('refused') || s.includes('timeout') ||
         s.includes('deadline') || s.includes('reset') ||
         s.includes('eof') || s.includes('no route');
}

function friendlySettingsError(err) {
  const s = String(err || '');
  // Checked FIRST: this message reaches the view with the whole dial/connect
  // string in it, and every pattern below would otherwise match a fragment of
  // that and print a speaker explanation for a laptop with no Wi-Fi.
  if (noNetworkHere(s)) return t('settingsView.errNoNetwork');
  if (/box_settings_empty/.test(s)) return t('settingsView.errBoseBusy');
  if (/refused/i.test(s)) return t('settingsView.errRefused');
  if (/timeout|deadline/i.test(s)) return t('settingsView.errTimeout');
  if (/no such host|no route/i.test(s)) return t('settingsView.errNoRoute');
  return t('settingsView.errGeneric', { err: s });
}

// isEmptySettings reports whether the agent returned a 200 but the box's
// Bose app (:8090) supplied nothing within the read timeout: no device
// info, no network interfaces, no sources. That means the speaker's Bose
// side is still starting or briefly hung, not that there is real data.
function isEmptySettings(s) {
  if (!s) return true;
  const info = s.info || {};
  const hasInfo = !!(info.deviceID || info.name || info.type);
  const hasNet = !!(s.network && Array.isArray(s.network.interfaces) && s.network.interfaces.length);
  const hasSources = Array.isArray(s.sources) && s.sources.length > 0;
  return !hasInfo && !hasNet && !hasSources;
}

// groupSettingsSections folds the settings sections into a few collapsible
// accordions (#declutter): Basics (open) / Sound & display / Network / Status &
// info / Actions / Advanced (collapsed). Each group module says which group its
// sections belong in (the group field of its render result), so nothing here
// has to recognise a section by its translated heading any more; the Status
// block and the phone card are pinned outside the groups by renderBoxSettings.
function groupSettingsSections(byGroup) {
  const GROUPS = [
    { key: 'basics', label: t('settingsView.groupBasics'), open: true },
    { key: 'sound', label: t('settingsView.groupSound'), open: false },
    { key: 'network', label: t('settingsView.groupNetwork'), open: false },
    { key: 'info', label: t('settingsView.groupInfo'), open: false },
    { key: 'actions', label: t('settingsView.groupActions'), open: false },
    { key: 'advanced', label: t('settingsView.groupAdvanced'), open: false },
  ];
  return GROUPS.map((g) => {
    const html = (byGroup[g.key] || '').trim();
    if (!html) return '';
    return `<details class="settings-group"${g.open ? ' open' : ''}><summary class="settings-group-summary">${escapeHtml(g.label)}</summary>
${html}
</details>`;
  }).join('');
}

function renderBoxSettings(s, box) {
  const info = s.info || {};
  const vol = s.volume || {};
  const bass = s.bass || {};
  const net = s.network || {};
  // Which network the speaker is on, and how sure we can be: see netview.js.
  // A name is only shown as the live network when the speaker reported it;
  // STR's own last network is shown below it as context, never in its place.
  const nv = networkView(net);
  // What every group module gets: the settings answer and the speaker, plus
  // the few helpers that stay in this file (the Bose language table and the
  // speaker dropdown).
  const ctx = { s, box, info, vol, bass, net, nv, langOptionsHtml, boseLangLabel, renderSettingsBoxSelect };
  // Each module renders its sections and says which group they belong in. The
  // Status block and the phone card are pinned outside the groups: the DOM
  // order is Status, the phone card, then the six accordions.
  const parts = [
    renderStatusSection(ctx),
    renderBasicsSections(ctx),
    renderSoundSections(ctx),
    renderNetworkSections(ctx),
    renderInfoSections(ctx),
    renderActionSections(ctx),
    renderAdvancedSections(ctx),
    renderPhoneSection(ctx),
  ];
  const byGroup = {};
  for (const p of parts) byGroup[p.group] = (byGroup[p.group] || '') + p.html;

  $('settingsBody').innerHTML = byGroup.top + byGroup.bottom + groupSettingsSections(byGroup);

  // Handlers go on once the markup exists, never from inside a template
  // literal: a call placed in there renders as literal text on the page
  // instead of running.
  wireStatusSections(ctx);
  wireBasicsSections(ctx);
  wireSoundSections(ctx);
  wireNetworkSections(ctx);
  wireInfoSections(ctx);
  wireActionSections(ctx);
  wireAdvancedSections(ctx);
}
