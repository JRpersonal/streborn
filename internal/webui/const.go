package webui

import "time"

const shutdownTimeout = 5 * time.Second

// indexHTML is the minimal HTML page the stick serves on "/".
// It is for direct browser access (a phone without the desktop app). The
// real UI comes later via the Wails desktop app over the same REST API.
const indexHTML = `<!doctype html>
<html lang="de">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>STR</title>
<style>
* { box-sizing: border-box; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
body { margin: 0; padding: 16px; background: #1a1a1a; color: #eee; max-width: 600px; margin: 0 auto; }
h1 { font-size: 22px; margin: 0 0 16px 0; color: #e88; }
.grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 10px; margin: 16px 0; }
.preset { background: #2a2a2a; border: 1px solid #444; border-radius: 8px; padding: 14px; cursor: pointer; min-height: 90px; display: flex; flex-direction: column; justify-content: center; transition: background 0.15s; }
.preset:hover { background: #333; }
.preset:active { background: #444; }
.preset .num { font-size: 12px; color: #888; }
.preset .name { font-size: 16px; font-weight: 600; margin-top: 4px; }
.preset .url { font-size: 11px; color: #777; margin-top: 4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.preset.empty { color: #666; border-style: dashed; }
.controls { display: flex; gap: 8px; margin: 16px 0; }
.controls button { flex: 1; background: #333; color: #eee; border: 1px solid #555; border-radius: 6px; padding: 10px; cursor: pointer; }
.controls button:hover { background: #444; }
.status { background: #222; padding: 10px; border-radius: 6px; font-size: 13px; color: #aaa; min-height: 40px; }
.status .now { color: #e88; }
</style>
</head>
<body>
<h1>STR</h1>
<div class="status" id="status">Status loading...</div>
<div class="controls">
<button onclick="api('/api/pause', 'POST')">Pause</button>
<button onclick="api('/api/stop', 'POST')">Stop</button>
<button onclick="refreshStatus()">Status</button>
</div>
<div class="grid" id="presets"></div>

<script>
async function api(path, method, body) {
  const r = await fetch(path, {
    method: method || 'GET',
    headers: { 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!r.ok) { console.error(path, r.status, await r.text()); return null; }
  const ct = r.headers.get('content-type') || '';
  return ct.includes('json') ? r.json() : r.text();
}

async function loadPresets() {
  const list = await api('/api/presets') || [];
  const grid = document.getElementById('presets');
  grid.innerHTML = '';
  for (let i = 1; i <= 6; i++) {
    const p = list.find(x => x.slot === i);
    const div = document.createElement('div');
    div.className = 'preset' + (p ? '' : ' empty');
    div.onclick = () => playSlot(i);
    if (p) {
      div.innerHTML = '<div class="num">#' + i + '</div><div class="name">' + escapeHtml(p.name || 'Preset ' + i) + '</div><div class="url">' + escapeHtml(p.stream_url || '') + '</div>';
    } else {
      div.innerHTML = '<div class="num">#' + i + '</div><div class="name">— empty —</div>';
    }
    grid.appendChild(div);
  }
}

async function playSlot(n) {
  const r = await api('/api/play/' + n, 'POST');
  if (r) {
    setTimeout(refreshStatus, 1500);
  }
}

async function refreshStatus() {
  const r = await fetch('/api/status');
  const t = await r.text();
  const m = t.match(/<itemName>([^<]+)<\/itemName>/) || t.match(/<track>([^<]+)<\/track>/);
  const src = (t.match(/source="([^"]+)"/) || [])[1] || '';
  const state = (t.match(/<playStatus>([^<]+)<\/playStatus>/) || [])[1] || '';
  const name = m ? m[1] : '';
  document.getElementById('status').innerHTML = '<span class="now">' + escapeHtml(name || src) + '</span> · ' + escapeHtml(state);
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[c]);
}

loadPresets();
refreshStatus();
setInterval(refreshStatus, 5000);
</script>
</body>
</html>
`
