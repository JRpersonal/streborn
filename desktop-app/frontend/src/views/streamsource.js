// views/streamsource.js — a provider-neutral LAN stream-server browser.
// The source service keeps its own login, provider-specific code, and audio.

import { $, escapeHtml, escapeAttr, getBoxLabel, showError, showToast } from '../utils.js';
import { state } from '../state.js';
import { GetStreamSourcePlayback, StreamSourceStations, PlayURL } from '../api.js';
import { t } from '../i18n/index.js';

const BASE_URL_KEY = 'str.streamSourceBaseURL';
const sourceState = { baseURL: '', stations: [], loading: false, error: '' };
let deps = {};

function savedBaseURL() {
  try { return localStorage.getItem(BASE_URL_KEY) || ''; } catch { return ''; }
}

function saveBaseURL(value) {
  try { localStorage.setItem(BASE_URL_KEY, value); } catch {}
}

export function initStreamSourceView(options = {}) {
  deps = options || {};
}

async function loadStations(baseURL) {
  sourceState.baseURL = baseURL.trim();
  sourceState.stations = [];
  sourceState.loading = true;
  sourceState.error = '';
  renderStreamSource();
  try {
    sourceState.stations = await StreamSourceStations(sourceState.baseURL);
    saveBaseURL(sourceState.baseURL);
  } catch (e) {
    sourceState.error = String(e);
  } finally {
    sourceState.loading = false;
    renderStreamSource();
  }
}

async function playStation(station) {
  const selected = state.currentBox;
  if (!selected || selected.kind === 'stock') {
    showToast(t('common.pickBox'));
    return;
  }
  const box = (deps.effectivePlayTarget && deps.effectivePlayTarget()) || selected;
  try {
    const playback = await GetStreamSourcePlayback(sourceState.baseURL, station.id);
    const title = playback.title || station.name;
    await PlayURL(box.host, box.port, playback.streamURL, title, '', '', '', '', '', 0);
    state.nowPlayState = 'BUFFERING_STATE';
    state.nowLocation = playback.streamURL;
    state.nowName = title;
    state.nowIcon = '';
    state.nowBitrate = 0;
    state.nowUUID = '';
    state.lastAppPlay = {
      url: playback.streamURL, name: title, icon: '', bitrate: 0,
      uuid: '', homepage: '', codec: '', at: Date.now(),
    };
    if (deps.playbackChanged) deps.playbackChanged();
    showToast(t('streamSource.playing', { name: title, speaker: getBoxLabel(selected) }));
  } catch (e) {
    showError(String(e));
  }
}

export function renderStreamSource() {
  const root = $('view-stream-source');
  if (!root) return;
  const baseURL = sourceState.baseURL || savedBaseURL();
  const selected = state.currentBox && state.currentBox.kind !== 'stock' ? getBoxLabel(state.currentBox) : t('common.pickBox');
  const rows = sourceState.stations.map((station) => `
    <li class="stream-source-row">
      <span class="stream-source-name">${escapeHtml(station.name)}</span>
      <button class="btn btn-mini stream-source-play" data-station-id="${escapeAttr(station.id)}">${escapeHtml(t('streamSource.play'))}</button>
    </li>`).join('');
  const body = sourceState.loading
    ? `<p class="muted">${escapeHtml(t('streamSource.loading'))}</p>`
    : sourceState.error
      ? `<p class="stream-source-error">${escapeHtml(sourceState.error)}</p>`
      : sourceState.stations.length
        ? `<ul class="stream-source-list">${rows}</ul>`
        : `<p class="muted">${escapeHtml(t('streamSource.empty'))}</p>`;
  root.innerHTML = `
    <div class="stream-source-page">
      <header class="stream-source-hero">
        <h2>${escapeHtml(t('streamSource.title'))}</h2>
        <p>${escapeHtml(t('streamSource.lead'))}</p>
      </header>
      <section class="stream-source-card">
        <label for="streamSourceURL">${escapeHtml(t('streamSource.serverLabel'))}</label>
        <div class="stream-source-connect">
          <input id="streamSourceURL" type="url" inputmode="url" spellcheck="false" placeholder="http://192.0.2.10:8091" value="${escapeAttr(baseURL)}">
          <button class="btn btn-primary" id="streamSourceConnect">${escapeHtml(t('streamSource.connect'))}</button>
        </div>
        <p class="muted small">${escapeHtml(t('streamSource.serverHelp'))}</p>
      </section>
      <section class="stream-source-card">
        <div class="stream-source-target"><span>${escapeHtml(t('streamSource.forSpeaker'))}</span><b>${escapeHtml(selected)}</b></div>
        ${body}
      </section>
      <p class="muted small">${escapeHtml(t('streamSource.presetHint'))}</p>
    </div>`;
  const input = $('streamSourceURL');
  const connect = $('streamSourceConnect');
  const submit = () => { if (input) loadStations(input.value); };
  if (connect) connect.onclick = submit;
  if (input) input.onkeydown = (e) => { if (e.key === 'Enter') submit(); };
  root.querySelectorAll('.stream-source-play').forEach((button) => {
    button.onclick = () => {
      const station = sourceState.stations.find((s) => s.id === button.dataset.stationId);
      if (station) playStation(station);
    };
  });
}
