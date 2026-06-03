// localization.js: country code helpers, genre canonicalisation, and
// translation tables for radio-browser tags.
//
// radio-browser.info emits country and tag strings in English (or a
// techie short form). Display labels live in the i18n bundles under
// the prefixes "country.<lowercased name>" and "genre.<canonical
// tag>". This module is locale-agnostic: it canonicalises the input
// and hands the canonical key to tLookup, so a single bundle swap in
// i18n flips every country and genre label.

import { t, tLookup, getLocale } from './i18n/index.js';

// regionName localizes a country to the active app language using the
// platform's Intl.DisplayNames (the Wails webview is Chromium, which
// supports it). This localizes the country dropdown for EVERY app
// language without hand-translated tables. Falls back to the i18n
// country table, then the raw key. Cached per locale.
let _regionDN = null;
let _regionDNLocale = null;
function regionName(cc, fallbackKey) {
  if (cc) {
    try {
      const loc = getLocale();
      if (_regionDNLocale !== loc) {
        _regionDN = new Intl.DisplayNames([loc], { type: 'region' });
        _regionDNLocale = loc;
      }
      const n = _regionDN.of(cc.toUpperCase());
      if (n && n.toUpperCase() !== cc.toUpperCase()) return n;
    } catch (_) {
      // Intl.DisplayNames unavailable / bad code: fall through.
    }
  }
  return translateCountry(fallbackKey);
}

// SKIP_TAGS: tags that are meaningless / are languages / are countries
// / regions / proper nouns. Filtered out before chip rendering or
// per-station tag pills. Language and country already have dedicated
// dropdowns, so duplicating them as tags is noise.
const SKIP_TAGS = new Set([
  // generic noise
  'music', 'música', 'musica', 'musik', 'sound', 'sounds',
  'radio', 'radios', 'radyo', 'station', 'estación', 'estacion',
  'fm', 'am', 'ukw',
  'live', 'online', 'internet', 'internet radio', 'streaming',
  '24/7', '24 7', '24h', 'free', 'best', 'good', 'good music', 'best music',
  'mp3', 'aac', 'flac',
  // languages
  'español', 'spanish', 'espanol',
  'deutsch', 'german', 'germany',
  'english', 'englisch',
  'français', 'francais', 'french',
  'italiano', 'italian',
  'português', 'portuguese',
  // countries / regions / continents
  'mexico', 'méxico', 'brazil', 'brasil', 'argentina', 'chile', 'colombia',
  'peru', 'venezuela', 'usa', 'canada', 'kanada',
  'norteamérica', 'norteamerica', 'sudamérica', 'sudamerica',
  'latinoamérica', 'latinoamerica', 'latin america',
  'américa', 'america', 'europa', 'europe', 'asia', 'africa', 'oceania',
  // known junk encountered in real samples
  'moi merino',
]);

// GENRE_ALIAS: canonicalise common duplicates onto one value.
const GENRE_ALIAS = {
  'pop music': 'pop',
  'pop musik': 'pop',
  'rock music': 'rock',
  'rock musik': 'rock',
  'classic': 'classical',
  'classic music': 'classical',
  'classical music': 'classical',
  'classic rock': 'classic rock', // keep separate, do NOT collapse into rock
  'klassik': 'classical',
  'klassische musik': 'classical',
  'dance music': 'dance',
  'electronic music': 'electronic',
  'electro music': 'electro',
  '80s80s': '80s',
  '90s90s': '90s',
  '2000s': '00s',
  '2010s': '10s',
  'top40': 'top 40',
  'r&b': 'rnb',
  'r and b': 'rnb',
  'rhythm and blues': 'rnb',
  'hip-hop': 'hip hop',
  'hiphop': 'hip hop',
  'heavy-metal': 'heavy metal',
  'oeffentlich rechtlich': 'public radio',
  'oeffentlich-rechtlich': 'public radio',
  'news talk': 'news',
  'news radio': 'news',
  'nachrichten': 'news',
  'sport': 'sports',
  'electronica': 'electronic',
};

// GENRE_CORE: 14 globally relevant pills, always rendered regardless
// of how many stations the current country actually has on each tag.
// Ordered roughly by mass-appeal so the visible row reads top-down.
// Curated from radio-browser's global tag stationcount distribution
// plus Statista/Nielsen radio-format share data.
export const GENRE_CORE = [
  'pop', 'rock', 'hits', 'oldies',
  'jazz', 'classical', 'chillout',
  'dance', 'electronic', 'house',
  'hip hop', 'latin',
  '80s', '90s',
  'news',
];

// GENRE_BY_COUNTRY: bubble these tags up as the "for your country"
// row, in front of the core pills. Max 2 per country to keep the
// visual budget tight. Country code matches state.searchCountry (ISO
// 3166 alpha-2, uppercase).
export const GENRE_BY_COUNTRY = {
  'DE': ['schlager', 'deutschrap'],
  'AT': ['schlager', 'volksmusik'],
  'CH': ['schlager', 'volksmusik'],
  'US': ['country', 'rnb'],
  'CA': ['country', 'rnb'],
  'AU': ['country', 'indie'],
  'GB': ['indie', 'rnb'],
  'IE': ['folk', 'indie'],
  'FR': ['chanson', 'variété'],
  'IT': ['italo', 'italian'],
  'ES': ['reggaeton', 'salsa'],
  'PT': ['fado', 'pimba'],
  'MX': ['reggaeton', 'salsa'],
  'AR': ['reggaeton', 'salsa'],
  'CO': ['reggaeton', 'salsa'],
  'CL': ['reggaeton', 'salsa'],
  'PE': ['reggaeton', 'salsa'],
  'VE': ['reggaeton', 'salsa'],
  'BR': ['sertanejo', 'samba'],
  'JP': ['j-pop', 'anime'],
  'KR': ['k-pop', 'kpop'],
  'IN': ['bollywood', 'bhangra'],
  'NL': ['levenslied', 'nederlandstalig'],
  'BE': ['chanson', 'levenslied'],
  'TR': ['turkish', 'halk'],
  'PL': ['disco polo', 'polski'],
  'RU': ['russian', 'retro'],
  'UA': ['ukrainian', 'retro'],
  'GR': ['greek', 'laika'],
  'SE': ['svensk', 'nordic'],
  'NO': ['norsk', 'nordic'],
  'DK': ['dansk', 'nordic'],
  'FI': ['suomi', 'nordic'],
};

// translateCountry looks up the display name for a country emitted by
// radio-browser. The input arrives in mixed case English; the bundle
// keys are lowercased English. Unknown countries return the raw input.
export function translateCountry(name) {
  if (!name) return '';
  return tLookup('country', name) || name;
}

// canonGenre canonicalises a tag for filtering. Returns empty string
// if the tag is in SKIP_TAGS (i.e. should not be displayed at all).
export function canonGenre(t) {
  const key = (t || '').toLowerCase().trim();
  if (!key) return '';
  if (SKIP_TAGS.has(key)) return '';
  return GENRE_ALIAS[key] || key;
}

// translateGenre canonicalises a tag and translates it for display.
export function translateGenre(tag) {
  const key = canonGenre(tag);
  if (!key) return '';
  const localized = tLookup('genre', key);
  if (localized) return localized;
  // Acronyms (BBC, WDR, ORF, ...) stay verbatim.
  if (/^[A-Z0-9]{2,5}$/.test(tag)) return tag;
  return key.replace(/\b\w/g, c => c.toUpperCase());
}

export function translateTags(tagsCsv) {
  if (!tagsCsv) return [];
  const seen = new Set();
  const out = [];
  for (const raw of tagsCsv.split(',')) {
    const tag = translateGenre(raw);
    if (tag && !seen.has(tag.toLowerCase())) {
      seen.add(tag.toLowerCase());
      out.push(tag);
    }
  }
  return out;
}

// ---------- Country code → flag emoji ----------
// ISO 3166-1 alpha-2 to regional indicator symbol (Unicode flag).
// Windows system fonts ship no country-flag emoji (regional indicators
// render blank/letters), and a native <select> cannot host the inline
// SVG flags used in the header. So on Windows the country dropdowns show
// names without a flag rather than a broken glyph.
export const IS_WINDOWS = typeof navigator !== 'undefined' && /Windows/i.test(navigator.userAgent || '');

// optFlag returns the emoji flag plus a trailing space for use in a
// <select> <option>, or '' on Windows (where it would render blank).
export function optFlag(cc) {
  if (IS_WINDOWS) return '';
  const f = flagFromCC(cc);
  return f ? f + ' ' : '';
}

export function flagFromCC(cc) {
  if (!cc || cc.length !== 2) return '';
  const A = 0x1F1E6;
  const c0 = cc.toUpperCase().charCodeAt(0) - 65;
  const c1 = cc.toUpperCase().charCodeAt(1) - 65;
  if (c0 < 0 || c0 > 25 || c1 < 0 || c1 > 25) return '';
  return String.fromCodePoint(A + c0) + String.fromCodePoint(A + c1);
}

// flagSvg returns an inline SVG flag for the small, fixed set of UI
// locales (the language picker), or '' for anything else. Unicode flag
// emoji do NOT render on Windows: Microsoft's "Segoe UI Emoji" ships no
// regional-indicator (country flag) glyphs by design, and no other
// Windows system font does either, so flagFromCC shows two letters on
// Windows while macOS (Apple Color Emoji) shows a real flag. Inline SVG
// renders identically on every platform. Only the picker's handful of
// flags use this; the long country <select> dropdowns keep emoji
// (native <option> cannot host inline SVG anyway).
const LOCALE_FLAG_SVG = {
  GB: '<svg class="loc-flag-svg" viewBox="0 0 60 30" width="22" height="13" aria-hidden="true"><rect width="60" height="30" fill="#012169"/><path d="M0,0 L60,30 M60,0 L0,30" stroke="#fff" stroke-width="6"/><path d="M0,0 L60,30 M60,0 L0,30" stroke="#C8102E" stroke-width="3"/><rect x="25" width="10" height="30" fill="#fff"/><rect y="10" width="60" height="10" fill="#fff"/><rect x="27" width="6" height="30" fill="#C8102E"/><rect y="12" width="60" height="6" fill="#C8102E"/></svg>',
  DE: '<svg class="loc-flag-svg" viewBox="0 0 5 3" width="21" height="13" aria-hidden="true"><rect width="5" height="3" fill="#000"/><rect y="1" width="5" height="1" fill="#D00"/><rect y="2" width="5" height="1" fill="#FFCE00"/></svg>',
  FR: '<svg class="loc-flag-svg" viewBox="0 0 3 2" width="20" height="13" aria-hidden="true"><rect width="3" height="2" fill="#fff"/><rect width="1" height="2" fill="#0055A4"/><rect x="2" width="1" height="2" fill="#EF4135"/></svg>',
  ES: '<svg class="loc-flag-svg" viewBox="0 0 3 2" width="20" height="13" aria-hidden="true"><rect width="3" height="2" fill="#AA151B"/><rect y="0.5" width="3" height="1" fill="#F1BF00"/></svg>',
  JP: '<svg class="loc-flag-svg" viewBox="0 0 3 2" width="20" height="13" aria-hidden="true"><rect width="3" height="2" fill="#fff"/><circle cx="1.5" cy="1" r="0.6" fill="#BC002D"/></svg>',
  UA: '<svg class="loc-flag-svg" viewBox="0 0 3 2" width="20" height="13" aria-hidden="true"><rect width="3" height="2" fill="#FFD700"/><rect width="3" height="1" fill="#0057B7"/></svg>',
  NL: '<svg class="loc-flag-svg" viewBox="0 0 3 2" width="20" height="13" aria-hidden="true"><rect width="3" height="2" fill="#21468B"/><rect width="3" height="1.333" fill="#fff"/><rect width="3" height="0.667" fill="#AE1C28"/></svg>',
};
export function flagSvg(cc) {
  if (!cc) return '';
  return LOCALE_FLAG_SVG[cc.toUpperCase()] || '';
}

// Country dropdown source. Each entry has a stable English lookup key
// (used for tLookup) and the ISO code; the display name comes from
// the active locale at render time so a language switch reflects
// immediately.
const COUNTRIES_RAW = [
  { cc: 'DE', key: 'germany' },
  { cc: 'AT', key: 'austria' },
  { cc: 'CH', key: 'switzerland' },
  { cc: 'NL', key: 'netherlands' },
  { cc: 'BE', key: 'belgium' },
  { cc: 'LU', key: 'luxembourg' },
  { cc: 'FR', key: 'france' },
  { cc: 'IT', key: 'italy' },
  { cc: 'ES', key: 'spain' },
  { cc: 'PT', key: 'portugal' },
  { cc: 'GB', key: 'united kingdom' },
  { cc: 'IE', key: 'ireland' },
  { cc: 'DK', key: 'denmark' },
  { cc: 'SE', key: 'sweden' },
  { cc: 'NO', key: 'norway' },
  { cc: 'FI', key: 'finland' },
  { cc: 'IS', key: 'iceland' },
  { cc: 'PL', key: 'poland' },
  { cc: 'CZ', key: 'czech republic' },
  { cc: 'SK', key: 'slovakia' },
  { cc: 'HU', key: 'hungary' },
  { cc: 'RO', key: 'romania' },
  { cc: 'BG', key: 'bulgaria' },
  { cc: 'GR', key: 'greece' },
  { cc: 'HR', key: 'croatia' },
  { cc: 'SI', key: 'slovenia' },
  { cc: 'TR', key: 'turkey' },
  { cc: 'RU', key: 'russia' },
  { cc: 'UA', key: 'ukraine' },
  { cc: 'US', key: 'united states' },
  { cc: 'CA', key: 'canada' },
  { cc: 'MX', key: 'mexico' },
  { cc: 'BR', key: 'brazil' },
  { cc: 'AR', key: 'argentina' },
  { cc: 'CL', key: 'chile' },
  { cc: 'CO', key: 'colombia' },
  { cc: 'PE', key: 'peru' },
  { cc: 'JP', key: 'japan' },
  { cc: 'CN', key: 'china' },
  { cc: 'TW', key: 'taiwan' },
  { cc: 'HK', key: 'hong kong' },
  { cc: 'KR', key: 'south korea' },
  { cc: 'IN', key: 'india' },
  { cc: 'TH', key: 'thailand' },
  { cc: 'VN', key: 'vietnam' },
  { cc: 'ID', key: 'indonesia' },
  { cc: 'PH', key: 'philippines' },
  { cc: 'MY', key: 'malaysia' },
  { cc: 'SG', key: 'singapore' },
  { cc: 'IL', key: 'israel' },
  { cc: 'AE', key: 'united arab emirates' },
  { cc: 'SA', key: 'saudi arabia' },
  { cc: 'EG', key: 'egypt' },
  { cc: 'MA', key: 'morocco' },
  { cc: 'AU', key: 'australia' },
  { cc: 'NZ', key: 'new zealand' },
  { cc: 'ZA', key: 'south africa' },
];

// COUNTRIES_TOP pin to the head of the dropdown. STR has a meaningful
// DACH user base, so DE/AT/CH always come first; everything else is
// sorted alphabetically by display name in the active locale.
const COUNTRIES_TOP = ['DE', 'AT', 'CH'];

// getCountries returns the dropdown content for the active locale.
// Recomputed on every call so a locale switch reflects immediately.
export function getCountries() {
  const enriched = COUNTRIES_RAW.map(c => ({ cc: c.cc, name: regionName(c.cc, c.key) }));
  const top = COUNTRIES_TOP
    .map(cc => enriched.find(c => c.cc === cc))
    .filter(Boolean);
  const rest = enriched
    .filter(c => !COUNTRIES_TOP.includes(c.cc))
    .sort((a, b) => a.name.localeCompare(b.name));
  return [{ cc: '', name: t('common.allCountries') }, ...top, ...rest];
}

// COUNTRIES is the canonical export consumed by main.js. Stays a
// getter-style array so call sites don't need to know it's recomputed.
// Snapshot once at module load: main.js currently reads it eagerly
// for dropdown render; locale switches trigger a full reload, so the
// stale snapshot is replaced on the next page lifecycle.
export const COUNTRIES = getCountries();

export const ORDERS = [
  { v: 'votes',      label: t('order.votes') },
  { v: 'clickcount', label: t('order.clickcount') },
  { v: 'clicktrend', label: t('order.clicktrend') },
  { v: 'name',       label: t('order.name') },
];
