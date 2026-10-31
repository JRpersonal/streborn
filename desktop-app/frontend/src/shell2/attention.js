// shell2/attention.js: what one speaker needs from you, said on that speaker
// (UI 2.0 phase 5; docs/UI-2.0.md, rule 3: errors where they happen).
//
// The classic layout has two window-wide banners that name speakers:
// #boxWedgeBanner (a speaker that accepts playback and never plays; unplug it
// once) and #boxIssueBanner (no internet, leftovers of another tool, a
// foreign cloud address, no saved Wi-Fi, a refusal storm, a refused key
// press, a long press STR could not keep, a stale folder key, a name the
// firmware cannot survive). In the preview those banners are hidden
// (banners.css) and the same words and the same buttons stand where the
// speaker is: as a note on its card in Speakers > list (list.js), as a card at
// the top of its Overview (speaker.js), and as a chip on its sidebar row (the
// 'states' package reads speakerAttention through the mount deps).
//
// main.js keeps deciding which speaker has which issue, in the very functions
// that render the classic banners, and hands the lists here (noteSpeakerIssues
// there, speakerIssuesFrom here); nothing is classified twice. The buttons
// run main.js's own handlers through the runner it registers
// (setSpeakerAttentionRunner), the ones the classic banner buttons use.
//
// Pure: the model and the markup are plain functions of their input, tested
// in attention.test.js; the state read and the runner are the only shared
// parts. No DOM at import.

import { t } from '../i18n/index.js';
import { escapeAttr, escapeHtml, holdRefusedBannerKey } from '../utils.js';
import { state } from '../state.js';
import { groupKeyErrorBanners } from '../groupkeys.js';

// The order the items show in, the classic banner's own order (the wedge
// banner sits above the issue banner).
export const ATTENTION_KINDS = Object.freeze([
  'wedged', 'noNet', 'conflict', 'foreignCloud', 'noWifi', 'storm',
  'keyErr', 'holdErr', 'staleKey', 'badName',
]);

// The kinds that stop the speaker from playing at all: the chip says so in
// the danger colour. The others are worth knowing (warn).
const BLOCKING = new Set(['wedged', 'noNet', 'foreignCloud', 'storm', 'badName']);

const hostOf = (b) => (b && b.host) || '';

// speakerIssuesFrom turns the lists main.js builds for its banners into one
// list of items per speaker host. parts holds any of the ATTENTION_KINDS as
// arrays of boxes; label names a box (getBoxLabel). Each item keeps the data
// its words need, so the text follows a language switch.
export function speakerIssuesFrom(parts = {}, label = (b) => (b && (b.friendlyName || b.name || b.host)) || '') {
  const out = {};
  const add = (b, item) => {
    const host = hostOf(b);
    if (!host) return;
    (out[host] = out[host] || []).push({ ...item, host, name: label(b) || host });
  };
  for (const b of parts.wedged || []) add(b, { kind: 'wedged' });
  for (const b of parts.noNet || []) add(b, { kind: 'noNet' });
  for (const b of parts.conflict || []) add(b, { kind: 'conflict', mod: b.conflictingMod || '' });
  for (const b of parts.foreignCloud || []) add(b, { kind: 'foreignCloud', url: b.foreignCloudURL || '' });
  for (const b of parts.noWifi || []) add(b, { kind: 'noWifi' });
  for (const b of parts.storm || []) add(b, { kind: 'storm' });
  for (const b of parts.keyErr || []) {
    const line = groupKeyErrorBanners([b], label)[0];
    if (line) add(b, { kind: 'keyErr', key: line.key, reason: line.reason || '' });
  }
  for (const b of parts.holdErr || []) {
    add(b, { kind: 'holdErr', key: holdRefusedBannerKey(b.holdRefusedSource), slot: b.holdRefusedSlot || '?' });
  }
  for (const b of parts.staleKey || []) add(b, { kind: 'staleKey', slot: b.staleFolderKeySlot || '' });
  for (const b of parts.badName || []) add(b, { kind: 'badName' });
  for (const host of Object.keys(out)) {
    out[host].sort((a, b) => ATTENTION_KINDS.indexOf(a.kind) - ATTENTION_KINDS.indexOf(b.kind));
  }
  return out;
}

// attentionItems: one speaker's items, from a speakerIssuesFrom map.
export function attentionItems(host, issues = state.speakerIssues) {
  if (!host || !issues || !Array.isArray(issues[host])) return [];
  return issues[host];
}

// itemText: the item's words, the classic banner's sentence for this one
// speaker. { title, text, hint, detail }: title bold, hint after it, detail
// (the speaker's raw reason) on a line of its own.
export function itemText(item) {
  const name = item.name;
  switch (item.kind) {
    case 'wedged': return { title: t('speaker.wedgedTitle'), text: t('speaker.wedgedBanner', { name }) };
    case 'noNet': return { title: t('speaker.noInternetBanner', { name }), text: t('speaker.noInternetHint') };
    case 'conflict': return { text: t('speaker.conflictModBanner', { name, mod: item.mod }) };
    case 'foreignCloud': return { text: t('speaker.foreignCloudBanner', { name, url: item.url }) };
    case 'noWifi': return { text: t('speaker.noWifiBanner', { name }) };
    case 'storm': return { text: t('speaker.stormBanner', { name }) };
    case 'keyErr': return {
      text: t(item.key, { name, reason: item.reason }),
      detail: item.key === 'speaker.groupKeyErrorBanner' ? '' : item.reason,
    };
    case 'holdErr': return { text: t(item.key, { name, slot: item.slot }) };
    case 'staleKey': return { text: t('speaker.staleFolderKeyBanner', { name, slot: item.slot }) };
    case 'badName': return { title: t('speaker.badNameBanner', { name }), text: t('speaker.badNameHint') };
    default: return { text: '' };
  }
}

// itemActions: the buttons the classic banner offers for this kind, as
// [{ act, label, primary }]. dismiss covers the two kinds the classic banner
// lets you dismiss per speaker (warnDismiss:<id>:conflict and :nowifi).
export function itemActions(item) {
  switch (item.kind) {
    case 'conflict': return [
      { act: 'removeConflict', label: t('speaker.conflictRemoveBtn') },
      { act: 'dismiss', label: t('speaker.issueDismiss'), dismiss: 'conflict' },
    ];
    case 'foreignCloud': return [{ act: 'cloudRestore', label: t('ui2.attention.cloudRestore'), primary: true }];
    case 'noWifi': return [
      { act: 'saveWifi', label: t('speaker.wifiSaveBtn') },
      { act: 'dismiss', label: t('speaker.issueDismiss'), dismiss: 'nowifi' },
    ];
    case 'storm': return [{ act: 'stormRestart', label: t('speaker.stormRestartBtn'), primary: true }];
    case 'badName': return [{ act: 'badNameFix', label: t('speaker.badNameFixBtn'), primary: true }];
    default: return [];
  }
}

// attentionChip: the sidebar chip for a speaker with items, or null.
export function attentionChip(items) {
  if (!items || !items.length) return null;
  const blocking = items.some((i) => BLOCKING.has(i.kind));
  return { kind: blocking ? 'danger' : 'warn', label: t('ui2.attention.chip') };
}

// speakerAttention is what the mount deps hand the sidebar: the items of one
// speaker and its chip.
export function speakerAttention(host, issues = state.speakerIssues) {
  const items = attentionItems(host, issues);
  return { items, chip: attentionChip(items) };
}

function itemHTML(item, cls) {
  const tx = itemText(item);
  const acts = itemActions(item).map((a) => `<button type="button" class="u-btn ${a.primary ? 'u-btn--primary' : 'u-btn--secondary'} attn-act"`
    + ` data-attn="${escapeAttr(a.act)}" data-host="${escapeAttr(item.host)}"${a.dismiss ? ` data-dismiss="${escapeAttr(a.dismiss)}"` : ''}>`
    + `${escapeHtml(a.label)}</button>`).join('');
  return `<li class="${cls}-item" data-kind="${escapeAttr(item.kind)}">`
    + `<p class="${cls}-text">${tx.title ? `<strong>${escapeHtml(tx.title)}</strong> ` : ''}${escapeHtml(tx.text)}</p>`
    + (tx.detail ? `<p class="${cls}-detail">${escapeHtml(tx.detail)}</p>` : '')
    + (acts ? `<div class="${cls}-acts">${acts}</div>` : '')
    + '</li>';
}

// attentionNoteHTML: the note on the speaker's card in Speakers > list.
export function attentionNoteHTML(items) {
  if (!items || !items.length) return '';
  const chip = attentionChip(items);
  return `<div class="spl-note spl-attn spl-attn--${chip.kind}">`
    + `<ul class="spl-attn-list">${items.map((i) => itemHTML(i, 'spl-attn')).join('')}</ul></div>`;
}

// attentionCardHTML: the card at the top of the speaker's Overview, headed by
// the speaker's name (rule 1).
export function attentionCardHTML(items, name) {
  if (!items || !items.length) return '';
  const chip = attentionChip(items);
  return `<section class="sps-attn sps-attn--${chip.kind}" aria-labelledby="ui2SpkAttnHead">`
    + `<h3 class="sps-attn-head" id="ui2SpkAttnHead">${escapeHtml(t('ui2.attention.heading', { name }))}</h3>`
    + `<ul class="sps-attn-list">${items.map((i) => itemHTML(i, 'sps-attn')).join('')}</ul></section>`;
}

// attentionSig: a short signature of every item, so the list and the page
// repaint when the issues change and only then.
export function attentionSig(issues = state.speakerIssues) {
  return JSON.stringify(issues || {});
}

// The runner main.js registers: (host, act, button, dismissKind) => void.
let runner = null;
export function setSpeakerAttentionRunner(fn) {
  runner = typeof fn === 'function' ? fn : null;
}

// runAttentionAction handles a click on one of the buttons above; true when
// the click was one of them.
export function runAttentionAction(target) {
  const btn = target && typeof target.closest === 'function' ? target.closest('.attn-act') : null;
  if (!btn) return false;
  if (btn.disabled || btn.getAttribute('aria-disabled') === 'true') return true;
  if (runner) runner(btn.dataset.host || '', btn.dataset.attn || '', btn, btn.dataset.dismiss || '');
  return true;
}
