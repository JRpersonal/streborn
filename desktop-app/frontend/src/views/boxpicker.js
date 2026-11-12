// views/boxpicker.js: the speaker picker of the Music tab (#boxSelect) and what
// choosing a speaker does, extracted from the main.js monolith.
//
// renderBoxSelect draws the tiles, the group and stereo frames, the empty state
// and the connect-by-IP field; pickBox is what a click on a tile does (and
// pickBoxFromShell the same for the UI 2.0 sidebar list, which re-arranges this
// DOM by id, so no id or class here may change); checkSpeakerDialog is the
// speaker check behind a speaker that does not answer; selectBox makes a
// speaker the controlled one, and speakerPickedInTab lets Settings and Setup do
// the same. Same pattern as the other views: nothing here touches the DOM at
// import time, and the main.js-local helpers arrive through initBoxPicker.

import { AddBoxByIP, CheckSpeaker, DissolveZone, InstallHolds, Stop } from '../api.js';
import { agoText } from '../format.js';
import {
  applyOptimisticZone,
  masterBoxForKey,
  notifyZoneLive,
  pairMemberBoxes,
  stereoPairsOf,
  masterOf as zoneMasterOf,
} from '../groups.js';
import { t } from '../i18n/index.js';
import { holdFor, holdsByHost } from '../installhold.js';
import { boxCannotReachInternet, noInternetText } from '../nointernet.js';
import { shell2Mounted, shellOfflinePick } from '../shell2/mount.js';
import { allowsInstallAgain, checkResultHTML, runSpeakerChecks } from '../speakercheck.js';
import { canRemoveFromList, removeOutcomeText } from '../speakerremove.js';
import { speakerTileState } from '../speakertile.js';
import { loadLastBox, saveLastBox, state } from '../state.js';
import { pairDisplayName } from '../stereoNames.js';
import { updateRunningChipHTML } from '../updaterunning.js';
import {
  $,
  GROUP_ICON,
  STEREO_ICON,
  confirmWarn,
  escapeAttr,
  escapeHtml,
  getBoxLabel,
  showError,
  showToast,
} from '../utils.js';
import { libraryFollowSpeaker } from './library.js';
import {
  dissolveIncompleteMessage,
  loadMusicTabVolume,
  loadPresets,
  loadStickRegion,
  loadTaxonomy,
  refreshBalance,
  refreshSourceButtons,
  refreshStatus,
  renderGroupControl,
  renderNowPlayingBar,
  resetNowPlaying,
  updatePandoraModeBtn,
} from './music/index.js';
import { recentFollowsShellPick } from './recent.js';
import { loadBoxSettings } from './settings.js';
import { installRunActive, renderSetupTargetPicker } from './setup.js';

// Injected main.js helpers (see initBoxPicker). These stay in main.js because
// the rest of the app calls them too; the picker calls them as deps.<name>. The
// defaults are no-ops so the module stays importable before main.js has run.
let deps = {
  // A tile shows "Update" when its speaker is behind this app.
  boxNeedsUpdate() { return false; },
  // The three banners under the picker are re-checked with every repaint.
  checkBoxIssueBanner() {},
  checkWedgeBanner() {},
  renderFirewallBanner() {},
  // selectBox re-runs the per-speaker update check for the new speaker.
  checkBoxUpdate() {},
  // The empty state's retry, an offline pick, a speaker added by IP and the
  // speaker check all start a fresh search.
  discoverBoxes() {},
  // Dissolving a group from its frame re-reads the live zones.
  refreshMusicZones() {},
  // The x on a greyed tile takes the speaker's entry off the list.
  removeSpeakerEntry() { return Promise.resolve({ status: 'failed', error: '' }); },
  // A stock speaker is an invitation into Setup; the settings shortcut on a
  // tile opens Speaker settings.
  switchView() {},
  // Every repaint ends by showing or hiding the controls for the selection.
  updateBoxUiVisibility() {},
};
export function initBoxPicker(d) {
  deps = { ...deps, ...d };
}

// manualIpInputBusy reports whether a manual connect-by-IP input is in use
// (focused or holding text). While one is, the speaker area must not be
// re-rendered: the recovery burst repaints every ~6s and a rebuild wiped the
// user's half-typed address and their focus. Both the empty state's field and
// the one behind the "add by IP" tile count.
export function manualIpInputBusy() {
  return ['emptyIpInput', 'listIpInput'].some(id => {
    const el = document.getElementById(id);
    if (!el) return false;
    return document.activeElement === el || !!(el.value || '').trim();
  });
}

// wireManualIp connects one connect-by-IP input/button pair. Shared by the
// empty state and by the "add by IP" tile that sits at the end of a populated
// speaker list: on a routed network, discovery finds ONE speaker and the empty
// state disappears, which used to leave no way at all to add the second and
// third by address (#420).
function wireManualIp(inputId, btnId) {
  const ipInput = document.getElementById(inputId);
  const addIp = document.getElementById(btnId);
  if (!ipInput || !addIp) return;
  const tryAddIp = async () => {
    const ip = (ipInput.value || '').trim();
    if (!ip) { ipInput.focus(); return; }
    addIp.disabled = true;
    showToast(t('speaker.manualIpSearching', { ip }));
    try {
      await AddBoxByIP(ip);
      ipInput.value = ''; // so the next address can be typed straight away
      await deps.discoverBoxes(); // RefreshKnownBoxes now includes the cached box
    } catch (e) {
      showError(t('speaker.manualIpNotFound', { ip }));
    } finally {
      addIp.disabled = false;
    }
  };
  addIp.onclick = tryAddIp;
  ipInput.onkeydown = (e) => { if (e.key === 'Enter') tryAddIp(); };
}

// offlineTitle is an offline speaker tile's tooltip: "how long unseen", worded
// by agoText (format.js) in the app language.
export function offlineTitle(b) {
  return t('speaker.offlineTooltip', { ago: agoText(b.offlineSinceSec || 0) });
}

// dissolveGroupFrame takes apart the group led by masterKey, from the x on its
// frame in the speaker picker. Same call the Multi-Room tab's x makes (the
// DELETE goes to the master and clears its stored group), then the reachable
// ex-followers are stopped so nothing keeps playing the group's stream alone.
async function dissolveGroupFrame(masterKey) {
  const mk = String(masterKey || '').toUpperCase();
  // The same resolver the frame's label uses. A plain deviceID lookup missed
  // whenever the app's record carries a different identity than the speaker's
  // own answer, and this handler then returned without a word: the x was drawn
  // and pressing it did nothing at all.
  const masterBox = masterBoxForKey(mk, state.zoneLive, state.boxes);
  if (!masterBox) {
    showToast(t('multiroom.dissolveIncomplete'));
    return;
  }
  const followers = state.boxes.filter(b => b !== masterBox && b.kind !== 'stock' && !b.offline &&
    String(((state.zoneLive || {})[b.deviceID] || {}).master || '').toUpperCase() === mk);
  try {
    // Trust the speaker's verdict, the same way views/multiroom.js already
    // does. A green tick on a group that is still playing is worse than no
    // message at all: it sends the user round the loop again, which is exactly
    // what nine refused dissolves in a row looked like from the outside
    // (Juergen, eleven speakers, 2026-09-14).
    const res = await DissolveZone(masterBox.host, masterBox.port);
    if (res && res.ok === false) {
      // The group is still there, so the optimistic empty zone must NOT be
      // applied: painting it empty is what made the frame vanish and come back.
      showToast(dissolveIncompleteMessage(res));
    } else {
      await Promise.allSettled(followers.map(b => Stop(b.host, b.port)));
      state.zoneLive = applyOptimisticZone(state.zoneLive, masterBox, []);
      notifyZoneLive();
      showToast(t(res && res.nothing ? 'multiroom.nothingToUngroup' : 'group.dissolvedToast'));
    }
  } catch (e) {
    showToast(t('multiroom.formFailed', { err: String((e && e.message) || e || '') }));
  }
  renderBoxSelect();
  renderGroupControl();
  setTimeout(() => deps.refreshMusicZones(true), 1200);
}

export function renderBoxSelect() {
  const sel = $('boxSelect');
  if (state.boxes.length === 0) {
    // The empty state is static markup; while the manual-IP field is in use
    // an identical rebuild would only destroy the input, so keep the DOM.
    if (manualIpInputBusy()) return;
    sel.innerHTML = `
      <div class="empty-state">
        <div class="empty-state-title">${escapeHtml(t('speaker.emptyTitle'))}</div>
        <div class="empty-state-text">
          ${escapeHtml(t('speaker.emptyHelp1'))}
          <br><br>
          ${escapeHtml(t('speaker.emptyHelp2'))}
        </div>
        <div class="empty-state-buttons">
          <button class="btn btn-mini" id="emptyRetry">${escapeHtml(t('speaker.retry'))}</button>
          <button class="btn btn-primary btn-mini" id="emptyGoSetup">${escapeHtml(t('speaker.goSetup'))}</button>
        </div>
        <div class="manual-ip">
          <div class="empty-state-text">${escapeHtml(t('speaker.manualIpHelp'))}</div>
          <div class="manual-ip-row">
            <input type="text" id="emptyIpInput" class="manual-ip-input" placeholder="${escapeAttr(t('speaker.manualIpPlaceholder'))}" inputmode="decimal" autocomplete="off" spellcheck="false">
            <button class="btn btn-mini" id="emptyAddIpBtn">${escapeHtml(t('speaker.manualIpButton'))}</button>
          </div>
        </div>
      </div>`;
    const go = document.getElementById('emptyGoSetup');
    if (go) go.onclick = () => deps.switchView('setup');
    const rt = document.getElementById('emptyRetry');
    if (rt) rt.onclick = () => deps.discoverBoxes();
    // Manual connect-by-IP: the fallback when discovery is blocked by the
    // network (AP isolation, a different subnet, a VPN, or a security suite).
    wireManualIp('emptyIpInput', 'emptyAddIpBtn');
    deps.updateBoxUiVisibility();
  deps.checkWedgeBanner();
  deps.checkBoxIssueBanner();
  deps.renderFirewallBanner();
    return;
  }
  // Cluster speakers that share a live multiroom zone into a colored frame
  // (display only; grouping is done in the Multi-Room tab). Defensive: with no
  // live group of >=2 discovered speakers the selector renders exactly as before.
  const zlMap = state.zoneLive || {};
  // Box-shaped wrapper around the shared groups.js helper (stock boxes are
  // never zone members, whatever their deviceID collides with).
  // A stereo pair is not a multiroom zone, and the speakers report it
  // separately, so the picker used to show the two halves as two unrelated
  // speakers. That is misleading twice over: they play as one, and pressing a
  // preset on the pair is refused by the firmware (#528) with nothing on screen
  // explaining why. Framing them like a group says what is going on.
  const livePairs = stereoPairsOf(zlMap);
  // Balance is read once when the selected speaker changes, never on the status
  // poll (the speaker hangs on that endpoint while it is asleep). Pairing and
  // unpairing happen without changing the selection, so without this the value
  // would stay stale, or stay hidden for a pair formed after the speaker was
  // picked. Keyed over ALL pairs, so a second pair forming after a speaker was
  // selected still re-reads once; an unchanged set re-reads nothing.
  const pairStamp = livePairs.map(p => `${p.id}|${p.master}`).sort().join(',');
  if (pairStamp !== state.lastBalancePair) {
    state.lastBalancePair = pairStamp;
    setTimeout(() => { refreshBalance().catch(() => {}); }, 0);
  }
  // Every live pair frames under its own key (its master box's discovered
  // deviceID). pairHostToKey routes any paired box to its frame; pairByKey
  // gives the frame its pair, so each pair shows its OWN stored name. Matched on
  // host, not deviceID: a two-chip chassis announces a different id over
  // discovery than the one the firmware puts in the pair.
  const pairHostToKey = new Map();
  const pairByKey = new Map();
  for (const p of livePairs) {
    const boxes = pairMemberBoxes(p, state.boxes).map(x => x.box).filter(Boolean);
    const masterBox = boxes.find(b =>
      String(b.deviceID || '').toUpperCase() === String(p.master || '').toUpperCase()) || boxes[0] || null;
    const key = masterBox ? String(masterBox.deviceID || '').toUpperCase() : '';
    if (!key) continue;
    pairByKey.set(key, p);
    for (const b of boxes) pairHostToKey.set(b.host, key);
  }
  const masterOf = (b) => {
    if (b.kind === 'stock') return '';
    const z = zoneMasterOf(b.deviceID, zlMap);
    if (z) return z;
    const k = pairHostToKey.get(b.host);
    if (k) return k;
    return '';
  };
  const memberCount = {};
  state.boxes.forEach(b => { const m = masterOf(b); if (m) memberCount[m] = (memberCount[m] || 0) + 1; });
  // Which discovered box leads a given master key. NOT a plain deviceID
  // lookup: the key comes from the speakers' own zone documents, so it names
  // the master by the identity the SPEAKER answers with, while the box record
  // here carries whatever the app resolved for it, and those two can be
  // different values for the same speaker (field bundle 2026-09-07: three
  // single-chip SoundTouch 10s where the box's own /info id WAS the zone master
  // and the app's record held the other MAC). The lookup then found nobody, so
  // the frame had no name and no x and no chip was starred. masterBoxForKey
  // asks by address and by group shape as well, and answers null rather than
  // guessing. Resolved once per key, because every pill asks again.
  const masterBoxCache = new Map();
  const masterBoxOfKey = (m) => {
    if (!masterBoxCache.has(m)) masterBoxCache.set(m, masterBoxForKey(m, zlMap, state.boxes));
    return masterBoxCache.get(m);
  };
  // A box is a framed master only when its group actually renders a frame, i.e.
  // >=2 of its members are discovered here. This keeps the master star and the
  // frame in lock-step (never a lone star on an unframed pill). Compared by
  // HOST for the same reason: the id is the field that can disagree.
  const isFramedMaster = (b) => {
    const m = masterOf(b);
    if (!m || memberCount[m] < 2) return false;
    const resolved = masterBoxOfKey(m);
    return !!resolved && resolved.host === b.host;
  };
  // The live flags of a tile (playing, offline, update, no internet) come from
  // speakertile.js, the same reading the UI 2.0 sidebar speaker list draws from, so
  // the two never disagree about a speaker.
  const tileCtx = {
    currentBox: state.currentBox,
    nowPlayState: state.nowPlayState,
    nowSource: state.nowSource,
    boxPlaying: state.boxPlaying,
    needsUpdate: deps.boxNeedsUpdate,
    cannotReachInternet: boxCannotReachInternet,
    stereoPairs: livePairs,
    installHolds: state.installHolds,
  };
  const pill = (b) => {
    const tile = speakerTileState(b, tileCtx);
    const isStock = tile.stock;
    // Sticky offline tiles (2026-07-26): a speaker once sighted stays listed
    // when scans miss it (reboot, plug pulled), clearly greyed out with a
    // disconnect mark and a "last seen ..." tooltip, until it answers again.
    const off = tile.offline;
    const offCls = off ? ' offline' : '';
    // A speaker restarting into its update drops off the network for a
    // while: its tooltip says why, not "last seen" (#1764).
    const offTitle = off ? (tile.updateRunning ? t('speaker.updateRunningHint') : offlineTitle(b)) : '';
    const offAttr = off ? ` data-offline="1" title="${escapeAttr(offTitle)}"` : '';
    const offMark = off
      ? `<span class="box-offline-mark" aria-label="${escapeAttr(offTitle)}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="12" height="12" aria-hidden="true"><line x1="1" y1="1" x2="23" y2="23"></line><path d="M16.72 11.06A10.94 10.94 0 0 1 19 12.55"></path><path d="M5 12.55a10.94 10.94 0 0 1 5.17-2.39"></path><path d="M10.71 5.05A16 16 0 0 1 22.58 9"></path><path d="M1.42 9a15.91 15.91 0 0 1 4.7-2.88"></path><path d="M8.53 16.11a6 6 0 0 1 6.95 0"></path><line x1="12" y1="20" x2="12.01" y2="20"></line></svg></span>`
      : '';
    // An x on the greyed tile takes the entry off the list: the way out for an
    // address the speaker no longer has, which nothing finds again across a
    // routed network (#1852). Only where the speaker is not reachable, and
    // never while an update from this app restarts it (speakerremove.js).
    // It is a real button NEXT to the tile, inside a wrapper that carries the
    // positioning: the tile is a button itself, and a control inside a control
    // is skipped by screen readers.
    const removable = off && !tile.updateRunning && canRemoveFromList(b, { installHolds: state.installHolds });
    const rmTitle = removable ? t('speaker.removeFromListLabel', { name: getBoxLabel(b) }) : '';
    const rmCls = removable ? ' has-remove' : '';
    const rmBtn = removable
      ? `<button type="button" class="box-remove" data-host="${b.host}" title="${escapeAttr(rmTitle)}" aria-label="${escapeAttr(rmTitle)}">&times;</button>`
      : '';
    const withRemove = (tileHTML) => (removable ? `<span class="box-wrap">${tileHTML}${rmBtn}</span>` : tileHTML);
    // The main speaker of a group is named as such, not only starred: a lone
    // star did not read as "this one leads" (Jens, 2026-09-06).
    const groupMark = isFramedMaster(b)
      ? `<span class="box-group-master" title="${escapeAttr(t('multiroom.groupMasterTitle'))}">&#9733; ${escapeHtml(t('multiroom.mainBadge'))}</span>`
      : '';
    const active = tile.isCurrent && !isStock ? ' active' : '';
    const stockCls = isStock ? ' stock' : '';
    const label = getBoxLabel(b);
    // Model (e.g. "SoundTouch 10") right next to the name so users
    // with several speakers can tell ST10 from ST20 at a glance.
    // Fall back gracefully when an older agent only advertises the
    // generic "SoundTouch".
    const model = b.model && b.model !== 'SoundTouch'
      ? `<span class="box-model" title="${escapeAttr(t('speaker.modelTitle'))}">${escapeHtml(b.model)}</span>`
      : '';
    if (isStock) {
      // Every stock Bose speaker STR discovers is an install candidate: a box
      // reachable by IP installs over the network (stick-free), so we invite the
      // install for any model rather than blocking one. Soundbars / adapters
      // install too; their missing hardware preset buttons are noted in Setup.
      //
      // A box STR once ran on whose agent went silent for good while the stock
      // firmware still answers (uninstalled, NAND wiped, agent crashed) is a
      // stock box too, but says so: a grey "STR not running" instead of the
      // first-time "Ready for STR", so the owner knows this is a repair, not
      // a speaker that never had STR (2026-09-06 report: the card kept the
      // old version badge on a speaker STR had just been removed from).
      //
      // A speaker an update from this app is still finishing on says that, and
      // its tooltip offers no install: the guard would refuse it (field case
      // 2026-10-30, the badge said "STR not running" and the tooltip "Click
      // to install STR again" while the update was still coming up).
      const gone = tile.strNotRunning;
      const finishing = tile.updateFinishing;
      const badgeKey = finishing ? 'speaker.updateFinishingBadge' : gone ? 'speaker.strNotRunningBadge' : 'speaker.needsInstallBadge';
      // A speaker an update is running or finishing on is marked so nobody
      // uses it meanwhile: bold, the accent colour, a turning ring (#1764).
      // While the update runs its agent is silent, so it looks stock here.
      const badge = tile.updateRunning ? updateRunningChipHTML()
        : finishing ? `<span class="box-stock-badge box-updating"><span class="update-running-spin" aria-hidden="true"></span>${escapeHtml(t(badgeKey))}</span>`
          : `<span class="box-stock-badge${gone ? ' box-stock-badge-gone' : ''}">${escapeHtml(t(badgeKey))}</span>`;
      const stockTitle = off ? offTitle
        : tile.updateRunning ? t('speaker.updateRunningHint')
          : t(finishing ? 'speaker.updateFinishingTooltip' : gone ? 'speaker.strNotRunningTooltip' : 'speaker.stockTooltip');
      return withRemove(`<span class="box-btn${stockCls}${offCls}${rmCls}" data-host="${b.host}" data-port="${b.port}" data-stock="1"${off ? ' data-offline="1"' : ''} role="button" tabindex="0" title="${escapeAttr(stockTitle)}">${offMark}${escapeHtml(label)}${model} <small>${b.host}</small>${badge}</span>`);
    }
    // An offline tile keeps showing the last CONFIRMED self-report, marked
    // stale: a fresh-looking version on an unreachable box is how #775's
    // falsified v-numbers stayed believable for twenty minutes.
    const ver = b.version ? `<span class="box-ver${b.offline ? ' box-ver-stale' : ''}" title="${escapeAttr(t(b.offline ? 'speaker.staleVersionTitle' : 'speaker.stickVersionTitle'))}">${escapeHtml(b.version)}</span>` : '';
    // Red dot when this speaker's agent is older than the app's embedded
    // one: a glanceable "update available" cue right on the speaker button
    // itself, in addition to the settings-tab badge and the music-tab
    // banner (#108).
    // Not while its update runs: the chip would invite a second one, and the
    // running update has its own mark (#1764).
    const showUpd = tile.needsUpdate && !tile.updateRunning;
    const updRunning = tile.updateRunning ? updateRunningChipHTML() : '';
    const updCls = showUpd ? ' needs-update' : '';
    // A WORD, not a dot: the blue dot that used to sit here reads as decoration
    // to non-technical users. A screenshot from a user whose speaker had been
    // three versions behind for days showed the dot in plain sight, twice, while
    // he was writing to ask why his speaker kept switching itself off
    // (2026-07-27). The tooltip spells out both versions.
    const updDot = showUpd
      ? `<span class="box-update-chip" role="button" tabindex="0" data-host="${b.host}" data-port="${b.port}" title="${escapeAttr(t('speaker.updateChipTitle', { box: b.version || '?', app: (state.appInfo && state.appInfo.version) || '?' }))}">${escapeHtml(t('speaker.updateChip'))}</span>`
      : '';
    // Small speaker icon on a tile whose speaker is currently playing, so the
    // playing speaker is obvious among several. The selected box is marked live
    // from nowPlayState (no poll lag); the others from the refreshBoxPlaying poll.
    const playingNow = tile.playing;
    const playMark = playingNow
      ? `<span class="box-playing" title="${escapeAttr(t('speaker.playingNow'))}" aria-label="${escapeAttr(t('speaker.playingNow'))}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="13" height="13" aria-hidden="true"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"></polygon><path d="M15.54 8.46a5 5 0 0 1 0 7.07"></path><path d="M19.07 4.93a7 7 0 0 1 0 14.14"></path></svg></span>`
      : '';
    // "No internet" right on the speaker's own tile, the router hint in its
    // tooltip: the tile is where an owner looks when a station will not play.
    const noNetChip = tile.noInternet
      ? `<span class="box-nointernet-chip" title="${escapeAttr(noInternetText(t))}">${escapeHtml(t('speaker.noInternetChip'))}</span>`
      : '';
    return withRemove(`<span class="box-btn${active}${updCls}${playingNow ? ' playing-now' : ''}${offCls}${rmCls}" data-host="${b.host}" data-port="${b.port}"${offAttr} role="button" tabindex="0">${groupMark}${offMark}${playMark}${escapeHtml(label)}${model} <small>${b.host}</small>${ver}${updRunning}${updDot}${noNetChip}<span class="box-edit" data-host="${b.host}" data-port="${b.port}" title="${escapeAttr(t('speaker.editTitle'))}">&#9881;</span></span>`);
  };
  // "Add by IP" tile, always available once at least one speaker is listed.
  // On a routed network mDNS and the local /24 scan find only the speakers on
  // this subnet, and the empty state's address field is gone the moment the
  // first one shows up, so there was no path left to add the others (#420).
  // Collapsed to a bare plus until clicked, so it stays out of the way for
  // everyone whose speakers are simply found: this is a rescue path for a
  // network that hides them, not something most people ever need. It carries
  // its name as the accessible label and its explanation as the tooltip, so
  // nothing is lost by dropping the words next to the symbol.
  const addIpTile = () => `
    <span class="box-btn box-add-ip" id="addIpTile" role="button" tabindex="0" aria-label="${escapeAttr(t('speaker.addByIp'))}" title="${escapeAttr(t('speaker.manualIpHelp'))}">+</span>
    <span class="manual-ip-row box-add-ip-row hidden" id="addIpRow">
      <input type="text" id="listIpInput" class="manual-ip-input" placeholder="${escapeAttr(t('speaker.manualIpPlaceholder'))}" inputmode="decimal" autocomplete="off" spellcheck="false">
      <button class="btn btn-mini" id="listAddIpBtn">${escapeHtml(t('speaker.manualIpButton'))}</button>
    </span>`;
  const groups = Object.keys(memberCount).filter(m => memberCount[m] >= 2).sort();
  if (groups.length === 0) {
    sel.innerHTML = state.boxes.map(pill).join('') + addIpTile();
  } else {
    // Key the frame colour to the GROUP, not to its position in the list. With
    // (i % 4) + 1 a zone changed colour whenever another zone lost a member and
    // dropped out of the list, which is the one thing an identity colour must
    // not do.
    //
    // The hash is only a PREFERENCE, though. Taken raw it collides: with four
    // slots and four zones two of them share a colour more often than not, and
    // the old positional scheme at least guaranteed four distinct colours for
    // four groups. So hash first, then linear-probe into the next free slot, so
    // a zone keeps its colour across refreshes AND simultaneous zones stay
    // distinguishable up to the four the palette holds.
    const colorOf = {};
    const taken = new Set();
    for (const m of groups) {
      let h = 0;
      for (let i = 0; i < m.length; i++) h = (h * 31 + m.charCodeAt(i)) >>> 0;
      let slot = h % 4;
      for (let k = 0; k < 4 && taken.has(slot); k++) slot = (slot + 1) % 4;
      taken.add(slot);
      colorOf[m] = slot + 1;
    }
    let html = '';
    for (const m of groups) {
      const members = state.boxes.filter(b => masterOf(b) === m);
      // master first inside the frame
      members.sort((a, b) => (((b.deviceID || '').toUpperCase() === m ? 1 : 0) - ((a.deviceID || '').toUpperCase() === m ? 1 : 0)));
      // Name the group after its master speaker so it is obvious which zone the
      // frame is (the master leads the multiroom group). Through the same
      // resolver the star uses, so a frame can never be left nameless and
      // without its x by a key the box record does not carry (see
      // masterBoxOfKey).
      const masterBox = masterBoxOfKey(m);
      const groupName = masterBox ? getBoxLabel(masterBox) : '';
      // A stereo pair gets its own wording and its own mark. Reusing the
      // multiroom label would call two speakers acting as one channel pair a
      // "group led by X", which is not what it is.
      const framePair = pairByKey.get(m) || null;
      const isPair = !!framePair;
      const pairIcon = STEREO_ICON;
      const zoneIcon = GROUP_ICON;
      // A permanent group says so on its frame: it comes back by itself when
      // the main speaker plays, a temporary one ends with the next standby.
      // The main speaker's own zone answer carries the flag.
      const permanent = !isPair && masterBox && !!(((state.zoneLive || {})[masterBox.deviceID] || {}).permanent);
      const permBadge = permanent
        ? ` <span class="box-group-perm" title="${escapeAttr(t('speaker.permanentTitle'))}">&#128257; ${escapeHtml(t('speaker.permanentBadge'))}</span>`
        : '';
      const groupLabel = groupName
        ? `<span class="box-group-label" title="${escapeAttr(isPair ? t('speaker.stereoPairTitle') : t('speaker.groupLabelTitle', { name: groupName }))}">${isPair ? pairIcon : zoneIcon} ${escapeHtml(isPair ? (pairDisplayName(framePair, renderBoxSelect) || t('multiroom.stereoHeading')) : groupName)}${permBadge}</span>`
        : '';
      // The same x the Multi-Room tab has: take THIS group apart from where it
      // is shown. It was only on the other tab (Jens, 2026-09-06). A stereo
      // pair keeps its undo on the Multi-Room tab, where both halves are named.
      const xBtn = (!isPair && masterBox)
        ? `<button class="box-group-x" data-dissolve="${escapeAttr(m)}" title="${escapeAttr(t('multiroom.dissolveGroupTip'))}" aria-label="${escapeAttr(t('multiroom.dissolveGroupTip'))}">&times;</button>`
        : '';
      html += `<div class="box-group box-group-c${colorOf[m]}${xBtn ? ' has-x' : ''}">${xBtn}${groupLabel}${members.map(pill).join('')}</div>`;
    }
    html += state.boxes.filter(b => { const mm = masterOf(b); return !(mm && memberCount[mm] >= 2); }).map(pill).join('');
    sel.innerHTML = html + addIpTile();
    sel.querySelectorAll('.box-group-x').forEach(x => {
      x.onclick = (e) => { e.stopPropagation(); dissolveGroupFrame(x.dataset.dissolve); };
    });
  }
  const ipTile = document.getElementById('addIpTile');
  const ipRow = document.getElementById('addIpRow');
  if (ipTile && ipRow) {
    const openIpRow = () => {
      ipTile.classList.add('hidden');
      ipRow.classList.remove('hidden');
      const inp = document.getElementById('listIpInput');
      if (inp) inp.focus();
    };
    ipTile.onclick = openIpRow;
    ipTile.onkeydown = (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openIpRow(); } };
  }
  wireManualIp('listIpInput', 'listAddIpBtn');
  sel.querySelectorAll('.box-btn').forEach(btn => {
    // The "add by IP" tile shares the .box-btn look but is not a speaker.
    // Without this it got the speaker handler assigned over its own, and
    // since it carries no host the handler found nothing and returned in
    // silence: the tile simply did not react to a click (live 2026-07-29).
    if (btn.classList.contains('box-add-ip')) return;
    // A click takes the same path as the UI 2.0 sidebar speaker list (pickBox).
    btn.onclick = (e) => {
      // A click on the gear icon opens the settings view rather than
      // selecting the speaker.
      if (e.target.closest('.box-edit') || e.target.closest('.box-update-chip') || e.target.closest('.box-remove')) return;
      const host = btn.dataset.host;
      const port = parseInt(btn.dataset.port, 10);
      const box = state.boxes.find(b => b.host === host && b.port === port);
      // The tile as it was drawn decides, not the live list: a greyed tile
      // whose speaker has just come back (list replaced, picker not yet
      // redrawn) still explains itself and rediscovers, as it always did.
      if (btn.dataset.offline) { pickBox({ ...(box || {}), offline: true }, btn.title); return; }
      if (!box) return;
      pickBox(box);
    };
  });
  // Gear click: set settingsBox and switch the tab.
  // The "Update" chip TAKES YOU THERE, it does not start the update. It used
  // to fire the update on the spot, from a tab where nothing about updating is
  // shown: one click on a small word next to a speaker name and the speaker
  // restarts. Updating a speaker takes minutes and reboots it, so it should be
  // a decision made on the page that explains it, not a side effect of a click
  // meant to find out what the chip even means.
  sel.querySelectorAll('.box-update-chip').forEach(chip => {
    const open = (e) => {
      e.stopPropagation();
      e.preventDefault();
      const host = chip.dataset.host;
      const port = parseInt(chip.dataset.port, 10);
      const box = (state.boxes || []).find(b => b.host === host && b.port === port);
      if (!box) return;
      state.settingsBox = box;
      deps.switchView('settings');
    };
    chip.onclick = open;
    chip.onkeydown = (e) => { if (e.key === 'Enter' || e.key === ' ') open(e); };
  });
  // The x of a greyed tile: ask, then take the entry off the list.
  sel.querySelectorAll('.box-remove').forEach(x => {
    const ask = (e) => {
      e.stopPropagation();
      e.preventDefault();
      const box = (state.boxes || []).find(b => b.host === x.dataset.host);
      if (box) removeFromListDialog(box);
    };
    // A real button: Enter and Space arrive as a click.
    x.onclick = ask;
  });
  sel.querySelectorAll('.box-edit').forEach(icon => {
    icon.onclick = (e) => {
      e.stopPropagation();
      const host = icon.dataset.host;
      const port = parseInt(icon.dataset.port, 10);
      const box = state.boxes.find(b => b.host === host && b.port === port);
      if (!box) return;
      state.settingsBox = box;
      deps.switchView('settings');
    };
  });
  if (!state.currentBox) {
    // Auto-select only STR speakers. Stock speakers cannot be
    // controlled and would put the music tab into a permanent
    // "loading" state.
    const strBoxes = state.boxes.filter(b => b.kind !== 'stock');
    const lastID = loadLastBox();
    let target = lastID ? strBoxes.find(b => b.deviceID === lastID) : null;
    if (!target && strBoxes.length === 1) target = strBoxes[0];
    if (target) selectBox(target);
  }
  deps.updateBoxUiVisibility();
  deps.checkWedgeBanner();
  deps.checkBoxIssueBanner();
  deps.renderFirewallBanner();
}

// removeFromListDialog is the x on a greyed tile: it says what removing means
// (the entry goes, the speaker is not touched, a speaker that is found again
// comes back) and, on a yes, takes the entry off the list. The outcome is a
// toast: removed, or why the entry stays (the speaker answers again, an update
// from this app is restarting it).
export async function removeFromListDialog(box) {
  if (!box || !box.host) return;
  const name = getBoxLabel(box);
  const ok = await confirmWarn(
    t('speaker.removeFromListLabel', { name }),
    t('speaker.removeConfirmBody', { name: escapeHtml(name), host: escapeHtml(box.host) }),
    { icon: null, calm: true, confirmLabel: t('speaker.removeFromList'), confirmClass: 'btn btn-primary' },
  );
  if (!ok) return;
  const res = await deps.removeSpeakerEntry(box);
  showToast(removeOutcomeText(res, name, t));
}

// pickBox is what choosing a speaker does, wherever it is chosen: the Music
// tab's picker (click or keyboard) and the UI 2.0 sidebar speaker list.
//
// An offline speaker cannot be selected (every call would fail); choosing it
// explains itself and kicks off a fresh discovery, which is also the fastest
// way to bring a returned speaker back to life in the list. A stock speaker is
// an invitation into Setup. Anything else becomes the controlled speaker.
export async function pickBox(box, titleHint) {
  if (!box) return;
  if (box.offline) {
    // UI 2.0 preview: no toast across the window; the speaker's own row says
    // it is not reachable and that the search runs, then what came of it.
    if (shell2Mounted() && shellOfflinePick(box, deps.discoverBoxes())) return;
    // The search runs as it always did; the check says WHY the speaker is
    // not answering and what to try, which "last seen ..." alone never did.
    // Awaited, like every dialog of pickBox: pickBoxFromShell restores a
    // running install's setup target only after pickBox is done.
    deps.discoverBoxes();
    if (box.host) { await checkSpeakerDialog(box); return; }
    const title = titleHint || '';
    showToast(title || t('speaker.offlineTooltip', { ago: '' }));
    return;
  }
  if (box.kind === 'stock') {
    // Any stock Bose speaker is the happy path now: reachable boxes install
    // over the network (stick-free), so we invite every model into Setup with
    // a positive CTA instead of blocking soundbars / adapters. Setup notes the
    // hardware-preset-button caveat for 'limited' models.
    const label = getBoxLabel(box);
    // An update from this app still finishing on the speaker makes it look
    // stock (its agent is restarting into the new version), and the install
    // this dialog invites would be refused by the install guard. Say what is
    // going on instead (field case 2026-10-30). A failed call falls through to
    // the usual invitation; the guard stays the backstop.
    let hold = null;
    try { hold = holdFor(holdsByHost(await InstallHolds([box.host])), box.host); } catch {}
    if (hold) {
      showToast(t('speaker.updateFinishingToast', { label }));
      return;
    }
    // A speaker whose STR stopped answering is checked first. "Install STR
    // again" was the only direction it used to get, and for a speaker that is
    // off the network, in another network or hung, a new install cannot help.
    // The check keeps the install on offer only when the Bose firmware answers
    // and STR does not (checkSpeakerDialog).
    if (box.strNotRunning) { await checkSpeakerDialog(box); return; }
    const ok = await confirmWarn(
      t('speaker.stockConfirmTitle'),
      t('speaker.stockConfirmBody', { label: escapeHtml(label) }),
      { icon: null, confirmLabel: t('speaker.stockConfirmCta'), confirmClass: 'btn btn-primary' },
    );
    // Pin the clicked box as the setup target so Setup opens on the
    // network-install hero for THIS speaker (not an arbitrary one on a
    // multi-box LAN, #44). The install starts from the hero button, not here.
    if (ok) { state.setupTarget = { kind: 'stock', box }; deps.switchView('setup'); }
    return;
  }
  selectBox(box);
}

// checkSpeakerDialog runs the speaker check (speakercheck.js) on a click and
// shows what it found with the steps. One check per speaker at a time; the Go
// side also joins a running check and reuses a fresh result, so nothing here
// can turn into a probe loop. The dialog's button follows the verdict: "Install
// STR again" only when the Bose firmware answers and STR does not ("To the
// install" for a Bose original STR was never on, which is simply back),
// otherwise a fresh search of the list, which is the step every other verdict
// ends on. Callers await it (pickBox), so a caller that guards a running
// install's setup target sees the dialog through.
const speakerChecksRunning = new Set();

export async function checkSpeakerDialog(box) {
  if (!box || !box.host || speakerChecksRunning.has(box.host)) return;
  speakerChecksRunning.add(box.host);
  const label = getBoxLabel(box);
  let res = null;
  try {
    showToast(t('speakerCheck.checking', { name: label }));
    [{ res }] = await runSpeakerChecks(CheckSpeaker, [box]);
  } finally {
    speakerChecksRunning.delete(box.host);
  }
  const install = allowsInstallAgain(res);
  const firstSetup = install && box.kind === 'stock' && !box.strNotRunning;
  const ok = await confirmWarn(
    t('speakerCheck.title', { name: label }),
    checkResultHTML(res, label, t, box),
    {
      icon: null,
      calm: true,
      rich: true,
      confirmLabel: t(firstSetup ? 'speaker.stockConfirmCta' : install ? 'speakerCheck.installAgain' : 'speakerCheck.refresh'),
      confirmClass: 'btn btn-primary',
      cancelLabel: t('common.close'),
    },
  );
  if (!ok) return;
  if (install) {
    const live = (state.boxes || []).find((b) => b.host === box.host) || box;
    // An offline entry that answers again: the list catches up with it.
    if (live.offline) deps.discoverBoxes();
    // A running install keeps its target; Setup then shows that install.
    if (!installRunActive()) state.setupTarget = { kind: 'stock', box: live };
    deps.switchView('setup');
    return;
  }
  deps.discoverBoxes();
}

// pickBoxFromShell is pickBox for the UI 2.0 sidebar speaker list. Unlike the Music
// tab's picker, the sidebar stays reachable while Speaker settings or Setup is
// open, so a pick there must bring the open view along: otherwise Settings
// would keep showing speaker A's form while its buttons act on B (they read
// state.settingsBox on click), and Setup would lose a chosen factory-reset
// target or the target of a running install without showing it.
export async function pickBoxFromShell(box) {
  if (!box) return;
  const view = state.view;
  const prevSettings = state.settingsBox;
  const prevTarget = state.setupTarget;
  // A running install and a deliberately chosen factory reset keep their
  // target. Picking a stock speaker is itself a Setup choice (pickBox asks
  // first), so it may replace a factory reset, never a running install.
  const keepTarget = installRunActive()
    || (!!prevTarget && prevTarget.kind === 'factory-reset' && box.kind !== 'stock');
  await pickBox(box);
  if (keepTarget && state.setupTarget !== prevTarget) state.setupTarget = prevTarget;
  if (state.view !== view) return; // pickBox moved on (stock speaker into Setup)
  if (view === 'settings' && state.settingsBox !== prevSettings) {
    // The same reset the Settings picker's own change does.
    if (state.settingsReconnect && state.settingsReconnect.timer) clearTimeout(state.settingsReconnect.timer);
    state.settingsReconnect = null;
    loadBoxSettings();
  } else if (view === 'setup' && state.setupTarget !== prevTarget) {
    renderSetupTargetPicker();
  } else if (view === 'library') {
    // Its play and key actions name the speaker they act on.
    libraryFollowSpeaker();
  } else if (view === 'recent') {
    recentFollowsShellPick();
  }
}

// speakerPickedInTab keeps the tabs in lock-step in the other direction: a
// speaker picked in Settings or Setup becomes the music tab's active box too.
// Stock boxes cannot be driven from the music tab, so they only preselect the
// Settings/Setup pickers and leave the music selection alone.
export function speakerPickedInTab(box) {
  if (!box) return;
  state.settingsBox = box;
  state.setupTarget = { kind: box.kind === 'stock' ? 'stock' : 'str', box };
  if (box.kind !== 'stock' && (!state.currentBox || state.currentBox.host !== box.host)) {
    selectBox(box);
  }
}

export function selectBox(box) {
  // Clear the cached now-playing line when actually switching to a different
  // speaker, so the status bar does not show the previous box's track until the
  // first poll for the new box lands (#207). Re-selecting the same box must not
  // blank it on every call.
  const switched = !state.currentBox || state.currentBox.host !== (box && box.host);
  state.currentBox = box;
  // Stereo balance is per-speaker and only exists on the master of a pair, so
  // it is re-read whenever the selected speaker changes. Not on the status
  // poll: the speaker does not answer this one while it is asleep.
  setTimeout(() => { refreshBalance().catch(() => {}); }, 0);
  if (box && box.deviceID) saveLastBox(box.deviceID);
  // Tab-selection lock-step: the speaker picked here is preselected in the
  // Settings and Setup tabs too (their pickers re-render on every tab entry),
  // so switching tabs never lands on a different speaker than the one the
  // user was just controlling.
  if (box) {
    state.settingsBox = box;
    state.setupTarget = { kind: box.kind === 'stock' ? 'stock' : 'str', box };
  }
  state.presetErrors = {};
  if (switched) { resetNowPlaying(); renderNowPlayingBar(); }
  renderBoxSelect();
  loadPresets();
  refreshStatus();
  deps.checkBoxUpdate();
  loadTaxonomy();
  // Pull the current volume so the music-tab slider does not start
  // at 0 — otherwise the first touch yanks it to whatever value the
  // slider was last left at. The tab-switch path in switchView()
  // also calls this, but a tab switch is not always involved (the
  // box-select buttons can fire without leaving the music view).
  loadMusicTabVolume();
  // Fetch the stick's region and use it as a default for radio search.
  // Do not overwrite a country the user has already picked manually.
  loadStickRegion();
  // The Pandora entry in Find stations belongs to this speaker's account.
  updatePandoraModeBtn();
  // Draw the input buttons this speaker actually has. A soundbar's TV and HDMI
  // sockets only appear once the speaker has listed them.
  refreshSourceButtons();
}
