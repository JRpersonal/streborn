// The alarm clock in Speaker settings (#2090): the document handling and the
// revision handshake it shares with the phone page (#1918).
import { describe, it, expect } from 'vitest';
import {
  DAY_ORDER, newAlarm, dayNames, daysLabel, hhmm, parseHHMM, readDoc, putBody, indexById,
  commitChange, refusalText, offChoices, offLabel, offAtDefault, draftProblem, keyIsEmptyIn,
  presetName, todayISO, addSkipDay, removeSkipDay, saveAlarm, deleteAlarm, setEnabled,
} from './alarmsmodel.js';

const doc = (over = {}) => ({
  zone: 'Europe/Berlin',
  alarms: [
    { id: 'a1', enabled: true, hour: 6, minute: 45, days: [1, 2, 3, 4, 5], slot: 2, volume: 20 },
    { id: 'a2', enabled: false, hour: 9, minute: 0, days: [0, 6], slot: 1 },
  ],
  skipDates: ['2026-12-24'],
  rev: 7,
  status: { clockTrusted: true },
  ...over,
});

describe('days and times', () => {
  it('shows the week Monday first but stores Sunday as 0', () => {
    expect(DAY_ORDER).toEqual([1, 2, 3, 4, 5, 6, 0]);
    const names = dayNames('So Mo Di Mi Do Fr Sa');
    expect(daysLabel({ days: [0, 6, 1] }, names)).toBe('Mo Sa So');
  });

  it('falls back to English day names when the translation is broken', () => {
    expect(dayNames('Mo Di')).toEqual(['Su', 'Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa']);
  });

  it('round-trips a time input value', () => {
    expect(hhmm({ hour: 7, minute: 5 })).toBe('07:05');
    expect(parseHHMM('07:05')).toEqual({ hour: 7, minute: 5 });
    expect(parseHHMM('25:00')).toBeNull();
    expect(parseHHMM('')).toBeNull();
  });

  it('starts a new alarm like the phone page does', () => {
    expect(newAlarm()).toEqual({ id: '', enabled: true, hour: 7, minute: 0, days: [1, 2, 3, 4, 5], slot: 1, volume: 0, autoOff: 60, snooze: 9 });
  });
});

describe('the document', () => {
  it('reads an old agent answering with its web page as "no alarm clock"', () => {
    expect(readDoc('<!doctype html>...')).toBeNull();
    expect(readDoc({})).toBeNull();
  });

  it('keeps rev null for an agent from before revisions', () => {
    expect(readDoc({ zone: '', alarms: [] }).rev).toBeNull();
    expect(readDoc(doc()).rev).toBe(7);
  });

  it('sends the revision, the skip days and its own zone only when the speaker has none', () => {
    expect(putBody(doc(), 'America/New_York')).toEqual({ zone: 'Europe/Berlin', alarms: doc().alarms, skipDates: ['2026-12-24'], rev: 7 });
    const b = putBody(readDoc({ zone: '', alarms: [] }), 'America/New_York');
    expect(b).toEqual({ zone: 'America/New_York', alarms: [], skipDates: [] });
    expect('rev' in b).toBe(false);
  });

  it('finds alarms by id, never by position', () => {
    expect(indexById(doc(), 'a2')).toBe(1);
    expect(indexById(doc(), 'nope')).toBe(-1);
    expect(indexById(doc(), '')).toBe(-1);
  });
});

describe('commitChange', () => {
  it('saves the change and takes the speaker\'s answer as the new document', async () => {
    const sent = [];
    const put = async (body) => { sent.push(body); return { status: 200, data: { ...body, rev: 8, status: {} } }; };
    const r = await commitChange(readDoc(doc()), setEnabled('a2', true), put, '');
    expect(r.ok).toBe(true);
    expect(sent).toHaveLength(1);
    expect(sent[0].alarms[1].enabled).toBe(true);
    expect(r.doc.rev).toBe(8);
  });

  it('does not touch the original document', async () => {
    const d = readDoc(doc());
    await commitChange(d, setEnabled('a2', true), async (body) => ({ status: 200, data: body }), '');
    expect(d.alarms[1].enabled).toBe(false);
  });

  it('re-applies the change to the newer document after a 409 (#1918)', async () => {
    // Another editor deleted a1 and moved to rev 9 in the meantime.
    const newer = doc({ alarms: [doc().alarms[1]], rev: 9 });
    const sent = [];
    const put = async (body) => {
      sent.push(body);
      if (sent.length === 1) return { status: 409, data: newer };
      return { status: 200, data: { ...body, rev: 10 } };
    };
    const r = await commitChange(readDoc(doc()), setEnabled('a2', true), put, '');
    expect(r.ok).toBe(true);
    expect(sent).toHaveLength(2);
    expect(sent[1].rev).toBe(9);
    expect(sent[1].alarms).toHaveLength(1);
    expect(sent[1].alarms[0]).toMatchObject({ id: 'a2', enabled: true });
  });

  it('stops when the alarm is gone in the newer document', async () => {
    const newer = doc({ alarms: [], rev: 9 });
    let calls = 0;
    const put = async () => { calls++; return { status: 409, data: newer }; };
    const r = await commitChange(readDoc(doc()), setEnabled('a1', false), put, '');
    expect(calls).toBe(1);
    expect(r.unchanged).toBe(true);
    expect(r.doc.alarms).toEqual([]);
  });

  it('reports a refusal and keeps the old document', async () => {
    const d = readDoc(doc());
    const r = await commitChange(d, deleteAlarm('a1'), async () => ({ status: 400, data: 'key 3 is empty\n' }), '');
    expect(r.ok).toBe(false);
    expect(r.doc).toBe(d);
    expect(refusalText(r.status, r.data)).toBe('Key 3 is empty');
  });

  it('sends nothing when there is nothing to change', async () => {
    let calls = 0;
    const r = await commitChange(readDoc(doc()), deleteAlarm('nope'), async () => { calls++; return { status: 200 }; }, '');
    expect(calls).toBe(0);
    expect(r.unchanged).toBe(true);
  });
});

describe('the changes', () => {
  it('saves a new alarm at the end and an edited one in place', () => {
    const d = readDoc(doc());
    saveAlarm({ ...newAlarm(), hour: 5 }, true)(d);
    expect(d.alarms).toHaveLength(3);
    saveAlarm({ ...d.alarms[0], hour: 8 }, false)(d);
    expect(d.alarms[0].hour).toBe(8);
    expect(d.alarms).toHaveLength(3);
  });

  it('brings an alarm back that another editor deleted while it was edited', () => {
    const d = readDoc(doc({ alarms: [] }));
    saveAlarm(doc().alarms[0], false)(d);
    expect(d.alarms.map((a) => a.id)).toEqual(['a1']);
  });

  it('adds and removes skip days by the day, sorted', () => {
    const d = readDoc(doc());
    expect(addSkipDay('2026-12-01')(d)).toBe(true);
    expect(d.skipDates).toEqual(['2026-12-01', '2026-12-24']);
    expect(addSkipDay('2026-12-01')(d)).toBe(false);
    expect(removeSkipDay('2026-12-24')(d)).toBe(true);
    expect(removeSkipDay('2026-12-24')(d)).toBe(false);
    expect(d.skipDates).toEqual(['2026-12-01']);
  });
});

describe('the editor', () => {
  const t = (k, p) => (k === 'alarms.offHours' ? `${p.n} h` : `${p.n} min`);

  it('keeps a switch-off value outside the steps instead of resetting it', () => {
    expect(offChoices(0)).toEqual([15, 30, 60, 120, 240, 480, 720]);
    expect(offChoices(90)).toEqual([15, 30, 60, 90, 120, 240, 480, 720]);
    expect(offLabel(120, t)).toBe('2 h');
    expect(offLabel(90, t)).toBe('90 min');
  });

  it('starts the switch-off time an hour after the alarm', () => {
    expect(offAtDefault({ hour: 23, minute: 30 })).toBe('00:30');
  });

  it('refuses a draft without days or on a known empty key', () => {
    const empty = (slot) => slot === 3;
    expect(draftProblem({ days: [], slot: 1 }, empty)).toBe('alarms.fail');
    expect(draftProblem({ days: [1], slot: 3 }, empty)).toBe('alarms.keyEmptyFail');
    expect(draftProblem({ days: [1], slot: 1 }, empty)).toBe('');
  });

  it('treats a key as empty only once the key list is known', () => {
    expect(keyIsEmptyIn(null, 3)).toBe(false);
    expect(keyIsEmptyIn([{ slot: 1, name: 'NDR 1' }], 3)).toBe(true);
    expect(keyIsEmptyIn([{ slot: 3, name: 'NDR 1' }], 3)).toBe(false);
    expect(presetName([{ slot: 3, name: 'NDR 1' }], 3)).toBe('NDR 1');
    expect(presetName(null, 3)).toBe('');
  });

  it('takes today from the speaker\'s zone', () => {
    // 23:30 UTC is already the next day in Berlin.
    const now = new Date(Date.UTC(2026, 10, 24, 23, 30));
    expect(todayISO('Europe/Berlin', now)).toBe('2026-11-25');
    expect(todayISO('UTC', now)).toBe('2026-11-24');
  });
});
