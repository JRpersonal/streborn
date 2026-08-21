// views/multiroom.js — the "Multi-Room" (zones + stereo pair) view (#70).
//
// Extracted from the main.js monolith, same pattern as views/recent.js: the
// module pulls shared things (state, utils, i18n, api) from their modules and
// receives the few main.js-local helpers it needs (boxNeedsUpdate, zoneLabel,
// discoverBoxes) via initMultiroomView, so it never imports back into main.js.

import { state } from '../state.js';
import { $, escapeHtml, escapeAttr, getBoxLabel, showToast, balanceLabel } from '../utils.js';
import { t } from '../i18n/index.js';
import {
  FormZone, DissolveZone, DissolveStereoPair, WakeBox, BrowserOpenURL, readBoxBalance,
  BoxAgentVersion, ListZoneTemplates, SaveZoneTemplate, DeleteZoneTemplate,
  SetPermanentZoneTemplate, ActivateZoneTemplate, ZoneTemplateMirror,
} from '../api.js';
// Group membership + the shared zoneLive poll live in groups.js: ONE
// implementation for this tab, the music-tab frames and the group chips.
import { masterOf as zoneMasterOf, fetchZoneLive, groupMembersOf, stereoPairOf, pairMemberBoxes, stereoUndoTargets } from '../groups.js';

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

// liveZoneMaster returns the speaker that is leading a group RIGHT NOW, or
// null when the speakers report none. It reads the same cached self-reports
// the cards do, so the summary above the grid and the badges inside it can
// never disagree.
//
// The picked master goes first only as a tie-break: when it is leading, the
// summary describes the group the user is looking at rather than an equally
// live one somewhere else in the list.
function liveZoneMaster(strBoxes) {
  const leads = (b) => {
    const m = zoneMasterOf(b.deviceID, state.zoneLive);
    return !!m && m === String(b.deviceID || '').toUpperCase();
  };
  const picked = strBoxes.find(b => b.deviceID === state.zoneMaster);
  if (picked && leads(picked)) return picked;
  const leader = strBoxes.find(leads);
  if (leader) return leader;
  // Last resort, from the followers' side: a follower names its master even
  // when that master's own poll came back empty-handed, and a group whose
  // leader was briefly busy is still a group the user should be told about.
  for (const b of strBoxes) {
    const m = zoneMasterOf(b.deviceID, state.zoneLive);
    if (!m) continue;
    const mb = strBoxes.find(x => String(x.deviceID || '').toUpperCase() === m);
    if (mb) return mb;
  }
  return null;
}

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
  // Group templates (beta): all transient section state lives on state.* so
  // the wholesale innerHTML rebuild below cannot lose it. zoneTplCaps caches
  // the per-master zoneTemplates capability from /api/agent/version;
  // zoneTemplates the fetched lists; zoneTplMirror the local backup offered
  // for re-seeding after a speaker factory reset.
  if (!state.zoneTemplates) state.zoneTemplates = {};
  if (!state.zoneTplCaps) state.zoneTplCaps = {};
  if (!state.zoneTplMirror) state.zoneTplMirror = {};
  // The MAIN star follows the LIVE leader on every repaint, not only when
  // nothing was selected yet. Guarding this on "zoneMaster unset" left the
  // Multiroom screen on its stale default while a group led by another
  // speaker played, so it named a different main speaker than the music tab
  // (field re-test on v0.9.50, 2026-08-19: the display-loss half was healed,
  // this half persisted, reproduced with both group directions). The one
  // thing that may override the live leader is the USER's own hand-pick: it
  // is the pending choice for the next group and must not be snatched away
  // by the poll, so it pins until it either became the live leader or every
  // group is gone.
  const liveNow = liveZoneMaster(strBoxes);
  const anyLive = strBoxes.some(b => zoneMasterOf(b.deviceID, state.zoneLive));
  if (state.zoneMasterPicked && (!anyLive || (liveNow && liveNow.deviceID === state.zoneMaster))) {
    state.zoneMasterPicked = false;
  }
  if (liveNow && !state.zoneMasterPicked) {
    state.zoneMaster = liveNow.deviceID;
  }
  if (!state.zoneMaster || !strBoxes.some(b => b.deviceID === state.zoneMaster)) {
    // No live group and nothing valid selected: default to the first speaker.
    // liveZoneMaster returns the box OBJECT; zoneMaster holds a deviceID
    // string everywhere else (card badges compare, doFormZone looks it up).
    // Assigning the object (v0.9.48) made every comparison false: no card
    // ever showed MAIN and forming silently no-oped on fleets with a live
    // zone answer.
    state.zoneMaster = (liveNow && liveNow.deviceID) || (strBoxes.length ? strBoxes[0].deviceID : '');
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

  // Per-card live status from the last parallel fetch. EVERY card gets this
  // row, always. It used to be dropped entirely for a speaker that had not
  // answered the poll, so those cards were one line shorter than the rest: the
  // grid then had cards of two different heights and the second row of cards
  // started at a ragged edge. A missing answer is also the one state a user
  // most needs told, and silence was the worst way to tell it, because a card
  // with no status line looks exactly like a card whose status is still
  // loading.
  //
  // Three states, and they are what the speaker itself reports: it leads a
  // group, it follows one, or it is on its own. A speaker with no entry in the
  // map has told us nothing (never answered, or its last answer aged out of
  // the carry window in groups.js), so it is reported as not answering rather
  // than quietly counted as standalone.
  const liveLine = (b) => {
    const zl = state.zoneLive[b.deviceID];
    if (zl === undefined) {
      return `<div class="zone-live">&#9675; ${escapeHtml(t('multiroom.liveNoAnswer'))}</div>`;
    }
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
        // A BUTTON, not a span. It looked exactly like the button beside it and
        // did nothing, and the click fell through to the card, which selected
        // the outdated speaker for the group anyway: the control did the
        // opposite of what it said.
        const upd = outdated ? `<button class="zone-update-badge" data-update-id="${escapeAttr(b.deviceID)}">${escapeHtml(t('multiroom.updateFirst'))}</button>` : '';
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

  // Summary line: the group that is LIVE on the speakers, not the group the
  // card selection would make. It used to read state.zoneMaster only, which is
  // the star the user last tapped and defaults to the first speaker in the
  // list, so with the star on a speaker that is on its own the line announced
  // "no group right now" while another speaker was leading two others right
  // there in the same grid. Undoing a stereo pair was taught the same lesson
  // (doDissolveStereo below): what is true is what the speakers report, not
  // what the picker points at.
  const masterBox = liveZoneMaster(strBoxes);
  // "No group" is a claim about the speakers, so it needs both to be earned:
  // at least one speaker must have answered (before that the app knows
  // nothing), and none of them may be reporting a master. The second half
  // covers a group led by a speaker this PC has not discovered: there is no
  // name to put in the line, but the group is real and denying it would
  // contradict the cards, which say "in a group" for its members.
  const anyAnswered = strBoxes.some(b => state.zoneLive[b.deviceID] !== undefined);
  const anyGrouped = strBoxes.some(b => zoneMasterOf(b.deviceID, state.zoneLive));
  let currentHtml = '';
  if (masterBox) {
    // groupMembersOf unions the master's own member list with the followers'
    // self-reports, so a member whose master missed a poll (or the other way
    // round) still shows up by name instead of dropping out of the line.
    const names = groupMembersOf(masterBox, state.zoneLive, strBoxes)
      .map(m => m.box ? zoneLabel(m.box) : (m.ip || m.deviceID));
    currentHtml = `<b>${escapeHtml(t('multiroom.currentZone'))}:</b> ` +
      escapeHtml(zoneLabel(masterBox) + (names.length ? ' + ' + names.join(', ') : ''));
  } else if (anyAnswered && !anyGrouped) {
    currentHtml = escapeHtml(t('multiroom.noZone'));
  }

  // Stereo pair (scaffold). Bose stereo pairing is a SoundTouch 10 feature, so
  // only ST10s are offered as candidates (matches the "needs two SoundTouch 10"
  // copy). \b10\b matches "SoundTouch 10" but not 20/30/300/Portable.
  const pairCands = strBoxes.filter(b => /\b10\b/.test(b.model || ''));
  const canPair = pairCands.length >= 2;
  // Which two speakers the dropdowns show, in order of trust: the pair that is
  // actually live on the speakers, then what the user last picked, then the
  // first two candidates. The last one used to be the ONLY rule, so with three
  // SoundTouch 10s the controls sat on two speakers that were not the paired
  // ones and every repaint put them back there ("die Lautsprecherauswahl
  // springt immer auf den nicht gepaarten Lautsprecher", field 2026-08-04).
  const livePair = stereoPairOf(state.zoneLive);
  const liveBoxes = pairMemberBoxes(livePair, strBoxes).map(x => x.box).filter(Boolean);
  const stillThere = (id) => id && pairCands.some(b => b.deviceID === id);
  const pairPick = [0, 1].map(i => {
    if (liveBoxes[i]) return liveBoxes[i].deviceID;
    const remembered = i === 0 ? state.stereoLeft : state.stereoRight;
    if (stillThere(remembered)) return remembered;
    return pairCands[i] ? pairCands[i].deviceID : '';
  });
  state.stereoLeft = pairPick[0];
  state.stereoRight = pairPick[1];
  const pairOpts = (sel) => pairCands
    .map(b => `<option value="${escapeAttr(b.deviceID)}"${b.deviceID === pairPick[sel] ? ' selected' : ''}>${escapeHtml(zoneLabel(b))}</option>`)
    .join('') || `<option>${escapeHtml(t('multiroom.noSpeaker'))}</option>`;
  const pairDis = canPair ? '' : ' disabled';
  // Say whether a pair exists at all. Until now the section gave no sign
  // either way, so a user could not tell a dissolve that did nothing from one
  // that worked.
  const pairStatus = livePair
    ? `<div class="muted small">${escapeHtml(t('multiroom.stereoCurrent', {
        names: pairMemberBoxes(livePair, strBoxes)
          .map(x => x.box ? zoneLabel(x.box) : (x.member.ip || x.member.deviceID)).join(' + '),
      }))}</div>`
    : `<div class="muted small">${escapeHtml(t('multiroom.stereoNoPair'))}</div>`;

  // The pair's balance belongs here, where the pair is made and undone, and
  // nowhere near a volume slider: it is a READ-OUT, not a control. The firmware
  // accepts no balance write that sticks (every attempt hung the endpoint until
  // the speaker was woken), so shown beside a slider it reads as a control that
  // is broken. An owner said exactly that: "steht neben dem Lautstaerkeregler
  // und hat auch keinen Effekt" (2026-08-09), and #70 asked twice where it was.
  const pairBalance = livePair
    ? `<div class="muted small" id="pairBalance" hidden></div>`
    : '';

  root.innerHTML = beta + topbar + previewNote + updateWarn +
    `<div class="zone-pick-hint muted small">${escapeHtml(t('multiroom.pickHint'))}</div>
     <div class="zone-cards">${cards}</div>
     ${pairBalance}
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

     ${zoneTemplatesSection(strBoxes)}

     <div class="zone-controls" style="margin-top:22px;border-top:1px solid var(--c-border);padding-top:16px">

       <b>${escapeHtml(t('multiroom.stereoHeading'))} <span class="beta-pill alpha-pill">${escapeHtml(t('common.alpha'))}</span></b>
       <div class="muted small">${escapeHtml(t('multiroom.stereoNote'))}</div>
       ${canPair ? '' : `<div class="setup-warn small">${escapeHtml(t('multiroom.stereoNeedTwo'))}</div>`}
       ${canPair ? pairStatus : ''}
       <label class="zone-field"><span>${escapeHtml(t('multiroom.stereoLeft'))}</span>
         <select id="stereoLeft"${pairDis}>${pairOpts(0)}</select></label>
       <label class="zone-field"><span>${escapeHtml(t('multiroom.stereoRight'))}</span>
         <select id="stereoRight"${pairDis}>${pairOpts(1)}</select></label>
       <div class="zone-actions">
         <button id="stereoCreate" class="btn"${pairDis}>${escapeHtml(t('multiroom.stereoCreateBtn'))}</button>
         <button id="stereoDissolve" class="btn btn-mini"${pairDis}>${escapeHtml(t('multiroom.stereoDissolveBtn'))}</button>
       </div>
       <div id="stereoResult">${state.stereoMsg || ''}</div>
     </div>`;

  // Read-only, filled after the markup exists, and only when a pair does.
  if (livePair) fillPairBalance(livePair, strBoxes).catch(() => {});

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
      const up = e.target.closest('.zone-update-badge');
      if (up) {
        e.stopPropagation();
        const target = (deps.allBoxes ? deps.allBoxes() : []).find(b => b.deviceID === up.dataset.updateId)
          || strBoxes.find(b => b.deviceID === up.dataset.updateId);
        if (target && deps.openSpeakerSettings) deps.openSpeakerSettings(target);
        return;
      }
      const mk = e.target.closest('.zone-makemain');
      if (mk) {
        state.zoneMaster = mk.dataset.id;
        // A hand-pick pins the star against the live-leader tracking above,
        // so preparing the next group is not undone by the running one.
        state.zoneMasterPicked = true;
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
    // Remember the user's choice so the next repaint (they happen on every
    // live-zone poll) does not throw it away.
    const left = $('stereoLeft'), right = $('stereoRight');
    if (left) left.onchange = () => { state.stereoLeft = left.value; };
    if (right) right.onchange = () => { state.stereoRight = right.value; };
    $('stereoCreate').onclick = () => doFormStereo(pairCands);
    // A pair could be created but never undone: the button to make one sat
    // right there while its counterpart did not exist, so the only way out
    // was the old Bose app (discussion #499). Dissolving is the operation
    // the zone section already offers, applied to the speakers chosen above.
    $('stereoDissolve').onclick = () => doDissolveStereo(pairCands);
  }
  wireZoneTemplateHandlers(strBoxes);

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
  // Group templates ride the same poll: capability probe + list fetch for the
  // picked master, repainting only when something actually changed.
  refreshZoneTemplates().catch(() => {});
}

// ---- Group templates (beta) ----
//
// Named speaker constellations, stored on the MASTER's agent (NAND) so the
// desktop app and the phone remote see the same list. The app is a remote
// control plus a local backup: ListZoneTemplates mirrors the list into the
// config dir, and when a factory-reset speaker comes back with an empty list
// the section offers to push the mirrored templates back.

// tplMasterBox is the speaker whose templates the section shows: the picked
// master (the star), else the speaker that is leading a group right now, else
// the speaker the app is currently on. Falling back matters: after an app
// restart nothing is pinned, and with only the star as the source the whole
// section silently vanished ("die Gruppen-Vorlagen sind nicht mehr sichtbar",
// live 2026-08-21) instead of showing the templates the speaker holds.
function tplMasterBox(strBoxes) {
  const byID = (id) => strBoxes.find(b => String(b.deviceID || '').toUpperCase() === String(id || '').toUpperCase());
  return byID(state.zoneMaster) ||
    liveZoneMaster(strBoxes) ||
    byID(state.currentBox && state.currentBox.deviceID) ||
    strBoxes[0] || null;
}

// zoneTemplatesSection builds the section markup. Pure string builder off
// state.* only: the view rebuilds innerHTML wholesale on every repaint, so
// nothing here may live in the DOM.
function zoneTemplatesSection(strBoxes) {
  const masterBox = tplMasterBox(strBoxes);
  if (!masterBox) return '';
  const head =
    `<b>${escapeHtml(t('multiroom.templatesHeading'))} <span class="beta-pill">${escapeHtml(t('common.beta'))}</span></b>` +
    `<div class="muted small">${escapeHtml(t('multiroom.templatesNote'))}</div>`;
  let body;
  const cap = state.zoneTplCaps[masterBox.deviceID];
  if (cap === 'false') {
    // The picked master's agent predates the templates endpoints: calling
    // them would hit the index catch-all. (A pending app/agent build-stamp
    // difference alone must NOT park the section: the list call carries its
    // own agent-too-old guard, so cap only goes 'false' on real evidence.)
    body = `<div class="setup-warn small">${escapeHtml(t('multiroom.templatesAgentOld'))}</div>`;
  } else if (cap === 'error') {
    body = `<div class="setup-warn small">${escapeHtml(t('multiroom.templatesUnreachable'))}</div>`;
  } else if (cap !== 'true') {
    // Capability not probed yet (refreshZoneTemplates fills it in); the
    // fetch resolves this to true/false/error, never leaves it here.
    body = `<div class="muted small">${escapeHtml(t('common.loading'))}</div>`;
  } else {
    const entry = state.zoneTemplates[masterBox.deviceID] || {};
    const tpls = entry.templates || [];
    const memberName = (m) => {
      const b = strBoxes.find(x => String(x.deviceID || '').toUpperCase() === String(m.deviceID || '').toUpperCase());
      return b ? zoneLabel(b) : (m.ip || m.deviceID || '');
    };
    const rows = tpls.map(tp => {
      const perm = !!tp.id && tp.id === entry.permanentId;
      const chip = perm ? ` <span class="zone-badge">${escapeHtml(t('multiroom.permanentActive'))}</span>` : '';
      const names = (tp.members || []).map(memberName).join(', ');
      return `<div class="zone-tpl-row" style="display:flex;align-items:center;gap:8px;flex-wrap:wrap">
          <div style="flex:1;min-width:140px"><b>${escapeHtml(tp.name || '')}</b>${chip}
            <div class="muted small">${escapeHtml(names)}</div></div>
          <button class="btn btn-mini tpl-activate" data-id="${escapeAttr(tp.id)}">${escapeHtml(t('multiroom.templateActivate'))}</button>
          <button class="btn btn-mini tpl-permanent" data-id="${escapeAttr(tp.id)}">${escapeHtml(t('multiroom.permanentLabel'))}${perm ? ' &#10003;' : ''}</button>
          <button class="btn btn-mini tpl-delete" data-id="${escapeAttr(tp.id)}">${escapeHtml(t('multiroom.templateDelete'))}</button>
        </div>`;
    }).join('');
    // Re-seed offer: the agent has no templates but the local mirror does,
    // which is what a true factory reset / reinstall looks like.
    const mir = state.zoneTplMirror[masterBox.deviceID] || [];
    const restore = (entry.fetched && !tpls.length && mir.length)
      ? `<div class="setup-warn small">${escapeHtml(t('multiroom.templatesRestoreOffer', { n: mir.length }))} ` +
        `<button id="tplRestore" class="btn btn-mini">${escapeHtml(t('multiroom.templatesRestore'))}</button></div>`
      : '';
    const saveRow = `<div class="zone-actions">
        <input id="tplName" type="text" maxlength="48"
          placeholder="${escapeAttr(t('multiroom.templateNamePlaceholder'))}"
          value="${escapeAttr(state.zoneTplName || '')}"
          style="flex:1;min-width:0;background:var(--c-bg);border:1px solid var(--c-border);color:var(--c-text);border-radius:4px;padding:6px 8px;font-size:13px">
        <button id="tplSave" class="btn btn-mini">${escapeHtml(t('multiroom.templateSave'))}</button>
      </div>`;
    // First-use guidance: with no template yet, say HOW to create one and
    // how the permanent group is reached from here (the note below explains
    // only what "permanent" does, not where to find it).
    const emptyHelp = (entry.fetched && !tpls.length && !mir.length)
      ? `<div class="muted small">${escapeHtml(t('multiroom.templatesEmptyHelp'))}</div>`
      : '';
    body = emptyHelp + rows + restore + saveRow +
      `<div id="tplResult">${state.zoneTemplateMsg || ''}</div>` +
      `<div class="muted small">${escapeHtml(t('multiroom.permanentNote'))}</div>`;
  }
  return `<div class="zone-controls" style="margin-top:22px;border-top:1px solid var(--c-border);padding-top:16px">
      ${head}${body}
    </div>`;
}

// wireZoneTemplateHandlers attaches the section's handlers after each
// innerHTML rebuild (same pattern as the zone/stereo buttons above).
function wireZoneTemplateHandlers(strBoxes) {
  const masterBox = tplMasterBox(strBoxes);
  if (!masterBox) return;
  const nameIn = $('tplName');
  if (nameIn) nameIn.oninput = () => { state.zoneTplName = nameIn.value; };
  const save = $('tplSave');
  if (save) save.onclick = () => doSaveZoneTemplate(strBoxes, masterBox);
  const restore = $('tplRestore');
  if (restore) restore.onclick = () => doRestoreZoneTemplates(masterBox);
  const root = $('view-multiroom');
  if (!root) return;
  root.querySelectorAll('.tpl-activate').forEach(btn => {
    btn.onclick = () => doActivateZoneTemplate(strBoxes, masterBox, btn.dataset.id);
  });
  root.querySelectorAll('.tpl-permanent').forEach(btn => {
    btn.onclick = () => doToggleZoneTemplatePermanent(masterBox, btn.dataset.id);
  });
  root.querySelectorAll('.tpl-delete').forEach(btn => {
    btn.onclick = () => doDeleteZoneTemplate(masterBox, btn.dataset.id);
  });
}

// refreshZoneTemplates probes the picked master's agent capability once per
// speaker (cached on state), then fetches its template list. The list read
// also refreshes the local mirror on the Go side; when the agent list is
// empty the mirror is loaded so the re-seed offer can render. Repaints only
// when something changed, so the zone poll cannot flicker the tab.
async function refreshZoneTemplates() {
  const strBoxes = (state.boxes || []).filter(b => b && b.kind !== 'stock' && b.host && b.deviceID);
  const masterBox = tplMasterBox(strBoxes);
  if (!masterBox) return;
  if (!state.zoneTplCaps) state.zoneTplCaps = {};
  if (!state.zoneTemplates) state.zoneTemplates = {};
  if (!state.zoneTplMirror) state.zoneTplMirror = {};
  let changed = false;
  if (state.zoneTplCaps[masterBox.deviceID] === undefined) {
    try {
      const v = await BoxAgentVersion(masterBox.host, masterBox.port);
      state.zoneTplCaps[masterBox.deviceID] = (v && v.zoneTemplates === 'true') ? 'true' : 'probe-missing';
    } catch {
      // Unreachable this instant: do NOT park the section in "loading".
      // The list call below carries the real agent-too-old guard and
      // resolves the state either way.
      state.zoneTplCaps[masterBox.deviceID] = 'probe-failed';
    }
    changed = true;
  }
  const capNow = state.zoneTplCaps[masterBox.deviceID];
  if (capNow === 'true' || capNow === 'probe-failed' || capNow === 'probe-missing') {
    try {
      const r = await ListZoneTemplates(masterBox.host, masterBox.port);
      // The list answered as JSON: this agent has the feature, whatever the
      // earlier capability probe thought (a stale/failed probe or a pending
      // app/agent stamp difference must not hide working templates).
      if (capNow !== 'true') {
        state.zoneTplCaps[masterBox.deviceID] = 'true';
        changed = true;
      }
      const tpls = (r && Array.isArray(r.templates)) ? r.templates : [];
      const next = {
        templates: tpls,
        permanentId: (r && r.permanentId) || '',
        fetched: true,
      };
      // Honor the "repaints only when something changed" promise above: the
      // zone poll runs this every cycle, and an unconditional repaint would
      // rebuild the tab's DOM under the user's mouse each time.
      const prev = state.zoneTemplates[masterBox.deviceID];
      if (JSON.stringify(prev) !== JSON.stringify(next)) {
        state.zoneTemplates[masterBox.deviceID] = next;
        changed = true;
      }
      if (!tpls.length) {
        try {
          const mirror = (await ZoneTemplateMirror(masterBox.deviceID)) || [];
          if (JSON.stringify(state.zoneTplMirror[masterBox.deviceID]) !== JSON.stringify(mirror)) {
            state.zoneTplMirror[masterBox.deviceID] = mirror;
            changed = true;
          }
        } catch {}
      }
    } catch (e) {
      // The Go side answers a typed "agent-too-old" error when the endpoint
      // came back as the index catch-all (200 + HTML): render the update
      // hint instead of an empty list that looks like data. Any OTHER error
      // surfaces as a visible unreachable hint, never as eternal loading;
      // the state resets to undefined so the next poll retries the probe.
      if (String((e && e.message) || e || '').includes('agent-too-old')) {
        state.zoneTplCaps[masterBox.deviceID] = 'false';
      } else {
        state.zoneTplCaps[masterBox.deviceID] = 'error';
        setTimeout(() => {
          if (state.zoneTplCaps[masterBox.deviceID] === 'error') {
            delete state.zoneTplCaps[masterBox.deviceID];
          }
        }, 15000);
      }
      changed = true;
    }
  }
  if (changed) renderMultiroom(false);
}

// doSaveZoneTemplate saves the CURRENT card selection (star = master, ticked
// cards = members) under the entered name. Exactly the member list
// doFormZone would send, so what you saved is what activation forms.
async function doSaveZoneTemplate(strBoxes, masterBox) {
  const name = (state.zoneTplName || '').trim();
  const sel = state.zoneSlaves || {};
  const members = strBoxes
    .filter(b => b.deviceID !== state.zoneMaster && sel[b.deviceID])
    .map(b => ({ deviceID: b.deviceID, ip: b.host }));
  if (!name || !members.length) {
    state.zoneTemplateMsg = `<div class="setup-warn">${escapeHtml(t('multiroom.templateNeedSelection'))}</div>`;
    renderMultiroom(false);
    return;
  }
  try {
    await SaveZoneTemplate(masterBox.host, masterBox.port, {
      id: '', name,
      master: { deviceID: masterBox.deviceID, ip: masterBox.host },
      members, permanent: false,
    });
    state.zoneTplName = '';
    state.zoneTemplateMsg = `<div class="setup-ok">${escapeHtml(t('multiroom.templateSaved', { name }))}</div>`;
  } catch (e) {
    state.zoneTemplateMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(e) }))}</div>`;
  }
  renderMultiroom(false);
  refreshZoneTemplates().catch(() => {});
}

// doActivateZoneTemplate forms the template's group in one tap. The agent
// drives the zone form and answers the same body shape as forming by hand,
// so the feedback contract below is doFormZone's verbatim: trust the
// followers' verification, name what did not join.
async function doActivateZoneTemplate(strBoxes, masterBox, id) {
  const entry = state.zoneTemplates[masterBox.deviceID] || {};
  const tpl = (entry.templates || []).find(x => x.id === id);
  if (!tpl) return;
  state.zoneTemplateMsg = `<div class="muted">${escapeHtml(t('multiroom.templateActivating', { name: tpl.name }))}</div>`;
  renderMultiroom(false);
  try {
    const res = await ActivateZoneTemplate(masterBox.host, masterBox.port, id);
    const total = (tpl.members || []).length;
    const notReady = (res && Array.isArray(res.notReady)) ? res.notReady : [];
    const missing = (res && Array.isArray(res.missing)) ? res.missing : [];
    const verified = (res && typeof res.verified === 'number')
      ? res.verified
      : Math.max(0, total - missing.length - notReady.length);
    const notReadyNames = notReady
      .map(ip => { const b = strBoxes.find(x => x.host === ip); return b ? zoneLabel(b) : ip; })
      .join(', ');
    if (res && res.ok === false && res.error) {
      state.zoneTemplateMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: res.error }))}</div>`;
    } else if (verified <= 0 && notReady.length) {
      state.zoneTemplateMsg = `<div class="setup-warn">${escapeHtml(t('multiroom.notReady', { names: notReadyNames }))}</div>`;
    } else if (verified <= 0) {
      state.zoneTemplateMsg = `<div class="setup-warn">${escapeHtml(t('multiroom.formedNone'))}</div>`;
    } else if (missing.length || notReady.length) {
      let msg = t('multiroom.formedPartial', { joined: verified, total });
      if (notReady.length) msg += ' ' + t('multiroom.notReady', { names: notReadyNames });
      state.zoneTemplateMsg = `<div class="setup-warn">${escapeHtml(msg)}</div>`;
    } else {
      state.zoneTemplateMsg = `<div class="setup-ok">${escapeHtml(t('multiroom.formedN', { n: verified }))}</div>`;
    }
    // Same follow-through as forming by hand: playback control moves to the
    // group master so the next play command reaches the group.
    if (state.currentBox && state.currentBox.host !== masterBox.host) {
      deps.selectBox(masterBox);
    }
  } catch (e) {
    state.zoneTemplateMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(e) }))}</div>`;
  }
  renderMultiroom(true);
}

// doToggleZoneTemplatePermanent flips the one permanent group on or off.
// Radio-style: setting a template permanent clears the flag everywhere else
// (the agent enforces it and answers the resulting permanentId).
async function doToggleZoneTemplatePermanent(masterBox, id) {
  const entry = state.zoneTemplates[masterBox.deviceID] || {};
  const on = entry.permanentId !== id;
  try {
    const res = await SetPermanentZoneTemplate(masterBox.host, masterBox.port, id, on);
    entry.permanentId = (res && res.permanentId) || '';
    state.zoneTemplates[masterBox.deviceID] = entry;
    state.zoneTemplateMsg = '';
  } catch (e) {
    state.zoneTemplateMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(e) }))}</div>`;
  }
  renderMultiroom(false);
  refreshZoneTemplates().catch(() => {});
}

// doDeleteZoneTemplate removes a template from the agent (and, on the Go
// side, from the local mirror, so the re-seed offer cannot bring it back).
async function doDeleteZoneTemplate(masterBox, id) {
  try {
    await DeleteZoneTemplate(masterBox.host, masterBox.port, id);
    const entry = state.zoneTemplates[masterBox.deviceID];
    if (entry) entry.templates = (entry.templates || []).filter(x => x.id !== id);
    state.zoneTemplateMsg = '';
  } catch (e) {
    state.zoneTemplateMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(e) }))}</div>`;
  }
  renderMultiroom(false);
  refreshZoneTemplates().catch(() => {});
}

// doRestoreZoneTemplates pushes the locally mirrored templates back to a
// master whose own list came back empty (factory reset / reinstall). The
// permanent flag is deliberately NOT restored: a reset counts as consent
// reset, the user re-enables it by hand.
async function doRestoreZoneTemplates(masterBox) {
  const mir = (state.zoneTplMirror && state.zoneTplMirror[masterBox.deviceID]) || [];
  if (!mir.length) return;
  let failure = null;
  for (const tp of mir) {
    try {
      await SaveZoneTemplate(masterBox.host, masterBox.port, {
        id: '', name: tp.name,
        master: { deviceID: masterBox.deviceID, ip: masterBox.host },
        members: tp.members || [], permanent: false,
      });
    } catch (e) { failure = e; }
  }
  state.zoneTemplateMsg = failure
    ? `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(failure) }))}</div>`
    : `<div class="setup-ok">${escapeHtml(t('multiroom.templateSaved', { name: mir.map(x => x.name).join(', ') }))}</div>`;
  renderMultiroom(false);
  refreshZoneTemplates().catch(() => {});
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
    const res = await FormZone(left.host, left.port, {
      master: { deviceID: left.deviceID, ip: left.host },
      slaves: [{ deviceID: right.deviceID, ip: right.host }],
      name: '', stereo: true,
    });
    // The agent answers 200 with ok:false when the firmware silently dropped a
    // member (incomplete pair) - and FormZone answers ok:false with notReady
    // when the partner's agent was still starting. Neither is success: only
    // one speaker would play, so show what actually happened.
    if (res && res.ok === false) {
      const notReady = Array.isArray(res.notReady) ? res.notReady : [];
      if (!res.error && notReady.length) {
        const names = notReady
          .map(ip => { const b = pairCands.find(x => x.host === ip); return b ? zoneLabel(b) : ip; })
          .join(', ');
        state.stereoMsg = `<div class="setup-warn">${escapeHtml(t('multiroom.notReady', { names }))}</div>`;
      } else {
        const err = res.error || t('multiroom.formedNone');
        state.stereoMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err }))}</div>`;
      }
    } else {
      state.stereoMsg = `<div class="setup-ok">${escapeHtml(t('multiroom.stereoFormed'))}</div>`;
    }
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
    // Wake the master and every selected member before enrolling them (#70): a box
    // switched off at the speaker still answers STR but would join the zone silent.
    // Waking an already-awake box is a fast no-op.
    const slaveBoxes = strBoxes.filter(b => b.deviceID !== state.zoneMaster && sel[b.deviceID]);
    await Promise.allSettled([master, ...slaveBoxes].map(b => WakeBox(b.host, b.port)));
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

// doDissolveStereo undoes a stereo pair and reports it WHERE THE USER IS
// LOOKING. Both buttons used to call doDissolveZone, which writes its outcome
// into the zone section further up the page and phrases it as "no group right
// now". So pressing "Undo stereo pair" looked like nothing had happened: the
// confirmation existed, but in another part of the page and about another
// feature. A user asked for exactly this, having watched the pair come apart
// with no sign that it had (2026-07-31). The toast makes it visible even when
// the stereo section has scrolled out of view.
//
// It also has to go to a speaker that is actually IN the pair. It used to aim
// at state.zoneMaster, the MULTIROOM master selection, which defaults to the
// first speaker in the list and has nothing to do with the pair. A user with
// three SoundTouch 10s pressed undo twice; both calls went to a speaker that
// was not paired, both returned "nothing to dissolve", the app reported
// success, and the pair was still there in the Bose app (field, 2026-08-04).
// EVERY member of the pair gets the undo, master first, because the pair does
// not reliably live where we expected. The rule used to be "ask the master, only
// its firmware reports the pair", and that held while a pair was healthy. It
// does not hold once one half has let go: measured 2026-08-10 on two SoundTouch
// 10s, the MASTER answered /getGroup with an empty group while the right-hand
// speaker still held the whole document naming the master as LEFT. Every undo
// went to the master, was told there was nothing to undo, and the app then said
// both "current stereo pair: ..." and "there is no stereo pair to undo" in the
// same panel. Sending it to the other half cleared both speakers at once.
//
// So neither half can be assumed to be the one holding it. Asking both is
// harmless (a speaker not in a pair answers "nothing to undo" and is left
// alone) and it is the only way a one-sided leftover can be cleared at all.
async function doDissolveStereo(pairCands) {
  const pair = stereoPairOf(state.zoneLive);
  const targets = stereoUndoTargets(pair, state.boxes || []);
  if (!targets.length) {
    const guess = pairCands.find(b => b.deviceID === ($('stereoLeft') || {}).value);
    if (guess) targets.push(guess);
  }
  if (!targets.length) {
    state.stereoMsg = `<div class="setup-warn">${escapeHtml(t('multiroom.stereoNothingToUndo'))}</div>`;
    renderMultiroom(false);
    return;
  }
  $('stereoResult').innerHTML = `<div class="muted">${escapeHtml(t('common.loading'))}</div>`;
  let dissolved = false;
  let failure = null;
  for (const box of targets) {
    try {
      // The stereo-intent endpoint: it also dissolves a firmware pair the agent
      // has no persisted record of (agent reinstalled, pair formed elsewhere),
      // which the plain dissolve deliberately leaves alone.
      await DissolveStereoPair(box.host, box.port);
      dissolved = true;
    } catch (e) {
      // "This speaker is not in a pair" is not an error the user should read as
      // a failure, and it must not read as success either (which is what it used
      // to do, because the agent answers 200 for it). With more than one target
      // it is also the EXPECTED answer from the half that already let go, so it
      // never stops the sweep.
      if (!String((e && e.message) || e || '').includes('stereo-not-paired')) failure = e;
    }
  }
  if (dissolved) {
    state.stereoMsg = `<div class="setup-ok">${escapeHtml(t('multiroom.stereoDissolved'))}</div>`;
    showToast(t('multiroom.stereoDissolved'));
  } else if (failure) {
    state.stereoMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(failure) }))}</div>`;
  } else {
    state.stereoMsg = `<div class="setup-warn">${escapeHtml(t('multiroom.stereoNothingToUndo'))}</div>`;
    showToast(t('multiroom.stereoNothingToUndo'));
  }
  renderMultiroom(true);
}

async function doDissolveZone(strBoxes) {
  // Send it to the speaker that leads the LIVE group, not to whichever one the
  // star happens to sit on. Dissolving through an uninvolved speaker did
  // nothing and still reported success, so the group played on.
  // liveZoneMaster hands back the speaker itself, and it prefers the selected
  // one when that one really does lead. Only when nothing leads does the star
  // decide, and then there is nothing live to disagree with.
  const master = liveZoneMaster(strBoxes) || strBoxes.find(b => b.deviceID === state.zoneMaster);
  if (!master) return;
  try {
    const res = await DissolveZone(master.host, master.port);
    // The speaker says whether the group is really gone. It reports the members
    // it could not remove, and whether it could read the result at all.
    if (res && res.ok === false) {
      state.zoneMsg = `<div class="setup-err">${escapeHtml(t('multiroom.dissolveIncomplete'))}</div>`;
    } else {
      state.zoneMsg = `<div class="setup-ok">${escapeHtml(t('multiroom.zoneDissolved'))}</div>`;
      showToast(t('multiroom.zoneDissolved'));
    }
  } catch (e) {
    state.zoneMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(e) }))}</div>`;
  }
  renderMultiroom(true);
}

// fillPairBalance shows the pair's balance as information, with where to change
// it, because here it cannot be changed. Asked from the pair's MASTER whichever
// half is selected: only the master reports one (#70).
async function fillPairBalance(pair, boxes) {
  const el = document.getElementById('pairBalance');
  if (!el || !pair) return;
  const master = pairMemberBoxes(pair, boxes).map(x => x.box)
    .find(b => b && String(b.deviceID || '').toUpperCase() === String(pair.master || '').toUpperCase());
  const src = master || pairMemberBoxes(pair, boxes).map(x => x.box).find(Boolean);
  if (!src || src.kind === 'stock') return;
  const v = await readBoxBalance(src);
  if (v === null) return;
  el.textContent = balanceLabel(v) + '. ' + t('controls.balanceTitle');
  el.hidden = false;
}
