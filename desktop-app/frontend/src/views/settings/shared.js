// views/settings/shared.js: what the settings group modules have in common.
//
// The injected main.js helpers (deps), the pending-button wrapper, the help
// text folding, the Playground discussion links, the deep-link reveal, the
// name combobox and the small formatters. Everything here is used by two or
// more of the group modules, or by settings.js and a module, so it lives in a
// module none of them has to import each other for.

import { state } from '../../state.js';
import { $, escapeHtml, escapeAttr, showError, showToast, confirmWarn } from '../../utils.js';
import { t } from '../../i18n/index.js';
import { revealSpeakerTabOf, speakerConfirmTitle } from '../../shell2/speakers/speaker.js';
import { wireToggleSection } from '../../togglesection.js';
import {
  openSection, showSectionError, showSectionWarn, clearSectionMsg,
  showFieldError, clearFieldError, paintReadState,
} from '../../shell2/speakers/sectionmsg.js';

// Injected main.js helpers (see initSettingsView). These stay in main.js because
// they are shared across views; the settings code calls them as deps.<name>.
export const deps = {
  switchView: () => {},
  updateFilterIndicators: () => {},
  discoverBoxes: async () => {},
  renderBoxSelect: () => {},
  localizeLanguageName: (n) => n,
  doBoxUpdate: async () => {},
  loadPresets: async () => {},
  getRoomNames: () => [],
};
export function initSettingsDeps(d) {
  Object.assign(deps, d);
}

// What wireToggleSection needs from this view: the element lookup, the i18n
// lookup and the two ways a write's outcome is reported.
export const toggleDeps = { $, t, showToast, showError };

// ---- Where a setting's outcome is reported (UI 2.0 phase 5) ----
//
// The classic layout reports every outcome window-wide: the error dialog for
// a refusal, a toast for a success. In the preview a refusal on the speaker
// page belongs into the card it happened in (docs/UI-2.0.md, rule 3), so the
// settings modules report through these instead of calling showError and
// showToast directly. anchor is the control that was used (an element or its
// id); it decides the card and where in it the line goes.
//
// Classic, or an anchor outside an open card of the speaker page (another
// tab, a dialog, the classic Settings view): exactly the old call, with the
// old arguments, so the classic layout cannot tell the difference.

// The card of the speaker page that holds anchor, while it is on screen.
function sectionOf(anchor) {
  if (!state.ui2Shell) return null;
  return openSection(anchor);
}

// reportError: the refusal line in the card, naming the speaker, or the
// error dialog.
export function reportError(anchor, e) {
  const hit = sectionOf(anchor);
  if (!hit) { showError(e); return; }
  showSectionError(hit.section, hit.anchor, e, { name: settingsBoxLabel(state.settingsBox) });
}

// reportDone: the success toast as always; in the preview it also takes the
// card's refusal line away, since the setting did go through now.
export function reportDone(anchor, msg, ms) {
  const hit = sectionOf(anchor);
  if (hit) clearSectionMsg(hit.section);
  if (ms === undefined) showToast(msg);
  else showToast(msg, ms);
}

// reportWarn: an outcome that only partly worked (a copy that reached some
// keys only). A warning line in the card, or the error dialog as before.
// classicToast keeps a call site that showed a toast in the classic layout on
// that toast (the single-target copy's partial success).
export function reportWarn(anchor, msg, { classicToast = false } = {}) {
  const hit = sectionOf(anchor);
  if (!hit) {
    if (classicToast) showToast(msg);
    else showError(msg);
    return;
  }
  showSectionWarn(hit.section, hit.anchor, msg);
}

// reportsInline: the next report about anchor lands in its card. A call site
// that put up a sticky progress toast takes it down when it does.
export function reportsInline(anchor) {
  return !!sectionOf(anchor);
}

// reportFieldError: a value the user typed is not usable. The field itself is
// marked (aria-invalid, a line it names through aria-describedby), or the
// error dialog as before.
export function reportFieldError(field, msg) {
  const el = typeof field === 'string' ? $(field) : field;
  const hit = sectionOf(el);
  if (!hit) { showError(msg); return; }
  showFieldError(el, msg);
}

// clearFieldErrors drops the field marks of a form that is used again.
export function clearFieldErrors(...fields) {
  for (const f of fields) clearFieldError(typeof f === 'string' ? $(f) : f);
}

// toggleDepsFor is toggleDeps with the two reports bound to one section (and
// its On button, which places the line after the toggle's row), so
// wireToggleSection reports where its section is without knowing about the
// preview.
export function toggleDepsFor(sectionId, anchorId) {
  const anchor = () => (anchorId && $(anchorId)) || $(sectionId);
  return {
    $,
    t,
    showToast: (msg, ms) => reportDone(anchor(), msg, ms),
    showError: (e) => reportError(anchor(), e),
  };
}

// endpointMissing: the agent answered that it has no such setting (an agent
// older than it answers 404; the Go binding turns that into "status 404").
// Such a section stays hidden as it always was: it is not unreadable, it is
// not there.
export function endpointMissing(e) {
  return /(^|\D)status 404(\D|$)/.test(String((e && e.message) || e || ''));
}

const NOT_THERE = Object.freeze({ notThere: true });

// What the last successful read of a toggle said about its section, per
// speaker: 'host|sectionId' -> shown. Many of these settings exist only on
// some models (a display, AirPlay, SSH), and only a read tells: a section
// that never read as shown on this speaker is not one to keep on screen as
// unreadable.
const shownBefore = new Map();

// wireSettingToggle wires one On/Off section of Speaker settings.
//
// Classic: wireToggleSection exactly as before, a read failure hides the
// section (or paints the call site's onGetError). Preview: a setting that
// could not be read stays visible (rule 4, nothing disappears): its buttons
// are aria-disabled, a press is refused, and a status line says the speaker
// did not answer, with Try again. That is for a setting known to be there:
// one the call site shows on a failed read anyway (its onGetError says show),
// or one an earlier read on this speaker showed. Any other unreadable section
// stays hidden as in the classic layout, so a model is never offered a
// setting it does not have. A setting the agent does not know (404) stays
// hidden in both layouts.
export function wireSettingToggle(opts) {
  const deps = toggleDepsFor(opts.sectionId, opts.onId);
  if (!state.ui2Shell) return wireToggleSection(opts, deps);
  let failed = false;
  let handle = null;
  const host = $(opts.sectionId);
  const buttons = [$(opts.onId), $(opts.offId)];
  const key = () => `${(state.settingsBox && state.settingsBox.host) || ''}|${opts.sectionId}`;
  // togglesection.js takes onGetError once, as a value: this object is it,
  // and every failed read sets what it says before the failure reaches there.
  const onGetError = { show: false, enabled: null, readFailed: false };
  const wrapped = {
    ...opts,
    get: async () => {
      try {
        const r = await opts.get();
        failed = false;
        return r;
      } catch (e) {
        if (endpointMissing(e)) { failed = false; return NOT_THERE; }
        failed = true;
        const known = shownBefore.get(key()) === true || !!(opts.onGetError && opts.onGetError.show);
        if (known) Object.assign(onGetError, { show: true, enabled: null, readFailed: true });
        else Object.assign(onGetError, { show: false, enabled: null }, opts.onGetError || {}, { readFailed: false });
        throw e;
      }
    },
    stateFn: (r) => {
      if (r === NOT_THERE) return opts.onGetError || { show: false, enabled: null };
      const st = opts.stateFn(r);
      shownBefore.set(key(), !!(st && st.show));
      return st;
    },
    onGetError,
    onLoaded: (st) => {
      paintReadState(host, !!(st && st.readFailed), {
        name: settingsBoxLabel(state.settingsBox),
        buttons,
        retry: () => { if (handle) handle.load(); },
      });
      if (opts.onLoaded) opts.onLoaded(st);
    },
    confirm: async (enabled) => {
      if (failed) return false;
      return opts.confirm ? opts.confirm(enabled) : true;
    },
  };
  handle = wireToggleSection(wrapped, deps);
  return handle;
}

// withButtonPending runs an async box write behind its button. The button
// flips to a working state the instant it is pressed and comes back when the
// write has succeeded or failed. Without this, a save toward a speaker that
// stopped answering looked like a dead click for the full transport timeouts
// (10+ seconds, field 2026-08-30: a rename showed no state at all until the
// raw timeout error appeared), and even a healthy save gave no sign between
// the press and the toast.
export async function withButtonPending(btn, fn) {
  if (!btn || btn.disabled) return;
  const prev = btn.textContent;
  btn.disabled = true;
  btn.classList.add('working');
  btn.textContent = t('common.saving');
  try {
    await fn();
  } finally {
    btn.disabled = false;
    btn.classList.remove('working');
    btn.textContent = prev;
  }
}

// settingsBoxLabel is the name the panels below address the speaker by.
export function settingsBoxLabel(box) {
  return (box && (box.friendlyName || box.name || box.host)) || '';
}

// confirmFor is confirmWarn for an action on one speaker. In the UI 2.0
// preview the dialog's title names the speaker ("Living room: Restart"), so
// no confirmation has to be read against the page behind it to know which
// speaker it is about (docs/UI-2.0.md, rule 1). The classic layout keeps its
// titles exactly as they were.
export function confirmFor(box, title, body, opts) {
  return confirmWarn(speakerConfirmTitle(title, settingsBoxLabel(box)), body, opts);
}

// openInstallFor sends the user to Setup with this speaker pinned as the
// network-install target (the same path the Listen to music card takes for a
// stock speaker), so the install starts on THIS box on a multi-speaker LAN.
export function openInstallFor(box) {
  state.strRemovedHost = null;
  state.setupTarget = { kind: 'stock', box };
  deps.switchView('setup');
}

// renderSTRRemovedPanel replaces the settings body after STR was removed from
// the selected speaker on purpose. The speaker rebooted into its Bose firmware
// by design, so the "reading speaker data" retry loop and its "agent died,
// unplug the speaker" ending would be nonsense here: nothing is wrong, and
// there is no agent to read from. Say what happened and where the box went.
export function renderSTRRemovedPanel(body, box) {
  body.innerHTML = `
    <div class="empty-state">
      <div class="empty-state-title">${escapeHtml(t('settingsView.strRemovedTitle'))}</div>
      <div class="empty-state-text">
        ${escapeHtml(t('settingsView.strRemovedHelp', { name: settingsBoxLabel(box) }))}
      </div>
      <div class="empty-state-buttons">
        <button class="btn btn-mini" id="settingsInstallAgain">${escapeHtml(t('settingsView.repairSTRBtn'))}</button>
        <button class="btn btn-primary btn-mini" id="settingsBackToBoxes">${escapeHtml(t('settingsView.backToSpeakerList'))}</button>
      </div>
    </div>`;
  const again = document.getElementById('settingsInstallAgain');
  if (again) again.onclick = () => openInstallFor(box);
  const back = document.getElementById('settingsBackToBoxes');
  wireBackToList(back, box, () => { state.strRemovedHost = null; });
}

// wireBackToList wires the "back to the speaker list" button of the panels a
// settings load can end on (STR removed, STR not running, an update still
// finishing, a speaker that answers nothing). Classic: the Listen to music
// view, as always. Preview: the button reads "All speakers" and opens the
// Speakers list with the keyboard on this speaker's card, so the way back
// leads to the speaker the panel was about (no dead end, rule 4). before runs
// first in both layouts (the call site's own reset).
export function wireBackToList(el, box, before = () => {}) {
  if (!el) return;
  if (!state.ui2Shell) {
    el.onclick = () => { before(); deps.switchView('box'); };
    return;
  }
  el.textContent = t('ui2.speakers.speaker.allSpeakers');
  el.onclick = () => { before(); openSpeakerListAt(box && box.host); };
}

// openSpeakerListAt opens Speakers > list and focuses the card of host (the
// first card when the speaker has none of its own, a half of a pair). The
// router is imported on use: views/setup.js imports views/settings.js, and the
// router imports the setup wizard, so a static import here would be a cycle.
export async function openSpeakerListAt(host) {
  try {
    const { openSpeakers } = await import('../../shell2/speakers/index.js');
    if (!openSpeakers('list')) { deps.switchView('box'); return; }
  } catch (_) {
    deps.switchView('box');
    return;
  }
  const focusCard = () => {
    const root = document.getElementById('view-speakers');
    if (!root) return false;
    const cards = [...root.querySelectorAll('.spl-open')];
    const card = cards.find((b) => b.dataset.host === host) || cards[0];
    if (!card) return false;
    card.focus();
    return true;
  };
  if (!focusCard() && typeof requestAnimationFrame === 'function') requestAnimationFrame(focusCard);
}

// Long help text: the opening sentence stays, the rest folds away.
//
// Several panels open with a paragraph the user has to read past before
// reaching anything they can actually change. Counted against the shipped
// bundles, the intro of "Play a custom URL from a preset" is 535 characters in
// English and 630 in German, the webhook one 496 and 592, and voice control 401
// and 486. The app is capped at 720 px (#app in style.css), which leaves a
// settings panel roughly 650 px, so that is five to six lines of prose before
// the first select box. Folding everything after the opening sentence into a
// closed disclosure deletes nothing (the reasoning is one click away) and lets
// the panel start with its controls instead of with an essay.
//
// The measurement runs per translation rather than once in English, because
// German runs about twenty percent longer for the same sentence: the same
// paragraph honestly deserves folding in one language and not in another.

// Roughly how many Latin characters fit on one line of 12px help text in the
// 650 px a settings panel leaves. Three of those lines is where a hint stops
// reading as a hint, so anything wider gets folded.
const HELP_LINE_WIDTH = 105;
const HELP_FOLD_WIDTH = HELP_LINE_WIDTH * 3;

// CJK scripts draw their characters full width, about twice a Latin letter, so
// counting characters alone would make the Japanese bundle look half as long as
// it renders and leave its longest paragraphs unfolded. Count those double.
const WIDE_CHAR = /[\u1100-\u115F\u2E80-\uA4CF\uAC00-\uD7A3\uF900-\uFAFF\uFE30-\uFE4F\uFF00-\uFF60\uFFE0-\uFFE6]/;
function renderedWidth(text) {
  let width = 0;
  for (const ch of String(text || '')) width += WIDE_CHAR.test(ch) ? 2 : 1;
  return width;
}

// A Latin full stop only ends a sentence when a space follows it, otherwise
// "192.0.2.1" and "st-reborn.de" would each read as several sentences. The CJK
// stops carry no following space and end a sentence on their own.
const SENTENCE_END = '.!?。！？';
const SENTENCE_END_WIDE = '。！？';
const BRACKET_OPEN = '([（【';
const BRACKET_CLOSE = ')]）】';

// Words that end in a full stop without ending the sentence. The shipped
// bundles only need the dotted forms ("e.g.", German "z.B."), which always have
// a single letter in front of the final dot, plus this handful of spelled-out
// abbreviations that the translations use before an example.
const ABBREVIATIONS = new Set(['etc', 'vs', 'ca', 'approx', 'bzw', 'usw', 'ggf', 'evtl', 'itd', 'itp', 'np', 'pvz', 'piem']);

// endsAbbreviation reports whether the full stop at dotAt closes an
// abbreviation rather than a sentence. The single-letter test is deliberately
// limited to Latin script: Arabic and Cyrillic are full of two-letter words,
// and a script-blind version of this rule skipped the real end of the Arabic
// voice-control sentence and folded a sentence too late.
function endsAbbreviation(text, dotAt) {
  let start = dotAt - 1;
  while (start >= 0 && !/[\s.]/.test(text[start])) start--;
  const word = text.slice(start + 1, dotAt);
  if (!word) return false;
  if (/^[A-Za-z]$/.test(word)) return true;
  return ABBREVIATIONS.has(word.toLowerCase());
}

// splitHelpText returns the opening sentence and the remainder, or null when
// there is no honest place to cut. Terminators inside brackets are ignored: an
// aside like "(e.g. a smart-home toggle, or waking a PC)" is not two sentences.
// Both halves have to carry real content, so a stray early stop cannot leave a
// three-word teaser above a fold.
function splitHelpText(text) {
  const s = String(text || '').trim();
  let depth = 0;
  for (let i = 0; i < s.length; i++) {
    const ch = s[i];
    if (BRACKET_OPEN.includes(ch)) { depth++; continue; }
    if (BRACKET_CLOSE.includes(ch)) { if (depth > 0) depth--; continue; }
    if (depth > 0 || !SENTENCE_END.includes(ch)) continue;
    if (!SENTENCE_END_WIDE.includes(ch)) {
      const next = s[i + 1];
      if (next !== ' ' && next !== '\u00a0') continue;
      if (endsAbbreviation(s, i)) continue;
    }
    const head = s.slice(0, i + 1).trim();
    const rest = s.slice(i + 1).trim();
    if (renderedWidth(head) < 40 || renderedWidth(rest) < 40) continue;
    return { head, rest };
  }
  return null;
}

// helpBlock renders a panel's explanatory paragraph, folding everything past
// the opening sentence when the paragraph is long enough to matter. Short hints
// come back untouched, so the call sites do not have to guess which is which.
// The toggle reuses the existing "Details" string rather than introducing a
// thirteenth translation of "show more", and copies the markup of the nested
// disclosure the announcement panel already ships so both look the same.
export function helpBlock(text, tag = 'small', cls = 'muted small expert-intro') {
  const raw = String(text || '');
  const parts = renderedWidth(raw) > HELP_FOLD_WIDTH ? splitHelpText(raw) : null;
  if (!parts) return `<${tag} class="${cls}">${escapeHtml(raw)}</${tag}>`;
  return `<${tag} class="${cls}" style="margin-bottom:4px">${escapeHtml(parts.head)}</${tag}>
      <details class="help-more" style="margin:0 0 8px">
        <summary class="muted small" style="cursor:pointer">${escapeHtml(t('setupAPPush.detailsToggle'))}</summary>
        <small class="muted small" style="display:block;margin-top:4px">${escapeHtml(parts.rest)}</small>
      </details>`;
}

// Each Playground (Spielwiese) section links to its own "Show and tell" thread
// so owners can swap setups and ideas for that one feature, building a small
// community around the experimental features (2026-09-02). data-url is opened by
// the .expert-disc-link handler in wireAdvancedSections, the same BrowserOpenURL
// pattern the firmware-banner links use.
const EXPERT_DISCUSS = {
  announce: 'https://github.com/JRpersonal/streborn/discussions/826',
  webhook: 'https://github.com/JRpersonal/streborn/discussions/827',
  voice: 'https://github.com/JRpersonal/streborn/discussions/828',
  copyPresets: 'https://github.com/JRpersonal/streborn/discussions/829',
  urlPreset: 'https://github.com/JRpersonal/streborn/discussions/830',
};

export function discussLink(key) {
  const url = EXPERT_DISCUSS[key];
  if (!url) return '';
  return `<div class="expert-disc-row" style="margin-top:10px">`
    + `<a href="#" class="expert-disc-link" data-url="${escapeAttr(url)}" style="font-size:0.9em">`
    + `&#128172; ${escapeHtml(t('settingsView.expertDiscussLink'))}</a></div>`;
}

// revealInSettings brings a settings element into view for real.
//
// Opening the element's own <details> is not enough: groupSettingsSections puts
// every section into a collapsible GROUP, and the two groups that matter here,
// Advanced and Info, start closed. A deep link that only opened the section
// itself therefore left the user on the settings page with everything still
// folded up and nothing to see, which is exactly how the Multi-Room tab's
// "show on the remote key map" button was reported (2026-09-10: "schickt den
// user nur auf die einstellungsseite aber nicht direkt in das untermenue").
//
// So open every <details> ANCESTOR as well, from the element outwards, and only
// then scroll. Walking the ancestors rather than naming the group keeps this
// correct if the grouping is ever changed again.
export function revealInSettings(el) {
  if (!el) return;
  // The UI 2.0 preview shows one tab of the speaker page at a time: open the
  // one that holds el first (a no-op in the classic layout).
  revealSpeakerTabOf(el);
  let node = el;
  while (node && node !== document.body) {
    if (node.tagName === 'DETAILS') node.open = true;
    node = node.parentElement;
  }
  try { el.scrollIntoView({ behavior: 'smooth', block: 'start' }); } catch { /* older webview */ }
}

// wireCombobox binds to an <input> + <button toggle> + <ul list>
// trio. Filters while typing, opens on toggle click, selects on
// item click.
export function wireCombobox(inputId, toggleId, listId, options) {
  const input = document.getElementById(inputId);
  const toggle = document.getElementById(toggleId);
  const list = document.getElementById(listId);
  if (!input || !toggle || !list) return;

  function render(filter) {
    const q = (filter || '').toLowerCase().trim();
    const matches = options.filter(o => !q || o.toLowerCase().includes(q));
    if (matches.length === 0) {
      list.innerHTML = `<li class="combo-empty">${escapeHtml(t('combobox.noSuggestions'))}</li>`;
      return;
    }
    list.innerHTML = matches.map(o => `<li data-value="${escapeAttr(o)}">${escapeHtml(o)}</li>`).join('');
    list.querySelectorAll('li[data-value]').forEach(li => {
      li.onmousedown = (e) => {
        // Use mousedown rather than click so the handler fires
        // before the input loses focus.
        e.preventDefault();
        input.value = li.dataset.value;
        list.classList.add('hidden');
      };
    });
  }

  // When opening the dropdown show ALL options rather than
  // filtering by the current input. Otherwise the suggestions are
  // gone whenever the current name does not match a room (e.g.
  // "Bose SoundTouch 02FFD8"). Filtering only kicks in on typing.
  let userIsTyping = false;
  const showAll = () => { render(''); list.classList.remove('hidden'); };
  const showFiltered = () => { render(input.value); list.classList.remove('hidden'); };
  const hide = () => list.classList.add('hidden');

  input.addEventListener('focus', () => {
    if (userIsTyping) showFiltered(); else showAll();
  });
  input.addEventListener('input', () => {
    userIsTyping = true;
    showFiltered();
  });
  input.addEventListener('blur', () => {
    setTimeout(hide, 150);
    userIsTyping = false;
  });
  toggle.addEventListener('mousedown', (e) => {
    e.preventDefault();
    if (list.classList.contains('hidden')) {
      input.focus();
      showAll();
    } else {
      hide();
    }
  });
}

// formatRel renders a signed relative value: 0, +3, -2.
export function formatRel(v) {
  const n = parseInt(v, 10);
  if (isNaN(n) || n === 0) return '0';
  return n > 0 ? '+' + n : String(n);
}
