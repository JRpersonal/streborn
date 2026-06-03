// i18n: minimal vanilla translation layer. No external library.
//
// Bundles are plain JSON keyed by flat dotted paths (e.g.
// "settings.title"). Lookup falls back to English if the active
// locale lacks a key, and finally to the raw key so missing strings
// are visible during development.
//
// Active locale is persisted in localStorage under "locale". On first
// load it is derived from navigator.language with a hard fallback to
// English: the app ships worldwide, English is the safe default.
//
// Adding a new language: drop a bundle file under bundles/, register
// it in BUNDLES below. Bundles are statically imported so Vite ships
// them as part of the main chunk; for the 50 to 100 strings STR has,
// the size cost is negligible compared to lazy-loading complexity.

import en from './bundles/en.json';
import de from './bundles/de.json';
import fr from './bundles/fr.json';
import es from './bundles/es.json';
import ja from './bundles/ja.json';
import uk from './bundles/uk.json';
import nl from './bundles/nl.json';

// Order here is the order the language picker renders. Each bundle
// covers the full UI chrome; missing keys fall back to English. The
// large country/genre/lang reference tables are not duplicated into
// fr/es/ja/uk, so those entries fall back to English via tLookup (radio
// metadata only). Note: Ukrainian is the app UI language; the Bose box
// sysLanguage enum has no Ukrainian, so a UA box display falls back to
// English (deliberately NOT Russian). See project_bose_language_enum.
const BUNDLES = { en, de, fr, es, ja, uk, nl };

export const AVAILABLE_LOCALES = Object.freeze(
  Object.keys(BUNDLES).map((code) => ({
    code,
    label: BUNDLES[code]['locale.label'] || code,
  })),
);

const LS_KEY = 'locale';
const FALLBACK = 'en';

function detectInitialLocale() {
  try {
    const stored = localStorage.getItem(LS_KEY);
    if (stored && BUNDLES[stored]) return stored;
  } catch (_) {
    // localStorage may be unavailable (private mode, sandbox); fall
    // through to navigator detection.
  }
  const nav = (typeof navigator !== 'undefined' && navigator.language) || '';
  const short = nav.toLowerCase().split('-')[0];
  if (short && BUNDLES[short]) return short;
  return FALLBACK;
}

let currentLocale = detectInitialLocale();
const listeners = new Set();

export function getLocale() {
  return currentLocale;
}

export function setLocale(code) {
  if (!BUNDLES[code]) return false;
  if (code === currentLocale) return true;
  currentLocale = code;
  try {
    localStorage.setItem(LS_KEY, code);
  } catch (_) {
    // ignore
  }
  for (const fn of listeners) {
    try {
      fn(code);
    } catch (_) {
      // listener errors must not break locale switching
    }
  }
  return true;
}

// onLocaleChange registers a callback fired after setLocale succeeds.
// Returns an unsubscribe function. View modules use it to rerender
// when the user picks a different language.
export function onLocaleChange(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

// t(key, params) returns the translated string. params are interpolated
// as {{name}} placeholders. Missing keys fall back through English to
// the raw key so they show up obviously in the UI.
export function t(key, params) {
  let v = BUNDLES[currentLocale] && BUNDLES[currentLocale][key];
  if (v == null) v = BUNDLES[FALLBACK] && BUNDLES[FALLBACK][key];
  if (v == null) return key;
  if (params && typeof v === 'string') {
    return v.replace(/\{\{(\w+)\}\}/g, (_, name) =>
      params[name] != null ? String(params[name]) : '',
    );
  }
  return v;
}

// tLookup returns a nested table from the active bundle, e.g.
// tLookup('country', 'germany') -> "Deutschland" or "Germany".
// Tables live under prefixed keys (country.germany, genre.rock, ...).
// Missing entries fall back to English then return undefined so callers
// can decide on their own fallback (e.g. the raw radio-browser tag).
export function tLookup(prefix, key) {
  if (!key) return undefined;
  const k = `${prefix}.${String(key).toLowerCase()}`;
  let v = BUNDLES[currentLocale] && BUNDLES[currentLocale][k];
  if (v == null) v = BUNDLES[FALLBACK] && BUNDLES[FALLBACK][k];
  return v;
}
