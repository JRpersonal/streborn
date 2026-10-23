// views/music/taxonomy.js: the radio search taxonomy (stick region, genre chips, the
// language list and its localised names).

import { boxFetch, RadioLanguages, RadioTags } from '../../api.js';
import { getLocale, t, tLookup } from '../../i18n/index.js';
import { canonGenre, GENRE_BY_COUNTRY, GENRE_CORE, translateGenre } from '../../localization.js';
import { state } from '../../state.js';
import { $, escapeAttr, escapeHtml, formatNumber } from '../../utils.js';
import { doRefilter, updateFilterIndicators } from './search.js';

// LOCALE_TO_RADIO_LANG maps the app UI locale to radio-browser's English
// language name, so the radio language filter can default to the chosen app
// language (e.g. a Dutch UI defaults the filter to Dutch stations) rather
// than to the stick region's language or a last-used value.
const LOCALE_TO_RADIO_LANG = {
  en: 'english',
  de: 'german',
  fr: 'french',
  es: 'spanish',
  ja: 'japanese',
  uk: 'ukrainian',
  nl: 'dutch',
  pl: 'polish',
  lt: 'lithuanian',
  lv: 'latvian',
  tr: 'turkish',
};

export async function loadLanguagesForCountry() {
  try {
    const cc = state.searchCountry || '';
    // App-side: no box needed; query radio-browser directly.
    state.languages = await RadioLanguages(cc, cc ? 60 : 40) || [];
    renderLanguageOptions();
  } catch {}
}

// boxFetch lives in api.js next to boxURL, imported above.

let regionLoaded = false;
export async function loadStickRegion() {
  if (regionLoaded || !state.currentBox) return;
  try {
    const r = await boxFetch(state.currentBox, '/api/region');
    if (!r.ok) return;
    const data = await r.json();
    if (data && data.country) {
      // Deliberately do NOT seed the radio-search country filter from the
      // stick region. STR is a worldwide app; the radio search defaults to
      // all countries so a German-provisioned box does not silently hide
      // every non-German station. The country filter stays at the user's
      // own choice (persisted, issue #86) or "all countries" until they
      // pick one. The region still drives only the language default below.
      //
      // Default the language filter to the APP language, not the stick
      // region's language and not a last-used value. Only when the app
      // locale has no obvious radio-browser language do we fall back to the
      // region's language.
      if (!state.searchLang) {
        state.searchLang = LOCALE_TO_RADIO_LANG[getLocale()] || data.language || '';
      }
      updateFilterIndicators();
      // Re-render the language dropdown so the locale-based default is
      // injected and selected even when this resolves after loadTaxonomy
      // (the two run concurrently on box selection).
      renderLanguageOptions();
      regionLoaded = true;
    }
  } catch {}
}

// loadTaxonomy fetches the genre tag list and the language list from
// the stick once, then renders the genre chips and the language
// dropdown.
export async function loadTaxonomy() {
  // App-side: query radio-browser directly, no box needed.
  if (state.tags.length === 0) {
    try {
      state.tags = await RadioTags(24) || [];
      renderGenreChips();
    } catch {}
  }
  if (state.languages.length === 0) {
    try {
      state.languages = await RadioLanguages('', 40) || [];
      renderLanguageOptions();
    } catch {}
  }
}

// Tracks whether the user has clicked "Mehr Genres" to expand the
// long tail of auto-fetched tags. Resets on every fresh page load.
state.showMoreGenres = false;

export function renderGenreChips() {
  const wrap = $('genreChips');
  if (!wrap) return;

  // 1. Aggregate the live counts from radio-browser so each chip can
  //    show "N Sender" in its tooltip. State.tags may be empty on
  //    first paint — that's fine, core chips still render with 0.
  const liveCounts = {};
  for (const t of state.tags) {
    const canon = canonGenre(t.name);
    if (!canon) continue;
    liveCounts[canon] = (liveCounts[canon] || 0) + (t.stationcount || 0);
  }

  // 2. Country-boost pills (max 2). state.searchCountry is the user's
  //    selected country in the search filter — it falls back to the
  //    stick's region.txt if the user hasn't manually picked one.
  const cc = (state.searchCountry || '').toUpperCase();
  const boost = GENRE_BY_COUNTRY[cc] || [];

  const chipHtml = (canon, label, count, extraClass) => {
    const active = state.searchTag === canon ? ' active' : '';
    const cls = ['chip', active.trim(), extraClass || ''].filter(Boolean).join(' ');
    const title = count > 0 ? t('search.nStations', { n: formatNumber(count) }) : '';
    return `<button class="${cls}" data-tag="${escapeAttr(canon)}" title="${escapeAttr(title)}">${escapeHtml(label)}</button>`;
  };
  const labelFor = (canon) => translateGenre(canon) || canon.replace(/\b\w/g, c => c.toUpperCase());

  const seen = new Set();
  const parts = [];

  parts.push('<button class="chip' + (!state.searchTag ? ' active' : '') + '" data-tag="">' + escapeHtml(t('search.allGenres')) + '</button>');

  for (const canon of boost) {
    if (!canon || seen.has(canon)) continue;
    seen.add(canon);
    parts.push(chipHtml(canon, labelFor(canon), liveCounts[canon] || 0, 'chip--boost'));
  }
  for (const canon of GENRE_CORE) {
    if (seen.has(canon)) continue;
    seen.add(canon);
    parts.push(chipHtml(canon, labelFor(canon), liveCounts[canon] || 0));
  }

  // 3. Long tail: tags from state.tags that the user might recognise
  //    but we did not promote into the core set. Shown only when the
  //    user expands via "Mehr Genres".
  const tail = Object.keys(liveCounts)
    .filter(canon => !seen.has(canon) && liveCounts[canon] > 0)
    .map(canon => ({ canon, count: liveCounts[canon] }))
    .sort((a, b) => b.count - a.count)
    .slice(0, 24);

  if (state.showMoreGenres) {
    for (const t of tail) {
      seen.add(t.canon);
      parts.push(chipHtml(t.canon, labelFor(t.canon), t.count));
    }
  }

  // 4. Toggle button at the end. Hidden when there is no tail to
  //    reveal, or when the currently selected tag is in the tail (so
  //    the user does not lose their selection by collapsing).
  const showSelectedInTail = state.searchTag && !seen.has(state.searchTag);
  if (tail.length > 0 || state.showMoreGenres) {
    const label = state.showMoreGenres ? t('search.fewerGenres') : t('search.moreGenres', { n: tail.length });
    parts.push(`<button class="chip chip--more" id="genreMoreToggle">${escapeHtml(label)}</button>`);
  }
  if (showSelectedInTail) {
    // Selection points to a tag the tail dropdown is hiding — force
    // expand so the user sees their own filter.
    state.showMoreGenres = true;
  }

  wrap.innerHTML = parts.join('');
  wrap.querySelectorAll('.chip').forEach(btn => {
    btn.onclick = () => {
      if (btn.id === 'genreMoreToggle') {
        state.showMoreGenres = !state.showMoreGenres;
        renderGenreChips();
        return;
      }
      state.searchTag = btn.dataset.tag || '';
      renderGenreChips();
      doRefilter();
    };
  });
}

// localizeLanguageName looks up the display name for a language emitted
// by radio-browser.info. The API hands us lowercased English names
// ("german", "english", ...). The i18n bundle holds the per-locale
// translation under `lang.<name>`. Unknown languages fall back to a
// capitalised version of the raw API value.
// radio-browser hands us lowercase English language names; map the
// common ones to ISO 639 codes so Intl.DisplayNames can localize them
// into the active app language (works for every locale, no per-language
// tables).
const LANG_NAME_TO_CODE = {
  german: 'de', english: 'en', french: 'fr', spanish: 'es', italian: 'it',
  dutch: 'nl', portuguese: 'pt', russian: 'ru', polish: 'pl', turkish: 'tr',
  arabic: 'ar', japanese: 'ja', chinese: 'zh', mandarin: 'zh', cantonese: 'yue',
  swedish: 'sv', norwegian: 'nb', danish: 'da', finnish: 'fi', czech: 'cs',
  hungarian: 'hu', romanian: 'ro', greek: 'el', ukrainian: 'uk', bulgarian: 'bg',
  croatian: 'hr', serbian: 'sr', slovak: 'sk', slovenian: 'sl', estonian: 'et',
  latvian: 'lv', lithuanian: 'lt', irish: 'ga', welsh: 'cy', catalan: 'ca',
  galician: 'gl', basque: 'eu', icelandic: 'is', hindi: 'hi', thai: 'th',
  vietnamese: 'vi', korean: 'ko', indonesian: 'id', malay: 'ms', persian: 'fa',
  hebrew: 'he', bengali: 'bn', tamil: 'ta', urdu: 'ur', maltese: 'mt',
};
let _langDN = null;
let _langDNLocale = null;
// localizeLanguageName localizes a radio-browser language name to the
// active app language via Intl.DisplayNames (per-locale cached), falling
// back to the i18n lang table, then a capitalized form of the raw name.
export function localizeLanguageName(name) {
  if (!name) return '';
  // Localize to the active app language via Intl.DisplayNames, exactly like
  // regionName does for countries (the Wails webview is Chromium and honours
  // the locale argument, as the localized country dropdown proves). So a
  // French UI shows French language names, Dutch shows Dutch, and so on,
  // for every app language without hand-translated tables. Cached per locale.
  const code = LANG_NAME_TO_CODE[name.toLowerCase().trim()];
  if (code) {
    try {
      const loc = getLocale();
      if (_langDNLocale !== loc) {
        _langDN = new Intl.DisplayNames([loc], { type: 'language' });
        _langDNLocale = loc;
      }
      const n = _langDN.of(code);
      if (n && n.toLowerCase() !== code) return n;
    } catch (_) {
      // Intl unavailable / bad code: fall through to the table.
    }
  }
  // Names not in the code map (e.g. "american english"), or if Intl failed:
  // the i18n lang table (selected language for de/en, else English), then the
  // raw radio-browser name title-cased.
  const translated = tLookup('lang', name.toLowerCase().trim());
  if (translated) return translated;
  return name.replace(/\b\w/g, (c) => c.toUpperCase());
}

function renderLanguageOptions() {
  const sel = $('searchLang');
  if (!sel) return;
  const sorted = (state.languages || [])
    .filter((l) => l.name)
    .map((l) => ({ name: l.name, stationcount: l.stationcount, label: localizeLanguageName(l.name) }));
  // Ensure the language matching the app UI locale is always selectable,
  // even when radio-browser's top-N-by-count list omits it (smaller
  // languages like Lithuanian or Latvian fall outside the limit).
  // Without this the locale-based pre-selection (LOCALE_TO_RADIO_LANG)
  // cannot apply, because the matching <option> would not exist.
  const want = state.searchLang || LOCALE_TO_RADIO_LANG[getLocale()] || '';
  if (want && !sorted.some((l) => l.name.toLowerCase() === want.toLowerCase())) {
    sorted.push({ name: want, stationcount: null, label: localizeLanguageName(want) });
  }
  // Sort alphabetically by the localized display name, consistent with
  // the country dropdown. The API returns languages by station count.
  sorted.sort((a, b) => a.label.localeCompare(b.label));
  const opts = [`<option value="">${escapeHtml(t('search.allLanguages'))}</option>`];
  for (const l of sorted) {
    const count = (l.stationcount == null) ? '' : ` (${l.stationcount})`;
    opts.push(`<option value="${escapeAttr(l.name)}">${escapeHtml(l.label)}${count}</option>`);
  }
  sel.innerHTML = opts.join('');
  sel.value = state.searchLang;
}
