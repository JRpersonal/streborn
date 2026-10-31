// views/music/group.js: the group control panel of the Music view (member chips,
// group volume, the effective play target of a grouped speaker).

import { BoxSettings, DissolveZone, FormZone, SetBoxVolume, Stop, WakeBox } from '../../api.js';
import {
  applyOptimisticZone,
  groupCandidates,
  groupMemberBoxes,
  groupMembersOf,
  inAnyStereoPair,
  notifyZoneLive,
  resolvePlayTarget,
} from '../../groups.js';
import { t } from '../../i18n/index.js';
import { state } from '../../state.js';
import { $, escapeAttr, escapeHtml, getBoxLabel, showError, showToast } from '../../utils.js';
// The UI 2.0 sidebar shows the same pending marks and the player the same
// group volume; both are no-ops while the preview shell is not mounted.
import { renderShellSpeakers, syncShellGroupPanel } from '../../shell2/mount.js';
import { deps } from './deps.js';

// ---- Group control (below the presets, next to Transfer) ----
// Chips to add/remove the other speakers to a multiroom group led by the
// selected speaker, plus one volume slider for the whole group. Bose forms the
// zone natively; the group's name comes from the master (see the Multi-Room tab).

// currentGroupSlaves returns the speakers in the group of the selected box,
// master excluded: groups.js's groupMemberBoxes, the same list the UI 2.0
// sidebar pins, so the group volume and the Levels sliders cover exactly the
// speakers the sidebar shows in the group.
function currentGroupSlaves() {
  const box = groupControlBox();
  if (!box || !box.deviceID) return [];
  return groupMemberBoxes(box, state.zoneLive, state.boxes);
}

// effectivePlayTarget returns the box a play command must actually go to: a
// zone FOLLOWER rejects direct UPnP control (the firmware answers 501 "Can't
// control member of group", #70), so when the selected box follows a master,
// the play belongs on that master and it distributes the audio to the group.
// Falls back to the selected box (standalone, master, or master not found).
// The decision itself is groups.js's resolvePlayTarget; this wraps it around
// the app state and logs the retarget.
export function effectivePlayTarget() {
  const box = state.currentBox;
  const target = resolvePlayTarget(box, state.zoneLive, state.boxes);
  if (target !== box && target) {
    try { console.info(`play retargeted to group lead ${target.host} (selected box is a zone follower)`); } catch {}
  }
  return target;
}

// groupControlBox returns the box the group controls operate on. A zone FOLLOWER
// has no followers of its own and the firmware rejects zone edits aimed at it, so
// the group panel used to render empty for a selected member: membership could
// only be changed while the master was selected, and picking a member to adjust
// its volume dropped the group controls with no hint the box was still grouped
// (akuethe, 8-box zone, 2026-07-14). Resolve to the master so the chips + group
// volume work from any selected member. A standalone or master box returns itself.
function groupControlBox() {
  return resolvePlayTarget(state.currentBox, state.zoneLive, state.boxes) || state.currentBox;
}

let _groupVolTimer = null;
// setGroupVolume moves the master AND every follower to pct, so the slider
// controls the whole group. Debounced so a slider drag does not flood the boxes.
// groupVol holds the per-member levels the user set, plus the group slider
// position they were captured at.
//
// The group slider is RELATIVE, and that is the whole point of #401: speakers of
// different types need different levels to sound equally loud, and the old
// behaviour of writing one absolute value to every member destroyed that balance
// on the first group adjustment. Now the group slider shifts every member by the
// same offset, and the offset is always computed against the captured baseline
// rather than against the current levels. That matters at the ends: without it,
// members that hit 0 or 100 would silently lose their spacing and never get it
// back. Computing from the baseline means returning the slider to where it was
// restores exactly the levels the user dialled in.
const groupVol = { base: {}, at: null, members: {} };

// captureGroupBaseline snapshots the members' current levels as the reference
// the group slider offsets from.
function captureGroupBaseline(anchor) {
  groupVol.base = {};
  Object.keys(groupVol.members).forEach(h => { groupVol.base[h] = groupVol.members[h]; });
  groupVol.at = anchor;
}

function setGroupVolume(pct) {
  if (_groupVolTimer) clearTimeout(_groupVolTimer);
  _groupVolTimer = setTimeout(() => {
    const box = groupControlBox();
    if (!box) return;
    const all = [box, ...currentGroupSlaves()];
    if (groupVol.at === null) captureGroupBaseline(pct);
    const delta = pct - groupVol.at;
    all.forEach(b => {
      const base = typeof groupVol.base[b.host] === 'number' ? groupVol.base[b.host] : pct;
      const v = Math.max(0, Math.min(100, Math.round(base + delta)));
      groupVol.members[b.host] = v;
      SetBoxVolume(b.host, b.port, v).catch(() => {});
      const el = document.getElementById('memberVol-' + cssId(b.host));
      if (el && document.activeElement !== el) {
        el.value = String(v);
        const lbl = document.getElementById('memberVolVal-' + cssId(b.host));
        if (lbl) lbl.textContent = String(v);
      }
    });
  }, 120);
}

// setMemberVolume sets ONE speaker in the group and makes that the new balance:
// the baseline is re-captured so the next group move offsets from what the user
// just dialled in.
function setMemberVolume(host, port, pct) {
  groupVol.members[host] = pct;
  const slider = $('groupVolume');
  captureGroupBaseline(slider ? parseInt(slider.value, 10) : pct);
  SetBoxVolume(host, port, pct).catch(() => {});
}

// cssId makes a host safe to embed in an element id.
function cssId(host) { return String(host).replace(/[^a-zA-Z0-9]/g, '_'); }

// loadGroupMemberVolumes reads each member's current level so the per-speaker
// sliders start where the speakers actually are, not at a guess.
async function loadGroupMemberVolumes(members) {
  await Promise.all(members.map(async b => {
    try {
      const data = await BoxSettings(b.host, b.port);
      const v = data && data.volume && data.volume.actual;
      if (typeof v !== 'number') return;
      groupVol.members[b.host] = v;
      const el = document.getElementById('memberVol-' + cssId(b.host));
      if (el && document.activeElement !== el) {
        el.value = String(v);
        const lbl = document.getElementById('memberVolVal-' + cssId(b.host));
        if (lbl) lbl.textContent = String(v);
      }
    } catch {}
  }));
  // Re-anchor from the freshly read levels, but never while the user is
  // touching the panel: doing that mid-drag measured the next move against
  // levels our own writes had already changed, which is how a drag gave away
  // most of its travel.
  if (groupPanelBusy()) return;
  // Position the group slider at the members' ACTUAL level (the loudest member)
  // and anchor there. The slider is relative, so if it sits at 0 while the
  // members are at 29 it can only ADD an offset, never subtract, and the group
  // cannot be turned down at all (screenshot 2026-09-03: group slider 0, members
  // 29, and every box had to be lowered by hand). Anchoring from the loudest
  // member means dragging the slider down lowers every speaker.
  const levels = Object.values(groupVol.members).filter(v => typeof v === 'number' && v >= 0);
  const rep = levels.length ? Math.max(...levels) : null;
  const slider = $('groupVolume');
  if (slider && rep !== null && document.activeElement !== slider) {
    slider.value = String(rep);
    captureGroupBaseline(rep);
  } else {
    captureGroupBaseline(slider ? parseInt(slider.value, 10) : 0);
  }
}

// toggleGroupMember adds/removes the speaker at host to/from the group led by
// the selected box. Removing the last one dissolves the zone; a removed
// speaker is stopped so the music stops on it too. The edit starts from
// groups.js's groupMembersOf — the union of the followers' self-reports and
// the master's own member list — so a follower whose single zone poll failed
// or that briefly dropped out of discovery is preserved instead of being
// silently kicked by an unrelated add/remove.
// Group edits run strictly one at a time.
//
// Forming a zone drives the master's firmware and takes a few seconds: eleven
// speakers took eighteen. Nothing stopped a second click starting a second
// drive on top of the first, and an owner who taps again because nothing has
// visibly happened is the normal case, not an unusual one. A field log from a
// twelve-speaker household (2026-08-08) shows the shape exactly: the group of
// twelve formed perfectly, then ten overlapping requests for a smaller group
// inside nineteen seconds, after which the master's /setZone stopped answering
// at all and every attempt after that timed out.
//
// Serialising is most of the fix: each edit still happens, it just waits its
// turn, and the queued one recomputes the membership when it actually runs, so
// it sees the result of the edit before it.
//
// The rest is refusing a repeat for a speaker whose edit has not finished. That
// is a toggle, so without it a second impatient tap on the same speaker would
// faithfully undo the first, and the owner who tapped twice because it felt
// slow would end up with the speaker they wanted OUT of the group. A later tap,
// once the first has completed, is a real change of mind and still works.
const groupOpPending = new Set();
let groupOpChain = Promise.resolve();

// Several chips clicked in a row become ONE group edit.
//
// Each click used to drive its own zone form, and a zone form takes seconds and
// interrupts the audio, so adding three speakers meant three interruptions and
// three repaints while the owner was still clicking. The phone remote already
// behaves the way people expect: every press is acknowledged at once and the
// speakers join together.
//
// So a click marks the chip immediately and records the intent, and the edit
// runs once the clicking stops. The window is short enough that a single click
// still feels immediate.
const GROUP_EDIT_COALESCE_MS = 600;
const pendingGroupEdits = new Map();
 // host -> port
let groupEditTimer = null;
// The master the waiting batch belongs to: the group that was on screen when
// its first click landed.
//
// The edit used to look the group up when the batch RAN, after the coalesce
// window and after any edit queued in front of it. By then the selection can
// have moved on, and in the UI 2.0 sidebar a speaker's "+" and its row sit
// side by side: "+" on Kitchen, then Kitchen's row inside the window, made
// Kitchen the group's master AND its own follower in one FormZone. Choosing
// any other speaker in that window sent the edit to that speaker's group.
let pendingGroupMaster = '';
// The report callback of the waiting batch (toggleGroupMember's report), or
// null: the batch then says its result with the classic toasts and dialog.
let pendingGroupReport = null;

// toggleGroupMember is the one way a speaker is taken in or out of the group
// led by the selected speaker: the Music view's chips and the UI 2.0 sidebar's
// "+" and "-" both call it, so there is a single group edit path.
//
// report (optional, the UI 2.0 sidebar) takes the batch's result instead of
// the toast and the error dialog: report({ tone: 'ok' | 'warn' | 'error',
// text, hosts, ports, master, added, removed }), so the sidebar can keep the
// row pending until the zone poll confirms and put a failure into the row
// itself. Without it, the toasts and the dialog say it exactly as before.
//
// masterHost names the group explicitly. The UI 2.0 sidebar pins the group
// that PLAYS, which is not the selected speaker's while you control an idle
// one, and its "+" must add to that music (#1284), so it passes the pinned
// group's master. Without it the group is the selected speaker's.
export function toggleGroupMember(host, port, masterHost, report) {
  if (groupOpPending.has(host)) return groupOpChain;
  const lead = masterHost ? null : groupControlBox();
  const leadHost = masterHost || (lead && lead.host) || '';
  // A click for a different group than the waiting batch's: that batch was
  // meant for the group it was clicked in, so it goes now, and this click
  // starts a batch of its own.
  if (pendingGroupEdits.size && pendingGroupMaster !== leadHost) {
    if (groupEditTimer) clearTimeout(groupEditTimer);
    runPendingGroupEdits();
  }
  if (!pendingGroupEdits.size) {
    pendingGroupMaster = leadHost;
    pendingGroupReport = null;
  }
  if (typeof report === 'function') pendingGroupReport = report;
  // Toggling the same speaker twice before the edit runs is a change of mind,
  // and the two cancel out rather than queueing.
  if (pendingGroupEdits.has(host)) pendingGroupEdits.delete(host);
  else pendingGroupEdits.set(host, port);
  markGroupChipPending();
  if (groupEditTimer) clearTimeout(groupEditTimer);
  groupEditTimer = setTimeout(runPendingGroupEdits, GROUP_EDIT_COALESCE_MS);
  return groupOpChain;
}

// groupEditPendingHosts lists the speakers whose group edit is waiting or
// running, for the UI 2.0 sidebar's pending marks.
export function groupEditPendingHosts() {
  return new Set([...pendingGroupEdits.keys(), ...groupOpPending]);
}

// markGroupChipPending gives the click its feedback straight away: the chip
// shows the state it is about to have, so a second click is never made because
// the first looked ignored.
function markGroupChipPending() {
  document.querySelectorAll('.group-chip').forEach(chip => {
    // Marked while the click is WAITING for the batch AND while the batch is
    // running. Keying it on the waiting list alone made the mark disappear at
    // the very moment the work started, which is when the user most needs to
    // see that something is happening: forming a group across five speakers
    // takes several seconds.
    const h = chip.dataset.host;
    chip.classList.toggle('pending', pendingGroupEdits.has(h) || groupOpPending.has(h));
  });
  // The sidebar repaints from groupEditPendingHosts. This runs on every change
  // of the pending set, including the batch's end on a path that repaints
  // nothing else (a refusal, a stock master, a batch that was dropped).
  renderShellSpeakers(true);
}

function runPendingGroupEdits() {
  groupEditTimer = null;
  const edits = [...pendingGroupEdits.entries()];
  const masterHost = pendingGroupMaster;
  const report = pendingGroupReport;
  pendingGroupEdits.clear();
  pendingGroupMaster = '';
  pendingGroupReport = null;
  if (edits.length === 0) return;
  edits.forEach(([h]) => groupOpPending.add(h));
  markGroupChipPending();
  groupOpChain = groupOpChain
    .then(() => runGroupMemberToggle(edits, masterHost, report))
    .catch(() => {})
    .finally(() => {
      edits.forEach(([h]) => groupOpPending.delete(h));
      markGroupChipPending();
    });
  return groupOpChain;
}

// dissolveIncompleteMessage turns the agent's refusal into something the user
// can act on. "remaining" is how many speakers the master still reports after
// every teardown route was tried, and naming it is the difference between "it
// did not work" and "four speakers are still in this group".
export function dissolveIncompleteMessage(res) {
  const left = res && Number(res.remaining) > 0 ? Number(res.remaining) : 0;
  return left
    ? t('multiroom.dissolveIncompleteN', { n: left })
    : t('multiroom.dissolveIncomplete');
}

// runGroupMemberToggle applies a whole batch of toggles as ONE zone edit.
// edits is [[host, port], ...]; a single click is simply a batch of one.
// masterHost is the master the batch was clicked for (toggleGroupMember).
// report, when set, takes every result the toasts and the dialog would say.
async function runGroupMemberToggle(edits, masterHost, report) {
  const base = {
    hosts: edits.map(([h]) => h),
    ports: Object.fromEntries(edits.map(([h, p]) => [h, p])),
    master: masterHost || '',
  };
  const say = (text, tone = 'ok', extra = {}) => {
    if (typeof report === 'function') report({ ...base, ...extra, tone, text });
    else showToast(text);
  };
  const fail = (text) => {
    if (typeof report === 'function') report({ ...base, tone: 'error', text });
    else showError(text);
  };
  // Edit the group the clicks were made in. When a follower was selected, its
  // master is the box that must receive the FormZone/DissolveZone (a follower
  // rejects zone control); groupControlBox resolved that at click time. A
  // master that has left the list, or that follows another speaker by now, is
  // no longer the group the clicks were about: the batch is dropped and says
  // so, rather than landing on a group nobody chose.
  const lead = masterHost ? (state.boxes || []).find(b => b && b.host === masterHost) : null;
  const box = lead && resolvePlayTarget(lead, state.zoneLive, state.boxes) === lead ? lead : null;
  if (!box) {
    if (masterHost) say(t('group.editStale'), 'warn');
    return;
  }
  if (box.kind === 'stock') return;
  // A speaker is never its own follower, whatever the clicks were.
  const targets = edits
    .map(([h]) => (state.boxes || []).find(b => b.host === h))
    .filter(b => b && b.host !== box.host);
  if (targets.length === 0) return;
  // A live stereo pair's halves are not groupable (#792): the picker-side
  // exclusion lives in the multiroom grid, this is the same rule for the
  // player's group chips. Say it instead of failing later with a generic
  // error after the zone budget.
  const inPair = targets.filter(tgt => inAnyStereoPair(tgt, state.zoneLive));
  if (inPair.length) {
    fail(t('multiroom.pairNotGroupable'));
    if (inPair.length === targets.length) return;
  }
  const members = groupMembersOf(box, state.zoneLive, state.boxes);
  // Apply every toggle to one membership list, so the speakers join or leave
  // together instead of one zone form per click.
  const next = targets.reduce((acc, tgt) => acc.some(m => m.ip === tgt.host)
    ? acc.filter(m => m.ip !== tgt.host)
    : [...acc, { deviceID: tgt.deviceID, ip: tgt.host, box: tgt }], members);
  const removed = targets.filter(tgt => members.some(m => m.ip === tgt.host) && !next.some(m => m.ip === tgt.host));
  // The speakers that just JOINED, symmetric with `removed` and derived from
  // the same two lists so it stays right for a coalesced batch where some chips
  // add and some remove in one edit.
  const added = targets.filter(tgt => !members.some(m => m.ip === tgt.host));
  const target = targets[0];
  const wasIn = removed.length > 0 && targets.length === 1;
  try {
    if (next.length === 0) {
      // Same rule as the group frame's x: a refusal from the speaker must not
      // come out as a green tick, or the group the user is looking at keeps
      // playing while the app says it is gone.
      const res = await DissolveZone(box.host, box.port);
      if (res && res.ok === false) {
        say(dissolveIncompleteMessage(res), 'warn');
      } else {
        // Stop the ex-followers we can reach (their agent port is only known
        // for discovered boxes).
        await Promise.allSettled(members.filter(m => m.box).map(m => Stop(m.box.host, m.box.port)));
        say(t(res && res.nothing ? 'multiroom.nothingToUngroup' : 'group.dissolvedToast'), 'ok',
          { added: [], removed: targets.map((x) => x.host) });
      }
    } else {
      // Preserve the group's mode when the agent reports one (a mirror group
      // must not be silently converted to native by an add/remove); older
      // agents carry no mode field, then native is what today's zones are.
      const own = (state.zoneLive || {})[box.deviceID];
      const mode = (own && typeof own.mode === 'string' && own.mode) ? own.mode : 'native';
      // Wake every member before enrolling it (#70): a box a user switched off at
      // the speaker still answers STR (the agent stays up in standby), so the
      // firmware would add it to the zone but it stays silent. Waking an
      // already-awake box is a fast no-op, so this is safe to do for all members.
      //
      // All at once, then a single retry for the ones that did not take it.
      // One wake per speaker with no retry meant a speaker that was busy at
      // that moment was enrolled asleep and stayed silent, and doing them in
      // sequence would add every speaker's wake time to the wait before the
      // group even starts forming.
      // ... except a member the app itself lists as offline: it cannot be
      // woken, and with the port fallback a dead address costs up to two 6 s
      // transport timeouts PER ROUND, so one unplugged speaker held every
      // group edit for over 20 seconds before the zone even started forming
      // (field, 2026-08-29). FormZone's preflight still drops an unreachable
      // member cleanly (notReady), so skipping its wake loses nothing.
      const wakeTargets = next.filter(m => m.box && !m.box.offline);
      // Bounded per speaker: a box that neither answers nor refuses (agent
      // wedged, just unplugged, offline flag not set yet) must not hold the
      // whole batch for the full transport timeouts either. A wake slower
      // than this window counts as failed and gets the one retry below;
      // enrolment continues either way.
      const WAKE_WAIT_MS = 4000;
      const wakeOne = (m) => Promise.race([
        WakeBox(m.box.host, m.box.port),
        new Promise((_, reject) => setTimeout(() => reject(new Error('wake window elapsed')), WAKE_WAIT_MS)),
      ]);
      const wakeOnce = (list) => Promise.allSettled(list.map(wakeOne))
        .then(rs => list.filter((_, i) => rs[i].status === 'rejected'));
      const failed = await wakeOnce(wakeTargets);
      if (failed.length) {
        console.warn('group: retrying wake for', failed.map(m => m.ip));
        await wakeOnce(failed);
      }
      const res = await FormZone(box.host, box.port, {
        master: { deviceID: box.deviceID, ip: box.host },
        slaves: next.map(m => ({ deviceID: m.deviceID, ip: m.ip })),
        stereo: false, mode,
      });
      if (res && res.ok === false) {
        // HTTP 200 with ok:false means the firmware formed NOTHING (#70).
        // Treating it as success painted checked chips and a group volume
        // slider that controlled a phantom group.
        //
        // But ok:false is not always the firmware shrugging. FormZone also
        // REFUSES on purpose, with a reason, when a participant is half of a
        // live stereo pair (#792): that shape starves the audio, so the app
        // never even asks the box. The Multi-Room view has said so since then;
        // this one did not, and answered a deliberate refusal with "try Mirror
        // mode, hit Refresh, or update the speakers, then send logs". A user
        // with a stereo pair on his terrace followed exactly that advice and
        // sent the logs (mail 2026-09-25), which is a round trip he should
        // never have been sent on.
        const inPair = Array.isArray(res.inPair) && res.inPair.length;
        fail(inPair
          ? t('multiroom.pairNotGroupable')
          : ((res.error && String(res.error)) || t('multiroom.formedNone')));
        deps.refreshMusicZones(true);
        renderGroupControl();
        return;
      }
      // Every speaker taken out of the group stops, not only the first one.
      await Promise.allSettled(removed.map(tgt => Stop(tgt.host, tgt.port)));
      // Speakers that just joined start at the group's volume. The master's own
      // agent does this too and is the authority (it can tell a confirmed join
      // from a failed one, which the app cannot); this covers a group whose
      // master still runs an older agent, and seeds the panel so the new
      // member's slider shows the right number straight away instead of the
      // level it happened to wake up at.
      //
      // Never the speakers that were already in the group: the group slider is
      // relative on purpose, and one absolute number written across every
      // member destroys the per-speaker balance behind it (#401).
      if (added.length) {
        try {
          const ms = await BoxSettings(box.host, box.port);
          // Target over actual, matching the agent: a muted or mid-ramp master
          // reports actual 0 while target still carries the real level.
          const mv = (ms && ms.volume && (ms.volume.target || ms.volume.actual)) || 0;
          // A master that reports nothing usable must not silence the joiner:
          // a speaker that is provably in the group and provably silent is the
          // hardest thing there is to tell apart from one that never joined.
          if (mv > 0) {
            // Two different "did not join" answers, and both have to be
            // subtracted. notReady is the app's own pre-flight drop: the agent
            // never answered, so the speaker was not sent to the firmware at
            // all. missing is the master's verify-first verdict for a slave it
            // DID send but whose own zone read never named the master. The
            // agent skips exactly that set in its own applier, so writing it
            // here would hand the group's level to a speaker sitting in another
            // room in no group at all.
            //
            // missing carries deviceIDs, not addresses, and on a two-chip
            // chassis the firmware's id is not the one discovery knows, so it
            // is resolved through the members list the agent returned.
            const notReady = new Set((res && Array.isArray(res.notReady) ? res.notReady : [])
              .map(nr => (nr && nr.ip) || nr).filter(Boolean));
            const missingIDs = new Set((res && Array.isArray(res.missing) ? res.missing : [])
              .map(id => String(id).toLowerCase()));
            const missingIPs = new Set((res && Array.isArray(res.members) ? res.members : [])
              .filter(m => m && m.deviceID && missingIDs.has(String(m.deviceID).toLowerCase()))
              .map(m => m.ip).filter(Boolean));
            const joined = added.filter(tgt =>
              !notReady.has(tgt.host) &&
              !missingIPs.has(tgt.host) &&
              !missingIDs.has(String(tgt.deviceID || '').toLowerCase()));
            await Promise.allSettled(joined.map(tgt => SetBoxVolume(tgt.host, tgt.port, mv)));
            joined.forEach(tgt => { groupVol.members[tgt.host] = mv; });
          }
        } catch { /* the agent is the authority; this is only the seed */ }
      }
      const moved = { added: added.map((x) => x.host), removed: removed.map((x) => x.host) };
      if (targets.length === 1) {
        say(t(wasIn ? 'group.removedToast' : 'group.addedToast', { name: getBoxLabel(target) }), 'ok', moved);
      } else {
        say(t('group.batchToast', { count: targets.length }), 'ok', moved);
      }
    }
    // Optimistic zone update so the chips + frames + Multi-Room summary
    // reflect the change at once, in the same shape a real poll returns
    // (including the master's OWN entry); the confirming poll below
    // corrects it shortly after.
    state.zoneLive = applyOptimisticZone(state.zoneLive, box, next);
    notifyZoneLive();
    deps.renderBoxSelect();
  } catch (e) {
    fail(String(e));
  }
  // Confirming fetch: force past the 8s debounce, which used to swallow this
  // call entirely and left the optimistic state on screen until the next
  // unrelated discovery cycle. Slightly delayed because a follower's own
  // zone self-report can lag the form/removal by around a second.
  setTimeout(() => deps.refreshMusicZones(true), 1200);
  renderGroupControl();
}

// groupPanelState remembers what the panel currently shows and whether the user
// is touching it, so a repaint cannot happen underneath a finger.
//
// The panel used to be rebuilt from scratch on every call, and it is called
// from the zone poll AND from renderPresets, which fires on things as unrelated
// as a Spotify cover change. A rebuild replaces the slider element, reseeds it
// from the master's own volume slider, and re-anchors the balance from levels
// our in-flight writes have already moved. Dragging to 53 across five speakers
// arrived at 39. The phone got these guards on 2026-08-15; this is the same
// treatment for the desktop.
const groupPanelState = { signature: '', dragging: false, wroteAt: 0 };
const GROUP_PANEL_SETTLE_MS = 1500;

// groupPanelBusy reports whether a rebuild would land on top of the user. A
// drag in progress is obvious; the settle window covers the moment between our
// write and the poll that reflects it.
function groupPanelBusy() {
  return groupPanelState.dragging || (Date.now() - groupPanelState.wroteAt < GROUP_PANEL_SETTLE_MS);
}

// noteGroupPanelWrite marks the moment a volume left the app, so the next poll
// does not overrule it.
function noteGroupPanelWrite() { groupPanelState.wroteAt = Date.now(); }

// renderGroupControl paints the group chips + (when a group is active) the group
// volume slider into #groupControl, based on the selected box.
//
// In the UI 2.0 preview the player has a slot for the group volume
// (#ui2GroupVolSlot): the group slider goes there, next to the speaker volume,
// and the chips and per-speaker levels stay in #groupControl, which the shell
// shows as the Levels panel. Without the slot (the preview off) the markup is
// exactly what it always was.
export function renderGroupControl() {
  const cont = $('groupControl');
  if (!cont) return;
  const slot = $('ui2GroupVolSlot');
  const sel = state.currentBox;
  if (!sel || sel.kind === 'stock') { clearGroupControl(cont, slot); return; }
  // Manage the group through its master: when the selected speaker is a zone
  // follower, box is its master (so chips + volume act on the real group and the
  // member can be removed from any selection); otherwise box is the selection.
  const box = groupControlBox();
  const isMember = !!box && box.host !== sel.host;
  // Chips are every other STR speaker relative to the MASTER (not the selection),
  // so the selected follower itself appears as an in-group chip that can be removed.
  // A live stereo pair's two halves are excluded here: a pair cannot join a group
  // as a unit yet, so offering its halves only produced a "not groupable" error on
  // click and left them cluttering the +N row (reported on the music tab,
  // 2026-08-31). This is the same exclusion the Multi-room grid applies, via the
  // shared groupCandidates helper (the UI 2.0 sidebar reads it too); the
  // click-time guard in runGroupMemberToggle stays as a defensive fallback for a
  // pair that forms while the row is stale.
  const others = groupCandidates(box, state.zoneLive, state.boxes);
  if (others.length === 0) { clearGroupControl(cont, slot); return; }
  const slaves = currentGroupSlaves();
  // The tick must come from the SAME list the click acts on. It used to be
  // painted from each speaker's own zone self-report while toggleGroupMember
  // works from groupMembersOf, which is the union of that and the master's
  // member list. A follower the master knows about but which does not report
  // itself therefore drew a "+", and pressing it re-formed the group WITHOUT
  // that speaker and stopped its music: the button did the opposite of what it
  // showed. Three of five speakers were in exactly that state on 2026-08-15.
  const editable = groupMembersOf(box, state.zoneLive, state.boxes);
  const inGroup = new Set(editable.map(m => m.ip).filter(Boolean));
  const chips = others.map(b => {
    const on = inGroup.has(b.host);
    const label = getBoxLabel(b);
    return `<button class="group-chip${on ? ' in-group' : ''}" data-host="${b.host}" data-port="${b.port}" `
      + `title="${escapeAttr(t(on ? 'group.removeTitle' : 'group.addTitle', { name: label }))}">`
      + `<span class="group-chip-mark" aria-hidden="true">${on ? '&#10003;' : '&#43;'}</span>${escapeHtml(label)}</button>`;
  }).join('');
  // One slider per member below the group slider (#401): speakers of different
  // types need different levels to sound equally loud, and the group slider then
  // moves them together while keeping that balance.
  const members = slaves.length > 0 ? [box, ...slaves] : [];
  const memberRows = members.map(b => {
    const id = cssId(b.host);
    const v = typeof groupVol.members[b.host] === 'number' ? groupVol.members[b.host] : 0;
    return `<div class="group-member-vol">`
      + `<span class="group-member-name" title="${escapeAttr(getBoxLabel(b))}">${escapeHtml(getBoxLabel(b))}</span>`
      + `<input type="range" id="memberVol-${id}" data-host="${b.host}" data-port="${b.port}" min="0" max="100" step="1" value="${v}" aria-label="${escapeAttr(t('group.memberVolumeLabel', { name: getBoxLabel(b) }))}" />`
      + `<span class="vol-val" id="memberVolVal-${id}">${v}</span></div>`;
  }).join('');
  const groupVolRow = slaves.length > 0
    ? `<div class="group-vol-row"><span class="vol-icon" aria-hidden="true">&#128266;</span>`
      + `<input type="range" id="groupVolume" min="0" max="100" step="1" aria-label="${escapeAttr(t('group.volumeLabel'))}" />`
      + `<span class="muted small">${escapeHtml(t('group.volumeLabel'))}</span></div>`
    : '';
  const memberVols = slaves.length > 0 ? `<div class="group-member-vols">${memberRows}</div>` : '';
  const volRow = groupVolRow + memberVols;
  // When a member is selected, name the group it belongs to so it is clear the
  // panel below now manages that group (not the empty state it used to show).
  const memberNote = isMember
    ? `<div class="group-member-note muted small">${escapeHtml(t('group.memberOf', { name: getBoxLabel(box) }))}</div>`
    : '';
  // Rebuild only when the panel would actually look different, and never while
  // the user is touching it. Everything below re-binds handlers to elements the
  // rebuild creates, so skipping here keeps the live ones and their state.
  const signature = JSON.stringify([box.host, isMember, others.map(b => b.host), slaves.map(b => b.host)]);
  // The panel is whole when its chips are drawn and, with the player's slot,
  // the group slider sits in the slot whenever there is a group.
  const drawn = !!cont.querySelector('.group-chips')
    && (!slot || slaves.length === 0 || !!slot.querySelector('#groupVolume'));
  syncShellGroupPanel(slaves.length > 0);
  if (signature === groupPanelState.signature && drawn) {
    // Same speakers as last time: only write the values into the rows that
    // already exist, and not even that while a finger is on a slider.
    if (!groupPanelBusy() && members.length) loadGroupMemberVolumes(members);
    return;
  }
  if (groupPanelBusy() && drawn) return;
  groupPanelState.signature = signature;
  if (slot) {
    slot.innerHTML = groupVolRow;
    cont.innerHTML = `<span class="group-label muted small">${escapeHtml(t('group.label'))}</span>`
      + memberNote
      + `<div class="group-chips">${chips}</div>${memberVols}`;
  } else {
    cont.innerHTML = `<span class="group-label muted small">${escapeHtml(t('group.label'))}</span>`
      + memberNote
      + `<div class="group-chips">${chips}</div>${volRow}`;
  }
  cont.querySelectorAll('.group-chip').forEach(chip => {
    chip.onclick = () => toggleGroupMember(chip.dataset.host, parseInt(chip.dataset.port, 10));
  });
  const vol = $('groupVolume');
  const seed = $('musicVolume');
  if (vol && seed && seed.value && !groupPanelBusy()) vol.value = seed.value; // start at the master's level
  if (vol) {
    vol.onpointerdown = () => {
      groupPanelState.dragging = true;
      captureGroupBaseline(parseInt(vol.value, 10));
    };
    const endDrag = () => { groupPanelState.dragging = false; noteGroupPanelWrite(); };
    vol.onpointerup = endDrag;
    vol.onpointercancel = endDrag;
    vol.onblur = endDrag;
    vol.oninput = () => { noteGroupPanelWrite(); setGroupVolume(parseInt(vol.value, 10)); };
  }
  cont.querySelectorAll('.group-member-vol input[type=range]').forEach(el => {
    el.onpointerdown = () => { groupPanelState.dragging = true; };
    const endMemberDrag = () => { groupPanelState.dragging = false; noteGroupPanelWrite(); };
    el.onpointerup = endMemberDrag;
    el.onpointercancel = endMemberDrag;
    el.onblur = endMemberDrag;
    el.oninput = () => {
      noteGroupPanelWrite();
      const lbl = document.getElementById('memberVolVal-' + cssId(el.dataset.host));
      if (lbl) lbl.textContent = el.value;
      setMemberVolume(el.dataset.host, parseInt(el.dataset.port, 10), parseInt(el.value, 10));
    };
  });
  if (members.length) loadGroupMemberVolumes(members);
}

// clearGroupControl empties the panel, and the player's group volume slot
// with it, when there is no group to manage.
function clearGroupControl(cont, slot) {
  cont.innerHTML = '';
  if (slot) slot.innerHTML = '';
  syncShellGroupPanel(false);
}

// purgeGroupStateForSpeaker drops the pending group edits of a speaker STR was
// removed from (the onSpeakerPurge hook in main.js).
export function purgeGroupStateForSpeaker(host) {
  groupOpPending.delete(host);
  pendingGroupEdits.delete(host);
  // The waiting batch was clicked for this speaker's group: with the master
  // gone there is no group left to edit, so the batch goes quietly instead of
  // ending in the "group changed" toast.
  if (host && pendingGroupMaster === host) {
    if (groupEditTimer) clearTimeout(groupEditTimer);
    groupEditTimer = null;
    pendingGroupEdits.clear();
    pendingGroupMaster = '';
    markGroupChipPending();
  }
}
