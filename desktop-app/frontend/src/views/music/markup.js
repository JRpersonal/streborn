// views/music/markup.js: the #view-box markup (topbar, status bar, transport,
// volume, preset grid, search). Returned as a string so main.js can assign it
// after the skeleton exists; the ids are what the other Music modules and the
// speaker picker in main.js (#boxSelect) look up.

import { t } from '../../i18n/index.js';
import { escapeAttr, escapeHtml } from '../../utils.js';

// ---------- Box steuern View ----------

export function musicViewMarkup() {
  return `
  <div class="topbar">
    <div class="topbar-head">
      <div class="topbar-title">${escapeHtml(t('topbar.title'))}</div>
      <button class="btn-icon" id="refreshBtn" aria-label="${escapeAttr(t('topbar.refreshTitle'))}" title="${escapeAttr(t('topbar.refreshTitle'))}"><span class="refresh-icon" aria-hidden="true">&#x21bb;</span></button>
    </div>
    <div class="box-select" id="boxSelect">${escapeHtml(t('speaker.searching'))}</div>
  </div>
  <div id="boxHint" class="box-hint hidden">
    <p>${escapeHtml(t('speaker.choose'))}</p>
  </div>
  <div id="boxControls" class="hidden">
    <div class="status-bar" id="statusBar" role="status" aria-live="polite">
      <div class="status-main" id="statusMain"></div>
      <div class="track-progress hidden" id="trackProgress">
        <span class="track-time" id="trackElapsed">0:00</span>
        <div class="track-bar" id="trackBar"><div class="track-bar-fill" id="trackBarFill"></div></div>
        <span class="track-time" id="trackTotal"></span>
      </div>
    </div>
    <label class="title-swap-row hidden" id="titleSwapRow"><input type="checkbox" id="titleSwapToggle"> <span>${escapeHtml(t('status.titleSwap'))}</span></label>
    <div class="group-control" id="groupControl"></div>
    <div class="controls">
      <button class="btn btn-mini hidden" id="trackPrevBtn" title="${escapeAttr(t('controls.prev'))}" aria-label="${escapeAttr(t('controls.prev'))}">&#9198;</button>
      <button class="btn" id="pauseBtn">&#9208; ${escapeHtml(t('controls.pause'))}</button>
      <button class="btn btn-mini hidden" id="trackNextBtn" title="${escapeAttr(t('controls.next'))}" aria-label="${escapeAttr(t('controls.next'))}">&#9197;</button>
      <button class="btn" id="stopBtn">&#9209; ${escapeHtml(t('controls.stop'))}</button>
      <div class="queue-controls hidden" id="queueControls">
        <button class="btn btn-mini" id="queuePrevBtn" title="${escapeAttr(t('controls.prev'))}">&#9198; ${escapeHtml(t('controls.prev'))}</button>
        <button class="btn btn-mini" id="queueNextBtn" title="${escapeAttr(t('controls.next'))}">&#9197; ${escapeHtml(t('controls.next'))}</button>
        <button class="btn btn-mini toggle-btn" id="queueShuffleBtn" aria-label="${escapeAttr(t('controls.shuffle'))}" title="${escapeAttr(t('controls.shuffle'))}">&#128256;</button>
        <button class="btn btn-mini toggle-btn" id="queueRepeatBtn" aria-label="${escapeAttr(t('controls.repeat'))}" title="${escapeAttr(t('controls.repeat'))}">&#128257;</button>
        <span class="queue-pos" id="queuePos"></span>
      </div>
      <div class="source-buttons" id="sourceButtons">
        <span class="source-inputs" id="sourceInputs"></span>
        <button class="btn btn-source btn-source-icon" data-source="STANDBY" aria-label="${escapeAttr(t('controls.standbyTitle'))}" title="${escapeAttr(t('controls.standbyTitle'))}">&#9211;</button>
      </div>
      <div class="volume-control">
        <span class="vol-scope hidden" id="musicVolumeScope"></span>
        <button class="vol-icon vol-mute" id="volMute" aria-pressed="false" aria-label="${escapeAttr(t('controls.mute'))}" title="${escapeAttr(t('controls.mute'))}">&#128266;</button>
        <button class="btn btn-mini vol-step" id="volDown" aria-label="${escapeAttr(t('controls.volumeDown'))}" title="${escapeAttr(t('controls.volumeDown'))}">&#8722;</button>
        <input type="range" id="musicVolume" min="0" max="100" step="1" aria-label="${escapeAttr(t('controls.volume'))}" title="${escapeAttr(t('controls.volumeWheelHint'))}" />
        <button class="btn btn-mini vol-step" id="volUp" aria-label="${escapeAttr(t('controls.volumeUp'))}" title="${escapeAttr(t('controls.volumeUp'))}">+</button>
        <span class="vol-val" id="musicVolumeVal">--</span>
        
      </div>
    </div>
    <div class="grid" id="presets"></div>
    <div class="preset-copy-row">
      <button class="btn btn-mini preset-transfer-btn" id="presetTransferBtn" aria-label="" title="" disabled><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="15" height="15" aria-hidden="true"><path d="M4 12v7a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-7"></path><polyline points="16 6 12 2 8 6"></polyline><line x1="12" y1="2" x2="12" y2="15"></line></svg> ${escapeHtml(t('presets.transferBtn'))}</button>
    </div>
    <div class="search">
      <h3>${escapeHtml(t('search.heading'))} <small>(${escapeHtml(t('search.headingSub'))})</small></h3>
      <details class="stream-source-embed" id="streamSourceDetails">
        <summary>${escapeHtml(t('streamSource.title'))}</summary>
        <div id="streamSourceInline"></div>
      </details>
      <div class="search-input-row">
        <input type="text" id="searchQ" placeholder="${escapeAttr(t('search.placeholder'))}" />
        <button class="btn" id="searchBtn">${escapeHtml(t('search.btn'))}</button>
        <button class="btn btn-mini" id="topBtn">${escapeHtml(t('search.topBtn'))}</button>
        <button class="btn btn-mini hidden" id="favModeBtn" title="${escapeAttr(t('search.favBtnTitle'))}">${escapeHtml(t('search.favBtn'))}</button>
        <button class="btn btn-mini hidden" id="pandoraModeBtn" title="${escapeAttr(t('pandora.btnTitle'))}">Pandora</button>
      </div>
      <div class="search-filters">
        <label>${escapeHtml(t('search.countryLabel'))}:
          <select id="searchCountry"></select>
        </label>
        <label>${escapeHtml(t('search.languageLabel'))}:
          <select id="searchLang"><option value="">${escapeHtml(t('search.allLanguages'))}</option></select>
        </label>
        <label>${escapeHtml(t('search.orderLabel'))}:
          <select id="searchOrder"></select>
        </label>
        <label>${escapeHtml(t('search.bitrateLabel'))}:
          <select id="searchBitrate">
            <option value="0">${escapeHtml(t('search.bitrateAny'))}</option>
            <option value="64">&ge; 64 kbit/s</option>
            <option value="96">&ge; 96 kbit/s</option>
            <option value="128">&ge; 128 kbit/s</option>
            <option value="192">&ge; 192 kbit/s</option>
            <option value="256">&ge; 256 kbit/s</option>
            <option value="320">&ge; 320 kbit/s</option>
          </select>
        </label>
        <label><input type="checkbox" id="searchOnlyOK" checked /> ${escapeHtml(t('search.onlyOK'))}</label>
        <label><input type="checkbox" id="searchOnlyBose" checked /> ${escapeHtml(t('search.onlyBose'))}</label>
      </div>
      <div class="genre-chips" id="genreChips"></div>
      <div class="search-count muted small hidden" id="searchCount"></div>
      <details class="search-addhint" id="addStationBox">
        <summary>${escapeHtml(t('search.addStationCta'))}</summary>
        <div class="search-addhint-body">
          ${escapeHtml(t('search.addStationHint'))}
          <div class="search-addhint-actions">
            <button class="btn btn-mini" id="addStationOpenBtn">radio-browser.info</button>
          </div>
        </div>
      </details>
      <div class="search-results" id="searchResults"></div>
      <div class="load-more-row hidden" id="loadMoreRow">
        <button class="btn btn-mini" id="loadMoreBtn">${escapeHtml(t('search.loadMore'))}</button>
      </div>
    </div>
  </div>
`;
}
