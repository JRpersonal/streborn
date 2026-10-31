// Day headings of Recently played in the UI 2.0 preview (#1286): calendar days
// in local time, across midnight and both clock changes.
import { describe, it, expect } from 'vitest';
import { localDate, dayKey, shiftDays, dayHeading, groupCardsByDay } from './recentdays.js';

const labels = { today: 'Today', yesterday: 'Yesterday', earlier: 'Earlier' };
const BERLIN = 'Europe/Berlin';
const NEW_YORK = 'America/New_York';
const group = (cards, now, extra = {}) =>
  groupCardsByDay(cards, { now: Date.parse(now), timeZone: BERLIN, locale: 'en-US', labels, ...extra });
const card = (ts, name = ts) => ({ ts, name });
const shape = (groups) => groups.map((g) => [g.label, g.items.map((i) => i.card.name)]);

describe('localDate and dayKey', () => {
  it('reads the calendar date in the given zone', () => {
    // 23:30 UTC is already the next day in Berlin (UTC+2 in summer).
    expect(dayKey(localDate('2026-07-14T23:30:00Z', BERLIN))).toBe('2026-07-15');
    expect(dayKey(localDate('2026-07-14T23:30:00Z', 'UTC'))).toBe('2026-07-14');
    expect(dayKey(localDate('2026-07-15T02:30:00Z', NEW_YORK))).toBe('2026-07-14');
  });

  it('gives null and an empty key for a timestamp that does not parse', () => {
    expect(localDate('', BERLIN)).toBeNull();
    expect(localDate('not a time', BERLIN)).toBeNull();
    expect(localDate(undefined, BERLIN)).toBeNull();
    expect(dayKey(null)).toBe('');
  });

  it('shifts across month and year ends', () => {
    expect(dayKey(shiftDays({ y: 2026, m: 3, d: 1 }, -1))).toBe('2026-02-28');
    expect(dayKey(shiftDays({ y: 2028, m: 3, d: 1 }, -1))).toBe('2028-02-29');
    expect(dayKey(shiftDays({ y: 2027, m: 1, d: 1 }, -1))).toBe('2026-12-31');
  });
});

describe('groupCardsByDay', () => {
  it('names today and yesterday, then the date with its weekday', () => {
    const g = group([
      card('2026-10-23T17:00:00Z', 'a'),
      card('2026-10-23T06:00:00Z', 'b'),
      card('2026-10-22T12:00:00Z', 'c'),
      card('2026-10-20T12:00:00Z', 'd'),
    ], '2026-10-23T18:00:00Z');
    expect(shape(g)).toEqual([
      ['Today', ['a', 'b']],
      ['Yesterday', ['c']],
      ['Tuesday, October 20', ['d']],
    ]);
  });

  it('keeps each card its index in the list it was given', () => {
    const g = group([card('2026-10-23T17:00:00Z'), card('2026-10-21T17:00:00Z'), card('2026-10-21T16:00:00Z')], '2026-10-23T18:00:00Z');
    expect(g.map((x) => x.items.map((i) => i.index))).toEqual([[0], [1, 2]]);
  });

  it('splits at local midnight, not at UTC midnight or 24 hours back', () => {
    // Now 00:20 Berlin on 24 October (22:20 UTC on the 23rd).
    const now = '2026-10-23T22:20:00Z';
    const g = group([
      card('2026-10-23T22:05:00Z', 'after midnight'), // 00:05 local, 15 minutes ago
      card('2026-10-23T21:55:00Z', 'before midnight'), // 23:55 local, 25 minutes ago
      card('2026-10-22T22:10:00Z', 'yesterday 00:10'), // 00:10 on the 23rd local
      card('2026-10-22T21:50:00Z', 'two days back'), // 23:50 on the 22nd local
    ], now);
    expect(shape(g)).toEqual([
      ['Today', ['after midnight']],
      ['Yesterday', ['before midnight', 'yesterday 00:10']],
      ['Thursday, October 22', ['two days back']],
    ]);
  });

  it('counts the 25 hour day when the clocks go back as one day', () => {
    // Berlin leaves summer time on 25 October 2026 at 03:00 local (01:00 UTC).
    // Now is 23:30 local on the 25th (22:30 UTC); 00:15 local that morning was
    // still summer time (22:15 UTC on the 24th), more than 24 hours ago.
    const g = group([
      card('2026-10-24T22:15:00Z', 'early on the 25th'),
      card('2026-10-24T21:45:00Z', 'late on the 24th'),
    ], '2026-10-25T22:30:00Z');
    expect(shape(g)).toEqual([
      ['Today', ['early on the 25th']],
      ['Yesterday', ['late on the 24th']],
    ]);
  });

  it('counts the 23 hour day when the clocks go forward as one day', () => {
    // Berlin starts summer time on 29 March 2026 at 02:00 local (01:00 UTC).
    // Now is 00:30 local on the 30th (22:30 UTC on the 29th). The 29th had 23
    // hours, so a play 23.5 hours ago is still yesterday, and one a little
    // over 23.5 hours ago already the day before.
    const g = group([
      card('2026-03-28T23:05:00Z', 'just after midnight on the 29th'), // 00:05 CET
      card('2026-03-28T22:59:00Z', 'last minute of the 28th'), // 23:59 CET
    ], '2026-03-29T22:30:00Z');
    expect(shape(g)).toEqual([
      ['Yesterday', ['just after midnight on the 29th']],
      ['Saturday, March 28', ['last minute of the 28th']],
    ]);
  });

  it('uses the zone it is given, so the same instant can be another day', () => {
    const cards = [card('2026-10-23T02:00:00Z', 'night')];
    expect(shape(group(cards, '2026-10-23T18:00:00Z'))[0][0]).toBe('Today');
    expect(shape(group(cards, '2026-10-23T18:00:00Z', { timeZone: NEW_YORK }))[0][0]).toBe('Yesterday');
  });

  it('adds the year only to a date from another year', () => {
    const g = group([card('2026-01-05T12:00:00Z', 'jan'), card('2025-12-30T12:00:00Z', 'dec')], '2026-01-20T12:00:00Z');
    expect(g.map((x) => x.label)).toEqual(['Monday, January 5', 'Tuesday, December 30, 2025']);
  });

  it('writes the date in the app language', () => {
    const g = group([card('2026-10-20T12:00:00Z')], '2026-10-23T18:00:00Z', { locale: 'de' });
    expect(g[0].label).toBe('Dienstag, 20. Oktober');
  });

  it('treats a card from a clock running ahead as today, never tomorrow', () => {
    const g = group([card('2026-10-23T22:10:00Z', 'ahead'), card('2026-10-23T20:00:00Z', 'now')], '2026-10-23T21:58:00Z');
    expect(shape(g)).toEqual([['Today', ['ahead', 'now']]]);
  });

  it('puts a card without a usable time last, under Earlier', () => {
    const g = group([card('garbage', 'bad'), card('2026-10-23T17:00:00Z', 'good')], '2026-10-23T18:00:00Z');
    expect(shape(g)).toEqual([['Today', ['good']], ['Earlier', ['bad']]]);
    expect(g[1].items[0].index).toBe(0);
  });

  it('sorts the days newest first even when the cards are not', () => {
    const g = group([card('2026-10-20T12:00:00Z', 'old'), card('2026-10-23T12:00:00Z', 'new')], '2026-10-23T18:00:00Z');
    expect(shape(g).map((x) => x[0])).toEqual(['Today', 'Tuesday, October 20']);
  });

  it('returns no group for no cards', () => {
    expect(group([], '2026-10-23T18:00:00Z')).toEqual([]);
    expect(group(undefined, '2026-10-23T18:00:00Z')).toEqual([]);
  });
});

describe('dayHeading', () => {
  it('falls back to the system language for a locale Intl does not know', () => {
    const today = { y: 2026, m: 10, d: 23 };
    expect(() => dayHeading('2026-10-01', today, { locale: 'xx-invalid-!!', labels })).not.toThrow();
  });
});
