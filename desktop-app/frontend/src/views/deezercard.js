// views/deezercard.js: the Deezer section of the Spotify & Deezer view (both
// layouts). Its heading carries a Beta pill (#2039).
//
// It says plainly what works: a new Deezer sign-in is not possible without
// the Bose cloud, a Deezer account the speaker had before keeps working, and
// otherwise Deezer reaches the speaker over Bluetooth or AirPlay. It shows
// the speaker's Deezer state, Check again (which hands a kept login back to a
// speaker that lost it, docs/streaming/deezer.md), and Disconnect. Below
// that, a compact list of the Deezer logins of the home's speakers, with Copy
// where this speaker's kept login can help another one and "Use the older
// login" where Deezer refuses this speaker's login and an older one is backed
// up. Nothing is copied unless the owner presses Copy. The rules are in
// deezer.js; this file only draws them.
//
// An agent too old for /api/deezer/account, or a build without the binding,
// leaves the section out.

import { $, escapeHtml, escapeAttr } from '../utils.js';
import { t, getLocale } from '../i18n/index.js';
import { state } from '../state.js';
import {
  DeezerStatus, DeezerRecheck, UnlinkMusicService, BrowserOpenURL, ClipboardSetText,
  DeezerHousehold, DeezerCopyLogin, DeezerUseOlderLogin,
} from '../api.js';
import {
  deezerView, deezerStatusLine, deezerPill, deezerCanDisconnect,
  deezerRefreshDelays, deezerDisconnectError,
  deezerHousehold, deezerHomeVisible, deezerShowCopyAll, deezerRowState, deezerRowDetails,
  deezerKeptDate, deezerCopyMessages, deezerCopyOk,
} from '../deezer.js';
import { agentErrorText } from '../usservices.js';
import { confirmFor } from './settings/shared.js';

// gen drops an answer for a speaker that is no longer the selected one, and
// pending holds the re-reads after a Check again so a new one replaces them.
let gen = 0;
let pending = [];
let msg = { host: '', cls: '', text: '' };

// DEEZER_SUPPORT_URL is Deezer's help centre, where its chat and contact form
// start. The support line asks owners to request access for open-source
// projects: since October 2025 Deezer registers no new API apps for private
// persons, which is the one door that would let STR sign a speaker in.
export const DEEZER_SUPPORT_URL = 'https://support.deezer.com/';
export const COPIED_MS = 2000;

export function deezerCardMarkup() {
  return `<section class="sp-card" id="deezerCard" aria-labelledby="deezerCardTitle" style="display:none">
        <h3 class="sp-card-title" id="deezerCardTitle">Deezer <span class="beta-pill" title="${escapeAttr(t('deezer.betaHint'))}">${escapeHtml(t('deezer.beta'))}</span></h3>
        <div id="deezerCardBody"></div>
      </section>`;
}

function speakerName(box) {
  return (box && (box.name || box.friendlyName || box.host)) || '';
}

// renderDeezerCard reads the selected speaker's Deezer state and draws the
// section. Called on every visit of the Spotify view and after each action.
// recheck: the read is the Check again button's, which may hand the kept
// login back first (an older agent without it gets the plain read).
export async function renderDeezerCard(recheck = false) {
  const card = $('deezerCard');
  if (!card) return;
  const box = state.currentBox;
  if (!box || !box.host || box.kind === 'stock') {
    card.style.display = 'none';
    return;
  }
  const mine = ++gen;
  let status;
  try {
    status = recheck
      ? await DeezerRecheck(box.host, box.port).catch(() => DeezerStatus(box.host, box.port))
      : await DeezerStatus(box.host, box.port);
  } catch {
    if (mine === gen) card.style.display = 'none';
    return;
  }
  if (mine !== gen || !$('deezerCardBody')) return;
  card.style.display = '';
  const v = deezerView(status);
  paint(box, v);
  if (recheck && v.recreated) refreshSoon();
  renderDeezerHome(box, mine).catch(() => {});
}

// renderDeezerHome reads the home's Deezer logins once (no polling) and
// draws the list into the card. An agent without the overview leaves it out.
async function renderDeezerHome(box, mine) {
  let raw;
  try {
    raw = await DeezerHousehold(box.host, box.port);
  } catch {
    raw = null;
  }
  if (mine !== gen) return;
  const el = $('deezerHome');
  if (!el) return;
  const hh = deezerHousehold(raw);
  el.innerHTML = deezerHomeHTML(hh, t, (iso) => deezerKeptDate(iso, getLocale()));
  wireHome(box);
}

// deezerHomeHTML draws the list: one compact entry per speaker, a button
// only where an action applies. tr is the translator, date the date format.
export function deezerHomeHTML(hh, tr, date) {
  if (!deezerHomeVisible(hh)) return '';
  const items = hh.rows.map((r) => {
    const name = r.self ? tr('deezer.home.this', { name: r.name || '?' }) : (r.name || '?');
    const details = deezerRowDetails(r, date).map(([k, p]) => escapeHtml(tr(k, p))).join(' · ');
    let action = '';
    if (r.canCopy) {
      action = `<button type="button" class="btn btn-mini" data-dz-copy="${escapeAttr(r.id)}">${escapeHtml(tr('deezer.home.copy', { name: r.name || '?' }))}</button>`;
    } else if (r.canUseOlder) {
      action = `<button type="button" class="btn btn-mini" id="deezerUseOlder">${escapeHtml(tr('deezer.home.useOlder'))}</button>`;
    }
    return `<li class="deezer-home-row" style="margin:6px 0">
        <div><b>${escapeHtml(name)}</b> <span class="muted">${escapeHtml(tr(deezerRowState(r)))}</span></div>
        ${details ? `<div class="muted small">${details}</div>` : ''}
        ${action ? `<div style="margin-top:4px">${action}</div>` : ''}
      </li>`;
  }).join('');
  const all = deezerShowCopyAll(hh)
    ? `<p style="margin-top:6px"><button type="button" class="btn btn-mini" id="deezerCopyAll">${escapeHtml(tr('deezer.home.copyAll'))}</button></p>`
    : '';
  return `<h4 class="small" style="margin:10px 0 2px">${escapeHtml(tr('deezer.home.title'))}</h4>
    <ul class="deezer-home" style="list-style:none;padding:0;margin:0">${items}</ul>
    ${all}
    <p class="muted small">${escapeHtml(tr('deezer.home.hint'))}</p>`;
}

function wireHome(box) {
  const el = $('deezerHome');
  if (!el) return;
  el.querySelectorAll('[data-dz-copy]').forEach((btn) => {
    btn.onclick = () => copyLogin(box, btn, btn.getAttribute('data-dz-copy'));
  });
  const all = $('deezerCopyAll');
  if (all) all.onclick = () => copyLogin(box, all, '');
  const older = $('deezerUseOlder');
  if (older) older.onclick = async () => {
    older.disabled = true;
    try {
      const res = await DeezerUseOlderLogin(box.host, box.port);
      say(box, 'setup-ok', t('deezer.home.older.done'));
      if (res && res.recreate && res.recreate.ran === true) refreshSoon();
    } catch (e) {
      say(box, 'setup-err', t('deezer.home.older.failed', { err: agentErrorText(e) }));
    }
    renderDeezerCard().catch(() => {});
  };
}

async function copyLogin(box, btn, target) {
  btn.disabled = true;
  say(box, '', t('deezer.home.copying'));
  try {
    const ans = await DeezerCopyLogin(box.host, box.port, target);
    const text = deezerCopyMessages(ans, target === '').map(([k, p]) => t(k, p)).join(' ');
    say(box, deezerCopyOk(ans) ? 'setup-ok' : 'setup-warn', text);
  } catch (e) {
    say(box, 'setup-err', t('deezer.home.result.failed', { err: agentErrorText(e) }));
  }
  renderDeezerCard().catch(() => {});
}

function say(box, cls, text) {
  msg = { host: box.host, cls, text };
  const el = $('deezerCardMsg');
  if (el) el.innerHTML = text ? `<div class="${escapeAttr(cls)}">${escapeHtml(text)}</div>` : '';
}

function paint(box, v) {
  const body = $('deezerCardBody');
  if (!body) return;
  const [key, params] = deezerStatusLine(v);
  const [tone, pill] = deezerPill(v);
  const name = speakerName(box);
  const disconnect = deezerCanDisconnect(v)
    ? `<button class="btn btn-mini" id="deezerDisconnect">${escapeHtml(t('usServices.disconnect'))}</button>`
    : '';
  const warn = [
    v.stuck ? t('deezer.stuck') : '',
    v.rejected ? t('deezer.rejected') : '',
  ].filter(Boolean).map((s) => `<p class="small setup-warn">${escapeHtml(s)}</p>`).join('');
  const keep = msg.host === box.host ? msg : { cls: '', text: '' };
  body.innerHTML = `
    <p class="muted small">${escapeHtml(t('deezer.intro'))}</p>
    <p><b>${escapeHtml(t('deezer.speaker', { name }))}</b>
      <span class="u-pill u-pill--${escapeAttr(tone)}"><span class="u-pill-dot" aria-hidden="true"></span>${escapeHtml(t(pill))}</span></p>
    <p class="small" role="status" aria-live="polite">${escapeHtml(t(key, params))}</p>
    ${warn}
    <div style="display:flex;gap:8px;flex-wrap:wrap;margin-top:6px">
      ${disconnect}
      <button class="btn btn-mini" id="deezerRefresh">${escapeHtml(t('deezer.refresh'))}</button>
    </div>
    <div id="deezerCardMsg" class="small">${keep.text ? `<div class="${escapeAttr(keep.cls)}">${escapeHtml(keep.text)}</div>` : ''}</div>
    <div id="deezerHome"></div>
    <p class="muted small">${escapeHtml(t('deezer.linkHint'))}</p>
    <p class="muted small">${escapeHtml(t('deezer.diagHint'))}</p>
    ${deezerSupportHTML(t)}`;
  wire(box);
}

// deezerSupportHTML is the line and the two buttons that point owners at
// Deezer support with a ready message asking for access for open-source
// projects like ST Reborn. Shown on every speaker: a new Deezer sign-in is
// impossible everywhere until Deezer opens such a door.
export function deezerSupportHTML(tr) {
  return `<p class="small deezer-support">${escapeHtml(tr('deezer.support'))}</p>`
    + '<p class="deezer-support-actions" style="display:flex;gap:8px;flex-wrap:wrap">'
    + `<button type="button" class="btn btn-mini" id="deezerSupportOpen">${escapeHtml(tr('deezer.supportOpen'))}</button>`
    + `<button type="button" class="btn btn-mini" id="deezerSupportCopy">${escapeHtml(tr('deezer.supportCopy'))}</button>`
    + '</p>';
}

// deezerSupportCopy copies the message and shows "Copied" on the button for
// COPIED_MS. deps carries the side effects so a test can drive it.
export function deezerSupportCopy(btn, deps) {
  const label = deps.t('deezer.supportCopy');
  return Promise.resolve()
    .then(() => deps.copy(deps.t('deezer.supportMessage')))
    .then((ok) => {
      // Wails answers false when the clipboard refused the text.
      if (ok === false) return false;
      btn.textContent = deps.t('deezer.supportCopied');
      deps.later(() => { btn.textContent = label; }, COPIED_MS);
      return true;
    })
    .catch(() => false);
}

function refreshSoon() {
  pending.forEach(clearTimeout);
  pending = deezerRefreshDelays.map((ms) => setTimeout(() => {
    if (state.view === 'spotify') renderDeezerCard().catch(() => {});
  }, ms));
}

function wire(box) {
  const disc = $('deezerDisconnect');
  if (disc) disc.onclick = async () => {
    const ok = await confirmFor(box, t('usServices.disconnectTitle', { service: 'Deezer' }), escapeHtml(t('deezer.disconnectBody')));
    if (!ok) return;
    disc.disabled = true;
    try {
      await UnlinkMusicService(box.host, box.port, 'deezer');
      say(box, 'setup-ok', t('usServices.disconnected'));
    } catch (e) {
      const [k, p] = deezerDisconnectError(e);
      say(box, 'setup-err', t(k, p));
    }
    renderDeezerCard().catch(() => {});
  };
  const open = $('deezerSupportOpen');
  if (open) open.onclick = () => { BrowserOpenURL(DEEZER_SUPPORT_URL); };
  const copy = $('deezerSupportCopy');
  if (copy) copy.onclick = () => {
    deezerSupportCopy(copy, { copy: ClipboardSetText, later: setTimeout, t });
  };
  const ref = $('deezerRefresh');
  if (ref) ref.onclick = () => {
    ref.disabled = true;
    say(box, '', '');
    renderDeezerCard(true).catch(() => {});
  };
}
