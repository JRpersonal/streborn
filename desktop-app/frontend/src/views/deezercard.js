// views/deezercard.js: the Deezer section of the Spotify & Deezer view (both
// layouts). Its heading carries a Beta pill: the login is new and not yet
// confirmed on every speaker (#2039).
//
// Signs the selected speaker into Deezer: e-mail, password, Connect,
// Disconnect, and what the speaker made of it. The speaker's own Deezer client
// logs in and plays; the password goes from the form to the agent and on to
// the speaker, and nothing here keeps it (docs/streaming/deezer.md, A2). The
// rules are in deezer.js; this file only draws them.
//
// An agent too old for /api/deezer/account, or a build without the binding,
// leaves the section out.

import { $, escapeHtml, escapeAttr } from '../utils.js';
import { t } from '../i18n/index.js';
import { state } from '../state.js';
import { DeezerStatus, LinkMusicService, UnlinkMusicService } from '../api.js';
import {
  deezerView, deezerStatusLine, deezerPill, deezerNeedsLogin, deezerCanDisconnect,
  deezerRefreshDelays, deezerConnectError, deezerDisconnectError,
} from '../deezer.js';
import { confirmFor } from './settings/shared.js';

// gen drops an answer for a speaker that is no longer the selected one, and
// pending holds the re-reads after a Connect so a new one replaces them.
let gen = 0;
let pending = [];
let msg = { host: '', cls: '', text: '' };

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
export async function renderDeezerCard() {
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
    status = await DeezerStatus(box.host, box.port);
  } catch {
    if (mine === gen) card.style.display = 'none';
    return;
  }
  if (mine !== gen || !$('deezerCardBody')) return;
  card.style.display = '';
  paint(box, deezerView(status));
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
  const login = deezerNeedsLogin(v)
    ? `<div class="wlan-row">
        <input type="email" autocomplete="off" id="deezerUser" placeholder="${escapeAttr(t('usServices.emailPlaceholder'))}" aria-label="${escapeAttr(t('usServices.emailPlaceholder'))}" />
      </div>
      <div class="wlan-row">
        <input type="password" autocomplete="off" id="deezerPass" placeholder="${escapeAttr(t('usServices.passwordPlaceholder'))}" aria-label="${escapeAttr(t('usServices.passwordPlaceholder'))}" />
        <button class="btn btn-mini btn-primary" id="deezerConnect">${escapeHtml(t('usServices.connect'))}</button>
      </div>`
    : '';
  const disconnect = deezerCanDisconnect(v)
    ? `<button class="btn btn-mini" id="deezerDisconnect">${escapeHtml(t('usServices.disconnect'))}</button>`
    : '';
  const keep = msg.host === box.host ? msg : { cls: '', text: '' };
  body.innerHTML = `
    <p class="muted small">${escapeHtml(t('deezer.intro'))}</p>
    <p class="muted small">${escapeHtml(t('deezer.untested'))}</p>
    <p><b>${escapeHtml(t('deezer.speaker', { name }))}</b>
      <span class="u-pill u-pill--${escapeAttr(tone)}"><span class="u-pill-dot" aria-hidden="true"></span>${escapeHtml(t(pill))}</span></p>
    <p class="small" role="status" aria-live="polite">${escapeHtml(t(key, params))}</p>
    ${login}
    <div style="display:flex;gap:8px;flex-wrap:wrap;margin-top:6px">
      ${disconnect}
      <button class="btn btn-mini" id="deezerRefresh">${escapeHtml(t('deezer.refresh'))}</button>
    </div>
    <div id="deezerCardMsg" class="small">${keep.text ? `<div class="${escapeAttr(keep.cls)}">${escapeHtml(keep.text)}</div>` : ''}</div>
    <p class="muted small">${escapeHtml(t('deezer.linkHint'))}</p>`;
  wire(box);
}

function refreshSoon() {
  pending.forEach(clearTimeout);
  pending = deezerRefreshDelays.map((ms) => setTimeout(() => {
    if (state.view === 'spotify') renderDeezerCard().catch(() => {});
  }, ms));
}

function wire(box) {
  const connect = $('deezerConnect');
  if (connect) connect.onclick = async () => {
    const userEl = $('deezerUser');
    const passEl = $('deezerPass');
    const user = userEl ? userEl.value.trim() : '';
    const pass = passEl ? passEl.value : '';
    if (!user || !pass) { say(box, 'setup-warn', t('usServices.needBoth')); return; }
    connect.disabled = true;
    say(box, 'muted small', t('usServices.connecting'));
    try {
      await LinkMusicService(box.host, box.port, 'deezer', user, pass);
      if (passEl) passEl.value = '';
      say(box, 'setup-ok', t('usServices.connectSent'));
      refreshSoon();
    } catch (e) {
      if (passEl) passEl.value = '';
      const [k, p] = deezerConnectError(e);
      say(box, 'setup-err', t(k, p));
    }
    renderDeezerCard().catch(() => {});
  };
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
  const ref = $('deezerRefresh');
  if (ref) ref.onclick = () => { renderDeezerCard().catch(() => {}); };
}
