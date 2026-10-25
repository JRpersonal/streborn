// views/recentdays.js: the day headings of Recently played in the UI 2.0
// preview (#1286). "Today", "Yesterday", then the date in the app's language.
//
// A day is a calendar day where the user sits, not a 24 hour window: a card
// played at 00:30 belongs to today even when it is only half an hour old, and
// the day a clock change shortens or stretches is still one day. So the
// timestamp is first turned into its local calendar date, and "yesterday" is
// that date minus one calendar day (counted on plain dates in UTC, where no
// day is 23 or 25 hours long). timeZone is injectable so the tests can pin a
// zone with a clock change; the app leaves it out and gets the system zone.
//
// Self-contained (no imports): the tests evaluate it on its own.

const partsFmt = new Map();

// localDate returns { y, m, d } of ts as a calendar date in timeZone (the
// system zone when omitted), or null for a timestamp that does not parse.
export function localDate(ts, timeZone) {
  const t = ts instanceof Date ? ts.getTime() : new Date(ts).getTime();
  if (!Number.isFinite(t)) return null;
  const k = timeZone || '';
  let fmt = partsFmt.get(k);
  if (!fmt) {
    // Latin digits and the Gregorian calendar, whatever the system locale:
    // these parts are a key, not text for the user.
    fmt = new Intl.DateTimeFormat('en-US-u-ca-gregory-nu-latn', {
      year: 'numeric', month: 'numeric', day: 'numeric', timeZone: timeZone || undefined,
    });
    partsFmt.set(k, fmt);
  }
  const p = {};
  for (const x of fmt.formatToParts(t)) p[x.type] = x.value;
  return { y: Number(p.year), m: Number(p.month), d: Number(p.day) };
}

// dayKey is the sortable "YYYY-MM-DD" of a calendar date, '' for null.
export function dayKey(date) {
  if (!date) return '';
  const pad = (n, w) => String(n).padStart(w, '0');
  return `${pad(date.y, 4)}-${pad(date.m, 2)}-${pad(date.d, 2)}`;
}

// shiftDays moves a calendar date by n days (Date.UTC rolls months and years).
export function shiftDays(date, n) {
  const u = new Date(Date.UTC(date.y, date.m - 1, date.d + n));
  return { y: u.getUTCFullYear(), m: u.getUTCMonth() + 1, d: u.getUTCDate() };
}

// dayHeading names one day for its heading. labels carries the translated
// today / yesterday / earlier words. A date is written out in the app's
// language with the weekday ("Tuesday, 20 October"), plus the year when it is
// not the current one. A key that is '' (a timestamp that did not parse) is
// "Earlier".
export function dayHeading(key, todayDate, { locale, labels }) {
  if (!key) return labels.earlier;
  if (key === dayKey(todayDate)) return labels.today;
  if (key === dayKey(shiftDays(todayDate, -1))) return labels.yesterday;
  const [y, m, d] = key.split('-').map(Number);
  const opts = { weekday: 'long', day: 'numeric', month: 'long', timeZone: 'UTC' };
  if (y !== todayDate.y) opts.year = 'numeric';
  // Noon UTC of that date, formatted in UTC: the date cannot slip a day.
  const at = Date.UTC(y, m - 1, d, 12);
  try {
    return new Intl.DateTimeFormat(locale || undefined, opts).format(at);
  } catch {
    return new Intl.DateTimeFormat(undefined, opts).format(at);
  }
}

// groupCardsByDay sorts the newest-first cards into day groups by each card's
// newest listening (card.ts), newest day first, keeping the cards' order and
// their index in the input (the view wires its buttons by that index). A card
// stamped after today (a speaker whose clock runs ahead) counts as today, so a
// clock a few minutes fast around midnight never makes "tomorrow" appear.
export function groupCardsByDay(cards, { now = Date.now(), timeZone, locale, labels }) {
  const today = localDate(now, timeZone) || localDate(Date.now(), timeZone);
  const todayKey = dayKey(today);
  const groups = [];
  const byKey = new Map();
  (cards || []).forEach((card, index) => {
    let key = dayKey(localDate(card && card.ts, timeZone));
    if (key > todayKey) key = todayKey;
    let g = byKey.get(key);
    if (!g) {
      g = { key, label: dayHeading(key, today, { locale, labels }), items: [] };
      byKey.set(key, g);
      groups.push(g);
    }
    g.items.push({ card, index });
  });
  // Newest day first, the unparseable ones last. Stable, so the cards inside
  // a day keep the order they came in.
  return groups.sort((a, b) => (a.key === b.key ? 0 : !a.key ? 1 : !b.key ? -1 : a.key < b.key ? 1 : -1));
}
