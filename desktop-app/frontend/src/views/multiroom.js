// views/multiroom.js — the "Multi-Room" (zones + stereo pair) view (#70).
//
// Extracted from the main.js monolith, same pattern as views/recent.js: the
// module pulls shared things (state, utils, i18n, api) from their modules and
// receives the few main.js-local helpers it needs (boxNeedsUpdate, zoneLabel,
// discoverBoxes) via initMultiroomView, so it never imports back into main.js.

import { state } from '../state.js';
import { $, escapeHtml, escapeAttr, getBoxLabel, showToast, balanceLabel, STEREO_ICON, GROUP_ICON } from '../utils.js';
import { t } from '../i18n/index.js';
import { FormZone, DissolveZone, DissolveStereoPair, PushStereoPairNameToBox, WakeBox, BrowserOpenURL, readBoxBalance } from '../api.js';
// Group membership + the shared zoneLive poll live in groups.js: ONE
// implementation for this tab, the music-tab frames and the group chips.
import { masterOf as zoneMasterOf, fetchZoneLive, groupMembersOf, stereoPairsOf, stereoPairKey, stereoSelectionPick, pairMemberBoxes, stereoUndoTargets, groupColorMap, zoneOrPairMaster } from '../groups.js';
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

// A success notice is transient: it confirms the action just taken and then
// has to get out of the way. state.stereoMsg used to be set and never cleared,
// so the green "pair created" confirmation stayed on screen forever and ended
// up sitting above a status that had since changed and contradicted it - the
// screenshot in #821 showed "Stereo pair created ..." right over "No stereo
// pair on these speakers right now." Only the green confirmations auto-clear;
// errors and warnings stay until the next action, since the user still has to
// act on them. The token guards a newer message from being wiped by an older
// timer, and the identity check makes sure we only clear the message we set.
// Cached stereo-pair balance readout. Without it the balance div was rendered
// `hidden` on every one of the 5s live repaints and revealed async after a box
// read, so the Multi-Room panel jumped a line each time (#821). Cache the text
// keyed on the pair master and render it in place, so the div keeps its height
// across repaints; a different pair does not show the previous one's value.
let pairBalanceText = '';
let pairBalanceMaster = '';

let stereoMsgToken = 0;
function flashStereoMsg(html) {
  state.stereoMsg = html;
  const mine = ++stereoMsgToken;
  setTimeout(() => {
    if (stereoMsgToken === mine && state.stereoMsg === html) {
      state.stereoMsg = '';
      if (state.view === 'multiroom') renderMultiroom(false);
    }
  }, 8000);
}

// The zone (multiroom group) confirmation is transient too. It used to be
// written to state.zoneMsg and never cleared, and a second Ungroup press also
// fired a toast, so the same "Group dissolved" line showed twice at once and
// then sat in the panel forever, still there after leaving the tab and coming
// back (#843). Flash it exactly like the stereo one: one confirmation, no toast
// duplicate, gone after a few seconds. Only green confirmations auto-clear;
// errors stay until the next action because the user still has to act on them.
let zoneMsgToken = 0;
function flashZoneMsg(html) {
  state.zoneMsg = html;
  const mine = ++zoneMsgToken;
  setTimeout(() => {
    if (zoneMsgToken === mine && state.zoneMsg === html) {
      state.zoneMsg = '';
      if (state.view === 'multiroom') renderMultiroom(false);
    }
  }, 8000);
}

// pairMemberIds is the set of every deviceID that belongs to a live stereo pair,
// uppercased. Ungroup uses it to refuse to act on a pair (a pair is not a
// multiroom group; it has its own "Undo stereo pair").
function pairMemberIds(zoneLive) {
  return new Set(
    stereoPairsOf(zoneLive).flatMap(p =>
      (p.members || []).map(m => String((m && m.deviceID) || '').toUpperCase())
        .concat(p.master ? [String(p.master).toUpperCase()] : []))
      .filter(Boolean));
}

// Live pair/zone status has to track changes made ELSEWHERE - the phone page,
// the Bose app, a second PC - while this screen sits open. renderMultiroom
// fetches every speaker's live zone once on entry and then never again, so an
// external unpair used to show as stale here until the user left the tab and
// came back (#821: "The Multimode screen needs to reflect the pairing status
// without requiring the user to go to another screen and come back."). A gentle
// interval, running ONLY while this is the visible view, re-polls and repaints.
// It self-stops the moment the view changes, and switchView clears it too, so
// it can never outlive the tab or stack a second timer.
let liveTimer = null;
function startMultiroomLive() {
  if (liveTimer) return;
  liveTimer = setInterval(() => {
    if (state.view !== 'multiroom') { stopMultiroomLive(); return; }
    refreshZoneLive();
  }, 5000);
}
export function stopMultiroomLive() {
  if (liveTimer) { clearInterval(liveTimer); liveTimer = null; }
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

  const intro =
    `<div class="zone-intro">` +
    `<h2>${escapeHtml(t('multiroom.heading'))}</h2>` +
    `<p>${escapeHtml(t('multiroom.intro1'))}</p></div>`;

  // The live groups and stereo pairs, framed in the exact colours the speaker
  // picker on the Music tab uses (groupColorMap is the shared scheme), so an
  // existing group reads the same on both pages (Jens 2026-08-30). Read-only
  // status; the controls below are what change it. colorMap is also used to tint
  // the stereo channel cards, so this is computed once here.
  const colorMap = groupColorMap(state.zoneLive, strBoxes);
  const livePairsTop = stereoPairsOf(state.zoneLive);
  const pairForMasterKey = (mk) => livePairsTop.find(p => {
    const bxs = pairMemberBoxes(p, strBoxes).map(x => x.box).filter(Boolean);
    const mb = bxs.find(b => (b.deviceID || '').toUpperCase() === (p.master || '').toUpperCase()) || bxs[0] || null;
    return mb && (mb.deviceID || '').toUpperCase() === mk;
  }) || null;
  const frameKeys = Object.keys(colorMap).sort();
  const liveFramesHtml = frameKeys.length
    ? `<div class="zone-frames">` + frameKeys.map(mk => {
        const members = strBoxes
          .filter(b => zoneOrPairMaster(b, state.zoneLive, strBoxes) === mk)
          .sort((a, b) => (((b.deviceID || '').toUpperCase() === mk ? 1 : 0) - ((a.deviceID || '').toUpperCase() === mk ? 1 : 0)));
        const pair = pairForMasterKey(mk);
        const masterBox = strBoxes.find(b => (b.deviceID || '').toUpperCase() === mk);
        const label = pair
          ? (pairDisplayName(pair, () => renderMultiroom(false)) || t('multiroom.stereoHeading'))
          : (masterBox ? zoneLabel(masterBox) : mk);
        const chips = members.map(b =>
          `<span class="zone-frame-chip">${escapeHtml(zoneLabel(b))}</span>`).join('');
        // A dismiss control per frame: the honest place to take THIS group apart,
        // where the user is looking at it, instead of the one shared Ungroup
        // button that could only reach whichever group liveZoneMaster happened to
        // pick (asked for by Jens; audit finding). A pair frame undoes the pair,
        // a group frame dissolves the group.
        const xTip = pair ? t('multiroom.undoPairTip') : t('multiroom.dissolveGroupTip');
        const xBtn = `<button class="box-group-x" data-dissolve="${escapeAttr(mk)}" data-dissolve-kind="${pair ? 'pair' : 'zone'}" title="${escapeAttr(xTip)}" aria-label="${escapeAttr(xTip)}">&times;</button>`;
        // #775: mark a pair with the two-speaker icon and show the user's own
        // name as-is, instead of prepending the word "Stereo" to it. Matches how
        // the music tab already labels the same frame (main.js groupLabel).
        const frameIcon = pair ? STEREO_ICON : GROUP_ICON;
        const frameTip = pair ? t('speaker.stereoPairTitle') : t('speaker.groupLabelTitle', { name: label });
        return `<div class="box-group box-group-c${colorMap[mk]}">` + xBtn +
          `<span class="box-group-label" title="${escapeAttr(frameTip)}">${frameIcon} ${escapeHtml(label)}</span>` +
          chips + `</div>`;
      }).join('') + `</div>`
    : '';

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

  // A live stereo pair stays OUT of the zone grid (#792): enrolling one
  // half serves two masters and plays out of sync with its own pair, and a
  // zone stacked on the pair's master never starts the firmware's audio
  // distribution (log-proven, 12-speaker household 2026-08-30). Until pairs
  // can join as one unit, the honest offer is: dissolve the pair, group,
  // re-pair afterwards - and the grid says so instead of hiding boxes
  // silently.
  const groupablePairIDs = new Set(
    stereoPairsOf(state.zoneLive).flatMap(pp => (pp.members || []).map(m => String(m.deviceID || '').toUpperCase())));
  const zoneBoxes = strBoxes.filter(b => !groupablePairIDs.has(String(b.deviceID || '').toUpperCase()));
  const pairHiddenCount = strBoxes.length - zoneBoxes.length;
  const cards = zoneBoxes.length
    ? zoneBoxes.map(b => {
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
      + (pairHiddenCount > 0 ? `<div class="muted small">${escapeHtml(t('multiroom.pairNotGroupable'))}</div>` : '')
    : `<div class="muted">${escapeHtml(t('multiroom.noSpeaker'))}</div>`
      + (pairHiddenCount > 0 ? `<div class="muted small">${escapeHtml(t('multiroom.pairNotGroupable'))}</div>` : '');
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
  // Which two speakers the dropdowns show: a still-valid USER pick wins per
  // slot, the first live pair only fills what the user has not chosen (the
  // 2026-08-04 "sit on a real pair by default" behaviour, first paint has no
  // remembered pick), and the first candidates are the last resort. The old
  // order put the live pair FIRST, which reverted every dropdown change on
  // the spot: the untouched second select still matched the existing pair,
  // the whole pair snapped back in, and forming a NEW pair while any pair
  // existed was impossible (field report 2026-08-29). Reaching a second
  // existing pair by picking one of its members (the 1e0eb40 intent) now
  // lives in the onchange handlers below, keyed on the FRESH pick alone.
  const liveBoxes = pairMemberBoxes(livePairs[0] || null, strBoxes).map(x => x.box).filter(Boolean);
  const pairPick = stereoSelectionPick({
    left: state.stereoLeft,
    right: state.stereoRight,
    liveIDs: liveBoxes.map(b => b.deviceID),
    candIDs: pairCands.map(b => b.deviceID),
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

  // Two channel cards that show the picked speakers by name and fill in with the
  // --brand highlight the moment the selected pair is actually live (Jens
  // 2026-08-30). They mirror the dropdowns below, so the pair reads at a glance:
  // "Living room is Left, Kitchen is Right", lit up when the pair exists.
  const stereoBoxById = (id) => strBoxes.find(b => (b.deviceID || '').toUpperCase() === String(id || '').toUpperCase());
  const stereoChannelCard = (ch, roleLabel, box) => {
    const nm = box ? zoneLabel(box) : '—';
    return `<div class="stereo-card${formingPair ? ' on' : ''}">` +
      `<span class="stereo-card-ch">${ch}</span>` +
      `<span class="stereo-card-label">${escapeHtml(roleLabel)}</span>` +
      `<span class="stereo-card-name${box ? '' : ' empty'}">${escapeHtml(nm)}</span></div>`;
  };
  const stereoCardsHtml = canPair
    ? `<div class="stereo-cards">` +
        stereoChannelCard('L', t('multiroom.stereoLeft'), stereoBoxById(pairPick[0])) +
        stereoChannelCard('R', t('multiroom.stereoRight'), stereoBoxById(pairPick[1])) +
      `</div>`
    : '';

  // The "keep permanently" help is written for the group the user is building
  // right now (Jens 2026-08-30): it names the chosen main speaker and the
  // members currently ticked, so it reads as "press a preset on Wohnzimmer and
  // Kueche + Buero come along", not an abstract rule. Falls back to naming just
  // the main speaker until members are picked, and to the generic line if there
  // is no main yet.
  const permMasterBox = strBoxes.find(b => b.deviceID === state.zoneMaster);
  const permMasterName = permMasterBox ? zoneLabel(permMasterBox) : '';
  const permMemberNames = strBoxes
    .filter(b => b.deviceID !== state.zoneMaster && state.zoneSlaves && state.zoneSlaves[b.deviceID])
    .map(b => zoneLabel(b));
  const permanentHelpText = permMasterName
    ? (permMemberNames.length
        ? t('multiroom.permanentHelpConcrete', { master: permMasterName, members: permMemberNames.join(', ') })
        : t('multiroom.permanentHelpMasterOnly', { master: permMasterName }))
    : t('multiroom.permanentHelp');

  // The pair's balance belongs here, where the pair is made and undone, and
  // nowhere near a volume slider: it is a READ-OUT, not a control. The firmware
  // accepts no balance write that sticks (every attempt hung the endpoint until
  // the speaker was woken), so shown beside a slider it reads as a control that
  // is broken. An owner said exactly that: "steht neben dem Lautstaerkeregler
  // und hat auch keinen Effekt" (2026-08-09), and #70 asked twice where it was.
  const fpMaster = formingPair ? String(formingPair.master || '').toUpperCase() : '';
  const showBal = !!(formingPair && pairBalanceText && fpMaster === pairBalanceMaster);
  const pairBalance = formingPair
    ? `<div class="muted small" id="pairBalance"${showBal ? '' : ' hidden'}>${showBal ? escapeHtml(pairBalanceText) : ''}</div>`
    : '';

  root.innerHTML = intro + liveFramesHtml + topbar + previewNote + updateWarn +
    `<div class="zone-pick-hint muted small">${escapeHtml(t('multiroom.pickHint'))}</div>
     <div class="zone-cards">${cards}</div>
     ${pairBalance}
     <div class="zone-controls">
       <label class="zone-permanent-card${state.zonePermanent ? ' on' : ''}">
         <input type="checkbox" id="zonePermanent"${state.zonePermanent ? ' checked' : ''}/>
         <span class="zone-permanent-body">
           <span class="zone-permanent-title"><span class="zone-permanent-icon" aria-hidden="true">&#128257;</span>${escapeHtml(t('multiroom.permanentLabel'))}<span class="str-badge" title="${escapeAttr(t('common.strOnlyHint'))}">${escapeHtml(t('common.strOnly'))}</span></span>
           <span class="muted small">${escapeHtml(permanentHelpText)}</span>
         </span>
       </label>
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

       <b>${escapeHtml(t('multiroom.stereoHeading'))}</b>
       <div class="muted small">${escapeHtml(t('multiroom.stereoNote'))}</div>
       ${canPair ? '' : `<div class="setup-warn small">${escapeHtml(t('multiroom.stereoNeedTwo'))}</div>`}
       ${canPair ? pairStatus : ''}
       ${stereoCardsHtml}
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

  // Read-only, filled after the markup exists, and only when a pair does. Skip
  // the async re-read entirely once the value is already cached for THIS pair
  // (showBal): the row is then rendered synchronously above at its full height,
  // so a post-paint readBoxBalance + el.hidden=false on every 5s repaint only
  // reflowed the "bottom half from the balance row down" for nothing (#840,
  // stereo-pair-only jitter). The read runs once, when the pair first appears or
  // its master changes; a genuine balance change still shows on the next entry.
  if (formingPair && !showBal) fillPairBalance(formingPair, strBoxes).catch(() => {});

  const refreshBtn = $('zoneRefresh');
  if (refreshBtn) refreshBtn.onclick = async () => {
    refreshBtn.disabled = true;
    try { await deps.discoverBoxes(); } catch {}
    renderMultiroom(true);
  };

  // Per-frame dismiss: dissolve exactly the group/pair whose frame carries the x.
  root.querySelectorAll('.box-group-x').forEach(x => {
    x.onclick = (e) => {
      e.stopPropagation();
      const mk = String(x.dataset.dissolve || '').toUpperCase();
      if (x.dataset.dissolveKind === 'pair') {
        const pair = stereoPairsOf(state.zoneLive).find(p =>
          String(p.master || '').toUpperCase() === mk ||
          (p.members || []).some(m => String((m && m.deviceID) || '').toUpperCase() === mk));
        doDissolveStereoPair(pair, strBoxes);
      } else {
        const mb = strBoxes.find(b => String(b.deviceID || '').toUpperCase() === mk);
        doDissolveZoneAt(mb);
      }
    };
  });

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
    if (perm) perm.onchange = () => {
      state.zonePermanent = perm.checked;
      const card = perm.closest('.zone-permanent-card');
      if (card) card.classList.toggle('on', perm.checked);
    };
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
    //
    // snapPair keys ONLY on the freshly picked id: picking a member of an
    // existing pair loads both of its members (so rename/dissolve reach a
    // second pair, the 1e0eb40 intent), while picking an unpaired speaker
    // sticks and lets a NEW pair be formed next to the existing one (field
    // report 2026-08-29). The old render-time snap matched against BOTH
    // selects, so the untouched side re-attached the old pair on every click.
    const selUpper = (id) => String(id || '').toUpperCase();
    const snapPair = (pickedId) => {
      const p = livePairs.find(pr => (pr.members || []).some(m => selUpper(m && m.deviceID) === selUpper(pickedId)));
      const mb = p ? pairMemberBoxes(p, strBoxes).map(x => x.box).filter(Boolean) : [];
      if (mb.length === 2) {
        state.stereoLeft = mb[0].deviceID;
        state.stereoRight = mb[1].deviceID;
      }
    };
    if (left) left.onchange = () => { state.stereoLeft = left.value; snapPair(left.value); delete state.stereoName; renderMultiroom(false); };
    if (right) right.onchange = () => { state.stereoRight = right.value; snapPair(right.value); delete state.stereoName; renderMultiroom(false); };
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
      const newName = $('stereoName').value;
      try { await setPairName(formingPair, newName); } catch {}
      // Push the name into BOTH members' firmware pair records too: that is
      // the record the Bose app reads and edits, so the rename shows up there
      // as well (field report, 2026-08-30). Best effort per member; the
      // app-side store above already carries the STR display.
      try {
        const members = pairMemberBoxes(formingPair, strBoxes).map(x => x.box).filter(Boolean);
        await Promise.allSettled(members.map(b => PushStereoPairNameToBox(b.host, b.port, newName.trim())));
      } catch {}
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
  // The one-shot fetch keeps entry snappy; startMultiroomLive then keeps the
  // pair/zone status current against changes made elsewhere while this screen
  // stays open (#821). Guarded, so the repeated renderMultiroom(true) from the
  // form/dissolve handlers never stacks a second interval.
  if (fetchLive && strBoxes.length) setTimeout(() => refreshZoneLive(), 0);
  if (strBoxes.length && state.view === 'multiroom') startMultiroomLive();
}

// refreshZoneLive queries every speaker's live zone through the shared
// groups.js poll (non-blocking) and repaints the badges without re-fetching.
// maxAgeMs 0 keeps this tab's always-fetch behavior; when the music-tab poll
// is already in flight the call shares its result instead of skipping the
// repaint (which used to leave stale badges).
async function refreshZoneLive() {
  const ran = await fetchZoneLive(state.boxes, { maxAgeMs: 0, minBoxes: 1 });
  if (!ran) return;
  // Do not repaint while the user is typing the pair name. The repaint rebuilds
  // the input, which drops focus mid-word and swallows the keystrokes, so the
  // box beeps and nothing lands in the field (#775). The live poll waits a
  // cycle; the value is already mirrored into state.stereoName by its oninput,
  // so nothing is lost, and the next poll after they click away still picks up
  // any change made elsewhere.
  const active = document.activeElement;
  if (active && active.id === 'stereoName') return;
  renderMultiroom(false);
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
      // The typed name goes into the firmware pair document from the start,
      // so the Bose app shows it too instead of "Stereo pair (L+R)".
      name: wantName.trim(), stereo: true,
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
      flashStereoMsg(`<div class="setup-ok">${escapeHtml(t('multiroom.stereoFormed'))}</div>`);
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
    //
    // NOT for a permanent group, though: waking a standby speaker resumes its
    // last station, so waking to create a permanent group started music on the
    // speakers. A permanent group is not formed now anyway, it is stored and the
    // box forms it (waking the members) the next time the master plays, so the
    // boxes are left exactly as they are.
    const slaveBoxes = strBoxes.filter(b => b.deviceID !== state.zoneMaster && sel[b.deviceID]);
    if (!state.zonePermanent) {
      await Promise.allSettled([master, ...slaveBoxes].map(b => WakeBox(b.host, b.port)));
    }
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
  // A target the app lists as offline gets no DELETE: the call cannot clear
  // anything on a speaker that is not there, it just burns the transport
  // timeouts and surfaces a raw dial error (field, 2026-08-29: the no-pair
  // fallback aimed at an unplugged speaker and the panel showed connectex
  // noise). Say which speaker is out instead; the reachable half of a pair
  // is still asked below and clears its side.
  const offline = targets.filter(b => b.offline);
  const reachable = targets.filter(b => !b.offline);
  if (!reachable.length) {
    state.stereoMsg = `<div class="setup-warn">${escapeHtml(t('multiroom.stereoOffline', {
      names: offline.map(b => zoneLabel(b)).join(', '),
    }))}</div>`;
    renderMultiroom(false);
    return;
  }
  $('stereoResult').innerHTML = `<div class="muted">${escapeHtml(t('common.loading'))}</div>`;
  let dissolved = false;
  let failure = null;
  for (const box of reachable) {
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
    const okHtml = `<div class="setup-ok">${escapeHtml(t('multiroom.stereoDissolved'))}</div>`;
    if (offline.length) {
      // The skipped offline half keeps its side of the pair until it is back;
      // name it so a later "the pair is still there" has its explanation. This
      // carries an actionable warning, so it stays put rather than auto-clearing.
      state.stereoMsg = okHtml + `<div class="setup-warn">${escapeHtml(t('multiroom.stereoOffline', {
          names: offline.map(b => zoneLabel(b)).join(', '),
        }))}</div>`;
    } else {
      flashStereoMsg(okHtml);
    }
    // No toast on top of the inline confirmation: the two said the same thing and
    // overlapped (#843 problem 2). The inline message sits in the stereo panel
    // right where the Undo button is, so it is already in view after the click.
  } else if (failure) {
    state.stereoMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(failure) }))}</div>`;
  } else {
    // Inline only: the transient toast duplicated this same "nothing to undo"
    // text and the two overlapped (#851).
    state.stereoMsg = `<div class="setup-warn">${escapeHtml(t('multiroom.stereoNothingToUndo'))}</div>`;
  }
  renderMultiroom(true);
}

// The shared Ungroup button at the bottom. It dissolves the LIVE group, not
// whatever the star happens to sit on: dissolving through an uninvolved speaker
// did nothing and still reported success, so the group played on. A stereo pair
// is NOT a multiroom group and Ungroup must never take one apart (#843): that is
// what "Undo stereo pair" is for, and the per-frame x also handles a pair. If
// the only thing this button could reach is a pair, or nothing leads a group at
// all, say there is nothing to ungroup instead of dissolving the pair or
// claiming success on an empty action.
async function doDissolveZone(strBoxes) {
  const master = liveZoneMaster(strBoxes) || strBoxes.find(b => b.deviceID === state.zoneMaster);
  const pairs = pairMemberIds(state.zoneLive);
  if (!master || pairs.has(String(master.deviceID || '').toUpperCase())) {
    flashZoneMsg(`<div class="setup-warn">${escapeHtml(t('multiroom.nothingToUngroup'))}</div>`);
    renderMultiroom(false);
    return;
  }
  await doDissolveZoneAt(master);
}

// doDissolveZoneAt dissolves the group led by a SPECIFIC master box. Shared by
// the bottom Ungroup button and the per-frame x, so both take the group apart
// the same way and confirm the same way: one flashed confirmation, no toast
// duplicate, and it clears itself instead of sitting in the panel forever.
async function doDissolveZoneAt(master) {
  if (!master) return;
  try {
    await DissolveZone(master.host, master.port);
    flashZoneMsg(`<div class="setup-ok">${escapeHtml(t('multiroom.zoneDissolved'))}</div>`);
  } catch (e) {
    state.zoneMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(e) }))}</div>`;
  }
  renderMultiroom(true);
}

// doDissolveStereoPair undoes ONE specific live pair (the per-frame x), sending
// the undo to every reachable member because a one-sided leftover only clears
// when the half that still holds the pair is asked (the same reason
// doDissolveStereo asks both halves).
async function doDissolveStereoPair(pair, boxes) {
  if (!pair) {
    flashZoneMsg(`<div class="setup-warn">${escapeHtml(t('multiroom.stereoNothingToUndo'))}</div>`);
    renderMultiroom(false);
    return;
  }
  const targets = stereoUndoTargets(pair, boxes || []);
  const reachable = targets.filter(b => !b.offline);
  if (!reachable.length) {
    flashZoneMsg(`<div class="setup-warn">${escapeHtml(t('multiroom.stereoNothingToUndo'))}</div>`);
    renderMultiroom(false);
    return;
  }
  let dissolved = false;
  let failure = null;
  for (const box of reachable) {
    try {
      await DissolveStereoPair(box.host, box.port);
      dissolved = true;
    } catch (e) {
      if (!String((e && e.message) || e || '').includes('stereo-not-paired')) failure = e;
    }
  }
  if (dissolved) {
    flashStereoMsg(`<div class="setup-ok">${escapeHtml(t('multiroom.stereoDissolved'))}</div>`);
  } else if (failure) {
    state.stereoMsg = `<div class="setup-err">${escapeHtml(t('multiroom.formFailed', { err: String(failure) }))}</div>`;
  } else {
    flashStereoMsg(`<div class="setup-warn">${escapeHtml(t('multiroom.stereoNothingToUndo'))}</div>`);
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
  pairBalanceText = balanceLabel(v) + '. ' + t('controls.balanceTitle');
  pairBalanceMaster = String(pair.master || '').toUpperCase();
  el.textContent = pairBalanceText;
  el.hidden = false;
}
