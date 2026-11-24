// views/settings/alarms.js: the alarm clock in Speaker settings (discussion
// #2090). Until now alarms could only be set on the speaker's own phone page;
// this is the same editor in the app, against the same agent endpoint
// (GET/PUT /api/alarms), so both always show the same alarms. The scheduler
// runs on the speaker: nothing here has to be open when an alarm goes off.
//
// The logic (document handling, the 409 retry, labels) lives in
// alarmsmodel.js and is unit-tested; this file only draws and wires.

import { escapeHtml, escapeAttr } from '../../utils.js';
import { t, getLocale } from '../../i18n/index.js';
import { boxFetch, GetPresets } from '../../api.js';
import { boxPresetRows, keyLabel, keyNum } from '../../presetrows.js';
import { reportDone, reportError } from './shared.js';
import {
  DAY_ORDER, SNOOZE_CHOICES, SNOOZE_DEFAULT,
  newAlarm, dayNames, daysLabel, hhmm, parseHHMM, timeLabel, nextLabel,
  readDoc, commitChange, refusalText, offChoices, offLabel, offAtDefault,
  draftProblem, keyIsEmptyIn, presetName, todayISO, skipLabel,
  addSkipDay, removeSkipDay, saveAlarm, deleteAlarm, setEnabled,
} from './alarmsmodel.js';
import './alarms.css';

// One speaker's editor at a time, like the rest of Speaker settings.
let cur = { box: null, doc: null, presets: null, draft: null, isNew: false, busy: false, gen: 0 };

export function renderAlarmSections(ctx) {
  const { box } = ctx;
  // A stock speaker has no agent and so no alarm clock.
  if (!box || box.kind === 'stock') return { group: 'alarms', html: '' };
  return {
    group: 'alarms',
    html: `
    <div data-ui2-sec="alarms" class="settings-section" id="alarmsSection">
      <h3>${escapeHtml(t('alarms.heading'))}</h3>
      <div class="alarms-warn" id="alarmsWarn" hidden></div>
      <div class="muted small" id="alarmsSum">${escapeHtml(t('common.loading'))}</div>
      <div id="alarmsList" class="alarms-list"></div>
      <div class="alarms-msg" id="alarmsMsg" role="status" hidden></div>
      <div class="setting-row" id="alarmsAddRow" hidden>
        <button class="btn btn-mini" type="button" id="alarmsAdd">${escapeHtml(t('alarms.add'))}</button>
      </div>
      <div id="alarmsSkip"></div>
      <small class="muted small" id="alarmsZone"></small>
      <small class="muted small">${escapeHtml(t('alarms.help'))}</small>
    </div>`,
  };
}

export function wireAlarmSections(ctx) {
  const section = document.getElementById('alarmsSection');
  if (!section) return;
  cur = { box: ctx.box, doc: null, presets: null, draft: null, isNew: false, busy: false, gen: cur.gen + 1 };
  const gen = cur.gen;
  const add = document.getElementById('alarmsAdd');
  if (add) add.onclick = () => {
    cur.draft = newAlarm();
    cur.isNew = true;
    paint();
  };
  load(gen);
}

async function load(gen) {
  const box = cur.box;
  // The key names come with the presets; the editor works without them (the
  // speaker checks an empty key itself on save), so this never blocks.
  GetPresets(box.host, box.port).then((list) => {
    if (gen !== cur.gen) return;
    cur.presets = Array.isArray(list) ? list : null;
    paint();
  }).catch(() => {});
  let doc = null;
  let reached = true;
  try {
    const r = await boxFetch(box, '/api/alarms');
    doc = readDoc(await readBody(r));
  } catch {
    reached = false;
  }
  if (gen !== cur.gen) return;
  const sum = document.getElementById('alarmsSum');
  if (!doc) {
    if (sum) sum.textContent = t(reached ? 'alarms.unsupported' : 'alarms.unreachable');
    return;
  }
  cur.doc = doc;
  paint();
}

// readBody reads an agent answer: JSON for the document (200 and 409), the
// speaker's own sentence for a refusal (400).
async function readBody(r) {
  const text = await r.text();
  try { return JSON.parse(text); } catch { return text; }
}

async function put(body) {
  const r = await boxFetch(cur.box, '/api/alarms', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  return { status: r.status, data: await readBody(r) };
}

function localZone() {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || ''; } catch { return ''; }
}

// commit saves one change and repaints from what the speaker now holds. A save
// that failed must never look like one that worked: the card goes back to the
// speaker's document and says why.
async function commit(change, anchor) {
  if (!cur.doc || cur.busy) return false;
  cur.busy = true;
  const gen = cur.gen;
  let res;
  try {
    res = await commitChange(cur.doc, change, put, localZone());
  } catch (e) {
    cur.busy = false;
    if (gen === cur.gen) reportError(anchor, t('alarms.unreachable'));
    return false;
  }
  cur.busy = false;
  if (gen !== cur.gen) return false;
  cur.doc = res.doc;
  if (!res.ok) {
    const why = refusalText(res.status, res.data);
    reportError(anchor, why ? `${t('alarms.fail')} ${why}` : t('alarms.fail'));
    paint();
    return false;
  }
  if (!res.unchanged) reportDone(anchor, t('alarms.saved'));
  return true;
}

// say puts a hint in the card itself: a missing day or an empty key is a fix
// the user makes right here, not an error worth a dialog.
function say(text) {
  const el = document.getElementById('alarmsMsg');
  if (!el) return;
  el.textContent = text || '';
  el.hidden = !text;
}

function keyName(slot) {
  const name = presetName(cur.presets, slot);
  const label = keyLabel(slot, t);
  if (keyIsEmptyIn(cur.presets, slot)) return t('alarms.keyEmpty', { n: keyNum(slot, t) });
  return name ? `${label} · ${name}` : label;
}

function paint() {
  say('');
  const doc = cur.doc;
  const list = document.getElementById('alarmsList');
  const sum = document.getElementById('alarmsSum');
  if (!doc || !list || !sum) return;
  const warn = document.getElementById('alarmsWarn');
  const zone = document.getElementById('alarmsZone');
  const addRow = document.getElementById('alarmsAddRow');
  const skip = document.getElementById('alarmsSkip');
  const st = doc.status || {};
  // The clock warning matters most: an alarm that cannot go off must never
  // look armed.
  if (warn) {
    warn.hidden = st.clockTrusted !== false;
    warn.textContent = st.clockTrusted === false ? t('alarms.clockBad') : '';
  }
  // Always shown: an empty zone silently means UTC, the one failure that looks
  // fine until an alarm rings an hour late.
  if (zone) zone.textContent = t('alarms.zone', { zone: doc.zone || 'UTC' });

  if (cur.draft) {
    sum.textContent = '';
    if (addRow) addRow.hidden = true;
    if (skip) skip.innerHTML = '';
    list.innerHTML = editorHtml(cur.draft);
    wireEditor();
    return;
  }
  if (addRow) addRow.hidden = false;
  const loc = getLocale();
  if (!doc.alarms.length) sum.textContent = t('alarms.none');
  else if (st.nextFire) sum.textContent = t('alarms.next', { when: nextLabel(st.nextFire, doc.zone, loc) });
  else sum.textContent = t('alarms.none');

  const names = dayNames(t('alarms.dow'));
  list.innerHTML = doc.alarms.map((a, i) => {
    const what = `${daysLabel(a, names)} · ${keyName(a.slot)}`;
    const time = timeLabel(a, loc);
    return `<div class="alarms-row">
      <label class="alarms-switch" title="${escapeAttr(t('alarms.enabledAria', { time }))}">
        <input type="checkbox" data-alarm-on="${i}"${a.enabled ? ' checked' : ''} aria-label="${escapeAttr(t('alarms.enabledAria', { time }))}">
      </label>
      <strong class="alarms-time">${escapeHtml(time)}</strong>
      <span class="alarms-what" title="${escapeAttr(what)}">${escapeHtml(what)}</span>
      <button class="btn btn-mini" type="button" data-alarm-edit="${i}">${escapeHtml(t('alarms.edit'))}</button>
    </div>`;
  }).join('');
  list.querySelectorAll('[data-alarm-edit]').forEach((b) => {
    b.onclick = () => {
      const a = cur.doc.alarms[Number(b.dataset.alarmEdit)];
      if (!a) return;
      cur.draft = JSON.parse(JSON.stringify(a));
      cur.isNew = false;
      paint();
    };
  });
  list.querySelectorAll('[data-alarm-on]').forEach((c) => {
    c.onchange = async () => {
      const a = cur.doc.alarms[Number(c.dataset.alarmOn)];
      if (!a) return;
      if (c.checked && keyIsEmptyIn(cur.presets, a.slot)) {
        // Switched on, it would be skipped when it comes due: say so instead.
        c.checked = false;
        say(t('alarms.keyEmptyFail', { n: keyNum(a.slot, t) }));
        return;
      }
      await commit(setEnabled(a.id, c.checked), c);
      paint();
    };
  });
  if (skip) {
    skip.innerHTML = skipHtml();
    wireSkip(skip);
  }
}

function editorHtml(a) {
  const names = dayNames(t('alarms.dow'));
  const days = DAY_ORDER.map((d) => {
    const on = (a.days || []).includes(d);
    return `<button class="btn btn-mini toggle-btn${on ? ' active' : ''}" type="button" data-alarm-day="${d}" aria-pressed="${on}">${escapeHtml(names[d])}</button>`;
  }).join('');
  const maxSlot = boxPresetRows(cur.box, cur.presets) * 6;
  let slots = '';
  for (let i = 1; i <= Math.max(maxSlot, Number(a.slot) || 1); i++) {
    slots += `<option value="${i}"${Number(a.slot) === i ? ' selected' : ''}>${escapeHtml(keyName(i))}</option>`;
  }
  const savedOff = Number(a.autoOff) || 0;
  const offAt = String(a.offAt || '');
  let offs = `<option value="0"${!offAt && !savedOff ? ' selected' : ''}>${escapeHtml(t('alarms.offNever'))}</option>`;
  offs += offChoices(savedOff).map((n) =>
    `<option value="${n}"${!offAt && savedOff === n ? ' selected' : ''}>${escapeHtml(offLabel(n, t))}</option>`).join('');
  offs += `<option value="at"${offAt ? ' selected' : ''}>${escapeHtml(t('alarms.offAt'))}</option>`;
  const snoozeNow = Number(a.snooze) || SNOOZE_DEFAULT;
  const snoozes = SNOOZE_CHOICES.map((n) =>
    `<option value="${n}"${snoozeNow === n ? ' selected' : ''}>${escapeHtml(t('alarms.offMin', { n }))}</option>`).join('');
  const vol = Number(a.volume) || 0;
  return `<div class="alarms-editor">
    <div class="alarms-field">
      <label class="muted small" for="alarmsTime">${escapeHtml(t('alarms.time'))}</label>
      <input type="time" id="alarmsTime" value="${escapeAttr(hhmm(a))}">
    </div>
    <div class="alarms-field">
      <span class="muted small">${escapeHtml(t('alarms.days'))}</span>
      <div class="alarms-days" id="alarmsDays">${days}</div>
    </div>
    <div class="alarms-field">
      <label class="muted small" for="alarmsSlot">${escapeHtml(t('alarms.preset'))}</label>
      <select id="alarmsSlot">${slots}</select>
    </div>
    <div class="alarms-field">
      <label class="muted small" for="alarmsVol">${escapeHtml(t('alarms.vol'))}: <span id="alarmsVolVal">${escapeHtml(vol > 0 ? String(vol) : t('alarms.volKeep'))}</span></label>
      <input type="range" id="alarmsVol" min="0" max="100" step="1" value="${vol}">
    </div>
    <div class="alarms-field">
      <label class="muted small" for="alarmsOff">${escapeHtml(t('alarms.off'))}</label>
      <select id="alarmsOff">${offs}</select>
      <div id="alarmsOffAtRow"${offAt ? '' : ' hidden'}>
        <input type="time" id="alarmsOffAt" value="${escapeAttr(offAt || offAtDefault(a))}" aria-label="${escapeAttr(t('alarms.offAt'))}">
        <small class="muted small">${escapeHtml(t('alarms.offAtHint'))}</small>
      </div>
    </div>
    <div class="alarms-field">
      <label class="muted small" for="alarmsSnooze">${escapeHtml(t('alarms.snooze'))}</label>
      <select id="alarmsSnooze">${snoozes}</select>
      <small class="muted small">${escapeHtml(t('alarms.snoozeHint'))}</small>
    </div>
    <div class="setting-row alarms-buttons">
      <button class="btn btn-mini" type="button" id="alarmsSave">${escapeHtml(t('alarms.save'))}</button>
      <button class="btn btn-mini" type="button" id="alarmsCancel">${escapeHtml(t('alarms.cancel'))}</button>
      ${cur.isNew ? '' : `<button class="btn btn-mini" type="button" id="alarmsDelete">${escapeHtml(t('alarms.delete'))}</button>`}
    </div>
  </div>`;
}

// capture copies the editor's fields into the draft; the day buttons write
// into it directly.
function capture() {
  const d = cur.draft;
  if (!d) return;
  const tm = parseHHMM((document.getElementById('alarmsTime') || {}).value);
  if (tm) { d.hour = tm.hour; d.minute = tm.minute; }
  const slot = document.getElementById('alarmsSlot');
  if (slot) d.slot = Number(slot.value) || 1;
  const vol = document.getElementById('alarmsVol');
  if (vol) d.volume = Number(vol.value) || 0;
  const off = document.getElementById('alarmsOff');
  if (off) {
    if (off.value === 'at') {
      const at = parseHHMM((document.getElementById('alarmsOffAt') || {}).value);
      if (at) d.offAt = `${String(at.hour).padStart(2, '0')}:${String(at.minute).padStart(2, '0')}`;
      d.autoOff = 0;
    } else {
      delete d.offAt;
      d.autoOff = Number(off.value) || 0;
    }
  }
  const snz = document.getElementById('alarmsSnooze');
  if (snz) d.snooze = Number(snz.value) || SNOOZE_DEFAULT;
}

function wireEditor() {
  document.querySelectorAll('[data-alarm-day]').forEach((b) => {
    b.onclick = () => {
      const d = Number(b.dataset.alarmDay);
      const days = cur.draft.days || (cur.draft.days = []);
      const at = days.indexOf(d);
      if (at >= 0) days.splice(at, 1); else days.push(d);
      const on = at < 0;
      b.classList.toggle('active', on);
      b.setAttribute('aria-pressed', String(on));
    };
  });
  const vol = document.getElementById('alarmsVol');
  if (vol) vol.oninput = () => {
    const lab = document.getElementById('alarmsVolVal');
    if (lab) lab.textContent = Number(vol.value) > 0 ? vol.value : t('alarms.volKeep');
  };
  const off = document.getElementById('alarmsOff');
  if (off) off.onchange = () => {
    const row = document.getElementById('alarmsOffAtRow');
    if (row) row.hidden = off.value !== 'at';
  };
  const save = document.getElementById('alarmsSave');
  if (save) save.onclick = async () => {
    capture();
    const draft = cur.draft;
    draft.enabled = true;
    const problem = draftProblem(draft, (slot) => keyIsEmptyIn(cur.presets, slot));
    if (problem) {
      // The editor stays open so the fix (a day, another key) can be made.
      say(t(problem, { n: keyNum(draft.slot, t) }));
      return;
    }
    save.disabled = true;
    const ok = await commit(saveAlarm(draft, cur.isNew), save);
    save.disabled = false;
    if (ok) { cur.draft = null; cur.isNew = false; }
    paint();
  };
  const cancel = document.getElementById('alarmsCancel');
  if (cancel) cancel.onclick = () => { cur.draft = null; cur.isNew = false; paint(); };
  const del = document.getElementById('alarmsDelete');
  if (del) del.onclick = async () => {
    const id = cur.draft.id;
    del.disabled = true;
    const ok = await commit(deleteAlarm(id), del);
    del.disabled = false;
    if (ok) { cur.draft = null; cur.isNew = false; }
    paint();
  };
}

// Days without an alarm (discussion #1491): holidays and days off, one list
// for every alarm on this speaker; past days drop off by themselves.
function skipHtml() {
  const doc = cur.doc;
  const loc = getLocale();
  const chips = (doc.skipDates || []).map((iso) =>
    `<span class="alarms-chip">${escapeHtml(skipLabel(iso, loc))}<button type="button" class="alarms-chip-x" data-skip-del="${escapeAttr(iso)}" aria-label="${escapeAttr(`${t('alarms.skipRemove')} ${skipLabel(iso, loc)}`)}">&times;</button></span>`).join('');
  return `<div class="alarms-skip">
    <span class="muted small"><strong>${escapeHtml(t('alarms.skip'))}</strong></span>
    ${chips ? `<div class="alarms-chips">${chips}</div>` : ''}
    <div class="setting-row">
      <input type="date" id="alarmsSkipDate" min="${escapeAttr(todayISO(doc.zone))}" aria-label="${escapeAttr(t('alarms.skip'))}">
      <button class="btn btn-mini" type="button" id="alarmsSkipAdd">${escapeHtml(t('alarms.skipAdd'))}</button>
    </div>
    <small class="muted small">${escapeHtml(t('alarms.skipHint'))}</small>
  </div>`;
}

function wireSkip(root) {
  const add = root.querySelector('#alarmsSkipAdd');
  if (add) add.onclick = async () => {
    const input = root.querySelector('#alarmsSkipDate');
    const iso = input ? String(input.value || '') : '';
    if (!/^\d{4}-\d{2}-\d{2}$/.test(iso)) return;
    const max = (cur.doc.status && cur.doc.status.maxSkipDates) || 60;
    if ((cur.doc.skipDates || []).length >= max && !(cur.doc.skipDates || []).includes(iso)) {
      say(t('alarms.skipFull', { n: max }));
      return;
    }
    await commit(addSkipDay(iso), add);
    paint();
  };
  root.querySelectorAll('[data-skip-del]').forEach((b) => {
    b.onclick = async () => {
      await commit(removeSkipDay(b.dataset.skipDel), b);
      paint();
    };
  });
}
