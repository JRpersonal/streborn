// Share buttons ("Recommend STR"). Two places show them: a dialog behind the
// footer menu item, always reachable, and one quiet row under the success
// message after the first install on this computer. Nothing else: no banner,
// nothing at app start, never a second ask.
//
// Which targets exist, their order, groups, texts and URLs come from the
// website registry only (src/data/share-targets.json, resolved by
// shareRegistry.js). This file renders whatever that registry says, so there is
// no list of platforms here. Every target is a plain link opened in the
// system browser through BrowserOpenURL, or a local copy action. No platform
// script, no API, no login, and no click counting.
import registry from './data/share-targets.json';
import { t, tIn, getLocale } from './i18n/index.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { BrowserOpenURL, ClipboardSetText, PhoneQR } from './api.js';
import { resolveShareTargets, cleanInstanceHost } from './shareRegistry.js';
import { SHARE_ICONS as ICONS } from './shareIcons.js';

const INSTANCE_KEY = 'str-mastodon-instance';
const OFFER_KEY = 'shareOfferShown';

function iconSVG(name) {
  return `<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true" focusable="false">${Object.hasOwn(ICONS, name) ? ICONS[name] : ICONS.native}</svg>`;
}

// The registry colour ends up in a style attribute, so only a plain hex value
// is let through.
function brandStyle(color) {
  return /^#[0-9a-f]{3,8}$/i.test(color || '') ? ` style="--share-brand:${color}"` : '';
}

// Resolved once per render; a language switch reloads the app anyway.
function currentTargets() {
  return resolveShareTargets(registry.targets, getLocale(), tIn);
}

function readInstance() {
  try { return localStorage.getItem(INSTANCE_KEY) || ''; } catch { return ''; }
}
function writeInstance(host) {
  try {
    if (host) localStorage.setItem(INSTANCE_KEY, host);
    else localStorage.removeItem(INSTANCE_KEY);
  } catch { /* storage blocked: then it simply asks each time */ }
}

// withQr: a scannable code under a link target, so the post can be opened on
// the phone where the user is logged in. Only plain share links get one:
// Mastodon needs an instance first, copy and email have nothing to post.
function targetHTML(trg, prefix, withQr) {
  const noteId = trg.note ? `${prefix}-note-${trg.id}` : '';
  const describedBy = noteId ? ` aria-describedby="${noteId}"` : '';
  let extra = '';
  if (trg.kind === 'instance-prompt') {
    // Asked once, inline (a Wails webview has no reliable window.prompt), then
    // remembered on this computer. The "use a different instance" button only
    // shows while one is remembered.
    extra = `<form class="share-instance" data-share-instance-form hidden>
        <label class="share-instance-lbl"><span>${escapeHtml(t('share.mastodonPrompt'))}</span>
          <input type="text" inputmode="url" autocomplete="off" spellcheck="false" placeholder="mastodon.social"></label>
        <span class="share-instance-btns">
          <button type="submit" class="btn btn-mini btn-primary">${escapeHtml(trg.label)}</button>
          <button type="button" class="btn btn-mini" data-share-instance-cancel>${escapeHtml(t('common.cancel'))}</button>
        </span>
      </form>
      <button type="button" class="share-reset" data-share-instance-reset${readInstance() ? '' : ' hidden'}>${escapeHtml(t('share.mastodonChange'))}</button>`;
  }
  return `<li class="share-item" data-share-item="${escapeAttr(trg.id)}">
      <button type="button" class="share-btn" data-share-id="${escapeAttr(trg.id)}" aria-label="${escapeAttr(trg.label)}"${describedBy}${brandStyle(trg.color)}>
        <span class="share-ic">${iconSVG(trg.icon)}</span><span class="share-name">${escapeHtml(trg.name)}</span>
      </button>
      ${trg.note ? `<p class="share-note" id="${noteId}">${escapeHtml(trg.note)}</p>` : ''}
      ${withQr && trg.kind === 'url-template' ? `<figure class="share-qr" data-share-qr="${escapeAttr(trg.id)}" hidden>
        <img alt="${escapeAttr(trg.label)}" width="96" height="96">
        <figcaption>${escapeHtml(t('share.scan'))}</figcaption>
      </figure>` : ''}
      ${extra}
    </li>`;
}

// shareButtonsHTML renders every enabled registry target for the UI language.
// grouped: one labelled block per registry group (the dialog). Otherwise one
// flat row in registry order (the install success row). prefix keeps ids
// unique when both are in the DOM at once.
export function shareButtonsHTML(prefix, { grouped = false } = {}) {
  const groups = currentTargets();
  const body = grouped
    ? groups.map((g) => `<div class="share-group" role="group" aria-labelledby="${prefix}-g-${g.group}">
        <h4 class="share-group-h" id="${prefix}-g-${g.group}">${escapeHtml(t('share.groups.' + g.group))}</h4>
        <ul class="share-list">${g.targets.map((trg) => targetHTML(trg, prefix, true)).join('')}</ul>
      </div>`).join('')
    : `<ul class="share-list">${groups.flatMap((g) => g.targets).map((trg) => targetHTML(trg, prefix, false)).join('')}</ul>`;
  return `<div class="share-block" data-share-root="${escapeAttr(prefix)}">
      ${body}
      <p class="share-status" role="status" aria-live="polite"></p>
      <div class="share-fallback" hidden>
        <label><span>${escapeHtml(t('share.copyFailed'))}</span>
          <input type="text" readonly></label>
      </div>
    </div>`;
}

async function copyText(text) {
  try {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch { /* webview refused, try the native clipboard */ }
  try { return !!(await ClipboardSetText(text)); } catch { return false; }
}

// paintShareQrs fills the QR slots of a block with codes from the Go backend
// (PhoneQR, the same local generator as the phone QR in Settings, no external
// service). Each code carries exactly the link its button opens. A code that
// cannot be made just stays hidden; the button still works.
export async function paintShareQrs(block, byId) {
  const slots = [...block.querySelectorAll('[data-share-qr]')];
  await Promise.all(slots.map(async (fig) => {
    const trg = byId.get(fig.dataset.shareQr);
    if (!trg || !trg.href) return;
    try {
      fig.querySelector('img').src = await PhoneQR(trg.href);
      fig.hidden = false;
    } catch { /* no code, button only */ }
  }));
}

// wireShareButtons attaches the behaviour to one rendered share block.
export function wireShareButtons(root) {
  const block = root && (root.matches('[data-share-root]') ? root : root.querySelector('[data-share-root]'));
  if (!block) return;
  const byId = new Map(currentTargets().flatMap((g) => g.targets).map((trg) => [trg.id, trg]));
  paintShareQrs(block, byId).catch(() => {});
  const status = block.querySelector('.share-status');
  const fallback = block.querySelector('.share-fallback');
  let statusTimer = null;
  const say = (msg) => {
    if (!status) return;
    status.textContent = msg;
    clearTimeout(statusTimer);
    statusTimer = setTimeout(() => { status.textContent = ''; }, 2600);
  };

  const openInstance = (trg, host) => {
    BrowserOpenURL(trg.template.replace('{instance}', host));
  };

  block.querySelectorAll('.share-btn[data-share-id]').forEach((btn) => {
    btn.onclick = async () => {
      const trg = byId.get(btn.dataset.shareId);
      if (!trg) return;
      if (trg.kind === 'copy') {
        if (await copyText(trg.shareUrl)) {
          if (fallback) fallback.hidden = true;
          say(t('share.copied'));
        } else if (fallback) {
          const input = fallback.querySelector('input');
          fallback.hidden = false;
          input.value = trg.shareUrl;
          input.focus();
          input.select();
        }
      } else if (trg.kind === 'instance-prompt') {
        const host = readInstance();
        if (host) { openInstance(trg, host); return; }
        const form = btn.closest('.share-item').querySelector('[data-share-instance-form]');
        if (!form) return;
        form.hidden = false;
        form.querySelector('input').focus();
      } else if (trg.href) {
        BrowserOpenURL(trg.href);
      }
    };
  });

  block.querySelectorAll('[data-share-instance-form]').forEach((form) => {
    const item = form.closest('.share-item');
    const trg = byId.get(item.dataset.shareItem);
    const input = form.querySelector('input');
    const reset = item.querySelector('[data-share-instance-reset]');
    const close = () => {
      form.hidden = true;
      input.value = '';
      input.removeAttribute('aria-invalid');
      item.querySelector('.share-btn').focus();
    };
    form.onsubmit = (e) => {
      e.preventDefault();
      const host = cleanInstanceHost(input.value);
      if (!host) {
        // Only a bare host name is accepted; anything else stays in the field.
        input.setAttribute('aria-invalid', 'true');
        input.select();
        return;
      }
      writeInstance(host);
      if (reset) reset.hidden = false;
      close();
      openInstance(trg, host);
    };
    form.querySelector('[data-share-instance-cancel]').onclick = close;
    form.onkeydown = (e) => { if (e.key === 'Escape') { e.stopPropagation(); close(); } };
    if (reset) reset.onclick = () => {
      writeInstance('');
      reset.hidden = true;
      form.hidden = false;
      input.focus();
    };
  });
}

// ---------- Dialog behind the footer menu item ----------

export function shareModalHTML() {
  return `<div class="modal hidden" id="shareModal">
    <div class="modal-content share-modal" role="dialog" aria-modal="true" aria-labelledby="shareTitle">
      <h3 id="shareTitle">${escapeHtml(t('share.menu'))}</h3>
      <div id="shareModalBody"></div>
      <div class="share-foot">
        <button class="btn" id="shareClose" type="button">${escapeHtml(t('common.close'))}</button>
      </div>
    </div>
  </div>`;
}

let returnFocus = null;

// openShareModal fills the dialog fresh each time (so a forgotten Mastodon
// instance or a copy fallback from last time never lingers), moves focus into
// it, keeps Tab inside, and closes on Escape or a backdrop click.
export function openShareModal() {
  const modal = document.getElementById('shareModal');
  const body = document.getElementById('shareModalBody');
  if (!modal || !body) return;
  returnFocus = document.activeElement;
  body.innerHTML = shareButtonsHTML('shareDlg', { grouped: true });
  wireShareButtons(body);
  const close = document.getElementById('shareClose');
  if (close) close.onclick = closeShareModal;
  modal.onclick = (e) => { if (e.target === modal) closeShareModal(); };
  modal.onkeydown = (e) => {
    if (e.key === 'Escape') { e.preventDefault(); closeShareModal(); return; }
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
  const firstBtn = body.querySelector('.share-btn');
  if (firstBtn) firstBtn.focus();
}

export function closeShareModal() {
  const modal = document.getElementById('shareModal');
  if (!modal || modal.classList.contains('hidden')) return;
  modal.classList.add('hidden');
  if (returnFocus && typeof returnFocus.focus === 'function') {
    try { returnFocus.focus(); } catch { /* element gone */ }
  }
  returnFocus = null;
}

// ---------- One-time row under the install success message ----------

// takeShareOffer returns the quiet share row for the install success message
// the first time it is asked on this computer, and '' ever after. The decision
// is remembered the moment the row is handed out. If storage cannot be written
// the row is not shown at all: without a place to remember it, showing it
// could mean showing it again next time.
export function takeShareOffer() {
  try {
    if (localStorage.getItem(OFFER_KEY)) return '';
    localStorage.setItem(OFFER_KEY, '1');
    if (!localStorage.getItem(OFFER_KEY)) return '';
  } catch {
    return '';
  }
  return `<div class="share-offer" id="installShareOffer">
      <p class="share-offer-line">${escapeHtml(t('share.successLine'))}</p>
      ${shareButtonsHTML('shareOffer')}
    </div>`;
}

// wireShareOffer wires the row once it is in the DOM; a no-op when
// takeShareOffer returned nothing.
export function wireShareOffer() {
  const el = document.getElementById('installShareOffer');
  if (el) wireShareButtons(el);
}
