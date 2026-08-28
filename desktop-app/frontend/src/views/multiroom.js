// views/multiroom.js — the "Multi-Room" (zones + stereo pair) view (#70).
//
// Extracted from the main.js monolith, same pattern as views/recent.js: the
// module pulls shared things (state, utils, i18n, api) from their modules and
// receives the few main.js-local helpers it needs (boxNeedsUpdate, zoneLabel,
// discoverBoxes) via initMultiroomView, so it never imports back into main.js.

import { state } from '../state.js';
import { $, escapeHtml, escapeAttr, getBoxLabel, showToast, balanceLabel } from '../utils.js';
import { t } from '../i18n/index.js';
import { FormZone, DissolveZone, DissolveStereoPair, WakeBox, BrowserOpenURL, readBoxBalance } from '../api.js';
// Group membership + the shared zoneLive poll live in groups.js: ONE
// implementation for this tab, the music-tab frames and the group chips.
import { masterOf as zoneMasterOf, fetchZoneLive, groupMembersOf, stereoPairsOf, stereoPairKey, pairMemberBoxes, stereoUndoTargets } from '../groups.js';
// App-side pair display name (STR keeps its own, survives updates): see stereoNames.js.
import { pairDisplayName, setPairName } from '../stereoNames.js';

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
  const livePairs = stereoPairsOf(state.zoneLive);
  // The controls act on the live pair that CONTAINS whichever speaker is
  // selected, so a household with two pairs can reach the second one: picking
  // one of its members snaps the whole pair in. Default is the first live pair,
  // which keeps the controls on a real pair rather than an unpaired speaker
  // (the 2026-08-04 fix), and forming a NEW pair (no live match) leaves the raw
  // selection alone via pairPick below.
  const selUp = (id) => String(id || '').toUpperCase();
  const pairHasSel = (p) => (p.members || []).some(m => {
    const id = selUp(m && m.deviceID);
    return id && (id === selUp(state.stereoLeft) || id === selUp(state.stereoRight));
  });
  const livePair = livePairs.find(pairHasSel) || livePairs[0] || null;
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
  // The pair the dropdowns describe: the live pair whose members match the L/R
  // selection (stereoPairKey normalizes case+order). null while forming a NEW
  // pair, so the rename/dissolve controls never act on the wrong existing pair.
  const formingPair = livePairs.find(p =>
    stereoPairKey(p) === stereoPairKey({ members: [{ deviceID: pairPick[0] }, { deviceID: pairPick[1] }] })) || null;
  const pairOpts = (sel) => pairCands
    .map(b => `<option value="${escapeAttr(b.deviceID)}"${b.deviceID === pairPick[sel] ? ' selected' : ''}>${escapeHtml(zoneLabel(b))}</option>`)
    .join('') || `<option>${escapeHtml(t('multiroom.noSpeaker'))}</option>`;
  const pairDis = canPair ? '' : ' disabled';
  // Say whether a pair exists at all. Until now the section gave no sign
  // either way, so a user could not tell a dissolve that did nothing from one
  // that worked.
  const pairStatus = formingPair
    ? `<div class="muted small">${escapeHtml(t('multiroom.stereoCurrent', {
        names: pairMemberBoxes(formingPair, strBoxes)
          .map(x => x.box ? zoneLabel(x.box) : (x.member.ip || x.member.deviceID)).join(' + '),
      }))}</div>`
    : `<div class="muted small">${escapeHtml(t('multiroom.stereoNoPair'))}</div>`;

  // The pair's own display name, kept app-side (stereoNames.js). Prefilled from
  // the store for the SELECTED pair; the async lookup repaints once it lands.
  const pairName = formingPair ? (pairDisplayName(formingPair, () => renderMultiroom(false)) || '') : '';

  // The pair's balance belongs here, where the pair is made and undone, and
  // nowhere near a volume slider: it is a READ-OUT, not a control. The firmware
  // accepts no balance write that sticks (every attempt hung the endpoint until
  // the speaker was woken), so shown beside a slider it reads as a control that
  // is broken. An owner said exactly that: "steht neben dem Lautstaerkeregler
  // und hat auch keinen Effekt" (2026-08-09), and #70 asked twice where it was.
  const pairBalance = formingPair
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
       <div class="zone-field"><label class="zone-permanent"><input type="checkbox" id="zonePermanent"${state.zonePermanent ? ' checked' : ''}/> ${escapeHtml(t('multiroom.permanentLabel'))}</label>
         <span class="muted small">${escapeHtml(t('multiroom.permanentHelp'))}</span></div>
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
       ${canPair ? pairStatus : ''}
       <label class="zone-field"><span>${escapeHtml(t('multiroom.stereoLeft'))}</span>
         <select id="stereoLeft"${pairDis}>${pairOpts(0)}</select></label>
       <label class="zone-field"><span>${escapeHtml(t('multiroom.stereoRight'))}</span>
         <select id="stereoRight"${pairDis}>${pairOpts(1)}</select></label>
       <label class="zone-field"><span>${escapeHtml(t('multiroom.stereoName'))}</span>
         <input id="stereoName" type="text" maxlength="40" placeholder="${escapeAttr(t('multiroom.stereoNamePlaceholder'))}" value="${escapeAttr(state.stereoName != null ? state.stereoName : pairName)}"${pairDis}></label>
       <div class="zone-actions">
         <button id="stereoCreate" class="btn"${pairDis}>${escapeHtml(t('multiroom.stereoCreateBtn'))}</button>
         ${formingPair ? `<button id="stereoNameSave" class="btn btn-mini">${escapeHtml(t('multiroom.stereoNameSaveBtn'))}</button>` : ''}
         <button id="stereoDissolve" class="btn btn-mini"${pairDis}>${escapeHtml(t('multiroom.stereoDissolveBtn'))}</button>
       </div>
       <div id="stereoResult">${state.stereoMsg || ''}</div>
     </div>`;

  // Read-only, filled after the markup exists, and only when a pair does.
  if (formingPair) fillPairBalance(formingPair, strBoxes).catch(() => {});

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
    const perm = $('zonePermanent');
    if (perm) perm.onchange = () => { state.zonePermanent = perm.checked; };
    $('zoneCreate').onclick = () => doFormZone(strBoxes);
    $('zoneUngroup').onclick = () => doDissolveZone(strBoxes);
  }
  if (canPair) {
    // Remember the user's choice so the next repaint (they happen on every
    // live-zone poll) does not throw it away.
    const left = $('stereoLeft'), right = $('stereoRight');
    // Dropping the in-progress name on a selection change re-seeds the single
    // Name field from the newly selected pair's stored name, so typing for one
    // pair never leaks onto another.
    if (left) left.onchange = () => { state.stereoLeft = left.value; delete state.stereoName; renderMultiroom(false); };
    if (right) right.onchange = () => { state.stereoRight = right.value; delete state.stereoName; renderMultiroom(false); };
    $('stereoCreate').onclick = () => doFormStereo(pairCands);
    // Keep the typed name across the automatic live-poll repaints (which rebuild
    // this markup wholesale), the same way the L/R selects persist to state.
    const nm = $('stereoName');
    if (nm) nm.oninput = () => { state.stereoName = nm.value; };
    // Rename an existing pair: STR stores the name app-side, keyed on the pair's
    // member deviceIDs, so it survives an update. A blank name clears it back to
    // the default heading. Clear the in-progress state so the field re-seeds from
    // the freshly stored name.
    const ns = $('stereoNameSave');
    if (ns) ns.onclick = async () => {
      try { await setPairName(formingPair, $('stereoName').value); } catch {}
      delete state.stereoName;
      renderMultiroom(false);
    };
    // A pair could be created but never undone: the button to make one sat
    // right there while its counterpart did not exist, so the only way out
    // was the old Bose app (discussion #499). Dissolving is the operation
    // the zone section already offers, applied to the speakers chosen above.
    $('stereoDissolve').onclick = () => doDissolveStereo(pairCands);
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
  // Read the typed name before any await, while this DOM is still live.
  const wantName = $('stereoName') ? $('stereoName').value : '';
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
      // Persist the user-given name app-side, keyed on the two members. The
      // desktop normalizes these deviceIDs to the firmware /info id, so the key
      // matches the one the live pair reports at display time.
      if (wantName.trim()) {
        try {
          await setPairName({ members: [{ deviceID: left.deviceID }, { deviceID: right.deviceID }] }, wantName);
        } catch {}
      }
      // Drop the in-progress name so the field re-seeds from the stored value.
      delete state.stereoName;
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
      // Opt-in: the group re-forms (and wakes its members) whenever the
      // master starts music (#70).
      permanent: !!state.zonePermanent,
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
  // Undo the pair the user has SELECTED in the dropdowns, not just the first
  // live one, so a household with two pairs dissolves the right one.
  const livePairs = stereoPairsOf(state.zoneLive);
  const pair = livePairs.find(p =>
    stereoPairKey(p) === stereoPairKey({ members: [{ deviceID: state.stereoLeft }, { deviceID: state.stereoRight }] }))
    || livePairs[0] || null;
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
