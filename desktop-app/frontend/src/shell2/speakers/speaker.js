// shell2/speakers/speaker.js: Speakers > one speaker, the page of one speaker
// in the UI 2.0 preview (phase 3, #1287; docs/design/ui-2.0/speakers.png,
// right column).
//
// SKELETON. What is here: the speaker's name and model on top and five named,
// keyboard-operable tabs (Overview, Sound, Network, Keys and alarms,
// Maintenance) with EMPTY panels. The classic Speaker settings view
// (#view-settings) still shows in full below them, so nothing is missing while
// the panels are being filled.
//
// Which speaker: always state.settingsBox, the speaker the classic Settings
// view loads. Opening a speaker (router, open('speaker', { host })) makes it
// the speaker you control as well (lock-step, exactly what the classic
// Settings dropdown does through speakerPicked), so the page, the selected
// sidebar row and the player always name the same speaker. Choosing another
// row in the sidebar while this page is open moves the page to that speaker
// (pickBoxFromShell reloads the settings; the router repaints this head).
//
// TODO(phase 3, speaker) contract for the part that fills this in:
//   receives  renderSpeakerPage(root, ctx), called by the router on every
//             entry to the speaker route and on every sidebar repaint.
//               root  #ui2SpkPage, inside the Speakers area head (markup.js),
//                     shown only on this route. The frame (head, tab row,
//                     five panels) is built ONCE and never rebuilt: a speaker
//                     change only rewrites the head and the tab row's name,
//                     so nodes moved into a panel stay there.
//               ctx   { box, tab, label(box), onTab(tab) }; box may be null
//                     (no speaker yet: the page says to choose one).
//   renders   into the five panels, speakerPanel(tab) returns each one
//             (#ui2SpkPanel-<tab>, role=tabpanel, named by its tab):
//               overview     the cards of the mockup: STR on this speaker
//                            (update ready, Update now, What's new), Bose
//                            firmware, Network summary, Phone remote (QR);
//               sound        volume, balance, bass, display and playback;
//               network      Wi-Fi, address, region;
//               keys         the remote key map, hardware keys, alarms;
//               maintenance  identify, save logs, support mail, backup and
//                            restore, restart, remove STR, factory reset.
//   reuses    the classic view, not a copy of it: views/settings.js renders
//             every section into #settingsBody on each load
//             (renderBoxSettings, one module per group under
//             views/settings/), so either move its sections into the panels
//             after each render (a post-render hook in settings.js, gated by
//             shell2Mounted()) or tag them per tab and let CSS under html.ui2
//             show the active tab's. Every section keeps its ids and handlers.
//             The classic speaker dropdown (#settingsBoxSelect) can hide under
//             html.ui2 once the head names the speaker.
//   keeps     the four rules: the speaker is named in every action and
//             confirmation; done only when the speaker confirmed; an offline
//             speaker explains itself in its Overview card; and EVERY control
//             of today's Speaker settings gets a fixed tab (make the
//             checklist from views/settings/* and tick it off). Restart,
//             remove and reset sit in Maintenance, confirmed, and the
//             confirmation names the speaker.
//
// Pure parts are exported for vitest; the DOM work happens only inside the
// render functions, never at import.

import { t } from '../../i18n/index.js';
import { escapeHtml } from '../../utils.js';
import { SPEAKER_TABS, SPEAKER_TAB_LABEL_KEYS, normalizeTab, tabIndexFor } from './routes.js';
import './speaker.css';

const tabId = (tab) => `ui2SpkTab-${tab}`;
const panelId = (tab) => `ui2SpkPanel-${tab}`;

// speakerHead is what the head of the page says about a speaker: its name,
// and the model and address under it.
export function speakerHead(box, label) {
  if (!box) return null;
  const name = (typeof label === 'function' ? label(box) : '') || box.friendlyName || box.name || box.host;
  return { name, meta: [box.model, box.host].filter(Boolean).join(' · ') };
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
    + '<h3 class="sps-name" id="ui2SpkPageName"></h3>'
    + '<p class="sps-meta" id="ui2SpkPageMeta"></p>'
    + `<p class="sps-none" id="ui2SpkPageNone" hidden>${escapeHtml(t('ui2.speakers.none'))}</p>`
    + '</div>'
    + `<div class="sps-tabs" role="tablist" id="ui2SpkTabs">${tabs}</div>`
    + `<div class="sps-panels">${panels}</div>`;
}

// tabsLabel is the tab row's accessible name: it names the speaker.
export function tabsLabel(head) {
  return head ? t('ui2.speakers.tabsLabel', { name: head.name }) : '';
}

let frameRoot = null;
let activeTab = SPEAKER_TABS[0];
let onTab = null;

export function currentSpeakerTab() {
  return activeTab;
}

// speakerPanel is the panel of one tab, for the part that fills it; null
// before the page was first rendered.
export function speakerPanel(tab) {
  if (!frameRoot) return null;
  return frameRoot.querySelector(`#${panelId(normalizeTab(tab))}`);
}

// selectSpeakerTab shows one tab's panel and gives that tab the Tab stop.
export function selectSpeakerTab(tab, { focus = false } = {}) {
  activeTab = normalizeTab(tab);
  if (!frameRoot) return;
  frameRoot.querySelectorAll('.sps-tab').forEach((b) => {
    const on = b.dataset.tab === activeTab;
    b.setAttribute('aria-selected', String(on));
    b.tabIndex = on ? 0 : -1;
    if (on && focus) b.focus();
  });
  frameRoot.querySelectorAll('.sps-panel').forEach((p) => { p.hidden = p.dataset.tab !== activeTab; });
}

function wireFrame(root) {
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

// renderSpeakerPage builds the frame on first use and then only updates the
// head for the speaker in ctx.box and the tab ctx.tab asks for.
export function renderSpeakerPage(root, ctx = {}) {
  if (!root) return;
  onTab = ctx.onTab || null;
  if (frameRoot !== root || !root.querySelector('#ui2SpkTabs')) {
    root.innerHTML = speakerFrameHTML(ctx.tab || activeTab);
    frameRoot = root;
    wireFrame(root);
  }
  const head = speakerHead(ctx.box, ctx.label);
  const name = root.querySelector('#ui2SpkPageName');
  const meta = root.querySelector('#ui2SpkPageMeta');
  const none = root.querySelector('#ui2SpkPageNone');
  const tabs = root.querySelector('#ui2SpkTabs');
  if (name) { name.textContent = head ? head.name : ''; name.hidden = !head; }
  if (meta) { meta.textContent = head ? head.meta : ''; meta.hidden = !head || !head.meta; }
  if (none) none.hidden = !!head;
  if (tabs) {
    tabs.hidden = !head;
    if (head) tabs.setAttribute('aria-label', tabsLabel(head));
  }
  root.querySelectorAll('.sps-panel').forEach((p) => { if (!head) p.hidden = true; });
  if (head) selectSpeakerTab(ctx.tab || activeTab);
}
