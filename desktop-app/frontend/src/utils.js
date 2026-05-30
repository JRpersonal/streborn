// utils.js — small, view-independent helper functions.
//
// Everything here is either pure (formatNumber, escapeHtml, debounce)
// or depends only on the DOM skeleton that main.js renders on boot
// (showError, showToast, confirmWarn). No coupling to the state
// object on purpose, so utils can be imported anywhere without
// circular-import risk.

export const $ = (id) => document.getElementById(id);

export function escapeHtml(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => (
    { '&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;' }[c]
  ));
}

export function escapeAttr(s) { return escapeHtml(s); }

// decodeXmlEntities decodes the five named XML entity sequences plus
// numeric character references that the Bose /now_playing XML
// occasionally emits. Without this, "Bryan Adams &amp; Tina Turner"
// would be stored verbatim as a preset name and double-escaped at
// the next render.
export function decodeXmlEntities(s) {
  if (!s) return s;
  return String(s)
    .replace(/&amp;/g, '&')
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&#(\d+);/g, (_, n) => String.fromCharCode(parseInt(n, 10)))
    .replace(/&#x([0-9a-f]+);/gi, (_, h) => String.fromCharCode(parseInt(h, 16)));
}

export function formatNumber(n) {
  if (!n || isNaN(n)) return '0';
  return Number(n).toLocaleString('de-DE');
}

export function debounce(fn, ms) {
  let h = null;
  return (...args) => {
    if (h) clearTimeout(h);
    h = setTimeout(() => { h = null; fn(...args); }, ms);
  };
}

export function sleep(ms) {
  return new Promise(r => setTimeout(r, ms));
}

// formatRemaining turns a remaining-ms value into a "m:ss" string for
// countdown UI. Negative or zero inputs return "0:00". Used by the
// stick-install and OTA-wait flows where the previous implementation
// showed elapsed seconds that jumped 3-6s at a time (the value was
// updated once per polling cycle, which is uneven), confusing users
// who could not tell how much wait was left.
export function formatRemaining(ms) {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${String(s).padStart(2, '0')}`;
}

// ---------- Modals ----------
//
// The three modals (warn, error, toast) live in the DOM skeleton in
// main.js. These helpers bind to the well-known IDs the first time
// they are used so the wiring lives alongside the helpers that use
// it — no need for main.js to remember to wire anything.

let warnResolve = null;
let warnWired = false;
function wireWarn() {
  if (warnWired) return;
  warnWired = true;
  const cancel = $('warnCancel');
  const confirm = $('warnConfirm');
  if (cancel)  cancel.onclick  = () => { closeWarn(); if (warnResolve) warnResolve(false); };
  if (confirm) confirm.onclick = () => { closeWarn(); if (warnResolve) warnResolve(true); };
}

export function confirmWarn(title, bodyHtml) {
  wireWarn();
  const m = $('warnModal');
  if (!m) return Promise.resolve(false);
  const t = m.querySelector('.warn-title');
  if (t) t.innerHTML = '<span class="warn-icon">&#9888;</span> ' + escapeHtml(title);
  $('warnBody').innerHTML = bodyHtml;
  m.classList.remove('hidden');
  return new Promise(res => { warnResolve = res; });
}

export function closeWarn() {
  const m = $('warnModal');
  if (m) m.classList.add('hidden');
}

let errorWired = false;
function wireError() {
  if (errorWired) return;
  errorWired = true;
  const close = $('errorClose');
  const copy  = $('errorCopy');
  if (close) close.onclick = () => $('errorModal').classList.add('hidden');
  if (copy)  copy.onclick  = async () => {
    const txt = $('errorText').value;
    try {
      await navigator.clipboard.writeText(txt);
      copy.textContent = 'Kopiert!';
      setTimeout(() => { copy.textContent = 'Kopieren'; }, 1500);
    } catch {
      $('errorText').select();
      document.execCommand('copy');
    }
  };
}

export function showError(msg) {
  wireError();
  const m = $('errorModal');
  if (!m) return;
  $('errorText').value = String(msg || 'Unbekannter Fehler');
  m.classList.remove('hidden');
}

let toastTimer = null;
export function showToast(msg) {
  const t = $('toast');
  if (!t) return;
  t.textContent = msg;
  t.classList.add('show');
  if (toastTimer) clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show'), 2200);
}
