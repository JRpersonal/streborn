// views/settings/alarmsmodel.js: the pure half of the alarm clock in Speaker
// settings (discussion #2090: "set the alarm in the PC app too, not only on the
// speaker's page").
//
// The alarm clock runs on the speaker (internal/webui/alarms.go); the app and
// the phone page are two editors of the same document behind GET/PUT
// /api/alarms. Everything here follows the phone page's editor
// (internal/webui/assets/index.html, renderAlarms and friends) so the two
// agree: the same new-alarm defaults, the same switch-off steps, the same
// revision handshake (discussion #1918) so neither overwrites a change the
// other just made.
//
// No DOM and no api.js here, so all of it is unit-tested.

// Weekdays as the speaker stores them: 0 = Sunday, matching Go's time.Weekday.
// SHOWN Monday first, the way a European week reads (the phone does the same).
export const DAY_ORDER = Object.freeze([1, 2, 3, 4, 5, 6, 0]);

// The switch-off choices in minutes, up to the agent's MaxAutoOffMinutes (12 h).
export const OFF_STEPS = Object.freeze([15, 30, 60, 120, 240, 480, 720]);

// The snooze lengths the agent accepts (alarm.SnoozeChoices); 9 when unset.
export const SNOOZE_CHOICES = Object.freeze([5, 9, 10, 15]);
export const SNOOZE_DEFAULT = 9;

// What a fresh alarm starts as, the phone page's defaults.
export function newAlarm() {
  return { id: '', enabled: true, hour: 7, minute: 0, days: [1, 2, 3, 4, 5], slot: 1, volume: 0, autoOff: 60, snooze: SNOOZE_DEFAULT };
}

// dayNames splits the translated "Su Mo Tu We Th Fr Sa" (Sunday first, as
// stored) into seven names, the English ones when the translation is broken.
export function dayNames(dow) {
  const n = String(dow || '').trim().split(/\s+/);
  return n.length === 7 ? n : ['Su', 'Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa'];
}

// daysLabel is an alarm's days in display order: "Mo Tu We Th Fr".
export function daysLabel(a, names) {
  return [...((a && a.days) || [])]
    .sort((x, y) => DAY_ORDER.indexOf(x) - DAY_ORDER.indexOf(y))
    .map((d) => names[d])
    .filter(Boolean)
    .join(' ');
}

const pad2 = (n) => String(Number(n) || 0).padStart(2, '0');

// hhmm is the value of a time input, always 24 h "07:05".
export function hhmm(a) {
  return `${pad2(a && a.hour)}:${pad2(a && a.minute)}`;
}

// parseHHMM reads a time input's value; null when it is not a time.
export function parseHHMM(v) {
  const m = /^(\d{1,2}):(\d{2})/.exec(String(v || ''));
  if (!m) return null;
  const hour = Number(m[1]);
  const minute = Number(m[2]);
  if (hour > 23 || minute > 59) return null;
  return { hour, minute };
}

// timeLabel is an alarm's time the way the list shows it, 12 h or 24 h as the
// computer's own settings say (the convention the time input follows too).
export function timeLabel(a, locale) {
  try {
    const hc = new Intl.DateTimeFormat(locale, { hour: 'numeric' }).resolvedOptions().hourCycle;
    const hour = hc === 'h23' || hc === 'h24' ? '2-digit' : 'numeric';
    return new Intl.DateTimeFormat(locale, { hour, minute: '2-digit', timeZone: 'UTC' })
      .format(new Date(Date.UTC(2000, 0, 1, Number(a.hour) || 0, Number(a.minute) || 0)));
  } catch {
    return hhmm(a);
  }
}

// nextLabel is the next alarm for the "Next:" line, in the SPEAKER's zone (the
// zone the list's times are in), without seconds. Falls back to the
// computer's zone when this browser does not know the speaker's.
export function nextLabel(iso, zone, locale) {
  const when = new Date(iso);
  if (Number.isNaN(when.getTime())) return String(iso || '');
  const opts = { weekday: 'short', day: 'numeric', month: 'short', hour: 'numeric', minute: '2-digit' };
  if (zone) {
    try { return when.toLocaleString(locale, { ...opts, timeZone: zone }); } catch { /* unknown zone */ }
  }
  try { return when.toLocaleString(locale, opts); } catch { return when.toISOString(); }
}

// readDoc turns the speaker's answer into the editor's document, or null when
// it is not one. An agent from before the alarm clock answers /api/alarms with
// its whole web page as a string (the path falls through to the index), which
// must read as "no alarm clock here", not as a speaker without alarms.
export function readDoc(d) {
  if (!d || typeof d !== 'object' || !Array.isArray(d.alarms)) return null;
  return {
    zone: d.zone || '',
    alarms: d.alarms.map((a) => ({ ...a, days: Array.isArray(a.days) ? [...a.days] : [] })),
    skipDates: Array.isArray(d.skipDates) ? [...d.skipDates] : [],
    // null against an agent from before revisions: the save then goes
    // without one, the way the phone page always did.
    rev: typeof d.rev === 'number' ? d.rev : null,
    status: d.status && typeof d.status === 'object' ? d.status : {},
  };
}

// putBody is what a save sends: the whole document, the revision it was read
// at, and the computer's zone only when the speaker has none yet (so two
// editors in different places never keep re-homing each other's alarms).
// skipDates always goes along, [] included: the agent keeps its stored list
// only when the field is missing.
export function putBody(doc, localZone) {
  const body = { zone: doc.zone || localZone || '', alarms: doc.alarms, skipDates: doc.skipDates || [] };
  if (typeof doc.rev === 'number') body.rev = doc.rev;
  return body;
}

// indexById finds an alarm by id, -1 when the document has none such.
export function indexById(doc, id) {
  if (!id || !doc) return -1;
  return doc.alarms.findIndex((a) => a && a.id === id);
}

// commitChange saves ONE change the user made. change(doc) applies it to a
// copy of the document (by alarm id or by day, never by position) and returns
// false when there is nothing left to apply it to. put(body) sends the PUT and
// resolves to { status, data }.
//
// When another editor saved in between, the speaker answers 409 with its
// current document; the change is applied to that one and saved once more
// (discussion #1918: an alarm switched off in one app came back on when the
// other saved its stale copy).
//
// Resolves to { ok, doc, status, data, unchanged }: doc is the document the
// speaker now holds when ok.
export async function commitChange(doc, change, put, localZone) {
  let work = clone(doc);
  if (change(work) === false) return { ok: true, doc, unchanged: true };
  let r = await put(putBody(work, localZone));
  if (r && r.status === 409) {
    const fresh = readDoc(r.data);
    if (fresh) {
      work = clone(fresh);
      if (change(work) === false) return { ok: true, doc: fresh, unchanged: true };
      r = await put(putBody(work, localZone));
    }
  }
  const saved = r && r.status >= 200 && r.status < 300 ? readDoc(r.data) : null;
  return { ok: !!saved, doc: saved || doc, status: r ? r.status : 0, data: r ? r.data : null };
}

function clone(doc) {
  return JSON.parse(JSON.stringify({ zone: doc.zone, alarms: doc.alarms, skipDates: doc.skipDates, rev: doc.rev, status: doc.status }));
}

// refusalText is the speaker's reason for a 400, first letter upper case, or ''.
// It reaches the user verbatim (the agent writes it for that, e.g. an empty key).
export function refusalText(status, data) {
  if (status !== 400 || typeof data !== 'string') return '';
  const why = data.trim();
  return why ? why.charAt(0).toUpperCase() + why.slice(1) : '';
}

// offChoices is the switch-off list for an alarm: the fixed steps, plus a saved
// value outside them (set through the API) so opening and saving the alarm
// does not quietly reset it.
export function offChoices(saved) {
  const steps = [...OFF_STEPS];
  const n = Number(saved) || 0;
  if (n > 0 && !steps.includes(n)) steps.push(n);
  return steps.sort((x, y) => x - y);
}

// offLabel names one switch-off step with the translated "{n} min" / "{n} h".
export function offLabel(minutes, t) {
  const hours = minutes >= 60 && minutes % 60 === 0;
  return hours ? t('alarms.offHours', { n: minutes / 60 }) : t('alarms.offMin', { n: minutes });
}

// offAtDefault is where the "at a set time" switch-off starts: an hour after
// the alarm.
export function offAtDefault(a) {
  return `${pad2(((Number(a.hour) || 0) + 1) % 24)}:${pad2(a.minute)}`;
}

// draftProblem is why a draft cannot be saved yet, as an i18n key, or ''. The
// speaker refuses both too; saying so here names the fix one round trip
// earlier and keeps the editor open.
export function draftProblem(draft, keyIsEmpty) {
  if (!draft.days || !draft.days.length) return 'alarms.fail';
  if (keyIsEmpty(draft.slot)) return 'alarms.keyEmptyFail';
  return '';
}

// keyIsEmptyIn says whether a key is KNOWN to hold nothing. Until the key list
// has been read (presets null) it says false and the speaker's own check on
// save decides instead.
export function keyIsEmptyIn(presets, slot) {
  if (!Array.isArray(presets)) return false;
  return !presets.some((p) => p && Number(p.slot) === Number(slot));
}

// presetName is the station on a key, or '' when the list does not know it.
export function presetName(presets, slot) {
  if (!Array.isArray(presets)) return '';
  const p = presets.find((x) => x && Number(x.slot) === Number(slot));
  return (p && p.name) || '';
}

// todayISO is today in the SPEAKER's zone, the calendar it skips days by.
export function todayISO(zone, now = new Date()) {
  try {
    const opts = { year: 'numeric', month: '2-digit', day: '2-digit' };
    if (zone) opts.timeZone = zone;
    const p = new Intl.DateTimeFormat('en-CA', opts).formatToParts(now);
    const get = (type) => (p.find((x) => x.type === type) || {}).value || '';
    return `${get('year')}-${get('month')}-${get('day')}`;
  } catch {
    return `${now.getFullYear()}-${pad2(now.getMonth() + 1)}-${pad2(now.getDate())}`;
  }
}

// skipLabel shows a "2026-12-24" day as a short local date.
export function skipLabel(iso, locale) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(iso));
  if (!m) return String(iso);
  const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  try {
    return d.toLocaleDateString(locale, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' });
  } catch {
    return String(iso);
  }
}

// addSkipDay and removeSkipDay are the changes for commitChange: by the day,
// never by position, so they still apply after a 409 brought another list.
export function addSkipDay(iso) {
  return (doc) => {
    const list = [...(doc.skipDates || [])];
    if (list.includes(iso)) return false;
    list.push(iso);
    list.sort();
    doc.skipDates = list;
    return true;
  };
}

export function removeSkipDay(iso) {
  return (doc) => {
    const list = [...(doc.skipDates || [])];
    const i = list.indexOf(iso);
    if (i < 0) return false;
    list.splice(i, 1);
    doc.skipDates = list;
    return true;
  };
}

// saveAlarm puts a draft back: in place by id, or appended when it is new or
// was deleted from another editor meanwhile (saving is what the user asked for).
export function saveAlarm(draft, isNew) {
  return (doc) => {
    const copy = JSON.parse(JSON.stringify(draft));
    const i = isNew ? -1 : indexById(doc, draft.id);
    if (i >= 0) doc.alarms[i] = copy;
    else doc.alarms.push(copy);
    return true;
  };
}

export function deleteAlarm(id) {
  return (doc) => {
    const i = indexById(doc, id);
    if (i < 0) return false;
    doc.alarms.splice(i, 1);
    return true;
  };
}

export function setEnabled(id, on) {
  return (doc) => {
    const i = indexById(doc, id);
    if (i < 0) return false;
    doc.alarms[i].enabled = !!on;
    return true;
  };
}
