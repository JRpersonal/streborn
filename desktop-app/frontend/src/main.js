import './style.css';
import {
  DiscoverBoxes,
  GetPresets,
  SetPreset,
  DeletePreset,
  PlaySlot,
  PlayURL,
  VoteStation,
  RebootBox,
  SyncBoxPresets,
  Pause,
  Stop,
  Status,
  ListDrives,
  WriteStickFiles,
  FormatStick,
  StickVersion,
  StickConfigs,
  AppInfo,
  EjectDrive,
  BoxAgentVersion,
  UpdateBoxAgent,
  WriteWLANConfig,
  WriteRegionConfig,
  WriteNameConfig,
  ListWiFiProfiles,
  TryWiFiPassword,
  CurrentWiFi,
  CheckAppUpdate,
  BoxSettings,
  SetBoxName,
  SetBoxVolume,
  SetBoxBass,
  SelectBoxSource,
} from '../wailsjs/go/main/App';
import {BrowserOpenURL} from '../wailsjs/runtime/runtime';

// ---------- State ----------

const state = {
  view: 'box',
  boxes: [],
  currentBox: null,
  settingsBox: null,   // Box deren Einstellungen wir gerade bearbeiten
  presets: [],
  searchResults: [],
  drives: [],
  selectedDrive: null,
  appInfo: null,
  nowLocation: '',     // aktueller stream url aus now_playing
  nowName: '',         // aktueller itemName aus now_playing
  nowPlayState: '',    // aktueller PlayState
  nowIcon: '',         // letztes bekanntes Sender Logo (favicon)
  nowUUID: '',         // letzte radio-browser UUID
  optimisticUntil: 0,  // Zeitpunkt ab dem refreshStatus wieder die Box als Quelle der Wahrheit nimmt
  presetErrors: {},    // slot → letzte Fehlermeldung (rot anzeigen)
  searchOrder: 'votes',
  searchCountry: 'DE',
  searchLang: '',
  searchTag: '',       // aktive Genre Chip
  searchOnlyOK: true,
  searchOnlyBose: true,
  searchOffset: 0,
  searchLastMode: 'top', // "top" oder "search" — fuer "mehr laden"
  searchLastQuery: '',
  tags: [],            // Cache der Top Tags fuer Chips
  languages: [],       // Cache der Sprachen
  // Pending Names: Box ID -> { name, until } — nach lokaler Umbenennung
  // ueberschreiben wir damit jeden mDNS Eintrag fuer max 90s. Genug bis
  // der Stick seinen TXT Record neu announciert hat.
  pendingNames: {},
};

// Persistente Selection: deviceID aus localStorage, reload nach Refresh.
function loadLastBox() {
  try { return localStorage.getItem('lastBoxDeviceID') || null; } catch { return null; }
}
function saveLastBox(id) {
  try { localStorage.setItem('lastBoxDeviceID', id || ''); } catch {}
}

// Persistenter Cache der zuletzt gesehenen Boxen Liste. Damit kann die
// App beim Start oder Tab Wechsel sofort die Box anzeigen ohne 4
// Sekunden auf mDNS zu warten. Im Hintergrund refresht discoverBoxes
// und ueberschreibt.
function loadCachedBoxes() {
  try {
    const raw = localStorage.getItem('cachedBoxes');
    if (!raw) return [];
    const arr = JSON.parse(raw);
    if (!Array.isArray(arr)) return [];
    return arr;
  } catch { return []; }
}
function saveCachedBoxes(list) {
  try { localStorage.setItem('cachedBoxes', JSON.stringify(list || [])); } catch {}
}

// ---------- Lokalisierung Land + Genres ----------
// radio-browser liefert country + tags auf Englisch / techsprech. Wir
// uebersetzen die haeufigen Werte ins Deutsche und kapitalisieren den
// Rest automatisch. Sender Namen bleiben unangetastet.

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

// SKIP_TAGS: aussagelos / Sprachen / Laender / Regionen / Eigennamen.
// Werden weder als Chip noch als Tag Pill am Sender angezeigt. Sprache
// und Land sind in den dedizierten Dropdowns abgedeckt.
const SKIP_TAGS = new Set([
  // generisch
  'music', 'música', 'musica', 'musik', 'sound', 'sounds',
  'radio', 'radios', 'radyo', 'station', 'estación', 'estacion',
  'fm', 'am', 'ukw',
  'live', 'online', 'internet', 'internet radio', 'streaming',
  '24/7', '24 7', '24h', 'free', 'best', 'good', 'good music', 'best music',
  'mp3', 'aac', 'flac',
  // Sprachen
  'español', 'spanish', 'espanol',
  'deutsch', 'german', 'germany',
  'english', 'englisch',
  'français', 'francais', 'french',
  'italiano', 'italian',
  'português', 'portuguese',
  // Laender / Regionen / Kontinente
  'mexico', 'méxico', 'brazil', 'brasil', 'argentina', 'chile', 'colombia',
  'peru', 'venezuela', 'usa', 'canada', 'kanada',
  'norteamérica', 'norteamerica', 'sudamérica', 'sudamerica',
  'latinoamérica', 'latinoamerica', 'latin america',
  'américa', 'america', 'europa', 'europe', 'asia', 'africa', 'oceania',
  // bekannter Muell aus den Beispielen
  'moi merino',
]);

// GENRE_ALIAS: kanonisiere haeufige Doubletten auf einen Wert.
const GENRE_ALIAS = {
  'pop music': 'pop',
  'pop musik': 'pop',
  'rock music': 'rock',
  'rock musik': 'rock',
  'classic': 'classical',
  'classic music': 'classical',
  'classical music': 'classical',
  'classic rock': 'classic rock', // bleibt eigen, nicht zu rock kollabieren
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
};

function translateCountry(name) {
  if (!name) return '';
  const key = name.toLowerCase().trim();
  return COUNTRY_DE[key] || name;
}

// canonGenre kanonisiert einen Tag fuer Filter Zwecke. Liefert leeren
// String falls der Tag in SKIP_TAGS ist (soll ausgeblendet werden).
function canonGenre(t) {
  const key = (t || '').toLowerCase().trim();
  if (!key) return '';
  if (SKIP_TAGS.has(key)) return '';
  return GENRE_ALIAS[key] || key;
}

// translateGenre kanonisiert und uebersetzt einen Tag fuer die Anzeige.
function translateGenre(t) {
  const key = canonGenre(t);
  if (!key) return '';
  if (GENRE_DE[key]) return GENRE_DE[key];
  if (/^[A-Z0-9]{2,5}$/.test(t)) return t;
  return key.replace(/\b\w/g, c => c.toUpperCase());
}

function translateTags(tagsCsv) {
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

function formatNumber(n) {
  if (!n || isNaN(n)) return '0';
  return Number(n).toLocaleString('de-DE');
}

// ---------- Logo / Icon Resolution ----------
// Viele radio-browser Eintraege haben kein favicon gesetzt. Wenn das
// passiert, leiten wir aus homepage / stream URL die Domain ab und
// nutzen den DuckDuckGo Icon Service als Fallback. Bei Fehler erneut
// kaskadieren via onerror.

function extractHost(u) {
  if (!u) return '';
  try { return new URL(u).hostname; } catch { return ''; }
}

// rootDomain entfernt die erste Subdomain. "stream.rockland-digital.de"
// → "rockland-digital.de", "icecast.wdr.de" → "wdr.de". So treffen die
// Favicon Services oft auch wenn der Streaming Host kein Logo hat.
function rootDomain(host) {
  if (!host) return '';
  const parts = host.split('.');
  if (parts.length < 3) return host;
  return parts.slice(1).join('.');
}

// iconServicesFor liefert mehrere Favicon Service URLs fuer einen Host.
// DDG ist Hauptquelle, Google als Backup (decken meist verschiedene
// Domains ab).
function iconServicesFor(host) {
  if (!host) return [];
  return [
    `https://icons.duckduckgo.com/ip3/${host}.ico`,
    `https://www.google.com/s2/favicons?domain=${encodeURIComponent(host)}&sz=64`,
  ];
}

function stationLogoCandidates(s) {
  const out = [];
  if (s.favicon) out.push(s.favicon);
  const hosts = [];
  for (const u of [s.homepage, s.url, s.url_resolved]) {
    const h = extractHost(u);
    if (h && !hosts.includes(h)) hosts.push(h);
    const r = rootDomain(h);
    if (r && !hosts.includes(r)) hosts.push(r);
  }
  for (const h of hosts) {
    for (const svc of iconServicesFor(h)) {
      if (!out.includes(svc)) out.push(svc);
    }
  }
  return out;
}

// onerrorChain feuert den naechsten Kandidaten im data-attribute. Wenn
// keine mehr, blendet das img aus (CSS zeigt dann den Placeholder).
function logoImgTag(s, cssClass) {
  const candidates = stationLogoCandidates(s);
  if (candidates.length === 0) return '<div class="fav-empty"></div>';
  const first = candidates[0];
  const rest = candidates.slice(1).join('|');
  return `<img class="${cssClass}"
            src="${escapeAttr(first)}"
            data-fallbacks="${escapeAttr(rest)}"
            onerror="window.__nextLogoFallback(this)"/>`;
}

// Global helper das vom onerror Handler gerufen wird. Cycled durch
// data-fallbacks bis eines laedt oder das Element ausgeblendet ist.
window.__nextLogoFallback = function(img) {
  const list = (img.dataset.fallbacks || '').split('|').filter(Boolean);
  if (list.length === 0) {
    img.onerror = null;
    img.style.display = 'none';
    return;
  }
  const next = list.shift();
  img.dataset.fallbacks = list.join('|');
  img.src = next;
};

// bestLogoForStation liefert die beste verfuegbare Logo URL fuer eine
// Station. Wird beim Speichern als Preset Art verwendet damit auch im
// Preset Button + auf der Box ein Logo da ist.
function bestLogoForStation(s) {
  return stationLogoCandidates(s)[0] || '';
}

// stationLogoChain liefert alle Logo Kandidaten als pipe-separierte
// Kette. Persistieren wir das in preset.art, kann das Frontend bei
// onerror durch alle Fallbacks kaskadieren — auch nach App Neustart.
// Der Stick splittet pipe-separated art beim PlayURL und nutzt nur den
// ersten Eintrag fuer den UPnP albumArtURI.
function stationLogoChain(s) {
  return stationLogoCandidates(s).join('|');
}

// ---------- Country Code → Flag Emoji ----------
// ISO 3166-1 alpha-2 zu Regional Indicator Symbol (Unicode Flag).
function flagFromCC(cc) {
  if (!cc || cc.length !== 2) return '';
  const A = 0x1F1E6;
  const c0 = cc.toUpperCase().charCodeAt(0) - 65;
  const c1 = cc.toUpperCase().charCodeAt(1) - 65;
  if (c0 < 0 || c0 > 25 || c1 < 0 || c1 > 25) return '';
  return String.fromCodePoint(A + c0) + String.fromCodePoint(A + c1);
}

// Laenderliste fuer Filter Dropdown. Wir zeigen nur die haeufigsten — der
// User kann jederzeit ohne Filter suchen um alles zu sehen.
// Alle unterstuetzten Laender, ungeordnet — Sortierung passiert unten
// (alphabetisch nach deutschem Namen, DACH oben fuer den Default User).
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

const COUNTRIES = (() => {
  const top = COUNTRIES_TOP
    .map(cc => COUNTRIES_ALL.find(c => c.cc === cc))
    .filter(Boolean);
  const rest = COUNTRIES_ALL
    .filter(c => !COUNTRIES_TOP.includes(c.cc))
    .sort((a, b) => a.name.localeCompare(b.name, 'de'));
  return [{ cc: '', name: 'alle Laender' }, ...top, ...rest];
})();

const ORDERS = [
  { v: 'votes',      label: 'Beliebtheit' },
  { v: 'clickcount', label: 'Hoererzahlen' },
  { v: 'clicktrend', label: 'Trend' },
  { v: 'name',       label: 'Name' },
];

// ---------- DOM Skeleton ----------

document.querySelector('#app').innerHTML = `
  <header class="app-header">
    <div class="app-brand">ST <span class="app-brand-accent">Reborn</span></div>
    <div class="app-tagline" id="appTagline"></div>
    <div class="app-supported" id="appSupported"></div>
  </header>
  <div class="tabs">
    <button class="tab-btn active" data-view="box">Musik hoeren</button>
    <button class="tab-btn" data-view="settings">Box Einstellungen</button>
    <button class="tab-btn" data-view="setup">USB-Stick einrichten</button>
  </div>
  <div id="appUpdateBanner" class="app-update-banner hidden"></div>
  <div id="globalSecurityBanner" class="global-security-banner hidden">
    <span class="global-security-text">
      <b>Empfehlung:</b> USB-Stick aus der Box ziehen und Box einmal neu starten. Sonst ist die Box im Netzwerk angreifbar.
    </span>
    <button class="btn btn-mini" id="globalSecurityRebootBtn">Box neu starten</button>
  </div>
  <div id="view-box" class="view"></div>
  <div id="view-settings" class="view hidden"></div>
  <div id="view-setup" class="view hidden"></div>

  <div class="modal hidden" id="pickModal">
    <div class="modal-content">
      <h3 id="pickTitle">Auf Speichertaste legen</h3>
      <p class="modal-sub" id="pickSub"></p>
      <div class="pick-grid" id="pickGrid"></div>
      <button class="btn btn-secondary" id="pickCancel">Abbrechen</button>
    </div>
  </div>

  <div class="modal hidden" id="warnModal">
    <div class="modal-content">
      <h3 class="warn-title"><span class="warn-icon">&#9888;</span> Achtung</h3>
      <div id="warnBody"></div>
      <div class="warn-buttons">
        <button class="btn btn-secondary" id="warnCancel">Abbrechen</button>
        <button class="btn btn-danger" id="warnConfirm">Trotzdem fortfahren</button>
      </div>
    </div>
  </div>

  <div class="modal hidden" id="errorModal">
    <div class="modal-content">
      <h3 class="warn-title"><span class="warn-icon">&#9888;</span> Fehler</h3>
      <textarea id="errorText" class="error-text" readonly></textarea>
      <div class="warn-buttons">
        <button class="btn btn-secondary" id="errorCopy">Kopieren</button>
        <button class="btn" id="errorClose">Schliessen</button>
      </div>
    </div>
  </div>

  <div id="toast" class="toast"></div>

  <footer class="app-footer" id="appFooter"></footer>
`;

const $ = (id) => document.getElementById(id);

// Tabs
document.querySelectorAll('.tab-btn').forEach(btn => {
  btn.onclick = () => switchView(btn.dataset.view);
});

// Lokalisierter Tagline. Quelle: navigator.language. Default Englisch.
const SUPPORTED_LINE = {
  de: 'fuer SoundTouch 10, 20, 30 und Portable',
  fr: 'pour SoundTouch 10, 20, 30 et Portable',
  it: 'per SoundTouch 10, 20, 30 e Portable',
  es: 'para SoundTouch 10, 20, 30 y Portable',
  nl: 'voor SoundTouch 10, 20, 30 en Portable',
  pt: 'para SoundTouch 10, 20, 30 e Portable',
  en: 'for SoundTouch 10, 20, 30 and Portable',
};

const TAGLINES = {
  de: 'Bose SoundTouch Lautsprecher ohne Bose Cloud weiter nutzen.',
  fr: 'Continue d\'utiliser tes enceintes Bose SoundTouch sans le cloud Bose.',
  it: 'Continua a usare gli altoparlanti Bose SoundTouch senza il cloud di Bose.',
  es: 'Sigue usando tus altavoces Bose SoundTouch sin la nube de Bose.',
  nl: 'Blijf je Bose SoundTouch speakers gebruiken, zonder de Bose cloud.',
  pt: 'Continua a usar os teus altifalantes Bose SoundTouch sem a cloud Bose.',
  en: 'Keep using your Bose SoundTouch speakers, without the Bose cloud.',
};

(function applyTagline() {
  const lang = (navigator.language || 'en').toLowerCase().split('-')[0];
  const tEl = $('appTagline');
  if (tEl) tEl.textContent = TAGLINES[lang] || TAGLINES.en;
  const sEl = $('appSupported');
  if (sEl) sEl.textContent = SUPPORTED_LINE[lang] || SUPPORTED_LINE.en;
})();

function switchView(view) {
  state.view = view;
  document.querySelectorAll('.tab-btn').forEach(b => {
    b.classList.toggle('active', b.dataset.view === view);
  });
  $('view-box').classList.toggle('hidden', view !== 'box');
  $('view-settings').classList.toggle('hidden', view !== 'settings');
  $('view-setup').classList.toggle('hidden', view !== 'setup');
  // Globaler SSH Banner: im Setup Tab fehlt Box Kontext, dort
  // immer ausblenden. Sonst checkSshBanner entscheiden lassen.
  if (view === 'setup') {
    const gb = $('globalSecurityBanner');
    if (gb) gb.classList.add('hidden');
  } else {
    checkSshBanner();
  }
  if (view === 'setup') refreshDrives();
  if (view === 'box') {
    // Beim Wechsel zur Musik Ansicht frische mDNS Liste holen, sonst
    // bleibt ein gerade veraendeter Name oder eine offline gegangene
    // Box stehen. discoverBoxes ist async und non blocking.
    discoverBoxes();
    refreshStatus();
    loadMusicTabVolume();
  }
  if (view === 'settings') loadBoxSettings();
}

// ---------- Footer ----------

async function renderFooter() {
  try {
    state.appInfo = await AppInfo();
  } catch {
    state.appInfo = { version: 'unbekannt', build: '', author: '', githubUrl: '', donateUrl: '', websiteUrl: '', donateSlogan: '' };
  }
  const i = state.appInfo;
  const links = [];
  if (i.githubUrl)  links.push(`<a href="#" data-url="${escapeAttr(i.githubUrl)}" class="footer-link">GitHub</a>`);
  if (i.websiteUrl) links.push(`<a href="#" data-url="${escapeAttr(i.websiteUrl)}" class="footer-link">Webseite</a>`);
  const buildStr = i.build && i.build !== 'dev' ? ` <span class="build-stamp">(Build ${escapeHtml(i.build)})</span>` : '';
  $('appFooter').innerHTML = `
    <div class="footer-left">
      ST Reborn &middot; Version <b>${escapeHtml(i.version)}</b>${buildStr}${i.author ? ' &middot; ' + escapeHtml(i.author) : ''}
      <div class="footer-fine">Independent open source project, donation funded, MIT license.</div>
    </div>
    <div class="footer-right">${links.join(' &middot; ')}</div>
  `;
  $('appFooter').querySelectorAll('.footer-link').forEach(a => {
    a.onclick = (e) => { e.preventDefault(); BrowserOpenURL(a.dataset.url); };
  });
  renderDonateSidebar();
  checkAppUpdate();
  // appInfo may have arrived after the first discovery completed; the
  // badge function defers until both are known.
  updateSettingsTabBadge();
}

function renderDonateSidebar() {
  const side = $('donateSide');
  if (!side) return;
  const i = state.appInfo || {};
  const slogan = i.donateSlogan || 'Dir gefaellt die App? Ich freue mich ueber einen Kaffee.';
  const hasUrl = !!i.donateUrl;
  side.innerHTML = `
    <div class="donate-icon">&#9749;</div>
    <div class="donate-slogan">${escapeHtml(slogan)}</div>
    <button class="donate-btn" id="donateBtn"${hasUrl ? '' : ' disabled title="Spenden Link folgt sobald die Webseite live ist"'}>
      Per PayPal spenden
    </button>
    <small>${hasUrl ? '' : '(Link folgt in Kuerze)'}</small>
  `;
  const btn = $('donateBtn');
  if (btn && hasUrl) btn.onclick = () => BrowserOpenURL(i.donateUrl);
}

async function checkAppUpdate() {
  try {
    const m = await CheckAppUpdate();
    if (!m || !m.version) return;
    const banner = $('appUpdateBanner');
    banner.innerHTML = `
      <div><b>Neue App Version verfuegbar:</b> ${escapeHtml(m.version)} <small>${escapeHtml(m.notes || '')}</small></div>
      ${m.downloadUrl ? `<button class="btn btn-mini" id="appUpdateBtn">Download</button>` : ''}
    `;
    banner.classList.remove('hidden');
    const dl = $('appUpdateBtn');
    if (dl) dl.onclick = () => BrowserOpenURL(m.downloadUrl);
  } catch {
    // Stille
  }
}

// ---------- Box steuern View ----------

$('view-box').innerHTML = `
  <div class="topbar">
    <div class="topbar-head">
      <div class="topbar-title">Deine gefundenen SoundTouch Lautsprecher im Netzwerk</div>
      <button class="btn-icon" id="refreshBtn" title="Boxen neu suchen"><span class="refresh-icon">&#x21bb;</span></button>
    </div>
    <div class="box-select" id="boxSelect">Suche nach Boxen...</div>
  </div>
  <div id="boxHint" class="box-hint hidden">
    <p>Waehle oben eine Bose Box aus um sie zu steuern.</p>
  </div>
  <div id="boxControls" class="hidden">
    <div id="boxUpdateBanner" class="update-banner hidden"></div>
    <div class="status-bar" id="statusBar"></div>
    <div class="controls">
      <button class="btn" id="pauseBtn">&#9208; Pause</button>
      <button class="btn" id="stopBtn">&#9209; Stop</button>
      <div class="source-buttons">
        <button class="btn btn-source" data-source="AUX" title="Aux Klinke Eingang">AUX</button>
        <button class="btn btn-source btn-source-icon" data-source="BLUETOOTH" title="Bluetooth"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="16" height="16"><polyline points="6.5 6.5 17.5 17.5 12 23 12 1 17.5 6.5 6.5 17.5"></polyline></svg></button>
        <button class="btn btn-source btn-source-icon" data-source="STANDBY" title="Standby">&#9211;</button>
      </div>
      <div class="volume-control">
        <span class="vol-icon" title="Lautstaerke">&#128266;</span>
        <input type="range" id="musicVolume" min="0" max="100" step="1" />
        <span class="vol-val" id="musicVolumeVal">--</span>
      </div>
    </div>
    <div class="grid" id="presets"></div>
    <div class="search">
      <h3>Sender suchen <small>(via radio-browser.info)</small></h3>
      <div class="search-input-row">
        <input type="text" id="searchQ" placeholder="z.B. NDR, SWR, Rock..." />
        <button class="btn" id="searchBtn">Suchen</button>
        <button class="btn btn-mini" id="topBtn">Top Liste</button>
      </div>
      <div class="search-filters">
        <label>Land:
          <select id="searchCountry"></select>
        </label>
        <label>Sprache:
          <select id="searchLang"><option value="">alle</option></select>
        </label>
        <label>Sortierung:
          <select id="searchOrder"></select>
        </label>
        <label><input type="checkbox" id="searchOnlyOK" checked /> nur funktionierende</label>
        <label><input type="checkbox" id="searchOnlyBose" checked /> nur Box kompatibel</label>
      </div>
      <div class="genre-chips" id="genreChips"></div>
      <div class="search-count muted small hidden" id="searchCount"></div>
      <div class="search-results" id="searchResults"></div>
      <div class="load-more-row hidden" id="loadMoreRow">
        <button class="btn btn-mini" id="loadMoreBtn">mehr laden</button>
      </div>
    </div>
  </div>
`;

// Filter Dropdowns befuellen
$('searchCountry').innerHTML = COUNTRIES.map(c =>
  `<option value="${c.cc}">${flagFromCC(c.cc)} ${escapeHtml(c.name)}</option>`
).join('');
$('searchOrder').innerHTML = ORDERS.map(o =>
  `<option value="${o.v}">${escapeHtml(o.label)}</option>`
).join('');
$('searchCountry').value = state.searchCountry;
$('searchOrder').value = state.searchOrder;
$('searchOnlyOK').checked = state.searchOnlyOK;
$('searchOnlyBose').checked = state.searchOnlyBose;

$('refreshBtn').onclick = discoverBoxes;

// Globaler Security Reboot Knopf (im Top Banner)
const gsb = $('globalSecurityRebootBtn');
if (gsb) gsb.onclick = async () => {
  const box = state.currentBox || (state.boxes && state.boxes[0]);
  if (!box) { showToast('Keine Box ausgewaehlt.'); return; }
  const ok = await confirmWarn(
    'Box jetzt neu starten?',
    'Hast du den USB-Stick bereits gezogen? Sonst bleibt SSH nach dem Reboot weiter offen. Aktuelle Wiedergabe wird unterbrochen.'
  );
  if (!ok) return;
  try {
    await RebootBox(box.host, box.port);
    showToast('Box startet neu. Sie ist gleich wieder verfuegbar.');
    setTimeout(discoverBoxes, 35000);
  } catch (e) { showError(e); }
};
$('pauseBtn').onclick = () => action('pause');
$('stopBtn').onclick = () => action('stop');

// Source Buttons (AUX / Bluetooth / Standby) im Musik-Hoeren Tab —
// rufen das neue /api/box/source Endpoint via SelectBoxSource Binding.
document.querySelectorAll('.btn-source').forEach(btn => {
  btn.onclick = async () => {
    const box = state.currentBox;
    if (!box) { showToast('Keine Box ausgewaehlt.'); return; }
    const src = btn.dataset.source;
    btn.disabled = true;
    try {
      await SelectBoxSource(box.host, box.port, src);
      showToast(`Quelle: ${src}`);
      setTimeout(refreshStatus, 800);
    } catch (e) {
      showError(e);
    } finally {
      btn.disabled = false;
    }
  };
});

// Lautstaerke Slider im Musik-Hoeren Tab. Nutzt SetBoxVolume,
// debounced damit User wischen kann ohne hundert API Calls.
let musicVolTimer = null;
let musicVolBox = null;
const musicVolEl = $('musicVolume');
const musicVolValEl = $('musicVolumeVal');
if (musicVolEl) {
  musicVolEl.oninput = () => {
    if (musicVolValEl) musicVolValEl.textContent = musicVolEl.value;
  };
  musicVolEl.onchange = () => {
    musicVolBox = state.currentBox;
    if (!musicVolBox) return;
    if (musicVolTimer) clearTimeout(musicVolTimer);
    musicVolTimer = setTimeout(() => {
      SetBoxVolume(musicVolBox.host, musicVolBox.port,
        parseInt(musicVolEl.value, 10)).catch(showError);
    }, 200);
  };
}

// checkSshBanner prueft via /api/stick/status ob auf der aktuellen
// Box SSH offen ist und togglet den globalen Top Banner entsprechend.
// Wird bei jedem refreshStatus + bei jedem discoverBoxes aufgerufen
// damit der Hinweis nicht erst sichtbar wird wenn der User in den
// Einstellungen Tab geht.
async function checkSshBanner() {
  const gb = $('globalSecurityBanner');
  if (!gb) return;
  const box = state.currentBox;
  // Im Setup Tab gibt es keinen aktuellen Box Kontext — Banner waere
  // ohne Bezug und stoert nur. Sonst sshOpen Status prueffen.
  if (!box || state.view === 'setup') { gb.classList.add('hidden'); return; }
  try {
    const r = await fetch(`http://${box.host}:${box.port}/api/stick/status`);
    if (!r.ok) return;
    const data = await r.json();
    if (data && data.sshOpen) gb.classList.remove('hidden');
    else gb.classList.add('hidden');
  } catch {}
}

// loadMusicTabVolume holt den aktuellen Volume Wert beim Tab Switch
// damit der Slider Position stimmt.
async function loadMusicTabVolume() {
  const box = state.currentBox;
  if (!box || !musicVolEl) return;
  try {
    const data = await BoxSettings(box.host, box.port);
    const vol = (data && data.volume && data.volume.actual) || 0;
    musicVolEl.value = String(vol);
    if (musicVolValEl) musicVolValEl.textContent = String(vol);
  } catch {}
}
$('searchBtn').onclick = () => doSearch();
$('topBtn').onclick = () => doTop();
$('loadMoreBtn').onclick = () => loadMore();
$('searchQ').onkeydown = (e) => { if (e.key === 'Enter') doSearch(); };
$('searchQ').oninput = () => {
  $('searchQ').classList.toggle('has-query', !!$('searchQ').value.trim());
};
$('searchCountry').onchange = () => {
  state.searchCountry = $('searchCountry').value;
  // Land Wechsel setzt die Sprache zurueck auf "alle" — sonst kommt es zu
  // leeren Ergebnissen weil ein Land Sprache Mismatch raus filtert.
  state.searchLang = '';
  const ls = $('searchLang');
  if (ls) ls.value = '';
  updateFilterIndicators();
  try { localStorage.setItem('userTouchedRegion', '1'); } catch {}
  // Sprach Liste fuer das gewaehlte Land nachladen — Counts spiegeln
  // dann genau die Stations in DIESEM Land wider, nicht global.
  state.languages = [];
  loadLanguagesForCountry();
  doRefilter();
};

async function loadLanguagesForCountry() {
  if (!state.currentBox) return;
  try {
    const cc = state.searchCountry || '';
    const url = cc
      ? `http://${state.currentBox.host}:${state.currentBox.port}/api/radio/languages?country=${encodeURIComponent(cc)}&limit=60`
      : `http://${state.currentBox.host}:${state.currentBox.port}/api/radio/languages?limit=40`;
    const r = await fetch(url);
    if (r.ok) {
      state.languages = await r.json() || [];
      renderLanguageOptions();
    }
  } catch {}
}
$('searchLang').onchange    = () => {
  state.searchLang = $('searchLang').value;
  updateFilterIndicators();
  try { localStorage.setItem('userTouchedRegion', '1'); } catch {}
  doRefilter();
};

// updateFilterIndicators setzt die has-filter CSS Klasse auf jene Filter
// Dropdowns die einen anderen Wert als "alle" haben. Damit erkennt der
// User sofort wo aktiv gefiltert wird.
function updateFilterIndicators() {
  const cc = $('searchCountry');
  const lang = $('searchLang');
  if (cc) cc.classList.toggle('has-filter', !!cc.value);
  if (lang) lang.classList.toggle('has-filter', !!lang.value);
}
updateFilterIndicators();
$('searchOrder').onchange   = () => { state.searchOrder   = $('searchOrder').value;   doRefilter(); };
$('searchOnlyOK').onchange  = () => { state.searchOnlyOK  = $('searchOnlyOK').checked; doRefilter(); };
$('searchOnlyBose').onchange = () => { state.searchOnlyBose = $('searchOnlyBose').checked; renderSearchResults(); };
$('pickCancel').onclick = closePick;

// doRefilter triggert die letzte Aktion (Top oder Search) mit den neuen
// Filtern, behaelt aber den Suchbegriff bei.
function doRefilter() {
  state.searchOffset = 0;
  if (state.searchLastMode === 'search' && state.searchLastQuery) {
    doSearch();
  } else {
    doTop();
  }
}

async function discoverBoxes() {
  const hadBoxes = state.boxes.length > 0;
  if (!hadBoxes) {
    // Erste Suche: deutliche Meldung damit der User weiss was passiert
    $('boxSelect').textContent = 'Suche nach Boxen...';
  } else {
    // Hintergrund Refresh: Refresh Button dreht, Liste bleibt sichtbar.
    const rb = $('refreshBtn');
    if (rb) rb.classList.add('spinning');
  }
  try {
    const list = await DiscoverBoxes(4);
    state.boxes = applyPendingNames(list || []);
    saveCachedBoxes(state.boxes);
    if (state.currentBox && state.currentBox.deviceID) {
      const fresh = state.boxes.find(b => b.deviceID === state.currentBox.deviceID);
      if (fresh) {
        const changed = fresh.host !== state.currentBox.host
                     || fresh.port !== state.currentBox.port
                     || fresh.version !== state.currentBox.version
                     || fresh.friendlyName !== state.currentBox.friendlyName;
        state.currentBox = fresh;
        if (changed) {
          state.presets = [];
          state.searchResults = [];
          state.nowLocation = '';
          state.nowPlayState = '';
          state.presetErrors = {};
          $('presets').innerHTML = '';
          $('searchResults').innerHTML = '';
          loadPresets();
          refreshStatus();
          checkBoxUpdate();
        }
      } else {
        state.currentBox = null;
        state.presets = [];
        $('presets').innerHTML = '';
      }
    }
    renderBoxSelect();
    updateSettingsTabBadge();
    // Auto Retry: wenn nach Setup/Reboot noch eine Box ihren neuen
    // Namen nicht ueber mDNS gemeldet hat, alle 4 s nochmal suchen
    // bis maximal 90 s. Das wird ueber pendingNames gesteuert.
    scheduleNextAutoRefresh();
  } catch (e) {
    if (!hadBoxes) $('boxSelect').textContent = 'Fehler: ' + e;
  } finally {
    const rb = $('refreshBtn');
    if (rb) rb.classList.remove('spinning');
  }
}

let _autoRefreshTimer = null;
function scheduleNextAutoRefresh() {
  if (_autoRefreshTimer) clearTimeout(_autoRefreshTimer);
  const now = Date.now();
  const stillPending = Object.keys(state.pendingNames).some(
    id => state.pendingNames[id].until > now
  );
  if (!stillPending) return; // alles bereits konsolidiert
  _autoRefreshTimer = setTimeout(() => {
    _autoRefreshTimer = null;
    discoverBoxes();
  }, 4000);
}

// applyPendingNames ueberschreibt friendlyName aus mDNS mit unserem
// lokal gespeicherten Wert solange der Stick noch nicht re-announciert
// hat. Eintraege expirieren nach state.pendingNames[id].until.
function applyPendingNames(list) {
  const now = Date.now();
  // Abgelaufene loeschen
  for (const id of Object.keys(state.pendingNames)) {
    if (now > state.pendingNames[id].until) delete state.pendingNames[id];
  }
  // Wenn der Stick schon den neuen Namen meldet, Pending Eintrag clearen
  return list.map(b => {
    const p = state.pendingNames[b.deviceID];
    if (!p) return b;
    if ((b.friendlyName || '') === p.name) {
      delete state.pendingNames[b.deviceID];
      return b;
    }
    return { ...b, friendlyName: p.name };
  });
}

function renderBoxSelect() {
  const sel = $('boxSelect');
  if (state.boxes.length === 0) {
    sel.innerHTML = `
      <div class="empty-state">
        <div class="empty-state-title">Keine Box im Netzwerk gefunden.</div>
        <div class="empty-state-text">
          Wenn der Stick schon in der Box steckt und die WLAN LED leuchtet,
          ist eventuell nur der Stick Agent haengen geblieben. Trenne dann
          kurz den Strom der Box und stecke sie wieder ein.
          <br><br>
          Wenn noch kein Stick gesteckt ist, richte ihn zuerst ein.
        </div>
        <div class="empty-state-buttons">
          <button class="btn btn-mini" id="emptyRetry">Erneut suchen</button>
          <button class="btn btn-primary btn-mini" id="emptyGoSetup">Zur Stick Einrichtung</button>
        </div>
      </div>`;
    const go = document.getElementById('emptyGoSetup');
    if (go) go.onclick = () => switchView('setup');
    const rt = document.getElementById('emptyRetry');
    if (rt) rt.onclick = () => discoverBoxes();
    updateBoxUiVisibility();
    return;
  }
  sel.innerHTML = state.boxes.map(b => {
    const active = state.currentBox && state.currentBox.host === b.host ? ' active' : '';
    const label = b.friendlyName || b.name || b.host;
    const ver = b.version ? `<span class="box-ver" title="Stick Version">${escapeHtml(b.version)}</span>` : '';
    return `<span class="box-btn${active}" data-host="${b.host}" data-port="${b.port}" role="button" tabindex="0">${escapeHtml(label)} <small>${b.host}</small>${ver}<span class="box-edit" data-host="${b.host}" data-port="${b.port}" title="Einstellungen dieser Box">&#9881;</span></span>`;
  }).join('');
  sel.querySelectorAll('.box-btn').forEach(btn => {
    btn.onclick = (e) => {
      // Klick auf das Zahnrad geht in die Einstellungen, nicht auswaehlen.
      if (e.target.closest('.box-edit')) return;
      const host = btn.dataset.host;
      const port = parseInt(btn.dataset.port, 10);
      const box = state.boxes.find(b => b.host === host && b.port === port);
      selectBox(box);
    };
  });
  // Zahnrad Click: setzt settingsBox und wechselt Tab.
  sel.querySelectorAll('.box-edit').forEach(icon => {
    icon.onclick = (e) => {
      e.stopPropagation();
      const host = icon.dataset.host;
      const port = parseInt(icon.dataset.port, 10);
      const box = state.boxes.find(b => b.host === host && b.port === port);
      if (!box) return;
      state.settingsBox = box;
      switchView('settings');
    };
  });
  if (!state.currentBox) {
    const lastID = loadLastBox();
    let target = lastID ? state.boxes.find(b => b.deviceID === lastID) : null;
    if (!target && state.boxes.length === 1) target = state.boxes[0];
    if (target) selectBox(target);
  }
  updateBoxUiVisibility();
}

function selectBox(box) {
  state.currentBox = box;
  if (box && box.deviceID) saveLastBox(box.deviceID);
  state.presetErrors = {};
  renderBoxSelect();
  loadPresets();
  refreshStatus();
  checkBoxUpdate();
  loadTaxonomy();
  // Region vom Stick holen und als Default fuer Radio Suche nutzen.
  // Wenn der User schon manuell ein Land im Dropdown gewaehlt hat, nicht
  // ueberschreiben.
  loadStickRegion();
}

let regionLoaded = false;
async function loadStickRegion() {
  if (regionLoaded || !state.currentBox) return;
  try {
    const r = await fetch(`http://${state.currentBox.host}:${state.currentBox.port}/api/region`);
    if (!r.ok) return;
    const data = await r.json();
    if (data && data.country) {
      // Nur defaults setzen wenn der User nichts manuelles eingestellt hat
      const userTouched = (() => { try { return !!localStorage.getItem('userTouchedRegion'); } catch { return false; }})();
      if (!userTouched) {
        state.searchCountry = data.country;
        const cs = $('searchCountry');
        if (cs) cs.value = data.country;
      }
      if (data.language && !state.searchLang) {
        state.searchLang = data.language;
      }
      updateFilterIndicators();
      regionLoaded = true;
    }
  } catch {}
}

// loadTaxonomy holt einmalig die Genre Tag Liste und die Sprachen vom
// Stick und befuellt damit die Genre Chips + Sprach Dropdown.
async function loadTaxonomy() {
  if (!state.currentBox) return;
  if (state.tags.length === 0) {
    try {
      const r = await fetch(`http://${state.currentBox.host}:${state.currentBox.port}/api/radio/tags?limit=24`);
      if (r.ok) {
        state.tags = await r.json() || [];
        renderGenreChips();
      }
    } catch {}
  }
  if (state.languages.length === 0) {
    try {
      const r = await fetch(`http://${state.currentBox.host}:${state.currentBox.port}/api/radio/languages?limit=40`);
      if (r.ok) {
        state.languages = await r.json() || [];
        renderLanguageOptions();
      }
    } catch {}
  }
}

function renderGenreChips() {
  const wrap = $('genreChips');
  if (!wrap) return;
  if (!state.tags.length) { wrap.innerHTML = ''; return; }
  // Buckets: kanonisierte Tag-Liste, Stationcount summieren.
  const buckets = {};
  for (const t of state.tags) {
    const canon = canonGenre(t.name);
    if (!canon) continue;
    if (!buckets[canon]) {
      buckets[canon] = { canon, label: translateGenre(t.name), count: 0 };
    }
    buckets[canon].count += (t.stationcount || 0);
  }
  const merged = Object.values(buckets)
    .filter(b => b.count > 0)
    .sort((a, b) => b.count - a.count);

  const chips = ['<button class="chip' + (!state.searchTag ? ' active' : '') + '" data-tag="">Alle</button>'].concat(
    merged.map(b => {
      const active = state.searchTag === b.canon ? ' active' : '';
      return `<button class="chip${active}" data-tag="${escapeAttr(b.canon)}" title="${formatNumber(b.count)} Sender">${escapeHtml(b.label)}</button>`;
    })
  );
  wrap.innerHTML = chips.join('');
  wrap.querySelectorAll('.chip').forEach(btn => {
    btn.onclick = () => {
      state.searchTag = btn.dataset.tag || '';
      renderGenreChips();
      doRefilter();
    };
  });
}

// LANG_LABELS_DE: deutsche Anzeigenamen fuer die radio-browser.info
// Sprachen. API liefert lowercased englische Namen ("german",
// "english"...). Bei nicht gemappten Sprachen zeigen wir den original
// Namen (capitalized) als Fallback.
const LANG_LABELS_DE = {
  'german': 'Deutsch', 'english': 'Englisch', 'french': 'Franzoesisch',
  'spanish': 'Spanisch', 'italian': 'Italienisch', 'dutch': 'Niederlaendisch',
  'portuguese': 'Portugiesisch', 'russian': 'Russisch', 'polish': 'Polnisch',
  'turkish': 'Tuerkisch', 'arabic': 'Arabisch', 'japanese': 'Japanisch',
  'chinese': 'Chinesisch', 'swedish': 'Schwedisch', 'norwegian': 'Norwegisch',
  'danish': 'Daenisch', 'finnish': 'Finnisch', 'czech': 'Tschechisch',
  'hungarian': 'Ungarisch', 'romanian': 'Rumaenisch', 'greek': 'Griechisch',
  'ukrainian': 'Ukrainisch', 'bulgarian': 'Bulgarisch', 'croatian': 'Kroatisch',
  'serbian': 'Serbisch', 'slovak': 'Slowakisch', 'slovenian': 'Slowenisch',
  'estonian': 'Estnisch', 'latvian': 'Lettisch', 'lithuanian': 'Litauisch',
  'irish': 'Irisch', 'welsh': 'Walisisch', 'catalan': 'Katalanisch',
  'galician': 'Galizisch', 'basque': 'Baskisch', 'icelandic': 'Islaendisch',
  'hindi': 'Hindi', 'thai': 'Thailaendisch', 'vietnamese': 'Vietnamesisch',
  'korean': 'Koreanisch', 'indonesian': 'Indonesisch', 'malay': 'Malaiisch',
  'persian': 'Persisch', 'hebrew': 'Hebraeisch', 'mandarin': 'Mandarin',
  'cantonese': 'Kantonesisch', 'bengali': 'Bengalisch', 'tamil': 'Tamilisch',
  'urdu': 'Urdu', 'maltese': 'Maltesisch',
};

function localizeLanguageName(name) {
  if (!name) return '';
  const key = name.toLowerCase();
  return LANG_LABELS_DE[key] || (name.charAt(0).toUpperCase() + name.slice(1));
}

function renderLanguageOptions() {
  const sel = $('searchLang');
  if (!sel || !state.languages.length) return;
  const opts = ['<option value="">alle</option>'];
  for (const l of state.languages) {
    if (!l.name) continue;
    const label = localizeLanguageName(l.name);
    opts.push(`<option value="${escapeAttr(l.name)}">${escapeHtml(label)} (${l.stationcount})</option>`);
  }
  sel.innerHTML = opts.join('');
  sel.value = state.searchLang;
}

// updateSettingsTabBadge shows a small blue dot on the "Box Einstellungen"
// tab whenever at least one discovered box reports a version different
// from the desktop app's own version. The dot signals: there is work to
// do in this tab, namely OTA-update at least one box.
//
// Version data comes from the mDNS TXT record so no extra HTTP call is
// needed — the badge updates as the box list refreshes.
function updateSettingsTabBadge() {
  const btn = document.querySelector('.tab-btn[data-view="settings"]');
  if (!btn) return;
  const appVer = state.appInfo && state.appInfo.version;
  let needsUpdate = false;
  if (appVer) {
    for (const b of state.boxes) {
      if (b && b.version && b.version !== appVer) {
        needsUpdate = true;
        break;
      }
    }
  }
  btn.classList.toggle('has-update', needsUpdate);
}

async function checkBoxUpdate() {
  if (!state.currentBox || !state.appInfo) return;
  const banner = $('boxUpdateBanner');
  banner.classList.add('hidden');
  try {
    const v = await BoxAgentVersion(state.currentBox.host, state.currentBox.port);
    const boxVer = v.version || 'unbekannt';
    const boxBuild = v.build || '';
    const appVer = state.appInfo.version;
    const appBuild = state.appInfo.build || '';
    // Banner zeigen wir nur bei echtem Version Unterschied. Build
    // Stamp Differenzen (jeder Rebuild bekommt neuen Stamp) sind
    // nicht alarmierend genug fuer ein prominentes Update Banner.
    if (boxVer === appVer) return;
    const boxLabel = boxBuild ? `${boxVer} (Build ${boxBuild})` : boxVer;
    const appLabel = appBuild ? `${appVer} (Build ${appBuild})` : appVer;
    banner.innerHTML = `
      <div class="update-msg">
        <b>Box Software Update verfuegbar</b>
        <small>Box: ${escapeHtml(boxLabel)} &middot; App: ${escapeHtml(appLabel)}</small>
      </div>
      <button class="btn btn-primary btn-mini" id="boxUpdateBtn">Aktualisieren</button>
    `;
    banner.classList.remove('hidden');
    $('boxUpdateBtn').onclick = doBoxUpdate;
  } catch {
    if (state.currentBox.version && state.currentBox.version !== state.appInfo.version) {
      banner.innerHTML = `
        <div class="update-msg">
          <b>Box Software Update verfuegbar</b>
          <small>Box laeuft mit Version ${escapeHtml(state.currentBox.version)}, die App hat Version ${escapeHtml(state.appInfo.version)}.</small>
        </div>
        <button class="btn btn-primary btn-mini" id="boxUpdateBtn">Aktualisieren</button>
      `;
      banner.classList.remove('hidden');
      $('boxUpdateBtn').onclick = doBoxUpdate;
    }
  }
}

async function doBoxUpdate() {
  if (!state.currentBox) return;
  // Beide Update Buttons gleichzeitig steuern (Banner oben + Stick Info Sektion)
  const buttons = ['boxUpdateBtn', 'stickInfoUpdateBtn'].map(id => $(id)).filter(Boolean);
  const setStatus = (text) => buttons.forEach(b => { b.textContent = text; b.disabled = true; });
  const reset = () => buttons.forEach(b => { b.disabled = false; b.textContent = 'Aktualisieren'; });
  setStatus('Hochladen...');
  try {
    await UpdateBoxAgent(state.currentBox.host, state.currentBox.port);
    setStatus('Software startet neu...');
    showToast('Update hochgeladen. Software auf der Box startet in ca. 10 Sekunden neu.');
    setTimeout(async () => {
      setStatus('Suche Box...');
      await discoverBoxes();
      setStatus('Pruefe Version...');
      setTimeout(() => {
        checkBoxUpdate();
        if (state.view === 'settings') loadBoxSettings();
        reset();
      }, 3000);
    }, 10000);
  } catch (e) {
    showError('Update fehlgeschlagen: ' + e + '\n\nFalls die Box danach nicht mehr antwortet, trenne kurz den Strom und stecke sie wieder an.');
    reset();
  }
}

function updateBoxUiVisibility() {
  const hasBox = !!state.currentBox;
  const hasAny = state.boxes.length > 0;
  $('boxControls').classList.toggle('hidden', !hasBox);
  $('boxHint').classList.toggle('hidden', !hasAny || hasBox);
}

async function loadPresets(retry = 0) {
  if (!state.currentBox) return;
  if (state.presets.length === 0) {
    $('presets').innerHTML = '<div class="muted small grid-loading">Speichertasten werden geladen...</div>';
  }
  try {
    state.presets = await GetPresets(state.currentBox.host, state.currentBox.port) || [];
    renderPresets();
    healPresetLogos();
  } catch {
    if (retry < 1) {
      setTimeout(() => loadPresets(retry + 1), 1500);
      return;
    }
    if (state.presets.length > 0) {
      renderPresets();
    } else {
      $('presets').innerHTML = '<div class="muted small">Box gerade nicht erreichbar — versuche es gleich nochmal.</div>';
    }
  }
}

// healPresetLogos sucht fuer Presets ohne Logo (alte Presets aus der
// Pre-Logo Zeit oder per Hardware angelegte) bei radio-browser nach dem
// Sender Namen und uebernimmt das Favicon. Persistiert das Ergebnis
// zurueck auf den Stick damit es auch im Box Display erscheint.
let healingInProgress = false;
async function healPresetLogos() {
  if (healingInProgress) return;
  if (!state.currentBox) return;
  const missing = state.presets.filter(p => !p.art && p.name);
  if (missing.length === 0) return;
  healingInProgress = true;
  try {
    await Promise.all(missing.map(async (p) => {
      try {
        // Bewusst tolerant: KEIN onlyok (auch wenn Sender gerade als
        // broken gilt, hat er meist trotzdem ein Logo). limit hoch genug
        // um einen exakten Name-Match unter mehreren gleichnamigen
        // Sendern zu finden.
        const params = new URLSearchParams({ q: p.name, limit: '12', order: 'votes' });
        const r = await fetch(`http://${state.currentBox.host}:${state.currentBox.port}/api/radio/search?${params}`);
        if (!r.ok) return;
        const list = await r.json() || [];
        const wanted = p.name.toLowerCase().trim();
        // 1) exakter Name match
        let pick = list.find(s => (s.name || '').toLowerCase().trim() === wanted);
        // 2) Substring beidseitig (z.B. "NDR2" vs "NDR 2")
        if (!pick) {
          pick = list.find(s => {
            const n = (s.name || '').toLowerCase().trim();
            return n && (n.includes(wanted) || wanted.includes(n));
          });
        }
        // 3) gleicher Stream Host → vermutlich derselbe Sender
        if (!pick && p.stream_url) {
          const wantHost = extractHost(p.stream_url);
          if (wantHost) {
            pick = list.find(s => {
              return extractHost(s.url) === wantHost || extractHost(s.url_resolved) === wantHost;
            });
          }
        }
        if (!pick) return;
        const logo = stationLogoChain(pick);
        if (!logo) return;
        p.art = logo;
        SetPreset(state.currentBox.host, state.currentBox.port, p.slot, p.name, p.stream_url, logo).catch(() => {});
      } catch {}
    }));
  } finally {
    healingInProgress = false;
    renderPresets();
  }
}

// ---------- Preset Render mit Long Press Support ----------

// activeSlotFromLocation extrahiert die Slot Nummer aus einer Stream
// Proxy URL wie http://127.0.0.1:8888/stream/3 — Box ContentItems
// laufen seit Build 2335 alle ueber den Proxy, daher matcht der frueher
// genutzte Direkt URL Vergleich nicht mehr. Mit Slot Match bleibt das
// green highlight stabil auch wenn die echte CDN URL Tokens wechselt.
function activeSlotFromLocation(loc) {
  if (!loc) return null;
  const m = loc.match(/\/stream\/(\d+)(?:[/?#]|$)/);
  return m ? parseInt(m[1], 10) : null;
}

function renderPresets() {
  const grid = $('presets');
  grid.innerHTML = '';
  const activeSlot = activeSlotFromLocation(state.nowLocation);
  // Wenn Box gerade ueber Stream Proxy spielt, die echte Stream URL des
  // Quell-Slots ermitteln. So koennen wir Geschwister Slots mit
  // demselben Sender ebenfalls als aktiv markieren — sonst leuchtet
  // nur der eine Slot dessen Slot-Nummer in /stream/<n> steht.
  let activeStreamURL = null;
  if (activeSlot !== null) {
    const ap = state.presets.find(x => x.slot === activeSlot);
    if (ap) activeStreamURL = ap.stream_url;
  }
  for (let i = 1; i <= 6; i++) {
    const p = state.presets.find(x => x.slot === i);
    const isActive = p && state.nowLocation && (
      p.stream_url === state.nowLocation ||
      (activeSlot !== null && p.slot === activeSlot) ||
      (activeStreamURL && p.stream_url === activeStreamURL)
    );
    const hasErr = !!state.presetErrors[i];
    const div = document.createElement('div');
    div.className = 'preset' + (p ? '' : ' empty') + (isActive ? ' playing' : '') + (hasErr ? ' error' : '');
    div.dataset.slot = i;
    if (p) {
      let stateLabel = '';
      if (hasErr) {
        stateLabel = `<div class="preset-state state-err">&#9888; ${escapeHtml(state.presetErrors[i])}</div>`;
      } else if (isActive) {
        const ps = state.nowPlayState;
        if (ps === 'PLAY_STATE') {
          stateLabel = '<div class="preset-state state-play">wird abgespielt</div>';
        } else if (ps === 'BUFFERING_STATE') {
          stateLabel = '<div class="preset-state state-buf">Stream wird hergestellt ...</div>';
        } else if (ps === 'PAUSE_STATE') {
          stateLabel = '<div class="preset-state state-pause">pausiert</div>';
        }
      }
      const hint = state.nowLocation && !isActive
        ? '<div class="preset-hint">lang gedrueckt halten = aktueller Sender hier speichern</div>'
        : '';
      // Preset Logo Fallback Kaskade:
      //   1. p.art Kandidaten (pipe-separiert wenn vorhanden).
      //   2. state.nowIcon NUR wenn p.art LEER ist und der Preset gerade
      //      aktiv ist — sonst koennte das gerade abgespielte Logo eines
      //      anderen Senders auf einen inaktiven Preset Button rutschen
      //      wenn dessen p.art kaputt ist und in der Kaskade durchfaellt.
      //   3. DDG / Google Service fuer Stream Host und dessen Wurzeldomain.
      const presetCandidates = [];
      const addCands = (val) => {
        if (!val) return;
        for (const c of String(val).split('|')) {
          const t = c.trim();
          if (t && !presetCandidates.includes(t)) presetCandidates.push(t);
        }
      };
      if (p.art) {
        addCands(p.art);
      } else if (isActive && state.nowIcon) {
        addCands(state.nowIcon);
        // Auto-Persistieren damit der Preset beim naechsten Laden direkt
        // sein Logo hat.
        p.art = state.nowIcon;
        SetPreset(state.currentBox.host, state.currentBox.port, p.slot, p.name, p.stream_url, state.nowIcon).catch(() => {});
      }
      const streamHost = extractHost(p.stream_url);
      const hostsToTry = [];
      if (streamHost) hostsToTry.push(streamHost);
      const streamRoot = rootDomain(streamHost);
      if (streamRoot && streamRoot !== streamHost) hostsToTry.push(streamRoot);
      for (const h of hostsToTry) {
        for (const svc of iconServicesFor(h)) {
          if (!presetCandidates.includes(svc)) presetCandidates.push(svc);
        }
      }
      const logo = presetCandidates.length === 0 ? '' :
        `<img class="preset-logo" src="${escapeAttr(presetCandidates[0])}"
              data-fallbacks="${escapeAttr(presetCandidates.slice(1).join('|'))}"
              onerror="window.__nextLogoFallback(this)"/>`;
      div.innerHTML = `
        <div class="preset-head"><span class="num">Taste ${i}</span><span class="del" data-slot="${i}" title="Belegung loeschen">&times;</span></div>
        <div class="preset-body">
          ${logo}
          <div class="preset-text">
            <div class="name">${escapeHtml(p.name || 'Taste ' + i)}</div>
            ${stateLabel}
          </div>
        </div>
        ${hint}
        <div class="long-press-bar" id="lp-bar-${i}"></div>
      `;
    } else {
      const hint = state.nowLocation
        ? '<div class="preset-hint">lang gedrueckt halten = aktueller Sender hier speichern</div>'
        : '<div class="url">Sender unten suchen, dann auf Taste legen</div>';
      div.innerHTML = `
        <div class="num">Taste ${i}</div>
        <div class="name">leer</div>
        ${hint}
        <div class="long-press-bar" id="lp-bar-${i}"></div>
      `;
    }
    attachPresetHandlers(div, i, p);
    grid.appendChild(div);
  }
  grid.querySelectorAll('.del').forEach(el => {
    el.onclick = async (e) => {
      e.stopPropagation();
      const slot = parseInt(el.dataset.slot, 10);
      const p = state.presets.find(x => x.slot === slot);
      const senderName = p && p.name ? escapeHtml(p.name) : 'Sender';
      const ok = await confirmWarn(
        'Speichertaste leeren',
        `Belegung der Taste ${slot} (<b>${senderName}</b>) wirklich loeschen?`
      );
      if (!ok) return;
      try {
        await DeletePreset(state.currentBox.host, state.currentBox.port, slot);
        loadPresets();
      } catch (err) { showError(err); }
    };
  });
}

// attachPresetHandlers wired Klick (kurz → play) und Long Press (lang →
// aktuellen Sender auf den Slot speichern). LONG_PRESS_MS = 800ms.
// VISUAL_HOLD_DELAY = 180ms: erst nach so langer Haltezeit zeigen wir
// das scale(0.96) Visual. Bei kurzem Klick gibt es so KEIN Mini Rutschen
// (transition wuerde sonst kurz scale-down + sofort wieder scale-up
// spielen, was Logos optisch verschiebt).
const LONG_PRESS_MS = 800;
const VISUAL_HOLD_DELAY = 180;
function attachPresetHandlers(el, slot, preset) {
  let timer = null;
  let visualTimer = null;
  let armed = false;       // wir starten den Hold
  let firedLong = false;   // true wenn Long Press ausgeloest
  let startedAt = 0;
  const bar = el.querySelector('.long-press-bar');
  const animateBar = () => {
    if (!armed) return;
    const elapsed = Date.now() - startedAt;
    const pct = Math.min(100, (elapsed / LONG_PRESS_MS) * 100);
    if (bar) bar.style.width = pct + '%';
    if (armed) requestAnimationFrame(animateBar);
  };
  const start = (e) => {
    if (e.button !== undefined && e.button !== 0) return; // nur Links Klick
    // Klick auf das X-Icon nicht als preset Klick werten
    if (e.target.classList && e.target.classList.contains('del')) return;
    armed = true;
    firedLong = false;
    startedAt = Date.now();
    requestAnimationFrame(animateBar);
    visualTimer = setTimeout(() => {
      if (armed) el.classList.add('long-press');
    }, VISUAL_HOLD_DELAY);
    timer = setTimeout(async () => {
      if (!armed) return;
      firedLong = true;
      await saveCurrentToSlot(slot);
      armed = false;
      el.classList.remove('long-press');
      if (bar) bar.style.width = '0%';
    }, LONG_PRESS_MS);
  };
  const cancel = () => {
    if (!armed) return;
    armed = false;
    if (timer) { clearTimeout(timer); timer = null; }
    if (visualTimer) { clearTimeout(visualTimer); visualTimer = null; }
    el.classList.remove('long-press');
    if (bar) bar.style.width = '0%';
  };
  const finish = (e) => {
    if (e.target.classList && e.target.classList.contains('del')) return;
    const wasArmed = armed;
    cancel();
    if (!wasArmed) return;
    if (firedLong) return;
    if (preset) play(slot);
  };
  el.addEventListener('mousedown', start);
  el.addEventListener('mouseup', finish);
  el.addEventListener('mouseleave', cancel);
  el.addEventListener('touchstart', (e) => { start(e); }, { passive: true });
  el.addEventListener('touchend', (e) => { finish(e); });
  el.addEventListener('touchcancel', cancel);
}

// saveCurrentToSlot speichert den aktuell laufenden Sender auf den
// uebergebenen Slot (ueberschreibt den vorherigen). Nutzt die now_playing
// Daten state.nowLocation + state.nowName plus das zuletzt bekannte Logo.
async function saveCurrentToSlot(slot) {
  // Frisch von der Box holen — bei HW Tasten Druck ist state.nowLocation
  // /nowName oft noch nicht aktualisiert (boxws Event hinkt hinterher),
  // sonst speichern wir "Sender" als Name oder den vorherigen Sender.
  try { await refreshStatus(); } catch {}
  if (!state.nowLocation) {
    showToast('Kein Sender laeuft gerade — Long Press hat keinen Effekt.');
    return;
  }

  // Fall A: Box spielt aktuell ein Proxy-Item (location = /stream/<sourceSlot>).
  // Das passiert wenn der Sender via Hardware Taste oder durch
  // Anwaehlen eines anderen Soft Slots ausgeloest wurde. Dann
  // kopieren wir direkt den Quell-Preset auf den Ziel-Slot — Name,
  // URL und Art Logo Eins-zu-Eins. Damit umgehen wir state.nowIcon
  // / state.nowName komplett, die bei HW Druck oft noch vom vorigen
  // Sender stammen.
  const sourceSlot = activeSlotFromLocation(state.nowLocation);
  if (sourceSlot !== null && sourceSlot !== slot) {
    const src = state.presets.find(p => p.slot === sourceSlot);
    if (src && src.stream_url) {
      try {
        await SetPreset(
          state.currentBox.host, state.currentBox.port,
          slot, src.name, src.stream_url, src.art || ''
        );
        showToast(`Auf Taste ${slot} kopiert: ${src.name}`);
        await loadPresets();
        return;
      } catch (err) {
        showError('Speichern fehlgeschlagen: ' + err);
        return;
      }
    }
  }

  // Fall B: Box spielt einen Stream der NICHT ueber unseren Proxy
  // laeuft (z.B. ein direkt via Radio Suche gestarteter Sender).
  // Hier nehmen wir state.nowLocation / nowName / nowIcon wie bisher.
  const name = state.nowName || 'Sender';
  try {
    await SetPreset(
      state.currentBox.host, state.currentBox.port,
      slot, name, state.nowLocation, state.nowIcon || ''
    );
    showToast(`Auf Taste ${slot} gespeichert: ${name}`);
    await loadPresets();
    if (state.nowUUID) {
      VoteStation(state.currentBox.host, state.currentBox.port, state.nowUUID).catch(() => {});
    }
  } catch (err) {
    showError('Speichern fehlgeschlagen: ' + err);
  }
}

async function play(slot) {
  const p = state.presets.find(x => x.slot === slot);
  if (p) {
    // Optimistic UI: direkt BUFFERING_STATE setzen damit user sofort
    // Feedback bekommt. Plus sticky bis 6s — refreshStatus darf in
    // dieser Zeit nicht den preset wieder auf grau setzen weil die
    // Box noch alten Stream oder leer meldet.
    state.nowPlayState = 'BUFFERING_STATE';
    state.nowLocation = p.stream_url || '';
    state.nowName = p.name || '';
    state.nowIcon = p.art || '';
    state.nowUUID = '';
    state.optimisticUntil = Date.now() + 6000;
    delete state.presetErrors[slot];
    renderPresets();
  }
  try {
    await PlaySlot(state.currentBox.host, state.currentBox.port, slot);
    delete state.presetErrors[slot];
    refreshStatus();
    setTimeout(refreshStatus, 1500);
  } catch (e) {
    state.nowPlayState = '';
    state.nowLocation = '';
    state.optimisticUntil = 0;
    state.presetErrors[slot] = friendlyPlayError(String(e));
    renderPresets();
    setTimeout(() => refreshStatus(), 2000);
  }
}

// friendlyPlayError macht aus einer technischen Fehlermeldung einen
// kurzen User-tauglichen Hinweis fuer das Preset Label.
function friendlyPlayError(s) {
  const l = String(s).toLowerCase();
  if (l.includes('no such host') || l.includes('lookup')) return 'kein Internet?';
  if (l.includes('timeout') || l.includes('deadline')) return 'Box reagiert nicht';
  if (l.includes('refused')) return 'Box lehnt ab';
  if (l.includes('402') || l.includes('no uri')) return 'Sender nicht abspielbar';
  if (l.includes('500')) return 'Sender Fehler';
  if (l.includes('konnte nicht')) {
    // backend gibt deutsche Meldung mit 'detail' bei UPnP fail
    return 'Sender nicht erreichbar';
  }
  return 'Fehler';
}

async function action(kind) {
  if (!state.currentBox) return;
  const fn = kind === 'pause' ? Pause : Stop;
  try { await fn(state.currentBox.host, state.currentBox.port); } catch (e) { showError(e); }
  setTimeout(refreshStatus, 1000);
}

async function refreshStatus() {
  if (!state.currentBox || state.view !== 'box') return;
  try {
    const xml = await Status(state.currentBox.host, state.currentBox.port);
    const name = decodeXmlEntities((xml.match(/<itemName>([^<]+)<\/itemName>/) || [])[1] || '');
    const src = (xml.match(/source="([^"]+)"/) || [])[1] || '';
    state.nowSource = src;

    // SSH Status passiv mitchecken solange wir eh am Box pollen.
    // Banner global toggeln damit User es ueberall sieht — nicht erst
    // wenn er in den Einstellungen Tab geht.
    checkSshBanner();
    const ps = (xml.match(/<playStatus>([^<]+)<\/playStatus>/) || [])[1] || '';
    const loc = decodeXmlEntities((xml.match(/location="([^"]+)"/) || [])[1] || '');
    // Art URL aus dem <art ...>URL</art> Tag extrahieren — Bose schickt
    // das fuer Stationen mit Bild (z.B. nach Radio Suche Play). Ohne
    // diese Aktualisierung bleibt state.nowIcon vom letzten Soft Klick
    // haengen — Bug "vorletzter Sender Logo".
    const artRaw = decodeXmlEntities((xml.match(/<art[^>]*>([^<]+)<\/art>/) || [])[1] || '');

    // Optimistic Guard: nach User Preset Klick haben wir nowLocation
    // direkt auf den Wunsch Stream gesetzt. Solange optimisticUntil
    // nicht abgelaufen ist, refreshStatus die Location/Name NICHT
    // ueberschreiben — sonst flackert der Button grau zwischen Klick
    // und tatsaechlichem Stream Start. Sobald Box unsere Location
    // bestaetigt: optimistic aufloesen.
    const optimistic = Date.now() < (state.optimisticUntil || 0);
    if (optimistic && loc && loc === state.nowLocation) {
      state.optimisticUntil = 0;
    }
    const newLoc = optimistic ? state.nowLocation : loc;
    const newName = optimistic ? state.nowName : name;
    const stateChanged = state.nowPlayState !== ps || state.nowLocation !== newLoc || state.nowName !== newName;
    state.nowPlayState = ps;
    state.nowLocation = newLoc;
    state.nowName = newName;
    // state.nowIcon aktualisieren: bevorzugt das art Tag aus now_playing.
    // Wenn das leer ist UND wir grad ueber den Stream Proxy spielen,
    // das Logo vom Quell-Preset uebernehmen — Bose UPnP Items haben
    // bei HW Tasten keinen art Tag, also brauchen wir den Fallback.
    if (!optimistic) {
      const slotFromProxy = activeSlotFromLocation(newLoc);
      if (artRaw) {
        state.nowIcon = artRaw;
      } else if (slotFromProxy !== null) {
        const ap = state.presets.find(p => p.slot === slotFromProxy);
        state.nowIcon = (ap && ap.art) || '';
      } else if (!newLoc) {
        state.nowIcon = '';
      }
    }

    // Wenn Box jetzt erfolgreich spielt, Preset Error reset.
    // Box-ContentItems laufen via Stream Proxy, daher Slot Match aus
    // /stream/<slot> auch akzeptieren.
    if (ps === 'PLAY_STATE') {
      const slotFromProxy = activeSlotFromLocation(loc);
      const ap = state.presets.find(p =>
        p.stream_url === loc || (slotFromProxy !== null && p.slot === slotFromProxy)
      );
      if (ap && state.presetErrors[ap.slot]) {
        delete state.presetErrors[ap.slot];
      }
    }

    if (stateChanged && state.presets.length > 0) {
      renderPresets();
    }

    let stateLabel;
    let stateClass;
    let displayName = name;
    if (ps === 'PLAY_STATE') { stateLabel = 'spielt'; stateClass = 'play'; }
    else if (ps === 'BUFFERING_STATE') { stateLabel = 'Stream wird hergestellt'; stateClass = 'buf'; }
    else if (ps === 'PAUSE_STATE') { stateLabel = 'pausiert'; stateClass = 'idle'; }
    else if (src === 'STANDBY') { stateLabel = 'Standby'; stateClass = 'idle'; }
    else { stateLabel = ''; stateClass = 'idle'; }
    // Source spezifische Labels: AUX/BT aktiv → Name + status zeigen
    if (src === 'AUX') {
      displayName = 'AUX Eingang';
      if (!stateLabel) stateLabel = 'aktiv';
    } else if (src === 'BLUETOOTH') {
      displayName = 'Bluetooth';
      if (!stateLabel) stateLabel = 'aktiv';
    }

    $('statusBar').className = 'status-bar status-' + stateClass;
    if (displayName) {
      $('statusBar').innerHTML = `<span class="now">&#9654; ${escapeHtml(displayName)}</span>${stateLabel ? ' <small>' + escapeHtml(stateLabel) + '</small>' : ''}`;
    } else if (stateLabel) {
      $('statusBar').innerHTML = `<span class="muted">${escapeHtml(stateLabel)}</span>`;
    } else {
      $('statusBar').innerHTML = `<span class="muted">bereit</span>`;
    }

    // Source Buttons: aktive Quelle gruen highlighten
    document.querySelectorAll('.btn-source').forEach(b => {
      const s = b.dataset.source;
      const active = (s === 'AUX' && src === 'AUX') ||
                     (s === 'BLUETOOTH' && src === 'BLUETOOTH') ||
                     (s === 'STANDBY' && src === 'STANDBY');
      b.classList.toggle('active', active);
    });
  } catch {
    $('statusBar').textContent = '—';
  }
}

// ---------- Search ----------

const PAGE_SIZE = 30;

async function doSearch() {
  if (!state.currentBox) { showError('Erst Box auswaehlen'); return; }
  const q = $('searchQ').value.trim();
  state.searchLastQuery = q;
  state.searchLastMode = q ? 'search' : 'top';
  state.searchOffset = 0;
  if (!q) { return doTop(); }
  await fetchSearchPage(false);
}

async function doTop() {
  if (!state.currentBox) { showError('Erst Box auswaehlen'); return; }
  state.searchLastMode = 'top';
  state.searchOffset = 0;
  await fetchSearchPage(false);
}

async function loadMore() {
  state.searchOffset += PAGE_SIZE;
  await fetchSearchPage(true);
}

function buildSearchURL() {
  const isSearch = state.searchLastMode === 'search' && state.searchLastQuery;
  // Server Side Sort: wenn User nach Name will, muss der Server das auch
  // so liefern — sonst landet A erst auf Seite 50. Bei order=name fetchen
  // wir aber 4x mehr damit der "nur Box kompatibel" Filter nach dem
  // Strip von laut.fm HTTPS Stationen noch genug uebrig laesst.
  const ord = state.searchOrder || 'votes';
  const limit = ord === 'name' ? PAGE_SIZE * 4 : PAGE_SIZE;
  const params = new URLSearchParams({
    limit: String(limit),
    offset: String(state.searchOffset),
    order: ord,
  });
  if (state.searchCountry) params.set('cc', state.searchCountry);
  if (state.searchLang)    params.set('lang', state.searchLang);
  if (state.searchTag)     params.set('tag', state.searchTag);
  if (state.searchOnlyOK)  params.set('onlyok', '1');
  if (isSearch) {
    params.set('q', state.searchLastQuery);
    return `http://${state.currentBox.host}:${state.currentBox.port}/api/radio/search?${params.toString()}`;
  }
  return `http://${state.currentBox.host}:${state.currentBox.port}/api/radio/top?${params.toString()}`;
}

async function fetchSearchPage(append) {
  const url = buildSearchURL();
  if (!append) {
    $('searchResults').innerHTML = '<div class="muted">Sender werden geladen...</div>';
    $('loadMoreRow').classList.add('hidden');
  }
  try {
    const r = await fetch(url);
    if (!r.ok) throw new Error('HTTP ' + r.status);
    const page = await r.json() || [];
    if (append) {
      state.searchResults = state.searchResults.concat(page);
    } else {
      state.searchResults = page;
    }
    // Dedup nach UUID (paginate + lokal sort kann Duplikate verursachen)
    const seen = new Set();
    state.searchResults = state.searchResults.filter(s => {
      const id = s.stationuuid || (s.name + '|' + s.url);
      if (seen.has(id)) return false;
      seen.add(id);
      return true;
    });
    // Lokale Sortierung — Server liefert immer order=votes, damit die
    // Station Menge ueber alle Sort Optionen konsistent bleibt.
    const ord = state.searchOrder || 'votes';
    state.searchResults.sort((a, b) => {
      switch (ord) {
        case 'name': {
          const ca = cleanForSort(a.name);
          const cb = cleanForSort(b.name);
          return ca.localeCompare(cb, 'de', { sensitivity: 'base' });
        }
        case 'clickcount':
          return (b.clickcount || 0) - (a.clickcount || 0);
        case 'clicktrend':
          return (b.clicktrend || 0) - (a.clicktrend || 0);
        case 'bitrate':
          return (b.bitrate || 0) - (a.bitrate || 0);
        case 'votes':
        default:
          return (b.votes || 0) - (a.votes || 0);
      }
    });
    renderSearchResults();
    $('loadMoreRow').classList.toggle('hidden', page.length < PAGE_SIZE);
  } catch (e) {
    $('searchResults').innerHTML = '<div class="muted">Fehler: ' + escapeHtml(e.message) + '</div>';
    $('loadMoreRow').classList.add('hidden');
  }
}

// cleanForSort entfernt alles Nicht-Buchstabe / Nicht-Zahl am Anfang
// damit " ABC", "-Best Radio" und "_NDR" nach ihrem ersten ECHTEN
// Zeichen sortiert werden statt nach Leerzeichen / Bindestrich.
// cleanForSort entfernt fuehrende Nicht-Buchstaben/Zahlen (Tab, Space,
// Bindestrich, Punkt, Stern usw) damit "  ABC" und "ABC" gleich
// sortieren. Robust gegen Webview Versionen ohne Unicode Property
// Escapes — wir matchen klassische ASCII Range + erlauben Umlaute.
function cleanForSort(name) {
  const raw = (name || '').toString();
  // Strip fuehrende non-alphanum Zeichen. Wir akzeptieren A-Z, 0-9 und
  // erweiterte Unicode Bereiche (deutsche Umlaute, kyrillisch etc.).
  const stripped = raw.replace(/^[^A-Za-z0-9À-ɏͰ-ӿ]+/, '');
  // Wenn nach dem Strip nichts uebrig ist (Name war nur Symbole), nehmen
  // wir den Original Namen damit der Sender konsistent einsortiert wird
  // statt mit anderen Leer-Strings am Anfang zu klumpen.
  return (stripped || raw).toLowerCase().trim();
}

// isBoseCompatible: schaetzt ob die Box den Stream zuverlaessig
// abspielen kann. Seit Stick Agent Build 0132 laufen alle Streams
// durch /stream/raw — damit fallen die TLS Bedenken weg. Wir checken
// nur noch den Codec:
//   - MP3 / AAC / AACP / MPEG funktionieren
//   - Ogg / Opus / FLAC kann der Bose Player nicht
// Konservativ — bei unbekanntem Codec lassen wir den Sender durch.
function isBoseCompatible(s) {
  const codec = String(s.codec || '').toUpperCase();
  if (!codec) return true; // unbekannt — Box probiert
  return codec === 'MP3' || codec === 'AAC' || codec === 'AACP' || codec === 'MPEG';
}

function renderSearchResults() {
  const res = $('searchResults');
  // Optional Bose Kompatibilitaets Filter clientseitig: HTTPS Streams
  // und exotische Codecs raus damit der User nicht in 502 Fehler laeuft.
  const totalRaw = (state.searchResults || []).length;
  let list = state.searchResults || [];
  if (state.searchOnlyBose) {
    list = list.filter(isBoseCompatible);
  }
  // Counter Zeile aktualisieren — radio-browser liefert kein Gesamt
  // Total bei einer Filter Suche, daher zeigen wir "X angezeigt"
  // und Hinweis dass mehr via "Mehr laden" kommt.
  const cnt = $('searchCount');
  if (cnt) {
    if (list.length === 0) {
      cnt.classList.add('hidden');
    } else {
      const moreHint = totalRaw >= PAGE_SIZE ? ' — weitere Treffer via "mehr laden"' : '';
      const filterHint = state.searchOnlyBose && list.length < totalRaw
        ? ` (${totalRaw - list.length} ausgeblendet wegen Filter)`
        : '';
      cnt.innerHTML = `<b>${list.length}</b> angezeigt${filterHint}${moreHint}`;
      cnt.classList.remove('hidden');
    }
  }
  if (list.length === 0) {
    res.innerHTML = '<div class="muted">' + (state.searchOnlyBose && (state.searchResults || []).length > 0
      ? 'Keine Box kompatiblen Sender in den Treffern. Deaktiviere "nur Box kompatibel" um auch HTTPS Streams zu sehen.'
      : 'Keine Sender gefunden.') + '</div>';
    return;
  }
  res.innerHTML = list.map((s, i) => {
    const flag = flagFromCC(s.countrycode);
    const okClass = s.lastcheckok ? 'ok' : 'bad';
    const okTitle = s.lastcheckok ? 'Sender war beim letzten Check online' : 'Sender war beim letzten Check NICHT erreichbar';
    let trend = '';
    if (s.clicktrend > 0) trend = `<span class="result-trend" title="Trend +${s.clicktrend} Hoerer">&#9650;</span>`;
    else if (s.clicktrend < 0) trend = `<span class="result-trend up-down" title="Trend ${s.clicktrend} Hoerer">&#9660;</span>`;

    const countryDe = translateCountry(s.country);
    const tagChips = translateTags(s.tags).slice(0, 4).map(t => `<span class="tag-pill">${escapeHtml(t)}</span>`).join('');

    const metaBits = [];
    if (countryDe) metaBits.push(escapeHtml(countryDe));
    if (s.bitrate) metaBits.push(`${s.bitrate} kbit/s`);
    if (s.votes)   metaBits.push(`${formatNumber(s.votes)} Stimmen`);

    const logo = `
      <div class="result-logo">
        ${logoImgTag(s, 'fav')}
        ${flag ? `<span class="fav-flag" title="${escapeAttr(s.country || '')}">${flag}</span>` : ''}
      </div>`;
    return `
      <div class="result-row" data-i="${i}">
        ${logo}
        <div class="result-text">
          <div class="result-name">
            <span class="result-online-dot ${okClass}" title="${okTitle}"></span>
            <span class="result-name-text">${escapeHtml(s.name || '(unbenannt)')}</span>
            ${trend}
          </div>
          <div class="result-meta">${metaBits.join(' &middot; ')}</div>
          ${tagChips ? `<div class="result-tag-chips">${tagChips}</div>` : ''}
        </div>
        <div class="result-actions">
          <button class="btn btn-mini play-now" data-i="${i}" title="Sofort spielen">&#9654;</button>
          <button class="btn btn-mini pick" data-i="${i}" title="Auf Taste legen">&#10133;</button>
        </div>
      </div>
    `;
  }).join('');
  res.querySelectorAll('.play-now').forEach(btn => {
    btn.onclick = async (e) => {
      e.stopPropagation();
      const s = list[parseInt(btn.dataset.i, 10)];
      const url = s.url_resolved || s.url;
      const chain = stationLogoChain(s);
      state.nowPlayState = 'BUFFERING_STATE';
      state.nowLocation = url;
      state.nowName = s.name;
      state.nowIcon = chain;
      state.nowUUID = s.stationuuid || '';
      renderPresets();
      try {
        await PlayURL(state.currentBox.host, state.currentBox.port, url, s.name, chain, s.stationuuid || '');
        setTimeout(refreshStatus, 1200);
      } catch (err) {
        state.nowPlayState = '';
        state.nowLocation = '';
        showError(err);
      }
    };
  });
  res.querySelectorAll('.pick').forEach(btn => {
    btn.onclick = (e) => { e.stopPropagation(); openPick(list[parseInt(btn.dataset.i, 10)]); };
  });
}

function openPick(station) {
  $('pickTitle').textContent = 'Sender auf Taste legen';
  $('pickSub').textContent = station.name + (station.bitrate ? ' (' + station.bitrate + ' kbit/s)' : '');
  const grid = $('pickGrid');
  grid.innerHTML = '';
  for (let i = 1; i <= 6; i++) {
    const p = state.presets.find(x => x.slot === i);
    const b = document.createElement('button');
    b.className = 'pick-slot' + (p ? ' has' : '');
    b.innerHTML = '<div class="ps-num">Taste ' + i + '</div><div class="ps-name">' + (p ? escapeHtml(p.name) : '— leer —') + '</div>';
    b.onclick = async () => {
      try {
        const logo = stationLogoChain(station);
        await SetPreset(state.currentBox.host, state.currentBox.port, i, station.name, station.url_resolved || station.url, logo);
        closePick();
        await loadPresets();
        if (station.stationuuid) {
          VoteStation(state.currentBox.host, state.currentBox.port, station.stationuuid).catch(() => {});
        }
        showToast(`Auf Taste ${i} gespeichert: ${station.name}`);
      } catch (err) { showError(err); }
    };
    grid.appendChild(b);
  }
  $('pickModal').classList.remove('hidden');
}
function closePick() { $('pickModal').classList.add('hidden'); }

// ---------- Box Einstellungen View ----------

const ROOM_NAMES = [
  'Wohnzimmer', 'Schlafzimmer', 'Kueche', 'Esszimmer',
  'Bad', 'Arbeitszimmer', 'Buero', 'Kinderzimmer',
  'Gaestezimmer', 'Flur', 'Diele', 'Eingang',
  'Garten', 'Terrasse', 'Balkon', 'Werkstatt',
  'Hobbyraum', 'Keller', 'Dachboden', 'Garage',
];

$('view-settings').innerHTML = `
  <h2>Box Einstellungen</h2>
  <div class="settings-box-switcher">
    <span class="muted small">Einstellungen fuer:</span>
    <select id="settingsBoxSelect"></select>
    <button class="btn-icon" id="settingsRefreshBtn" title="Box Liste neu suchen"><span class="refresh-icon">&#x21bb;</span></button>
  </div>
  <div id="settingsBody">
    <div class="muted">Waehle erst eine Box aus.</div>
  </div>
`;

$('settingsBoxSelect').onchange = () => {
  const id = $('settingsBoxSelect').value;
  const box = state.boxes.find(b => b.deviceID === id);
  if (box) {
    state.settingsBox = box;
    loadBoxSettings();
  }
};
$('settingsRefreshBtn').onclick = async () => {
  $('settingsRefreshBtn').disabled = true;
  await discoverBoxes();
  renderSettingsBoxSelect();
  loadBoxSettings();
  $('settingsRefreshBtn').disabled = false;
};

// uidSuffixFor liefert die letzten 4 Zeichen der Device ID als Suffix
// fuer Name Dopplungs Aufloesung (z.B. "FFD8").
function uidSuffixFor(box) {
  const id = (box && box.deviceID) || '';
  return id.slice(-4).toUpperCase();
}

// ensureWithUID haengt immer den UID Suffix der Box an, damit der User
// sofort sieht dass ein Identifikator angehaengt wurde — beugt Dopplungen
// im Netz vor und ist auch fuer Support nuetzlich.
function ensureWithUID(desired, ownBox) {
  const trimmed = (desired || '').trim();
  if (!trimmed) return trimmed;
  const suffix = uidSuffixFor(ownBox);
  if (!suffix) return trimmed;
  if (trimmed.toUpperCase().endsWith(suffix)) return trimmed;
  return `${trimmed} ${suffix}`;
}

function renderSettingsBoxSelect() {
  const sel = $('settingsBoxSelect');
  if (!state.boxes.length) {
    sel.innerHTML = '<option value="">keine Box gefunden</option>';
    return;
  }
  const target = state.settingsBox || state.currentBox || state.boxes[0];
  if (target) state.settingsBox = target;
  sel.innerHTML = state.boxes.map(b => {
    const label = b.friendlyName || b.name || b.host;
    return `<option value="${escapeAttr(b.deviceID)}">${escapeHtml(label)} (${escapeHtml(b.host)})</option>`;
  }).join('');
  if (state.settingsBox) sel.value = state.settingsBox.deviceID;
}

async function loadBoxSettings() {
  renderSettingsBoxSelect();
  const body = $('settingsBody');
  if (!state.settingsBox) {
    body.innerHTML = `
      <div class="empty-state">
        <div class="empty-state-title">Keine Box im Netzwerk gefunden.</div>
        <div class="empty-state-text">
          Damit die Einstellungen einer Box bearbeitet werden koennen, muss in ihr ein
          vorbereiteter ST Reborn Stick stecken. Lege den Stick zuerst an.
        </div>
        <button class="btn btn-primary btn-mini" id="settingsGoSetup">Zur Stick Einrichtung</button>
      </div>`;
    const go = document.getElementById('settingsGoSetup');
    if (go) go.onclick = () => switchView('setup');
    return;
  }
  // Wenn schon gerenderter Inhalt da ist, nicht ueberschreiben — User
  // soll weiter Werte sehen waehrend wir frische holen. Sonst Hinweis.
  const hasContent = body.querySelector('.settings-section');
  if (!hasContent) {
    body.innerHTML = '<div class="muted">Box Daten werden gelesen...</div>';
  }

  let lastErr = null;
  // Retry-Schleife: bei "connection refused" / "timeout" 2x wiederholen.
  // Beim Umbenennen restartet die Bose Box kurz ihren Webserver, das ist
  // erwartbar transient.
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      const s = await BoxSettings(state.settingsBox.host, state.settingsBox.port);
      // Erfolg: Reconnect Counter + Timer aufloesen
      if (state.settingsReconnect && state.settingsReconnect.timer) {
        clearTimeout(state.settingsReconnect.timer);
      }
      state.settingsReconnect = null;
      renderBoxSettings(s, state.settingsBox);
      return;
    } catch (e) {
      lastErr = e;
      if (attempt < 2 && isTransientBoxError(e)) {
        await sleep(1500);
        continue;
      }
      break;
    }
  }

  // Persistent Reconnect Banner statt wiederkehrendem Toast.
  // Zaehlt verbleibende Versuche herunter, gibt nach 10 auf und zeigt
  // dann eine klare Anleitung was zu tun ist (Strom trennen nach
  // fehlgeschlagenem OTA Update).
  const friendly = friendlySettingsError(lastErr);
  state.settingsReconnect = state.settingsReconnect || { attempts: 0, max: 10 };
  state.settingsReconnect.attempts++;
  const remaining = state.settingsReconnect.max - state.settingsReconnect.attempts;

  if (remaining > 0) {
    const bannerHtml = `
      <div class="reconnect-banner">
        <div>
          <b>Box gerade nicht erreichbar.</b>
          <small>${escapeHtml(friendly)}</small>
          <small>Naechster Versuch in 4 Sekunden &middot; noch ${remaining} Versuche bis zur Aufgabe.</small>
        </div>
      </div>`;
    if (hasContent) {
      // Bestehende Anzeige behalten, Banner oben einblenden
      let existing = body.querySelector('.reconnect-banner');
      if (!existing) {
        body.insertAdjacentHTML('afterbegin', bannerHtml);
      } else {
        existing.outerHTML = bannerHtml;
      }
    } else {
      body.innerHTML = bannerHtml;
    }
    if (state.settingsReconnect.timer) clearTimeout(state.settingsReconnect.timer);
    state.settingsReconnect.timer = setTimeout(loadBoxSettings, 4000);
  } else {
    // Nach 10 Fehlversuchen: aufgeben + klare Anleitung
    state.settingsReconnect = null;
    body.innerHTML = `
      <div class="empty-state">
        <div class="empty-state-title">Box reagiert nicht mehr</div>
        <div class="empty-state-text">
          Mehrere Versuche fehlgeschlagen. Wahrscheinliche Ursache: der Software Agent auf der Box
          ist gestorben (z.B. nach einem Over the Air Update das nicht sauber durchlief).
          <br><br>
          <b>Loesung:</b> Box komplett vom Strom trennen, 10 Sekunden warten, wieder einstecken.
          Sobald die Box im WLAN ist, sollte sie hier wieder auftauchen.
        </div>
        <div class="empty-state-buttons">
          <button class="btn btn-mini" id="settingsRetry">Erneut versuchen</button>
          <button class="btn btn-primary btn-mini" id="settingsBackToBoxes">Box Auswahl</button>
        </div>
      </div>`;
    const r = document.getElementById('settingsRetry');
    if (r) r.onclick = () => { state.settingsReconnect = null; loadBoxSettings(); };
    const b2 = document.getElementById('settingsBackToBoxes');
    if (b2) b2.onclick = () => { state.settingsReconnect = null; switchView('box'); };
  }
}

function isTransientBoxError(err) {
  const s = String(err || '').toLowerCase();
  return s.includes('refused') || s.includes('timeout') ||
         s.includes('deadline') || s.includes('reset') ||
         s.includes('eof') || s.includes('no route');
}

function friendlySettingsError(err) {
  const s = String(err || '');
  if (/refused/i.test(s)) return 'Die Box hat die Verbindung abgelehnt. Beim Umbenennen oder direkt nach dem Aufwachen aus Standby kommt das kurz vor.';
  if (/timeout|deadline/i.test(s)) return 'Die Box antwortet gerade nicht. Eventuell steht sie auf Standby oder ist im WLAN noch nicht wieder da.';
  if (/no such host|no route/i.test(s)) return 'Die Box ist im Netzwerk nicht erreichbar. Pruefe ob sie eingeschaltet und im WLAN ist.';
  return 'Verbindung fehlgeschlagen. ' + s;
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

function renderBoxSettings(s, box) {
  const info = s.info || {};
  const vol = s.volume || {};
  const bass = s.bass || {};
  const net = s.network || {};
  const sources = rollupSources(s.sources || []);
  const wifi = (net.interfaces || []).find(i => i.type === 'WIFI_INTERFACE' && i.state === 'NETWORK_WIFI_CONNECTED');
  const signalLabel = {
    'GOOD_SIGNAL': 'Gut', 'MARGINAL_SIGNAL': 'Mittelmaessig',
    'POOR_SIGNAL': 'Schwach', 'NO_SIGNAL': 'Kein Signal',
  };
  const uid = uidSuffixFor(box);

  $('settingsBody').innerHTML = `
    <div class="settings-section">
      <h3>Name</h3>
      <div class="setting-row">
        <div class="combobox" id="boxNameCombo">
          <input type="text" id="boxNameInput" autocomplete="off"
                 value="${escapeAttr(info.name || '')}"
                 placeholder="Raum eintippen oder aus Liste waehlen" />
          <button type="button" class="combo-toggle" id="boxNameToggle" title="Liste anzeigen">&#9662;</button>
          <ul class="combo-list hidden" id="boxNameList"></ul>
        </div>
        <button class="btn btn-mini" id="boxNameSave">Speichern</button>
      </div>
      <small class="muted small">Erscheint in Bose Apps, mDNS und UPnP Discovery. Eine Box-ID (${escapeHtml(uid || '----')}) wird beim Speichern automatisch angehaengt, damit es keine Namens Dopplungen im Netz gibt.</small>
    </div>

    <div class="settings-section">
      <h3>Lautstaerke</h3>
      <div class="setting-row">
        <input type="range" id="boxVolume" min="0" max="100" value="${vol.actual || 0}" />
        <span class="setting-value" id="boxVolumeVal">${vol.actual || 0}</span>
      </div>
      ${vol.muted ? '<small class="muted small">Aktuell stummgeschaltet</small>' : ''}
    </div>

    <div class="settings-section">
      <h3>Bass</h3>
      <div class="setting-row">
        <input type="range" id="boxBass"
               min="${(bass.min || 0) - (bass.default || 0)}"
               max="${(bass.max || 0) - (bass.default || 0)}"
               step="1"
               value="${(bass.actual || 0) - (bass.default || 0)}"
               ${bass.available ? '' : 'disabled'} />
        <span class="setting-value" id="boxBassVal">${formatRel((bass.actual || 0) - (bass.default || 0))}</span>
        <button class="btn btn-mini" id="boxBassReset" title="Auf Werks Einstellung zuruecksetzen">Reset</button>
      </div>
      <small class="muted small">0 ist die Werks Einstellung. Negative Werte machen den Bass leiser, positive heben ihn an (Geraet abhaengig).</small>
    </div>

    <div class="settings-section">
      <h3>WLAN</h3>
      ${wifi ? `
        <div class="kv-row"><span class="kv-key">SSID</span><span class="kv-val">${escapeHtml(wifi.ssid || '-')}</span></div>
        <div class="kv-row"><span class="kv-key">IP Adresse</span><span class="kv-val">${escapeHtml(wifi.ipAddress || '-')}</span></div>
        <div class="kv-row"><span class="kv-key">Signal</span><span class="kv-val">${escapeHtml(signalLabel[wifi.signal] || wifi.signal || '-')}</span></div>
        <div class="kv-row"><span class="kv-key">Frequenz</span><span class="kv-val">${wifi.frequencyKHz ? (wifi.frequencyKHz/1000).toFixed(0) + ' MHz' : '-'}</span></div>
      ` : '<div class="muted small">Box ist nicht im WLAN verbunden.</div>'}
      <button class="btn btn-mini" id="wlanSwitchToggle" style="margin-top:8px">Anderes WLAN konfigurieren</button>
      <div id="wlanSwitchForm" class="hidden" style="margin-top:8px">
        <div class="wlan-row">
          <select id="boxWlanSelect"><option value="">- WLAN auswaehlen oder unten eintippen -</option></select>
          <button class="btn btn-icon-sm" id="boxWlanRefresh" title="WLAN Liste vom PC neu laden">&#x21bb;</button>
        </div>
        <input type="text" id="boxWlanSSID" placeholder="WLAN Name (SSID)" />
        <div class="wlan-row">
          <input type="password" id="boxWlanPass" placeholder="WLAN Passwort (leer fuer offenes WLAN)" />
          <button class="btn btn-icon-sm" id="boxWlanShowPass" title="Passwort anzeigen">&#128065;</button>
        </div>
        <button class="btn btn-danger btn-mini" id="boxWlanSave">Box auf dieses WLAN umschalten</button>
        <small class="muted small">Achtung: bei falschem Passwort verliert die Box die Netzverbindung. Wiederherstellung nur ueber Stick neu bestuecken oder Werks Reset.</small>
      </div>
    </div>

    <div class="settings-section">
      <h3>Quellen</h3>
      <div class="sources-grid">
        ${sources.map(src => {
          const cls = src.status === 'READY' ? 'src-ok' : 'src-unav';
          const label = (SOURCE_LABEL[src.source] || src.source);
          const statusLabel = src.status === 'READY' ? 'aktiv' : 'inaktiv';
          return `<div class="source-pill ${cls}" title="${escapeAttr(SOURCE_HINT[src.source] || src.sourceAccount || '')}">${escapeHtml(label)} <small>${statusLabel}</small></div>`;
        }).join('')}
      </div>
      <small class="muted small">Spotify Connect ohne Bose Cloud aktuell nicht aktivierbar. Implementierung via Spotify Web API folgt.</small>
      ${sources.some(x => x.source === 'AIRPLAY' && x.status !== 'READY') ? '<small class="muted small">AirPlay 2 ist hardwareseitig da, wird aber erst durch Bose Setup mit Cloud Account aktiviert. Wenn die Box vorher nie mit einem Bose Konto verbunden war, bleibt es inaktiv.</small>' : ''}
    </div>

    <div class="settings-section">
      <h3>Region</h3>
      <div class="kv-row"><span class="kv-key">Aktuell</span><span class="kv-val" id="currentAppRegion">wird geladen...</span></div>
      <div class="setting-row">
        <select id="appRegionSelect"></select>
        <button class="btn btn-mini" id="appRegionSave">Speichern</button>
      </div>
      <small class="muted small">Wird fuer Default Land der Radio Suche und Sprach Filter benutzt. Aenderung greift sofort und wird auf dem Stick gespeichert.</small>
    </div>
    <div class="settings-section" id="stickInfoSection">
      <h3>Status</h3>
      <div id="stickInfoBody"><span class="muted small">wird geladen...</span></div>
    </div>
    <div class="settings-section">
      <h3>Aktionen</h3>
      <p class="muted small">Box neu starten: noetig wenn du den Stick mit neuen Setup Daten in eine laufende Box gesteckt hast. Hardware Tasten reparieren: synct alle Speichertasten erneut an die Box, falls die physischen Tasten 1-6 nicht reagieren.</p>
      <div class="setting-row">
        <button class="btn btn-mini" id="boxSyncPresetsBtn">Hardware Tasten reparieren</button>
        <button class="btn btn-mini btn-danger" id="boxRebootBtn">Box neu starten</button>
      </div>
    </div>
    <div class="settings-section">
      <h3>Box Info</h3>
      <div class="kv-row"><span class="kv-key">Modell</span><span class="kv-val">${escapeHtml(info.type || '-')}</span></div>
      <div class="kv-row"><span class="kv-key">Firmware</span>
        <span class="kv-val">${fwStatusInline(info)}</span>
      </div>
      <div class="kv-row"><span class="kv-key">Device ID</span><span class="kv-val small muted">${escapeHtml(info.deviceID || '-')}</span></div>
      ${fwUpdateHint(info)}
    </div>
  `;

  // Combobox fuer Namen: Input + Dropdown List, frei tippbar + filterbar
  wireCombobox('boxNameInput', 'boxNameToggle', 'boxNameList', ROOM_NAMES);

  // WLAN Switch UI wire — collapsible Form, vom PC bekannte SSIDs als
  // Dropdown mit Auto Passwort Befuellung, Save schickt PUT /api/box/wlan
  wireWlanSwitch(box);

  // FW Update Button neben der Versionszeile scrollt zum Update Banner
  const fwBtn = $('fwUpdateBtn');
  if (fwBtn) {
    fwBtn.onclick = () => {
      const banner = $('fwUpdateBanner');
      if (banner) banner.scrollIntoView({ behavior: 'smooth', block: 'center' });
    };
  }

  // Status Block: Software Version + USB Stick Mount
  (async () => {
    const body = $('stickInfoBody');
    if (!body) return;
    const app = state.appInfo || {};

    // Software Version: drei Stufen
    //   - Version + Build gleich     → aktuell (gruen)
    //   - Version gleich, Build neu  → Update verfuegbar (gelb)
    //   - Version unterschiedlich    → veraltet (rot)
    let softwareLine = '<span class="muted small">unbekannt</span>';
    let softwareBtn = '';
    try {
      const v = await BoxAgentVersion(box.host, box.port);
      const boxVer = v.version || '?';
      const boxBuild = v.build || '';
      const appVer = app.version || '?';
      const appBuild = app.build || '';
      const sameVer = boxVer === appVer;
      const sameBuild = boxBuild === appBuild;
      if (sameVer && sameBuild) {
        const buildSuffix = boxBuild ? ` (Build ${escapeHtml(boxBuild)})` : '';
        softwareLine = `<span class="fw-ok">&#10003; aktuell</span> <span class="muted small">${escapeHtml(boxVer)}${buildSuffix}</span>`;
      } else if (sameVer && !sameBuild) {
        softwareLine = `<span class="fw-pending">Update verfuegbar</span> <span class="muted small">${escapeHtml(boxBuild)} &rarr; ${escapeHtml(appBuild)}</span>`;
        softwareBtn = `<button class="btn btn-mini btn-primary" id="stickInfoUpdateBtn">Aktualisieren</button>`;
      } else {
        softwareLine = `<span class="fw-old">veraltet</span> <span class="muted small">${escapeHtml(boxVer)} &rarr; ${escapeHtml(appVer)}</span>`;
        softwareBtn = `<button class="btn btn-mini btn-primary" id="stickInfoUpdateBtn">Aktualisieren</button>`;
      }
    } catch {}

    // USB Stick Mount Status. Erst /api/stick/status (neuer Agent),
    // sonst /api/debug/state.stick_listing als Fallback fuer aeltere
    // Agent Versionen.
    let stickLine = '<span class="muted small">unbekannt</span>';
    let stickMounted = false;
    let sshOpen = false;
    try {
      const r = await fetch(`http://${box.host}:${box.port}/api/stick/status`);
      const ct = r.headers.get('content-type') || '';
      if (r.ok && ct.includes('json')) {
        const data = await r.json();
        if (data.mounted) {
          stickMounted = true;
          stickLine = `<span class="fw-ok">&#10003; erkannt</span>` + (data.version ? ` <span class="muted small">${escapeHtml(data.version)}</span>` : '');
        } else {
          stickLine = `<span class="fw-old">nicht erkannt</span>`;
        }
        sshOpen = !!data.sshOpen;
      } else {
        // Fallback: debug/state listing
        const rd = await fetch(`http://${box.host}:${box.port}/api/debug/state`);
        if (rd.ok && (rd.headers.get('content-type') || '').includes('json')) {
          const d = await rd.json();
          const listing = d.stick_listing;
          if (Array.isArray(listing) && listing.length > 0 && !String(listing[0]).startsWith('ERR')) {
            stickMounted = true;
            stickLine = `<span class="fw-ok">&#10003; erkannt</span>`;
          } else {
            stickLine = `<span class="fw-old">nicht erkannt</span>`;
          }
        }
      }
    } catch {}

    // Globalen Top Banner toggeln — sshOpen ist die Quelle der Wahrheit
    // (wir pruefen aktiv ob Port 22 listened). Mounted Stick allein
    // reicht nicht, weil ein Stick ohne remote_services Datei den
    // SSH Dienst nicht aktiviert.
    const gb = $('globalSecurityBanner');
    if (gb) {
      if (sshOpen) gb.classList.remove('hidden');
      else gb.classList.add('hidden');
    }

    const securityWarn = sshOpen ? `
      <div class="security-warn">
        <div class="security-warn-title">Empfehlung</div>
        <div class="security-warn-text">
          Stick rausziehen und Box einmal neu starten. Sonst ist die Box im Netzwerk angreifbar.
        </div>
        <button class="btn btn-mini" id="securityRebootBtn">Box jetzt neu starten</button>
      </div>` : '';

    body.innerHTML = `
      <div class="kv-row"><span class="kv-key">Software</span>
        <span class="kv-val">${softwareLine} ${softwareBtn}</span></div>
      <div class="kv-row"><span class="kv-key">USB-Stick</span>
        <span class="kv-val">${stickLine}</span></div>
      ${securityWarn}
    `;
    const ub = $('stickInfoUpdateBtn');
    if (ub) ub.onclick = doBoxUpdate;
    const sb = $('securityRebootBtn');
    if (sb) sb.onclick = async () => {
      const ok = await confirmWarn(
        'Box jetzt neu starten?',
        'Hast du den USB-Stick bereits gezogen? <br><br>' +
        '<b>Stick noch drin</b> &rarr; SSH bleibt nach dem Reboot offen.<br>' +
        '<b>Stick gezogen</b> &rarr; SSH ist nach dem Reboot zu.<br><br>' +
        'Aktuelle Wiedergabe wird unterbrochen.'
      );
      if (!ok) return;
      try {
        await RebootBox(box.host, box.port);
        showToast('Box startet neu. Sie ist gleich wieder verfuegbar.');
        setTimeout(discoverBoxes, 35000);
      } catch (e) { showError(e); }
    };
  })();

  // Hardware Tasten Reparieren
  const syncBtn = $('boxSyncPresetsBtn');
  if (syncBtn) {
    syncBtn.onclick = async () => {
      syncBtn.disabled = true;
      syncBtn.textContent = 'Wird gesynct...';
      try {
        const r = await SyncBoxPresets(box.host, box.port);
        const synced = r && r.synced != null ? r.synced : 0;
        showToast(`Hardware Tasten gesynct (${synced} Speichertasten).`);
      } catch (e) { showError(e); }
      syncBtn.disabled = false;
      syncBtn.textContent = 'Hardware Tasten reparieren';
    };
  }

  // Box Reboot Button
  const rebootBtn = $('boxRebootBtn');
  if (rebootBtn) {
    rebootBtn.onclick = async () => {
      const ok = await confirmWarn(
        'Box neu starten',
        'Die Box wird in 1 Sekunde neu gestartet. Aktuelle Wiedergabe wird unterbrochen. Fortfahren?'
      );
      if (!ok) return;
      try {
        await RebootBox(box.host, box.port);
        showToast('Box startet neu. Sie ist gleich wieder verfuegbar.');
        // Box ist ~30s weg, dann discover wieder
        setTimeout(discoverBoxes, 35000);
      } catch (e) { showError(e); }
    };
  }

  // App Region dropdown fuellen + aktuelle Region selektieren
  const regSel = $('appRegionSelect');
  if (regSel) {
    regSel.innerHTML = COUNTRIES.filter(c => c.cc).map(c =>
      `<option value="${c.cc}">${flagFromCC(c.cc)} ${escapeHtml(c.name)}</option>`
    ).join('');
    const updateCurrentDisplay = (cc) => {
      const el = $('currentAppRegion');
      if (!el) return;
      const c = COUNTRIES.find(x => x.cc === cc);
      el.innerHTML = c
        ? `${flagFromCC(cc)} ${escapeHtml(c.name)} (${escapeHtml(cc)})`
        : escapeHtml(cc || 'unbekannt');
    };
    fetch(`http://${box.host}:${box.port}/api/region`).then(r => r.ok ? r.json() : null).then(data => {
      if (data && data.country) {
        regSel.value = data.country;
        updateCurrentDisplay(data.country);
      }
    }).catch(() => {});
    $('appRegionSave').onclick = async () => {
      const cc = regSel.value;
      try {
        const r = await fetch(`http://${box.host}:${box.port}/api/region`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ country: cc }),
        });
        if (!r.ok) throw new Error('HTTP ' + r.status);
        const data = await r.json();
        try { localStorage.removeItem('userTouchedRegion'); } catch {}
        state.searchCountry = data.country;
        state.searchLang = data.language;
        const cs = $('searchCountry');
        if (cs) cs.value = data.country;
        updateFilterIndicators();
        updateCurrentDisplay(data.country);
        showToast('Region gespeichert: ' + (COUNTRIES.find(c => c.cc === cc) || {}).name);
      } catch (e) { showError(e); }
    };
  }

  // Wire handlers — alle Operationen gehen explizit gegen settingsBox (nicht currentBox)
  $('boxNameSave').onclick = async () => {
    const desired = $('boxNameInput').value.trim();
    if (!desired) return;
    const finalName = ensureWithUID(desired, box);
    try {
      await SetBoxName(box.host, box.port, finalName);
      showToast('Box Name gespeichert: ' + finalName);
      $('boxNameInput').value = finalName;
      // Lokal updaten — sowohl die ausgewaehlte Box als auch den
      // entsprechenden Eintrag in der globalen Boxes Liste, damit alle
      // Dropdowns sofort konsistent sind. KEIN discoverBoxes oder
      // loadBoxSettings hinterher: das wuerde ein Flackern verursachen
      // und ggf. gerade getaetigte Slider Eingaben ueberschreiben.
      box.friendlyName = finalName;
      const idx = state.boxes.findIndex(b => b.deviceID === box.deviceID);
      if (idx >= 0) state.boxes[idx] = { ...state.boxes[idx], friendlyName: finalName };
      // 90s pending Name: ueberlebt einen mDNS Refresh in dem der Stick
      // noch den alten Namen announct.
      state.pendingNames[box.deviceID] = { name: finalName, until: Date.now() + 90000 };
      renderSettingsBoxSelect();
      renderBoxSelect();
    } catch (e) { showError(e); }
  };
  $('boxVolume').oninput = () => { $('boxVolumeVal').textContent = $('boxVolume').value; };
  $('boxVolume').onchange = () => debouncedSetVolume(box);
  $('boxBass').oninput = () => { $('boxBassVal').textContent = formatRel($('boxBass').value); };
  $('boxBass').onchange = () => debouncedSetBass(box, bass.default || 0);
  $('boxBassReset').onclick = async () => {
    $('boxBass').value = 0;
    $('boxBassVal').textContent = formatRel(0);
    try {
      await SetBoxBass(box.host, box.port, bass.default || 0);
    } catch (e) { showError(e); }
  };
}

// wireWlanSwitch verbindet die WLAN Wechsel UI im Settings Tab mit der
// Box. Listet vom PC bekannte WLANs auf (statt manuell eintippen), holt
// Passwort automatisch, schickt PUT /api/box/wlan auf Save.
function wireWlanSwitch(box) {
  const toggle = $('wlanSwitchToggle');
  const form = $('wlanSwitchForm');
  if (!toggle || !form) return;
  toggle.onclick = () => {
    form.classList.toggle('hidden');
    if (!form.classList.contains('hidden')) {
      loadBoxWlanList();
    }
  };
  async function loadBoxWlanList() {
    const sel = $('boxWlanSelect');
    try {
      const profiles = await ListWiFiProfiles() || [];
      sel.innerHTML = '<option value="">- WLAN auswaehlen oder unten eintippen -</option>' +
        profiles.map(p => `<option value="${escapeAttr(p.ssid)}">${escapeHtml(p.ssid)}</option>`).join('');
    } catch {
      sel.innerHTML = '<option value="">- (WLAN Liste nicht verfuegbar) -</option>';
    }
  }
  $('boxWlanRefresh').onclick = loadBoxWlanList;
  $('boxWlanSelect').onchange = async () => {
    const v = $('boxWlanSelect').value;
    if (!v) return;
    $('boxWlanSSID').value = v;
    try {
      const pw = await TryWiFiPassword(v);
      if (pw) $('boxWlanPass').value = pw;
    } catch {}
  };
  $('boxWlanShowPass').onclick = () => {
    const i = $('boxWlanPass');
    const b = $('boxWlanShowPass');
    if (i.type === 'password') { i.type = 'text'; b.innerHTML = '&#128064;'; }
    else { i.type = 'password'; b.innerHTML = '&#128065;'; }
  };
  $('boxWlanSave').onclick = async () => {
    const ssid = $('boxWlanSSID').value.trim();
    const pass = $('boxWlanPass').value;
    if (!ssid) { showError('SSID darf nicht leer sein'); return; }
    const ok = await confirmWarn(
      'WLAN umschalten',
      `Die Box wechselt sofort auf <b>${escapeHtml(ssid)}</b>. Bei falschem Passwort verliert sie die Verbindung. Fortfahren?`
    );
    if (!ok) return;
    try {
      const r = await fetch(`http://${box.host}:${box.port}/api/box/wlan`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ssid, password: pass }),
      });
      if (!r.ok) {
        const t = await r.text();
        throw new Error('HTTP ' + r.status + ': ' + t);
      }
      $('boxWlanPass').value = '';
      form.classList.add('hidden');
      showToast('WLAN umgeschaltet. Box bekommt eine neue IP, gleich Liste neu suchen.');
      // Box bekommt vermutlich neue IP via DHCP — Discovery in 10s neu
      setTimeout(discoverBoxes, 10000);
    } catch (e) { showError(e); }
  };
}

// wireCombobox laesst sich an ein <input>+<button toggle>+<ul list>
// Trio binden. Filter waehrend Tippen, Klick auf Toggle oder Item.
function wireCombobox(inputId, toggleId, listId, options) {
  const input = document.getElementById(inputId);
  const toggle = document.getElementById(toggleId);
  const list = document.getElementById(listId);
  if (!input || !toggle || !list) return;

  function render(filter) {
    const q = (filter || '').toLowerCase().trim();
    const matches = options.filter(o => !q || o.toLowerCase().includes(q));
    if (matches.length === 0) {
      list.innerHTML = '<li class="combo-empty">keine Vorschlaege</li>';
      return;
    }
    list.innerHTML = matches.map(o => `<li data-value="${escapeAttr(o)}">${escapeHtml(o)}</li>`).join('');
    list.querySelectorAll('li[data-value]').forEach(li => {
      li.onmousedown = (e) => {
        // mousedown statt click damit es vor dem blur des Inputs feuert
        e.preventDefault();
        input.value = li.dataset.value;
        list.classList.add('hidden');
      };
    });
  }

  // Beim Aufklappen alle Optionen zeigen, NICHT nach aktuellem Text
  // filtern — sonst sind die Vorschlaege weg sobald der Name nicht zu
  // einem Raum passt (z.B. "Bose SoundTouch 02FFD8"). Filter wird erst
  // beim Tippen aktiv.
  let userIsTyping = false;
  const showAll = () => { render(''); list.classList.remove('hidden'); };
  const showFiltered = () => { render(input.value); list.classList.remove('hidden'); };
  const hide = () => list.classList.add('hidden');

  input.addEventListener('focus', () => {
    if (userIsTyping) showFiltered(); else showAll();
  });
  input.addEventListener('input', () => {
    userIsTyping = true;
    showFiltered();
  });
  input.addEventListener('blur', () => {
    setTimeout(hide, 150);
    userIsTyping = false;
  });
  toggle.addEventListener('mousedown', (e) => {
    e.preventDefault();
    if (list.classList.contains('hidden')) {
      input.focus();
      showAll();
    } else {
      hide();
    }
  });
}

// formatRel zeigt einen relativen Wert mit Vorzeichen: 0, +3, -2.
function formatRel(v) {
  const n = parseInt(v, 10);
  if (isNaN(n) || n === 0) return '0';
  return n > 0 ? '+' + n : String(n);
}

// Letzte bekannte Firmware Major Minor Patch pro SoundTouch Modell.
// Bose hat 2022 die letzte Welle veroeffentlicht, danach kam wegen der
// Cloud Abschaltung nichts mehr. Quelle: support.bose.com.
const LATEST_FW = {
  'SoundTouch 10': '27.0.6',
  'SoundTouch 20': '27.0.6',
  'SoundTouch 30': '27.0.6',
  'SoundTouch Portable': '27.0.6',
};

// fwVersionTuple extrahiert die ersten 3 Zahlen aus "27.0.6.46330.5043500"
// fuer Vergleich. Liefert null wenn unbekanntes Format.
function fwVersionTuple(v) {
  if (!v) return null;
  const m = v.match(/^(\d+)\.(\d+)\.(\d+)/);
  if (!m) return null;
  return [parseInt(m[1]), parseInt(m[2]), parseInt(m[3])];
}

function isFwOutdated(info) {
  const want = LATEST_FW[info.type || ''];
  const have = fwVersionTuple(info.version || '');
  const wantT = fwVersionTuple(want || '');
  if (!have || !wantT) return false;
  for (let i = 0; i < 3; i++) {
    if (have[i] < wantT[i]) return true;
    if (have[i] > wantT[i]) return false;
  }
  return false;
}

// fwStatusInline rendert die Firmware Zeile mit Vorzeichen.
//   Aktuell: gruene Schrift + Haken
//   Veraltet: rote Schrift + Update Button daneben
function fwStatusInline(info) {
  const v = escapeHtml(info.version || '-');
  if (!info.version) return v;
  if (isFwOutdated(info)) {
    return `<span class="fw-old">${v}</span> <button class="btn btn-mini btn-danger" id="fwUpdateBtn">Update</button>`;
  }
  return `<span class="fw-ok">&#10003; ${v}</span>`;
}

function fwUpdateHint(info) {
  const want = LATEST_FW[info.type || ''];
  if (!isFwOutdated(info)) {
    return `<small class="muted small">Aktuell. Bose stellt seit der Cloud Abschaltung keine neuen Updates mehr bereit. Letzte Version ${escapeHtml(want || '27.0.6')} von Sep 2022.</small>`;
  }
  return `
    <div class="fw-update-banner" id="fwUpdateBanner">
      <b>Firmware veraltet.</b>
      <div>
        Die letzte offizielle Bose Firmware fuer dein Modell ist <b>${escapeHtml(want || '27.0.6')}</b> (Sep 2022). Damit funktioniert der Stick zuverlaessig.
      </div>
      <div class="fw-update-howto">
        <b>So aktualisierst du:</b>
        <ol>
          <li>Box muss im WLAN sein. Stick darf dabei drinstecken.</li>
          <li>Bose SoundTouch App vom Smartphone oeffnen (App Store / Play Store, falls noch installiert).</li>
          <li>Box dort auswaehlen. Falls ein Update verfuegbar ist, wird es angeboten.</li>
          <li>Alternative ohne App: USB Update Tool von Bose laden, FW auf einen anderen Stick schreiben und in die Box stecken: <span class="kv-val">downloads.bose.com/ced/soundtouch/soundtouch_usb/</span></li>
        </ol>
        <small class="muted small">Hinweis: Bose Update Server koennten je nach Region noch online sein, auch wenn die Cloud Streaming abgeschaltet ist. Falls der App Weg nicht klappt, ist das USB Tool der zuverlaessigste Weg.</small>
      </div>
    </div>`;
}

// formatCountryCode wandelt ISO Codes in lesbare Namen.
const COUNTRY_CODE_DE = {
  DE: 'Deutschland', AT: 'Oesterreich', CH: 'Schweiz',
  GB: 'Vereinigtes Koenigreich', US: 'USA', FR: 'Frankreich',
  IT: 'Italien', ES: 'Spanien', NL: 'Niederlande', BE: 'Belgien',
  PL: 'Polen', SE: 'Schweden', DK: 'Daenemark', NO: 'Norwegen',
  FI: 'Finnland', IE: 'Irland', CZ: 'Tschechien', PT: 'Portugal',
  CA: 'Kanada', AU: 'Australien', JP: 'Japan',
};
function formatCountryCode(cc) {
  if (!cc) return '-';
  return COUNTRY_CODE_DE[cc.toUpperCase()] ? `${COUNTRY_CODE_DE[cc.toUpperCase()]} (${cc})` : cc;
}

const SOURCE_LABEL = {
  AUX: 'Aux Eingang', AIRPLAY: 'AirPlay 2', BLUETOOTH: 'Bluetooth',
  SPOTIFY: 'Spotify Connect', QPLAY: 'QPlay', UPNP: 'UPnP',
  STORED_MUSIC_MEDIA_RENDERER: 'Mediathek',
  ALEXA: 'Alexa',
};
const SOURCE_HINT = {
  AUX: 'Audio Klinke Eingang',
  AIRPLAY: 'Apple AirPlay 2. Aktivierung haengt am Bose Setup Flow.',
  BLUETOOTH: 'Bluetooth Geraet koppeln',
  SPOTIFY: 'Spotify Connect. Ohne Bose Cloud aktuell nicht aktiv.',
  QPLAY: 'QPlay (chinesischer Markt)',
  UPNP: 'UPnP Streaming. Wird von uns aktiv genutzt.',
  STORED_MUSIC_MEDIA_RENDERER: 'Lokale Mediathek im LAN',
  ALEXA: 'Amazon Alexa Integration',
};

// rollupSources entfernt NOTIFICATION (intern), fasst mehrere Eintraege
// gleichen Source Typs zu einer Pille zusammen und priorisiert READY
// ueber UNAVAILABLE. Anschliessend READY zuerst sortiert.
function rollupSources(raw) {
  const grouped = {};
  for (const src of raw) {
    if (!src || !src.source) continue;
    if (src.source === 'NOTIFICATION') continue;
    const existing = grouped[src.source];
    if (!existing || (src.status === 'READY' && existing.status !== 'READY')) {
      grouped[src.source] = src;
    }
  }
  return Object.values(grouped).sort((a, b) => {
    if (a.status === 'READY' && b.status !== 'READY') return -1;
    if (a.status !== 'READY' && b.status === 'READY') return 1;
    return (SOURCE_LABEL[a.source] || a.source).localeCompare(SOURCE_LABEL[b.source] || b.source);
  });
}

const debouncedSetVolume = debounce(async (box) => {
  try {
    await SetBoxVolume(box.host, box.port, parseInt($('boxVolume').value, 10));
  } catch (e) { showError(e); }
}, 200);
const debouncedSetBass = debounce(async (box, defaultBass) => {
  try {
    // Slider liefert relativen Wert (0 = Werks Default). Box erwartet
    // den absoluten Wert, also Default Offset wieder addieren.
    const rel = parseInt($('boxBass').value, 10);
    await SetBoxBass(box.host, box.port, rel + (defaultBass || 0));
  } catch (e) { showError(e); }
}, 200);

function debounce(fn, ms) {
  let t = null;
  return (...args) => {
    if (t) clearTimeout(t);
    t = setTimeout(() => fn(...args), ms);
  };
}

// ---------- Setup View ----------

$('view-setup').innerHTML = `
  <h2>USB-Stick vorbereiten</h2>
  <p class="setup-intro">Stecke einen USB-Stick (mindestens 4 GB) in den Rechner. Die App findet ihn automatisch. Waehle ihn aus und klicke auf den Knopf unten. Danach wird der Stick automatisch ausgeworfen, und du steckst ihn in die Bose Box.</p>
  <div class="setup-section">
    <div class="setup-section-head">
      <h3>1. USB-Stick auswaehlen</h3>
      <button class="btn btn-mini" id="drivesRefresh">Neu suchen</button>
    </div>
    <div id="drivesList">Suche nach USB-Sticks...</div>
    <label class="format-toggle">
      <input type="checkbox" id="setupFormat" />
      <span>Stick zuerst neu formatieren (FAT32, alle bisherigen Daten gehen verloren). <b>Stark empfohlen</b> wenn der Stick neu ist oder nicht zuverlaessig erkannt wird. Windows fragt einmal nach Administrator Rechten.</span>
    </label>
  </div>
  <div class="setup-section" id="nameSection">
    <h3>2. Box Name <small>(optional)</small></h3>
    <p class="muted small">Wie soll deine Box heissen? Erscheint in mDNS, UPnP und Bose Apps. Tippe selbst oder waehle einen Raum. Eine Box-ID wird automatisch angehaengt damit es keine Dopplungen gibt.</p>
    <div class="combobox" id="setupNameCombo">
      <input type="text" id="setupName" autocomplete="off" placeholder="z.B. Wohnzimmer" />
      <button type="button" class="combo-toggle" id="setupNameToggle">&#9662;</button>
      <ul class="combo-list hidden" id="setupNameList"></ul>
    </div>
  </div>
  <div class="setup-section" id="regionSection">
    <h3>3. Region</h3>
    <p class="muted small">Setzt Land und Sprache als Vorauswahl in der Radio Suche. Pro Suche aenderbar.</p>
    <select id="setupRegion"></select>
  </div>
  <div class="setup-section" id="wlanSection">
    <h3>4. WLAN <small>(optional)</small></h3>
    <p class="muted small">Wenn die Box noch nicht im WLAN ist, kannst du sie hier konfigurieren. Beim ersten Booten verbindet die Box sich automatisch. Daten landen nur auf dem Stick und werden nach Provisionierung geloescht.</p>
    <div class="wlan-row">
      <select id="wlanSelect">
        <option value="">- WLAN auswaehlen oder unten eintippen -</option>
      </select>
      <button class="btn btn-icon-sm" id="wlanRefresh" title="WLAN Liste neu laden">&#x21bb;</button>
    </div>
    <input type="text" id="wlanSsid" placeholder="WLAN Name (SSID)" />
    <div class="wlan-row">
      <input type="password" id="wlanPass" placeholder="WLAN Passwort" />
      <button class="btn btn-icon-sm" id="wlanShowPass" title="Passwort anzeigen">&#128065;</button>
    </div>
  </div>
  <div class="setup-section">
    <h3>5. Vorbereiten</h3>
    <div id="updateInfo" class="update-info hidden"></div>
    <div id="formatWarn" class="format-warn hidden">
      <div class="warn-icon-inline">&#9888;</div>
      <div>
        <b>Achtung:</b> Auf diesem Stick ist noch kein STR installiert. Beim Vorbereiten werden <b>alle bestehenden Daten geloescht</b>.
      </div>
    </div>
    <button class="btn btn-primary" id="setupGo" disabled>USB-Stick vorbereiten</button>
    <div id="setupResult" class="setup-result"></div>
  </div>
`;

// Setup Box Name Combobox wiren — gleicher Helper wie im Settings Tab
wireCombobox('setupName', 'setupNameToggle', 'setupNameList', ROOM_NAMES);

// Region Dropdown im Setup mit den gleichen Laendern wie der Radio Filter,
// Default Deutschland.
(function fillSetupRegion() {
  const sel = $('setupRegion');
  if (!sel) return;
  sel.innerHTML = COUNTRIES.filter(c => c.cc).map(c =>
    `<option value="${c.cc}">${flagFromCC(c.cc)} ${escapeHtml(c.name)}</option>`
  ).join('');
  const saved = (() => { try { return localStorage.getItem('setupRegion'); } catch { return null; }})();
  sel.value = saved || 'DE';
})();

$('drivesRefresh').onclick = () => refreshDrives(true);
$('setupGo').onclick = doSetup;
$('wlanRefresh').onclick = loadWifiProfiles;
$('wlanSelect').onchange = onWifiSelect;
$('wlanShowPass').onclick = togglePasswordVisibility;

function togglePasswordVisibility() {
  const input = $('wlanPass');
  const btn = $('wlanShowPass');
  if (input.type === 'password') {
    input.type = 'text';
    btn.innerHTML = '&#128064;';
    btn.title = 'Passwort verbergen';
  } else {
    input.type = 'password';
    btn.innerHTML = '&#128065;';
    btn.title = 'Passwort anzeigen';
  }
}

async function loadWifiProfiles() {
  const sel = $('wlanSelect');
  try {
    const profiles = await ListWiFiProfiles() || [];
    sel.innerHTML = '<option value="">- WLAN auswaehlen oder unten eintippen -</option>' +
      profiles.map(p => `<option value="${escapeAttr(p.ssid)}">${escapeHtml(p.ssid)}</option>`).join('');
    try {
      const current = await CurrentWiFi();
      if (current && profiles.some(p => p.ssid === current)) {
        sel.value = current;
        onWifiSelect();
      }
    } catch {}
  } catch {
    sel.innerHTML = '<option value="">- (WLAN Liste nicht verfuegbar) -</option>';
  }
}

async function onWifiSelect() {
  const v = $('wlanSelect').value;
  if (!v) return;
  $('wlanSsid').value = v;
  try {
    const pw = await TryWiFiPassword(v);
    if (pw) $('wlanPass').value = pw;
  } catch {}
}

async function prefillWizardFromStick(path) {
  try {
    const c = await StickConfigs(path);
    if (!c) return;
    if (c.region) {
      const r = $('setupRegion');
      if (r) r.value = c.region;
    }
    if (c.name) {
      const n = $('setupName');
      if (n && !n.value) n.value = c.name;
    }
    if (c.wlanSSID) {
      const s = $('wlanSsid');
      if (s && !s.value) s.value = c.wlanSSID;
    }
    if (c.wlanPass) {
      const p = $('wlanPass');
      if (p && !p.value) p.value = c.wlanPass;
    }
  } catch {}
}

async function refreshDrives(clearResult) {
  state.selectedDrive = null;
  try {
    state.drives = await ListDrives() || [];
    // Erfolgsmeldung nur loeschen wenn der User AKTIV neu gesucht hat
    // (Button Klick). Beim automatischen Refresh nach Setup soll die
    // Erfolgsmeldung sichtbar bleiben.
    if (clearResult) {
      const res = $('setupResult');
      if (res) res.innerHTML = '';
    }
    renderDrives();
  } catch (e) {
    $('drivesList').textContent = 'Fehler: ' + e;
  }
}

async function renderDrives() {
  if (!state.drives.length) {
    $('drivesList').innerHTML = '<div class="muted">Keine USB-Sticks gefunden. Stecke einen ein (mindestens 4 GB) und klicke auf "Neu suchen".</div>';
    $('setupGo').disabled = true;
    $('updateInfo').classList.add('hidden');
    $('formatWarn').classList.add('hidden');
    return;
  }
  $('drivesList').innerHTML = state.drives.map((d, i) => {
    const gb = (d.totalBytes / (1024*1024*1024)).toFixed(1);
    const fs = (d.filesystem || '').toUpperCase();
    const isFat32 = fs === 'FAT32';
    const has = d.hasStick ? '<span class="badge">STR</span>' : '';
    const fsBadge = !isFat32 ? ` <span class="badge badge-warn">${escapeHtml(fs || 'unbekannt')} – muss formatiert werden</span>` : '';
    const active = state.selectedDrive === i ? ' active' : '';
    return `<div class="drive-row${active}" data-i="${i}">
      <div class="drive-info">
        <div class="drive-path"><b>${escapeHtml(d.path)}</b> ${has}${fsBadge}</div>
        <div class="drive-meta">${escapeHtml(d.label || '')} &middot; ${gb} GB &middot; ${escapeHtml(d.filesystem)}</div>
      </div>
    </div>`;
  }).join('');
  document.querySelectorAll('.drive-row').forEach(el => {
    el.onclick = async () => {
      state.selectedDrive = parseInt(el.dataset.i, 10);
      await renderDrives();
      await updateDrivePanels();
    };
  });
  if (state.selectedDrive == null && state.drives.length === 1) {
    state.selectedDrive = 0;
    await renderDrives();
    await updateDrivePanels();
  } else {
    await updateDrivePanels();
  }
}

async function updateDrivePanels() {
  const drive = state.drives[state.selectedDrive];
  const btn = $('setupGo');
  const upd = $('updateInfo');
  const warn = $('formatWarn');
  if (!drive) {
    btn.disabled = true;
    btn.textContent = 'USB-Stick vorbereiten';
    upd.classList.add('hidden');
    warn.classList.add('hidden');
    return;
  }
  btn.disabled = false;
  // Wenn der Stick noch unverbrauchte Setup Configs hat (User hat den
  // Stick noch nicht in die Box gesteckt), Wizard Felder daraus
  // vorbefuellen.
  prefillWizardFromStick(drive.path);
  const isFat32 = (drive.filesystem || '').toUpperCase() === 'FAT32';

  // Wenn der Stick nicht FAT32 ist: Format Checkbox automatisch
  // aktivieren + visuelle Warnung. hasStick gilt sowieso nur fuer
  // FAT32 — Bose Box liest nichts anderes.
  if (!isFat32) {
    const cb = $('setupFormat');
    if (cb && !cb.checked) cb.checked = true;
    upd.innerHTML =
      `<b>Stick ist ${escapeHtml(drive.filesystem || 'unbekannt')} formatiert.</b> ` +
      `<div class="muted small" style="margin-top:6px">Die Bose Box liest nur FAT32. Der Stick wird beim Vorbereiten neu formatiert (Windows fragt einmal nach Administrator Rechten). Alle Daten auf dem Stick gehen verloren.</div>`;
    upd.classList.remove('hidden');
    warn.classList.add('hidden');
    btn.textContent = 'USB-Stick formatieren und vorbereiten';
    return;
  }

  if (drive.hasStick) {
    try {
      const fromFull = (await StickVersion(drive.path) || '').trim();
      const appVer = state.appInfo ? state.appInfo.version : '';
      const appBld = state.appInfo ? (state.appInfo.build || '') : '';
      const toFull = appBld && appBld !== 'dev' ? `${appVer}+${appBld}` : appVer;
      // version.txt hat Format "1.0.0" oder "1.0.0+2026-05-15-2202".
      // Vergleich strict — gleiche Version aber neuer Build = Update.
      const same = fromFull === toFull;
      const fromShort = fromFull || 'unbekannt';
      upd.innerHTML = (same
        ? `<b>Stick ist aktuell.</b> <small>Version ${escapeHtml(fromShort)}</small>`
        : `<b>Aktualisierung verfuegbar.</b> <small>${escapeHtml(fromShort)} &rarr; ${escapeHtml(toFull)}</small>`)
        + ` <div class="muted small" style="margin-top:6px">Bereits konfigurierter Stick. Wenn du ihn erneut benutzen willst (z.B. andere Box, neue WLAN Daten oder neuer Name), wird die Konfiguration unten beim Speichern neu auf den Stick geschrieben.</div>`;
      upd.classList.remove('hidden');
    } catch {
      upd.classList.add('hidden');
    }
    warn.classList.add('hidden');
    btn.textContent = 'USB-Stick aktualisieren';
  } else {
    upd.classList.add('hidden');
    warn.classList.remove('hidden');
    btn.textContent = 'USB-Stick vorbereiten';
  }
}

async function doSetup() {
  const drive = state.drives[state.selectedDrive];
  if (!drive) return;
  const isFat32 = (drive.filesystem || '').toUpperCase() === 'FAT32';
  const wantFormat = $('setupFormat') && $('setupFormat').checked;
  // Box liest nur FAT32. Wenn der Stick NTFS/exFAT ist und der User
  // die Format Option NICHT aktiviert hat, ist Weiterschreiben
  // sinnlos — wir blocken und erklaeren das.
  if (!isFat32 && !wantFormat) {
    $('setupResult').innerHTML =
      '<div class="setup-err"><b>Stick ist ' + escapeHtml(drive.filesystem || 'unbekannt') + ' formatiert.</b> ' +
      'Die Bose Box liest nur FAT32 — bitte oben die Option <b>Stick formatieren (FAT32)</b> aktivieren und nochmal auf Vorbereiten klicken.</div>';
    return;
  }
  if (!drive.hasStick && !wantFormat) {
    const ok = await confirmWarn(
      'Stick wird geloescht',
      `Auf <b>${escapeHtml(drive.path)}</b> sind <b>keine STR Daten</b>. Beim Vorbereiten werden <b>alle bestehenden Daten unwiderruflich geloescht</b>. Wirklich fortfahren?`
    );
    if (!ok) return;
  }
  $('setupGo').disabled = true;
  $('setupResult').innerHTML = wantFormat
    ? '<div class="muted">Stick wird formatiert (FAT32)...</div>'
    : '<div class="muted">Stick wird vorbereitet...</div>';
  try {
    let formatLine = '';
    if (wantFormat) {
      try {
        $('setupResult').innerHTML = '<div class="muted">Stick wird formatiert (FAT32). Windows zeigt gleich ein Administrator Fenster, bitte mit "Ja" bestaetigen...</div>';
        await FormatStick(drive.path);
        formatLine = '<div class="setup-ok">Format erfolgreich (FAT32).</div>';
        $('setupResult').innerHTML = formatLine + '<div class="muted">Stick wird vorbereitet...</div>';
        await sleep(1500);
        state.drives = await ListDrives() || [];
        const fresh = state.drives.find(d => d.path === drive.path);
        if (!fresh) {
          $('setupResult').innerHTML = formatLine + '<div class="setup-warn">Stick nach Format nicht mehr unter ' + escapeHtml(drive.path) + ' sichtbar. Bitte in der Liste oben nochmal auswaehlen.</div>';
          $('setupGo').disabled = false;
          refreshDrives();
          return;
        }
      } catch (fErr) {
        $('setupResult').innerHTML = '<div class="setup-err">Format fehlgeschlagen: ' + escapeHtml(String(fErr)) + '</div>';
        $('setupGo').disabled = false;
        return;
      }
    }
    const written = await WriteStickFiles(drive.path);
    let html = formatLine + `<div class="setup-ok">Stick vorbereitet (${written.length} Eintraege gespeichert).</div>`;
    const region = $('setupRegion').value || 'DE';
    try {
      await WriteRegionConfig(drive.path, region);
      try { localStorage.setItem('setupRegion', region); } catch {}
      html += '<div class="setup-ok">Region gespeichert (' + escapeHtml(region) + ').</div>';
    } catch (regErr) {
      html += '<div class="setup-warn">Region Konfig fehlgeschlagen: ' + escapeHtml(String(regErr)) + '</div>';
    }
    const boxName = $('setupName').value.trim();
    if (boxName) {
      try {
        await WriteNameConfig(drive.path, boxName);
        html += '<div class="setup-ok">Box Name gespeichert (' + escapeHtml(boxName) + ', Box ID wird beim ersten Boot angehaengt).</div>';
      } catch (nErr) {
        html += '<div class="setup-warn">Name Konfig fehlgeschlagen: ' + escapeHtml(String(nErr)) + '</div>';
      }
    }
    const ssid = $('wlanSsid').value.trim();
    const pass = $('wlanPass').value;
    if (ssid) {
      try {
        await WriteWLANConfig(drive.path, ssid, pass);
        html += '<div class="setup-ok">WLAN Konfig gespeichert (' + escapeHtml(ssid) + ').</div>';
        $('wlanPass').value = '';
      } catch (wlanErr) {
        html += '<div class="setup-warn">WLAN Konfig fehlgeschlagen: ' + escapeHtml(String(wlanErr)) + '</div>';
      }
    }
    try {
      $('setupResult').innerHTML = html + '<div class="muted small">Stick wird ausgeworfen...</div>';
      await EjectDrive(drive.path);
      html += '<p>Stick wurde ausgeworfen. Jetzt entnehmen und in den USB Port der Bose Box stecken. Beim ersten Boot dauert es etwa eine Minute, dann kannst du im Tab <b>Musik hoeren</b> die Box bedienen.</p>';
      html += '<p class="setup-warn"><b>Wichtig:</b> Der Stick ist jetzt fuer genau diese Box konfiguriert. Beim ersten Boot wendet die Box die Einstellungen an und loescht alle Geheimnisse (z.B. WLAN Passwort) vom Stick. Wenn du spaeter eine andere Box einrichten oder geaenderte Werte aufspielen willst, musst du den Stick zuerst wieder hier vorbereiten.</p>';
    } catch (ejErr) {
      html += '<p class="setup-warn">Die Daten sind drauf, aber das automatische Auswerfen ging nicht: <small>' + escapeHtml(String(ejErr)) + '</small> Bitte ueber den Explorer "Auswerfen".</p>';
    }
    $('setupResult').innerHTML = html;
    state.selectedDrive = null;
    state.currentBox = null;
    state.presets = [];
    refreshDrives();
    discoverBoxes();
  } catch (e) {
    $('setupResult').innerHTML = '<div class="setup-err">Fehler: ' + escapeHtml(String(e)) + '</div>';
  }
  $('setupGo').disabled = false;
}

// ---------- Modals ----------

let warnResolve = null;
$('warnCancel').onclick = () => { closeWarn(); if (warnResolve) warnResolve(false); };
$('warnConfirm').onclick = () => { closeWarn(); if (warnResolve) warnResolve(true); };
function confirmWarn(title, bodyHtml) {
  $('warnModal').querySelector('.warn-title').innerHTML = '<span class="warn-icon">&#9888;</span> ' + escapeHtml(title);
  $('warnBody').innerHTML = bodyHtml;
  $('warnModal').classList.remove('hidden');
  return new Promise(res => { warnResolve = res; });
}
function closeWarn() { $('warnModal').classList.add('hidden'); }

$('errorClose').onclick = () => $('errorModal').classList.add('hidden');
$('errorCopy').onclick = async () => {
  const txt = $('errorText').value;
  try {
    await navigator.clipboard.writeText(txt);
    $('errorCopy').textContent = 'Kopiert!';
    setTimeout(() => $('errorCopy').textContent = 'Kopieren', 1500);
  } catch {
    $('errorText').select();
    document.execCommand('copy');
  }
};
function showError(msg) {
  $('errorText').value = String(msg || 'Unbekannter Fehler');
  $('errorModal').classList.remove('hidden');
}

let toastTimer = null;
function showToast(msg) {
  const t = $('toast');
  if (!t) return;
  t.textContent = msg;
  t.classList.add('show');
  if (toastTimer) clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show'), 2200);
}

// ---------- Helper ----------

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}
function escapeAttr(s) { return escapeHtml(s); }

// decodeXmlEntities macht aus "Rock &amp; Roll" wieder "Rock & Roll".
// Bose liefert in /now_playing XML escaped — wenn wir den String 1:1
// als Preset Name speichern, kommt er beim naechsten Render doppelt
// escaped raus.
function decodeXmlEntities(s) {
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

renderFooter();

// Zuerst aus dem Cache vorbefuellen damit die UI sofort die zuletzt
// genutzte Box zeigt. discoverBoxes im Hintergrund refreshed die echte
// Liste binnen ein paar Sekunden.
(function bootFromCache() {
  const cached = loadCachedBoxes();
  if (cached.length === 0) return;
  state.boxes = cached;
  const lastID = loadLastBox();
  const target = lastID ? cached.find(b => b.deviceID === lastID) : null;
  if (target) {
    state.currentBox = target;
    renderBoxSelect();
    loadPresets();
    refreshStatus();
    loadTaxonomy();
    loadStickRegion();
  } else {
    renderBoxSelect();
  }
})();

discoverBoxes();
loadWifiProfiles();
setInterval(refreshStatus, 2000);
