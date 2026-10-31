// shell2/speakers/groupsmodel.js: the pure part of Speakers > Groups in the
// UI 2.0 preview (groups.js paints and wires; this decides). No DOM, no i18n,
// no binding: vitest covers it in groupsmodel.test.js.
//
// Three questions live here:
//
//   which action a click on the classic Multi-room markup is (actionOf), so
//   the preview can ask before a destructive one and remember what it sent;
//
//   what the speakers have to report once that action took effect
//   (expectationFor) and whether the shared zone poll says so yet
//   (expectationMet, pendingPhase). "Done" is said only when the speakers
//   confirmed: the classic notes trust the agent's answer, the preview also
//   waits for the speakers' own zone reports, the poll the classic view runs
//   every 5 s anyway;
//
//   how to find a control again after the classic view rebuilt its markup
//   (focusKeyOf, selectorForKey), so the keyboard focus and a confirmed click
//   survive the live repaints.

import { masterOf, stereoPairsOf, stereoPairKey, storedPermanentGroupsOf } from '../../groups.js';

const up = (s) => String(s || '').toUpperCase();

// How long the preview waits for the speakers' reports before it says they
// did not confirm (four rounds of the 5 s poll), and how long a confirmed
// "done" stays up.
export const CONFIRM_LATE_MS = 20000;
export const DONE_SHOWN_MS = 10000;

// The classic controls that change a pair or a group, by what they do.
// confirm: destructive, the preview asks first (naming the pair or group).
// section: where its outcome is shown (now | pairs | fixed | keys).
export const ACTIONS = Object.freeze({
  'pair-create': { confirm: false, section: 'pairs' },
  'pair-rename': { confirm: false, section: 'pairs' },
  'pair-undo': { confirm: true, section: 'pairs' },
  'pair-undo-sel': { confirm: true, section: 'pairs' },
  'stale-fix': { confirm: true, section: 'pairs' },
  'group-create': { confirm: false, section: 'fixed' },
  'group-dissolve': { confirm: true, section: 'now' },
  'group-ungroup': { confirm: true, section: 'now' },
  'group-forget': { confirm: true, section: 'fixed' },
  'member-remove': { confirm: true, section: 'fixed' },
  'gk-remove': { confirm: true, section: 'keys' },
});

const attr = (el, name) => (el && typeof el.getAttribute === 'function' ? (el.getAttribute(name) || '') : '');
const hasClass = (el, c) => !!(el && el.classList && el.classList.contains(c));

// actionOf names the action of a clicked classic control, or null for
// anything else. el is the control itself (the click's closest match).
export function actionOf(el) {
  if (!el) return null;
  const id = el.id || '';
  if (id === 'stereoCreate') return { kind: 'pair-create' };
  if (id === 'stereoNameSave') return { kind: 'pair-rename' };
  if (id === 'stereoDissolve') return { kind: 'pair-undo-sel' };
  if (id === 'zoneCreate') return { kind: 'group-create' };
  if (id === 'zoneUngroup') return { kind: 'group-ungroup' };
  if (hasClass(el, 'box-group-x')) {
    const k = attr(el, 'data-dissolve-kind');
    const key = up(attr(el, 'data-dissolve'));
    if (k === 'pair') return { kind: 'pair-undo', key };
    if (k === 'stored') return { kind: 'group-forget', key };
    return { kind: 'group-dissolve', key };
  }
  if (hasClass(el, 'zone-chip-x')) return { kind: 'member-remove', key: up(attr(el, 'data-gkill')), ip: attr(el, 'data-gkill-ip') };
  if (hasClass(el, 'stale-stereo-fix')) return { kind: 'stale-fix', index: Number(attr(el, 'data-stale')) };
  if (hasClass(el, 'gk-remove')) return { kind: 'gk-remove', template: attr(el, 'data-gk') };
  return null;
}

// ACTION_SELECTOR finds the controls actionOf knows from a click target.
export const ACTION_SELECTOR = '#stereoCreate, #stereoNameSave, #stereoDissolve, #zoneCreate, #zoneUngroup, '
  + '.box-group-x, .zone-chip-x, .stale-stereo-fix, .gk-remove';

const cssValue = (v) => String(v).replace(/["\\]/g, '\\$&');

// focusKeyOf is a selector that finds the same control in a rebuilt markup,
// or '' for an element that cannot be told apart from its siblings.
export function focusKeyOf(el) {
  if (!el) return '';
  if (el.id) return `#${el.id}`;
  const pairs = [
    ['box-group-x', ['data-dissolve', 'data-dissolve-kind']],
    ['zone-chip-x', ['data-gkill', 'data-gkill-ip']],
    ['zone-makemain', ['data-id']],
    ['zone-update-badge', ['data-update-id']],
    ['zone-card', ['data-id']],
    ['gk-key', ['data-gk']],
    ['gk-remove', ['data-gk']],
    ['stale-stereo-fix', ['data-stale']],
    ['ui2-grp-act', ['data-ui2-act', 'data-key']],
  ];
  for (const [cls, names] of pairs) {
    if (!hasClass(el, cls)) continue;
    return `.${cls}` + names.map((n) => `[${n}="${cssValue(attr(el, n))}"]`).join('');
  }
  return '';
}

// selectorForKey is the selector of a focus key (the same string).
export const selectorForKey = (key) => key || '';

// ---- what the speakers report ------------------------------------------

const boxById = (boxes, id) => (boxes || []).find((b) => b && b.deviceID && up(b.deviceID) === up(id)) || null;

// pairFormed: a live pair holds exactly these two speakers.
// pairGone: no live pair holds them any more (and the holder of a one-sided
//   leftover reports no pair document).
// pairName: the speakers carry this name in their pair document.
// groupLive: every member names the main speaker as its master.
// groupFormed: groupLive, or for a fixed group the main speaker stores it.
// groupGone: no speaker names this main speaker as its master any more.
// storedGone: the main speaker keeps no fixed group any more.
// memberOut: the member is neither stored nor live in the main's group.
export function expectationMet(exp, zoneLive, boxes) {
  if (!exp) return true;
  const zl = zoneLive || {};
  const pairs = stereoPairsOf(zl);
  switch (exp.type) {
    case 'pairFormed':
      return pairs.some((p) => stereoPairKey(p) === exp.key);
    case 'pairGone': {
      if (exp.key && pairs.some((p) => stereoPairKey(p) === exp.key)) return false;
      if (exp.holder) {
        const e = zl[exp.holder];
        const st = e && e.stereo;
        if (st && ((st.members || []).length || st.id)) return false;
      }
      return true;
    }
    case 'pairName': {
      const p = pairs.find((x) => stereoPairKey(x) === exp.key);
      return !!p && String(p.name || '').trim() === String(exp.name || '').trim();
    }
    case 'groupLive':
    case 'groupFormed': {
      // The key the speakers report the group under: the main speaker's own
      // answer first (on a two-chip chassis it is not the app's deviceID).
      const key = masterOf(exp.master, zl) || up(exp.master);
      const live = (exp.members || []).length > 0
        && exp.members.every((id) => masterOf(id, zl) === key);
      if (live || exp.type === 'groupLive' || !exp.permanent) return live;
      const g = storedPermanentGroupsOf(zl, boxes).find((x) => x.masterKey === up(exp.master));
      if (!g) return false;
      return exp.members.every((id) => {
        const b = boxById(boxes, id);
        return g.members.some((m) => (m.box && up(m.box.deviceID) === up(id)) || (b && m.ip && m.ip === b.host));
      });
    }
    case 'groupGone':
      return !(boxes || []).some((b) => b && b.deviceID && masterOf(b.deviceID, zl) === up(exp.master));
    case 'storedGone': {
      const e = zl[exp.masterID];
      return !e || !e.permanent || !(e.remembered || []).length;
    }
    case 'memberOut': {
      const e = zl[exp.masterID] || {};
      const inList = (list) => (list || []).some((m) => m && ((exp.ip && m.ip === exp.ip) || (exp.memberID && up(m.deviceID) === up(exp.memberID))));
      return !inList(e.remembered) && !inList(e.members);
    }
    default:
      return true;
  }
}

// pendingPhase is where a sent action stands:
//   failed   the classic view reported a refusal or an error (it shows it)
//   done     the speakers report the change; shown for DONE_SHOWN_MS
//   expired  done long enough ago, or late for a minute: drop it
//   late     not confirmed after CONFIRM_LATE_MS
//   waiting  sent, the speakers have not reported it yet
export function pendingPhase(p, zoneLive, boxes, now = Date.now()) {
  if (!p) return 'expired';
  if (p.failed) return now - p.at > 60000 ? 'expired' : 'failed';
  if (p.doneAt) return now - p.doneAt > DONE_SHOWN_MS ? 'expired' : 'done';
  if (expectationMet(p.expect, zoneLive, boxes)) return 'done';
  if (now - p.at > CONFIRM_LATE_MS + 60000) return 'expired';
  return now - p.at > CONFIRM_LATE_MS ? 'late' : 'waiting';
}

// noteVerdict reads the classic result note an action wrote: 'error' for a
// refusal or a warning the user has to act on, 'ok' for a confirmation, ''
// while nothing (or only the loading line) is there.
export function noteVerdict(html) {
  const s = String(html || '');
  if (/\bsetup-(err|warn)\b/.test(s)) return 'error';
  if (/\bsetup-ok\b/.test(s)) return 'ok';
  return '';
}

// pairKeyOf is the store key of two picked speakers (stereoPairKey's shape).
export function pairKeyOf(leftID, rightID) {
  return stereoPairKey({ members: [{ deviceID: leftID }, { deviceID: rightID }] });
}

// pairUndoSplit sorts the halves of a pair (pairMemberBoxes) into the ones an
// undo reaches and the ones it does not: a half the app lists as offline, or
// one it does not know at all, is not asked (multiroom.js
// doDissolveStereoPair) and keeps its side of the pair. Names via nameOf.
export function pairUndoSplit(halves, nameOf = (b) => (b && (b.name || b.host)) || '') {
  const reachable = [];
  const offline = [];
  for (const h of halves || []) {
    if (!h) continue;
    const m = h.member || {};
    if (h.box && !h.box.offline) reachable.push(nameOf(h.box));
    else offline.push(h.box ? nameOf(h.box) : (m.ip || m.deviceID || ''));
  }
  return { reachable: reachable.filter(Boolean), offline: offline.filter(Boolean) };
}

// liveLeaderOf is the speaker that leads a group right now, the one the
// classic Ungroup takes apart (the hand-picked main first when it leads).
export function liveLeaderOf(boxes, zoneLive, pickedID = '') {
  const list = (boxes || []).filter((b) => b && b.kind !== 'stock' && b.deviceID);
  const leads = (b) => masterOf(b.deviceID, zoneLive) === up(b.deviceID);
  const picked = list.find((b) => b.deviceID === pickedID);
  if (picked && leads(picked)) return picked;
  return list.find(leads) || null;
}
