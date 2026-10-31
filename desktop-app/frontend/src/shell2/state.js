// shell2/state.js: the empty, loading, offline and error states of the UI 2.0
// preview, in one place (phase 5, docs/UI-2.0.md "States": no dead ends).
//
// Every state says what happened in words (never by colour alone) and offers
// the next step as real buttons:
//
//   noSpeakersState(phase)   no speakers yet, the search running or failed,
//                            every speaker offline, none chosen
//   connectHTML(id)          "Connect by address": the speakers a network
//                            search cannot see (guest Wi-Fi, a VPN, isolation)
//   playerStateModel/HTML    what the persistent player says without a live
//                            speaker
//   sectionReport(el, r)     a result or an error in the row, card or player
//                            it belongs to, instead of a toast across the
//                            window (rule 3: errors where they happen)
//
// The markup builders are pure (data in, markup out, vitest reads them). The
// few functions that touch the page (wireStates, sectionReport, announceState,
// paintPlayerState) do it only when called, never at import: main.js imports
// the shell before the view containers exist. The buttons are wired once, by
// delegation on the document, and only after the shell has mounted
// (wireStates from mountShell2), so the classic layout never sees a handler
// from here.
//
// The library helpers statusHTML, stateHTML and loadingHTML moved here from
// shell2/library.js, which re-exports them unchanged.

import './state.css';
import { t } from '../i18n/index.js';
import { state } from '../state.js';
import { escapeAttr, escapeHtml, showError, showToast } from '../utils.js';

// ---- the library helpers (moved from shell2/library.js) ---------------------

// statusHTML is the feedback line of a row or of the folder actions. It is
// always in the page, empty until there is news, so a screen reader hears it
// change (role=status) without the row being redrawn. tone: busy, ok, warn or
// error; the words say it, the colour only repeats them.
export function statusHTML(key, st) {
  const tone = (st && st.tone) || '';
  const text = (st && st.text) || '';
  return `<span class="lib2-status${tone ? ` lib2-status--${tone}` : ''}" role="status" data-skey="${escapeAttr(key)}">${escapeHtml(text)}</span>`;
}

// stateHTML is an empty, loading or error state that says what happened and
// what to do next (docs/UI-2.0.md: no dead ends). actions: [{ id, label,
// primary }]. An error is announced at once (alert); the rest politely.
export function stateHTML({ tone = 'info', title = '', body = '', detail = '', actions = [], extra = '' } = {}) {
  const role = tone === 'error' ? 'alert' : 'status';
  const acts = actions.length
    ? `<div class="lib2-state-acts">${actions.map((a) =>
      `<button type="button" class="u-btn ${a.primary ? 'u-btn--primary' : 'u-btn--secondary'}" id="${escapeAttr(a.id)}">${escapeHtml(a.label)}</button>`).join('')}</div>`
    : '';
  return `
    <div class="lib2-state lib2-state--${escapeAttr(tone)}" role="${role}">
      ${title ? `<h3 class="lib2-state-title lib2-focus-target" tabindex="-1">${escapeHtml(title)}</h3>` : ''}
      ${body ? `<p class="lib2-state-body">${escapeHtml(body)}</p>` : ''}
      ${detail ? `<p class="lib2-state-detail">${escapeHtml(detail)}</p>` : ''}
      ${extra}
      ${acts}
    </div>`;
}

// loadingHTML is a level that is on its way.
export function loadingHTML(text) {
  return `<p class="lib2-loading" role="status"><span class="lib2-spinner" aria-hidden="true"></span>${escapeHtml(text)}</p>`;
}

// ---- where the speakers stand -----------------------------------------------

export const NO_SPEAKER_PHASES = Object.freeze(['searching', 'none', 'failed', 'allOffline', 'noneChosen']);

// noNetworkText mirrors views/settings.js noNetworkHere: the request never
// left this computer (no Wi-Fi, no cable). Kept as a copy so this module does
// not pull the settings view in.
export function noNetworkText(err) {
  const s = String(err || '').toLowerCase();
  return s.includes('network is unreachable') || s.includes('unreachable network');
}

// discoveryPhase reads main.js state.discovery ({ phase: 'searching' | 'done'
// | 'failed', error, at, last }) for a list without a speaker in it:
//   searching  no search has finished yet (the first one of the session)
//   failed     the last search that finished threw
//   none       it finished and found nothing
// A repeat search keeps the answer of the last one on screen (busy: true) so
// the window never toggles between two screens on every sweep (#935).
export function discoveryPhase(discovery) {
  const d = discovery || null;
  if (!d) return { phase: 'searching', busy: true, error: '' };
  if (d.phase === 'searching') {
    const last = d.last || null;
    if (!last) return { phase: 'searching', busy: true, error: '' };
    return { phase: last.phase === 'failed' ? 'failed' : 'none', busy: true, error: last.error || '' };
  }
  if (d.phase === 'failed') return { phase: 'failed', busy: false, error: d.error || '' };
  return { phase: 'none', busy: false, error: '' };
}

// speakersPhase is the state of the whole speaker list: '' when there is a
// live speaker to control, else one of NO_SPEAKER_PHASES.
//   boxes       state.boxes
//   currentBox  state.currentBox
//   discovery   state.discovery
export function speakersPhase({ boxes = [], currentBox = null, discovery = null } = {}) {
  const list = (boxes || []).filter((b) => b && b.host);
  if (!list.length) return discoveryPhase(discovery);
  const busy = !!(discovery && discovery.phase === 'searching');
  if (list.every((b) => b.offline)) return { phase: 'allOffline', busy, error: '' };
  if (!currentBox) return { phase: 'noneChosen', busy, error: '' };
  return { phase: '', busy, error: '' };
}

// listStateNow is where the search stands for a view without its own
// discovery input (the Speakers list, the one-speaker page, the wizard):
//   phase      searching | none | failed (for a list without a speaker)
//   error      the failed search's message
//   searching  a search runs while speakers are listed
export function listStateNow() {
  const d = discoveryPhase(state.discovery);
  const has = (state.boxes || []).some((b) => b && b.host);
  return { phase: d.phase, error: d.error, searching: has && !!(state.discovery && state.discovery.phase === 'searching') };
}

// The actions of each phase, in DOM order. search: Search again; add: Add
// speaker (the setup wizard); connect: open the Connect by address block;
// list: Go to the speaker list (focuses the sidebar's speaker row).
const PHASE_ACTIONS = Object.freeze({
  searching: ['connect'],
  none: ['search', 'add', 'connect'],
  failed: ['search', 'connect'],
  allOffline: ['search', 'connect'],
  noneChosen: ['list'],
});

const PHASE_TONE = Object.freeze({
  searching: 'empty',
  none: 'empty',
  failed: 'error',
  allOffline: 'offline',
  noneChosen: 'empty',
});

// The state in words, the kicker above the heading: the dashed border or the
// red one only repeats it.
const PHASE_WORD = Object.freeze({
  searching: 'ui2.state.word.searching',
  none: 'ui2.state.word.none',
  failed: 'ui2.state.word.failed',
  allOffline: 'ui2.state.word.offline',
  noneChosen: 'ui2.state.word.noneChosen',
});

function phaseText(phase, ctx) {
  switch (phase) {
    case 'searching':
      return { title: t('ui2.state.searchingTitle'), body: t('ui2.state.searchingBody') };
    case 'failed':
      if (noNetworkText(ctx.error)) return { title: t('settingsView.noNetworkTitle'), body: t('settingsView.noNetworkHelp') };
      return { title: t('ui2.state.failedTitle'), body: t('ui2.state.failedBody') };
    case 'allOffline':
      return { title: t('ui2.state.allOfflineTitle'), body: t('ui2.state.allOfflineBody') };
    case 'noneChosen':
      return { title: t('ui2.state.noneChosenTitle'), body: t('ui2.state.noneChosenBody') };
    default:
      return { title: t('ui2.side.empty'), body: t('ui2.state.noneBody') };
  }
}

// searchBusy is the Search again of these states while it runs: the button
// says "Searching..." (aria-busy) on every copy and over every repaint.
let searchBusy = false;
export function stateSearchBusy() {
  return searchBusy;
}

function stateActionHTML(act, ctx, i) {
  const primary = i === 0 && act !== 'connect';
  const cls = `u-btn ${primary ? 'u-btn--primary' : 'u-btn--secondary'} u-state-act`;
  if (act === 'search') {
    const busy = !!ctx.searchBusy;
    return `<button type="button" class="${cls}" data-state-act="search"${busy ? ' aria-busy="true" aria-disabled="true"' : ''}>`
      + `${escapeHtml(t(busy ? 'ui2.state.searching' : 'ui2.state.searchAgain'))}</button>`;
  }
  if (act === 'add') return `<button type="button" class="${cls}" data-state-act="add">${escapeHtml(t('ui2.speakers.add'))}</button>`;
  // Set up STR: the same way in (the setup wizard), worded for a Bose original.
  if (act === 'setup') return `<button type="button" class="${cls}" data-state-act="add">${escapeHtml(t('ui2.speakers.list.setUp'))}</button>`;
  if (act === 'list') return `<button type="button" class="${cls}" data-state-act="list">${escapeHtml(t('ui2.state.goToList'))}</button>`;
  if (act === 'allSpeakers') return `<button type="button" class="${cls}" data-state-act="allSpeakers">${escapeHtml(t('ui2.speakers.all'))}</button>`;
  if (act === 'connect' && ctx.connectTarget) {
    // The block lives elsewhere on the page (the Speakers list's foot): the
    // button takes the keyboard there.
    return `<button type="button" class="${cls}" data-state-act="connect" data-connect-focus="1" aria-controls="${escapeAttr(ctx.connectTarget)}">`
      + `${escapeHtml(t('ui2.state.connect'))}</button>`;
  }
  if (act === 'connect') {
    const id = `${ctx.id}-connect`;
    const open = !!ctx.connectOpen;
    return `<button type="button" class="${cls}" data-state-act="connect" aria-expanded="${open}" aria-controls="${escapeAttr(id)}">`
      + `${escapeHtml(t('ui2.state.connect'))}</button>`;
  }
  return '';
}

// noSpeakersState is the card of one phase (NO_SPEAKER_PHASES).
//   ctx.id           a unique prefix for the ids inside (default ui2State)
//   ctx.error        the search's error, folded under Details (failed)
//   ctx.searchBusy   Search again is running (default: stateSearchBusy())
//   ctx.connectOpen  the Connect by address block is open
//   ctx.actions      replaces the phase's actions (same names)
//   ctx.level        the heading level (default 3)
//   ctx.title/body   replace the words of the phase
//   ctx.connectTarget  the id of a Connect by address block elsewhere on the
//                    page: Connect by address focuses it, no second block
// Its heading takes focus (tabindex=-1) when a view lands on it; a failure is
// announced at once (alert), every other phase politely (status).
export function noSpeakersState(phase, ctx = {}) {
  const p = NO_SPEAKER_PHASES.includes(phase) ? phase : 'none';
  const c0 = { id: 'ui2State', searchBusy: stateSearchBusy(), ...ctx };
  // The block stays open over a repaint once it was opened, holds an address
  // or an answer, or connects.
  const held = connectOf(`${c0.id}-connect`);
  const c = { ...c0, connectOpen: !!(c0.connectOpen || openConnects.has(c0.id) || held.value || held.error || held.busy || held.done) };
  const words = phaseText(p, c);
  const title = c.title || words.title;
  const body = c.body != null ? c.body : words.body;
  const tone = PHASE_TONE[p];
  const level = Math.min(6, Math.max(2, Number(c.level) || 3));
  const acts = (c.actions || PHASE_ACTIONS[p]).map((a, i) => stateActionHTML(a, c, i)).join('');
  const detail = p === 'failed' && c.error && !noNetworkText(c.error)
    ? `<details class="u-state-detail"><summary>${escapeHtml(t('ui2.state.details'))}</summary><p class="u-state-error">${escapeHtml(String(c.error))}</p></details>`
    : '';
  const connect = (c.actions || PHASE_ACTIONS[p]).includes('connect') && !c.connectTarget
    ? connectHTML(`${c.id}-connect`, { hidden: !c.connectOpen })
    : '';
  return `<div class="u-card u-card--${tone} u-state" data-phase="${p}" id="${escapeAttr(c.id)}" role="${p === 'failed' ? 'alert' : 'status'}">`
    + `<p class="u-state-word">${escapeHtml(t(PHASE_WORD[p]))}</p>`
    + `<h${level} class="u-state-title" tabindex="-1">${escapeHtml(title)}</h${level}>`
    + (body ? `<p class="u-state-body">${escapeHtml(body)}</p>` : '')
    + detail
    + (acts ? `<div class="u-state-acts">${acts}</div>` : '')
    + connect
    + '</div>';
}

// ---- Connect by address -----------------------------------------------------

// What each block holds over a repaint: the half-typed address, whether it is
// connecting, and the answer of the last try. Keyed by the block's id.
const connects = new Map();
const connectOf = (id) => connects.get(id) || { value: '', busy: false, error: '', done: '' };

// connectHTML is the Connect by address block: a visible label, the address
// field (the classic placeholder), the classic Connect by IP button, and two
// lines under it, the progress (status) and a failure (alert), so a failure
// stays where the address was typed instead of opening the error dialog. A
// form, so Enter in the field submits.
//   opts.hidden  closed (its opener is the phase's Connect by address)
//   opts.state   { value, busy, error, done } (default: what the block held)
export function connectHTML(id, opts = {}) {
  const st = opts.state || connectOf(id);
  const input = `${id}-ip`;
  const status = st.busy
    ? t('speaker.manualIpSearching', { ip: st.busy === true ? st.value : st.busy })
    : (st.done || '');
  return `<form class="u-connect" id="${escapeAttr(id)}" data-connect="${escapeAttr(id)}" novalidate${opts.hidden ? ' hidden' : ''}${st.busy ? ' aria-busy="true"' : ''}>`
    + `<label class="u-connect-label" for="${escapeAttr(input)}">${escapeHtml(t('ui2.state.addressLabel'))}</label>`
    + '<div class="u-connect-row">'
    + `<input type="text" class="u-connect-input" id="${escapeAttr(input)}" name="ip" value="${escapeAttr(st.value || '')}"`
    + ` placeholder="${escapeAttr(t('speaker.manualIpPlaceholder'))}" inputmode="decimal" autocomplete="off" spellcheck="false"`
    + (st.error ? ` aria-invalid="true" aria-describedby="${escapeAttr(id)}-err"` : '') + '>'
    + `<button type="submit" class="u-btn u-btn--secondary u-connect-btn"${st.busy ? ' aria-disabled="true"' : ''}>${escapeHtml(t('speaker.manualIpButton'))}</button>`
    + '</div>'
    + `<p class="u-connect-status" role="status">${escapeHtml(status)}</p>`
    + `<p class="u-connect-error" id="${escapeAttr(id)}-err" role="alert">${escapeHtml(st.error || '')}</p>`
    + '</form>';
}

// connectBusy reports whether a Connect by address block inside root is in
// use: connecting, or somebody typing in it (the field has the focus and
// holds an address). A repaint of root must wait while it is, or the minute
// sweep wipes what is being typed and moves the caret. An address left in a
// field that has no focus (after a failed connect, say) holds nothing back:
// the block keeps it (connects, read again by connectHTML), and the speakers
// a search found meanwhile must show. An empty field that only has focus does
// not hold a repaint back either; the caller puts the focus back.
export function connectBusy(root) {
  if (!root || typeof root.querySelectorAll !== 'function') return false;
  const active = typeof document !== 'undefined' ? document.activeElement : null;
  for (const form of root.querySelectorAll('.u-connect')) {
    const field = form.querySelector('.u-connect-input');
    if (connectOf(form.dataset.connect).busy) return true;
    if (field && field === active && (field.value || '').trim()) return true;
  }
  return false;
}

// connectFocus and restoreConnectFocus keep the keyboard in a Connect by
// address field over a repaint of root.
export function connectFocus(root) {
  const a = typeof document !== 'undefined' ? document.activeElement : null;
  return a && root && root.contains(a) && a.classList && a.classList.contains('u-connect-input') ? a.id : '';
}
export function restoreConnectFocus(root, id) {
  if (!id || !root) return;
  const el = root.querySelector(`#${CSS.escape(id)}`);
  if (el) el.focus();
}

// ---- the environment and the delegated wiring --------------------------------

// What the buttons act with, handed in by mount.js (configureStates):
//   discoverBoxes()     main.js discoverBoxes
//   addBoxByIp(ip)      main.js AddBoxByIP plus discoverBoxes (no new binding)
//   open(route, opts)   speakers/index.js openSpeakers
//   focusSpeakerList()  focuses the sidebar's roving speaker row
//   boxes()             the speaker list now (for the search's result line)
//   repaint()           repaint everything that shows a state
let env = {};
export function configureStates(e = {}) {
  env = { ...env, ...e };
}

// Views that draw a state outside the sidebar (Radio, the Speakers list) ask
// to be repainted when the speakers change (onStatesChange); mount.js calls
// notifyStates from renderShellSpeakers.
const watchers = new Set();
export function onStatesChange(fn) {
  if (typeof fn === 'function') watchers.add(fn);
  return () => watchers.delete(fn);
}
export function notifyStates() {
  for (const fn of [...watchers]) {
    try { fn(); } catch (_) { /* one view must not stop the others */ }
  }
}

const repaintAll = () => {
  if (typeof env.repaint === 'function') {
    try { env.repaint(); } catch (_) { /* keep going */ }
  }
  notifyStates();
};

// announceState says a result once, politely, through one live region that
// exists before the first result is written into it.
let stateLive = null;
export function announceState(text) {
  if (typeof document === 'undefined' || !text) return;
  if (!stateLive || !document.body.contains(stateLive)) {
    stateLive = document.createElement('div');
    stateLive.id = 'ui2StateLive';
    stateLive.className = 'u-sr-only';
    stateLive.setAttribute('role', 'status');
    stateLive.setAttribute('aria-live', 'polite');
    document.body.appendChild(stateLive);
  }
  const region = stateLive;
  // The same words twice are news too: clear first.
  region.textContent = '';
  setTimeout(() => { region.textContent = text; }, 50);
}

// searchAgain is every Search again of these states: one search at a time,
// "Searching..." on the button while it runs, then the result in words.
export async function stateSearchAgain() {
  if (searchBusy || typeof env.discoverBoxes !== 'function') return;
  searchBusy = true;
  repaintAll();
  let failed = false;
  try {
    await env.discoverBoxes();
  } catch (_) {
    failed = true;
  }
  searchBusy = false;
  const d = state.discovery;
  if (d && d.phase === 'failed') failed = true;
  const boxes = typeof env.boxes === 'function' ? (env.boxes() || []) : (state.boxes || []);
  const n = boxes.filter((b) => b && !b.offline).length;
  repaintAll();
  if (failed) announceState(t('ui2.state.searchFailed'));
  else announceState(n ? t('ui2.state.foundN', { n }) : t('ui2.state.foundNone'));
}

// connectSubmit runs one Connect by address: busy with the classic
// "Connecting to ..." line, then the field empties for the next address, or
// the classic "No speaker answered at ..." stays under it (alert).
export async function connectSubmit(form) {
  if (!form) return;
  const id = form.dataset.connect;
  const field = form.querySelector('.u-connect-input');
  const ip = ((field && field.value) || '').trim();
  const cur = connectOf(id);
  if (cur.busy) return;
  if (!ip) {
    if (field) field.focus();
    return;
  }
  connects.set(id, { value: ip, busy: ip, error: '', done: '' });
  paintConnect(form, id);
  let ok = false;
  try {
    if (typeof env.addBoxByIp !== 'function') throw new Error('no addBoxByIp');
    await env.addBoxByIp(ip);
    ok = true;
  } catch (_) {
    ok = false;
  }
  connects.set(id, ok
    ? { value: '', busy: false, error: '', done: t('ui2.state.connected', { ip }) }
    : { value: ip, busy: false, error: t('speaker.manualIpNotFound', { ip }), done: '' });
  // The block may have been repainted while it waited: find it again.
  const again = (typeof document !== 'undefined' && document.getElementById(id)) || form;
  paintConnect(again, id);
  if (!ok) {
    const f = again.querySelector('.u-connect-input');
    if (f) f.focus();
  } else {
    repaintAll();
  }
}

// paintConnect updates a block in place (its field keeps focus and caret).
function paintConnect(form, id) {
  if (!form || !form.isConnected) return;
  const st = connectOf(id);
  const field = form.querySelector('.u-connect-input');
  const btn = form.querySelector('.u-connect-btn');
  const status = form.querySelector('.u-connect-status');
  const err = form.querySelector('.u-connect-error');
  if (field && field.value !== st.value) field.value = st.value;
  if (field) {
    if (st.error) {
      field.setAttribute('aria-invalid', 'true');
      field.setAttribute('aria-describedby', `${id}-err`);
    } else {
      field.removeAttribute('aria-invalid');
      field.removeAttribute('aria-describedby');
    }
  }
  if (btn) {
    if (st.busy) btn.setAttribute('aria-disabled', 'true');
    else btn.removeAttribute('aria-disabled');
  }
  if (st.busy) form.setAttribute('aria-busy', 'true');
  else form.removeAttribute('aria-busy');
  if (status) status.textContent = st.busy ? t('speaker.manualIpSearching', { ip: st.busy }) : (st.done || '');
  if (err) err.textContent = st.error || '';
}

// Remembers the open Connect by address blocks over a repaint (by the id of
// the state card they belong to).
const openConnects = new Set();
export function connectOpen(id) {
  return openConnects.has(id);
}

function onStateClick(e) {
  const btn = e.target && e.target.closest ? e.target.closest('[data-state-act]') : null;
  if (!btn) return;
  if (btn.getAttribute('aria-disabled') === 'true') return;
  switch (btn.dataset.stateAct) {
    case 'search': stateSearchAgain(); break;
    case 'add': if (typeof env.open === 'function') env.open('add'); break;
    case 'allSpeakers': if (typeof env.open === 'function') env.open('list'); break;
    case 'list': if (typeof env.focusSpeakerList === 'function') env.focusSpeakerList(); break;
    case 'connect': {
      const id = btn.getAttribute('aria-controls');
      const form = id ? document.getElementById(id) : null;
      if (!form) break;
      if (btn.dataset.connectFocus) {
        form.hidden = false;
        const f = form.querySelector('.u-connect-input');
        if (f) {
          f.focus();
          try { f.scrollIntoView({ block: 'nearest' }); } catch (_) { /* old engine */ }
        }
        break;
      }
      const open = form.hidden;
      form.hidden = !open;
      btn.setAttribute('aria-expanded', String(open));
      const card = btn.closest('.u-state');
      if (card && card.id) {
        if (open) openConnects.add(card.id);
        else openConnects.delete(card.id);
      }
      if (open) {
        const f = form.querySelector('.u-connect-input');
        if (f) f.focus();
      }
      break;
    }
    default: break;
  }
}

function onStateSubmit(e) {
  const form = e.target && e.target.closest ? e.target.closest('form.u-connect') : null;
  if (!form) return;
  e.preventDefault();
  connectSubmit(form);
}

function onStateInput(e) {
  const field = e.target && e.target.closest ? e.target.closest('.u-connect-input') : null;
  if (!field) return;
  const form = field.closest('form.u-connect');
  if (!form) return;
  const id = form.dataset.connect;
  const cur = connectOf(id);
  // A new address clears the answer about the old one.
  connects.set(id, { ...cur, value: field.value, error: cur.busy ? cur.error : '', done: '' });
  if (!cur.busy) {
    const err = form.querySelector('.u-connect-error');
    const status = form.querySelector('.u-connect-status');
    if (err && err.textContent) err.textContent = '';
    if (status && status.textContent) status.textContent = '';
    field.removeAttribute('aria-invalid');
    field.removeAttribute('aria-describedby');
  }
}

// wireStates wires every state's buttons and every Connect by address block,
// once, by delegation, so a repaint never loses a handler. Called by
// mountShell2 only: without the shell nothing here listens.
let wired = false;
export function wireStates(doc = typeof document !== 'undefined' ? document : null) {
  if (wired || !doc) return false;
  wired = true;
  doc.addEventListener('click', onStateClick);
  doc.addEventListener('submit', onStateSubmit);
  doc.addEventListener('input', onStateInput);
  return true;
}

// ---- errors where they happen -------------------------------------------------

const REPORT_HOSTS = 'li, .u-card, .ui2-player, [data-report]';

// sectionReport writes a result into the row, card or player anchorEl sits in:
// one line, role=status (role=alert for an error), kept in that section until
// the next report replaces it. Outside the preview it is the classic feedback,
// unchanged: a toast, or the error dialog for an error.
//   r  { tone: 'ok' | 'warn' | 'busy' | 'error', text }
export function sectionReport(anchorEl, r = {}) {
  const text = (r && r.text) || '';
  const tone = (r && r.tone) || 'ok';
  if (!state.ui2Shell) {
    if (tone === 'error') showError(text);
    else showToast(text);
    return null;
  }
  const host = anchorEl && anchorEl.closest ? anchorEl.closest(REPORT_HOSTS) : null;
  if (!host) {
    if (tone === 'error') showError(text);
    else showToast(text);
    return null;
  }
  let line = host.querySelector(':scope > .u-report');
  if (!line) {
    line = document.createElement('p');
    line.className = 'u-report';
    host.appendChild(line);
  }
  line.setAttribute('role', tone === 'error' ? 'alert' : 'status');
  line.dataset.tone = tone;
  line.textContent = text;
  return line;
}

// Report hooks: a module outside the shell (nowplaying.js) asks for one by
// name when something fails and falls back to its classic feedback without
// one. mount.js sets them while the shell is mounted, so the classic layout
// never has a hook.
const hooks = new Map();
export function setReportHook(name, fn) {
  if (typeof fn === 'function') hooks.set(name, fn);
  else hooks.delete(name);
}
export function reportHook(name) {
  return (state.ui2Shell && hooks.get(name)) || null;
}

// reporter is the lazy form of sectionReport for a hook that takes a callback
// (group.js, nowplaying.js): the anchor is looked up when the report comes,
// so a section repainted in between still gets it.
export function reporter(anchor) {
  return (r) => sectionReport(typeof anchor === 'function' ? anchor() : anchor, r);
}

// ---- the persistent player without a live speaker ------------------------------

// playerStateModel says what the player shows instead of its controls:
//   kind  'live' (a reachable speaker: the controls show), 'offline' (the
//         speaker you control is not reachable), or a phase of the list
//   text  the line, actions  its buttons (noSpeakersState names)
export function playerStateModel({ currentBox = null, boxes = [], discovery = null, label } = {}) {
  const name = (b) => (typeof label === 'function' ? label(b) : '') || (b && (b.friendlyName || b.name || b.host)) || '';
  if (currentBox) {
    const rec = (boxes || []).find((b) => b && b.host === currentBox.host) || currentBox;
    if (rec.offline) {
      return { kind: 'offline', text: t('ui2.state.player.offline', { name: name(rec) }), actions: ['search'], name: name(rec) };
    }
    return { kind: 'live', text: '', actions: [], name: name(rec) };
  }
  const p = speakersPhase({ boxes, currentBox, discovery });
  if (p.phase === 'searching') return { kind: 'searching', text: t('ui2.state.searchingTitle'), actions: [] };
  if (p.phase === 'noneChosen' || p.phase === 'allOffline') {
    return { kind: 'noneChosen', text: t('ui2.state.noneChosenBody'), actions: ['list'] };
  }
  if (p.phase === 'failed') return { kind: 'failed', text: t('ui2.state.player.none'), actions: ['search', 'add'] };
  return { kind: 'none', text: t('ui2.state.player.none'), actions: ['add'] };
}

export function playerStateHTML(m) {
  if (!m || m.kind === 'live') return '';
  const acts = (m.actions || []).map((a, i) => stateActionHTML(a, { id: 'ui2PlayerState', searchBusy: stateSearchBusy() }, i)).join('');
  return `<p class="ui2-player-state-text" data-kind="${escapeAttr(m.kind)}">${escapeHtml(m.text)}</p>`
    + (acts ? `<span class="ui2-player-state-acts">${acts}</span>` : '');
}

// paintPlayerState puts the model into the player's empty slot, only when
// what it would draw changed (the status poll calls it every few seconds).
export function paintPlayerState(el, m) {
  if (!el) return;
  const html = playerStateHTML(m);
  if (el.dataset.sig === html) return;
  const hadFocus = el.contains(document.activeElement) ? document.activeElement.dataset.stateAct : '';
  el.dataset.sig = html;
  el.innerHTML = html;
  if (hadFocus) {
    const again = el.querySelector(`[data-state-act="${hadFocus}"]`);
    if (again) again.focus();
  }
}

// ---- the player's own error line ---------------------------------------------

const TRANSPORT_KEYS = Object.freeze({
  pause: 'ui2.state.player.failPause',
  resume: 'ui2.state.player.failResume',
  stop: 'ui2.state.player.failStop',
  next: 'ui2.state.player.failNext',
  prev: 'ui2.state.player.failPrev',
  queue: 'ui2.state.player.failQueue',
});

// transportErrorText names the speaker and what it did not do.
export function transportErrorText(kind, name, err) {
  const key = TRANSPORT_KEYS[kind] || TRANSPORT_KEYS.queue;
  const msg = String((err && err.message) || err || '').trim() || t('common.error');
  return t(key, { name, err: msg });
}

// groupEditVerdict decides a sidebar group edit the speakers accepted and the
// zone state has not shown yet (rule 2: done only when the speaker confirmed).
//   w        { want: 'in' | 'out', master, at }: master is the group's master
//            host, at when the edit was accepted
//   host     the speaker the edit is about
//   round    groups.js lastZoneRound(): { startedAt, answered: Set of hosts }
//   inGroup  whether the zone state now lists host in master's group
// 'confirmed' only from a zone round that started at least after ms past the
// edit (a follower's own report lags the change by about a second) and that
// both speakers answered: the optimistic entry written at the edit and an
// entry carried over from a round a speaker missed say nothing. 'failed'
// once within ms passed without that; 'wait' until then.
export function groupEditVerdict(w, host, round, inGroup, now, { after = 1000, within = 20000 } = {}) {
  if (!w) return 'wait';
  const r = round || {};
  const answered = r.answered && typeof r.answered.has === 'function' ? r.answered : new Set();
  const fresh = typeof r.startedAt === 'number' && r.startedAt >= w.at + after
    && answered.has(host) && (!w.master || answered.has(w.master));
  if (fresh && (w.want === 'in') === !!inGroup) return 'confirmed';
  if (now - w.at > within) return 'failed';
  return 'wait';
}
