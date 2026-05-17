// localization.js — country code/name/flag handling plus genre and
// tag translation tables.
//
// radio-browser.info emits country and tag strings in English (or a
// techie short form). This module bundles all translation tables and
// helpers so view modules only call simple t-like functions instead
// of juggling dicts. Phase B (i18n) will swap the hardcoded German
// fallbacks for locale-aware lookups; for now German is the only
// non-English target and the canonical tag is the same as the
// English tag the radio-browser API hands us.
//
// User-facing German strings inside the data tables (country names,
// order labels, "alle Laender") are intentionally left as-is at this
// stage — Phase B will move them into the i18n bundle.

const COUNTRY_DE = {
  'germany': 'Deutschland',
  'austria': 'Oesterreich',
  'switzerland': 'Schweiz',
  'netherlands': 'Niederlande',
  'belgium': 'Belgien',
  'france': 'Frankreich',
  'italy': 'Italien',
  'spain': 'Spanien',
  'portugal': 'Portugal',
  'united kingdom': 'Vereinigtes Koenigreich',
  'ireland': 'Irland',
  'denmark': 'Daenemark',
  'sweden': 'Schweden',
  'norway': 'Norwegen',
  'finland': 'Finnland',
  'iceland': 'Island',
  'poland': 'Polen',
  'czech republic': 'Tschechien',
  'czechia': 'Tschechien',
  'slovakia': 'Slowakei',
  'hungary': 'Ungarn',
  'romania': 'Rumaenien',
  'bulgaria': 'Bulgarien',
  'greece': 'Griechenland',
  'turkey': 'Tuerkei',
  'russia': 'Russland',
  'ukraine': 'Ukraine',
  'united states of america': 'USA',
  'united states': 'USA',
  'canada': 'Kanada',
  'mexico': 'Mexiko',
  'brazil': 'Brasilien',
  'argentina': 'Argentinien',
  'australia': 'Australien',
  'new zealand': 'Neuseeland',
  'japan': 'Japan',
  'china': 'China',
  'india': 'Indien',
  'south korea': 'Suedkorea',
  'the united states of america': 'USA',
};

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

// German display labels for canonical genre tags. The English/canonical
// tag is the key; Phase B will replace this map with a per-locale
// lookup driven by the active locale.
const GENRE_DE = {
  'rock': 'Rock', 'pop': 'Pop', 'jazz': 'Jazz', 'classical': 'Klassik',
  'classic': 'Klassik', 'klassik': 'Klassik',
  'news': 'Nachrichten', 'talk': 'Talk', 'sport': 'Sport', 'sports': 'Sport',
  'oldies': 'Oldies', 'hits': 'Hits',
  '80s': '80er', '90s': '90er', '70s': '70er', '60s': '60er', '50s': '50er',
  '80s80s': '80er', '90s90s': '90er',
  'metal': 'Metal', 'heavy metal': 'Heavy Metal', 'death metal': 'Death Metal',
  'punk': 'Punk', 'indie': 'Indie', 'alternative': 'Alternative',
  'electronic': 'Elektronisch', 'electro': 'Elektro', 'techno': 'Techno',
  'house': 'House', 'trance': 'Trance', 'edm': 'EDM',
  'hip hop': 'Hip Hop', 'hip-hop': 'Hip Hop', 'rap': 'Rap',
  'rnb': 'RnB', 'r&b': 'R&B', 'soul': 'Soul', 'funk': 'Funk', 'disco': 'Disco',
  'reggae': 'Reggae', 'ska': 'Ska', 'blues': 'Blues', 'country': 'Country',
  'folk': 'Folk', 'volksmusik': 'Volksmusik', 'schlager': 'Schlager',
  'chillout': 'Chill', 'chill': 'Chill', 'lounge': 'Lounge',
  'ambient': 'Ambient', 'dance': 'Dance',
  'public radio': 'Oeffentlich Rechtlich', 'public': 'Oeffentlich Rechtlich',
  'ard': 'ARD', 'wdr': 'WDR', 'ndr': 'NDR', 'mdr': 'MDR', 'rbb': 'RBB',
  'swr': 'SWR', 'br': 'BR', 'hr': 'HR', 'orf': 'ORF', 'srf': 'SRF', 'bbc': 'BBC',
  'top 40': 'Top 40', 'charts': 'Charts',
  'christian': 'Christlich', 'religious': 'Religioes', 'gospel': 'Gospel',
  'culture': 'Kultur', 'comedy': 'Comedy', 'kids': 'Kinder', 'children': 'Kinder',
  'german': 'Deutsch', 'english': 'Englisch',
  'world music': 'Weltmusik', 'world': 'Welt',
  'instrumental': 'Instrumental', 'orchestra': 'Orchester',
  'movie': 'Film', 'soundtrack': 'Soundtrack',
  'news talk': 'Nachrichten Talk', 'news radio': 'Nachrichten',
  'easy listening': 'Easy Listening',
  'live': 'Live', 'local': 'Lokal',
  'variety': 'Vielfalt', 'mix': 'Mix',
  'eurodance': 'Eurodance', 'eurodisco': 'Eurodisco',
  'entretenimiento': 'Unterhaltung', 'entertainment': 'Unterhaltung',
  'sports': 'Sport', 'sport': 'Sport',
  'family': 'Familie', 'kinder': 'Kinder',
  'evergreen': 'Evergreens', 'evergreens': 'Evergreens',
  'love songs': 'Liebeslieder', 'romantic': 'Romantik',
  'party': 'Party', 'dj': 'DJ', 'mixtape': 'Mixtape',
  'workout': 'Workout', 'fitness': 'Fitness',
  'meditation': 'Meditation', 'relax': 'Entspannung', 'relaxation': 'Entspannung',
  'piano': 'Klavier', 'guitar': 'Gitarre',
  'opera': 'Oper', 'musical': 'Musical',
  'singer-songwriter': 'Singer Songwriter', 'singer songwriter': 'Singer Songwriter',
  'experimental': 'Experimentell', 'underground': 'Underground',
  'drum and bass': 'Drum & Bass', 'dnb': 'Drum & Bass', 'd&b': 'Drum & Bass',
  'minimal': 'Minimal', 'dubstep': 'Dubstep',
  'pop rock': 'Pop Rock', 'hard rock': 'Hard Rock', 'soft rock': 'Soft Rock',
  'classic rock': 'Classic Rock', 'indie rock': 'Indie Rock', 'alternative rock': 'Alternative Rock',
  // Country-boost labels (kept in English where the genre name itself
  // is a proper noun the audience would recognise everywhere).
  'country': 'Country', 'deutschrap': 'Deutschrap', 'latin': 'Latin',
  'reggaeton': 'Reggaeton', 'salsa': 'Salsa', 'samba': 'Samba',
  'sertanejo': 'Sertanejo', 'bossa nova': 'Bossa Nova',
  'j-pop': 'J-Pop', 'jpop': 'J-Pop', 'anime': 'Anime',
  'bollywood': 'Bollywood', 'bhangra': 'Bhangra', 'hindi': 'Hindi',
  'chanson': 'Chanson', 'variété': 'Variété', 'variete': 'Variété',
  'italo': 'Italo', 'italian': 'Italienisch', 'italiano': 'Italienisch',
  'levenslied': 'Levenslied', 'nederlandstalig': 'Niederländisch',
  'turkish': 'Türkisch', 'tuerkisch': 'Türkisch', 'halk': 'Halk',
  'disco polo': 'Disco Polo', 'polski': 'Polnisch',
  'russian': 'Russisch', 'retro': 'Retro',
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

export function translateCountry(name) {
  if (!name) return '';
  const key = name.toLowerCase().trim();
  return COUNTRY_DE[key] || name;
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
export function translateGenre(t) {
  const key = canonGenre(t);
  if (!key) return '';
  if (GENRE_DE[key]) return GENRE_DE[key];
  if (/^[A-Z0-9]{2,5}$/.test(t)) return t;
  return key.replace(/\b\w/g, c => c.toUpperCase());
}

export function translateTags(tagsCsv) {
  if (!tagsCsv) return [];
  const seen = new Set();
  const out = [];
  for (const raw of tagsCsv.split(',')) {
    const t = translateGenre(raw);
    if (t && !seen.has(t.toLowerCase())) {
      seen.add(t.toLowerCase());
      out.push(t);
    }
  }
  return out;
}

// ---------- Country code → flag emoji ----------
// ISO 3166-1 alpha-2 to regional indicator symbol (Unicode flag).
export function flagFromCC(cc) {
  if (!cc || cc.length !== 2) return '';
  const A = 0x1F1E6;
  const c0 = cc.toUpperCase().charCodeAt(0) - 65;
  const c1 = cc.toUpperCase().charCodeAt(1) - 65;
  if (c0 < 0 || c0 > 25 || c1 < 0 || c1 > 25) return '';
  return String.fromCodePoint(A + c0) + String.fromCodePoint(A + c1);
}

// Country list for the filter dropdown. Only the most relevant
// countries are listed by default — the user can always pick "all
// countries" to see the global pool.
// All supported countries, unsorted. Sorting happens below
// (alphabetical by translated name, DACH pinned to the top for the
// default user).
const COUNTRIES_ALL = [
  { cc: 'DE', name: 'Deutschland' },
  { cc: 'AT', name: 'Oesterreich' },
  { cc: 'CH', name: 'Schweiz' },
  { cc: 'NL', name: 'Niederlande' },
  { cc: 'BE', name: 'Belgien' },
  { cc: 'LU', name: 'Luxemburg' },
  { cc: 'FR', name: 'Frankreich' },
  { cc: 'IT', name: 'Italien' },
  { cc: 'ES', name: 'Spanien' },
  { cc: 'PT', name: 'Portugal' },
  { cc: 'GB', name: 'Vereinigtes Koenigreich' },
  { cc: 'IE', name: 'Irland' },
  { cc: 'DK', name: 'Daenemark' },
  { cc: 'SE', name: 'Schweden' },
  { cc: 'NO', name: 'Norwegen' },
  { cc: 'FI', name: 'Finnland' },
  { cc: 'IS', name: 'Island' },
  { cc: 'PL', name: 'Polen' },
  { cc: 'CZ', name: 'Tschechien' },
  { cc: 'SK', name: 'Slowakei' },
  { cc: 'HU', name: 'Ungarn' },
  { cc: 'RO', name: 'Rumaenien' },
  { cc: 'BG', name: 'Bulgarien' },
  { cc: 'GR', name: 'Griechenland' },
  { cc: 'HR', name: 'Kroatien' },
  { cc: 'SI', name: 'Slowenien' },
  { cc: 'TR', name: 'Tuerkei' },
  { cc: 'RU', name: 'Russland' },
  { cc: 'UA', name: 'Ukraine' },
  { cc: 'US', name: 'USA' },
  { cc: 'CA', name: 'Kanada' },
  { cc: 'MX', name: 'Mexiko' },
  { cc: 'BR', name: 'Brasilien' },
  { cc: 'AR', name: 'Argentinien' },
  { cc: 'CL', name: 'Chile' },
  { cc: 'CO', name: 'Kolumbien' },
  { cc: 'PE', name: 'Peru' },
  { cc: 'JP', name: 'Japan' },
  { cc: 'CN', name: 'China' },
  { cc: 'TW', name: 'Taiwan' },
  { cc: 'HK', name: 'Hongkong' },
  { cc: 'KR', name: 'Suedkorea' },
  { cc: 'IN', name: 'Indien' },
  { cc: 'TH', name: 'Thailand' },
  { cc: 'VN', name: 'Vietnam' },
  { cc: 'ID', name: 'Indonesien' },
  { cc: 'PH', name: 'Philippinen' },
  { cc: 'MY', name: 'Malaysia' },
  { cc: 'SG', name: 'Singapur' },
  { cc: 'IL', name: 'Israel' },
  { cc: 'AE', name: 'Vereinigte Arabische Emirate' },
  { cc: 'SA', name: 'Saudi Arabien' },
  { cc: 'EG', name: 'Aegypten' },
  { cc: 'MA', name: 'Marokko' },
  { cc: 'AU', name: 'Australien' },
  { cc: 'NZ', name: 'Neuseeland' },
  { cc: 'ZA', name: 'Suedafrika' },
];

const COUNTRIES_TOP = ['DE', 'AT', 'CH'];

export const COUNTRIES = (() => {
  const top = COUNTRIES_TOP
    .map(cc => COUNTRIES_ALL.find(c => c.cc === cc))
    .filter(Boolean);
  const rest = COUNTRIES_ALL
    .filter(c => !COUNTRIES_TOP.includes(c.cc))
    .sort((a, b) => a.name.localeCompare(b.name, 'de'));
  return [{ cc: '', name: 'alle Laender' }, ...top, ...rest];
})();

export const ORDERS = [
  { v: 'votes',      label: 'Beliebtheit' },
  { v: 'clickcount', label: 'Hoererzahlen' },
  { v: 'clicktrend', label: 'Trend' },
  { v: 'name',       label: 'Name' },
];
