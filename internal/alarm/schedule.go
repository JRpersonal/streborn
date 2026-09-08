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

// MostRecentDue returns the latest instant at or before now that this alarm was
// due, and whether one exists inside the scan window.
//
// The weekday tested is the CALENDAR day being scanned, not the weekday of the
// normalised instant, so each day yields exactly one candidate and a DST shift
// can neither skip a day nor produce two.
func (a Alarm) MostRecentDue(now time.Time, loc *time.Location) (time.Time, bool) {
	local := now.In(loc)
	for back := 0; back < scanDays; back++ {
		day := local.AddDate(0, 0, -back)
		if !a.Fires(day.Weekday()) {
			continue
		}
		if cand := a.at(day, loc); !cand.After(now) {
			return cand, true
		}
	}
	return time.Time{}, false
}

// NextDue returns the earliest instant strictly after now that this alarm is
// due, and whether one exists inside the scan window.
func (a Alarm) NextDue(now time.Time, loc *time.Location) (time.Time, bool) {
	local := now.In(loc)
	for fwd := 0; fwd < scanDays; fwd++ {
		day := local.AddDate(0, 0, fwd)
		if !a.Fires(day.Weekday()) {
			continue
		}
		if cand := a.at(day, loc); cand.After(now) {
			return cand, true
		}
	}
	return time.Time{}, false
}

// NextFire returns the earliest instant after now that any ENABLED alarm in the
// document is due, along with that alarm. A document whose zone no longer
// resolves schedules nothing rather than guessing at UTC.
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
	for _, a := range d.Alarms {
		if !a.Enabled {
			continue
		}
		t, ok := a.NextDue(now, loc)
		if !ok {
			continue
		}
		if !found || t.Before(best) {
			best, which, found = t, a, true
		}
	}
	return best, which, found
}

// UntilNextFire is how long the scheduler may sleep before the next alarm is
// due. It returns fallback when nothing is scheduled, and never a negative
// duration.
func (d Document) UntilNextFire(now time.Time, fallback time.Duration) time.Duration {
	next, _, ok := d.NextFire(now)
	if !ok {
		return fallback
	}
	if wait := next.Sub(now); wait > 0 {
		return wait
	}
	return 0
}
