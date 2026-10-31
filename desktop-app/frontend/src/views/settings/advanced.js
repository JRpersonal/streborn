// views/settings/advanced.js: the Advanced group of Speaker settings, the
// Playground. Announcements, the smart-home webhooks with the remote key map,
// voice control, repairing the preset keys on the speaker, a custom URL on a
// preset, and the experimental cloud-preset restore.

import { state } from '../../state.js';
import { $, escapeHtml, escapeAttr, showToast } from '../../utils.js';
import { t } from '../../i18n/index.js';
import { boxPresetRows, keyLabel, keyNum, rowSlots } from '../../presetrows.js';
// Group keys (#863): the thumbs keys that carry a saved group are marked on
// the remote key map; the document itself is edited on the Multi-Room tab.
import { normalizeDoc, webhookPresent, keyMapTarget, validWolMac } from '../../groupkeys.js';
import { normalizeKeyActions, sleepOn, snoozeOn, bassOn, isBassAction, actionOn, withAction, sleepBlockedBy, SLEEP_KEY_IDS, KEY_ACTION_KEY_IDS } from '../../keyactions.js';
import {
  SetPreset,
  RestoreBoxSnapshot,
  RebootBox,
  BrowserOpenURL,
  AnnounceExample,
  SendAnnounce,
  Translate,
  GetWebhooks,
  GetGroupKeys,
  GetKeyActions,
  SaveKeyActions,
  SaveWebhookConfig,
  TestWebhookAction,
  SyncBoxPresets,
} from '../../api.js';
import {
  deps, helpBlock, discussLink, revealInSettings, confirmFor,
  reportError, reportDone, reportFieldError, clearFieldErrors,
} from './shared.js';

// Where the voice-control explanation lives. In the repo for now so the link
// works the day the button ships; move it to the website page when that exists
// and this one line follows.
const VOICE_GUIDE_URL = 'https://github.com/JRpersonal/streborn/blob/main/docs/ALEXA.md';

// The Home Assistant guide sits on the webhook tile: that is where someone
// wiring a hub lands first, and the guide lists every local address STR keeps
// alive plus the integrations that target them (Jens, 2026-09-06).
const HOME_ASSISTANT_GUIDE_URL = 'https://github.com/JRpersonal/streborn/blob/main/docs/HOME-ASSISTANT.md';

// remoteSvg draws the SoundTouch remote as a clickable key map for the
// webhook section, laid out from a photo of the real remote (2026-09-06):
// power and AUX on top, presets 1 to 6, volume up/down stacked beside a tall
// play/pause, then back/forward, then thumbs down/up. No Bose logo: it is a
// trademark, and the drawing only needs the keys. Keys carry data-key with
// the trigger id; the volume keys carry no trigger and are drawn dimmed.
function remoteSvg() {
  const key = (id, x, y, w, h, label, cls) =>
    `<g class="rc-keyg" data-key="${id}">`
    + `<rect class="rc-key ${cls || ''}" data-key="${id}" x="${x}" y="${y}" width="${w}" height="${h}" rx="6"/>`
    + `<text class="rc-label" x="${x + w / 2}" y="${y + h / 2 + 1}">${label}</text>`
    + `</g>`;
  const keys = [
    key('power', 16, 22, 80, 46, '&#9211;'), key('aux', 104, 22, 80, 46, 'AUX'),
    key('preset1', 16, 78, 50, 46, '1'), key('preset2', 75, 78, 50, 46, '2'), key('preset3', 134, 78, 50, 46, '3'),
    key('preset4', 16, 134, 50, 46, '4'), key('preset5', 75, 134, 50, 46, '5'), key('preset6', 134, 134, 50, 46, '6'),
    key('volUp', 16, 190, 80, 46, '&#128266;+', 'rc-off'), key('volDown', 16, 246, 80, 46, '&#128266;&#8722;', 'rc-off'),
    key('playPause', 104, 190, 80, 102, '&#9654;&#10074;&#10074;'),
    key('prev', 16, 302, 80, 46, '&#9198;'), key('next', 104, 302, 80, 46, '&#9197;'),
    key('thumbsDown', 16, 358, 80, 46, '&#128078;'), key('thumbsUp', 104, 358, 80, 46, '&#128077;'),
  ].join('');
  // A "G" in the corner of a thumbs key that carries a saved group (#863).
  // Hidden until the speaker's group keys are read; paintRemote shows it.
  const gmark = (id, x, y) =>
    `<text class="rc-gmark" data-gmark="${id}" x="${x}" y="${y}" style="display:none">G</text>`;
  // A crescent moon in the same corner for a thumbs key that carries the
  // sleep timer (a key carries a group or the timer, never both, so the two
  // marks never show together). Drawn as a path, not a glyph, so it looks the
  // same whatever fonts the system has.
  const smark = (id, cx, cy) =>
    `<path class="rc-smark" data-smark="${id}" style="display:none" d="M${cx + 2} ${cy - 4.58}A5 5 0 1 0 ${cx + 2} ${cy + 4.58}A5.5 5.5 0 0 1 ${cx + 2} ${cy - 4.58}Z"/>`;
  // A "Z" in the same corner for a key that snoozes a ringing alarm (#1415):
  // the thumbs keys and AUX. A key carries one action, so it never shares the
  // corner with the G or the moon.
  const zmark = (id, x, y) =>
    `<text class="rc-zmark" data-zmark="${id}" x="${x}" y="${y}" style="display:none">Z</text>`;
  // A "B" in the same corner for a thumbs key that moves the bass (#1494).
  const bmark = (id, x, y) =>
    `<text class="rc-bmark" data-bmark="${id}" x="${x}" y="${y}" style="display:none">B</text>`;
  const marks = gmark('thumbsDown', 84, 372) + gmark('thumbsUp', 172, 372)
    + smark('thumbsDown', 85, 369) + smark('thumbsUp', 173, 369)
    + zmark('thumbsDown', 84, 372) + zmark('thumbsUp', 172, 372) + zmark('aux', 172, 36)
    + bmark('thumbsDown', 84, 372) + bmark('thumbsUp', 172, 372);
  return `<svg class="rc-svg" viewBox="0 0 200 428" role="img" aria-label="remote">`
    + `<rect class="rc-body" x="4" y="4" width="192" height="420" rx="30"/>${keys}${marks}</svg>`;
}

// openWebhookKeyMap takes the user to the remote key map in the webhook
// section of THIS speaker's settings: the Multi-Room tab links here so a
// thumbs key that was just bound to a group is seen marked on the remote.
// The section renders asynchronously after the tab switch, so the intent is
// parked and consumed once the key map has been wired and painted.
let pendingKeyMapOpen = false;
export function openWebhookKeyMap(box) {
  if (!box) return;
  state.settingsBox = box;
  pendingKeyMapOpen = true;
  deps.switchView('settings');
}

export function renderAdvancedSections() {
  return {
    group: 'advanced',
    html: `
    <details data-ui2-sec="announce" class="settings-section settings-expert" id="announceSection">
      <summary class="settings-expert-summary">${escapeHtml(t('settingsView.announceHeading'))} <span class="expert-badge">${escapeHtml(t('settingsView.expertBadge'))}</span></summary>
      ${helpBlock(t('settingsView.announceHelp'))}
      <div class="setting-row" style="margin-top:8px">
        <input type="text" id="announceText" class="text-input" maxlength="200" value="${escapeAttr(t('settingsView.announceDefault'))}" placeholder="${escapeAttr(t('settingsView.announcePlaceholder'))}" style="flex:1" />
        <select id="announceLang" style="flex:0 0 130px;" title="${escapeAttr(t('settingsView.announceLangLabel'))}">
          <option value="de">Deutsch</option>
          <option value="en">English</option>
          <option value="fr">Français</option>
          <option value="es">Español</option>
          <option value="it">Italiano</option>
          <option value="nl">Nederlands</option>
          <option value="pl">Polski</option>
          <option value="pt">Português</option>
          <option value="tr">Türkçe</option>
          <option value="uk">Українська</option>
          <option value="ja">日本語</option>
        </select>
        <button class="btn btn-mini" id="announceTranslate" title="${escapeAttr(t('settingsView.announceTranslateTitle'))}">${escapeHtml(t('settingsView.announceTranslate'))}</button>
        <button class="btn btn-mini" id="announceSend">${escapeHtml(t('settingsView.announceSend'))}</button>
      </div>
      <div class="setting-row" style="align-items:center;gap:10px;margin-top:8px">
        <label class="muted small" for="announceVolume" style="flex:0 0 auto">${escapeHtml(t('settingsView.announceVolumeLabel'))}</label>
        <input type="range" id="announceVolume" min="1" max="100" value="25" style="flex:1" />
        <span class="muted small" id="announceVolumeVal" style="flex:0 0 2.5em;text-align:right">25</span>
      </div>
      <small class="muted small">${escapeHtml(t('settingsView.announceCharHint'))}</small>
      <small class="muted small" style="display:block;margin-top:6px">&#9432; ${escapeHtml(t('settingsView.announcePrivacy'))}</small>
      <details style="margin-top:10px">
        <summary class="muted small" style="cursor:pointer">${escapeHtml(t('settingsView.announceExpert'))}</summary>
        <small class="muted small" style="display:block;margin:6px 0">${escapeHtml(t('settingsView.announceExpertHelp'))}</small>
        <div class="setting-row">
          <code id="announceCurl" class="announce-curl"></code>
          <button class="btn btn-mini" id="announceCopy">${escapeHtml(t('settingsView.announceCopy'))}</button>
        </div>
      </details>
      ${discussLink('announce')}
    </details>

    <details data-ui2-sec="keyMap" class="settings-section settings-expert" id="webhookSection">
      <summary class="settings-expert-summary">${escapeHtml(t('settingsView.webhookHeading'))} <span class="expert-badge">${escapeHtml(t('settingsView.expertBadge'))}</span><span class="str-badge" title="${escapeAttr(t('common.strOnlyHint'))}">${escapeHtml(t('common.strOnly'))}</span></summary>
      ${helpBlock(t('settingsView.webhookHelp'))}
      <small class="muted small" style="display:block;margin:0 0 8px">${escapeHtml(t('settingsView.webhookKeyTraceNote'))}</small>
      <div class="rc-wrap" id="webhookKeyMap">
        ${remoteSvg()}
        <div class="rc-side">
          <small class="muted small">${escapeHtml(t('settingsView.webhookRemoteHint'))}</small>
          <small class="muted small rc-legend"><span class="rc-legend-on"></span> ${escapeHtml(t('settingsView.webhookRemoteLegendOn'))}<br><span class="rc-legend-sel"></span> ${escapeHtml(t('settingsView.webhookRemoteLegendSel'))}<br><span class="rc-legend-group"></span> ${escapeHtml(t('settingsView.webhookRemoteLegendGroup'))}<br><span class="rc-legend-sleep"></span> ${escapeHtml(t('settingsView.webhookRemoteLegendSleep'))}<br><span class="rc-legend-snooze"></span> ${escapeHtml(t('settingsView.webhookRemoteLegendSnooze'))}<br><span class="rc-legend-bass"></span> ${escapeHtml(t('settingsView.webhookRemoteLegendBass'))}</small>
          <small class="muted small">${escapeHtml(t('settingsView.webhookRemoteNoVol'))}</small>
        </div>
      </div>
      <div class="setting-row">
        <select id="webhookTarget" style="flex:1;">
          <option value="thumbsUp">${escapeHtml(t('settingsView.webhookKeyThumbsUp'))}</option>
          <option value="thumbsDown">${escapeHtml(t('settingsView.webhookKeyThumbsDown'))}</option>
          <option value="prev">${escapeHtml(t('settingsView.webhookKeyPrev'))}</option>
          <option value="next">${escapeHtml(t('settingsView.webhookKeyNext'))}</option>
          <option value="playPause">${escapeHtml(t('settingsView.webhookKeyPlayPause'))}</option>
          <option value="thumb">${escapeHtml(t('settingsView.webhookKeyThumb'))}</option>
          <option value="preset1">${escapeHtml(t('preset.key', { n: 1 }))}</option>
          <option value="preset2">${escapeHtml(t('preset.key', { n: 2 }))}</option>
          <option value="preset3">${escapeHtml(t('preset.key', { n: 3 }))}</option>
          <option value="preset4">${escapeHtml(t('preset.key', { n: 4 }))}</option>
          <option value="preset5">${escapeHtml(t('preset.key', { n: 5 }))}</option>
          <option value="preset6">${escapeHtml(t('preset.key', { n: 6 }))}</option>
          <option value="aux">AUX</option>
          <option value="power">Power</option>
        </select>
        <select id="webhookMode" style="flex:0 0 170px;">
          <option value="additional">${escapeHtml(t('settingsView.webhookModeAdditional'))}</option>
          <option value="replace">${escapeHtml(t('settingsView.webhookModeReplace'))}</option>
        </select>
      </div>
      <div class="setting-row key-action-row hidden" id="sleepKeyRow">
        <label for="keyActionSelect">${escapeHtml(t('settingsView.keyActionLabel'))}</label>
        <select id="keyActionSelect">
          <option value="">${escapeHtml(t('settingsView.keyActionNone'))}</option>
          <option value="sleep" id="keyActionSleepOpt">${escapeHtml(t('settingsView.sleepKeyLabel'))}</option>
          <option value="snooze">${escapeHtml(t('settingsView.snoozeKeyLabel'))}</option>
          <option value="bassUp" class="key-action-bass">${escapeHtml(t('settingsView.bassKeyUpLabel'))}</option>
          <option value="bassDown" class="key-action-bass">${escapeHtml(t('settingsView.bassKeyDownLabel'))}</option>
        </select>
      </div>
      <small class="muted small hidden" id="sleepKeyNote"></small>
      <small class="muted small" id="webhookModeNote"></small>
      <div class="setting-row">
        <select id="webhookType" style="flex:0 0 200px;">
          <option value="http">${escapeHtml(t('settingsView.webhookTypeHttp'))}</option>
          <option value="wol">${escapeHtml(t('settingsView.webhookTypeWol'))}</option>
          <option value="udp">${escapeHtml(t('settingsView.webhookTypeUdp'))}</option>
        </select>
      </div>
      <div class="setting-row" id="webhookHttpRow">
        <input type="text" id="webhookUrl" autocomplete="off" placeholder="${escapeAttr(t('settingsView.webhookUrlPlaceholder'))}" />
        <select id="webhookMethod" style="flex:0 0 90px;">
          <option value="GET">GET</option>
          <option value="POST">POST</option>
        </select>
      </div>
      <div class="setting-row" id="webhookBodyRow">
        <input type="text" id="webhookBody" autocomplete="off" placeholder="${escapeAttr(t('settingsView.webhookBodyPlaceholder'))}" />
      </div>
      <div class="setting-row hidden" id="webhookWolRow">
        <input type="text" id="webhookMac" autocomplete="off" placeholder="AA:BB:CC:DD:EE:FF" />
      </div>
      <div class="setting-row hidden" id="webhookUdpRow">
        <input type="text" id="webhookUdpHost" autocomplete="off" placeholder="${escapeAttr(t('settingsView.webhookUdpHostPlaceholder'))}" />
        <input type="text" id="webhookUdpPort" autocomplete="off" placeholder="Port" style="flex:0 0 80px;" />
        <select id="webhookUdpEnc" style="flex:0 0 110px;">
          <option value="text">text</option>
          <option value="hex">hex</option>
          <option value="base64">base64</option>
        </select>
      </div>
      <div class="setting-row hidden" id="webhookUdpPayloadRow">
        <input type="text" id="webhookUdpPayload" autocomplete="off" placeholder="${escapeAttr(t('settingsView.webhookUdpPayloadPlaceholder'))}" />
      </div>
      <div class="setting-row">
        <button class="btn btn-mini toggle-btn" id="webhookOn">${escapeHtml(t('settingsView.clockOn'))}</button>
        <button class="btn btn-mini toggle-btn" id="webhookOff">${escapeHtml(t('settingsView.clockOff'))}</button>
        <span style="flex:1;"></span>
        <button class="btn btn-mini" id="webhookTestBtn">${escapeHtml(t('settingsView.webhookTestBtn'))}</button>
        <button class="btn btn-mini btn-primary" id="webhookSaveBtn">${escapeHtml(t('common.save'))}</button>
      </div>
      <div class="setting-row" style="align-items:center;gap:10px;margin-top:10px">
        <small class="muted small" style="flex:1">${escapeHtml(t('settingsView.webhookHaHint'))}</small>
        <button class="btn btn-mini" id="webhookHaGuideBtn">${escapeHtml(t('settingsView.webhookHaGuideBtn'))}</button>
      </div>
      ${discussLink('webhook')}
    </details>

    <details data-ui2-sec="voice" class="settings-section settings-expert">
      <summary class="settings-expert-summary">${escapeHtml(t('settingsView.voiceHeading'))} <span class="expert-badge">${escapeHtml(t('settingsView.expertBadge'))}</span></summary>
      ${helpBlock(t('settingsView.voiceHelp'))}
      <div class="setting-row">
        <button class="btn btn-mini" id="voiceGuideBtn">${escapeHtml(t('settingsView.voiceGuideBtn'))}</button>
      </div>
      ${discussLink('voice')}
    </details>

    <details data-ui2-sec="syncKeys" class="settings-section settings-expert" id="syncKeysSection">
      <summary class="settings-expert-summary">${escapeHtml(t('ui2.speakers.speaker.hwKeysHeading'))}</summary>
      <p class="muted small" id="boxSyncPresetsHelp">${escapeHtml(t('settingsView.syncHardwareKeysHelp'))}</p>
      <div class="setting-row">
        <button class="btn btn-mini" id="boxSyncPresetsBtn">${escapeHtml(t('settingsView.syncHardwareKeys'))}</button>
      </div>
    </details>

    <details data-ui2-sec="urlPreset" class="settings-section settings-expert">
      <summary class="settings-expert-summary">${escapeHtml(t('settingsView.urlPresetHeading'))} <span class="expert-badge">${escapeHtml(t('settingsView.expertBadge'))}</span></summary>
      ${helpBlock(t('settingsView.urlPresetHelp'))}
      <div class="setting-row">
        <select id="urlPresetSlot" style="flex:0 0 130px;">
          ${[...rowSlots(1), ...(boxPresetRows(state.settingsBox) >= 2 ? rowSlots(2) : [])].map(n => `<option value="${n}">${escapeHtml(keyLabel(n, t))}</option>`).join('')}
        </select>
        <input type="text" id="urlPresetName" autocomplete="off" placeholder="${escapeAttr(t('settingsView.urlPresetNamePlaceholder'))}" style="flex:1;" />
      </div>
      <div class="setting-row">
        <input type="text" id="urlPresetUrl" autocomplete="off" placeholder="${escapeAttr(t('settingsView.urlPresetUrlPlaceholder'))}" />
        <button class="btn btn-mini btn-primary" id="urlPresetSaveBtn">${escapeHtml(t('common.save'))}</button>
      </div>
      ${discussLink('urlPreset')}
    </details>

    <details data-ui2-sec="restore" class="settings-section settings-expert">
      <summary class="settings-expert-summary">${escapeHtml(t('settingsView.restoreHeading'))} <span class="exp-badge">${escapeHtml(t('settingsView.experimentalBadge'))}</span></summary>
      ${helpBlock(t('settingsView.restoreHelp'), 'p', 'muted small')}
      <label class="muted small" for="boxRestoreXml">${escapeHtml(t('settingsView.restoreXmlLabel'))}</label>
      <textarea id="boxRestoreXml" rows="5" placeholder="${escapeAttr(t('settingsView.restoreImportPlaceholder'))}" style="width:100%;margin-top:4px"></textarea>
      <div class="setting-row" style="margin-top:6px">
        <button class="btn btn-mini" id="boxRestoreBtn">${escapeHtml(t('settingsView.restoreBtn'))}</button>
      </div>
      <div id="boxRestoreResult" class="muted small" style="margin-top:8px"></div>
    </details>
  `,
  };
}

export function wireAdvancedSections(ctx) {
  const { box } = ctx;

  // Voice control: STR itself will never speak to Alexa (the old skill was
  // Bose's cloud talking to Bose's cloud, and a new one would mean an account,
  // a public endpoint and a bill). What does work is a hub the user runs at
  // home, so the app says so once, in the expert area, and hands over to the
  // written guide rather than trying to explain a Home Assistant setup in a
  // settings panel. One constant, so the target can move to the website later
  // without hunting through the UI.
  {
    const vg = $('voiceGuideBtn');
    if (vg) vg.onclick = () => { try { BrowserOpenURL(VOICE_GUIDE_URL); } catch {} };
    const hg = $('webhookHaGuideBtn');
    if (hg) hg.onclick = () => { try { BrowserOpenURL(HOME_ASSISTANT_GUIDE_URL); } catch {} };
  }
  // Playground discussion links: every expert section links to its own
  // "Show and tell" thread. One handler for all of them, driven by data-url.
  for (const el of document.querySelectorAll('.expert-disc-link')) {
    el.onclick = (e) => { e.preventDefault(); try { BrowserOpenURL(el.dataset.url); } catch {} };
  }

  // Experimental: restore account-linked cloud presets (e.g. Deezer) the box
  // dropped, from STR's snapshot or a saved presets XML the user pastes. Writes
  // them back onto their slots and re-advertises the source; the box usually
  // needs a reboot to re-sync, so we offer one when the agent recommends it.
  async function doCloudRestore(xml) {
    const out = $('boxRestoreResult');
    if (out) out.textContent = t('settingsView.restoreRunning');
    try {
      const r = await RestoreBoxSnapshot(box.host, box.port, xml || '');
      const restored = (r && r.restored) || [];
      const services = (r && r.services) || [];
      const unavailable = (r && r.unavailable) || [];
      const expired = (r && r.expired) || [];
      const parsed = (r && typeof r.parsed === 'number') ? r.parsed : null;
      if (!restored.length && !services.length && !expired.length) {
        // Distinguish "could not read any buttons from the paste" (parsed 0,
        // usually the wrong text) from "read buttons but none are account-bound"
        // (parsed > 0, nothing to restore here), so the guidance is precise.
        if (out) {
          out.textContent = (parsed === 0)
            ? t('settingsView.restoreNoneParsed')
            : (parsed > 0 ? t('settingsView.restoreNoneCloud', { count: parsed }) : t('settingsView.restoreNone'));
        }
        return;
      }
      if (out) {
        const parts = [];
        if (restored.length) {
          // If a written source still reads unavailable, its saved login has
          // likely expired: say so honestly rather than implying a clean success.
          parts.push(unavailable.length
            ? t('settingsView.restoreUnavailable', { slots: restored.join(', '), services: services.join(', '), unavailable: unavailable.join(', ') })
            : t('settingsView.restoreDone', { slots: restored.join(', '), services: services.join(', ') }));
        }
        if (expired.length) {
          // Services whose saved login on the speaker already expired with the Bose
          // cloud: STR did NOT write those buttons (the speaker drops them on its
          // own within seconds) and a reboot cannot bring the login back.
          parts.push(t('settingsView.restoreExpired', { services: expired.join(', ') }));
        }
        out.textContent = parts.join('\n');
      }
      if (r && r.rebootRecommended) {
        const ok = await confirmFor(box, t('speaker.rebootConfirmTitle'), t('settingsView.restoreRebootBody'));
        if (ok) {
          try {
            await RebootBox(box.host, box.port);
            reportDone(out || 'boxRestoreBtn', t('speaker.rebootingToast'));
            setTimeout(deps.discoverBoxes, 35000);
          } catch (e) { reportError(out || 'boxRestoreBtn', e); }
        }
      }
    } catch (e) {
      if (out) out.textContent = '';
      // In the preview the refusal stands right under the restore's result.
      reportError(out || 'boxRestoreBtn', e);
    }
  }
  const restoreBtn = $('boxRestoreBtn');
  if (restoreBtn) restoreBtn.onclick = () => {
    const ta = $('boxRestoreXml');
    doCloudRestore(ta ? ta.value : '');
  };

  // Announcements (#125, beta): a quick test field plus a copy-paste curl command
  // for power users (Home Assistant / scripts). The command is built on the Go
  // side so it carries the agent port that actually answers this box.
  const annText = $('announceText');
  const annSend = $('announceSend');
  const annLang = $('announceLang');
  const annCurl = $('announceCurl');
  const annCopy = $('announceCopy');
  const annVol = $('announceVolume');
  const annVolVal = $('announceVolumeVal');
  const annTranslate = $('announceTranslate');
  if (annVol && annVolVal) {
    annVolVal.textContent = annVol.value;
    annVol.oninput = () => { annVolVal.textContent = annVol.value; };
  }
  // Default the TTS voice to the app's UI language when it is one of the offered
  // voices, so e.g. a German user gets a German voice without having to pick.
  if (annLang) {
    const ui = (document.documentElement.lang || '').slice(0, 2);
    if (ui && [...annLang.options].some(o => o.value === ui)) annLang.value = ui;
  }
  if (annCurl) {
    (async () => {
      try { annCurl.textContent = await AnnounceExample(box.host, box.port); } catch { /* leave blank */ }
    })();
  }
  if (annSend) {
    annSend.onclick = async () => {
      const text = ((annText && annText.value) || '').trim();
      if (!text) return;
      annSend.disabled = true;
      try {
        const vol = annVol ? (parseInt(annVol.value, 10) || 0) : 0;
        await SendAnnounce(box.host, box.port, text, (annLang && annLang.value) || '', vol);
        reportDone(annSend, t('settingsView.announceSentToast'));
      } catch (e) { reportError(annSend, e); }
      annSend.disabled = false;
    };
  }
  if (annTranslate) {
    // Translate the field text into the selected voice language (keyless Google
    // Translate, app-side), then drop it back in the field so the user can
    // review before sending. Same language picker doubles as the TTS voice.
    annTranslate.onclick = async () => {
      const text = ((annText && annText.value) || '').trim();
      if (!text) return;
      annTranslate.disabled = true;
      try {
        const translated = await Translate(text, (annLang && annLang.value) || 'en');
        if (translated && annText) annText.value = translated;
      } catch (e) { reportError(annTranslate, e); }
      annTranslate.disabled = false;
    };
  }
  if (annText) {
    annText.onkeydown = (e) => { if (e.key === 'Enter' && annSend) annSend.click(); };
  }
  if (annCopy && annCurl) {
    annCopy.onclick = async () => {
      try {
        await navigator.clipboard.writeText(annCurl.textContent || '');
        showToast(t('settingsView.announceCopiedToast'));
      } catch { /* clipboard blocked */ }
    };
  }

  // Smart-home / webhook: the remote thumbs keys (up and down are
  // indistinguishable on this firmware, so one shared toggle trigger) fire a
  // user-defined HTTP request the agent persists on the box.
  const whUrl = $('webhookUrl');
  const whMethod = $('webhookMethod');
  const whBody = $('webhookBody');
  const whBodyRow = $('webhookBodyRow');
  const whOn = $('webhookOn');
  const whOff = $('webhookOff');
  const whTest = $('webhookTestBtn');
  const whSave = $('webhookSaveBtn');
  const whTarget = $('webhookTarget');
  const whMode = $('webhookMode');
  const whModeNote = $('webhookModeNote');
  const whType = $('webhookType');
  const whHttpRow = $('webhookHttpRow');
  const whWolRow = $('webhookWolRow');
  const whUdpRow = $('webhookUdpRow');
  const whUdpPayloadRow = $('webhookUdpPayloadRow');
  const whMac = $('webhookMac');
  const whUdpHost = $('webhookUdpHost');
  const whUdpPort = $('webhookUdpPort');
  const whUdpEnc = $('webhookUdpEnc');
  const whUdpPayload = $('webhookUdpPayload');
  // The UDP field a missing target points at: the host while it is empty,
  // the port otherwise.
  const udpField = () => (whUdpHost && !whUdpHost.value.trim() ? whUdpHost : whUdpPort);
  if (whUrl && whOn && whOff && whSave && whTarget) {
    let whEnabled = false;
    // Full config held locally; each target's edits are captured into it before
    // switching target or saving, then the WHOLE config is PUT (a partial PUT
    // would wipe the other keys). buttons keys: preset1..preset6, aux, power,
    // and the trace keys thumbsUp, thumbsDown, prev, next, playPause.
    let cfg = { thumb: {}, buttons: {} };
    // The thumbs keys that carry a saved group on THIS speaker (#863), key id
    // -> template name. A bound key is drawn with the "G" marker and its mode
    // note says the group wins over the webhook.
    let gkBindings = {};
    // The speaker's key actions: which key carries the sleep timer (thumbs
    // keys) or snoozes an alarm (thumbs keys and AUX). kaSupported is false
    // for an agent that predates /api/keyactions.
    let kaDoc = { keys: {} };
    let kaSupported = true;
    // Whether this speaker reports adjustable bass (the settings read, the
    // bass slider's source). A speaker that says no is not offered the bass
    // steps; one already bound keeps its choice visible so it can be removed.
    const bassAvail = !(ctx.bass && ctx.bass.available === false);
    const bassOpts = Array.from(document.querySelectorAll('#keyActionSelect option.key-action-bass'));
    const sleepRow = $('sleepKeyRow');
    const actionSelect = $('keyActionSelect');
    const sleepOpt = $('keyActionSleepOpt');
    const sleepNote = $('sleepKeyNote');
    let prevTarget = whTarget.value || 'thumbsUp';
    const presetIds = new Set(['preset1', 'preset2', 'preset3', 'preset4', 'preset5', 'preset6']);
    // Keys the speaker's own key trace identifies (agent internal/boxlog):
    // additional-only, there is no box reaction to withhold.
    const traceKeyIds = new Set(['thumbsUp', 'thumbsDown', 'prev', 'next', 'playPause']);
    const isModeTarget = (tg) => presetIds.has(tg); // only presets support replace
    const actionOf = (tg) => tg === 'thumb' ? (cfg.thumb || {}) : ((cfg.buttons && cfg.buttons[tg]) || {});
    const paintWh = (en) => {
      whEnabled = en === true;
      whOn.classList.toggle('active', whEnabled === true);
      whOff.classList.toggle('active', whEnabled === false);
    };
    const syncBodyRow = () => {
      // A request body is only sent for non-GET methods.
      if (whBodyRow) whBodyRow.style.display = (whMethod.value === 'GET') ? 'none' : '';
    };
    // Show only the fields the selected transport needs (http | wol | udp). The
    // Test button works for all three (it fires the actual packet/request).
    const syncType = () => {
      const ty = (whType && whType.value) || 'http';
      if (whHttpRow) whHttpRow.classList.toggle('hidden', ty !== 'http');
      if (whBodyRow) whBodyRow.classList.toggle('hidden', ty !== 'http');
      if (whWolRow) whWolRow.classList.toggle('hidden', ty !== 'wol');
      if (whUdpRow) whUdpRow.classList.toggle('hidden', ty !== 'udp');
      if (whUdpPayloadRow) whUdpPayloadRow.classList.toggle('hidden', ty !== 'udp');
      if (ty === 'http') syncBodyRow();
    };
    const loadInto = (tg) => {
      const a = actionOf(tg);
      if (whType) whType.value = a.type || 'http';
      whUrl.value = a.url || '';
      whMethod.value = a.method || 'GET';
      whBody.value = a.body || '';
      if (whMac) whMac.value = a.mac || '';
      if (whUdpHost) whUdpHost.value = a.host || '';
      if (whUdpPort) whUdpPort.value = a.port ? String(a.port) : '';
      if (whUdpEnc) whUdpEnc.value = a.payload_enc || 'text';
      if (whUdpPayload) whUdpPayload.value = a.payload || '';
      paintWh(a.enabled === true);
      whMode.value = a.mode === 'replace' ? 'replace' : 'additional';
      whMode.style.display = isModeTarget(tg) ? '' : 'none';
      const modeNote = isModeTarget(tg)
        ? t('settingsView.webhookModePresetNote')
        : (traceKeyIds.has(tg) ? t('settingsView.webhookModeKeyNote')
          : (tg === 'thumb' ? t('settingsView.webhookThumbSharedNote') : t('settingsView.webhookModeAuxPowerNote')));
      whModeNote.textContent = gkBindings[tg]
        ? t('settingsView.webhookGroupKeyNote', { name: gkBindings[tg] }) + ' ' + modeNote
        : (sleepOn(kaDoc, tg) ? t('settingsView.webhookSleepKeyNote') + ' ' + modeNote
          : (snoozeOn(kaDoc, tg) ? t('settingsView.webhookSnoozeKeyNote') + ' ' + modeNote
            : (bassOn(kaDoc, tg) ? t('settingsView.webhookBassKeyNote') + ' ' + modeNote : modeNote)));
      syncSleepRow(tg);
      syncType();
      paintRemote(tg);
    };
    // The built-in action picker shows for the keys that can carry one: the
    // thumbs keys (sleep timer, snooze or a bass step) and AUX (snooze only).
    // A key that forms a group cannot take one: the picker is disabled and
    // says which group to take off the key first.
    const keyActionHelp = (tg, action) => {
      if (isBassAction(action)) {
        return t('settingsView.bassKeyHelp') + (bassAvail ? '' : ' ' + t('settingsView.bassKeyUnavailable'));
      }
      if (action === 'sleep') return t('settingsView.sleepKeyHelp');
      if (tg === 'aux') return t('settingsView.snoozeKeyAuxHelp');
      if (action === 'snooze') return t('settingsView.snoozeKeyHelp');
      return t('settingsView.sleepKeyHelp') + ' ' + t('settingsView.snoozeKeyHelp')
        + (bassAvail ? '' : ' ' + t('settingsView.bassKeyUnavailable'));
    };
    const syncSleepRow = (tg) => {
      if (!sleepRow || !actionSelect || !sleepNote) return;
      const bindable = KEY_ACTION_KEY_IDS.includes(tg);
      sleepRow.classList.toggle('hidden', !bindable);
      sleepNote.classList.toggle('hidden', !bindable);
      if (!bindable) return;
      if (sleepOpt) {
        const sleepFits = SLEEP_KEY_IDS.includes(tg);
        sleepOpt.hidden = !sleepFits;
        sleepOpt.disabled = !sleepFits;
      }
      const group = sleepBlockedBy(gkBindings, tg);
      const cur = actionOn(kaDoc, tg);
      // The bass steps go on the thumbs keys only, and only on a speaker
      // with adjustable bass (the bound one stays selectable to remove it).
      for (const o of bassOpts) {
        const fits = SLEEP_KEY_IDS.includes(tg);
        o.hidden = !fits;
        o.disabled = !fits || (!bassAvail && o.value !== cur);
      }
      actionSelect.value = cur;
      actionSelect.disabled = !kaSupported || (!!group && !cur);
      sleepNote.textContent = !kaSupported
        ? t('settingsView.sleepKeyNeedsUpdate')
        : (group ? t('settingsView.keyActionGroupConflict', { name: group }) : keyActionHelp(tg, cur));
    };
    if (actionSelect) actionSelect.onchange = async () => {
      const tg = whTarget.value;
      const action = actionSelect.value;
      const before = actionOn(kaDoc, tg);
      if (action && sleepBlockedBy(gkBindings, tg)) { syncSleepRow(tg); return; }
      const next = withAction(kaDoc, tg, action);
      actionSelect.disabled = true;
      try {
        await SaveKeyActions(box.host, box.port, next);
        kaDoc = normalizeKeyActions(next);
        const toast = action === 'sleep' ? 'settingsView.sleepKeyOnToast'
          : action === 'snooze' ? 'settingsView.snoozeKeyOnToast'
            : isBassAction(action) ? 'settingsView.bassKeyOnToast'
              : (before === 'snooze' ? 'settingsView.snoozeKeyOffToast'
                : (isBassAction(before) ? 'settingsView.bassKeyOffToast' : 'settingsView.sleepKeyOffToast'));
        reportDone(actionSelect, t(toast));
      } catch (e) {
        // An agent from before the bass steps refuses them as an unknown
        // action; say what to do instead of showing the raw refusal.
        reportError(actionSelect, isBassAction(action) && /unknown key action/i.test(String((e && e.message) || e))
          ? t('settingsView.bassKeyNeedsUpdate') : e);
      }
      // repaint from what the speaker holds now (a refusal leaves it as it was)
      loadInto(tg);
    };
    // paintRemote marks every key that carries an enabled trigger and the key
    // currently being edited. The legacy shared thumbs action lights both
    // thumbs keys, since it fires for either. A broken entry (an address that
    // does not parse) lights too: it is on the key, and the user has to be
    // able to find it, open it and switch it off.
    const configured = webhookPresent;
    const paintRemote = (selected) => {
      const thumbShared = configured(cfg.thumb);
      document.querySelectorAll('.rc-key[data-key]').forEach((el) => {
        const k = el.getAttribute('data-key');
        if (el.classList.contains('rc-off')) return;
        const on = configured(cfg.buttons && cfg.buttons[k]) || ((k === 'thumbsUp' || k === 'thumbsDown') && thumbShared);
        el.classList.toggle('rc-on', on);
        el.classList.toggle('rc-sel', k === selected || (selected === 'thumb' && (k === 'thumbsUp' || k === 'thumbsDown')));
        el.classList.toggle('rc-group', !!gkBindings[k]);
        el.classList.toggle('rc-sleep', !gkBindings[k] && sleepOn(kaDoc, k));
        el.classList.toggle('rc-snooze', !gkBindings[k] && snoozeOn(kaDoc, k));
        el.classList.toggle('rc-bass', !gkBindings[k] && bassOn(kaDoc, k));
      });
      document.querySelectorAll('.rc-bmark[data-bmark]').forEach((m) => {
        const k = m.getAttribute('data-bmark');
        m.style.display = !gkBindings[k] && bassOn(kaDoc, k) ? '' : 'none';
      });
      document.querySelectorAll('.rc-zmark[data-zmark]').forEach((m) => {
        const k = m.getAttribute('data-zmark');
        m.style.display = !gkBindings[k] && snoozeOn(kaDoc, k) ? '' : 'none';
      });
      document.querySelectorAll('.rc-gmark[data-gmark]').forEach((m) => {
        m.style.display = gkBindings[m.getAttribute('data-gmark')] ? '' : 'none';
      });
      document.querySelectorAll('.rc-smark[data-smark]').forEach((m) => {
        const k = m.getAttribute('data-smark');
        m.style.display = !gkBindings[k] && sleepOn(kaDoc, k) ? '' : 'none';
      });
    };
    document.querySelectorAll('.rc-keyg[data-key]').forEach((g) => {
      g.addEventListener('click', () => {
        const rect = g.querySelector('.rc-key');
        if (!rect || rect.classList.contains('rc-off')) return;
        // A thumbs key lit only by the shared thumbs entry opens that entry,
        // so what lights the key is what the form shows and can switch off.
        const k = keyMapTarget(cfg, g.getAttribute('data-key'));
        captureInto(prevTarget);
        whTarget.value = k;
        prevTarget = k;
        loadInto(k);
      });
    });
    const captureInto = (tg) => {
      const ty = (whType && whType.value) || 'http';
      const a = { enabled: whEnabled === true, type: ty };
      if (ty === 'wol') {
        a.mac = (whMac && whMac.value.trim()) || '';
      } else if (ty === 'udp') {
        a.host = (whUdpHost && whUdpHost.value.trim()) || '';
        a.port = parseInt((whUdpPort && whUdpPort.value.trim()) || '0', 10) || 0;
        a.payload = (whUdpPayload && whUdpPayload.value) || '';
        a.payload_enc = (whUdpEnc && whUdpEnc.value) || 'text';
      } else {
        a.method = whMethod.value;
        a.url = whUrl.value.trim();
        a.body = whBody.value.trim();
        a.content_type = '';
      }
      if (tg === 'thumb') {
        cfg.thumb = a;
      } else {
        if (!cfg.buttons) cfg.buttons = {};
        if (isModeTarget(tg)) a.mode = whMode.value;
        cfg.buttons[tg] = a;
      }
    };
    (async () => {
      // Both reads in parallel: the group keys only decorate the map, so an
      // older agent without /api/groupkeys (404) simply shows no marker.
      const [w, g, ka] = await Promise.allSettled([GetWebhooks(box.host, box.port), GetGroupKeys(box.host, box.port), GetKeyActions(box.host, box.port)]);
      const wv = w.status === 'fulfilled' ? w.value : null;
      cfg = { thumb: (wv && wv.thumb) || {}, buttons: (wv && wv.buttons) || {} };
      gkBindings = normalizeDoc(g.status === 'fulfilled' ? g.value : null).bindings;
      // An agent that predates the sleep-timer key answers its index page or
      // a 404: the switch then asks for an update instead of failing on save.
      kaSupported = ka.status === 'fulfilled';
      kaDoc = normalizeKeyActions(kaSupported ? ka.value : null);
      loadInto(prevTarget);
      // Arrived from the Multi-Room tab's "show on the key map" link: open the
      // section and bring the remote into view, once, now that it is painted.
      if (pendingKeyMapOpen) {
        pendingKeyMapOpen = false;
        // The remote itself, not the section: the key map is the thing the user
        // came to look at, and the section header can be a screen above it.
        revealInSettings($('webhookKeyMap') || $('webhookSection'));
      }
    })();
    whTarget.onchange = () => { captureInto(prevTarget); prevTarget = whTarget.value; loadInto(whTarget.value); };
    whOn.onclick = () => paintWh(true);
    whOff.onclick = () => paintWh(false);
    whMethod.onchange = syncBodyRow;
    if (whType) whType.onchange = syncType;
    whSave.onclick = async () => {
      const tg = whTarget.value;
      const ty = (whType && whType.value) || 'http';
      clearFieldErrors(whUrl, whMac, whUdpHost, whUdpPort);
      if (whEnabled) {
        if (ty === 'http' && !whUrl.value.trim()) { reportFieldError(whUrl, t('settingsView.webhookUrlRequired')); return; }
        if (ty === 'wol' && !(whMac && whMac.value.trim())) { reportFieldError(whMac, t('settingsView.webhookMacRequired')); return; }
        if (ty === 'wol' && !validWolMac(whMac.value)) { reportFieldError(whMac, t('settingsView.webhookMacInvalid')); return; }
        if (ty === 'udp' && (!(whUdpHost && whUdpHost.value.trim()) || !(whUdpPort && parseInt(whUdpPort.value, 10) > 0))) { reportFieldError(udpField(), t('settingsView.webhookUdpRequired')); return; }
      }
      captureInto(tg);
      try {
        await SaveWebhookConfig(box.host, box.port, cfg);
        paintRemote(tg);
        reportDone(whSave, t('settingsView.webhookSavedToast'));
      } catch (e) { reportError(whSave, e); }
    };
    whTest.onclick = async () => {
      // Build the action by type and fire it once, the same for http/udp/wol.
      const ty = (whType && whType.value) || 'http';
      const a = { enabled: true, type: ty };
      clearFieldErrors(whUrl, whMac, whUdpHost, whUdpPort);
      if (ty === 'wol') {
        if (!(whMac && whMac.value.trim())) { reportFieldError(whMac, t('settingsView.webhookMacRequired')); return; }
        if (!validWolMac(whMac.value)) { reportFieldError(whMac, t('settingsView.webhookMacInvalid')); return; }
        a.mac = whMac.value.trim();
      } else if (ty === 'udp') {
        if (!(whUdpHost && whUdpHost.value.trim()) || !(whUdpPort && parseInt(whUdpPort.value, 10) > 0)) { reportFieldError(udpField(), t('settingsView.webhookUdpRequired')); return; }
        a.host = whUdpHost.value.trim();
        a.port = parseInt(whUdpPort.value, 10);
        a.payload = (whUdpPayload && whUdpPayload.value) || '';
        a.payload_enc = (whUdpEnc && whUdpEnc.value) || 'text';
      } else {
        if (!whUrl.value.trim()) { reportFieldError(whUrl, t('settingsView.webhookUrlRequired')); return; }
        a.method = whMethod.value;
        a.url = whUrl.value.trim();
        a.body = whBody.value.trim();
        a.content_type = '';
      }
      whTest.disabled = true;
      try {
        const r = await TestWebhookAction(box.host, box.port, JSON.stringify(a));
        reportDone(whTest, t('settingsView.webhookTestOk', { status: (r && r.status) || '' }));
      } catch (e) {
        reportError(whTest, t('settingsView.webhookTestFailed', { err: String(e) }));
      } finally {
        whTest.disabled = false;
      }
    };
  }

  // Repair the hardware keys: push every preset to the speaker again. STR
  // already does this by itself (after every start, on every wake from standby
  // and in the idle pass), so the action sits here in the expert area for the
  // rare key that stays dead after a restart.
  const syncBtn = $('boxSyncPresetsBtn');
  if (syncBtn) {
    syncBtn.onclick = async () => {
      syncBtn.disabled = true;
      syncBtn.textContent = t('settingsView.syncing');
      try {
        const r = await SyncBoxPresets(box.host, box.port);
        const synced = r && r.synced != null ? r.synced : 0;
        reportDone(syncBtn, t('settingsView.syncDoneToast', { n: synced }));
      } catch (e) { reportError(syncBtn, e); }
      syncBtn.disabled = false;
      syncBtn.textContent = t('settingsView.syncHardwareKeys');
    };
  }

  // Expert: put a custom audio URL on a preset. Stored as a normal radio preset,
  // so the box plays the URL over UPnP when the key (hardware or app) is pressed,
  // using STR's robust play path (DIDL metadata, HTTP handling) rather than a raw
  // UPnP push. Replaces the current source on press (no auto-resume).
  const urlPresetBtn = $('urlPresetSaveBtn');
  if (urlPresetBtn) {
    urlPresetBtn.onclick = async () => {
      const slot = parseInt(($('urlPresetSlot') || {}).value, 10);
      const url = (($('urlPresetUrl') || {}).value || '').trim();
      const name = (($('urlPresetName') || {}).value || '').trim() || t('settingsView.urlPresetDefaultName');
      clearFieldErrors('urlPresetSlot', 'urlPresetUrl');
      if (!slot || !/^https?:\/\/\S+/i.test(url)) { reportFieldError(slot ? 'urlPresetUrl' : 'urlPresetSlot', t('settingsView.urlPresetInvalid')); return; }
      urlPresetBtn.disabled = true;
      try {
        await SetPreset(box.host, box.port, slot, name, url, '', 0, '', '');
        reportDone(urlPresetBtn, t('settingsView.urlPresetSaved', { n: keyNum(slot, t) }));
        if (state.currentBox && state.currentBox.host === box.host) await deps.loadPresets();
      } catch (e) { reportError(urlPresetBtn, e); }
      urlPresetBtn.disabled = false;
    };
  }
}
