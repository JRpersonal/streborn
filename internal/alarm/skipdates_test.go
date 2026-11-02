package alarm

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Days without an alarm (discussion #1491): public holidays and days off the
// user enters on the phone page.

func TestValidateSortsAndDeduplicatesSkipDates(t *testing.T) {
	d := Document{Zone: "Europe/Berlin", Alarms: []Alarm{weekdayAlarm()},
		SkipDates: []string{"2026-12-25", " 2026-10-03 ", "2026-12-25", "2026-12-24"}}
	if err := d.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	want := []string{"2026-10-03", "2026-12-24", "2026-12-25"}
	if !reflect.DeepEqual(d.SkipDates, want) {
		t.Errorf("got %v, want %v", d.SkipDates, want)
	}
}

func TestValidateRejectsBadSkipDates(t *testing.T) {
	for _, bad := range []string{"24.12.2026", "2026-02-30", "2026-1-5", "tomorrow", ""} {
		d := Document{Zone: "Europe/Berlin", SkipDates: []string{bad}}
		err := d.Validate()
		if err == nil || !strings.Contains(err.Error(), "not a date") {
			t.Errorf("%q: got %v, want a 'not a date' rejection", bad, err)
		}
	}
}

func TestValidateCapsSkipDates(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := Document{Zone: "Europe/Berlin"}
	for i := 0; i < MaxSkipDates+1; i++ {
		d.SkipDates = append(d.SkipDates, start.AddDate(0, 0, i).Format(dateLayout))
	}
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), fmt.Sprint(MaxSkipDates)) {
		t.Errorf("one over the cap: got %v, want a rejection naming %d", err, MaxSkipDates)
	}
	d.SkipDates = d.SkipDates[:MaxSkipDates]
	if err := d.Validate(); err != nil {
		t.Errorf("exactly the cap has to validate: %v", err)
	}
}

// Past days drop off, today stays (an alarm later today is still skipped), and
// "today" is the calendar day in the document's zone, not in UTC: at 00:30 in
// Berlin it is already the next day there while UTC still reads the old one.
func TestDropPastSkipDates(t *testing.T) {
	loc := berlin(t)
	d := Document{Zone: "Europe/Berlin", SkipDates: []string{"2026-12-24", "2026-12-25", "2026-12-26"}}
	d.DropPastSkipDates(time.Date(2026, 12, 25, 0, 30, 0, 0, loc))
	if want := []string{"2026-12-25", "2026-12-26"}; !reflect.DeepEqual(d.SkipDates, want) {
		t.Errorf("got %v, want %v", d.SkipDates, want)
	}
	d.DropPastSkipDates(time.Date(2027, 1, 1, 8, 0, 0, 0, loc))
	if d.SkipDates != nil {
		t.Errorf("every day has passed, got %v", d.SkipDates)
	}
}

// A weekday alarm stays silent on a holiday Monday and rings on Tuesday.
func TestNextDueSkipsAHoliday(t *testing.T) {
	loc := berlin(t)
	// Sunday 2026-10-04, the evening before; Monday the 5th is on the list.
	now := time.Date(2026, 10, 4, 22, 0, 0, 0, loc)
	skip := SkipSet{"2026-10-05": true}
	got, ok := weekdayAlarm().NextDue(now, loc, skip)
	if !ok {
		t.Fatal("expected a next alarm")
	}
	if want := time.Date(2026, 10, 6, 6, 30, 0, 0, loc); !got.Equal(want) {
		t.Errorf("got %v, want %v (Tuesday)", got, want)
	}
}

// An alarm on one weekday whose next occurrences are all skipped is due weeks
// later: the scan must reach past its usual eight days.
func TestNextDueReachesPastSeveralSkippedWeeks(t *testing.T) {
	loc := berlin(t)
	monday := Alarm{ID: "m", Enabled: true, Hour: 7, Days: []int{1}, Slot: 1}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, loc) // a Sunday
	skip := SkipSet{}
	for i := 0; i < 5; i++ {
		skip[time.Date(2026, 11, 2+7*i, 0, 0, 0, 0, loc).Format(dateLayout)] = true
	}
	got, ok := monday.NextDue(now, loc, skip)
	if !ok {
		t.Fatal("five skipped Mondays must not make the alarm vanish")
	}
	if want := time.Date(2026, 12, 7, 7, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("got %v, want %v (the sixth Monday)", got, want)
	}
}

// On a skipped morning there is nothing due today: the most recent due instant
// is the last ring before it, far outside any grace window.
func TestMostRecentDueOnASkippedMorning(t *testing.T) {
	loc := berlin(t)
	now := time.Date(2026, 10, 5, 6, 31, 0, 0, loc) // holiday Monday, a minute late
	got, ok := weekdayAlarm().MostRecentDue(now, loc, SkipSet{"2026-10-05": true})
	if !ok {
		t.Fatal("expected the previous ring")
	}
	if want := time.Date(2026, 10, 2, 6, 30, 0, 0, loc); !got.Equal(want) {
		t.Errorf("got %v, want %v (the Friday before)", got, want)
	}
}

// A skip date on the fall-back day: the day is skipped as a calendar day, so
// neither of the two 02:30s rings, and the next ring is the 26th.
func TestSkipDateOnTheFallBackDay(t *testing.T) {
	loc := berlin(t)
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, loc)
	got, ok := daily(2, 30).NextDue(now, loc, SkipSet{"2026-10-25": true})
	if !ok {
		t.Fatal("expected a next alarm")
	}
	if got.Day() != 26 || got.Hour() != 2 || got.Minute() != 30 {
		t.Errorf("got %v, want 02:30 on the 26th", got)
	}
}

// A skip date on the spring-forward day skips that day's ring, which on that
// day lands at 03:30; the one on the next day is untouched.
func TestSkipDateOnTheSpringForwardDay(t *testing.T) {
	loc := berlin(t)
	now := time.Date(2026, 3, 28, 23, 0, 0, 0, loc)
	got, _ := daily(2, 30).NextDue(now, loc, SkipSet{"2026-03-29": true})
	if want := time.Date(2026, 3, 30, 2, 30, 0, 0, loc); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The next ring the editor shows comes from NextFire, so it has to honour the
// skip dates too.
func TestNextFireHonoursSkipDates(t *testing.T) {
	loc := berlin(t)
	d := Document{Zone: "Europe/Berlin", Alarms: []Alarm{weekdayAlarm()}, SkipDates: []string{"2026-10-05", "2026-10-06"}}
	at, _, ok := d.NextFire(time.Date(2026, 10, 4, 22, 0, 0, 0, loc))
	if !ok {
		t.Fatal("expected a next fire")
	}
	if want := time.Date(2026, 10, 7, 6, 30, 0, 0, loc); !at.Equal(want) {
		t.Errorf("got %v, want %v (Wednesday)", at, want)
	}
}

func TestSkipDatesSurviveAReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms.json")
	s, err := Load(path, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	d := Document{Zone: "Europe/Berlin", Alarms: []Alarm{weekdayAlarm()}, SkipDates: []string{"2026-12-24"}}
	if err := s.Set(d); err != nil {
		t.Fatalf("set: %v", err)
	}
	again, err := Load(path, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Get().SkipDates; !reflect.DeepEqual(got, []string{"2026-12-24"}) {
		t.Errorf("after a reload: %v", got)
	}
	// Get hands out a copy: changing it must not reach the store.
	cp := again.Get()
	cp.SkipDates[0] = "2099-01-01"
	if got := again.Get().SkipDates[0]; got != "2026-12-24" {
		t.Errorf("the store shares its skip dates with a caller: %s", got)
	}
}
