package alarm

import "time"

// scanDays bounds every weekday scan. A weekday set is never empty (Validate
// rejects that), so a matching day always exists within a week; the eighth day
// is only there so "today, but the time has already passed" resolves to the
// same weekday next week rather than falling off the end.
const scanDays = 8

// Fires reports whether this alarm repeats on w.
func (a Alarm) Fires(w time.Weekday) bool {
	for _, d := range a.Days {
		if time.Weekday(d) == w {
			return true
		}
	}
	return false
}

// at builds this alarm's instant on the calendar day of day, in loc.
//
// time.Date is doing real work here, and both of its normalisations are the
// behaviour we want. On the spring-forward day a local 02:30 does not exist,
// and time.Date returns the real instant an hour later rather than an error, so
// the alarm still goes off that morning. On the fall-back day 02:30 happens
// twice, and time.Date resolves it to a single one of them (measured: the
// second, in standard time), so the alarm goes off once at the wall-clock
// reading the user set, which is what an alarm clock is for. Go documents that
// choice as not guaranteed, so the tests assert that it resolves to one instant
// on the right day and not which side of the transition it lands on.
func (a Alarm) at(day time.Time, loc *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), a.Hour, a.Minute, 0, 0, loc)
}

// SkipSet is a document's skip dates as a lookup, keyed "YYYY-MM-DD". A nil
// set skips nothing.
type SkipSet map[string]bool

// Skips reports whether the calendar day of day, read in day's own location,
// is a skip date.
func (s SkipSet) Skips(day time.Time) bool {
	return s[day.Format(dateLayout)]
}

// Skips returns the document's skip dates as a lookup.
func (d Document) Skips() SkipSet {
	if len(d.SkipDates) == 0 {
		return nil
	}
	s := make(SkipSet, len(d.SkipDates))
	for _, day := range d.SkipDates {
		s[day] = true
	}
	return s
}

// scanFor widens the scan by one week per skip date: an alarm on a single
// weekday whose next n occurrences are all skipped is due n weeks later, and
// the scan must still reach it. Without skip dates it is scanDays.
func scanFor(skip SkipSet) int {
	return scanDays + 7*len(skip)
}

// firesOn reports whether the alarm rings on the calendar day of day: its
// weekday is in the set and the date is not skipped.
func (a Alarm) firesOn(day time.Time, skip SkipSet) bool {
	return a.Fires(day.Weekday()) && !skip.Skips(day)
}

// MostRecentDue returns the latest instant at or before now that this alarm was
// due, and whether one exists inside the scan window. A skipped day is not due
// at all, so the answer on a skipped morning is the alarm's previous ring,
// which is long out of any grace window.
//
// The weekday tested is the CALENDAR day being scanned, not the weekday of the
// normalised instant, so each day yields exactly one candidate and a DST shift
// can neither skip a day nor produce two.
func (a Alarm) MostRecentDue(now time.Time, loc *time.Location, skip SkipSet) (time.Time, bool) {
	local := now.In(loc)
	for back, n := 0, scanFor(skip); back < n; back++ {
		day := local.AddDate(0, 0, -back)
		if !a.firesOn(day, skip) {
			continue
		}
		if cand := a.at(day, loc); !cand.After(now) {
			return cand, true
		}
	}
	return time.Time{}, false
}

// NextDue returns the earliest instant strictly after now that this alarm is
// due, and whether one exists inside the scan window. Skipped days are passed
// over, so this is also the "next alarm" the editor shows.
func (a Alarm) NextDue(now time.Time, loc *time.Location, skip SkipSet) (time.Time, bool) {
	local := now.In(loc)
	for fwd, n := 0, scanFor(skip); fwd < n; fwd++ {
		day := local.AddDate(0, 0, fwd)
		if !a.firesOn(day, skip) {
			continue
		}
		if cand := a.at(day, loc); cand.After(now) {
			return cand, true
		}
	}
	return time.Time{}, false
}

// SwitchOff returns the moment a (re)fire of this alarm switches the speaker
// off, and whether it does at all. due is the alarm's scheduled instant and
// rang the moment this (re)fire started ringing, which differ after a snooze.
//
// A switch-off after some minutes counts from rang, so a snooze moves it on.
// A switch-off at a time of day (OffAt) is the first such moment strictly
// after due, on due's calendar day or the next one, and does NOT move with a
// snooze: "off at 20:00" means 20:00. The caller decides what a moment that
// has already passed means.
//
// time.Date carries the DST cases the same way at does: on the spring-forward
// day a switch-off inside the missing hour lands an hour later, and the
// duration from due counts the real time in between, so a 20:00 alarm with
// OffAt 06:00 across the fall-back night plays for eleven real hours, not ten.
func (a Alarm) SwitchOff(due, rang time.Time, loc *time.Location) (time.Time, bool) {
	if a.OffAt != "" {
		off, err := time.Parse(clockLayout, a.OffAt)
		if err != nil {
			return time.Time{}, false
		}
		local := due.In(loc)
		for fwd := 0; fwd < 2; fwd++ {
			cand := time.Date(local.Year(), local.Month(), local.Day()+fwd, off.Hour(), off.Minute(), 0, 0, loc)
			if cand.After(due) {
				return cand, true
			}
		}
		return time.Time{}, false
	}
	if a.AutoOff > 0 {
		return rang.Add(time.Duration(a.AutoOff) * time.Minute), true
	}
	return time.Time{}, false
}

// NextFire returns the earliest instant after now that any ENABLED alarm in the
// document is due, along with that alarm. A document whose zone no longer
// resolves schedules nothing rather than guessing at UTC. Skip dates are
// honoured, so this is the next morning the speaker really rings.
func (d Document) NextFire(now time.Time) (time.Time, Alarm, bool) {
	loc, err := d.Location()
	if err != nil {
		return time.Time{}, Alarm{}, false
	}
	var (
		best  time.Time
		which Alarm
		found bool
	)
	skip := d.Skips()
	for _, a := range d.Alarms {
		if !a.Enabled {
			continue
		}
		t, ok := a.NextDue(now, loc, skip)
		if !ok {
			continue
		}
		if !found || t.Before(best) {
			best, which, found = t, a, true
		}
	}
	return best, which, found
}
