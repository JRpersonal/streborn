import { $, escapeHtml, escapeAttr, showError, showToast } from '../utils.js';
import { SiriusXMConfig, SaveSiriusXMConfig, StartSiriusXM, StopSiriusXM, SiriusXMStatus, SiriusXMStations, SiriusXMPlay, SiriusXMURL, SetPreset } from '../api.js';
import { state } from '../state.js';

let deps = { effectivePlayTarget: () => state.currentBox, showSlotPicker: null };
let stations = [];
let config = { username: '' };
let running = false;

export function initSiriusXMView(d) { deps = { ...deps, ...d }; }

function renderStations(root, query = '') {
  const q = String(query || '').trim().toLowerCase();
  const list = q ? stations.filter(s => `${s.name} ${s.mount} ${s.id}`.toLowerCase().includes(q)) : stations;
  const box = root.querySelector('#siriusStationList');
  if (!box) return;
  box.innerHTML = list.map((s, i) => `
    <div class="sirius-station-row">
      <div class="sirius-station-main">
        <div class="sirius-station-name">${escapeHtml(s.name)}</div>
        <div class="sirius-station-id muted small">${escapeHtml(s.id)}</div>
      </div>
      <div class="sirius-station-actions">
        <button class="btn btn-primary btn-mini" data-sxm-play="${escapeAttr(String(i))}">Play</button>
        <button class="btn btn-secondary btn-mini" data-sxm-preset="${escapeAttr(String(i))}">Preset</button>
      </div>
    </div>`).join('') || '<p class="muted">No stations found.</p>';

  box.querySelectorAll('[data-sxm-play]').forEach(b => b.onclick = async () => {
    const s = list[Number(b.dataset.sxmPlay)];
    const target = deps.effectivePlayTarget ? deps.effectivePlayTarget() : state.currentBox;
    if (!target) { showError('Select a speaker first.'); return; }
    b.disabled = true;
    try { await SiriusXMPlay(target.host, target.port, s.mount, s.name); showToast(`Playing ${s.name}`); }
    catch (e) { showError(e); }
    finally { b.disabled = false; }
  });

  box.querySelectorAll('[data-sxm-preset]').forEach(b => b.onclick = async () => {
    const s = list[Number(b.dataset.sxmPreset)];
    const target = deps.effectivePlayTarget ? deps.effectivePlayTarget() : state.currentBox;
    if (!target) { showError('Select a speaker first.'); return; }
    if (!deps.showSlotPicker) return;
    deps.showSlotPicker({
      title: `Save ${s.name}`,
      subtitle: 'Choose a SoundTouch preset key.',
      onPick: async slot => {
        const url = await SiriusXMURL(target.host, s.mount);
        await SetPreset(target.host, target.port, slot, s.name, url, '', 256, '', 'MP3');
        showToast(`Saved ${s.name} to key ${slot}`);
      },
    });
  });
}

async function refresh(root) {
  try { config = { ...config, ...(await SiriusXMConfig()) }; } catch {}
  try {
    const st = await SiriusXMStatus();
    running = !!(st && st.sxm_running && st.relay_running);
  } catch { running = false; }
  try { stations = await SiriusXMStations() || []; } catch (e) { showError(`Could not load SiriusXM stations: ${e}`); }
  const status = root.querySelector('#siriusStatus');
  if (status) status.textContent = running ? 'Running' : 'Stopped';
  root.querySelector('#siriusStartBtn').textContent = running ? 'Restart SiriusXM' : 'Start SiriusXM';
  root.querySelector('#siriusUsername').value = config.username || '';
  renderStations(root, root.querySelector('#siriusSearch')?.value || '');
}

export async function renderSiriusXM() {
  const root = $('view-siriusxm');
  if (!root) return;
  root.innerHTML = `
    <div class="sirius-wrap">
      <div class="sirius-header">
        <div>
          <h2>SiriusXM</h2>
          <p class="muted">Built-in SiriusXM playback with on-demand HLS handling.</p>
        </div>
        <div class="sirius-status"><span class="sirius-dot"></span><span id="siriusStatus">Checking…</span></div>
      </div>
      <div class="sirius-login cardish">
        <h3>Login</h3>
        <div class="sirius-fields">
          <label>Username / email<input id="siriusUsername" type="text" autocomplete="username"></label>
          <label>Password<input id="siriusPassword" type="password" autocomplete="current-password" placeholder="Saved password is kept when left blank"></label>
        </div>
        <div class="sirius-native-note">
          <span class="sirius-native-badge">Built in</span>
          <span class="muted small">SiriusXM playback is built into ST Reborn. Python and FFmpeg are not required.</span>
        </div>
<div class="sirius-actions">
          <button class="btn btn-primary" id="siriusStartBtn">Start SiriusXM</button>
          <button class="btn btn-secondary" id="siriusStopBtn">Stop</button>
        </div>
        <p class="muted small">The SiriusXM connection, HLS decryption, and on-demand audio relay run inside ST Reborn.</p>
      </div>
      <div class="sirius-list cardish">
        <div class="sirius-list-head"><h3>Stations</h3><input id="siriusSearch" type="search" placeholder="Search stations…"></div>
        <div id="siriusStationList"></div>
      </div>
    </div>`;

  root.querySelector('#siriusStartBtn').onclick = async () => {
    try {
      const username = root.querySelector('#siriusUsername').value.trim();
      const password = root.querySelector('#siriusPassword').value;
      await SaveSiriusXMConfig(username, password || '__KEEP__', '', '');
      await StartSiriusXM();
      await refresh(root);
      showToast('SiriusXM started');
    } catch (e) { showError(e); }
  };
  root.querySelector('#siriusStopBtn').onclick = async () => {
    try { await StopSiriusXM(); await refresh(root); showToast('SiriusXM stopped'); }
    catch (e) { showError(e); }
  };
  root.querySelector('#siriusSearch').oninput = () => renderStations(root, root.querySelector('#siriusSearch').value);
  await refresh(root);
}
