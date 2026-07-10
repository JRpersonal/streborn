// views/multiroom.js — the "Multi-Room" (zones + stereo pair) view (#70).
//
// Extracted from the main.js monolith, same pattern as views/recent.js: the
// module pulls shared things (state, utils, i18n, api) from their modules and
// receives the few main.js-local helpers it needs (boxNeedsUpdate, zoneLabel,
// discoverBoxes) via initMultiroomView, so it never imports back into main.js.

import { state } from '../state.js';
import { $, escapeHtml, escapeAttr, getBoxLabel } from '../utils.js';
import { t } from '../i18n/index.js';
import { FormZone, DissolveZone, BrowserOpenURL } from '../api.js';
// Group membership + the shared zoneLive poll live in groups.js: ONE
// implementation for this tab, the music-tab frames and the group chips.
import { masterOf as zoneMasterOf, fetchZoneLive } from '../groups.js';

// Injected main.js helpers (see initMultiroomView).
let deps = {
  boxNeedsUpdate: () => false,
  discoverBoxes: async () => {},
  selectBox: () => {},
};
export function initMultiroomView(d) {
  deps = { ...deps, ...d };
}

// zoneLabel is the speaker's display name, used in the group list and the stereo
// pair dropdown. friendlyName first: the backend always fills name with a
// "str-<ip>"/"STR-<hex>" fallback, so name-first never reached the real speaker
// name (Michal's group menu showing str-192.168.x.y). Fall back to name/host only
// when no friendly name resolved. Matches the box switcher and the recent view.
function zoneLabel(b) { return getBoxLabel(b); }

// renderMultiroom paints the Multi-Room view. fetchLive triggers a non-blocking
// parallel poll of every speaker's live zone after paint (skipped on repaints).
export function renderMultiroom(fetchLive) {
  const root = $('view-multiroom');
  if (!root) return;
  // Require deviceID too: the live-zone map is keyed by deviceID, so a box
  // without one (very early discovery) would key an entry under undefined and
  // collide with the music-tab group frames that share state.zoneLive.
  const strBoxes = (state.boxes || []).filter(b => b && b.kind !== 'stock' && b.host && b.deviceID);
  const enough = strBoxes.length >= 2;
  if (!state.zoneLive) state.zoneLive = {};
  if (!state.zoneSlaves) state.zoneSlaves = {};
  if (!state.zoneMode) state.zoneMode = 'native';
  if (!state.zoneMaster || !strBoxes.some(b => b.deviceID === state.zoneMaster)) {
    state.zoneMaster = strBoxes.length ? strBoxes[0].deviceID : '';
  }
  const anyOutdated = strBoxes.some(b => deps.boxNeedsUpdate(b));

  const beta =
    `<div class="setup-help" style="margin-bottom:14px">` +
    `<b>${escapeHtml(t('multiroom.heading'))} <span class="beta-pill">${escapeHtml(t('common.beta'))}</span></b>` +
    `<div class="muted small" style="margin-top:6px">${escapeHtml(t('multiroom.betaNote'))}</div>` +
    `<div class="muted small" style="margin-top:6px">${escapeHtml(t('multiroom.feedbackPre'))} ` +
    `<a href="#" id="multiroomIssueLink">${escapeHtml(t('multiroom.issueLink'))}</a> &middot; ` +
    `<a href="#" id="multiroomEmail">str@sichtbar-app.de</a></div></div>`;

  const topbar = `<div class="zone-topbar"><button id="zoneRefresh" class="btn btn-mini">${escapeHtml(t('common.refresh'))}</button></div>`;
  const previewNote = enough ? '' :
    `<div class="setup-warn small" style="margin-bottom:10px">${escapeHtml(t('multiroom.previewNote'))}</div>`;
  const updateWarn = anyOutdated ?
    `<div class="setup-warn small" style="margin-bottom:10px">${escapeHtml(t('multiroom.updateWarn'))}</div>` : '';

  // Per-card live status from the last parallel fetch (undefined = not fetched).
  const liveLine = (b) => {
    const zl = state.zoneLive[b.deviceID];
    if (zl === undefined) return '';
    const m = zoneMasterOf(b.deviceID, state.zoneLive);
    if (m) {
      const isLead = m === (b.deviceID || '').toUpperCase();
      const txt = isLead ? t('multiroom.liveLeading', { n: (zl.members || []).length }) : t('multiroom.liveInGroup');
      return `<div class="zone-live in">&#9679; ${escapeHtml(txt)}</div>`;
    }
    return `<div class="zone-live">&#9675; ${escapeHtml(t('multiroom.liveStandalone'))}</div>`;
  };

  const cards = strBoxes.length
    ? strBoxes.map(b => {
        const isMaster = b.deviceID === state.zoneMaster;
        const selected = !isMaster && !!state.zoneSlaves[b.deviceID];
        const outdated = deps.boxNeedsUpdate(b);
        const model = (b.model && b.model !== 'SoundTouch')
          ? `<span class="box-model">${escapeHtml(b.model)}</span>` : '';
        const foot = isMaster
          ? `<span class="zone-badge">${escapeHtml(t('multiroom.mainBadge'))}</span>`
          : `<button class="zone-makemain" data-id="${escapeAttr(b.deviceID)}">${escapeHtml(t('multiroom.makeMain'))}</button>`;
        const upd = outdated ? `<span class="zone-update-badge">${escapeHtml(t('multiroom.updateFirst'))}</span>` : '';
        return `<div class="zone-card${isMaster ? ' master' : ''}${selected ? ' selected' : ''}${outdated ? ' outdated' : ''}" data-id="${escapeAttr(b.deviceID)}" role="button" tabindex="0">
            <span class="zone-card-tick">${selected ? '&#10003;' : (isMaster ? '&#9733;' : '')}</span>
            <div class="zone-card-name">${escapeHtml(zoneLabel(b))} ${model}</div>
            <small class="zone-card-host">${escapeHtml(b.host)}</small>
            ${liveLine(b)}
            <div class="zone-card-foot">${foot}${upd}</div>
          </div>`;
      }).join('')
    : `<div class="muted">${escapeHtml(t('multiroom.noSpeaker'))}</div>`;
  const dis = enough ? '' : ' disabled';
  const modeBtn = (m, lbl) => `<button class="seg-btn${state.zoneMode === m ? ' active' : ''}" data-mode="${m}">${escapeHtml(lbl)}</button>`;

  // Summary line for the chosen master, computed from the cached live map
  // via the same masterOf helper the frames and chips use.
  const masterBox = strBoxes.find(b => b.deviceID === state.zoneMaster);
  const ml = masterBox ? state.zoneLive[masterBox.deviceID] : undefined;
  let currentHtml = '';
  if (masterBox && zoneMasterOf(masterBox.deviceID, state.zoneLive)) {
    const names = (ml.members || []).map(m => {
      const b = strBoxes.find(x => (x.deviceID || '').toUpperCase() === (m.deviceID || '').toUpperCase());
      return b ? zoneLabel(b) : (m.ip || m.deviceID);
    });
    currentHtml = `<b>${escapeHtml(t('multiroom.currentZone'))}:</b> ` +
      escapeHtml(zoneLabel(masterBox) + (names.length ? ' + ' + names.join(', ') : ''));
  } else if (ml !== undefined) {
    currentHtml = escapeHtml(t('multiroom.noZone'));
  }

  // Stereo pair (scaffold). Bose stereo pairing is a SoundTouch 10 feature, so
  // only ST10s are offered as candidates (matches the "needs two SoundTouch 10"
  // copy). \b10\b matches "SoundTouch 10" but not 20/30/300/Portable.
  const pairCands = strBoxes.filter(b => /\b10\b/.test(b.model || ''));
  const canPair = pairCands.length >= 2;
  const pairOpts = (sel) => pairCands
    .map((b, i) => `<option value="${escapeAttr(b.deviceID)}" ${i === sel ? 'selected' : ''}>${escapeHtml(zoneLabel(b))}</option>`)
    .join('') || `<option>${escapeHtml(t('multiroom.noSpeaker'))}</option>`;
  const pairDis = canPair ? '' : ' disabled';

  root.innerHTML = beta + topbar + previewNote + updateWarn +
    `<div class="zone-pick-hint muted small">${escapeHtml(t('multiroom.pickHint'))}</div>
     <div class="zone-cards">${cards}</div>
     <div class="zone-controls">
       <div class="zone-field"><span>${escapeHtml(t('multiroom.modeLabel'))}</span>
         <div class="seg">${modeBtn('native', t('multiroom.modeNative'))}${modeBtn('mirror', t('multiroom.modeMirror'))}</div>
         <span class="muted small">${escapeHtml(t('multiroom.modeHelp'))}</span></div>
       <div class="zone-name-note muted small">${escapeHtml(t('multiroom.groupNameNote'))}</div>
       <div class="zone-actions">
         <button id="zoneCreate" class="btn"${dis}>${escapeHtml(t('multiroom.createBtn'))}</button>
         <button id="zoneUngroup" class="btn btn-mini"${dis}>${escapeHtml(t('multiroom.ungroupBtn'))}</button>
       </div>
       <div id="zoneResult">${state.zoneMsg || ''}</div>
       <div id="zoneCurrent" class="muted small" style="margin-top:10px">${currentHtml}</div>
     </div>

     <div class="zone-controls" style="margin-top:22px;border-top:1px solid var(--c-border);padding-top:16px">
       <b>${escapeHtml(t('multiroom.stereoHeading'))} <span class="beta-pill alpha-pill">${escapeHtml(t('common.alpha'))}</span></b>
       <div class="muted small">${escapeHtml(t('multiroom.stereoNote'))}</div>
       ${canPair ? '' : `<div class="setup-warn small">${escapeHtml(t('multiroom.stereoNeedTwo'))}</div>`}
       <label class="zone-field"><span>${escapeHtml(t('multiroom.stereoLeft'))}</span>
         <select id="stereoLeft"${pairDis}>${pairOpts(0)}</select></label>
       <label class="zone-field"><span>${escapeHtml(t('multiroom.stereoRight'))}</span>
         <select id="stereoRight"${pairDis}>${pairOpts(1)}</select></label>
       <div class="zone-actions"><button id="stereoCreate" class="btn"${pairDis}>${escapeHtml(t('multiroom.stereoCreateBtn'))}</button></div>
       <div id="stereoResult">${state.stereoMsg || ''}</div>
     </div>`;

  const issueLink = $('multiroomIssueLink');
  if (issueLink) issueLink.onclick = (e) => { e.preventDefault(); try { BrowserOpenURL('https://github.com/JRpersonal/streborn/issues/70'); } catch {} };
  const email = $('multiroomEmail');
  if (email) email.onclick = (e) => { e.preventDefault(); try { BrowserOpenURL('mailto:str@sichtbar-app.de'); } catch {} };
  const refreshBtn = $('zoneRefresh');
  if (refreshBtn) refreshBtn.onclick = async () => {
    refreshBtn.disabled = true;
    try { await deps.discoverBoxes(); } catch {}
    renderMultiroom(true);
  };

  // Card interactions: the "set as main" button promotes to master; a tap on
  // the rest of a non-master card toggles it in/out of the group. These repaint
  // only (no fetch) so toggling is instant.
  root.querySelectorAll('.zone-card').forEach(card => {
    card.onclick = (e) => {
      const mk = e.target.closest('.zone-makemain');
      if (mk) {
        state.zoneMaster = mk.dataset.id;
        delete state.zoneSlaves[state.zoneMaster];
        renderMultiroom();
        return;
      }
      const id = card.dataset.id;
      if (!enough || id === state.zoneMaster) return;
      state.zoneSlaves[id] = !state.zoneSlaves[id];
      renderMultiroom();
    };
  });
  root.querySelectorAll('.seg-btn').forEach(btn => {
    btn.onclick = () => { state.zoneMode = btn.dataset.mode; renderMultiroom(); };
  });
  if (enough) {
    $('zoneCreate').onclick = () => doFormZone(strBoxes);
    $('zoneUngroup').onclick = () => doDissolveZone(strBoxes);
  }
  if (canPair) {
    $('stereoCreate').onclick = () => doFormStereo(pairCands);
  }

  // Live status: parallel, non-blocking, after paint. Never blocks the tab.
  if (fetchLive && strBoxes.length) setTimeout(() => refreshZoneLive(), 0);
}

// refreshZoneLive queries every speaker's live zone through the shared
// groups.js poll (non-blocking) and repaints the badges without re-fetching.
// maxAgeMs 0 keeps this tab's always-fetch behavior; when the music-tab poll
// is already in flight the call shares its result instead of skipping the
// repaint (which used to leave stale badges).
async function refreshZoneLive() {
  const ran = await fetchZoneLive(state.boxes, { maxAgeMs: 0, minBoxes: 1 });
  if (ran) renderMultiroom(false);
}

// doFormStereo creates a real left/right stereo pair on two SoundTouch 10s
// (#70). The agent drives the firmware-native POST /addGroup (LEFT = the picked
// left speaker as master, RIGHT = the partner); only the ST10 actually pairs, so
// the agent surfaces the firmware's error verbatim if a box refuses. The result
// also shows in /getGroup and the logs.
async function doFormStereo(pairCands) {
  const leftId = $('stereoLeft').value;
  const rightId = $('stereoRight').value;
  if (leftId === rightId) {
    state.stereoMsg = `<div class="setup-warn">${escapeHtml(t('multiroom.stereoSamePicked'))}</div>`;
    renderMultiroom(false);
    return;
  }
  const left = pairCands.find(b => b.deviceID === leftId);
  const right = pairCands.find(b => b.deviceID === rightId);
  if (!left || !right) return;
  $('stereoResult').innerHTML = `<div class="muted">${escapeHtml(t('common.loading'))}</div>`;
  try {
    // The picked left speaker is the master (LEFT channel); the agent assigns
    // the partner the RIGHT channel.
    await FormZone(left.host, left.port, {
      master: { deviceID: left.deviceID, ip: left.host },
      slaves: [{ deviceID: right.deviceID, ip: right.host }],
      name: '', stereo: true,
    });
    state.stereoMsg = `<div class="setup-ok">${escapeHtml(t('multiroom.stereoFormed'))}</div>`;
  } catch (e) {
    state.stereoMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(e) }))}</div>`;
  }
  renderMultiroom(true);
}

async function doFormZone(strBoxes) {
  const master = strBoxes.find(b => b.deviceID === state.zoneMaster);
  if (!master) return;
  const sel = state.zoneSlaves || {};
  const slaves = strBoxes
    .filter(b => b.deviceID !== state.zoneMaster && sel[b.deviceID])
    .map(b => ({ deviceID: b.deviceID, ip: b.host }));
  if (!slaves.length) {
    state.zoneMsg = `<div class="setup-warn">${escapeHtml(t('multiroom.pickAtLeastOne'))}</div>`;
    renderMultiroom(false);
    return;
  }
  const mode = state.zoneMode || 'native';
  $('zoneResult').innerHTML = `<div class="muted">${escapeHtml(t('common.loading'))}</div>`;
  try {
    const res = await FormZone(master.host, master.port, {
      master: { deviceID: master.deviceID, ip: master.host },
      slaves, stereo: false, mode,
    });
    // Real feedback: mirror reports back {ok,mode}; native returns the live
    // zone, so verify the firmware actually took the members.
    if (mode === 'mirror') {
      state.zoneMsg = `<div class="setup-ok">${escapeHtml(t('multiroom.formedMirror', { n: slaves.length }))}</div>`;
    } else {
      // Trust the followers' own zone self-report, not the master's optimistic
      // member list (#70). notReady = speakers that were still starting and were
      // not enrolled (app-side readiness gate); missing = speakers enrolled but
      // that never self-confirmed they joined (agent-side verify); verified =
      // speakers that confirmed. Name any not-ready speakers so the user retries.
      const notReady = (res && Array.isArray(res.notReady)) ? res.notReady : [];
      const missing = (res && Array.isArray(res.missing)) ? res.missing : [];
      const verified = (res && typeof res.verified === 'number')
        ? res.verified
        : Math.max(0, slaves.length - missing.length - notReady.length);
      const notReadyNames = notReady
        .map(ip => { const b = strBoxes.find(x => x.host === ip); return b ? zoneLabel(b) : ip; })
        .join(', ');
      if (verified <= 0 && notReady.length) {
        state.zoneMsg = `<div class="setup-warn">${escapeHtml(t('multiroom.notReady', { names: notReadyNames }))}</div>`;
      } else if (verified <= 0) {
        state.zoneMsg = `<div class="setup-warn">${escapeHtml(t('multiroom.formedNone'))}</div>`;
      } else if (missing.length || notReady.length) {
        let msg = t('multiroom.formedPartial', { joined: verified, total: slaves.length });
        if (notReady.length) msg += ' ' + t('multiroom.notReady', { names: notReadyNames });
        state.zoneMsg = `<div class="setup-warn">${escapeHtml(msg)}</div>`;
      } else {
        state.zoneMsg = `<div class="setup-ok">${escapeHtml(t('multiroom.formedN', { n: verified }))}</div>`;
      }
    }
    // Move the app's playback selection to the group master (#70 scenario c):
    // leaving it on a previous (possibly just-ungrouped) speaker sent the next
    // play command to a box OUTSIDE the fresh group, so music came out of the
    // wrong speaker while the group stayed silent.
    if (state.currentBox && state.currentBox.host !== master.host) {
      deps.selectBox(master);
    }
  } catch (e) {
    state.zoneMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(e) }))}</div>`;
  }
  renderMultiroom(true);
}

async function doDissolveZone(strBoxes) {
  const master = strBoxes.find(b => b.deviceID === state.zoneMaster);
  if (!master) return;
  try {
    await DissolveZone(master.host, master.port);
    state.zoneMsg = `<div class="setup-ok">${escapeHtml(t('multiroom.noZone'))}</div>`;
  } catch (e) {
    state.zoneMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(e) }))}</div>`;
  }
  renderMultiroom(true);
}
