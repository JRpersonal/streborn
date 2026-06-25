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
:root { --bg:#1a1a1a; --card:#242424; --card2:#2a2a2a; --line:#3a3a3a; --fg:#eee; --muted:#9e9e9e; --accent:#e88; }
* { box-sizing: border-box; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
:focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
body { margin:0; padding:16px 16px calc(16px + env(safe-area-inset-bottom)); background:var(--bg); color:var(--fg); max-width:620px; margin:0 auto; }
header { display:flex; align-items:center; gap:10px; margin-bottom:14px; }
header img { width:30px; height:30px; border-radius:7px; }
header .brand { font-size:18px; font-weight:700; letter-spacing:.2px; }
header .brand span { color:var(--accent); }
header .dev { margin-left:auto; font-size:12px; color:var(--muted); text-align:right; max-width:55%; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.card { background:var(--card); border:1px solid var(--line); border-radius:10px; padding:12px; margin:12px 0; }
.nowcard { padding:14px 16px; background:linear-gradient(180deg,#2c2c2c,#242424); }
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
button.btn:active { background:#3d3d3d; }
@media (hover:hover) { button.btn:hover, a.btn:hover { background:#333; } .preset:hover { background:#333; } }
.vol { display:flex; align-items:center; gap:12px; }
.vol input[type=range] { flex:1; accent-color:var(--accent); height:44px; }
.vol input[type=range]::-webkit-slider-thumb { -webkit-appearance:none; width:24px; height:24px; border-radius:50%; background:var(--accent); }
.vol input[type=range]::-moz-range-thumb { width:24px; height:24px; border:0; border-radius:50%; background:var(--accent); }
.vol .val { width:36px; text-align:right; font-variant-numeric:tabular-nums; color:var(--fg); }
.grid { display:grid; grid-template-columns:repeat(2,1fr); gap:8px; }
.preset { background:var(--card2); border:1px solid var(--line); border-radius:10px; padding:14px; cursor:pointer; min-height:80px; display:flex; flex-direction:column; justify-content:center; transition:background .15s; }
.preset:active { background:#3d3d3d; }
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
</style>
</head>
<body>
<header>
<img src="/icon.png" alt="STR">
<div class="brand">ST <span>Reborn</span></div>
<div class="dev" id="dev"></div>
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

loadSettings(); loadPresets(); refreshStatus(); loadPeers(); loadVersion();
setInterval(refreshStatus, 5000);
</script>
</body>
</html>
`
