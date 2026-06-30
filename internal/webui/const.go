package webui

import (
	_ "embed"
	"time"
)

const shutdownTimeout = 5 * time.Second

// iconPNG is the STR app icon, served at /icon.png for the favicon, the iOS
// apple-touch-icon and the PWA manifest, so a phone that saves the page to its
// home screen gets a proper STR icon.
//
//go:embed assets/icon.png
var iconPNG []byte

// webManifest is the PWA manifest served at /manifest.webmanifest. With it (plus
// the apple-mobile-web-app meta in indexHTML) a phone can "Add to Home Screen"
// and the page opens full-screen as a standalone STR app.
const webManifest = `{
  "name": "ST Reborn",
  "short_name": "STR",
  "description": "Control your Bose SoundTouch speaker",
  "start_url": "/",
  "scope": "/",
  "display": "standalone",
  "orientation": "portrait",
  "background_color": "#1a1a1a",
  "theme_color": "#1a1a1a",
  "icons": [
    { "src": "/icon.png", "sizes": "192x192", "type": "image/png", "purpose": "any" },
    { "src": "/icon.png", "sizes": "192x192", "type": "image/png", "purpose": "maskable" }
  ]
}`

// indexHTML is the self-contained controller page the agent serves on "/". It is
// the phone remote: a mobile-first page (no desktop app needed) that drives the
// box over the same REST API the desktop app uses. It is PWA-capable (save to
// home screen), shows volume + input + presets + transport, links to the other
// STR speakers on the network, and is branded as ST Reborn.
const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="theme-color" content="#1a1a1a">
<meta name="apple-mobile-web-app-capable" content="yes">
<meta name="mobile-web-app-capable" content="yes">
<meta name="apple-mobile-web-app-status-bar-style" content="black-translucent">
<meta name="apple-mobile-web-app-title" content="ST Reborn">
<link rel="manifest" href="/manifest.webmanifest">
<link rel="icon" href="/icon.png">
<link rel="apple-touch-icon" href="/icon.png">
<title>ST Reborn</title>
<style>
:root {
  color-scheme:dark;
  --bg:#1a1a1a; --card:#242424; --card2:#2a2a2a; --line:#3a3a3a; --fg:#eee; --muted:#9e9e9e; --accent:#e88;
  --hover:#333; --press:#3d3d3d; --nowgrad1:#2c2c2c; --nowgrad2:#242424;
}
/* Light theme: the greyscale tokens flip to a light palette and the coral
   accent is darkened so it keeps contrast on white. color-scheme:light also
   makes the native volume slider track render light. */
html.a11y-light {
  color-scheme:light;
  --bg:#f4f4f5; --card:#ffffff; --card2:#ececef; --line:#d4d4d8; --fg:#1a1a1a; --muted:#5a5e66; --accent:#bd3c2c;
  --hover:#e6e6ea; --press:#dadade; --nowgrad1:#ffffff; --nowgrad2:#f0f0f3;
}
/* High-contrast theme: black base, pure-white text and borders, a bright
   accent. Also seeded automatically on first run when the OS asks for more
   contrast (prefers-contrast: more). */
html.a11y-contrast {
  --bg:#000; --card:#000; --card2:#000; --line:#fff; --fg:#fff; --muted:#fff; --accent:#ffe066;
  --hover:#1a1a1a; --press:#333; --nowgrad1:#000; --nowgrad2:#000;
}
/* Text size. The page uses pixel sizes throughout, so scaling root font-size
   would not reach them; zoom scales the whole rendered page (text, controls,
   hit targets) uniformly. */
html.a11y-scale-l  body { zoom:1.15; }
html.a11y-scale-xl body { zoom:1.30; }
* { box-sizing: border-box; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
:focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
html.a11y-contrast :focus-visible { outline-color:#fff; }
/* Pad every edge by the device safe-area inset on top of the base 16px. The
   page is PWA "standalone" with a black-translucent status bar, so on a notch /
   Dynamic Island phone (e.g. iPhone) iOS draws the content behind the status
   bar; without the top inset the header collides with the clock. The insets are
   0 on hardware that has none, so the base 16px is unchanged there. */
body { margin:0; padding:calc(16px + env(safe-area-inset-top)) calc(16px + env(safe-area-inset-right)) calc(16px + env(safe-area-inset-bottom)) calc(16px + env(safe-area-inset-left)); background:var(--bg); color:var(--fg); max-width:620px; margin:0 auto; }
header { display:flex; align-items:center; gap:10px; margin-bottom:14px; }
header img { width:30px; height:30px; border-radius:7px; }
header .brand { font-size:18px; font-weight:700; letter-spacing:.2px; }
header .brand span { color:var(--accent); }
header .dev { margin-left:auto; min-width:0; font-size:12px; color:var(--muted); text-align:right; max-width:42%; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.card { background:var(--card); border:1px solid var(--line); border-radius:10px; padding:12px; margin:12px 0; }
.nowcard { padding:14px 16px; background:linear-gradient(180deg,var(--nowgrad1),var(--nowgrad2)); }
.nowcard .now { display:block; color:var(--accent); font-weight:600; font-size:18px; line-height:1.25; }
.nowcard .st { font-size:13px; color:var(--muted); margin-top:3px; }
.nowcard.loading { opacity:.6; }
@media (prefers-reduced-motion:no-preference) { .nowcard.loading { animation:pulse 1.2s ease-in-out infinite; } @keyframes pulse { 50% { opacity:.3; } } }
.label { font-size:11px; text-transform:uppercase; letter-spacing:.5px; color:var(--muted); margin-bottom:8px; }
.row { display:grid; gap:8px; }
.row.c2 { grid-template-columns:1fr 1fr; }
.row.c3 { grid-template-columns:1fr 1fr 1fr; }
button.btn, a.btn { display:flex; align-items:center; justify-content:center; min-height:44px; background:var(--card2); color:var(--fg); border:1px solid var(--line); border-radius:8px; padding:10px; font-size:14px; cursor:pointer; text-decoration:none; transition:background .15s,border-color .15s,color .15s; }
button.btn.active, a.btn.active { border-color:var(--accent); color:var(--accent); }
button.btn:active { background:var(--press); }
@media (hover:hover) { button.btn:hover, a.btn:hover { background:var(--hover); } .preset:hover { background:var(--hover); } }
.vol { display:flex; align-items:center; gap:12px; }
.vol input[type=range] { flex:1; accent-color:var(--accent); height:44px; }
.vol input[type=range]::-webkit-slider-thumb { -webkit-appearance:none; width:24px; height:24px; border-radius:50%; background:var(--accent); }
.vol input[type=range]::-moz-range-thumb { width:24px; height:24px; border:0; border-radius:50%; background:var(--accent); }
.vol .val { width:36px; text-align:right; font-variant-numeric:tabular-nums; color:var(--fg); }
.grid { display:grid; grid-template-columns:repeat(2,1fr); gap:8px; }
.preset { background:var(--card2); border:1px solid var(--line); border-radius:10px; padding:14px; cursor:pointer; min-height:80px; display:flex; flex-direction:column; justify-content:center; transition:background .15s; }
.preset:active { background:var(--press); }
.preset:focus-visible { outline-offset:-2px; }
.preset .num { font-size:11px; color:var(--muted); }
.preset .name { font-size:15px; font-weight:600; margin-top:4px; }
.preset.empty { color:var(--muted); border-style:dashed; cursor:default; }
.preset.active { border-color:transparent; box-shadow:0 0 0 2px var(--accent) inset; }
.preset.active .num { color:var(--accent); }
#peersCard { display:none; }
.peer { display:flex; align-items:center; gap:8px; }
.peer .dot { width:8px; height:8px; border-radius:50%; background:var(--accent); flex:none; }
.sponsors { display:grid; grid-template-columns:repeat(3,1fr); gap:8px; max-width:340px; margin:0 auto 8px; }
.sponsors a.btn { min-height:40px; font-size:13px; background:transparent; border-color:var(--line); color:var(--muted); font-weight:500; }
@media (hover:hover) { .sponsors a.btn:hover { background:var(--card2); color:var(--fg); } }
footer { margin-top:18px; text-align:center; font-size:12px; color:var(--muted); }
footer .web { display:inline-block; margin-top:4px; color:var(--accent); text-decoration:none; }
footer .web:hover { text-decoration:underline; }
footer .ver { display:block; margin-top:8px; }
footer .hint { display:block; margin-top:6px; color:var(--muted); opacity:.7; }
/* "Aa" display-options menu (text size + theme), pinned to the header's right. */
.a11y { position:relative; flex:none; }
.a11y-trigger { display:inline-flex; align-items:center; min-height:36px; padding:6px 11px; background:var(--card2); color:var(--fg); border:1px solid var(--line); border-radius:8px; font-size:15px; font-weight:700; letter-spacing:-.5px; cursor:pointer; }
.a11y-trigger:active { background:var(--press); }
.a11y-menu { position:absolute; top:calc(100% + 6px); right:0; z-index:50; padding:12px; min-width:228px; background:var(--card); border:1px solid var(--line); border-radius:10px; box-shadow:0 10px 30px rgba(0,0,0,.5); }
.a11y-menu[hidden] { display:none; }
.a11y-group + .a11y-group { margin-top:12px; }
.a11y-glabel { font-size:11px; text-transform:uppercase; letter-spacing:.5px; color:var(--muted); margin-bottom:6px; }
.a11y-seg { display:flex; gap:6px; }
.a11y-seg button { flex:1; min-height:44px; padding:8px 6px; background:var(--card2); color:var(--fg); border:1px solid var(--line); border-radius:8px; font-size:13px; line-height:1.15; cursor:pointer; }
.a11y-seg button[aria-pressed="true"] { border-color:var(--accent); color:var(--accent); font-weight:600; }
@media (hover:hover) { .a11y-trigger:hover, .a11y-seg button:hover { background:var(--hover); } }
/* Respect the OS "reduce motion" setting. */
@media (prefers-reduced-motion:reduce) { *, *::before, *::after { animation-duration:.001ms !important; animation-iteration-count:1 !important; transition-duration:.001ms !important; } }
</style>
<script>
// Display preferences (text size + theme). Applied to <html> here in <head>,
// before the body paints, so the chosen theme/size is in effect on first paint
// with no flash. Stored in localStorage (a phone-local UI preference, like the
// language), so it costs the speaker nothing. On first run the theme is seeded
// from the OS accessibility settings.
(function(){
  var SCALE_KEY='a11yScale', THEME_KEY='a11yTheme';
  function osTheme(){
    try {
      if (window.matchMedia('(prefers-contrast: more)').matches) return 'contrast';
      if (window.matchMedia('(prefers-color-scheme: light)').matches) return 'light';
    } catch (e) {}
    return 'dark';
  }
  window.a11yGetScale=function(){
    try { var n=Number(localStorage.getItem(SCALE_KEY)); if (n===2||n===3) return n; } catch (e) {}
    return 1;
  };
  window.a11yGetTheme=function(){
    try { var v=localStorage.getItem(THEME_KEY); if (v==='dark'||v==='light'||v==='contrast') return v; } catch (e) {}
    return osTheme();
  };
  window.a11yApply=function(){
    var el=document.documentElement;
    el.classList.remove('a11y-scale-l','a11y-scale-xl','a11y-light','a11y-contrast');
    var s=window.a11yGetScale();
    if (s===2) el.classList.add('a11y-scale-l'); else if (s===3) el.classList.add('a11y-scale-xl');
    var t=window.a11yGetTheme();
    if (t==='light') el.classList.add('a11y-light'); else if (t==='contrast') el.classList.add('a11y-contrast');
    // Keep the mobile browser chrome / PWA status bar in step with the theme.
    var meta=document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute('content', t==='light' ? '#f4f4f5' : (t==='contrast' ? '#000000' : '#1a1a1a'));
  };
  window.a11ySetScale=function(n){ try { localStorage.setItem(SCALE_KEY,String(n)); } catch (e) {} window.a11yApply(); };
  window.a11ySetTheme=function(t){ try { localStorage.setItem(THEME_KEY,t); } catch (e) {} window.a11yApply(); };
  window.a11yApply();
})();
</script>
</head>
<body>
<header>
<img src="/icon.png" alt="STR">
<div class="brand">ST <span>Reborn</span></div>
<div class="dev" id="dev"></div>
<div class="a11y">
<button type="button" class="a11y-trigger" id="a11yTrigger" aria-haspopup="dialog" aria-expanded="false" aria-label="Display &amp; accessibility" title="Display &amp; accessibility">Aa</button>
<div class="a11y-menu" id="a11yMenu" role="dialog" aria-label="Display &amp; accessibility" hidden>
<div class="a11y-group">
<div class="a11y-glabel" id="a11ySizeLabel">Text size</div>
<div class="a11y-seg" role="group" aria-labelledby="a11ySizeLabel">
<button type="button" data-scale="1" aria-pressed="true">Normal</button>
<button type="button" data-scale="2" aria-pressed="false">Large</button>
<button type="button" data-scale="3" aria-pressed="false">Extra large</button>
</div>
</div>
<div class="a11y-group">
<div class="a11y-glabel" id="a11yThemeLabel">Theme</div>
<div class="a11y-seg" role="group" aria-labelledby="a11yThemeLabel">
<button type="button" data-theme="dark" aria-pressed="true">Dark</button>
<button type="button" data-theme="light" aria-pressed="false">Light</button>
<button type="button" data-theme="contrast" aria-pressed="false">High contrast</button>
</div>
</div>
</div>
</div>
</header>

<main>
<div class="card nowcard loading" id="statusCard">
<div class="label">Now playing</div>
<div id="status"><span class="now">Loading&hellip;</span></div>
</div>

<div class="card">
<div class="label">Volume</div>
<div class="vol"><input type="range" id="vol" min="0" max="100" value="0" aria-label="Volume" oninput="onVol(this.value)"><span class="val" id="volval">0</span></div>
</div>

<div class="card">
<div class="label">Input</div>
<div class="row c3" id="inputs">
<button class="btn" onclick="setSource('BLUETOOTH',this)">Bluetooth</button>
<button class="btn" onclick="setSource('AUX',this)">AUX</button>
<button class="btn" onclick="setSource('STANDBY',this)">Standby</button>
</div>
</div>

<div class="card">
<div class="label">Playback</div>
<div class="row c2">
<button class="btn" onclick="pp(this,'/api/pause')">Pause</button>
<button class="btn" onclick="pp(this,'/api/stop')">Stop</button>
</div>
</div>

<div class="label" style="margin:18px 12px 8px">Presets</div>
<div class="grid" id="presets"></div>

<div class="card" id="peersCard">
<div class="label">Other speakers</div>
<div class="row" id="peers"></div>
</div>
</main>

<footer>
<div class="label" style="margin-bottom:8px">Support ST Reborn</div>
<div class="sponsors">
<a class="btn" href="https://github.com/sponsors/JRpersonal" target="_blank" rel="noopener">&#9829; GitHub</a>
<a class="btn" href="https://ko-fi.com/streborn" target="_blank" rel="noopener">&#9749; Ko-fi</a>
<a class="btn" href="https://paypal.me/JR31337" target="_blank" rel="noopener">PayPal</a>
</div>
<a class="web" href="https://st-reborn.de" target="_blank" rel="noopener">st-reborn.de</a>
<span class="ver" id="ver"></span>
<span class="hint">Tip: use your browser menu and "Add to Home Screen" to keep this as an app.</span>
</footer>

<script>
async function api(path, method, body) {
  const r = await fetch(path, { method: method || 'GET', headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
  if (!r.ok) { console.error(path, r.status); return null; }
  const ct = r.headers.get('content-type') || '';
  return ct.includes('json') ? r.json() : r.text();
}
function escapeHtml(s){ return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[c]); }

var volTimer = null, volLast = -1;
function onVol(v) {
  document.getElementById('volval').textContent = v;
  if (volTimer) clearTimeout(volTimer);
  volTimer = setTimeout(function(){ if (+v !== volLast) { volLast = +v; api('/api/box/volume','PUT',{value:+v}); } }, 250);
}
// setNow renders the now-playing card and clears its loading state.
function setNow(name, state) {
  document.getElementById('status').innerHTML = '<span class="now">' + escapeHtml(name) + '</span>' + (state ? '<div class="st">' + escapeHtml(state) + '</div>' : '');
  document.getElementById('statusCard').classList.remove('loading');
}
// press gives a momentary tap highlight on a control button.
function press(btn) { if (!btn) return; btn.classList.add('active'); setTimeout(function(){ btn.classList.remove('active'); }, 600); }
// pp = press + POST + refresh, for the Pause/Stop controls.
async function pp(btn, path) { press(btn); await api(path, 'POST'); setTimeout(refreshStatus, 1200); }
// setSource selects an input and keeps that button highlighted until another is chosen.
async function setSource(s, btn) {
  document.querySelectorAll('#inputs .btn').forEach(function(e){ e.classList.remove('active'); });
  if (btn) btn.classList.add('active');
  await api('/api/box/source','PUT',{source:s});
  setTimeout(refreshStatus, 1200);
}

async function loadSettings() {
  const s = await api('/api/box/settings');
  if (!s) return;
  if (s.info && s.info.name) { var d = document.getElementById('dev'); d.textContent = s.info.name; d.title = s.info.name; }
  if (s.volume && typeof s.volume.actual === 'number') {
    volLast = s.volume.actual;
    var el = document.getElementById('vol'); el.value = s.volume.actual;
    document.getElementById('volval').textContent = s.volume.actual;
  }
}

async function loadPresets() {
  const list = await api('/api/presets') || [];
  const grid = document.getElementById('presets');
  grid.innerHTML = '';
  for (let i = 1; i <= 6; i++) {
    const p = list.find(x => x.slot === i);
    const div = document.createElement('div');
    div.className = 'preset' + (p ? '' : ' empty');
    if (p) {
      const nm = p.name || ('Preset ' + i);
      div.setAttribute('role','button'); div.tabIndex = 0;
      div.onclick = () => playSlot(i, div, nm);
      div.onkeydown = (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); playSlot(i, div, nm); } };
      div.innerHTML = '<div class="num">#' + i + '</div><div class="name">' + escapeHtml(nm) + '</div>';
    }
    else { div.innerHTML = '<div class="num">#' + i + '</div><div class="name">&mdash; empty &mdash;</div>'; }
    grid.appendChild(div);
  }
}
// playSlot gives instant client-side feedback (highlight the tapped tile + a
// "Starting..." status) so the user sees the press land, then confirms via the
// existing 5s status poll. No extra box request beyond the play + one refresh,
// so it adds no polling load on the speaker.
async function playSlot(n, tile, name) {
  document.querySelectorAll('.preset.active').forEach(function(e){ e.classList.remove('active'); });
  if (tile) tile.classList.add('active');
  setNow((name ? 'Starting ' + name : 'Starting'), 'please wait');
  const r = await api('/api/play/' + n, 'POST');
  if (r) { setTimeout(refreshStatus, 1200); setTimeout(refreshStatus, 3000); }
  else { setNow('Could not start', 'tap again'); if (tile) tile.classList.remove('active'); }
}

async function refreshStatus() {
  const r = await fetch('/api/status'); const t = await r.text();
  const m = t.match(/<itemName>([^<]+)<\/itemName>/) || t.match(/<track>([^<]+)<\/track>/);
  const src = (t.match(/source="([^"]+)"/) || [])[1] || '';
  const state = (t.match(/<playStatus>([^<]+)<\/playStatus>/) || [])[1] || '';
  const name = m ? m[1] : '';
  const human = { PLAY_STATE:'Playing', PAUSE_STATE:'Paused', STOP_STATE:'Stopped', BUFFERING_STATE:'Buffering', INVALID_SOURCE:'Stopped' };
  setNow(name || src || 'Idle', human[state] || (state ? state.replace('_STATE','').toLowerCase() : 'stopped'));
}

async function loadPeers() {
  // Forward-compatible: the /api/peers endpoint is added with the peer-browse
  // step; until then this 404s and the section stays hidden.
  const list = await api('/api/peers');
  if (!list || !list.length) return;
  const box = document.getElementById('peers'); box.innerHTML = '';
  list.forEach(function(p){
    const a = document.createElement('a'); a.className = 'btn peer'; a.href = p.url; a.rel = 'noopener';
    a.innerHTML = '<span class="dot"></span>' + escapeHtml(p.name || p.url);
    box.appendChild(a);
  });
  document.getElementById('peersCard').style.display = 'block';
}

async function loadVersion() {
  const v = await api('/api/agent/version');
  if (v && v.version) document.getElementById('ver').textContent = 'ST Reborn ' + v.version;
}

// Wire the "Aa" display-options menu. The preferences are applied instantly by
// the helpers defined in the <head> (toggling classes on <html>), so there is
// no reload and no request to the speaker.
(function wireA11y() {
  var trigger = document.getElementById('a11yTrigger');
  var menu = document.getElementById('a11yMenu');
  if (!trigger || !menu) return;
  // Localize the menu labels to the phone's language, reusing the same strings
  // as the desktop app. Only these controls are translated; the rest of the
  // page is English. Done client-side from navigator.language, so it costs the
  // speaker nothing.
  var A11Y_I18N = {
    en:{t:"Display & accessibility",sz:"Text size",n:"Normal",l:"Large",x:"Extra large",th:"Theme",d:"Dark",li:"Light",c:"High contrast"},
    de:{t:"Anzeige und Barrierefreiheit",sz:"Textgröße",n:"Normal",l:"Groß",x:"Sehr groß",th:"Darstellung",d:"Dunkel",li:"Hell",c:"Hoher Kontrast"},
    nl:{t:"Weergave en toegankelijkheid",sz:"Tekstgrootte",n:"Normaal",l:"Groot",x:"Extra groot",th:"Thema",d:"Donker",li:"Licht",c:"Hoog contrast"},
    fr:{t:"Affichage et accessibilité",sz:"Taille du texte",n:"Normale",l:"Grande",x:"Très grande",th:"Thème",d:"Sombre",li:"Clair",c:"Contraste élevé"},
    es:{t:"Pantalla y accesibilidad",sz:"Tamaño del texto",n:"Normal",l:"Grande",x:"Muy grande",th:"Tema",d:"Oscuro",li:"Claro",c:"Alto contraste"},
    pl:{t:"Wyświetlanie i dostępność",sz:"Rozmiar tekstu",n:"Normalny",l:"Duży",x:"Bardzo duży",th:"Motyw",d:"Ciemny",li:"Jasny",c:"Wysoki kontrast"},
    tr:{t:"Görüntü ve erişilebilirlik",sz:"Yazı boyutu",n:"Normal",l:"Büyük",x:"Çok büyük",th:"Tema",d:"Koyu",li:"Açık",c:"Yüksek kontrast"},
    ar:{t:"العرض وإمكانية الوصول",sz:"حجم النص",n:"عادي",l:"كبير",x:"كبير جدًا",th:"السمة",d:"داكن",li:"فاتح",c:"تباين عالٍ"},
    ja:{t:"表示とアクセシビリティ",sz:"文字サイズ",n:"標準",l:"大",x:"特大",th:"テーマ",d:"ダーク",li:"ライト",c:"ハイコントラスト"},
    lt:{t:"Rodymas ir prieinamumas",sz:"Teksto dydis",n:"Normalus",l:"Didelis",x:"Labai didelis",th:"Tema",d:"Tamsi",li:"Šviesi",c:"Didelis kontrastas"},
    lv:{t:"Attēlojums un pieejamība",sz:"Teksta izmērs",n:"Normāls",l:"Liels",x:"Ļoti liels",th:"Motīvs",d:"Tumšs",li:"Gaišs",c:"Augsts kontrasts"},
    uk:{t:"Відображення та доступність",sz:"Розмір тексту",n:"Звичайний",l:"Великий",x:"Дуже великий",th:"Тема",d:"Темна",li:"Світла",c:"Високий контраст"}
  };
  var langs = (navigator.languages && navigator.languages.length) ? navigator.languages : [navigator.language || 'en'];
  var code = 'en';
  for (var i = 0; i < langs.length; i++) {
    var pri = String(langs[i] || '').toLowerCase().split('-')[0];
    if (A11Y_I18N[pri]) { code = pri; break; }
  }
  var L = A11Y_I18N[code];
  menu.setAttribute('lang', code);
  if (code === 'ar') menu.setAttribute('dir', 'rtl');
  trigger.setAttribute('aria-label', L.t); trigger.title = L.t; menu.setAttribute('aria-label', L.t);
  document.getElementById('a11ySizeLabel').textContent = L.sz;
  document.getElementById('a11yThemeLabel').textContent = L.th;
  var setTxt = function(sel, v) { var el = menu.querySelector(sel); if (el) el.textContent = v; };
  setTxt('button[data-scale="1"]', L.n); setTxt('button[data-scale="2"]', L.l); setTxt('button[data-scale="3"]', L.x);
  setTxt('button[data-theme="dark"]', L.d); setTxt('button[data-theme="light"]', L.li); setTxt('button[data-theme="contrast"]', L.c);
  function close() { menu.hidden = true; trigger.setAttribute('aria-expanded', 'false'); }
  function open() { menu.hidden = false; trigger.setAttribute('aria-expanded', 'true'); }
  trigger.onclick = function(e) { e.stopPropagation(); if (menu.hidden) open(); else close(); };
  function sync(attr, val) {
    menu.querySelectorAll('button[' + attr + ']').forEach(function(b) {
      b.setAttribute('aria-pressed', String(b.getAttribute(attr) === String(val)));
    });
  }
  menu.querySelectorAll('button[data-scale]').forEach(function(b) {
    b.onclick = function() { var n = Number(b.dataset.scale); a11ySetScale(n); sync('data-scale', n); };
  });
  menu.querySelectorAll('button[data-theme]').forEach(function(b) {
    b.onclick = function() { a11ySetTheme(b.dataset.theme); sync('data-theme', b.dataset.theme); };
  });
  sync('data-scale', a11yGetScale());
  sync('data-theme', a11yGetTheme());
  document.addEventListener('click', function(e) { if (!e.target.closest('.a11y')) close(); });
  document.addEventListener('keydown', function(e) { if (e.key === 'Escape') close(); });
})();

loadSettings(); loadPresets(); refreshStatus(); loadPeers(); loadVersion();
setInterval(refreshStatus, 5000);
</script>
</body>
</html>
`
