package alarm

import (
	"testing"
	"time"
)

func berlin(t *testing.T) *time.Location {
	t.Helper()
	// This resolves from the embedded database (tzdata.go), so the test does
	// not care whether the machine running it has a system zoneinfo.
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("Europe/Berlin has to resolve from the embedded tzdata: %v", err)
	}
	return loc
}

func daily(hour, minute int) Alarm {
	return Alarm{ID: "d", Enabled: true, Hour: hour, Minute: minute, Days: []int{0, 1, 2, 3, 4, 5, 6}, Slot: 1}
}

func TestNextDueToday(t *testing.T) {
	loc := berlin(t)
	// Monday 2026-09-07, before the alarm.
	now := time.Date(2026, 9, 7, 5, 0, 0, 0, loc)
	got, ok := weekdayAlarm().NextDue(now, loc)
	if !ok {
		t.Fatal("a weekday alarm has a next fire on a Monday morning")
	}
	want := time.Date(2026, 9, 7, 6, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("next due = %v, want %v", got, want)
	}
}

func TestNextDueSkipsTheWeekend(t *testing.T) {
	loc := berlin(t)
	// Friday 2026-09-11, after the alarm has already gone off.
	now := time.Date(2026, 9, 11, 7, 0, 0, 0, loc)
	got, ok := weekdayAlarm().NextDue(now, loc)
	if !ok {
		t.Fatal("expected a next fire")
	}
	want := time.Date(2026, 9, 14, 6, 30, 0, 0, loc) // the following Monday
	if !got.Equal(want) {
		t.Errorf("next due = %v, want the Monday %v", got, want)
	}
}

// A single-day alarm has to wrap all the way round the week in both directions.
func TestWeekdayWraps(t *testing.T) {
	loc := berlin(t)
	sunday := Alarm{ID: "s", Enabled: true, Hour: 9, Minute: 0, Days: []int{0}, Slot: 1}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, loc) // a Monday
	next, ok := sunday.NextDue(now, loc)
	if !ok || next.Weekday() != time.Sunday {
		t.Fatalf("next due = %v (%v), want a Sunday", next, next.Weekday())
	}
	if want := time.Date(2026, 9, 13, 9, 0, 0, 0, loc); !next.Equal(want) {
		t.Errorf("next due = %v, want %v", next, want)
	}
	prev, ok := sunday.MostRecentDue(now, loc)
	if !ok || prev.Weekday() != time.Sunday {
		t.Fatalf("most recent due = %v (%v), want a Sunday", prev, prev.Weekday())
	}
	if want := time.Date(2026, 9, 6, 9, 0, 0, 0, loc); !prev.Equal(want) {
		t.Errorf("most recent due = %v, want %v", prev, want)
	}
}

func TestMostRecentDueIsNeverInTheFuture(t *testing.T) {
	loc := berlin(t)
	now := time.Date(2026, 9, 7, 5, 0, 0, 0, loc) // Monday, before 06:30
	got, ok := weekdayAlarm().MostRecentDue(now, loc)
	if !ok {
		t.Fatal("expected a previous due instant")
	}
	if got.After(now) {
		t.Fatalf("most recent due %v is after now %v", got, now)
	}
	if want := time.Date(2026, 9, 4, 6, 30, 0, 0, loc); !got.Equal(want) { // the Friday
		t.Errorf("most recent due = %v, want %v", got, want)
	}
}

func TestMostRecentDueIncludesThisMinute(t *testing.T) {
	loc := berlin(t)
	now := time.Date(2026, 9, 7, 6, 30, 0, 0, loc)
	got, ok := weekdayAlarm().MostRecentDue(now, loc)
	if !ok || !got.Equal(now) {
		t.Fatalf("an alarm due exactly now has to count: got %v, ok %v", got, ok)
	}
}

// The wall time is the same; the zone decides what instant it means.
func TestZoneDecidesTheInstant(t *testing.T) {
	berlinLoc := berlin(t)
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("America/New_York: %v", err)
	}
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	a := daily(6, 30)
	inBerlin, _ := a.NextDue(now, berlinLoc)
	inNY, _ := a.NextDue(now, ny)
	if inBerlin.Equal(inNY) {
		t.Fatal("06:30 in Berlin and 06:30 in New York are not the same instant")
	}
	if !inBerlin.Before(inNY) {
		t.Errorf("Berlin morning (%v) has to come before the New York one (%v)", inBerlin, inNY)
	}
}

// Spring forward: on 2026-03-29 Europe/Berlin jumps 02:00 -> 03:00, so a local
// 02:30 does not exist. The alarm still has to go off that morning rather than
// vanishing for the week.
func TestDSTSpringForwardStillFires(t *testing.T) {
	loc := berlin(t)
	now := time.Date(2026, 3, 29, 0, 0, 0, 0, loc)
	got, ok := daily(2, 30).NextDue(now, loc)
	if !ok {
		t.Fatal("a 02:30 alarm must not disappear on the spring-forward day")
	}
	if got.Year() != 2026 || got.Month() != time.March || got.Day() != 29 {
		t.Fatalf("the alarm moved off its day: %v", got)
	}
	// 02:30 CET would have been 01:30 UTC, which is already CEST, so the real
	// instant reads 03:30 local.
	if want := time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v (%v UTC), want %v UTC", got, got.UTC(), want)
	}
}

// Fall back: on 2026-10-25 Europe/Berlin repeats 02:00-03:00, so a local 02:30
// happens twice. The scheduler has to resolve it to exactly one instant (the
// first); the fired state is what stops the second.
func TestDSTFallBackResolvesOnce(t *testing.T) {
	loc := berlin(t)
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, loc)
	got, ok := daily(2, 30).NextDue(now, loc)
	if !ok {
		t.Fatal("a 02:30 alarm has to resolve on the fall-back day")
	}
	// Which side of the transition time.Date lands on is explicitly not
	// guaranteed by Go, so assert what actually matters: one instant, on the
	// right day, reading 02:30 on the wall clock the user set it by.
	if got.Year() != 2026 || got.Month() != time.October || got.Day() != 25 {
		t.Fatalf("the alarm moved off its day: %v", got)
	}
	if got.Hour() != 2 || got.Minute() != 30 {
		t.Errorf("got %v, want a local 02:30", got)
	}
	// And asking again from just after it does not hand back the same wall
	// time an hour later.
	next, ok := daily(2, 30).NextDue(got.Add(time.Minute), loc)
	if !ok {
		t.Fatal("expected a following day")
	}
	if next.Day() == 25 {
		t.Errorf("the duplicated hour fired twice: %v then %v", got, next)
	}
}

func TestNextFirePicksTheEarliestEnabled(t *testing.T) {
	loc := berlin(t)
	d := Document{Zone: "Europe/Berlin", Alarms: []Alarm{
		{ID: "late", Enabled: true, Hour: 8, Minute: 0, Days: []int{1}, Slot: 1},
		{ID: "early", Enabled: true, Hour: 6, Minute: 30, Days: []int{1}, Slot: 2},
		{ID: "earliest-but-off", Enabled: false, Hour: 5, Minute: 0, Days: []int{1}, Slot: 3},
	}}
	now := time.Date(2026, 9, 7, 4, 0, 0, 0, loc) // Monday
	at, which, ok := d.NextFire(now)
	if !ok {
		t.Fatal("expected a next fire")
	}
	if which.ID != "early" {
		t.Errorf("picked %q, want the earliest enabled alarm", which.ID)
	}
	if want := time.Date(2026, 9, 7, 6, 30, 0, 0, loc); !at.Equal(want) {
		t.Errorf("next fire = %v, want %v", at, want)
	}
}

func TestNextFireIgnoresEverythingDisabled(t *testing.T) {
	d := Document{Alarms: []Alarm{{ID: "x", Enabled: false, Hour: 6, Days: []int{1}, Slot: 1}}}
	if _, _, ok := d.NextFire(time.Now()); ok {
		t.Error("a document with nothing enabled has no next fire")
	}
}

// A zone that no longer resolves schedules nothing rather than quietly falling
// back to UTC and going off at the wrong hour.
func TestNextFireRefusesAnUnknownZone(t *testing.T) {
	d := Document{Zone: "Mars/Olympus", Alarms: []Alarm{daily(6, 30)}}
	if _, _, ok := d.NextFire(time.Now()); ok {
		t.Error("an unresolvable zone must not schedule anything")
	}
}

func TestUntilNextFire(t *testing.T) {
	loc := berlin(t)
	d := Document{Zone: "Europe/Berlin", Alarms: []Alarm{daily(6, 30)}}
	now := time.Date(2026, 9, 7, 6, 0, 0, 0, loc)
	if got := d.UntilNextFire(now, time.Hour); got != 30*time.Minute {
		t.Errorf("until next fire = %v, want 30m", got)
	}
	empty := Document{}
	if got := empty.UntilNextFire(now, time.Hour); got != time.Hour {
		t.Errorf("an empty document has to return the fallback, got %v", got)
	}
}
