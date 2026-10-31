// shell2/speakers/groups.js: Speakers > Groups in the UI 2.0 preview, where
// stereo pairs and fixed (permanent) groups are SET UP (phase 3, #1287).
//
// Not here, on purpose: who plays along RIGHT NOW. That stays in the sidebar
// speaker list ("+" on an idle speaker, "-" on a member; phase 1), and the
// page says so in its first line. There is no output popover and no room
// switcher (docs/UI-2.0.md, "What we change").
//
// The classic Multi-room view (views/multiroom.js) stays the one source of
// every control and every handler: it renders and wires its markup as always,
// with its live poll, its notes and its checks. This module only ARRANGES
// that markup after each paint (multiroom.js setMultiroomRenderHooks), so the
// 5 s live repaints keep the arrangement, and nothing here sends a group or
// pair request itself:
//
//   Playing together now   the line about the sidebar, the group that plays
//                          (#zoneCurrent), a card per live group that is not
//                          a fixed one (its dissolve), Ungroup (#zoneUngroup)
//   Stereo pairs           a card per pair: its name, left and right, Change
//                          (loads it into the editor) and Undo (the frame x);
//                          the editor card: #stereoLeft, #stereoRight,
//                          #stereoName, #stereoNameSave, #stereoCreate,
//                          #stereoDissolve, #pairBalance, the L/R cards, the
//                          notes, #stereoResult
//   Fixed groups           a card per fixed group, live or stored: the main
//                          speaker, the members with "take out" (.zone-chip-x),
//                          dissolve or delete (the frame x); the editor card:
//                          the zone cards (main, members, update first),
//                          #zonePermanent, #zoneCreate, #zoneResult
//   Group keys             #gkRemote, the template rows (.gk-key, .gk-remove),
//                          #gkName, #gkSave, #gkKeyMap, #gkResult
//   top of the page        the stale-pair notices (.stale-stereo-fix), the
//                          "needs two speakers" and "update first" notes,
//                          #zoneRefresh
//
// On top of the classic behaviour, gated to the preview:
//   - the speaker is named: the create buttons say which speakers they join,
//     every card names its speakers, every confirmation names the pair or
//     group;
//   - a destructive step (undo a pair, dissolve, delete a fixed group, take a
//     member out, remove a group key) asks first, in a dialog that names it;
//   - done only when the speakers confirmed: after an action the page says
//     "sent, waiting for ..." until the speakers' own zone reports (the poll
//     the classic view runs anyway) show the change, and says so when they do
//     not (groupsmodel.js);
//   - errors where they happen: the classic result note moves into the card
//     of the pair or group the action was about.
//
// Nothing runs without the shell (mountGroups is called by the router only),
// and nothing touches the DOM at import: main.js imports this before the
// views exist.

import { state } from '../../state.js';
import { t } from '../../i18n/index.js';
import { escapeAttr, escapeHtml, getBoxLabel } from '../../utils.js';
import { groupMembersOf, masterBoxForKey, pairMemberBoxes, stereoPairKey, stereoPairsOf, storedPermanentGroupsOf } from '../../groups.js';
import { pairDisplayName } from '../../stereoNames.js';
import { staleNoticeText } from '../../stalestereo.js';
import { renderMultiroom, setMultiroomRenderHooks } from '../../views/multiroom.js';
import { ACTIONS, ACTION_SELECTOR, actionOf, expectationMet, focusKeyOf, liveLeaderOf, noteVerdict, pairKeyOf, pairUndoSplit, pendingPhase } from './groupsmodel.js';
import './groups.css';

let wiredRoot = null;
let labelFn = null;
// The action sent last and what the speakers have to report for it.
let pending = null;
// The section of the last group action, where its classic note is shown.
let lastZoneSec = 'fixed';
// Set while a confirmed click is replayed, so the capture handler lets it pass.
let bypass = false;
// A control to focus after the next paint (Change on a pair card).
let wantFocus = '';
// The preview opens the group editor on "keep permanently": the page is where
// fixed groups are made. Once per session; the box can still be unticked.
let permanentDefaulted = false;
let dialog = null;
let liveRegion = null;
let lastSaid = '';

const up = (s) => String(s || '').toUpperCase();
const nameOf = (b) => (b ? ((labelFn && labelFn(b)) || getBoxLabel(b)) : '');
const joinNames = (list) => (list || []).filter(Boolean).join(', ');
const strBoxes = () => (state.boxes || []).filter((b) => b && b.kind !== 'stock' && b.host && b.deviceID);
const isOn = (root) => !!(state.ui2Shell && root && root.dataset.ui2Route === 'groups');

// mountGroups is the groups route's hook (index.js), called on every entry
// to the route, after switchView('multiroom') already painted the classic
// view, and on every sidebar repaint while the route is open.
//   ctx  { root: #view-multiroom, open(route, opts), label(box) }
export function mountGroups(ctx = {}) {
  const root = ctx.root;
  if (!root) return;
  root.dataset.ui2Route = 'groups';
  ensureLiveRegion();
  if (typeof ctx.label === 'function') labelFn = ctx.label;
  if (wiredRoot !== root) {
    wire(root);
    wiredRoot = root;
  }
  setMultiroomRenderHooks({ before: beforePaint, after: afterPaint });
  if (!permanentDefaulted) {
    permanentDefaulted = true;
    if (!state.zonePermanent) {
      state.zonePermanent = true;
      renderMultiroom(false);
      return;
    }
  }
  afterPaint(root, '');
}

// ---- the paint hooks ------------------------------------------------------

function beforePaint(root) {
  if (!isOn(root)) return '';
  const a = document.activeElement;
  return a && a !== root && root.contains(a) ? focusKeyOf(a) : '';
}

function afterPaint(root, focusKey) {
  if (!isOn(root)) return;
  if (root.querySelector(':scope > .ui2-grp')) return;
  const lastSec = pending ? pending.section : lastZoneSec;
  try {
    arrange(root);
  } catch (e) {
    // Never leave the view half-moved: fall back to the classic markup.
    try { console.warn('UI 2.0 preview: Groups not arranged', e); } catch (_) { /* no console */ }
    setMultiroomRenderHooks(null);
    renderMultiroom(false);
    return;
  }
  const key = wantFocus || focusKey;
  wantFocus = '';
  if (key) {
    const el = root.querySelector(key);
    if (el && !el.disabled) el.focus({ preventScroll: !wantFocusScroll(key) });
    else {
      // The focused control is gone with its card: the heading of the
      // section the last action was in, never <body>.
      const h = root.querySelector(`#ui2GrpH-${lastSec}`);
      if (h) h.focus({ preventScroll: true });
    }
  }
}

const wantFocusScroll = (key) => key === '#stereoName';

// ---- arranging the classic markup ----------------------------------------

const SECTIONS = [
  { sec: 'now', title: 'ui2.speakers.groups.nowTitle' },
  { sec: 'pairs', title: 'ui2.speakers.groups.pairsTitle' },
  { sec: 'fixed', title: 'ui2.speakers.groups.fixedTitle' },
  { sec: 'keys', title: 'ui2.speakers.groups.keysTitle' },
];

function pageHTML() {
  const head = (s) => `<div class="ui2-grp-sechead"><h3 class="ui2-grp-h" id="ui2GrpH-${s.sec}" tabindex="-1">${escapeHtml(t(s.title))}</h3></div>`;
  const status = (sec) => `<p class="ui2-grp-status" id="ui2GrpStatus-${sec}" hidden></p>`;
  const sec = (s, body) => `<section class="ui2-grp-sec" data-sec="${s.sec}" aria-labelledby="ui2GrpH-${s.sec}">${head(s)}${body}</section>`;
  const [now, pairs, fixed, keys] = SECTIONS;
  return '<div class="ui2-grp">'
    + '<div class="ui2-grp-top" data-slot="top"><div class="ui2-grp-tools" data-slot="tools"></div></div>'
    + sec(now, `<p class="ui2-grp-lead">${escapeHtml(t('ui2.speakers.groups.lead'))}</p>${status('now')}`
      + '<div data-slot="current"></div><div class="ui2-grp-cards" data-slot="cards-now"></div>'
      + '<div class="ui2-grp-actions" data-slot="actions-now"></div><div class="ui2-grp-result" data-slot="result-now"></div>')
    + sec(pairs, `${status('pairs')}<div class="ui2-grp-cards" data-slot="cards-pairs"></div>`
      + '<div class="u-card ui2-grp-editor" id="ui2GrpPairEditor" role="group" aria-labelledby="ui2GrpPairEditorTitle">'
      + '<h4 class="ui2-grp-card-title" id="ui2GrpPairEditorTitle"></h4>'
      + '<div data-slot="editor-pairs"></div><div class="ui2-grp-result" data-slot="result-pairs"></div></div>')
    + sec(fixed, `${status('fixed')}<div class="ui2-grp-cards" data-slot="cards-fixed"></div>`
      + '<div class="u-card ui2-grp-editor" id="ui2GrpFixedEditor" role="group" aria-labelledby="ui2GrpFixedEditorTitle">'
      + '<h4 class="ui2-grp-card-title" id="ui2GrpFixedEditorTitle"></h4>'
      + '<div data-slot="editor-fixed"></div><div class="ui2-grp-result" data-slot="result-fixed"></div></div>')
    + sec(keys, '<div data-slot="editor-keys"></div>')
    + '</div>';
}

function pillHTML(kind, key) {
  return `<span class="u-pill u-pill--${kind}"><span class="u-pill-dot" aria-hidden="true"></span>${escapeHtml(t(key))}</span>`;
}

function cardHTML({ kind, key, pairKey, title, pills, members, note }) {
  const id = `ui2GrpCard-${kind}-${key}`;
  return `<article class="u-card ui2-grp-card" data-kind="${kind}" data-key="${escapeAttr(key)}"`
    + `${pairKey ? ` data-pair-key="${escapeAttr(pairKey)}"` : ''} aria-labelledby="${escapeAttr(id)}">`
    + `<div class="ui2-grp-card-head"><h4 class="ui2-grp-card-title" id="${escapeAttr(id)}">${escapeHtml(title)}</h4>`
    + `<span class="ui2-grp-pills">${pills}</span></div>`
    + `<ul class="ui2-grp-members">${members}</ul>`
    + (note ? `<p class="ui2-grp-note">${escapeHtml(note)}</p>` : '')
    + '<div class="ui2-grp-card-actions" data-slot="card-actions"></div>'
    + '<div class="ui2-grp-result" data-slot="card-result"></div></article>';
}

const btn = (el, variant) => {
  if (el) el.classList.add('u-btn', `u-btn--${variant}`);
  return el;
};

function arrange(root) {
  const kids = [...root.children];
  const first = (cls) => kids.find((el) => el.classList.contains(cls)) || null;
  const intro = first('zone-intro');
  const stale = kids.filter((el) => el.classList.contains('stale-stereo'));
  const notes = kids.filter((el) => el.classList.contains('setup-warn') && !el.classList.contains('stale-stereo'));
  const frames = first('zone-frames');
  const topbar = first('zone-topbar');
  const hint = first('zone-pick-hint');
  const zoneCards = first('zone-cards');
  const balance = root.querySelector('#pairBalance');
  const controls = kids.filter((el) => el.classList.contains('zone-controls'));
  const zoneCtl = controls.find((c) => c.querySelector('#zoneCreate')) || null;
  const stereoCtl = controls.find((c) => c.querySelector('#stereoLeft')) || null;
  const keysCtl = controls.find((c) => c.querySelector('#gkRemote')) || null;

  const page = document.createElement('div');
  page.innerHTML = pageHTML();
  const grp = page.firstElementChild;
  // In the view from the start, so every lookup below finds a control
  // whether it has been moved into the page yet or not.
  root.insertBefore(grp, root.firstChild);
  const slot = (name, scope = grp) => scope.querySelector(`[data-slot="${name}"]`);
  const put = (name, ...nodes) => { const s = slot(name); for (const n of nodes) if (n && s) s.appendChild(n); };

  // Top: what explains every other symptom first, then the notes, Refresh.
  const top = slot('top');
  for (const n of [...stale, ...notes]) top.insertBefore(n, slot('tools'));
  stale.forEach((n) => btn(n.querySelector('.stale-stereo-fix'), 'secondary'));
  put('tools', btn(root.querySelector('#zoneRefresh'), 'secondary'));

  // The frames above the classic grid become one card each, in the section
  // they belong to.
  const zl = state.zoneLive || {};
  const boxes = strBoxes();
  const pairs = stereoPairsOf(zl);
  const stored = storedPermanentGroupsOf(zl, boxes);
  const counts = { now: 0, pairs: 0, fixed: 0 };
  for (const fr of frames ? [...frames.querySelectorAll(':scope > .box-group')] : []) {
    const x = fr.querySelector('.box-group-x');
    if (!x) continue;
    const kind = x.dataset.dissolveKind;
    const mk = up(x.dataset.dissolve);
    let sec;
    let html;
    // The frame x in words, naming what it takes apart: several cards must
    // never give several identical buttons.
    let xText = '';
    if (kind === 'pair') {
      const pair = pairs.find((p) => up(p.master) === mk || (p.members || []).some((m) => up(m && m.deviceID) === mk)) || null;
      const name = pairTitle(pair);
      const lr = pairMemberBoxes(pair, boxes);
      const who = (i) => (lr[i] ? (lr[i].box ? nameOf(lr[i].box) : (lr[i].member.ip || lr[i].member.deviceID)) : '');
      const members = `<li>${escapeHtml(t('ui2.speakers.groups.leftOf', { name: who(0) }))}</li>`
        + `<li>${escapeHtml(t('ui2.speakers.groups.rightOf', { name: who(1) }))}</li>`;
      sec = 'pairs';
      xText = t('ui2.speakers.groups.confirmPairBtn', { name });
      html = cardHTML({ kind: 'pair', key: mk, pairKey: stereoPairKey(pair), title: name, pills: pillHTML('accent', 'ui2.speakers.groups.pillPair'), members });
    } else {
      const perm = kind === 'stored' || !!fr.querySelector('.box-group-perm');
      const g = kind === 'stored' ? stored.find((s) => s.masterKey === mk) : null;
      const mb = g ? g.masterBox : masterBoxForKey(mk, zl, boxes);
      const name = mb ? nameOf(mb) : t('multiroom.groupLabelPrefix');
      const pills = (perm ? pillHTML('accent', 'ui2.speakers.groups.pillFixed') : '')
        + (kind === 'stored' ? pillHTML('muted', 'ui2.speakers.groups.pillSaved') : pillHTML('ok', 'ui2.speakers.groups.pillNow'));
      const note = fr.querySelector('.zone-frame-note');
      sec = perm ? 'fixed' : 'now';
      xText = t(kind === 'stored' ? 'ui2.speakers.groups.confirmForgetBtn' : 'ui2.speakers.groups.confirmGroupBtn', { name });
      html = cardHTML({ kind: kind === 'stored' ? 'stored' : 'zone', key: mk, title: name, pills, members: '', note: note ? note.textContent : '' });
    }
    const holder = document.createElement('div');
    holder.innerHTML = html;
    const card = holder.firstElementChild;
    if (kind !== 'pair') {
      const list = card.querySelector('.ui2-grp-members');
      for (const chip of fr.querySelectorAll('.zone-frame-chip')) {
        const li = document.createElement('li');
        li.appendChild(chip);
        const cx = chip.querySelector('.zone-chip-x');
        if (cx) {
          // The member's own "take out", in words: the x was a 15 px glyph.
          btn(cx, 'secondary').classList.add('ui2-grp-x');
          cx.textContent = cx.getAttribute('aria-label') || '';
          li.appendChild(cx);
        }
        list.appendChild(li);
      }
    }
    const acts = slot('card-actions', card);
    if (kind === 'pair') {
      const edit = document.createElement('button');
      edit.type = 'button';
      edit.className = 'u-btn u-btn--secondary ui2-grp-act';
      edit.dataset.ui2Act = 'edit-pair';
      edit.dataset.key = card.dataset.pairKey || '';
      edit.textContent = t('ui2.speakers.groups.change', { name: card.querySelector('.ui2-grp-card-title').textContent });
      acts.appendChild(edit);
    }
    btn(x, 'secondary').classList.add('ui2-grp-x');
    x.textContent = xText || x.getAttribute('aria-label') || '';
    if (xText) x.setAttribute('aria-label', xText);
    x.removeAttribute('title');
    acts.appendChild(x);
    slot(`cards-${sec}`).appendChild(card);
    counts[sec]++;
  }
  for (const sec of ['pairs', 'fixed']) {
    if (counts[sec]) continue;
    const p = document.createElement('p');
    p.className = 'ui2-grp-empty';
    p.textContent = t(sec === 'pairs' ? 'ui2.speakers.groups.noPairs' : 'ui2.speakers.groups.noFixed');
    slot(`cards-${sec}`).appendChild(p);
  }

  // Playing together now: what plays, and Ungroup.
  put('current', root.querySelector('#zoneCurrent'));
  put('actions-now', btn(root.querySelector('#zoneUngroup'), 'secondary'));
  // Ungroup only while a group plays: without one the classic button just
  // answers "nothing to ungroup". It comes back with the next live group.
  const ungroup = root.querySelector('#zoneUngroup');
  if (ungroup) ungroup.classList.toggle('ui2-grp-off', !grp.querySelector('.ui2-grp-card[data-kind="zone"]'));

  // The stereo pair editor.
  if (stereoCtl) {
    const head = stereoCtl.querySelector(':scope > b');
    if (head) head.hidden = true;
    put('editor-pairs', balance, stereoCtl);
    btn(root.querySelector('#stereoCreate'), 'primary');
    btn(root.querySelector('#stereoNameSave'), 'secondary');
    btn(root.querySelector('#stereoDissolve'), 'secondary');
  }
  labelPairEditor(grp, root);

  // The group editor: main speaker and members, keep permanently, create.
  put('editor-fixed', hint, zoneCards, zoneCtl);
  if (zoneCards) markZoneCards(zoneCards);
  btn(root.querySelector('#zoneCreate'), 'primary');
  labelGroupEditor(grp, root);

  // Group keys: the classic heading's STR badge moves into the section head.
  if (keysCtl) {
    const head = keysCtl.querySelector(':scope > b');
    const badge = head && head.querySelector('.str-badge');
    if (badge) grp.querySelector('#ui2GrpH-keys').appendChild(badge);
    if (head) head.hidden = true;
    put('editor-keys', keysCtl);
    btn(root.querySelector('#gkSave'), 'secondary');
    btn(root.querySelector('#gkKeyMap'), 'secondary');
    keysCtl.querySelectorAll('.gk-remove').forEach((b) => btn(b, 'secondary'));
  }

  placeResults(grp, root);
  paintStatus(grp);
  needsTwoSpeakers(grp);

  // What is left of the classic markup is its intro and empty wrappers.
  for (const n of [intro, frames, topbar]) if (n && n.parentNode === root) n.remove();
}

// needsTwoSpeakers (phase 5): with fewer than two STR speakers there is
// nothing to pair or group, so the two editors make way for a note that says
// so, with Add speaker. The editors stay in the page, hidden, so the classic
// handlers keep every node; the next paint with two speakers shows them again.
function needsTwoSpeakers(grp) {
  const few = strBoxes().length < 2;
  for (const id of ['ui2GrpPairEditor', 'ui2GrpFixedEditor']) {
    const ed = grp.querySelector(`#${id}`);
    if (ed) ed.hidden = few;
  }
  if (!few) return;
  ['pairs', 'fixed'].forEach((sec, i) => {
    const s = grp.querySelector(`.ui2-grp-sec[data-sec="${sec}"]`);
    if (!s || s.querySelector('.ui2-grp-two')) return;
    const note = document.createElement('div');
    note.className = 'u-card u-card--empty ui2-grp-two';
    note.dataset.sec = sec;
    // One Add speaker on the page: the second note only says it.
    note.innerHTML = `<p class="ui2-grp-two-text">${escapeHtml(t('ui2.state.groupsNeedTwo'))}</p>`
      + (i === 0
        ? `<div class="u-state-acts"><button type="button" class="u-btn u-btn--primary u-state-act" data-state-act="add">${escapeHtml(t('ui2.speakers.add'))}</button></div>`
        : '');
    const editor = s.querySelector('.ui2-grp-editor');
    if (editor) s.insertBefore(note, editor);
    else s.appendChild(note);
  });
}

// The zone cards: a real toggle button per selectable card, so the card is
// no longer a button holding buttons (Set as main, Update first).
function markZoneCards(cards) {
  cards.querySelectorAll('.zone-card').forEach((card) => {
    card.removeAttribute('role');
    card.removeAttribute('tabindex');
    const id = card.dataset.id || '';
    const box = strBoxes().find((b) => b.deviceID === id);
    const master = card.classList.contains('master');
    btn(card.querySelector('.zone-makemain'), 'secondary');
    btn(card.querySelector('.zone-update-badge'), 'secondary');
    if (master) return;
    const pressed = card.classList.contains('selected');
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'u-btn u-btn--secondary ui2-grp-act ui2-grp-toggle';
    b.dataset.ui2Act = 'toggle-member';
    b.dataset.key = id;
    b.setAttribute('aria-pressed', String(pressed));
    b.textContent = t('ui2.speakers.groups.include', { name: nameOf(box) });
    const foot = card.querySelector('.zone-card-foot');
    if (foot) foot.insertBefore(b, foot.firstChild); else card.appendChild(b);
  });
}

// pairTitle is a pair's name: STR's own, else the one the speakers carry.
const pairTitle = (pair) => (pair && (pairDisplayName(pair, () => renderMultiroom(false)) || pair.name)) || t('multiroom.stereoHeading');

const optionText = (sel) => (sel && sel.selectedOptions && sel.selectedOptions[0] ? sel.selectedOptions[0].textContent : '');

function selectedPair() {
  const key = pairKeyOf(state.stereoLeft, state.stereoRight);
  return stereoPairsOf(state.zoneLive || {}).find((p) => stereoPairKey(p) === key) || null;
}

function labelPairEditor(grp, root) {
  const title = grp.querySelector('#ui2GrpPairEditorTitle');
  const pair = root.querySelector('#stereoNameSave') ? selectedPair() : null;
  const name = pair ? pairTitle(pair) : '';
  if (title) title.textContent = pair ? t('ui2.speakers.groups.change', { name }) : t('ui2.speakers.groups.newPair');
  const create = root.querySelector('#stereoCreate');
  const left = optionText(root.querySelector('#stereoLeft'));
  const right = optionText(root.querySelector('#stereoRight'));
  if (create && !create.disabled && left && right && left !== right) {
    create.textContent = t('ui2.speakers.groups.createPair', { left, right });
  }
}

function labelGroupEditor(grp, root) {
  const title = grp.querySelector('#ui2GrpFixedEditorTitle');
  if (title) title.textContent = t(state.zonePermanent ? 'ui2.speakers.groups.newFixed' : 'ui2.speakers.groups.newGroup');
  const create = root.querySelector('#zoneCreate');
  if (!create || create.disabled) return;
  const { master, members } = composedGroup();
  if (!master || !members.length) return;
  create.textContent = t(state.zonePermanent ? 'ui2.speakers.groups.createFixed' : 'ui2.speakers.groups.createNow',
    { main: nameOf(master), members: joinNames(members.map(nameOf)) });
}

// composedGroup is what Create would send: the main speaker (the star) and
// the ticked speakers, the way views/multiroom.js reads them.
function composedGroup() {
  const boxes = strBoxes();
  const master = boxes.find((b) => b.deviceID === state.zoneMaster) || null;
  const sel = state.zoneSlaves || {};
  const members = boxes.filter((b) => master && b.deviceID !== master.deviceID && sel[b.deviceID]);
  return { master, members };
}

// placeResults moves each classic result note into the card of the pair or
// group the last action was about, or into its editor.
function placeResults(grp, root) {
  const into = (node, sec, fallback) => {
    if (!node) return;
    let target = null;
    if (pending && pending.section === sec && pending.cardSel) {
      const card = grp.querySelector(pending.cardSel);
      target = card && card.querySelector('[data-slot="card-result"]');
    }
    if (!target && sec === 'now') target = grp.querySelector('[data-slot="result-now"]');
    if (!target) target = grp.querySelector(`[data-slot="${fallback}"]`);
    // While the page waits for the speakers, their "done" is not said yet:
    // the classic green note would say it early.
    node.classList.toggle('ui2-grp-quiet-ok', !!pending && !pending.failed && !pending.doneAt);
    target.appendChild(node);
  };
  into(root.querySelector('#stereoResult'), 'pairs', 'result-pairs');
  into(root.querySelector('#zoneResult'), pending && pending.msgField === 'zoneMsg' ? pending.section : lastZoneSec, 'result-fixed');
}

// ---- honest feedback ------------------------------------------------------

function paintStatus(grp) {
  if (!pending) return;
  const msg = pending.msgField ? state[pending.msgField] : '';
  if (!pending.failed && !pending.doneAt && noteVerdict(msg) === 'error') pending.failed = true;
  const phase = pendingPhase(pending, state.zoneLive || {}, strBoxes());
  if (phase === 'expired') { pending = null; return; }
  if (phase === 'done' && !pending.doneAt) pending.doneAt = Date.now();
  const el = grp.querySelector(`#ui2GrpStatus-${pending.section}`);
  let text = '';
  let cls = '';
  // Done on part of the speakers only (an offline pair half) is not all done.
  if (phase === 'done') { text = pending.doneText(); cls = pending.partial ? 'is-warn' : 'is-ok'; }
  else if (phase === 'waiting') { text = t('ui2.speakers.groups.waiting', { names: joinNames(pending.names) }); }
  else if (phase === 'late') { text = t('ui2.speakers.groups.late', { names: joinNames(pending.names), refresh: t('common.refresh') }); cls = 'is-warn'; }
  else if (phase === 'failed') {
    // The classic note says what went wrong, in the card; say it once aloud.
    say(textOf(msg));
  }
  if (el && text) {
    el.textContent = text;
    el.hidden = false;
    if (cls) el.classList.add(cls);
  }
  if (text) say(text);
}

const textOf = (html) => {
  const d = document.createElement('div');
  d.innerHTML = String(html || '');
  return d.textContent.trim();
};

// say puts a status into one live region outside the view, so a repaint of
// the view never repeats it; only a change is announced.
function say(text) {
  if (!text || text === lastSaid) return;
  lastSaid = text;
  const fresh = !liveRegion || !document.body.contains(liveRegion);
  const region = ensureLiveRegion();
  // A region that only now joined the page is not registered with a screen
  // reader yet: write into it a moment later, or the first status is lost.
  if (fresh) setTimeout(() => { if (lastSaid === text) region.textContent = text; }, 50);
  else region.textContent = text;
}

// ensureLiveRegion creates the region empty; mountGroups calls it on entry,
// so it exists before the first action's status.
function ensureLiveRegion() {
  if (liveRegion && document.body.contains(liveRegion)) return liveRegion;
  liveRegion = document.createElement('div');
  liveRegion.id = 'ui2GrpLive';
  liveRegion.className = 'u-sr-only';
  liveRegion.setAttribute('role', 'status');
  liveRegion.setAttribute('aria-live', 'polite');
  document.body.appendChild(liveRegion);
  return liveRegion;
}

// describe works out, for one action, what the speakers are, what to ask
// before it (destructive ones) and what the speakers report once it is done.
function describe(act, el, root) {
  const zl = state.zoneLive || {};
  const boxes = strBoxes();
  const secOf = el.closest('[data-sec]');
  const pairName = pairTitle;
  const pairHalves = (pair) => pairMemberBoxes(pair, boxes).map((x) => (x.box ? nameOf(x.box) : (x.member.ip || x.member.deviceID)));
  const pairUndo = (pair) => {
    const [left, right] = pairHalves(pair);
    const name = pairName(pair);
    const key = stereoPairKey(pair);
    // The undo goes to the reachable halves only (multiroom.js
    // doDissolveStereoPair); an offline half keeps its side of the pair, so
    // "done" names only the halves that took it and says so about the other.
    const split = pairUndoSplit(pairMemberBoxes(pair, boxes), nameOf);
    const partial = split.offline.length > 0 && split.reachable.length > 0;
    return {
      names: partial ? split.reachable : [left, right],
      partial,
      confirm: {
        title: t('ui2.speakers.groups.confirmPairTitle', { name }),
        body: t('ui2.speakers.groups.confirmPairBody', { left, right }),
        yes: t('ui2.speakers.groups.confirmPairBtn', { name }),
      },
      expect: { type: 'pairGone', key },
      cardSel: key ? `.ui2-grp-card[data-pair-key="${key}"]` : '',
      doneText: () => (partial
        ? t('ui2.speakers.groups.donePairPartial', { name, names: joinNames(split.reachable), offline: joinNames(split.offline) })
        : t('ui2.speakers.groups.donePairGone', { name, names: joinNames([left, right]) })),
    };
  };
  const groupOf = (mk) => {
    const mb = masterBoxForKey(mk, zl, boxes);
    const name = mb ? nameOf(mb) : t('multiroom.groupLabelPrefix');
    const names = [name, ...(mb ? groupMembersOf(mb, zl, boxes).map((m) => (m.box ? nameOf(m.box) : (m.ip || m.deviceID))) : [])];
    return {
      names,
      confirm: {
        title: t('ui2.speakers.groups.confirmGroupTitle', { name }),
        body: t('ui2.speakers.groups.confirmGroupBody', { names: joinNames(names) }),
        yes: t('ui2.speakers.groups.confirmGroupBtn', { name }),
      },
      expect: { type: 'groupGone', master: mk },
      cardSel: `.ui2-grp-card[data-kind="zone"][data-key="${mk}"]`,
      doneText: () => t('ui2.speakers.groups.doneGroupGone', { name, names: joinNames(names) }),
    };
  };
  const storedOf = (mk) => storedPermanentGroupsOf(zl, boxes).find((g) => g.masterKey === mk) || null;
  const memberName = (m) => (m.box ? nameOf(m.box) : (m.name || m.ip));

  switch (act.kind) {
    case 'pair-create': {
      const l = root.querySelector('#stereoLeft');
      const r = root.querySelector('#stereoRight');
      if (!l || !r || l.value === r.value) return null;
      const left = optionText(l);
      const right = optionText(r);
      const key = pairKeyOf(l.value, r.value);
      const typed = ((root.querySelector('#stereoName') || {}).value || '').trim();
      const name = typed || pairName({ members: [{ deviceID: l.value }, { deviceID: r.value }] });
      return {
        names: [left, right],
        expect: { type: 'pairFormed', key },
        cardSel: `.ui2-grp-card[data-pair-key="${key}"]`,
        doneText: () => t('ui2.speakers.groups.donePair', { name, left, right }),
      };
    }
    case 'pair-rename': {
      const pair = selectedPair();
      const name = ((root.querySelector('#stereoName') || {}).value || '').trim();
      if (!pair || !name) return null;
      const [left, right] = pairHalves(pair);
      return {
        names: [left, right],
        expect: { type: 'pairName', key: stereoPairKey(pair), name },
        cardSel: `.ui2-grp-card[data-pair-key="${stereoPairKey(pair)}"]`,
        doneText: () => t('ui2.speakers.groups.doneRename', { name, left, right }),
      };
    }
    case 'pair-undo': {
      const pair = stereoPairsOf(zl).find((p) => up(p.master) === act.key
        || (p.members || []).some((m) => up(m && m.deviceID) === act.key));
      return pair ? pairUndo(pair) : null;
    }
    case 'pair-undo-sel': {
      const pair = selectedPair();
      if (pair) return pairUndo(pair);
      const l = root.querySelector('#stereoLeft');
      const r = root.querySelector('#stereoRight');
      if (!l || !r) return null;
      const left = optionText(l);
      const right = optionText(r);
      return {
        names: [left, right],
        confirm: {
          title: t('ui2.speakers.groups.confirmSelTitle', { left, right }),
          body: t('ui2.speakers.groups.confirmSelBody'),
          yes: t('multiroom.stereoDissolveBtn'),
        },
        expect: { type: 'pairGone', key: pairKeyOf(l.value, r.value), holder: l.value },
        doneText: () => t('ui2.speakers.groups.donePairGone', { name: t('multiroom.stereoHeading'), names: joinNames([left, right]) }),
      };
    }
    case 'stale-fix': {
      const f = (state.staleStereo || [])[act.index];
      if (!f) return null;
      const holder = nameOf(f.holder);
      const names = [holder, f.partnerLabel].filter(Boolean);
      return {
        names: [holder],
        confirm: {
          title: t('ui2.speakers.groups.confirmStaleTitle', { name: holder }),
          body: staleNoticeText(f, t),
          yes: t('stereoStale.fixBtn'),
        },
        expect: { type: 'pairGone', key: stereoPairKey(f.pair), holder: f.holder && f.holder.deviceID },
        doneText: () => t('ui2.speakers.groups.donePairGone', { name: (f.pair && f.pair.name) || t('multiroom.stereoHeading'), names: joinNames(names) }),
      };
    }
    case 'group-dissolve': {
      const d = groupOf(act.key);
      return { ...d, section: secOf ? secOf.dataset.sec : 'now' };
    }
    case 'group-ungroup': {
      const leader = liveLeaderOf(boxes, zl, state.zoneMaster);
      return leader ? groupOf(up(leader.deviceID)) : null;
    }
    case 'group-forget': {
      const g = storedOf(act.key);
      if (!g) return null;
      const name = nameOf(g.masterBox);
      const names = g.members.map(memberName);
      return {
        names: [name],
        confirm: {
          title: t('ui2.speakers.groups.confirmForgetTitle', { name }),
          body: t('ui2.speakers.groups.confirmForgetBody', { name, names: joinNames(names) }),
          yes: t('ui2.speakers.groups.confirmForgetBtn', { name }),
        },
        expect: { type: 'storedGone', masterID: g.masterBox.deviceID },
        cardSel: `.ui2-grp-card[data-kind="stored"][data-key="${act.key}"]`,
        doneText: () => t('ui2.speakers.groups.doneForgotten', { name }),
      };
    }
    case 'member-remove': {
      const g = storedOf(act.key);
      if (!g) return null;
      const name = nameOf(g.masterBox);
      const m = g.members.find((x) => (x.box && x.box.host === act.ip) || x.ip === act.ip);
      const member = m ? memberName(m) : act.ip;
      return {
        names: [name],
        confirm: {
          title: t('ui2.speakers.groups.confirmMemberTitle', { member, name }),
          body: t('ui2.speakers.groups.confirmMemberBody'),
          yes: t('ui2.speakers.groups.confirmMemberBtn', { member }),
        },
        expect: { type: 'memberOut', masterID: g.masterBox.deviceID, ip: act.ip, memberID: m && m.box ? m.box.deviceID : '' },
        cardSel: `.ui2-grp-card[data-kind="stored"][data-key="${act.key}"]`,
        doneText: () => t('ui2.speakers.groups.doneMemberOut', { member, name }),
      };
    }
    case 'group-create': {
      const { master, members } = composedGroup();
      if (!master || !members.length) return null;
      const permanent = !!state.zonePermanent;
      const name = nameOf(master);
      const memberNames = members.map(nameOf);
      const exp = { type: 'groupFormed', master: master.deviceID, members: members.map((b) => b.deviceID), permanent };
      return {
        names: [name, ...memberNames],
        expect: exp,
        doneText: () => (expectLive(exp)
          ? t('ui2.speakers.groups.doneLive', { name, names: joinNames(memberNames) })
          : t('ui2.speakers.groups.doneStored', { name })),
      };
    }
    case 'gk-remove': {
      const name = optionText(root.querySelector('#gkRemote'));
      const template = act.template;
      return {
        confirm: {
          title: t('ui2.speakers.groups.confirmKeyTitle', { template, name }),
          body: t('ui2.speakers.groups.confirmKeyBody', { name }),
          yes: t('ui2.speakers.groups.confirmKeyBtn', { template }),
        },
      };
    }
    default:
      return null;
  }
}

const expectLive = (exp) => expectationMet({ ...exp, type: 'groupLive' }, state.zoneLive || {}, strBoxes());

// start remembers an action that is about to be sent. The classic note of its
// section is cleared first, so the note found after it is this action's own.
function start(act, info) {
  if (!info || !info.expect) { pending = null; return; }
  const section = info.section || ACTIONS[act.kind].section;
  const msgField = section === 'pairs' ? 'stereoMsg' : 'zoneMsg';
  state[msgField] = '';
  pending = {
    kind: act.kind,
    section,
    msgField,
    at: Date.now(),
    expect: info.expect,
    names: info.names || [],
    cardSel: info.cardSel || '',
    doneText: info.doneText,
    partial: !!info.partial,
  };
  lastSaid = '';
}

// ---- events ---------------------------------------------------------------

function wire(root) {
  // Capture: runs before the classic handler on the control itself, so a
  // destructive click can be held until it is confirmed.
  root.addEventListener('click', (e) => {
    if (!isOn(root) || bypass) return;
    const own = e.target.closest('.ui2-grp-act');
    if (own && root.contains(own)) {
      if (own.dataset.ui2Act === 'edit-pair') {
        e.stopPropagation();
        editPair(own.dataset.key);
      }
      // toggle-member: falls through to the zone card's own click.
      return;
    }
    const el = e.target.closest(ACTION_SELECTOR);
    if (!el || !root.contains(el) || el.disabled) return;
    const act = actionOf(el);
    if (!act) return;
    const info = describe(act, el, root);
    const sec = (info && info.section) || ACTIONS[act.kind].section;
    if (sec === 'now' || sec === 'fixed') lastZoneSec = sec;
    if (info && info.confirm && ACTIONS[act.kind].confirm) {
      e.preventDefault();
      e.stopPropagation();
      const key = focusKeyOf(el);
      askConfirm(info.confirm).then((ok) => {
        const again = key ? root.querySelector(key) : null;
        if (!ok) {
          if (again) again.focus();
          return;
        }
        if (!again || again.disabled || !isOn(root)) return;
        start(act, info);
        bypass = true;
        try { again.click(); } finally { bypass = false; }
        // The control goes away with what it took apart, once the speakers
        // report it (a later repaint): the section heading keeps the keyboard
        // where the outcome will show, rather than on a control about to go.
        // The dialog's close already gave the focus back to that control.
        const h = root.querySelector(`#ui2GrpH-${sec}`);
        if (h) h.focus();
      });
      return;
    }
    start(act, info);
  }, true);

  // The classic "keep permanently" box relabels its own button; the preview
  // names the speakers on it and titles the editor.
  root.addEventListener('change', (e) => {
    if (!isOn(root) || !e.target || e.target.id !== 'zonePermanent') return;
    const grp = root.querySelector(':scope > .ui2-grp');
    if (grp) labelGroupEditor(grp, root);
  });

  // Leaving the route drops a pending action, the way the classic view drops
  // its notes (#882): coming back must not greet the user with an old one.
  new MutationObserver(() => {
    if (root.classList.contains('hidden')) { pending = null; lastSaid = ''; }
  }).observe(root, { attributes: true, attributeFilter: ['class'] });
}

// editPair loads a pair into the editor, the way picking one of its halves in
// the classic Left dropdown does, and puts the keyboard on its name.
function editPair(key) {
  const pair = stereoPairsOf(state.zoneLive || {}).find((p) => stereoPairKey(p) === key);
  const halves = pair ? pairMemberBoxes(pair, strBoxes()).map((x) => x.box).filter(Boolean) : [];
  if (halves.length !== 2) return;
  state.stereoLeft = halves[0].deviceID;
  state.stereoRight = halves[1].deviceID;
  delete state.stereoName;
  wantFocus = '#stereoName';
  renderMultiroom(false);
}

// ---- the confirmation -----------------------------------------------------

function ensureDialog() {
  if (dialog && document.body.contains(dialog)) return dialog;
  dialog = document.createElement('dialog');
  dialog.className = 'ui2-grp-dialog';
  dialog.id = 'ui2GrpConfirm';
  dialog.setAttribute('aria-labelledby', 'ui2GrpConfirmTitle');
  dialog.setAttribute('aria-describedby', 'ui2GrpConfirmBody');
  dialog.innerHTML = '<h2 class="ui2-grp-dialog-title" id="ui2GrpConfirmTitle"></h2>'
    + '<p class="ui2-grp-dialog-body" id="ui2GrpConfirmBody"></p>'
    + '<div class="ui2-grp-dialog-actions">'
    + '<button type="button" class="u-btn u-btn--secondary" id="ui2GrpConfirmNo"></button>'
    + '<button type="button" class="u-btn u-btn--danger" id="ui2GrpConfirmYes"></button></div>';
  document.body.appendChild(dialog);
  return dialog;
}

// askConfirm resolves true when the user confirms. Cancel has the focus, so
// Enter on an unread dialog never takes anything apart; Esc cancels.
function askConfirm({ title, body, yes }) {
  const d = ensureDialog();
  if (typeof d.showModal !== 'function') return Promise.resolve(window.confirm(`${title}\n\n${body}`));
  d.querySelector('#ui2GrpConfirmTitle').textContent = title;
  d.querySelector('#ui2GrpConfirmBody').textContent = body;
  const no = d.querySelector('#ui2GrpConfirmNo');
  const ok = d.querySelector('#ui2GrpConfirmYes');
  no.textContent = t('common.cancel');
  ok.textContent = yes;
  return new Promise((resolve) => {
    const finish = (v) => {
      no.onclick = null;
      ok.onclick = null;
      d.oncancel = null;
      if (d.open) d.close();
      resolve(v);
    };
    no.onclick = () => finish(false);
    ok.onclick = () => finish(true);
    d.oncancel = (e) => { e.preventDefault(); finish(false); };
    d.showModal();
    no.focus();
  });
}
