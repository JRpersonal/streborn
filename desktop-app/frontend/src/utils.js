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

// compareVerBuild compares two (version, build) pairs and returns -1 if A<B,
// 1 if A>B, 0 if equal. version is "vMAJOR.MINOR.PATCH" (a leading "v" and any
// "-N-gHASH" dev suffix are ignored); build is a sortable "YYYY-MM-DD-HHMM"
// stamp used as the tie-breaker. This tells whether the desktop app can upgrade
// a speaker agent (app newer -> offer the OTA) or whether the app itself is
// behind the speaker (app older -> an OTA would DOWNGRADE the box, so offer an
// app update instead). The old logic only checked "differs" and so offered a
// downgrade when the speaker was newer than the app (#105 update-banner UX).
export function compareVerBuild(aVer, aBuild, bVer, bBuild) {
  const parse = (v) => String(v || '').replace(/^v/, '').split('-')[0].split('.').map(n => parseInt(n, 10) || 0);
  const pa = parse(aVer), pb = parse(bVer);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const da = pa[i] || 0, db = pb[i] || 0;
    if (da !== db) return da < db ? -1 : 1;
  }
  const sa = String(aBuild || ''), sb = String(bBuild || '');
  if (sa === sb) return 0;
  return sa < sb ? -1 : 1;
}

// boxModelSupport classifies a Bose /info <type> model string for STR support.
// STR only runs on the standalone SoundTouch speakers that have physical preset
// buttons: SoundTouch 10, 20, 30, Portable, and the Wave SoundTouch system.
// Other SoundTouch-speaking devices show up in discovery but cannot run STR, and
// the install then dead-ends in an ssh255 error the user cannot interpret
// (reported for Lifestyle / CineMate home-cinema systems, the SoundTouch 300
// soundbar, and the SoundTouch Wireless Link Adapter). Returns:
//   'supported'   - a standalone SoundTouch speaker STR targets
//   'unsupported' - a known device STR cannot run on: show a clear note, no install
//   'unknown'     - type absent or unrecognised: do NOT block. STR is
//                   compatibility-first, so an odd type string on a real speaker
//                   must still be installable; callers treat 'unknown' like
//                   'supported' for gating and only special-case 'unsupported'.
export function boxModelSupport(model) {
  const m = String(model || '').toLowerCase().trim();
  if (!m || m === 'soundtouch') return 'unknown';
  // Known-unsupported families first: a soundbar / home-cinema system / adapter
  // can itself contain the word "soundtouch", so these take precedence over the
  // speaker whitelist below.
  if (/\blifestyle\b/.test(m) || /\bcinemate\b/.test(m) || /\bacoustimass\b/.test(m)
      || /soundtouch\s*300\b/.test(m) || /\bsoundbar\b/.test(m)
      || /wireless\s*link\s*adapter/.test(m) || /\badapter\b/.test(m)) {
    return 'unsupported';
  }
  // Supported standalone speakers. Match the model number as a whole token so
  // "SoundTouch 30" does not also catch the "SoundTouch 300" soundbar.
  if (/soundtouch\s*(10|20|30)\b/.test(m) || /\bportable\b/.test(m) || /\bwave\b/.test(m)) {
    return 'supported';
  }
  return 'unknown';
}

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
// Default label/class of the confirm button, captured from the static
// modal HTML the first time we wire it. The modal is reused across very
// different prompts (destructive warnings AND happy-path invitations),
// so every confirmWarn call resets the button to these defaults unless
// the caller overrides them, otherwise a positive prompt's styling would
// leak into the next destructive one.
let warnConfirmDefaults = null;
function wireWarn() {
  if (warnWired) return;
  warnWired = true;
  const cancel = $('warnCancel');
  const confirm = $('warnConfirm');
  if (confirm) warnConfirmDefaults = { label: confirm.textContent, cls: confirm.className };
  if (cancel)  cancel.onclick  = () => { closeWarn(); if (warnResolve) warnResolve(false); };
  if (confirm) confirm.onclick = () => { closeWarn(); if (warnResolve) warnResolve(true); };
}

// confirmWarn(title, bodyHtml, opts?)
//   opts.icon         HTML for the title icon; pass null for no icon (use
//                     for happy-path prompts so there is no warning
//                     triangle). Defaults to the warning triangle.
//   opts.confirmLabel text for the confirm button (default: the static
//                     localized "Proceed anyway").
//   opts.confirmClass class for the confirm button (default: btn-danger).
//                     Pass 'btn btn-primary' for an encouraging CTA.
export function confirmWarn(title, bodyHtml, opts) {
  opts = opts || {};
  wireWarn();
  const m = $('warnModal');
  if (!m) return Promise.resolve(false);
  const t = m.querySelector('.warn-title');
  if (t) {
    const icon = (opts.icon === null) ? ''
      : '<span class="warn-icon">' + (opts.icon || '&#9888;') + '</span> ';
    t.innerHTML = icon + escapeHtml(title);
  }
  const confirm = $('warnConfirm');
  if (confirm && warnConfirmDefaults) {
    confirm.textContent = opts.confirmLabel || warnConfirmDefaults.label;
    confirm.className = opts.confirmClass || warnConfirmDefaults.cls;
  }
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
export function showToast(msg, ms = 2200) {
  const t = $('toast');
  if (!t) return;
  t.textContent = msg;
  t.classList.add('show');
  if (toastTimer) clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show'), ms);
}
