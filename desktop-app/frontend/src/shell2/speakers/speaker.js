// shell2/speakers/speaker.js: Speakers > one speaker, the page of one speaker
// in the UI 2.0 preview (phase 3, #1287; docs/design/ui-2.0/speakers.png,
// right column).
//
// The page is a head (the speaker's name, model and address, its state pill
// and what it plays) and five keyboard-operable tabs: Overview, Sound,
// Network, Keys and alarms, Maintenance.
//
// Which speaker: always state.settingsBox, the speaker the classic Settings
// view loads. Opening a speaker (router, open('speaker', { host })) makes it
// the speaker you control as well (lock-step, exactly what the classic
// Settings dropdown does through speakerPicked), so the page, the selected
// sidebar row and the player always name the same speaker. Choosing another
// row in the sidebar while this page is open moves the page to that speaker
// (pickBoxFromShell reloads the settings; the router repaints this head).
//
// The panels hold the classic Speaker settings, not a copy of them. The
// classic view renders every section into #settingsBody on each load
// (views/settings.js renderBoxSettings, one module per group under
// views/settings/), and each section carries a data-ui2-sec key. With the
// shell mounted, two hooks in settings.js hand that body to this page:
//
//   placeSettingsBody()        moves #settingsBody itself into the open tab's
//                              panel (and back into #view-settings when no
//                              speaker is chosen). The node is never copied
//                              and never rebuilt here, so every load, retry,
//                              reconnect banner and "speaker unreachable"
//                              panel of the classic view lands in the open
//                              tab exactly as it would in the classic layout:
//                              an offline speaker explains itself where you
//                              look, and a switch to another speaker blanks
//                              the old values the same way.
//   arrangeSpeakerSections()   after each render: sorts the sections into one
//                              wrapper per tab (SPEAKER_SECTIONS, the order
//                              below), unwraps the classic accordion groups,
//                              and adds two cards of its own: the Network
//                              summary on Overview and the hardware key repair
//                              on Keys and alarms (the classic button and its
//                              help, moved out of the Actions grid).
//
// CSS under html.ui2 (speaker.css) shows only the open tab's wrapper. Every
// section keeps its ids and its handlers. revealInSettings (views/settings/
// shared.js) opens the tab of the element it reveals (revealSpeakerTabOf), so
// "Update" next to the firmware, the Wi-Fi form and the Multi-room tab's way
// to the remote key map still land on their control.
//
// Where every classic section lives now (the checklist; the e2e spec
// desktop-ui2-speakers-page.spec.mjs walks the five tabs and finds every
// classic control in its tab):
//
//   overview     status (STR version, update, update all, USB stick, SSH
//                restart advice), speakerInfo (model, Bose firmware, device
//                id, firmware update guide), netSummary (new), phone (QR,
//                address, copy, home screen how-to), name (rename), language,
//                sources;
//   sound        volume (volume, balance, start volume), bass, levels (centre
//                and surround), resumeOnPower, clock, displayTrack, displayMsg,
//                displaySplash, displayNotices, arcade, airplay, announce;
//   network      wifi (network, switch form, stored networks, forget),
//                region, musicLib, usServices (Pandora and iHeartRadio);
//   keys         hwKeys (new: the "Repair speaker buttons" action), keyMap
//                (remote key map, key actions incl. sleep, snooze and bass,
//                webhooks), copyPresets, urlPreset, voice;
//   maintenance  actions (identify, save logs, support mail, restart, remove
//                a conflicting tool or cloud address, factory reset, remove
//                STR), backup, ssh, foreign (other tools' changes), restore.
//
// The classic Settings dropdown listed both halves of a stereo pair; the
// sidebar and the list show a pair as one row that opens its main speaker, so
// the head of a half's page leads to the other half (otherPairHalf).
//
// Classic Speaker settings has no alarm settings; the tab's name follows the
// mockup and carries the sleep and snooze key actions.
//
// Restart, remove and reset sit in Maintenance; every confirmation that acts
// on this speaker names it in the preview (shared.js confirmFor).
//
// Pure parts are exported for vitest; the DOM work happens only inside the
// render and hook functions, never at import.

import { t } from '../../i18n/index.js';
import { escapeAttr, escapeHtml } from '../../utils.js';
import { state } from '../../state.js';
import { inStereoPair, pairMemberBoxes, stereoPairsOf } from '../../groups.js';
import { speakerTileState } from '../../speakertile.js';
import { chipHTML, speakerChipKinds, speakerPillKind, statusPillHTML } from '../pill.js';
import { SPEAKER_TABS, SPEAKER_TAB_LABEL_KEYS, normalizeTab, tabIndexFor } from './routes.js';
import './speaker.css';

const tabId = (tab) => `ui2SpkTab-${tab}`;
const panelId = (tab) => `ui2SpkPanel-${tab}`;

// SPEAKER_SECTIONS: every section of the classic Speaker settings (its
// data-ui2-sec key) and the tab it lives on, in the order the tab shows them.
// netSummary and hwKeys are the page's own cards (arrangeSpeakerSections).
export const SPEAKER_SECTIONS = Object.freeze([
  ['status', 'overview'],
  ['speakerInfo', 'overview'],
  ['netSummary', 'overview'],
  ['phone', 'overview'],
  ['name', 'overview'],
  ['language', 'overview'],
  ['sources', 'overview'],
  ['volume', 'sound'],
  ['bass', 'sound'],
  ['levels', 'sound'],
  ['resumeOnPower', 'sound'],
  ['clock', 'sound'],
  ['displayTrack', 'sound'],
  ['displayMsg', 'sound'],
  ['displaySplash', 'sound'],
  ['displayNotices', 'sound'],
  ['arcade', 'sound'],
  ['airplay', 'sound'],
  ['announce', 'sound'],
  ['wifi', 'network'],
  ['region', 'network'],
  ['musicLib', 'network'],
  ['usServices', 'network'],
  ['hwKeys', 'keys'],
  ['keyMap', 'keys'],
  ['copyPresets', 'keys'],
  ['urlPreset', 'keys'],
  ['voice', 'keys'],
  ['actions', 'maintenance'],
  ['backup', 'maintenance'],
  ['ssh', 'maintenance'],
  ['foreign', 'maintenance'],
  ['restore', 'maintenance'],
].map((e) => Object.freeze(e)));

// The two cards the page adds; every other key comes from the classic markup.
export const PAGE_SECTIONS = Object.freeze(['netSummary', 'hwKeys']);

// sectionTab is the tab a section key lives on. A section nobody mapped yet
// (added to the classic view later) shows on Overview rather than nowhere.
export function sectionTab(key) {
  const hit = SPEAKER_SECTIONS.find(([k]) => k === key);
  return hit ? hit[1] : 'overview';
}

// speakerHead is what the head of the page says about a speaker: its name,
// and the model and address under it.
export function speakerHead(box, label) {
  if (!box) return null;
  const name = (typeof label === 'function' ? label(box) : '') || box.friendlyName || box.name || box.host;
  return { name, meta: [box.model, box.host].filter(Boolean).join(' · ') };
}

// speakerConfirmTitle is the title of a confirmation that acts on one
// speaker: in the preview it names the speaker, in the classic layout it is
// the title unchanged (views/settings/shared.js confirmFor).
export function speakerConfirmTitle(title, name) {
  if (!state.ui2Shell || !name) return title;
  return t('ui2.speakers.speaker.confirmTitle', { name, action: title });
}

// speakerStateKind is the one pill the head shows. The tile flags are the
// sidebar's (speakertile.js), so the two never disagree; a speaker whose Bose
// side answers while its agent does not yet (strSilent) is starting up, which
// the tile does not derive.
export function speakerStateKind(box, tile) {
  if (!box) return '';
  if (!box.offline && box.kind !== 'stock' && box.strSilent) return 'startingUp';
  return speakerPillKind(tile);
}

// speakerStateHTML: the pill, the chips (update, no internet) and what the
// speaker plays, as one line under the head.
export function speakerStateHTML(box, tile, playing) {
  if (!box) return '';
  const kind = speakerStateKind(box, tile);
  const chips = speakerChipKinds(tile).map(chipHTML).join('');
  const what = playing ? `<span class="sps-now">${escapeHtml(t('ui2.speakers.speaker.nowPlaying', { what: playing }))}</span>` : '';
  return statusPillHTML(kind) + chips + what;
}

const SIGNAL_KEYS = Object.freeze({
  EXCELLENT_SIGNAL: 'signal.excellent',
  GOOD_SIGNAL: 'signal.good',
  MARGINAL_SIGNAL: 'signal.marginal',
  POOR_SIGNAL: 'signal.poor',
  NO_SIGNAL: 'signal.none',
});

// networkSummaryRows is the Overview's short answer to "how is it
// connected", from the same networkView (netview.js) the Network tab's
// section reads: [labelKey, value] rows, or a single note.
export function networkSummaryRows(nv) {
  const v = nv || {};
  if (v.kind === 'wired') {
    return { rows: [['settingsView.wlanIp', v.ip || '-']], note: 'settingsView.wlanWired' };
  }
  if (v.kind === 'wifi' || v.kind === 'wifi-unnamed') {
    const sig = SIGNAL_KEYS[v.signal] ? t(SIGNAL_KEYS[v.signal]) : (v.signal || (v.coprocessor ? t('signal.notReported') : '-'));
    return {
      rows: [
        ['settingsView.wlanSsid', v.ssid || t('settingsView.wlanSsidUnknown')],
        ['settingsView.wlanSignal', sig],
        ['settingsView.wlanIp', v.ip || '-'],
      ],
      note: '',
    };
  }
  if (v.kind === 'unknown-link') {
    return { rows: [['settingsView.netConnection', t('settingsView.netConnUnknown')], ['settingsView.wlanIp', v.ip || '-']], note: '' };
  }
  return { rows: [], note: 'settingsView.wlanNotConnected' };
}

export function networkSummaryHTML(nv) {
  const { rows, note } = networkSummaryRows(nv);
  const kv = rows.map(([k, val]) => `<div class="kv-row"><span class="kv-key">${escapeHtml(t(k))}</span>`
    + `<span class="kv-val">${escapeHtml(val)}</span></div>`).join('');
  return `<h3>${escapeHtml(t('ui2.speakers.tab.network'))}</h3>${kv}`
    + (note ? `<div class="muted small">${escapeHtml(t(note))}</div>` : '')
    + `<button type="button" class="btn btn-mini sps-to-tab" data-to-tab="network">${escapeHtml(t('ui2.speakers.speaker.netMore'))}</button>`;
}

// speakerFrameHTML is the frame built once: the head, the tab row and the
// five panels, the first tab selected.
export function speakerFrameHTML(tab = SPEAKER_TABS[0]) {
  const on = normalizeTab(tab);
  const tabs = SPEAKER_TABS.map((k) => {
    const sel = k === on;
    return `<button type="button" role="tab" class="sps-tab" id="${tabId(k)}" data-tab="${k}"`
      + ` aria-controls="${panelId(k)}" aria-selected="${sel}" tabindex="${sel ? 0 : -1}">`
      + `${escapeHtml(t(SPEAKER_TAB_LABEL_KEYS[k]))}</button>`;
  }).join('');
  const panels = SPEAKER_TABS.map((k) => `<div class="sps-panel" role="tabpanel" id="${panelId(k)}" data-tab="${k}"`
    + ` aria-labelledby="${tabId(k)}" tabindex="0"${k === on ? '' : ' hidden'}></div>`).join('');
  return '<div class="sps-head">'
    + '<div class="sps-head-text">'
    + '<h3 class="sps-name" id="ui2SpkPageName"></h3>'
    + '<p class="sps-meta" id="ui2SpkPageMeta"></p>'
    + '<p class="sps-state" id="ui2SpkPageState" hidden></p>'
    + '<p class="sps-half" id="ui2SpkPageHalf" hidden></p>'
    + `<p class="sps-none" id="ui2SpkPageNone" hidden>${escapeHtml(t('ui2.speakers.none'))}</p>`
    + '</div>'
    + '<div class="sps-head-actions" id="ui2SpkPageActions"></div>'
    + '</div>'
    + `<div class="sps-tabs" role="tablist" id="ui2SpkTabs">${tabs}</div>`
    + `<div class="sps-panels" id="ui2SpkPanels">${panels}</div>`;
}

// otherPairHalf is the other half of the live stereo pair box belongs to, or
// null. The sidebar and the Speakers list show a pair as one row that opens
// its main speaker, so the page itself leads to the other half: the classic
// Settings dropdown listed both, and each half has its own settings.
export function otherPairHalf(box, zoneLive, boxes) {
  if (!box) return null;
  const pair = stereoPairsOf(zoneLive || {}).find((p) => inStereoPair(box, p));
  if (!pair) return null;
  const other = pairMemberBoxes(pair, boxes).map((x) => x.box)
    .find((b) => b && b.host && b.host !== box.host);
  return other || null;
}

// tabsLabel is the tab row's accessible name: it names the speaker.
export function tabsLabel(head) {
  return head ? t('ui2.speakers.tabsLabel', { name: head.name }) : '';
}

let frameRoot = null;
// The five panels. Built with the frame, then carried into #view-settings
// (placeSettingsBody), so the open tab's settings sit in the view they belong
// to: the tab row in the area head names them (aria-controls), the view shows
// them, and everything that looks for a setting inside #view-settings still
// finds it.
let panelsEl = null;
let activeTab = SPEAKER_TABS[0];
let onTab = null;
let openFn = null;
let hasHead = false;
let lastState = '';
let lastHalf = '';
// The ctx of the last settings render (arrangeSpeakerSections).
let lastArrangeCtx = null;

export function currentSpeakerTab() {
  return activeTab;
}

// speakerPanel is the panel of one tab; null before the page was first
// rendered.
export function speakerPanel(tab) {
  if (!panelsEl) return null;
  return panelsEl.querySelector(`#${panelId(normalizeTab(tab))}`);
}

// selectSpeakerTab shows one tab's panel, gives that tab the Tab stop and
// carries the settings body into it.
export function selectSpeakerTab(tab, { focus = false } = {}) {
  activeTab = normalizeTab(tab);
  if (!frameRoot) return;
  if (panelsEl) panelsEl.dataset.tab = activeTab;
  frameRoot.querySelectorAll('.sps-tab').forEach((b) => {
    const on = b.dataset.tab === activeTab;
    b.setAttribute('aria-selected', String(on));
    b.tabIndex = on ? 0 : -1;
    if (on && focus) b.focus();
  });
  if (panelsEl) panelsEl.querySelectorAll('.sps-panel').forEach((p) => { p.hidden = !hasHead || p.dataset.tab !== activeTab; });
  placeSettingsBody();
}

// placeSettingsBody puts the classic settings body (#settingsBody) where the
// preview shows it: in the open tab's panel while a speaker is on the page,
// otherwise back in #view-settings, where its "no speaker" panel and its
// way to the setup show below the page head. A no-op without the shell, and
// when the body already sits where it belongs (so a repaint never moves the
// node that has the keyboard focus). settings.js calls it once the body
// exists (mountSettingsShell runs only on the first load of the view).
export function placeSettingsBody() {
  if (!state.ui2Shell || typeof document === 'undefined') return;
  const body = document.getElementById('settingsBody');
  const view = document.getElementById('view-settings');
  if (!body || !view) return;
  // A body rendered before the shell mounted (the preview's storage came
  // late, classic Speaker settings were open) is still the classic accordion:
  // sort it now, with the ctx of the render that built it, or every tab
  // would show all of it until the next load.
  if (hasHead && lastArrangeCtx && bodyUnsorted(body)) {
    arrangeSpeakerSections(lastArrangeCtx);
    return;
  }
  if (panelsEl && panelsEl.parentNode !== view) view.appendChild(panelsEl);
  const panel = hasHead ? speakerPanel(activeTab) : null;
  const home = panel || view;
  if (body.parentNode !== home) home.appendChild(body);
  // The classic list refresh (rediscover the speakers, then reload this one)
  // keeps its place in the page head; the speaker dropdown next to it is the
  // sidebar's job in the preview (speaker.css hides it).
  const refresh = document.getElementById('settingsRefreshBtn');
  const actions = frameRoot && frameRoot.querySelector('#ui2SpkPageActions');
  if (refresh && actions && refresh.parentNode !== actions) {
    if (!refresh.getAttribute('aria-label') && refresh.title) refresh.setAttribute('aria-label', refresh.title);
    actions.appendChild(refresh);
  }
}

// bodyUnsorted: the settings body still holds classic markup the tabs have
// not taken in (an accordion group, or a section outside every tab wrapper).
export function bodyUnsorted(body) {
  if (!body || typeof body.querySelector !== 'function') return false;
  if (body.querySelector('details.settings-group')) return true;
  return [...body.querySelectorAll('.settings-section')].some((el) => !el.closest('.sps-sec'));
}

// arrangeSpeakerSections sorts a freshly rendered settings body into the five
// tabs. Called by renderBoxSettings after its handlers are on; a no-op without
// the shell. ctx is the render's ctx ({ box, nv, ... }), kept also without the
// shell so a late mount can sort the body it finds (placeSettingsBody).
export function arrangeSpeakerSections(ctx = {}) {
  lastArrangeCtx = ctx;
  if (!state.ui2Shell || typeof document === 'undefined') return;
  const body = document.getElementById('settingsBody');
  if (!body) return;
  const section = (key, inner) => {
    const el = document.createElement('div');
    el.className = 'settings-section sps-own';
    el.dataset.ui2Sec = key;
    el.innerHTML = inner;
    return el;
  };
  // Keys and alarms: "Repair speaker buttons" and its help, moved (handler
  // and all) out of the Actions grid into a card of their own.
  const sync = body.querySelector('#boxSyncPresetsBtn');
  if (sync) {
    const help = sync.nextElementSibling && sync.nextElementSibling.tagName === 'P' ? sync.nextElementSibling : null;
    const card = section('hwKeys', `<h3>${escapeHtml(t('ui2.speakers.speaker.hwKeysHeading'))}</h3><div class="actions-grid"></div>`);
    const grid = card.querySelector('.actions-grid');
    grid.appendChild(sync);
    if (help) grid.appendChild(help);
    body.appendChild(card);
  }
  // Overview: the Network summary, read from the same answer the Network tab
  // shows in full.
  const net = section('netSummary', networkSummaryHTML(ctx.nv));
  net.querySelector('.sps-to-tab').addEventListener('click', () => {
    selectSpeakerTab('network', { focus: true });
    if (typeof onTab === 'function') onTab(activeTab);
  });
  body.appendChild(net);

  const wraps = {};
  for (const tab of SPEAKER_TABS) {
    const w = document.createElement('div');
    w.className = 'sps-sec';
    w.dataset.ui2Tab = tab;
    wraps[tab] = w;
  }
  for (const [key, tab] of SPEAKER_SECTIONS) {
    const el = body.querySelector(`[data-ui2-sec="${key}"]`);
    if (el) wraps[tab].appendChild(el);
  }
  // A section without a place yet still shows, on Overview.
  body.querySelectorAll('.settings-section').forEach((el) => {
    if (!el.closest('.sps-sec')) wraps[sectionTab(el.dataset.ui2Sec)].appendChild(el);
  });
  // The accordion groups are empty now; the tabs replace them.
  body.querySelectorAll('details.settings-group').forEach((d) => d.remove());
  for (const tab of SPEAKER_TABS) body.appendChild(wraps[tab]);
  placeSettingsBody();
}

// revealSpeakerTabOf opens the tab that holds el (revealInSettings: a control
// another place points at). False when the page does not hold it.
export function revealSpeakerTabOf(el) {
  if (!state.ui2Shell || !el || typeof el.closest !== 'function') return false;
  const wrap = el.closest('.sps-sec[data-ui2-tab]');
  if (!wrap) return false;
  if (wrap.dataset.ui2Tab !== activeTab) {
    selectSpeakerTab(wrap.dataset.ui2Tab);
    if (typeof onTab === 'function') onTab(activeTab);
  }
  return true;
}

function wireFrame(root) {
  const half = root.querySelector('#ui2SpkPageHalf');
  if (half) {
    half.addEventListener('click', (e) => {
      const b = e.target.closest('.sps-half-btn');
      if (b && typeof openFn === 'function') openFn('speaker', { host: b.dataset.host });
    });
  }
  const tabs = root.querySelector('#ui2SpkTabs');
  if (!tabs) return;
  tabs.addEventListener('click', (e) => {
    const b = e.target.closest('.sps-tab');
    if (!b) return;
    selectSpeakerTab(b.dataset.tab);
    if (typeof onTab === 'function') onTab(activeTab);
  });
  tabs.addEventListener('keydown', (e) => {
    const b = e.target.closest('.sps-tab');
    if (!b) return;
    const list = [...tabs.querySelectorAll('.sps-tab')];
    const rtl = getComputedStyle(tabs).direction === 'rtl';
    const next = tabIndexFor(e.key, list.indexOf(b), list.length, rtl);
    if (next < 0) return;
    e.preventDefault();
    // Automatic activation: the panels are cheap to show.
    selectSpeakerTab(list[next].dataset.tab, { focus: true });
    if (typeof onTab === 'function') onTab(activeTab);
  });
}

// tileOf is the sidebar's view of this speaker (speakertile.js), from the
// shared state and the checks the router hands in.
function tileOf(box, ctx) {
  return speakerTileState(box, {
    currentBox: state.currentBox,
    nowPlayState: state.nowPlayState,
    nowSource: state.nowSource,
    boxPlaying: state.boxPlaying || {},
    needsUpdate: ctx.needsUpdate,
    cannotReachInternet: ctx.cannotReachInternet,
    stereoPairs: stereoPairsOf(state.zoneLive || {}),
  });
}

// renderSpeakerPage builds the frame on first use and then only updates the
// head for the speaker in ctx.box and the tab ctx.tab asks for.
export function renderSpeakerPage(root, ctx = {}) {
  if (!root) return;
  onTab = ctx.onTab || null;
  openFn = ctx.open || null;
  if (frameRoot !== root || !root.querySelector('#ui2SpkTabs')) {
    if (panelsEl) {
      // Never take the settings body down with the old frame.
      const body = document.getElementById('settingsBody');
      const view = document.getElementById('view-settings');
      if (body && view && panelsEl.contains(body)) view.appendChild(body);
      panelsEl.remove();
    }
    root.innerHTML = speakerFrameHTML(ctx.tab || activeTab);
    frameRoot = root;
    panelsEl = root.querySelector('#ui2SpkPanels');
    lastState = '';
    lastHalf = '';
    wireFrame(root);
  }
  const head = speakerHead(ctx.box, ctx.label);
  hasHead = !!head;
  const name = root.querySelector('#ui2SpkPageName');
  const meta = root.querySelector('#ui2SpkPageMeta');
  const stateLine = root.querySelector('#ui2SpkPageState');
  const none = root.querySelector('#ui2SpkPageNone');
  const tabs = root.querySelector('#ui2SpkTabs');
  if (name) { name.textContent = head ? head.name : ''; name.hidden = !head; }
  if (meta) { meta.textContent = head ? head.meta : ''; meta.hidden = !head || !head.meta; }
  if (stateLine) {
    const box = ctx.box;
    const tile = head ? tileOf(box, ctx) : null;
    const playing = head && tile.playing && tile.isCurrent ? (state.nowName || '') : '';
    const html = head ? speakerStateHTML(box, tile, playing) : '';
    if (html !== lastState) { stateLine.innerHTML = html; lastState = html; }
    stateLine.hidden = !html;
  }
  const half = root.querySelector('#ui2SpkPageHalf');
  if (half) {
    const other = head ? otherPairHalf(ctx.box, state.zoneLive, state.boxes) : null;
    const otherName = other ? ((typeof ctx.label === 'function' && ctx.label(other)) || other.friendlyName || other.name || other.host) : '';
    const sig = other ? `${other.host}|${otherName}` : '';
    if (sig !== lastHalf) {
      lastHalf = sig;
      half.innerHTML = other
        ? `<button type="button" class="u-btn u-btn--secondary sps-half-btn" data-host="${escapeAttr(other.host)}">`
          + `${escapeHtml(t('ui2.speakers.speaker.otherHalf', { name: otherName }))}</button>`
        : '';
    }
    half.hidden = !other;
  }
  if (none) none.hidden = !!head;
  if (tabs) {
    tabs.hidden = !head;
    if (head) tabs.setAttribute('aria-label', tabsLabel(head));
  }
  selectSpeakerTab(ctx.tab || activeTab);
}
