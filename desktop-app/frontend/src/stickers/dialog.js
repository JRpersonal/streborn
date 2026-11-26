// stickers/dialog.js: the "Print stickers" dialog and its two ways in.
//
// One sheet for every STR speaker on the network: per speaker a sticker with
// what is on its keys and a QR code to its phone remote. The dialog shows the
// pages as they will print, lets the user say white or dark per speaker (the
// housing colour cannot be read from a speaker) and saves the PDF. Every
// speaker also gets a sticker for the back of its remote, always: there is
// no choice to make about it (Jens, 2026-11-23).
//
// The window paints the pages (draw.js) and Go writes the file
// (SaveStickerPDF); layout.js says where everything goes.

import { GetPresets, PhoneAddressFor, PhoneQR, ResolveStationLogo, SaveStickerPDF, StickerImage } from '../api.js';
import { t } from '../i18n/index.js';
import { state } from '../state.js';
import { escapeAttr, escapeHtml, friendlyErrorText } from '../utils.js';
import { collectSpeakers } from './collect.js';
import {
  clearStale,
  finishHint,
  hintState,
  keysFingerprint,
  largeTextOn,
  rememberPrinted,
  setHousing,
  setLargeText,
  stickersStale,
} from './data.js';
import { PRINT_DPI, drawPage, loadImages, makeMeasure, squeezeFor } from './draw.js';
import { layoutStickers } from './layout.js';

// The preview is the same drawing at a resolution a screen can use.
const PREVIEW_DPI = 110;

const PRINTER_ICON = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="15" height="15" aria-hidden="true"><polyline points="6 9 6 2 18 2 18 9"></polyline><path d="M6 18H4a2 2 0 0 1-2-2v-5a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2v5a2 2 0 0 1-2 2h-2"></path><rect x="6" y="14" width="12" height="8"></rect></svg>';

// Tolerates a missing document: the keys view calls in here from code that is
// also unit-tested without one.
const $ = (id) => (typeof document === 'undefined' ? null : document.getElementById(id));

// ---------- the button and the hint in the keys view ----------

// stickerButtonHTML is the button next to the keys, styled like Transfer
// beside it in both layouts. The dot and the hidden sentence are filled by
// syncStickerEntry when a printed sticker is out of date.
export function stickerButtonHTML() {
  return `<button class="btn btn-mini preset-transfer-btn sticker-print-btn" id="stickerPrintBtn" type="button" title="${escapeAttr(t('stickers.btnTitle'))}">${PRINTER_ICON} ${escapeHtml(t('stickers.btn'))}<span class="sticker-stale-dot hidden" id="stickerStaleDot" aria-hidden="true"></span><span class="visually-hidden" id="stickerStaleText"></span></button>`;
}

// stickerHintHTML is the one-time pointer to the feature, shown under the keys
// after the first preset the user saves.
export function stickerHintHTML() {
  return `<div class="sticker-hint hidden" id="stickerHint">
      <p>${escapeHtml(t('stickers.hint'))}</p>
      <button class="btn btn-mini" id="stickerHintOpen" type="button">${escapeHtml(t('stickers.btn'))}</button>
      <button class="btn btn-mini" id="stickerHintClose" type="button">${escapeHtml(t('common.close'))}</button>
    </div>`;
}

// syncStickerEntry brings the button's out-of-date marker and the hint in
// line with what is remembered. Called whenever the keys are drawn; wires the
// two on first sight.
export function syncStickerEntry() {
  const btn = $('stickerPrintBtn');
  if (btn) {
    if (!btn.dataset.wired) {
      btn.dataset.wired = '1';
      btn.onclick = () => { openStickerDialog(); };
    }
    const stale = stickersStale();
    const dot = $('stickerStaleDot');
    const text = $('stickerStaleText');
    if (dot) dot.classList.toggle('hidden', !stale);
    if (text) text.textContent = stale ? ` (${t('stickers.stale')})` : '';
    btn.title = stale ? t('stickers.stale') : t('stickers.btnTitle');
  }
  const hint = $('stickerHint');
  if (hint) {
    if (!hint.dataset.wired) {
      hint.dataset.wired = '1';
      $('stickerHintOpen').onclick = () => { openStickerDialog(); };
      $('stickerHintClose').onclick = () => { finishHint(); syncStickerEntry(); };
    }
    hint.classList.toggle('hidden', hintState() !== 'show');
  }
}

// ---------- the dialog ----------

export function stickerModalHTML() {
  return `<div class="modal hidden" id="stickerModal">
    <div class="modal-content sticker-modal" role="dialog" aria-modal="true" aria-labelledby="stickerTitle">
      <h3 id="stickerTitle">${escapeHtml(t('stickers.title'))}</h3>
      <p class="modal-sub">${escapeHtml(t('stickers.intro'))}</p>
      <label class="sticker-large" title="${escapeAttr(t('stickers.largeTitle'))}"><input type="checkbox" id="stickerLarge"> ${escapeHtml(t('stickers.large'))}</label>
      <div class="sticker-speakers" id="stickerSpeakers"></div>
      <div class="sticker-skipped-list" id="stickerSkipped"></div>
      <p class="sticker-status" id="stickerStatus" role="status" aria-live="polite"></p>
      <div class="sticker-preview" id="stickerPreview"></div>
      <div class="share-foot">
        <button class="btn" id="stickerClose" type="button">${escapeHtml(t('common.close'))}</button>
        <button class="btn btn-primary" id="stickerSave" type="button" disabled>${escapeHtml(t('stickers.save'))}</button>
      </div>
    </div>
  </div>`;
}

// What the open dialog works on. run tells a late answer from an earlier
// opening apart from the current one.
let run = 0;
let model = null;
let returnFocus = null;

const say = (text, kind = '') => {
  const el = $('stickerStatus');
  if (!el) return;
  el.textContent = text || '';
  el.className = `sticker-status${kind ? ` is-${kind}` : ''}`;
};

// speakersHTML is one small card per speaker: its name with the model under
// it, and the white/dark choice. The cards sit in a grid that scrolls on its
// own, so a network of many speakers does not push the preview out of the
// dialog.
function speakersHTML(speakers) {
  return speakers.map((sp, i) => `<div class="sticker-speaker" role="group" aria-labelledby="stickerSp-${i}">
      <span class="sticker-speaker-name"><strong id="stickerSp-${i}" title="${escapeAttr(sp.name)}">${escapeHtml(sp.name)}</strong> <span class="muted small" title="${escapeAttr(sp.model)}">${escapeHtml(sp.model)}</span></span>
      <span class="sticker-housing" role="radiogroup" aria-label="${escapeAttr(t('stickers.housing'))}">
        ${['white', 'dark'].map((h) => `<label><input type="radio" name="stickerHousing-${i}" value="${h}" data-speaker="${i}"${sp.housing === h ? ' checked' : ''}> ${escapeHtml(t(h === 'white' ? 'stickers.white' : 'stickers.dark'))}</label>`).join('')}
      </span>
    </div>`).join('');
}

// skippedHTML names the speakers that did not answer, under the grid.
function skippedHTML(skipped) {
  return skipped.map((name) => `<p class="sticker-skipped">${escapeHtml(t('stickers.skipped', { name }))}</p>`).join('');
}

// homeScreenTexts are the steps of the Settings card that puts the phone
// remote on a home screen, for the end of the sheet. Their markup is dropped
// by the layout.
function homeScreenTexts() {
  return {
    title: t('settingsView.phoneHowtoSummary'),
    iosTitle: `${t('settingsView.phoneIosTitle')} (${t('settingsView.phoneIosChip')})`,
    ios: [t('settingsView.phoneIos1'), t('settingsView.phoneIos2'), t('settingsView.phoneIos3'), t('settingsView.phoneIos4')],
    iosNote: t('settingsView.phoneIosNote'),
    androidTitle: `${t('settingsView.phoneAndroidTitle')} (${t('settingsView.phoneAndroidChip')})`,
    android: [t('settingsView.phoneAndroid1'), t('settingsView.phoneAndroid2'), t('settingsView.phoneAndroid3'), t('settingsView.phoneAndroid4')],
    androidNote: t('settingsView.phoneAndroidNote'),
  };
}

// redraw lays the sheet out again and paints the preview. Called after the
// speakers were read, after every colour switch and when Large text changes.
async function redraw(mine) {
  const m = model;
  if (!m) return;
  m.layout = layoutStickers(m.speakers, {
    measure: m.measure,
    squeeze: m.squeeze,
    large: m.large,
    texts: { instruction: t('stickers.sheetInstruction'), control: t('stickers.sheetControl'), remote: t('stickers.remoteHeader'), homeScreen: homeScreenTexts() },
  });
  m.images = await loadImages(m.layout);
  if (mine !== run || model !== m) return;
  const preview = $('stickerPreview');
  if (!preview) return;
  preview.innerHTML = '';
  m.layout.pages.forEach((page, i) => {
    const canvas = document.createElement('canvas');
    canvas.className = 'sticker-page';
    canvas.setAttribute('role', 'img');
    canvas.setAttribute('aria-label', t('stickers.pageN', { n: i + 1, total: m.layout.pages.length }));
    drawPage(canvas, page, { dpi: PREVIEW_DPI, images: m.images });
    preview.appendChild(canvas);
  });
}

// pagePNGs paints every page at print resolution and reads it back.
function pagePNGs(m, images) {
  const canvas = document.createElement('canvas');
  return m.layout.pages.map((page) => drawPage(canvas, page, { dpi: PRINT_DPI, images }).toDataURL('image/png'));
}

async function save(mine) {
  const m = model;
  const btn = $('stickerSave');
  if (!m || !m.layout || !m.layout.pages.length || !btn) return;
  btn.disabled = true;
  say(t('stickers.saving'));
  // Let the line appear before the pages are painted, which blocks for a moment.
  await new Promise((r) => setTimeout(r, 30));
  try {
    let pages;
    try {
      pages = pagePNGs(m, m.images);
    } catch {
      // A webview that treats a logo as foreign content refuses to hand the
      // page back. The codes are plain PNGs it never objects to, so the sheet
      // is painted again with letter tiles instead of failing.
      pages = pagePNGs(m, new Map([...m.images].filter(([src]) => /^data:image\/png/i.test(src))));
    }
    const path = await SaveStickerPDF(pages);
    if (mine !== run) return;
    if (!path) { say(''); return; } // cancelled in the save dialog
    rememberPrinted(Object.fromEntries(m.speakers.map((sp) => [sp.id, keysFingerprint(sp.keys)])));
    clearStale(m.speakers.map((sp) => sp.id));
    syncStickerEntry();
    say(t('stickers.saved', { path }), 'ok');
  } catch (e) {
    if (mine === run) say(t('stickers.failed', { err: friendlyErrorText(e) }), 'error');
  } finally {
    if (mine === run) btn.disabled = false;
  }
}

// openStickerDialog reads every speaker and shows the sheet. Opening it is
// "first use": the one-time hint is not shown again.
export async function openStickerDialog() {
  const modal = $('stickerModal');
  if (!modal) return;
  finishHint();
  syncStickerEntry();
  const mine = ++run;
  model = null;
  returnFocus = document.activeElement;
  $('stickerSpeakers').innerHTML = '';
  $('stickerSkipped').innerHTML = '';
  $('stickerPreview').innerHTML = '';
  const saveBtn = $('stickerSave');
  saveBtn.disabled = true;
  const largeBox = $('stickerLarge');
  largeBox.checked = largeTextOn();
  largeBox.onchange = () => {
    setLargeText(largeBox.checked);
    if (!model) return;
    model.large = largeBox.checked;
    redraw(run);
  };
  saveBtn.onclick = () => { save(run); };
  $('stickerClose').onclick = closeStickerDialog;
  modal.onclick = (e) => { if (e.target === modal) closeStickerDialog(); };
  modal.onkeydown = (e) => {
    if (e.key === 'Escape') { e.preventDefault(); closeStickerDialog(); return; }
    if (e.key !== 'Tab') return;
    const focusables = [...modal.querySelectorAll('button, input, [href]')]
      .filter((el) => !el.disabled && el.offsetParent !== null);
    if (!focusables.length) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
  };
  modal.classList.remove('hidden');
  $('stickerClose').focus();
  say(t('stickers.loading'));

  const { speakers, skipped } = await collectSpeakers(state.boxes, { GetPresets, PhoneAddressFor, PhoneQR, ResolveStationLogo, StickerImage });
  if (mine !== run || modal.classList.contains('hidden')) return;
  model = { speakers, skipped, measure: makeMeasure(), squeeze: squeezeFor(), large: largeBox.checked, layout: null, images: new Map() };
  const list = $('stickerSpeakers');
  list.innerHTML = speakersHTML(speakers);
  $('stickerSkipped').innerHTML = skippedHTML(skipped);
  list.onchange = (e) => {
    const input = e.target;
    if (!input || input.type !== 'radio' || !input.checked) return;
    const sp = speakers[Number(input.dataset.speaker)];
    if (!sp) return;
    sp.housing = input.value === 'white' ? 'white' : 'dark';
    setHousing(sp.id, sp.housing);
    redraw(run);
  };
  if (!speakers.length) { say(t('stickers.none'), 'error'); return; }
  await redraw(mine);
  if (mine !== run) return;
  say('');
  saveBtn.disabled = false;
}

export function closeStickerDialog() {
  const modal = $('stickerModal');
  if (!modal || modal.classList.contains('hidden')) return;
  modal.classList.add('hidden');
  run++;
  model = null;
  // The preview canvases are large; drop them with the dialog.
  $('stickerPreview').innerHTML = '';
  if (returnFocus && typeof returnFocus.focus === 'function') {
    try { returnFocus.focus(); } catch { /* element gone */ }
  }
  returnFocus = null;
}
